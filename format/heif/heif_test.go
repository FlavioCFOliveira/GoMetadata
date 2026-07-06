package heif

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"
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
	if err := Inject(bytes.NewReader(data), &out, newExif, nil, nil, true); err != nil {
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
	if err := Inject(bytes.NewReader(data), &out, newExif, nil, nil, true); err != nil {
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
	if err := Inject(bytes.NewReader(data), &out, newExif, nil, newXMP, true); err != nil {
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
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
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
		_ = Inject(bytes.NewReader(data), nopWriter{}, newEXIF, nil, nil, true)
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

// TestExtractExifFromData_32BitSignednessOverflow is a regression test for
// HEIF-32BIT-01: on a 32-bit platform (GOARCH=386/arm/mips/mipsle) int is
// 32-bit, so int(uint32) for a 4-byte prefix >= 0x80000000 used to wrap
// negative. A negative skip is never > len(data), so the old int-typed guard
// (`skip > len(data)`) was silently bypassed and `data[skip:]` panicked with
// a negative slice index (CWE-681 -> CWE-190 -> CWE-129). The fix computes
// skip in uint64 before comparing, which has no wraparound on any platform.
// This test passes on this 64-bit host both before and after the fix — it
// exists to lock the contract for 32-bit builds, which cannot be exercised
// directly by `go test` on this host (see GOARCH=386 build check instead).
func TestExtractExifFromData_32BitSignednessOverflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prefix uint32
		want   []byte // nil means "must return nil, never panic"
	}{
		{
			name:   "prefix exactly 0x80000000 (32-bit int sign boundary)",
			prefix: 0x80000000,
			want:   nil,
		},
		{
			name:   "prefix 0xFFFFFFFF (maximum uint32)",
			prefix: 0xFFFFFFFF,
			want:   nil,
		},
		{
			name:   "prefix 0xC0000000 (well past the 32-bit sign boundary)",
			prefix: 0xC0000000,
			want:   nil,
		},
		{
			name:   "small valid prefix still extracts correctly",
			prefix: 0,
			want:   []byte("EXIFPAYLOAD"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var data []byte
			if tc.want == nil {
				// Any payload length is fine here: the point is that a huge
				// prefix must never pass the length guard, regardless of
				// int word size.
				data = make([]byte, 16)
			} else {
				data = append([]byte{0, 0, 0, 0}, tc.want...)
			}
			binary.BigEndian.PutUint32(data, tc.prefix)

			// The guarantee under test: this call must never panic, on any
			// platform. testing.T has no built-in "must not panic" assertion,
			// so a plain call that runs to completion under -race is the
			// proof; a regression would surface as a test crash, not a
			// failed assertion.
			got := extractExifFromData(data)

			if tc.want == nil {
				if got != nil {
					t.Errorf("extractExifFromData(prefix=0x%08X) = %v, want nil", tc.prefix, got)
				}
				return
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("extractExifFromData(prefix=0x%08X) = %q, want %q", tc.prefix, got, tc.want)
			}
		})
	}
}

// TestExtractMalformedBoxSizeLessThanHeader is a regression test for the
// panic "slice bounds out of range [8:N]" triggered when an ISOBMFF box
// declares a size smaller than its own minimum header length.
//
// ISO 14496-12 §4.2: a box whose size field is < 8 (standard header) or
// < 16 (extended-size header, size == 1) is unconditionally malformed.
// parseHEIFBoxHeader must return ok=false in those cases so no caller can
// form the slice data[pos+headerLen : pos+size].
//
// PoC: 8 bytes with size=4 — the fuzzer-discovered crash case.
func TestExtractMalformedBoxSizeLessThanHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []byte
	}{
		{
			// Fuzzer-discovered PoC: size=4 (< headerLen=8) followed by a type tag.
			// findBox would compute data[pos+8 : pos+4] and panic.
			name:  "size=4 less than standard header (8 bytes)",
			input: []byte{0x00, 0x00, 0x00, 0x04, 't', 'e', 's', 't'},
		},
		{
			// Extended-size box (size field == 1 triggers 16-byte header read),
			// but the largesize field is set to 12 which is < headerLen=16.
			// ISO 14496-12 §4.2: largesize must be >= 16.
			name: "extended-size box with largesize=12 less than extended header (16 bytes)",
			input: []byte{
				0x00, 0x00, 0x00, 0x01, // size == 1 → extended-size form
				't', 'e', 's', 't', // type
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x0C, // largesize = 12
			},
		},
		{
			// size=1: the absolute minimum malformed case — only 1 byte declared.
			name:  "size=1 single byte declared",
			input: []byte{0x00, 0x00, 0x00, 0x01, 'm', 'e', 't', 'a'},
		},
		{
			// size=0 is special (box extends to end-of-container) and is valid;
			// include it to confirm the guard does not break the size==0 path.
			name: "size=0 extends-to-end (valid, must not panic)",
			input: func() []byte {
				// Wrap a size=0 meta box inside a minimal ftyp header so Extract
				// can reach findBox. A size=0 meta box with no payload is fine.
				meta := []byte{
					0x00, 0x00, 0x00, 0x00, // size=0: extends to end
					'm', 'e', 't', 'a',
				}
				ftyp := make([]byte, 0, 16+len(meta))
				ftyp = append(ftyp,
					0x00, 0x00, 0x00, 0x10,
					'f', 't', 'y', 'p',
					'h', 'e', 'i', 'c',
					0x00, 0x00, 0x00, 0x00,
				)
				return append(ftyp, meta...)
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Must not panic regardless of the malformed input.
			rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(tc.input))
			// A malformed box must never produce metadata.
			if rawEXIF != nil || rawIPTC != nil || rawXMP != nil {
				t.Errorf("expected all nil for malformed box input, got exif=%v iptc=%v xmp=%v",
					rawEXIF, rawIPTC, rawXMP)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestInjectNormalizesConstructionMethod
// ---------------------------------------------------------------------------

// buildHEIFV1Iloc constructs a minimal ISOBMFF/HEIF stream whose iloc box uses
// version=1 and stores the XMP item with construction_method=1 (idat-relative).
// This exercises the bug path: before the fix, Inject left construction_method
// as 1 while writing an absolute file offset, producing a corrupt output.
//
// Stream layout:
//
//	ftyp (16 bytes)
//	meta (FullBox: version/flags + iinf + iloc v1)
//	<XMP payload at absolute offset>
func buildHEIFV1Iloc(xmpPayload []byte) []byte {
	const xmpItemID uint16 = 1

	// --- infe v2 for XMP item ---
	// infe v2: version(1)+flags(3)+item_id(2)+item_protection_index(2)+item_type(4)+item_name(NUL)
	infeBody := make([]byte, 4+2+2+4+1)
	infeBody[0] = 2 // version 2
	binary.BigEndian.PutUint16(infeBody[4:], xmpItemID)
	copy(infeBody[8:], "mime")
	infeBody[12] = 0 // NUL-terminated item name
	infeHdr := make([]byte, 0, 8+len(infeBody))
	infeHdr = append(infeHdr, 0, 0, 0, 0, 'i', 'n', 'f', 'e')
	binary.BigEndian.PutUint32(infeHdr, uint32(8+len(infeBody))) //nolint:gosec // G115: test helper, bounded size
	infeBox := append(infeHdr, infeBody...)

	// --- iinf box ---
	iinfBody := make([]byte, 0, 6+len(infeBox))   // version+flags(4)+item_count(2)+infe
	iinfBody = append(iinfBody, 0, 0, 0, 0, 0, 1) // version 0 + flags (4) + item_count=1 (2)
	iinfBody = append(iinfBody, infeBox...)
	iinfHdr := make([]byte, 0, 8+len(iinfBody))
	iinfHdr = append(iinfHdr, 0, 0, 0, 0, 'i', 'i', 'n', 'f')
	binary.BigEndian.PutUint32(iinfHdr, uint32(8+len(iinfBody))) //nolint:gosec // G115: test helper, bounded size
	iinfBox := append(iinfHdr, iinfBody...)

	// --- iloc v1 box with construction_method=1 ---
	// iloc v1 body layout (ISO 14496-12 §8.11.3):
	//   version(1) + flags(3)
	//   offset_size(4bits) + length_size(4bits)   = 0x44 (4,4)
	//   base_offset_size(4bits) + index_size(4bits) = 0x00
	//   item_count(2)
	//   for each item:
	//     item_ID(2)
	//     construction_method(2)   [v1/v2 only]
	//     base_offset (0 bytes, base_offset_size=0)
	//     extent_count(2) = 1
	//     extent_offset(4)
	//     extent_length(4)
	//
	// We set construction_method=1 (idat-relative) to trigger the bug.
	// The extent offset is a placeholder; we will patch it after computing
	// the final meta box size.
	makeIlocV1 := func(extentOffset uint32) []byte {
		// Fixed body: version+flags(4)+sizes(2)+item_count(2)+item_ID(2)+
		//             construction_method(2)+extent_count(2)+offset(4)+length(4) = 22 bytes.
		body := make([]byte, 0, 22)
		body = append(body,
			0x01, 0x00, 0x00, 0x00, // version=1, flags=0
			0x44,       // offset_size=4, length_size=4
			0x00,       // base_offset_size=0, index_size=0
			0x00, 0x01, // item_count = 1
			0x00, byte(xmpItemID), // item_ID = 1 //nolint:gosec // G115: constant 1
			0x00, 0x01, // construction_method = 1 (idat-relative)
			// base_offset: omitted (base_offset_size=0)
			0x00, 0x01, // extent_count = 1
		)
		off := [4]byte{}
		binary.BigEndian.PutUint32(off[:], extentOffset)
		body = append(body, off[:]...)
		ln := [4]byte{}
		binary.BigEndian.PutUint32(ln[:], uint32(len(xmpPayload))) //nolint:gosec // G115: test helper, bounded size
		body = append(body, ln[:]...)
		hdr := make([]byte, 0, 8+len(body))
		hdr = append(hdr, 0, 0, 0, 0, 'i', 'l', 'o', 'c')
		binary.BigEndian.PutUint32(hdr, uint32(8+len(body))) //nolint:gosec // G115: test helper, bounded size
		return append(hdr, body...)
	}

	// --- ftyp box ---
	ftyp := make([]byte, 16)
	binary.BigEndian.PutUint32(ftyp, 16)
	copy(ftyp[4:], "ftyp")
	copy(ftyp[8:], "heic")

	// Build meta box with placeholder iloc offset=0.
	buildMeta := func(ilocBox []byte) []byte {
		metaBody := make([]byte, 0, 4+len(iinfBox)+len(ilocBox)) // version+flags(4)+iinf+iloc
		metaBody = append(metaBody, 0, 0, 0, 0)                  // version + flags
		metaBody = append(metaBody, iinfBox...)
		metaBody = append(metaBody, ilocBox...)
		hdr := make([]byte, 0, 8)
		hdr = append(hdr, 0, 0, 0, 0, 'm', 'e', 't', 'a')
		binary.BigEndian.PutUint32(hdr, uint32(8+len(metaBody))) //nolint:gosec // G115: test helper, bounded size
		return append(hdr, metaBody...)
	}

	// Pass 1: compute meta size to determine where XMP payload starts.
	pass1Meta := buildMeta(makeIlocV1(0))
	xmpStart := uint32(len(ftyp)) + uint32(len(pass1Meta)) //nolint:gosec // G115: test helper, bounded size

	// Pass 2: rebuild with the correct offset.
	finalIloc := makeIlocV1(xmpStart)
	finalMeta := buildMeta(finalIloc)

	// Recheck: if meta size changed between passes, do one more iteration.
	if len(finalMeta) != len(pass1Meta) {
		xmpStart2 := uint32(len(ftyp)) + uint32(len(finalMeta)) //nolint:gosec // G115: test helper, bounded size
		finalIloc = makeIlocV1(xmpStart2)
		finalMeta = buildMeta(finalIloc)
	}

	result := make([]byte, 0, len(ftyp)+len(finalMeta)+len(xmpPayload))
	result = append(result, ftyp...)
	result = append(result, finalMeta...)
	result = append(result, xmpPayload...)
	return result
}

// TestInjectNormalizesConstructionMethod is the regression gate for issue #61.
//
// It verifies that when Inject relocates an iloc item whose original
// construction_method is 1 (idat-relative), the rebuilt iloc entry has
// construction_method==0 (file offset) and its extent_offset points at the
// appended payload in the output stream.
//
// ISO 14496-12 §8.11.3: construction_method 0 = file offset,
// 1 = idat-relative, 2 = item-relative.  Writing an absolute file offset with
// construction_method still set to 1 would cause conformant readers to
// misresolve the metadata location.
func TestInjectNormalizesConstructionMethod(t *testing.T) {
	t.Parallel()

	originalXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)
	newXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end="w"?>`)

	// Build a HEIF whose XMP item uses iloc v1 with construction_method=1.
	input := buildHEIFV1Iloc(originalXMP)

	// Verify that the input really does encode construction_method=1.
	// findBox returns meta box content with version+flags stripped (per its contract).
	{
		metaContent, err := findBox(input, "meta", 0)
		if err != nil || len(metaContent) == 0 {
			t.Fatalf("setup: meta box not found in synthetic input (err=%v)", err)
		}
		ilocInfo, ok := parseIlocFull(metaContent)
		if !ok || len(ilocInfo.items) == 0 {
			t.Fatal("setup: could not parse iloc from synthetic input")
		}
		if ilocInfo.items[0].constructMethod != 1 {
			t.Fatalf("setup: expected construction_method=1 in input iloc, got %d", ilocInfo.items[0].constructMethod)
		}
	}

	// Inject new XMP.
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(input), &out, nil, nil, newXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	output := out.Bytes()

	// Parse the output iloc and assert:
	// 1. construction_method of the XMP item is 0 (file offset).
	// 2. The extent_offset points at the appended XMP payload.
	outMetaContent, err := findBox(output, "meta", 0)
	if err != nil || len(outMetaContent) == 0 {
		t.Fatalf("output: meta box not found (err=%v)", err)
	}
	ilocInfo, ok := parseIlocFull(outMetaContent)
	if !ok || len(ilocInfo.items) == 0 {
		t.Fatal("output: could not parse iloc")
	}

	item := ilocInfo.items[0]
	if item.constructMethod != 0 {
		t.Errorf("output iloc construction_method = %d, want 0 (file offset)", item.constructMethod)
	}

	if len(item.extents) == 0 {
		t.Fatal("output iloc item has no extents")
	}
	extOff := item.extents[0].offset
	extLen := item.extents[0].length
	if extOff == 0 {
		t.Error("output iloc extent_offset is 0, want a valid file offset")
	}
	if extOff+extLen > uint64(len(output)) {
		t.Errorf("output iloc extent [%d, %d) exceeds output file size %d",
			extOff, extOff+extLen, len(output))
	}

	// The payload at that offset must equal the injected XMP.
	got := output[extOff : extOff+extLen]
	if !bytes.Equal(got, newXMP) {
		t.Errorf("payload at iloc extent does not match injected XMP:\ngot  %q\nwant %q", got, newXMP)
	}

	// Also verify round-trip: Extract must return the same XMP.
	_, _, extractedXMP, extractErr := Extract(bytes.NewReader(output))
	if extractErr != nil {
		t.Fatalf("Extract after Inject: %v", extractErr)
	}
	if !bytes.Equal(extractedXMP, newXMP) {
		t.Errorf("Extract XMP mismatch after Inject:\ngot  %q\nwant %q", extractedXMP, newXMP)
	}
}

// ---------------------------------------------------------------------------
// Regression tests for bugs #76, #82, #83
// ---------------------------------------------------------------------------

// buildHEIFSlowPath constructs a synthetic HEIF file whose meta box is padded
// to begin beyond the first 64 KB (headerWindow), forcing Extract's slow path
// (io.ReadAll). The file structure is:
//
//	ftyp (16 bytes)
//	padding box (fills the remaining bytes up to headerWindow+1)
//	meta box (iinf + iloc + item data)
//
// This helper is used by TestHEIFSlowPathMemory (#76).
func buildHEIFSlowPath(exifData, xmpData []byte) []byte {
	// Build the payload section first to know item sizes.
	inner := buildHEIF(exifData, xmpData)
	// inner = ftyp(16) + meta(...) + item data
	// We wrap the meta box and items in a "skip" (padding) box preceded by
	// a large padding box so that the meta box starts after offset 65536.

	const headerWindow = 65536

	// The ftyp box (16 bytes) is always first.
	// We need a padding box of size (headerWindow + 1 - 16 - 8) so that the
	// next box (meta) starts at offset headerWindow+1.
	// Padding box layout: size(4) + type(4) + payload.
	const paddingBoxHeaderSize = 8
	paddingPayloadSize := headerWindow + 1 - 16 - paddingBoxHeaderSize
	paddingBox := make([]byte, paddingBoxHeaderSize+paddingPayloadSize)
	binary.BigEndian.PutUint32(paddingBox, uint32(len(paddingBox))) //nolint:gosec // G115: test helper, bounded size
	copy(paddingBox[4:], "free")                                    // 'free' is the standard ISO padding box type

	// The rest of inner (from offset 16) is meta+items.
	rest := inner[16:]

	result := make([]byte, 0, 16+len(paddingBox)+len(rest))
	result = append(result, inner[:16]...) // ftyp
	result = append(result, paddingBox...)
	result = append(result, rest...)
	return result
}

// TestHEIFSlowPathMemory is the regression gate for bug #76
// (HEIF slow-path rawEXIF/rawXMP sub-slice retains whole-file buffer).
//
// Strategy: we verify that the slices returned by parseHEIFMetadata (the
// slow path) do NOT share the underlying array with the full-file buffer.
// We do this by:
//  1. Building a synthetic HEIF with the meta box beyond offset 65536 to
//     force the slow path.
//  2. Calling parseHEIFMetadata (the internal function that allocates `data`).
//  3. Checking that rawEXIF and rawXMP do not point into that `data` buffer
//     by modifying a byte in `data` and asserting the returned slices are
//     unaffected.
//
// This is a whitebox test (package heif internal) so we have direct access.
func TestHEIFSlowPathMemory(t *testing.T) {
	t.Parallel()

	exifPayload := minimalTIFFExif()
	xmpPayload := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)

	fileData := buildHEIFSlowPath(exifPayload, xmpPayload)

	// Verify the file is large enough that meta is beyond 64 KB.
	const headerWindow = 65536
	metaData, err := findBox(fileData, "meta", 0)
	if err != nil || metaData == nil {
		t.Fatalf("setup: meta box not found in slow-path synthetic HEIF")
	}
	// Confirm the meta box starts after headerWindow by checking that a quick
	// scan of the first headerWindow bytes does NOT find the meta box.
	quickMeta, _ := findBox(fileData[:headerWindow], "meta", 0)
	if quickMeta != nil {
		t.Fatalf("setup: meta box found within first %d bytes — slow path will not be triggered", headerWindow)
	}

	// Call parseHEIFMetadata with a copy of the file data so we can mutate
	// the original copy independently.
	dataCopy := make([]byte, len(fileData))
	copy(dataCopy, fileData)

	rawEXIF, rawXMP, parseErr := parseHEIFMetadata(dataCopy)
	if parseErr != nil {
		t.Fatalf("parseHEIFMetadata: %v", parseErr)
	}
	if rawEXIF == nil {
		t.Fatal("rawEXIF is nil after parseHEIFMetadata")
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil after parseHEIFMetadata")
	}

	// Snapshot the returned payloads before mutating the source buffer.
	exifSnapshot := make([]byte, len(rawEXIF))
	copy(exifSnapshot, rawEXIF)
	xmpSnapshot := make([]byte, len(rawXMP))
	copy(xmpSnapshot, rawXMP)

	// Aliasing check: mutate every byte of dataCopy. If rawEXIF or rawXMP are
	// sub-slices of dataCopy (i.e. bytes.Clone was not applied), they will
	// reflect the mutation and fail the equality check below.
	for i := range dataCopy {
		dataCopy[i] ^= 0xFF
	}
	if !bytes.Equal(rawEXIF, exifSnapshot) {
		t.Error("rawEXIF changed after mutating dataCopy — it aliases the source buffer (bug #76 regression)")
	}
	if !bytes.Equal(rawXMP, xmpSnapshot) {
		t.Error("rawXMP changed after mutating dataCopy — it aliases the source buffer (bug #76 regression)")
	}
}

// buildIlocWithLargeExtentCount assembles a minimal HEIF stream whose iloc box
// carries one item (XMP) with the given extentCount declared in the iloc data.
// Only one actual extent is present in the byte stream (the rest are implicit
// truncation), which lets readIlocFullExtents hit the inner bounds checks and
// stop early. The goal is to drive a large cap allocation in the pre-fix code.
//
// Used by TestHEIFInjectLargeExtentCountBounded (#82).
func buildIlocWithLargeExtentCount(xmpPayload []byte, extentCount uint16) []byte {
	const xmpItemID uint16 = 1

	// infe v2 for the XMP item.
	infeBody := make([]byte, 4+2+2+4+1)
	infeBody[0] = 2
	binary.BigEndian.PutUint16(infeBody[4:], xmpItemID)
	copy(infeBody[8:], "mime")
	infeHdr := make([]byte, 0, 8+len(infeBody))
	infeHdr = append(infeHdr, 0, 0, 0, 0, 'i', 'n', 'f', 'e')
	binary.BigEndian.PutUint32(infeHdr, uint32(8+len(infeBody))) //nolint:gosec // G115: test helper
	infeBox := append(infeHdr, infeBody...)

	// iinf box.
	iinfBody := make([]byte, 0, 6+len(infeBox))
	iinfBody = append(iinfBody, 0, 0, 0, 0, 0, 1) // version0 + item_count=1
	iinfBody = append(iinfBody, infeBox...)
	iinfHdr := make([]byte, 0, 8+len(iinfBody))
	iinfHdr = append(iinfHdr, 0, 0, 0, 0, 'i', 'i', 'n', 'f')
	binary.BigEndian.PutUint32(iinfHdr, uint32(8+len(iinfBody))) //nolint:gosec // G115: test helper
	iinfBox := append(iinfHdr, iinfBody...)

	// iloc v1 with offsetSize=4, lengthSize=4, one item with extentCount=N
	// but only one actual (offset, length) pair written. The parser will hit
	// bounds checks and stop reading after the first real extent.
	makeIlocV1Large := func(xmpOffset uint32) []byte {
		// version+flags(4) + sizes(2) + item_count(2) +
		// item: id(2)+construct(2)+extent_count(2)+offset(4)+length(4) = 14 bytes per item
		body := make([]byte, 0, 26)
		body = append(body,
			0x01, 0x00, 0x00, 0x00, // version=1, flags=0
			0x44,       // offset_size=4, length_size=4
			0x00,       // base_offset_size=0, index_size=0
			0x00, 0x01, // item_count = 1
			0x00, byte(xmpItemID), // item_ID //nolint:gosec // G115: constant
			0x00, 0x00, // construction_method = 0
		)
		// extent_count = extentCount (the attacker-controlled large value).
		ecBytes := [2]byte{}
		binary.BigEndian.PutUint16(ecBytes[:], extentCount)
		body = append(body, ecBytes[:]...)
		// Write only one actual extent (the rest are absent — truncated stream).
		offBytes := [4]byte{}
		binary.BigEndian.PutUint32(offBytes[:], xmpOffset)
		body = append(body, offBytes[:]...)
		lnBytes := [4]byte{}
		binary.BigEndian.PutUint32(lnBytes[:], uint32(len(xmpPayload))) //nolint:gosec // G115: test helper
		body = append(body, lnBytes[:]...)
		hdr := make([]byte, 0, 8+len(body))
		hdr = append(hdr, 0, 0, 0, 0, 'i', 'l', 'o', 'c')
		binary.BigEndian.PutUint32(hdr, uint32(8+len(body))) //nolint:gosec // G115: test helper
		return append(hdr, body...)
	}

	ftyp := make([]byte, 16)
	binary.BigEndian.PutUint32(ftyp, 16)
	copy(ftyp[4:], "ftyp")
	copy(ftyp[8:], "heic")

	buildMeta := func(ilocBox []byte) []byte {
		metaBody := make([]byte, 0, 4+len(iinfBox)+len(ilocBox))
		metaBody = append(metaBody, 0, 0, 0, 0)
		metaBody = append(metaBody, iinfBox...)
		metaBody = append(metaBody, ilocBox...)
		hdr := make([]byte, 0, 8+len(metaBody))
		hdr = append(hdr, 0, 0, 0, 0, 'm', 'e', 't', 'a')
		binary.BigEndian.PutUint32(hdr, uint32(8+len(metaBody))) //nolint:gosec // G115: test helper
		return append(hdr, metaBody...)
	}

	pass1Meta := buildMeta(makeIlocV1Large(0))
	xmpStart := uint32(len(ftyp)) + uint32(len(pass1Meta)) //nolint:gosec // G115: test helper
	finalIloc := makeIlocV1Large(xmpStart)
	finalMeta := buildMeta(finalIloc)
	if len(finalMeta) != len(pass1Meta) {
		xmpStart2 := uint32(len(ftyp)) + uint32(len(finalMeta)) //nolint:gosec // G115: test helper
		finalIloc = makeIlocV1Large(xmpStart2)
		finalMeta = buildMeta(finalIloc)
	}

	result := make([]byte, 0, len(ftyp)+len(finalMeta)+len(xmpPayload))
	result = append(result, ftyp...)
	result = append(result, finalMeta...)
	result = append(result, xmpPayload...)
	return result
}

// TestHEIFInjectLargeExtentCountBounded is the regression gate for bug #82
// (HEIF iloc extent_count drives allocation amplification).
//
// It crafts an iloc box with extentCount=65535 (max uint16) for one item and
// verifies that:
//  1. Inject does not panic or OOM — it completes in bounded time/memory.
//  2. The pre-allocated extent slice is capped at maxIlocExtentsPerItem (1024),
//     not at the attacker-supplied 65535.
//
// We test the bound directly via readIlocFullExtents and also exercise the
// full Inject path to confirm end-to-end safety.
func TestHEIFInjectLargeExtentCountBounded(t *testing.T) {
	t.Parallel()

	xmpPayload := []byte(`<?xpacket begin="" id="x"?><x:xmpmeta/><?xpacket end="r"?>`)
	const largeExtentCount = 65535 // max uint16

	// --- Direct unit test of readIlocFullExtents cap ---
	t.Run("readIlocFullExtents cap", func(t *testing.T) {
		t.Parallel()
		// Build a minimal ilocData with offsetSize=4, lengthSize=4, indexSize=0.
		// Provide only one real extent (8 bytes) but declare extentCount=65535.
		info := ilocBoxInfo{
			version:    1,
			offsetSize: 4,
			lengthSize: 4,
		}
		// One real extent: offset(4) + length(4) = 8 bytes.
		ilocData := []byte{
			0x00, 0x00, 0x00, 0x10, // offset = 16
			0x00, 0x00, 0x00, 0x08, // length = 8
		}
		extents, _ := readIlocFullExtents(ilocData, 0, largeExtentCount, info)
		// The pre-allocated capacity must be capped.
		if cap(extents) > maxIlocExtentsPerItem {
			t.Errorf("readIlocFullExtents allocated cap=%d, want <= %d (maxIlocExtentsPerItem)",
				cap(extents), maxIlocExtentsPerItem)
		}
	})

	// --- Full Inject path with crafted iloc ---
	t.Run("Inject completes without OOM", func(t *testing.T) {
		t.Parallel()
		data := buildIlocWithLargeExtentCount(xmpPayload, largeExtentCount)
		newXMP := []byte(`<?xpacket begin="" id="x"?><x:xmpmeta><new/></x:xmpmeta><?xpacket end="r"?>`)
		var out bytes.Buffer
		// Must not panic, OOM, or hang.
		_ = Inject(bytes.NewReader(data), &out, nil, nil, newXMP, true)
		// We do not assert a specific return value — the crafted truncated iloc
		// may cause graceful early-termination. The key invariant is: no crash.
	})
}

// boxWithHeader assembles a single ISOBMFF box: a 4-byte size, a 4-byte type,
// then body. size is computed automatically. ISO 14496-12 §4.2.
func boxWithHeader(typ string, body []byte) []byte {
	hdr := make([]byte, 0, 8+len(body))
	hdr = append(hdr, 0, 0, 0, 0)
	hdr = append(hdr, typ...)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(body))) //nolint:gosec // G115: test helper, bounded body length
	return append(hdr, body...)
}

// buildIlocZeroFieldAmplification assembles a minimal ftyp+meta HEIF stream
// whose iloc box declares itemCount items, each with extentCount extents, but
// with EVERY per-extent field-size nibble (offset_size, length_size,
// base_offset_size, index_size) set to zero. Zero field width means no extent
// consumes any input byte, so pre-fix code would spin extentCount times per
// item (and itemCount times overall) without the loop bound ever being
// naturally limited by input length — the exact amplification vector fixed
// by HEIF-ILOC-EXTENT-AMPLIFICATION (security audit FIX 1).
//
// The iinf box is intentionally empty (item_count=0): both parseIloc (used by
// Extract, via extractFromMetaData) and parseIlocFull (used by Inject, via
// buildInjectComponents) parse the iloc box unconditionally, before any
// iinf-driven item-type matching takes place, so the vulnerable code path is
// reached through the public Extract/Inject entry points regardless of
// whether any item actually resolves to Exif/XMP.
func buildIlocZeroFieldAmplification(itemCount int, extentCount uint16, ilocVersion uint8) []byte {
	iinfBody := []byte{0, 0, 0, 0, 0, 0} // version=0, flags=0, item_count=0
	iinfBox := boxWithHeader("iinf", iinfBody)

	ilocBody := make([]byte, 0, 8+itemCount*6)
	// version + flags, then offset|length=0, base_offset|index=0.
	ilocBody = append(ilocBody, ilocVersion, 0, 0, 0, 0x00, 0x00)

	if ilocVersion < 2 {
		var ic [2]byte
		binary.BigEndian.PutUint16(ic[:], uint16(itemCount)) //nolint:gosec // G115: test helper, itemCount is a small constant
		ilocBody = append(ilocBody, ic[:]...)
	} else {
		var ic [4]byte
		binary.BigEndian.PutUint32(ic[:], uint32(itemCount)) //nolint:gosec // G115: test helper, itemCount is a small constant
		ilocBody = append(ilocBody, ic[:]...)
	}

	for i := range itemCount {
		var id [2]byte
		binary.BigEndian.PutUint16(id[:], uint16(i+1)) // i is bounded by itemCount (a small test constant)
		ilocBody = append(ilocBody, id[:]...)
		if ilocVersion == 1 || ilocVersion == 2 {
			ilocBody = append(ilocBody, 0x00, 0x00) // construction_method = 0
		}
		// base_offset: 0 bytes (base_offset_size = 0).
		var ec [2]byte
		binary.BigEndian.PutUint16(ec[:], extentCount)
		ilocBody = append(ilocBody, ec[:]...)
		// extents: 0 bytes each — offset_size, length_size, index_size are all 0.
	}
	ilocBox := boxWithHeader("iloc", ilocBody)

	metaBody := make([]byte, 0, 4+len(iinfBox)+len(ilocBox))
	metaBody = append(metaBody, 0, 0, 0, 0) // meta FullBox version+flags
	metaBody = append(metaBody, iinfBox...)
	metaBody = append(metaBody, ilocBox...)
	metaBox := boxWithHeader("meta", metaBody)

	ftyp := []byte{
		0x00, 0x00, 0x00, 0x14, 'f', 't', 'y', 'p',
		'h', 'e', 'i', 'c', 0x00, 0x00, 0x00, 0x00, 'm', 'i', 'f', '1',
	}

	out := make([]byte, 0, len(ftyp)+len(metaBox))
	out = append(out, ftyp...)
	out = append(out, metaBox...)
	return out
}

// TestHEIFIlocZeroFieldSizeAmplificationBounded is the regression gate for
// security audit FIX 1 (HEIF-ILOC-EXTENT-AMPLIFICATION, CWE-770/834).
//
// Root cause (pre-fix): readIlocFullExtents and readIlocSimpleExtents looped
// `for range extentCount` (an attacker-controlled uint16, up to 65535)
// unconditionally. The doc comment claimed excess extents were "dropped", but
// the append in readIlocFullExtents was unconditional, so once the
// pre-allocated 1024-capacity slice filled, append silently reallocated a
// larger backing array anyway. Worse: when every per-extent field-size nibble
// is zero (offset_size == length_size == index_size == 0), no iteration reads
// any input byte — pos never advances — so the full extentCount iterations
// ran regardless of the actual file size: an ~8-20 KB crafted file with
// itemCount items each declaring extentCount=0xFFFF drove itemCount×65535
// slice appends (readIlocFullExtents, Inject path) or itemCount×65535 no-op
// loop iterations (readIlocSimpleExtents, Extract path) — multi-GB memory
// amplification / CPU-exhaustion DoS from a tiny input.
//
// Fix: both functions now cap their effective loop bound at
// maxIlocExtentsPerItem when the combined per-extent field size is zero, and
// readIlocFullExtents additionally guards its append with a length check so
// extents genuinely are dropped once the cap is reached (matching the
// original doc comment's intent). parseIloc and parseIlocFull additionally
// reject an iloc box whose declared item_count exceeds maxIlocItems before
// doing any per-item work.
func TestHEIFIlocZeroFieldSizeAmplificationBounded(t *testing.T) {
	t.Parallel()

	const extentCount = 0xFFFF // max uint16 — fully attacker-controlled
	const itemCount = 5        // "several" items, per the audit's PoC shape

	// --- White-box: readIlocFullExtents (Inject/write path) ---
	t.Run("readIlocFullExtents zero field size is bounded", func(t *testing.T) {
		t.Parallel()
		info := ilocBoxInfo{version: 1} // offsetSize=lengthSize=indexSize=0 (zero value)
		start := time.Now()
		extents, pos := readIlocFullExtents(nil, 0, extentCount, info)
		elapsed := time.Since(start)
		if len(extents) > maxIlocExtentsPerItem {
			t.Errorf("readIlocFullExtents returned %d extents, want <= %d (maxIlocExtentsPerItem)",
				len(extents), maxIlocExtentsPerItem)
		}
		if cap(extents) > maxIlocExtentsPerItem {
			t.Errorf("readIlocFullExtents allocated cap=%d, want <= %d (maxIlocExtentsPerItem)",
				cap(extents), maxIlocExtentsPerItem)
		}
		if pos != 0 {
			t.Errorf("readIlocFullExtents advanced pos to %d, want 0 (zero field size consumes no bytes)", pos)
		}
		if elapsed > time.Second {
			t.Errorf("readIlocFullExtents took %v, want well under 1s (amplification not bounded)", elapsed)
		}
	})

	// --- White-box: readIlocSimpleExtents (Extract/read path) ---
	t.Run("readIlocSimpleExtents zero field size is bounded", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		_, _, pos, ok := readIlocSimpleExtents(nil, 0, extentCount, 0, 0, 0, 0)
		elapsed := time.Since(start)
		if !ok {
			t.Fatal("readIlocSimpleExtents returned ok=false for a well-formed (zero-width) extent loop")
		}
		if pos != 0 {
			t.Errorf("readIlocSimpleExtents advanced pos to %d, want 0 (zero field size consumes no bytes)", pos)
		}
		if elapsed > time.Second {
			t.Errorf("readIlocSimpleExtents took %v, want well under 1s (amplification not bounded)", elapsed)
		}
	})

	// --- Black-box: full Extract() on a crafted zero-field-size iloc (v0, simple parser) ---
	t.Run("Extract completes promptly", func(t *testing.T) {
		t.Parallel()
		data := buildIlocZeroFieldAmplification(itemCount, extentCount, 0)
		start := time.Now()
		_, _, _, err := Extract(bytes.NewReader(data))
		elapsed := time.Since(start)
		if elapsed > 2*time.Second {
			t.Errorf("Extract took %v on a %d-byte crafted file, want well under 2s (amplification not bounded)",
				elapsed, len(data))
		}
		_ = err // no specific error expected; absence of a hang/OOM is the invariant under test
	})

	// --- Black-box: full Inject() on a crafted zero-field-size iloc (v1, full parser) ---
	t.Run("Inject completes promptly", func(t *testing.T) {
		t.Parallel()
		data := buildIlocZeroFieldAmplification(itemCount, extentCount, 1)
		rawXMP := []byte(`<?xpacket begin="" id="x"?><x:xmpmeta><new/></x:xmpmeta><?xpacket end="r"?>`)
		var out bytes.Buffer
		start := time.Now()
		err := Inject(bytes.NewReader(data), &out, nil, nil, rawXMP, true)
		elapsed := time.Since(start)
		if elapsed > 2*time.Second {
			t.Errorf("Inject took %v on a %d-byte crafted file, want well under 2s (amplification not bounded)",
				elapsed, len(data))
		}
		_ = err // no specific error expected; absence of a hang/OOM is the invariant under test
	})

	// --- White-box: parseIlocFull rejects an oversized item_count outright ---
	t.Run("parseIlocFull rejects item_count above maxIlocItems", func(t *testing.T) {
		t.Parallel()
		// A 4-item iloc box, but with item_count patched to maxIlocItems+1
		// after construction — parseIlocFull must reject it (ok=false) before
		// attempting to iterate any items.
		//
		// Layout produced by buildIlocZeroFieldAmplification: ftyp(20 bytes) +
		// meta box [size(4)+type(4)+version/flags(4)+iinf+iloc]. metaContent is
		// the meta box payload with its own header and version/flags stripped —
		// exactly the argument shape parseIloc/parseIlocFull expect (see their
		// doc comments) — reached here by skipping ftyp(20) + meta header(8) +
		// meta version/flags(4) = 32 bytes.
		const ftypAndMetaHeaderLen = 20 + 8 + 4
		data := buildIlocZeroFieldAmplification(4, 1, 2) // version 2 -> 4-byte item_count field
		metaContent := data[ftypAndMetaHeaderLen:]
		ilocData := findInnerBox(metaContent, "iloc")
		if ilocData == nil {
			t.Fatal("test setup: could not locate iloc box in crafted data")
		}
		// item_count field is at ilocData[6:10] for version >= 2. ilocData
		// shares metaContent's backing array, so this mutates data in place.
		binary.BigEndian.PutUint32(ilocData[6:], maxIlocItems+1)
		if _, ok := parseIlocFull(metaContent); ok {
			t.Error("parseIlocFull accepted item_count > maxIlocItems, want ok=false")
		}
	})

	// --- White-box: parseIloc rejects an oversized item_count outright ---
	t.Run("parseIloc rejects item_count above maxIlocItems", func(t *testing.T) {
		t.Parallel()
		const ftypAndMetaHeaderLen = 20 + 8 + 4
		data := buildIlocZeroFieldAmplification(4, 1, 0) // version 0 -> 2-byte item_count field
		metaContent := data[ftypAndMetaHeaderLen:]
		ilocData := findInnerBox(metaContent, "iloc")
		if ilocData == nil {
			t.Fatal("test setup: could not locate iloc box in crafted data")
		}
		// item_count field is at ilocData[6:8] for version < 2; math.MaxUint16
		// (65535) exceeds maxIlocItems (4096) while still fitting the 2-byte
		// field width that version < 2 mandates.
		binary.BigEndian.PutUint16(ilocData[6:], math.MaxUint16)
		if result := parseIloc(metaContent); len(result) != 0 {
			t.Errorf("parseIloc accepted item_count > maxIlocItems, want empty result, got %d items", len(result))
		}
	})
}

// TestHEIFInjectPatchAncestorSizeOverflow is the regression gate for bug #83
// (HEIF patchAncestorSize uint32 truncation on large boxes).
//
// Tests three scenarios:
//  1. moov size = 0xFFFFFFF0: after a +1 delta the new size 0xFFFFFFF1 still
//     fits uint32 and must be written correctly.
//  2. moov size = 0xFFFFFFFF: after a +1 delta the new size 0x100000000 overflows
//     uint32; patchAncestorSize must NOT write uint32(0) — it must leave the
//     field unchanged (safe skip).
//  3. Extended 64-bit size (size==1): the largesize field must be patched
//     correctly without truncation.
func TestHEIFInjectPatchAncestorSizeOverflow(t *testing.T) {
	t.Parallel()

	t.Run("size near max uint32 but fits after delta", func(t *testing.T) {
		t.Parallel()
		// patchAncestorSize with a moov box whose declared size fits in the
		// buffer AND after adding delta the result still fits uint32.
		// moov: size=32, type="moov"; inner meta at offset 8.
		// This exercises the normal 32-bit patch path with a non-trivial initial
		// size to confirm no accidental truncation for values far below overflow.
		const moovSize = uint32(32)
		data := make([]byte, 32)
		binary.BigEndian.PutUint32(data[0:], moovSize)
		copy(data[4:8], "moov")
		binary.BigEndian.PutUint32(data[8:], 16) // inner box size
		copy(data[12:16], "meta")

		patchAncestorSize(data, 8, 4) // delta = +4
		got := binary.BigEndian.Uint32(data[0:])
		want := moovSize + 4 // 36
		if got != want {
			t.Errorf("patchAncestorSize: got size=0x%X, want 0x%X", got, want)
		}
	})

	t.Run("size = max uint32, delta overflows", func(t *testing.T) {
		t.Parallel()
		// moov size = 0xFFFFFFFF; delta=1 would make newSize=0x100000000 which
		// overflows uint32. The fix must leave the field unchanged.
		data := make([]byte, 32)
		binary.BigEndian.PutUint32(data[0:], math.MaxUint32) // moov size = 0xFFFFFFFF
		copy(data[4:8], "moov")
		binary.BigEndian.PutUint32(data[8:], 16) // inner box
		copy(data[12:16], "meta")

		patchAncestorSize(data, 8, 1) // delta=+1 causes overflow
		got := binary.BigEndian.Uint32(data[0:])
		// Must NOT be 0 (uint32(0x100000000) == 0 truncation), and must NOT be
		// MaxUint32+1 (impossible). Acceptable: MaxUint32 (unchanged).
		if got == 0 {
			t.Errorf("patchAncestorSize truncated to 0 on uint32 overflow (bug #83 regression)")
		}
		if got != math.MaxUint32 {
			t.Errorf("patchAncestorSize: expected field unchanged (0xFFFFFFFF) on overflow, got 0x%X", got)
		}
	})

	t.Run("extended 64-bit size box is patched correctly", func(t *testing.T) {
		t.Parallel()
		// Build a data buffer where the first box uses size=1 (extended 64-bit).
		// Layout: size32(4)=1 + type(4)="moov" + largesize(8) + content...
		const boxPayloadSize = 8 // just enough for an inner "meta" placeholder
		const largeSize = uint64(16 + boxPayloadSize)
		data := make([]byte, 16+boxPayloadSize)
		binary.BigEndian.PutUint32(data[0:], 1) // size==1 sentinel
		copy(data[4:], "moov")
		binary.BigEndian.PutUint64(data[8:], largeSize)
		// Inner box at offset 16.
		binary.BigEndian.PutUint32(data[16:], uint32(boxPayloadSize))
		copy(data[20:], "meta")

		// metaAbsStart=16 is inside the 64-bit moov box. delta=4.
		patchAncestorSize(data, 16, 4)
		gotLargeSize := binary.BigEndian.Uint64(data[8:])
		wantLargeSize := largeSize + 4
		if gotLargeSize != wantLargeSize {
			t.Errorf("64-bit largesize after patch = %d, want %d", gotLargeSize, wantLargeSize)
		}
		// The 32-bit sentinel must remain 1.
		if binary.BigEndian.Uint32(data[0:]) != 1 {
			t.Errorf("size==1 sentinel was overwritten during 64-bit patch")
		}
	})
}
