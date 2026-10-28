package iobuf

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// This file holds the helpers that the streaming write paths of the
// containers (HEIF/AVIF, WebP, ORF, RW2, JPEG trailers; tasks #390-#393) share:
// a write copies the unchanged byte ranges of the source straight to the
// destination (CopyRange), reads the few bytes it has to inspect through a
// small window (Window), and looks for a signature in a range of the source
// without loading it (IndexRange). Together with StreamCopyN they let a write
// use memory proportional to the metadata it rewrites instead of the file.

// CopyRange copies the n bytes of r that start at the absolute offset off to
// w, through the pooled buffer of StreamCopyN. n <= 0 is a no-op (r is not
// moved). The returned error is unwrapped: a source that ends before n bytes
// were copied gives io.ErrUnexpectedEOF (or io.EOF when it ends at off).
func CopyRange(r io.ReadSeeker, w io.Writer, off, n int64) error {
	if n <= 0 {
		return nil
	}
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return err
	}
	return StreamCopyN(r, w, n)
}

// windowSize is the size of the buffer of a Window.
const windowSize = defaultSize

// Window gives read access to small pieces of a seekable source of known size
// through one pooled buffer: a scan that reads a header here and a header
// there (box headers, chunk headers) costs one seek and one read per
// windowSize bytes of consecutive headers instead of one per header.
//
// A Window is not safe for concurrent use. Close returns its buffer to the
// pool; the zero value is not usable.
type Window struct {
	r    io.ReadSeeker
	size int64
	buf  *[]byte
	off  int64 // absolute offset of the first byte of the buffer
	n    int   // valid bytes in the buffer
}

// NewWindow returns a Window over r, a source of size bytes.
func NewWindow(r io.ReadSeeker, size int64) *Window {
	return &Window{r: r, size: size, buf: Get(windowSize)}
}

// Peek returns up to n bytes (n <= 4096) of the source at the absolute offset
// off: fewer when the source ends first, none when off is outside it. The
// result aliases the window and is valid until the next call of Peek or Close.
// A source that has fewer bytes than its size gives io.ErrUnexpectedEOF.
func (w *Window) Peek(off int64, n int) ([]byte, error) {
	if off < 0 || off >= w.size || n <= 0 {
		return nil, nil
	}
	n = int(min(int64(n), w.size-off, int64(windowSize)))
	if off >= w.off && off+int64(n) <= w.off+int64(w.n) {
		return (*w.buf)[off-w.off : off-w.off+int64(n)], nil
	}
	fill := int(min(w.size-off, int64(windowSize)))
	if _, err := w.r.Seek(off, io.SeekStart); err != nil {
		w.n = 0
		return nil, err
	}
	if _, err := io.ReadFull(w.r, (*w.buf)[:fill]); err != nil {
		w.n = 0
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	w.off, w.n = off, fill
	return (*w.buf)[:n], nil
}

// Close returns the buffer of the window to the pool. The window must not be
// used afterwards.
func (w *Window) Close() {
	if w.buf != nil {
		Put(w.buf)
		w.buf = nil
	}
}

// ErrPatternTooLong is returned by IndexRange for a pattern longer than the
// window it searches through, and by ScanRange for a pattern and window that do
// not fit the buffer.
var ErrPatternTooLong = errors.New("iobuf: pattern longer than the search window")

// IndexRange returns the absolute offset of the first occurrence of pat in
// the bytes [from, to) of r, or -1 when there is none. It reads the range once,
// sequentially, through one pooled buffer (never allocating memory
// proportional to the range), and finds a pattern that spans two reads. A
// source that ends before to gives io.ErrUnexpectedEOF. It reads a buffer for
// each call, so it is for one search; a caller that visits every occurrence in a
// range uses ScanRange (task #417).
func IndexRange(r io.ReadSeeker, from, to int64, pat []byte) (int64, error) {
	if len(pat) == 0 || to-from < int64(len(pat)) {
		return -1, nil
	}
	bufPtr := Get(StreamCopyBufSize)
	defer Put(bufPtr)
	buf := *bufPtr
	if len(pat) > len(buf)/2 {
		return -1, ErrPatternTooLong
	}
	if _, err := r.Seek(from, io.SeekStart); err != nil {
		return -1, err
	}
	carry := 0  // bytes at the start of buf kept from the previous read
	pos := from // absolute offset of buf[carry]
	for pos < to {
		want := int(min(int64(len(buf)-carry), to-pos))
		n, err := io.ReadFull(r, buf[carry:carry+want])
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return -1, fmt.Errorf("iobuf: index range: %w", err)
		}
		chunk := buf[:carry+n]
		if i := bytes.Index(chunk, pat); i >= 0 {
			return pos - int64(carry) + int64(i), nil
		}
		pos += int64(n)
		keep := min(len(pat)-1, len(chunk))
		copy(buf, chunk[len(chunk)-keep:])
		carry = keep
	}
	return -1, nil
}

// ScanVisitor receives the occurrences that ScanRange finds. It is an interface,
// not a function, so that a caller that scans often keeps its state in a pooled
// value and allocates nothing per scan.
type ScanVisitor interface {
	// Visit is called for the occurrence at the offset at; win is the window
	// around it (see ScanRange). It returns the offset where the search for the
	// next occurrence starts, or a negative offset to stop.
	Visit(at int64, win []byte) (next int64, err error)
}

// ScanFunc is a function that is a ScanVisitor.
type ScanFunc func(at int64, win []byte) (next int64, err error)

// Visit calls f.
func (f ScanFunc) Visit(at int64, win []byte) (int64, error) { return f(at, win) }

// errScanWindow reports a window that is not in the buffer of ScanRange; the
// buffer keeps the bytes that every window needs, so it is never returned.
var errScanWindow = errors.New("iobuf: scan window outside the buffer")

// ScanRange calls fn.Visit for the occurrences of pat in the bytes [from, to) of r,
// in ascending order of offset, reading the range once, sequentially, through
// one pooled buffer. It is the loop "at = IndexRange(r, next, to, pat); next =
// at+1" without its cost: IndexRange called once per occurrence reads a buffer
// of data per call, which is quadratic when occurrences are close together
// (task #417); ScanRange never reads a byte of the range twice.
//
// fn receives the offset at of the occurrence and win, the bytes
// [at-behind, at+ahead) of the file, or nil when that window is not wholly
// inside [from, to) (ahead must be at least len(pat)). win aliases the buffer:
// it is valid until fn returns, and fn must not read from r, which the scan
// owns. fn returns the offset where the search for the next occurrence starts
// (an offset at or before at is read as at+1, so an occurrence may overlap the
// previous one), or a negative offset to stop. The occurrences that fn sees are
// those that repeated IndexRange calls would find.
//
// A source that ends before to gives io.ErrUnexpectedEOF; an error of fn stops
// the scan and is returned as it is.
//
//nolint:gocyclo,cyclop // one loop whose branches (read, search, window, skip ahead, keep) each have a reason; differential-tested against bytes.Index
func ScanRange(r io.ReadSeeker, from, to int64, pat []byte, behind, ahead int, fn ScanVisitor) error {
	if len(pat) == 0 || to-from < int64(len(pat)) {
		return nil
	}
	bufPtr := Get(StreamCopyBufSize)
	defer Put(bufPtr)
	buf := *bufPtr
	keepMax := behind + ahead - 1 // bytes kept between two reads: a window that ends in the next read
	if behind < 0 || ahead < len(pat) || keepMax > len(buf)/2 {
		return ErrPatternTooLong
	}
	if _, err := r.Seek(from, io.SeekStart); err != nil {
		return err
	}
	carry := 0       // bytes at the start of buf kept from the previous read
	pos := from      // absolute offset of buf[carry]
	searchAt := from // no occurrence that starts before it is reported
	for pos < to {
		want := int(min(int64(len(buf)-carry), to-pos))
		n, err := io.ReadFull(r, buf[carry:carry+want])
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return fmt.Errorf("iobuf: scan range: %w", err)
		}
		chunk := buf[:carry+n]
		chunkStart := pos - int64(carry)
		chunkEnd := pos + int64(n)
		for {
			rel := max(searchAt-chunkStart, 0)
			if rel >= int64(len(chunk)) {
				break
			}
			i := bytes.Index(chunk[rel:], pat)
			if i < 0 {
				break
			}
			at := chunkStart + rel + int64(i)
			if at+int64(ahead) > chunkEnd && chunkEnd < to {
				break // the window ends in the next read
			}
			var win []byte
			if at-int64(behind) >= from && at+int64(ahead) <= to {
				lo, hi := at-int64(behind)-chunkStart, at+int64(ahead)-chunkStart
				if lo < 0 || hi > int64(len(chunk)) { // the buffer keeps what a window needs: a guard, not a path
					return errScanWindow
				}
				win = chunk[lo:hi]
			}
			next, err := fn.Visit(at, win)
			if err != nil || next < 0 {
				return err
			}
			searchAt = max(next, at+1)
		}
		pos = chunkEnd
		if searchAt >= pos {
			// The next occurrence starts beyond what was read: continue from there,
			// with the bytes before it that its window needs.
			if searchAt >= to {
				return nil
			}
			pos = max(searchAt-int64(behind), from)
			if _, err := r.Seek(pos, io.SeekStart); err != nil {
				return err
			}
			carry = 0
			continue
		}
		keep := min(keepMax, len(chunk))
		copy(buf, chunk[len(chunk)-keep:])
		carry = keep
	}
	return nil
}
