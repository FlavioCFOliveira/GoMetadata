// Package png implements extraction and injection of EXIF and XMP metadata
// within PNG files.
//
// PNG structure: 8-byte signature followed by chunks, each with:
// 4-byte length + 4-byte type + <length> bytes data + 4-byte CRC.
//
// Relevant chunks:
//   - eXIf (PNG Extension, registered 2017): raw EXIF payload.
//   - iTXt with keyword "XML:com.adobe.xmp": XMP packet (RFC 2083 §12.13).
package png

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"math"
	"sync"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// pngSig is the 8-byte PNG file signature (PNG §5.2).
var pngSig = [8]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} //nolint:gochecknoglobals // package-level constant bytes

// xmpWireFrameMagic is the 8-byte sentinel that identifies a JPEG extended-XMP
// wire-frame payload (defined in format/jpeg; duplicated here to avoid an import
// cycle). The wire-frame is an internal encoding used exclusively by jpeg.Inject;
// it must never reach the PNG injector.
//
// Layout: [0x00]['X']['M']['P']['E']['X']['T'][0x00]
// The leading 0x00 is unambiguous: no valid XMP packet starts with a null byte.
var xmpWireFrameMagic = [8]byte{0x00, 'X', 'M', 'P', 'E', 'X', 'T', 0x00} //nolint:gochecknoglobals // package-level constant bytes

// xmpKeyword is the iTXt keyword used by Adobe XMP (XMP Part 3 §1.1.4).
const xmpKeyword = "XML:com.adobe.xmp"

// PNG chunk types compared against the [4]byte chunk type readChunk decodes
// from the wire (PNG §5.3: the chunk type is a 4-byte code). Comparing
// [4]byte values, and building one from a package-level constant, is
// allocation-free — unlike the former string-keyed comparison, which
// allocated a fresh string for every chunk read (task #232).
var (
	chunkEXIf = [4]byte{'e', 'X', 'I', 'f'} //nolint:gochecknoglobals // package-level constant chunk type; never mutated
	chunkITXt = [4]byte{'i', 'T', 'X', 't'} //nolint:gochecknoglobals // package-level constant chunk type; never mutated
	chunkTEXt = [4]byte{'t', 'E', 'X', 't'} //nolint:gochecknoglobals // package-level constant chunk type; never mutated
	chunkZTXt = [4]byte{'z', 'T', 'X', 't'} //nolint:gochecknoglobals // package-level constant chunk type; never mutated
	chunkIHDR = [4]byte{'I', 'H', 'D', 'R'} //nolint:gochecknoglobals // package-level constant chunk type; never mutated
	chunkIEND = [4]byte{'I', 'E', 'N', 'D'} //nolint:gochecknoglobals // package-level constant chunk type; never mutated
)

// maxZlibDecompressSize is the upper bound on decompressed output from a single
// PNG metadata chunk. Legitimate EXIF and XMP payloads are many orders of
// magnitude smaller; exceeding this limit indicates a decompression bomb or
// a malformed file. Enforced via io.LimitReader to avoid unbounded allocation.
const maxZlibDecompressSize int64 = 64 << 20 // 64 MiB

// maxPNGChunkSize is the maximum allowed data length for a single PNG chunk
// before any allocation is attempted. The PNG spec (ISO 15948 §11.2.1) permits
// up to 2^31−1 bytes, but that value is pathological for metadata. 256 MiB is
// orders of magnitude larger than any real EXIF or XMP payload; any file that
// declares more is either malformed or adversarial.
// Guard applied in readChunk before iobuf.Get(length) to prevent a single
// 4-byte field in the file from causing a multi-gigabyte heap allocation.
const maxPNGChunkSize = 256 << 20 // 256 MiB

// zlibPool stores reusable io.ReadCloser values (zlib.NewReader return type).
// Reusing them via zlib.Resetter avoids the ~32 KB internal decompression-state
// allocation on every call to zlibDecompress.
var zlibPool sync.Pool //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure

// bytesReaderPool stores reusable *bytes.Reader values, the io.Reader zlib
// decompresses from. Pooling it (task #233) avoids a fresh allocation on
// every zlibDecompress call, mirroring zlibPool's own reuse strategy.
var bytesReaderPool = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
	New: func() any { return new(bytes.Reader) },
}

// crc32Pool stores reusable hash.Hash32 values (crc32.NewIEEE return type).
// Reusing them via Reset avoids a small allocation on every writeChunk call.
var crc32Pool = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
	New: func() any { return crc32.NewIEEE() },
}

// putBytesReader returns br to bytesReaderPool after clearing its internal
// slice reference via Reset(nil).
//
// Security audit finding PNG-BYTESREADERPOOL-RETENTION-01: bytes.Reader.Reset
// stores its argument in an unexported field that a plain bytesReaderPool.Put
// does not clear, so a pooled reader keeps the CALLER'S input slice reachable
// for as long as it sits in the pool — for zlibDecompress's callers, that
// input is the compressed chunk payload, bounded by maxPNGChunkSize (256
// MiB), not by the much smaller decompressed-output cap. Reset(nil) drops
// the reference immediately, before the reader re-enters the pool, at
// negligible cost (it only zeroes three fields).
func putBytesReader(br *bytes.Reader) {
	br.Reset(nil)
	bytesReaderPool.Put(br)
}

// zlibDecompress decompresses a zlib-deflated payload. It gets a reader from
// zlibPool (or allocates one) and returns it to the pool when done without
// closing it, so the next caller can Reset it instead of allocating again.
// The intermediate *bytes.Reader is likewise pooled (task #233); both pools
// are released via explicit Put calls (putBytesReader for the *bytes.Reader)
// on every return path rather than defer, so the function stays simple to
// reason about at every exit point.
func zlibDecompress(data []byte) ([]byte, error) {
	br := bytesReaderPool.Get().(*bytes.Reader) //nolint:forcetypeassert,revive // bytesReaderPool.New always stores *bytes.Reader; pool invariant
	br.Reset(data)

	var zr io.ReadCloser
	if v := zlibPool.Get(); v != nil {
		zr = v.(io.ReadCloser)                                    //nolint:forcetypeassert,revive // zlibPool.New always stores io.ReadCloser; pool invariant
		if err := zr.(zlib.Resetter).Reset(br, nil); err != nil { //nolint:forcetypeassert // zlib.NewReader always implements zlib.Resetter; Go stdlib guarantee
			putBytesReader(br)
			return nil, fmt.Errorf("png: zlib reset: %w", err)
		}
	} else {
		var err error
		zr, err = zlib.NewReader(br)
		if err != nil {
			putBytesReader(br)
			return nil, fmt.Errorf("png: zlib open: %w", err)
		}
	}
	// Cap decompressed output to prevent decompression bombs: a crafted zlib
	// stream with a tiny compressed payload can expand to gigabytes of output.
	// Reading one byte beyond the limit lets us detect overflow without reading it all.
	out, err := io.ReadAll(io.LimitReader(zr, maxZlibDecompressSize+1))
	// Return both pooled values now that reading has fully completed; zr is
	// returned without closing so it can be Reset on next use.
	zlibPool.Put(zr)
	putBytesReader(br)
	if err != nil {
		return nil, fmt.Errorf("png: zlib decompress: %w", err)
	}
	if int64(len(out)) > maxZlibDecompressSize {
		return nil, errDecompressBomb
	}
	return out, nil
}

// processExtractChunk dispatches a single PNG chunk during Extract, updating
// rawEXIF and rawXMP as appropriate. It returns (rawEXIF, rawXMP, done, err)
// where done is true when IEND signals the end of the chunk stream.
// data is backed by a pooled buffer; any slice that must outlive this call is
// cloned here or in its callees before being returned as rawEXIF/rawXMP.
func processExtractChunk(chunkType [4]byte, data, rawEXIF, rawXMP []byte) ([]byte, []byte, bool, error) {
	switch chunkType {
	case chunkEXIf:
		// Clone: data is pooled and will be returned to the pool after this call.
		return bytes.Clone(data), rawXMP, false, nil
	case chunkITXt, chunkTEXt, chunkZTXt:
		xmp, err := handleXMPChunk(chunkType, data, rawXMP)
		if err != nil {
			return rawEXIF, rawXMP, false, err
		}
		return rawEXIF, xmp, false, nil
	case chunkIEND:
		return rawEXIF, rawXMP, true, nil
	}
	return rawEXIF, rawXMP, false, nil
}

// extractIgnores reports whether Extract's chunk-scanning loop can skip a
// chunk's payload and CRC trailer entirely via Seek, without reading them.
// Extract only ever looks at eXIf, iTXt/tEXt/zTXt (candidate XMP carriers),
// and IEND (end-of-stream signal); IHDR is also read, even though Extract
// never inspects its fields, to preserve today's behaviour of validating its
// CRC (IHDR is part of metadataChunks, shared with the verification policy
// documented there).
//
// #288: every other chunk type — IDAT (pixel data, routinely megabytes per
// chunk), PLTE, gAMA, cHRM, sRGB, pHYs, tIME, bKGD, and any private or
// ancillary chunk — carries no metadata this library reads, so allocating a
// buffer, reading it, and (for IDAT especially) discarding it immediately
// was pure waste: round-1 profiling attributed 47.5% of PNG Read CPU to the
// resulting memmove traffic and the bulk of its allocations to these
// discarded reads.
func extractIgnores(chunkType [4]byte) bool {
	switch chunkType {
	case chunkEXIf, chunkITXt, chunkTEXt, chunkZTXt, chunkIEND, chunkIHDR:
		return false
	}
	return true
}

// streamSize returns the total length of r as seen from its current
// position, restoring that position before returning, or (-1, nil) if r does
// not support seeking to its end. -1 disables the bounds-checked chunk-skip
// fast path in Extract/Inject; callers fall back to the natural read-based
// truncation detection that was already in place for every chunk they
// actually read, so correctness never depends on Seek(SeekEnd) support.
//
// #288: computed once per Extract/Inject call (two Seek calls, no I/O to
// speak of), not per chunk. A chunk that IS skipped still needs its declared
// length checked against the remaining stream before Seek-ing past it: most
// io.ReadSeeker implementations (bytes.Reader, os.File) allow seeking past
// EOF without error, so an unchecked skip would silently tolerate a
// truncated file instead of reporting it — mirroring the bounds check
// Exiv2's pngimage.cpp performs before every chunk skip (~L414/L473).
func streamSize(r io.ReadSeeker) (int64, error) {
	cur, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return -1, nil //nolint:nilerr // non-seekable-position reader: disable the bounds-checked fast path, not fatal
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return -1, nil //nolint:nilerr // same rationale: degrade gracefully, do not fail the caller
	}
	if _, err := r.Seek(cur, io.SeekStart); err != nil {
		return 0, fmt.Errorf("png: seek restore: %w", err)
	}
	return end, nil
}

// checkChunkTruncation returns a "truncated chunk" error when total is known
// (>= 0) and the chunk beginning at pos (the position immediately after its
// 8-byte header) with the given declared data length would extend past the
// end of the stream. Shared by Extract's readOrSkipChunk and Inject's
// injectOneChunk (#288) so both apply the identical bounds check documented
// on streamSize.
func checkChunkTruncation(chunkType [4]byte, pos, length, total int64) error {
	if total >= 0 && pos+length+4 > total {
		return fmt.Errorf("png: truncated chunk %q: declared length %d exceeds remaining stream: %w",
			chunkTypeStr(chunkType), length, io.ErrUnexpectedEOF)
	}
	return nil
}

// Extract reads the PNG chunk stream from r and returns raw metadata payloads.
//
// #294: when a chunk's declared length would overrun the stream — whether
// the chunk would have been skipped (extractIgnores) or read for its
// metadata content — Extract stops scanning at that point and returns
// whatever rawEXIF/rawIPTC/rawXMP had already been collected, with a nil
// error, rather than rejecting the file outright. This restores parity with
// ce1dc82 (pre-#288) for testdata/corpus/png/exiv2/issue_790_poc2.png (an
// overrunning iCCP chunk, which #288's stream-size bounds check correctly
// detects but which #288 then treated as fatal) and issue_428_poc2.png (an
// overrunning iTXt chunk — not an ignored chunk type, proving the graceful
// stop is not limited to the skip path): both files predate whatever
// metadata chunk their declared length overruns, so ce1dc82 itself read
// nothing from them either — the correct outcome is "no metadata, no
// error", not "no metadata, error". Inject's own truncation check
// (injectOneChunk) is unaffected and continues to reject a truncated input
// outright: writing a NEW file that silently drops trailing content the
// caller did not ask to remove is a materially different, unsafe operation
// from a best-effort Read.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("png: seek: %w", err)
	}

	var sig [8]byte
	if _, err = io.ReadFull(r, sig[:]); err != nil {
		return nil, nil, nil, fmt.Errorf("png: read signature: %w", err)
	}
	if sig != pngSig {
		return nil, nil, nil, ErrInvalidSignature
	}

	total, szErr := streamSize(r)
	if szErr != nil {
		return nil, nil, nil, fmt.Errorf("png: %w", szErr)
	}

	// #288: hdr is hoisted out of the loop and reused for every chunk header
	// instead of being declared fresh per call.
	var hdr [8]byte
	pos := int64(len(pngSig))
	var done bool
	for !done {
		var n int64
		n, err = readOrSkipChunk(r, &hdr, pos, total, func(chunkType [4]byte, data []byte) error {
			var perr error
			rawEXIF, rawXMP, done, perr = processExtractChunk(chunkType, data, rawEXIF, rawXMP)
			return perr
		})
		pos = n
		if err != nil {
			// #294: io.EOF is a clean end of stream (unchanged from before
			// #288). io.ErrUnexpectedEOF is what checkChunkTruncation wraps
			// when a chunk's declared length would overrun the stream — see
			// this function's own doc comment for why that is treated the
			// same way here: stop and return what was collected, not an
			// error.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return nil, nil, nil, err
		}
	}
	return rawEXIF, nil, rawXMP, nil
}

// readOrSkipChunk reads one PNG chunk header at the reader's current
// position into hdr, then either skips the chunk's data and CRC trailer via
// Seek (when extractIgnores reports Extract does not interpret it) or reads
// it fully and calls fn — the same behaviour readChunk provided before
// #288, including the same CRC verification policy (shouldVerifyCRC /
// verifyCRC32).
//
// pos is the stream position immediately after the signature or the
// previous chunk; total is streamSize's result (-1 if unknown). Every
// chunk's declared length is checked against total via checkChunkTruncation
// BEFORE it is skipped or read, so a chunk that overruns a truncated file is
// rejected here rather than tolerated by a Seek landing past EOF or masked
// by a later io.EOF indistinguishable from a clean end of stream.
//
// Returns the stream position after this chunk so the caller can pass it
// back in on the next call.
//
//nolint:cyclop,gocyclo // straight-line header-parse-then-dispatch flow; splitting would scatter the shared bounds check across multiple functions
func readOrSkipChunk(r io.ReadSeeker, hdr *[8]byte, pos, total int64, fn func(chunkType [4]byte, data []byte) error) (int64, error) {
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return pos, fmt.Errorf("png: read chunk header: %w", err)
	}
	pos += int64(len(hdr))

	var chunkType [4]byte
	copy(chunkType[:], hdr[4:8])

	rawLen := binary.BigEndian.Uint32(hdr[:4])
	if rawLen > math.MaxInt32 {
		return pos, fmt.Errorf("png: chunk %q length %d exceeds 2^31-1: %w", chunkTypeStr(chunkType), rawLen, ErrChunkTooLarge)
	}
	length := int64(rawLen)
	if length > maxPNGChunkSize {
		return pos, fmt.Errorf("png: chunk %q length %d exceeds limit: %w", chunkTypeStr(chunkType), length, ErrChunkTooLarge)
	}
	if err := checkChunkTruncation(chunkType, pos, length, total); err != nil {
		return pos, err
	}

	if extractIgnores(chunkType) {
		if _, err := r.Seek(length+4, io.SeekCurrent); err != nil {
			return pos, fmt.Errorf("png: skip chunk %q: %w", chunkTypeStr(chunkType), err)
		}
		return pos + length + 4, nil
	}

	verifyCRC := shouldVerifyCRC(chunkType)
	if length > 0 {
		if err := readNonEmptyChunk(r, chunkType, int(length), verifyCRC, fn); err != nil {
			return pos, err
		}
		return pos + length + 4, nil
	}

	var crcB [4]byte
	if _, err := io.ReadFull(r, crcB[:]); err != nil {
		return pos, fmt.Errorf("png: read CRC for %q: %w", chunkTypeStr(chunkType), err)
	}
	if verifyCRC {
		if err := verifyCRC32(chunkType, nil, binary.BigEndian.Uint32(crcB[:])); err != nil {
			return pos, err
		}
	}
	if err := fn(chunkType, nil); err != nil {
		return pos, err
	}
	return pos + 4, nil
}

// handleITXtXMP extracts XMP from an iTXt chunk. Returns (xmp, nil) on
// success, (nil, nil) when the chunk is not an XMP iTXt, or (nil, err).
func handleITXtXMP(data []byte) ([]byte, error) {
	return extractXMPFromITXt(data)
}

// handleLegacyXMP extracts XMP from a tEXt or zTXt chunk only when existing
// is nil (legacy chunks do not override a higher-priority iTXt source).
func handleLegacyXMP(chunkType [4]byte, data []byte) ([]byte, error) {
	if chunkType == chunkZTXt {
		return extractXMPFromZTxt(data)
	}
	return extractXMPFromTExt(data), nil
}

// handleXMPChunk dispatches iTXt, tEXt, and zTXt chunks to the appropriate
// XMP extractor. It returns existing unchanged if the chunk does not contain
// XMP, or if existing is already set and the chunk type does not override it.
//
// iTXt priority rule (XMP Part 3 §1.6 / PNG-04): "Exactly one XMP iTXt;
// reader uses first." Once rawXMP is set from an iTXt, all subsequent iTXt
// (and legacy tEXt/zTXt) XMP chunks are ignored.
func handleXMPChunk(chunkType [4]byte, data []byte, existing []byte) ([]byte, error) {
	// XMP Part 3 §1.6 (PNG-04): use the first XMP chunk encountered; never
	// overwrite an already-found XMP with a later chunk of any type.
	if existing != nil {
		return existing, nil
	}
	switch chunkType {
	case chunkITXt:
		xmp, err := handleITXtXMP(data)
		if err != nil {
			return nil, err
		}
		if xmp != nil {
			return xmp, nil
		}
	case chunkTEXt, chunkZTXt:
		xmp, err := handleLegacyXMP(chunkType, data)
		if err != nil {
			return nil, err
		}
		if xmp != nil {
			return xmp, nil
		}
	}
	return existing, nil
}

// shouldDropChunk reports whether the chunk should be dropped during Inject
// because it is being replaced by a new metadata chunk. eXIf is always
// dropped; iTXt is dropped only when it carries an XMP payload.
func shouldDropChunk(chunkType [4]byte, data []byte) bool {
	if chunkType == chunkEXIf {
		return true
	}
	return chunkType == chunkITXt && isXMPChunk(data)
}

// pngCopyBufSize is the fixed size of the reusable buffer streamCopyN uses to
// move pass-through chunk bytes from r to w. Chosen to equal iobuf's own
// largePool tier ceiling (largeSize = 65536) so the buffer is served from
// that existing pool instead of a dedicated one.
const pngCopyBufSize = 65536

// streamCopyN copies exactly n bytes from r to w using one pooled, fixed-size
// buffer, reused across every chunk of the call — never allocating
// proportionally to n and never paying the per-call io.LimitReader /
// io.CopyN allocation a naive io.CopyN(w, r, n) would add on every chunk
// (#288). n is always length+4 (chunk data plus its CRC trailer), so n == 0
// never occurs in practice, but is handled defensively.
func streamCopyN(r io.Reader, w io.Writer, n int64) error {
	if n <= 0 {
		return nil
	}
	bufPtr := iobuf.Get(pngCopyBufSize)
	buf := *bufPtr
	for n > 0 {
		chunk := buf
		if int64(len(chunk)) > n {
			chunk = chunk[:n]
		}
		if _, err := io.ReadFull(r, chunk); err != nil {
			iobuf.Put(bufPtr)
			return fmt.Errorf("png: read chunk data: %w", err)
		}
		if _, err := w.Write(chunk); err != nil {
			iobuf.Put(bufPtr)
			return fmt.Errorf("png: write chunk data: %w", err)
		}
		n -= int64(len(chunk))
	}
	iobuf.Put(bufPtr)
	return nil
}

// copyChunkVerbatim writes hdr to w, then streams length data bytes plus the
// 4-byte CRC trailer from r to w unchanged: no CRC is computed or verified,
// and — unlike readChunkBody — no buffer proportional to length is ever
// allocated. This is the path for every PNG chunk Inject does not need to
// inspect (IDAT, PLTE, gAMA, tEXt, zTXt, IHDR, and any other ancillary or
// private chunk): IDAT alone is routinely megabytes per chunk, and round-1
// profiling attributed the bulk of PNG Write's CPU to needlessly
// recomputing CRC-32 over bytes the library never interprets (#288).
func copyChunkVerbatim(r io.Reader, w io.Writer, hdr *[8]byte, length int64) error {
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("png: write chunk header: %w", err)
	}
	return streamCopyN(r, w, length+4)
}

// readChunkBody reads a chunk's data (length bytes) and its 4-byte CRC
// trailer, without verifying the CRC. It mirrors readNonEmptyChunk's
// pooled-vs-exact-allocation size threshold, so a caller that decides, after
// inspecting data, to copy the chunk through unchanged (writeVerbatimChunk)
// never pays for a second read.
//
// release must be called exactly once when the caller is done with data, to
// return any pooled buffer; it is nil (never call it) whenever err != nil.
func readChunkBody(r io.Reader, length int64) (data []byte, crcB [4]byte, release func(), err error) {
	if length <= largeChunkReadThreshold {
		buf := iobuf.Get(int(length))
		data = (*buf)[:length]
		if _, ferr := io.ReadFull(r, data); ferr != nil {
			iobuf.Put(buf)
			return nil, crcB, nil, fmt.Errorf("png: truncated chunk: %w", ferr)
		}
		release = func() { iobuf.Put(buf) }
	} else {
		data, err = io.ReadAll(io.LimitReader(r, length))
		if err != nil {
			return nil, crcB, nil, fmt.Errorf("png: read chunk: %w", err)
		}
		if int64(len(data)) != length {
			return nil, crcB, nil, fmt.Errorf("png: truncated chunk: read %d of %d bytes: %w", len(data), length, io.ErrUnexpectedEOF)
		}
		release = func() {}
	}
	if _, ferr := io.ReadFull(r, crcB[:]); ferr != nil {
		release()
		return nil, crcB, nil, fmt.Errorf("png: read CRC: %w", ferr)
	}
	return data, crcB, release, nil
}

// writeVerbatimChunk writes hdr, data, and crcB to w unchanged: no CRC is
// computed. Used for an iTXt chunk that Inject inspected but decided to
// preserve (it does not carry the XMP payload being replaced).
func writeVerbatimChunk(w io.Writer, hdr *[8]byte, data []byte, crcB [4]byte) error {
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("png: write chunk header: %w", err)
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("png: write chunk data: %w", err)
		}
	}
	if _, err := w.Write(crcB[:]); err != nil {
		return fmt.Errorf("png: write chunk CRC: %w", err)
	}
	return nil
}

// injectOneChunk reads one PNG chunk header from r and either:
//   - skips the existing eXIf chunk (always replaced, dropped without ever
//     being read);
//   - reads an iTXt chunk's payload to decide whether it is the XMP chunk
//     being replaced — dropping it if so, or copying it through
//     byte-identical (including its original CRC) if not;
//   - copies every other chunk through byte-identical, verbatim, including
//     its original CRC — writing the library's own eXIf/XMP chunks
//     immediately after IHDR, and signalling errPNGDone after IEND.
//
// #288 / USER DECISION: a chunk that is copied through is NEVER "repaired" —
// its original CRC trailer is written back exactly as read, even when that
// CRC does not match the chunk's data (matches ExifTool/Exiv2's documented
// behaviour; see TestInjectPreservesOriginalCRCEvenWhenInvalid). CRC is
// computed only for the eXIf and XMP-iTXt chunks the library itself
// constructs (writeChunk, called from writeMetadataAfterIHDR).
//
// pos/total mirror readOrSkipChunk's bounds-checking contract (see
// checkChunkTruncation): every chunk's declared length is checked against
// the remaining stream before any Seek or read, so a truncated source PNG is
// rejected here rather than silently accepted.
//
//nolint:cyclop,gocyclo // straight-line chunk-type dispatch; splitting would obscure the shared bounds check every branch needs
func injectOneChunk(r io.ReadSeeker, w io.Writer, hdr *[8]byte, pos, total int64, rawEXIF, rawXMP []byte) (int64, error) {
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return pos, fmt.Errorf("png: read chunk header: %w", err)
	}
	pos += int64(len(hdr))

	var chunkType [4]byte
	copy(chunkType[:], hdr[4:8])

	rawLen := binary.BigEndian.Uint32(hdr[:4])
	if rawLen > math.MaxInt32 {
		return pos, fmt.Errorf("png: chunk %q length %d exceeds 2^31-1: %w", chunkTypeStr(chunkType), rawLen, ErrChunkTooLarge)
	}
	length := int64(rawLen)
	if length > maxPNGChunkSize {
		return pos, fmt.Errorf("png: chunk %q length %d exceeds limit: %w", chunkTypeStr(chunkType), length, ErrChunkTooLarge)
	}
	if err := checkChunkTruncation(chunkType, pos, length, total); err != nil {
		return pos, err
	}

	if shouldDropChunk(chunkType, nil) {
		// Only eXIf can be identified as a drop target without its data
		// (shouldDropChunk's iTXt branch always returns false for nil data);
		// skip it without ever reading its payload (#288).
		if _, err := r.Seek(length+4, io.SeekCurrent); err != nil {
			return pos, fmt.Errorf("png: skip chunk %q: %w", chunkTypeStr(chunkType), err)
		}
		return pos + length + 4, nil
	}

	if chunkType == chunkITXt {
		data, crcB, release, err := readChunkBody(r, length)
		if err != nil {
			return pos, fmt.Errorf("png: chunk %q: %w", chunkTypeStr(chunkType), err)
		}
		var werr error
		if !shouldDropChunk(chunkType, data) {
			werr = writeVerbatimChunk(w, hdr, data, crcB)
		}
		release()
		if werr != nil {
			return pos, werr
		}
		return pos + length + 4, nil
	}

	if err := copyChunkVerbatim(r, w, hdr, length); err != nil {
		return pos, err
	}
	switch chunkType {
	case chunkIHDR:
		if err := writeMetadataAfterIHDR(w, rawEXIF, rawXMP); err != nil {
			return pos, err
		}
	case chunkIEND:
		return pos + length + 4, errPNGDone
	}
	return pos + length + 4, nil
}

// Inject reads the PNG chunk stream from r, replaces or inserts the eXIf and
// iTXt(XMP) chunks, and writes the result to w. IPTC is not natively
// supported in PNG; rawIPTC is ignored.
//
// preserveUnknownSegments must be true. PNG chunk semantics differ
// fundamentally from JPEG APPn segments: chunks such as tEXt, gAMA, cHRM, and
// custom application chunks carry data that may be essential to downstream
// readers. Selective stripping without exhaustive chunk-type knowledge risks
// corrupting the file. Pass PreserveUnknownSegments(true) (the default) when
// writing to PNG. Passing false returns ErrPreserveUnknownSegmentsNotSupported.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error { //nolint:cyclop,gocyclo // preserveUnknownSegments guard adds one branch; function already contained maximum allowed complexity pre-#85
	// Reject PreserveUnknownSegments(false) for PNG: PNG chunk semantics are not
	// equivalent to JPEG APPn segments. Chunks like tEXt, gAMA, cHRM, and custom
	// application chunks may be required by downstream tools; stripping them
	// without understanding every registered type risks file corruption.
	// Return an explicit error rather than silently ignoring the request.
	if !preserveUnknownSegments {
		return ErrPreserveUnknownSegmentsNotSupported
	}

	// Defense in depth: reject a JPEG extended-XMP wire-frame that was not
	// filtered out by the encodeXMP format check in write.go. The wire-frame
	// begins with 0x00XMPEXT\x00 — an invalid start for any XMP packet — and
	// can only be decoded by jpeg.Inject. Writing it verbatim to a PNG iTXt
	// chunk would produce a corrupt, non-XMP blob. (Bug #70.)
	if len(rawXMP) >= len(xmpWireFrameMagic) && [8]byte(rawXMP[:8]) == xmpWireFrameMagic {
		return fmt.Errorf("png: rawXMP contains an internal JPEG wire-frame encoding that cannot be stored in a PNG container: %w", ErrCorruptXMP)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("png: seek: %w", err)
	}

	// Validate input signature BEFORE writing anything to w.
	// W3C PNG 3rd ed. §5.2: the first 8 bytes must be the PNG magic sequence.
	// Reading and checking the signature first ensures that w remains empty if
	// the input is not a PNG (e.g. a JPEG passed by mistake); writing the PNG
	// signature unconditionally and then returning an error would leave a
	// partial/corrupt byte sequence in w.
	var sig [8]byte
	if _, err := io.ReadFull(r, sig[:]); err != nil {
		return fmt.Errorf("png: read signature: %w", err)
	}
	if sig != pngSig {
		return ErrInvalidSignature
	}

	total, szErr := streamSize(r)
	if szErr != nil {
		return fmt.Errorf("png: %w", szErr)
	}

	// Input is a valid PNG; write the signature to w.
	if _, err := w.Write(pngSig[:]); err != nil {
		return fmt.Errorf("png: write signature: %w", err)
	}

	var hdr [8]byte
	pos := int64(len(pngSig))
	for {
		var err error
		pos, err = injectOneChunk(r, w, &hdr, pos, total, rawEXIF, rawXMP)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, errPNGDone) {
				// Source PNG had an IEND chunk; it has already been copied
				// through verbatim by injectOneChunk. Return immediately so
				// we do not write a second IEND (W3C PNG 3rd ed. §5.6: IEND
				// is unique).
				return nil
			}
			return err
		}
	}

	// The source PNG ended without an IEND chunk (truncated or malformed input).
	// W3C PNG 3rd ed. §5.6: "The IEND chunk must appear LAST. It marks the end
	// of the PNG datastream." Write a synthetic IEND so the output is always a
	// structurally complete PNG regardless of whether the source was well-formed.
	return writeChunk(w, chunkIEND, nil)
}

// xmpITXtOverhead is the fixed byte count prepended to the XMP data by
// buildXMPChunk: keyword(17) + NUL(1) + compFlag(1) + compMethod(1) +
// langNUL(1) + transKwNUL(1) = 22 bytes. The PNG chunk Length field covers
// the iTXt data payload (type + data header + XMP text), not the 4-byte chunk
// type or 4-byte CRC trailer (W3C PNG 3rd ed. §5.3).
const xmpITXtOverhead = len(xmpKeyword) + 5 // 17 + 5 = 22

// writeMetadataAfterIHDR writes the eXIf chunk (if rawEXIF is non-nil) and
// the iTXt XMP chunk (if rawXMP is non-nil) to w. Both chunks are placed
// immediately after IHDR per the PNG metadata extension specification.
//
// Size guards (W3C PNG 3rd ed. §5.3 / ISO 15948 §11.2.1): the PNG chunk
// Length field is a 31-bit unsigned integer; values >= 2^31 are forbidden.
// If either payload would exceed this limit the function returns ErrXMPTooLarge
// (for XMP) or ErrChunkTooLarge (for EXIF) without writing any bytes.
func writeMetadataAfterIHDR(w io.Writer, rawEXIF, rawXMP []byte) error {
	if rawEXIF != nil {
		// PNG §5.3: chunk Length must be ≤ 2^31−1.
		if len(rawEXIF) > math.MaxInt32 {
			return fmt.Errorf("png: EXIF payload (%d bytes) exceeds PNG chunk limit of 2^31-1: %w",
				len(rawEXIF), ErrChunkTooLarge)
		}
		if err := writeChunk(w, chunkEXIf, rawEXIF); err != nil {
			return err
		}
	}
	if rawXMP != nil {
		// W3C PNG 3rd ed. §5.3: chunk Length field is 31-bit unsigned.
		// The iTXt payload = xmpITXtOverhead + len(rawXMP); guard against overflow
		// before PutUint32 silently truncates a value ≥ 2^31.
		if len(rawXMP) > math.MaxInt32-xmpITXtOverhead {
			return fmt.Errorf("png: XMP payload (%d bytes) would produce iTXt chunk length > 2^31-1: %w",
				len(rawXMP), ErrXMPTooLarge)
		}
		xmpChunk := buildXMPChunk(rawXMP)
		if err := writeChunk(w, chunkITXt, xmpChunk); err != nil {
			return err
		}
	}
	return nil
}

// errPNGDone is a sentinel returned from the readChunk callback to signal that
// Inject has written the final chunk (IEND) and the loop should stop cleanly.
var errPNGDone = errors.New("png: inject complete")

// errDecompressBomb is returned when decompressed metadata exceeds maxZlibDecompressSize,
// indicating a decompression bomb or malformed file.
var errDecompressBomb = errors.New("png: decompressed metadata chunk exceeds size limit")

// metadataChunks is the set of PNG chunk types whose data this library
// interprets and therefore must verify for integrity. All other chunk types
// (IDAT, PLTE, IEND, ancillary chunks, …) are passed through or skipped
// without CRC computation.
//
// Verified chunks: eXIf, iTXt, tEXt, zTXt (metadata payloads the library
// parses), and IHDR (structural header whose 13-byte fields anchor the image
// geometry and are read during stream traversal).
//
// Design rationale: computing CRC-32 over IDAT pixel data (potentially many
// megabytes per frame) adds +35% latency to BenchmarkPNGExtract and provides
// zero benefit — the library never interprets IDAT bytes. CRC verification is
// a correctness guard for data the library actually uses; spending cycles on
// data it discards violates the project's ultra-performance constraint (§2).
// PNG §5.4 says decoders "should" check CRCs; it does not mandate checking
// every chunk, and display decoders (e.g. libpng) routinely skip IDAT CRC
// unless requested. The library documents this policy explicitly so callers
// understand what the error ErrChunkCRCMismatch can and cannot signal.
var metadataChunks = map[[4]byte]struct{}{ //nolint:gochecknoglobals // package-level lookup table; read-only after init
	chunkEXIf: {},
	chunkITXt: {},
	chunkTEXt: {},
	chunkZTXt: {},
	chunkIHDR: {},
}

// chunkTypeStr converts chunkType to a string for use only in the cold,
// rarely-taken error-message-construction paths of readChunk,
// readNonEmptyChunk, readLargeChunk, and verifyCRC32.
//
// This exists because Go's escape analysis is control-flow-insensitive: a
// [4]byte function PARAMETER whose address is taken anywhere in the
// function body — even inside an `if err != nil` branch that runs on a tiny
// fraction of calls — is marked as escaping for EVERY call, not just the
// branch that actually executes. Slicing chunkType directly inline
// (`chunkType[:]`) inside each function's own fmt.Errorf calls was measured
// to force that function's own chunkType copy to the heap unconditionally,
// which is worse than the single string(hdr[4:8]) allocation task #232 set
// out to remove (confirmed via `go build -gcflags="-m -m"` and a benchmark
// regression from 15 to 21 allocs/op on BenchmarkPNGExtract). Routing the
// conversion through this small, `//go:noinline` helper isolates the
// escaping copy to a call that only actually runs on the error path: passing
// chunkType BY VALUE into a call does not, by itself, force the caller's own
// copy to escape — only inlining the callee's body back into the caller
// would do that, which //go:noinline forbids.
//
//go:noinline
func chunkTypeStr(t [4]byte) string {
	return string(t[:])
}

// shouldVerifyCRC reports whether the CRC of chunkType should be checked.
// Only chunks whose data is interpreted by this library are checked; pixel-data
// and other pass-through chunks are skipped to avoid burning CPU on bytes that
// are never read by the metadata layer.
func shouldVerifyCRC(chunkType [4]byte) bool {
	_, ok := metadataChunks[chunkType]
	return ok
}

// verifyCRC32 checks the CRC-32/IEEE of (chunkType + data) against stored.
// Returns ErrChunkCRCMismatch on mismatch. chunkType is a [4]byte value (task
// #232), so no string→[]byte conversion is needed to hash it.
func verifyCRC32(chunkType [4]byte, data []byte, stored uint32) error {
	h := crc32Pool.Get().(hash.Hash32) //nolint:forcetypeassert,revive // crc32Pool.New always stores hash.Hash32; pool invariant
	h.Reset()
	_, _ = h.Write(chunkType[:])
	_, _ = h.Write(data)
	computed := h.Sum32()
	crc32Pool.Put(h)
	if computed != stored {
		return fmt.Errorf("png: chunk %q CRC mismatch (stored %08x, computed %08x): %w",
			chunkTypeStr(chunkType), stored, computed, ErrChunkCRCMismatch)
	}
	return nil
}

// largeChunkReadThreshold is the boundary above which readNonEmptyChunk
// switches from iobuf-pool allocation to an incremental read strategy.
// For length <= largeChunkReadThreshold the iobuf pool is used as normal.
// For length > largeChunkReadThreshold (but still ≤ maxPNGChunkSize) the
// declared size is large enough that pre-allocating it before seeing actual
// data would be wasteful or dangerous if the stream is adversarial: the iobuf
// pool would fall back to make([]byte, length) for any length > largeSize.
// Using io.ReadAll(io.LimitReader(r, length)) instead allocates only as much
// memory as data actually arrives, so a 200 MiB declared length in a 50-byte
// stream fails after reading ~50 bytes, not after allocating 200 MiB.
// iobuf.largeSize is 65536; mirror it here to stay in sync.
const largeChunkReadThreshold = 65536

// readNonEmptyChunk handles the length > 0 branch of readChunk: reads data,
// reads the CRC trailer, optionally verifies, then calls fn.
//
// Allocation strategy:
//   - length ≤ largeChunkReadThreshold: use iobuf pool (zero heap for small chunks).
//   - length >  largeChunkReadThreshold: delegate to readLargeChunk which reads
//     incrementally via io.ReadAll + io.LimitReader so that a crafted length
//     field in a short stream does not pre-allocate proportionally to the
//     declared length.
func readNonEmptyChunk(r io.Reader, chunkType [4]byte, length int, verifyCRC bool, fn func([4]byte, []byte) error) error {
	if length > largeChunkReadThreshold {
		return readLargeChunk(r, chunkType, length, verifyCRC, fn)
	}

	// Fast path: pool-backed buffer, no heap allocation for small chunks.
	buf := iobuf.Get(length)
	data := (*buf)[:length]
	if _, err := io.ReadFull(r, data); err != nil {
		iobuf.Put(buf)
		return fmt.Errorf("png: truncated chunk %q: %w", chunkTypeStr(chunkType), err)
	}

	// Read the 4-byte CRC trailer (PNG §5.3) — always consumed to keep the
	// stream position correct, even when CRC verification is skipped. A
	// plain stack array is used rather than an iobuf-pooled one: measurement
	// showed the sync.Pool round trip costs more in ns/op for an object this
	// small than the single unavoidable heap escape it would replace (the
	// address still crosses io.ReadFull's io.Reader interface call either
	// way — see readChunk's doc comment) — pooling only pays off for the
	// larger, size-variable buffers this function already pools (buf above).
	var crcB [4]byte
	if _, err := io.ReadFull(r, crcB[:]); err != nil {
		iobuf.Put(buf)
		return fmt.Errorf("png: read CRC for %q: %w", chunkTypeStr(chunkType), err)
	}
	stored := binary.BigEndian.Uint32(crcB[:])

	if verifyCRC {
		// Verify CRC-32/IEEE over chunk type + chunk data (PNG §5.4).
		if err := verifyCRC32(chunkType, data, stored); err != nil {
			iobuf.Put(buf)
			return err
		}
	}

	fnErr := fn(chunkType, data)
	iobuf.Put(buf)
	return fnErr
}

// readLargeChunk handles the length > largeChunkReadThreshold branch of
// readNonEmptyChunk. It uses io.ReadAll(io.LimitReader(r, length)) to read
// data incrementally: a stream with fewer bytes than length yields a short
// slice without a proportional allocation, and the truncation check returns
// an error immediately. An honest large chunk is read fully up to length bytes.
func readLargeChunk(r io.Reader, chunkType [4]byte, length int, verifyCRC bool, fn func([4]byte, []byte) error) error {
	// length is bounded by maxPNGChunkSize (256 MiB) checked in readChunk.
	// The int→int64 conversion is safe: length ≤ 256<<20 < math.MaxInt64.
	data, err := io.ReadAll(io.LimitReader(r, int64(length)))
	if err != nil {
		return fmt.Errorf("png: read chunk %q: %w", chunkTypeStr(chunkType), err)
	}
	if len(data) != length {
		return fmt.Errorf("png: truncated chunk %q: read %d of %d bytes: %w",
			chunkTypeStr(chunkType), len(data), length, io.ErrUnexpectedEOF)
	}

	// Read the 4-byte CRC trailer (PNG §5.3). Plain stack array: see the
	// identical rationale in readNonEmptyChunk above.
	var crcB [4]byte
	if _, err := io.ReadFull(r, crcB[:]); err != nil {
		return fmt.Errorf("png: read CRC for %q: %w", chunkTypeStr(chunkType), err)
	}
	stored := binary.BigEndian.Uint32(crcB[:])

	if verifyCRC {
		if err := verifyCRC32(chunkType, data, stored); err != nil {
			return err
		}
	}

	return fn(chunkType, data)
}

// writeChunk writes a PNG chunk with a correct CRC-32 checksum (PNG §5.4).
// The 8-byte header and 4-byte CRC trailer are obtained from the iobuf pool
// (task #231) instead of fresh stack arrays: both addresses are passed
// through w.Write's io.Writer interface call, which forces them to escape to
// the heap regardless of how this function itself is compiled (the same
// unavoidable-escape class documented on readChunk above) — a pooled buffer
// pays that allocation once, amortised across every chunk written, instead of
// once per call. crc32Pool.Put is called explicitly on every return path so
// the function has no defer.
func writeChunk(w io.Writer, chunkType [4]byte, data []byte) error {
	hdrPtr := iobuf.Get(8)
	hdr := *hdrPtr
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(data))) //nolint:gosec // G115: chunk data length bounded by input
	copy(hdr[4:8], chunkType[:])
	if _, err := w.Write(hdr); err != nil {
		iobuf.Put(hdrPtr)
		return fmt.Errorf("png: write chunk header: %w", err)
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			iobuf.Put(hdrPtr)
			return fmt.Errorf("png: write chunk data: %w", err)
		}
	}
	// CRC covers chunk type + chunk data (PNG §5.4). Hash the type bytes
	// already sitting in hdr[4:8] rather than re-deriving them from chunkType,
	// avoiding a second copy.
	h := crc32Pool.Get().(hash.Hash32) //nolint:forcetypeassert,revive // crc32Pool.New always stores hash.Hash32; pool invariant
	h.Reset()
	_, _ = h.Write(hdr[4:8])
	_, _ = h.Write(data)
	sum := h.Sum32()
	crc32Pool.Put(h)
	iobuf.Put(hdrPtr)

	crcPtr := iobuf.Get(4)
	crcB := *crcPtr
	binary.BigEndian.PutUint32(crcB, sum)
	_, err := w.Write(crcB)
	iobuf.Put(crcPtr)
	if err != nil {
		return fmt.Errorf("png: write chunk CRC: %w", err)
	}
	return nil
}

// extractXMPFromITXt parses an iTXt chunk and returns the XMP text if the
// keyword is "XML:com.adobe.xmp", or nil otherwise.
// Compressed iTXt payloads (compFlag != 0) are decompressed via zlib (PNG §11.3.4).
func extractXMPFromITXt(data []byte) ([]byte, error) {
	// iTXt layout: keyword\x00 compFlag(1) compMethod(1) lang\x00 transKw\x00 text
	null := bytes.IndexByte(data, 0x00)
	if null < 0 {
		return nil, nil
	}
	if string(data[:null]) != xmpKeyword {
		return nil, nil
	}
	pos := null + 1 // skip null terminator
	if pos+2 > len(data) {
		return nil, nil
	}
	compFlag := data[pos]
	compMethod := data[pos+1]
	pos += 2 // skip compFlag + compMethod

	// Skip language tag (null-terminated).
	lang := bytes.IndexByte(data[pos:], 0x00)
	if lang < 0 {
		return nil, nil
	}
	pos += lang + 1

	// Skip translated keyword (null-terminated).
	tk := bytes.IndexByte(data[pos:], 0x00)
	if tk < 0 {
		return nil, nil
	}
	pos += tk + 1

	text := data[pos:]

	if compFlag == 0 {
		// Clone: text is a subslice of a pooled buffer; caller retains the result.
		return bytes.Clone(text), nil
	}

	// Compressed iTXt: decompress using zlib (compMethod 0 = deflate, PNG §11.3.4).
	if compMethod != 0 {
		return nil, fmt.Errorf("png: compressed XMP: unsupported compression method %d: %w", compMethod, ErrUnsupportedCompression)
	}
	dec, err := zlibDecompress(text)
	if err != nil {
		return nil, fmt.Errorf("png: compressed XMP: decompression failed: %w", err)
	}
	return dec, nil
}

// extractXMPFromTExt extracts XMP from a tEXt chunk if its keyword is
// "XML:com.adobe.xmp" (legacy uncompressed form, RFC 2083 §12.13).
func extractXMPFromTExt(data []byte) []byte {
	keyword, text, found := bytes.Cut(data, []byte{0x00})
	if !found {
		return nil
	}
	if string(keyword) != xmpKeyword {
		return nil
	}
	if len(text) == 0 {
		return nil
	}
	// Clone: text is a subslice of a pooled buffer; caller retains the result.
	return bytes.Clone(text)
}

// extractXMPFromZTxt extracts and decompresses XMP from a zTXt chunk if its
// keyword is "XML:com.adobe.xmp" (legacy deflate-compressed form, PNG §11.3.3).
func extractXMPFromZTxt(data []byte) ([]byte, error) {
	null := bytes.IndexByte(data, 0x00)
	if null < 0 {
		return nil, nil
	}
	if string(data[:null]) != xmpKeyword {
		return nil, nil
	}
	pos := null + 1
	if pos >= len(data) {
		return nil, nil
	}
	compMethod := data[pos]
	pos++
	if compMethod != 0 {
		return nil, fmt.Errorf("png: zTXt XMP: unsupported compression method %d: %w", compMethod, ErrUnsupportedCompression)
	}
	dec, err := zlibDecompress(data[pos:])
	if err != nil {
		return nil, fmt.Errorf("png: zTXt XMP: decompression failed: %w", err)
	}
	return dec, nil
}

// isXMPChunk reports whether an iTXt chunk contains XMP data.
func isXMPChunk(data []byte) bool {
	return len(data) > len(xmpKeyword) &&
		bytes.HasPrefix(data, []byte(xmpKeyword)) &&
		data[len(xmpKeyword)] == 0x00
}

// buildXMPChunk constructs an iTXt chunk payload for an XMP packet.
func buildXMPChunk(xmpData []byte) []byte {
	// keyword\x00 compFlag(0) compMethod(0) lang\x00 transKw\x00 text
	// Pre-sized to its exact final length (xmpITWtOverhead + len(xmpData), the
	// same quantity writeMetadataAfterIHDR already validates against the PNG
	// chunk-length limit) instead of growing a zero-cap bytes.Buffer, which
	// would reallocate at least twice for any non-trivial payload (task #231).
	buf := make([]byte, 0, xmpITXtOverhead+len(xmpData))
	buf = append(buf, xmpKeyword...)
	// NUL(keyword) + compFlag(0=uncompressed) + compMethod(0) + NUL(lang) + NUL(transKw).
	buf = append(buf, 0x00, 0x00, 0x00, 0x00, 0x00)
	buf = append(buf, xmpData...)
	return buf
}
