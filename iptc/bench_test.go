package iptc

import "testing"

// BenchmarkDecodeString measures the cost of ISO-8859-1 → UTF-8 decoding via
// the pooled decoder. Input contains non-ASCII bytes (0xE9 = é, 0xFC = ü) to
// exercise the charmap transform path, not just the ASCII fast path.
func BenchmarkDecodeString(b *testing.B) {
	// "Hello étéü" in ISO-8859-1.
	input := []byte{0x48, 0x65, 0x6C, 0x6C, 0x6F, 0x20, 0xE9, 0x74, 0xE9, 0xFC}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = decodeString(input, false)
	}
}
