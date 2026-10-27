// Package webp implements extraction and injection of EXIF and XMP metadata
// within WebP files.
//
// WebP uses a RIFF container: "RIFF" + 4-byte size + "WEBP" + chunks.
// Relevant chunks:
//   - "EXIF": raw EXIF payload (VP8X feature bit 0x08). A leading "Exif\0\0"
//     identifier, written by some tools, is skipped on read and never written.
//   - "XMP ": XMP packet (VP8X feature bit 0x04). The non-standard FourCC
//     "XMP\0" is read as XMP too and replaced by "XMP " on write.
package webp

// Adapted from metadata-extractor Source/com/drew/metadata/webp/WebpRiffHandler.java:WebpRiffHandler.processChunk, Copyright 2002-2019 Drew Noakes and contributors, SPDX-License-Identifier: Apache-2.0
//
// Changes from the reference (Apache-2.0 §4(b)): the "Exif\0\0" identifier
// test of ExifReader.startsWithJpegExifPreamble is re-implemented in Go as
// stripExifPreamble, applied while the chunk is read; the reference ignores a
// chunk with the FourCC "XMP\0", which is read here as XMP, and the write path
// that the reference does not have drops both the identifier and that FourCC
// (task #333). See THIRD_PARTY_NOTICES.md.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
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

// fourCCEXIF and fourCCXMP are the two metadata chunk identifiers this
// package dispatches on. Comparing chunk.FourCC ([4]byte) directly against
// these constants is allocation-free, unlike the former
// `switch chunk.FourCCString()` dispatch, which allocated a new string via
// string(c.FourCC[:]) on every chunk — not just the EXIF/XMP ones — because
// the conversion happens inside the (*Chunk).FourCCString method rather than
// directly in a switch expression, so the compiler's allocation-free
// switch/map-on-string(byteSlice) special case cannot apply (task #209).
var (
	fourCCEXIF = [4]byte{'E', 'X', 'I', 'F'} //nolint:gochecknoglobals // immutable comparison constant
	fourCCXMP  = [4]byte{'X', 'M', 'P', ' '} //nolint:gochecknoglobals // immutable comparison constant
	// fourCCXMPNul is the non-standard "XMP\0" FourCC (fourth byte NUL instead
	// of a space) that some writers emit; ExifTool and other readers accept it
	// as XMP (task #333). The standard FourCC is 'XMP ' (RFC 9649 §2.7.1.5).
	fourCCXMPNul = [4]byte{'X', 'M', 'P', 0x00} //nolint:gochecknoglobals // immutable comparison constant
	fourCCVP8X   = [4]byte{'V', 'P', '8', 'X'}  //nolint:gochecknoglobals // immutable comparison constant
)

// readWebPChunks iterates over the RIFF chunk list in r, accumulating EXIF and
// XMP payloads. r must be positioned immediately after the 12-byte RIFF/WEBP
// header. All non-metadata chunks are skipped.
//
// Offset tracking (task #234): offset is maintained locally instead of asking
// riff.ReadChunkBuf to discover it via Seek(SeekCurrent) on every chunk — the
// stream is read strictly sequentially here (SkipChunk seeks forward past
// skipped chunks; readPaddedChunk reads its chunk's data immediately after
// the header with no intervening seek), so the position after processing any
// chunk is always exactly offset + 8 (header) + chunk.Size + padding. This
// removes one Seek call per chunk, metadata or not.
func readWebPChunks(r io.ReadSeeker) (rawEXIF, rawXMP []byte, err error) {
	var hdrBuf [8]byte
	offset := int64(12) // immediately after the 12-byte RIFF/WEBP header

	// streamEnd is the total stream length, measured lazily via a single
	// Seek(SeekEnd)+Seek(SeekStart) round trip the first time a metadata chunk
	// is actually encountered (readPaddedChunk needs it for its
	// stream-availability guard). A file with no EXIF/XMP chunks pays zero
	// seek cost at all; a file with both pays it once, not twice.
	streamEnd := int64(-1)

	for {
		dataOffset := offset + 8
		chunk, rerr := riff.ReadChunkHeaderAt(r, &hdrBuf, dataOffset)
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return rawEXIF, rawXMP, nil
			}
			return nil, nil, fmt.Errorf("webp: read chunk: %w", rerr)
		}

		if chunk.FourCC == fourCCEXIF || chunk.FourCC == fourCCXMP || chunk.FourCC == fourCCXMPNul {
			streamEnd, rawEXIF, rawXMP, err = readMetadataChunk(r, chunk, streamEnd, dataOffset, rawEXIF, rawXMP)
			if err != nil {
				return nil, nil, err
			}
		} else if err = riff.SkipChunk(r, chunk); err != nil {
			return nil, nil, fmt.Errorf("webp: skip chunk: %w", err)
		}

		advance := int64(8) + int64(chunk.Size)
		if chunk.Size%2 != 0 {
			advance++ // RIFF odd-size padding byte
		}
		offset += advance
	}
}

// readMetadataChunk handles the EXIF/XMP branch of readWebPChunks's per-chunk
// dispatch, extracted to keep readWebPChunks's own cyclomatic complexity low.
// It measures streamEnd on first use (streamEnd < 0 signals "not yet
// measured") and reads chunk's payload into rawEXIF or rawXMP.
func readMetadataChunk(r io.ReadSeeker, chunk riff.Chunk, streamEnd, dataOffset int64, rawEXIF, rawXMP []byte) (newStreamEnd int64, newEXIF, newXMP []byte, err error) {
	if streamEnd < 0 {
		streamEnd, err = measureStreamEnd(r, dataOffset)
		if err != nil {
			return streamEnd, rawEXIF, rawXMP, err
		}
	}
	data, rerr := readPaddedChunk(r, chunk, streamEnd)
	if rerr != nil {
		return streamEnd, rawEXIF, rawXMP, fmt.Errorf("webp: read %s chunk: %w", chunk.FourCCString(), rerr)
	}
	switch chunk.FourCC {
	case fourCCEXIF:
		rawEXIF = stripExifPreamble(data)
	case fourCCXMP:
		rawXMP = data // the standard FourCC wins over a "XMP\0" chunk
	default: // fourCCXMPNul
		if rawXMP == nil {
			rawXMP = data
		}
	}
	return streamEnd, rawEXIF, rawXMP, nil
}

// exifPreamble is the "Exif\0\0" identifier of a JPEG APP1 Exif segment.
var exifPreamble = []byte("Exif\x00\x00") //nolint:gochecknoglobals // immutable comparison constant

// stripExifPreamble returns the TIFF stream of an 'EXIF' chunk payload. The
// chunk holds the TIFF structure of the Exif data (Google WebP Container
// Specification, "Metadata"; RFC 9649 §2.7.1.5), but writers exist that copy
// a whole JPEG APP1 payload into it, "Exif\0\0" identifier included; as
// metadata-extractor does (WebpRiffHandler.java:processChunk,
// ExifReader.startsWithJpegExifPreamble), the identifier is skipped so the
// payload parses as the TIFF stream it carries (task #333). A payload without
// it is returned unchanged; the result aliases data.
func stripExifPreamble(data []byte) []byte {
	if bytes.HasPrefix(data, exifPreamble) {
		return data[len(exifPreamble):]
	}
	return data
}

// measureStreamEnd seeks to the end of r to learn the total stream length,
// then restores the position to resumeAt so sequential chunk reading can
// continue uninterrupted. Called at most once per Extract call — only when a
// metadata chunk is actually encountered — instead of once per metadata
// chunk (task #234).
func measureStreamEnd(r io.ReadSeeker, resumeAt int64) (int64, error) {
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("webp: seek to end for size check: %w", err)
	}
	if _, err = r.Seek(resumeAt, io.SeekStart); err != nil {
		return 0, fmt.Errorf("webp: seek back after size check: %w", err)
	}
	return end, nil
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

// readPaddedChunk reads chunk.Size bytes from r into a new slice and consumes
// the RIFF odd-size padding byte when needed.
// RIFF spec: chunks with odd byte counts are followed by a 1-byte zero pad.
//
// DoS defence — two-stage guard before any allocation:
//  1. Hard cap: reject chunk.Size > maxWebPChunkSize (256 MiB) regardless.
//  2. Stream-availability check: streamEnd (the total stream length, measured
//     once by the caller — see measureStreamEnd) minus chunk.Offset gives the
//     bytes actually remaining; if chunk.Size exceeds that, the declared size
//     cannot be satisfied and the file is adversarial or truncated — return
//     error without allocating.
//
// Stage 2 prevents a crafted file (e.g. chunk.Size = 200 MiB in a 50-byte
// stream) from triggering a multi-hundred-megabyte make([]byte, chunk.Size)
// before io.ReadFull inevitably fails. Because streamEnd is measured once per
// Extract call rather than once per metadata chunk (task #234), and r is
// already positioned at chunk.Offset by sequential consumption, this
// function itself performs no Seek at all: the odd-size padding byte is read
// (and discarded) rather than skipped via Seek(SeekCurrent), so a plain
// io.Reader suffices.
func readPaddedChunk(r io.Reader, chunk riff.Chunk, streamEnd int64) ([]byte, error) {
	// Stage 1: hard cap — rejects pathologically large values (> 256 MiB).
	if chunk.Size > maxWebPChunkSize {
		return nil, fmt.Errorf("webp: chunk %q size %d exceeds limit: %w",
			chunk.FourCCString(), chunk.Size, ErrChunkTooLarge)
	}

	// Stage 2: stream-availability guard, computed purely from the
	// caller-supplied streamEnd — no Seek needed here.
	remaining := max(streamEnd-chunk.Offset, 0)
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
		var pad [1]byte
		if _, err := io.ReadFull(r, pad[:]); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("webp: read past odd-size padding byte: %w", err)
		}
	}
	return data, nil
}

// Inject writes a modified WebP stream to w with updated EXIF and XMP chunks.
// rawIPTC is ignored (WebP has no IPTC support).
//
// preserveUnknownSegments must be true. WebP RIFF chunks other than EXIF and
// XMP (VP8, VP8L, VP8X, ANIM, ANMF, ALPH, etc.) are structurally required for
// correct image decoding and are never "unknown" in the JPEG APPn sense.
// Passing false returns ErrPreserveUnknownSegmentsNotSupported.
//
// Task #391: the file is never held in memory. Two passes over the chunk
// headers of r (which must be seekable) compute the RIFF size field and the VP8X
// chunk of the output; the image chunks are then copied from r to w through a
// fixed pooled buffer (iobuf.CopyRange), so memory use is independent of the
// size of the file, which has no size limit but the 4 GiB of the RIFF size
// field. The bytes written are those the former whole-file implementation wrote.
// A source that is shorter than it was when the first pass measured it makes
// Inject return an error after part of the output has been written (Write has
// the same trust boundary).
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	// Reject PreserveUnknownSegments(false) for WebP: all non-metadata chunks
	// (VP8, VP8L, VP8X, ANIM, ANMF, ALPH) are required for correct image
	// decoding. There is no equivalent of JPEG's "unknown APPn" in WebP.
	if !preserveUnknownSegments {
		return ErrPreserveUnknownSegmentsNotSupported
	}

	// Defense in depth: reject a JPEG extended-XMP wire-frame that was not
	// filtered out by the encodeXMP format check in write.go. The wire-frame
	// begins with 0x00XMPEXT\x00 — an invalid start for any XMP packet — and
	// can only be decoded by jpeg.Inject. Writing it verbatim to a WebP XMP
	// chunk would produce a corrupt, non-XMP blob. (Bug #70.)
	if len(rawXMP) >= len(xmpWireFrameMagic) && [8]byte(rawXMP[:8]) == xmpWireFrameMagic {
		return fmt.Errorf("webp: rawXMP contains an internal JPEG wire-frame encoding that cannot be stored in a WebP container: %w", ErrCorruptXMP)
	}
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("webp: seek: %w", err)
	}
	if size < 12 {
		return ErrFileTooShort
	}

	win := iobuf.NewWindow(r, size)
	defer win.Close()
	out, err := planOutput(win, size, rawEXIF, rawXMP)
	if err != nil {
		return err
	}
	return out.write(r, w, win)
}

// output is the plan of the file Inject writes, made by the first pass over the
// chunks of the source.
type output struct {
	size      int64 // size of the source
	rawEXIF   []byte
	rawXMP    []byte
	hasEXIF   bool
	hasXMP    bool
	writeVP8X bool                // the output has a VP8X chunk
	vp8x      [vp8xChunkSize]byte // its payload
	riffSize  uint32              // the size field of the RIFF header
}

// planOutput makes the first pass: it measures the output and builds the VP8X
// chunk of it.
func planOutput(win *iobuf.Window, size int64, rawEXIF, rawXMP []byte) (output, error) {
	out := output{size: size, rawEXIF: rawEXIF, rawXMP: rawXMP, hasEXIF: rawEXIF != nil, hasXMP: rawXMP != nil}
	scan, err := scanOriginalChunks(win, size, out.hasEXIF || out.hasXMP)
	if err != nil {
		return out, fmt.Errorf("webp: read chunks: %w", err)
	}
	// The VP8X chunk of the output: written when EXIF or XMP is present or the
	// source was already extended. When the source has none, it is built from
	// the image bitstream (task #299), or from a zero canvas when no image chunk
	// has a decodable header.
	out.writeVP8X = out.hasEXIF || out.hasXMP || scan.haveVP8X
	if out.writeVP8X {
		out.vp8x = scan.vp8xPayload(out.hasEXIF, out.hasXMP)
	}

	// RIFF size = the "WEBP" form type + every chunk of the output
	// (RFC 9649 §2.3: the file size minus the 8 bytes of "RIFF" and the size).
	body := int64(4) + scan.retained
	if out.writeVP8X {
		body += chunkLen(vp8xChunkSize)
	}
	if out.hasEXIF {
		body += chunkLen(int64(len(rawEXIF)))
	}
	if out.hasXMP {
		body += chunkLen(int64(len(rawXMP)))
	}
	if body > math.MaxUint32 {
		return out, fmt.Errorf("webp: the output is %d bytes, more than the RIFF size field can hold: %w", body+8, ErrFileTooLarge)
	}
	out.riffSize = uint32(body) //nolint:gosec // G115: body <= MaxUint32 checked above
	return out, nil
}

// write makes the second pass: the RIFF header, the VP8X chunk, the chunks of
// the source that are kept (copied from r), then the EXIF and XMP chunks.
func (o *output) write(r io.ReadSeeker, w io.Writer, win *iobuf.Window) error {
	// hdr is the scratch of the headers written below: declared once, because
	// passing a stack array through io.Writer allocates it on every call.
	hdr := new([riffHeaderLen]byte)
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], o.riffSize)
	copy(hdr[8:12], "WEBP")
	if _, err := w.Write(hdr[:riffHeaderLen]); err != nil {
		return fmt.Errorf("webp: write header: %w", err)
	}
	if o.writeVP8X {
		if err := writeChunk(w, hdr, fourCCVP8X, o.vp8x[:]); err != nil {
			return err
		}
	}
	if err := copyOriginalChunks(r, w, win, o.size, hdr); err != nil {
		return err
	}
	if o.hasEXIF {
		if err := writeChunk(w, hdr, fourCCEXIF, o.rawEXIF); err != nil {
			return err
		}
	}
	if o.hasXMP {
		return writeChunk(w, hdr, fourCCXMP, o.rawXMP)
	}
	return nil
}

// riffHeaderLen is the length of the RIFF header: "RIFF", the size, "WEBP".
const riffHeaderLen = 12

// chunkLen returns the length of a chunk of n data bytes in the output: the
// 8-byte header, the data and the pad byte of an odd size (RIFF: chunks are
// word aligned).
func chunkLen(n int64) int64 { return 8 + n + n&1 }

// writeChunk writes a chunk with the data given: header, data and the pad
// byte of an odd size. hdr is the caller's scratch.
func writeChunk(w io.Writer, hdr *[riffHeaderLen]byte, id [4]byte, data []byte) error {
	if err := writeChunkHeader(w, hdr, id, int64(len(data))); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("webp: write %s chunk: %w", id[:], err)
	}
	return writePad(w, hdr, int64(len(data)))
}

// writeChunkHeader writes the 8-byte header of a chunk of n data bytes.
func writeChunkHeader(w io.Writer, hdr *[riffHeaderLen]byte, id [4]byte, n int64) error {
	copy(hdr[0:4], id[:])
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(n)) //nolint:gosec // G115: RIFF chunk size bounded by the size field of the output, checked by Inject
	if _, err := w.Write(hdr[:8]); err != nil {
		return fmt.Errorf("webp: write %s chunk header: %w", id[:], err)
	}
	return nil
}

// writePad writes the zero pad byte that follows a chunk of n data bytes when
// n is odd.
func writePad(w io.Writer, hdr *[riffHeaderLen]byte, n int64) error {
	if n&1 == 0 {
		return nil
	}
	hdr[0] = 0
	if _, err := w.Write(hdr[:1]); err != nil {
		return fmt.Errorf("webp: write chunk padding: %w", err)
	}
	return nil
}

// chunkWalker iterates over the RIFF chunk list of a source of size bytes,
// from byte 12 (after the RIFF/WEBP header), reading only the chunk headers.
type chunkWalker struct {
	win  *iobuf.Window
	size int64
	pos  int64
}

// origChunk is one chunk of the source as chunkWalker reports it.
type origChunk struct {
	id [4]byte
	// declared is the size field of the chunk; start and end bound its data,
	// clamped to the source (truncated or RIFF-size-mismatched files).
	declared   int64
	start, end int64
}

// dataLen returns the number of data bytes the chunk has in the source.
func (c origChunk) dataLen() int64 { return c.end - c.start }

// next returns the next chunk, or false when the list ends. The list ends at
// fewer than 8 bytes, and at a chunk that claims 2 GiB or more (a RIFF chunk
// size is 32 bits, and on a 32-bit platform such a size would wrap negative,
// task #74; such a chunk cannot hold valid EXIF or XMP, and the rest of the
// stream is treated as unreadable).
func (cw *chunkWalker) next() (c origChunk, ok bool, err error) {
	if cw.pos+8 > cw.size {
		return c, false, nil
	}
	hdr, err := cw.win.Peek(cw.pos, 8)
	if err != nil {
		return c, false, fmt.Errorf("webp: read chunk header: %w", err)
	}
	rawSize := binary.LittleEndian.Uint32(hdr[4:])
	if rawSize > math.MaxInt32 {
		return c, false, nil
	}
	copy(c.id[:], hdr[:4])
	c.declared = int64(rawSize)
	c.start = cw.pos + 8
	// Clamp to the available data so that the chunks after a truncated one are
	// not dropped when its size exceeds the remaining bytes.
	c.end = min(c.start+c.declared, cw.size)
	cw.pos = c.end + c.declared&1 // RIFF padding byte
	return c, true, nil
}

// isMetadataChunk reports whether the chunk is one the output rebuilds: VP8X,
// EXIF and XMP (RFC 9649 §2.7.1.5: at most one chunk of each type); a
// non-standard "XMP\0" chunk is XMP read as such (task #333) and is dropped
// with them so that only one XMP chunk remains.
func isMetadataChunk(id [4]byte) bool {
	switch id {
	case fourCCVP8X, fourCCEXIF, fourCCXMP, fourCCXMPNul:
		return true
	}
	return false
}

// originalScan is what the first pass over the chunks of the source finds.
type originalScan struct {
	// retained is the length, in the output, of the chunks that are kept.
	retained int64
	// vp8x holds the first 10 bytes of the VP8X payload of the source when
	// haveVP8X: the last one whose declared region is trusted.
	vp8x     [vp8xChunkSize]byte
	haveVP8X bool
	still    stillImage
}

// scanOriginalChunks makes the first pass: it measures the chunks that stay in
// the output, captures the VP8X payload of the source, and, when wantImage,
// reads what a VP8X chunk built from the image bitstream needs.
func scanOriginalChunks(win *iobuf.Window, size int64, wantImage bool) (scan originalScan, err error) {
	cw := chunkWalker{win: win, size: size, pos: 12}
	for {
		c, ok, err := cw.next()
		if err != nil || !ok {
			return scan, err
		}
		if c.id == fourCCVP8X {
			if cerr := scan.captureVP8X(win, size, c); cerr != nil {
				return scan, cerr
			}
			continue
		}
		if isMetadataChunk(c.id) {
			continue
		}
		scan.retained += chunkLen(c.dataLen())
		if !wantImage {
			continue
		}
		var head []byte
		if scan.still.wantsHead(c.id) {
			if head, err = win.Peek(c.start, int(min(c.dataLen(), headBytes))); err != nil {
				return scan, fmt.Errorf("webp: read chunk data: %w", err)
			}
		}
		scan.still.add(c.id, head, int(c.dataLen()))
	}
}

// captureVP8X records the payload of the VP8X chunk c, so that the canvas
// dimensions and the other feature bits can be kept.
//
// Cross-chunk contamination guard (rmp task #57): a malformed file can declare
// VP8X size=10 while holding fewer real VP8X bytes, followed immediately by the
// next RIFF chunk; the declared bytes would then include the header of the
// next chunk and corrupt the canvas. So after the declared region there must be
// the end of the file or a recognised WebP chunk FourCC; otherwise the declared
// size is wrong and the payload is discarded (the VP8X chunk of the output
// then has a zero canvas, or is built from the image bitstream).
func (s *originalScan) captureVP8X(win *iobuf.Window, size int64, c origChunk) error {
	next := c.start + c.declared
	if c.declared < vp8xChunkSize || next > size {
		return nil
	}
	known, err := isEOFOrKnownFourCCAt(win, size, next)
	if err != nil || !known {
		return err
	}
	payload, err := win.Peek(c.start, vp8xChunkSize)
	if err != nil {
		return fmt.Errorf("webp: read VP8X chunk: %w", err)
	}
	copy(s.vp8x[:], payload)
	s.haveVP8X = true
	return nil
}

// vp8xPayload returns the payload of the VP8X chunk of the output. When the
// source has a VP8X chunk its canvas and feature flags are kept and only the
// EXIF and XMP flags are updated (buildVP8XFlags). When the source is a
// simple-format file (VP8 or VP8L only, no VP8X), the chunk is built from the
// image bitstream: the WebP Container Specification requires the VP8X canvas
// to match the image dimensions, and libwebp's decoder rejects a mismatch as
// BITSTREAM_ERROR (src/dec/webp_dec.c ParseHeadersInternal:
// canvas_width != image_width). Only when no image chunk has a decodable
// header, a malformed source with no valid canvas size to copy, does it fall
// back to buildVP8XFlags' zero canvas.
func (s *originalScan) vp8xPayload(hasEXIF, hasXMP bool) [vp8xChunkSize]byte {
	var orig []byte
	if s.haveVP8X {
		orig = s.vp8x[:]
	} else if payload, ok := s.still.payload(hasEXIF, hasXMP); ok {
		return payload
	}
	return [vp8xChunkSize]byte(buildVP8XFlags(hasEXIF, hasXMP, orig))
}

// copyOriginalChunks makes the second pass: it writes every chunk of the source
// that is not rebuilt, in order, each with its size field set to the data it
// has in the source (the clamped length) and a zero pad byte after an odd size.
func copyOriginalChunks(r io.ReadSeeker, w io.Writer, win *iobuf.Window, size int64, hdr *[riffHeaderLen]byte) error {
	cw := chunkWalker{win: win, size: size, pos: 12}
	for {
		c, ok, err := cw.next()
		if err != nil {
			return fmt.Errorf("webp: read chunks: %w", err)
		}
		if !ok {
			return nil
		}
		if isMetadataChunk(c.id) {
			continue
		}
		n := c.dataLen()
		if err := writeChunkHeader(w, hdr, c.id, n); err != nil {
			return err
		}
		if err := iobuf.CopyRange(r, w, c.start, n); err != nil {
			return fmt.Errorf("webp: copy %s chunk: %w", c.id[:], err)
		}
		if err := writePad(w, hdr, n); err != nil {
			return err
		}
	}
}

// buildVP8XFlags constructs a 10-byte VP8X chunk payload with the EXIF (bit 3)
// and XMP (bit 2) feature flags set or cleared according to hasEXIF/hasXMP.
// All other bits and the canvas dimension fields are copied from origVP8XData
// when present, preserving ICC, animation, alpha, and dimension information.
func buildVP8XFlags(hasEXIF, hasXMP bool, origVP8XData []byte) []byte {
	vp8xData := make([]byte, vp8xChunkSize)
	// Guard: only copy from origVP8XData when it is known to hold at least 10
	// bytes. captureVP8X enforces this invariant at capture time, but the
	// explicit len check here makes the safety guarantee local and prevents a
	// future caller from accidentally passing a short slice.
	if len(origVP8XData) >= 10 {
		copy(vp8xData, origVP8XData[:10])
	}
	// Update only the EXIF (bit 3) and XMP (bit 2) feature flags.
	flags := binary.LittleEndian.Uint32(vp8xData[0:])
	if hasXMP {
		flags |= vp8xFlagXMP
	} else {
		flags &^= vp8xFlagXMP
	}
	if hasEXIF {
		flags |= vp8xFlagEXIF
	} else {
		flags &^= vp8xFlagEXIF
	}
	binary.LittleEndian.PutUint32(vp8xData[0:], flags)
	return vp8xData
}

// knownWebPFourCCs is the exhaustive set of chunk FourCC identifiers defined
// by the WebP container specification (https://developers.google.com/speed/webp/docs/riff_container).
// Used by isEOFOrKnownFourCCAt to validate VP8X region boundaries. [4]byte
// values (task #234) let the comparison avoid allocating.
var knownWebPFourCCs = [...][4]byte{ //nolint:gochecknoglobals // immutable lookup table
	{'V', 'P', '8', ' '}, {'V', 'P', '8', 'L'}, {'V', 'P', '8', 'X'},
	{'A', 'N', 'I', 'M'}, {'A', 'N', 'M', 'F'}, {'A', 'L', 'P', 'H'},
	{'I', 'C', 'C', 'P'}, {'E', 'X', 'I', 'F'}, {'X', 'M', 'P', ' '},
}

// isEOFOrKnownFourCCAt reports whether position pos of a source of size bytes
// is either past the end of the source (EOF) or the start of a recognised WebP
// chunk FourCC (4 bytes). A VP8X declared region is only trusted when the byte
// immediately following it satisfies this predicate — otherwise the declared
// size is wrong and the captured bytes span into an adjacent chunk.
func isEOFOrKnownFourCCAt(win *iobuf.Window, size, pos int64) (bool, error) {
	if pos >= size {
		return true, nil // EOF is valid
	}
	if pos+4 > size {
		return false, nil // not enough bytes to read a FourCC
	}
	b, err := win.Peek(pos, 4)
	if err != nil {
		return false, fmt.Errorf("webp: read chunk header: %w", err)
	}
	candidate := [4]byte(b)
	for _, known := range knownWebPFourCCs {
		if candidate == known {
			return true, nil
		}
	}
	return false, nil
}
