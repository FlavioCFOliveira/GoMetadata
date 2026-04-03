package cr2

import (
	"bytes"
	"testing"
)

func FuzzCR2Extract(f *testing.F) {
	// Seed: minimal LE TIFF (same base as CR2 without the "CR" signature).
	f.Add(minimalTIFF())

	// Seed: minimal CR2 with "CR" signature at bytes 8-9.
	cr2 := minimalTIFF()
	cr2[8], cr2[9] = 'C', 'R'
	f.Add(cr2)

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated header.
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
