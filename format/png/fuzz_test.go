package png

import (
	"bytes"
	"testing"
)

func FuzzPNGExtract(f *testing.F) {
	// Seed with a valid minimal PNG (signature + IHDR + IEND).
	seed := buildPNG(nil, nil)
	f.Add(seed)

	// Seed with PNG containing EXIF.
	exifSeed := buildPNG([]byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}, nil)
	f.Add(exifSeed)

	// Seed with PNG containing XMP.
	xmpSeed := buildPNG(nil, []byte("<?xpacket begin='' uid='x'?><xmpmeta/><?xpacket end='r'?>"))
	f.Add(xmpSeed)

	// Seed with empty input.
	f.Add([]byte{})

	// Seed with just the PNG signature.
	f.Add(pngSig[:])

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
