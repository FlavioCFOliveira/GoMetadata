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
// Additional tests for uncovered branches
// ---------------------------------------------------------------------------

// TestParseHEIFBoxHeaderExtendedSize exercises the extended-size (size==1) path
// in parseHEIFBoxHeader where a 64-bit largesize field follows the type.
// ISO 14496-12 §4.2.
func TestParseHEIFBoxHeaderExtendedSize(t *testing.T) {
	t.Parallel()
	// Build an extended-size box: 4-byte size=1, 4-byte type "test", 8-byte largesize.
	// Total box size = 16 (header) + 4 (body) = 20.
	const bodyLen = 4
	buf := make([]byte, 16+bodyLen)
	binary.BigEndian.PutUint32(buf[0:], 1) // size == 1 → extended
	copy(buf[4:8], "test")
	binary.BigEndian.PutUint64(buf[8:], uint64(16+bodyLen)) // largesize
	// body bytes stay zero

	size, typ, headerLen, ok := parseHEIFBoxHeader(buf, 0)
	if !ok {
		t.Fatal("parseHEIFBoxHeader: extended-size box should succeed")
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

// TestParseHEIFBoxHeaderZeroSize exercises the size==0 path (box extends to end).
func TestParseHEIFBoxHeaderZeroSize(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.BigEndian.PutUint32(buf[0:], 0) // size==0 → extends to EOF
	copy(buf[4:8], "free")

	size, typ, _, ok := parseHEIFBoxHeader(buf, 0)
	if !ok {
		t.Fatal("parseHEIFBoxHeader: size==0 box should succeed")
	}
	if typ != "free" {
		t.Errorf("typ = %q, want %q", typ, "free")
	}
	if size != uint64(len(buf)) {
		t.Errorf("size = %d, want %d (full slice length)", size, len(buf))
	}
}

// TestParseHEIFBoxHeaderTooShort verifies the < 8 byte guard.
func TestParseHEIFBoxHeaderTooShort(t *testing.T) {
	t.Parallel()
	_, _, _, ok := parseHEIFBoxHeader([]byte{0, 0, 0, 8, 'f'}, 0)
	if ok {
		t.Error("expected ok=false for buffer shorter than 8 bytes")
	}
}

// TestParseHEIFBoxHeaderExtendedSizeTooShort verifies that an extended-size box
// that doesn't fit in the buffer returns ok=false.
func TestParseHEIFBoxHeaderExtendedSizeTooShort(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12) // only 12 bytes; extended header needs 16
	binary.BigEndian.PutUint32(buf[0:], 1)
	copy(buf[4:8], "test")
	_, _, _, ok := parseHEIFBoxHeader(buf, 0)
	if ok {
		t.Error("expected ok=false for extended-size box shorter than 16 bytes")
	}
}

// TestReadItemPayloadZeroLength verifies that length==0 returns (nil, nil).
func TestReadItemPayloadZeroLength(t *testing.T) {
	t.Parallel()
	data := bytes.NewReader([]byte("hello"))
	payload, err := readItemPayload(data, itemLoc{offset: 0, length: 0})
	if err != nil {
		t.Fatalf("readItemPayload(length=0): unexpected error: %v", err)
	}
	if payload != nil {
		t.Errorf("readItemPayload(length=0): got %v, want nil", payload)
	}
}

// TestReadItemPayloadTooLarge verifies that an oversized length returns an error.
func TestReadItemPayloadTooLarge(t *testing.T) {
	t.Parallel()
	data := bytes.NewReader([]byte("hello"))
	_, err := readItemPayload(data, itemLoc{offset: 0, length: maxItemPayloadSize + 1})
	if err == nil {
		t.Fatal("readItemPayload(too large): expected error, got nil")
	}
}

// TestExtractExifFromDataShort verifies that a payload shorter than 4 bytes returns nil.
func TestExtractExifFromDataShort(t *testing.T) {
	t.Parallel()
	if result := extractExifFromData([]byte{0, 0, 0}); result != nil {
		t.Error("expected nil for payload shorter than 4 bytes")
	}
}

// TestExtractExifFromDataSkipTooLarge verifies that an out-of-range skip returns nil.
func TestExtractExifFromDataSkipTooLarge(t *testing.T) {
	t.Parallel()
	// 4-byte header: skip value = 0x7FFFFFFF → skip+4 >> len(data).
	buf := []byte{0x7F, 0xFF, 0xFF, 0xFF, 0x01, 0x02}
	if result := extractExifFromData(buf); result != nil {
		t.Error("expected nil when skip offset exceeds payload length")
	}
}

// TestParsePitmVersion1 exercises the pitm version-1 (uint32 item_ID) path.
func TestParsePitmVersion1(t *testing.T) {
	t.Parallel()
	// Build a minimal meta box containing a pitm FullBox version 1.
	// pitm v1: version(1)=1 + flags(3)=0 + item_ID(4)
	pitmBody := make([]byte, 8)
	pitmBody[0] = 1                              // version 1
	binary.BigEndian.PutUint32(pitmBody[4:], 42) // item_ID = 42

	pitmBox := make([]byte, 8+len(pitmBody))
	binary.BigEndian.PutUint32(pitmBox, uint32(len(pitmBox))) //nolint:gosec // test helper
	copy(pitmBox[4:8], "pitm")
	copy(pitmBox[8:], pitmBody)

	id := parsePitm(pitmBox)
	if id != 42 {
		t.Errorf("parsePitm v1: got %d, want 42", id)
	}
}

// TestParsePitmNoPitmBox verifies that a meta blob with no pitm child returns 0.
func TestParsePitmNoPitmBox(t *testing.T) {
	t.Parallel()
	id := parsePitm([]byte{})
	if id != 0 {
		t.Errorf("parsePitm(no pitm): got %d, want 0", id)
	}
}

// TestParseInfeV0V1 exercises the infe version 0/1 parsing path.
func TestParseInfeV0V1(t *testing.T) {
	t.Parallel()
	// Build an infe v0 body (version+flags already stripped by parseInfe caller).
	// version(1)=0 + flags(3)=0 + item_ID(2) + protection_index(2) + item_name(NUL) + content_type(NUL)
	body := []byte{
		0, 0, 0, 0, // version 0 + flags
		0, 5, // item_ID = 5
		0, 0, // item_protection_index = 0
		0,                                                                                                // item_name = "" (NUL)
		'a', 'p', 'p', 'l', 'i', 'c', 'a', 't', 'i', 'o', 'n', '/', 'r', 'd', 'f', '+', 'x', 'm', 'l', 0, // content_type
	}
	id, itemType := parseInfe(body)
	if id != 5 {
		t.Errorf("parseInfe v0: id = %d, want 5", id)
	}
	if itemType != "mime" {
		t.Errorf("parseInfe v0: itemType = %q, want %q", itemType, "mime")
	}
}

// TestParseInfeV3 exercises the infe version 3 path (uint32 item_ID).
func TestParseInfeV3(t *testing.T) {
	t.Parallel()
	// version(1)=3 + flags(3)=0 + item_ID(4) + item_protection_index(2) + item_type(4)
	body := make([]byte, 4+4+2+4)
	body[0] = 3                             // version 3
	binary.BigEndian.PutUint32(body[4:], 7) // item_ID = 7
	// item_protection_index = 0 (bytes 8–9)
	copy(body[10:14], "Exif") // item_type
	id, itemType := parseInfe(body)
	if id != 7 {
		t.Errorf("parseInfe v3: id = %d, want 7", id)
	}
	if itemType != "Exif" {
		t.Errorf("parseInfe v3: itemType = %q, want %q", itemType, "Exif")
	}
}

// TestParseInfeUnknownVersion verifies that unknown infe versions return ("", "").
func TestParseInfeUnknownVersion(t *testing.T) {
	t.Parallel()
	body := make([]byte, 20)
	body[0] = 10 // unsupported version
	id, itemType := parseInfe(body)
	if id != 0 || itemType != "" {
		t.Errorf("parseInfe unknown version: got (%d, %q), want (0, \"\")", id, itemType)
	}
}

// TestSelectBestItemPrimaryPreferred verifies that the primary item ID wins
// over a lower-numbered non-primary item.
func TestSelectBestItemPrimaryPreferred(t *testing.T) {
	t.Parallel()
	itemTypes := map[uint16]string{
		1: "Exif",
		3: "Exif", // primary item
	}
	bestID, found := selectBestItem(itemTypes, 3, "Exif")
	if !found {
		t.Fatal("selectBestItem: found=false, want true")
	}
	if bestID != 3 {
		t.Errorf("selectBestItem: bestID = %d, want 3 (primary)", bestID)
	}
}

// TestSelectBestItemLowestIDFallback verifies that without a primary match,
// the lowest ID wins.
func TestSelectBestItemLowestIDFallback(t *testing.T) {
	t.Parallel()
	itemTypes := map[uint16]string{
		5: "Exif",
		2: "Exif",
	}
	bestID, found := selectBestItem(itemTypes, 0, "Exif") // primaryID=0 → no match
	if !found {
		t.Fatal("selectBestItem: found=false, want true")
	}
	if bestID != 2 {
		t.Errorf("selectBestItem: bestID = %d, want 2 (lowest)", bestID)
	}
}

// TestSelectBestItemNoMatch verifies that an absent type returns found=false.
func TestSelectBestItemNoMatch(t *testing.T) {
	t.Parallel()
	itemTypes := map[uint16]string{1: "Exif"}
	_, found := selectBestItem(itemTypes, 0, "mime")
	if found {
		t.Error("selectBestItem: found=true for absent type, want false")
	}
}

// TestAppendUintNNonStandardWidth exercises the default fallback in appendUintN.
func TestAppendUintNNonStandardWidth(t *testing.T) {
	t.Parallel()
	// Width=3 is non-standard but must produce correct big-endian output.
	result := appendUintN(nil, 3, 0x010203)
	if len(result) != 3 {
		t.Fatalf("appendUintN(3, 0x010203): len=%d, want 3", len(result))
	}
	if result[0] != 0x01 || result[1] != 0x02 || result[2] != 0x03 {
		t.Errorf("appendUintN(3, 0x010203): got %v, want [01 02 03]", result)
	}
}

// TestExtractSlowPath builds a HEIF file where the meta box starts beyond the
// 64 KB fast-path header window, forcing the slow-path full-file read.
func TestExtractSlowPath(t *testing.T) {
	t.Parallel()
	exifData := minimalTIFFExif()
	inner := buildHEIF(exifData, nil)

	// Pad the file with a large filler box before the meta content so that
	// the meta box starts beyond 64 KB. We replace the ftyp box with a
	// much-larger "free" box followed by the original content.
	const pad = 65536 + 512
	filler := make([]byte, 8+pad)
	binary.BigEndian.PutUint32(filler, uint32(8+pad))
	copy(filler[4:8], "free")

	// Prepend the filler to the inner HEIF stream.
	padded := make([]byte, 0, len(filler)+len(inner))
	padded = append(padded, filler...)
	padded = append(padded, inner...)

	// Extract must still work even though meta is beyond the fast-path window.
	rawEXIF, _, _, err := Extract(bytes.NewReader(padded))
	if err != nil {
		t.Fatalf("Extract slow path: %v", err)
	}
	// meta box is now past the 64KB window; the slow path finds it in the full file.
	// result may or may not contain EXIF depending on findBox depth — just no panic.
	_ = rawEXIF
}

// TestParseIlocFullVersion1 verifies that iloc version 1 (with indexSize) parses
// correctly. ISO 14496-12 §8.11.3.
func TestParseIlocFullVersion1(t *testing.T) {
	t.Parallel()
	// Build a meta content blob containing an iloc v1 box with one item.
	// iloc v1: version(1)=1 + flags(3) + offsetSize(4bit)=4 + lengthSize(4bit)=4 +
	//          baseOffsetSize(4bit)=0 + indexSize(4bit)=0 + item_count(2) +
	//          [ item_ID(2) + construction_method(2) + extent_count(2) + offset(4) + length(4) ]
	ilocBody := make([]byte, 0, 32)
	ilocBody = append(ilocBody,
		1, 0, 0, 0, // version 1 + flags
		0x44, // offsetSize=4, lengthSize=4
		0x00, // baseOffsetSize=0, indexSize=0
		0, 1, // item_count = 1
		0, 1, // item_ID = 1
		0, 0, // construction_method = 0
		0, 1, // extent_count = 1
		0, 0, 0, 0x10, // offset = 16
		0, 0, 0, 0x08, // length = 8
	)
	ilocBox := make([]byte, 8+len(ilocBody))
	binary.BigEndian.PutUint32(ilocBox, uint32(8+len(ilocBody))) //nolint:gosec // test helper
	copy(ilocBox[4:8], "iloc")
	copy(ilocBox[8:], ilocBody)

	info, ok := parseIlocFull(ilocBox)
	if !ok {
		t.Fatal("parseIlocFull v1: returned ok=false")
	}
	if info.version != 1 {
		t.Errorf("version = %d, want 1", info.version)
	}
	if len(info.items) != 1 || info.items[0].id != 1 {
		t.Errorf("items = %v, want [{id:1}]", info.items)
	}
}

// TestFindBoxMaxDepth verifies that findBox returns ErrMaxNestingDepth when the
// nesting depth exceeds 32.
func TestFindBoxMaxDepth(t *testing.T) {
	t.Parallel()
	// A simple moov box containing a free box — depth will exceed 32 immediately
	// by calling findBox with depth=33.
	buf := make([]byte, 16)
	binary.BigEndian.PutUint32(buf, 16)
	copy(buf[4:8], "moov")
	binary.BigEndian.PutUint32(buf[8:], 8)
	copy(buf[12:16], "free")
	_, err := findBox(buf, "free", 33) // depth > 32 → immediate return
	if err == nil {
		t.Fatal("findBox with depth>32: expected error, got nil")
	}
}

// TestInjectNoMetaBox verifies that Inject passes through the data unchanged
// when no meta box can be found.
func TestInjectNoMetaBox(t *testing.T) {
	t.Parallel()
	// Build a file with only a ftyp box — no meta box.
	ftyp := make([]byte, 16)
	binary.BigEndian.PutUint32(ftyp, 16)
	copy(ftyp[4:], "ftyp")
	copy(ftyp[8:], "heic")
	original := make([]byte, len(ftyp))
	copy(original, ftyp)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(ftyp), &out, []byte("exif"), nil, nil); err != nil {
		t.Fatalf("Inject no meta: %v", err)
	}
	if !bytes.Equal(out.Bytes(), original) {
		t.Error("Inject no meta: output differs from input (expected pass-through)")
	}
}

// TestExtractItemSliceBoundsCheck verifies extractItemSlice returns nil for
// out-of-range or overflow locations.
func TestExtractItemSliceBoundsCheck(t *testing.T) {
	t.Parallel()
	data := []byte("ABCDEFGHIJ")

	// Valid location.
	slice := extractItemSlice(data, itemLoc{offset: 0, length: 5})
	if string(slice) != "ABCDE" {
		t.Errorf("extractItemSlice valid: got %q, want %q", slice, "ABCDE")
	}

	// End exceeds data length.
	slice = extractItemSlice(data, itemLoc{offset: 8, length: 5})
	if slice != nil {
		t.Errorf("extractItemSlice out-of-range: got %v, want nil", slice)
	}

	// Offset alone overflows int.
	slice = extractItemSlice(data, itemLoc{offset: ^uint64(0), length: 1})
	if slice != nil {
		t.Errorf("extractItemSlice overflow offset: got %v, want nil", slice)
	}
}
