// Package iobuf provides sync.Pool-backed reusable byte buffers for use in
// performance-critical parsing paths. All parsers that need temporary scratch
// space must obtain it from here rather than allocating directly.
package iobuf

import "sync"

const defaultSize = 4096

var pool = sync.Pool{
	New: func() any {
		b := make([]byte, defaultSize)
		return &b
	},
}

// Get returns a pointer to a byte slice from the pool. The caller must call
// Put when finished. The slice is at least n bytes long; it may be longer.
func Get(n int) *[]byte {
	p := pool.Get().(*[]byte)
	if cap(*p) < n {
		b := make([]byte, n)
		return &b
	}
	*p = (*p)[:n]
	return p
}

// Put returns a buffer to the pool. The caller must not use the buffer after
// calling Put.
func Put(p *[]byte) {
	if p == nil {
		return
	}
	*p = (*p)[:cap(*p)]
	pool.Put(p)
}
