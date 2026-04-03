package orf

import (
	"bytes"
	"testing"
)

func FuzzORFExtract(f *testing.F) {
	// Seed: minimal ORF with correct magic.
	f.Add(buildORF())

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated header.
	f.Add(append(orfMagic, 0x00))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
