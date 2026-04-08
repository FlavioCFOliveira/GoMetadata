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

// ---------------------------------------------------------------------------
// Additional tests for uncovered branches
// ---------------------------------------------------------------------------

// TestParseCR3BoxHeaderExtendedSize exercises the extended-size (size==1) path.
// ISOBMFF (ISO 14496-12) §4.2.
func TestParseCR3BoxHeaderExtendedSize(t *testing.T) {
	t.Parallel()
	// 4-byte size=1 + 4-byte type + 8-byte largesize + 4-byte body = 20 bytes total.
	const bodyLen = 4
	buf := make([]byte, 16+bodyLen)
	binary.BigEndian.PutUint32(buf[0:], 1) // size==1 → extended
	copy(buf[4:8], "test")
	binary.BigEndian.PutUint64(buf[8:], uint64(16+bodyLen))

	size, typ, headerLen, ok := parseCR3BoxHeader(buf, 0)
	if !ok {
		t.Fatal("parseCR3BoxHeader extended size: ok=false")
	}
	if typ != "test" {
		t.Errorf("typ = %q, want %q", typ, "test")
	}
	if headerLen != 16 {
		t.Errorf("headerLen = %d, want 16", headerLen)
	}
	if size != uint64(16+bodyLen) {
		t.Errorf("size = %d, want %d", size, 16+bodyLen)
	}
}

// TestParseCR3BoxHeaderZeroSize exercises the size==0 path (extends to end).
func TestParseCR3BoxHeaderZeroSize(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.BigEndian.PutUint32(buf[0:], 0) // size==0 → extends to EOF
	copy(buf[4:8], "free")

	size, _, _, ok := parseCR3BoxHeader(buf, 0)
	if !ok {
		t.Fatal("parseCR3BoxHeader size==0: ok=false")
	}
	if size != uint64(len(buf)) {
		t.Errorf("size = %d, want %d", size, len(buf))
	}
}

// TestParseCR3BoxHeaderTooShort verifies that a too-short buffer returns ok=false.
func TestParseCR3BoxHeaderTooShort(t *testing.T) {
	t.Parallel()
	_, _, _, ok := parseCR3BoxHeader([]byte{0, 0, 0, 8, 'f'}, 0)
	if ok {
		t.Error("expected ok=false for buffer shorter than 8 bytes")
	}
}

// TestParseCR3BoxHeaderExtendedSizeTooShort verifies extended-size box that
// doesn't fit in the buffer returns ok=false.
func TestParseCR3BoxHeaderExtendedSizeTooShort(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12) // only 12 bytes; extended header needs 16
	binary.BigEndian.PutUint32(buf[0:], 1)
	copy(buf[4:8], "test")
	_, _, _, ok := parseCR3BoxHeader(buf, 0)
	if ok {
		t.Error("expected ok=false for extended-size box shorter than 16 bytes")
	}
}

// TestGetExifIFDOffsetBigEndian exercises the big-endian branch in getExifIFDOffset.
func TestGetExifIFDOffsetBigEndian(t *testing.T) {
	t.Parallel()
	// Build a minimal big-endian TIFF with IFD0 at offset 8.
	// IFD0 has one entry: ExifIFD pointer (tag 0x8769).
	buf := make([]byte, 8+2+12+4)
	buf[0], buf[1] = 'M', 'M'
	binary.BigEndian.PutUint16(buf[2:], 0x002A)
	binary.BigEndian.PutUint32(buf[4:], 8) // IFD0 at offset 8
	binary.BigEndian.PutUint16(buf[8:], 1) // 1 entry
	// Entry: tag=0x8769, type=LONG(4), count=1, value=99
	binary.BigEndian.PutUint16(buf[10:], 0x8769)
	binary.BigEndian.PutUint16(buf[12:], 4) // LONG
	binary.BigEndian.PutUint32(buf[14:], 1)
	binary.BigEndian.PutUint32(buf[18:], 99) // ExifIFD at offset 99

	off := getExifIFDOffset(buf)
	if off != 99 {
		t.Errorf("getExifIFDOffset BE: got %d, want 99", off)
	}
}

// TestGetExifIFDOffsetNoExifTag verifies that getExifIFDOffset returns 0
// when the ExifIFD tag is absent from IFD0.
func TestGetExifIFDOffsetNoExifTag(t *testing.T) {
	t.Parallel()
	// IFD0 with one entry: ImageWidth (0x0100), no ExifIFD pointer.
	buf := make([]byte, 8+2+12+4)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[8:], 1)
	binary.LittleEndian.PutUint16(buf[10:], 0x0100) // ImageWidth
	binary.LittleEndian.PutUint16(buf[12:], 4)
	binary.LittleEndian.PutUint32(buf[14:], 1)
	binary.LittleEndian.PutUint32(buf[18:], 640)

	off := getExifIFDOffset(buf)
	if off != 0 {
		t.Errorf("getExifIFDOffset no ExifIFD tag: got %d, want 0", off)
	}
}

// TestGetExifIFDOffsetTooShort verifies that getExifIFDOffset returns 0
// for a too-short CMT1 buffer.
func TestGetExifIFDOffsetTooShort(t *testing.T) {
	t.Parallel()
	off := getExifIFDOffset([]byte("II*\x00"))
	if off != 0 {
		t.Errorf("getExifIFDOffset too short: got %d, want 0", off)
	}
}

// TestGetExifIFDOffsetBadByteOrder verifies that an unrecognised byte-order
// marker causes getExifIFDOffset to return 0.
func TestGetExifIFDOffsetBadByteOrder(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 20)
	buf[0], buf[1] = 'X', 'X' // bad byte order marker
	off := getExifIFDOffset(buf)
	if off != 0 {
		t.Errorf("getExifIFDOffset bad byte order: got %d, want 0", off)
	}
}

// TestMergeCMTExifIFDWithinCMT1 verifies that mergeCMT returns cmt1 unchanged
// when the ExifIFD offset lies within cmt1 (no merge needed).
func TestMergeCMTExifIFDWithinCMT1(t *testing.T) {
	t.Parallel()
	cmt1 := minimalTIFF() // has no ExifIFD tag → offset=0 → within cmt1
	cmt2 := []byte("extra data")
	result := mergeCMT(cmt1, cmt2)
	if &result[0] != &cmt1[0] {
		t.Error("mergeCMT: expected cmt1 returned unchanged when ExifIFD is within cmt1")
	}
}

// TestMergeCMTExifIFDInCMT2 verifies that mergeCMT appends cmt2 to cmt1 when
// the ExifIFD offset points beyond cmt1.
func TestMergeCMTExifIFDInCMT2(t *testing.T) {
	t.Parallel()
	// Build a LE TIFF where ExifIFD pointer (tag 0x8769) has value=9999
	// which is well beyond the size of cmt1.
	n := 1
	buf := make([]byte, 8+2+n*12+4)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8)       // IFD0 at 8
	binary.LittleEndian.PutUint16(buf[8:], 1)       // 1 entry
	binary.LittleEndian.PutUint16(buf[10:], 0x8769) // ExifIFD
	binary.LittleEndian.PutUint16(buf[12:], 4)      // LONG
	binary.LittleEndian.PutUint32(buf[14:], 1)
	binary.LittleEndian.PutUint32(buf[18:], 9999) // offset way beyond cmt1

	cmt2 := []byte("extra exif data")
	result := mergeCMT(buf, cmt2)
	if len(result) != len(buf)+len(cmt2) {
		t.Errorf("mergeCMT: result len=%d, want %d", len(result), len(buf)+len(cmt2))
	}
}

// TestMergeCMTNilCMT2 verifies that mergeCMT returns cmt1 when cmt2 is nil.
func TestMergeCMTNilCMT2(t *testing.T) {
	t.Parallel()
	cmt1 := minimalTIFF()
	result := mergeCMT(cmt1, nil)
	if !bytes.Equal(result, cmt1) {
		t.Error("mergeCMT nil cmt2: expected cmt1 unchanged")
	}
}

// TestFindBoxDepthLimit verifies that findBox returns nil when depth > 32.
func TestFindBoxDepthLimit(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 16)
	binary.BigEndian.PutUint32(buf, 16)
	copy(buf[4:], "moov")
	binary.BigEndian.PutUint32(buf[8:], 8)
	copy(buf[12:], "CMT1")
	result := findBox(buf, "CMT1", 33) // depth > 32 → immediate nil
	if result != nil {
		t.Error("findBox depth>32: expected nil")
	}
}

// TestFindBoxRecurseIntoMoov verifies that findBox recurses into moov to find
// a nested box.
func TestFindBoxRecurseIntoMoov(t *testing.T) {
	t.Parallel()
	// Build: moov [ CMT1 ]
	cmt1Body := []byte("tiff data")
	cmt1Box := buildBox("CMT1", cmt1Body)
	moovBox := buildBox("moov", cmt1Box)

	result := findBox(moovBox, "CMT1", 0)
	if result == nil {
		t.Fatal("findBox: CMT1 not found inside moov")
	}
	if !bytes.Equal(result, cmt1Body) {
		t.Errorf("findBox: got %q, want %q", result, cmt1Body)
	}
}

// TestMatchesUUIDMismatch verifies that matchesUUID returns false for a
// UUID that does not match.
func TestMatchesUUIDMismatch(t *testing.T) {
	t.Parallel()
	wrong := make([]byte, 16)
	if matchesUUID(wrong, canonUUID) {
		t.Error("matchesUUID: expected false for non-matching UUID")
	}
}

// TestMatchesUUIDMatch verifies that matchesUUID returns true for an exact match.
func TestMatchesUUIDMatch(t *testing.T) {
	t.Parallel()
	data := make([]byte, 32)
	copy(data, canonUUID)
	if !matchesUUID(data, canonUUID) {
		t.Error("matchesUUID: expected true for matching UUID")
	}
}

// TestMatchesUUIDTooShort verifies that matchesUUID returns false when data
// is shorter than 16 bytes.
func TestMatchesUUIDTooShort(t *testing.T) {
	t.Parallel()
	if matchesUUID([]byte{0x85, 0xC0}, canonUUID) {
		t.Error("matchesUUID too short: expected false")
	}
}

// TestExtractFallbackToDirectCMT1 verifies Extract when there is no Canon UUID
// box: it should fall back to finding CMT1/CMT2 directly within moov.
func TestExtractFallbackToDirectCMT1(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	// Build a CR3-like file: moov [ CMT1 ] with no UUID box.
	cmt1Box := buildBox("CMT1", exif)
	moovBox := buildBox("moov", cmt1Box)
	ftyp := make([]byte, 0, 16+len(moovBox))
	ftyp = append(ftyp, 0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 0)
	data := append(ftyp, moovBox...)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract fallback: %v", err)
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil (no XMP box)", rawXMP)
	}
	if !bytes.Equal(rawEXIF, exif) {
		t.Errorf("rawEXIF mismatch: got %d bytes, want %d bytes", len(rawEXIF), len(exif))
	}
}

// TestRebuildUUIDContentPreservesOtherBoxes verifies that rebuildUUIDContent
// preserves sub-boxes other than CMT1 and XMP .
func TestRebuildUUIDContentPreservesOtherBoxes(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	cmt3Body := []byte("CMT3 data")

	cmt1Box := buildBox("CMT1", exif)
	cmt3Box := buildBox("CMT3", cmt3Body)
	uuidContent := append(cmt1Box, cmt3Box...)

	newExif := append(exif, 0xAA, 0xBB)
	newContent, hadXMP := rebuildUUIDContent(uuidContent, newExif, nil)
	if hadXMP {
		t.Error("rebuildUUIDContent: hadXMP should be false (no XMP box present)")
	}
	// CMT3 should be preserved in the output.
	if !bytes.Contains(newContent, cmt3Body) {
		t.Error("rebuildUUIDContent: CMT3 sub-box not preserved")
	}
}

// TestInjectBothEXIFAndXMP verifies that injecting both EXIF and XMP into a
// CR3 with only EXIF produces a file containing both.
func TestInjectBothEXIFAndXMP(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	data := buildMinimalCR3(exif, nil)

	newExif := append(exif[:len(exif):len(exif)], 0x01)
	newXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newExif, nil, newXMP); err != nil {
		t.Fatalf("Inject EXIF+XMP: %v", err)
	}

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject EXIF+XMP: %v", err)
	}
	if !bytes.Equal(rawEXIF, newExif) {
		t.Errorf("EXIF mismatch after inject: got %d bytes, want %d bytes", len(rawEXIF), len(newExif))
	}
	if !bytes.Equal(rawXMP, newXMP) {
		t.Errorf("XMP mismatch after inject: got %d bytes, want %d bytes", len(rawXMP), len(newXMP))
	}
}

// TestInjectPassThroughWhenNoUUID verifies that Inject passes through unchanged
// when the moov box has no Canon UUID sub-box.
func TestInjectPassThroughWhenNoUUID(t *testing.T) {
	t.Parallel()
	// Build: ftyp + moov [ CMT1 ] — no uuid box.
	cmt1Box := buildBox("CMT1", minimalTIFF())
	moovBox := buildBox("moov", cmt1Box)
	ftyp := make([]byte, 0, 16+len(moovBox))
	ftyp = append(ftyp, 0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 0)
	data := append(ftyp, moovBox...)
	original := make([]byte, len(data))
	copy(original, data)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, []byte("new exif"), nil, nil); err != nil {
		t.Fatalf("Inject no UUID: %v", err)
	}
	if !bytes.Equal(out.Bytes(), original) {
		t.Error("Inject no UUID: output differs from input (expected pass-through)")
	}
}
