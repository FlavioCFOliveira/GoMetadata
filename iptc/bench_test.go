package iptc

import "testing"

// BenchmarkDecodeString measures the cost of ISO-8859-1 → UTF-8 decoding.
// Input contains non-ASCII bytes (0xE9 = é, 0xFC = ü) to exercise the
// 2-byte-UTF-8-emitting path, not just the ASCII fast path (task #205).
func BenchmarkDecodeString(b *testing.B) {
	// "Hello étéü" in ISO-8859-1.
	input := []byte{0x48, 0x65, 0x6C, 0x6C, 0x6F, 0x20, 0xE9, 0x74, 0xE9, 0xFC}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = decodeString(input, false)
	}
}

// BenchmarkDecodeStringASCII measures decodeString's fast path for a
// non-UTF-8-declared value that happens to contain only 7-bit ASCII bytes
// (the overwhelmingly common case for real-world IPTC streams). Task #205:
// this path must cost a single string(b) allocation, identical to the
// isUTF8=true path, instead of round-tripping through a decoder.
func BenchmarkDecodeStringASCII(b *testing.B) {
	input := []byte("Hello, this is a plain ASCII IPTC caption value.")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = decodeString(input, false)
	}
}
