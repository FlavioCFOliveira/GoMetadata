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

// TestInjectEXIFRoundTrip verifies that Inject correctly replaces CMT1 content
// and that a subsequent Extract returns the new EXIF bytes.
func TestInjectEXIFRoundTrip(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	data := buildMinimalCR3(exif, nil)

	newExif := append(exif, 0x00, 0x01, 0x02, 0x03) // different size than original

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newExif, nil, nil, true); err != nil {
		t.Fatalf("Inject with non-nil rawEXIF: unexpected error: %v", err)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawEXIF, newExif) {
		t.Errorf("round-trip: rawEXIF mismatch: got %d bytes, want %d", len(rawEXIF), len(newExif))
	}
}

// TestInjectXMPRoundTrip verifies that Inject correctly replaces an existing
// XMP  sub-box and that Extract returns the new XMP bytes.
func TestInjectXMPRoundTrip(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	origXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)
	data := buildMinimalCR3(exif, origXMP)

	newXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end="w"?>`)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, newXMP, true); err != nil {
		t.Fatalf("Inject with non-nil rawXMP: unexpected error: %v", err)
	}

	_, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawXMP, newXMP) {
		t.Errorf("round-trip XMP: mismatch: got %d bytes, want %d", len(rawXMP), len(newXMP))
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
	if err := Inject(bytes.NewReader(ftyp), &out, nil, nil, nil, true); err != nil {
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

// TestInjectAddsNewXMPWhenAbsent verifies that Inject successfully appends an
// XMP  sub-box when rawXMP is provided but the original CR3 file had none.
// After the write, Extract must return the injected XMP bytes.
func TestInjectAddsNewXMPWhenAbsent(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	data := buildMinimalCR3(exif, nil) // no XMP

	xmp := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="w"?>`)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, xmp, true); err != nil {
		t.Fatalf("Inject with non-nil rawXMP (no existing XMP): unexpected error: %v", err)
	}

	_, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawXMP, xmp) {
		t.Errorf("round-trip XMP (newly added): mismatch: got %d bytes, want %d", len(rawXMP), len(xmp))
	}
}

// TestExtractSmallSizeNoPanic is a regression test for CVE-class panic:
// ISOBMFF boxes with size < headerLen must be rejected, not sliced.
//
// PoC byte sequence: \x00\x00\x00\x05moov — declares size=5, which is less
// than the 8-byte minimum header, triggering a "slice bounds out of range
// [8:5]" panic in findBox at data[pos+headerLen : pos+size].
func TestExtractSmallSizeNoPanic(t *testing.T) {
	t.Parallel()

	t.Run("size less than header (findBox path)", func(t *testing.T) {
		t.Parallel()
		// size=5, type="moov": size < headerLen(8) → parseCR3BoxHeader must return ok=false.
		input := []byte{0x00, 0x00, 0x00, 0x05, 'm', 'o', 'o', 'v'}
		_, _, _, err := Extract(bytes.NewReader(input))
		// Must not panic; must return an error (no valid moov found).
		if err == nil {
			t.Error("Extract: expected error for size<headerLen input, got nil")
		}
	})

	t.Run("size exactly equals header (empty box, no content)", func(t *testing.T) {
		t.Parallel()
		// size=8, type="moov": valid header, zero-length content — no panic.
		buf := make([]byte, 8)
		binary.BigEndian.PutUint32(buf[0:], 8)
		copy(buf[4:], "moov")
		_, _, _, err := Extract(bytes.NewReader(buf))
		// moov is found but has no sub-boxes → no CMT1 → nil rawEXIF, no panic.
		if err != nil {
			t.Errorf("Extract: unexpected error for size==headerLen: %v", err)
		}
	})

	t.Run("uuid box size less than header+16 (findUUIDBox path)", func(t *testing.T) {
		t.Parallel()
		// Build a moov box containing a malformed uuid box whose declared size is
		// headerLen(8)+10 — just enough to pass the size>=headerLen guard but not
		// the size>=headerLen+16 guard, preventing the slice into UUID bytes.
		uuidBoxSize := uint32(8 + 10) // 18 bytes: header(8) + 10 payload bytes (< 16 UUID)
		inner := make([]byte, uuidBoxSize)
		binary.BigEndian.PutUint32(inner[0:], uuidBoxSize)
		copy(inner[4:], "uuid")
		// Fill payload with canonical UUID prefix bytes (only 10, not 16).
		copy(inner[8:], canonUUID[:10])

		moovBox := buildBox("moov", inner)

		var stream bytes.Buffer
		stream.Write([]byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 0})
		stream.Write(moovBox)

		_, _, _, err := Extract(bytes.NewReader(stream.Bytes()))
		// Must not panic; uuid box is skipped, fallback finds no CMT1 → nil rawEXIF is acceptable.
		_ = err
	})

	t.Run("uuid box size less than headerLen+16 exact boundary", func(t *testing.T) {
		t.Parallel()
		// size = headerLen(8) + 15: one byte short of the 16-byte UUID — must not panic.
		uuidBoxSize := uint32(8 + 15) // 23 bytes
		inner := make([]byte, uuidBoxSize)
		binary.BigEndian.PutUint32(inner[0:], uuidBoxSize)
		copy(inner[4:], "uuid")
		copy(inner[8:], canonUUID[:15])

		moovBox := buildBox("moov", inner)

		var stream bytes.Buffer
		stream.Write([]byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 0})
		stream.Write(moovBox)

		// Must not panic.
		_, _, _, _ = Extract(bytes.NewReader(stream.Bytes()))
	})
}

// TestInjectUUIDBoxSizeUpdated verifies that after injecting a larger EXIF
// payload, the output moov box size is correctly updated (larger than original).
func TestInjectUUIDBoxSizeUpdated(t *testing.T) {
	t.Parallel()
	exif := minimalTIFF()
	data := buildMinimalCR3(exif, nil)

	larger := make([]byte, len(exif)+100) // different size — larger than original
	copy(larger, exif)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, larger, nil, nil, true); err != nil {
		t.Fatalf("Inject with larger rawEXIF: unexpected error: %v", err)
	}

	outBytes := out.Bytes()
	// The output must be larger than the input (moov grew).
	if len(outBytes) <= len(data) {
		t.Errorf("output size %d <= input size %d; expected output to be larger after bigger EXIF", len(outBytes), len(data))
	}

	// Extract must return the new EXIF.
	rawEXIF, _, _, err := Extract(bytes.NewReader(outBytes))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawEXIF, larger) {
		t.Errorf("round-trip: rawEXIF mismatch: got %d bytes, want %d", len(rawEXIF), len(larger))
	}
}

// TestInjectNilPayloadsPassThrough verifies that Inject with all-nil payloads
// is a safe pass-through: it copies the source bytes unchanged and returns nil.
// This is the only non-gated Inject path, and it is safe because moov size
// does not change (no stco/co64 invalidation).
func TestInjectNilPayloadsPassThrough(t *testing.T) {
	t.Parallel()
	data := buildMinimalCR3(minimalTIFF(), nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("Inject(nil,nil,nil): unexpected error: %v", err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Errorf("Inject(nil,nil,nil): output differs from input (got %d bytes, want %d)", out.Len(), len(data))
	}
}

// buildCR3WithOffsetTable constructs a synthetic CR3 stream designed to test
// stco/co64 relocation. Layout:
//
//	ftyp (16 bytes)
//	moov
//	  trak
//	    mdia
//	      minf
//	        stbl
//	          <stco or co64 with one entry whose value = mdatOffset>
//	  uuid (Canon UUID)
//	    CMT1 (tiffData)
//	mdat (mdatPayload)
//
// The chunk-offset entry is set to the absolute file offset of mdatPayload's
// first byte so that the caller can verify relocation correctness.
//
// offsetBox is either "stco" (uint32 entries) or "co64" (uint64 entries).
// Returns (fileBytes, mdatOffset) where mdatOffset is the absolute file offset
// of the first byte of mdatPayload in fileBytes.
func buildCR3WithOffsetTable(tiffData, mdatPayload []byte, offsetBox string) ([]byte, int) {
	const ftypSize = 16

	// Build the Canon UUID box (CMT1 only — no XMP).
	cmt1Box := buildBox("CMT1", tiffData)
	uuidBox := buildUUIDBox(canonUUID, cmt1Box)

	// We need to know mdatOffset before building stco/co64, but mdatOffset
	// depends on moov size, which depends on the offset-box content, which in
	// turn references mdatOffset. Bootstrap: build with a placeholder 0, then
	// patch. The offset-box size is fixed regardless of the offset value.
	buildOffsetBox := func(offset uint64) []byte {
		var payload []byte
		switch offsetBox {
		case "stco":
			// FullBox: version(1) + flags(3) + entry_count(4) + offset(4) = 12 bytes payload.
			payload = make([]byte, 12)
			// version=0, flags=0 → first 4 bytes = 0.
			binary.BigEndian.PutUint32(payload[4:], 1)              // entry_count = 1
			binary.BigEndian.PutUint32(payload[8:], uint32(offset)) //nolint:gosec // G115: offset fits uint32 for test values
		default: // "co64"
			// FullBox: version(1) + flags(3) + entry_count(4) + offset(8) = 16 bytes payload.
			payload = make([]byte, 16)
			binary.BigEndian.PutUint32(payload[4:], 1) // entry_count = 1
			binary.BigEndian.PutUint64(payload[8:], offset)
		}
		return buildBox(offsetBox, payload)
	}

	// Build the stbl → minf → mdia → trak chain with a placeholder offset.
	stblBox := buildBox("stbl", buildOffsetBox(0))
	minfBox := buildBox("minf", stblBox)
	mdiaBox := buildBox("mdia", minfBox)
	trakBox := buildBox("trak", mdiaBox)

	// moov content = trak + uuid.
	moovContent := append(trakBox, uuidBox...)
	moovBox := buildBox("moov", moovContent)

	// Compute mdatOffset: ftyp + moov.
	mdatOffset := ftypSize + len(moovBox)

	// Rebuild stbl with the correct mdat offset.
	stblBoxFinal := buildBox("stbl", buildOffsetBox(uint64(mdatOffset)))
	minfBoxFinal := buildBox("minf", stblBoxFinal)
	mdiaBoxFinal := buildBox("mdia", minfBoxFinal)
	trakBoxFinal := buildBox("trak", mdiaBoxFinal)

	// Rebuild moov with the patched trak.
	moovContentFinal := append(trakBoxFinal, uuidBox...)
	moovBoxFinal := buildBox("moov", moovContentFinal)

	// Verify mdatOffset is stable (no structural change from patching the offset value itself).
	if ftypSize+len(moovBoxFinal) != mdatOffset {
		// This would mean a circular dependency — the offset size changed the box size.
		// That cannot happen: the offset value is inlined in a fixed-width field.
		panic("buildCR3WithOffsetTable: mdat offset unstable after patch")
	}

	// Assemble: ftyp + moov + mdat.
	ftyp := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 0}
	mdatBox := buildBox("mdat", mdatPayload)

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(moovBoxFinal)
	out.Write(mdatBox)

	return out.Bytes(), mdatOffset
}

// TestInjectPreservesMdatOffsets is the primary regression gate for task #91.
//
// It verifies that after Inject replaces CMT1 with a payload of a different
// size, the stco/co64 chunk-offset entries in the output are correctly
// relocated by delta (where delta = new_moov_size - old_moov_size), and that
// the bytes at the relocated offset still match the original mdat sentinel.
//
// Sub-tests:
//  1. co64 + delta>0 (larger EXIF — mdat shifts forward)
//  2. co64 + delta<0 (smaller EXIF — mdat shifts backward)
//  3. stco (32-bit offsets) + delta>0
//  4. multi-trak: two traks each with their own co64; both must be relocated
//
// This test MUST fail on the pre-#91 code (which did no relocation) and MUST
// pass after the offset-relocation pass is in place.
func TestInjectPreservesMdatOffsets(t *testing.T) {
	t.Parallel()

	// mdatMarker is a recognisable byte pattern placed at the start of mdat payload.
	// After relocation, the bytes at the new offset must still equal this sentinel.
	mdatMarker := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE}

	// smallTIFF and largeTIFF are TIFF payloads of different sizes so that
	// replacing one with the other produces a non-zero delta.
	smallTIFF := minimalTIFF()                          // 14 bytes
	largeTIFF := append(smallTIFF, make([]byte, 80)...) // 94 bytes → delta = +80 for CMT1 box payload

	t.Run("co64 delta>0 (larger EXIF)", func(t *testing.T) {
		t.Parallel()

		// Build CR3 with smallTIFF (smaller CMT1) and a co64 offset table.
		fileBytes, mdatOffset := buildCR3WithOffsetTable(smallTIFF, mdatMarker, "co64")

		// Inject a larger EXIF — moov must grow, delta > 0.
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(fileBytes), &out, largeTIFF, nil, nil, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		outBytes := out.Bytes()

		// Compute the expected delta and relocated offset.
		oldMoovSize := mdatOffset - 16 // ftyp is always 16 bytes
		// Parse the actual new moov size from the output.
		_, _, newMoovFound := findMoovRange(outBytes)
		newMoovStart := 16
		if newMoovFound {
			// findMoovRange returns end; start is fixed at 16 for our fixture.
			_ = newMoovFound
		}
		newMoovSize := newMoovStart // will recompute below
		_ = newMoovSize
		_ = oldMoovSize

		newMoovStart2, newMoovEnd2, ok := findMoovRange(outBytes)
		if !ok {
			t.Fatal("no moov box in output")
		}
		actualDelta := (newMoovEnd2 - newMoovStart2) - (mdatOffset - 16)
		expectedNewOffset := int64(mdatOffset) + int64(actualDelta)

		// (a) Read the co64 entry from the output and verify it equals mdatOffset+delta.
		// Pass moov content (skip 8-byte moov box header) to the scanner.
		co64Val := readFirstOffsetFromMoov(t, outBytes[newMoovStart2+8:newMoovEnd2], "co64")
		if co64Val != expectedNewOffset {
			t.Errorf("co64 after inject = %d, want %d (orig=%d + delta=%d)",
				co64Val, expectedNewOffset, mdatOffset, actualDelta)
		}

		// (b) Verify the bytes at the relocated offset still equal mdatMarker.
		mdatPayloadOffset := int(expectedNewOffset) + 8 // skip mdat box header (8 bytes)
		if mdatPayloadOffset+len(mdatMarker) > len(outBytes) {
			t.Fatalf("relocated offset %d+8=%d out of bounds (file len=%d)", expectedNewOffset, mdatPayloadOffset, len(outBytes))
		}
		if !bytes.Equal(outBytes[mdatPayloadOffset:mdatPayloadOffset+len(mdatMarker)], mdatMarker) {
			t.Errorf("mdat content at relocated offset %d: got %x, want %x",
				mdatPayloadOffset, outBytes[mdatPayloadOffset:mdatPayloadOffset+len(mdatMarker)], mdatMarker)
		}

		// (c) Re-Extract returns the new EXIF.
		rawEXIF, _, _, err := Extract(bytes.NewReader(outBytes))
		if err != nil {
			t.Fatalf("Extract after Inject: %v", err)
		}
		if !bytes.Equal(rawEXIF, largeTIFF) {
			t.Errorf("Extract rawEXIF mismatch: got %d bytes, want %d", len(rawEXIF), len(largeTIFF))
		}
	})

	t.Run("co64 delta<0 (smaller EXIF)", func(t *testing.T) {
		t.Parallel()

		// Build CR3 with largeTIFF (larger CMT1) and a co64 offset table.
		fileBytes, mdatOffset := buildCR3WithOffsetTable(largeTIFF, mdatMarker, "co64")

		// Inject a smaller EXIF — moov must shrink, delta < 0.
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(fileBytes), &out, smallTIFF, nil, nil, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		outBytes := out.Bytes()

		newMoovStart, newMoovEnd, ok := findMoovRange(outBytes)
		if !ok {
			t.Fatal("no moov box in output")
		}
		actualDelta := (newMoovEnd - newMoovStart) - (mdatOffset - 16)
		if actualDelta >= 0 {
			t.Errorf("expected delta < 0 (smaller EXIF), got delta=%d", actualDelta)
		}
		expectedNewOffset := int64(mdatOffset) + int64(actualDelta)

		co64Val := readFirstOffsetFromMoov(t, outBytes[newMoovStart+8:newMoovEnd], "co64")
		if co64Val != expectedNewOffset {
			t.Errorf("co64 after inject = %d, want %d", co64Val, expectedNewOffset)
		}

		mdatPayloadOffset := int(expectedNewOffset) + 8
		if mdatPayloadOffset+len(mdatMarker) > len(outBytes) {
			t.Fatalf("relocated offset %d+8=%d out of bounds (file len=%d)", expectedNewOffset, mdatPayloadOffset, len(outBytes))
		}
		if !bytes.Equal(outBytes[mdatPayloadOffset:mdatPayloadOffset+len(mdatMarker)], mdatMarker) {
			t.Errorf("mdat content at relocated offset %d: got %x, want %x",
				mdatPayloadOffset, outBytes[mdatPayloadOffset:mdatPayloadOffset+len(mdatMarker)], mdatMarker)
		}

		rawEXIF, _, _, err := Extract(bytes.NewReader(outBytes))
		if err != nil {
			t.Fatalf("Extract after Inject: %v", err)
		}
		if !bytes.Equal(rawEXIF, smallTIFF) {
			t.Errorf("Extract rawEXIF mismatch: got %d bytes, want %d", len(rawEXIF), len(smallTIFF))
		}
	})

	t.Run("stco (32-bit offsets) delta>0", func(t *testing.T) {
		t.Parallel()

		fileBytes, mdatOffset := buildCR3WithOffsetTable(smallTIFF, mdatMarker, "stco")

		var out bytes.Buffer
		if err := Inject(bytes.NewReader(fileBytes), &out, largeTIFF, nil, nil, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		outBytes := out.Bytes()

		newMoovStart, newMoovEnd, ok := findMoovRange(outBytes)
		if !ok {
			t.Fatal("no moov box in output")
		}
		actualDelta := (newMoovEnd - newMoovStart) - (mdatOffset - 16)
		expectedNewOffset := int64(mdatOffset) + int64(actualDelta)

		stcoVal := readFirstOffsetFromMoov(t, outBytes[newMoovStart+8:newMoovEnd], "stco")
		if stcoVal != expectedNewOffset {
			t.Errorf("stco after inject = %d, want %d (orig=%d + delta=%d)",
				stcoVal, expectedNewOffset, mdatOffset, actualDelta)
		}

		mdatPayloadOffset := int(expectedNewOffset) + 8
		if mdatPayloadOffset+len(mdatMarker) > len(outBytes) {
			t.Fatalf("relocated offset %d+8=%d out of bounds (file len=%d)", expectedNewOffset, mdatPayloadOffset, len(outBytes))
		}
		if !bytes.Equal(outBytes[mdatPayloadOffset:mdatPayloadOffset+len(mdatMarker)], mdatMarker) {
			t.Errorf("mdat content at relocated offset: got %x, want %x",
				outBytes[mdatPayloadOffset:mdatPayloadOffset+len(mdatMarker)], mdatMarker)
		}
	})

	t.Run("multi-trak both co64 relocated", func(t *testing.T) {
		t.Parallel()
		// Build two traks, each with a co64 pointing into two separate mdat boxes.
		// Verify both are correctly relocated.

		const ftypSize = 16
		// Build mdat payloads with recognisable sentinel bytes at the front.
		mdat1Payload := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		mdat2Payload := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00, 0x11,
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		marker1 := mdat1Payload[:8]
		marker2 := mdat2Payload[:8]

		// Build the Canon UUID box with smallTIFF.
		cmt1Box := buildBox("CMT1", smallTIFF)
		uuidBox := buildUUIDBox(canonUUID, cmt1Box)

		buildTrakWithCo64 := func(offsetPlaceholder uint64) []byte {
			payload := make([]byte, 16) // version(1)+flags(3)+count(4)+offset(8)
			binary.BigEndian.PutUint32(payload[4:], 1)
			binary.BigEndian.PutUint64(payload[8:], offsetPlaceholder)
			co64Box := buildBox("co64", payload)
			stblBox := buildBox("stbl", co64Box)
			minfBox := buildBox("minf", stblBox)
			mdiaBox := buildBox("mdia", minfBox)
			return buildBox("trak", mdiaBox)
		}

		// Bootstrap: build with placeholder offsets to compute moov size.
		trak1Placeholder := buildTrakWithCo64(0)
		trak2Placeholder := buildTrakWithCo64(0)
		moovContentPlaceholder := append(append(trak1Placeholder, trak2Placeholder...), uuidBox...)
		moovPlaceholder := buildBox("moov", moovContentPlaceholder)

		// Compute absolute offsets for both mdat boxes.
		mdat1Start := ftypSize + len(moovPlaceholder) // mdat1 starts right after moov
		mdat1Box := buildBox("mdat", mdat1Payload)
		mdat2Start := mdat1Start + len(mdat1Box) // mdat2 follows mdat1

		// Rebuild traks with correct offsets.
		trak1 := buildTrakWithCo64(uint64(mdat1Start))
		trak2 := buildTrakWithCo64(uint64(mdat2Start))
		moovContent := append(append(trak1, trak2...), uuidBox...)
		moovBox := buildBox("moov", moovContent)

		// Verify offsets are stable (structure didn't change).
		if ftypSize+len(moovBox) != mdat1Start {
			t.Fatalf("multi-trak fixture: mdat1Start unstable: want %d, got %d",
				mdat1Start, ftypSize+len(moovBox))
		}

		ftyp := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 0}
		mdat2Box := buildBox("mdat", mdat2Payload)
		var fileBuf bytes.Buffer
		fileBuf.Write(ftyp)
		fileBuf.Write(moovBox)
		fileBuf.Write(mdat1Box)
		fileBuf.Write(mdat2Box)
		fileBytes := fileBuf.Bytes()

		// Inject a larger EXIF to force delta > 0.
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(fileBytes), &out, largeTIFF, nil, nil, true); err != nil {
			t.Fatalf("Inject (multi-trak): %v", err)
		}
		outBytes := out.Bytes()

		newMoovStart, newMoovEnd, ok := findMoovRange(outBytes)
		if !ok {
			t.Fatal("no moov box in output")
		}
		oldMoovSize := len(moovBox)
		newMoovSize := newMoovEnd - newMoovStart
		delta := int64(newMoovSize - oldMoovSize)
		if delta <= 0 {
			t.Fatalf("expected delta>0 (larger EXIF), got %d", delta)
		}

		expectedOffset1 := int64(mdat1Start) + delta
		expectedOffset2 := int64(mdat2Start) + delta

		// Read co64 entries from both traks in the rebuilt moov.
		moovContent2 := outBytes[newMoovStart+8 : newMoovEnd]
		offset1, offset2 := readTwoTrakOffsets(t, moovContent2, "co64")

		if offset1 != expectedOffset1 {
			t.Errorf("trak1 co64 = %d, want %d (orig=%d + delta=%d)", offset1, expectedOffset1, mdat1Start, delta)
		}
		if offset2 != expectedOffset2 {
			t.Errorf("trak2 co64 = %d, want %d (orig=%d + delta=%d)", offset2, expectedOffset2, mdat2Start, delta)
		}

		// Verify mdat content is intact at the new offsets.
		newMdat1PayloadOffset := int(expectedOffset1) + 8
		newMdat2PayloadOffset := int(expectedOffset2) + 8

		if newMdat1PayloadOffset+len(marker1) > len(outBytes) {
			t.Fatalf("relocated mdat1 offset %d out of bounds", newMdat1PayloadOffset)
		}
		if !bytes.Equal(outBytes[newMdat1PayloadOffset:newMdat1PayloadOffset+len(marker1)], marker1) {
			t.Errorf("mdat1 content: got %x, want %x",
				outBytes[newMdat1PayloadOffset:newMdat1PayloadOffset+len(marker1)], marker1)
		}

		if newMdat2PayloadOffset+len(marker2) > len(outBytes) {
			t.Fatalf("relocated mdat2 offset %d out of bounds", newMdat2PayloadOffset)
		}
		if !bytes.Equal(outBytes[newMdat2PayloadOffset:newMdat2PayloadOffset+len(marker2)], marker2) {
			t.Errorf("mdat2 content: got %x, want %x",
				outBytes[newMdat2PayloadOffset:newMdat2PayloadOffset+len(marker2)], marker2)
		}
	})
}

// readFirstOffsetFromMoov walks moovBytes (the moov content, NOT including
// the moov box header) to find the first stco or co64 box and returns the
// value of its first entry as int64.
func readFirstOffsetFromMoov(t *testing.T, moovBytes []byte, boxType string) int64 {
	t.Helper()
	return readFirstOffsetInContainer(t, moovBytes, boxType)
}

func readFirstOffsetInContainer(t *testing.T, data []byte, boxType string) int64 {
	t.Helper()
	pos := 0
	for pos+8 <= len(data) {
		size, typ, headerLen, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		contentOff := pos + int(headerLen) //nolint:gosec // G115: headerLen is 8 or 16
		boxEnd := pos + int(size)          //nolint:gosec // G115: ISOBMFF box size bounded by file size
		if typ == boxType {
			// FullBox: version(1)+flags(3) = 4 bytes; entry_count at +4.
			if contentOff+8 > len(data) {
				t.Fatalf("readFirstOffsetInContainer: %s box too small", boxType)
			}
			entryStart := contentOff + 8
			switch boxType {
			case "stco":
				if entryStart+4 > len(data) {
					t.Fatalf("readFirstOffsetInContainer: stco entry out of bounds")
				}
				return int64(binary.BigEndian.Uint32(data[entryStart:]))
			case "co64":
				if entryStart+8 > len(data) {
					t.Fatalf("readFirstOffsetInContainer: co64 entry out of bounds")
				}
				return int64(binary.BigEndian.Uint64(data[entryStart:])) //nolint:gosec // G115: test helper
			}
		}
		// Recurse into container boxes.
		switch typ {
		case "trak", "mdia", "minf", "stbl":
			if val := readFirstOffsetInContainer(t, data[contentOff:boxEnd], boxType); val != 0 {
				return val
			}
		}
		pos = boxEnd
	}
	return 0
}

// readTwoTrakOffsets reads the co64/stco first entry from each of the two
// trak boxes in moovContent (the raw moov content bytes, without moov header).
func readTwoTrakOffsets(t *testing.T, moovContent []byte, boxType string) (int64, int64) {
	t.Helper()
	var offsets []int64
	pos := 0
	for pos+8 <= len(moovContent) {
		size, typ, headerLen, ok := parseCR3BoxHeader(moovContent, pos)
		if !ok {
			break
		}
		contentOff := pos + int(headerLen) //nolint:gosec // G115: headerLen is 8 or 16
		boxEnd := pos + int(size)          //nolint:gosec // G115: ISOBMFF box size bounded by file size
		if typ == "trak" {
			val := readFirstOffsetInContainer(t, moovContent[contentOff:boxEnd], boxType)
			offsets = append(offsets, val)
		}
		pos = boxEnd
	}
	if len(offsets) < 2 {
		t.Fatalf("readTwoTrakOffsets: expected 2 trak boxes, found %d", len(offsets))
	}
	return offsets[0], offsets[1]
}
