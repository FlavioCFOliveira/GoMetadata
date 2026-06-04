package webp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func FuzzWebPExtract(f *testing.F) {
	// Seed: minimal RIFF/WEBP header with no chunks.
	minWebP := []byte{
		'R', 'I', 'F', 'F',
		0x04, 0x00, 0x00, 0x00, // file size = 4 (body = "WEBP" only)
		'W', 'E', 'B', 'P',
	}
	f.Add(minWebP)

	// Seed: RIFF/WEBP with a minimal VP8 chunk.
	withVP8 := []byte{
		'R', 'I', 'F', 'F',
		0x14, 0x00, 0x00, 0x00, // 4 + VP8 chunk (8 + 10) = 22 = 0x16? let's use 0x14 which is 20
		'W', 'E', 'B', 'P',
		'V', 'P', '8', ' ',
		0x0A, 0x00, 0x00, 0x00, // chunk size = 10
		0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00,
	}
	f.Add(withVP8)

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated RIFF header.
	f.Add([]byte{'R', 'I', 'F', 'F', 0x00, 0x00})

	// Seed: RIFF/WEBP with a chunk whose declared size is 0x80000000 (task #74
	// regression seed). On a 32-bit build, int(0x80000000) == -2147483648;
	// collectOriginalChunks must break early rather than computing a negative
	// dataStart+size and passing it to min() with a wrong result.
	{
		// 12-byte RIFF/WEBP header + 8-byte chunk header (FourCC + uint32 size).
		var streamBuf [20]byte
		copy(streamBuf[0:4], "RIFF")
		binary.LittleEndian.PutUint32(streamBuf[4:8], 12) // RIFF body size
		copy(streamBuf[8:12], "WEBP")
		// VP8 chunk with declared size 0x80000000, no body bytes.
		copy(streamBuf[12:16], "VP8 ")
		binary.LittleEndian.PutUint32(streamBuf[16:20], 0x80000000)
		f.Add(streamBuf[:])
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
