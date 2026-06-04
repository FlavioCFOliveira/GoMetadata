package png

import (
	"bytes"
	"encoding/binary"
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

	// Seed with PNG signature followed by a chunk whose length field is 0x80000000
	// (task #74 regression seed). On a 32-bit build, int(0x80000000) == -2147483648;
	// the MaxInt32 guard must fire and return ErrChunkTooLarge before any slice OOB.
	{
		buf := make([]byte, 0, 8+8)
		buf = append(buf, pngSig[:]...)
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[:4], 0x80000000)
		copy(hdr[4:8], "IHDR")
		buf = append(buf, hdr[:]...)
		f.Add(buf)
	}

	// Seed with chunk length == 0xFFFFFFFF (max uint32, task #74).
	{
		buf := make([]byte, 0, 8+8)
		buf = append(buf, pngSig[:]...)
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[:4], 0xFFFFFFFF)
		copy(hdr[4:8], "IHDR")
		buf = append(buf, hdr[:]...)
		f.Add(buf)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
