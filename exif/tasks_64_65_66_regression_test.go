package exif

// tasks_64_65_66_regression_test.go — regression tests for fixes in tasks #64, #65, #66.
//
// Each test is paired with a comment identifying the task and the exact behaviour
// that was broken before the fix. The tests are designed to FAIL on the pre-fix
// code and PASS on the fixed code.

import (
	"encoding/binary"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// Task #64 — IFDEntry.Rational() accepted TypeSRational, returning wrong bits
//
// EXIF 2.32 CIPA DC-008-2023 §4.6.3: RATIONAL and SRATIONAL are distinct types.
// Rational() must only accept TypeRational; callers needing signed values must
// use SRational(). Before the fix, Rational() also accepted TypeSRational and
// returned the int32 bit-pattern as a large uint32 (e.g. −2 → 4294967294).
// ---------------------------------------------------------------------------

// TestRationalRejectsSRational asserts that Rational(0) returns [2]uint32{} for
// a TypeSRational entry, never misinterpreting the signed bit-pattern as unsigned.
func TestRationalRejectsSRational(t *testing.T) {
	t.Parallel()

	// Encode numerator −2, denominator 3 as a TypeSRational value.
	// In two's-complement LE: −2 = 0xFFFFFFFE.
	val := make([]byte, 8)
	binary.LittleEndian.PutUint32(val[0:], 0xFFFFFFFE) // −2
	binary.LittleEndian.PutUint32(val[4:], 3)

	e := IFDEntry{
		Tag:       TagExposureBiasValue,
		Type:      TypeSRational,
		Count:     1,
		Value:     val,
		bigEndian: false,
	}

	got := e.Rational(0)
	if got != ([2]uint32{}) {
		// Pre-fix: returns [2]uint32{4294967294, 3} — the bit pattern of −2 treated
		// as an unsigned integer. Post-fix: Rational() rejects TypeSRational and
		// returns the zero value.
		t.Errorf("Rational(0) on TypeSRational entry = %v; want [2]uint32{} (zero/empty)",
			got)
	}
}

// TestSRationalNegativeNumeratorPositiveControl is a positive-control companion:
// SRational(0) must still return the correct signed pair for the same entry.
func TestSRationalNegativeNumeratorPositiveControl(t *testing.T) {
	t.Parallel()

	val := make([]byte, 8)
	binary.LittleEndian.PutUint32(val[0:], 0xFFFFFFFE) // −2 in two's complement
	binary.LittleEndian.PutUint32(val[4:], 3)

	e := IFDEntry{
		Tag:       TagExposureBiasValue,
		Type:      TypeSRational,
		Count:     1,
		Value:     val,
		bigEndian: false,
	}

	got := e.SRational(0)
	want := [2]int32{-2, 3}
	if got != want {
		t.Errorf("SRational(0) = %v; want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Task #65 — IFD upsert (format/tiff upsertIFD0Entry) could violate sorted order
//
// The test lives here (package exif) because it exercises IFD.set() directly.
// The format/tiff regression test lives in format/tiff/bug65_regression_test.go.
//
// This test verifies the invariant that IFD.set() always maintains sorted-unique
// entries: replace existing tag in place; insert new tag at sorted position;
// never produce duplicates.
// ---------------------------------------------------------------------------

// TestIFDSetSortedUniqueInvariant verifies that set() maintains the sorted-unique
// invariant when inserting tags in reverse order, random order, and when replacing
// an existing tag.
func TestIFDSetSortedUniqueInvariant(t *testing.T) {
	t.Parallel()

	ifd := &IFD{}

	// Insert tags in reverse order (worst case for naive append-then-sort).
	// Tags chosen: ImageWidth(0x0100), IPTC(0x83BB), ExifIFDPointer(0x8769), XMP(0x02BC=700).
	// In hex sorted order: 0x0100 < 0x02BC < 0x8769 < 0x83BB.
	ifd.set(TagIPTC, TypeUndefined, 4, []byte{1, 2, 3, 4}, false)      // 0x83BB = 33723
	ifd.set(TagExifIFDPointer, TypeLong, 1, []byte{0, 0, 0, 0}, false) // 0x8769 = 34665
	ifd.set(TagXMP, TypeUndefined, 4, []byte{5, 6, 7, 8}, false)       // 0x02BC = 700
	ifd.set(TagImageWidth, TypeLong, 1, []byte{128, 0, 0, 0}, false)   // 0x0100 = 256

	// Verify sorted order.
	for i := 1; i < len(ifd.Entries); i++ {
		if ifd.Entries[i].Tag <= ifd.Entries[i-1].Tag {
			t.Errorf("entries not sorted at index %d: entries[%d].Tag=0x%04X <= entries[%d].Tag=0x%04X",
				i, i, ifd.Entries[i].Tag, i-1, ifd.Entries[i-1].Tag)
		}
	}

	// Verify no duplicates.
	seen := make(map[TagID]bool)
	for _, e := range ifd.Entries {
		if seen[e.Tag] {
			t.Errorf("duplicate tag 0x%04X in IFD entries", e.Tag)
		}
		seen[e.Tag] = true
	}

	// Verify all four tags are present.
	if len(ifd.Entries) != 4 {
		t.Errorf("expected 4 entries, got %d", len(ifd.Entries))
	}

	// Replace an existing tag: count must stay at 4.
	ifd.set(TagXMP, TypeUndefined, 4, []byte{9, 10, 11, 12}, false)
	if len(ifd.Entries) != 4 {
		t.Errorf("after replace: expected 4 entries, got %d (duplicate?)", len(ifd.Entries))
	}
	// Verify the value was actually replaced.
	e := ifd.Get(TagXMP)
	if e == nil {
		t.Fatal("TagXMP not found after replace")
	}
	if e.Value[0] != 9 {
		t.Errorf("TagXMP value not updated: got %v, want first byte=9", e.Value)
	}

	// Binary-search Get must find each inserted tag.
	for _, tag := range []TagID{TagImageWidth, TagXMP, TagExifIFDPointer, TagIPTC} {
		if ifd.Get(tag) == nil {
			t.Errorf("Get(0x%04X) returned nil after set()", tag)
		}
	}
}

// ---------------------------------------------------------------------------
// Task #66 — ifdTotalSize uint32 overflow for large Count values
//
// Before the fix: sz += uint32(total) where total = uint64(typeSize)*uint64(Count).
// For Count=0xFFFFFFFF and TypeRational (typeSize=8), total ≈ 34 GiB; uint32(total)
// wraps to a small value, producing an under-reported IFD size.
// After the fix: ifdTotalSize accumulates in uint64 and caps at math.MaxUint32,
// never returning a value smaller than the true size modulo 4 GiB.
// ---------------------------------------------------------------------------

// TestIFDTotalSizeWrap constructs an IFD with a TypeRational entry whose Count
// equals math.MaxUint32 and asserts that ifdTotalSize returns math.MaxUint32
// (saturated), never the wrapped-small value from pre-fix uint32 truncation.
//
// Pre-fix calculation:
//
//	8 * (MaxUint32) = 0x7_FFFF_FFF8; uint32 truncation = 0xFFFFFFF8 = 4294967288.
//	Base IFD size = 18; 18 + 4294967288 = 4294967306; wraps to 10.
//	Pre-fix returns 10; post-fix returns math.MaxUint32 (4294967295, saturated).
func TestIFDTotalSizeWrap(t *testing.T) {
	t.Parallel()

	entries := []IFDEntry{
		{
			Tag:   TagMake,
			Type:  TypeRational,
			Count: math.MaxUint32,
			Value: []byte{0, 0, 0, 0, 0, 0, 0, 0}, // stub — only Count matters for size
		},
	}

	got := ifdTotalSize(entries)
	if got != math.MaxUint32 {
		t.Errorf("ifdTotalSize with Count=MaxUint32 TypeRational = %d; want %d (math.MaxUint32, saturated)",
			got, uint32(math.MaxUint32))
	}
}

// TestIFDTotalSizeNoOverflowNormalInput verifies that ifdTotalSize returns the
// correct exact value for normal (non-overflow) input after the fix.
func TestIFDTotalSizeNoOverflowNormalInput(t *testing.T) {
	t.Parallel()

	// TypeRational (8 bytes), Count=2 → value area = 16 bytes (out-of-line).
	// IFD block = 2 + 1*12 + 4 = 18. Value area = 16. Total = 34.
	entries := []IFDEntry{
		{Tag: TagMake, Type: TypeRational, Count: 2, Value: make([]byte, 16)},
	}
	got := ifdTotalSize(entries)
	want := uint32(2 + 1*12 + 4 + 16) // 34
	if got != want {
		t.Errorf("ifdTotalSize = %d; want %d", got, want)
	}
}
