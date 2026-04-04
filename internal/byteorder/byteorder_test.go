package byteorder

import (
	"encoding/binary"
	"testing"
)

// TestUint16LE exercises little-endian uint16 reads at various offsets.
func TestUint16LE(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
		off  int
		want uint16
	}{
		{"zero value", []byte{0x00, 0x00}, 0, 0x0000},
		{"max value", []byte{0xFF, 0xFF}, 0, 0xFFFF},
		{"little-endian order", []byte{0x34, 0x12}, 0, 0x1234},
		{"non-zero offset", []byte{0x00, 0x78, 0x56}, 1, 0x5678},
		{"0x0001", []byte{0x01, 0x00}, 0, 0x0001},
		{"0x0100", []byte{0x00, 0x01}, 0, 0x0100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Uint16LE(tc.buf, tc.off)
			if got != tc.want {
				t.Errorf("Uint16LE(%v, %d) = 0x%04X, want 0x%04X", tc.buf, tc.off, got, tc.want)
			}
		})
	}
}

// TestUint16BE exercises big-endian uint16 reads at various offsets.
func TestUint16BE(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
		off  int
		want uint16
	}{
		{"zero value", []byte{0x00, 0x00}, 0, 0x0000},
		{"max value", []byte{0xFF, 0xFF}, 0, 0xFFFF},
		{"big-endian order", []byte{0x12, 0x34}, 0, 0x1234},
		{"non-zero offset", []byte{0x00, 0x56, 0x78}, 1, 0x5678},
		{"0x0001", []byte{0x00, 0x01}, 0, 0x0001},
		{"0x0100", []byte{0x01, 0x00}, 0, 0x0100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Uint16BE(tc.buf, tc.off)
			if got != tc.want {
				t.Errorf("Uint16BE(%v, %d) = 0x%04X, want 0x%04X", tc.buf, tc.off, got, tc.want)
			}
		})
	}
}

// TestUint32LE exercises little-endian uint32 reads.
func TestUint32LE(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
		off  int
		want uint32
	}{
		{"zero value", []byte{0x00, 0x00, 0x00, 0x00}, 0, 0x00000000},
		{"max value", []byte{0xFF, 0xFF, 0xFF, 0xFF}, 0, 0xFFFFFFFF},
		{"little-endian order", []byte{0x78, 0x56, 0x34, 0x12}, 0, 0x12345678},
		{"non-zero offset", []byte{0x00, 0xEF, 0xCD, 0xAB, 0x89}, 1, 0x89ABCDEF},
		{"0x00000001", []byte{0x01, 0x00, 0x00, 0x00}, 0, 0x00000001},
		{"0x01000000", []byte{0x00, 0x00, 0x00, 0x01}, 0, 0x01000000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Uint32LE(tc.buf, tc.off)
			if got != tc.want {
				t.Errorf("Uint32LE(%v, %d) = 0x%08X, want 0x%08X", tc.buf, tc.off, got, tc.want)
			}
		})
	}
}

// TestUint32BE exercises big-endian uint32 reads.
func TestUint32BE(t *testing.T) {
	tests := []struct {
		name string
		buf  []byte
		off  int
		want uint32
	}{
		{"zero value", []byte{0x00, 0x00, 0x00, 0x00}, 0, 0x00000000},
		{"max value", []byte{0xFF, 0xFF, 0xFF, 0xFF}, 0, 0xFFFFFFFF},
		{"big-endian order", []byte{0x12, 0x34, 0x56, 0x78}, 0, 0x12345678},
		{"non-zero offset", []byte{0x00, 0x89, 0xAB, 0xCD, 0xEF}, 1, 0x89ABCDEF},
		{"0x00000001", []byte{0x00, 0x00, 0x00, 0x01}, 0, 0x00000001},
		{"0x01000000", []byte{0x01, 0x00, 0x00, 0x00}, 0, 0x01000000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Uint32BE(tc.buf, tc.off)
			if got != tc.want {
				t.Errorf("Uint32BE(%v, %d) = 0x%08X, want 0x%08X", tc.buf, tc.off, got, tc.want)
			}
		})
	}
}

// TestUint16LEBESymmetry verifies that LE and BE read the same bytes in
// opposite order, i.e. their results are byte-swapped versions of each other.
func TestUint16LEBESymmetry(t *testing.T) {
	buf := []byte{0xAB, 0xCD}
	le := Uint16LE(buf, 0)
	be := Uint16BE(buf, 0)
	// swap le bytes and compare with be
	swapped := le>>8 | le<<8
	if swapped != be {
		t.Errorf("byte-swap symmetry failed: LE=0x%04X, BE=0x%04X, swap(LE)=0x%04X", le, be, swapped)
	}
}

// TestUint32LEBESymmetry verifies byte-swap symmetry for 32-bit reads.
func TestUint32LEBESymmetry(t *testing.T) {
	buf := []byte{0x12, 0x34, 0x56, 0x78}
	le := Uint32LE(buf, 0)
	be := Uint32BE(buf, 0)
	// manual byte swap of le
	swapped := (le>>24)&0xFF | (le>>8)&0xFF00 | (le<<8)&0xFF0000 | (le<<24)&0xFF000000
	if swapped != be {
		t.Errorf("byte-swap symmetry failed: LE=0x%08X, BE=0x%08X, swap(LE)=0x%08X", le, be, swapped)
	}
}

// TestUint16WithOrder verifies that Uint16 dispatches correctly for both orders.
func TestUint16WithOrder(t *testing.T) {
	buf := []byte{0x12, 0x34}
	if got := Uint16(buf, 0, binary.LittleEndian); got != Uint16LE(buf, 0) {
		t.Errorf("Uint16(LE) = 0x%04X, want 0x%04X", got, Uint16LE(buf, 0))
	}
	if got := Uint16(buf, 0, binary.BigEndian); got != Uint16BE(buf, 0) {
		t.Errorf("Uint16(BE) = 0x%04X, want 0x%04X", got, Uint16BE(buf, 0))
	}
}

// TestUint32WithOrder verifies that Uint32 dispatches correctly for both orders.
func TestUint32WithOrder(t *testing.T) {
	buf := []byte{0x12, 0x34, 0x56, 0x78}
	if got := Uint32(buf, 0, binary.LittleEndian); got != Uint32LE(buf, 0) {
		t.Errorf("Uint32(LE) = 0x%08X, want 0x%08X", got, Uint32LE(buf, 0))
	}
	if got := Uint32(buf, 0, binary.BigEndian); got != Uint32BE(buf, 0) {
		t.Errorf("Uint32(BE) = 0x%08X, want 0x%08X", got, Uint32BE(buf, 0))
	}
}

// TestUint16LEPanicOnShortSlice confirms that Uint16LE panics when the slice
// is too short; callers are expected to validate bounds beforehand.
func TestUint16LEPanicOnShortSlice(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic on out-of-bounds read, got none")
		}
	}()
	Uint16LE([]byte{0x01}, 0) // only 1 byte — need 2
}

// TestUint32BEPanicOnShortSlice confirms that Uint32BE panics on too-short input.
func TestUint32BEPanicOnShortSlice(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic on out-of-bounds read, got none")
		}
	}()
	Uint32BE([]byte{0x01, 0x02, 0x03}, 0) // only 3 bytes — need 4
}

// BenchmarkUint16LE measures the cost of a single little-endian uint16 read.
func BenchmarkUint16LE(b *testing.B) {
	buf := []byte{0x34, 0x12}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Uint16LE(buf, 0)
	}
}

// BenchmarkUint32LE measures the cost of a single little-endian uint32 read.
func BenchmarkUint32LE(b *testing.B) {
	buf := []byte{0x78, 0x56, 0x34, 0x12}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Uint32LE(buf, 0)
	}
}
