package tiff

// relocate_stream.go — task #291: stream a relocated TIFF-family write's
// image-data blocks from the original source instead of buffering the whole
// file. See relocateTIFFFromParsed's own doc comment (relocate.go) for the
// header/blocks split this file's writeRelocated consumes.

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
	"github.com/FlavioCFOliveira/GoMetadata/internal/trailer"
)

// resolveFileLen returns the true total length of the original source: when
// wholeFile is true, prefix already IS the whole file, so its own length is
// the answer at no I/O cost; otherwise it is learned via a single cheap
// Seek(SeekEnd) on r (r's position is restored to 0 before returning, ready
// for the caller's own subsequent use).
func resolveFileLen(r io.ReadSeeker, prefix []byte, wholeFile bool) (uint64, error) {
	if wholeFile {
		return uint64(len(prefix)), nil
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("tiff: seek to end: %w", err)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("tiff: seek to start: %w", err)
	}
	return uint64(end), nil //nolint:gosec // G115: file length bounded by maxFileSize (256 MiB), never negative
}

// writePassThrough writes the ENTIRE original source to w unchanged: prefix
// directly when it already holds the whole file (wholeFile true), or
// streamed from r via a pooled buffer otherwise (never buffering the whole
// file). Used by every Inject entry point's "no metadata changes requested"
// fast path.
func writePassThrough(r io.ReadSeeker, w io.Writer, prefix []byte, wholeFile bool) error {
	if wholeFile {
		if _, err := w.Write(prefix); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}
	fileLen, err := resolveFileLen(r, prefix, wholeFile)
	if err != nil {
		return err
	}
	if err := iobuf.StreamCopyN(r, w, int64(fileLen)); err != nil { //nolint:gosec // G115: fileLen bounded by maxFileSize (256 MiB), fits int64
		return fmt.Errorf("tiff: copy: %w", err)
	}
	return nil
}

// writeRelocated writes header to w, then writes every block's bytes, in
// slice order, immediately after — reproducing exactly the single contiguous
// buffer relocateTIFFFromParsed (and its NEF/ARW/ORF/RW2-specific siblings)
// used to return directly before task #291.
//
// wholeFile reports whether prefix already contains the ENTIRE original
// source file (see Metadata.rawEXIFIsWholeFile — #289/#293). Two paths:
//
//   - wholeFile == true: every block's bytes are already present in prefix
//     (at prefix[blk.srcOffset:blk.srcOffset+blk.size]) — sliced and written
//     directly, exactly as the pre-#291 implementation did, with no Seek/Read
//     round trip through r. r is never touched in this path (may be nil).
//   - wholeFile == false: prefix is only the metadata prefix; each block's
//     bytes live beyond it in the original file, so they are streamed from r
//     — seeking to blk.srcOffset and copying blk.size bytes through a fixed,
//     pooled buffer (iobuf.StreamCopyN) — never buffering the whole file.
//     r must be seekable and remain valid for the duration of this call.
//
// iptc is the IIM written to the IPTC tag, or nil.
//
// Bytes after the data the write moves are carried (task #360): the tail of
// the source, from the first byte after every byte that an IFD addresses
// (the image blocks, and the IFDs and out-of-line values of the metadata,
// metadataEnd) to its end, is written after the last block unchanged. It holds
// the trailers that some writers append after the TIFF data: AFCP and
// FotoStation, whose IPTC copy is replaced by iptc and whose absolute offsets
// are set for their new position (task #354, trailer.Plan), and every other
// trailer or private data, which is copied byte for byte (CLAUDE.md §5).
// Bytes between addressed regions (padding, and the IFDs and values that this
// write replaces) are not carried: they are exactly what the write
// regenerates, and carrying them would duplicate stale metadata.
//
// Every block is bounds-checked against the source's true total length
// (resolveFileLen) before any Seek/Read is attempted, preserving the exact
// ErrBlockOutOfBounds protection the pre-#291 single-buffer implementation
// applied via `end > len(base)`.
func writeRelocated(r io.ReadSeeker, w io.Writer, header []byte, blocks []*imageBlock, prefix []byte, wholeFile bool, iptc []byte) error {
	tail, err := planTail(r, prefix, wholeFile, blocks, iptc)
	if err != nil {
		return err
	}
	if tail.start >= tail.end {
		return writeHeaderAndBlocks(r, w, header, blocks, prefix, wholeFile)
	}
	if len(tail.plan.Spans) == 0 {
		if err := writeHeaderAndBlocks(r, w, header, blocks, prefix, wholeFile); err != nil {
			return err
		}
		return tail.write(r, w, nil)
	}
	// A trailer with absolute offsets needs the position it takes in the output.
	out := &trailer.OutWriter{W: w}
	if err := writeHeaderAndBlocks(r, out, header, blocks, prefix, wholeFile); err != nil {
		return err
	}
	return tail.write(r, out, out)
}

// tailPlan is the part of the source after the data that the write moves.
type tailPlan struct {
	// start and end are the source offsets of the tail; start >= end: none.
	start, end uint64
	// plan holds the tail bytes (plan.Region) and the trailers inside it that
	// need rewriting; Region is nil when the tail is larger than
	// trailer.MaxRegion and is streamed from the source instead.
	plan trailer.Plan
}

// planTail finds the tail of the source (see writeRelocated). A trailer found
// from the end of the file (trailer.FindTail) may start before the end of the
// addressed data when the metadata structures it overlaps are mere pointers
// into it; such a trailer extends the tail down to its start, provided it
// starts after every image block (a candidate that overlaps one is image data
// that merely looks like a trailer, and is ignored). iptc is the IIM written to
// the IPTC tag (nil: leave the IPTC copies as they are).
func planTail(r io.ReadSeeker, prefix []byte, wholeFile bool, blocks []*imageBlock, iptc []byte) (tailPlan, error) {
	fileLen, err := resolveFileLen(r, prefix, wholeFile)
	if err != nil {
		return tailPlan{}, err
	}
	blocksEnd := endOfBlocks(blocks)
	if blocksEnd >= fileLen {
		return tailPlan{}, nil // the last image block ends the file: nothing follows it
	}
	start := min(max(blocksEnd, metadataEnd(prefix)), fileLen)

	region, base, err := findTrailerRegion(r, prefix, wholeFile, fileLen)
	if err != nil {
		return tailPlan{}, err
	}
	if region != nil && base >= blocksEnd && base < start {
		start = base
	}
	if start >= fileLen {
		return tailPlan{}, nil
	}
	return loadTail(r, prefix, wholeFile, start, fileLen, region, base, iptc)
}

// loadTail returns the plan for the tail [start, end) of the source: its
// bytes, with the trailers in it to rewrite. region (at offset base) is what
// trailer.FindTail already loaded, or nil.
func loadTail(r io.ReadSeeker, prefix []byte, wholeFile bool, start, end uint64, region []byte, base uint64, iptc []byte) (tailPlan, error) {
	t := tailPlan{start: start, end: end}
	if end-start > trailer.MaxRegion {
		return t, nil // too large to look for trailers in: streamed as it is
	}
	if region == nil || base != start {
		var err error
		if region, err = loadRegion(r, prefix, wholeFile, start, end); err != nil {
			return tailPlan{}, err
		}
	}
	t.plan = trailer.PlanFromRegion(region, int64(start), iptc) //nolint:gosec // G115: start <= end <= maxFileSize
	return t, nil
}

// endOfBlocks returns the offset one past the last byte of the image blocks.
func endOfBlocks(blocks []*imageBlock) uint64 {
	end := uint64(0)
	for _, blk := range blocks {
		end = max(end, saturatingAdd(blk.srcOffset, blk.size))
	}
	return end
}

// findTrailerRegion looks for an AFCP or FotoStation trailer at the end of the
// source (trailer.FindTail) and returns the bytes from the start of the first
// to the end of the file and its offset; region is nil when there is none.
func findTrailerRegion(r io.ReadSeeker, prefix []byte, wholeFile bool, fileLen uint64) (region []byte, base uint64, err error) {
	var b int64
	if wholeFile {
		region, b, err = trailer.FindTail(trailer.BytesSource(prefix), int64(fileLen)) //nolint:gosec // G115: fileLen is bounded by maxFileSize
	} else {
		region, b, err = trailer.FindTail(trailer.ReaderSource{R: r}, int64(fileLen)) //nolint:gosec // G115: fileLen is bounded by maxFileSize
	}
	if err != nil {
		return nil, 0, fmt.Errorf("tiff: look for trailers: %w", err)
	}
	return region, uint64(b), nil //nolint:gosec // G115: b is a non-negative file offset
}

// loadRegion returns the source bytes [start, end): a slice of prefix when it
// holds the whole file, otherwise read from r.
func loadRegion(r io.ReadSeeker, prefix []byte, wholeFile bool, start, end uint64) ([]byte, error) {
	if wholeFile {
		return prefix[start:end:end], nil
	}
	region, err := (trailer.ReaderSource{R: r}).Range(int64(start), int64(end-start)) //nolint:gosec // G115: bounded by maxFileSize
	if err != nil {
		return nil, fmt.Errorf("tiff: read trailing bytes: %w", err)
	}
	return region, nil
}

// write appends the tail to w: from the plan's region when it was loaded
// (rewriting the trailers that need it; out counts the bytes written so far),
// otherwise streamed from r.
func (t *tailPlan) write(r io.ReadSeeker, w io.Writer, out *trailer.OutWriter) error {
	switch {
	case len(t.plan.Spans) > 0:
		if err := t.plan.WriteRegion(w, out); err != nil {
			return fmt.Errorf("tiff: write trailer: %w", err)
		}
	case t.plan.Region != nil:
		if _, err := w.Write(t.plan.Region); err != nil {
			return fmt.Errorf("tiff: write trailing bytes: %w", err)
		}
	default:
		if _, err := r.Seek(int64(t.start), io.SeekStart); err != nil { //nolint:gosec // G115: bounded by maxFileSize
			return fmt.Errorf("tiff: seek to trailing bytes: %w", err)
		}
		if err := iobuf.StreamCopyN(r, w, int64(t.end-t.start)); err != nil { //nolint:gosec // G115: bounded by maxFileSize
			return fmt.Errorf("tiff: stream trailing bytes: %w", err)
		}
	}
	return nil
}

// saturatingAdd returns a+b, or math.MaxUint64 when the sum overflows.
func saturatingAdd(a, b uint64) uint64 {
	if s := a + b; s >= a {
		return s
	}
	return math.MaxUint64
}

// bigTIFFMagic is the version field of a BigTIFF header (BigTIFF spec §2).
const bigTIFFMagic = 0x002B

// metadataEnd returns the offset one past the last byte that an IFD of the
// classic or BigTIFF header at the start of prefix addresses as structure: the
// IFDs of IFD0's chain, their sub-IFDs and every out-of-line value (the extent
// scanner of Extract, extent.go). The strip, tile and thumbnail data that the
// IFDs describe is not included: it is the image blocks. It returns 0 when
// prefix does not start with a TIFF header. The non-standard magic of ORF and
// RW2 is accepted: only the byte order and the IFD0 offset are read.
func metadataEnd(prefix []byte) uint64 {
	if len(prefix) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch {
	case prefix[0] == 'I' && prefix[1] == 'I':
		order = binary.LittleEndian
	case prefix[0] == 'M' && prefix[1] == 'M':
		order = binary.BigEndian
	default:
		return 0
	}
	ifd0, bigTIFF := uint64(order.Uint32(prefix[4:])), false
	if order.Uint16(prefix[2:]) == bigTIFFMagic {
		if len(prefix) < 16 {
			return 0
		}
		ifd0, bigTIFF = order.Uint64(prefix[8:]), true
	}
	scan := extentScanPool.Get().(*extentScan) //nolint:forcetypeassert,revive // extentScanPool.New always stores *extentScan
	end := scanExtentPass(scan, prefix, ifd0, order, bigTIFF)
	scan.buf = nil // do not retain the caller's prefix in the pool
	extentScanPool.Put(scan)
	return end
}

// extentScanPool recycles the scanner (and its map of visited IFDs) of
// metadataEnd, which every TIFF write runs.
//
//nolint:gochecknoglobals // sync.Pool: reuse avoids two allocations per write
var extentScanPool = sync.Pool{New: func() any { return &extentScan{seenIFD: make(map[uint64]bool, 16)} }}

// checkBlocks validates every block against the source's true total length
// before anything is written: each block must lie inside the source.
//
// A block's range may overlap another's: real files do (a Sony ARW holds one
// StripOffsets strip and the TileOffsets tiles over the same bytes; several
// TIFFs hold JPEGInterchangeFormat data inside a strip). The overlap is not
// rejected: planBlocks coalesces the source ranges so that every source byte is
// copied once, which keeps the output proportional to the input however many
// entries repeat a range (security audit F-07a: 2000 StripOffsets entries over
// one 1 MiB range used to write 2000 MiB). The copied union lies inside the
// source (this check), so it is at most the length of the file.
//
// It reports whether the blocks are disjoint and ascending (disjointAscending),
// found in the same pass.
func checkBlocks(blocks []*imageBlock, fileLen uint64) (disjoint bool, err error) {
	disjoint = true
	var end uint64
	for _, blk := range blocks {
		// fits (extent.go), not a raw `blk.srcOffset+blk.size > fileLen`
		// comparison: a crafted BigTIFF LONG8 offset/size pair can wrap that
		// raw addition to a small value, passing this check incorrectly and
		// then panicking on prefix[blk.srcOffset:end] below — srcOffset
		// itself already exceeds cap(prefix) (security audit finding,
		// 2026-09-26).
		if !fits(blk.srcOffset, blk.size, fileLen) {
			return false, fmt.Errorf("tiff: image block offset=%d size=%d: %w: %w",
				blk.srcOffset, blk.size, ErrBlockOutOfBounds, errTruncatedImageData())
		}
		if blk.size > 0 {
			disjoint = disjoint && blk.srcOffset >= end
			end = blk.srcOffset + blk.size // fits: cannot overflow
		}
	}
	return disjoint, nil
}

// writeHeaderAndBlocks writes header and then the bytes of the blocks to w (see
// writeRelocated): each block in slice order when the blocks are disjoint, else
// each range of the coalesced union (planBlocks) in the order assignNewOffsets
// placed it.
func writeHeaderAndBlocks(r io.ReadSeeker, w io.Writer, header []byte, blocks []*imageBlock, prefix []byte, wholeFile bool) error {
	disjoint := true
	if len(blocks) > 0 {
		fileLen, err := resolveFileLen(r, prefix, wholeFile)
		if err != nil {
			return err
		}
		if disjoint, err = checkBlocks(blocks, fileLen); err != nil {
			return err
		}
	}
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("tiff: write header: %w", err)
	}
	return writeBlocks(r, w, blocks, prefix, wholeFile, disjoint)
}

// writeBlocks writes the bytes of the blocks to w: each block in slice order
// when disjoint, else each range of the coalesced union (planBlocks).
func writeBlocks(r io.ReadSeeker, w io.Writer, blocks []*imageBlock, prefix []byte, wholeFile, disjoint bool) error {
	if !disjoint {
		for _, rg := range planBlocks(blocks).ranges {
			if err := writeSource(r, w, prefix, wholeFile, rg.start, rg.end-rg.start); err != nil {
				return err
			}
		}
		return nil
	}
	for _, blk := range blocks {
		if wholeFile { // the common path, kept inline: it runs once per strip or tile
			if _, err := w.Write(prefix[blk.srcOffset : blk.srcOffset+blk.size]); err != nil { // checkBlocks proved the range
				return fmt.Errorf("tiff: write image block: %w", err)
			}
			continue
		}
		if err := writeSource(r, w, prefix, wholeFile, blk.srcOffset, blk.size); err != nil {
			return err
		}
	}
	return nil
}

// writeSource writes the size source bytes at off to w: sliced from prefix when
// it holds the whole file, otherwise streamed from r. checkBlocks proved that
// the range lies inside the source.
func writeSource(r io.ReadSeeker, w io.Writer, prefix []byte, wholeFile bool, off, size uint64) error {
	if wholeFile {
		end := off + size // safe: checkBlocks proved this cannot overflow
		if _, err := w.Write(prefix[off:end]); err != nil {
			return fmt.Errorf("tiff: write image block: %w", err)
		}
		return nil
	}
	if _, err := r.Seek(int64(off), io.SeekStart); err != nil { //nolint:gosec // G115: off <= fileLen, itself bounded by maxFileSize (256 MiB), fits int64
		return fmt.Errorf("tiff: seek image block at %d: %w", off, err)
	}
	if err := iobuf.StreamCopyN(r, w, int64(size)); err != nil { //nolint:gosec // G115: size <= fileLen, itself bounded by maxFileSize (256 MiB), fits int64
		return fmt.Errorf("tiff: stream image block offset=%d size=%d: %w", off, size, err)
	}
	return nil
}
