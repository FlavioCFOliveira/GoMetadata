package testutil

import (
	"errors"
	"io"
	"sort"
)

var (
	errWhence      = errors.New("testutil: invalid whence")
	errNegativePos = errors.New("testutil: negative position")
)

// Patch is a run of bytes of a SparseFile.
type Patch struct {
	Off  int64
	Data []byte
}

// SparseFile is an io.ReadSeeker over a virtual file of Total bytes that are
// zero except for the patches: a test can present a file of hundreds of
// megabytes without holding it in memory. It is a test fixture (the streaming
// writers of tasks #390-#393 must handle files larger than any in-memory
// limit); it is not safe for concurrent use.
type SparseFile struct {
	Total   int64
	patches []Patch
	pos     int64
}

// NewSparseFile returns a SparseFile of total bytes with the patches applied
// in the order given (a later patch wins where two overlap).
func NewSparseFile(total int64, patches ...Patch) *SparseFile {
	p := append([]Patch(nil), patches...)
	sort.SliceStable(p, func(i, j int) bool { return p[i].Off < p[j].Off })
	return &SparseFile{Total: total, patches: p}
}

// Read implements io.Reader.
func (s *SparseFile) Read(b []byte) (int, error) {
	if s.pos >= s.Total {
		return 0, io.EOF
	}
	n := int(min(int64(len(b)), s.Total-s.pos))
	b = b[:n]
	clear(b)
	for _, p := range s.patches {
		lo, hi := max(p.Off, s.pos), min(p.Off+int64(len(p.Data)), s.pos+int64(n))
		if lo < hi {
			copy(b[lo-s.pos:hi-s.pos], p.Data[lo-p.Off:hi-p.Off])
		}
	}
	s.pos += int64(n)
	return n, nil
}

// Seek implements io.Seeker.
func (s *SparseFile) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = s.pos + offset
	case io.SeekEnd:
		abs = s.Total + offset
	default:
		return 0, errWhence
	}
	if abs < 0 {
		return 0, errNegativePos
	}
	s.pos = abs
	return abs, nil
}

// CountingWriter discards what is written to it and counts the bytes.
type CountingWriter struct{ N int64 }

// Write implements io.Writer.
func (c *CountingWriter) Write(b []byte) (int, error) {
	c.N += int64(len(b))
	return len(b), nil
}
