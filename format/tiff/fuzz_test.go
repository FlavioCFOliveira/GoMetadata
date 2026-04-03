package tiff

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func FuzzTIFFExtract(f *testing.F) {
	// Seed: minimal little-endian TIFF with 0-entry IFD0.
	minLE := make([]byte, 14)
	minLE[0], minLE[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(minLE[2:], 0x002A)
	binary.LittleEndian.PutUint32(minLE[4:], 8)
	f.Add(minLE)

	// Seed: minimal big-endian TIFF with 0-entry IFD0.
	minBE := make([]byte, 14)
	minBE[0], minBE[1] = 'M', 'M'
	binary.BigEndian.PutUint16(minBE[2:], 0x002A)
	binary.BigEndian.PutUint32(minBE[4:], 8)
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
