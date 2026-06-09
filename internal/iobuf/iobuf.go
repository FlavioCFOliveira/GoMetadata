// Package iobuf provides sync.Pool-backed reusable byte buffers for use in
// performance-critical parsing paths. All parsers that need temporary scratch
// space must obtain it from here rather than allocating directly.
//
// # Buffer contract
//
// Get does NOT zero the returned buffer. Callers that need a clean buffer
// must zero it themselves (e.g. with the built-in clear, bytes.Equal, or a
// manual loop). This matches the pattern used by writeIFD in exif/ifd.go,
// which calls clear() on the pooled slice before encoding entries —
// preventing stale bytes from leaking across encode calls.
//
// Callers must not read from or write to a buffer after calling Put. The
// pointer is returned to the pool and may be handed out to another goroutine
// immediately.
package iobuf

import "sync"

const defaultSize = 4096
const largeSize = 65536

// pool serves buffers up to defaultSize bytes.
var pool = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
	New: func() any {
		b := make([]byte, defaultSize)
		return &b
	},
}

// largePool serves buffers between defaultSize+1 and largeSize bytes.
// Kept separate to prevent large buffers (EXIF segments, extended-XMP chunks)
// from polluting the small-buffer pool and wasting memory.
var largePool = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
	New: func() any {
		b := make([]byte, largeSize)
		return &b
	},
}

// Get returns a pointer to a byte slice from the appropriate pool tier.
// The returned slice has len == max(n,0) and cap >= max(n,0).
//
// Contract: the buffer is NOT zeroed. Callers must zero it when needed.
// See package-level documentation for the full buffer contract.
//
// The caller must call Put when finished. After Put the pointer must not
// be used.
func Get(n int) *[]byte {
	if n < 0 {
		// Clamp negative requests to zero rather than panicking. Callers that
		// pass a computed size should not receive a panic; an empty slice is the
		// correct defensive return.
		n = 0
	}
	if n > defaultSize {
		p := largePool.Get().(*[]byte) //nolint:forcetypeassert,revive // largePool.New always stores *[]byte; pool invariant
		if cap(*p) < n {
			// The pooled buffer is too small for the request. Return it to the
			// pool before allocating a fresh one — otherwise the pool slot is
			// permanently lost, silently eroding pool hit-rate (#186).
			largePool.Put(p)
			b := make([]byte, n)
			return &b
		}
		*p = (*p)[:n]
		return p
	}
	p := pool.Get().(*[]byte) //nolint:forcetypeassert,revive // pool.New always stores *[]byte; pool invariant
	if cap(*p) < n {
		// Same as above: return the undersized buffer before allocating (#186).
		pool.Put(p)
		b := make([]byte, n)
		return &b
	}
	*p = (*p)[:n]
	return p
}

// Put returns a buffer to the appropriate pool tier. The caller must not use
// the buffer after calling Put.
//
// Buffers whose capacity exceeds the canonical tier cap (largeSize = 65536)
// are discarded rather than pooled to prevent unbounded pool growth. A buffer
// that was originally from the small pool but grew via append is also discarded
// if its cap now exceeds largeSize; this closes the channel by which a single
// runaway append could contaminate the pool with an arbitrarily large buffer.
func Put(p *[]byte) {
	if p == nil {
		return
	}
	c := cap(*p)
	// Discard buffers that grew beyond the large-tier canonical cap. Retaining
	// them would allow a single large encode to permanently enlarge the pool,
	// defeating the memory-budget goal of the two-tier design.
	if c > largeSize {
		return
	}
	*p = (*p)[:c]
	if c > defaultSize {
		largePool.Put(p)
	} else {
		pool.Put(p)
	}
}
