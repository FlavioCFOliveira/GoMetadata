package cr3

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildMinimalCR3 assembles a minimal CR3 ISOBMFF stream:
//
//	ftyp ("crx ")
//	moov
//	  uuid (Canon UUID)
//	    CMT1 (TIFF bytes, required)
//	    XMP  (XMP bytes, optional)
func buildMinimalCR3(tiffData, xmpData []byte) []byte {
	// Build CMT1 box.
	cmt1 := buildBox("CMT1", tiffData)

	// Build uuid content: CMT1 + optional XMP .
	uuidContent := cmt1
	if xmpData != nil {
		uuidContent = append(uuidContent, buildBox("XMP ", xmpData)...)
	}

	// Build Canon UUID box.
	uuidBox := buildUUIDBox(canonUUID, uuidContent)

	// Build moov box.
	moovBox := buildBox("moov", uuidBox)

	// Build ftyp box (16 bytes: size + "ftyp" + brand + minor version).
	ftyp := make([]byte, 0, 16+len(moovBox))
	ftyp = append(ftyp, 0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 0)

	return append(ftyp, moovBox...)
}

// minimalTIFF builds a bare-minimum little-endian TIFF stream.
func minimalTIFF() []byte {
	buf := make([]byte, 14)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8)
	// IFD0: 0 entries, next IFD = 0
	return buf
}

func TestExtractEXIF(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	data := buildMinimalCR3(exif, nil)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil, want CMT1 content")
	}
	if !bytes.Equal(rawEXIF, exif) {
		t.Errorf("rawEXIF mismatch: got %d bytes, want %d bytes", len(rawEXIF), len(exif))
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil", rawXMP)
	}
}

func TestExtractXMP(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	xmp := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)
	data := buildMinimalCR3(exif, xmp)

	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Error("rawXMP is nil, want XMP content")
	}
	if !bytes.Equal(rawXMP, xmp) {
		t.Errorf("rawXMP mismatch: got %d bytes, want %d bytes", len(rawXMP), len(xmp))
	}
}

func TestExtractNoMoovReturnsError(t *testing.T) {
	t.Parallel()
	// A file with only an ftyp box — no moov.
	ftyp := make([]byte, 16)
	binary.BigEndian.PutUint32(ftyp, 16)
	copy(ftyp[4:], "ftyp")
	copy(ftyp[8:], "crx ")
	_, _, _, err := Extract(bytes.NewReader(ftyp))
	if err == nil {
		t.Error("Extract with no moov box: expected error, got nil")
	}
}

func TestExtractTruncatedNoPanic(t *testing.T) {
	t.Parallel()
	data := buildMinimalCR3(minimalTIFF(), nil)
	for i := 0; i < len(data); i += len(data) / 10 {
		_, _, _, _ = Extract(bytes.NewReader(data[:i]))
	}
}

func TestInjectEXIFRoundTrip(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	data := buildMinimalCR3(exif, nil)

	exif = append(exif, 0x00, 0x01, 0x02, 0x03) // extend to differ from original
	newExif := exif

	// Patch IFD offset to keep it valid for the extended slice.
	newData := make([]byte, len(newExif))
	copy(newData, newExif)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newExif, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawEXIF, newExif) {
		t.Errorf("EXIF after inject: got %d bytes, want %d bytes", len(rawEXIF), len(newExif))
	}
}

func TestInjectXMPRoundTrip(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	data := buildMinimalCR3(exif, nil)

	xmp := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, xmp); err != nil {
		t.Fatalf("Inject (XMP): %v", err)
	}

	_, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject (XMP): %v", err)
	}
	if !bytes.Equal(rawXMP, xmp) {
		t.Errorf("XMP after inject: got %d bytes, want %d bytes", len(rawXMP), len(xmp))
	}
}

func TestInjectPassThroughWhenNoMoov(t *testing.T) {
	t.Parallel()
	// Without moov, Inject passes through unchanged.
	ftyp := make([]byte, 16)
	binary.BigEndian.PutUint32(ftyp, 16)
	copy(ftyp[4:], "ftyp")
	copy(ftyp[8:], "crx ")
	original := make([]byte, len(ftyp))
	copy(original, ftyp)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(ftyp), &out, nil, nil, nil); err != nil {
		t.Fatalf("Inject pass-through: %v", err)
	}
	if !bytes.Equal(out.Bytes(), original) {
		t.Error("pass-through: output differs from input")
	}
}

func TestInjectUUIDBoxSizeUpdated(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	data := buildMinimalCR3(exif, nil)

	// After injecting a larger EXIF, the moov box must be at least as large
	// as the new CMT1 content — verify the output is parseable and returns the new data.
	larger := make([]byte, len(exif)+100)
	copy(larger, exif)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, larger, nil, nil); err != nil {
		t.Fatalf("Inject larger EXIF: %v", err)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after inject larger EXIF: %v", err)
	}
	if !bytes.Equal(rawEXIF, larger) {
		t.Errorf("EXIF after inject larger: got %d bytes, want %d bytes", len(rawEXIF), len(larger))
	}
}
