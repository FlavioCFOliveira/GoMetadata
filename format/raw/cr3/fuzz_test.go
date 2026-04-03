package cr3

import (
	"bytes"
	"testing"
)

func FuzzCR3Extract(f *testing.F) {
	// Seed: minimal CR3 with valid ISOBMFF structure.
	minTIFF := func() []byte {
		buf := make([]byte, 14)
		buf[0], buf[1] = 'I', 'I'
		buf[2], buf[3] = 0x2A, 0x00
		buf[4], buf[5], buf[6], buf[7] = 0x08, 0x00, 0x00, 0x00
		return buf
	}
	f.Add(buildMinimalCR3(minTIFF(), nil))

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated ftyp box.
	f.Add([]byte{0x00, 0x00, 0x00, 0x14, 'f', 't', 'y', 'p'})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
