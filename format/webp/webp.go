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
	"fmt"
	"io"

	"github.com/flaviocfo/img-metadata/internal/riff"
)

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
		return nil, nil, nil, fmt.Errorf("webp: not a WebP file")
	}

	for {
		chunk, rerr := riff.ReadChunk(r)
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return nil, nil, nil, fmt.Errorf("webp: read chunk: %w", rerr)
		}

		switch chunk.FourCCString() {
		case "EXIF":
			rawEXIF = make([]byte, chunk.Size)
			if _, err = io.ReadFull(r, rawEXIF); err != nil {
				return nil, nil, nil, fmt.Errorf("webp: read EXIF chunk: %w", err)
			}
		case "XMP ":
			rawXMP = make([]byte, chunk.Size)
			if _, err = io.ReadFull(r, rawXMP); err != nil {
				return nil, nil, nil, fmt.Errorf("webp: read XMP chunk: %w", err)
			}
		default:
			if err = riff.SkipChunk(r, chunk); err != nil {
				return nil, nil, nil, fmt.Errorf("webp: skip chunk: %w", err)
			}
		}
	}

	return rawEXIF, nil, rawXMP, nil
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
		return fmt.Errorf("webp: file too short")
	}

	// Collect all existing chunks except EXIF, XMP, VP8X (we rebuild VP8X).
	var chunks []struct {
		id   string
		data []byte
	}
	pos := 12 // skip RIFF header
	for pos+8 <= len(original) {
		id := string(original[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(original[pos+4:]))
		dataStart := pos + 8
		dataEnd := dataStart + size
		if dataEnd > len(original) {
			break
		}
		if id != "EXIF" && id != "XMP " && id != "VP8X" {
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

	// Build VP8X flags.
	hasEXIF := rawEXIF != nil
	hasXMP := rawXMP != nil

	// Determine if there was an original VP8X or if we need to create one.
	// Check if original has a VP8X chunk.
	origHasVP8X := bytes.Contains(original[:min(len(original), 64)], []byte("VP8X"))

	var body bytes.Buffer

	// Write VP8X if needed (EXIF or XMP present, or was already extended).
	if hasEXIF || hasXMP || origHasVP8X {
		flags := uint32(0)
		if hasXMP {
			flags |= 0x04
		}
		if hasEXIF {
			flags |= 0x08
		}
		vp8xData := make([]byte, 10)
		binary.LittleEndian.PutUint32(vp8xData[0:], flags)
		// Canvas width/height: copy from original VP8X if present, else 0.
		if origHasVP8X {
			vp8xOff := bytes.Index(original, []byte("VP8X"))
			if vp8xOff >= 0 && vp8xOff+18 <= len(original) {
				copy(vp8xData[4:], original[vp8xOff+12:vp8xOff+18])
			}
		}
		writeRIFFChunk(&body, "VP8X", vp8xData)
	}

	// Write original image chunks.
	for _, c := range chunks {
		writeRIFFChunk(&body, c.id, c.data)
	}

	// Append metadata chunks.
	if hasEXIF {
		writeRIFFChunk(&body, "EXIF", rawEXIF)
	}
	if hasXMP {
		writeRIFFChunk(&body, "XMP ", rawXMP)
	}

	// Write RIFF header with updated size.
	totalSize := 4 + body.Len() // "WEBP" + chunks
	riffHdr := make([]byte, 12)
	copy(riffHdr[:4], "RIFF")
	binary.LittleEndian.PutUint32(riffHdr[4:], uint32(totalSize))
	copy(riffHdr[8:], "WEBP")
	if _, err := w.Write(riffHdr); err != nil {
		return err
	}
	_, err = w.Write(body.Bytes())
	return err
}

func writeRIFFChunk(w *bytes.Buffer, id string, data []byte) {
	w.WriteString(id)
	sz := make([]byte, 4)
	binary.LittleEndian.PutUint32(sz, uint32(len(data)))
	w.Write(sz)
	w.Write(data)
	if len(data)%2 != 0 {
		w.WriteByte(0x00)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
