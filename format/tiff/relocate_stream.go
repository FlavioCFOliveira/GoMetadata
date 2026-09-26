package tiff

// relocate_stream.go — task #291: stream a relocated TIFF-family write's
// image-data blocks from the original source instead of buffering the whole
// file. See relocateTIFFFromParsed's own doc comment (relocate.go) for the
// header/blocks split this file's writeRelocated consumes.

import (
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
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
// Every block is bounds-checked against the source's true total length
// (resolveFileLen) before any Seek/Read is attempted, preserving the exact
// ErrBlockOutOfBounds protection the pre-#291 single-buffer implementation
// applied via `end > len(base)`.
func writeRelocated(r io.ReadSeeker, w io.Writer, header []byte, blocks []*imageBlock, prefix []byte, wholeFile bool) error {
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("tiff: write header: %w", err)
	}
	if len(blocks) == 0 {
		return nil
	}
	fileLen, err := resolveFileLen(r, prefix, wholeFile)
	if err != nil {
		return err
	}

	for _, blk := range blocks {
		// fits (extent.go), not a raw `blk.srcOffset+blk.size > fileLen`
		// comparison: a crafted BigTIFF LONG8 offset/size pair can wrap that
		// raw addition to a small value, passing this check incorrectly and
		// then panicking on prefix[blk.srcOffset:end] below — srcOffset
		// itself already exceeds cap(prefix) (security audit finding,
		// 2026-09-26).
		if !fits(blk.srcOffset, blk.size, fileLen) {
			return fmt.Errorf("tiff: image block offset=%d size=%d: %w",
				blk.srcOffset, blk.size, ErrBlockOutOfBounds)
		}
		if wholeFile {
			end := blk.srcOffset + blk.size // safe: fits() above proved this cannot overflow
			if _, err := w.Write(prefix[blk.srcOffset:end]); err != nil {
				return fmt.Errorf("tiff: write image block: %w", err)
			}
			continue
		}
		if _, err := r.Seek(int64(blk.srcOffset), io.SeekStart); err != nil { //nolint:gosec // G115: blk.srcOffset <= fileLen, itself bounded by maxFileSize (256 MiB), fits int64
			return fmt.Errorf("tiff: seek image block at %d: %w", blk.srcOffset, err)
		}
		if err := iobuf.StreamCopyN(r, w, int64(blk.size)); err != nil { //nolint:gosec // G115: blk.size <= fileLen, itself bounded by maxFileSize (256 MiB), fits int64
			return fmt.Errorf("tiff: stream image block offset=%d size=%d: %w", blk.srcOffset, blk.size, err)
		}
	}
	return nil
}
