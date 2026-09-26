package iobuf

import (
	"errors"
	"io"
)

// ErrShortFirstWrite is returned by MagicWriter.Write when the first write
// carries fewer than 4 bytes, so bytes [2:4] of the stream cannot be patched.
var ErrShortFirstWrite = errors.New("iobuf: first write shorter than 4 bytes")

// MagicWriter forwards a byte stream to W with bytes [2:4] replaced by Magic.
// TIFF-based RAW containers (ORF, RW2) use it to restore their non-standard
// magic on the output of the standard TIFF writer without buffering the whole
// output. The slices passed to Write are never modified.
//
// The first Write must carry at least 4 bytes; later writes pass through
// unchanged. A MagicWriter must not be copied after first use.
type MagicWriter struct {
	// W is the destination writer.
	W io.Writer
	// Magic replaces bytes [2:4] of the stream.
	Magic [2]byte
	// Err records the first error returned by W, so callers can distinguish
	// a destination failure from other failures of the producer.
	Err error

	hdr     [4]byte
	started bool
}

// Write implements io.Writer.
func (m *MagicWriter) Write(p []byte) (int, error) {
	if m.started {
		return m.forward(p)
	}
	if len(p) < 4 {
		return 0, ErrShortFirstWrite
	}
	m.started = true
	m.hdr = [4]byte{p[0], p[1], m.Magic[0], m.Magic[1]}
	n, err := m.forward(m.hdr[:])
	if err != nil {
		return n, err
	}
	rest, err := m.forward(p[4:])
	return n + rest, err
}

// Started reports whether the first write has been forwarded.
func (m *MagicWriter) Started() bool { return m.started }

// forward writes p to W and records the first destination error.
func (m *MagicWriter) forward(p []byte) (int, error) {
	n, err := m.W.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	if err != nil && m.Err == nil {
		m.Err = err
	}
	return n, err
}
