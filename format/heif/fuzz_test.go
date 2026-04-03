package heif

import (
	"bytes"
	"testing"
)

func FuzzHEIFExtract(f *testing.F) {
	// Seed with a minimal HEIF/ISOBMFF structure.
	// ftyp box: size=20, type="ftyp", brand="heic", version=0, compat="mif1"
	seed := []byte{
		0x00, 0x00, 0x00, 0x14, // size = 20
		'f', 't', 'y', 'p', // type = ftyp
		'h', 'e', 'i', 'c', // major brand
		0x00, 0x00, 0x00, 0x00, // minor version
		'm', 'i', 'f', '1', // compatible brand
	}
	f.Add(seed)

	// Seed with empty input.
	f.Add([]byte{})

	// Seed with truncated box header.
	f.Add([]byte{0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p'})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
