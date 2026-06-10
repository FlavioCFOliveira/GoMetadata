package exif

// task199_byteorder_flag_test.go — Regression gate for task #199.
//
// task #199 (performance audit 2026-06-10): replace the 16-byte
// binary.ByteOrder interface in IFDEntry with a 1-byte bool bigEndian flag.
// The interface held a GC-scannable pointer on every entry, inflating the
// arena backing arrays introduced in task #198 and increasing GC scan work.
//
// Expected size reduction: 56 B → 48 B (−14.3%).
//
// This test file pins the new layout so any future change that widens
// IFDEntry past 48 bytes is caught immediately.
//
// Spec references: CIPA DC-008-2023 §4.6.2; TIFF 6.0 §2.

import (
	"testing"
	"unsafe"
)

// TestIFDEntrySize asserts that IFDEntry fits within the 48-byte budget
// established by task #199.
//
// Layout after task #199 (64-bit platforms; Go struct alignment rules):
//
//	Tag        TagID    (uint16)  2 B  @ offset 0
//	Type       DataType (uint16)  2 B  @ offset 2
//	Count      uint32             4 B  @ offset 4
//	Value      []byte            24 B  @ offset 8  (pointer + len + cap)
//	bigEndian  bool               1 B  @ offset 32
//	_pad                          7 B  (alignment gap before rawOffset)
//	rawOffset  uint64             8 B  @ offset 40
//	                             ─────
//	Total                        48 B
//
// If the size regresses above 48 B this test fails, surfacing the regression
// before it silently inflates the arena backing arrays.
func TestIFDEntrySize(t *testing.T) {
	t.Parallel()

	// task #199: IFDEntry must be exactly 48 bytes on 64-bit platforms.
	// 56 bytes was the previous size (with binary.ByteOrder interface = 16 B).
	const wantSize = uintptr(48)
	got := unsafe.Sizeof(IFDEntry{})
	if got != wantSize {
		t.Errorf("unsafe.Sizeof(IFDEntry{}) = %d, want %d — task #199 size regression (CIPA DC-008-2023 §4.6.2)", got, wantSize)
	}
}

// TestIFDEntryOrder_LittleEndian verifies that an IFDEntry with bigEndian=false
// returns binary.LittleEndian from its order() helper, which in turn is used
// by all decoder methods (Uint16, Uint32, Rational, etc.).
//
// This is a correctness gate for the bool→ByteOrder translation.
func TestIFDEntryOrder_LittleEndian(t *testing.T) {
	t.Parallel()

	e := IFDEntry{bigEndian: false}
	ord := e.order()

	// Write a known LE-encoded uint32 and verify the decoder reads it correctly.
	// 0x01000000 in LE is the byte sequence [0x00, 0x00, 0x00, 0x01] in memory? No.
	// LE: value 0x01020304 → bytes [0x04, 0x03, 0x02, 0x01]
	val := []byte{0x04, 0x03, 0x02, 0x01}
	got := ord.Uint32(val)
	if got != 0x01020304 {
		t.Errorf("order() LE: Uint32([04 03 02 01]) = 0x%08X, want 0x01020304", got)
	}
}

// TestIFDEntryOrder_BigEndian verifies that an IFDEntry with bigEndian=true
// returns binary.BigEndian from its order() helper.
func TestIFDEntryOrder_BigEndian(t *testing.T) {
	t.Parallel()

	e := IFDEntry{bigEndian: true}
	ord := e.order()

	// BE: value 0x01020304 → bytes [0x01, 0x02, 0x03, 0x04]
	val := []byte{0x01, 0x02, 0x03, 0x04}
	got := ord.Uint32(val)
	if got != 0x01020304 {
		t.Errorf("order() BE: Uint32([01 02 03 04]) = 0x%08X, want 0x01020304", got)
	}
}

// TestIFDEntryOrder_ZeroValue verifies that the zero value of IFDEntry
// (bigEndian == false, i.e. the Go zero value) returns little-endian, which
// preserves the library's default LE convention for programmatically
// constructed EXIF structs.
//
// Regression gate for audit finding #189: before task #199 the zero value was
// a nil binary.ByteOrder interface which panicked on any method call; the bool
// zero value is well-defined and safe.
func TestIFDEntryOrder_ZeroValue(t *testing.T) {
	t.Parallel()

	var e IFDEntry // all fields at zero value
	ord := e.order()

	// The zero value must return LE (false = LE, the library default).
	// 0x00000001 LE: bytes [0x01, 0x00, 0x00, 0x00]
	val := []byte{0x01, 0x00, 0x00, 0x00}
	got := ord.Uint32(val)
	if got != 0x00000001 {
		t.Errorf("order() zero value: Uint32([01 00 00 00]) = 0x%08X, want 0x00000001 (LE default)", got)
	}
}
