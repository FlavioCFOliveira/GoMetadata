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
