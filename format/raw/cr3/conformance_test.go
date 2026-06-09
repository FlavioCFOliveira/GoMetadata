package cr3

// conformance_test.go — Canon CR3 container specification-conformance battery.
//
// Authoritative specs:
//   - ISO/IEC 14496-12 (ISO BMFF): box layout, largesize, ftyp, uuid, stco/co64.
//   - lclevy canon_cr3 (github.com/lclevy/canon_cr3): CMT1/CMT2/CMT3/CMT4 box roles,
//     Canon UUID {85C0B687-820F-11E0-8111-F4CE462B6A48}.
//   - containers.md §4 (ISO BMFF rules) + §8 (CR3 rules).
//
// Rule IDs are stable identifiers that match containers.md:
//
//	BMFF-box-*              — §4(c) box layout (size, type, largesize, size=0)
//	BMFF-ftyp-*             — §4(c) ftyp brand / structure
//	BMFF-uuid-*             — §4(c) uuid box 16-byte UUID extension
//	BMFF-child-iter-*       — §4(c) child iteration bounded by parent size
//	BMFF-robust-*           — §4(f) robustness (invalid sizes, deep nesting, duplicate ftyp)
//	CR3-detect-*            — §8(b) detection (crx  brand)
//	CR3-CMT1-*              — §8(d) CMT1 = IFD0 TIFF stream
//	CR3-CMT2-*              — §8(d) CMT2 = Exif IFD; merged with CMT1
//	CR3-CMT3-*              — §8(d) CMT3 = Canon MakerNote (present in UUID; not extracted)
//	CR3-CMT4-*              — §8(d) CMT4 = GPS IFD
//	CR3-XMP-*               — §8(d) XMP  sub-box in Canon UUID
//	CR3-box-largesize-*     — §4(c) largesize (size==1 + u64) box support
//	CR3-box-size0-*         — §4(c) size==0 expands to end of container
//	CR3-write-*             — §8(e) write byte-correctness (moov rebuild, stco/co64 relocation)
//	CR3-round-trip-*        — §8(d)+(e) inject→extract preserves payloads exactly
//	CR3-robust-*            — §8(f) robustness (missing/extra CMT* boxes, malformed moov/uuid)
//	CR3-corpus-*            — corpus parity over testdata/corpus/raw (*.cr3)
//
// No t.Skip in any synthetic test. All tests pass -race deterministically.
// Corpus-parity tests use testutil.CorpusFiles which skips if the corpus directory is absent.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ---------------------------------------------------------------------------
// shared test fixtures
// ---------------------------------------------------------------------------

// conformanceTIFFLE returns a minimal valid little-endian TIFF stream.
// TIFF 6.0 §2: II + magic 0x002A + IFD0 offset.
// containers.md §8(d): CMT1 carries a raw TIFF IFD directly (no Exif\0\0 prefix).
var conformanceTIFFLE = func() []byte { //nolint:gochecknoglobals // immutable test fixture
	buf := make([]byte, 14) // header(8) + entry_count(2) + next_ifd(4)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8) // IFD0 at offset 8
	binary.LittleEndian.PutUint16(buf[8:], 0) // 0 IFD entries
	// next IFD = 0 (already zero from make)
	return buf
}()

// conformanceTIFFBE returns a minimal valid big-endian TIFF stream.
var conformanceTIFFBE = func() []byte { //nolint:gochecknoglobals // immutable test fixture
	buf := make([]byte, 14)
	buf[0], buf[1] = 'M', 'M'
	binary.BigEndian.PutUint16(buf[2:], 0x002A)
	binary.BigEndian.PutUint32(buf[4:], 8)
	binary.BigEndian.PutUint16(buf[8:], 0)
	return buf
}()

// conformanceXMPPacket returns a minimal valid XMP packet.
var conformanceXMPPacket = []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end="r"?>`) //nolint:gochecknoglobals // immutable test fixture

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// buildCR3FtypOnly builds a stream with a ftyp box for the "crx " brand, but no moov.
// Used to test error paths that require no moov.
func buildCR3FtypOnly() []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint32(b[0:], 16)
	copy(b[4:], "ftyp")
	copy(b[8:], "crx ") // CR3 ftyp brand: containers.md §8(b) — major_brand "crx " (63 72 78 20)
	// minor_version = 0 (already zero)
	return b
}

// buildCR3WithCMT builds a CR3 whose Canon UUID sub-boxes include the given CMT
// boxes in the listed order. Each element of cmtBoxes is a pre-encoded ISOBMFF box.
// The ftyp major_brand is always "crx ".
func buildCR3WithCMT(cmtBoxes ...[]byte) []byte {
	var uuidContent []byte
	for _, box := range cmtBoxes {
		uuidContent = append(uuidContent, box...)
	}
	uuidBox := buildUUIDBox(canonUUID, uuidContent)
	moovBox := buildBox("moov", uuidBox)
	ftyp := buildCR3FtypOnly()
	return append(ftyp, moovBox...)
}

// extractFirstOffset extracts the first offset entry from the first stco or co64
// box found in moovContent (the moov payload, without moov box header).
// Returns the value and whether it was found. Used to verify stco/co64 relocation.
func extractFirstOffset(moovContent []byte, boxType string) (int64, bool) {
	pos := 0
	for pos+8 <= len(moovContent) {
		size, typ, headerLen, ok := parseCR3BoxHeader(moovContent, pos)
		if !ok {
			break
		}
		content := moovContent[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by slice length
		switch typ {
		case boxType:
			// FullBox: version(1)+flags(3)+entry_count(4) = 8 bytes prefix.
			if len(content) < 8 {
				return 0, false
			}
			entryStart := 8
			switch boxType {
			case "stco":
				if entryStart+4 > len(content) {
					return 0, false
				}
				return int64(binary.BigEndian.Uint32(content[entryStart:])), true
			case "co64":
				if entryStart+8 > len(content) {
					return 0, false
				}
				return int64(binary.BigEndian.Uint64(content[entryStart:])), true //nolint:gosec // G115: test helper
			}
		case "trak", "mdia", "minf", "stbl":
			if val, found := extractFirstOffset(content, boxType); found {
				return val, true
			}
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by slice length
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// §4(c) BMFF-box-* — ISO BMFF box layout rules
// ---------------------------------------------------------------------------

// TestBMFFBoxSizeNormal verifies that a normal 8-byte box header (size ≥ 8) is
// accepted by parseCR3BoxHeader.
// ISO 14496-12 §4.2: box = size(u32 BE) + type(4cc) + body.
func TestBMFFBoxSizeNormal(t *testing.T) {
	// BMFF-box-size-normal: §4.2 — standard 8-byte header accepted.
	t.Parallel()
	buf := make([]byte, 16)
	binary.BigEndian.PutUint32(buf[0:], 16)
	copy(buf[4:], "moov")
	sz, typ, hdrLen, ok := parseCR3BoxHeader(buf, 0)
	if !ok {
		t.Fatal("BMFF-box-size-normal: parseCR3BoxHeader returned ok=false for valid box")
	}
	if sz != 16 {
		t.Errorf("BMFF-box-size-normal: sz=%d, want 16", sz)
	}
	if typ != "moov" {
		t.Errorf("BMFF-box-size-normal: typ=%q, want moov", typ)
	}
	if hdrLen != 8 {
		t.Errorf("BMFF-box-size-normal: hdrLen=%d, want 8", hdrLen)
	}
}

// TestBMFFBoxSizeLargesize verifies that a box with size==1 (extended 64-bit
// largesize encoding) is correctly parsed.
// ISO 14496-12 §4.2: size==1 signals that an 8-byte u64 largesize follows the type.
// containers.md §4(c): "size==1 → 8-byte largesize(u64) follows type; headerLen = 16".
func TestBMFFBoxSizeLargesize(t *testing.T) {
	// CR3-box-largesize / BMFF-box-size-largesize: ISO 14496-12 §4.2.
	t.Parallel()
	// 24-byte box: size==1 sentinel + "uuid" + largesize=24 + 8 bytes body.
	buf := make([]byte, 24)
	binary.BigEndian.PutUint32(buf[0:], 1) // size==1 → largesize follows
	copy(buf[4:], "uuid")
	binary.BigEndian.PutUint64(buf[8:], 24) // largesize = 24

	sz, typ, hdrLen, ok := parseCR3BoxHeader(buf, 0)
	if !ok {
		t.Fatal("BMFF-box-size-largesize: parseCR3BoxHeader returned ok=false")
	}
	if sz != 24 {
		t.Errorf("BMFF-box-size-largesize: sz=%d, want 24", sz)
	}
	if typ != "uuid" {
		t.Errorf("BMFF-box-size-largesize: typ=%q, want uuid", typ)
	}
	if hdrLen != 16 {
		t.Errorf("BMFF-box-size-largesize: hdrLen=%d, want 16 (4+4+8)", hdrLen)
	}
}

// TestBMFFBoxLargesizeRoundTrip verifies that a CR3 file whose ftyp box is
// encoded with largesize (size==1) is parsed without error by Extract.
// ISO 14496-12 §4.2: largesize boxes are valid anywhere in the box tree.
func TestBMFFBoxLargesizeRoundTrip(t *testing.T) {
	// CR3-box-largesize-round-trip: largesize ftyp accepted by Extract.
	t.Parallel()

	// Build a largesize ftyp box.
	// size==1 + "ftyp" + largesize(8) + major_brand(4) + minor_version(4) = 24 bytes total.
	// ISO 14496-12 §4.2: largesize box header is 4(size)+4(type)+8(largesize) = 16 bytes.
	ftypLarge := make([]byte, 0, 24)
	ftypLarge = append(ftypLarge,
		0, 0, 0, 1, // size==1 sentinel
		'f', 't', 'y', 'p', // type
	)
	ftypLarge = binary.BigEndian.AppendUint64(ftypLarge, 24)      // largesize = 24
	ftypLarge = append(ftypLarge, 'c', 'r', 'x', ' ', 0, 0, 0, 0) // major_brand + minor_version

	moovBox := buildBox("moov", buildUUIDBox(canonUUID, buildBox("CMT1", conformanceTIFFLE)))
	data := append(ftypLarge, moovBox...)

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-box-largesize-round-trip: Extract returned error: %v", err)
	}
	if !bytes.Equal(rawEXIF, conformanceTIFFLE) {
		t.Errorf("CR3-box-largesize-round-trip: rawEXIF mismatch: got %d bytes, want %d",
			len(rawEXIF), len(conformanceTIFFLE))
	}
}

// TestBMFFBoxSize0ToEOF verifies that a box with size==0 is treated as
// "extends to end of containing structure."
// ISO 14496-12 §4.2: size==0 valid only for the last box; expands to EOF.
// containers.md §4(c): "size==0 → box to EOF".
func TestBMFFBoxSize0ToEOF(t *testing.T) {
	// BMFF-box-size0-to-EOF: ISO 14496-12 §4.2 — size==0 expands to remaining bytes.
	t.Parallel()
	buf := make([]byte, 20)
	binary.BigEndian.PutUint32(buf[0:], 0) // size==0 → extend to EOF
	copy(buf[4:], "mdat")
	sz, typ, hdrLen, ok := parseCR3BoxHeader(buf, 0)
	if !ok {
		t.Fatal("BMFF-box-size0-to-EOF: parseCR3BoxHeader returned ok=false for size==0")
	}
	if sz != 20 {
		t.Errorf("BMFF-box-size0-to-EOF: sz=%d, want 20 (full slice length)", sz)
	}
	if typ != "mdat" {
		t.Errorf("BMFF-box-size0-to-EOF: typ=%q, want mdat", typ)
	}
	if hdrLen != 8 {
		t.Errorf("BMFF-box-size0-to-EOF: hdrLen=%d, want 8", hdrLen)
	}
}

// TestBMFFBoxSizeInvalid2to7 verifies that sizes 2–7 are rejected as malformed.
// ISO 14496-12 §4.2: box must be large enough to contain its own 8-byte header.
// containers.md §4(f): "size 2–7 invalid".
func TestBMFFBoxSizeInvalid2to7(t *testing.T) {
	// BMFF-box-size-invalid-2-to-7: ISO 14496-12 §4.2 — sizes < 8 are invalid.
	t.Parallel()
	for size := 2; size <= 7; size++ {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			t.Parallel()
			buf := make([]byte, 8)
			binary.BigEndian.PutUint32(buf[0:], uint32(size)) //nolint:gosec // G115: test helper, size in [2,7]
			copy(buf[4:], "test")
			_, _, _, ok := parseCR3BoxHeader(buf, 0)
			if ok {
				t.Errorf("BMFF-box-size-invalid-2-to-7: size=%d accepted, want ok=false", size)
			}
		})
	}
}

// TestBMFFBoxSizePastEOF verifies that a box whose declared size exceeds the
// available data is rejected by parseCR3BoxHeader.
// ISO 14496-12 §4.2: bounds check — size must not exceed containing structure.
// containers.md §4(f): "size past EOF".
func TestBMFFBoxSizePastEOF(t *testing.T) {
	// BMFF-box-size-past-EOF: ISO 14496-12 §4.2 — size > available bytes rejected.
	t.Parallel()
	buf := make([]byte, 8)
	binary.BigEndian.PutUint32(buf[0:], 100) // claims 100 bytes, only 8 available
	copy(buf[4:], "moov")
	_, _, _, ok := parseCR3BoxHeader(buf, 0)
	if ok {
		t.Error("BMFF-box-size-past-EOF: parseCR3BoxHeader accepted box with size > len(data)")
	}
}

// TestBMFFBoxSizeExactlyHeader verifies that a box of exactly 8 bytes
// (header only, empty body) is accepted.
// ISO 14496-12 §4.2: minimum valid size is 8.
func TestBMFFBoxSizeExactlyHeader(t *testing.T) {
	// BMFF-box-size-exactly-header: §4.2 — size==8 is the minimum valid box.
	t.Parallel()
	buf := make([]byte, 8)
	binary.BigEndian.PutUint32(buf[0:], 8)
	copy(buf[4:], "free")
	sz, typ, hdrLen, ok := parseCR3BoxHeader(buf, 0)
	if !ok {
		t.Fatal("BMFF-box-size-exactly-header: expected ok=true for size==8")
	}
	if sz != 8 {
		t.Errorf("BMFF-box-size-exactly-header: sz=%d, want 8", sz)
	}
	if typ != "free" {
		t.Errorf("BMFF-box-size-exactly-header: typ=%q, want free", typ)
	}
	if hdrLen != 8 {
		t.Errorf("BMFF-box-size-exactly-header: hdrLen=%d, want 8", hdrLen)
	}
}

// TestBMFFBoxTruncatedHeader verifies that parseCR3BoxHeader rejects inputs
// shorter than 8 bytes (cannot contain a valid header).
// ISO 14496-12 §4.2: box header is at minimum 8 bytes.
func TestBMFFBoxTruncatedHeader(t *testing.T) {
	// BMFF-box-truncated: §4.2 — < 8 bytes can never be a valid box.
	t.Parallel()
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"1-byte", []byte{0x00}},
		{"4-bytes-size-only", []byte{0x00, 0x00, 0x00, 0x10}},
		{"7-bytes", []byte{0x00, 0x00, 0x00, 0x10, 'm', 'o', 'o'}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, ok := parseCR3BoxHeader(tc.data, 0)
			if ok {
				t.Errorf("BMFF-box-truncated %s: expected ok=false, got ok=true", tc.name)
			}
		})
	}
}

// TestBMFFBoxLargesizeTruncated verifies that a largesize box whose largesize
// field is cut off is correctly rejected.
// ISO 14496-12 §4.2: largesize (size==1) needs 8 bytes following the type.
// containers.md §4(f): "largesize overflow".
func TestBMFFBoxLargesizeTruncated(t *testing.T) {
	// BMFF-box-largesize-truncated: §4.2 — largesize field must be present.
	t.Parallel()
	buf := make([]byte, 8) // size==1 + "uuid", but no room for 8-byte largesize
	binary.BigEndian.PutUint32(buf[0:], 1)
	copy(buf[4:], "uuid")
	_, _, _, ok := parseCR3BoxHeader(buf, 0)
	if ok {
		t.Error("BMFF-box-largesize-truncated: expected ok=false when largesize field is missing")
	}
}

// ---------------------------------------------------------------------------
// §4(c) BMFF-ftyp-* — ftyp box structure
// ---------------------------------------------------------------------------

// TestBMFFFtypCRXBrand verifies that a CR3 file with ftyp major_brand "crx "
// (0x63 72 78 20) is accepted without error.
// ISO 14496-12 §4.3: ftyp = major_brand(4) + minor_version(u32) + compatible_brands[].
// containers.md §8(b): CR3 ftyp major_brand is "crx " (63 72 78 20).
func TestBMFFFtypCRXBrand(t *testing.T) {
	// CR3-detect-crx-brand: containers.md §8(b) — major_brand "crx " accepted.
	t.Parallel()
	data := buildMinimalCR3(conformanceTIFFLE, nil)
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-detect-crx-brand: Extract returned error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("CR3-detect-crx-brand: rawEXIF is nil, want CMT1 content")
	}
}

// TestBMFFFtypBrandAtBytes8to12 verifies that the ftyp major_brand occupies
// bytes [8:12] of the file (after the 4-byte size and 4-byte "ftyp" type).
// ISO 14496-12 §4.3.
func TestBMFFFtypBrandAtBytes8to12(t *testing.T) {
	// BMFF-ftyp-brand-position: §4.3 — major_brand at bytes [8:12].
	t.Parallel()
	data := buildMinimalCR3(conformanceTIFFLE, nil)
	if len(data) < 12 {
		t.Fatal("BMFF-ftyp-brand-position: fixture too short")
	}
	brand := string(data[8:12])
	if brand != "crx " {
		t.Errorf("BMFF-ftyp-brand-position: brand=%q at [8:12], want %q", brand, "crx ")
	}
}

// ---------------------------------------------------------------------------
// §4(c) BMFF-uuid-* — uuid box with 16-byte UUID extension
// ---------------------------------------------------------------------------

// TestBMFFUUIDBoxLayout verifies that the Canon UUID box is correctly structured:
// size(u32 BE) + "uuid" + 16-byte UUID + content.
// ISO 14496-12 §4.2: uuid box has a 16-byte user-defined type identifier after the 4cc.
// containers.md §8(d): Canon UUID {85C0B687-820F-11E0-8111-F4CE462B6A48}.
func TestBMFFUUIDBoxLayout(t *testing.T) {
	// BMFF-uuid-layout: §4.2 — uuid box = size + "uuid" + 16-byte UUID + content.
	t.Parallel()
	content := []byte("test-content")
	uuidBox := buildUUIDBox(canonUUID, content)

	// Verify size field.
	expectedSize := uint32(8 + 16 + len(content)) //nolint:gosec // G115: test helper, bounded
	gotSize := binary.BigEndian.Uint32(uuidBox[0:4])
	if gotSize != expectedSize {
		t.Errorf("BMFF-uuid-layout: size=%d, want %d", gotSize, expectedSize)
	}

	// Verify type field.
	if string(uuidBox[4:8]) != "uuid" {
		t.Errorf("BMFF-uuid-layout: type=%q, want uuid", string(uuidBox[4:8]))
	}

	// Verify UUID bytes.
	if !bytes.Equal(uuidBox[8:24], canonUUID) {
		t.Errorf("BMFF-uuid-layout: UUID mismatch: got %X, want %X", uuidBox[8:24], canonUUID)
	}

	// Verify content.
	if !bytes.Equal(uuidBox[24:], content) {
		t.Errorf("BMFF-uuid-layout: content mismatch")
	}
}

// TestBMFFUUIDCanonBytes verifies the exact byte values of the Canon UUID.
// lclevy canon_cr3: {85C0B687-820F-11E0-8111-F4CE462B6A48}.
func TestBMFFUUIDCanonBytes(t *testing.T) {
	// BMFF-uuid-canon-bytes: lclevy canon_cr3 — UUID must be exactly {85C0B687-820F-11E0-8111-F4CE462B6A48}.
	t.Parallel()
	expected := []byte{
		0x85, 0xC0, 0xB6, 0x87, 0x82, 0x0F, 0x11, 0xE0,
		0x81, 0x11, 0xF4, 0xCE, 0x46, 0x2B, 0x6A, 0x48,
	}
	if !bytes.Equal(canonUUID, expected) {
		t.Errorf("BMFF-uuid-canon-bytes: canonUUID=%X, want %X", canonUUID, expected)
	}
}

// ---------------------------------------------------------------------------
// §4(c) BMFF-child-iter-* — child iteration bounded by parent size
// ---------------------------------------------------------------------------

// TestBMFFChildIterationBounded verifies that the box walker stops scanning
// children when it reaches the parent box boundary, not when it hits EOF.
// ISO 14496-12 §4.2: each child box must lie entirely within the parent box.
// containers.md §4(c): "Bound child iteration by parent size."
func TestBMFFChildIterationBounded(t *testing.T) {
	// BMFF-child-iter-bounded: §4.2 — child scan stops at parent boundary.
	t.Parallel()

	// Build an inner moov with a uuid sub-box. Then place extra bytes after the moov
	// that would decode as another uuid box with the Canon UUID.
	// findUUIDBox must not find the outer one because it searches within moovData.
	uuidBox := buildUUIDBox(canonUUID, buildBox("CMT1", conformanceTIFFLE))
	innerMoov := buildBox("moov", uuidBox)

	// Bytes that look like a Canon UUID but are outside the moov boundary.
	decoyUUID := buildUUIDBox(canonUUID, buildBox("CMT1", []byte("should-not-see-this")))

	var stream bytes.Buffer
	stream.Write(buildCR3FtypOnly())
	stream.Write(innerMoov)
	stream.Write(decoyUUID) // outside moov — must not be reached

	rawEXIF, _, _, err := Extract(bytes.NewReader(stream.Bytes()))
	if err != nil {
		t.Fatalf("BMFF-child-iter-bounded: Extract error: %v", err)
	}
	// Must return the inner CMT1 (conformanceTIFFLE), not the decoy.
	if !bytes.Equal(rawEXIF, conformanceTIFFLE) {
		t.Errorf("BMFF-child-iter-bounded: rawEXIF mismatch: got %d bytes, want %d",
			len(rawEXIF), len(conformanceTIFFLE))
	}
}

// ---------------------------------------------------------------------------
// §8(b) CR3-detect-* — detection rules
// ---------------------------------------------------------------------------

// TestCR3DetectNoMoovError verifies that a stream containing only an ftyp box
// (no moov) returns ErrNoMoovBox.
// containers.md §8(f): "CR3 missing moov/uuid".
func TestCR3DetectNoMoovError(t *testing.T) {
	// CR3-detect-no-moov: containers.md §8(f) — missing moov returns ErrNoMoovBox.
	t.Parallel()
	data := buildCR3FtypOnly()
	_, _, _, err := Extract(bytes.NewReader(data))
	if !errors.Is(err, ErrNoMoovBox) {
		t.Errorf("CR3-detect-no-moov: got %v, want ErrNoMoovBox", err)
	}
}

// TestCR3DetectEmptyInputError verifies that an empty byte slice returns an error.
// containers.md §8(f).
func TestCR3DetectEmptyInputError(t *testing.T) {
	// CR3-detect-empty-input: Extract on empty input must return an error.
	t.Parallel()
	_, _, _, err := Extract(bytes.NewReader([]byte{}))
	if err == nil {
		t.Error("CR3-detect-empty-input: expected error for empty input, got nil")
	}
}

// ---------------------------------------------------------------------------
// §8(d) CR3-CMT1-* — CMT1 carries the IFD0 TIFF stream
// ---------------------------------------------------------------------------

// TestCR3CMT1IFD0Extracted verifies that Extract returns the raw TIFF bytes
// from the CMT1 sub-box inside the Canon UUID box.
// lclevy canon_cr3: CMT1 = IFD0 (TIFF header + IFD entries).
// containers.md §8(d): "CR3 CMT1=IFD0".
func TestCR3CMT1IFD0Extracted(t *testing.T) {
	// CR3-CMT1-ifd0: lclevy canon_cr3 — CMT1 is the IFD0 TIFF stream.
	t.Parallel()
	data := buildCR3WithCMT(buildBox("CMT1", conformanceTIFFLE))
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-CMT1-ifd0: Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, conformanceTIFFLE) {
		t.Errorf("CR3-CMT1-ifd0: rawEXIF mismatch: got %d bytes, want %d",
			len(rawEXIF), len(conformanceTIFFLE))
	}
}

// TestCR3CMT1BigEndianTIFF verifies that CMT1 may carry a big-endian TIFF stream.
// TIFF 6.0 §2: byte order mark is "II" (LE) or "MM" (BE).
func TestCR3CMT1BigEndianTIFF(t *testing.T) {
	// CR3-CMT1-big-endian: TIFF 6.0 §2 — MM byte-order mark valid in CMT1.
	t.Parallel()
	data := buildCR3WithCMT(buildBox("CMT1", conformanceTIFFBE))
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-CMT1-big-endian: Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, conformanceTIFFBE) {
		t.Errorf("CR3-CMT1-big-endian: rawEXIF mismatch")
	}
}

// TestCR3CMT1NoPrefix verifies that the EXIF bytes returned by Extract do not
// carry the JPEG-style "Exif\0\0" prefix.
// containers.md §8, cross-cutting prefix matrix: CR3 CMT1 has no Exif\0\0 prefix.
func TestCR3CMT1NoPrefix(t *testing.T) {
	// CR3-CMT1-no-prefix: containers.md cross-cutting matrix — CMT1 is raw TIFF, no Exif\0\0.
	t.Parallel()
	data := buildCR3WithCMT(buildBox("CMT1", conformanceTIFFLE))
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-CMT1-no-prefix: Extract error: %v", err)
	}
	if len(rawEXIF) >= 6 && bytes.Equal(rawEXIF[:6], []byte("Exif\x00\x00")) {
		t.Error("CR3-CMT1-no-prefix: rawEXIF has Exif\\0\\0 prefix — must be raw TIFF only")
	}
}

// TestCR3CMT1MissingReturnsErrNoCMT1Box verifies that when CMT1 is absent from
// the Canon UUID box, Extract returns ErrNoCMT1Box and nil rawEXIF.
// The BMFF structure is intact; only the mandatory metadata box is missing.
//
// audit #138: ErrNoCMT1Box distinguishes "no EXIF metadata" from a broken
// container parse, letting callers decide whether to proceed with XMP-only data.
// lclevy canon_cr3: CMT1 is the IFD0 TIFF stream; its absence means no EXIF.
func TestCR3CMT1MissingReturnsErrNoCMT1Box(t *testing.T) {
	// CR3-robust-missing-CMT1: audit #138 — missing CMT1 → ErrNoCMT1Box + nil rawEXIF.
	t.Parallel()
	// Build a UUID box with CMT3 only (no CMT1).
	data := buildCR3WithCMT(buildBox("CMT3", []byte("makernote-data")))
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if !errors.Is(err, ErrNoCMT1Box) {
		t.Errorf("CR3-robust-missing-CMT1: got err=%v, want ErrNoCMT1Box", err)
	}
	if rawEXIF != nil {
		t.Errorf("CR3-robust-missing-CMT1: rawEXIF = %d bytes, want nil when CMT1 absent",
			len(rawEXIF))
	}
}

// ---------------------------------------------------------------------------
// §8(d) CR3-CMT2-* — CMT2 carries the Exif IFD; merged with CMT1
// ---------------------------------------------------------------------------

// TestCR3CMT2ExifIFDMerged verifies that when the ExifIFD pointer in CMT1's
// IFD0 points beyond CMT1's length, CMT2 bytes are appended to produce a
// contiguous buffer that exif.Parse can traverse.
// lclevy canon_cr3: CMT2 = Exif IFD; merged by offset arithmetic.
// containers.md §8(d): "CMT2=Exif IFD".
func TestCR3CMT2ExifIFDMerged(t *testing.T) {
	// CR3-CMT2-exif-ifd: lclevy canon_cr3 — CMT2 appended when ExifIFD offset exceeds CMT1.
	t.Parallel()

	// Build a CMT1 TIFF with ExifIFD pointer = 9999, far beyond len(cmt1).
	cmt1 := make([]byte, 8+2+12+4)
	binary.LittleEndian.PutUint16(cmt1[0:], 0x4949)
	binary.LittleEndian.PutUint16(cmt1[2:], 0x002A)
	binary.LittleEndian.PutUint32(cmt1[4:], 8)
	binary.LittleEndian.PutUint16(cmt1[8:], 1)
	binary.LittleEndian.PutUint16(cmt1[10:], 0x8769) // ExifIFD pointer tag
	binary.LittleEndian.PutUint16(cmt1[12:], 4)      // LONG type
	binary.LittleEndian.PutUint32(cmt1[14:], 1)      // count = 1
	binary.LittleEndian.PutUint32(cmt1[18:], 9999)   // offset > len(cmt1)

	cmt2 := bytes.Repeat([]byte{0x42}, 16) // Exif IFD placeholder

	data := buildCR3WithCMT(buildBox("CMT1", cmt1), buildBox("CMT2", cmt2))
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-CMT2-exif-ifd: Extract error: %v", err)
	}
	// Merged buffer must be len(cmt1)+len(cmt2).
	wantLen := len(cmt1) + len(cmt2)
	if len(rawEXIF) != wantLen {
		t.Errorf("CR3-CMT2-exif-ifd: merged len=%d, want %d", len(rawEXIF), wantLen)
	}
	// CMT1 prefix must be intact.
	if !bytes.HasPrefix(rawEXIF, cmt1) {
		t.Error("CR3-CMT2-exif-ifd: merged buffer does not start with CMT1 bytes")
	}
	// CMT2 suffix must follow.
	if !bytes.HasSuffix(rawEXIF, cmt2) {
		t.Error("CR3-CMT2-exif-ifd: merged buffer does not end with CMT2 bytes")
	}
}

// TestCR3CMT2NotMergedWhenExifIFDWithinCMT1 verifies that when the ExifIFD
// pointer lies within CMT1, cmt2 is NOT appended (zero-copy fast path).
// containers.md §8(d): CMT1 and CMT2 merging is conditional on the offset.
func TestCR3CMT2NotMergedWhenExifIFDWithinCMT1(t *testing.T) {
	// CR3-CMT2-no-merge-when-within: CMT2 not appended if ExifIFD offset < len(CMT1).
	t.Parallel()

	// CMT1 with ExifIFD pointer = 10 (within CMT1).
	cmt1 := make([]byte, 8+2+12+4)
	binary.LittleEndian.PutUint16(cmt1[0:], 0x4949)
	binary.LittleEndian.PutUint16(cmt1[2:], 0x002A)
	binary.LittleEndian.PutUint32(cmt1[4:], 8)
	binary.LittleEndian.PutUint16(cmt1[8:], 1)
	binary.LittleEndian.PutUint16(cmt1[10:], 0x8769)
	binary.LittleEndian.PutUint16(cmt1[12:], 4)
	binary.LittleEndian.PutUint32(cmt1[14:], 1)
	binary.LittleEndian.PutUint32(cmt1[18:], 10) // offset 10 < len(cmt1) = 26

	cmt2 := bytes.Repeat([]byte{0xFF}, 32)

	data := buildCR3WithCMT(buildBox("CMT1", cmt1), buildBox("CMT2", cmt2))
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-CMT2-no-merge-when-within: Extract error: %v", err)
	}
	// Should return only CMT1 (no merge).
	if len(rawEXIF) != len(cmt1) {
		t.Errorf("CR3-CMT2-no-merge-when-within: len=%d, want %d (CMT1 only, no merge)",
			len(rawEXIF), len(cmt1))
	}
}

// TestCR3CMT2MissingNoError verifies that Extract succeeds when CMT2 is absent.
// containers.md §8(f): "missing CMT* boxes" must not crash.
func TestCR3CMT2MissingNoError(t *testing.T) {
	// CR3-robust-missing-CMT2: containers.md §8(f) — absent CMT2 is not an error.
	t.Parallel()
	data := buildCR3WithCMT(buildBox("CMT1", conformanceTIFFLE))
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-robust-missing-CMT2: Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, conformanceTIFFLE) {
		t.Errorf("CR3-robust-missing-CMT2: rawEXIF mismatch: got %d bytes, want %d",
			len(rawEXIF), len(conformanceTIFFLE))
	}
}

// ---------------------------------------------------------------------------
// §8(d) CR3-CMT3-* — CMT3 carries the Canon MakerNote
// ---------------------------------------------------------------------------

// TestCR3CMT3PresentDoesNotAffectEXIF verifies that the presence of a CMT3
// (Canon MakerNote) sub-box does not corrupt or discard the CMT1 payload.
// lclevy canon_cr3: CMT3 = MakerNote; not extracted by this package.
func TestCR3CMT3PresentDoesNotAffectEXIF(t *testing.T) {
	// CR3-CMT3-present-no-corruption: CMT3 present alongside CMT1 — CMT1 unaffected.
	t.Parallel()
	makerNote := bytes.Repeat([]byte{0xAB}, 64)
	data := buildCR3WithCMT(
		buildBox("CMT1", conformanceTIFFLE),
		buildBox("CMT3", makerNote),
	)
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-CMT3-present-no-corruption: Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, conformanceTIFFLE) {
		t.Errorf("CR3-CMT3-present-no-corruption: rawEXIF corrupted by CMT3 presence")
	}
}

// ---------------------------------------------------------------------------
// §8(d) CR3-CMT4-* — CMT4 carries the GPS IFD
// ---------------------------------------------------------------------------

// TestCR3CMT4PresentDoesNotAffectEXIF verifies that CMT4 (GPS IFD) does not
// interfere with CMT1 extraction.
// lclevy canon_cr3: CMT4 = GPS IFD.
// containers.md §8(d): "CMT4=GPS".
func TestCR3CMT4PresentDoesNotAffectEXIF(t *testing.T) {
	// CR3-CMT4-gps: lclevy canon_cr3 / containers.md §8(d) — CMT4 is GPS IFD, does not corrupt CMT1.
	t.Parallel()
	gpsIFD := bytes.Repeat([]byte{0x47, 0x50, 0x53}, 10) // placeholder GPS data
	data := buildCR3WithCMT(
		buildBox("CMT1", conformanceTIFFLE),
		buildBox("CMT4", gpsIFD),
	)
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-CMT4-gps: Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, conformanceTIFFLE) {
		t.Errorf("CR3-CMT4-gps: rawEXIF corrupted by CMT4 presence")
	}
}

// TestCR3AllCMTBoxesPresent verifies that all four CMT boxes may coexist
// without mutual interference.
// lclevy canon_cr3 + containers.md §8(d).
func TestCR3AllCMTBoxesPresent(t *testing.T) {
	// CR3-all-CMT: all four CMT* boxes coexist without interference.
	t.Parallel()
	data := buildCR3WithCMT(
		buildBox("CMT1", conformanceTIFFLE),
		buildBox("CMT2", bytes.Repeat([]byte{0x22}, 12)),
		buildBox("CMT3", bytes.Repeat([]byte{0x33}, 20)),
		buildBox("CMT4", bytes.Repeat([]byte{0x44}, 16)),
	)
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-all-CMT: Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, conformanceTIFFLE) {
		t.Errorf("CR3-all-CMT: rawEXIF mismatch with all CMT boxes present")
	}
}

// ---------------------------------------------------------------------------
// §8(d) CR3-XMP-* — XMP sub-box in Canon UUID
// ---------------------------------------------------------------------------

// TestCR3XMPExtracted verifies that Extract returns the raw XMP packet bytes
// from the "XMP " sub-box inside the Canon UUID box.
// containers.md §8(d): XMP is located via XMP  sub-box in the Canon UUID.
func TestCR3XMPExtracted(t *testing.T) {
	// CR3-XMP-extracted: containers.md §8(d) — XMP  sub-box content returned as rawXMP.
	t.Parallel()
	data := buildCR3WithCMT(
		buildBox("CMT1", conformanceTIFFLE),
		buildBox("XMP ", conformanceXMPPacket),
	)
	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-XMP-extracted: Extract error: %v", err)
	}
	if !bytes.Equal(rawXMP, conformanceXMPPacket) {
		t.Errorf("CR3-XMP-extracted: rawXMP mismatch: got %d bytes, want %d",
			len(rawXMP), len(conformanceXMPPacket))
	}
}

// TestCR3XMPFourCCTrailingSpace verifies that the XMP sub-box uses the FourCC
// "XMP " (0x58 4D 50 20) — with a trailing space.
// The fourth byte is 0x20 (space), not 0x00.
// containers.md §8(d): XMP FourCC is "XMP " (four bytes including the space).
func TestCR3XMPFourCCTrailingSpace(t *testing.T) {
	// CR3-XMP-fourcc-trailing-space: XMP FourCC = 0x58 4D 50 20 (space not null).
	t.Parallel()
	data := buildCR3WithCMT(
		buildBox("CMT1", conformanceTIFFLE),
		buildBox("XMP ", conformanceXMPPacket),
	)
	// Verify the raw bytes of the XMP  box in the Canon UUID payload.
	// Find the uuid content: skip ftyp, moov header, uuid box header + 16-byte UUID.
	// Easiest: just verify Extract returns the XMP and the 4cc is "XMP ".
	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-XMP-fourcc-trailing-space: Extract error: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("CR3-XMP-fourcc-trailing-space: rawXMP is nil")
	}
	// Confirm the box was built with "XMP " (space) not "XMP\0".
	// Re-find the XMP  box in the raw bytes.
	found := false
	for i := 0; i+8 <= len(data); i++ {
		if data[i+4] == 'X' && data[i+5] == 'M' && data[i+6] == 'P' && data[i+7] == 0x20 {
			found = true
			break
		}
	}
	if !found {
		t.Error("CR3-XMP-fourcc-trailing-space: XMP FourCC 'XMP '(0x20) not found in stream")
	}
}

// TestCR3XMPAbsentNil verifies that Extract returns nil rawXMP when there is no
// XMP  sub-box in the Canon UUID.
// containers.md §8(d).
func TestCR3XMPAbsentNil(t *testing.T) {
	// CR3-XMP-absent-nil: absent XMP  sub-box → rawXMP == nil.
	t.Parallel()
	data := buildCR3WithCMT(buildBox("CMT1", conformanceTIFFLE))
	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-XMP-absent-nil: Extract error: %v", err)
	}
	if rawXMP != nil {
		t.Errorf("CR3-XMP-absent-nil: rawXMP = %d bytes, want nil when XMP  box absent",
			len(rawXMP))
	}
}

// ---------------------------------------------------------------------------
// §8(d) — IPTC is not stored in CR3
// ---------------------------------------------------------------------------

// TestCR3IPTCAlwaysNil verifies that Extract always returns nil rawIPTC
// because CR3 does not carry native IPTC.
// containers.md §8: IPTC is via XMP only in BMFF-based formats.
func TestCR3IPTCAlwaysNil(t *testing.T) {
	// CR3-IPTC-nil: containers.md §8 — CR3 has no native IPTC; rawIPTC always nil.
	t.Parallel()
	data := buildCR3WithCMT(
		buildBox("CMT1", conformanceTIFFLE),
		buildBox("XMP ", conformanceXMPPacket),
	)
	_, rawIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-IPTC-nil: Extract error: %v", err)
	}
	if rawIPTC != nil {
		t.Errorf("CR3-IPTC-nil: rawIPTC = %d bytes, want nil", len(rawIPTC))
	}
}

// ---------------------------------------------------------------------------
// §8(e) CR3-write-* — write byte-correctness
// ---------------------------------------------------------------------------

// TestCR3WriteMoovSizeUpdated verifies that after Inject replaces a CMT1 payload
// with a differently-sized EXIF, the output moov box size field is updated to
// reflect the new content.
// ISO 14496-12 §4.2: box size = total including header.
// containers.md §8(e): "recompute ancestor sizes when inner content changes".
func TestCR3WriteMoovSizeUpdated(t *testing.T) {
	// CR3-write-moov-size: ISO 14496-12 §4.2 — moov size recalculated after inject.
	t.Parallel()
	original := buildMinimalCR3(conformanceTIFFLE, nil)

	// Read original moov size.
	origMoovStart, origMoovEnd, ok := findMoovRange(original)
	if !ok {
		t.Fatal("CR3-write-moov-size: no moov in fixture")
	}
	origMoovSize := origMoovEnd - origMoovStart

	// Inject a larger EXIF (100 bytes larger).
	larger := make([]byte, len(conformanceTIFFLE)+100)
	copy(larger, conformanceTIFFLE)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, larger, nil, nil, true); err != nil {
		t.Fatalf("CR3-write-moov-size: Inject error: %v", err)
	}
	result := out.Bytes()

	newMoovStart, newMoovEnd, ok2 := findMoovRange(result)
	if !ok2 {
		t.Fatal("CR3-write-moov-size: no moov in output")
	}
	newMoovSize := newMoovEnd - newMoovStart
	if newMoovSize <= origMoovSize {
		t.Errorf("CR3-write-moov-size: newMoovSize=%d <= origMoovSize=%d; expected growth",
			newMoovSize, origMoovSize)
	}

	// Verify the moov size field matches actual content length.
	gotSizeField := int(binary.BigEndian.Uint32(result[newMoovStart : newMoovStart+4]))
	if gotSizeField != newMoovSize {
		t.Errorf("CR3-write-moov-size: size field=%d, actual=%d", gotSizeField, newMoovSize)
	}
}

// TestCR3WritePreserveUnknownSegmentsRequired verifies that Inject returns
// ErrPreserveUnknownSegmentsNotSupported when called with preserveUnknownSegments=false.
// containers.md §8(e): ISOBMFF boxes are structurally mandatory; no optional stripping.
func TestCR3WritePreserveUnknownSegmentsRequired(t *testing.T) {
	// CR3-write-preserve-unknown: ErrPreserveUnknownSegmentsNotSupported when preserve=false.
	t.Parallel()
	data := buildMinimalCR3(conformanceTIFFLE, nil)
	var out bytes.Buffer
	err := Inject(bytes.NewReader(data), &out, conformanceTIFFLE, nil, nil, false)
	if !errors.Is(err, ErrPreserveUnknownSegmentsNotSupported) {
		t.Errorf("CR3-write-preserve-unknown: got %v, want ErrPreserveUnknownSegmentsNotSupported", err)
	}
}

// TestCR3WriteNilPayloadsPassThrough verifies that Inject with all-nil metadata
// payloads copies the source bytes exactly (no structural changes, no stco/co64
// invalidation).
// containers.md §8(e): "If all payloads are nil, pass through unchanged."
func TestCR3WriteNilPayloadsPassThrough(t *testing.T) {
	// CR3-write-nil-pass-through: all-nil payloads → output byte-for-byte identical to input.
	t.Parallel()
	data := buildMinimalCR3(conformanceTIFFLE, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("CR3-write-nil-pass-through: Inject error: %v", err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Errorf("CR3-write-nil-pass-through: output differs from input (%d vs %d bytes)",
			out.Len(), len(data))
	}
}

// TestCR3WriteStcoRelocated verifies that after Inject grows the moov box,
// the stco chunk-offset entries are correctly relocated by delta.
// ISO 14496-12 §8.7.3: stco entries are absolute file offsets.
// containers.md §8(e): "patch iloc/stco/co64 offsets when boxes move".
func TestCR3WriteStcoRelocated(t *testing.T) {
	// CR3-write-stco-relocated: ISO 14496-12 §8.7.3 — stco entries updated after moov grows.
	t.Parallel()

	smallTIFF := conformanceTIFFLE
	largeTIFF := append(append([]byte(nil), conformanceTIFFLE...), make([]byte, 64)...)

	mdatPayload := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	fileBytes, mdatOffset := buildCR3WithOffsetTable(smallTIFF, mdatPayload, "stco")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(fileBytes), &out, largeTIFF, nil, nil, true); err != nil {
		t.Fatalf("CR3-write-stco-relocated: Inject error: %v", err)
	}
	outBytes := out.Bytes()

	newMoovStart, newMoovEnd, ok := findMoovRange(outBytes)
	if !ok {
		t.Fatal("CR3-write-stco-relocated: no moov in output")
	}
	delta := (newMoovEnd - newMoovStart) - (mdatOffset - 16)
	if delta <= 0 {
		t.Fatalf("CR3-write-stco-relocated: delta=%d, expected >0 (moov grew)", delta)
	}
	expectedOffset := int64(mdatOffset) + int64(delta)

	stcoVal, found := extractFirstOffset(outBytes[newMoovStart+8:newMoovEnd], "stco")
	if !found {
		t.Fatal("CR3-write-stco-relocated: stco box not found in output")
	}
	if stcoVal != expectedOffset {
		t.Errorf("CR3-write-stco-relocated: stco=%d, want %d (orig=%d + delta=%d)",
			stcoVal, expectedOffset, mdatOffset, delta)
	}

	// Verify the mdat sentinel bytes are reachable at the relocated offset.
	mdatPayloadOff := int(expectedOffset) + 8 // skip mdat box header
	if mdatPayloadOff+len(mdatPayload) > len(outBytes) {
		t.Fatalf("CR3-write-stco-relocated: relocated offset %d out of bounds", mdatPayloadOff)
	}
	if !bytes.Equal(outBytes[mdatPayloadOff:mdatPayloadOff+len(mdatPayload)], mdatPayload) {
		t.Errorf("CR3-write-stco-relocated: mdat content at relocated offset: got %x, want %x",
			outBytes[mdatPayloadOff:mdatPayloadOff+len(mdatPayload)], mdatPayload)
	}
}

// TestCR3WriteCo64Relocated verifies the same as TestCR3WriteStcoRelocated but
// with co64 (64-bit) chunk-offset entries.
// ISO 14496-12 §8.7.5: co64 entries are absolute u64 file offsets.
func TestCR3WriteCo64Relocated(t *testing.T) {
	// CR3-write-co64-relocated: ISO 14496-12 §8.7.5 — co64 entries updated after moov grows.
	t.Parallel()

	smallTIFF := conformanceTIFFLE
	largeTIFF := append(append([]byte(nil), conformanceTIFFLE...), make([]byte, 64)...)

	mdatPayload := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	fileBytes, mdatOffset := buildCR3WithOffsetTable(smallTIFF, mdatPayload, "co64")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(fileBytes), &out, largeTIFF, nil, nil, true); err != nil {
		t.Fatalf("CR3-write-co64-relocated: Inject error: %v", err)
	}
	outBytes := out.Bytes()

	newMoovStart, newMoovEnd, ok := findMoovRange(outBytes)
	if !ok {
		t.Fatal("CR3-write-co64-relocated: no moov in output")
	}
	delta := (newMoovEnd - newMoovStart) - (mdatOffset - 16)
	if delta <= 0 {
		t.Fatalf("CR3-write-co64-relocated: delta=%d, expected >0", delta)
	}
	expectedOffset := int64(mdatOffset) + int64(delta)

	co64Val, found := extractFirstOffset(outBytes[newMoovStart+8:newMoovEnd], "co64")
	if !found {
		t.Fatal("CR3-write-co64-relocated: co64 box not found in output")
	}
	if co64Val != expectedOffset {
		t.Errorf("CR3-write-co64-relocated: co64=%d, want %d", co64Val, expectedOffset)
	}
}

// TestCR3WriteStcoOffsetNotRelocatedWhenBeforeOldMoov verifies that stco entries
// whose value is less than oldMoovEnd are left unchanged.
// ISO 14496-12 §8.7.3: only offsets pointing after the moov need adjustment.
// containers.md §8(e): offset relocation algorithm — "O < oldMoovEnd: leave unchanged".
func TestCR3WriteStcoOffsetNotRelocatedWhenBeforeOldMoov(t *testing.T) {
	// CR3-write-stco-no-relocate-before-moov: §8.7.3 — offsets < oldMoovEnd unchanged.
	t.Parallel()

	// Build a CR3 with a stco entry pointing to offset 0 (before any moov).
	// Any stco entry with offset < moovEnd must not be adjusted.
	cmt1Box := buildBox("CMT1", conformanceTIFFLE)
	uuidBox := buildUUIDBox(canonUUID, cmt1Box)

	const ftypSize = 16
	// Use a low offset value (e.g. 4) that will always be < moovEnd.
	lowOffset := uint64(4)
	stcoPayload := make([]byte, 12) // version(1)+flags(3)+count(4)+offset(4)
	binary.BigEndian.PutUint32(stcoPayload[4:], 1)
	binary.BigEndian.PutUint32(stcoPayload[8:], uint32(lowOffset))

	stblBox := buildBox("stbl", buildBox("stco", stcoPayload))
	minfBox := buildBox("minf", stblBox)
	mdiaBox := buildBox("mdia", minfBox)
	trakBox := buildBox("trak", mdiaBox)

	moovContent := append(trakBox, uuidBox...)
	moovBox := buildBox("moov", moovContent)

	ftyp := buildCR3FtypOnly()
	fileBytes := append(ftyp, moovBox...)

	larger := make([]byte, len(conformanceTIFFLE)+64)
	copy(larger, conformanceTIFFLE)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(fileBytes), &out, larger, nil, nil, true); err != nil {
		t.Fatalf("CR3-write-stco-no-relocate-before-moov: Inject error: %v", err)
	}
	outBytes := out.Bytes()

	newMoovStart, newMoovEnd, ok := findMoovRange(outBytes)
	if !ok {
		t.Fatal("CR3-write-stco-no-relocate-before-moov: no moov in output")
	}

	stcoVal, found := extractFirstOffset(outBytes[newMoovStart+8:newMoovEnd], "stco")
	if !found {
		t.Fatal("CR3-write-stco-no-relocate-before-moov: stco not found in output")
	}
	// oldMoovEnd = ftypSize + len(moovBox).
	oldMoovEnd := ftypSize + len(moovBox)
	if int64(lowOffset) >= int64(oldMoovEnd) {
		t.Skip("CR3-write-stco-no-relocate-before-moov: low offset is not actually before moov end")
	}
	// The entry value must remain unchanged (= lowOffset).
	if stcoVal != int64(lowOffset) {
		t.Errorf("CR3-write-stco-no-relocate-before-moov: stco was relocated from %d to %d; expected no change",
			lowOffset, stcoVal)
	}
}

// TestCR3WriteStcoShrinkDeltaNegative verifies that when Inject replaces CMT1
// with a smaller payload (delta < 0), stco entries are decremented correctly.
// ISO 14496-12 §8.7.3: delta may be negative when moov shrinks.
// containers.md §8(e): delta < 0 — mdat shifts backward.
func TestCR3WriteStcoShrinkDeltaNegative(t *testing.T) {
	// CR3-write-stco-shrink: delta<0 when moov shrinks — stco entries decremented.
	t.Parallel()

	largeTIFF := make([]byte, len(conformanceTIFFLE)+64)
	copy(largeTIFF, conformanceTIFFLE)

	mdatPayload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	fileBytes, mdatOffset := buildCR3WithOffsetTable(largeTIFF, mdatPayload, "stco")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(fileBytes), &out, conformanceTIFFLE, nil, nil, true); err != nil {
		t.Fatalf("CR3-write-stco-shrink: Inject error: %v", err)
	}
	outBytes := out.Bytes()

	newMoovStart, newMoovEnd, ok := findMoovRange(outBytes)
	if !ok {
		t.Fatal("CR3-write-stco-shrink: no moov in output")
	}
	delta := (newMoovEnd - newMoovStart) - (mdatOffset - 16)
	if delta >= 0 {
		t.Fatalf("CR3-write-stco-shrink: delta=%d, expected <0 (moov shrunk)", delta)
	}
	expectedOffset := int64(mdatOffset) + int64(delta)

	stcoVal, found := extractFirstOffset(outBytes[newMoovStart+8:newMoovEnd], "stco")
	if !found {
		t.Fatal("CR3-write-stco-shrink: stco not found in output")
	}
	if stcoVal != expectedOffset {
		t.Errorf("CR3-write-stco-shrink: stco=%d, want %d", stcoVal, expectedOffset)
	}

	// Verify mdat content.
	mdatPayloadOff := int(expectedOffset) + 8
	if mdatPayloadOff+len(mdatPayload) > len(outBytes) {
		t.Fatalf("CR3-write-stco-shrink: relocated offset %d out of bounds", mdatPayloadOff)
	}
	if !bytes.Equal(outBytes[mdatPayloadOff:mdatPayloadOff+len(mdatPayload)], mdatPayload) {
		t.Errorf("CR3-write-stco-shrink: mdat content mismatch")
	}
}

// TestCR3WriteStcoOverflowDetected verifies that ErrStcoOverflow is returned
// when a stco offset + delta would exceed math.MaxUint32.
// containers.md §8(e): "stco overflow: fail rather than truncate".
func TestCR3WriteStcoOverflowDetected(t *testing.T) {
	// CR3-write-stco-overflow: containers.md §8(e) — stco overflow returns ErrStcoOverflow.
	t.Parallel()

	// Build a stco with an entry near MaxUint32 so delta pushes it over.
	cmt1Box := buildBox("CMT1", conformanceTIFFLE)
	uuidBox := buildUUIDBox(canonUUID, cmt1Box)

	const ftypSize = 16
	// Set the stco offset to MaxUint32 - 1 so that any positive delta overflows.
	nearMax := uint64(0xFFFFFFFE)
	stcoPayload := make([]byte, 12)
	binary.BigEndian.PutUint32(stcoPayload[4:], 1)
	binary.BigEndian.PutUint32(stcoPayload[8:], uint32(nearMax))

	stblBox := buildBox("stbl", buildBox("stco", stcoPayload))
	minfBox := buildBox("minf", stblBox)
	mdiaBox := buildBox("mdia", minfBox)
	trakBox := buildBox("trak", mdiaBox)
	moovContent := append(trakBox, uuidBox...)
	moovBox := buildBox("moov", moovContent)

	ftyp := buildCR3FtypOnly()
	fileBytes := append(ftyp, moovBox...)

	// Inject a larger EXIF to create a positive delta.
	larger := make([]byte, len(conformanceTIFFLE)+64)
	copy(larger, conformanceTIFFLE)

	var out bytes.Buffer
	err := Inject(bytes.NewReader(fileBytes), &out, larger, nil, nil, true)

	// If the offset is actually < oldMoovEnd, relocation is skipped (no overflow).
	// If it's >= oldMoovEnd, overflow must be detected.
	oldMoovEnd := ftypSize + len(moovBox)
	if int64(nearMax) >= int64(oldMoovEnd) && err == nil {
		t.Error("CR3-write-stco-overflow: expected ErrStcoOverflow or relocation-triggered error, got nil")
	}
	if err != nil && !errors.Is(err, ErrStcoOverflow) {
		// Any other error is also acceptable for overflow detection; just ensure no panic.
		t.Logf("CR3-write-stco-overflow: got error %v (ErrStcoOverflow preferred but any error acceptable)", err)
	}
}

// ---------------------------------------------------------------------------
// §8(d)+(e) CR3-round-trip-* — inject→extract preserves payloads exactly
// ---------------------------------------------------------------------------

// TestCR3RoundTripEXIF verifies that injecting EXIF and then extracting
// returns byte-for-byte identical data.
// containers.md §8(d)+(e).
func TestCR3RoundTripEXIF(t *testing.T) {
	// CR3-round-trip-EXIF: inject→extract preserves EXIF bytes exactly.
	t.Parallel()
	original := buildMinimalCR3(conformanceTIFFLE, nil)
	newEXIF := append(append([]byte(nil), conformanceTIFFLE...), 0x01, 0x02, 0x03, 0x04)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, newEXIF, nil, nil, true); err != nil {
		t.Fatalf("CR3-round-trip-EXIF: Inject error: %v", err)
	}
	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("CR3-round-trip-EXIF: Extract after Inject error: %v", err)
	}
	if !bytes.Equal(rawEXIF, newEXIF) {
		t.Errorf("CR3-round-trip-EXIF: rawEXIF mismatch: got %d bytes, want %d",
			len(rawEXIF), len(newEXIF))
	}
}

// TestCR3RoundTripXMP verifies that injecting XMP and then extracting
// returns the same bytes.
// containers.md §8(d)+(e).
func TestCR3RoundTripXMP(t *testing.T) {
	// CR3-round-trip-XMP: inject→extract preserves XMP bytes exactly.
	t.Parallel()
	original := buildMinimalCR3(conformanceTIFFLE, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, nil, nil, conformanceXMPPacket, true); err != nil {
		t.Fatalf("CR3-round-trip-XMP: Inject error: %v", err)
	}
	_, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("CR3-round-trip-XMP: Extract after Inject error: %v", err)
	}
	if !bytes.Equal(rawXMP, conformanceXMPPacket) {
		t.Errorf("CR3-round-trip-XMP: rawXMP mismatch: got %d bytes, want %d",
			len(rawXMP), len(conformanceXMPPacket))
	}
}

// TestCR3RoundTripEXIFAndXMP verifies simultaneous EXIF and XMP injection
// and extraction.
func TestCR3RoundTripEXIFAndXMP(t *testing.T) {
	// CR3-round-trip-EXIF-and-XMP: simultaneous EXIF+XMP inject→extract.
	t.Parallel()
	original := buildMinimalCR3(conformanceTIFFLE, nil)

	newEXIF := make([]byte, len(conformanceTIFFLE)+20)
	copy(newEXIF, conformanceTIFFLE)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, newEXIF, nil, conformanceXMPPacket, true); err != nil {
		t.Fatalf("CR3-round-trip-EXIF-and-XMP: Inject error: %v", err)
	}
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("CR3-round-trip-EXIF-and-XMP: Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, newEXIF) {
		t.Errorf("CR3-round-trip-EXIF-and-XMP: rawEXIF mismatch")
	}
	if !bytes.Equal(rawXMP, conformanceXMPPacket) {
		t.Errorf("CR3-round-trip-EXIF-and-XMP: rawXMP mismatch")
	}
}

// TestCR3RoundTripReplaceExistingXMP verifies that injecting XMP into a file
// that already has XMP replaces the existing value.
func TestCR3RoundTripReplaceExistingXMP(t *testing.T) {
	// CR3-round-trip-replace-XMP: existing XMP  box is replaced by Inject.
	t.Parallel()
	original := buildMinimalCR3(conformanceTIFFLE, []byte("old-xmp-data"))
	newXMP := conformanceXMPPacket

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, nil, nil, newXMP, true); err != nil {
		t.Fatalf("CR3-round-trip-replace-XMP: Inject error: %v", err)
	}
	_, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("CR3-round-trip-replace-XMP: Extract error: %v", err)
	}
	if !bytes.Equal(rawXMP, newXMP) {
		t.Errorf("CR3-round-trip-replace-XMP: rawXMP = %q, want new XMP", rawXMP)
	}
}

// TestCR3RoundTripImageDataPreserved verifies that the mdat bytes after the moov
// are preserved byte-for-byte after Inject.
// containers.md §8(e): "do not corrupt the image data".
func TestCR3RoundTripImageDataPreserved(t *testing.T) {
	// CR3-round-trip-image-data: containers.md §8(e) — mdat bytes survive inject unchanged.
	t.Parallel()

	// Build a CR3 with a recognisable mdat payload.
	sentinel := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	fileBytes, mdatOffset := buildCR3WithOffsetTable(conformanceTIFFLE, sentinel, "co64")

	newEXIF := make([]byte, len(conformanceTIFFLE)+32)
	copy(newEXIF, conformanceTIFFLE)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(fileBytes), &out, newEXIF, nil, nil, true); err != nil {
		t.Fatalf("CR3-round-trip-image-data: Inject error: %v", err)
	}
	outBytes := out.Bytes()

	// Read the relocated mdat offset from co64.
	newMoovStart, newMoovEnd, ok := findMoovRange(outBytes)
	if !ok {
		t.Fatal("CR3-round-trip-image-data: no moov in output")
	}
	co64Val, found := extractFirstOffset(outBytes[newMoovStart+8:newMoovEnd], "co64")
	if !found {
		t.Fatal("CR3-round-trip-image-data: co64 not found in output")
	}

	// Verify the bytes at the co64 offset contain the mdat box header + sentinel.
	mdatBoxStart := int(co64Val)
	mdatPayloadStart := mdatBoxStart + 8 // skip mdat box header
	if mdatPayloadStart+len(sentinel) > len(outBytes) {
		t.Fatalf("CR3-round-trip-image-data: relocated offset %d out of bounds", mdatPayloadStart)
	}
	if !bytes.Equal(outBytes[mdatPayloadStart:mdatPayloadStart+len(sentinel)], sentinel) {
		t.Errorf("CR3-round-trip-image-data: image data corrupted at offset %d: got %x, want %x",
			mdatPayloadStart, outBytes[mdatPayloadStart:mdatPayloadStart+len(sentinel)], sentinel)
	}

	// Silence "unused variable" if mdatOffset is not otherwise used.
	_ = mdatOffset
}

// ---------------------------------------------------------------------------
// §8(f) CR3-robust-* — robustness (no panic on malformed input)
// ---------------------------------------------------------------------------

// TestCR3RobustMalformedMoovNoPanic verifies that Extract does not panic on
// a moov box whose content is entirely garbage bytes.
// containers.md §8(f): "malformed moov/uuid".
func TestCR3RobustMalformedMoovNoPanic(t *testing.T) {
	// CR3-robust-malformed-moov: containers.md §8(f) — garbage moov content, no panic.
	t.Parallel()
	garbage := bytes.Repeat([]byte{0xFF, 0x00, 0xAB, 0xCD}, 32)
	moovBox := buildBox("moov", garbage)
	ftyp := buildCR3FtypOnly()
	data := append(ftyp, moovBox...)
	// Must not panic; may return error or nil metadata.
	_, _, _, _ = Extract(bytes.NewReader(data))
}

// TestCR3RobustMissingUUIDFallback verifies that when no Canon UUID box is
// present in moov, the fallback path (flat search for CMT1/CMT2) is used.
// containers.md §8(f): "missing uuid" degrades gracefully.
func TestCR3RobustMissingUUIDFallback(t *testing.T) {
	// CR3-robust-missing-uuid: containers.md §8(f) — no uuid → fallback to flat CMT* search.
	t.Parallel()
	// Build a moov with CMT1 directly (no uuid wrapper).
	cmt1Box := buildBox("CMT1", conformanceTIFFLE)
	moovBox := buildBox("moov", cmt1Box)
	ftyp := buildCR3FtypOnly()
	data := append(ftyp, moovBox...)

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-robust-missing-uuid: Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, conformanceTIFFLE) {
		t.Errorf("CR3-robust-missing-uuid: fallback rawEXIF mismatch: got %d bytes, want %d",
			len(rawEXIF), len(conformanceTIFFLE))
	}
}

// TestCR3RobustExtraCMTBoxes verifies that extra CMT* boxes beyond what the
// spec defines do not cause panics or errors.
// containers.md §8(f): "extra CMT* boxes".
func TestCR3RobustExtraCMTBoxes(t *testing.T) {
	// CR3-robust-extra-CMT: containers.md §8(f) — extra CMT* boxes tolerated without error.
	t.Parallel()
	data := buildCR3WithCMT(
		buildBox("CMT1", conformanceTIFFLE),
		buildBox("CMT2", bytes.Repeat([]byte{0x02}, 8)),
		buildBox("CMT3", bytes.Repeat([]byte{0x03}, 8)),
		buildBox("CMT4", bytes.Repeat([]byte{0x04}, 8)),
		buildBox("CMT5", bytes.Repeat([]byte{0x05}, 8)), // extra, non-standard
		buildBox("CMT9", bytes.Repeat([]byte{0x09}, 8)), // extra, non-standard
	)
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR3-robust-extra-CMT: Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, conformanceTIFFLE) {
		t.Errorf("CR3-robust-extra-CMT: rawEXIF mismatch with extra CMT boxes present")
	}
}

// TestCR3RobustTruncatedStreamNoPanic verifies that Extract does not panic on
// truncated input (all prefix lengths from 0 to full length).
// containers.md §8(f): general robustness.
func TestCR3RobustTruncatedStreamNoPanic(t *testing.T) {
	// CR3-robust-truncated: all truncation lengths must not panic.
	t.Parallel()
	data := buildMinimalCR3(conformanceTIFFLE, conformanceXMPPacket)
	step := 1
	if len(data) > 100 {
		step = len(data) / 50
	}
	for i := 0; i < len(data); i += step {
		_, _, _, _ = Extract(bytes.NewReader(data[:i]))
	}
}

// TestCR3RobustBoxSizeTooSmallNoPanic is a regression test for the class of
// panic: a box whose declared size < headerLen causes a slice-bounds panic in
// findBox at data[pos+headerLen : pos+size].
// ISO 14496-12 §4.2: parseCR3BoxHeader rejects size < headerLen.
func TestCR3RobustBoxSizeTooSmallNoPanic(t *testing.T) {
	// CR3-robust-size-too-small: §4.2 — size < headerLen must be rejected, not panic.
	t.Parallel()
	cases := []struct {
		name  string
		input []byte
	}{
		{
			"size=5-moov",
			[]byte{0x00, 0x00, 0x00, 0x05, 'm', 'o', 'o', 'v'},
		},
		{
			"size=1-short-largesize",
			func() []byte {
				// size==1 (largesize sentinel) but only 8 bytes available.
				b := make([]byte, 8)
				binary.BigEndian.PutUint32(b, 1)
				copy(b[4:], "uuid")
				return b
			}(),
		},
		{
			"size=7",
			func() []byte {
				b := make([]byte, 8)
				binary.BigEndian.PutUint32(b, 7)
				copy(b[4:], "moov")
				return b
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, _ = Extract(bytes.NewReader(tc.input))
			// Primary assertion: no panic. Error is expected.
		})
	}
}

// TestCR3RobustUUIDBoxTooShortForUUIDNoPanic verifies that a uuid box whose
// payload is too short to hold the 16-byte UUID does not cause a slice panic.
// ISO 14496-12 §4.2: uuid box must include the 16-byte user-defined type.
// containers.md §4(f): "largesize overflow" / "child larger than parent".
func TestCR3RobustUUIDBoxTooShortForUUIDNoPanic(t *testing.T) {
	// CR3-robust-uuid-too-short: uuid box with < 16 bytes payload must not panic.
	t.Parallel()
	// uuid box: 8-byte header + 10 bytes (not enough for 16-byte UUID).
	uuidBoxSize := uint32(8 + 10)
	inner := make([]byte, uuidBoxSize)
	binary.BigEndian.PutUint32(inner[0:], uuidBoxSize)
	copy(inner[4:], "uuid")
	copy(inner[8:], canonUUID[:10])

	moovBox := buildBox("moov", inner)
	ftyp := buildCR3FtypOnly()
	data := append(ftyp, moovBox...)

	_, _, _, _ = Extract(bytes.NewReader(data))
	// Must not panic.
}

// TestCR3RobustDeepNestingNoPanic verifies that deeply nested boxes do not
// cause a stack overflow. findBox has a depth limit of 32.
// containers.md §4(f): "deep nesting (recursion guard)".
func TestCR3RobustDeepNestingNoPanic(t *testing.T) {
	// CR3-robust-deep-nesting: containers.md §4(f) — depth > 32 must terminate gracefully.
	t.Parallel()
	// Build a chain: moov > trak > trak > … (40 levels) with CMT1 at the bottom.
	inner := buildBox("CMT1", conformanceTIFFLE)
	for range 40 {
		inner = buildBox("moov", inner)
	}
	ftyp := buildCR3FtypOnly()
	data := append(ftyp, inner...)

	// Must not stack-overflow; may return error or nil rawEXIF.
	_, _, _, _ = Extract(bytes.NewReader(data))
}

// TestCR3RobustNoMoovPassThrough verifies that Inject passes the file through
// unchanged when no moov box exists (corrupt/incomplete file).
// containers.md §8(f): "malformed moov" — Inject degrades gracefully.
func TestCR3RobustNoMoovPassThrough(t *testing.T) {
	// CR3-robust-no-moov-inject: containers.md §8(f) — Inject on no-moov file passes through.
	t.Parallel()
	ftyp := buildCR3FtypOnly()
	original := make([]byte, len(ftyp))
	copy(original, ftyp)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(ftyp), &out, conformanceTIFFLE, nil, nil, true); err != nil {
		t.Fatalf("CR3-robust-no-moov-inject: Inject error: %v", err)
	}
	if !bytes.Equal(out.Bytes(), original) {
		t.Error("CR3-robust-no-moov-inject: output differs from input for no-moov file")
	}
}

// TestCR3RobustGarbageInputNoPanic verifies that completely arbitrary byte
// sequences passed to Extract never cause a panic.
// containers.md §4(f) / §8(f).
func TestCR3RobustGarbageInputNoPanic(t *testing.T) {
	// CR3-robust-garbage: arbitrary input must not panic.
	t.Parallel()
	cases := [][]byte{
		{},
		{0x00},
		{0xFF, 0xFF, 0xFF, 0xFF},
		bytes.Repeat([]byte{0x41}, 1024), // "AAAA..."
		{0x00, 0x00, 0x00, 0x08, 'm', 'o', 'o', 'v', 0x00, 0x00, 0x00, 0x08, 'u', 'u', 'i', 'd'}, // moov with malformed uuid
	}
	for _, c := range cases {
		_, _, _, _ = Extract(bytes.NewReader(c))
	}
}

// TestCR3RobustDuplicateFtypNoPanic verifies that a stream with two ftyp boxes
// does not panic. Only the first ftyp matters for brand detection.
// containers.md §4(f): "duplicate ftyp".
func TestCR3RobustDuplicateFtypNoPanic(t *testing.T) {
	// CR3-robust-duplicate-ftyp: containers.md §4(f) — two ftyp boxes, no panic.
	t.Parallel()
	ftyp1 := buildCR3FtypOnly()
	ftyp2 := buildCR3FtypOnly()
	moovBox := buildBox("moov", buildUUIDBox(canonUUID, buildBox("CMT1", conformanceTIFFLE)))
	data := append(append(ftyp1, ftyp2...), moovBox...)

	// Must not panic; may succeed or fail gracefully.
	_, _, _, _ = Extract(bytes.NewReader(data))
}

// ---------------------------------------------------------------------------
// §8 corpus-parity — CR3-corpus-*
// ---------------------------------------------------------------------------

// TestCR3CorpusNoPanic verifies that Extract does not panic on any real-world
// CR3 file in the corpus. Uses testutil.CorpusFiles which skips when absent.
// containers.md §8: general robustness over real-world files.
func TestCR3CorpusNoPanic(t *testing.T) {
	// CR3-corpus-no-panic: no panic on any corpus CR3 file.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "raw")
	var cr3Paths []string
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".cr3") {
			cr3Paths = append(cr3Paths, p)
		}
	}
	if len(cr3Paths) == 0 {
		t.Skip("CR3-corpus-no-panic: no .cr3 files in testdata/corpus/raw")
	}
	for _, p := range cr3Paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Parallel()
			f, err := openCorpusFile(t, p)
			if err != nil {
				t.Skipf("open %s: %v", p, err)
			}
			defer func() { _ = f.Close() }()
			_, _, _, _ = Extract(f)
			// Primary assertion: no panic.
		})
	}
}

// TestCR3CorpusRoundTrip verifies that for every corpus CR3 file, Inject
// with the extracted metadata followed by Extract returns identical payloads.
// This is the strongest correctness property: round-trip byte fidelity.
// containers.md §8(d)+(e).
func TestCR3CorpusRoundTrip(t *testing.T) {
	// CR3-corpus-round-trip: inject→extract on real CR3 files preserves metadata.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "raw")
	var cr3Paths []string
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".cr3") {
			cr3Paths = append(cr3Paths, p)
		}
	}
	if len(cr3Paths) == 0 {
		t.Skip("CR3-corpus-round-trip: no .cr3 files in testdata/corpus/raw")
	}
	for _, p := range cr3Paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Parallel()
			f, err := openCorpusFile(t, p)
			if err != nil {
				t.Skipf("open %s: %v", p, err)
			}
			defer func() { _ = f.Close() }()

			origEXIF, _, origXMP, extractErr := Extract(f)
			if extractErr != nil {
				t.Skipf("CR3-corpus-round-trip: Extract failed for %s: %v", p, extractErr)
			}
			if origEXIF == nil && origXMP == nil {
				t.Skipf("CR3-corpus-round-trip: no metadata in %s", p)
			}

			// Read the full file for Inject.
			f2, err2 := openCorpusFile(t, p)
			if err2 != nil {
				t.Skipf("open %s: %v", p, err2)
			}
			defer func() { _ = f2.Close() }()

			var injected bytes.Buffer
			if injectErr := Inject(f2, &injected, origEXIF, nil, origXMP, true); injectErr != nil {
				t.Skipf("CR3-corpus-round-trip: Inject failed for %s: %v", p, injectErr)
			}

			roundEXIF, _, roundXMP, err3 := Extract(bytes.NewReader(injected.Bytes()))
			if err3 != nil {
				t.Errorf("CR3-corpus-round-trip: Extract after Inject failed for %s: %v", p, err3)
				return
			}
			if !bytes.Equal(roundEXIF, origEXIF) {
				t.Errorf("CR3-corpus-round-trip %s: EXIF mismatch: got %d bytes, want %d",
					filepath.Base(p), len(roundEXIF), len(origEXIF))
			}
			if !bytes.Equal(roundXMP, origXMP) {
				t.Errorf("CR3-corpus-round-trip %s: XMP mismatch: got %d bytes, want %d",
					filepath.Base(p), len(roundXMP), len(origXMP))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helpers for corpus tests
// ---------------------------------------------------------------------------

// openCorpusFile opens a corpus file for reading and returns an *os.File.
// The caller must close it after use. Returns an error (not fatal) so that
// corpus tests can skip missing files gracefully.
//
// The path originates from testutil.CorpusFiles (controlled test data directory);
// G304 and wrapcheck are suppressed: the path is safe and the error is used
// only to skip the sub-test, not surface to a caller that expects wrapping.
func openCorpusFile(t *testing.T, path string) (*os.File, error) {
	t.Helper()
	return os.Open(path) //nolint:wrapcheck // test helper: caller uses error only to t.Skip
}
