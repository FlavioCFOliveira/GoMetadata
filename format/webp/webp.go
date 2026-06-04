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
	"math"
	"sync"

	"github.com/FlavioCFOliveira/GoMetadata/internal/riff"
)

// maxWebPChunkSize is the maximum allowed data size for a single RIFF chunk
// before any allocation is attempted. The RIFF spec encodes chunk sizes as
// uint32, permitting up to ~4 GiB, but that is pathological for EXIF or XMP
// metadata. 256 MiB is orders of magnitude larger than any real payload; any
// chunk that declares more is either malformed or adversarial.
// Guard applied in readPaddedChunk before make([]byte, chunk.Size) to prevent
// a single 4-byte field in the file from causing a multi-gigabyte heap allocation.
const maxWebPChunkSize = 256 << 20 // 256 MiB

// xmpWireFrameMagic is the 8-byte sentinel that identifies a JPEG extended-XMP
// wire-frame payload (defined in format/jpeg; duplicated here to avoid an import
// cycle). The wire-frame is an internal encoding used exclusively by jpeg.Inject;
// it must never reach the WebP injector.
//
// Layout: [0x00]['X']['M']['P']['E']['X']['T'][0x00]
// The leading 0x00 is unambiguous: no valid XMP packet starts with a null byte.
var xmpWireFrameMagic = [8]byte{0x00, 'X', 'M', 'P', 'E', 'X', 'T', 0x00} //nolint:gochecknoglobals // package-level constant bytes

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
//
// DoS defence — two-stage guard before any allocation:
//  1. Hard cap: reject chunk.Size > maxWebPChunkSize (256 MiB) regardless.
//  2. Stream-availability check: seek to EOF, compute bytes remaining, seek
//     back; if chunk.Size exceeds the stream remainder, the declared size
//     cannot be satisfied and the file is adversarial or truncated — return
//     error without allocating.
//
// Stage 2 prevents a crafted file (e.g. chunk.Size = 200 MiB in a 50-byte
// stream) from triggering a multi-hundred-megabyte make([]byte, chunk.Size)
// before io.ReadFull inevitably fails. The Seek-based check costs two Seek
// syscalls but avoids proportional heap allocation for adversarial inputs.
func readPaddedChunk(r io.ReadSeeker, chunk riff.Chunk) ([]byte, error) {
	// Stage 1: hard cap — rejects pathologically large values (> 256 MiB).
	if chunk.Size > maxWebPChunkSize {
		return nil, fmt.Errorf("webp: chunk %q size %d exceeds limit: %w",
			chunk.FourCCString(), chunk.Size, ErrChunkTooLarge)
	}

	// Stage 2: stream-availability guard — seek to measure remaining bytes.
	// chunk.Offset is the position of the first data byte (set by riff.ReadChunk).
	// r is currently positioned at chunk.Offset (immediately after the header).
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("webp: seek to end for size check: %w", err)
	}
	// Restore position before any comparison or allocation.
	if _, err = r.Seek(chunk.Offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("webp: seek back after size check: %w", err)
	}
	remaining := max(end-chunk.Offset, 0)
	// chunk.Size is uint32 ≤ maxWebPChunkSize after stage 1; int64 cast is safe.
	if int64(chunk.Size) > remaining {
		return nil, fmt.Errorf("webp: chunk %q declared size %d exceeds available stream bytes %d: %w",
			chunk.FourCCString(), chunk.Size, remaining, ErrChunkTooLarge)
	}

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
	// Defense in depth: reject a JPEG extended-XMP wire-frame that was not
	// filtered out by the encodeXMP format check in write.go. The wire-frame
	// begins with 0x00XMPEXT\x00 — an invalid start for any XMP packet — and
	// can only be decoded by jpeg.Inject. Writing it verbatim to a WebP XMP
	// chunk would produce a corrupt, non-XMP blob. (Bug #70.)
	if len(rawXMP) >= len(xmpWireFrameMagic) && [8]byte(rawXMP[:8]) == xmpWireFrameMagic {
		return fmt.Errorf("webp: rawXMP contains an internal JPEG wire-frame encoding that cannot be stored in a WebP container: %w", ErrCorruptXMP)
	}
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
		case "VP8X":
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
			if size >= 10 && nextPos <= len(original) && isEOFOrKnownFourCC(original, nextPos) {
				origVP8XData = original[dataStart:nextPos]
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
	// Guard: only copy from origVP8XData when it is known to hold at least 10
	// bytes. collectOriginalChunks now enforces this invariant at capture time,
	// but the explicit len check here makes the safety guarantee local and
	// prevents a future caller from accidentally passing a short slice.
	if len(origVP8XData) >= 10 {
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

// knownWebPFourCCs is the exhaustive set of chunk FourCC identifiers defined
// by the WebP container specification (https://developers.google.com/speed/webp/docs/riff_container).
// Used by isEOFOrKnownFourCC to validate VP8X region boundaries.
var knownWebPFourCCs = [...]string{ //nolint:gochecknoglobals // immutable lookup table
	"VP8 ", "VP8L", "VP8X",
	"ANIM", "ANMF", "ALPH",
	"ICCP", "EXIF", "XMP ",
}

// isEOFOrKnownFourCC reports whether position pos in data is either past the
// end of data (EOF) or the start of a recognised WebP chunk FourCC (4 bytes).
// A VP8X declared region is only trusted when the byte immediately following
// it satisfies this predicate — otherwise the declared size is wrong and the
// captured bytes span into an adjacent chunk.
func isEOFOrKnownFourCC(data []byte, pos int) bool {
	if pos >= len(data) {
		return true // EOF is valid
	}
	if pos+4 > len(data) {
		return false // not enough bytes to read a FourCC
	}
	candidate := string(data[pos : pos+4])
	for _, known := range knownWebPFourCCs {
		if candidate == known {
			return true
		}
	}
	return false
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
