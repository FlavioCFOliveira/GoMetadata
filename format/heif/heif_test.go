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
	// Truncated input must not panic.
	data := buildHEIF(minimalTIFFExif(), nil)
	for i := 0; i < len(data); i += len(data) / 8 {
		_, _, _, _ = Extract(bytes.NewReader(data[:i]))
	}
}

func TestInjectRoundTrip(t *testing.T) {
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
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(data))
	}
}
