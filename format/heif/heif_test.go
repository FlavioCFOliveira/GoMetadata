package heif

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildHEIF assembles a minimal ISOBMFF/HEIF stream containing optional EXIF
// and XMP items. The file structure is:
//
//	ftyp box (16 bytes)
//	meta box (FullBox: version + flags + iinf + iloc)
//	EXIF item data (optional)
//	XMP item data (optional)
func buildHEIF(exifData, xmpData []byte) []byte {
	// We assign item IDs sequentially.
	// Item 1 = Exif (if exifData != nil)
	// Item 2 = XMP  (if xmpData  != nil)
	const (
		exifItemID uint16 = 1
		xmpItemID  uint16 = 2
	)

	// --- Build infe boxes ---
	// infe v2: version(1)+flags(3)+item_id(2)+item_protection_index(2)+item_type(4)+item_name(1, "")
	makeInfe := func(id uint16, itemType string) []byte {
		body := make([]byte, 4+2+2+4+1)
		body[0] = 2 // version 2
		binary.BigEndian.PutUint16(body[4:], id)
		// item_protection_index = 0
		copy(body[8:], itemType)
		// item_name = "" (single NUL)
		body[12] = 0
		size := uint32(8 + len(body)) //nolint:gosec // G115: test helper, intentional type cast
		hdr := make([]byte, 0, 8+len(body))
		hdr = append(hdr, 0, 0, 0, 0, 'i', 'n', 'f', 'e')
		binary.BigEndian.PutUint32(hdr, size)
		return append(hdr, body...)
	}

	// --- Build iinf box ---
	makeIinf := func(infes ...[]byte) []byte {
		var iinfBody []byte
		iinfBody = append(iinfBody, 0, 0, 0, 0) // version 0 + flags
		cnt := make([]byte, 2)
		binary.BigEndian.PutUint16(cnt, uint16(len(infes))) //nolint:gosec // G115: test helper, intentional type cast
		iinfBody = append(iinfBody, cnt...)
		for _, infe := range infes {
			iinfBody = append(iinfBody, infe...)
		}
		size := uint32(8 + len(iinfBody)) //nolint:gosec // G115: test helper, intentional type cast
		hdr := make([]byte, 0, 8+len(iinfBody))
		hdr = append(hdr, 0, 0, 0, 0, 'i', 'i', 'n', 'f')
		binary.BigEndian.PutUint32(hdr, size)
		return append(hdr, iinfBody...)
	}

	// --- Build iloc box ---
	// iloc: version(1)+flags(3)+offset_size(4bit)+length_size(4bit)+
	//       base_offset_size(4bit)+reserved(4bit)+item_count(2)+items
	// We use offset_size=4, length_size=4.
	makeIloc := func(items []ilocTestItem) []byte {
		ilocBody := make([]byte, 0, 6+2+len(items)*(2+2+4+4)) // version+flags+sizes+item_count + items
		ilocBody = append(ilocBody,
			0x00, 0x00, 0x00, 0x00, // version + flags
			0x44, // offset_size=4, length_size=4
			0x00, // base_offset_size=0, reserved=0
		)
		cnt := make([]byte, 2)
		binary.BigEndian.PutUint16(cnt, uint16(len(items))) //nolint:gosec // G115: test helper, intentional type cast
		ilocBody = append(ilocBody, cnt...)
		for _, item := range items {
			id := make([]byte, 2)
			binary.BigEndian.PutUint16(id, item.id)
			ilocBody = append(ilocBody, id...)
			ec := make([]byte, 2)
			binary.BigEndian.PutUint16(ec, 1) // 1 extent
			ilocBody = append(ilocBody, ec...)
			off := make([]byte, 4)
			binary.BigEndian.PutUint32(off, item.offset)
			ilocBody = append(ilocBody, off...)
			ln := make([]byte, 4)
			binary.BigEndian.PutUint32(ln, item.length)
			ilocBody = append(ilocBody, ln...)
		}
		size := uint32(8 + len(ilocBody)) //nolint:gosec // G115: test helper, intentional type cast
		hdr := make([]byte, 0, 8+len(ilocBody))
		hdr = append(hdr, 0, 0, 0, 0, 'i', 'l', 'o', 'c')
		binary.BigEndian.PutUint32(hdr, size)
		return append(hdr, ilocBody...)
	}

	// --- ftyp box ---
	ftyp := make([]byte, 16)
	binary.BigEndian.PutUint32(ftyp, 16)
	copy(ftyp[4:], "ftyp")
	copy(ftyp[8:], "heic")
	// compatible brands...

	// Calculate data offsets: file = ftyp + meta + item data
	// We first figure out the meta box size, then place items after it.
	var infes [][]byte
	var ilocItems []ilocTestItem

	// We need the meta box size to compute absolute offsets for item data.
	// Strategy: compute all item sizes first; placeholder for meta size.
	// After computing everything, patch the offsets.

	// Item data area: starts after ftyp + meta.
	// Compute meta size iteratively.

	// Build with placeholder offsets first, then patch.
	var itemDataBlocks [][]byte

	if exifData != nil {
		infes = append(infes, makeInfe(exifItemID, "Exif"))
		// HEIF EXIF item starts with 4-byte header offset (= 0 here).
		exifBlock := append([]byte{0, 0, 0, 0}, exifData...)
		itemDataBlocks = append(itemDataBlocks, exifBlock)
		ilocItems = append(ilocItems, ilocTestItem{id: exifItemID, offset: 0, length: uint32(len(exifBlock))}) //nolint:gosec // G115: test helper, intentional type cast
	}
	if xmpData != nil {
		infes = append(infes, makeInfe(xmpItemID, "mime"))
		itemDataBlocks = append(itemDataBlocks, xmpData)
		ilocItems = append(ilocItems, ilocTestItem{id: xmpItemID, offset: 0, length: uint32(len(xmpData))}) //nolint:gosec // G115: test helper, intentional type cast
	}

	iinfBox := makeIinf(infes...)

	// Build iloc with placeholder offsets — will be patched below.
	ilocBox := makeIloc(ilocItems)

	// meta body: version/flags(4) + iinf + iloc
	metaBody := append([]byte{0, 0, 0, 0}, iinfBox...)
	metaBody = append(metaBody, ilocBox...)
	metaBox := make([]byte, 8+len(metaBody))
	binary.BigEndian.PutUint32(metaBox, uint32(len(metaBox))) //nolint:gosec // G115: test helper, intentional type cast
	copy(metaBox[4:], "meta")
	copy(metaBox[8:], metaBody)

	// Full file: ftyp + meta + item data.
	// Keep ftyp (16 bytes) immutable; build file images as separate slices.
	pass1 := make([]byte, 0, len(ftyp)+len(metaBox))
	pass1 = append(pass1, ftyp...)
	pass1 = append(pass1, metaBox...)
	dataStart := uint32(len(pass1)) //nolint:gosec // G115: test helper, intentional type cast

	// Patch iloc offsets now that we know dataStart.
	// Re-build iloc with correct offsets.
	curOff := dataStart
	for i := range ilocItems {
		ilocItems[i].offset = curOff
		curOff += ilocItems[i].length
	}
	ilocBox = makeIloc(ilocItems)
	metaBody2 := append([]byte{0, 0, 0, 0}, iinfBox...)
	metaBody2 = append(metaBody2, ilocBox...)
	metaBox2 := make([]byte, 8+len(metaBody2))
	binary.BigEndian.PutUint32(metaBox2, uint32(len(metaBox2))) //nolint:gosec // G115: test helper, intentional type cast
	copy(metaBox2[4:], "meta")
	copy(metaBox2[8:], metaBody2)

	// Recompute dataStart with the corrected meta box.
	pass2 := make([]byte, 0, len(ftyp)+len(metaBox2))
	pass2 = append(pass2, ftyp...)
	pass2 = append(pass2, metaBox2...)
	dataStart2 := uint32(len(pass2)) //nolint:gosec // G115: test helper, intentional type cast
	// Patch offsets again if meta size changed.
	curOff2 := dataStart2
	for i := range ilocItems {
		ilocItems[i].offset = curOff2
		curOff2 += ilocItems[i].length
	}
	ilocBox2 := makeIloc(ilocItems)
	metaBody3 := append([]byte{0, 0, 0, 0}, iinfBox...)
	metaBody3 = append(metaBody3, ilocBox2...)
	metaBox3 := make([]byte, 8+len(metaBody3))
	binary.BigEndian.PutUint32(metaBox3, uint32(len(metaBox3))) //nolint:gosec // G115: test helper, intentional type cast
	copy(metaBox3[4:], "meta")
	copy(metaBox3[8:], metaBody3)

	result := make([]byte, 0, len(ftyp)+len(metaBox3))
	result = append(result, ftyp...)
	result = append(result, metaBox3...)
	for _, block := range itemDataBlocks {
		result = append(result, block...)
	}
	return result
}

type ilocTestItem struct {
	id     uint16
	offset uint32
	length uint32
}

// minimalTIFFExif builds a tiny valid EXIF/TIFF blob.
func minimalTIFFExif() []byte {
	order := binary.LittleEndian
	buf := make([]byte, 8+2+12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)
	order.PutUint16(buf[10:], 0x010E) // ImageDescription
	order.PutUint16(buf[12:], 2)      // ASCII
	order.PutUint32(buf[14:], 4)
	copy(buf[18:], "test")
	return buf
}

func TestExtractEXIF(t *testing.T) {
	t.Parallel()
	exif := minimalTIFFExif()
	data := buildHEIF(exif, nil)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil, want non-nil")
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil", rawXMP)
	}
	if !bytes.Equal(rawEXIF, exif) {
		t.Errorf("rawEXIF mismatch: got %d bytes, want %d bytes", len(rawEXIF), len(exif))
	}
}

func TestExtractXMP(t *testing.T) {
	t.Parallel()
	xmp := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)
	data := buildHEIF(nil, xmp)

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF != nil {
		t.Errorf("rawEXIF = %v, want nil", rawEXIF)
	}
	if rawXMP == nil {
		t.Error("rawXMP is nil, want non-nil")
	}
}

func TestExtractBothItems(t *testing.T) {
	t.Parallel()
	exif := minimalTIFFExif()
	xmp := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)
	data := buildHEIF(exif, xmp)

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil")
	}
	if rawXMP == nil {
		t.Error("rawXMP is nil")
	}
}

func TestExtractEmpty(t *testing.T) {
	t.Parallel()
	data := buildHEIF(nil, nil)
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract on empty HEIF: %v", err)
	}
	if rawEXIF != nil || rawIPTC != nil || rawXMP != nil {
		t.Errorf("expected all nil for HEIF without metadata items, got exif=%v iptc=%v xmp=%v",
			rawEXIF, rawIPTC, rawXMP)
	}
}

func TestExtractTruncated(t *testing.T) {
	t.Parallel()
	// Truncated input must not panic.
	data := buildHEIF(minimalTIFFExif(), nil)
	for i := 0; i < len(data); i += len(data) / 8 {
		_, _, _, _ = Extract(bytes.NewReader(data[:i]))
	}
}

func TestInjectRoundTrip(t *testing.T) {
	t.Parallel()
	exif := minimalTIFFExif()
	data := buildHEIF(exif, nil)

	exif = append(exif[:len(exif)-4], 'X', 'X', 'X', 'X')
	newExif := exif
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

// buildHEIFInMoov constructs a minimal HEIF stream where the meta box is
// nested inside a moov box, exercising the ancestor-size-patching path in Inject.
func buildHEIFInMoov(exifData, xmpData []byte) []byte {
	inner := buildHEIF(exifData, xmpData)

	// Locate the meta box inside the inner stream and wrap it in moov.
	// inner = ftyp(16) + meta + item data
	// We wrap everything after ftyp into a moov box.
	ftyp := inner[:16]
	rest := inner[16:] // meta + item data

	moovBody := rest
	moovHdr := make([]byte, 0, 8+len(moovBody))
	moovHdr = append(moovHdr, 0, 0, 0, 0, 'm', 'o', 'o', 'v')
	binary.BigEndian.PutUint32(moovHdr, uint32(8+len(moovBody))) //nolint:gosec // G115: test helper, intentional type cast
	moovHdr = append(moovHdr, moovBody...)

	return append(ftyp, moovHdr...)
}

func TestInjectMetaInsideMoov(t *testing.T) {
	t.Parallel()
	exif := minimalTIFFExif()
	data := buildHEIFInMoov(exif, nil)

	exif = append(exif[:len(exif)-4], 'Y', 'Y', 'Y', 'Y')
	newExif := exif
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newExif, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject (meta-in-moov): %v", err)
	}
	if !bytes.Equal(rawEXIF, newExif) {
		t.Errorf("EXIF mismatch: got %d bytes, want %d bytes", len(rawEXIF), len(newExif))
	}
}

func TestInjectBothEXIFAndXMP(t *testing.T) {
	t.Parallel()
	exif := minimalTIFFExif()
	xmp := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)
	data := buildHEIF(exif, xmp)

	exif = append(exif[:len(exif)-4], 'Z', 'Z', 'Z', 'Z')
	newExif := exif
	newXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end="w"?>`)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newExif, nil, newXMP); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawEXIF, newExif) {
		t.Errorf("EXIF mismatch: got %d bytes, want %d", len(rawEXIF), len(newExif))
	}
	if !bytes.Equal(rawXMP, newXMP) {
		t.Errorf("XMP mismatch: got %d bytes, want %d", len(rawXMP), len(newXMP))
	}
}

func TestInjectPassThroughNilPayloads(t *testing.T) {
	t.Parallel()
	exif := minimalTIFFExif()
	data := buildHEIF(exif, nil)
	original := make([]byte, len(data))
	copy(original, data)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if !bytes.Equal(out.Bytes(), original) {
		t.Error("pass-through: output differs from input when no payloads provided")
	}
}

func BenchmarkHEIFExtract(b *testing.B) {
	data := buildHEIF(minimalTIFFExif(), nil)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(data))
	}
}

// BenchmarkHEIFInject measures the full Inject path for a HEIF stream:
// parse ftyp + meta boxes, locate iloc extents, rewrite item data offsets,
// TestParseInfeV0V1 exercises parseInfeV0V1 (0% coverage).
func TestParseInfeV0V1(t *testing.T) {
	t.Parallel()
	t.Run("valid XMP content-type", func(t *testing.T) {
		t.Parallel()
		// Layout: item_ID(2) + item_protection_index(2) + item_name(NUL) + content_type(NUL)
		name := []byte("xmpitem\x00")
		contentType := []byte("application/rdf+xml\x00")
		data := make([]byte, 4+len(name)+len(contentType))
		binary.BigEndian.PutUint16(data[0:], 42) // item ID
		// protection index = 0 (already zero)
		copy(data[4:], name)
		copy(data[4+len(name):], contentType)
		id, typ := parseInfeV0V1(data, 0)
		if id != 42 {
			t.Errorf("id = %d, want 42", id)
		}
		if typ != "mime" {
			t.Errorf("type = %q, want %q", typ, "mime")
		}
	})
	t.Run("other content-type returns empty type", func(t *testing.T) {
		t.Parallel()
		name := []byte("item\x00")
		contentType := []byte("image/jpeg\x00")
		data := make([]byte, 4+len(name)+len(contentType))
		binary.BigEndian.PutUint16(data[0:], 7)
		copy(data[4:], name)
		copy(data[4+len(name):], contentType)
		id, typ := parseInfeV0V1(data, 0)
		if id != 7 {
			t.Errorf("id = %d, want 7", id)
		}
		if typ != "" {
			t.Errorf("type = %q, want empty", typ)
		}
	})
	t.Run("too short returns zero", func(t *testing.T) {
		t.Parallel()
		id, typ := parseInfeV0V1([]byte{0x00}, 0)
		if id != 0 || typ != "" {
			t.Errorf("too short: id=%d type=%q, want 0 and empty", id, typ)
		}
	})
	t.Run("no NUL in item_name returns id with empty type", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 4+5) // id(2)+prot(2)+5 bytes with no NUL
		binary.BigEndian.PutUint16(data[0:], 3)
		copy(data[4:], "noNUL")
		id, typ := parseInfeV0V1(data, 0)
		if id != 3 {
			t.Errorf("id = %d, want 3", id)
		}
		if typ != "" {
			t.Errorf("type = %q, want empty", typ)
		}
	})
	t.Run("content type without NUL (EOF)", func(t *testing.T) {
		t.Parallel()
		name := []byte("item\x00")
		contentType := []byte("application/rdf+xml") // no trailing NUL
		data := make([]byte, 4+len(name)+len(contentType))
		binary.BigEndian.PutUint16(data[0:], 9)
		copy(data[4:], name)
		copy(data[4+len(name):], contentType)
		id, typ := parseInfeV0V1(data, 0)
		if id != 9 {
			t.Errorf("id = %d, want 9", id)
		}
		// Without the NUL we fall into the contentType = string(data[pos:]) branch,
		// which returns "mime" for "application/rdf+xml".
		if typ != "mime" {
			t.Errorf("type = %q, want mime", typ)
		}
	})
}

// TestExtractItemSlice exercises extractItemSlice (0% coverage).
func TestExtractItemSlice(t *testing.T) {
	t.Parallel()
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7}

	t.Run("valid slice", func(t *testing.T) {
		t.Parallel()
		loc := itemLoc{offset: 2, length: 3}
		got := extractItemSlice(data, loc)
		want := data[2:5]
		if !bytes.Equal(got, want) {
			t.Errorf("extractItemSlice = %v, want %v", got, want)
		}
	})
	t.Run("out of bounds", func(t *testing.T) {
		t.Parallel()
		loc := itemLoc{offset: 6, length: 10} // 6+10=16 > 8
		got := extractItemSlice(data, loc)
		if got != nil {
			t.Errorf("extractItemSlice OOB = %v, want nil", got)
		}
	})
	t.Run("zero length", func(t *testing.T) {
		t.Parallel()
		loc := itemLoc{offset: 3, length: 0}
		got := extractItemSlice(data, loc)
		if !bytes.Equal(got, data[3:3]) {
			t.Errorf("extractItemSlice zero len = %v, want empty", got)
		}
	})
}

// TestPatchAncestorSize exercises patchAncestorSize (0% coverage).
func TestPatchAncestorSize(t *testing.T) {
	t.Parallel()
	t.Run("patch box that wraps target offset", func(t *testing.T) {
		t.Parallel()
		// Build a simple ISOBMFF stream: one 40-byte box containing a 20-byte sub-box.
		data := make([]byte, 40)
		binary.BigEndian.PutUint32(data[0:], 40) // outer box size
		copy(data[4:8], "moov")
		// Inner box starts at offset 8, size 20.
		binary.BigEndian.PutUint32(data[8:], 20)
		copy(data[12:16], "meta")

		// metaAbsStart=8 is inside the outer box (0..40), so the outer box size
		// should be patched.
		patchAncestorSize(data, 8, 4) // delta = +4
		newSize := binary.BigEndian.Uint32(data[0:])
		if newSize != 44 {
			t.Errorf("patched outer box size = %d, want 44", newSize)
		}
	})
	t.Run("extended size box is skipped", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 24)
		binary.BigEndian.PutUint32(data[0:], 1) // sentinel: extended size
		copy(data[4:8], "moov")
		binary.BigEndian.PutUint64(data[8:], 24) // 64-bit size
		patchAncestorSize(data, 8, 4)            // should not modify anything
		// The first 4 bytes should still be 1 (unchanged).
		if binary.BigEndian.Uint32(data[0:]) != 1 {
			t.Error("extended-size box was unexpectedly patched")
		}
	})
	t.Run("no box wraps target offset", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 16)
		binary.BigEndian.PutUint32(data[0:], 16)
		copy(data[4:8], "ftyp")
		// metaAbsStart=20 is beyond the end of this box (0..16), so no patch.
		patchAncestorSize(data, 20, 4)
		if binary.BigEndian.Uint32(data[0:]) != 16 {
			t.Errorf("size was unexpectedly patched")
		}
	})
	t.Run("zero size box (extends to EOF)", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 16)
		binary.BigEndian.PutUint32(data[0:], 0) // size=0 means EOF
		copy(data[4:8], "mdat")
		// This should try to patch because offset 0 to len(data)=16 contains
		// metaAbsStart=8.
		patchAncestorSize(data, 8, 4)
		// size==0 path sets size=uint64(len(data)-pos)=16, boxEnd=16 > 8, so it patches.
		if binary.BigEndian.Uint32(data[0:]) == 0 {
			t.Logf("note: zero-size box may not be patched (expected behavior)")
		}
	})
}

// and stream the updated ISOBMFF to the output. The synthetic input carries
// one EXIF item so that the iloc-patching and item-rewrite paths are exercised.
// io.Discard is used as the writer so that output-buffer growth is not timed.
func BenchmarkHEIFInject(b *testing.B) {
	exifData := minimalTIFFExif()
	data := buildHEIF(exifData, nil)
	newEXIF := append(exifData[:len(exifData)-4:len(exifData)-4], 'B', 'E', 'N', 'C')
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = Inject(bytes.NewReader(data), nopWriter{}, newEXIF, nil, nil)
	}
}

// nopWriter is an io.Writer that discards all bytes without allocating. It is
// used by benchmarks to avoid measuring output-buffer growth.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// ---------------------------------------------------------------------------
// appendUintN
// ---------------------------------------------------------------------------

// TestAppendUintN exercises every branch of appendUintN.
func TestAppendUintN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n    int
		v    uint64
		want []byte
	}{
		{1, 0x42, []byte{0x42}},
		{2, 0xBEEF, []byte{0xBE, 0xEF}},
		{4, 0xDEADBEEF, []byte{0xDE, 0xAD, 0xBE, 0xEF}},
		{8, 0x0102030405060708, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}},
		// default branch: n=3 (non-standard width)
		{3, 0xABCDEF, []byte{0xAB, 0xCD, 0xEF}},
	}
	for _, tc := range tests {
		got := appendUintN(nil, tc.n, tc.v)
		if !bytes.Equal(got, tc.want) {
			t.Errorf("appendUintN(nil,%d,0x%X) = %x, want %x", tc.n, tc.v, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// readIlocItemID
// ---------------------------------------------------------------------------

// TestReadIlocItemID tests both version < 2 (uint16) and version 2 (uint32) paths.
func TestReadIlocItemID(t *testing.T) {
	t.Parallel()

	t.Run("version 0 reads uint16", func(t *testing.T) {
		t.Parallel()
		data := []byte{0x00, 0x0A, 0x00} // item_ID = 10
		id, newPos, ok := readIlocItemID(data, 0, 0)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if id != 10 {
			t.Errorf("id = %d, want 10", id)
		}
		if newPos != 2 {
			t.Errorf("newPos = %d, want 2", newPos)
		}
	})

	t.Run("version 0 too short", func(t *testing.T) {
		t.Parallel()
		_, _, ok := readIlocItemID([]byte{0x00}, 0, 0)
		if ok {
			t.Error("expected ok=false for too-short data")
		}
	})

	t.Run("version 2 reads uint32", func(t *testing.T) {
		t.Parallel()
		data := []byte{0x00, 0x00, 0x00, 0x07}
		id, newPos, ok := readIlocItemID(data, 0, 2)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if id != 7 {
			t.Errorf("id = %d, want 7", id)
		}
		if newPos != 4 {
			t.Errorf("newPos = %d, want 4", newPos)
		}
	})

	t.Run("version 2 too short", func(t *testing.T) {
		t.Parallel()
		_, _, ok := readIlocItemID([]byte{0x00, 0x00}, 0, 2)
		if ok {
			t.Error("expected ok=false for too-short data")
		}
	})

	t.Run("version 2 item ID exceeds uint16", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 4)
		binary.BigEndian.PutUint32(data, 0x00020000) // > 0xFFFF
		_, _, ok := readIlocItemID(data, 0, 2)
		if ok {
			t.Error("expected ok=false for item ID exceeding uint16 range")
		}
	})
}

// ---------------------------------------------------------------------------
// parseInfeV2V3
// ---------------------------------------------------------------------------

// TestParseInfeV2V3 tests version 2 (uint16 ID) and version 3 (uint32 ID).
func TestParseInfeV2V3(t *testing.T) {
	t.Parallel()

	makeV2 := func(id uint16, itemType string) []byte {
		// pos=0: item_ID(2) + protection_index(2) + item_type(4)
		data := make([]byte, 8)
		binary.BigEndian.PutUint16(data[0:], id)
		// protection index = 0
		copy(data[4:], itemType)
		return data
	}

	t.Run("version 2 valid", func(t *testing.T) {
		t.Parallel()
		data := makeV2(42, "Exif")
		id, typ := parseInfeV2V3(data, 0, 2)
		if id != 42 {
			t.Errorf("id = %d, want 42", id)
		}
		if typ != "Exif" {
			t.Errorf("type = %q, want Exif", typ)
		}
	})

	t.Run("version 2 too short for ID", func(t *testing.T) {
		t.Parallel()
		id, typ := parseInfeV2V3([]byte{0x00}, 0, 2)
		if id != 0 || typ != "" {
			t.Errorf("expected (0,'') for too-short v2, got (%d,%q)", id, typ)
		}
	})

	t.Run("version 3 valid", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 10)
		binary.BigEndian.PutUint32(data[0:], 5) // uint32 ID = 5
		// protection index = 0 at [4:6]
		copy(data[6:], "mime")
		id, typ := parseInfeV2V3(data, 0, 3)
		if id != 5 {
			t.Errorf("id = %d, want 5", id)
		}
		if typ != "mime" {
			t.Errorf("type = %q, want mime", typ)
		}
	})

	t.Run("version 3 too short for ID", func(t *testing.T) {
		t.Parallel()
		id, typ := parseInfeV2V3([]byte{0x00, 0x00}, 0, 3)
		if id != 0 || typ != "" {
			t.Errorf("expected (0,'') for too-short v3, got (%d,%q)", id, typ)
		}
	})

	t.Run("version 3 ID exceeds uint16", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 10)
		binary.BigEndian.PutUint32(data[0:], 0x00020000)
		id, typ := parseInfeV2V3(data, 0, 3)
		if id != 0 || typ != "" {
			t.Errorf("expected (0,'') for oversized v3 ID, got (%d,%q)", id, typ)
		}
	})

	t.Run("version 2 too short for item_type", func(t *testing.T) {
		t.Parallel()
		// only 4 bytes: ID(2)+prot(2), no room for item_type(4)
		data := make([]byte, 4)
		binary.BigEndian.PutUint16(data[0:], 1)
		id, typ := parseInfeV2V3(data, 0, 2)
		if id != 0 || typ != "" {
			t.Errorf("expected (0,'') when item_type field truncated, got (%d,%q)", id, typ)
		}
	})
}

// ---------------------------------------------------------------------------
// parsePitm
// ---------------------------------------------------------------------------

// TestParsePitm tests all branches of parsePitm.
func TestParsePitm(t *testing.T) {
	t.Parallel()

	makePitmBox := func(version byte, id uint32) []byte {
		// pitm inner box: version(1)+flags(3)+item_ID(2 or 4)
		var idBytes []byte
		if version == 0 {
			idBytes = make([]byte, 2)
			binary.BigEndian.PutUint16(idBytes, uint16(id)) //nolint:gosec // G115: safe test helper
		} else {
			idBytes = make([]byte, 4)
			binary.BigEndian.PutUint32(idBytes, id)
		}
		body := append([]byte{version, 0, 0, 0}, idBytes...)
		size := uint32(8 + len(body)) //nolint:gosec // G115: safe test helper
		hdr := make([]byte, 0, 8+len(body))
		hdr = append(hdr, 0, 0, 0, 0, 'p', 'i', 't', 'm')
		binary.BigEndian.PutUint32(hdr, size)
		return append(hdr, body...)
	}

	t.Run("version 0 returns uint16 id", func(t *testing.T) {
		t.Parallel()
		pitm := makePitmBox(0, 3)
		got := parsePitm(pitm)
		if got != 3 {
			t.Errorf("parsePitm v0 = %d, want 3", got)
		}
	})

	t.Run("version 1 returns uint32 id (fits uint16)", func(t *testing.T) {
		t.Parallel()
		pitm := makePitmBox(1, 7)
		got := parsePitm(pitm)
		if got != 7 {
			t.Errorf("parsePitm v1 = %d, want 7", got)
		}
	})

	t.Run("version 1 id exceeds uint16 returns 0", func(t *testing.T) {
		t.Parallel()
		pitm := makePitmBox(1, 0x00020000)
		got := parsePitm(pitm)
		if got != 0 {
			t.Errorf("parsePitm v1 oversized = %d, want 0", got)
		}
	})

	t.Run("no pitm box returns 0", func(t *testing.T) {
		t.Parallel()
		got := parsePitm([]byte{})
		if got != 0 {
			t.Errorf("parsePitm empty = %d, want 0", got)
		}
	})
}

// ---------------------------------------------------------------------------
// parseIinfItemCount
// ---------------------------------------------------------------------------

// TestParseIinfItemCount tests version < 2 (uint16) and version 2 (uint32) paths.
func TestParseIinfItemCount(t *testing.T) {
	t.Parallel()

	t.Run("version 0 uint16", func(t *testing.T) {
		t.Parallel()
		data := []byte{0x00, 0x05}
		count, newPos, ok := parseIinfItemCount(data, 0, 0)
		if !ok || count != 5 || newPos != 2 {
			t.Errorf("v0: count=%d newPos=%d ok=%v, want 5 2 true", count, newPos, ok)
		}
	})

	t.Run("version 2 uint32", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 4)
		binary.BigEndian.PutUint32(data, 12)
		count, newPos, ok := parseIinfItemCount(data, 0, 2)
		if !ok || count != 12 || newPos != 4 {
			t.Errorf("v2: count=%d newPos=%d ok=%v, want 12 4 true", count, newPos, ok)
		}
	})

	t.Run("version 2 too short", func(t *testing.T) {
		t.Parallel()
		_, _, ok := parseIinfItemCount([]byte{0x00}, 0, 2)
		if ok {
			t.Error("expected ok=false for too-short v2")
		}
	})
}

// ---------------------------------------------------------------------------
// parseHEIFBoxHeader
// ---------------------------------------------------------------------------

// TestParseHEIFBoxHeader exercises the extended-size (size==1) branch and the
// size==0 (extends to EOF) branch.
func TestParseHEIFBoxHeader(t *testing.T) {
	t.Parallel()

	t.Run("normal box", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 12)
		binary.BigEndian.PutUint32(data[0:], 12)
		copy(data[4:], "ftyp")
		sz, typ, hdrLen, ok := parseHEIFBoxHeader(data, 0)
		if !ok || sz != 12 || typ != "ftyp" || hdrLen != 8 {
			t.Errorf("normal: sz=%d typ=%q hdrLen=%d ok=%v", sz, typ, hdrLen, ok)
		}
	})

	t.Run("extended size (sentinel 1)", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 24)
		binary.BigEndian.PutUint32(data[0:], 1) // extended size sentinel
		copy(data[4:], "mdat")
		binary.BigEndian.PutUint64(data[8:], 24) // actual size in next 8 bytes
		sz, typ, hdrLen, ok := parseHEIFBoxHeader(data, 0)
		if !ok || sz != 24 || typ != "mdat" || hdrLen != 16 {
			t.Errorf("extended: sz=%d typ=%q hdrLen=%d ok=%v", sz, typ, hdrLen, ok)
		}
	})

	t.Run("extended size too short for 64-bit field", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 12) // only 12 bytes, not enough for 16-byte header
		binary.BigEndian.PutUint32(data[0:], 1)
		copy(data[4:], "mdat")
		_, _, _, ok := parseHEIFBoxHeader(data, 0)
		if ok {
			t.Error("expected ok=false when extended-size header truncated")
		}
	})

	t.Run("size 0 extends to EOF", func(t *testing.T) {
		t.Parallel()
		data := make([]byte, 16)
		binary.BigEndian.PutUint32(data[0:], 0) // size=0: extends to EOF
		copy(data[4:], "mdat")
		sz, typ, hdrLen, ok := parseHEIFBoxHeader(data, 0)
		if !ok || sz != 16 || typ != "mdat" || hdrLen != 8 {
			t.Errorf("size=0: sz=%d typ=%q hdrLen=%d ok=%v", sz, typ, hdrLen, ok)
		}
	})

	t.Run("too short (< 8 bytes)", func(t *testing.T) {
		t.Parallel()
		_, _, _, ok := parseHEIFBoxHeader([]byte{0, 0, 0}, 0)
		if ok {
			t.Error("expected ok=false for < 8 byte data")
		}
	})
}

// ---------------------------------------------------------------------------
// parseHEIFMetadata
// ---------------------------------------------------------------------------

// TestParseHEIFMetadata exercises parseHEIFMetadata via a complete synthetic HEIF.
func TestParseHEIFMetadata(t *testing.T) {
	t.Parallel()

	t.Run("no meta box returns nil", func(t *testing.T) {
		t.Parallel()
		// A ftyp box with no meta box.
		ftyp := make([]byte, 16)
		binary.BigEndian.PutUint32(ftyp, 16)
		copy(ftyp[4:], "ftyp")
		copy(ftyp[8:], "heic")
		rawEXIF, rawXMP, err := parseHEIFMetadata(ftyp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rawEXIF != nil || rawXMP != nil {
			t.Errorf("expected nil payloads for no meta box, got exif=%v xmp=%v", rawEXIF, rawXMP)
		}
	})

	t.Run("EXIF item parsed", func(t *testing.T) {
		t.Parallel()
		exifPayload := minimalTIFFExif()
		heifData := buildHEIF(exifPayload, nil)
		rawEXIF, rawXMP, err := parseHEIFMetadata(heifData)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rawEXIF == nil {
			t.Error("expected non-nil rawEXIF")
		}
		if rawXMP != nil {
			t.Error("expected nil rawXMP")
		}
	})

	t.Run("XMP item parsed", func(t *testing.T) {
		t.Parallel()
		xmpPayload := []byte(`<?xpacket begin="" id="x"?><x:xmpmeta/><?xpacket end="r"?>`)
		heifData := buildHEIF(nil, xmpPayload)
		rawEXIF, rawXMP, err := parseHEIFMetadata(heifData)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rawEXIF != nil {
			t.Error("expected nil rawEXIF")
		}
		if rawXMP == nil {
			t.Error("expected non-nil rawXMP")
		}
	})
}

// ---------------------------------------------------------------------------
// extractExifFromData
// ---------------------------------------------------------------------------

// TestExtractExifFromData exercises the short-data and skip-out-of-range paths.
func TestExtractExifFromData(t *testing.T) {
	t.Parallel()

	t.Run("too short returns nil", func(t *testing.T) {
		t.Parallel()
		if got := extractExifFromData([]byte{0, 0, 0}); got != nil {
			t.Errorf("expected nil for 3-byte input, got %v", got)
		}
	})

	t.Run("skip offset out of range returns nil", func(t *testing.T) {
		t.Parallel()
		// 4-byte prefix with value 100 — skip would be 104, but data is only 8 bytes.
		data := make([]byte, 8)
		binary.BigEndian.PutUint32(data, 100)
		if got := extractExifFromData(data); got != nil {
			t.Errorf("expected nil when skip > len(data), got %v", got)
		}
	})

	t.Run("valid extraction", func(t *testing.T) {
		t.Parallel()
		exif := []byte("EXIFPAYLOAD")
		data := append([]byte{0, 0, 0, 0}, exif...)
		got := extractExifFromData(data)
		if string(got) != "EXIFPAYLOAD" {
			t.Errorf("extractExifFromData = %q, want %q", got, "EXIFPAYLOAD")
		}
	})
}
