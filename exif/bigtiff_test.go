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

// ---------------------------------------------------------------------------
// Gate tests for audit findings #141, #142, #143
// ---------------------------------------------------------------------------

// TestExtractJPEGThumbnailBigTIFF_Long8Offset is the gate for audit finding #141.
//
// Before the fix: extractJPEGThumbnail always called order.Uint32(jifEntry.Value)
// on the JPEGInterchangeFormat entry regardless of type.  For a TypeLong8 entry
// (8-byte Value), this silently read only the lower 4 bytes, yielding a truncated
// offset.  A thumbnail whose offset has any non-zero bits in the upper 32 bits
// would be located at the wrong position (or past the end of the buffer, returning
// nil).
//
// The test builds a BigTIFF IFD1 with:
//   - JPEGInterchangeFormat (0x0201) as TypeLong8, offset = 0x1_0000_0000 + dataOff
//     whose lower 32 bits are zero — an offset that order.Uint32 would read as 0.
//   - JPEGInterchangeFormatLength (0x0202) as TypeLong (classic, fits 4 bytes).
//   - JPEG thumbnail bytes at the correct offset.
//
// Because we cannot allocate >4 GiB in a unit test, we use a synthetic buffer
// laid out so the *lower* 32 bits of the offset are a valid in-buffer offset, but
// the full 64-bit value is what actually locates the data.  Specifically the offset
// stored in the TypeLong8 field points WITHIN the test buffer — but its upper 32
// bits are non-zero to prove the parser reads 8 bytes, not 4.
//
// Practically: we encode offset = 0x1_0000_0000 | realOff where realOff is in
// range.  Since the buffer cannot be that large, we instead use a small synthetic
// test that proves the ENTRY VALUE is read as 64-bit rather than 32-bit: if
// order.Uint32 is used, jifOff = 0 (the lower 32 bits are zero when
// offset = 0x100), which produces a mis-read.  We choose offset bytes so that
// only the 64-bit read produces the correct value.
//
// BigTIFF spec §2 (Aware Systems / libtiff); EXIF §4.5.5.
// Audit finding #141.
func TestExtractJPEGThumbnailBigTIFF_Long8Offset(t *testing.T) {
	t.Parallel()

	// We want to prove that the 8-byte field is read as uint64.
	// Strategy: encode the offset as a value whose lower 4 bytes are 0x00 but
	// whose bytes [4..7] are non-zero.  Concretely: store the offset 0x0100_0000_00
	// — but since that's > 4 GiB and we can't allocate that, we instead store it
	// as 0x0000_0001_0000_00XX where the lower byte XX is the real in-buffer offset.
	//
	// Simpler approach: construct the IFD-only portion of the buffer so that
	// the offset bytes [4..7] of the 8-byte Long8 field carry a non-zero value.
	// We use offset = 0x00000001_000000xx (big upper half, small lower half) but
	// since the upper half exceeds our buffer we rely on a different invariant:
	//
	// The test allocates a buffer sized to hold the IFD + a thumbnail at a real
	// reachable offset.  The TypeLong8 value-or-offset field encodes the thumbnail
	// offset as follows: real offset in bytes [0..3], value 0x01 in bytes [4..7].
	// This means:
	//   order.Uint32(val[0:]) = real_offset   (correct 32-bit read)
	//   order.Uint64(val[0:]) = 0x01_00000000 | real_offset  (64-bit read; too big)
	//
	// That's the wrong direction.  Instead encode high bytes = 0, low bytes = offset:
	// then Uint32 and Uint64 produce the same result, which doesn't test anything.
	//
	// Correct strategy: encode the offset such that the LOWER 4 bytes are ZERO but
	// the full uint64 is the real (small) offset.  That is impossible (lower 4 bytes
	// zero means the offset is a multiple of 2^32, which exceeds any real buffer).
	//
	// CORRECT approach: use an offset whose lower 4 bytes happen to decode to a
	// WRONG location (not 0x00 but some other wrong value), and the full 8-byte
	// read produces the RIGHT location.
	//
	// Encoding: store offset bytes as [wrongLow32, highByte] in little-endian order
	// where wrongLow32 points outside the buffer (so Uint32 returns wrong/oob) and
	// the full Uint64 decodes to the true in-buffer offset.
	// Example: real offset = 300; wrong low32 = 500 (out of range for our ~400-byte
	// buffer).  Encode as LE uint64: bytes = [0xF4,0x01,0x00,0x00, 0x2C,0x01,0x00,0x00]
	// Uint32 → 0x000001F4 = 500 (out of range); Uint64 → 0x0000012C000001F4 (huge, oob).
	//
	// That approach makes both reads wrong.  We need a 64-bit value that, when the
	// lower 32 bits are read as Uint32, gives the WRONG location, but when read as
	// Uint64, gives the RIGHT location.  This is impossible with a single uint64 whose
	// lower 32 bits are different from the full value — the only way Uint64(v)==correct
	// and Uint32(v[0:4])!=correct is if the upper 32 bits somehow affect the result,
	// which they do not when the correct value fits in 32 bits.
	//
	// ALTERNATIVE: assert on the parsed IFDEntry.Type rather than on ThumbnailData
	// content.  We construct a BigTIFF where the JPEGInterchangeFormat entry has
	// TypeLong8 and a plausible in-buffer offset, and verify that ThumbnailData is
	// non-nil (i.e. the thumbnail was extracted successfully).  This proves that
	// extractJPEGThumbnail accepts TypeLong8 entries.  The pre-fix code would return
	// nil here if len(Value) < 4 (it checked length < 4, not type), but actually
	// len(Value) = 8 for TypeLong8 so the Uint32 read would succeed at the WRONG
	// byte slice — but in this test the lower 4 bytes happen to encode the right
	// offset, so we prove the fix by choosing an offset whose LE bytes [0..3] encode
	// a value that differs from the correct uint64.
	//
	// FINAL CLEAN APPROACH: build a buffer where the thumbnail lives at an offset
	// whose little-endian bytes [0..3] encode 0 (a wrong offset) while the full 8
	// bytes encode the real offset.  This requires: real_offset mod 2^32 == 0, i.e.
	// real_offset is a multiple of 4 GiB — impossible in a test.
	//
	// PRAGMATIC GATE: We assert on the TypeLong8 branch being taken by checking that
	// ThumbnailData is non-nil when JPEGInterchangeFormat is TypeLong8 with a
	// valid small in-buffer offset.  Pre-fix code treated TypeLong8 as TypeLong
	// (read 4 bytes via Uint32) — since TypeLong8 value bytes are ordered the same
	// way for in-range offsets, this gave the correct answer by coincidence.  The
	// pre-fix BUG only manifests when the offset has non-zero upper 32 bits.
	//
	// We PROVE the 64-bit read by directly calling extractJPEGThumbnail with a
	// synthetic IFD where the entry Type is TypeLong8 and the Value is an 8-byte
	// LE encoding of a valid offset, and verify the thumbnail is extracted.  We
	// also test a case where the lower 4 bytes of the 8-byte value encode 0 (wrong
	// offset) but the full 8 bytes encode the real offset — impossible to make
	// in-range as noted — so instead we verify the TYPE DISPATCH by injecting an
	// entry whose Value is 8 bytes (TypeLong8), confirm non-nil ThumbnailData, then
	// check the pre-fix behaviour would have failed: we create a SECOND variant
	// where entry.Type is set to TypeLong (4 bytes) with Value = first 4 bytes of
	// the Long8 encoding — if those 4 bytes happen to be the same as the full 8-byte
	// offset (small offset), both produce the same result.  In that case we cannot
	// distinguish, so we instead inject a DELIBERATELY WRONG 4-byte prefix.
	//
	// DEFINITIVE TEST: we build the entry manually with Type=TypeLong8, Value=8 bytes
	// where bytes[0..3] = encodeUint32(badOff) and bytes[4..7] = 0x00.
	// Uint32(val[0:4]) = badOff (out of buffer = nil thumbnail).
	// Uint64(val[0:8]) in LE = badOff | (0 << 32) = badOff (same!).
	// Still the same in LE when upper bytes are 0.
	//
	// ---- The CORRECT encoding to distinguish the reads ----
	// Big-endian: uint64 = realOff (small, fits 32 bits).
	// Bytes [0..7] BE = [0x00, 0x00, 0x00, 0x00, HH, HL, LH, LL].
	// Uint32(bytes[0:4]) = 0x00000000 = 0 (wrong!).
	// Uint64(bytes[0:8]) = realOff (correct!).
	//
	// YES: in BIG-ENDIAN encoding, a small offset is stored in bytes [4..7] with
	// bytes [0..3] = 0.  order.Uint32(val[0:4]) = 0 (wrong).
	// order.Uint64(val[0:8]) = realOff (correct).
	//
	// We use big-endian to distinguish the two reads unambiguously.

	const (
		// JPEG SOI + EOI = minimal 2-byte JPEG marker pair.
		jpegSOI = 0xFF
		jpegEOI = 0xD9 // after SOI 0xFF
	)

	// We'll place the thumbnail at offset 200 inside a ~300-byte buffer.
	const thumbOff = uint64(200)
	const thumbLen = uint32(2) // minimal JPEG: {0xFF, 0xD9}

	// Build a BigTIFF buffer in big-endian order so that a small offset stored
	// as uint64 has its value in bytes [4..7] of the 8-byte field, leaving bytes
	// [0..3] = 0x00.  order.Uint32(val[0:4]) = 0 (wrong); order.Uint64(val) = thumbOff.
	//
	// BigTIFF spec §2 layout:
	//  [0..15]  header (BE: "MM" + 0x002B + 8 + 0 + ifd0Off)
	//  [16..23] IFD0 count = 2
	//  [24..43] entry 0: tag=0x0201 (JPEGInterchangeFormat) TypeLong8 count=1 value=thumbOff(8B BE)
	//  [44..63] entry 1: tag=0x0202 (JPEGInterchangeFormatLength) TypeLong count=1 value=thumbLen(4B BE inline)
	//  [64..71] next-IFD = 0
	//  [72..199] padding
	//  [200..201] thumbnail JPEG bytes
	order := binary.BigEndian
	const bufLen = 202
	buf := make([]byte, bufLen)

	// Header.
	buf[0], buf[1] = 'M', 'M'
	order.PutUint16(buf[2:], 0x002B) // BigTIFF magic
	order.PutUint16(buf[4:], 8)      // offset bytesize
	order.PutUint16(buf[6:], 0)
	const ifd0Off = uint64(16)
	order.PutUint64(buf[8:], ifd0Off)

	// IFD0 entry count = 2.
	order.PutUint64(buf[16:], 2)

	// Entry 0: tag=0x0201, TypeLong8 (16), count=1, value=thumbOff (big-endian uint64).
	// In BE encoding, thumbOff=200=0xC8 occupies only the lowest byte:
	//   buf[36..43] = [0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC8]
	// Uint32(buf[36:40]) = 0x00000000 = 0 (WRONG).
	// Uint64(buf[36:44]) = 0x00000000000000C8 = 200 (CORRECT).
	p := 24
	order.PutUint16(buf[p:], 0x0201)              // tag
	order.PutUint16(buf[p+2:], uint16(TypeLong8)) // TypeLong8
	order.PutUint64(buf[p+4:], 1)                 // count=1
	order.PutUint64(buf[p+12:], thumbOff)         // value = 200 (BE: upper bytes zero)
	p += 20

	// Entry 1: tag=0x0202, TypeLong (4), count=1, value=thumbLen (inline in BE field).
	// TypeLong inline in BigTIFF: value stored left-justified in bytes [0..3] of the 8B field.
	order.PutUint16(buf[p:], 0x0202)             // tag
	order.PutUint16(buf[p+2:], uint16(TypeLong)) // TypeLong
	order.PutUint64(buf[p+4:], 1)                // count=1
	// Inline: value stored in bytes [0..3] of the 8-byte field (left-justified, BE).
	order.PutUint32(buf[p+12:], thumbLen)

	// next-IFD = 0 (already zero).
	// bytes [64..71] are next-IFD — already zero.

	// Thumbnail bytes at offset 200.
	buf[thumbOff] = jpegSOI
	buf[thumbOff+1] = jpegEOI

	// Parse the BigTIFF.
	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}

	// extractJPEGThumbnail is called by parseSingleIFDBigTIFF via parseSingleIFD.
	// The result is in IFD0.ThumbnailData.
	td := e.IFD0.ThumbnailData
	if td == nil {
		t.Fatal("ThumbnailData is nil; extractJPEGThumbnail did not extract the TypeLong8 thumbnail offset")
	}
	if len(td) != int(thumbLen) {
		t.Fatalf("ThumbnailData length = %d, want %d", len(td), thumbLen)
	}
	if td[0] != jpegSOI || td[1] != jpegEOI {
		t.Errorf("ThumbnailData = %v, want [0xFF 0xD9]", td)
	}
}

// TestBigTIFFMakerNoteOffset64 is the gate for audit finding #142.
//
// Before the fix: IFDEntry.rawOffset was uint32 and parseIFDEntryBigTIFF stored
// uint32(valOff & 0xFFFFFFFF) — explicitly truncating BigTIFF offsets above 4 GiB.
// parseExifSubIFDsBigTIFF then assigned mn.rawOffset to EXIF.MakerNoteOffset (uint32),
// losing the upper 32 bits.  EXIF.MakerNoteOffset64 did not exist.
//
// After the fix: rawOffset is uint64, parseIFDEntryBigTIFF stores the full offset,
// and EXIF.MakerNoteOffset64 exposes it without truncation.
//
// The test constructs a BigTIFF with IFD0 → ExifIFD, where ExifIFD has a MakerNote
// entry that is out-of-line (count > 8 for TypeUndefined so the BigTIFF inline
// threshold is exceeded, forcing an OOL fetch and setting rawOffset).  It verifies:
//  1. MakerNoteOffset64 is non-zero and equals the exact buffer offset of the MakerNote.
//  2. MakerNoteOffset equals the lower 32 bits (backward compat, valid for small offsets).
//
// For MakerNotes above 4 GiB (the primary scenario) the test indirectly covers the
// fix: the rawOffset field is now uint64 (widened from uint32) and the truncation
// uint32(valOff & 0xFFFFFFFF) is removed from parseIFDEntryBigTIFF.  The code path
// is exercised for any out-of-line entry; the uint64 precision only matters when
// valOff > 0xFFFFFFFF, which cannot be reproduced in a bounded test buffer.
//
// BigTIFF spec §2 (Aware Systems / libtiff); EXIF §4.6.5 tag 0x927C.
// Audit finding #142.
func TestBigTIFFMakerNoteOffset64(t *testing.T) {
	t.Parallel()

	// Build a BigTIFF with IFD0 → ExifIFD, where ExifIFD contains a MakerNote.
	//
	// The MakerNote must be OUT-OF-LINE (count > 8 bytes for TypeUndefined with
	// element size 1) so that parseIFDEntryBigTIFF fetches via the OOL path and
	// sets rawOffset.  We use 16 bytes of MakerNote data.
	//
	// Layout (LE):
	//  [0..15]   header
	//  [16..23]  IFD0 count = 1
	//  [24..43]  IFD0 entry: tag=0x8769 TypeLong count=1 value=exifIFDOff (inline 4B)
	//  [44..51]  IFD0 next-IFD = 0
	//  [52..59]  ExifIFD count = 1
	//  [60..79]  ExifIFD entry: tag=0x927C TypeUndefined count=16 offset=mnOff (OOL)
	//  [80..87]  ExifIFD next-IFD = 0
	//  [88..103] MakerNote bytes (16 bytes, OOL because 16 > 8 = BigTIFF inline threshold)

	order := binary.LittleEndian
	const (
		hdrSize     = 16
		countSize   = 8
		entrySize   = 20
		nextPtrSize = 8

		ifd0Off    = hdrSize                                                  // 16
		exifIFDOff = ifd0Off + countSize + entrySize + nextPtrSize            // 52
		mnOff      = uint64(exifIFDOff + countSize + entrySize + nextPtrSize) // 88
		mnLen      = uint64(16)                                               // 16 > 8 (BigTIFF inline threshold) → OOL; rawOffset is set
	)

	bufLen := int(mnOff + mnLen)
	buf := make([]byte, bufLen)

	// Header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], uint64(ifd0Off))

	// IFD0: 1 entry — ExifIFD pointer (TypeLong, 4-byte inline).
	order.PutUint64(buf[ifd0Off:], 1)
	p := ifd0Off + countSize
	order.PutUint16(buf[p:], 0x8769) // TagExifIFDPointer
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint64(buf[p+4:], 1)
	order.PutUint32(buf[p+12:], uint32(exifIFDOff))
	// IFD0 next-IFD = 0 (already zero).

	// ExifIFD at offset 52: 1 entry — MakerNote (TypeUndefined, count=16 OOL).
	order.PutUint64(buf[exifIFDOff:], 1)
	q := exifIFDOff + countSize
	order.PutUint16(buf[q:], 0x927C) // TagMakerNote
	order.PutUint16(buf[q+2:], uint16(TypeUndefined))
	order.PutUint64(buf[q+4:], mnLen)  // count=16 (>8 → OOL)
	order.PutUint64(buf[q+12:], mnOff) // offset = 88 (OOL)
	// ExifIFD next-IFD = 0.

	// MakerNote bytes at mnOff (all 0x4E = 'N' to be recognisable).
	for i := range int(mnLen) {
		buf[mnOff+uint64(i)] = 'N'
	}

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.ExifIFD == nil {
		t.Fatal("ExifIFD is nil; ExifIFD pointer not followed")
	}
	if e.MakerNote == nil {
		t.Fatal("MakerNote is nil; MakerNote entry not parsed (check count > 8 for OOL)")
	}

	// MakerNoteOffset64 must equal mnOff exactly (no truncation).
	// Pre-fix: rawOffset was uint32 and MakerNoteOffset64 did not exist.
	// Post-fix: rawOffset is uint64 and MakerNoteOffset64 == mnOff.
	if e.MakerNoteOffset64 == 0 {
		t.Error("MakerNoteOffset64 is 0; rawOffset not propagated to EXIF.MakerNoteOffset64 (#142)")
	}
	if e.MakerNoteOffset64 != mnOff {
		t.Errorf("MakerNoteOffset64 = %d, want %d (#142: 64-bit offset must not be truncated)", e.MakerNoteOffset64, mnOff)
	}
	// MakerNoteOffset (uint32 backward compat) must equal lower 32 bits of mnOff.
	// For small offsets (mnOff < 2^32) both values are identical.
	wantOff32 := uint32(mnOff) // mnOff=88 fits uint32; backward compat check
	if e.MakerNoteOffset != wantOff32 {
		t.Errorf("MakerNoteOffset = %d, want %d", e.MakerNoteOffset, wantOff32)
	}
}

// TestBigTIFFSubIFDTypeShort is the gate for audit finding #143.
//
// Before the fix: readBigTIFFSubIFDOffset only handled TypeLong, TypeLong8, and
// TypeIFD8.  A BigTIFF where the ExifIFD pointer (0x8769) was encoded as TypeShort
// (value fits in 2 bytes) was silently ignored, leaving EXIF.ExifIFD nil and
// causing CameraModel, ISO, and other ExifIFD-derived fields to be empty.
//
// After the fix: readBigTIFFSubIFDOffset also handles TypeShort, reading 2 bytes
// via order.Uint16.
//
// The test constructs a BigTIFF with:
//   - IFD0 entry tag=0x8769 (ExifIFDPointer) TypeShort count=1 value=exifIFDOff (inline 2B)
//   - ExifIFD at exifIFDOff with one entry: CameraModel (tag=0x0110 TypeASCII)
//
// After parsing, EXIF.ExifIFD must be non-nil and CameraModel must be non-empty.
//
// BigTIFF spec §2 (Aware Systems / libtiff); EXIF §4.6.3 tag 0x8769.
// Audit finding #143.
func TestBigTIFFSubIFDTypeShort(t *testing.T) {
	t.Parallel()

	// Layout (LE):
	//  [0..15]   header
	//  [16..23]  IFD0 count = 1
	//  [24..43]  IFD0 entry: tag=0x8769 TypeShort count=1 value=exifIFDOff (inline 2B)
	//  [44..51]  next-IFD = 0
	//  [52..59]  ExifIFD count = 1
	//  [60..79]  ExifIFD entry: tag=0x0110 TypeASCII count=len("TestCamera\x00") offset=modelOff
	//  [80..87]  next-IFD = 0
	//  [88..]    "TestCamera\x00"

	const wantModel = "TestCamera"
	modelBytes := asciiPayload(wantModel)

	order := binary.LittleEndian
	const (
		hdrSize     = 16
		countSize   = 8
		entrySize   = 20
		nextPtrSize = 8

		ifd0Off    = hdrSize                                                                      // 16
		exifIFDOff = uint16(ifd0Off + countSize + entrySize + nextPtrSize)                        // 52; fits uint16
		modelOff   = uint32(int(exifIFDOff) + int(countSize) + int(entrySize) + int(nextPtrSize)) // 88
	)

	bufLen := int(modelOff) + len(modelBytes)
	buf := make([]byte, bufLen)

	// Header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], uint64(ifd0Off))

	// IFD0: 1 entry — ExifIFD pointer (TypeShort, 2-byte inline value).
	// BigTIFF inline threshold = 8 bytes; SHORT (2B) is always inline.
	// value-or-offset bytes [0..1] = exifIFDOff (LE uint16); bytes [2..7] = 0.
	order.PutUint64(buf[ifd0Off:], 1)
	p := ifd0Off + countSize
	order.PutUint16(buf[p:], 0x8769)              // TagExifIFDPointer
	order.PutUint16(buf[p+2:], uint16(TypeShort)) // TypeShort (the point of the test!)
	order.PutUint64(buf[p+4:], 1)                 // count=1
	order.PutUint16(buf[p+12:], exifIFDOff)       // 2-byte value inline in 8-byte field
	// next-IFD = 0.

	// ExifIFD at offset 52.
	order.PutUint64(buf[exifIFDOff:], 1)
	q := int(exifIFDOff) + countSize
	order.PutUint16(buf[q:], 0x0110) // TagModel
	order.PutUint16(buf[q+2:], uint16(TypeASCII))
	order.PutUint64(buf[q+4:], uint64(len(modelBytes)))
	order.PutUint64(buf[q+12:], uint64(modelOff)) // out-of-line offset
	// next-IFD = 0.

	// Model bytes at modelOff.
	copy(buf[modelOff:], modelBytes)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.ExifIFD == nil {
		t.Fatal("ExifIFD is nil; TypeShort ExifIFD pointer was not followed (#143: readBigTIFFSubIFDOffset missing TypeShort case)")
	}

	modelEntry := e.ExifIFD.Get(TagModel)
	if modelEntry == nil {
		t.Fatal("Model entry missing from ExifIFD")
	}
	if got := modelEntry.String(); got != wantModel {
		t.Errorf("CameraModel = %q, want %q", got, wantModel)
	}
}

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
