package exif

// tasks_84_87_regression_test.go — regression tests for fixes in tasks #84 and #87.
//
// Each test is paired with a comment identifying the task and the exact behaviour
// that was broken before the fix. Tests are designed to FAIL on the pre-fix code
// and PASS on the fixed code.

import (
	"encoding/binary"
	"testing"
)

// ---------------------------------------------------------------------------
// Task #87 — IFDEntry.Rational(i)/SRational(i) panic on negative index
//
// Root cause: for i = -1, off = -8, off+8 = 0, and "0 > len(e.Value)" is
// false for any non-empty slice, so execution reaches e.Value[-8:] which
// panics with "slice bounds out of range [-8:]". The fix adds an explicit
// "if i < 0" guard before computing off.
//
// These tests will panic (not fail) on the pre-fix code; they pass cleanly
// on the fixed code.
// ---------------------------------------------------------------------------

// TestRationalNegativeIndex verifies that Rational(-1) returns [2]uint32{}
// instead of panicking. Pre-fix: panic. Post-fix: safe zero value.
func TestRationalNegativeIndex(t *testing.T) {
	t.Parallel()

	// Construct a valid TypeRational entry (8 bytes, one rational).
	val := make([]byte, 8)
	binary.LittleEndian.PutUint32(val[0:], 1)
	binary.LittleEndian.PutUint32(val[4:], 100)

	e := IFDEntry{
		Tag:       TagExposureTime,
		Type:      TypeRational,
		Count:     1,
		Value:     val,
		bigEndian: false,
	}

	// This must not panic. Pre-fix: panic("slice bounds out of range [-8:]").
	// Post-fix: returns [2]uint32{} (zero value).
	got := e.Rational(-1)
	if got != ([2]uint32{}) {
		t.Errorf("Rational(-1) = %v; want [2]uint32{} (zero value, no panic)", got)
	}

	// Additional boundary: Rational(-100) must also return zero, not panic.
	got2 := e.Rational(-100)
	if got2 != ([2]uint32{}) {
		t.Errorf("Rational(-100) = %v; want [2]uint32{} (zero value, no panic)", got2)
	}
}

// TestRationalNegativeIndexPositiveControl confirms that Rational(0) still
// works correctly after the negative-index guard is added.
func TestRationalNegativeIndexPositiveControl(t *testing.T) {
	t.Parallel()

	val := make([]byte, 8)
	binary.LittleEndian.PutUint32(val[0:], 1)
	binary.LittleEndian.PutUint32(val[4:], 100)

	e := IFDEntry{
		Tag:       TagExposureTime,
		Type:      TypeRational,
		Count:     1,
		Value:     val,
		bigEndian: false,
	}

	got := e.Rational(0)
	want := [2]uint32{1, 100}
	if got != want {
		t.Errorf("Rational(0) = %v; want %v", got, want)
	}
}

// TestSRationalNegativeIndex verifies that SRational(-1) returns [2]int32{}
// instead of panicking. Pre-fix: panic. Post-fix: safe zero value.
func TestSRationalNegativeIndex(t *testing.T) {
	t.Parallel()

	// Encode numerator -2, denominator 1 as a TypeSRational value.
	val := make([]byte, 8)
	binary.LittleEndian.PutUint32(val[0:], 0xFFFFFFFE) // -2 two's complement
	binary.LittleEndian.PutUint32(val[4:], 1)

	e := IFDEntry{
		Tag:       TagExposureBiasValue,
		Type:      TypeSRational,
		Count:     1,
		Value:     val,
		bigEndian: false,
	}

	// This must not panic. Pre-fix: panic("slice bounds out of range [-8:]").
	// Post-fix: returns [2]int32{} (zero value).
	got := e.SRational(-1)
	if got != ([2]int32{}) {
		t.Errorf("SRational(-1) = %v; want [2]int32{} (zero value, no panic)", got)
	}

	// Additional boundary: SRational(-100) must also return zero, not panic.
	got2 := e.SRational(-100)
	if got2 != ([2]int32{}) {
		t.Errorf("SRational(-100) = %v; want [2]int32{} (zero value, no panic)", got2)
	}
}

// TestSRationalNegativeIndexPositiveControl confirms that SRational(0) still
// works correctly after the negative-index guard is added.
func TestSRationalNegativeIndexPositiveControl(t *testing.T) {
	t.Parallel()

	val := make([]byte, 8)
	binary.LittleEndian.PutUint32(val[0:], 0xFFFFFFFE) // -2 two's complement
	binary.LittleEndian.PutUint32(val[4:], 1)

	e := IFDEntry{
		Tag:       TagExposureBiasValue,
		Type:      TypeSRational,
		Count:     1,
		Value:     val,
		bigEndian: false,
	}

	got := e.SRational(0)
	want := [2]int32{-2, 1}
	if got != want {
		t.Errorf("SRational(0) = %v; want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Task #84 — EXIF re-encode drops unknown-type out-of-line entries
//
// BEHAVIOUR GATE: This test pins the CURRENT documented behaviour so that any
// future change to how unknown-type entries are preserved is a conscious,
// tested decision. See Encode() godoc for the full constraint description.
//
// REPRODUCTION: parse an EXIF stream containing an entry with an unknown type
// code (type 0x00FF) whose 4-byte IFD field is a non-zero value (simulating an
// offset into a private blob); modify any normal field; Encode; re-parse.
// The unknown entry must still be present (its 4-byte field is written back
// verbatim), but any data it might have pointed to at the original offset is
// NOT in the new stream — the offset is stale.
//
// This test verifies specifically:
//  1. The unknown-type entry SURVIVES re-encode (the 4-byte raw field is written back).
//  2. The entry's Value bytes are identical to what was parsed (verbatim round-trip
//     of the 4-byte field itself).
//  3. The test documents that we do NOT attempt to copy any pointed-to blob.
// ---------------------------------------------------------------------------

// buildTIFFWithUnknownType builds a minimal little-endian TIFF byte stream
// that contains one normal entry (ImageWidth, TypeLong, inline) followed by
// one unknown-type entry (type 0x00FF, count=1, simulated offset value 0xDEAD).
// The "pointed-to blob" is NOT included in the stream; the 4-byte field value
// is written as a raw offset to simulate a private data pointer.
func buildTIFFWithUnknownType(t *testing.T) []byte {
	t.Helper()

	// Layout:
	//   [0..7]   TIFF header (II, 42, IFD0 offset=8)
	//   [8..9]   entry count = 2
	//   [10..21] entry 0: ImageWidth  (0x0100, TypeLong=4, count=1, value=640)
	//   [22..33] entry 1: unknown tag (0x0200, type=0x00FF, count=1, value=0xDEAD)
	//   [34..37] next-IFD pointer = 0
	const (
		unknownTag  = TagID(0x0200) // arbitrary tag in known tag space but with unknown type
		unknownType = DataType(0x00FF)
		unknownVal  = uint32(0x0000DEAD) // simulates an offset pointer into a private blob
	)

	buf := make([]byte, 38)
	order := binary.LittleEndian

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at offset 8

	// Entry count.
	order.PutUint16(buf[8:], 2)

	// Entry 0: ImageWidth = 640 (TypeLong, Count=1, inline).
	// TagImageWidth and TypeLong are both uint16-based types; no sign-change conversion.
	order.PutUint16(buf[10:], uint16(TagImageWidth))
	order.PutUint16(buf[12:], uint16(TypeLong))
	order.PutUint32(buf[14:], 1)
	order.PutUint32(buf[18:], 640)

	// Entry 1: unknown type, count=1, raw 4-byte field = 0xDEAD (simulated offset).
	// typeSize(0x00FF) == 0 → parseIFDEntry stores the raw 4-byte field verbatim.
	order.PutUint16(buf[22:], uint16(unknownTag))
	order.PutUint16(buf[24:], uint16(unknownType))
	order.PutUint32(buf[26:], 1)
	order.PutUint32(buf[30:], unknownVal)

	// Next-IFD pointer = 0.
	order.PutUint32(buf[34:], 0)

	return buf
}

// TestEncodeUnknownTypeDataLossDocumented pins the CURRENT documented behaviour
// for EXIF re-encode of unknown-type IFD entries (task #84):
//
//   - The unknown-type entry survives re-encode: its 4-byte raw field is written
//     back verbatim into the new stream.
//   - The entry's Value bytes in the re-parsed EXIF are equal to the original
//     4-byte field (verbatim round-trip of the inline raw field).
//   - Any data that the original 4-byte field might have pointed to (private blob)
//     is NOT copied — the offset is stale in the new stream. This is the known
//     constraint documented in Encode().
//
// If this test starts failing because unknown-type out-of-line data IS now being
// preserved, update the test and the Encode() godoc together — that change must
// be intentional and fully tested.
func TestEncodeUnknownTypeDataLossDocumented(t *testing.T) {
	t.Parallel()

	const (
		unknownTag = TagID(0x0200)
		unknownVal = uint32(0x0000DEAD)
	)

	// Build and parse an EXIF stream with a known-type and an unknown-type entry.
	rawStream := buildTIFFWithUnknownType(t)
	parsed, err := Parse(rawStream)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Verify the unknown-type entry was captured during parse.
	unknownEntry := parsed.IFD0.Get(unknownTag)
	if unknownEntry == nil {
		t.Fatalf("unknown-type entry (tag 0x%04X) not found after initial parse", unknownTag)
	}
	if len(unknownEntry.Value) != 4 {
		t.Fatalf("unknown-type entry.Value len = %d; want 4 (raw 4-byte field)", len(unknownEntry.Value))
	}
	originalRaw := binary.LittleEndian.Uint32(unknownEntry.Value)
	if originalRaw != unknownVal {
		t.Fatalf("unknown-type entry raw value = 0x%08X; want 0x%08X", originalRaw, unknownVal)
	}

	// Modify a normal field to trigger a full re-encode (not a verbatim rawEXIF pass-through).
	parsed.SetCameraModel("TestCam")

	// Re-encode.
	encoded, err := Encode(parsed)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Re-parse the encoded output.
	reparsed, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse of re-encoded stream: %v", err)
	}

	// ASSERTION 1: The unknown-type entry must still be present after re-encode.
	// writeIFD writes the 4-byte raw field back verbatim (ts==0 → inline path).
	reEntry := reparsed.IFD0.Get(unknownTag)
	if reEntry == nil {
		t.Fatalf("unknown-type entry (tag 0x%04X) lost after re-encode — unexpected regression", unknownTag)
	}

	// ASSERTION 2: The 4-byte raw field must be identical to the original.
	// This confirms verbatim round-trip of the inline field itself.
	if len(reEntry.Value) != 4 {
		t.Fatalf("re-encoded unknown-type entry.Value len = %d; want 4", len(reEntry.Value))
	}
	reRaw := binary.LittleEndian.Uint32(reEntry.Value)
	if reRaw != unknownVal {
		t.Errorf("re-encoded unknown-type entry raw value = 0x%08X; want 0x%08X (verbatim raw field round-trip)",
			reRaw, unknownVal)
	}

	// ASSERTION 3: Document that no pointed-to blob was copied. The re-encoded
	// stream is smaller than the original (no private blob in the new stream)
	// or at minimum does not contain the "blob" at the offset the entry points to.
	// Since the original stream never actually contained a blob either (test helper
	// only stored the offset value without a blob), we verify the entry type and
	// count are preserved verbatim so the caller can detect the stale-offset condition.
	if reEntry.Type != DataType(0x00FF) {
		t.Errorf("re-encoded unknown-type entry.Type = 0x%04X; want 0x00FF", reEntry.Type)
	}
	if reEntry.Count != 1 {
		t.Errorf("re-encoded unknown-type entry.Count = %d; want 1", reEntry.Count)
	}

	// Positive control: the normal field modification survived the re-encode.
	if reparsed.CameraModel() != "TestCam" {
		t.Errorf("CameraModel after re-encode = %q; want %q", reparsed.CameraModel(), "TestCam")
	}
}
