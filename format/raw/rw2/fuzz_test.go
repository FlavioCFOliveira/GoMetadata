package rw2

import (
	"bytes"
	"testing"
)

func FuzzRW2Extract(f *testing.F) {
	// Seed: minimal RW2 with correct magic.
	f.Add(buildRW2())

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated header.
	f.Add(append(rw2Magic, 0x00))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
