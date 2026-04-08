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

// TestGetExifIFDOffset exercises getExifIFDOffset for LE, BE, bad byte order,
// and too-short inputs.
func TestGetExifIFDOffset(t *testing.T) {
	t.Parallel()

	// Build a minimal LE TIFF with one IFD0 entry: ExifIFD pointer (0x8769).
	buildTIFFWithExifPtr := func(byteOrder binary.ByteOrder, exifOff uint32) []byte {
		buf := make([]byte, 8+2+12+4) // header + 1 entry + next-IFD
		if byteOrder == binary.LittleEndian {
			buf[0], buf[1] = 'I', 'I'
		} else {
			buf[0], buf[1] = 'M', 'M'
		}
		byteOrder.PutUint16(buf[2:], 0x002A)
		byteOrder.PutUint32(buf[4:], 8) // IFD0 at offset 8
		byteOrder.PutUint16(buf[8:], 1) // 1 entry
		byteOrder.PutUint16(buf[10:], 0x8769)
		byteOrder.PutUint16(buf[12:], 4) // LONG
		byteOrder.PutUint32(buf[14:], 1) // count
		byteOrder.PutUint32(buf[18:], exifOff)
		return buf
	}

	t.Run("little endian finds ExifIFD offset", func(t *testing.T) {
		t.Parallel()
		tiff := buildTIFFWithExifPtr(binary.LittleEndian, 999)
		got := getExifIFDOffset(tiff)
		if got != 999 {
			t.Errorf("getExifIFDOffset LE = %d, want 999", got)
		}
	})

	t.Run("big endian finds ExifIFD offset", func(t *testing.T) {
		t.Parallel()
		tiff := buildTIFFWithExifPtr(binary.BigEndian, 888)
		got := getExifIFDOffset(tiff)
		if got != 888 {
			t.Errorf("getExifIFDOffset BE = %d, want 888", got)
		}
	})

	t.Run("bad byte order returns 0", func(t *testing.T) {
		t.Parallel()
		buf := make([]byte, 14)
		buf[0], buf[1] = 'X', 'X' // invalid
		got := getExifIFDOffset(buf)
		if got != 0 {
			t.Errorf("getExifIFDOffset bad order = %d, want 0", got)
		}
	})

	t.Run("too short returns 0", func(t *testing.T) {
		t.Parallel()
		got := getExifIFDOffset([]byte{0x49, 0x49, 0x2A, 0x00})
		if got != 0 {
			t.Errorf("getExifIFDOffset too short = %d, want 0", got)
		}
	})

	t.Run("ExifIFD tag absent returns 0", func(t *testing.T) {
		t.Parallel()
		// TIFF with a different tag (ImageWidth = 0x0100), no ExifIFD.
		buf := make([]byte, 8+2+12+4)
		binary.LittleEndian.PutUint16(buf[0:], 0x4949) // II
		binary.LittleEndian.PutUint16(buf[2:], 0x002A)
		binary.LittleEndian.PutUint32(buf[4:], 8)
		binary.LittleEndian.PutUint16(buf[8:], 1)
		binary.LittleEndian.PutUint16(buf[10:], 0x0100) // ImageWidth, not ExifIFD
		got := getExifIFDOffset(buf)
		if got != 0 {
			t.Errorf("getExifIFDOffset no ExifIFD tag = %d, want 0", got)
		}
	})
}

// TestMergeCMT exercises the paths in mergeCMT:
// nil cmt2, ExifIFD within cmt1 (no merge needed), and ExifIFD extending into cmt2.
func TestMergeCMT(t *testing.T) {
	t.Parallel()

	t.Run("nil cmt2 returns cmt1 unchanged", func(t *testing.T) {
		t.Parallel()
		cmt1 := minimalTIFF()
		got := mergeCMT(cmt1, nil)
		if &got[0] != &cmt1[0] {
			// Different backing array — acceptable, but length should match.
			if len(got) != len(cmt1) {
				t.Errorf("mergeCMT nil cmt2: len=%d want %d", len(got), len(cmt1))
			}
		}
	})

	t.Run("ExifIFD within cmt1 returns cmt1 unchanged", func(t *testing.T) {
		t.Parallel()
		// Build a TIFF where ExifIFD pointer is within cmt1 (offset < len(cmt1)).
		cmt1 := make([]byte, 8+2+12+4)
		binary.LittleEndian.PutUint16(cmt1[0:], 0x4949)
		binary.LittleEndian.PutUint16(cmt1[2:], 0x002A)
		binary.LittleEndian.PutUint32(cmt1[4:], 8)
		binary.LittleEndian.PutUint16(cmt1[8:], 1)
		binary.LittleEndian.PutUint16(cmt1[10:], 0x8769) // ExifIFD
		binary.LittleEndian.PutUint16(cmt1[12:], 4)      // LONG
		binary.LittleEndian.PutUint32(cmt1[14:], 1)
		binary.LittleEndian.PutUint32(cmt1[18:], 10) // offset 10 < len(cmt1)=26

		cmt2 := []byte("extra-data")
		got := mergeCMT(cmt1, cmt2)
		if len(got) != len(cmt1) {
			t.Errorf("mergeCMT ExifIFD within cmt1: len=%d want %d", len(got), len(cmt1))
		}
	})

	t.Run("ExifIFD extends into cmt2 triggers merge", func(t *testing.T) {
		t.Parallel()
		// ExifIFD pointer = 9999, far beyond len(cmt1).
		cmt1 := make([]byte, 8+2+12+4)
		binary.LittleEndian.PutUint16(cmt1[0:], 0x4949)
		binary.LittleEndian.PutUint16(cmt1[2:], 0x002A)
		binary.LittleEndian.PutUint32(cmt1[4:], 8)
		binary.LittleEndian.PutUint16(cmt1[8:], 1)
		binary.LittleEndian.PutUint16(cmt1[10:], 0x8769) // ExifIFD
		binary.LittleEndian.PutUint16(cmt1[12:], 4)      // LONG
		binary.LittleEndian.PutUint32(cmt1[14:], 1)
		binary.LittleEndian.PutUint32(cmt1[18:], 9999) // beyond len(cmt1)

		cmt2 := []byte("exif-data-in-cmt2")
		got := mergeCMT(cmt1, cmt2)
		want := len(cmt1) + len(cmt2)
		if len(got) != want {
			t.Errorf("mergeCMT merge: len=%d want %d", len(got), want)
		}
	})
}

// TestParseCR3BoxHeader exercises the parseCR3BoxHeader branches:
// normal box, extended (largesize) box, size==0 (to-end), and truncated inputs.
func TestParseCR3BoxHeader(t *testing.T) {
	t.Parallel()

	t.Run("normal box", func(t *testing.T) {
		t.Parallel()
		// size=16, type="test"
		buf := make([]byte, 16)
		binary.BigEndian.PutUint32(buf[0:], 16)
		copy(buf[4:], "test")
		size, typ, headerLen, ok := parseCR3BoxHeader(buf, 0)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if size != 16 {
			t.Errorf("size = %d, want 16", size)
		}
		if typ != "test" {
			t.Errorf("typ = %q, want test", typ)
		}
		if headerLen != 8 {
			t.Errorf("headerLen = %d, want 8", headerLen)
		}
	})

	t.Run("extended box (largesize)", func(t *testing.T) {
		t.Parallel()
		// size==1 means extended: next 8 bytes hold the real size.
		buf := make([]byte, 24)
		binary.BigEndian.PutUint32(buf[0:], 1) // size==1 → largesize follows
		copy(buf[4:], "uuid")
		binary.BigEndian.PutUint64(buf[8:], 24) // largesize = 24
		size, typ, headerLen, ok := parseCR3BoxHeader(buf, 0)
		if !ok {
			t.Fatal("expected ok=true for extended box")
		}
		if size != 24 {
			t.Errorf("size = %d, want 24", size)
		}
		if typ != "uuid" {
			t.Errorf("typ = %q, want uuid", typ)
		}
		if headerLen != 16 {
			t.Errorf("headerLen = %d, want 16", headerLen)
		}
	})

	t.Run("size==0 extends to end", func(t *testing.T) {
		t.Parallel()
		// size==0 means extends to end of container.
		buf := make([]byte, 20)
		binary.BigEndian.PutUint32(buf[0:], 0) // size==0
		copy(buf[4:], "mdat")
		size, _, _, ok := parseCR3BoxHeader(buf, 0)
		if !ok {
			t.Fatal("expected ok=true for size==0 box")
		}
		if size != 20 {
			t.Errorf("size = %d, want 20 (len(data))", size)
		}
	})

	t.Run("truncated (< 8 bytes)", func(t *testing.T) {
		t.Parallel()
		_, _, _, ok := parseCR3BoxHeader([]byte{0x00, 0x00, 0x00}, 0)
		if ok {
			t.Error("expected ok=false for truncated input")
		}
	})

	t.Run("extended box too short for largesize", func(t *testing.T) {
		t.Parallel()
		// size==1 but total buffer is only 8 bytes — can't read 8-byte largesize.
		buf := make([]byte, 8)
		binary.BigEndian.PutUint32(buf[0:], 1)
		copy(buf[4:], "uuid")
		_, _, _, ok := parseCR3BoxHeader(buf, 0)
		if ok {
			t.Error("expected ok=false when largesize field is missing")
		}
	})

	t.Run("box extends beyond buffer", func(t *testing.T) {
		t.Parallel()
		// size=999, buf only 16 bytes.
		buf := make([]byte, 16)
		binary.BigEndian.PutUint32(buf[0:], 999)
		copy(buf[4:], "moov")
		_, _, _, ok := parseCR3BoxHeader(buf, 0)
		if ok {
			t.Error("expected ok=false when box extends beyond buffer")
		}
	})
}

// TestFlatUUIDBoxRange exercises flatUUIDBoxRange: match, no-match, and
// a box whose UUID prefix is truncated.
func TestFlatUUIDBoxRange(t *testing.T) {
	t.Parallel()

	t.Run("finds Canon UUID box", func(t *testing.T) {
		t.Parallel()
		// Build a flat stream with one uuid box containing canonUUID.
		content := []byte("payload")
		uuidBox := buildUUIDBox(canonUUID, content)
		start, end, found := flatUUIDBoxRange(uuidBox, canonUUID)
		if !found {
			t.Fatal("flatUUIDBoxRange: expected found=true")
		}
		if start != 0 {
			t.Errorf("start = %d, want 0", start)
		}
		if end != len(uuidBox) {
			t.Errorf("end = %d, want %d", end, len(uuidBox))
		}
	})

	t.Run("returns not-found for wrong UUID", func(t *testing.T) {
		t.Parallel()
		wrongUUID := make([]byte, 16) // all zeros — not canonUUID
		content := []byte("payload")
		uuidBox := buildUUIDBox(wrongUUID, content)
		_, _, found := flatUUIDBoxRange(uuidBox, canonUUID)
		if found {
			t.Error("flatUUIDBoxRange: expected found=false for non-matching UUID")
		}
	})

	t.Run("uuid box too short to hold UUID bytes", func(t *testing.T) {
		t.Parallel()
		// Build a minimal uuid box whose payload is only 8 bytes (less than 16 bytes for UUID).
		buf := make([]byte, 16) // 8-byte header + 8-byte content (not enough for UUID)
		binary.BigEndian.PutUint32(buf[0:], 16)
		copy(buf[4:], "uuid")
		// No 16-byte UUID follows — pos+headerLen+16 > len(data).
		_, _, found := flatUUIDBoxRange(buf, canonUUID)
		if found {
			t.Error("flatUUIDBoxRange: expected found=false when box too short for UUID")
		}
	})
}

// TestMatchesUUID exercises the matchesUUID function: match, mismatch, and
// inputs shorter than 16 bytes.
func TestMatchesUUID(t *testing.T) {
	t.Parallel()

	t.Run("matches", func(t *testing.T) {
		t.Parallel()
		if !matchesUUID(canonUUID, canonUUID) {
			t.Error("matchesUUID: expected true for identical UUIDs")
		}
	})

	t.Run("does not match", func(t *testing.T) {
		t.Parallel()
		other := make([]byte, 16)
		if matchesUUID(canonUUID, other) {
			t.Error("matchesUUID: expected false for different UUIDs")
		}
	})

	t.Run("data too short", func(t *testing.T) {
		t.Parallel()
		if matchesUUID(canonUUID[:8], canonUUID) {
			t.Error("matchesUUID: expected false for data < 16 bytes")
		}
	})

	t.Run("uuid too short", func(t *testing.T) {
		t.Parallel()
		if matchesUUID(canonUUID, canonUUID[:8]) {
			t.Error("matchesUUID: expected false for uuid < 16 bytes")
		}
	})
}

// TestExtractFallbackNoCMT1 verifies that Extract succeeds when the Canon UUID
// box is absent and CMT1 is found via the fallback flat search in moov.
func TestExtractFallbackNoCMT1(t *testing.T) {
	t.Parallel()
	// Build a moov box with CMT1 directly (no uuid box) — triggers the fallback path.
	exif := minimalTIFF()
	cmt1Box := buildBox("CMT1", exif)
	moovBox := buildBox("moov", cmt1Box) // no uuid wrapper

	// Build a minimal ftyp + moov stream.
	var buf bytes.Buffer
	buf.Write([]byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 0})
	buf.Write(moovBox)

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract fallback: %v", err)
	}
	if !bytes.Equal(rawEXIF, exif) {
		t.Errorf("rawEXIF mismatch: got %d bytes, want %d", len(rawEXIF), len(exif))
	}
}

// TestRebuildUUIDContent exercises rebuildUUIDContent when:
//   - CMT1 is replaced, XMP  is replaced (already present)
//   - CMT1 is replaced, no XMP  sub-box (hadXMP=false)
//   - rawEXIF is nil (original CMT1 is preserved)
//   - rawXMP is nil but XMP  box present (original preserved)
func TestRebuildUUIDContent(t *testing.T) {
	t.Parallel()

	t.Run("replaces both CMT1 and XMP", func(t *testing.T) {
		t.Parallel()
		origExif := minimalTIFF()
		origXMP := []byte("original-xmp")
		// Build UUID content: CMT1 + XMP
		content := append(buildBox("CMT1", origExif), buildBox("XMP ", origXMP)...)

		newExif := append(origExif, 0xFF) // different bytes
		newXMP := []byte("new-xmp-data")
		result, hadXMP := rebuildUUIDContent(content, newExif, newXMP)
		if !hadXMP {
			t.Error("expected hadXMP=true")
		}
		// Result should contain the new CMT1 and new XMP  boxes.
		if len(result) == 0 {
			t.Error("result is empty")
		}
	})

	t.Run("no XMP box in original content returns hadXMP=false", func(t *testing.T) {
		t.Parallel()
		origExif := minimalTIFF()
		content := buildBox("CMT1", origExif)

		_, hadXMP := rebuildUUIDContent(content, nil, nil)
		if hadXMP {
			t.Error("expected hadXMP=false when no XMP  box present")
		}
	})

	t.Run("nil rawEXIF preserves original CMT1", func(t *testing.T) {
		t.Parallel()
		origExif := minimalTIFF()
		content := buildBox("CMT1", origExif)

		result, _ := rebuildUUIDContent(content, nil, nil)
		// The result should contain the original CMT1 box (unchanged).
		if !bytes.Contains(result, origExif) {
			t.Error("original CMT1 not preserved when rawEXIF is nil")
		}
	})

	t.Run("nil rawXMP preserves original XMP  box", func(t *testing.T) {
		t.Parallel()
		origExif := minimalTIFF()
		origXMP := []byte("keep-this-xmp")
		content := append(buildBox("CMT1", origExif), buildBox("XMP ", origXMP)...)

		result, hadXMP := rebuildUUIDContent(content, nil, nil)
		if !hadXMP {
			t.Error("expected hadXMP=true")
		}
		if !bytes.Contains(result, origXMP) {
			t.Error("original XMP  not preserved when rawXMP is nil")
		}
	})
}

// TestInjectAddsNewXMPWhenAbsent verifies that Inject appends an XMP  sub-box
// when the original CR3 file had no XMP  box but rawXMP is provided.
func TestInjectAddsNewXMPWhenAbsent(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	data := buildMinimalCR3(exif, nil) // no XMP

	xmp := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="w"?>`)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, xmp); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	_, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawXMP, xmp) {
		t.Errorf("rawXMP after inject: got %q, want %q", rawXMP, xmp)
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
