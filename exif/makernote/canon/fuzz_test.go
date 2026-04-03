package canon

import "testing"

func FuzzCanonParse(f *testing.F) {
	// Seed: minimal valid Canon MakerNote IFD (3 entries).
	f.Add(buildCanonIFD([]struct{ tag, typ uint16; val uint32 }{
		{TagCameraSettings, 3, 0x0001},
		{TagModelID, 4, 0x80000010},
		{TagColorSpace, 3, 0x0001},
	}))

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
