package jpeg

import (
	"bytes"
	"testing"
)

func FuzzJPEGExtract(f *testing.F) {
	// Seed: minimal JPEG (SOI + EOI).
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xD9})

	// Seed: JPEG with a single APP1 marker and truncated length.
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x08, 'E', 'x', 'i', 'f', 0x00, 0x00})

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: SOI only.
	f.Add([]byte{0xFF, 0xD8})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
