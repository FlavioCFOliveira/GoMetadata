// Package iobuf provides sync.Pool-backed reusable byte buffers for use in
// performance-critical parsing paths. All parsers that need temporary scratch
// space must obtain it from here rather than allocating directly.
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
// The caller must call Put when finished. The slice is at least n bytes long;
// it may be longer.
func Get(n int) *[]byte {
	if n > defaultSize {
		p := largePool.Get().(*[]byte)
		if cap(*p) < n {
			b := make([]byte, n)
			return &b
		}
		*p = (*p)[:n]
		return p
	}
	p := pool.Get().(*[]byte)
	if cap(*p) < n {
		b := make([]byte, n)
		return &b
	}
	*p = (*p)[:n]
	return p
}

// Put returns a buffer to the appropriate pool tier. The caller must not use
// the buffer after calling Put.
func Put(p *[]byte) {
	if p == nil {
		return
	}
	*p = (*p)[:cap(*p)]
	if cap(*p) > defaultSize {
		largePool.Put(p)
	} else {
		pool.Put(p)
	}
}
