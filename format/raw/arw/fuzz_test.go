package arw

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func FuzzARWExtract(f *testing.F) {
	// Seed: minimal LE TIFF.
	minLE := make([]byte, 14)
	minLE[0], minLE[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(minLE[2:], 0x002A)
	binary.LittleEndian.PutUint32(minLE[4:], 8)
	f.Add(minLE)

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated header.
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
