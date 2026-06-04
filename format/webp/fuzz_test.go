package webp

import (
	"bytes"
	"encoding/binary"
	"io"
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

// FuzzWebPInject feeds arbitrary bytes as the source WebP container and asserts
// that Inject never panics — it must return an error or write valid output, never
// crash. The no-panic contract is the primary correctness invariant for the
// Inject write path.
//
// preserveUnknownSegments is always true because WebP RIFF chunks (VP8, VP8L,
// VP8X, ANIM, ANMF, ALPH, ICCP) are structurally required; passing false is a
// documented user error that returns ErrPreserveUnknownSegmentsNotSupported
// before any parsing begins. Fuzzing that early-return gate adds no value.
//
// Fixed short payloads are used for rawEXIF and rawXMP so the fuzzer can focus
// on structural variation in the container bytes rather than payload content.
func FuzzWebPInject(f *testing.F) {
	// Seed 1: minimal RIFF/WEBP with no chunks — exercises the "file too short
	// for body" path (body = "WEBP" only, no image chunks).
	f.Add([]byte{
		'R', 'I', 'F', 'F',
		0x04, 0x00, 0x00, 0x00, // RIFF body size = 4
		'W', 'E', 'B', 'P',
	})

	// Seed 2: RIFF/WEBP with a minimal VP8 chunk — simplest decodable container.
	f.Add([]byte{
		'R', 'I', 'F', 'F',
		0x14, 0x00, 0x00, 0x00, // RIFF body size = 20
		'W', 'E', 'B', 'P',
		'V', 'P', '8', ' ',
		0x0A, 0x00, 0x00, 0x00, // chunk size = 10
		0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00,
	})

	// Seed 3: empty input — exercises the ErrFileTooShort path.
	f.Add([]byte{})

	// Seed 4: truncated RIFF header — fewer than 12 bytes.
	f.Add([]byte{'R', 'I', 'F', 'F', 0x00, 0x00})

	// Seed 5: RIFF/WEBP with VP8X chunk (EXIF+XMP flags set) — exercises
	// the VP8X rebuild path. Canvas = 100×100, flags = 0x0C (EXIF|XMP).
	{
		vp8xPayload := make([]byte, 10)
		binary.LittleEndian.PutUint32(vp8xPayload[0:], 0x0C) // EXIF|XMP flags
		// canvas width-1 = 99, height-1 = 99 (3 bytes each, LE)
		vp8xPayload[4] = 99
		vp8xPayload[7] = 99
		var buf bytes.Buffer
		// VP8X chunk
		buf.WriteString("VP8X")
		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], 10)
		buf.Write(sz[:])
		buf.Write(vp8xPayload)
		// Minimal VP8 chunk
		buf.WriteString("VP8 ")
		binary.LittleEndian.PutUint32(sz[:], 10)
		buf.Write(sz[:])
		buf.Write([]byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00})

		var out bytes.Buffer
		out.WriteString("RIFF")
		riffSize := make([]byte, 4)
		binary.LittleEndian.PutUint32(riffSize, uint32(4+buf.Len())) //nolint:gosec // G115: test helper, bounded
		out.Write(riffSize)
		out.WriteString("WEBP")
		out.Write(buf.Bytes())
		f.Add(out.Bytes())
	}

	// Seed 6: crafted chunk with declared size 0x80000000 — exercises the
	// rawSize > math.MaxInt32 guard in collectOriginalChunks.
	{
		var streamBuf [20]byte
		copy(streamBuf[0:4], "RIFF")
		binary.LittleEndian.PutUint32(streamBuf[4:8], 12)
		copy(streamBuf[8:12], "WEBP")
		copy(streamBuf[12:16], "VP8 ")
		binary.LittleEndian.PutUint32(streamBuf[16:20], 0x80000000)
		f.Add(streamBuf[:])
	}

	// Fixed metadata payloads: short enough to avoid dominating execution time
	// but long enough to exercise external-storage code paths.
	rawEXIF := []byte{
		'I', 'I', 0x2A, 0x00, // LE TIFF header
		0x08, 0x00, 0x00, 0x00, // IFD0 at offset 8
		0x00, 0x00, // 0 entries
		0x00, 0x00, 0x00, 0x00, // next-IFD = 0
	}
	rawXMP := []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>`)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug.
		err := Inject(bytes.NewReader(data), io.Discard, rawEXIF, nil, rawXMP, true)
		_ = err
	})
}
