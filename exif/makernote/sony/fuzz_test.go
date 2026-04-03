package sony

import "testing"

func FuzzSonyParse(f *testing.F) {
	// Seed: minimal valid Sony MakerNote IFD (2 entries minimum).
	f.Add(buildSonyIFD())

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: single byte.
	f.Add([]byte{0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		p := Parser{}
		_, _ = p.Parse(data)
	})
}
