package webp

import (
	"bytes"
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

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}
