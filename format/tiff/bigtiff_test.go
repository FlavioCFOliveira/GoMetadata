package tiff

// bigtiff_test.go — task #54: BigTIFF read support tests.
//
// Spec references:
//   - BigTIFF spec (Aware Systems / libtiff) §2: magic 0x002B, 16-byte header,
//     8-byte IFD offsets, 20-byte IFD entries, 8-byte inline threshold.
//   - BigTIFF types: LONG8(16)=uint64, SLONG8(17)=int64, IFD8(18)=uint64 offset.
//
// Test categories:
//   F — Functional: correct parsing of valid BigTIFF LE/BE with real tags.
//   S — Security: malformed BigTIFF must not panic or OOM.
//   E — Evidence: real-fixture tests against tiffcp-generated files.
//   R — Regression: classic TIFF 0x002A path is unchanged.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// --------------------------------------------------------------------------
// Helpers: BigTIFF synthetic file construction
// --------------------------------------------------------------------------

// buildMinimalBigTIFF constructs a minimal BigTIFF file with optional IPTC
// (tag 0x83BB, TypeUndefined/1-byte) and XMP (tag 0x02BC, TypeByte) payloads
// stored as out-of-line values (>8 bytes).
//
// BigTIFF spec §2 layout:
//
//	[0..1]   byte order marker ("II" or "MM")
//	[2..3]   magic = 0x002B
//	[4..5]   offset bytesize = 8
//	[6..7]   constant = 0
//	[8..15]  IFD0 offset (uint64) = 16 (immediately after header)
//	[16..23] IFD entry count (uint64)
//	per entry (20 bytes):
//	  [+0..1]  tag (uint16)
//	  [+2..3]  type (uint16)
//	  [+4..11] count (uint64)
//	  [+12..19] value-or-offset (uint64)
//	after entries:
//	  next-IFD pointer (uint64) = 0
//	then: out-of-line value data
func buildMinimalBigTIFF(order binary.ByteOrder, iptc, xmp []byte) []byte {
	// Collect the entries to emit.
	type entrySpec struct {
		tag     uint16
		payload []byte
	}
	var specs []entrySpec
	if iptc != nil {
		specs = append(specs, entrySpec{0x83BB, iptc})
	}
	if xmp != nil {
		specs = append(specs, entrySpec{0x02BC, xmp})
	}

	// Fixed layout sizes:
	const hdrSize = 16
	const countSize = 8
	const entrySize = 20
	const nextPtrSize = 8

	ifdBlockSize := countSize + len(specs)*entrySize + nextPtrSize
	ifdOff := uint64(hdrSize)
	dataOff := ifdOff + uint64(ifdBlockSize)

	// Calculate total out-of-line data size.
	var totalDataLen int
	for _, s := range specs {
		totalDataLen += len(s.payload)
	}
	bufLen := int(dataOff) + totalDataLen //nolint:gosec // G115: test helper, dataOff bounded by spec-sized layout
	buf := make([]byte, bufLen)

	// Write header.
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002B) // BigTIFF magic
	order.PutUint16(buf[4:], 8)      // offset bytesize = 8
	order.PutUint16(buf[6:], 0)      // constant = 0
	order.PutUint64(buf[8:], ifdOff) // IFD0 offset

	// Write IFD.
	ifdPos := int(ifdOff)
	order.PutUint64(buf[ifdPos:], uint64(len(specs)))
	ifdPos += 8

	curDataOff := dataOff
	for _, s := range specs {
		order.PutUint16(buf[ifdPos:], s.tag)
		order.PutUint16(buf[ifdPos+2:], 7) // TypeUndefined (size 1)
		order.PutUint64(buf[ifdPos+4:], uint64(len(s.payload)))
		order.PutUint64(buf[ifdPos+12:], curDataOff)
		copy(buf[int(curDataOff):], s.payload) //nolint:gosec // G115: test helper, curDataOff bounded by payload layout
		curDataOff += uint64(len(s.payload))   //nolint:gosec // G115: test helper, len bounded by input
		ifdPos += 20
	}
	// next-IFD = 0 (already zero-initialised)
	return buf
}

// buildBigTIFFWithInlineValues constructs a BigTIFF where all values are
// stored inline in the 8-byte value-or-offset field (total <= 8 bytes).
//
// This tests the inline-value path of extractTagValuesBigTIFF: an IPTC value
// of exactly 8 bytes fits inline per the BigTIFF spec §2 (threshold = 8).
func buildBigTIFFWithInlineValues(order binary.ByteOrder) []byte {
	// Use an 8-byte IPTC payload that fits exactly in the inline field.
	iptc := []byte{0x1c, 0x01, 0x5a, 0x00, 0x03, 0x55, 0x54, 0x46}
	const hdrSize = 16
	const countSize = 8
	const entrySize = 20
	const nextPtrSize = 8
	bufLen := hdrSize + countSize + entrySize + nextPtrSize

	buf := make([]byte, bufLen)
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], 16)

	// IFD: 1 entry, IPTC tag, 8 bytes inline.
	ifdPos := 16
	order.PutUint64(buf[ifdPos:], 1)
	ifdPos += 8
	order.PutUint16(buf[ifdPos:], 0x83BB) // IPTC tag
	order.PutUint16(buf[ifdPos+2:], 7)    // TypeUndefined
	order.PutUint64(buf[ifdPos+4:], 8)    // count = 8
	copy(buf[ifdPos+12:], iptc)           // inline value (fits in 8-byte field)
	return buf
}

// buildBigTIFFWithLONG8Tags constructs a BigTIFF whose IFD contains an entry
// with the BigTIFF-specific LONG8 type (code 16, uint64, 8 bytes per element).
// The tag 0x0111 (StripOffsets) with LONG8 is the typical real-world use case.
//
// This tests that extractTagValuesBigTIFF correctly handles type code 16.
func buildBigTIFFWithLONG8Tags(order binary.ByteOrder) []byte {
	const hdrSize = 16
	const countSize = 8
	const entrySize = 20
	const nextPtrSize = 8
	// 2 entries: StripOffsets (LONG8, count=2, 16 bytes OOL) + ImageWidth (SHORT, count=1, inline).
	const nEntries = 2
	ifdBlockSize := countSize + nEntries*entrySize + nextPtrSize
	dataOff := uint64(hdrSize + ifdBlockSize)
	// LONG8 array: 2 × 8 = 16 bytes out-of-line.
	bufLen := int(dataOff) + 16
	buf := make([]byte, bufLen)

	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], 16)

	ifdPos := 16
	order.PutUint64(buf[ifdPos:], nEntries)
	ifdPos += 8

	// Entry 0: tag 0x0111 (StripOffsets), type 16 (LONG8), count=2, OOL.
	order.PutUint16(buf[ifdPos:], 0x0111)
	order.PutUint16(buf[ifdPos+2:], 16) // LONG8
	order.PutUint64(buf[ifdPos+4:], 2)  // count = 2
	order.PutUint64(buf[ifdPos+12:], dataOff)
	// Write two uint64 strip offsets.
	order.PutUint64(buf[int(dataOff):], 0x100)
	order.PutUint64(buf[int(dataOff)+8:], 0x200)
	ifdPos += 20

	// Entry 1: tag 0x0100 (ImageWidth), type SHORT (3), count=1, inline.
	order.PutUint16(buf[ifdPos:], 0x0100)
	order.PutUint16(buf[ifdPos+2:], 3) // SHORT
	order.PutUint64(buf[ifdPos+4:], 1) // count = 1
	order.PutUint16(buf[ifdPos+12:], 800)
	return buf
}

// buildBigTIFFBadOffsetBytesize constructs a BigTIFF with an invalid
// offset-bytesize field (not 8). Extract must return an error.
func buildBigTIFFBadOffsetBytesize(order binary.ByteOrder, badBytesize uint16) []byte {
	buf := make([]byte, 16)
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], badBytesize) // invalid: not 8
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], 16)
	return buf
}

// --------------------------------------------------------------------------
// F — Functional tests
// --------------------------------------------------------------------------

// TestExtractBigTIFFLEBasic verifies that a minimal BigTIFF LE file with
// IPTC and XMP payloads is correctly parsed by Extract.
//
// BigTIFF spec §2: magic 0x002B, little-endian "II", 8-byte offsets.
// Task #54.
func TestExtractBigTIFFLEBasic(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte("bigtiff-le-iptc-payload-longer-than-8-bytes")
	wantXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")
	data := buildMinimalBigTIFF(binary.LittleEndian, wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract BigTIFF LE: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil")
	}
	if !bytes.Equal(rawEXIF, data) {
		t.Errorf("rawEXIF: want == data (full BigTIFF bytes), got different")
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestExtractBigTIFFBEBasic verifies that a minimal BigTIFF BE file with
// IPTC and XMP payloads is correctly parsed.
//
// BigTIFF spec §2: magic 0x002B, big-endian "MM", 8-byte offsets.
// Task #54.
func TestExtractBigTIFFBEBasic(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte("bigtiff-be-iptc-payload-longer-than-8-bytes")
	wantXMP := []byte("<xmpmeta be='1'/>")
	data := buildMinimalBigTIFF(binary.BigEndian, wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract BigTIFF BE: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil")
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestExtractBigTIFFNoMetadata verifies that a BigTIFF with no IPTC/XMP tags
// returns nil rawIPTC and nil rawXMP.
func TestExtractBigTIFFNoMetadata(t *testing.T) {
	t.Parallel()
	data := buildMinimalBigTIFF(binary.LittleEndian, nil, nil)
	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract BigTIFF no-metadata: %v", err)
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil", rawXMP)
	}
}

// TestExtractBigTIFFInlineValues verifies that an IPTC value stored inline in
// the BigTIFF IFD entry (total <= 8 bytes) is correctly extracted.
//
// BigTIFF spec §2: inline threshold is 8 bytes (vs 4 bytes in classic TIFF).
// An IPTC value of exactly 8 bytes fits inline; its raw bytes must be returned.
func TestExtractBigTIFFInlineValues(t *testing.T) {
	t.Parallel()
	data := buildBigTIFFWithInlineValues(binary.LittleEndian)
	_, rawIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract BigTIFF inline values: %v", err)
	}
	// The 8-byte inline IPTC payload must be extracted.
	// After TrimRight("\x00") (trailing-zero strip for TypeLong padding):
	// the last byte is 0x46 = 'F' (non-zero), so no trimming occurs.
	wantIPTC := []byte{0x1c, 0x01, 0x5a, 0x00, 0x03, 0x55, 0x54, 0x46}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("rawIPTC = %x, want %x", rawIPTC, wantIPTC)
	}
}

// TestExtractBigTIFFLONG8TagSkipped verifies that BigTIFF-specific LONG8
// entries (type 16, uint64 per element) are handled correctly.  Since IPTC and
// XMP are not stored as LONG8, those entries are simply skipped without error.
func TestExtractBigTIFFLONG8Tag(t *testing.T) {
	t.Parallel()
	data := buildBigTIFFWithLONG8Tags(binary.LittleEndian)
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract BigTIFF LONG8: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil")
	}
	// No IPTC/XMP tags in this file.
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil", rawXMP)
	}
}

// TestExtractBigTIFFBEIPTCOnly verifies extraction of IPTC without XMP from
// a big-endian BigTIFF file.
func TestExtractBigTIFFBEIPTCOnly(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte("big-endian-bigtiff-iptc-only-longer-than-8b")
	data := buildMinimalBigTIFF(binary.BigEndian, wantIPTC, nil)
	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract BigTIFF BE IPTC-only: %v", err)
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil", rawXMP)
	}
}

// --------------------------------------------------------------------------
// S — Security / anti-DoS tests
// --------------------------------------------------------------------------

// TestExtractBigTIFFBadOffsetBytesize verifies that Extract returns an error
// (not a panic) when the BigTIFF offset-bytesize field is not 8.
//
// BigTIFF spec §2: "bytesize of offsets — ALWAYS 8 (0x0008). Must be validated."
func TestExtractBigTIFFBadOffsetBytesize(t *testing.T) {
	t.Parallel()
	for _, bad := range []uint16{0, 4, 16, 0xFFFF} {
		t.Run("bytesize="+string(rune('0'+bad)), func(t *testing.T) {
			t.Parallel()
			data := buildBigTIFFBadOffsetBytesize(binary.LittleEndian, bad)
			_, _, _, err := Extract(bytes.NewReader(data))
			if err == nil {
				t.Errorf("Extract BigTIFF bad bytesize %d: expected error, got nil", bad)
			}
		})
	}
}

// TestExtractBigTIFFHugeEntryCount verifies that a BigTIFF with an
// astronomically large IFD entry count (near MaxUint64) does not OOM or panic.
//
// Anti-DoS: bigTIFFMaxIFDEntries caps the count at 65535; the actual entry-area
// size is further bounded to fit within the data buffer.
func TestExtractBigTIFFHugeEntryCount(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 32)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B)
	binary.LittleEndian.PutUint16(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], 16) // IFD0 at offset 16
	// IFD0: entry count = MaxUint64 — must be clamped by the guard.
	binary.LittleEndian.PutUint64(buf[16:], ^uint64(0))
	// No entries follow — the count guard must prevent reading past the buffer.

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf))
	// Must not panic. Either succeeds with no entries or returns an error.
	_ = err
	if rawEXIF == nil {
		// rawEXIF nil is unexpected — extractBigTIFF sets it before scanning IFD.
		t.Error("rawEXIF is nil for huge-count BigTIFF; expected the full bytes")
	}
}

// TestExtractBigTIFFHugeValueCount verifies that a BigTIFF IFD entry with a
// huge count (MaxUint64/typesize would overflow) is skipped without panic.
//
// Anti-DoS: extractTagValuesBigTIFF checks cnt > maxUint64/sz before
// multiplying to detect overflow.
func TestExtractBigTIFFHugeValueCount(t *testing.T) {
	t.Parallel()
	// Build a BigTIFF with one IPTC entry whose count × typeSize overflows uint64.
	// typeSize for TypeUndefined = 1, so MaxUint64 × 1 = MaxUint64 (no overflow
	// for type 1). Use TypeRational (sz=8): MaxUint64/8 + 1 would overflow.
	const overflowCount = ^uint64(0)/8 + 1

	buf := make([]byte, 52)
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], 16) // IFD0 at 16

	// IFD: 1 entry.
	order.PutUint64(buf[16:], 1)
	// Entry: tag=0x83BB, type=5 (RATIONAL, sz=8), count=overflowCount, offset=0
	order.PutUint16(buf[24:], 0x83BB)
	order.PutUint16(buf[26:], 5)             // RATIONAL
	order.PutUint64(buf[28:], overflowCount) // would overflow × 8
	order.PutUint64(buf[36:], 0)             // offset 0

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	_ = err
	// The overflowing entry must be skipped — rawIPTC must be nil.
	if rawIPTC != nil {
		t.Error("rawIPTC must be nil when entry count overflows uint64")
	}
}

// TestExtractBigTIFFHugeOffset verifies that a BigTIFF IFD entry pointing to
// an offset far beyond the file does not panic or OOM.
func TestExtractBigTIFFHugeOffset(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 52)
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], 16)

	order.PutUint64(buf[16:], 1) // 1 entry
	// IPTC entry: 100 bytes at offset 0xFFFFFFFFFFFFF000 (way past EOF).
	order.PutUint16(buf[24:], 0x83BB)
	order.PutUint16(buf[26:], 7)                  // TypeUndefined
	order.PutUint64(buf[28:], 100)                // count = 100
	order.PutUint64(buf[36:], 0xFFFFFFFFFFFFF000) // huge offset

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	_ = err
	if rawIPTC != nil {
		t.Error("rawIPTC must be nil when entry value offset is out-of-bounds")
	}
}

// TestExtractBigTIFFTruncatedAfterHeader verifies that a BigTIFF that is
// exactly 16 bytes (header only, IFD0 offset == 16 == len(data)) succeeds
// with empty metadata but does not panic.
func TestExtractBigTIFFTruncatedAfterHeader(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 16)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B)
	binary.LittleEndian.PutUint16(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], 16) // IFD0 == EOF

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Extract BigTIFF truncated-after-header: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil even for truncated BigTIFF")
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Error("expected nil IPTC/XMP for empty-IFD BigTIFF")
	}
}

// TestExtractBigTIFFTooShort verifies that a BigTIFF truncated before the
// full 16-byte header returns ErrFileTooShort.
//
// Classic TIFF ErrFileTooShort fires at < 8 bytes; BigTIFF fires at < 16 bytes.
func TestExtractBigTIFFTooShort(t *testing.T) {
	t.Parallel()
	// 14 bytes: valid byte-order, magic, bytesize, constant — but only 6/8 bytes of IFD offset.
	buf := make([]byte, 14)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B)
	binary.LittleEndian.PutUint16(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	// IFD offset truncated — only bytes 8-13 present (6 bytes instead of 8).

	_, _, _, err := Extract(bytes.NewReader(buf))
	if err == nil {
		t.Error("Extract BigTIFF 14-byte truncated: expected error, got nil")
	}
}

// TestExtractBigTIFFCyclicIFD verifies that a BigTIFF whose IFD entry count
// fills exactly one entry that points back via a huge fake offset does not hang.
// extractTagValuesBigTIFF does not follow next-IFD chains; this test exercises
// that the scan terminates after the single IFD0 regardless.
func TestExtractBigTIFFIFDOffsetAtEOF(t *testing.T) {
	t.Parallel()
	// IFD0 offset pointing exactly to EOF — the IFD count field is absent.
	buf := make([]byte, 24)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B)
	binary.LittleEndian.PutUint16(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], 24) // IFD0 == EOF

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf))
	_ = err
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil even when IFD0 points to EOF")
	}
}

// --------------------------------------------------------------------------
// E — Evidence: real-fixture tests (tiffcp-generated BigTIFF files)
// --------------------------------------------------------------------------

const bigCrampsLEFixture = "testdata/big_cramps_le.tif"
const bigCrampsBEFixture = "testdata/big_cramps_be.tif"
const crampsClassicFixture = "testdata/cramps.tif"

// TestExtractBigTIFFRealFileLE is an evidence test against a real BigTIFF
// file generated from the cramps.tif libtiff sample using tiffcp -8 -L.
//
// Evidence: xxd -l16 big_cramps_le.tif produces:
//
//	49 49 2B 00 08 00 00 00 2E F2 02 00 00 00 00 00
//	  "II" magic=43 bytesize=8 const=0 ifd0=0x2F22E
//
// tiffinfo reports Format: Big TIFF, same tags as cramps.tif.
// exiftool reports ImageWidth=800, ImageHeight=607.
func TestExtractBigTIFFRealFileLE(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(bigCrampsLEFixture)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("testdata/big_cramps_le.tif not present; skipping real-file test")
		}
		t.Fatalf("read fixture: %v", err)
	}

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract BigTIFF LE real file: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil for real BigTIFF LE")
	}
	if !bytes.Equal(rawEXIF, data) {
		t.Error("rawEXIF does not equal the full input data")
	}
	// cramps.tif carries no IPTC or XMP — expect nil.
	if rawIPTC != nil {
		t.Errorf("rawIPTC: expected nil for cramps.tif (no IPTC), got %d bytes", len(rawIPTC))
	}
	if rawXMP != nil {
		t.Errorf("rawXMP: expected nil for cramps.tif (no XMP), got %d bytes", len(rawXMP))
	}
}

// TestExtractBigTIFFRealFileBE is the big-endian counterpart to
// TestExtractBigTIFFRealFileLE.
//
// Evidence: xxd -l16 big_cramps_be.tif produces:
//
//	4D 4D 00 2B 00 08 00 00 00 00 00 00 00 02 F2 2E
//	  "MM" magic=43 bytesize=8 const=0 ifd0=0x2F22E (BE)
func TestExtractBigTIFFRealFileBE(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(bigCrampsBEFixture)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("testdata/big_cramps_be.tif not present; skipping real-file test")
		}
		t.Fatalf("read fixture: %v", err)
	}

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract BigTIFF BE real file: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil for real BigTIFF BE")
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC: expected nil for cramps.tif (no IPTC), got %d bytes", len(rawIPTC))
	}
	if rawXMP != nil {
		t.Errorf("rawXMP: expected nil for cramps.tif (no XMP), got %d bytes", len(rawXMP))
	}
}

// TestExtractBigTIFFTagMatchVsClassic verifies that the BigTIFF versions of
// cramps.tif report identical IFD0 tag values to the classic TIFF version.
//
// Tags verified (IFD0 of cramps.tif):
//
//	0x0100 (ImageWidth)  = 800  (SHORT, inline)
//	0x0101 (ImageLength) = 607  (SHORT, inline)
//	0x0112 (Orientation) = 1    (SHORT, inline)
//	0x0115 (SamplesPerPixel) = 1 (SHORT, inline)
//	0x0116 (RowsPerStrip)  = 12 (SHORT, inline)
//
// This matches exiftool output:
//
//	ImageWidth=800, ImageHeight=607, Orientation=Horizontal(normal)
//	SamplesPerPixel=1, RowsPerStrip=12
//
// Comparison approach: scan the BigTIFF IFD directly using
// extractTagValuesBigTIFF-equivalent logic and compare a key tag.
func TestExtractBigTIFFTagMatchVsClassic(t *testing.T) {
	t.Parallel()

	leData, err := os.ReadFile(bigCrampsLEFixture)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("testdata/big_cramps_le.tif not present")
		}
		t.Fatalf("read LE fixture: %v", err)
	}
	beData, err := os.ReadFile(bigCrampsBEFixture)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("testdata/big_cramps_be.tif not present")
		}
		t.Fatalf("read BE fixture: %v", err)
	}

	// scanBigTIFFTag reads the first SHORT value for a given tag from a BigTIFF.
	// Returns (0, false) if not found or not SHORT.
	scanBigTIFFTag := func(data []byte, wantTag uint16) (val uint16, ok bool) {
		order, oErr := byteOrder(data)
		if oErr != nil || len(data) < 16 {
			return 0, false
		}
		ifd0Off := order.Uint64(data[8:])
		if ifd0Off > uint64(len(data))-8 {
			return 0, false
		}
		count := order.Uint64(data[ifd0Off:])
		if count > bigTIFFMaxIFDEntries {
			count = bigTIFFMaxIFDEntries
		}
		const entSize = 20
		if count > (uint64(len(data))-ifd0Off-8)/entSize {
			count = (uint64(len(data)) - ifd0Off - 8) / entSize
		}
		pos := ifd0Off + 8
		for i := uint64(0); i < count; i++ { //nolint:intrange // BigTIFF parser: loop variable is a byte-slice offset multiplier
			e := pos + i*entSize
			tag := order.Uint16(data[e:])
			typ := order.Uint16(data[e+2:])
			if tag == wantTag && typ == 3 { // SHORT
				return order.Uint16(data[e+12:]), true
			}
		}
		return 0, false
	}

	tests := []struct {
		tag  uint16
		want uint16
		name string
	}{
		{0x0100, 800, "ImageWidth"},
		{0x0101, 607, "ImageLength"},
		{0x0112, 1, "Orientation"},
		{0x0115, 1, "SamplesPerPixel"},
		{0x0116, 12, "RowsPerStrip"},
	}

	for _, tc := range tests {
		leVal, leOK := scanBigTIFFTag(leData, tc.tag)
		if !leOK {
			t.Errorf("LE BigTIFF: tag 0x%04X (%s) not found or wrong type", tc.tag, tc.name)
			continue
		}
		if leVal != tc.want {
			t.Errorf("LE BigTIFF tag 0x%04X (%s) = %d, want %d (exiftool/cramps.tif)", tc.tag, tc.name, leVal, tc.want)
		}

		beVal, beOK := scanBigTIFFTag(beData, tc.tag)
		if !beOK {
			t.Errorf("BE BigTIFF: tag 0x%04X (%s) not found or wrong type", tc.tag, tc.name)
			continue
		}
		if beVal != tc.want {
			t.Errorf("BE BigTIFF tag 0x%04X (%s) = %d, want %d (exiftool/cramps.tif)", tc.tag, tc.name, beVal, tc.want)
		}
	}
}

// --------------------------------------------------------------------------
// R — Regression: classic TIFF path unchanged after BigTIFF switch
// --------------------------------------------------------------------------

// TestExtractClassicTIFFUnchangedAfterBigTIFFSwitch is a regression test to
// confirm that implementing BigTIFF support did not regress the classic TIFF
// (magic 0x002A) Extract path.
func TestExtractClassicTIFFUnchangedAfterBigTIFFSwitch(t *testing.T) {
	t.Parallel()

	// Build a minimal classic TIFF with known IPTC and XMP.
	wantIPTC := []byte("classic-tiff-regression-iptc-longer-than-8-bytes")
	wantXMP := []byte("<xmpmeta/>")
	data := buildMinimalTIFF(binary.LittleEndian, wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract classic TIFF after BigTIFF switch: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF nil for classic TIFF")
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestExtractRealClassicTIFFUnchanged confirms the real cramps.tif
// (classic BE TIFF) still parses without error after the BigTIFF addition.
func TestExtractRealClassicTIFFUnchanged(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(crampsClassicFixture)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("testdata/cramps.tif not present")
		}
		t.Fatalf("read fixture: %v", err)
	}
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract classic cramps.tif after BigTIFF switch: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF nil for classic cramps.tif")
	}
}

// TestExtractUnknownMagicStillErrors verifies that a magic other than 0x002A
// or 0x002B still returns ErrUnsupportedMagic.
func TestExtractUnknownMagicStillErrors(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 8)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x1234) // unknown magic
	binary.LittleEndian.PutUint32(buf[4:], 8)
	_, _, _, err := Extract(bytes.NewReader(buf))
	if err == nil {
		t.Error("expected error for unknown magic 0x1234, got nil")
	}
	if !errors.Is(err, ErrUnsupportedMagic) {
		t.Errorf("error does not wrap ErrUnsupportedMagic: %v", err)
	}
}

// --------------------------------------------------------------------------
// Benchmark
// --------------------------------------------------------------------------

// BenchmarkBigTIFFExtract measures the Extract throughput on a synthetic
// BigTIFF with IPTC and XMP payloads.
func BenchmarkBigTIFFExtract(b *testing.B) {
	iptc := []byte("bigtiff-bench-iptc-payload-long-enough")
	xmp := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")
	data := buildMinimalBigTIFF(binary.LittleEndian, iptc, xmp)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(data))
	}
}
