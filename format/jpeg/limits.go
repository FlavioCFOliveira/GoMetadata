package jpeg

import (
	"io"
	"sync"
)

// countingReader — task #262 defense-in-depth hardening.
//
// Every sibling container package (webp, tiff, heif, dng, cr2, cr3, nef, arw,
// orf, rw2) enforces a 256 MiB aggregate maxFileSize cap on total input bytes
// (the #140 fix) by buffering the whole file through
// io.ReadAll(io.LimitReader(r, maxFileSize+1)) and rejecting the result if it
// exceeds maxFileSize. format/jpeg was the one exception: it streams the
// marker sequence incrementally (readSegment) rather than buffering the whole
// file, because a single APPn segment is already bounded to 65535 bytes by
// the JPEG 16-bit length field (ISO/IEC 10918-1 §B.1.1.4) — so the existing
// design carried no per-segment amplification risk. The round-4 production
// audit (2026-07-06) flagged this asymmetry as an INFO-level defense-in-depth
// gap: the codebase's security posture should be uniform across every
// container format, even where an individual package is not independently
// vulnerable.
//
// countingReader closes that gap without regressing the streaming design: it
// wraps the caller's io.ReadSeeker and tracks the cumulative number of bytes
// read since the most recent Seek call, returning ErrFileTooLarge once that
// count exceeds maxFileSize. Extract and Inject only ever Seek to a fixed,
// code-controlled offset (0, or 2 to skip past an already-validated SOI) —
// never to an offset derived from file content — so resetting the budget on
// every Seek cannot be exploited by an attacker to bypass the cap. It also
// correctly re-derives, for each logical pass over the stream (Inject runs an
// IRB pre-scan and then a separate main copy pass), the same bound that a
// single io.ReadAll(io.LimitReader(r, maxFileSize+1)) would have enforced on
// the file as a whole, without falsely rejecting a legitimate file merely
// because Inject happens to read parts of it more than once.
//
// countingReader is obtained from countingReaderPool so that wrapping a
// reader costs zero additional heap allocations on the fast path (Extract and
// Inject already run once per file; the pool amortises the wrapper's own
// allocation to nothing after warm-up, matching the sync.Pool convention used
// throughout this codebase — see internal/iobuf and the entrySlicePool /
// webpBufPool patterns).
type countingReader struct {
	r        io.ReadSeeker
	n        int64
	exceeded bool
}

// Read implements io.Reader. It delegates to the wrapped reader and returns
// ErrFileTooLarge — without performing any further underlying reads — once
// the cumulative byte count since the last Seek exceeds maxFileSize.
//
// The cap is checked AFTER accumulating the current read's byte count rather
// than before, so a read that lands exactly on the threshold is allowed to
// complete normally; only the NEXT read call is rejected. This mirrors the
// "read maxFileSize+1 bytes, then compare" idiom used by the sibling
// packages' io.LimitReader(r, maxFileSize+1) pattern, and avoids a subtle
// interaction with io.ReadFull/io.ReadAtLeast: if a Read call both satisfies
// the caller's requested length AND crosses the cap in the same call,
// io.ReadAtLeast discards the accompanying error (it only surfaces an error
// when fewer bytes than requested were read). Checking on the FOLLOWING call
// guarantees ErrFileTooLarge is always observed by the caller.
func (c *countingReader) Read(p []byte) (int, error) {
	if c.exceeded {
		return 0, ErrFileTooLarge
	}
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > maxFileSize {
		c.exceeded = true
	}
	// err (typically nil, or the wrapped reader's literal io.EOF) MUST be
	// returned unwrapped: this method implements the io.Reader contract, and
	// io.ReadFull/io.ReadAtLeast compare the returned error to io.EOF with
	// == (not errors.Is) to decide whether to convert a partial read at
	// end-of-stream into io.ErrUnexpectedEOF. copyNonMetadataSegments also
	// relies on errors.Is(err, io.EOF) to distinguish a clean end-of-stream
	// from a truncated/malformed one. Wrapping here would break both.
	return n, err //nolint:wrapcheck // io.Reader contract: callers require exact io.EOF identity, see comment above
}

// WriteTo implements io.WriterTo. writeSOS copies the compressed image data
// via io.Copy(w, r); io.Copy prefers src.WriteTo over dst.ReadFrom, which in
// turn is preferred over its generic 32 KiB-buffered copy loop. Without this
// method, wrapping r in countingReader would silently defeat that
// optimisation whenever the wrapped reader implements io.WriterTo itself
// (the common case: *bytes.Reader.WriteTo does one exact-sized Write call).
// io.Copy would then fall back to dst.(io.ReaderFrom) when the destination
// supports it — e.g. *bytes.Buffer.ReadFrom — which speculatively grows its
// backing array by at least 512 bytes (bytes.MinRead) per call regardless of
// how much data actually remains, needlessly inflating the destination
// buffer's capacity for what may be a copy of only a few bytes. Benchmarking
// (go test -bench=BenchmarkJPEGInject -benchmem) caught this: omitting
// WriteTo raised Inject from 376 to 1017 B/op purely from this lost fast
// path, with zero difference in the bytes actually written.
//
// The cap is still enforced and the copy is still bounded: a wrapped reader's
// own WriteTo method copies all remaining bytes in a single, uninterruptible
// call, so the fast path above is only taken once remainingFitsBudget has
// proven the remaining data cannot exceed maxFileSize on its own. Otherwise
// WriteTo falls back to the incremental, cap-checked Read-based copy, which
// stops partway through an oversized body exactly like the sibling packages'
// io.LimitReader-based guards stop partway through an oversized read.
func (c *countingReader) WriteTo(w io.Writer) (int64, error) {
	if c.exceeded {
		return 0, ErrFileTooLarge
	}
	if wt, ok := c.r.(io.WriterTo); ok && c.remainingFitsBudget() {
		n, err := wt.WriteTo(w)
		c.n += n
		if c.n > maxFileSize {
			// Cannot happen given remainingFitsBudget's guarantee; kept for
			// defence in depth in case a concurrent Seek raced this check.
			c.exceeded = true
			if err == nil {
				err = ErrFileTooLarge
			}
		}
		return n, err
	}
	// Either c.r does not implement io.WriterTo, or delegating to it could
	// copy more than the remaining budget in one uninterruptible call: fall
	// back to the generic, incremental io.Copy algorithm. readerOnly hides
	// countingReader's own WriteTo method from io.Copy's src.(WriterTo)
	// check so this does not recurse into itself; countingReader.Read still
	// enforces the cap on every chunk read, bounding the total bytes copied.
	return io.Copy(w, readerOnly{c}) //nolint:wrapcheck // io.WriterTo contract: io.Copy's error is already unwrapped
}

// remainingFitsBudget reports whether the number of bytes remaining to be
// read from c.r — from its current position to EOF — is small enough that
// delegating the whole copy to c.r's own io.WriterTo implementation cannot
// exceed the countingReader's remaining maxFileSize budget. This must be
// checked BEFORE using the fast path in WriteTo: unlike Read, a WriterTo
// implementation copies everything in one uninterruptible call, so there is
// no opportunity to stop partway through an oversized body once started.
//
// The remaining length is determined generically via two Seek calls
// (SeekCurrent, SeekEnd), then the original position is restored — safe
// because in practice this branch is only reached for readers that combine
// io.ReadSeeker with io.WriterTo (bytes.Reader, strings.Reader), for which
// Seek is O(1) and side-effect-free; *os.File does not implement
// io.WriterTo, so this cost is never paid for file-backed input. If any
// Seek fails, the check conservatively reports false so the caller falls
// back to the bounded, incremental copy path.
func (c *countingReader) remainingFitsBudget() bool {
	cur, err := c.r.Seek(0, io.SeekCurrent)
	if err != nil {
		return false
	}
	end, err := c.r.Seek(0, io.SeekEnd)
	if err != nil {
		return false
	}
	if _, err := c.r.Seek(cur, io.SeekStart); err != nil {
		// The reader may now be positioned at EOF instead of its original
		// offset, and there is no way to recover that position. Treat this
		// as a broken io.ReadSeeker and conservatively refuse the fast path.
		return false
	}
	remaining := end - cur
	budget := maxFileSize - c.n
	return remaining <= budget
}

// readerOnly narrows an io.Reader to exactly the io.Reader interface,
// hiding any WriteTo/ReadFrom fast-path methods the concrete value might
// otherwise promote. Used solely by countingReader.WriteTo's fallback branch
// to prevent io.Copy from re-entering countingReader.WriteTo.
type readerOnly struct{ io.Reader }

// Seek implements io.Seeker. On success it resets the byte-count budget and
// the exceeded flag, starting a fresh maxFileSize allowance for the next pass
// over the stream. See the countingReader doc comment for why this reset is
// safe: Seek targets in this package are always fixed, code-controlled
// offsets, never attacker-influenced ones.
func (c *countingReader) Seek(offset int64, whence int) (int64, error) {
	pos, err := c.r.Seek(offset, whence)
	if err == nil {
		c.n = 0
		c.exceeded = false
	}
	// Returned unwrapped for the same reason as Read above: this method
	// implements the io.Seeker contract and must preserve the underlying
	// error's identity for callers that compare it directly.
	return pos, err //nolint:wrapcheck // io.Seeker contract: preserve underlying error identity
}

// countingReaderPool recycles *countingReader values across Extract/Inject
// calls so that wrapping the caller's io.ReadSeeker allocates nothing on the
// fast path once the pool is warm.
var countingReaderPool = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse avoids a heap allocation per Extract/Inject call
	New: func() any { return &countingReader{} },
}

// getCountingReader returns a pooled *countingReader wrapping r with its
// budget counter reset to zero. The caller must call putCountingReader when
// done with it.
func getCountingReader(r io.ReadSeeker) *countingReader {
	cr := countingReaderPool.Get().(*countingReader) //nolint:forcetypeassert,revive // countingReaderPool.New always stores *countingReader; pool invariant
	cr.r = r
	cr.n = 0
	cr.exceeded = false
	return cr
}

// putCountingReader clears the wrapped reader reference (avoiding a pinned
// reference to the caller's io.ReadSeeker) and returns cr to the pool. The
// caller must not use cr after this call.
func putCountingReader(cr *countingReader) {
	cr.r = nil
	countingReaderPool.Put(cr)
}
