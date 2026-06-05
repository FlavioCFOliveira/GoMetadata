package exif

// bigtiff_test.go — task #54 (final pass): unit tests for BigTIFF-aware exif.Parse.
//
// Spec references:
//   - BigTIFF spec (Aware Systems / libtiff) §2: magic 0x002B, 16-byte header,
//     8-byte IFD offsets, 20-byte entries, 8-byte inline threshold.
//   - BigTIFF types: LONG8(16)=uint64, SLONG8(17)=int64, IFD8(18)=uint64 offset.
//   - EXIF §4.6.3: ExifIFD pointer tag 0x8769, GPS IFD pointer tag 0x8825.
//   - EXIF §4.6.4: Make (0x010F), Model (0x0110), ImageDescription (0x010E).
//
// Test categories:
//   F — Functional: correct EXIF decode for BigTIFF LE and BE with real tag values.
//   S — Security:   malformed BigTIFF inputs must not panic or OOM.
//   R — Regression: classic TIFF 0x002A path behaviour is unchanged.

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/metaerr"
)

// ---------------------------------------------------------------------------
// Helpers: BigTIFF synthetic file construction
// ---------------------------------------------------------------------------

// bigTIFFEntry describes a single IFD entry for buildBigTIFF.
type bigTIFFEntry struct {
	tag     uint16
	typ     uint16
	count   uint64
	payload []byte // out-of-line when len > 8; inline value (up to 8 bytes) when len <= 8
}

// buildBigTIFF constructs a complete BigTIFF byte stream with a single IFD0
// containing the given entries. Inline values (≤ 8 bytes) are stored in the
// value-or-offset field; larger values are stored out-of-line.
//
// BigTIFF spec §2 layout:
//
//	[0..1]   byte order "II" or "MM"
//	[2..3]   magic 0x002B
//	[4..5]   offset-bytesize = 8
//	[6..7]   constant = 0
//	[8..15]  IFD0 offset (uint64) = 16
//	[16..23] IFD entry count (uint64)
//	per entry (20 bytes):
//	  [+0..1]  tag
//	  [+2..3]  type
//	  [+4..11] count
//	  [+12..19] value-or-offset
//	after entries: next-IFD pointer (uint64) = 0
//	then: out-of-line value data
func buildBigTIFF(order binary.ByteOrder, entries []bigTIFFEntry) []byte {
	const (
		hdrSize      = 16
		countSize    = 8
		entrySize    = 20
		nextPtrSize  = 8
		inlineThresh = 8
	)

	ifdBlockSize := countSize + len(entries)*entrySize + nextPtrSize
	ifdOff := uint64(hdrSize)
	dataBase := ifdOff + uint64(ifdBlockSize)

	// Compute total OOL data size.
	var totalOOL int
	for _, e := range entries {
		if len(e.payload) > inlineThresh {
			totalOOL += len(e.payload)
		}
	}
	bufLen := int(dataBase) + totalOOL //nolint:gosec // G115: test helper, dataBase bounded by spec-sized BigTIFF layout
	buf := make([]byte, bufLen)

	// Write header.
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002B) // BigTIFF magic
	order.PutUint16(buf[4:], 8)      // offset bytesize
	order.PutUint16(buf[6:], 0)      // constant 0
	order.PutUint64(buf[8:], ifdOff) // IFD0 offset

	// Write IFD entry count.
	ifdPos := int(ifdOff)
	order.PutUint64(buf[ifdPos:], uint64(len(entries)))
	ifdPos += 8

	curDataOff := dataBase
	for _, e := range entries {
		order.PutUint16(buf[ifdPos:], e.tag)
		order.PutUint16(buf[ifdPos+2:], e.typ)
		order.PutUint64(buf[ifdPos+4:], e.count)

		if len(e.payload) <= inlineThresh {
			// Inline: copy left-justified into the 8-byte value-or-offset field.
			copy(buf[ifdPos+12:ifdPos+20], e.payload)
		} else {
			// Out-of-line: store offset in value-or-offset field.
			order.PutUint64(buf[ifdPos+12:], curDataOff)
			copy(buf[int(curDataOff):], e.payload) //nolint:gosec // G115: test helper, curDataOff bounded
			curDataOff += uint64(len(e.payload))
		}
		ifdPos += entrySize
	}
	// next-IFD = 0 (already zero-initialised)
	return buf
}

// asciiPayload encodes s as NUL-terminated ASCII for a BigTIFF TypeASCII entry.
func asciiPayload(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

// leUint16Payload encodes v as a 2-byte little-endian value for use in a TypeShort inline entry.
func shortPayload(order binary.ByteOrder, v uint16) []byte {
	b := make([]byte, 2)
	order.PutUint16(b, v)
	return b
}

// ---------------------------------------------------------------------------
// F — Functional tests
// ---------------------------------------------------------------------------

// TestParseBigTIFF_LE_StringTags verifies that exif.Parse decodes out-of-line
// ASCII tags (Make, Model, ImageDescription) from a BigTIFF little-endian file.
//
// Key difference from classic TIFF: "Canon\x00" (6 bytes) is stored INLINE in
// BigTIFF (inline threshold = 8 bytes), whereas in classic TIFF (threshold = 4)
// it would be out-of-line. Both cases must be handled.
func TestParseBigTIFF_LE_StringTags(t *testing.T) {
	t.Parallel()
	const (
		wantMake    = "Canon"
		wantModel   = "Canon EOS DIGITAL REBEL"
		wantCaption = "The picture caption"
	)
	order := binary.LittleEndian
	entries := []bigTIFFEntry{
		// ImageDescription (0x010E): 20 bytes ASCII — out-of-line (> 8).
		{tag: 0x010E, typ: uint16(TypeASCII), count: uint64(len(wantCaption) + 1), payload: asciiPayload(wantCaption)},
		// Make (0x010F): 6 bytes "Canon\x00" — INLINE in BigTIFF (≤ 8 bytes).
		{tag: 0x010F, typ: uint16(TypeASCII), count: uint64(len(wantMake) + 1), payload: asciiPayload(wantMake)},
		// Model (0x0110): 24 bytes — out-of-line.
		{tag: 0x0110, typ: uint16(TypeASCII), count: uint64(len(wantModel) + 1), payload: asciiPayload(wantModel)},
	}
	data := buildBigTIFF(order, entries)

	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse BigTIFF LE string tags: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}

	if got := e.IFD0.Get(TagMake); got == nil || got.String() != wantMake {
		if got == nil {
			t.Errorf("Make tag missing")
		} else {
			t.Errorf("Make = %q, want %q", got.String(), wantMake)
		}
	}
	if got := e.IFD0.Get(TagModel); got == nil || got.String() != wantModel {
		if got == nil {
			t.Errorf("Model tag missing")
		} else {
			t.Errorf("Model = %q, want %q", got.String(), wantModel)
		}
	}
	if got := e.IFD0.Get(TagImageDescription); got == nil || got.String() != wantCaption {
		if got == nil {
			t.Errorf("ImageDescription tag missing")
		} else {
			t.Errorf("ImageDescription = %q, want %q", got.String(), wantCaption)
		}
	}
}

// TestParseBigTIFF_BE_StringTags is the big-endian counterpart to
// TestParseBigTIFF_LE_StringTags.
func TestParseBigTIFF_BE_StringTags(t *testing.T) {
	t.Parallel()
	const (
		wantMake  = "Canon"
		wantModel = "Canon EOS DIGITAL REBEL"
	)
	order := binary.BigEndian
	entries := []bigTIFFEntry{
		{tag: 0x010F, typ: uint16(TypeASCII), count: uint64(len(wantMake) + 1), payload: asciiPayload(wantMake)},
		{tag: 0x0110, typ: uint16(TypeASCII), count: uint64(len(wantModel) + 1), payload: asciiPayload(wantModel)},
	}
	data := buildBigTIFF(order, entries)

	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse BigTIFF BE: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}
	if got := e.IFD0.Get(TagMake); got == nil || got.String() != wantMake {
		if got == nil {
			t.Errorf("Make tag missing in BE BigTIFF")
		} else {
			t.Errorf("BE Make = %q, want %q", got.String(), wantMake)
		}
	}
	if got := e.IFD0.Get(TagModel); got == nil || got.String() != wantModel {
		if got == nil {
			t.Errorf("Model tag missing in BE BigTIFF")
		} else {
			t.Errorf("BE Model = %q, want %q", got.String(), wantModel)
		}
	}
}

// TestParseBigTIFF_InlineShort verifies that TypeShort (2-byte) inline values
// are decoded correctly from BigTIFF (inline threshold 8 bytes; SHORT is always inline).
func TestParseBigTIFF_InlineShort(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	const wantWidth = uint16(160)
	entries := []bigTIFFEntry{
		{tag: 0x0100, typ: uint16(TypeShort), count: 1, payload: shortPayload(order, wantWidth)},
	}
	data := buildBigTIFF(order, entries)

	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}
	got := e.IFD0.Get(TagImageWidth)
	if got == nil {
		t.Fatal("ImageWidth entry missing")
	}
	if got.Uint16() != wantWidth {
		t.Errorf("ImageWidth = %d, want %d", got.Uint16(), wantWidth)
	}
}

// TestParseBigTIFF_ExifSubIFD verifies that ExifIFD sub-IFD traversal works
// in BigTIFF files. The ExifIFD pointer (0x8769) is stored as TypeLong (32-bit
// offset) — the format written by libtiff/tiffcp — and must be followed
// correctly by parseExifSubIFDsBigTIFF via readBigTIFFSubIFDOffset.
func TestParseBigTIFF_ExifSubIFD(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// We build the BigTIFF manually to place the ExifIFD at a known offset.
	//
	// Layout:
	//   [0..15]   header
	//   [16..23]  IFD0 count (uint64) = 1
	//   [24..43]  IFD0 entry: tag=0x8769, TypeLong, count=1, value=exifIFDOff (inline 4B)
	//   [44..51]  IFD0 next-IFD (uint64) = 0
	//   [52..59]  ExifIFD count (uint64) = 1
	//   [60..79]  ExifIFD entry: tag=0x8827 (ISOSpeedRatings), TypeShort, count=1, value=400 (inline)
	//   [80..87]  ExifIFD next-IFD (uint64) = 0

	const (
		hdrSize     = 16
		countSize   = 8
		entrySize   = 20
		nextPtrSize = 8
		ifd0Off     = hdrSize
		exifIFDOff  = hdrSize + countSize + entrySize + nextPtrSize // = 52
	)

	buf := make([]byte, exifIFDOff+countSize+entrySize+nextPtrSize)

	// Header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], uint64(ifd0Off))

	// IFD0: 1 entry — ExifIFD pointer (TypeLong, 4-byte inline value).
	order.PutUint64(buf[ifd0Off:], 1)
	p := ifd0Off + countSize
	order.PutUint16(buf[p:], 0x8769) // TagExifIFDPointer
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint64(buf[p+4:], 1) // count=1
	order.PutUint32(buf[p+12:], exifIFDOff)
	// next-IFD = 0 (already zero).

	// ExifIFD: 1 entry — ISOSpeedRatings (TypeShort, inline).
	order.PutUint64(buf[exifIFDOff:], 1)
	q := exifIFDOff + countSize
	order.PutUint16(buf[q:], 0x8827) // TagISOSpeedRatings
	order.PutUint16(buf[q+2:], uint16(TypeShort))
	order.PutUint64(buf[q+4:], 1)    // count=1
	order.PutUint16(buf[q+12:], 400) // ISO=400 inline
	// next-IFD = 0.

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse BigTIFF with ExifIFD: %v", err)
	}
	if e.ExifIFD == nil {
		t.Fatal("ExifIFD is nil; sub-IFD traversal failed")
	}
	isoEntry := e.ExifIFD.Get(TagISOSpeedRatings)
	if isoEntry == nil {
		t.Fatal("ISOSpeedRatings entry missing from ExifIFD")
	}
	if got := isoEntry.Uint16(); got != 400 {
		t.Errorf("ISOSpeedRatings = %d, want 400", got)
	}
}

// TestParseBigTIFF_LONG8Tag verifies that BigTIFF-specific LONG8 (type 16)
// entries are parsed without error. LONG8 is used by libtiff/tiffcp for
// StripOffsets and StripByteCounts in large BigTIFF files.
func TestParseBigTIFF_LONG8Tag(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// StripOffsets (0x0111), TypeLONG8 (16), count=1, value=0x10 inline.
	var valBuf [8]byte
	order.PutUint64(valBuf[:], 0x10)
	entries := []bigTIFFEntry{
		{tag: 0x0111, typ: 16, count: 1, payload: valBuf[:]},
	}
	data := buildBigTIFF(order, entries)

	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse BigTIFF LONG8: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}
	got := e.IFD0.Get(TagStripOffsets)
	if got == nil {
		t.Fatal("StripOffsets entry missing")
	}
	// The entry must have TypeLong8 and a non-nil value.
	if got.Type != TypeLong8 {
		t.Errorf("StripOffsets type = %d, want TypeLong8 (%d)", got.Type, TypeLong8)
	}
}

// TestParseBigTIFF_ZeroEntry verifies that a BigTIFF with IFD0 entry count = 0
// parses successfully and returns an empty IFD0.
func TestParseBigTIFF_ZeroEntry(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	data := buildBigTIFF(order, nil)

	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse BigTIFF zero-entry: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 nil for zero-entry BigTIFF")
	}
	if len(e.IFD0.Entries) != 0 {
		t.Errorf("IFD0 entry count = %d, want 0", len(e.IFD0.Entries))
	}
}

// TestParseBigTIFF_ByteOrderLE verifies that ByteOrder is LittleEndian for
// a BigTIFF "II" file and BigEndian for an "MM" file.
func TestParseBigTIFF_ByteOrder(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		order binary.ByteOrder
	}{
		{"LE", binary.LittleEndian},
		{"BE", binary.BigEndian},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := buildBigTIFF(tc.order, nil)
			e, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse BigTIFF %s: %v", tc.name, err)
			}
			if e.ByteOrder != tc.order {
				t.Errorf("ByteOrder = %v, want %v", e.ByteOrder, tc.order)
			}
		})
	}
}

// TestParseBigTIFF_RealFileLE verifies that exif.Parse decodes Make and Model
// from the committed BigTIFF_LE.tif fixture.
//
// This is the key regression gate: Parse must decode IFD0 EXIF tags from a
// real-world BigTIFF file generated by tiffcp -8 -L.
func TestParseBigTIFF_RealFileLE(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../testdata/corpus/tiff/exiftool/BigTIFF_LE.tif")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	e, ferr := Parse(data)
	if ferr != nil {
		t.Fatalf("exif.Parse BigTIFF_LE.tif: %v", ferr)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}
	makeEntry := e.IFD0.Get(TagMake)
	if makeEntry == nil {
		t.Fatal("Make tag missing from BigTIFF_LE.tif IFD0")
	}
	if got := makeEntry.String(); got != "Canon" {
		t.Errorf("Make = %q, want %q", got, "Canon")
	}
	modelEntry := e.IFD0.Get(TagModel)
	if modelEntry == nil {
		t.Fatal("Model tag missing from BigTIFF_LE.tif IFD0")
	}
	if got := modelEntry.String(); got != "Canon EOS DIGITAL REBEL" {
		t.Errorf("Model = %q, want %q", got, "Canon EOS DIGITAL REBEL")
	}
}

// TestParseBigTIFF_RealFileBE is the big-endian counterpart to
// TestParseBigTIFF_RealFileLE.
func TestParseBigTIFF_RealFileBE(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../testdata/corpus/tiff/exiftool/BigTIFF_BE.tif")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	e, ferr := Parse(data)
	if ferr != nil {
		t.Fatalf("exif.Parse BigTIFF_BE.tif: %v", ferr)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}
	makeEntry := e.IFD0.Get(TagMake)
	if makeEntry == nil {
		t.Fatal("Make tag missing from BigTIFF_BE.tif IFD0")
	}
	if got := makeEntry.String(); got != "Canon" {
		t.Errorf("Make = %q, want %q", got, "Canon")
	}
}

// ---------------------------------------------------------------------------
// S — Security / anti-DoS tests
// ---------------------------------------------------------------------------

// TestParseBigTIFF_BadOffsetBytesize verifies that Parse returns a
// CorruptMetadataError when the BigTIFF offset-bytesize field is not 8.
// BigTIFF spec §2: "bytesize of offsets — ALWAYS 8 (0x0008)."
func TestParseBigTIFF_BadOffsetBytesize(t *testing.T) {
	t.Parallel()
	for _, bad := range []uint16{0, 4, 16, 0xFFFF} {
		t.Run("bytesize", func(t *testing.T) {
			t.Parallel()
			buf := make([]byte, 16)
			buf[0], buf[1] = 'I', 'I'
			binary.LittleEndian.PutUint16(buf[2:], 0x002B)
			binary.LittleEndian.PutUint16(buf[4:], bad) // invalid
			binary.LittleEndian.PutUint16(buf[6:], 0)
			binary.LittleEndian.PutUint64(buf[8:], 16)

			_, err := Parse(buf)
			if err == nil {
				t.Errorf("expected error for offset-bytesize=%d, got nil", bad)
			}
		})
	}
}

// TestParseBigTIFF_TooShort verifies that Parse returns TruncatedFileError for
// a BigTIFF that is shorter than the 16-byte minimum header.
func TestParseBigTIFF_TooShort(t *testing.T) {
	t.Parallel()
	// 14 bytes: has magic 0x002B but the 8-byte IFD0 offset field is truncated.
	buf := make([]byte, 14)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B)
	binary.LittleEndian.PutUint16(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	// IFD0 offset truncated — only bytes 8-13 present.

	_, err := Parse(buf)
	if err == nil {
		t.Fatal("expected TruncatedFileError for BigTIFF with truncated header")
	}
	var tf *metaerr.TruncatedFileError
	if !errors.As(err, &tf) {
		t.Errorf("expected TruncatedFileError, got %T: %v", err, err)
	}
}

// TestParseBigTIFF_HugeEntryCount verifies that Parse does not OOM or panic
// when the IFD entry count is near MaxUint64.
// Anti-DoS: bigTIFFMaxEntries caps the count at 65535.
func TestParseBigTIFF_HugeEntryCount(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 32)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B)
	binary.LittleEndian.PutUint16(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], 16) // IFD0 at 16
	// IFD0: count = MaxUint64 — must be clamped and not OOM.
	binary.LittleEndian.PutUint64(buf[16:], ^uint64(0))
	// No entries follow — the count guard must clamp to 0.

	// Must not panic and must return either a valid (empty) IFD or an error.
	_, _ = Parse(buf)
}

// TestParseBigTIFF_CyclicIFD verifies that a BigTIFF whose next-IFD pointer
// forms a cycle does not hang. traverseBigTIFF uses a visited-offset set.
func TestParseBigTIFF_CyclicIFD(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Build a valid BigTIFF whose IFD's next-IFD pointer points back to itself.
	const (
		hdrSize     = 16
		countSize   = 8
		nextPtrSize = 8
		ifd0Off     = hdrSize
	)
	buf := make([]byte, hdrSize+countSize+nextPtrSize)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], ifd0Off)

	// IFD0: count=0, next-IFD = ifd0Off (points back to itself).
	order.PutUint64(buf[ifd0Off:], 0)                 // 0 entries
	order.PutUint64(buf[ifd0Off+countSize:], ifd0Off) // next = self (cycle)

	// Must not hang.
	e, err := Parse(buf)
	_ = err
	// If we got here, cycle detection worked.
	if e == nil && err == nil {
		t.Error("expected non-nil EXIF or an error for cyclic BigTIFF")
	}
}

// TestParseBigTIFF_HugeValueCount verifies that an IFD entry with a count that
// would overflow uint64 when multiplied by the element size is skipped safely.
func TestParseBigTIFF_HugeValueCount(t *testing.T) {
	t.Parallel()
	// Build a BigTIFF with one entry: TypeRational (sz=8),
	// count = MaxUint64/8 + 1 (would overflow * 8).
	const overflowCount = ^uint64(0)/8 + 1
	const (
		hdrSize     = 16
		countSize   = 8
		entrySize   = 20
		nextPtrSize = 8
	)
	bufLen := hdrSize + countSize + entrySize + nextPtrSize
	buf := make([]byte, bufLen)
	order := binary.LittleEndian

	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], 16)

	order.PutUint64(buf[16:], 1) // 1 entry
	// Entry: Make (0x010F), TypeRational (5, sz=8), count=overflowCount.
	order.PutUint16(buf[24:], 0x010F)
	order.PutUint16(buf[26:], 5)             // TypeRational
	order.PutUint64(buf[28:], overflowCount) // would overflow × 8
	order.PutUint64(buf[36:], 0)             // offset

	e, err := Parse(buf)
	_ = err
	// The entry must be skipped; either IFD0 is empty or the whole parse fails.
	if e != nil && e.IFD0 != nil {
		makeEntry := e.IFD0.Get(TagMake)
		if makeEntry != nil {
			t.Error("Make entry with overflow count must be skipped")
		}
	}
}

// TestParseBigTIFF_HugeOffset verifies that an IFD entry pointing to an offset
// far beyond EOF is skipped safely without panic.
func TestParseBigTIFF_HugeOffset(t *testing.T) {
	t.Parallel()
	const (
		hdrSize     = 16
		countSize   = 8
		entrySize   = 20
		nextPtrSize = 8
	)
	bufLen := hdrSize + countSize + entrySize + nextPtrSize
	buf := make([]byte, bufLen)
	order := binary.LittleEndian

	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], 16)

	order.PutUint64(buf[16:], 1) // 1 entry
	// Make (0x010F), TypeASCII, count=100, offset=MaxUint64-8 (way past EOF).
	order.PutUint16(buf[24:], 0x010F)
	order.PutUint16(buf[26:], 2)                  // TypeASCII
	order.PutUint64(buf[28:], 100)                // count=100
	order.PutUint64(buf[36:], 0xFFFFFFFFFFFFFFF0) // huge offset

	// Must not panic.
	_, _ = Parse(buf)
}

// ---------------------------------------------------------------------------
// R — Regression: classic TIFF 0x002A path unchanged
// ---------------------------------------------------------------------------

// TestParseBigTIFF_ClassicUnchanged verifies that the addition of BigTIFF
// support did not change the behaviour of the classic 0x002A parse path.
// This is a regression guard: the classic path must still decode Make correctly.
func TestParseBigTIFF_ClassicUnchanged(t *testing.T) {
	t.Parallel()
	// Build a classic TIFF LE with Make = "TestMake\x00".
	const wantMake = "TestMake"
	makeBytes := asciiPayload(wantMake)
	order := binary.LittleEndian

	// Classic TIFF layout:
	// header(8) + IFD count(2) + entry(12) + next-IFD(4) + value area
	const hdrSize = 8
	const entrySize = 12
	ifdOff := uint32(hdrSize)
	valueAreaOff := ifdOff + 2 + entrySize + 4
	bufLen := int(valueAreaOff) + len(makeBytes)
	buf := make([]byte, bufLen)

	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifdOff)

	// IFD: 1 entry.
	order.PutUint16(buf[ifdOff:], 1)
	p := int(ifdOff) + 2
	order.PutUint16(buf[p:], 0x010F)                   // Make tag
	order.PutUint16(buf[p+2:], 2)                      // TypeASCII
	order.PutUint32(buf[p+4:], uint32(len(makeBytes))) //nolint:gosec // G115: test helper
	order.PutUint32(buf[p+8:], valueAreaOff)           // out-of-line offset
	// next-IFD = 0.
	copy(buf[valueAreaOff:], makeBytes)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse classic TIFF after BigTIFF addition: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 nil for classic TIFF")
	}
	got := e.IFD0.Get(TagMake)
	if got == nil || got.String() != wantMake {
		if got == nil {
			t.Error("Make tag missing from classic TIFF")
		} else {
			t.Errorf("Make = %q, want %q", got.String(), wantMake)
		}
	}
}

// TestParseBigTIFF_UnknownMagic verifies that a magic value other than
// 0x002A or 0x002B returns a CorruptMetadataError.
func TestParseBigTIFF_UnknownMagic(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 16)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x1234) // unknown magic
	binary.LittleEndian.PutUint32(buf[4:], 8)

	_, err := Parse(buf)
	if err == nil {
		t.Fatal("expected error for unknown magic, got nil")
	}
	var cm *metaerr.CorruptMetadataError
	if !errors.As(err, &cm) {
		t.Errorf("expected CorruptMetadataError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Benchmark: BigTIFF parse vs classic TIFF parse (regression guard)
// ---------------------------------------------------------------------------

// BenchmarkParseBigTIFF_Simple measures Parse throughput on a minimal BigTIFF
// with 3 string tags. This establishes a baseline and can be compared against
// BenchmarkEXIFParse to confirm the BigTIFF path does not regress the classic path.
func BenchmarkParseBigTIFF_Simple(b *testing.B) {
	b.ReportAllocs()
	order := binary.LittleEndian
	entries := []bigTIFFEntry{
		{tag: 0x010F, typ: uint16(TypeASCII), count: 6, payload: asciiPayload("Canon")},
		{tag: 0x0110, typ: uint16(TypeASCII), count: 24, payload: asciiPayload("Canon EOS DIGITAL REBEL")},
		{tag: 0x0100, typ: uint16(TypeShort), count: 1, payload: shortPayload(order, 160)},
	}
	data := buildBigTIFF(order, entries)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for range b.N {
		_, _ = Parse(data)
	}
}
