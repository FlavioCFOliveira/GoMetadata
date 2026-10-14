package webp

// Adapted from libwebp src/dec/vp8_dec.c:VP8GetInfo, Copyright 2010 Google Inc., SPDX-License-Identifier: BSD-3-Clause
// Adapted from libwebp src/dec/vp8l_dec.c:VP8LGetInfo,ReadImageInfo, Copyright 2012 Google Inc., SPDX-License-Identifier: BSD-3-Clause
// Adapted from libwebp src/mux/muxedit.c:CreateVP8XChunk, Copyright 2011 Google Inc., SPDX-License-Identifier: BSD-3-Clause
//
// Changes from libwebp: the frame headers are decoded from the chunk payload
// bytes already in memory, without a bit reader; only the fields that a VP8X
// chunk needs (canvas width and height, and the alpha hint of a lossless
// bitstream) are returned; the VP8X chunk is built for a still image only
// (animated files always carry a VP8X chunk already, so they never reach
// this code).

import "encoding/binary"

// VP8X feature flags (libwebp src/webp/mux_types.h WebPFeatureFlags; WebP
// Container Specification, "Extended File Format").
const (
	vp8xFlagXMP   = 0x04
	vp8xFlagEXIF  = 0x08
	vp8xFlagAlpha = 0x10
	vp8xFlagICCP  = 0x20
)

// vp8xChunkSize is the payload size of a VP8X chunk
// (libwebp VP8X_CHUNK_SIZE).
const vp8xChunkSize = 10

// Frame header constants (libwebp src/webp/format_constants.h).
const (
	vp8FrameHeaderSize  = 10   // VP8_FRAME_HEADER_SIZE
	vp8lFrameHeaderSize = 5    // VP8L_FRAME_HEADER_SIZE
	vp8lMagicByte       = 0x2f // VP8L_MAGIC_BYTE
	vp8lImageSizeBits   = 14   // VP8L_IMAGE_SIZE_BITS
)

// FourCCs of the chunks that set VP8X feature flags.
var (
	fourCCVP8  = [4]byte{'V', 'P', '8', ' '} //nolint:gochecknoglobals // immutable comparison constant
	fourCCVP8L = [4]byte{'V', 'P', '8', 'L'} //nolint:gochecknoglobals // immutable comparison constant
	fourCCALPH = [4]byte{'A', 'L', 'P', 'H'} //nolint:gochecknoglobals // immutable comparison constant
	fourCCICCP = [4]byte{'I', 'C', 'C', 'P'} //nolint:gochecknoglobals // immutable comparison constant
)

// vp8Info returns the width and height of a lossy VP8 key frame, as libwebp
// VP8GetInfo validates and decodes them (RFC 6386 §9.1, §19.1: 3-byte frame
// tag, start code 9d 01 2a, then 14-bit width and height with 2-bit scale).
// ok is false when data is not a decodable VP8 key frame header.
func vp8Info(data []byte) (width, height int, ok bool) {
	if len(data) < vp8FrameHeaderSize {
		return 0, 0, false
	}
	// Start code (libwebp VP8CheckSignature).
	if data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
		return 0, 0, false
	}
	if !vp8FrameTagOK(uint32(data[0])|uint32(data[1])<<8|uint32(data[2])<<16, len(data)) {
		return 0, 0, false
	}
	width = int(binary.LittleEndian.Uint16(data[6:]) & 0x3fff)
	height = int(binary.LittleEndian.Uint16(data[8:]) & 0x3fff)
	if width == 0 || height == 0 {
		return 0, 0, false
	}
	return width, height, true
}

// vp8FrameTagOK applies libwebp VP8GetInfo's checks to the 24-bit frame tag
// of a VP8 chunk of chunkSize bytes (RFC 6386 §9.1): a key frame, a known
// profile, a visible frame, and a first-partition length inside the chunk.
func vp8FrameTagOK(bits uint32, chunkSize int) bool {
	return bits&1 == 0 && // key frame
		(bits>>1)&7 <= 3 && // known profile
		(bits>>4)&1 == 1 && // shown frame
		uint64(bits>>5) < uint64(chunkSize) //nolint:gosec // G115: chunkSize is a non-negative length
}

// vp8lInfo returns the width, height and alpha hint of a lossless VP8L
// bitstream, as libwebp VP8LGetInfo/ReadImageInfo decode them (WebP Lossless
// Bitstream Specification §3, RFC 9649 §3.2: signature 0x2f, then, least
// significant bit first, 14 bits width-1, 14 bits height-1, 1 bit
// alpha_is_used and a 3-bit version that must be 0). ok is false when data
// is not a decodable VP8L header.
func vp8lInfo(data []byte) (width, height int, alpha, ok bool) {
	if len(data) < vp8lFrameHeaderSize || data[0] != vp8lMagicByte {
		return 0, 0, false, false
	}
	bits := binary.LittleEndian.Uint32(data[1:5])
	if bits>>29 != 0 { // version
		return 0, 0, false, false
	}
	const sizeMask = 1<<vp8lImageSizeBits - 1
	width = int(bits&sizeMask) + 1
	height = int((bits>>vp8lImageSizeBits)&sizeMask) + 1
	alpha = (bits>>(2*vp8lImageSizeBits))&1 == 1
	return width, height, alpha, true
}

// newVP8XPayload builds the 10-byte VP8X payload of a still image whose
// chunks (excluding VP8X, EXIF and XMP) are chunks, following libwebp
// CreateVP8XChunk: ICCP when an ICCP chunk is present; ALPHA when an ALPH
// chunk is present or the VP8L bitstream declares alpha; EXIF and XMP as
// requested; canvas width-1 and height-1 as 24-bit little-endian values
// taken from the image bitstream. ok is false when no image chunk has a
// decodable header, so no valid canvas size exists.
func newVP8XPayload(chunks []webpOriginalChunk, hasEXIF, hasXMP bool) (payload [vp8xChunkSize]byte, ok bool) {
	width, height, flags, ok := stillImageInfo(chunks)
	if !ok {
		return payload, false
	}
	if hasEXIF {
		flags |= vp8xFlagEXIF
	}
	if hasXMP {
		flags |= vp8xFlagXMP
	}
	binary.LittleEndian.PutUint32(payload[0:4], flags)
	putLE24(payload[4:7], uint32(width-1))   //nolint:gosec // G115: width is in [1, 16384]
	putLE24(payload[7:10], uint32(height-1)) //nolint:gosec // G115: height is in [1, 16384]
	return payload, true
}

// stillImageInfo returns the canvas size of a still image — the size of its
// first decodable VP8 or VP8L bitstream — and the ICCP and ALPHA feature
// flags its chunks imply. ok is false when no image chunk has a decodable
// header.
func stillImageInfo(chunks []webpOriginalChunk) (width, height int, flags uint32, ok bool) {
	for _, c := range chunks {
		switch c.id {
		case fourCCICCP:
			flags |= vp8xFlagICCP
		case fourCCALPH:
			flags |= vp8xFlagAlpha
		case fourCCVP8, fourCCVP8L:
			if ok {
				continue
			}
			var alpha bool
			if c.id == fourCCVP8 {
				width, height, ok = vp8Info(c.data)
			} else {
				width, height, alpha, ok = vp8lInfo(c.data)
			}
			if alpha {
				flags |= vp8xFlagAlpha
			}
		}
	}
	return width, height, flags, ok
}

// putLE24 stores the low 24 bits of v in b[0:3], little-endian
// (libwebp PutLE24).
func putLE24(b []byte, v uint32) {
	b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16) //nolint:gosec // G115: deliberate truncation to the three low bytes
}
