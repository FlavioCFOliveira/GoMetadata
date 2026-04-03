package dng

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func FuzzDNGExtract(f *testing.F) {
	// Seed: minimal LE TIFF.
	minLE := make([]byte, 14)
	minLE[0], minLE[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(minLE[2:], 0x002A)
	binary.LittleEndian.PutUint32(minLE[4:], 8)
	f.Add(minLE)

	// Seed: minimal BE TIFF.
	minBE := make([]byte, 14)
	minBE[0], minBE[1] = 'M', 'M'
	binary.BigEndian.PutUint16(minBE[2:], 0x002A)
	binary.BigEndian.PutUint32(minBE[4:], 8)
	f.Add(minBE)

	// Seed: empty input.
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
