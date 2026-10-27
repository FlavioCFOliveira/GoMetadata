package webp

// oracle_test.go — the whole-file implementation of Inject that task #391
// replaced, kept as the test oracle: a differential test and a fuzz target
// check that the streaming Inject writes the same bytes for every input.

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
)

// oracleInject is the pre-#391 Inject: the whole file in memory.
func oracleInject(original, rawEXIF, rawXMP []byte) ([]byte, error) {
	if len(original) < 12 {
		return nil, ErrFileTooShort
	}
	return oracleBuildBody(original, rawEXIF, rawXMP).Bytes(), nil
}

var _ = io.Discard

func oracleBuildBody(original, rawEXIF, rawXMP []byte) *bytes.Buffer {
	chunks, origVP8XData := oracleCollectChunks(original)

	hasEXIF := rawEXIF != nil
	hasXMP := rawXMP != nil

	body := new(bytes.Buffer)
	body.Reset()

	// Reserve the 12-byte RIFF header at the start of the same pooled buffer
	// used for the chunk stream: "RIFF" + a zero size placeholder + "WEBP".
	// The size field is patched in place once the final length is known,
	// after every chunk below has been appended.
	body.WriteString("RIFF")
	var sizePlaceholder [4]byte
	body.Write(sizePlaceholder[:])
	body.WriteString("WEBP")

	// Write VP8X if needed (EXIF or XMP present, or was already extended).
	if hasEXIF || hasXMP || origVP8XData != nil {
		oracleWriteVP8X(body, chunks, hasEXIF, hasXMP, origVP8XData)
	}

	// Write original image chunks.
	for _, c := range chunks {
		writeRIFFChunk(body, c.id, c.data)
	}

	// Append metadata chunks.
	if hasEXIF {
		writeRIFFChunk(body, fourCCEXIF, rawEXIF)
	}
	if hasXMP {
		writeRIFFChunk(body, fourCCXMP, rawXMP)
	}

	// Patch the RIFF size field now that the final body length is known:
	// per the RIFF spec, size = total byte count following the size field
	// itself (i.e. everything after the first 8 bytes).
	buf := body.Bytes()
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(buf)-8)) //nolint:gosec // G115: RIFF size bounded by buffer length

	return body
}

type oracleChunk struct {
	id   [4]byte
	data []byte
}

// oracleCollectChunks parses the flat RIFF chunk list starting at byte 12 of
// original (after the RIFF/WEBP header). It drops VP8X, EXIF, and XMP chunks
// (caller rebuilds them) and returns all remaining chunks. The VP8X payload is
// returned separately so canvas dimensions and other feature bits can be
// preserved by buildVP8XFlags.
func oracleCollectChunks(original []byte) (chunks []oracleChunk, origVP8XData []byte) {
	pos := 12 // skip RIFF header
	for pos+8 <= len(original) {
		var id [4]byte
		copy(id[:], original[pos:pos+4])
		// Read the raw uint32 chunk size BEFORE converting to int. On a 32-bit
		// platform (GOARCH=386/arm, int=32 bits), a RIFF chunk size >= 2^31 would
		// become negative after int(uint32), causing dataStart+size to underflow
		// and dataEnd to be clamped to a wrong position. Skip oversized chunks
		// (they cannot contain valid EXIF/XMP metadata anyway) (task #74).
		rawSize := binary.LittleEndian.Uint32(original[pos+4:])
		if rawSize > math.MaxInt32 {
			break // chunk claims to be >= 2 GiB; treat rest of stream as unreadable
		}
		size := int(rawSize)
		dataStart := pos + 8
		// Clamp to available data so subsequent chunks are not silently dropped
		// when chunk size exceeds remaining bytes (truncated or RIFF size mismatch).
		dataEnd := min(dataStart+size, len(original))
		switch id {
		case fourCCVP8X:
			// Capture original VP8X payload so canvas dimensions can be preserved.
			//
			// Cross-chunk contamination guard (rmp task #57):
			// A malformed file can declare VP8X size=10 while writing only N < 10
			// real VP8X bytes, followed immediately by the next RIFF chunk. Because
			// dataEnd is clamped to len(original), the condition
			// "dataEnd-dataStart >= 10" passes even though 5 of the 10 captured bytes
			// belong to the adjacent chunk header (its FourCC and size bytes).
			// Those bytes would corrupt canvas width/height in the rebuilt VP8X.
			//
			// Similarly, "dataStart+size <= len(original)" also passes when the file
			// continues past the declared VP8X region — the 10 declared bytes are
			// available in the backing array even though they span into adjacent data.
			//
			// Robust fix: after the declared VP8X region we must see either EOF or a
			// recognised WebP chunk FourCC. If the bytes at dataStart+size are not
			// a known FourCC (and are not EOF), the declared size is wrong and the
			// captured slice contains contaminating bytes from the adjacent chunk.
			// In that case we discard origVP8XData; buildVP8XFlags produces a
			// zero-canvas payload with only the requested EXIF/XMP flags set.
			nextPos := dataStart + size
			if size >= 10 && nextPos <= len(original) && oracleKnownFourCC(original, nextPos) {
				origVP8XData = original[dataStart:nextPos]
			}
		case fourCCEXIF, fourCCXMP, fourCCXMPNul:
			// Drop: caller will re-append updated versions in the standard
			// 'EXIF' and 'XMP ' chunks (RFC 9649 §2.7.1.5: at most one chunk of
			// each type); a non-standard "XMP\0" chunk is XMP read as such (task
			// #333) and is dropped with them so only one XMP chunk remains.
		default:
			chunks = append(chunks, oracleChunk{id: id, data: original[dataStart:dataEnd]})
		}
		pos = dataEnd
		if size%2 != 0 {
			pos++ // RIFF padding byte
		}
	}
	return chunks, origVP8XData
}

func oracleWriteVP8X(body *bytes.Buffer, chunks []oracleChunk, hasEXIF, hasXMP bool, origVP8XData []byte) {
	if origVP8XData == nil {
		if payload, ok := oracleNewVP8X(chunks, hasEXIF, hasXMP); ok {
			writeRIFFChunk(body, fourCCVP8X, payload[:])
			return
		}
	}
	writeRIFFChunk(body, fourCCVP8X, buildVP8XFlags(hasEXIF, hasXMP, origVP8XData))
}

func oracleKnownFourCC(data []byte, pos int) bool {
	if pos >= len(data) {
		return true // EOF is valid
	}
	if pos+4 > len(data) {
		return false // not enough bytes to read a FourCC
	}
	var candidate [4]byte
	copy(candidate[:], data[pos:pos+4])
	for _, known := range knownWebPFourCCs {
		if candidate == known {
			return true
		}
	}
	return false
}

func oracleNewVP8X(chunks []oracleChunk, hasEXIF, hasXMP bool) (payload [vp8xChunkSize]byte, ok bool) {
	width, height, flags, ok := oracleStillInfo(chunks)
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

// oracleStillInfo returns the canvas size of a still image — the size of its
// first decodable VP8 or VP8L bitstream — and the ICCP and ALPHA feature
// flags its chunks imply. ok is false when no image chunk has a decodable
// header.
func oracleStillInfo(chunks []oracleChunk) (width, height int, flags uint32, ok bool) {
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
				width, height, ok = vp8Info(c.data, len(c.data))
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
