// Package webp implements extraction and injection of EXIF and XMP metadata
// within WebP files.
//
// WebP uses a RIFF container: "RIFF" + 4-byte size + "WEBP" + chunks.
// Relevant chunks:
//   - "EXIF": raw EXIF payload (VP8X feature bit 0x08).
//   - "XMP ": XMP packet (VP8X feature bit 0x04).
package webp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/FlavioCFOliveira/GoMetadata/internal/riff"
)

// readWebPChunks iterates over the RIFF chunk list in r, accumulating EXIF and
// XMP payloads. r must be positioned immediately after the 12-byte RIFF/WEBP
// header. All non-metadata chunks are skipped.
func readWebPChunks(r io.ReadSeeker) (rawEXIF, rawXMP []byte, err error) {
	for {
		chunk, rerr := riff.ReadChunk(r)
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return rawEXIF, rawXMP, nil
			}
			return nil, nil, fmt.Errorf("webp: read chunk: %w", rerr)
		}

		switch chunk.FourCCString() {
		case "EXIF":
			rawEXIF, err = readPaddedChunk(r, chunk)
			if err != nil {
				return nil, nil, fmt.Errorf("webp: read EXIF chunk: %w", err)
			}
		case "XMP ":
			rawXMP, err = readPaddedChunk(r, chunk)
			if err != nil {
				return nil, nil, fmt.Errorf("webp: read XMP chunk: %w", err)
			}
		default:
			if err = riff.SkipChunk(r, chunk); err != nil {
				return nil, nil, fmt.Errorf("webp: skip chunk: %w", err)
			}
		}
	}
}

// Extract reads the RIFF/WebP chunk stream from r and returns raw metadata payloads.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("webp: seek: %w", err)
	}

	// Read RIFF header: "RIFF" + 4-byte file size + "WEBP"
	var hdr [12]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return nil, nil, nil, fmt.Errorf("webp: read header: %w", err)
	}
	if string(hdr[:4]) != "RIFF" || string(hdr[8:12]) != "WEBP" {
		return nil, nil, nil, ErrNotWebP
	}

	rawEXIF, rawXMP, err = readWebPChunks(r)
	if err != nil {
		return nil, nil, nil, err
	}
	return rawEXIF, nil, rawXMP, nil
}

// readPaddedChunk reads chunk.Size bytes from r into a new slice and seeks
// past the RIFF odd-size padding byte when needed.
// RIFF spec: chunks with odd byte counts are followed by a 1-byte zero pad.
func readPaddedChunk(r io.ReadSeeker, chunk riff.Chunk) ([]byte, error) {
	data := make([]byte, chunk.Size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("webp: read chunk data: %w", err)
	}
	if chunk.Size%2 != 0 {
		if _, err := r.Seek(1, io.SeekCurrent); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("webp: seek past odd-size padding byte: %w", err)
		}
	}
	return data, nil
}

// Inject writes a modified WebP stream to w with updated EXIF and XMP chunks.
// rawIPTC is ignored (WebP has no IPTC support).
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("webp: seek: %w", err)
	}

	// Buffer the whole file and rebuild (simple but correct approach).
	original, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("webp: read: %w", err)
	}
	if len(original) < 12 {
		return ErrFileTooShort
	}

	body := buildWebPBody(original, rawEXIF, rawXMP)
	defer webpBufPool.Put(body)

	// Write RIFF header with updated size.
	totalSize := 4 + body.Len() // "WEBP" + chunks
	riffHdr := make([]byte, 12)
	copy(riffHdr[:4], "RIFF")
	binary.LittleEndian.PutUint32(riffHdr[4:], uint32(totalSize)) //nolint:gosec // G115: RIFF size bounded by body size
	copy(riffHdr[8:], "WEBP")
	if _, writeErr := w.Write(riffHdr); writeErr != nil {
		return fmt.Errorf("webp: write header: %w", writeErr)
	}
	_, err = w.Write(body.Bytes())
	if err != nil {
		return fmt.Errorf("webp: write body: %w", err)
	}
	return nil
}

// webpBufPool stores reusable *bytes.Buffer values for buildWebPBody.
// Reusing them avoids a large heap allocation on every Inject call.
var webpBufPool = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
	New: func() any { return new(bytes.Buffer) },
}

// buildWebPBody assembles the RIFF body (everything after the 12-byte RIFF
// header) from the original file bytes plus the new EXIF and XMP payloads.
// It rebuilds VP8X flags, preserves all non-metadata chunks in order, and
// appends EXIF/XMP chunks at the end. The caller must call webpBufPool.Put on
// the returned buffer after all writes to w are complete.
func buildWebPBody(original, rawEXIF, rawXMP []byte) *bytes.Buffer {
	chunks, origVP8XData := collectOriginalChunks(original)

	hasEXIF := rawEXIF != nil
	hasXMP := rawXMP != nil

	body := webpBufPool.Get().(*bytes.Buffer) //nolint:forcetypeassert,revive // webpBufPool.New always stores *bytes.Buffer; pool invariant
	body.Reset()

	// Write VP8X if needed (EXIF or XMP present, or was already extended).
	if hasEXIF || hasXMP || origVP8XData != nil {
		vp8xData := buildVP8XFlags(hasEXIF, hasXMP, origVP8XData)
		writeRIFFChunk(body, "VP8X", vp8xData)
	}

	// Write original image chunks.
	for _, c := range chunks {
		writeRIFFChunk(body, c.id, c.data)
	}

	// Append metadata chunks.
	if hasEXIF {
		writeRIFFChunk(body, "EXIF", rawEXIF)
	}
	if hasXMP {
		writeRIFFChunk(body, "XMP ", rawXMP)
	}

	return body
}

// collectOriginalChunks parses the flat RIFF chunk list starting at byte 12 of
// original (after the RIFF/WEBP header). It drops VP8X, EXIF, and XMP chunks
// (caller rebuilds them) and returns all remaining chunks. The VP8X payload is
// returned separately so canvas dimensions and other feature bits can be
// preserved by buildVP8XFlags.
func collectOriginalChunks(original []byte) (chunks []struct {
	id   string
	data []byte
}, origVP8XData []byte) {
	pos := 12 // skip RIFF header
	for pos+8 <= len(original) {
		id := string(original[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(original[pos+4:]))
		dataStart := pos + 8
		// Clamp to available data so subsequent chunks are not silently dropped
		// when chunk size exceeds remaining bytes (truncated or RIFF size mismatch).
		dataEnd := min(dataStart+size, len(original))
		switch id {
		case "VP8X":
			// Capture original VP8X payload so canvas dimensions can be preserved.
			if size >= 10 {
				origVP8XData = original[dataStart:dataEnd]
			}
		case "EXIF", "XMP ":
			// Drop: caller will re-append updated versions.
		default:
			chunks = append(chunks, struct {
				id   string
				data []byte
			}{id, original[dataStart:dataEnd]})
		}
		pos = dataEnd
		if size%2 != 0 {
			pos++ // RIFF padding byte
		}
	}
	return chunks, origVP8XData
}

// buildVP8XFlags constructs a 10-byte VP8X chunk payload with the EXIF (bit 3)
// and XMP (bit 2) feature flags set or cleared according to hasEXIF/hasXMP.
// All other bits and the canvas dimension fields are copied from origVP8XData
// when present, preserving ICC, animation, alpha, and dimension information.
func buildVP8XFlags(hasEXIF, hasXMP bool, origVP8XData []byte) []byte {
	vp8xData := make([]byte, 10)
	if origVP8XData != nil {
		copy(vp8xData, origVP8XData[:10])
	}
	// Update only the EXIF (bit 3) and XMP (bit 2) feature flags.
	flags := binary.LittleEndian.Uint32(vp8xData[0:])
	if hasXMP {
		flags |= 0x04
	} else {
		flags &^= 0x04
	}
	if hasEXIF {
		flags |= 0x08
	} else {
		flags &^= 0x08
	}
	binary.LittleEndian.PutUint32(vp8xData[0:], flags)
	return vp8xData
}

func writeRIFFChunk(w *bytes.Buffer, id string, data []byte) {
	w.WriteString(id)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(data))) //nolint:gosec // G115: RIFF chunk size bounded by buffer size
	w.Write(sz[:])
	w.Write(data)
	if len(data)%2 != 0 {
		w.WriteByte(0x00)
	}
}
