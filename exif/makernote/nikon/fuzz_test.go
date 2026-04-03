package nikon

import "testing"

func FuzzNikonParse(f *testing.F) {
	// Seed: minimal Nikon Type 1 (big-endian IFD at offset 0).
	f.Add(buildNikonType1IFD())

	// Seed: minimal Nikon Type 3 (embedded TIFF with "Nikon\0" prefix).
	f.Add(buildNikonType3())

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: "Nikon\0" prefix with no trailing data.
	f.Add([]byte{'N', 'i', 'k', 'o', 'n', 0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		p := Parser{}
		_, _ = p.Parse(data)
	})
}
