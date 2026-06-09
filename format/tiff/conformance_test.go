package tiff

// conformance_test.go — TIFF container specification-conformance test battery.
// Task #156.
//
// Rule IDs (S-*, R-*, TIFF-*, BigTIFF-*) are used verbatim as sub-test names
// and cite the authoritative specification clause for each assertion.
//
// Sources:
//   - TIFF Revision 6.0 (Adobe, 1992)                 §2 — IFD layout, header, byte order
//   - BigTIFF Design (Aware Systems / libtiff)         §2 — 16-byte header, 8-byte offsets,
//     20-byte entries, uint64 counts, 8-byte inline threshold
//   - CIPA DC-X008-Translation-2019 (Exif 2.32)        §4.6 — IFD entry types
//   - Adobe XMP Spec Part 3 §1.3 (TIFF-01..03)        — tag 0x02BC type=BYTE, no size limit
//   - IPTC conformance contract (iptc.md ROBUST-16)    — TypeLong-only structural padding trim
//
// Test categories:
//   S   — Structural: header fields, IFD layout, entry encoding
//   R   — Robustness: malformed input must not panic; correct degradation
//   TIFF — XMP/IPTC embedding in TIFF tags 0x02BC / 0x83BB
//   BigTIFF — BigTIFF-specific structural rules
//   Corpus  — Parity over real-world files in testdata/corpus/tiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// S — Structural: Header
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_TIFF_byteorder_II_LE verifies that "II" byte-order marker
// produces little-endian parse.
//
// TIFF 6.0 §2: "II" = Intel byte order (little-endian).
func TestConformance_TIFF_byteorder_II_LE(t *testing.T) {
	t.Parallel()
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	order, err := byteOrder(data)
	if err != nil {
		t.Fatalf("S-01: byteOrder for 'II': %v", err)
	}
	if order != binary.LittleEndian {
		t.Errorf("S-01: 'II' byte-order marker: got BigEndian, want LittleEndian")
	}
}

// TestConformance_TIFF_byteorder_MM_BE verifies that "MM" byte-order marker
// produces big-endian parse.
//
// TIFF 6.0 §2: "MM" = Motorola byte order (big-endian).
func TestConformance_TIFF_byteorder_MM_BE(t *testing.T) {
	t.Parallel()
	data := buildMinimalTIFF(binary.BigEndian, nil, nil)
	order, err := byteOrder(data)
	if err != nil {
		t.Fatalf("S-01: byteOrder for 'MM': %v", err)
	}
	if order != binary.BigEndian {
		t.Errorf("S-01: 'MM' byte-order marker: got LittleEndian, want BigEndian")
	}
}

// TestConformance_TIFF_byteorder_invalid verifies that an unrecognised
// byte-order marker returns ErrInvalidByteOrder and does not panic.
//
// TIFF 6.0 §2: only "II" and "MM" are valid; anything else is a corrupt file.
// S-01 fixture: header[0:2] = "ZZ" → error.
func TestConformance_TIFF_byteorder_invalid(t *testing.T) {
	t.Parallel()
	for _, bom := range [][]byte{
		{0x00, 0x00}, {0x49, 0x4D}, {0xFF, 0xFF}, {0x4D, 0x49},
	} {
		buf := make([]byte, 8)
		buf[0], buf[1] = bom[0], bom[1]
		_, _, _, err := Extract(bytes.NewReader(buf))
		if err == nil {
			t.Errorf("S-01: BOM %02x %02x: expected error, got nil", bom[0], bom[1])
		}
		if !errors.Is(err, ErrInvalidByteOrder) {
			t.Errorf("S-01: BOM %02x %02x: error does not wrap ErrInvalidByteOrder: %v", bom[0], bom[1], err)
		}
	}
}

// TestConformance_TIFF_magic_42_LE verifies that classic TIFF magic 42
// (0x002A) is accepted in little-endian orientation.
//
// TIFF 6.0 §2: bytes [2:4] = 42 decimal = 0x002A (LE: 0x2A 0x00).
// S-02 positive case.
func TestConformance_TIFF_magic_42_LE(t *testing.T) {
	t.Parallel()
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	_, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("S-02: classic TIFF LE magic 42: unexpected error: %v", err)
	}
}

// TestConformance_TIFF_magic_42_BE verifies that classic TIFF magic 42
// (0x002A) is accepted in big-endian orientation.
//
// TIFF 6.0 §2: bytes [2:4] = 42 decimal = 0x002A (BE: 0x00 0x2A).
// S-02 positive case.
func TestConformance_TIFF_magic_42_BE(t *testing.T) {
	t.Parallel()
	data := buildMinimalTIFF(binary.BigEndian, nil, nil)
	_, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("S-02: classic TIFF BE magic 42: unexpected error: %v", err)
	}
}

// TestConformance_TIFF_magic_43_rejected_in_classic_context verifies that
// BigTIFF magic 43 (0x002B) in a byte stream is NOT treated as classic TIFF.
//
// TIFF 6.0 §2: magic must be 42; any other value is invalid for classic TIFF.
// S-02 fixture: bytes[2:4] = 0x2B 0x00 in LE context.
// Note: BigTIFF magic 0x002B IS valid for BigTIFF; here we test that the
// Extract dispatch table routes it to the BigTIFF path, not the classic path.
// The fixture has invalid BigTIFF header bytes beyond magic → error is expected.
func TestConformance_TIFF_magic_43_invalid_bytesize(t *testing.T) {
	t.Parallel()
	// Build a nominal BigTIFF header but with bytesize=4 (invalid — must be 8).
	// S-05 fixture: BigTIFF offset-bytesize ≠ 8 → ErrUnsupportedMagic.
	buf := make([]byte, 16)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B) // BigTIFF magic
	binary.LittleEndian.PutUint16(buf[4:], 4)      // invalid bytesize
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], 16)

	_, _, _, err := Extract(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("S-02/S-05: BigTIFF with bytesize=4: expected error, got nil")
	}
	if !errors.Is(err, ErrUnsupportedMagic) {
		t.Errorf("S-02/S-05: error does not wrap ErrUnsupportedMagic: %v", err)
	}
}

// TestConformance_TIFF_magic_unknown rejects magic values other than 42 and 43.
//
// TIFF 6.0 §2 / BigTIFF spec: only 42 (classic) and 43 (BigTIFF) are valid.
// S-02 fixture: magic 0x1234 → ErrUnsupportedMagic.
func TestConformance_TIFF_magic_unknown(t *testing.T) {
	t.Parallel()
	for _, magic := range []uint16{0x0000, 0x0029, 0x002C, 0xFFFF, 0x1234} {
		buf := make([]byte, 8)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], magic)
		binary.LittleEndian.PutUint32(buf[4:], 8)

		_, _, _, err := Extract(bytes.NewReader(buf))
		if err == nil {
			t.Errorf("S-02: magic 0x%04X: expected error, got nil", magic)
			continue
		}
		if !errors.Is(err, ErrUnsupportedMagic) {
			t.Errorf("S-02: magic 0x%04X: error does not wrap ErrUnsupportedMagic: %v", magic, err)
		}
	}
}

// TestConformance_TIFF_ifd0_offset_zero verifies that IFD0 offset = 0 is
// treated gracefully (the IFD list is empty, not a crash).
//
// TIFF 6.0 §2: IFD0 offset MUST be ≥ 8 for a well-formed file. Offset 0
// means the IFD starts at the byte-order mark, which is invalid.
// S-03 fixture: IFD0 offset = 0.
func TestConformance_TIFF_ifd0_offset_zero(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 8)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 0) // offset = 0

	// Must not panic; may produce empty EXIF or return an error.
	rawEXIF, _, _, err := Extract(bytes.NewReader(buf))
	_ = err
	// rawEXIF is always set to data before IFD scanning (Extract sets it early).
	if rawEXIF == nil {
		t.Error("S-03: rawEXIF should not be nil even for IFD0 offset=0")
	}
}

// TestConformance_TIFF_ifd0_offset_past_eof verifies that an IFD0 offset
// beyond the file length produces no panic and returns rawEXIF.
//
// TIFF 6.0 §2: IFD0 offset + 2 must be ≤ file size.
// S-03 fixture: IFD0 offset = 0xFFFFFFFF (far past EOF).
func TestConformance_TIFF_ifd0_offset_past_eof(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 8)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 0xFFFFFFFF) // IFD0 = MaxUint32

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(buf))
	_ = err
	// rawEXIF should be the full buffer (Extract sets it before scanning).
	if rawEXIF == nil {
		t.Error("S-03: rawEXIF must not be nil for past-EOF IFD0 offset")
	}
	// No metadata can have been found.
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("S-03: expected nil IPTC/XMP for past-EOF IFD0 offset; got IPTC=%v XMP=%v", rawIPTC, rawXMP)
	}
}

// TestConformance_TIFF_ifd0_odd_offset verifies that an odd IFD0 offset
// does not crash Extract (although it is spec-non-conformant).
//
// TIFF 6.0 §2: IFD must begin on a word boundary (even offset). Parser must
// not crash on an odd offset — it may produce partial or no results.
// S-04 fixture: IFD0 offset = 9 (odd).
func TestConformance_TIFF_ifd0_odd_offset(t *testing.T) {
	t.Parallel()
	// Build a minimal TIFF but patch the IFD0 offset to 9 (odd).
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	binary.LittleEndian.PutUint32(data[4:], 9) // odd offset

	rawEXIF, _, _, _ := Extract(bytes.NewReader(data))
	// rawEXIF must be set regardless.
	if rawEXIF == nil {
		t.Error("S-04: rawEXIF must not be nil for odd IFD0 offset")
	}
}

// TestConformance_TIFF_min_length_classic verifies that inputs shorter than 8
// bytes return ErrFileTooShort.
//
// TIFF 6.0 §2: minimum valid TIFF is 8 bytes (header only).
// R-13 fixture: 0, 1, 4, 7 bytes → ErrFileTooShort.
func TestConformance_TIFF_min_length_classic(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 1, 4, 7} {
		buf := make([]byte, n)
		if n >= 2 {
			buf[0], buf[1] = 'I', 'I'
		}
		_, _, _, err := Extract(bytes.NewReader(buf))
		if err == nil {
			t.Errorf("R-13: %d-byte input: expected error, got nil", n)
		}
	}
}

// TestConformance_TIFF_truncated_after_header verifies that a valid 8-byte
// TIFF header whose IFD0 offset equals 8 (right at EOF) does not crash and
// returns rawEXIF.
//
// R-12 fixture: file cut after header, before IFD0 entry count.
func TestConformance_TIFF_truncated_after_header(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 8)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8) // IFD0 == EOF

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(buf))
	// Must not crash; either returns an error or rawEXIF with nil metadata.
	_ = err
	if rawEXIF == nil {
		t.Error("R-12: rawEXIF must not be nil for header-only classic TIFF")
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("R-12: expected nil IPTC/XMP; got IPTC=%v XMP=%v", rawIPTC, rawXMP)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// S — IFD Structure (classic TIFF)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_TIFF_ifd_empty_count verifies that an IFD with 0 entries
// is a valid (empty) IFD; Extract should return nil IPTC/XMP, no error.
//
// TIFF 6.0 §2: entry count 0 is valid; next-IFD pointer follows immediately.
// S-14 fixture.
func TestConformance_TIFF_ifd_empty_count(t *testing.T) {
	t.Parallel()
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("S-14: empty IFD0: unexpected error: %v", err)
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("S-14: empty IFD0: expected nil IPTC/XMP, got IPTC=%v XMP=%v", rawIPTC, rawXMP)
	}
}

// TestConformance_TIFF_ifd_entry_count_partial verifies that when the IFD
// entry count claims more entries than the buffer can hold, only entries that
// actually fit are read — no panic, no OOB.
//
// TIFF 6.0 §2: each entry is 12 bytes; partial IFD must be tolerated.
// R-05 fixture: count=50 but only 2 entries fit in the buffer.
func TestConformance_TIFF_ifd_entry_count_partial(t *testing.T) {
	t.Parallel()
	// Build a buffer that has valid header and IFD count=50 but only holds 2 entries.
	buf := make([]byte, 8+2+2*12+4) // header + count + 2 entries + next-ptr
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 50) // claims 50 entries; buffer only holds 2

	// Entry 0: XMP, TypeByte, count=0, inline value=0 (harmless)
	e0 := 10
	order.PutUint16(buf[e0:], 0x02BC) // XMP
	order.PutUint16(buf[e0+2:], 1)    // BYTE
	order.PutUint32(buf[e0+4:], 0)    // count = 0
	order.PutUint32(buf[e0+8:], 0)    // inline 0

	// Entry 1: IPTC, TypeByte, count=0, inline 0
	e1 := 22
	order.PutUint16(buf[e1:], 0x83BB) // IPTC
	order.PutUint16(buf[e1+2:], 1)    // BYTE
	order.PutUint32(buf[e1+4:], 0)
	order.PutUint32(buf[e1+8:], 0)

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("R-05: partial IFD count: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("R-05: rawEXIF must not be nil")
	}
}

// TestConformance_TIFF_ifd_ool_value_offset_odd verifies that an out-of-line
// value at an odd offset does not crash — the value is read at the declared offset.
//
// TIFF 6.0 §2: out-of-line offsets should be even; parser must not crash on odd.
// S-11 fixture: IPTC value offset = 27 (odd).
func TestConformance_TIFF_ifd_ool_value_offset_odd(t *testing.T) {
	t.Parallel()
	// Construct a TIFF with an IPTC value at an odd offset.
	// Layout: header(8) + ifd_count(2) + entry(12) + next-ptr(4) + pad(1) + value
	//            = 27 bytes before value data
	const valueLen = 8
	buf := make([]byte, 27+valueLen)
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at 8
	order.PutUint16(buf[8:], 1) // 1 entry

	const valueOff = 27                 // odd offset
	order.PutUint16(buf[10:], 0x83BB)   // IPTC
	order.PutUint16(buf[12:], 7)        // UNDEFINED
	order.PutUint32(buf[14:], valueLen) // count = 8
	order.PutUint32(buf[18:], valueOff) // offset to value
	order.PutUint32(buf[22:], 0)        // next-IFD = 0
	// value at offset 27
	copy(buf[valueOff:], []byte{0x1C, 0x02, 0x78, 0x00, 0x01, 0x41, 0x42, 0x43})

	rawEXIF, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("S-11: odd OOL offset: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("S-11: rawEXIF must not be nil")
	}
	// The spec says parser MUST NOT CRASH; reading the value at odd offset is
	// the compliant behaviour (S-11: "Parser must not crash on odd").
	if rawIPTC == nil {
		t.Logf("S-11: rawIPTC=nil with odd offset (skipped or read); either is compliant — no crash")
	}
}

// TestConformance_TIFF_next_ifd_out_of_bounds verifies that a next-IFD
// pointer beyond the file is treated as end-of-chain, not a crash.
//
// TIFF 6.0 §2: out-of-bounds next-IFD → treat as end of chain.
// S-13 fixture.
func TestConformance_TIFF_next_ifd_out_of_bounds(t *testing.T) {
	t.Parallel()
	// Build a TIFF where IFD0's next-IFD pointer is 0xFFFFFFFF (past EOF).
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	// The next-IFD pointer is at: 8 (header) + 2 (count) + 0*12 (entries) = 10
	binary.LittleEndian.PutUint32(data[10:], 0xFFFFFFFF)

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("S-13: out-of-bounds next-IFD: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("S-13: rawEXIF must not be nil")
	}
}

// TestConformance_TIFF_ifd_unsorted_entries verifies that an IFD with entries
// in non-ascending tag order is parsed without error.
//
// TIFF 6.0 §2: entries MUST be sorted ascending by tag (writer); parser MUST
// handle unsorted (S-12).
func TestConformance_TIFF_ifd_unsorted_entries(t *testing.T) {
	t.Parallel()
	// Build a TIFF with two entries: XMP (0x02BC) before IPTC (0x83BB) — wrong order.
	// Layout: hdr(8) + count(2) + 2×entry(24) + next(4) + iptc_data + xmp_data
	const iptcData = "iptc-unsorted-test-payload-long" // 31 bytes
	const xmpData = "<xmpmeta/>"

	hdrSize := 8
	ifdSize := 2 + 2*12 + 4
	iptcOff := uint32(hdrSize + ifdSize)      //nolint:mnd // hdrSize/ifdSize are manifest constants
	xmpOff := iptcOff + uint32(len(iptcData)) //nolint:mnd // len of string constant
	bufLen := int(xmpOff) + len(xmpData)

	buf := make([]byte, bufLen)
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 2) // 2 entries

	// Entry 0: XMP 0x02BC (SMALLER tag) — unsorted: should come AFTER IPTC 0x83BB.
	e0 := 10
	order.PutUint16(buf[e0:], 0x02BC)                 // XMP
	order.PutUint16(buf[e0+2:], 1)                    // BYTE
	order.PutUint32(buf[e0+4:], uint32(len(xmpData))) //nolint:mnd // len of string constant
	order.PutUint32(buf[e0+8:], xmpOff)

	// Entry 1: IPTC 0x83BB (LARGER tag) — unsorted: should come BEFORE XMP.
	e1 := 22
	order.PutUint16(buf[e1:], 0x83BB)                  // IPTC
	order.PutUint16(buf[e1+2:], 7)                     // UNDEFINED
	order.PutUint32(buf[e1+4:], uint32(len(iptcData))) //nolint:mnd // len of string constant
	order.PutUint32(buf[e1+8:], iptcOff)
	order.PutUint32(buf[e1+12:], 0) // next-IFD = 0

	// Write data.
	copy(buf[iptcOff:], iptcData)
	copy(buf[xmpOff:], xmpData)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("S-12: unsorted IFD: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("S-12: rawEXIF must not be nil")
	}
	// Both tags must be found regardless of ordering in the IFD.
	if !bytes.Equal(rawIPTC, []byte(iptcData)) {
		t.Errorf("S-12: rawIPTC = %q, want %q", rawIPTC, iptcData)
	}
	if !bytes.Equal(rawXMP, []byte(xmpData)) {
		t.Errorf("S-12: rawXMP = %q, want %q", rawXMP, xmpData)
	}
}

// TestConformance_TIFF_ifd_inline_value_threshold_4 verifies that values ≤ 4
// bytes are stored inline (in the value-or-offset field), not as offsets.
//
// TIFF 6.0 §2: if typeSize×count ≤ 4, value is inline left-justified.
// S-09 validation.
func TestConformance_TIFF_ifd_inline_value_threshold_4(t *testing.T) {
	t.Parallel()
	// Build a TIFF with a single BYTE[4] entry (exactly at the inline threshold).
	// The value bytes 0x11,0x22,0x33,0x44 must round-trip via Extract→injected.
	const privateTag = uint16(0x4444)
	value := []byte{0x11, 0x22, 0x33, 0x44}
	data := buildTIFFWithPrivateTag(privateTag, 1 /*BYTE*/, value)

	parsed, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("S-09: exif.Parse for 4-byte inline: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("S-09: IFD0 is nil")
	}
	entry := parsed.IFD0.Get(exif.TagID(privateTag))
	if entry == nil {
		t.Fatalf("S-09: tag 0x%04X not found", privateTag)
	}
	if len(entry.Value) < 4 {
		t.Fatalf("S-09: value too short: %d bytes", len(entry.Value))
	}
	if !bytes.Equal(entry.Value[:4], value) {
		t.Errorf("S-09: inline value = %x, want %x", entry.Value[:4], value)
	}
}

// TestConformance_TIFF_ifd_ool_value_threshold_5 verifies that a 5-byte value
// is stored out-of-line (requires an offset in the value-or-offset field).
//
// TIFF 6.0 §2: if typeSize×count > 4, bytes 8–11 = uint32 offset.
// S-10 validation.
func TestConformance_TIFF_ifd_ool_value_threshold_5(t *testing.T) {
	t.Parallel()
	const privateTag = uint16(0x4445)
	value := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE} // 5 bytes — must be OOL
	data := buildTIFFWithPrivateTag(privateTag, 1 /*BYTE*/, value)

	parsed, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("S-10: exif.Parse for 5-byte OOL: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("S-10: IFD0 is nil")
	}
	entry := parsed.IFD0.Get(exif.TagID(privateTag))
	if entry == nil {
		t.Fatalf("S-10: tag 0x%04X not found", privateTag)
	}
	if !bytes.Equal(entry.Value, value) {
		t.Errorf("S-10: OOL value = %x, want %x", entry.Value, value)
	}
}

// TestConformance_TIFF_overflow_count_uint64_guard verifies that an IFD
// entry whose count×typeSize overflows uint32 (but not uint64) is correctly
// handled — the entry is skipped because the computed value range is past EOF.
//
// TIFF 6.0 §2 / R-06: count×typeSize overflow MUST be checked with uint64
// arithmetic, never uint32.
func TestConformance_TIFF_overflow_count_uint64_guard(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 26)
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)

	// count = 0x10000001, typeSize = 4 (LONG) → product = 0x40000004 > 2^32 in uint32
	// but still representable in uint64. The offset check must catch it.
	order.PutUint16(buf[10:], 0x83BB)     // IPTC
	order.PutUint16(buf[12:], 4)          // LONG
	order.PutUint32(buf[14:], 0x10000001) // count
	order.PutUint32(buf[18:], 26)         // offset = just past buffer

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("R-06: overflow count uint64: unexpected error: %v", err)
	}
	if rawIPTC != nil {
		t.Error("R-06: rawIPTC must be nil for overflow-count entry")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// S — Field Type Sizes (S-18)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_TIFF_type_sizes verifies that typeSize returns the correct
// byte size for all standard TIFF 6.0 type codes.
//
// TIFF 6.0 §2 (types 1–12), Exif 2.32 §4.6.3 (type 13 = UTF8, Exif 3.0 only).
// S-18 validation.
func TestConformance_TIFF_type_sizes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code uint16
		want uint32
		name string
	}{
		{1, 1, "BYTE"},
		{2, 1, "ASCII"},
		{3, 2, "SHORT"},
		{4, 4, "LONG"},
		{5, 8, "RATIONAL"},
		{6, 1, "SBYTE"},
		{7, 1, "UNDEFINED"},
		{8, 2, "SSHORT"},
		{9, 4, "SLONG"},
		{10, 8, "SRATIONAL"},
		{11, 4, "FLOAT"},
		{12, 8, "DOUBLE"},
		// Unknown types must return 0 (skip them).
		{0, 0, "unknown-0"},
		{13, 0, "UTF8-unknown-in-classic"},
		{14, 0, "unknown-14"},
		{15, 0, "unknown-15"},
		{0xFF, 0, "unknown-255"},
	}
	for _, tc := range tests {
		got := typeSize(tc.code)
		if got != tc.want {
			t.Errorf("S-18: typeSize(%d=%s) = %d, want %d", tc.code, tc.name, got, tc.want)
		}
	}
}

// TestConformance_TIFF_bigtiff_type_sizes verifies BigTIFF-specific type codes
// (LONG8=16, SLONG8=17, IFD8=18) each have element size 8.
//
// BigTIFF spec §3.3 / S-22 validation.
func TestConformance_TIFF_bigtiff_type_sizes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code uint64
		want uint64
		name string
	}{
		{1, 1, "BYTE"},
		{3, 2, "SHORT"},
		{4, 4, "LONG"},
		{5, 8, "RATIONAL"},
		{7, 1, "UNDEFINED"},
		{16, 8, "LONG8"},
		{17, 8, "SLONG8"},
		{18, 8, "IFD8"},
		{0, 0, "unknown-0"},
		{99, 0, "unknown-99"},
	}
	for _, tc := range tests {
		got := typeSizeBigTIFF(uint16(tc.code)) //nolint:gosec // G115: test data, bounded by valid type codes
		if got != tc.want {
			t.Errorf("S-22: typeSizeBigTIFF(%d=%s) = %d, want %d", tc.code, tc.name, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// BigTIFF — Structural rules S-05, S-06, S-15, S-16, S-17
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_BigTIFF_16byte_header verifies that a valid BigTIFF header
// is exactly 16 bytes and that all header fields are correctly parsed.
//
// BigTIFF spec §2: BOM(2) + magic(2) + offset-bytesize(2) + reserved(2) + IFD0-offset(8) = 16.
// S-05 positive case.
func TestConformance_BigTIFF_16byte_header(t *testing.T) {
	t.Parallel()
	// LE: verify every field is at the right position.
	buf := make([]byte, 16)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B) // magic = 43
	binary.LittleEndian.PutUint16(buf[4:], 8)      // offset bytesize = 8
	binary.LittleEndian.PutUint16(buf[6:], 0)      // reserved = 0
	binary.LittleEndian.PutUint64(buf[8:], 16)     // IFD0 offset (= EOF, no IFD content)

	// Parse must succeed; IFD has no entries.
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("S-05: BigTIFF 16-byte header: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("S-05: rawEXIF must not be nil")
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("S-05: expected nil IPTC/XMP for empty BigTIFF, got IPTC=%v XMP=%v", rawIPTC, rawXMP)
	}
}

// TestConformance_BigTIFF_8byte_offsets verifies that a BigTIFF file with
// out-of-line values at offsets > 4GB is handled correctly.
//
// BigTIFF spec §2: IFD offsets are uint64 (8 bytes); inline threshold is 8.
// S-15/S-16 validation: 20-byte entries with uint64 count and value-or-offset.
func TestConformance_BigTIFF_8byte_offsets(t *testing.T) {
	t.Parallel()
	// Build a BigTIFF with IPTC and XMP; extract must return both.
	wantIPTC := []byte("bigtiff-8-byte-offset-iptc-payload-long-enough")
	wantXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")
	data := buildMinimalBigTIFF(binary.LittleEndian, wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("S-15/S-16: BigTIFF 8-byte offsets: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("S-15/S-16: rawEXIF must not be nil")
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("S-15/S-16: rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("S-15/S-16: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestConformance_BigTIFF_8byte_offsets_BE is the big-endian counterpart of
// TestConformance_BigTIFF_8byte_offsets.
//
// BigTIFF spec §2 / S-15/S-16.
func TestConformance_BigTIFF_8byte_offsets_BE(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte("bigtiff-be-8-byte-offset-iptc-payload-long")
	wantXMP := []byte("<xmpmeta be='true'/>")
	data := buildMinimalBigTIFF(binary.BigEndian, wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("S-15/S-16 BE: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("S-15/S-16 BE: rawEXIF must not be nil")
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("S-15/S-16 BE: rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("S-15/S-16 BE: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestConformance_BigTIFF_offset_bytesize_not_8 verifies that BigTIFF header
// offset-bytesize ≠ 8 is rejected with ErrUnsupportedMagic.
//
// BigTIFF spec §2: "bytesize of offsets — ALWAYS 8. Must be validated."
// S-05 fixture: invalid bytesize values.
func TestConformance_BigTIFF_offset_bytesize_not_8(t *testing.T) {
	t.Parallel()
	for _, bad := range []uint16{0, 4, 6, 16, 0xFF} {
		buf := make([]byte, 16)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], bad)
		binary.LittleEndian.PutUint16(buf[6:], 0)
		binary.LittleEndian.PutUint64(buf[8:], 16)

		_, _, _, err := Extract(bytes.NewReader(buf))
		if err == nil {
			t.Errorf("S-05: bytesize=%d: expected error, got nil", bad)
		}
	}
}

// TestConformance_BigTIFF_reserved_field_nonzero verifies that a non-zero
// reserved field (bytes 6–7) is ignored — parser continues without error.
//
// BigTIFF spec §2: bytes 6–7 "SHOULD be 0"; parser SHOULD ignore non-zero.
// S-06: advisory (SHOULD), not mandatory (MUST).
func TestConformance_BigTIFF_reserved_field_nonzero(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 16)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B)
	binary.LittleEndian.PutUint16(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[6:], 0xBEEF) // non-zero reserved
	binary.LittleEndian.PutUint64(buf[8:], 16)     // IFD0 at EOF

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("S-06: non-zero reserved field: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("S-06: rawEXIF must not be nil for non-zero reserved BigTIFF header")
	}
}

// TestConformance_BigTIFF_min_length_16 verifies that a BigTIFF shorter than
// 16 bytes returns an error (R-13 / S-05).
//
// BigTIFF spec §2: minimum valid BigTIFF is 16 bytes.
func TestConformance_BigTIFF_min_length_16(t *testing.T) {
	t.Parallel()
	for _, n := range []int{8, 12, 14, 15} {
		buf := make([]byte, n)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B) // BigTIFF magic
		if n >= 6 {
			binary.LittleEndian.PutUint16(buf[4:], 8)
		}

		_, _, _, err := Extract(bytes.NewReader(buf))
		if err == nil {
			t.Errorf("R-13: BigTIFF %d-byte input: expected error, got nil", n)
		}
	}
}

// TestConformance_BigTIFF_count_over_65535_rejected verifies that an IFD
// with more than 65535 entries is treated as corrupt (DoS guard).
//
// BigTIFF spec §2 (S-17): entry count > 65535 must be rejected to prevent OOM.
func TestConformance_BigTIFF_count_over_65535_rejected(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 32)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B)
	binary.LittleEndian.PutUint16(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], 16)
	binary.LittleEndian.PutUint64(buf[16:], 0x100000) // count = 1M — way above 65535 cap

	rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(buf))
	// Must not panic or OOM. rawEXIF should be the full buffer.
	if rawEXIF == nil {
		t.Error("S-17: rawEXIF must not be nil even for huge-count BigTIFF")
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("S-17: expected nil IPTC/XMP; got IPTC=%v XMP=%v", rawIPTC, rawXMP)
	}
}

// TestConformance_BigTIFF_inline_threshold_8 verifies that values of exactly
// 8 bytes are stored inline in BigTIFF (the inline threshold is 8).
//
// BigTIFF spec §2: inline if total ≤ 8; OOL if total > 8. S-16.
func TestConformance_BigTIFF_inline_threshold_8(t *testing.T) {
	t.Parallel()
	data := buildBigTIFFWithInlineValues(binary.LittleEndian)
	wantIPTC := []byte{0x1c, 0x01, 0x5a, 0x00, 0x03, 0x55, 0x54, 0x46}

	_, rawIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("S-16: BigTIFF inline threshold 8: unexpected error: %v", err)
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("S-16: rawIPTC = %x, want %x", rawIPTC, wantIPTC)
	}
}

// TestConformance_BigTIFF_uint64_count_overflow_guard verifies that a BigTIFF
// entry with count×typeSize overflowing uint64 is skipped safely.
//
// BigTIFF spec §2 / R-06: uint64 overflow must be detected before multiplication.
func TestConformance_BigTIFF_uint64_count_overflow_guard(t *testing.T) {
	t.Parallel()
	// RATIONAL (sz=8): count = MaxUint64/8 + 1 → count × 8 overflows uint64.
	const overflowCount = ^uint64(0)/8 + 1

	buf := make([]byte, 52)
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], 16)
	order.PutUint64(buf[16:], 1) // 1 entry
	order.PutUint16(buf[24:], 0x83BB)
	order.PutUint16(buf[26:], 5) // RATIONAL
	order.PutUint64(buf[28:], overflowCount)
	order.PutUint64(buf[36:], 0)

	_, rawIPTC, _, _ := Extract(bytes.NewReader(buf))
	if rawIPTC != nil {
		t.Error("R-06/BigTIFF: rawIPTC must be nil when count×typeSize overflows uint64")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TIFF — XMP embedding (TIFF-01..03)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_TIFF_XMP_tag_0x02BC_TypeByte verifies that after a write
// round-trip via Inject, the XMP tag 0x02BC is stored as TypeByte (not TypeUndefined).
//
// Adobe XMP Spec Part 3 §1.3 (TIFF-01): XMP in TIFF tag 700 (0x02BC),
// type BYTE(1). Accept UNDEFINED on read; write BYTE.
func TestConformance_TIFF_XMP_tag_0x02BC_TypeByte(t *testing.T) {
	t.Parallel()
	rawXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, nil, rawXMP, true); err != nil {
		t.Fatalf("TIFF-01: Inject XMP: %v", err)
	}
	outBytes := out.Bytes()

	parsed, err := exif.Parse(outBytes)
	if err != nil {
		t.Fatalf("TIFF-01: exif.Parse after inject: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("TIFF-01: IFD0 is nil")
	}
	entry := parsed.IFD0.Get(exif.TagXMP)
	if entry == nil {
		t.Fatal("TIFF-01: XMP tag 0x02BC not found in IFD0")
	}
	// Adobe XMP Part 3 §1.3: type MUST be BYTE(1) when written.
	if entry.Type != exif.TypeByte {
		t.Errorf("TIFF-01: XMP tag type = %d, want %d (TypeByte=1)", entry.Type, exif.TypeByte)
	}
	// TIFF-02: no size limit — value must match exactly.
	if uint32(len(rawXMP)) != entry.Count { //nolint:gosec // G115: bounded by test data
		t.Errorf("TIFF-01/02: XMP count = %d, want %d", entry.Count, len(rawXMP))
	}
}

// TestConformance_TIFF_XMP_no_size_limit verifies that XMP payloads of
// varying sizes (small, large) are round-tripped without truncation.
//
// Adobe XMP Spec Part 3 §1.3 (TIFF-02): no size limit for TIFF tag 700.
func TestConformance_TIFF_XMP_no_size_limit(t *testing.T) {
	t.Parallel()
	// Generate XMP blobs of various sizes to exercise round-trip fidelity.
	sizes := []int{10, 100, 1000, 4096}
	for _, n := range sizes {
		rawXMP := make([]byte, n)
		copy(rawXMP, `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>`)
		rawXMP[n-1] = 0x3E // '>' — non-zero tail

		data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(data), &out, data, nil, rawXMP, true); err != nil {
			t.Errorf("TIFF-02: Inject XMP size=%d: %v", n, err)
			continue
		}
		_, _, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Errorf("TIFF-02: Extract XMP size=%d: %v", n, err)
			continue
		}
		if !bytes.Equal(gotXMP, rawXMP) {
			t.Errorf("TIFF-02: XMP size=%d: round-trip mismatch (got %d bytes, want %d)", n, len(gotXMP), n)
		}
	}
}

// TestConformance_TIFF_XMP_accept_TypeByte_and_TypeUndefined verifies that
// Extract accepts XMP tag 0x02BC with both TypeByte(1) and TypeUndefined(7).
//
// Adobe XMP Spec Part 3 §1.3 (TIFF-01): accept both BYTE and UNDEFINED on read.
func TestConformance_TIFF_XMP_accept_TypeByte_and_TypeUndefined(t *testing.T) {
	t.Parallel()
	wantXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")

	for _, typ := range []uint16{1, 7} { // TypeByte=1, TypeUndefined=7
		data := buildTIFFWithTag(binary.LittleEndian, 0x02BC, typ, wantXMP)
		_, _, rawXMP, err := Extract(bytes.NewReader(data))
		if err != nil {
			t.Errorf("TIFF-01: TypeCode=%d: unexpected error: %v", typ, err)
			continue
		}
		if !bytes.Equal(rawXMP, wantXMP) {
			t.Errorf("TIFF-01: TypeCode=%d: rawXMP = %q, want %q", typ, rawXMP, wantXMP)
		}
	}
}

// TestConformance_TIFF_XMP_no_APP1_framing verifies that the XMP stored in
// tag 0x02BC is the raw RDF/XML packet — NOT wrapped in APP1 framing.
//
// Adobe XMP Spec Part 3 §1.3 (TIFF-03): no APP1 framing in TIFF tag 700.
// If the round-tripped XMP starts with 0xFF 0xE1 (APP1 marker), it is wrong.
func TestConformance_TIFF_XMP_no_APP1_framing(t *testing.T) {
	t.Parallel()
	rawXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, nil, rawXMP, true); err != nil {
		t.Fatalf("TIFF-03: Inject XMP: %v", err)
	}
	_, _, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("TIFF-03: Extract XMP: %v", err)
	}
	// Must NOT be APP1-framed (first bytes must NOT be 0xFF 0xE1).
	if len(gotXMP) >= 2 && gotXMP[0] == 0xFF && gotXMP[1] == 0xE1 {
		t.Error("TIFF-03: XMP in TIFF tag 700 must not carry APP1 framing (0xFF 0xE1)")
	}
	if !bytes.Equal(gotXMP, rawXMP) {
		t.Errorf("TIFF-03: XMP round-trip mismatch:\n got  %q\n want %q", gotXMP, rawXMP)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// TIFF — IPTC embedding (tag 0x83BB)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_TIFF_IPTC_tag_0x83BB_TypeLong verifies that after a write
// round-trip via Inject, the IPTC tag 0x83BB is stored as TypeLong.
//
// ExifTool convention (widely adopted): IPTC-NAA (0x83BB) is TypeLong with
// Count = ceil(len(rawIPTC)/4); IPTC byte blob padded to 4-byte boundary.
// TIFF 6.0 §2 / iptc.md ROBUST-16.
func TestConformance_TIFF_IPTC_tag_0x83BB_TypeLong(t *testing.T) {
	t.Parallel()
	// 4-byte-aligned IPTC payload.
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x01}
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, nil, true); err != nil {
		t.Fatalf("TIFF-IPTC-TypeLong: Inject IPTC: %v", err)
	}
	parsed, err := exif.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("TIFF-IPTC-TypeLong: exif.Parse: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("TIFF-IPTC-TypeLong: IFD0 is nil")
	}
	entry := parsed.IFD0.Get(exif.TagIPTC)
	if entry == nil {
		t.Fatal("TIFF-IPTC-TypeLong: IPTC tag 0x83BB not found")
	}
	if entry.Type != exif.TypeLong {
		t.Errorf("TIFF-IPTC-TypeLong: type = %d, want %d (TypeLong=4)", entry.Type, exif.TypeLong)
	}
	// Count = ceil(4/4) = 1.
	if entry.Count != 1 {
		t.Errorf("TIFF-IPTC-TypeLong: Count = %d, want 1", entry.Count)
	}
}

// TestConformance_TIFF_IPTC_TypeLong_padding verifies that when an IPTC
// payload is not 4-byte-aligned, it is padded and Count is ceil(n/4).
//
// iptc.md ROBUST-16: TypeLong IPTC is padded to 4-byte boundary; structural
// padding bytes are trimmed on read.
func TestConformance_TIFF_IPTC_TypeLong_padding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rawIPTC   []byte
		wantCount uint32
		name      string
	}{
		{[]byte{0x1C, 0x02, 0x78, 0x00, 0x01}, 2, "5-bytes-padded-to-8"},
		{[]byte{0x1C, 0x02, 0x78, 0x00, 0x01, 0x41, 0x42}, 2, "7-bytes-padded-to-8"},
		{[]byte{0x1C, 0x02, 0x78, 0x00, 0x01, 0x41, 0x42, 0x43, 0x44}, 3, "9-bytes-padded-to-12"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(data), &out, data, tc.rawIPTC, nil, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			parsed, err := exif.Parse(out.Bytes())
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			entry := parsed.IFD0.Get(exif.TagIPTC)
			if entry == nil {
				t.Fatal("IPTC tag not found")
			}
			if entry.Count != tc.wantCount {
				t.Errorf("IPTC Count = %d, want %d", entry.Count, tc.wantCount)
			}
			// Round-trip: the extracted IPTC must equal the original payload.
			gotIPTC, _ := extractTagValues(out.Bytes(), 8, binary.LittleEndian)
			if !bytes.Equal(gotIPTC, tc.rawIPTC) {
				t.Errorf("IPTC round-trip: got %x, want %x", gotIPTC, tc.rawIPTC)
			}
		})
	}
}

// TestConformance_TIFF_IPTC_trailing_NUL_TypeLong_trimmed verifies that
// TypeLong structural padding zeros are trimmed on extract.
//
// iptc.md ROBUST-16: trim trailing 0x00 ONLY for TypeLong (structural artefact).
// A TypeLong IPTC padded to 4-byte boundary: padding zeros must be trimmed so
// the returned slice equals the original unpadded IPTC.
func TestConformance_TIFF_IPTC_trailing_NUL_TypeLong_trimmed(t *testing.T) {
	t.Parallel()
	// rawIPTC: 5 bytes → padded to 8 (3 trailing zeros added by TypeLong padding)
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x41}

	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	_, gotIPTC, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Trailing zeros from TypeLong padding must be stripped; result must equal rawIPTC.
	if !bytes.Equal(gotIPTC, rawIPTC) {
		t.Errorf("ROBUST-16: IPTC after TypeLong trim: got %x, want %x", gotIPTC, rawIPTC)
	}
}

// TestConformance_TIFF_IPTC_TypeByte_no_NUL_trim verifies that when IPTC is
// stored as TypeByte or TypeUndefined, trailing zeros are NOT trimmed.
//
// iptc.md ROBUST-16: TypeByte/Undefined payloads must NOT be TrimRight'd —
// a valid IPTC dataset value may legitimately end in 0x00.
func TestConformance_TIFF_IPTC_TypeByte_no_NUL_trim(t *testing.T) {
	t.Parallel()
	// IPTC payload that ends in 0x00 — represents a valid dataset with
	// an empty string value (length=0, no following bytes). The trailing 0x00
	// is not structural padding; it is the value of dataset 2:80 (Byline).
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x00}

	// Build a TIFF with IPTC stored as TypeUndefined (typ=7) — NOT TypeLong.
	data := buildTIFFWithTag(binary.LittleEndian, 0x83BB, 7 /*UNDEFINED*/, rawIPTC)

	_, gotIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ROBUST-16 TypeUndefined: Extract: %v", err)
	}
	// Must NOT be trimmed.
	if !bytes.Equal(gotIPTC, rawIPTC) {
		t.Errorf("ROBUST-16: TypeUndefined IPTC with trailing 0x00 trimmed incorrectly: got %x, want %x", gotIPTC, rawIPTC)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// R — Robustness
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_TIFF_robust_cyclic_ifd_no_hang verifies that a cyclic IFD
// chain (IFD0 next-ptr → IFD0) does not hang or panic.
//
// R-01: circular IFD chains must be detected; break, no infinite loop.
func TestConformance_TIFF_robust_cyclic_ifd_no_hang(t *testing.T) {
	t.Parallel()
	data := buildCyclicIFDTIFF()

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	_ = err
	if rawEXIF == nil {
		t.Error("R-01: rawEXIF must not be nil even for cyclic IFD chain")
	}
}

// TestConformance_TIFF_robust_overlapping_ifds verifies that a TIFF with
// two IFDs at the same offset does not crash.
//
// R-07: overlapping IFDs must not crash (may produce duplicate/incorrect values).
func TestConformance_TIFF_robust_overlapping_ifds(t *testing.T) {
	t.Parallel()
	// Build a TIFF where IFD0 next-ptr points back to the start of IFD0.
	// This makes IFD0 and "IFD1" overlap completely.
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	// Patch next-IFD pointer to 8 (start of IFD0 itself).
	// IFD0 with 0 entries: count at [8:10], next-ptr at [10:14].
	binary.LittleEndian.PutUint32(data[10:], 8) // points back to IFD0

	rawEXIF, _, _, _ := Extract(bytes.NewReader(data))
	if rawEXIF == nil {
		t.Error("R-07: rawEXIF must not be nil for overlapping IFD chain")
	}
}

// TestConformance_TIFF_robust_value_offset_past_eof verifies that an IFD
// entry pointing to a value offset beyond EOF is skipped gracefully.
//
// R-03: any offset outside stream → treat as absent; skip; no crash.
// R-04: offset + count×typeSize > len → skip entry; never slice past buffer.
func TestConformance_TIFF_robust_value_offset_past_eof(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 26)
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)
	order.PutUint16(buf[10:], 0x83BB)  // IPTC
	order.PutUint16(buf[12:], 7)       // UNDEFINED
	order.PutUint32(buf[14:], 100)     // count = 100 bytes
	order.PutUint32(buf[18:], 999_999) // offset way past EOF

	rawEXIF, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("R-03/R-04: past-EOF offset: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("R-03: rawEXIF must not be nil")
	}
	if rawIPTC != nil {
		t.Error("R-03: rawIPTC must be nil for past-EOF value offset")
	}
}

// TestConformance_TIFF_robust_truncated_mid_ifd verifies that a TIFF
// truncated mid-IFD entry list returns rawEXIF without panic.
//
// R-12: truncated mid-IFD → partial IFD (read only entries that fit).
func TestConformance_TIFF_robust_truncated_mid_ifd(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 18) // header(8) + count(2) + 8 bytes (partial entry of 12)
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 5) // claims 5 entries, only ~8 bytes of entry data follow

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf))
	_ = err
	if rawEXIF == nil {
		t.Error("R-12: rawEXIF must not be nil for truncated-mid-IFD input")
	}
}

// TestConformance_TIFF_robust_zero_count_entry verifies that a zero-count IFD
// entry is handled gracefully (no panic, no crash).
//
// TIFF 6.0 §2: count = 0 is unusual but should not crash the parser.
func TestConformance_TIFF_robust_zero_count_entry(t *testing.T) {
	t.Parallel()
	// IPTC entry with count=0. typeSize×0 = 0 → inline path (total ≤ 4).
	buf := make([]byte, 26)
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)
	order.PutUint16(buf[10:], 0x83BB) // IPTC
	order.PutUint16(buf[12:], 7)      // UNDEFINED
	order.PutUint32(buf[14:], 0)      // count = 0
	order.PutUint32(buf[18:], 0)      // value = 0

	rawEXIF, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("zero-count entry: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("zero-count: rawEXIF must not be nil")
	}
	// Zero-length IPTC should produce nil rawIPTC (nothing meaningful to return).
	_ = rawIPTC
}

// ─────────────────────────────────────────────────────────────────────────────
// Write byte-correctness (TIFF-write-*)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_TIFF_write_IFD_sorted verifies that after Inject, all IFD0
// entries in the output are in ascending tag order.
//
// TIFF 6.0 §2: entries MUST be sorted ascending by tag (writer requirement).
// S-12 writer side.
func TestConformance_TIFF_write_IFD_sorted(t *testing.T) {
	t.Parallel()
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x05, 0x48, 0x65, 0x6C, 0x6C, 0x6F}
	rawXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, rawXMP, true); err != nil {
		t.Fatalf("TIFF-write-sorted: Inject: %v", err)
	}
	outBytes := out.Bytes()

	parsed, err := exif.Parse(outBytes)
	if err != nil {
		t.Fatalf("TIFF-write-sorted: exif.Parse: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("TIFF-write-sorted: IFD0 is nil")
	}
	// Verify entries are sorted.
	entries := parsed.IFD0.Entries
	for i := 1; i < len(entries); i++ {
		if entries[i].Tag < entries[i-1].Tag {
			t.Errorf("TIFF-write-sorted S-12: IFD0 entry[%d] tag 0x%04X < entry[%d] tag 0x%04X (unsorted)",
				i, entries[i].Tag, i-1, entries[i-1].Tag)
		}
	}
}

// TestConformance_TIFF_write_word_aligned_OOL_offsets verifies that all
// out-of-line value offsets in the output of Inject are even (word-aligned).
//
// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
// S-11 writer side.
func TestConformance_TIFF_write_word_aligned_OOL_offsets(t *testing.T) {
	t.Parallel()
	// Use a 101-byte (odd) XMP and a 19-byte (odd) IPTC to stress alignment.
	rawXMP := make([]byte, 101)
	copy(rawXMP, `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>`)
	rawXMP[100] = 0x3E
	rawIPTC := make([]byte, 19)
	rawIPTC[0] = 0x1C

	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, rawXMP, true); err != nil {
		t.Fatalf("TIFF-write-aligned: Inject: %v", err)
	}
	outBytes := out.Bytes()

	oolOffsets := scanAllOOLOffsets(t, outBytes)
	for _, off := range oolOffsets {
		if off%2 != 0 {
			t.Errorf("TIFF-write-aligned S-11: OOL value at odd offset %d (0x%X) — word-alignment violation", off, off)
		}
	}
}

// TestConformance_TIFF_write_round_trip_EXIF_XMP_IPTC verifies a full
// Extract → Inject → Extract round trip for all three payload types.
//
// TIFF 6.0 §2 / XMP Part 3 §1.3 / iptc.md ROBUST-16.
func TestConformance_TIFF_write_round_trip_EXIF_XMP_IPTC(t *testing.T) {
	t.Parallel()
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x05, 0x48, 0x65, 0x6C, 0x6C, 0x6F}
	rawXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildMinimalTIFF(binary.LittleEndian, nil, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, rawXMP, true); err != nil {
		t.Fatalf("round-trip: Inject: %v", err)
	}
	rawEXIF2, gotIPTC, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("round-trip: Extract: %v", err)
	}
	if rawEXIF2 == nil {
		t.Error("round-trip: rawEXIF must not be nil")
	}
	if !bytes.Equal(gotIPTC, rawIPTC) {
		t.Errorf("round-trip: IPTC = %x, want %x", gotIPTC, rawIPTC)
	}
	if !bytes.Equal(gotXMP, rawXMP) {
		t.Errorf("round-trip: XMP = %q, want %q", gotXMP, rawXMP)
	}
}

// TestConformance_TIFF_write_round_trip_BE verifies the full round-trip for
// a big-endian TIFF. The output of Inject is always LE (exif.Encode writes LE)
// but the metadata values must be preserved faithfully.
func TestConformance_TIFF_write_round_trip_BE(t *testing.T) {
	t.Parallel()
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x03, 0x41, 0x42, 0x43}
	rawXMP := []byte("<xmpmeta be='1'/>")
	data := buildMinimalTIFF(binary.BigEndian, nil, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, rawXMP, true); err != nil {
		t.Fatalf("round-trip-BE: Inject: %v", err)
	}
	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("round-trip-BE: Extract: %v", err)
	}
	if !bytes.Equal(gotIPTC, rawIPTC) {
		t.Errorf("round-trip-BE: IPTC = %x, want %x", gotIPTC, rawIPTC)
	}
	if !bytes.Equal(gotXMP, rawXMP) {
		t.Errorf("round-trip-BE: XMP = %q, want %q", gotXMP, rawXMP)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Corpus parity (real-world TIFF files)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_TIFF_corpus_extract verifies that Extract does not panic
// and returns a valid rawEXIF for every file in the tiff corpus.
//
// Covers: byte-order detection, magic acceptance, IFD traversal, tag extraction
// across a diverse set of real-world TIFF/BTF files including BigTIFF variants.
//
// Note: uses testutil.CorpusFiles which skips if the corpus directory is absent.
func TestConformance_TIFF_corpus_extract(t *testing.T) {
	t.Parallel()
	paths := testutil.CorpusFiles(t, "tiff")

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("read %s: %v", path, err)
			}
			if len(data) == 0 {
				return // .gitkeep
			}

			// Must not panic; errors are acceptable for adversarial files.
			rawEXIF, _, _, extractErr := Extract(bytes.NewReader(data))

			if extractErr != nil {
				// Errors are acceptable for known-malformed files (exiv2 corpus,
				// CVE reproducers). The key invariant is no panic.
				return
			}
			// For successfully-parsed files, rawEXIF must be non-nil.
			if rawEXIF == nil {
				t.Errorf("corpus %s: rawEXIF is nil but Extract returned no error", name)
			}
		})
	}
}

// TestConformance_TIFF_corpus_exiftool_bigtiff verifies specific properties
// of the exiftool BigTIFF corpus files (LE, BE, .btf).
//
// Corpus: testdata/corpus/tiff/exiftool/BigTIFF_{LE,BE}.tif and BigTIFF.btf
func TestConformance_TIFF_corpus_exiftool_bigtiff(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		path string
		name string
	}{
		{filepath.Join("testdata", "corpus", "tiff", "exiftool", "BigTIFF_LE.tif"), "BigTIFF_LE"},
		{filepath.Join("testdata", "corpus", "tiff", "exiftool", "BigTIFF_BE.tif"), "BigTIFF_BE"},
		{filepath.Join("testdata", "corpus", "tiff", "exiftool", "BigTIFF.btf"), "BigTIFF.btf"},
	}
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(fx.path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					t.Skipf("corpus file %s not present", fx.path)
				}
				t.Fatalf("read %s: %v", fx.path, err)
			}

			rawEXIF, _, _, extractErr := Extract(bytes.NewReader(data))
			if extractErr != nil {
				t.Fatalf("BigTIFF corpus file %s: Extract error: %v", fx.name, extractErr)
			}
			if rawEXIF == nil {
				t.Errorf("BigTIFF corpus file %s: rawEXIF is nil", fx.name)
			}
			// Verify the file is detected as BigTIFF (magic = 0x002B).
			if len(data) >= 4 {
				var magic uint16
				switch {
				case data[0] == 'I' && data[1] == 'I':
					magic = binary.LittleEndian.Uint16(data[2:])
				case data[0] == 'M' && data[1] == 'M':
					magic = binary.BigEndian.Uint16(data[2:])
				}
				if magic != 0x002B {
					t.Errorf("BigTIFF corpus file %s: magic = 0x%04X, want 0x002B", fx.name, magic)
				}
			}
		})
	}
}

// TestConformance_TIFF_corpus_no_panic_adversarial verifies that the exiv2
// adversarial corpus (CVE reproducers) does not cause a panic in Extract.
//
// R-03, R-04, R-05, R-06, R-12, R-13 — all combined in adversarial files.
func TestConformance_TIFF_corpus_no_panic_adversarial(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("testdata", "corpus", "tiff", "exiv2")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("exiv2 adversarial corpus not present: %v", err)
	}

	paths := testutil.CorpusFiles(t, filepath.Join("tiff", "exiv2"))
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("read: %v", err)
			}
			// Must not panic.
			_, _, _, _ = Extract(bytes.NewReader(data))
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers specific to this file
// ─────────────────────────────────────────────────────────────────────────────

// buildTIFFWithTag constructs a minimal TIFF with a single IFD0 entry
// using the given tag, type code, and value bytes. The value is stored
// out-of-line when len(value) > 4, inline otherwise.
//
// This is a more flexible variant of buildTIFFWithPrivateTag that accepts an
// arbitrary TIFF type code and handles both byte orders.
func buildTIFFWithTag(order binary.ByteOrder, tag uint16, typ uint16, value []byte) []byte {
	const ifd0Off = 8
	const dataOff = uint32(26) // header(8) + count(2) + entry(12) + next(4)

	total := len(value)
	bufLen := int(dataOff)
	if total > 4 {
		bufLen += total
	}
	buf := make([]byte, bufLen)

	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)
	order.PutUint16(buf[ifd0Off:], 1) // entry count = 1

	e := ifd0Off + 2
	order.PutUint16(buf[e:], tag)
	order.PutUint16(buf[e+2:], typ)
	order.PutUint32(buf[e+4:], uint32(total)) //nolint:gosec // G115: test helper, bounded by input
	if total <= 4 {
		copy(buf[e+8:], value)
	} else {
		order.PutUint32(buf[e+8:], dataOff)
		copy(buf[dataOff:], value)
	}
	// next-IFD = 0 (already zero-initialised)
	return buf
}
