package iptc

import "testing"

// FuzzParseIPTC exercises the IPTC parser against arbitrary byte inputs.
// Run with: go test -fuzz=FuzzParseIPTC -fuzztime=60s ./iptc/...
func FuzzParseIPTC(f *testing.F) {
	// Seed: minimal IPTC marker (0x1C) + record 2, dataset 120, length 0.
	f.Add([]byte{0x1C, 0x02, 0x78, 0x00, 0x00})

	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Parse(b)
	})
}
