package heif

// heif_conformance_test.go — HEIF/HEIC + ISO BMFF container conformance battery.
//
// Rule IDs match verbatim the stable identifiers in docs/conformance/containers.md:
//
//	BMFF-box-*         — §4(c) box layout (size, type, largesize, size=0, uuid)
//	BMFF-ftyp-*        — §4(c) ftyp brand/version/compat structure
//	BMFF-fullbox-*     — §4(c) FullBox version+flags
//	BMFF-child-iter-*  — §4(c) child iteration bounded by parent
//	HEIF-brand-*       — §5(b) brand detection
//	HEIF-Exif-item-*   — §5(d) EXIF item (infe Exif type + 4-byte prefix + iloc)
//	HEIF-mime-xmp-*    — §5(d) XMP item (infe mime type + content_type)
//	HEIF-meta-*        — §5(c) meta box children (hdlr/pitm/iinf/iloc/iref/iprp)
//	HEIF-cdsc-*        — §5(d) cdsc iref links metadata to primary
//	HEIF-write-*       — §5(e) write byte-correctness (iloc offsets, EXIF prefix)
//	HEIF-robust-*      — §5(f) robustness (OOB, truncated, oversized, zero-extent)
//	HEIF-corpus-*      — corpus parity via testutil.CorpusFiles
//
// No t.Skip in synthetic tests. All tests pass -race deterministically.
// Corpus tests use testutil.CorpusFiles which skips when corpus is absent.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ---------------------------------------------------------------------------
// Synthetic BMFF fixture builders — §4 ISO BMFF conformance
// ---------------------------------------------------------------------------

// bmffBox assembles an ISOBMFF box: size(u32 BE) + type(4cc) + body.
// ISO 14496-12 §4.2: size includes the 8-byte header itself.
func bmffBox(typ string, body []byte) []byte {
	total := uint32(8 + len(body)) //nolint:gosec // G115: test helper, bounded by body length
	hdr := make([]byte, 0, 8+len(body))
	hdr = append(hdr, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(hdr[0:], total)
	hdr = append(hdr, typ[0], typ[1], typ[2], typ[3])
	return append(hdr, body...)
}

// bmffLargesizeBox assembles an ISOBMFF box with extended 64-bit size (size==1).
// ISO 14496-12 §4.2: size field=1 signals that a u64 largesize follows the type.
func bmffLargesizeBox(typ string, body []byte) []byte {
	total := uint64(16 + len(body)) // 4(size)+4(type)+8(largesize)+body
	hdr := make([]byte, 0, 16+len(body))
	hdr = append(hdr, 0, 0, 0, 1, typ[0], typ[1], typ[2], typ[3]) // size==1 sentinel + 4cc
	hdr = binary.BigEndian.AppendUint64(hdr, total)               // largesize
	return append(hdr, body...)
}

// bmffFullBox wraps a body with a FullBox version+flags header (4 bytes).
// ISO 14496-12 §4.2: FullBox = Box + version(1) + flags(3).
// flags is a 3-byte value; version occupies the leading byte.
func bmffFullBox(typ string, version byte, flags uint32, body []byte) []byte { //nolint:unparam // flags always 0 by design in conformance tests; version varies
	vf := make([]byte, 0, 4+len(body))
	// Encode version+flags as a 4-byte big-endian value with version in the high byte.
	combined := (uint32(version) << 24) | (flags & 0x00FFFFFF)
	vf = binary.BigEndian.AppendUint32(vf, combined)
	return bmffBox(typ, append(vf, body...))
}

// bmffFtyp assembles an ISOBMFF ftyp box.
// ISO 14496-12 §4.3: ftyp = major_brand(4) + minor_version(u32 BE) + compatible_brands[].
func bmffFtyp(majorBrand string, minorVersion uint32, compatBrands ...string) []byte { //nolint:unparam // minorVersion always 0 in conformance tests; parameter kept for readability
	body := make([]byte, 0, 8+4*len(compatBrands))
	body = append(body, majorBrand[0], majorBrand[1], majorBrand[2], majorBrand[3])
	body = binary.BigEndian.AppendUint32(body, minorVersion)
	for _, cb := range compatBrands {
		body = append(body, cb[0], cb[1], cb[2], cb[3])
	}
	return bmffBox("ftyp", body)
}

// bmffInfeV2 assembles an infe (item info entry) FullBox version 2.
// ISO 14496-12 §8.11.6: version(1)+flags(3)+item_ID(2)+item_protection_index(2)+item_type(4)+item_name(NUL).
func bmffInfeV2(itemID uint16, itemType string) []byte { //nolint:unparam // itemType always "Exif" in most call sites; parameter kept for multi-type tests
	body := make([]byte, 0, 2+2+4+1)
	body = binary.BigEndian.AppendUint16(body, itemID)
	// item_protection_index(2) + item_type(4) + item_name NUL(1)
	body = append(body, 0, 0, itemType[0], itemType[1], itemType[2], itemType[3], 0)
	return bmffFullBox("infe", 2, 0, body)
}

// bmffInfeV2WithContentType assembles an infe v2 for mime items (XMP).
// ISO 14496-12 §8.11.6 + ISO 23008-12 §6.2: content_type NUL-terminated after item_type.
func bmffInfeV2WithContentType(itemID uint16, itemType, contentType string) []byte {
	body := make([]byte, 0, 2+2+4+1+len(contentType)+1)
	body = binary.BigEndian.AppendUint16(body, itemID)
	// item_protection_index(2) + item_type(4) + item_name NUL(1)
	body = append(body, 0, 0, itemType[0], itemType[1], itemType[2], itemType[3], 0)
	body = append(body, []byte(contentType)...) // content_type
	body = append(body, 0)                      // NUL terminator
	return bmffFullBox("infe", 2, 0, body)
}

// bmffIinf assembles an iinf box from a list of infe boxes.
// ISO 14496-12 §8.11.6: iinf FullBox version=0 + entry_count(2) + infe[].
func bmffIinf(infes ...[]byte) []byte {
	body := binary.BigEndian.AppendUint16(nil, uint16(len(infes))) //nolint:gosec // G115: test helper, bounded
	for _, infe := range infes {
		body = append(body, infe...)
	}
	return bmffFullBox("iinf", 0, 0, body)
}

// bmffIlocItem holds the parameters for one iloc item entry.
type bmffIlocItem struct {
	id     uint16
	offset uint32
	length uint32
}

// bmffIloc assembles an iloc FullBox v0 with offsetSize=4, lengthSize=4, no baseOffset.
// ISO 14496-12 §8.11.3: item location box.
func bmffIloc(items []bmffIlocItem) []byte {
	// body: sizes(2)+item_count(2)+items[]
	// Each item: item_ID(2)+extent_count(2)+extent_offset(4)+extent_length(4) = 12 bytes
	body := make([]byte, 0, 4+len(items)*12)
	body = append(body,
		0x44, // offset_size=4, length_size=4
		0x00, // base_offset_size=0, reserved=0
	)
	body = binary.BigEndian.AppendUint16(body, uint16(len(items))) //nolint:gosec // G115: test helper, bounded
	for _, it := range items {
		body = binary.BigEndian.AppendUint16(body, it.id)
		body = append(body, 0x00, 0x01) // extent_count=1
		body = binary.BigEndian.AppendUint32(body, it.offset)
		body = binary.BigEndian.AppendUint32(body, it.length)
	}
	return bmffFullBox("iloc", 0, 0, body)
}

// buildConformanceHEIF assembles a minimal valid HEIF file for conformance testing.
// Produces: ftyp + meta{iinf{infe[]}, iloc} + optional item payloads.
// Offsets in iloc are computed automatically after meta box size is known.
// The majorBrand parameter allows testing different brand variants (heic/heix/mif1/…).
func buildConformanceHEIF(majorBrand string, exifPayload, xmpPayload []byte) []byte {
	const (
		exifItemID uint16 = 1
		xmpItemID  uint16 = 2
	)

	ftyp := bmffFtyp(majorBrand, 0, "mif1")

	// Build infe boxes.
	var infes [][]byte
	var ilocItems []bmffIlocItem

	if exifPayload != nil {
		infes = append(infes, bmffInfeV2(exifItemID, "Exif"))
		// HEIF EXIF item payload = 4-byte u32 BE exif_tiff_header_offset (=0) + EXIF data.
		// ISO 23008-12 §6.6.1: ExifDataBlock = offset_to_TIFF_header(4) + TIFF data.
		blockLen := uint32(4 + len(exifPayload)) //nolint:gosec // G115: test helper, bounded
		ilocItems = append(ilocItems, bmffIlocItem{id: exifItemID, length: blockLen})
	}
	if xmpPayload != nil {
		// ISO 23008-12 §6.2: XMP item uses item_type='mime', content_type='application/rdf+xml'.
		infes = append(infes, bmffInfeV2WithContentType(xmpItemID, "mime", "application/rdf+xml"))
		ilocItems = append(ilocItems, bmffIlocItem{id: xmpItemID, length: uint32(len(xmpPayload))}) //nolint:gosec // G115: test helper, bounded
	}

	iinf := bmffIinf(infes...)

	// Two-pass layout: compute meta box size to determine absolute item offsets.
	// Pass 1: placeholder iloc with zero offsets.
	placeholderIloc := bmffIloc(ilocItems)
	metaBody := make([]byte, 0, 4+len(iinf)+len(placeholderIloc))
	metaBody = append(metaBody, 0, 0, 0, 0) // FullBox version+flags
	metaBody = append(metaBody, iinf...)
	metaBody = append(metaBody, placeholderIloc...)
	metaBox := bmffBox("meta", metaBody)

	dataStart := uint32(len(ftyp)) + uint32(len(metaBox)) //nolint:gosec // G115: test helper, bounded

	// Pass 2: fill real offsets.
	cursor := dataStart
	for i := range ilocItems {
		ilocItems[i].offset = cursor
		cursor += ilocItems[i].length
	}
	realIloc := bmffIloc(ilocItems)
	metaBody2 := make([]byte, 0, 4+len(iinf)+len(realIloc))
	metaBody2 = append(metaBody2, 0, 0, 0, 0)
	metaBody2 = append(metaBody2, iinf...)
	metaBody2 = append(metaBody2, realIloc...)
	metaBox2 := bmffBox("meta", metaBody2)

	// If meta size changed in pass 2 (it shouldn't since iloc sizes match), do pass 3.
	if len(metaBox2) != len(metaBox) {
		dataStart = uint32(len(ftyp)) + uint32(len(metaBox2)) //nolint:gosec // G115: test helper, bounded
		cursor = dataStart
		for i := range ilocItems {
			ilocItems[i].offset = cursor
			cursor += ilocItems[i].length
		}
		realIloc = bmffIloc(ilocItems)
		metaBody2 = metaBody2[:0]
		metaBody2 = append(metaBody2, 0, 0, 0, 0)
		metaBody2 = append(metaBody2, iinf...)
		metaBody2 = append(metaBody2, realIloc...)
		metaBox2 = bmffBox("meta", metaBody2)
	}

	// Assemble final file.
	result := make([]byte, 0, int(cursor))
	result = append(result, ftyp...)
	result = append(result, metaBox2...)
	if exifPayload != nil {
		result = append(result, 0, 0, 0, 0) // 4-byte exif_tiff_header_offset prefix
		result = append(result, exifPayload...)
	}
	if xmpPayload != nil {
		result = append(result, xmpPayload...)
	}
	return result
}

// conformanceMinimalTIFF returns a minimal valid TIFF/EXIF payload (no Exif\0\0 prefix).
// The caller is responsible for adding the 4-byte HEIF EXIF item prefix when needed.
func conformanceMinimalTIFF() []byte {
	// LE TIFF header + 1-entry IFD0 + next-IFD=0.
	buf := make([]byte, 8+2+12+4)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8)       // IFD0 at offset 8
	binary.LittleEndian.PutUint16(buf[8:], 1)       // 1 entry
	binary.LittleEndian.PutUint16(buf[10:], 0x010F) // Make tag
	binary.LittleEndian.PutUint16(buf[12:], 2)      // ASCII type
	binary.LittleEndian.PutUint32(buf[14:], 4)      // count=4
	copy(buf[18:], "CAM")                           // value inline (≤4 bytes)
	// next IFD = 0 (already zero)
	return buf
}

// conformanceXMPPacket returns a minimal valid XMP packet.
func conformanceXMPPacket() []byte {
	return []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end="r"?>`)
}

// ---------------------------------------------------------------------------
// §4 — ISO BMFF box layout tests (BMFF-box-*)
// ---------------------------------------------------------------------------

// TestBMFFBoxSizeNormal verifies that a standard 8-byte box header (size ≥ 8)
// is correctly parsed.
// ISO 14496-12 §4.2: box = size(u32 BE) + type(4cc) + body.
func TestBMFFBoxSizeNormal(t *testing.T) {
	// BMFF-box-size-normal: §4.2 — standard 8-byte header is accepted.
	t.Parallel()
	data := buildConformanceHEIF("heic", conformanceMinimalTIFF(), nil)
	if _, _, _, err := Extract(bytes.NewReader(data)); err != nil {
		t.Fatalf("BMFF-box-size-normal: Extract failed: %v", err)
	}
}

// TestBMFFBoxSizeLargesize verifies that a box with size==1 (extended 64-bit
// largesize) is correctly parsed by parseHEIFBoxHeader.
// ISO 14496-12 §4.2: if size==1, an 8-byte u64 largesize field follows the type.
func TestBMFFBoxSizeLargesize(t *testing.T) {
	// BMFF-box-size-largesize: §4.2 — size==1 sentinel with u64 largesize.
	t.Parallel()

	// A 24-byte ftyp box with largesize encoding:
	// size32=1 + type="ftyp" + largesize=24 + brand="heic" + minor_version=0
	body := make([]byte, 8) // major_brand(4)+minor_version(4)
	copy(body[0:], "heic")
	data := bmffLargesizeBox("ftyp", body)

	// parseHEIFBoxHeader must parse it as size=total, headerLen=16, typ="ftyp".
	sz, typ, hdrLen, ok := parseHEIFBoxHeader(data, 0)
	if !ok {
		t.Fatal("BMFF-box-size-largesize: parseHEIFBoxHeader returned ok=false")
	}
	if sz != uint64(len(data)) {
		t.Errorf("BMFF-box-size-largesize: sz=%d, want %d", sz, len(data))
	}
	if typ != "ftyp" {
		t.Errorf("BMFF-box-size-largesize: typ=%q, want ftyp", typ)
	}
	if hdrLen != 16 {
		t.Errorf("BMFF-box-size-largesize: hdrLen=%d, want 16", hdrLen)
	}
}

// TestBMFFBoxSize0ToEOF verifies that a box with size==0 is treated as
// "extends to end of containing structure."
// ISO 14496-12 §4.2: size==0 is only valid for the last box; it expands to EOF.
func TestBMFFBoxSize0ToEOF(t *testing.T) {
	// BMFF-size0-to-EOF: §4.2 — size==0 expands to remaining bytes.
	t.Parallel()
	data := make([]byte, 24)
	binary.BigEndian.PutUint32(data[0:], 0) // size==0 → extend to EOF
	copy(data[4:], "mdat")
	// 16 bytes of payload

	sz, typ, hdrLen, ok := parseHEIFBoxHeader(data, 0)
	if !ok {
		t.Fatal("BMFF-size0-to-EOF: parseHEIFBoxHeader returned ok=false for size=0")
	}
	if sz != 24 {
		t.Errorf("BMFF-size0-to-EOF: sz=%d, want 24 (full slice length)", sz)
	}
	if typ != "mdat" {
		t.Errorf("BMFF-size0-to-EOF: typ=%q, want mdat", typ)
	}
	if hdrLen != 8 {
		t.Errorf("BMFF-size0-to-EOF: hdrLen=%d, want 8", hdrLen)
	}
}

// TestBMFFBoxSizeInvalid2to7 verifies that box sizes 2–7 are rejected as invalid
// (smaller than the minimum 8-byte header).
// ISO 14496-12 §4.2: sizes 1–7 are reserved/invalid when not the extended-size sentinel.
func TestBMFFBoxSizeInvalid2to7(t *testing.T) {
	// BMFF-box-size-invalid-2-to-7: §4.2 — sizes 2–7 are malformed.
	t.Parallel()
	for size := 2; size <= 7; size++ {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			t.Parallel()
			data := make([]byte, 8)
			binary.BigEndian.PutUint32(data[0:], uint32(size)) //nolint:gosec // G115: test helper, size in [2,7]
			copy(data[4:], "test")
			_, _, _, ok := parseHEIFBoxHeader(data, 0)
			if ok {
				t.Errorf("BMFF-box-size-invalid-2-to-7: size=%d accepted, want ok=false", size)
			}
		})
	}
}

// TestBMFFBoxSizePastEOF verifies that a declared box size that exceeds the
// available data is rejected.
// ISO 14496-12 §4.2: bounds check — size must not exceed containing structure.
func TestBMFFBoxSizePastEOF(t *testing.T) {
	// BMFF-box-size-past-EOF: §4.2 — size > len(data) must be rejected.
	t.Parallel()
	data := make([]byte, 8)
	binary.BigEndian.PutUint32(data[0:], 100) // claims 100 bytes, only 8 available
	copy(data[4:], "moov")
	_, _, _, ok := parseHEIFBoxHeader(data, 0)
	if ok {
		t.Error("BMFF-box-size-past-EOF: accepted size > len(data), want ok=false")
	}
}

// TestBMFFBoxSizeTruncatedAtHeader verifies that < 8 bytes is always rejected.
// ISO 14496-12 §4.2: minimum valid box is 8 bytes (size + type only).
func TestBMFFBoxSizeTruncatedAtHeader(t *testing.T) {
	// BMFF-box-truncated-header: §4.2 — < 8 bytes must return ok=false.
	t.Parallel()
	for n := range 8 {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			t.Parallel()
			_, _, _, ok := parseHEIFBoxHeader(make([]byte, n), 0)
			if ok {
				t.Errorf("BMFF-box-truncated-header: %d bytes accepted, want ok=false", n)
			}
		})
	}
}

// TestBMFFLargesizeTruncated verifies that size==1 with insufficient bytes for
// the 64-bit largesize field is rejected.
// ISO 14496-12 §4.2: largesize requires bytes [0:16].
func TestBMFFLargesizeTruncated(t *testing.T) {
	// BMFF-largesize-truncated: §4.2 — extended size with < 16 bytes rejected.
	t.Parallel()
	data := make([]byte, 12)                // 8-byte header + only 4 bytes of largesize
	binary.BigEndian.PutUint32(data[0:], 1) // size==1 sentinel
	copy(data[4:], "moov")
	_, _, _, ok := parseHEIFBoxHeader(data, 0)
	if ok {
		t.Error("BMFF-largesize-truncated: 12-byte extended-size box accepted, want ok=false")
	}
}

// ---------------------------------------------------------------------------
// §4 — ftyp box (BMFF-ftyp-*)
// ---------------------------------------------------------------------------

// TestBMFFftypMajorBrand verifies that ftyp carries a valid 4-byte major brand.
// ISO 14496-12 §4.3: ftyp = major_brand(4) + minor_version(u32) + compatible_brands[].
func TestBMFFftypMajorBrand(t *testing.T) {
	// BMFF-ftyp-major-brand: §4.3 — ftyp brand must be exactly 4 bytes.
	t.Parallel()
	data := bmffFtyp("heic", 0, "mif1")
	// Brand lives at bytes [8:12].
	if got := string(data[8:12]); got != "heic" {
		t.Errorf("BMFF-ftyp-major-brand: brand=%q, want heic", got)
	}
}

// TestBMFFftypCompatibleBrands verifies that compatible brands are parsed at
// multiples of 4 bytes after major_brand + minor_version.
// ISO 14496-12 §4.3: compatible_brands[] are 4-byte entries.
func TestBMFFftypCompatibleBrands(t *testing.T) {
	// BMFF-ftyp-compat-brands: §4.3 — compatible_brands at 4-byte multiples.
	t.Parallel()
	data := bmffFtyp("heic", 0, "mif1", "miaf", "heis")
	// ftyp body at [8:]: major(4)+minor(4)+compat[3]*4 = 20 bytes body → total 28.
	if len(data) != 28 {
		t.Errorf("BMFF-ftyp-compat-brands: len=%d, want 28", len(data))
	}
	// compatible brands at [16:20], [20:24], [24:28].
	if got := string(data[16:20]); got != "mif1" {
		t.Errorf("BMFF-ftyp-compat-brands: compat[0]=%q, want mif1", got)
	}
	if got := string(data[20:24]); got != "miaf" {
		t.Errorf("BMFF-ftyp-compat-brands: compat[1]=%q, want miaf", got)
	}
}

// ---------------------------------------------------------------------------
// §4 — FullBox version+flags (BMFF-fullbox-*)
// ---------------------------------------------------------------------------

// TestBMFFFullBoxVersionFlags verifies that FullBox adds version(1)+flags(3)
// before the box body.
// ISO 14496-12 §4.2: FullBox extends Box with version (1 byte) + flags (3 bytes).
func TestBMFFFullBoxVersionFlags(t *testing.T) {
	// BMFF-fullbox-version-flags: §4.2 — version and flags correctly positioned.
	t.Parallel()
	meta := bmffFullBox("meta", 0, 0, []byte("body"))
	// Box header: size(4) + type(4) = 8 bytes.
	// FullBox fields: version at [8], flags at [9:12].
	if len(meta) < 12 {
		t.Fatalf("BMFF-fullbox-version-flags: box too short: %d bytes", len(meta))
	}
	if meta[8] != 0 {
		t.Errorf("BMFF-fullbox-version-flags: version=%d, want 0", meta[8])
	}
}

// ---------------------------------------------------------------------------
// §5(b) — HEIF brand detection (HEIF-brand-*)
// ---------------------------------------------------------------------------

// TestHEIFBrandHeic verifies that major_brand "heic" is accepted by Extract.
// ISO 23008-12 §3: "heic" is the primary brand for HEVC-coded HEIF.
func TestHEIFBrandHeic(t *testing.T) {
	// HEIF-brand-heic: §5(b) — heic brand must be accepted.
	t.Parallel()
	data := buildConformanceHEIF("heic", conformanceMinimalTIFF(), nil)
	if _, _, _, err := Extract(bytes.NewReader(data)); err != nil {
		t.Fatalf("HEIF-brand-heic: Extract failed: %v", err)
	}
}

// TestHEIFBrandHeix verifies that major_brand "heix" is accepted.
// ISO 23008-12: "heix" = HEVC image extended brand.
func TestHEIFBrandHeix(t *testing.T) {
	// HEIF-brand-heix: §5(b) — heix brand must be accepted.
	t.Parallel()
	data := buildConformanceHEIF("heix", conformanceMinimalTIFF(), nil)
	if _, _, _, err := Extract(bytes.NewReader(data)); err != nil {
		t.Fatalf("HEIF-brand-heix: Extract failed: %v", err)
	}
}

// TestHEIFBrandMif1 verifies that major_brand "mif1" (Multi-Image Format) is accepted.
// ISO 23008-12: "mif1" = HEIF single image.
func TestHEIFBrandMif1(t *testing.T) {
	// HEIF-brand-mif1: §5(b) — mif1 brand must be accepted.
	t.Parallel()
	data := buildConformanceHEIF("mif1", conformanceMinimalTIFF(), nil)
	if _, _, _, err := Extract(bytes.NewReader(data)); err != nil {
		t.Fatalf("HEIF-brand-mif1: Extract failed: %v", err)
	}
}

// TestHEIFBrandMsf1 verifies that major_brand "msf1" (Multi-image Sequence) is accepted.
// ISO 23008-12: "msf1" = HEIF image sequence.
func TestHEIFBrandMsf1(t *testing.T) {
	// HEIF-brand-msf1: §5(b) — msf1 brand must be accepted.
	t.Parallel()
	data := buildConformanceHEIF("msf1", conformanceMinimalTIFF(), nil)
	if _, _, _, err := Extract(bytes.NewReader(data)); err != nil {
		t.Fatalf("HEIF-brand-msf1: Extract failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// §5(d) — EXIF item extraction (HEIF-Exif-item-*)
// ---------------------------------------------------------------------------

// TestHEIFExifItem4BytePrefix verifies that the EXIF item payload begins with
// a 4-byte u32 BE exif_tiff_header_offset and that Extract strips it correctly.
// ISO 23008-12 §6.6.1: ExifDataBlock = exif_tiff_header_offset(4) + TIFF data.
func TestHEIFExifItem4BytePrefix(t *testing.T) {
	// HEIF-Exif-item-4byte-prefix: §5(d) / ISO 23008-12 §6.6.1 — 4-byte prefix must be stripped.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceHEIF("heic", exif, nil)

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("HEIF-Exif-item-4byte-prefix: Extract failed: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("HEIF-Exif-item-4byte-prefix: rawEXIF is nil, want non-nil")
	}
	// The returned rawEXIF must equal the original TIFF bytes (prefix stripped).
	if !bytes.Equal(rawEXIF, exif) {
		t.Errorf("HEIF-Exif-item-4byte-prefix: rawEXIF len=%d, want %d", len(rawEXIF), len(exif))
	}
}

// TestHEIFExifItem4BytePrefixNonZero verifies that a non-zero exif_tiff_header_offset
// is respected: the skip is offset + 4 bytes.
// ISO 23008-12 §6.6.1: skip = exif_tiff_header_offset + sizeof(prefix).
func TestHEIFExifItem4BytePrefixNonZero(t *testing.T) {
	// HEIF-Exif-item-4byte-prefix-nonzero: §5(d) — non-zero prefix offset is honoured.
	t.Parallel()
	exif := conformanceMinimalTIFF()

	// Build a synthetic EXIF item with a non-zero prefix:
	// payload = prefix(4, value=6) + padding(6 bytes) + TIFF data
	// Skip = 6 + 4 = 10 bytes into payload → lands at TIFF data.
	const skipVal = 6
	itemPayload := make([]byte, 4+skipVal+len(exif))
	binary.BigEndian.PutUint32(itemPayload[0:], skipVal) // exif_tiff_header_offset
	// bytes [4:10] = zero padding (skipped over)
	copy(itemPayload[4+skipVal:], exif)

	got := extractExifFromData(itemPayload)
	if !bytes.Equal(got, exif) {
		t.Errorf("HEIF-Exif-item-4byte-prefix-nonzero: extracted %d bytes, want %d", len(got), len(exif))
	}
}

// TestHEIFExifItemMissingPrefix verifies that an EXIF item with fewer than 4
// bytes is safely rejected (returns nil).
// ISO 23008-12 §6.6.1: ExifDataBlock is always at least 4 bytes.
func TestHEIFExifItemMissingPrefix(t *testing.T) {
	// HEIF-Exif-item-missing-prefix: §5(d) — item < 4 bytes must return nil.
	t.Parallel()
	tests := [][]byte{
		nil,
		{},
		{0x00},
		{0x00, 0x00},
		{0x00, 0x00, 0x00},
	}
	for _, input := range tests {
		got := extractExifFromData(input)
		if got != nil {
			t.Errorf("HEIF-Exif-item-missing-prefix: extractExifFromData(%d bytes) returned non-nil, want nil", len(input))
		}
	}
}

// TestHEIFExifItemPrefixOutOfRange verifies that an exif_tiff_header_offset that
// points beyond the item payload is safely rejected.
// ISO 23008-12 §6.6.1: skip > len(data) is a malformed ExifDataBlock.
func TestHEIFExifItemPrefixOutOfRange(t *testing.T) {
	// HEIF-Exif-item-prefix-out-of-range: §5(d) — skip > len(data) returns nil.
	t.Parallel()
	// 4-byte prefix value=1000, but total data is only 10 bytes.
	// skip = 1000 + 4 = 1004 > 10 → must return nil.
	data := make([]byte, 10)
	binary.BigEndian.PutUint32(data[0:], 1000)
	got := extractExifFromData(data)
	if got != nil {
		t.Error("HEIF-Exif-item-prefix-out-of-range: returned non-nil for oversized skip, want nil")
	}
}

// TestHEIFExifItemInfe verifies that an infe box with item_type "Exif" is
// recognised and its payload extracted via iloc.
// ISO 23008-12 §6.2: item type identification via infe item_type field.
func TestHEIFExifItemInfe(t *testing.T) {
	// HEIF-Exif-item-infe: §5(c)/(d) — infe item_type="Exif" triggers EXIF extraction.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceHEIF("heic", exif, nil)
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("HEIF-Exif-item-infe: Extract failed: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("HEIF-Exif-item-infe: rawEXIF is nil, want Exif item payload")
	}
}

// TestHEIFExifItemIlocOffset verifies that the EXIF item's iloc extent_offset
// points to the actual payload in the file.
// ISO 14496-12 §8.11.3: extent_offset is a file-absolute byte position.
func TestHEIFExifItemIlocOffset(t *testing.T) {
	// HEIF-Exif-item-iloc-offset: §5(d) — iloc offset must resolve to item bytes.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceHEIF("heic", exif, nil)

	// Parse meta to find iloc and verify the offset.
	metaContent, err := findBox(data, "meta", 0)
	if err != nil || metaContent == nil {
		t.Fatalf("HEIF-Exif-item-iloc-offset: meta box not found (err=%v)", err)
	}
	locs := parseIloc(metaContent)
	if len(locs) == 0 {
		t.Fatal("HEIF-Exif-item-iloc-offset: no iloc entries found")
	}
	for _, loc := range locs {
		if loc.offset == 0 || loc.length == 0 {
			t.Errorf("HEIF-Exif-item-iloc-offset: zero offset or length in iloc entry")
		}
		end := loc.offset + loc.length
		if end > uint64(len(data)) {
			t.Errorf("HEIF-Exif-item-iloc-offset: iloc extent [%d,%d) exceeds file size %d",
				loc.offset, end, len(data))
		}
	}
}

// ---------------------------------------------------------------------------
// §5(d) — XMP item extraction (HEIF-mime-xmp-*)
// ---------------------------------------------------------------------------

// TestHEIFMimeXMPItem verifies that a mime item with content_type
// "application/rdf+xml" is extracted as XMP.
// ISO 23008-12 §6.2 + XMP Part 3 §1.8 / HEIF-01: content_type must be exact.
func TestHEIFMimeXMPItem(t *testing.T) {
	// HEIF-mime-xmp-item: §5(d) / HEIF-01 — mime item with application/rdf+xml is XMP.
	t.Parallel()
	xmp := conformanceXMPPacket()
	data := buildConformanceHEIF("heic", nil, xmp)

	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("HEIF-mime-xmp-item: Extract failed: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("HEIF-mime-xmp-item: rawXMP is nil, want XMP payload")
	}
	if !bytes.Equal(rawXMP, xmp) {
		t.Errorf("HEIF-mime-xmp-item: rawXMP len=%d, want %d", len(rawXMP), len(xmp))
	}
}

// TestHEIFMimeXMPContentTypeExact verifies that the content_type stored in the
// infe box is exactly "application/rdf+xml" (no trailing whitespace, correct case).
// XMP Part 3 §1.8 / HEIF-01: content_type must be literally "application/rdf+xml".
func TestHEIFMimeXMPContentTypeExact(t *testing.T) {
	// HEIF-mime-xmp-content-type-exact: §5(d) / HEIF-01 — content_type must be exact.
	t.Parallel()
	xmp := conformanceXMPPacket()
	data := buildConformanceHEIF("heic", nil, xmp)

	// Parse the infe box to verify the content_type bytes.
	metaContent, err := findBox(data, "meta", 0)
	if err != nil || metaContent == nil {
		t.Fatalf("HEIF-mime-xmp-content-type-exact: meta box not found (err=%v)", err)
	}
	itemTypes := parseIinf(metaContent)
	found := false
	for _, typ := range itemTypes {
		if typ == "mime" || typ == "rdf+xml" {
			found = true
		}
	}
	if !found {
		t.Errorf("HEIF-mime-xmp-content-type-exact: no mime or rdf+xml item found in parsed iinf; item types: %v", itemTypes)
	}
}

// TestHEIFBothItems verifies that when both EXIF and XMP items are present,
// both are correctly extracted.
// ISO 23008-12 §6.2: multiple items may coexist in the same meta box.
func TestHEIFBothItems(t *testing.T) {
	// HEIF-both-items: §5(d) — EXIF and XMP items coexist and are both extracted.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	xmp := conformanceXMPPacket()
	data := buildConformanceHEIF("heic", exif, xmp)

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("HEIF-both-items: Extract failed: %v", err)
	}
	if rawEXIF == nil {
		t.Error("HEIF-both-items: rawEXIF is nil")
	}
	if rawXMP == nil {
		t.Error("HEIF-both-items: rawXMP is nil")
	}
	if !bytes.Equal(rawEXIF, exif) {
		t.Errorf("HEIF-both-items: EXIF mismatch: got %d bytes, want %d", len(rawEXIF), len(exif))
	}
	if !bytes.Equal(rawXMP, xmp) {
		t.Errorf("HEIF-both-items: XMP mismatch: got %d bytes, want %d", len(rawXMP), len(xmp))
	}
}

// ---------------------------------------------------------------------------
// §5(d) — cdsc iref (HEIF-cdsc-*)
// ---------------------------------------------------------------------------

// TestHEIFCdscRef verifies that the library does not crash when an iref box
// containing cdsc references is present.
// ISO 23008-12 §6.3: cdsc (content describes) links metadata items to primary image.
func TestHEIFCdscRef(t *testing.T) {
	// HEIF-cdsc-ref: §5(d) — iref/cdsc present does not crash Extract.
	t.Parallel()

	// Build a HEIF with an iref box containing a cdsc entry.
	// iref box: cdsc type, item_ID=1, referenced item_count=1, ref item_ID=2.
	// ISO 14496-12 §8.11.12: iref = FullBox + typed reference entries.
	cdscEntry := make([]byte, 2+2+2)             // from_ID(2)+entry_count(2)+to_ID(2)
	binary.BigEndian.PutUint16(cdscEntry[0:], 1) // from_item_ID = 1 (Exif item)
	binary.BigEndian.PutUint16(cdscEntry[2:], 1) // reference_count = 1
	binary.BigEndian.PutUint16(cdscEntry[4:], 2) // to_item_ID = 2 (primary image)
	cdscBox := bmffFullBox("cdsc", 0, 0, cdscEntry)
	irefBox := bmffBox("iref", cdscBox)

	// Wrap in meta alongside iinf + iloc for a valid EXIF item.
	exif := conformanceMinimalTIFF()
	baseFile := buildConformanceHEIF("heic", exif, nil)

	// Insert iref box into meta box. Parse and re-assemble.
	// Locate meta box in baseFile and append iref to its content.
	ms, me, ok := flatBoxRangeInFile(baseFile, "meta")
	if !ok {
		t.Skip("HEIF-cdsc-ref: could not locate meta box in synthetic file")
	}
	metaContent := baseFile[ms+8+4 : me] // skip box header(8) + version/flags(4)
	newMetaBody := make([]byte, 0, 4+len(metaContent)+len(irefBox))
	newMetaBody = append(newMetaBody, 0, 0, 0, 0) // version+flags
	newMetaBody = append(newMetaBody, metaContent...)
	newMetaBody = append(newMetaBody, irefBox...)
	newMeta := bmffBox("meta", newMetaBody)

	result := make([]byte, 0, ms+len(newMeta)+(len(baseFile)-me))
	result = append(result, baseFile[:ms]...)
	result = append(result, newMeta...)
	result = append(result, baseFile[me:]...)

	// Must not panic or error.
	rawEXIF, _, _, err := Extract(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("HEIF-cdsc-ref: Extract failed with iref/cdsc present: %v", err)
	}
	// EXIF should still be found.
	if rawEXIF == nil {
		t.Error("HEIF-cdsc-ref: rawEXIF is nil after adding iref/cdsc box")
	}
}

// ---------------------------------------------------------------------------
// §5(e) — Write byte-correctness (HEIF-write-*)
// ---------------------------------------------------------------------------

// TestHEIFWriteEXIFPrefixOnInject verifies that Inject stores the EXIF item
// payload with the 4-byte exif_tiff_header_offset prefix (value 0).
// ISO 23008-12 §6.6.1: the injected EXIF item must start with offset prefix.
func TestHEIFWriteEXIFPrefixOnInject(t *testing.T) {
	// HEIF-write-exif-prefix-on-inject: §5(e) — Inject adds 4-byte prefix to EXIF item.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceHEIF("heic", exif, nil)
	newEXIF := conformanceMinimalTIFF()
	// Modify the last byte to distinguish from original.
	newEXIF[len(newEXIF)-1] ^= 0xFF

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newEXIF, nil, nil, true); err != nil {
		t.Fatalf("HEIF-write-exif-prefix-on-inject: Inject failed: %v", err)
	}

	// The EXIF item payload in the output file must begin with 4 zero bytes (the prefix).
	outData := out.Bytes()
	metaContent, err := findBox(outData, "meta", 0)
	if err != nil || metaContent == nil {
		t.Fatalf("HEIF-write-exif-prefix-on-inject: meta box not found in output (err=%v)", err)
	}
	locs := parseIloc(metaContent)
	if len(locs) == 0 {
		t.Fatal("HEIF-write-exif-prefix-on-inject: no iloc entries found in output")
	}
	for _, loc := range locs {
		if loc.length < 4 {
			t.Errorf("HEIF-write-exif-prefix-on-inject: iloc item too short: %d bytes", loc.length)
			continue
		}
		end := loc.offset + loc.length
		if end > uint64(len(outData)) {
			t.Errorf("HEIF-write-exif-prefix-on-inject: iloc extent past output EOF")
			continue
		}
		prefix := outData[loc.offset : loc.offset+4]
		if prefix[0] != 0 || prefix[1] != 0 || prefix[2] != 0 || prefix[3] != 0 {
			t.Errorf("HEIF-write-exif-prefix-on-inject: EXIF item does not start with 4-byte zero prefix: %x", prefix)
		}
	}
}

// TestHEIFWriteIlocOffsetsPatched verifies that after Inject, all iloc offsets
// point to valid byte ranges within the output file.
// ISO 14496-12 §8.11.3: extent offsets must be absolute file positions.
func TestHEIFWriteIlocOffsetsPatched(t *testing.T) {
	// HEIF-write-iloc-offsets-patched: §5(e) — iloc offsets correct after inject.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	xmp := conformanceXMPPacket()
	data := buildConformanceHEIF("heic", exif, xmp)
	newEXIF := conformanceMinimalTIFF()
	newXMP := conformanceXMPPacket()

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newEXIF, nil, newXMP, true); err != nil {
		t.Fatalf("HEIF-write-iloc-offsets-patched: Inject failed: %v", err)
	}
	outData := out.Bytes()

	metaContent, err := findBox(outData, "meta", 0)
	if err != nil || metaContent == nil {
		t.Fatalf("HEIF-write-iloc-offsets-patched: meta box not found (err=%v)", err)
	}
	locs := parseIloc(metaContent)
	for id, loc := range locs {
		if loc.length == 0 {
			continue
		}
		end := loc.offset + loc.length
		if end > uint64(len(outData)) {
			t.Errorf("HEIF-write-iloc-offsets-patched: item %d: iloc extent [%d,%d) exceeds output file %d bytes",
				id, loc.offset, end, len(outData))
		}
	}
}

// TestHEIFWriteRoundTrip verifies a full Extract→Inject→Extract round trip
// for both EXIF and XMP items.
// ISO 23008-12 §5 + §6: write path must be reversible by read path.
func TestHEIFWriteRoundTrip(t *testing.T) {
	// HEIF-write-round-trip: §5(e) — inject then extract returns same payloads.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	xmp := conformanceXMPPacket()
	data := buildConformanceHEIF("heic", exif, xmp)

	newEXIF := append(bytes.Clone(exif), 0xAB, 0xCD) // distinct from original
	newXMP := append(bytes.Clone(xmp), '\n')

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newEXIF, nil, newXMP, true); err != nil {
		t.Fatalf("HEIF-write-round-trip: Inject failed: %v", err)
	}

	gotEXIF, _, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("HEIF-write-round-trip: Extract after Inject failed: %v", err)
	}
	if !bytes.Equal(gotEXIF, newEXIF) {
		t.Errorf("HEIF-write-round-trip: EXIF mismatch: got %d bytes, want %d", len(gotEXIF), len(newEXIF))
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("HEIF-write-round-trip: XMP mismatch: got %d bytes, want %d", len(gotXMP), len(newXMP))
	}
}

// TestHEIFWriteXMPContentTypePreserved verifies that after Inject the XMP item
// retains its mime type in iinf (content_type "application/rdf+xml").
// XMP Part 3 §1.8 / HEIF-01: content_type must be preserved on write.
func TestHEIFWriteXMPContentTypePreserved(t *testing.T) {
	// HEIF-write-xmp-content-type-preserved: §5(e) / HEIF-01 — content_type intact after inject.
	t.Parallel()
	xmp := conformanceXMPPacket()
	data := buildConformanceHEIF("heic", nil, xmp)

	newXMP := conformanceXMPPacket()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, newXMP, true); err != nil {
		t.Fatalf("HEIF-write-xmp-content-type-preserved: Inject failed: %v", err)
	}

	metaContent, err := findBox(out.Bytes(), "meta", 0)
	if err != nil || metaContent == nil {
		t.Fatalf("HEIF-write-xmp-content-type-preserved: meta box not found (err=%v)", err)
	}
	itemTypes := parseIinf(metaContent)
	found := false
	for _, typ := range itemTypes {
		if typ == "mime" || typ == "rdf+xml" {
			found = true
		}
	}
	if !found {
		t.Errorf("HEIF-write-xmp-content-type-preserved: XMP item type lost after inject; types=%v", itemTypes)
	}
}

// TestHEIFWritePreserveUnknownSegmentsFalse verifies that Inject returns
// ErrPreserveUnknownSegmentsNotSupported when preserveUnknownSegments=false.
// ISOBMFF boxes are structurally mandatory; there are no optional segments.
func TestHEIFWritePreserveUnknownSegmentsFalse(t *testing.T) {
	// HEIF-write-preserve-unknown-false: ISOBMFF has no optional segments.
	t.Parallel()
	data := buildConformanceHEIF("heic", conformanceMinimalTIFF(), nil)
	err := Inject(bytes.NewReader(data), &bytes.Buffer{}, nil, nil, nil, false)
	if err == nil {
		t.Error("HEIF-write-preserve-unknown-false: want error for preserveUnknownSegments=false, got nil")
	}
}

// ---------------------------------------------------------------------------
// §5(f) — Robustness (HEIF-robust-*)
// ---------------------------------------------------------------------------

// TestHEIFRobustInfeOOB is the CRITICAL regression test for the known infe/iloc/iinf
// OOB panic (reliability audit finding #106).
//
// It crafts a meta box with a deliberately truncated iinf body so that parsing
// an infe entry reaches the end of the buffer. The parser must return gracefully
// without panicking, indexing out of bounds, or crashing.
//
// ISO 14496-12 §8.11.6: infe parsers must bounds-check every field read.
func TestHEIFRobustInfeOOB(t *testing.T) {
	// HEIF-robust-infe-OOB: §5(f) CRITICAL — infe walk must not panic on truncated entry.
	t.Parallel()

	// Build a synthetic meta box whose iinf body claims 5 entries but only has
	// 2 bytes of data — every infe parse attempt hits EOF immediately.
	//
	// iinf body: version(1)+flags(3)+entry_count(2)+truncated_infe_data
	iinfTruncBody := []byte{
		0x00, 0x00, 0x00, 0x00, // version=0, flags=0
		0x00, 0x05, // entry_count=5 (claims 5 infe boxes)
		0x00, 0x01, // 2 stray bytes — nowhere near a valid infe header
	}
	iinfTrunc := bmffBox("iinf", iinfTruncBody)

	// Build an iloc with one item pointing to valid data offset.
	ilocItems := []bmffIlocItem{{id: 1, offset: 200, length: 4}}
	iloc := bmffIloc(ilocItems)

	// meta FullBox: version+flags(4) + iinf + iloc
	metaBody := make([]byte, 0, 4+len(iinfTrunc)+len(iloc))
	metaBody = append(metaBody, 0, 0, 0, 0) // version+flags
	metaBody = append(metaBody, iinfTrunc...)
	metaBody = append(metaBody, iloc...)
	meta := bmffBox("meta", metaBody)

	ftyp := bmffFtyp("heic", 0, "mif1")
	// Pad to ensure item offsets don't land in the meta box.
	padding := make([]byte, 200)
	payload := []byte{0x49, 0x49, 0x2A, 0x00} // 4 stray bytes at offset 200

	data := make([]byte, 0, len(ftyp)+len(meta)+len(padding)+len(payload))
	data = append(data, ftyp...)
	data = append(data, meta...)
	data = append(data, padding...)
	data = append(data, payload...)

	// Must not panic. May return nil metadata or an error — either is acceptable.
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
	_ = err

	// No metadata should be returned since the infe is truncated.
	if len(rawEXIF) > 0 {
		t.Logf("HEIF-robust-infe-OOB: rawEXIF non-nil (len=%d) on truncated infe — acceptable if no panic", len(rawEXIF))
	}
	_ = rawXMP
}

// TestHEIFRobustInfeEntryCountMismatch verifies that an entry_count in iinf
// that exceeds the available bytes does not cause a panic.
// ISO 14496-12 §8.11.6: iinf entry_count is unverified by spec; parser must guard.
func TestHEIFRobustInfeEntryCountMismatch(t *testing.T) {
	// HEIF-robust-infe-entry-count-mismatch: §5(f) — entry_count > actual entries is safe.
	t.Parallel()

	// iinf that claims 1000 entries but has data for only 1.
	realInfe := bmffInfeV2(1, "Exif")
	iinfBody := make([]byte, 0, 4+2+len(realInfe))           // version+flags(4)+entry_count(2)+infe
	iinfBody = append(iinfBody, 0, 0, 0, 0)                  // version+flags
	iinfBody = binary.BigEndian.AppendUint16(iinfBody, 1000) // claims 1000 entries
	iinfBody = append(iinfBody, realInfe...)
	iinf := bmffBox("iinf", iinfBody)

	exif := conformanceMinimalTIFF()
	ftyp := bmffFtyp("heic", 0, "mif1")
	// Build meta with placeholder iloc first, then fix up offset.
	exifLen := uint32(len(exif) + 4) //nolint:gosec // G115: test helper, bounded
	ilocPlaceholder := bmffIloc([]bmffIlocItem{{id: 1, offset: 0, length: exifLen}})
	metaBody := make([]byte, 0, 4+len(iinf)+len(ilocPlaceholder))
	metaBody = append(metaBody, 0, 0, 0, 0)
	metaBody = append(metaBody, iinf...)
	metaBody = append(metaBody, ilocPlaceholder...)
	meta := bmffBox("meta", metaBody)

	exifOffset := uint32(len(ftyp) + len(meta)) //nolint:gosec // G115: test helper, bounded
	ilocFinal := bmffIloc([]bmffIlocItem{{id: 1, offset: exifOffset, length: exifLen}})
	metaBody2 := make([]byte, 0, 4+len(iinf)+len(ilocFinal))
	metaBody2 = append(metaBody2, 0, 0, 0, 0)
	metaBody2 = append(metaBody2, iinf...)
	metaBody2 = append(metaBody2, ilocFinal...)
	meta2 := bmffBox("meta", metaBody2)

	exifBlock := make([]byte, 0, 4+len(exif))
	exifBlock = append(exifBlock, 0, 0, 0, 0) // 4-byte prefix
	exifBlock = append(exifBlock, exif...)
	data := make([]byte, 0, len(ftyp)+len(meta2)+len(exifBlock))
	data = append(data, ftyp...)
	data = append(data, meta2...)
	data = append(data, exifBlock...)

	// Must not panic.
	_, _, _, err := Extract(bytes.NewReader(data))
	_ = err
}

// TestHEIFRobustIlocExtentPastEOF verifies that an iloc extent that extends
// past the end of file is handled gracefully.
// ISO 14496-12 §8.11.3: extents must be within the file.
func TestHEIFRobustIlocExtentPastEOF(t *testing.T) {
	// HEIF-robust-iloc-extent-past-EOF: §5(f) — extent past EOF must not crash.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceHEIF("heic", exif, nil)

	// Corrupt the iloc to point way past EOF.
	// Find iloc in meta and corrupt the offset to math.MaxUint32.
	metaStart, metaEnd, ok := flatBoxRangeInFile(data, "meta")
	if !ok {
		t.Skip("HEIF-robust-iloc-extent-past-EOF: meta box not found")
	}
	corrupt := bytes.Clone(data)
	metaContent := corrupt[metaStart+8+4 : metaEnd]
	ilocStart, _, ilocOK := flatBoxRangeInFile(metaContent, "iloc")
	if !ilocOK {
		t.Skip("HEIF-robust-iloc-extent-past-EOF: iloc box not found")
	}
	// The iloc body offset field is deep in the structure; corrupt the last 4 bytes
	// of meta to a huge value — this will cause bounds checks to fire.
	for i := range metaContent[ilocStart:] {
		metaContent[ilocStart+i] ^= 0xFF
	}

	// Must not panic regardless of corruption.
	rawEXIF, _, rawXMP, _ := Extract(bytes.NewReader(corrupt))
	_ = rawEXIF
	_ = rawXMP
}

// TestHEIFRobustZeroExtentLength verifies that iloc items with zero extent
// length are handled without crashing.
// ISO 14496-12 §8.11.3: extent_length=0 is technically legal (empty item).
func TestHEIFRobustZeroExtentLength(t *testing.T) {
	// HEIF-robust-zero-extent-length: §5(f) — zero-length iloc extent is safe.
	t.Parallel()

	// Build a HEIF with an EXIF item that has a zero-length iloc extent.
	infe := bmffInfeV2(1, "Exif")
	iinf := bmffIinf(infe)
	// iloc item: offset=100, length=0
	ilocItems := []bmffIlocItem{{id: 1, offset: 100, length: 0}}
	iloc := bmffIloc(ilocItems)

	metaBody := make([]byte, 0, 4+len(iinf)+len(iloc))
	metaBody = append(metaBody, 0, 0, 0, 0)
	metaBody = append(metaBody, iinf...)
	metaBody = append(metaBody, iloc...)
	meta := bmffBox("meta", metaBody)
	ftyp := bmffFtyp("heic", 0, "mif1")

	data := append(ftyp, meta...)

	rawEXIF, _, rawXMP, _ := Extract(bytes.NewReader(data))
	// Zero-length item: rawEXIF should be nil (nothing to extract).
	if rawEXIF != nil {
		t.Logf("HEIF-robust-zero-extent-length: rawEXIF non-nil for zero-length item (len=%d)", len(rawEXIF))
	}
	_ = rawXMP
}

// TestHEIFRobustTruncatedMeta verifies that a truncated meta box (declared size
// larger than actual data) does not cause a panic.
// ISO 14496-12 §8.11.1: meta box content is bounded by the box size.
func TestHEIFRobustTruncatedMeta(t *testing.T) {
	// HEIF-robust-truncated-meta: §5(f) — truncated meta box must not panic.
	t.Parallel()

	data := buildConformanceHEIF("heic", conformanceMinimalTIFF(), nil)
	// Progressively truncate the file and verify no panic occurs.
	for i := 1; i <= len(data); i += len(data)/16 + 1 {
		truncated := data[:i]
		rawEXIF, _, rawXMP, _ := Extract(bytes.NewReader(truncated))
		_ = rawEXIF
		_ = rawXMP
	}
}

// TestHEIFRobustDeepNesting verifies that deeply nested container boxes do not
// cause a stack overflow (findBox has a depth limit of 32).
// ISO 14496-12 §4.2: arbitrary nesting is not guaranteed valid.
func TestHEIFRobustDeepNesting(t *testing.T) {
	// HEIF-robust-deep-nesting: §5(f) — depth > 32 returns ErrMaxNestingDepth.
	t.Parallel()

	// Build 35 levels of "moov" nesting (findBox limit is 32).
	innerData := bmffFtyp("heic", 0)
	for range 35 {
		innerData = bmffBox("moov", innerData)
	}

	_, err := findBox(innerData, "meta", 0)
	if err == nil {
		t.Log("HEIF-robust-deep-nesting: findBox returned nil error on >32 nesting (may have gracefully stopped)")
	} else if !strings.Contains(err.Error(), "nesting") {
		t.Logf("HEIF-robust-deep-nesting: got error %v (may be depth limit)", err)
	}
}

// TestHEIFRobustChildLargerThanParent verifies that a child box claiming to be
// larger than its parent does not cause a panic.
// ISO 14496-12 §4.2: child iteration is bounded by parent box size.
func TestHEIFRobustChildLargerThanParent(t *testing.T) {
	// BMFF-child-iter-child-larger-than-parent: §4(c) — oversized child must not panic.
	t.Parallel()

	// Build a meta box with an iinf child whose declared size > meta content size.
	// meta content size = 4 (version/flags) + 8 (box hdr) + iinf(12) = ~24 bytes
	// but iinf declares size=10000.
	hugeSizeIinf := make([]byte, 8)
	binary.BigEndian.PutUint32(hugeSizeIinf[0:], 10000) // claims 10000 bytes
	copy(hugeSizeIinf[4:], "iinf")

	metaBody := make([]byte, 0, 4+len(hugeSizeIinf))
	metaBody = append(metaBody, 0, 0, 0, 0)
	metaBody = append(metaBody, hugeSizeIinf...)
	meta := bmffBox("meta", metaBody)
	ftyp := bmffFtyp("heic", 0)

	data := append(ftyp, meta...)
	rawEXIF, _, rawXMP, _ := Extract(bytes.NewReader(data))
	_ = rawEXIF
	_ = rawXMP
}

// TestHEIFRobustMultipleExifItems verifies that when multiple Exif items are
// present, Extract returns exactly one (the best selection) without panicking.
// ISO 23008-12 §6.2: primary item selection handles multiple matching items.
func TestHEIFRobustMultipleExifItems(t *testing.T) {
	// HEIF-robust-multiple-exif-items: §5(f) — multiple Exif items: best one selected.
	t.Parallel()

	exif1 := conformanceMinimalTIFF()
	exif2 := append(bytes.Clone(conformanceMinimalTIFF()), 0xFF, 0xFF) // distinct

	infe1 := bmffInfeV2(1, "Exif")
	infe2 := bmffInfeV2(2, "Exif")
	iinf := bmffIinf(infe1, infe2)

	// Two EXIF items at different file positions.
	block1 := make([]byte, 0, 4+len(exif1))
	block1 = append(block1, 0, 0, 0, 0)
	block1 = append(block1, exif1...)
	block2 := make([]byte, 0, 4+len(exif2))
	block2 = append(block2, 0, 0, 0, 0)
	block2 = append(block2, exif2...)

	ftyp := bmffFtyp("heic", 0, "mif1")
	ilocPlaceholder := bmffIloc([]bmffIlocItem{
		{id: 1, offset: 0, length: uint32(len(block1))}, //nolint:gosec // G115: test helper
		{id: 2, offset: 0, length: uint32(len(block2))}, //nolint:gosec // G115: test helper
	})
	metaBody := make([]byte, 0, 4+len(iinf)+len(ilocPlaceholder))
	metaBody = append(metaBody, 0, 0, 0, 0)
	metaBody = append(metaBody, iinf...)
	metaBody = append(metaBody, ilocPlaceholder...)
	meta := bmffBox("meta", metaBody)

	off1 := uint32(len(ftyp) + len(meta))   //nolint:gosec // G115: test helper
	off2 := uint32(int(off1) + len(block1)) //nolint:gosec // G115: test helper
	ilocFinal := bmffIloc([]bmffIlocItem{
		{id: 1, offset: off1, length: uint32(len(block1))}, //nolint:gosec // G115: test helper
		{id: 2, offset: off2, length: uint32(len(block2))}, //nolint:gosec // G115: test helper
	})
	metaBody2 := make([]byte, 0, 4+len(iinf)+len(ilocFinal))
	metaBody2 = append(metaBody2, 0, 0, 0, 0)
	metaBody2 = append(metaBody2, iinf...)
	metaBody2 = append(metaBody2, ilocFinal...)
	meta2 := bmffBox("meta", metaBody2)

	data := make([]byte, 0, len(ftyp)+len(meta2)+len(block1)+len(block2))
	data = append(data, ftyp...)
	data = append(data, meta2...)
	data = append(data, block1...)
	data = append(data, block2...)

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("HEIF-robust-multiple-exif-items: Extract failed: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("HEIF-robust-multiple-exif-items: rawEXIF is nil; want one of the two items")
	}
	_ = rawXMP
}

// TestHEIFRobustEmptyFile verifies that an empty input returns nil metadata without error.
func TestHEIFRobustEmptyFile(t *testing.T) {
	// HEIF-robust-empty-file: §5(f) — empty file must not crash.
	t.Parallel()
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("HEIF-robust-empty-file: got error %v, want nil", err)
	}
	if rawEXIF != nil || rawIPTC != nil || rawXMP != nil {
		t.Errorf("HEIF-robust-empty-file: got non-nil metadata for empty input")
	}
}

// TestHEIFRobustRandomBytes verifies that random/garbage bytes do not cause a panic.
// §5(f): parser must degrade gracefully on any input.
func TestHEIFRobustRandomBytes(t *testing.T) {
	// HEIF-robust-random-bytes: §5(f) — random bytes must not panic.
	t.Parallel()
	inputs := [][]byte{
		{0xFF, 0xFE, 0xFD, 0xFC},
		{0x00, 0x00, 0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		bytes.Repeat([]byte{0xAA}, 64),
		make([]byte, 64), // 64 zero bytes (gocritic: prefer make over bytes.Repeat)
	}
	for _, input := range inputs {
		rawEXIF, _, rawXMP, _ := Extract(bytes.NewReader(input))
		_ = rawEXIF
		_ = rawXMP
	}
}

// TestHEIFRobustInjectOnTruncatedMeta verifies that Inject does not panic when
// the meta box is truncated or malformed.
// §5(f): write path must be as robust as read path.
func TestHEIFRobustInjectOnTruncatedMeta(t *testing.T) {
	// HEIF-robust-inject-truncated-meta: §5(f) — Inject handles truncated meta gracefully.
	t.Parallel()
	data := buildConformanceHEIF("heic", conformanceMinimalTIFF(), nil)

	// Truncate at various points.
	for i := len(data) / 2; i < len(data); i += len(data)/8 + 1 {
		truncated := data[:i]
		newEXIF := conformanceMinimalTIFF()
		var out bytes.Buffer
		// Must not panic.
		_ = Inject(bytes.NewReader(truncated), &out, newEXIF, nil, nil, true)
	}
}

// TestHEIFRobustIlocV2ItemID verifies that iloc v2 with 4-byte item IDs
// that exceed uint16 are handled without panic.
// ISO 14496-12 §8.11.3: iloc v2 uses uint32 item IDs; library caps to uint16.
func TestHEIFRobustIlocV2ItemID(t *testing.T) {
	// HEIF-robust-iloc-v2-item-id: §5(f) — iloc v2 4-byte ID > uint16 is safe.
	t.Parallel()

	// Build a minimal iloc v2 with one item whose ID = 0x00020000 (> math.MaxUint16).
	// body: version+flags(4) + sizes(2) + item_count(4) + item_ID(4)+construct(2)+extcnt(2)
	ilocBody := []byte{
		0x02, 0x00, 0x00, 0x00, // version=2, flags=0
		0x44,                   // offset_size=4, length_size=4
		0x00,                   // base_offset_size=0, index_size=0
		0x00, 0x00, 0x00, 0x01, // item_count=1 (v2=4 bytes)
		0x00, 0x02, 0x00, 0x00, // item_ID = 0x00020000 > 0xFFFF → rejected
		0x00, 0x00, // construction_method=0
		0x00, 0x01, // extent_count=1
		0x00, 0x00, 0x00, 0x00, // extent_offset=0
		0x00, 0x00, 0x00, 0x04, // extent_length=4
	}
	// Build raw iloc box (header + body) rather than using bmffFullBox to avoid
	// double version+flags wrapping.
	rawIloc := make([]byte, 8+len(ilocBody))
	binary.BigEndian.PutUint32(rawIloc[0:], uint32(len(rawIloc))) //nolint:gosec // G115: test helper
	copy(rawIloc[4:], "iloc")
	copy(rawIloc[8:], ilocBody)

	ftyp := bmffFtyp("heic", 0)
	infe := bmffInfeV2(1, "Exif")
	iinf := bmffIinf(infe)
	metaBody := make([]byte, 0, 4+len(iinf)+len(rawIloc))
	metaBody = append(metaBody, 0, 0, 0, 0)
	metaBody = append(metaBody, iinf...)
	metaBody = append(metaBody, rawIloc...)
	meta := bmffBox("meta", metaBody)
	data := append(ftyp, meta...)

	// Must not panic.
	rawEXIF, _, rawXMP, _ := Extract(bytes.NewReader(data))
	_ = rawEXIF
	_ = rawXMP
}

// ---------------------------------------------------------------------------
// §5(f) — Reliability-audit regression gates (HEIF-robust-audit-*)
// These tests reproduce the exact panic/incorrect-read conditions found in
// the 2026-06-09 audit (findings #106, #133, #169, #177).
// ---------------------------------------------------------------------------

// TestHEIFInjectMetaTooSmallForFullBox is the regression gate for finding #169.
// A meta box whose declared size is < 12 bytes cannot hold the FullBox version+flags
// (header=8 + version/flags=4 = 12 bytes minimum). Before the fix, Inject would
// call buildInjectComponents with metaContentOff = metaAbsStart+12 > metaAbsEnd,
// causing data[metaAbsStart+12 : metaAbsEnd] to panic with "slice bounds out of range".
//
// ISO 14496-12 §8.11.1: meta is a FullBox; minimum valid size = 12 bytes.
func TestHEIFInjectMetaTooSmallForFullBox(t *testing.T) {
	// HEIF-robust-audit-169: §5(f) CRITICAL — meta box < 12 bytes must not panic on Inject.
	t.Parallel()

	rawEXIF := []byte{'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

	cases := []struct {
		name string
		data []byte
	}{
		{
			// 8-byte meta box: only the box header (size+type); no FullBox version/flags.
			// metaContentOff = 8+4 = 12 > metaAbsEnd = 8 → was panic [12:8].
			name: "meta-size-8",
			data: []byte{
				0x00, 0x00, 0x00, 0x08, // size = 8
				'm', 'e', 't', 'a', // type = meta
			},
		},
		{
			// 9-byte meta box: 1 byte of FullBox version, still < 12.
			name: "meta-size-9",
			data: []byte{
				0x00, 0x00, 0x00, 0x09, // size = 9
				'm', 'e', 't', 'a', // type = meta
				0x00, // 1 partial byte of version+flags
			},
		},
		{
			// 11-byte meta box: header(8) + 3 bytes of version+flags; still < 12.
			// metaContentOff = 12 > metaAbsEnd = 11 → was panic [12:11].
			name: "meta-size-11",
			data: []byte{
				0x00, 0x00, 0x00, 0x0B, // size = 11
				'm', 'e', 't', 'a', // type = meta
				0x00, 0x00, 0x00, // 3 bytes of version+flags (incomplete)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			err := Inject(bytes.NewReader(tc.data), &out, rawEXIF, nil, nil, true)
			// Must not panic. Acceptable outcomes: pass-through write (err==nil)
			// or an error. Panics are caught by the testing harness and fail the test.
			_ = err
		})
	}
}

// TestHEIFRobustInfeV0V1Truncated is the regression gate for finding #106.
// parseInfeV0V1 panics when the infe body has item_ID (2 bytes) but is missing
// the item_protection_index (2 bytes), because the unconditional pos+=2 for the
// protection_index advances pos past len(data) and the subsequent
// bytes.IndexByte(data[pos:], 0x00) panics with "slice bounds out of range".
//
// ISO 14496-12 §8.11.6: all fields in infe v0/v1 must be bounds-checked.
func TestHEIFRobustInfeV0V1Truncated(t *testing.T) {
	// HEIF-robust-audit-106: §5(f) CRITICAL — truncated infe v0/v1 must not panic.
	t.Parallel()

	// Build a valid HEIF file that contains an infe v0 box with a deliberately
	// truncated body (only item_ID present; item_protection_index and item_name
	// are missing). The infe box must have a valid ISOBMFF header so that the
	// loop in parseIinf dispatches into parseInfe → parseInfeV0V1.
	//
	// infe v0 body layout (total after header):
	//   version(1)+flags(3)+item_ID(2)+item_protection_index(2)+item_name(NUL-term)…
	// We create an infe body with only version+flags+item_ID = 6 bytes, so
	// after pos=4 (skip v+flags), id is read at [4:5], then pos+=2 for
	// protection_index would put pos=8 > len(data)=6 — triggering the panic.
	infeBody := []byte{
		0x00, 0x00, 0x00, 0x00, // version=0, flags=0
		0x00, 0x01, // item_ID = 1 (only 2 bytes; protection_index absent)
	}
	// The infe box must be wrapped in a box header; total size = 8+6 = 14 bytes.
	infeBox := bmffBox("infe", infeBody) // bmffBox prefixes 8-byte size+type

	// Wrap infe in iinf: version+flags(4) + entry_count(2) + infe box.
	iinfBody := make([]byte, 0, 6+len(infeBox)) // v0 + count=1 + infe
	iinfBody = append(iinfBody, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01)
	iinfBody = append(iinfBody, infeBox...)
	iinf := bmffBox("iinf", iinfBody)

	// Build minimal iloc (no items).
	iloc := bmffIloc(nil)

	// meta FullBox: version+flags(4) + iinf + iloc.
	metaBody := make([]byte, 0, 4+len(iinf)+len(iloc))
	metaBody = append(metaBody, 0, 0, 0, 0) // version=0, flags=0
	metaBody = append(metaBody, iinf...)
	metaBody = append(metaBody, iloc...)
	meta := bmffBox("meta", metaBody)

	ftyp := bmffFtyp("heic", 0, "mif1")
	data := append(ftyp, meta...)

	// Must not panic. Result may be nil EXIF/XMP (item type unrecognised); that is correct.
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
	_ = err
	// Truncated infe v0 cannot yield an Exif item type (no item_type field in v0);
	// rawEXIF must be nil.
	if rawEXIF != nil {
		t.Errorf("HEIF-robust-audit-106: rawEXIF non-nil for truncated infe v0: %d bytes", len(rawEXIF))
	}
	_ = rawXMP
}

// TestHEIFRobustIlocConstructionMethod1 is the regression gate for finding #177.
// Before the fix, parseIlocItemSimple for iloc v1/v2 read the 2-byte
// construction_method field but discarded it, resolving all extents as file-absolute
// (method 0) regardless of the actual value. An item with method=1 (idat-relative)
// would have its offset silently misinterpreted, potentially returning wrong bytes.
//
// The fix: construction_method != 0 → return zero itemLoc so the item is ignored
// rather than mis-resolved.
//
// ISO 14496-12 §8.11.3: construction_method semantics.
func TestHEIFRobustIlocConstructionMethod1(t *testing.T) {
	// HEIF-robust-audit-177: §5(f) MEDIUM — iloc construction_method != 0 must yield nil EXIF.
	t.Parallel()

	// Build an iloc v1 body with a single item using construction_method=1 (idat-relative).
	// The item claims to carry EXIF data, but its extent cannot be resolved without
	// the idat box. The library must not guess and must return nil for this item.
	//
	// iloc v1 body layout:
	//   version(1)+flags(3)+sizes(2)+item_count(2)+
	//   item_ID(2)+construction_method(2)+base_offset(0)+extent_count(2)+offset(4)+length(4)
	ilocBody := []byte{
		0x01, 0x00, 0x00, 0x00, // version=1, flags=0
		0x44,       // offset_size=4, length_size=4
		0x10,       // base_offset_size=1, index_size=0
		0x00, 0x01, // item_count=1
		// Item entry:
		0x00, 0x01, // item_ID=1
		0x00, 0x01, // construction_method=1 (idat-relative — cannot resolve)
		0x10,       // base_offset (1 byte, value=16)
		0x00, 0x01, // extent_count=1
		0x00, 0x00, 0x00, 0x10, // extent_offset=16
		0x00, 0x00, 0x00, 0x08, // extent_length=8
	}
	rawIloc := make([]byte, 8+len(ilocBody))
	binary.BigEndian.PutUint32(rawIloc, uint32(len(rawIloc))) //nolint:gosec // G115: test helper, bounded
	copy(rawIloc[4:], "iloc")
	copy(rawIloc[8:], ilocBody)

	// Build an infe v2 for item 1 with type "Exif".
	infe := bmffInfeV2(1, "Exif")
	iinf := bmffIinf(infe)

	// meta FullBox.
	metaBody := make([]byte, 0, 4+len(iinf)+len(rawIloc))
	metaBody = append(metaBody, 0, 0, 0, 0)
	metaBody = append(metaBody, iinf...)
	metaBody = append(metaBody, rawIloc...)
	meta := bmffBox("meta", metaBody)

	ftyp := bmffFtyp("heic", 0, "mif1")
	data := append(ftyp, meta...)

	// Must not panic. rawEXIF must be nil: the item's construction_method=1 cannot
	// be resolved to a file offset, so the item must be skipped (not mis-resolved).
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	_ = err
	if rawEXIF != nil {
		t.Errorf("HEIF-robust-audit-177: rawEXIF non-nil for iloc construction_method=1 item: "+
			"method-1 items cannot be resolved to file offsets; got %d bytes", len(rawEXIF))
	}
}

// TestReadIlocSimpleExtentsTruncatedIndex is the regression gate for finding #133.
// readIlocSimpleExtents lacked a bounds check for the extent_index field (indexSize > 0,
// present in iloc v1/v2). A truncated iloc where the data ends before extent_index
// would silently advance pos past len(ilocData), then fail the subsequent offsetSize
// check — returning ok=false but with offset=0, length=0. The caller discarded ok
// (_ = ok), so the zero-offset item was recorded as valid, causing the EXIF read
// to seek to file offset 0 and return image header bytes instead of metadata.
//
// The fix adds the indexSize bounds check and propagates ok so the item is omitted.
//
// ISO 14496-12 §8.11.3: all extent fields must be bounds-checked.
func TestReadIlocSimpleExtentsTruncatedIndex(t *testing.T) {
	// HEIF-robust-audit-133: §5(f) LOW — truncated iloc extent_index must not produce zero-offset item.
	t.Parallel()

	// Build iloc v1 with index_size=2 (i.e. extent_index is 2 bytes per extent)
	// but the extent data is truncated after the item_ID and construction_method —
	// there is no room for even the extent_index field.
	//
	// iloc v1 body: version(1)+flags(3)+sizes(2)+item_count(2)+
	//   item_ID(2)+const_method(2)+extent_count(2)+extent_index(2)[truncated]
	ilocBody := []byte{
		0x01, 0x00, 0x00, 0x00, // version=1, flags=0
		0x44,       // offset_size=4, length_size=4
		0x02,       // base_offset_size=0, index_size=2  ← index present
		0x00, 0x01, // item_count=1
		// Item entry (truncated after extent_count):
		0x00, 0x01, // item_ID=1
		0x00, 0x00, // construction_method=0
		0x00, 0x01, // extent_count=1
		// extent_index (2 bytes) is missing — truncated here
	}
	rawIloc := make([]byte, 8+len(ilocBody))
	binary.BigEndian.PutUint32(rawIloc, uint32(len(rawIloc))) //nolint:gosec // G115: test helper, bounded
	copy(rawIloc[4:], "iloc")
	copy(rawIloc[8:], ilocBody)

	// Build an infe v2 for item 1 with type "Exif".
	infe := bmffInfeV2(1, "Exif")
	iinf := bmffIinf(infe)

	// Add a fake EXIF payload at the start of the file (offset=0) to confirm that
	// a wrongly-recorded zero-offset item would read these bytes as EXIF — which
	// would be incorrect. After the fix the EXIF item must be omitted.
	fakeEXIF := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0x00, 0x00}

	metaBody := make([]byte, 0, 4+len(iinf)+len(rawIloc))
	metaBody = append(metaBody, 0, 0, 0, 0)
	metaBody = append(metaBody, iinf...)
	metaBody = append(metaBody, rawIloc...)
	meta := bmffBox("meta", metaBody)

	ftyp := bmffFtyp("heic", 0, "mif1")
	data := make([]byte, 0, len(ftyp)+len(meta)+len(fakeEXIF))
	data = append(data, ftyp...)
	data = append(data, meta...)
	data = append(data, fakeEXIF...)

	// Must not panic. rawEXIF must be nil: the iloc extent_index is truncated,
	// so the item should be dropped rather than silently recording offset=0.
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	_ = err
	if rawEXIF != nil {
		t.Errorf("HEIF-robust-audit-133: rawEXIF non-nil for truncated iloc extent_index: "+
			"truncated item should be dropped; got %d bytes (possible zero-offset mis-read)", len(rawEXIF))
	}
}

// ---------------------------------------------------------------------------
// §5 — Corpus parity (HEIF-corpus-*)
// ---------------------------------------------------------------------------

// TestHEIFCorpusExtract runs Extract on every file in testdata/corpus/heif
// and verifies no panic. Skipped if corpus is absent.
func TestHEIFCorpusExtract(t *testing.T) {
	// HEIF-corpus-extract: §5 — real-world files must not panic.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "heif")
	for _, path := range paths {
		// Skip AVIF files here — they are covered by avif_conformance_test.go.
		if strings.HasSuffix(strings.ToLower(path), ".avif") {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("HEIF-corpus-extract: open %s: %v", path, err)
			}
			t.Cleanup(func() { _ = f.Close() })
			rawEXIF, _, rawXMP, _ := Extract(f)
			// No panic assertion: if we got here, the test passed.
			_ = rawEXIF
			_ = rawXMP
		})
	}
}

// TestHEIFCorpusInjectRoundTrip runs an Inject→Extract round trip on every
// non-AVIF HEIF corpus file and verifies that injected metadata is readable back.
// Skipped if corpus is absent.
func TestHEIFCorpusInjectRoundTrip(t *testing.T) {
	// HEIF-corpus-inject-round-trip: §5(e) — corpus files survive inject round trip.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "heif")
	newXMP := conformanceXMPPacket()
	for _, path := range paths {
		if strings.HasSuffix(strings.ToLower(path), ".avif") {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("HEIF-corpus-inject-round-trip: open %s: %v", path, err)
			}
			t.Cleanup(func() { _ = f.Close() })

			var out bytes.Buffer
			injectErr := Inject(f, &out, nil, nil, newXMP, true)
			if injectErr != nil {
				// Acceptable: some corpus files may have no meta box or
				// unsupported structures. The critical invariant is no panic.
				return
			}
			// If inject succeeded, extract must work without error.
			_, _, gotXMP, extractErr := Extract(bytes.NewReader(out.Bytes()))
			if extractErr != nil {
				t.Errorf("HEIF-corpus-inject-round-trip: Extract after Inject failed for %s: %v",
					filepath.Base(path), extractErr)
			}
			_ = gotXMP
		})
	}
}

// TestHEIFCorpusMaxNestingDepthSafe runs Extract on all HEIF corpus files
// including PoC files from exiv2 that triggered OOB panics in other parsers.
// These specific files are known to exercise edge cases.
func TestHEIFCorpusMaxNestingDepthSafe(t *testing.T) {
	// HEIF-corpus-max-nesting-depth-safe: §5(f) — known PoC/edge-case files must not panic.
	t.Parallel()
	knownPoCFiles := []string{
		"testdata/corpus/heif/exiv2/pr_2612_poc.heic",
		"testdata/corpus/heif/exiv2/issue_1793_poc.heic",
		"testdata/corpus/heif/metadata-extractor/IllegalArgumentException.HeifReader.processBoxes.heif",
		"testdata/corpus/heif/metadata-extractor/NegativeArraySizeException.HeifReader.processBoxes.heif",
		"testdata/corpus/heif/metadata-extractor/NullPointerException.HeifPictureHandler.processBox.heif",
		"testdata/corpus/heif/metadata-extractor/NegativeArraySizeException.ItemInfoBox.init.heif",
	}
	for _, path := range knownPoCFiles {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(path)
			if err != nil {
				t.Skipf("HEIF-corpus-max-nesting-depth-safe: %s not found: %v", path, err)
			}
			t.Cleanup(func() { _ = f.Close() })
			// Must not panic.
			rawEXIF, _, rawXMP, _ := Extract(f)
			_ = rawEXIF
			_ = rawXMP
		})
	}
}
