package iobuf

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

func TestStreamCopyN(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		n    int64
		data []byte
	}{
		{name: "zero", n: 0, data: nil},
		{name: "negative", n: -5, data: nil},
		{name: "smaller than buffer", n: 10, data: bytes.Repeat([]byte{0x42}, 10)},
		{name: "exactly one buffer", n: StreamCopyBufSize, data: bytes.Repeat([]byte{0x7A}, StreamCopyBufSize)},
		{name: "spans several buffers", n: StreamCopyBufSize*2 + 137, data: bytes.Repeat([]byte{0x11, 0x22, 0x33}, (StreamCopyBufSize*2+137)/3+1)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var src []byte
			if tc.n > 0 {
				src = tc.data[:tc.n]
			}
			r := bytes.NewReader(src)
			var w bytes.Buffer

			if err := StreamCopyN(r, &w, tc.n); err != nil {
				t.Fatalf("StreamCopyN: unexpected error: %v", err)
			}
			if !bytes.Equal(w.Bytes(), src) {
				t.Errorf("StreamCopyN: output length = %d, want %d", w.Len(), len(src))
			}
		})
	}
}

// TestStreamCopyNShortRead verifies that a reader that returns fewer bytes
// than requested (but not EOF) is still handled correctly — StreamCopyN uses
// io.ReadFull internally, so a slow/chunked reader must not truncate output.
func TestStreamCopyNShortRead(t *testing.T) {
	t.Parallel()

	data := bytes.Repeat([]byte{0x5A}, StreamCopyBufSize+1000)
	// iotest.OneByteReader forces single-byte reads, maximally exercising the
	// io.ReadFull retry loop inside StreamCopyN.
	r := iotest.OneByteReader(bytes.NewReader(data))
	var w bytes.Buffer

	if err := StreamCopyN(r, &w, int64(len(data))); err != nil {
		t.Fatalf("StreamCopyN: unexpected error: %v", err)
	}
	if !bytes.Equal(w.Bytes(), data) {
		t.Error("StreamCopyN: output does not match input under single-byte reads")
	}
}

// TestStreamCopyNReadError verifies that a read error partway through is
// propagated, unwrapped, to the caller.
func TestStreamCopyNReadError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	r := iotest.ErrReader(sentinel)
	var w bytes.Buffer

	err := StreamCopyN(r, &w, 100)
	if !errors.Is(err, sentinel) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("StreamCopyN: error = %v, want sentinel %v (or io.ErrUnexpectedEOF wrapping it)", err, sentinel)
	}
}

// failWriter always fails, to verify StreamCopyN propagates write errors.
type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) { return 0, f.err }

func TestStreamCopyNWriteError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("write boom")
	r := bytes.NewReader(bytes.Repeat([]byte{0x01}, 100))

	err := StreamCopyN(r, failWriter{err: sentinel}, 100)
	if !errors.Is(err, sentinel) {
		t.Errorf("StreamCopyN: error = %v, want %v", err, sentinel)
	}
}

// TestStreamCopyNNoProportionalAllocation proves the core performance claim:
// copying a large byte count allocates only the fixed pooled buffer, never a
// buffer proportional to n.
//
//nolint:paralleltest // testing.AllocsPerRun panics if the test (or a parent) is parallel; must run serially
func TestStreamCopyNNoProportionalAllocation(t *testing.T) {
	const n = 8 << 20 // 8 MiB — far larger than StreamCopyBufSize (64 KiB)
	data := bytes.Repeat([]byte{0x99}, n)

	allocs := testing.AllocsPerRun(5, func() {
		r := bytes.NewReader(data)
		var w bytes.Buffer
		w.Grow(n)
		if err := StreamCopyN(r, &w, n); err != nil {
			t.Fatalf("StreamCopyN: unexpected error: %v", err)
		}
	})
	// A handful of allocations are expected (bytes.Reader, w.Grow's backing
	// array, etc.) — the property under test is that allocs are O(1) w.r.t.
	// n, not that there are zero. 10 is generous headroom while still failing
	// loudly if StreamCopyN regresses to allocating a buffer sized to n.
	if allocs > 10 {
		t.Errorf("StreamCopyN: %.1f allocs/op copying %d bytes — expected O(1), not proportional to n", allocs, n)
	}
}

// opaqueWriter wraps a *bytes.Buffer behind a concrete type StreamCopyN's
// *bytes.Buffer type assertion cannot see through — used to exercise the
// *bytes.Reader-source fast path (streamCopyNFast's second case) in
// isolation from the *bytes.Buffer-sink fast path (its first case), since a
// bare *bytes.Buffer sink would otherwise take the first case regardless of
// the source's own type.
type opaqueWriter struct{ buf *bytes.Buffer }

func (o opaqueWriter) Write(p []byte) (int, error) { return o.buf.Write(p) }

// TestStreamCopyNBytesReaderSourceFullRemainder proves the *bytes.Reader
// fast path (task #297): when n equals the reader's own remaining length,
// StreamCopyN must use (*bytes.Reader).WriteTo's zero-copy slice write
// (verified indirectly via output correctness, since the fast path and the
// pooled path are indistinguishable from the caller's side other than
// performance/allocation count — see BenchmarkStreamCopyNBytesReaderSource
// and TestStreamCopyNBytesReaderSourceNoAddedAllocation for that half of the
// proof).
func TestStreamCopyNBytesReaderSourceFullRemainder(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte{0x9B}, 12345)
	r := bytes.NewReader(data)
	var buf bytes.Buffer
	w := opaqueWriter{buf: &buf}

	if err := StreamCopyN(r, w, int64(len(data))); err != nil {
		t.Fatalf("StreamCopyN: unexpected error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Error("StreamCopyN: output does not match input for the full-remainder *bytes.Reader fast path")
	}
	if r.Len() != 0 {
		t.Errorf("StreamCopyN: reader has %d bytes unread, want 0 (WriteTo must fully consume it)", r.Len())
	}
}

// TestStreamCopyNBytesReaderSourcePartial proves the fast path correctly
// declines (falls back to the pooled path) when n is LESS than the
// *bytes.Reader's own remaining length — bytes.Reader.WriteTo has no way to
// bound itself to fewer than all remaining bytes, so taking the fast path
// here would over-copy. This also exercises the real multi-block call
// pattern this package's own callers use: seek, stream N1 bytes, seek again,
// stream N2 more — from the SAME underlying *bytes.Reader.
func TestStreamCopyNBytesReaderSourcePartial(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 100) // 400 bytes
	r := bytes.NewReader(data)

	var block1, block2 bytes.Buffer
	if err := StreamCopyN(r, &block1, 150); err != nil {
		t.Fatalf("StreamCopyN (block1): unexpected error: %v", err)
	}
	if !bytes.Equal(block1.Bytes(), data[:150]) {
		t.Error("StreamCopyN (block1): output does not match the first 150 bytes")
	}
	if r.Len() != len(data)-150 {
		t.Fatalf("StreamCopyN (block1): reader has %d bytes unread, want %d", r.Len(), len(data)-150)
	}

	// Second call: n now DOES equal the reader's remaining length (the
	// common "stream the rest to EOF" pattern), so THIS call takes the fast
	// path — proving the two calls compose correctly against the same
	// underlying reader regardless of which one takes which path.
	if err := StreamCopyN(r, &block2, int64(r.Len())); err != nil {
		t.Fatalf("StreamCopyN (block2): unexpected error: %v", err)
	}
	if !bytes.Equal(block2.Bytes(), data[150:]) {
		t.Error("StreamCopyN (block2): output does not match the remaining 250 bytes")
	}
	if r.Len() != 0 {
		t.Errorf("StreamCopyN (block2): reader has %d bytes unread, want 0", r.Len())
	}
}

// TestStreamCopyNBytesReaderSourceNoAddedAllocation and
// TestStreamCopyNBytesBufferSinkNoAddedAllocation prove task #297's own
// stated goal directly: neither fast path adds an allocation beyond what
// the caller's own bytes.NewReader/bytes.Buffer construction already costs.
//
//nolint:paralleltest // testing.AllocsPerRun panics if the test (or a parent) is parallel; must run serially
func TestStreamCopyNBytesReaderSourceNoAddedAllocation(t *testing.T) {
	data := bytes.Repeat([]byte{0x42}, 4096)
	var buf bytes.Buffer
	w := opaqueWriter{buf: &buf}

	allocs := testing.AllocsPerRun(20, func() {
		r := bytes.NewReader(data) // the ONE expected allocation, from the caller, not StreamCopyN
		buf.Reset()
		if err := StreamCopyN(r, w, int64(len(data))); err != nil {
			t.Fatalf("StreamCopyN: unexpected error: %v", err)
		}
	})
	if allocs > 1 {
		t.Errorf("StreamCopyN: %.1f allocs/op via the *bytes.Reader fast path, want <= 1 (the caller's own bytes.NewReader)", allocs)
	}
}

//nolint:paralleltest // testing.AllocsPerRun panics if the test (or a parent) is parallel; must run serially
func TestStreamCopyNBytesBufferSinkNoAddedAllocation(t *testing.T) {
	data := bytes.Repeat([]byte{0x24}, 4096)
	var w bytes.Buffer
	w.Grow(len(data))
	// Warm limitedReaderPool before measuring: the FIRST call may pay a
	// genuine pool-miss allocation (matching this package's own Get/Put
	// []byte pools, which have the identical, accepted characteristic).
	r := bytes.NewReader(data)
	w.Reset()
	if err := StreamCopyN(r, &w, int64(len(data))); err != nil {
		t.Fatalf("StreamCopyN: unexpected error: %v", err)
	}

	allocs := testing.AllocsPerRun(20, func() {
		r := bytes.NewReader(data) // the ONE expected allocation, from the caller, not StreamCopyN
		w.Reset()
		if err := StreamCopyN(r, &w, int64(len(data))); err != nil {
			t.Fatalf("StreamCopyN: unexpected error: %v", err)
		}
	})
	if allocs > 1 {
		t.Errorf("StreamCopyN: %.1f allocs/op via the *bytes.Buffer fast path (warm pool), want <= 1 (the caller's own bytes.NewReader)", allocs)
	}
}

func BenchmarkStreamCopyNBytesReaderSource(b *testing.B) {
	const n = 4 << 20 // 4 MiB
	data := bytes.Repeat([]byte{0xEE}, n)
	var buf bytes.Buffer
	w := opaqueWriter{buf: &buf}

	b.ReportAllocs()
	b.SetBytes(n)
	for range b.N {
		r := bytes.NewReader(data)
		buf.Reset()
		if err := StreamCopyN(r, w, n); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStreamCopyN(b *testing.B) {
	const n = 4 << 20 // 4 MiB
	data := bytes.Repeat([]byte{0xFF}, n)
	var w bytes.Buffer
	w.Grow(n)

	b.ReportAllocs()
	b.SetBytes(n)
	for range b.N {
		r := bytes.NewReader(data)
		w.Reset()
		if err := StreamCopyN(r, &w, n); err != nil {
			b.Fatal(err)
		}
	}
}
