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

// crc32Pool stores reusable hash.Hash32 values (crc32.NewIEEE return type).
// Reusing them via Reset avoids a small allocation on every writeChunk call.
var crc32Pool = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
	New: func() any { return crc32.NewIEEE() },
}

// zlibDecompress decompresses a zlib-deflated payload. It gets a reader from
// zlibPool (or allocates one) and returns it to the pool when done without
// closing it, so the next caller can Reset it instead of allocating again.
func zlibDecompress(data []byte) ([]byte, error) {
	r := bytes.NewReader(data)
	var zr io.ReadCloser
	if v := zlibPool.Get(); v != nil {
		zr = v.(io.ReadCloser)                                   //nolint:forcetypeassert,revive // zlibPool.New always stores io.ReadCloser; pool invariant
		if err := zr.(zlib.Resetter).Reset(r, nil); err != nil { //nolint:forcetypeassert // zlib.NewReader always implements zlib.Resetter; Go stdlib guarantee
			return nil, fmt.Errorf("png: zlib reset: %w", err)
		}
	} else {
		var err error
		zr, err = zlib.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("png: zlib open: %w", err)
		}
	}
	// Return to pool without closing so it can be Reset on next use.
	defer zlibPool.Put(zr)
	// Cap decompressed output to prevent decompression bombs: a crafted zlib
	// stream with a tiny compressed payload can expand to gigabytes of output.
	// Reading one byte beyond the limit lets us detect overflow without reading it all.
	data, err := io.ReadAll(io.LimitReader(zr, maxZlibDecompressSize+1))
	if err != nil {
		return nil, fmt.Errorf("png: zlib decompress: %w", err)
	}
	if int64(len(data)) > maxZlibDecompressSize {
		return nil, errDecompressBomb
	}
	return data, nil
}

// processExtractChunk dispatches a single PNG chunk during Extract, updating
// rawEXIF and rawXMP as appropriate. It returns (rawEXIF, rawXMP, done, err)
// where done is true when IEND signals the end of the chunk stream.
// data is backed by a pooled buffer; any slice that must outlive this call is
// cloned here or in its callees before being returned as rawEXIF/rawXMP.
func processExtractChunk(chunkType string, data, rawEXIF, rawXMP []byte) ([]byte, []byte, bool, error) {
	switch chunkType {
	case "eXIf":
		// Clone: data is pooled and will be returned to the pool after this call.
		return bytes.Clone(data), rawXMP, false, nil
	case "iTXt", "tEXt", "zTXt":
		xmp, err := handleXMPChunk(chunkType, data, rawXMP)
		if err != nil {
			return rawEXIF, rawXMP, false, err
		}
		return rawEXIF, xmp, false, nil
	case "IEND":
		return rawEXIF, rawXMP, true, nil
	}
	return rawEXIF, rawXMP, false, nil
}

// Extract reads the PNG chunk stream from r and returns raw metadata payloads.
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

	var done bool
	for !done {
		rerr := readChunk(r, func(chunkType string, data []byte) error {
			rawEXIF, rawXMP, done, err = processExtractChunk(chunkType, data, rawEXIF, rawXMP)
			return err
		})
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return nil, nil, nil, rerr
		}
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return rawEXIF, nil, rawXMP, nil
}

// handleITXtXMP extracts XMP from an iTXt chunk. Returns (xmp, nil) on
// success, (nil, nil) when the chunk is not an XMP iTXt, or (nil, err).
func handleITXtXMP(data []byte) ([]byte, error) {
	return extractXMPFromITXt(data)
}

// handleLegacyXMP extracts XMP from a tEXt or zTXt chunk only when existing
// is nil (legacy chunks do not override a higher-priority iTXt source).
func handleLegacyXMP(chunkType string, data []byte) ([]byte, error) {
	if chunkType == "zTXt" {
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
func handleXMPChunk(chunkType string, data []byte, existing []byte) ([]byte, error) {
	// XMP Part 3 §1.6 (PNG-04): use the first XMP chunk encountered; never
	// overwrite an already-found XMP with a later chunk of any type.
	if existing != nil {
		return existing, nil
	}
	switch chunkType {
	case "iTXt":
		xmp, err := handleITXtXMP(data)
		if err != nil {
			return nil, err
		}
		if xmp != nil {
			return xmp, nil
		}
	case "tEXt", "zTXt":
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
func shouldDropChunk(chunkType string, data []byte) bool {
	if chunkType == "eXIf" {
		return true
	}
	return chunkType == "iTXt" && isXMPChunk(data)
}

// writeInjectChunk writes chunkType/data to w and, if chunkType is "IHDR",
// immediately writes the new metadata chunks. It returns (done=true) when
// chunkType is "IEND". This helper extracts the per-chunk logic from Inject,
// reducing that function's cyclomatic complexity.
func writeInjectChunk(w io.Writer, chunkType string, data, rawEXIF, rawXMP []byte) (bool, error) {
	if err := writeChunk(w, chunkType, data); err != nil {
		return false, err
	}
	if chunkType == "IHDR" {
		if err := writeMetadataAfterIHDR(w, rawEXIF, rawXMP); err != nil {
			return false, err
		}
	}
	return chunkType == "IEND", nil
}

// injectChunk is the per-chunk callback used by Inject's readChunk loop.
// It skips replacement targets, writes surviving chunks, and returns errPNGDone
// when IEND has been written so the caller can break cleanly.
func injectChunk(w io.Writer, chunkType string, data, rawEXIF, rawXMP []byte) error {
	if shouldDropChunk(chunkType, data) {
		return nil
	}
	done, err := writeInjectChunk(w, chunkType, data, rawEXIF, rawXMP)
	if err != nil {
		return err
	}
	if done {
		return errPNGDone
	}
	return nil
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

	// Input is a valid PNG; write the signature to w.
	if _, err := w.Write(pngSig[:]); err != nil {
		return fmt.Errorf("png: write signature: %w", err)
	}

	for {
		err := readChunk(r, func(chunkType string, data []byte) error {
			return injectChunk(w, chunkType, data, rawEXIF, rawXMP)
		})
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, errPNGDone) {
				// Source PNG had an IEND chunk; it has already been written by
				// injectChunk → writeInjectChunk. Return immediately so we do not
				// write a second IEND (W3C PNG 3rd ed. §5.6: IEND is unique).
				return nil
			}
			return err
		}
	}

	// The source PNG ended without an IEND chunk (truncated or malformed input).
	// W3C PNG 3rd ed. §5.6: "The IEND chunk must appear LAST. It marks the end
	// of the PNG datastream." Write a synthetic IEND so the output is always a
	// structurally complete PNG regardless of whether the source was well-formed.
	return writeChunk(w, "IEND", nil)
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
		if err := writeChunk(w, "eXIf", rawEXIF); err != nil {
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
		if err := writeChunk(w, "iTXt", xmpChunk); err != nil {
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
var metadataChunks = map[string]struct{}{ //nolint:gochecknoglobals // package-level lookup table; read-only after init
	"eXIf": {},
	"iTXt": {},
	"tEXt": {},
	"zTXt": {},
	"IHDR": {},
}

// shouldVerifyCRC reports whether the CRC of chunkType should be checked.
// Only chunks whose data is interpreted by this library are checked; pixel-data
// and other pass-through chunks are skipped to avoid burning CPU on bytes that
// are never read by the metadata layer.
func shouldVerifyCRC(chunkType string) bool {
	_, ok := metadataChunks[chunkType]
	return ok
}

// verifyCRC32 checks the CRC-32/IEEE of (typeBytes + data) against stored.
// typeBytes must be the 4-byte chunk-type field already in a caller-owned
// stack buffer (avoids an allocation for the string→[]byte conversion).
// Returns ErrChunkCRCMismatch on mismatch.
func verifyCRC32(chunkType string, typeBytes, data []byte, stored uint32) error {
	h := crc32Pool.Get().(hash.Hash32) //nolint:forcetypeassert,revive // crc32Pool.New always stores hash.Hash32; pool invariant
	h.Reset()
	_, _ = h.Write(typeBytes) // chunk type (4 bytes from caller's stack buffer)
	_, _ = h.Write(data)
	computed := h.Sum32()
	crc32Pool.Put(h)
	if computed != stored {
		return fmt.Errorf("png: chunk %q CRC mismatch (stored %08x, computed %08x): %w",
			chunkType, stored, computed, ErrChunkCRCMismatch)
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
func readNonEmptyChunk(r io.Reader, hdr []byte, chunkType string, length int, verifyCRC bool, fn func(string, []byte) error) error {
	if length > largeChunkReadThreshold {
		return readLargeChunk(r, hdr, chunkType, length, verifyCRC, fn)
	}

	// Fast path: pool-backed buffer, no heap allocation for small chunks.
	buf := iobuf.Get(length)
	data := (*buf)[:length]
	if _, err := io.ReadFull(r, data); err != nil {
		iobuf.Put(buf)
		return fmt.Errorf("png: truncated chunk %q: %w", chunkType, err)
	}

	// Read the 4-byte CRC trailer (PNG §5.3) — always consumed to keep the
	// stream position correct, even when CRC verification is skipped.
	var crcB [4]byte
	if _, err := io.ReadFull(r, crcB[:]); err != nil {
		iobuf.Put(buf)
		return fmt.Errorf("png: read CRC for %q: %w", chunkType, err)
	}

	if verifyCRC {
		// Verify CRC-32/IEEE over chunk type + chunk data (PNG §5.4).
		if err := verifyCRC32(chunkType, hdr[4:8], data, binary.BigEndian.Uint32(crcB[:])); err != nil {
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
func readLargeChunk(r io.Reader, hdr []byte, chunkType string, length int, verifyCRC bool, fn func(string, []byte) error) error {
	// length is bounded by maxPNGChunkSize (256 MiB) checked in readChunk.
	// The int→int64 conversion is safe: length ≤ 256<<20 < math.MaxInt64.
	data, err := io.ReadAll(io.LimitReader(r, int64(length)))
	if err != nil {
		return fmt.Errorf("png: read chunk %q: %w", chunkType, err)
	}
	if len(data) != length {
		return fmt.Errorf("png: truncated chunk %q: read %d of %d bytes: %w",
			chunkType, len(data), length, io.ErrUnexpectedEOF)
	}

	// Read the 4-byte CRC trailer (PNG §5.3).
	var crcB [4]byte
	if _, err := io.ReadFull(r, crcB[:]); err != nil {
		return fmt.Errorf("png: read CRC for %q: %w", chunkType, err)
	}

	if verifyCRC {
		if err := verifyCRC32(chunkType, hdr[4:8], data, binary.BigEndian.Uint32(crcB[:])); err != nil {
			return err
		}
	}

	return fn(chunkType, data)
}

// readChunk reads one PNG chunk and calls fn(chunkType, data) with a slice
// backed by a pooled buffer. fn must not retain data after returning; call
// bytes.Clone inside fn if the data must outlive the call.
//
// CRC verification policy: CRC-32/IEEE (PNG §5.3/§5.4) is verified only for
// metadata chunks that this library interprets: eXIf, iTXt, tEXt, zTXt, and
// IHDR. Pixel-data chunks (IDAT) and all other pass-through chunks are read
// and forwarded without CRC computation. This is intentional — computing CRC
// over IDAT adds +35% latency with no correctness benefit for a metadata-only
// library. ErrChunkCRCMismatch therefore signals corruption in metadata, not
// in pixel data. Callers needing full-stream integrity must use a separate
// tool (e.g. a display decoder with CRC checking enabled).
func readChunk(r io.Reader, fn func(chunkType string, data []byte) error) error {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return fmt.Errorf("png: read chunk header: %w", err)
	}
	chunkType := string(hdr[4:8])

	// Read the raw uint32 length BEFORE converting to int. On a 32-bit platform
	// (GOARCH=386/arm, int=32 bits), a chunk length >= 2^31 would become negative
	// after int(uint32), silently passing the maxPNGChunkSize guard and causing
	// the chunk to be processed as zero-length — wrong behaviour. Checking the
	// uint32 value against math.MaxInt32 first ensures correctness on all platform
	// widths. PNG spec ISO 15948 §11.2.1 caps chunk length at 2^31−1 anyway (task #74).
	rawLen := binary.BigEndian.Uint32(hdr[:4])
	if rawLen > math.MaxInt32 {
		return fmt.Errorf("png: chunk %q length %d exceeds 2^31-1: %w", chunkType, rawLen, ErrChunkTooLarge)
	}
	length := int(rawLen)

	// Guard against adversarial or malformed chunk lengths before any allocation.
	// PNG spec ISO 15948 §11.2.1 permits up to 2^31−1 bytes, but that is
	// pathological for metadata; enforce a tighter application-level limit here.
	if length > maxPNGChunkSize {
		return fmt.Errorf("png: chunk %q length %d exceeds limit: %w", chunkType, length, ErrChunkTooLarge)
	}

	verifyCRC := shouldVerifyCRC(chunkType)

	if length > 0 {
		return readNonEmptyChunk(r, hdr[:], chunkType, length, verifyCRC, fn)
	}

	// Zero-length chunk: read the 4-byte CRC trailer to advance the stream,
	// then verify only for metadata chunk types.
	var crcB [4]byte
	if _, err := io.ReadFull(r, crcB[:]); err != nil {
		return fmt.Errorf("png: read CRC for %q: %w", chunkType, err)
	}

	if verifyCRC {
		// PNG §5.4: CRC covers chunk type + data; for zero-length chunks, data is empty.
		if err := verifyCRC32(chunkType, hdr[4:8], nil, binary.BigEndian.Uint32(crcB[:])); err != nil {
			return err
		}
	}

	return fn(chunkType, nil)
}

// writeChunk writes a PNG chunk with a correct CRC-32 checksum (PNG §5.4).
func writeChunk(w io.Writer, chunkType string, data []byte) error {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(data))) //nolint:gosec // G115: chunk data length bounded by input
	copy(hdr[4:8], chunkType)
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("png: write chunk header: %w", err)
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("png: write chunk data: %w", err)
		}
	}
	// CRC covers chunk type + chunk data (PNG §5.4).
	h := crc32Pool.Get().(hash.Hash32) //nolint:forcetypeassert,revive // crc32Pool.New always stores hash.Hash32; pool invariant
	h.Reset()
	defer crc32Pool.Put(h)
	_, _ = h.Write([]byte(chunkType))
	_, _ = h.Write(data)
	var crcB [4]byte
	binary.BigEndian.PutUint32(crcB[:], h.Sum32())
	if _, err := w.Write(crcB[:]); err != nil {
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
	var buf bytes.Buffer
	buf.WriteString(xmpKeyword)
	buf.WriteByte(0x00) // null terminator for keyword
	buf.WriteByte(0x00) // compression flag: not compressed
	buf.WriteByte(0x00) // compression method
	buf.WriteByte(0x00) // empty language tag
	buf.WriteByte(0x00) // empty translated keyword
	buf.Write(xmpData)
	return buf.Bytes()
}
