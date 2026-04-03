package exif

import "testing"

// FuzzParseEXIF exercises the EXIF parser against arbitrary byte inputs.
// Run with: go test -fuzz=FuzzParseEXIF -fuzztime=60s ./exif/...
func FuzzParseEXIF(f *testing.F) {
	// Seed corpus: minimal valid little-endian TIFF header.
	f.Add([]byte("II\x2A\x00\x08\x00\x00\x00"))
	// Seed corpus: minimal valid big-endian TIFF header.
	f.Add([]byte("MM\x00\x2A\x00\x00\x00\x08"))

	f.Fuzz(func(t *testing.T, b []byte) {
		// Must not panic on any input.
		_, _ = Parse(b)
	})
}
