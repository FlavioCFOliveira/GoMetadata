// Package tiff implements extraction and injection of metadata within TIFF
// container files. TIFF stores EXIF in a SubIFD (tag 0x8769), IPTC in tag
// 0x83BB, and XMP in tag 0x02BC (TIFF Technical Note 3).
package tiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
	"github.com/FlavioCFOliveira/GoMetadata/internal/irb"
	"github.com/FlavioCFOliveira/GoMetadata/internal/tiffscan"
)

// xmpWireFrameMagic is the 8-byte sentinel that identifies a JPEG extended-XMP
// wire-frame payload produced by jpeg.ExtractWithWire. This internal encoding
// MUST NOT be passed to any TIFF Inject entry point: it is only understood by
// jpeg.Inject and would produce a corrupt tag 0x02BC if written verbatim.
//
// Task #70 regression: JPEG extended-XMP wire-frame leak to non-JPEG containers.
// Task #118 regression: TIFF inject paths were missing this guard (PNG/WebP/HEIF had it).
//
// Value: 0x00 'X' 'M' 'P' 'E' 'X' 'T' 0x00 — same as jpeg.xmpWireMagic.
// xmpWireFrameMagic mirrors the constant in format/jpeg, format/png, format/webp, and format/heif.
var xmpWireFrameMagic = [8]byte{0x00, 'X', 'M', 'P', 'E', 'X', 'T', 0x00} //nolint:gochecknoglobals // package-level constant bytes; never mutated

// rejectWireFrameXMP returns ErrCorruptXMP when rawXMP begins with the JPEG
// extended-XMP wire-frame magic (xmpWireFrameMagic). All TIFF Inject entry
// points call this guard before proceeding.
//
// Task #118 regression: defence-in-depth mirror of the identical guard in
// png.Inject, webp.Inject, and heif.Inject (task #70).
func rejectWireFrameXMP(rawXMP []byte) error {
	if len(rawXMP) >= 8 && [8]byte(rawXMP[:8]) == xmpWireFrameMagic {
		return fmt.Errorf("tiff: rawXMP contains an internal JPEG wire-frame encoding that cannot be stored in a TIFF container: %w", ErrCorruptXMP)
	}
	return nil
}

// readInput reads the whole of r from its current position. The read is
// capped at maxFileSize bytes (#140) so that an oversized or infinite
// reader cannot trigger unbounded heap allocation; ErrFileTooLarge is
// returned before any parsing takes place. A seekable reader is read with a
// single allocation of its exact size (iobuf.ReadAll).
func readInput(r io.ReadSeeker) ([]byte, error) {
	data, err := iobuf.ReadAll(r, maxFileSize)
	if err != nil {
		if errors.Is(err, iobuf.ErrTooLarge) {
			return nil, fmt.Errorf("tiff: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
		}
		return nil, fmt.Errorf("tiff: read: %w", err)
	}
	return data, nil
}

// seekFileSize returns r's total length via Seek(0, io.SeekEnd), restoring
// the current position (assumed 0) before returning, or (-1, nil) if r does
// not support seeking to its end. -1 tells Extract to fall back to
// extractWholeFile, so correctness never depends on Seek(SeekEnd) support.
func seekFileSize(r io.ReadSeeker) (int64, error) {
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return -1, nil //nolint:nilerr // non-seekable-to-end reader: disable the prefix fast path, not fatal
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("tiff: seek: %w", err)
	}
	return end, nil
}

// readInitialPrefix reads min(fileSize, extentInitialPrefixSize) bytes from
// r's current position (assumed 0). fileSize is only used to avoid
// allocating more than the file actually contains; a short read for any
// other reason is a genuine I/O error.
func readInitialPrefix(r io.Reader, fileSize int64) ([]byte, error) {
	buf := make([]byte, min(fileSize, extentInitialPrefixSize))
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("tiff: read: %w", err)
	}
	return buf, nil
}

// extractWholeFile is Extract's pre-#289 behaviour: read the entire file and
// scan IFD0 directly. Used only when r cannot report its own size (so the
// prefix/growth machinery in scanMetadataExtent has no reliable maxFileSize
// bound to grow toward) — correctness must never depend on Seek(SeekEnd)
// support, only performance does — or when fileSize is small enough that the
// prefix scanner's own overhead costs more than it saves (Extract's
// smallFileWholeReadThreshold check). acceptMagic: see ExtractWithMagic.
func extractWholeFile(r io.ReadSeeker, acceptMagic uint16) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	data, err := readInput(r)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(data) < 8 {
		return nil, nil, nil, ErrFileTooShort
	}
	order, err := byteOrder(data)
	if err != nil {
		return nil, nil, nil, err
	}
	magic := order.Uint16(data[2:])
	switch {
	case magic == 0x002A || (acceptMagic != 0 && magic == acceptMagic):
		ifd0Off := order.Uint32(data[4:])
		rawIPTC, rawXMP = extractTagValues(data, ifd0Off, order)
		return data, rawIPTC, rawXMP, nil
	case magic == 0x002B:
		return extractBigTIFF(data, order)
	default:
		return nil, nil, nil, fmt.Errorf("tiff: unsupported magic 0x%04X (expected 0x002A classic TIFF or 0x002B BigTIFF): %w",
			magic, ErrUnsupportedMagic)
	}
}

// Extract reads metadata payloads from a TIFF container.
//
// #289: rawEXIF is the metadata PREFIX of the TIFF byte stream — every IFD in
// IFD0's next-IFD chain, ExifIFD, GPSIFD, InteropIFD, and SubIFDs, and every
// out-of-line tag value within them (see extent.go) — not the whole file.
// Strip/tile image data and embedded thumbnails/previews are excluded; a
// caller that needs the original file bytes verbatim (e.g. to re-embed a
// thumbnail this package never reads) must keep its own copy of the source.
// rawIPTC and rawXMP are read from the respective IFD0 tags, which are
// always within the returned prefix.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	return extractTIFFWithMagic(r, 0)
}

// ExtractWithMagic is Extract, but additionally treats magic value
// acceptMagic (when non-zero) at bytes[2:4] as equivalent to classic TIFF's
// 0x002A — mirroring exif.AcceptRAWMagic (exif/exif.go).
//
// #293: format/raw/orf and format/raw/rw2 are structurally classic TIFF —
// only bytes[2:4] (the manufacturer's own magic: Olympus ORF "RO"/0x4F52 or
// "RS"/0x5352; Panasonic RW2 "U\x00"/0x0055) differ from TIFF 6.0 §2's
// 0x002A — so they call this instead of Extract to get the SAME
// metadata-prefix scanning (#289) TIFF/CR2/NEF/ARW/DNG already use, instead
// of reading the whole file.
func ExtractWithMagic(r io.ReadSeeker, acceptMagic uint16) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	return extractTIFFWithMagic(r, acceptMagic)
}

func extractTIFFWithMagic(r io.ReadSeeker, acceptMagic uint16) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("tiff: seek: %w", err)
	}

	fileSize, sizeErr := seekFileSize(r)
	if sizeErr != nil {
		return nil, nil, nil, sizeErr
	}
	if fileSize < 0 {
		return extractWholeFile(r, acceptMagic)
	}
	if fileSize > maxFileSize {
		return nil, nil, nil, fmt.Errorf("tiff: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}
	if fileSize <= smallFileWholeReadThreshold {
		// #289 follow-up: the prefix optimization exists to avoid reading
		// tens of megabytes of strip/tile image data a real camera RAW file
		// spends the overwhelming majority of its bytes on. Below this
		// threshold, reading the WHOLE file is already cheap in absolute
		// terms, and scanMetadataExtent's own incremental-growth machinery
		// (see extent.go) costs MORE than it saves for files this size: a
		// corpus-wide per-file Read benchmark (500 real TIFF/CR2/NEF/ARW/DNG
		// files) found every file whose extent scan cost more than a plain
		// whole-file read bounded at ~2.6 MiB; real camera RAW files, tens
		// of MB, are unaffected either way and keep the >90% Read cost
		// reduction the scanner exists for. Reusing extractWholeFile here
		// matches HEAD's exact Read cost for every such file instead of
		// merely bounding the scanner's worst case. See
		// smallFileWholeReadThreshold's own doc comment (extent.go) for the
		// exact threshold and its headroom above that measured bound.
		return extractWholeFile(r, acceptMagic)
	}
	return extractPrefixed(r, fileSize, acceptMagic)
}

// extractPrefixed implements Extract's prefix-scanning path (files above
// smallFileWholeReadThreshold): read the initial chunk, determine byte order
// and container variant, and dispatch to the matching prefix scanner. Split
// out of Extract to keep its cyclomatic complexity within the project's
// gocyclo threshold.
func extractPrefixed(r io.ReadSeeker, fileSize int64, acceptMagic uint16) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	initial, err := readInitialPrefix(r, fileSize)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(initial) < 8 {
		return nil, nil, nil, ErrFileTooShort
	}

	order, err := byteOrder(initial)
	if err != nil {
		return nil, nil, nil, err
	}

	// TIFF 6.0 §2: magic number is 42 (0x002A) for classic TIFF.
	// BigTIFF spec (Aware Systems / libtiff) §2: magic 43 (0x002B) for BigTIFF,
	// which uses a 16-byte header with 8-byte IFD offsets.
	magic := order.Uint16(initial[2:])
	switch {
	case magic == 0x002A || (acceptMagic != 0 && magic == acceptMagic):
		return extractClassicPrefix(r, initial, order, uint64(fileSize)) //nolint:gosec // G115: Extract validates 0 <= fileSize <= maxFileSize before calling extractPrefixed
	case magic == 0x002B:
		return extractBigTIFFPrefix(r, initial, order, uint64(fileSize)) //nolint:gosec // G115: same rationale
	default:
		return nil, nil, nil, fmt.Errorf("tiff: unsupported magic 0x%04X (expected 0x002A classic TIFF or 0x002B BigTIFF): %w",
			magic, ErrUnsupportedMagic)
	}
}

// extractClassicPrefix implements Extract's classic-TIFF (magic 0x002A)
// branch: 8-byte header, 32-bit IFD offsets, 12-byte entries. fileSize is the
// total size of the source file (already known from Extract's own
// seekFileSize call), passed through so scanMetadataExtent can avoid a
// wasteful trailing growth pass when only a small remainder of the file is
// left unread (see scanMetadataExtent's doc comment).
func extractClassicPrefix(r io.ReadSeeker, initial []byte, order binary.ByteOrder, fileSize uint64) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	ifd0Off := order.Uint32(initial[4:])
	buf, serr := scanMetadataExtent(r, initial, uint64(ifd0Off), order, false, fileSize)
	if serr != nil {
		return nil, nil, nil, serr
	}
	rawIPTC, rawXMP = extractTagValues(buf, ifd0Off, order)
	return buf, rawIPTC, rawXMP, nil
}

// extractBigTIFFPrefix implements Extract's BigTIFF (magic 0x002B) branch:
// 16-byte header, 64-bit IFD offsets, 20-byte entries. BigTIFF spec §2:
// bytes [4:6] = offset bytesize (must be 8), bytes [6:8] = constant 0,
// bytes [8:16] = IFD0 offset (uint64). fileSize: see extractClassicPrefix.
func extractBigTIFFPrefix(r io.ReadSeeker, initial []byte, order binary.ByteOrder, fileSize uint64) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if len(initial) < bigTIFFMinHeaderLen {
		return nil, nil, nil, ErrFileTooShort
	}
	offsetBytesize := order.Uint16(initial[4:])
	if offsetBytesize != bigTIFFOffsetBytesize {
		return nil, nil, nil, fmt.Errorf("tiff: BigTIFF offset bytesize = %d, must be 8: %w",
			offsetBytesize, ErrUnsupportedMagic)
	}
	ifd0Off := order.Uint64(initial[8:])
	buf, serr := scanMetadataExtent(r, initial, ifd0Off, order, true, fileSize)
	if serr != nil {
		return nil, nil, nil, serr
	}
	rawIPTC, rawXMP = extractTagValuesBigTIFF(buf, ifd0Off, order)
	return buf, rawIPTC, rawXMP, nil
}

// Inject writes a modified TIFF stream to w, replacing the metadata tags.
//
// When rawIPTC or rawXMP is non-nil, Inject uses the copy-and-relocate path
// (relocateTIFF) to rebuild the IFD chain, upsert the new metadata payloads,
// and append every image-data block (strips, tiles, main-image JPEG) at a
// fresh absolute offset — preserving the pixel data byte-identically.
//
// When both rawIPTC and rawXMP are nil, the base bytes are written verbatim
// (pass-through path).
//
// Nil-means-delete semantics (footgun warning — task #149 regression):
//
//	A nil rawIPTC value means "remove the IPTC tag from the output". When
//	only XMP is being updated (rawXMP != nil, rawIPTC == nil) and the source
//	TIFF carries existing IPTC data, that IPTC data will be silently dropped
//	from the output. To preserve existing IPTC, pass the original rawIPTC
//	bytes from a prior tiff.Extract call. The top-level gometadata.Write path
//	handles this automatically by passing m.rawIPTC through encodeIPTC.
//
// Round-trip fidelity:
//   - All IFD entries with known TIFF type codes are faithfully preserved.
//   - Image-data blocks (StripOffsets, TileOffsets, JPEGInterchangeFormat for
//     non-thumbnail IFDs) are copied verbatim from the source and their offset
//     entries are patched to the new positions.
//   - SubIFDs (tag 0x014A) are recursively followed; their image blocks are
//     enumerated and relocated alongside the SubIFD structure (task #94).
//     This enables correct DNG write support (multi-SubIFD and tiled DNG).
//   - IFD1 JPEG thumbnails are handled by exif.Encode's patchThumbnailEntries.
//   - MakerNote blobs are copied verbatim (see relocate.go for safety note).
//   - Unknown-type IFD entries retain their 4-byte field; out-of-line data
//     referenced by unknown types is not copied (see exif.Encode docs).
//
// If exif.Parse fails, Inject returns the parse error rather than silently
// discarding the requested metadata.
//
// Note: the rawEXIF parameter is used as the base TIFF bytes for relocation.
// When rawEXIF was produced by exif.Encode (an IFD skeleton without image
// blocks), image block enumeration will fail with ErrBlockOutOfBounds. Callers
// that hold the original TIFF bytes AND a modified *exif.EXIF struct (e.g. the
// gometadata.Write path) must use InjectWithEXIF (or its streaming variant
// InjectWithEXIFStream) instead.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, _ bool) error { //nolint:cyclop // inherent nil-dispatch logic
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads before
	// proceeding. The wire-frame encoding (magic 0x00XMPEXT\x00) is specific to
	// JPEG APP1 and cannot be stored in TIFF tag 0x02BC. Writing it verbatim
	// produces a corrupt XMP blob. Mirror of the identical guard in png.Inject,
	// webp.Inject, and heif.Inject (task #70).
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("tiff: seek: %w", err)
	}

	// Determine the base TIFF data to work with.
	var base []byte
	if rawEXIF != nil {
		base = rawEXIF
	} else {
		var err error
		base, err = readInput(r)
		if err != nil {
			return err
		}
	}

	// Pass-through: no metadata changes requested.
	// #190 fix: treat zero-length slices the same as nil — an empty rawIPTC or
	// rawXMP must not trigger the copy-and-relocate path or write a zero-length
	// tag. encodeIPTC normalises empty→nil, but guard here for any direct callers.
	if len(rawIPTC) == 0 && len(rawXMP) == 0 {
		if _, err := w.Write(base); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}

	// Copy-and-relocate path: rebuild IFD structure with upserted metadata and
	// appended image-data blocks at corrected offsets (epic #33, tasks #92/#93).
	//
	// Task #149 regression: nil rawIPTC causes the 0x83BB IPTC tag to be omitted
	// from the rebuilt IFD (upsertIFD0Entry is only called when the value is
	// non-nil). Callers that only want to update XMP must pass the original
	// rawIPTC (from tiff.Extract) to avoid silently deleting IPTC.
	//
	// #291: base is always the WHOLE file here (Inject's own contract: base is
	// either the caller-supplied rawEXIF or a full readInput(r) — see above),
	// so writeRelocated's wholeFile fast path (slice directly from base, no
	// r.Seek/Read round trip) always applies.
	header, blocks, err := relocateTIFF(base, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if err := writeRelocated(r, w, header, blocks, base, true); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// InjectWithEXIF writes a modified TIFF stream to w using the ORIGINAL TIFF
// bytes for image-data relocation and a pre-built (already-mutated) *exif.EXIF
// struct for the IFD content.
//
// InjectWithEXIF is a whole-file convenience wrapper preserved for backward
// compatibility (this exact signature shipped in v1.3.0): it is equivalent to
//
//	InjectWithEXIFStream(bytes.NewReader(originalBytes), originalBytes, true,
//		modifiedEXIF, rawIPTC, rawXMP, w)
//
// Callers that read from a seekable source (an *os.File, for example) and
// want to avoid buffering the whole file — including its image data — in
// memory should call InjectWithEXIFStream directly; see its own doc comment
// for the streaming contract introduced in task #291.
//
// InjectWithEXIF avoids ErrBlockOutOfBounds by separating concerns:
//   - originalBytes: the ORIGINAL TIFF file bytes (all image blocks at
//     original absolute offsets). Used only as the source for copying image
//     data.
//   - modifiedEXIF: the *exif.EXIF struct produced by exif.Parse(originalBytes)
//     and subsequently mutated by the caller (SetCopyright, SetGPS, etc.).
//     Its IFDs carry both the edited metadata AND the original image-block offsets
//     (StripOffsets/TileOffsets still point at originalBytes positions).
//   - rawIPTC, rawXMP: freshly encoded IPTC/XMP payloads to upsert into IFD0
//     (may be nil if unchanged).
//
// If modifiedEXIF is nil, InjectWithEXIF falls back to parsing originalBytes
// (same behaviour as Inject).
//
// fix(tiff): task #97 — real-file TIFF/DNG write produced ErrBlockOutOfBounds
// because encodeEXIF fed an IFD skeleton as the relocate base.
func InjectWithEXIF(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	return InjectWithEXIFStream(bytes.NewReader(originalBytes), originalBytes, true, modifiedEXIF, rawIPTC, rawXMP, w)
}

// InjectWithEXIFStream is InjectWithEXIF's streaming variant: it writes a
// modified TIFF stream to w using the ORIGINAL TIFF bytes (in full, or as a
// metadata prefix accompanied by the seekable source they were read from) for
// image-data relocation, and a pre-built (already-mutated) *exif.EXIF struct
// for the IFD content.
//
// This is the correct entry point for the gometadata.Write path on TIFF-based
// containers. The standard Inject function receives rawEXIF from encodeEXIF,
// which calls exif.Encode and produces an IFD skeleton that lacks image blocks.
// Feeding that skeleton to the relocator causes ErrBlockOutOfBounds because the
// skeleton is shorter than the original strip/tile offsets stored in the IFD.
//
// InjectWithEXIFStream avoids this by separating concerns:
//   - prefix: the original file's bytes — either the ENTIRE file (wholeFile
//     true) or just its METADATA PREFIX (wholeFile false; #289/#293's extent
//     scanner). Sufficient either way to parse/enumerate every IFD, SubIFD,
//     and out-of-line offset/bytecount ARRAY (see relocateTIFFFromParsed's own
//     doc comment) — never the image pixel bytes those arrays point to.
//   - r: the original source, used to stream image-block bytes when prefix is
//     only a metadata prefix (#291). Ignored when wholeFile is true (may be
//     nil in that case); must be seekable and remain valid for the call's
//     duration otherwise.
//   - modifiedEXIF: the *exif.EXIF struct produced by exif.Parse(prefix, ...)
//     and subsequently mutated by the caller (SetCopyright, SetGPS, etc.).
//     Its IFDs carry both the edited metadata AND the original image-block offsets
//     (StripOffsets/TileOffsets still point at ORIGINAL absolute positions).
//   - rawIPTC, rawXMP: freshly encoded IPTC/XMP payloads to upsert into IFD0
//     (may be nil if unchanged).
//
// If modifiedEXIF is nil, InjectWithEXIFStream falls back to parsing prefix
// (same behaviour as Inject); this requires wholeFile to be true, since a
// metadata prefix alone cannot be re-parsed as a self-contained TIFF stream
// by Parse's generic image-block bounds checks.
//
// fix(tiff): task #97 — real-file TIFF/DNG write produced ErrBlockOutOfBounds
// because encodeEXIF fed an IFD skeleton as the relocate base.
// #291: image-data blocks are now streamed from r instead of being buffered
// in prefix, when prefix is only a metadata prefix.
func InjectWithEXIFStream(r io.ReadSeeker, prefix []byte, wholeFile bool, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		return writePassThrough(r, w, prefix, wholeFile)
	}

	fileLen, err := resolveFileLen(r, prefix, wholeFile)
	if err != nil {
		return err
	}
	header, blocks, err := relocateTIFFFromParsed(prefix, modifiedEXIF, rawIPTC, rawXMP, fileLen)
	if err != nil {
		return err
	}
	if err := writeRelocated(r, w, header, blocks, prefix, wholeFile); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// InjectWithEXIFCR2 is the CR2-specific variant of InjectWithEXIF.
//
// Canon CR2 files carry a proprietary 4-byte marker immediately after the
// standard 8-byte TIFF header, at bytes 8–11:
//
//	bytes 8–9:   0x43 0x52 ('C','R') — Canon CR2 signature
//	bytes 10–11: 0x02 0x00           — CR2 version (2.0)
//
// This is followed by IFD0, which therefore begins at offset 16 (not 8) in
// a well-formed CR2 file (Canon CR2 spec §3.1).
//
// The standard relocateTIFFFromParsed rebuilds the TIFF via exif.Encode, which
// always produces a standard 8-byte header with IFD0 at offset 8. This places
// IFD0 directly at the CR marker position, making the output unparseable: the
// first 2 bytes of IFD0 (the entry count) are interpreted as 0x4352 = 17235
// in big-endian or 0x5243 = 21059 in little-endian, causing exif.Parse to
// report "IFD at offset 8 could not be parsed".
//
// The fix inserts 8 bytes at position 8 (4 CR-marker bytes + 4 zero-pad bytes)
// and rebases all IFD absolute offsets by +8, so the standard 8-byte TIFF
// header is extended to a 16-byte CR2 header and IFD0 is at offset 16.
// This mirrors the insertRW2GUIDAndShiftOffsets approach (inserting 16 bytes
// for the RW2 GUID at position 8), but with delta=8 for the CR2 marker.
//
// Canon CR2 spec §3.1: bytes 8–9 identify the file as CR2; bytes 10–11 carry
// the version. IFD0 starts at offset 16. These bytes MUST be preserved
// verbatim on every write cycle so that standard CR2 readers and ExifTool
// continue to recognise the format.
//
// containers.md §8(e): "CR2: preserve CR 02 00 at offset 8; IFD0 at offset 16."
//
// InjectWithEXIFCR2 is a whole-file convenience wrapper preserved for backward
// compatibility (this exact signature shipped in v1.3.0): it is equivalent to
// InjectWithEXIFCR2Stream(bytes.NewReader(originalBytes), originalBytes, true,
// modifiedEXIF, rawIPTC, rawXMP, w). Callers that want to stream image data
// directly from a seekable source instead of buffering the whole file should
// call InjectWithEXIFCR2Stream instead.
func InjectWithEXIFCR2(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	return InjectWithEXIFCR2Stream(bytes.NewReader(originalBytes), originalBytes, true, modifiedEXIF, rawIPTC, rawXMP, w)
}

// InjectWithEXIFCR2Stream is InjectWithEXIFCR2's streaming variant.
//
// This is the entry point used by gometadata.Write for FormatCR2.
//
// #291: see InjectWithEXIFStream's own doc comment for the prefix/r/wholeFile
// contract — identical here. insertCR2MarkerAndShiftOffsets only ever
// rewrites offset VALUES stored within the header's own IFD entries (it never
// reads or writes a byte beyond the header it is given), so applying it to
// just the HEADER portion (before image blocks are streamed) reproduces
// exactly the same header bytes as applying it to the old combined
// header+blocks buffer used to.
func InjectWithEXIFCR2Stream(r io.ReadSeeker, prefix []byte, wholeFile bool, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		return writePassThrough(r, w, prefix, wholeFile)
	}

	fileLen, err := resolveFileLen(r, prefix, wholeFile)
	if err != nil {
		return err
	}
	header, blocks, err := relocateTIFFFromParsed(prefix, modifiedEXIF, rawIPTC, rawXMP, fileLen)
	if err != nil {
		return err
	}

	// Insert CR2 marker at position 8 and rebase all IFD offsets by +8.
	//
	// exif.Encode always places IFD0 at offset 8 (hardcoded in writeTIFFHeader:
	// headerSize = 8). After inserting 8 bytes at position 8, IFD0 moves to
	// offset 16, which is the canonical CR2 IFD0 position.
	//
	// Canon CR2 spec §3.1: TIFF header (8 bytes) + CR marker (4 bytes) + RAW IFD
	// offset (4 bytes) = 16-byte total header; IFD0 immediately follows at offset 16.
	newHeader, insertErr := insertCR2MarkerAndShiftOffsets(header, prefix)
	if insertErr != nil {
		return insertErr
	}

	if err := writeRelocated(r, w, newHeader, blocks, prefix, wholeFile); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// cr2MarkerLen is the length of the Canon CR2 proprietary header extension
// inserted at byte position 8 of the TIFF stream.
// lclevy CR2 header: 4-byte marker ('C','R',0x02,0x00) + 4-byte RAW IFD offset = 8 bytes.
const cr2MarkerLen = 8

// cr2IFD0Offset is the canonical IFD0 offset in a well-formed CR2 file.
// Canon CR2 spec §3.1: standard 8-byte TIFF header + 8-byte CR2 marker = 16.
const cr2IFD0Offset = 16

// insertCR2MarkerAndShiftOffsets inserts the 8-byte CR2 header extension at
// byte position 8 of finalTIFF, updates the IFD0 offset in the TIFF header
// from 8 to 16, and rebases all IFD absolute offsets by +8.
//
// CR2 header layout (lclevy, "Canon CR2 specifications", CR2 header):
// bytes [8:10] "CR", [10] major version 2, [11] minor version 0, [12:16] the
// offset of the RAW IFD (IFD3). The signature and version are copied from
// originalBytes[8:12]; the RAW IFD offset is recomputed for the output (step
// 4) because the IFDs move when the metadata is rewritten.
//
// The original TIFF header at [0:4] (byte-order + magic) is preserved.
//
// Algorithm:
//
//  1. Build output: hdr[0:8] + marker[0:8] + finalTIFF[8:].
//  2. Update header bytes [4:8] → 16 (new IFD0 offset).
//  3. Walk IFD0 (at offset 16) and every IFD reachable from it, shifting all
//     offsets by +cr2MarkerLen (+8); see rebaseAllIFDsAfterCR2Marker.
//  4. Write the output offset of the RAW IFD into bytes [12:16]: the IFD at
//     the same position of the IFD0 chain as the one the original header
//     pointed to (task #281).
//
// This mirrors insertRW2GUIDAndShiftOffsets (delta=16 for RW2 GUID) but uses
// delta=8 for the 8-byte CR2 marker extension.
//
// Canon CR2 spec §3.1 / TIFF 6.0 §2 / containers.md §8(e).
func insertCR2MarkerAndShiftOffsets(finalTIFF, originalBytes []byte) ([]byte, error) {
	if len(finalTIFF) < 8 {
		return nil, fmt.Errorf("%w (%d bytes)", ErrCR2OutputTooShort, len(finalTIFF))
	}

	order, orderErr := byteOrder(finalTIFF)
	if orderErr != nil {
		return nil, fmt.Errorf("tiff: CR2 insert marker: %w", orderErr)
	}

	// Build the 8-byte CR2 marker: bytes [8:12] from the original; bytes
	// [12:16] (the RAW IFD offset) are filled in by step 4.
	// lclevy CR2 header: 0x43 'C', 0x52 'R', 0x02 major, 0x00 minor version.
	var marker [cr2MarkerLen]byte
	if len(originalBytes) >= 12 {
		copy(marker[0:4], originalBytes[8:12]) // CR2 signature + version
	} else {
		// Fallback: synthesise canonical marker when original is too short.
		marker[0] = 0x43 // 'C'
		marker[1] = 0x52 // 'R'
		marker[2] = 0x02 // version 2
		marker[3] = 0x00 // version minor 0
	}

	// Step 1: assemble output with marker inserted at position 8.
	//
	//   [0:8]   original TIFF header slot (II/MM + 0x2A 0x00 + IFD0_off=8 → 16)
	//   [8:16]  CR2 marker (4-byte CR signature + 4 zero bytes)
	//   [16:]   finalTIFF[8:] (IFD block + image data)
	//
	// #285: relocateTIFFFromParsed reserves cr2MarkerLen extra bytes of spare
	// CAPACITY in finalTIFF specifically for this insertion (see that
	// function's step 9 doc comment). When present, the marker is inserted IN
	// PLACE: finalTIFF is extended into its own spare capacity and bytes [8:]
	// are shifted right by cr2MarkerLen with a single overlap-safe copy() —
	// the Go spec guarantees copy() behaves correctly for overlapping slices
	// ("The source and destination may overlap"), so this shift-right is safe
	// regardless of copy order. This eliminates the second whole-file-sized
	// allocation this function used to require on every CR2 write; bytes
	// [0:8] are already correct after the reslice (out shares finalTIFF's
	// backing array and starting offset) and are left untouched by both
	// copies below. A caller that supplies finalTIFF without the reserved
	// capacity still gets a correct result via the fallback branch.
	var out []byte
	if cap(finalTIFF) >= len(finalTIFF)+cr2MarkerLen {
		out = finalTIFF[:len(finalTIFF)+cr2MarkerLen]
		copy(out[8+cr2MarkerLen:], finalTIFF[8:]) // shift IFD block + image data right by cr2MarkerLen
		copy(out[8:8+cr2MarkerLen], marker[:])    // insert CR2 marker into the vacated gap
	} else {
		out = make([]byte, len(finalTIFF)+cr2MarkerLen)
		copy(out[0:8], finalTIFF[0:8])            // preserve BOM + magic
		copy(out[8:8+cr2MarkerLen], marker[:])    // insert CR2 marker
		copy(out[8+cr2MarkerLen:], finalTIFF[8:]) // IFD block + image data
	}

	// Step 2: update IFD0 offset in TIFF header from 8 to 16.
	// TIFF 6.0 §2: bytes [4:8] = IFD0 offset. exif.Encode wrote 8; we write 16.
	order.PutUint32(out[4:], uint32(cr2IFD0Offset)) // 16

	// Step 3: rebase all IFD absolute offsets by +cr2MarkerLen (+8).
	// IFD0 is now at position 16 in the output.
	ifd0Start := cr2IFD0Offset
	if ifd0Start+2 > len(out) {
		return nil, fmt.Errorf("%w (offset=%d, len=%d)", ErrCR2IFD0OutOfBounds, ifd0Start, len(out))
	}
	rebaseAllIFDsAfterCR2Marker(out, ifd0Start, order)

	// Step 4 (task #281): restore the RAW IFD pointer of the CR2 header. The
	// original header names the RAW IFD by offset; its position in the IFD0
	// chain (IFD3 in every CR2 the lclevy reference describes) is the same in
	// the output, whose IFDs have moved. When the original pointer cannot be
	// resolved to a chained IFD the field stays 0, as in a header that has
	// none.
	if idx, ok := cr2RawIFDIndex(originalBytes); ok {
		if off, ok := nthCR2IFD(out, idx, order); ok {
			order.PutUint32(out[12:], off)
		}
	}

	return out, nil
}

// cr2MaxChain bounds the IFD0 chain walks of cr2RawIFDIndex and nthCR2IFD
// so a cyclic chain cannot loop forever. A CR2 chains four IFDs.
const cr2MaxChain = 64

// cr2RawIFDIndex returns the position in the IFD0 chain (0 = IFD0) of the
// IFD that the CR2 header of orig names as the RAW IFD (bytes [12:16],
// lclevy CR2 header), and false when orig has no such pointer or it does not
// address a chained IFD.
func cr2RawIFDIndex(orig []byte) (int, bool) {
	if len(orig) < cr2IFD0Offset {
		return 0, false
	}
	order, err := byteOrder(orig)
	if err != nil {
		return 0, false
	}
	raw := order.Uint32(orig[12:])
	if raw == 0 {
		return 0, false
	}
	off := order.Uint32(orig[4:])
	for i := range cr2MaxChain {
		if off == raw {
			return i, true
		}
		next, ok := nextIFDOffset(orig, off, order)
		if !ok || next == 0 {
			return 0, false
		}
		off = next
	}
	return 0, false
}

// nthCR2IFD returns the offset of the IFD at position n of the IFD0 chain of
// the CR2 stream b (IFD0 at cr2IFD0Offset), and false when the chain is
// shorter or leaves b.
func nthCR2IFD(b []byte, n int, order binary.ByteOrder) (uint32, bool) {
	off := uint32(cr2IFD0Offset)
	for range min(n, cr2MaxChain) {
		next, ok := nextIFDOffset(b, off, order)
		if !ok || next == 0 {
			return 0, false
		}
		off = next
	}
	if n > cr2MaxChain || uint64(off)+2 > uint64(len(b)) {
		return 0, false
	}
	return off, true
}

// nextIFDOffset returns the next-IFD pointer of the classic IFD at off
// (TIFF 6.0 §2: 2-byte count, count 12-byte entries, 4-byte pointer), and
// false when the IFD does not fit in b.
func nextIFDOffset(b []byte, off uint32, order binary.ByteOrder) (uint32, bool) {
	if uint64(off)+2 > uint64(len(b)) {
		return 0, false
	}
	at := uint64(off) + 2 + uint64(order.Uint16(b[off:]))*12
	if at+4 > uint64(len(b)) {
		return 0, false
	}
	return order.Uint32(b[at:]), true
}

// cr2MaxIFDs bounds the number of IFDs one rebase pass visits. A Canon CR2
// holds four chained IFDs plus their Exif/GPS/Interop/MakerNote children
// (lclevy "Canon CR2 specifications", poster layout: IFD0 main JPEG preview,
// IFD1 thumbnail, IFD2 uncompressed RGB preview, IFD3 lossless-JPEG raw), so
// 4096 is far above any real file and still bounds the work on hostile input.
const cr2MaxIFDs = 4096

// cr2Rebaser shifts the TIFF-absolute offsets of every IFD reachable from
// IFD0 by cr2MarkerLen. seen holds each IFD start (in output coordinates)
// already shifted, so an offset reachable through two paths is shifted once.
type cr2Rebaser struct {
	out    []byte
	order  binary.ByteOrder
	seen   map[int]struct{}
	budget int
}

// shiftOffset returns v + cr2MarkerLen, and false when v lies before the
// insertion point (8) or the sum would wrap past 4 GiB. #176: an offset
// near MaxUint32 would wrap to ~0 and point into the header.
func shiftOffset(v uint32) (uint32, bool) {
	const insertionPoint = 8
	if v < insertionPoint || v > math.MaxUint32-uint32(cr2MarkerLen) {
		return v, false
	}
	return v + uint32(cr2MarkerLen), true
}

// rebaseAllIFDsAfterCR2Marker shifts every TIFF-absolute offset reachable from
// the IFD at ifdStart in out by +cr2MarkerLen (+8), to account for the 8-byte
// CR2 marker inserted at file position 8. Everything that follows the marker
// moved by +8, including the image data, so every offset that addresses a
// byte at or after position 8 must move too.
//
// Per IFD (TIFF 6.0 §2: 12-byte entries tag(2) type(2) count(4) val_or_off(4)):
//   - the next-IFD pointer is shifted and the chained IFD is rebased in turn
//     (CR2 chains IFD0 -> IFD1 -> IFD2 -> IFD3);
//   - sub-IFD pointer tags (0x8769 ExifIFD, 0x8825 GPS, 0xA005 Interop) and
//     the SubIFDs array (0x014A) are shifted and each target IFD is rebased;
//   - image-data offset arrays StripOffsets (0x0111), TileOffsets (0x0144) and
//     JPEGInterchangeFormat (0x0201) are LONG offsets into the file: each
//     element is shifted, whether stored inline (count 1) or out of line;
//   - every other out-of-line entry (type x count > 4) has its val_or_off
//     pointer shifted.
//
// Guards:
//   - depth limits recursion to maxSubIFDDepth (8) levels, and cr2MaxIFDs
//     bounds the IFDs visited, so a crafted file cannot exhaust the stack or
//     the CPU. #172.
//   - an offset that would wrap past 4 GiB is left unchanged. #176.
//   - an IFD reachable twice is shifted once (cycle and double-shift guard).
//
// Layout reference: lclevy, "Canon CR2 specifications" (IFD0-IFD3 chain, strip
// offsets in IFD0/IFD3, JPEG offsets in IFD0/IFD1/IFD2).
func rebaseAllIFDsAfterCR2Marker(out []byte, ifdStart int, order binary.ByteOrder) {
	r := cr2Rebaser{out: out, order: order, budget: cr2MaxIFDs}
	r.chain(ifdStart, 0)
}

// chain rebases the IFD at start and every IFD linked from it by next-IFD
// pointers. start is in output coordinates (already shifted).
func (r *cr2Rebaser) chain(start, depth int) {
	for start > 0 && depth < maxSubIFDDepth && r.budget > 0 {
		if start+2 > len(r.out) {
			return
		}
		if _, dup := r.seen[start]; dup {
			return
		}
		if r.seen == nil {
			r.seen = make(map[int]struct{}, 8)
		}
		r.seen[start] = struct{}{}
		r.budget--

		count := int(r.order.Uint16(r.out[start:]))
		r.ifd(start, count, depth)

		nextAt := start + 2 + count*12
		if nextAt+4 > len(r.out) {
			return
		}
		next := r.order.Uint32(r.out[nextAt:])
		if next == 0 {
			return
		}
		shifted, ok := shiftOffset(next)
		if !ok {
			return
		}
		r.order.PutUint32(r.out[nextAt:], shifted)
		start = int(shifted)
	}
}

// ifd rebases the entries of one IFD (count entries at start+2).
//
//nolint:cyclop,gocyclo // per-tag dispatch (pointer, offset array, out-of-line) mirrors TIFF 6.0 §2 entry semantics
func (r *cr2Rebaser) ifd(start, count, depth int) {
	for i := range count {
		e := start + 2 + i*12
		if e+12 > len(r.out) {
			return
		}
		tag := exif.TagID(r.order.Uint16(r.out[e:]))
		typ := r.order.Uint16(r.out[e+2:])
		n := r.order.Uint32(r.out[e+4:])
		elemSz := typeSize(typ)
		if elemSz == 0 || n == 0 {
			continue
		}
		total := uint64(elemSz) * uint64(n)

		isPtr := tag == exif.TagExifIFDPointer || tag == exif.TagGPSIFDPointer ||
			tag == exif.TagInteropIFDPointer || tag == exif.TagSubIFDs
		isOffsets := tag == exif.TagStripOffsets || tag == exif.TagTileOffsets ||
			tag == exif.TagJPEGInterchangeFormat

		// Offset-bearing tags are arrays of 32-bit offsets (LONG, or IFD for
		// the pointer tags, TIFF Extension §F); anything else is opaque.
		if (isPtr || isOffsets) && elemSz == 4 {
			r.offsetArray(e, n, total, isPtr, depth)
			continue
		}
		if total > 4 {
			// Out-of-line value: val_or_off is a TIFF-absolute pointer.
			if v, ok := shiftOffset(r.order.Uint32(r.out[e+8:])); ok {
				r.order.PutUint32(r.out[e+8:], v)
			}
		}
	}
}

// offsetArray shifts the n 32-bit file offsets of the entry at e, stored inline
// (total <= 4, n == 1) or out of line behind the val_or_off pointer. When
// descend is set every shifted element is an IFD that is rebased in turn.
func (r *cr2Rebaser) offsetArray(e int, n uint32, total uint64, descend bool, depth int) {
	base := e + 8 // inline: the elements start at the val_or_off field
	if total > 4 {
		ptr, ok := shiftOffset(r.order.Uint32(r.out[e+8:]))
		if !ok {
			return
		}
		r.order.PutUint32(r.out[e+8:], ptr)
		if uint64(ptr)+total > uint64(len(r.out)) {
			return
		}
		base = int(ptr)
	}
	for k := range int(n) {
		at := base + k*4
		v, ok := shiftOffset(r.order.Uint32(r.out[at:]))
		if !ok {
			continue
		}
		r.order.PutUint32(r.out[at:], v)
		if descend && int(v)+2 <= len(r.out) {
			r.chain(int(v), depth+1)
		}
	}
}

// InjectWithEXIFNEF is the NEF-specific variant of InjectWithEXIF.
//
// It runs the Nikon-specific preprocessing step (extend MakerNote blob,
// enumerate PreviewIFD image block) before the standard TIFF copy-and-relocate
// algorithm, and patches the MakerNote-relative PreviewIFD offsets after encoding.
//
// InjectWithEXIFNEF is a whole-file convenience wrapper preserved for
// backward compatibility (this exact signature shipped in v1.3.0): it is
// equivalent to InjectWithEXIFNEFStream(bytes.NewReader(originalBytes),
// originalBytes, true, modifiedEXIF, rawIPTC, rawXMP, w). Callers that want
// to stream image data directly from a seekable source instead of buffering
// the whole file should call InjectWithEXIFNEFStream instead.
//
// See relocate_nef.go for the full algorithm description.
func InjectWithEXIFNEF(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	return InjectWithEXIFNEFStream(bytes.NewReader(originalBytes), originalBytes, true, modifiedEXIF, rawIPTC, rawXMP, w)
}

// InjectWithEXIFNEFStream is InjectWithEXIFNEF's streaming variant.
//
// This is the entry point used by gometadata.Write for FormatNEF.
// See relocate_nef.go for the full algorithm description.
// #291: see InjectWithEXIFStream's own doc comment for the prefix/r/wholeFile
// contract — identical here.
func InjectWithEXIFNEFStream(r io.ReadSeeker, prefix []byte, wholeFile bool, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		return writePassThrough(r, w, prefix, wholeFile)
	}

	fileLen, err := resolveFileLen(r, prefix, wholeFile)
	if err != nil {
		return err
	}
	header, blocks, err := relocateTIFFFromParsedNEF(prefix, r, fileLen, modifiedEXIF, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if err := writeRelocated(r, w, header, blocks, prefix, wholeFile); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// InjectWithEXIFARW is the ARW-specific variant of InjectWithEXIF.
//
// It runs the Sony-specific preprocessing step (extract SR2Private block and
// MakerNote info) before the standard TIFF copy-and-relocate algorithm, and
// patches the following in the output after encoding:
//   - Sony MakerNote (0x927C) OOL offsets are rebased (Sony uses TIFF-absolute
//     offsets, unlike Canon which uses blob-relative).
//   - SR2Private (0xC634) inline 4-byte value is updated to point to the new SR2
//     block position; SR2 internal pointers are rebased.
//
// InjectWithEXIFARW is a whole-file convenience wrapper preserved for
// backward compatibility (this exact signature shipped in v1.3.0): it is
// equivalent to InjectWithEXIFARWStream(bytes.NewReader(originalBytes),
// originalBytes, true, modifiedEXIF, rawIPTC, rawXMP, w). Callers that want
// to stream image data directly from a seekable source instead of buffering
// the whole file should call InjectWithEXIFARWStream instead.
//
// See relocate_arw.go for the full algorithm description.
func InjectWithEXIFARW(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	return InjectWithEXIFARWStream(bytes.NewReader(originalBytes), originalBytes, true, modifiedEXIF, rawIPTC, rawXMP, w)
}

// InjectWithEXIFARWStream is InjectWithEXIFARW's streaming variant.
//
// This is the entry point used by gometadata.Write for FormatARW.
// See relocate_arw.go for the full algorithm description.
// #291: see InjectWithEXIFStream's own doc comment for the prefix/r/wholeFile
// contract — identical here.
func InjectWithEXIFARWStream(r io.ReadSeeker, prefix []byte, wholeFile bool, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		return writePassThrough(r, w, prefix, wholeFile)
	}

	fileLen, err := resolveFileLen(r, prefix, wholeFile)
	if err != nil {
		return err
	}
	header, blocks, err := relocateTIFFFromParsedARW(prefix, r, fileLen, modifiedEXIF, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if err := writeRelocated(r, w, header, blocks, prefix, wholeFile); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// InjectWithEXIFORF is the ORF-specific variant of InjectWithEXIF.
//
// It patches the non-standard Olympus ORF magic bytes (IIRO "0x49 0x49 0x52 0x4F"
// or IIRS "0x49 0x49 0x52 0x53") to standard TIFF magic before relocation, and
// restores the original magic in the output.
//
// originalBytes must carry a valid ORF magic at bytes [0:4]; the caller
// (writeTIFFORF in write.go) is responsible for restoring the real magic
// into m.rawEXIF before calling this function, since orf.Extract patches
// bytes [2:4] to 0x2A 0x00.
//
// InjectWithEXIFORF is a whole-file convenience wrapper preserved for
// backward compatibility (this exact signature shipped in v1.3.0): it is
// equivalent to InjectWithEXIFORFStream(bytes.NewReader(originalBytes),
// originalBytes, true, modifiedEXIF, rawIPTC, rawXMP, w). Callers that want
// to stream image data directly from a seekable source instead of buffering
// the whole file should call InjectWithEXIFORFStream instead.
//
// See relocate_orf.go for the full algorithm description.
func InjectWithEXIFORF(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	return InjectWithEXIFORFStream(bytes.NewReader(originalBytes), originalBytes, true, modifiedEXIF, rawIPTC, rawXMP, w)
}

// InjectWithEXIFORFStream is InjectWithEXIFORF's streaming variant.
//
// prefix must carry a valid ORF magic at bytes [0:4]; the caller
// (writeTIFFORF in write.go) is responsible for restoring the real magic
// into m.rawEXIF before calling this function, since orf.Extract patches
// bytes [2:4] to 0x2A 0x00.
//
// This is the entry point used by gometadata.Write for FormatORF.
// See relocate_orf.go for the full algorithm description.
// #291: see InjectWithEXIFStream's own doc comment for the prefix/r/wholeFile
// contract — identical here.
func InjectWithEXIFORFStream(r io.ReadSeeker, prefix []byte, wholeFile bool, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		return writePassThrough(r, w, prefix, wholeFile)
	}

	fileLen, err := resolveFileLen(r, prefix, wholeFile)
	if err != nil {
		return err
	}
	header, blocks, err := relocateTIFFAsORF(prefix, r, fileLen, modifiedEXIF, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if err := writeRelocated(r, w, header, blocks, prefix, wholeFile); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// InjectWithEXIFRW2 is the RW2-specific variant of InjectWithEXIF.
//
// It handles two Panasonic RW2 non-standard features before relocation:
//   - Non-standard "IIU\x00" magic (patched to 0x2A 0x00 for exif.Parse).
//   - 16-byte device GUID at bytes [8:24] (GUID is saved and re-inserted post-encode;
//     all absolute offsets in IFD0 are rebased by +16 after the GUID insertion).
//
// originalBytes must carry the valid RW2 magic "IIU\x00" at bytes [0:4]; the
// caller (writeTIFFRW2 in write.go) is responsible for restoring the real magic
// since rw2.Extract patches bytes [2:4] to 0x2A 0x00.
//
// InjectWithEXIFRW2 is a whole-file convenience wrapper preserved for
// backward compatibility (this exact signature shipped in v1.3.0): it is
// equivalent to InjectWithEXIFRW2Stream(bytes.NewReader(originalBytes),
// originalBytes, true, modifiedEXIF, rawIPTC, rawXMP, w). Callers that want
// to stream image data directly from a seekable source instead of buffering
// the whole file should call InjectWithEXIFRW2Stream instead.
//
// See relocate_rw2.go for the full algorithm description.
func InjectWithEXIFRW2(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	return InjectWithEXIFRW2Stream(bytes.NewReader(originalBytes), originalBytes, true, modifiedEXIF, rawIPTC, rawXMP, w)
}

// InjectWithEXIFRW2Stream is InjectWithEXIFRW2's streaming variant.
//
// prefix must carry the valid RW2 magic "IIU\x00" at bytes [0:4]; the
// caller (writeTIFFRW2 in write.go) is responsible for restoring the real magic
// since rw2.Extract patches bytes [2:4] to 0x2A 0x00.
//
// This is the entry point used by gometadata.Write for FormatRW2.
// See relocate_rw2.go for the full algorithm description.
// #291: see InjectWithEXIFStream's own doc comment for the prefix/r/wholeFile
// contract — identical here.
func InjectWithEXIFRW2Stream(r io.ReadSeeker, prefix []byte, wholeFile bool, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		return writePassThrough(r, w, prefix, wholeFile)
	}

	fileLen, err := resolveFileLen(r, prefix, wholeFile)
	if err != nil {
		return err
	}
	header, blocks, err := relocateTIFFAsRW2(prefix, fileLen, modifiedEXIF, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if err := writeRelocated(r, w, header, blocks, prefix, wholeFile); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// upsertIFD0Entry adds or replaces an entry in ifd for the given tag while
// maintaining the sorted-by-tag invariant required by IFD.Get (binary search).
//
// TIFF 6.0 §7: each tag in an IFD must appear exactly once and entries must be
// stored in ascending tag order. Violating this invariant causes filterEntries'
// binary search to misidentify present tags as absent, producing duplicate
// entries in the re-encoded output.
//
// For TypeLong (element size = 4 bytes), value is padded to the next 4-byte
// boundary with zero bytes and Count is set to len(paddedValue)/4. IPTC data
// is stored as TypeLong per Adobe XMP Spec and ExifTool convention; padding
// with zero bytes is safe because the IPTC parser scans for 0x1C tag markers
// and silently skips all other byte values (IIM §1.6).
//
// For all other types (e.g. TypeByte for XMP), Count equals len(value).
//
// Implementation: binary search locates the insertion point in O(log n).
// Replace in-place when the tag already exists; otherwise slices.Insert places
// the new entry at the correct sorted position in O(n) (one memmove).
func upsertIFD0Entry(ifd *exif.IFD, tag exif.TagID, typ exif.DataType, value []byte) {
	count := uint32(len(value)) //nolint:gosec // G115: IFD value length bounded by input
	if typ == exif.TypeLong {
		// TIFF 6.0 §2: Count = number of uint32 elements.
		// Round up to the next 4-byte boundary; writeIFD zero-fills the gap in
		// the value area.  The original (unpadded) bytes are kept in Value so
		// that the read-back via extractTagValues returns the unpadded bytes
		// after the caller trims the value to the IFD-declared byte length.
		count = uint32((len(value) + 3) / 4) //nolint:gosec // G115: IFD value length bounded by input
	}
	entry := exif.IFDEntry{
		Tag:   tag,
		Type:  typ,
		Count: count,
		Value: value,
	}

	// Binary search for the insertion point. Entries are expected to be sorted
	// (parseSingleIFD calls sortEntries; prior upsertIFD0Entry calls maintain
	// the invariant after this fix).
	n := len(ifd.Entries)
	lo, hi := 0, n
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if ifd.Entries[mid].Tag < tag {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	i := lo

	if i < n && ifd.Entries[i].Tag == tag {
		// Replace existing entry in-place; sorted order is preserved.
		ifd.Entries[i] = entry
		return
	}
	// Insert at position i to maintain sorted order.
	// slices.Insert is O(n) (one memmove) vs. append-then-sort O(n log n).
	ifd.Entries = slices.Insert(ifd.Entries, i, entry)
}

// tagPhotoshopIRB is the TIFF tag of the Photoshop image resource block
// (Adobe Photoshop File Formats Specification, "TIFF tags": 34377).
const tagPhotoshopIRB = exif.TagID(tiffscan.PSIRTag)

// upsertIPTC stores rawIPTC in tag 0x83BB of ifd (TypeLong, see
// upsertIFD0Entry) and keeps the IPTC digest of the Photoshop image resource
// block (tag 0x8649, resource 0x0425) consistent with it (task #284).
//
// MWG Guidelines v2.0 §3.3.1: the digest is the MD5 of the IPTC-IIM block, so
// a writer that changes the IIM must update it. The new digest covers the
// stored value of tag 0x83BB including its LONG zero padding, the bytes
// Photoshop and ExifTool hash (irb.Digest with align 4). Policy, shared with
// the JPEG writer (irb.AppendSpliced):
//   - no digest resource: none is created;
//   - the IIM changed: the digest is refreshed;
//   - the IIM is unchanged: a current digest is rewritten in the padded form
//     (same meaning, now also current for ExifTool), and a stale digest is
//     left stale, because a stale digest is the MWG signal that the IIM was
//     edited without updating XMP, and rewriting identical IIM is no edit.
//
// The 0x0404 copy of the IIM inside the resource block is not rewritten
// (writing IPTC into tag 0x8649 is out of scope, task #301); readers take
// tag 0x83BB first (TIFF_Handler.cpp:TIFF_MetaHandler::ProcessXMP).
func upsertIPTC(ifd *exif.IFD, rawIPTC []byte) {
	oldIPTC := currentIPTC(ifd)
	upsertIFD0Entry(ifd, exif.TagIPTC, exif.TypeLong, rawIPTC)

	psir := ifd.Get(tagPhotoshopIRB) // after the upsert: it may move entries
	if psir == nil {
		return
	}
	stored := irb.FindDigest(psir.Value)
	if stored == nil {
		return
	}
	if bytes.Equal(oldIPTC, tiffscan.TrimIPTCLongPadding(rawIPTC)) && !irb.DigestCurrent(oldIPTC, stored, 4) {
		return // unchanged IIM, stale digest: keep the MWG signal
	}
	digest := irb.Digest(rawIPTC, 4)
	if bytes.Equal(stored, digest[:]) {
		return
	}
	// RefreshDigest copies: psir.Value may alias the caller's input.
	if v := irb.RefreshDigest(psir.Value, digest); v != nil {
		psir.Value = v
	}
}

// currentIPTC returns the IIM stream ifd holds before an IPTC write: the
// value of tag 0x83BB without its LONG padding, or, when that tag is absent
// or empty, the 0x0404 resource of tag 0x8649 (the read precedence of
// tiffscan.WithPSIRFallback).
func currentIPTC(ifd *exif.IFD) []byte {
	if e := ifd.Get(exif.TagIPTC); e != nil {
		v := e.Value
		if e.Type == exif.TypeLong {
			v = tiffscan.TrimIPTCLongPadding(v)
		}
		if len(v) > 0 {
			return v
		}
	}
	if p := ifd.Get(tagPhotoshopIRB); p != nil {
		return irb.FindIPTC(p.Value)
	}
	return nil
}

// byteOrder determines the TIFF byte order from the first 2 bytes.
func byteOrder(b []byte) (binary.ByteOrder, error) {
	switch {
	case b[0] == 'I' && b[1] == 'I':
		return binary.LittleEndian, nil
	case b[0] == 'M' && b[1] == 'M':
		return binary.BigEndian, nil
	}
	return nil, fmt.Errorf("tiff: invalid byte order marker %q: %w", b[:2], ErrInvalidByteOrder)
}

// extractTagValues scans IFD0 for the IPTC (0x83BB) and XMP (0x02BC) tags
// and returns their raw byte values, using the shared tiffscan scanner with
// this package's type table (type 13 is the 4-byte TIFF Extensions IFD type).
func extractTagValues(data []byte, ifd0Off uint32, order binary.ByteOrder) (rawIPTC, rawXMP []byte) {
	return tiffscan.ExtractTagValues(data, ifd0Off, order, true)
}

// typeSize returns the byte size of a single value for the given TIFF type.
//
// Type 13 ("IFD") intentionally diverges from exif/type.go's DataType table
// (task #270, format/tiff-only fix — does NOT touch the exif package).
//
// CIPA DC-008-2023 §4.6.3 (EXIF 3.0) assigns type code 13 to TypeUTF8, a NEW
// 1-byte-per-element EXIF-registry string type. Adobe TIFF 6.0 Extensions
// ("Adding New Fields", also codified in libtiff's tif_dir.h as TIFF_IFD)
// assigns the SAME numeric code 13 to IFD — a 4-byte pointer to a child IFD —
// predating EXIF 3.0 by decades. This is a genuine type-code collision between
// the two specifications' independent numbering spaces.
//
// exif/type.go correctly follows CIPA DC-008-2023 for its OWN EXIF-registry
// tag table (IFD0/ExifIFD/GPSIFD tags never legitimately used type 13 before
// EXIF 3.0, so the EXIF-3.0 interpretation is safe there). But tag 0x014A
// (SubIFDs) is a TIFF-Extension tag, not an EXIF-registry tag (TIFF Extension
// §F; Adobe DNG Spec §4), and real-world files legitimately declare it as
// type 13 meaning IFD — see the conformance fixture
// testdata/corpus/tiff/metadata-extractor/BigTIFFSubIFD4.tif, purpose-built to
// exercise exactly this. format/tiff's own generic raw-IFD scanners (which
// walk 0x014A arrays and SubIFD entries independently of exif.Parse) must
// interpret type 13 as IFD (4 bytes) for that tag family, matching the
// TIFF-Extension meaning, not the EXIF-3.0 one.
//
// #270 regression: exif.Parse (which correctly follows CIPA DC-008-2023 for
// type 13 in its own struct-based IFD model) mis-sizes a 0x014A/type=13
// entry's inline value to 1 byte instead of 4, silently truncating the SubIFD
// pointer. format/tiff's write-path relocator never relies on exif's parsed
// Value for 0x014A; it re-reads the raw bytes directly using this table (see
// findEntryInIFD / enumerateSubIFDs in relocate_bigtiff.go).
func typeSize(t uint16) uint32 {
	switch t {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
		return 4
	case 13: // IFD — TIFF 6.0 Extensions / libtiff TIFF_IFD; see doc comment above.
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	}
	return 0
}

// typeSizeBigTIFF returns the byte size of a single value for a BigTIFF type.
// It extends typeSize with the three BigTIFF-only 64-bit types:
//
//	LONG8  (16) = uint64, 8 bytes — BigTIFF spec §3.3
//	SLONG8 (17) = int64,  8 bytes — BigTIFF spec §3.3
//	IFD8   (18) = uint64 IFD offset, 8 bytes — BigTIFF spec §3.3
//
// These types are only valid inside a BigTIFF container; classic TIFF parsers
// must reject them.  Returns 0 for any unknown type code (same sentinel as
// typeSize) to allow the caller to skip unrecognised entries gracefully.
//
// Type 13 ("IFD", 4 bytes) is handled identically to typeSize — see that
// function's doc comment for the EXIF-3.0-vs-TIFF-Extension type-code
// collision this deliberately resolves in favour of the TIFF-Extension
// meaning for format/tiff's own generic raw-IFD scanners.
func typeSizeBigTIFF(t uint16) uint64 {
	switch t {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
		return 4
	case 13: // IFD — TIFF 6.0 Extensions / libtiff TIFF_IFD; see typeSize doc comment.
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	case 16, 17, 18: // LONG8, SLONG8, IFD8 — BigTIFF spec §3.3
		return 8
	}
	return 0 // unknown type: caller skips the entry
}

// bigTIFFMinHeaderLen is the minimum length of a valid BigTIFF header.
// BigTIFF spec §2: 16 bytes = 2 (order) + 2 (magic) + 2 (offset-bytesize) +
// 2 (constant) + 8 (IFD0 offset).
const bigTIFFMinHeaderLen = 16

// bigTIFFOffsetBytesize is the only valid value for bytes [4:6] of the
// BigTIFF header.  Any other value means the file is invalid or uses a future
// variant not handled here.  BigTIFF spec §2.
const bigTIFFOffsetBytesize = 8

// bigTIFFMaxIFDEntries caps the entry count read from a single BigTIFF IFD to
// prevent DoS via a crafted count that would exhaust memory.  Real BigTIFF
// files never approach this — it is purely a safety bound.
//
// Classic TIFF uses uint16 (max 65535) for entry count; BigTIFF uses uint64.
// We apply the same 65535 cap here: if an IFD claims more entries than a
// classic TIFF header can even hold, the file is either corrupt or malicious.
const bigTIFFMaxIFDEntries = 65535

// extractBigTIFF parses the BigTIFF header, validates the offset-bytesize
// field, then scans IFD0 for IPTC (0x83BB) and XMP (0x02BC) payloads.
//
// BigTIFF spec §2 (Aware Systems / libtiff):
//
//	bytes  0-1: byte order marker ("II" or "MM")
//	bytes  2-3: magic = 43 (0x002B)
//	bytes  4-5: bytesize-of-offsets — MUST equal 8; reject any other value
//	bytes  6-7: constant = 0 — SHOULD equal 0 (reserved/padding)
//	bytes  8-15: IFD0 offset (uint64, in file byte order)
//
// Anti-DoS invariants carried over from the classic path:
//   - IFD entry count is capped at bigTIFFMaxIFDEntries.
//   - Every uint64 arithmetic step that could overflow is guarded before
//     the multiplication/addition is performed.
//   - All slice accesses are bounds-checked against len(data).
//   - No memory is allocated proportional to claimed counts until the
//     bounds are verified.
func extractBigTIFF(data []byte, order binary.ByteOrder) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	// Minimum 16-byte header required.
	if len(data) < bigTIFFMinHeaderLen {
		return nil, nil, nil, ErrFileTooShort
	}

	// BigTIFF spec §2: bytes [4:6] must be 8.
	offsetBytesize := order.Uint16(data[4:])
	if offsetBytesize != bigTIFFOffsetBytesize {
		return nil, nil, nil, fmt.Errorf("tiff: BigTIFF offset bytesize = %d, must be 8: %w",
			offsetBytesize, ErrUnsupportedMagic)
	}
	// bytes [6:7] should be 0 (reserved). We warn-and-continue rather than
	// reject, to handle any future minor variants that set this field.
	// (BigTIFF spec §2: "constant 0x0000"; validation is advisory.)

	// IFD0 offset is a uint64 at bytes [8:16].
	ifd0Off := order.Uint64(data[8:])

	// The whole data IS the EXIF payload (BigTIFF is itself a TIFF container).
	rawEXIF = data
	rawIPTC, rawXMP = extractTagValuesBigTIFF(data, ifd0Off, order)
	return rawEXIF, rawIPTC, rawXMP, nil
}

// extractTagValuesBigTIFF scans a single BigTIFF IFD at ifd0Off within data
// for IPTC (0x83BB) and XMP (0x02BC) tags and returns their raw byte values.
//
// BigTIFF IFD layout (BigTIFF spec §2):
//
//	bytes 0-7:   entry count (uint64)
//	per entry (20 bytes each):
//	  bytes 0-1:   tag (uint16)
//	  bytes 2-3:   type (uint16)
//	  bytes 4-11:  count (uint64)
//	  bytes 12-19: value-or-offset (uint64)
//	    — inline when typeSizeBigTIFF(type)*count <= 8
//	    — otherwise: 64-bit file offset to the value data
//	after entries:
//	  bytes 0-7:   next-IFD offset (uint64); 0 = end of chain
//
// Anti-DoS: entry count is capped at bigTIFFMaxIFDEntries; every arithmetic
// step that could overflow uint64 is checked before the operation.
func extractTagValuesBigTIFF(data []byte, ifd0Off uint64, order binary.ByteOrder) (rawIPTC, rawXMP []byte) { //nolint:cyclop,gocyclo // BigTIFF IFD scan mirrors extractTagValues but with 8-byte fields; splitting reduces clarity
	// Guard: IFD offset + 8-byte count field must fit in data.
	if ifd0Off > uint64(len(data)) || uint64(len(data))-ifd0Off < 8 {
		return nil, nil
	}

	count := order.Uint64(data[ifd0Off:])
	// Cap the entry count to prevent DoS via huge count values.
	count = min(count, bigTIFFMaxIFDEntries)

	// Each BigTIFF entry is 20 bytes; validate the total entry area fits.
	// Use uint64 arithmetic; check for overflow before multiplication.
	const bigTIFFEntrySize = 20
	if count > (uint64(len(data))-ifd0Off-8)/bigTIFFEntrySize {
		// Entry list is truncated — clamp to what fits.
		count = (uint64(len(data)) - ifd0Off - 8) / bigTIFFEntrySize
	}

	pos := ifd0Off + 8 // first entry starts after the 8-byte count field
	var psir []byte

	for i := uint64(0); i < count; i++ { //nolint:intrange // BigTIFF parser: loop variable is a byte-slice offset multiplier
		e := pos + i*bigTIFFEntrySize
		// Safety: each entry is 20 bytes; confirmed by count clamping above.
		if e+bigTIFFEntrySize > uint64(len(data)) {
			break
		}

		tag := order.Uint16(data[e:])
		typ := order.Uint16(data[e+2:])
		cnt := order.Uint64(data[e+4:])

		// Skip entries with unknown types — we cannot compute a valid value size.
		sz := typeSizeBigTIFF(typ)
		if sz == 0 {
			continue
		}

		// Guard against count*sz overflow before multiplying.
		// sz <= 8 and cnt is uint64; if cnt > MaxUint64/sz the product wraps.
		// Equivalent condition: cnt > maxUint64/sz.
		const maxUint64 = ^uint64(0)
		if sz != 0 && cnt > maxUint64/sz {
			continue // would overflow: entry is corrupt/malicious
		}
		total := sz * cnt

		var v []byte
		// BigTIFF spec §2: inline threshold is 8 bytes (vs 4 in classic TIFF).
		if total <= 8 {
			// Value fits in the 8-byte value-or-offset field (bytes e+12 to e+20).
			v = data[e+12 : e+12+total]
		} else {
			// Value is out-of-line; bytes [e+12:e+20] hold a uint64 file offset.
			off := order.Uint64(data[e+12:])
			// Guard: off + total must not overflow and must be within data.
			if off > uint64(len(data)) || total > uint64(len(data))-off {
				continue // out-of-bounds: skip entry
			}
			v = data[off : off+total]
		}

		switch tag {
		case tiffscan.PSIRTag: // Photoshop IRB; its 0x0404 resource is the IPTC fallback (task #301)
			psir = v
		case 0x83BB: // IPTC-NAA
			// ROBUST-16 (iptc.md §5): type-aware trimming — only TypeLong (4)
			// gets structural-padding trimmed; TypeByte/Undefined are returned
			// as-is. Same logic as the classic-TIFF path. Task #153.
			if len(v) > 0 {
				if typ == 4 { // TypeLong: trim structural alignment padding
					rawIPTC = tiffscan.TrimIPTCLongPadding(v)
				} else {
					rawIPTC = v // TypeByte / TypeUndefined: no trim (ROBUST-16)
				}
				if len(rawIPTC) == 0 {
					rawIPTC = nil
				}
			}
		case 0x02BC: // XMP
			rawXMP = v
		}
	}
	return tiffscan.WithPSIRFallback(rawIPTC, psir), rawXMP
}
