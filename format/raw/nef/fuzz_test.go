package nef

import (
	"bytes"
	"testing"
)

func FuzzNEFExtract(f *testing.F) {
	// Seed: minimal LE TIFF.
	f.Add(minimalTIFF())

	// Seed: minimal BE TIFF (Nikon D1 used big-endian).
	minBE := make([]byte, 14)
	minBE[0], minBE[1] = 'M', 'M'
	minBE[2], minBE[3] = 0x00, 0x2A
	minBE[4], minBE[5], minBE[6], minBE[7] = 0x00, 0x00, 0x00, 0x08
	f.Add(minBE)

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated header.
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
