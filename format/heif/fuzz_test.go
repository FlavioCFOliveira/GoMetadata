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

	// Seed: ftyp box followed by a mdat box — minimal two-box HEIF structure.
	// mdat (media data) box: size=8, type="mdat", no content.
	{
		seed2 := []byte{
			// ftyp box (20 bytes)
			0x00, 0x00, 0x00, 0x14,
			'f', 't', 'y', 'p',
			'h', 'e', 'i', 'c',
			0x00, 0x00, 0x00, 0x00,
			'm', 'i', 'f', '1',
			// mdat box (8 bytes, empty body)
			0x00, 0x00, 0x00, 0x08,
			'm', 'd', 'a', 't',
		}
		f.Add(seed2)
	}

	// Seed: ftyp with "mif1" major brand (alternate HEIF variant).
	{
		seed3 := []byte{
			0x00, 0x00, 0x00, 0x14,
			'f', 't', 'y', 'p',
			'm', 'i', 'f', '1', // mif1 brand
			0x00, 0x00, 0x00, 0x00,
			'h', 'e', 'i', 'c',
		}
		f.Add(seed3)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		rawEXIF, _, _, err := Extract(bytes.NewReader(data))
		if err != nil {
			return
		}

		// Post-success assertion: a non-nil EXIF payload must carry at least the
		// TIFF header (8 bytes: byte-order mark + magic + IFD0 offset).
		// HEIF embeds EXIF as a full TIFF block (ISO 23008-12 §A.2.1).
		if rawEXIF != nil && len(rawEXIF) < 8 {
			t.Errorf("rawEXIF too short after successful Extract: got %d bytes, want >= 8", len(rawEXIF))
		}
	})
}
