package iobuf

import (
	"bytes"
	"io"
	"math"
	"sync"
)

// limitedReaderPool serves *io.LimitedReader values reused across
// streamCopyNFast calls: constructing one fresh per call and passing it to
// (*bytes.Buffer).ReadFrom as an io.Reader interface value always
// heap-allocates it (bytes.Buffer.ReadFrom's own escape summary treats its
// io.Reader parameter as retained-and-escaping — confirmed via
// `go build -gcflags="-m -m"`, not an assumption), even though ReadFrom
// itself never actually keeps the reference beyond the call. Pooling
// amortises that allocation to a pool miss instead of every call, matching
// this package's own Get/Put pattern for []byte buffers.
var limitedReaderPool = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure, mirrors iobuf.go's pool/largePool
	New: func() any { return new(io.LimitedReader) },
}

// StreamCopyBufSize is the fixed size of the reusable buffer StreamCopyN uses
// to move bytes from r to w. Equal to the largePool tier ceiling (largeSize)
// so the buffer is served from that existing pool instead of a dedicated one.
const StreamCopyBufSize = largeSize

// StreamCopyN copies exactly n bytes from r to w using one pooled, fixed-size
// buffer reused across the whole call, never allocating memory proportional
// to n. This is the shared primitive behind every container format's
// "rebuild only the small metadata region, stream everything else verbatim"
// write path (PNG's pass-through chunks, CR3's ftyp/mdat regions and
// #291's TIFF-family image-block copies): none of them need n's bytes to be
// inspected, only relocated in the output stream, so holding them in memory
// even briefly would defeat the whole point of avoiding a whole-file buffer.
//
// n <= 0 is a no-op (returns nil immediately) rather than an error, since
// callers commonly compute n as a difference between two file offsets that
// may legitimately be zero (e.g. a container with nothing between two
// adjacent regions).
//
// Returns the first read or write error encountered, unwrapped — callers add
// their own format-specific context (e.g. "cr3: copy pre-moov bytes: %w").
//
// Task #297: for two specific, safe-to-detect endpoint types, StreamCopyN
// moves n's bytes only ONCE instead of twice (pooled ReadFull, then
// w.Write) — see streamCopyNFast's own doc comment for why these two cases,
// and only these two, get the fast path. Every other (r, w) pair — in
// particular file-to-file, this package's original and still most common
// caller — keeps the exact pooled-buffer loop unchanged: a naive "use
// io.CopyN whenever w implements io.ReaderFrom" was measured 31-38% SLOWER
// file-to-file on darwin, because (*os.File).ReadFrom falls back to
// io.Copy's own small internal buffer instead of this package's larger,
// pooled one.
func StreamCopyN(r io.Reader, w io.Writer, n int64) error {
	if n <= 0 {
		return nil
	}
	if handled, err := streamCopyNFast(r, w, n); handled {
		return err
	}
	bufPtr := Get(StreamCopyBufSize)
	buf := *bufPtr
	for n > 0 {
		chunk := buf
		if int64(len(chunk)) > n {
			chunk = chunk[:n]
		}
		if _, err := io.ReadFull(r, chunk); err != nil {
			Put(bufPtr)
			return err
		}
		if _, err := w.Write(chunk); err != nil {
			Put(bufPtr)
			return err
		}
		n -= int64(len(chunk))
	}
	Put(bufPtr)
	return nil
}

// streamCopyNFast implements StreamCopyN's single-copy path for two
// endpoint types whose own standard-library methods already move bytes
// directly into or out of their final resting place, with no intermediate
// buffer at all:
//
//   - w is *bytes.Buffer: (*bytes.Buffer).ReadFrom reads directly into the
//     buffer's own backing array (growing it first via Grow, so ReadFrom's
//     own internal growth never re-allocates), instead of reading into a
//     pooled scratch buffer that Write then copies AGAIN into the same
//     backing array. Bounded to exactly n bytes via a pooled
//     *io.LimitedReader (limitedReaderPool) — never io.LimitReader(r, n),
//     which always heap-allocates a fresh *io.LimitedReader on every call —
//     so this path adds no allocation over the pooled path it replaces,
//     beyond the occasional pool-miss the sync.Pool itself may incur.
//   - r is *bytes.Reader and n equals its own remaining length: r.WriteTo
//     writes r's own remaining bytes (a zero-copy slice into r's backing
//     array) directly to w in one call. This is deliberately narrower than
//     "any n up to r.Len()": bytes.Reader exposes no public way to bound a
//     WriteTo to fewer than all of its remaining bytes without copying, so
//     the fast path only applies to the (common, in this project's own
//     "stream everything from here to EOF" call sites) case where the two
//     already coincide.
//
// handled reports whether one of these cases applied (regardless of
// whether it then encountered an error), so the caller knows whether to
// fall back to the generic pooled-buffer loop.
func streamCopyNFast(r io.Reader, w io.Writer, n int64) (handled bool, err error) {
	// n > math.MaxInt guards bytes.Buffer.Grow's int parameter on a 32-bit
	// platform; in practice n is always bounded by this project's own
	// maxFileSize (256 MiB) caps, far below MaxInt even at 32 bits, but this
	// keeps the fast path's own precondition self-contained rather than
	// relying on every caller's cap to hold forever.
	if buf, ok := w.(*bytes.Buffer); ok && n <= math.MaxInt {
		buf.Grow(int(n))
		lr, _ := limitedReaderPool.Get().(*io.LimitedReader) // comma-ok: limitedReaderPool.New always stores *io.LimitedReader; pool invariant
		lr.R, lr.N = r, n
		written, err := buf.ReadFrom(lr)
		lr.R = nil // drop the reference to r before returning to the pool
		limitedReaderPool.Put(lr)
		if err == nil && written != n {
			// (*bytes.Buffer).ReadFrom treats io.EOF as normal termination
			// and never returns it — io.ReadFull's convention (which the
			// pooled path below uses) is what StreamCopyN's callers expect:
			// a short read is io.ErrUnexpectedEOF, not a silent success.
			err = io.ErrUnexpectedEOF
		}
		return true, err
	}
	if br, ok := r.(*bytes.Reader); ok && n == int64(br.Len()) {
		written, err := br.WriteTo(w)
		if err == nil && written != n {
			err = io.ErrUnexpectedEOF
		}
		return true, err
	}
	return false, nil
}
