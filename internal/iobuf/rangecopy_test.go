package iobuf

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestCopyRange(t *testing.T) {
	t.Parallel()
	data := make([]byte, 3*StreamCopyBufSize+17)
	for i := range data {
		data[i] = byte(i * 7)
	}
	for _, tc := range []struct{ off, n int64 }{
		{0, 0}, {5, 0}, {0, 1}, {0, int64(len(data))}, {13, StreamCopyBufSize}, {StreamCopyBufSize - 1, 2},
		{int64(len(data)) - 3, 3}, {7, -4},
	} {
		var out bytes.Buffer
		if err := CopyRange(bytes.NewReader(data), &out, tc.off, tc.n); err != nil {
			t.Fatalf("CopyRange(%d, %d): %v", tc.off, tc.n, err)
		}
		want := []byte(nil)
		if tc.n > 0 {
			want = data[tc.off : tc.off+tc.n]
		}
		if !bytes.Equal(out.Bytes(), want) {
			t.Errorf("CopyRange(%d, %d): wrong bytes", tc.off, tc.n)
		}
	}
	// A source that ends early: an error, never a short success or a hang.
	var out bytes.Buffer
	if err := CopyRange(bytes.NewReader(data[:100]), &out, 50, 100); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short source: err = %v, want io.ErrUnexpectedEOF", err)
	}
	if err := CopyRange(bytes.NewReader(data), &out, -1, 10); err == nil {
		t.Error("negative offset: expected an error")
	}
}

func TestWindowPeek(t *testing.T) {
	t.Parallel()
	data := make([]byte, 3*windowSize+100)
	for i := range data {
		data[i] = byte(i)
	}
	w := NewWindow(bytes.NewReader(data), int64(len(data)))
	defer w.Close()
	for _, tc := range []struct {
		off     int64
		n, want int
	}{
		{0, 8, 8}, {4, 16, 16}, {windowSize - 4, 16, 16}, {int64(len(data)) - 3, 16, 3},
		{int64(len(data)), 4, 0}, {-1, 4, 0}, {10, 0, 0}, {2 * windowSize, windowSize, windowSize},
	} {
		got, err := w.Peek(tc.off, tc.n)
		if err != nil {
			t.Fatalf("Peek(%d, %d): %v", tc.off, tc.n, err)
		}
		if len(got) != tc.want {
			t.Fatalf("Peek(%d, %d): %d bytes, want %d", tc.off, tc.n, len(got), tc.want)
		}
		if tc.want > 0 && !bytes.Equal(got, data[tc.off:tc.off+int64(tc.want)]) {
			t.Errorf("Peek(%d, %d): wrong bytes", tc.off, tc.n)
		}
	}
	// A source shorter than its declared size is an error, not a short read.
	short := NewWindow(bytes.NewReader(data[:50]), int64(len(data)))
	defer short.Close()
	if _, err := short.Peek(10, 8); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short source: err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestIndexRange(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte{'.'}, 2*StreamCopyBufSize+500)
	pat := []byte("NEEDLE")
	// The needle at the start, across every read boundary, at the end, and none.
	for _, at := range []int{0, 1, StreamCopyBufSize - 5, StreamCopyBufSize - 3, StreamCopyBufSize, StreamCopyBufSize + 1, 2*StreamCopyBufSize - 6, len(data) - 6} {
		buf := bytes.Clone(data)
		copy(buf[at:], pat)
		got, err := IndexRange(bytes.NewReader(buf), 0, int64(len(buf)), pat)
		if err != nil || got != int64(at) {
			t.Errorf("needle at %d: got %d, %v", at, got, err)
		}
		// From an offset after it: not found; before it: found.
		if got, _ := IndexRange(bytes.NewReader(buf), int64(at)+1, int64(len(buf)), pat); got != -1 {
			t.Errorf("needle at %d, searched after it: got %d", at, got)
		}
		// A needle cut by the end of the range is not found.
		if got, _ := IndexRange(bytes.NewReader(buf), 0, int64(at)+3, pat); got != -1 {
			t.Errorf("needle at %d, range ends inside it: got %d", at, got)
		}
	}
	if got, err := IndexRange(bytes.NewReader(data), 0, int64(len(data)), pat); err != nil || got != -1 {
		t.Errorf("no needle: got %d, %v", got, err)
	}
	if got, _ := IndexRange(bytes.NewReader(data), 0, 3, pat); got != -1 {
		t.Errorf("range shorter than the pattern: got %d", got)
	}
	// The first of two.
	two := bytes.Clone(data)
	copy(two[100:], pat)
	copy(two[StreamCopyBufSize+9:], pat)
	if got, _ := IndexRange(bytes.NewReader(two), 0, int64(len(two)), pat); got != 100 {
		t.Errorf("first of two: got %d", got)
	}
	// A source that ends before the range does.
	if _, err := IndexRange(bytes.NewReader(data[:1000]), 0, int64(len(data)), pat); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short source: err = %v, want io.ErrUnexpectedEOF", err)
	}
	if _, err := IndexRange(bytes.NewReader(data), 0, int64(len(data)), make([]byte, StreamCopyBufSize)); !errors.Is(err, ErrPatternTooLong) {
		t.Errorf("long pattern: err = %v, want ErrPatternTooLong", err)
	}
}

func FuzzIndexRange(f *testing.F) {
	f.Add([]byte("abcNEEDLEdef"), []byte("NEEDLE"), uint16(0), uint16(12))
	f.Add(bytes.Repeat([]byte{'a'}, 70000), []byte("aab"), uint16(5), uint16(60000))
	f.Fuzz(func(t *testing.T, data, pat []byte, from, to uint16) {
		if len(pat) == 0 || len(pat) > 64 {
			return
		}
		lo, hi := int(from)%(len(data)+1), int(to)%(len(data)+1)
		if lo > hi {
			lo, hi = hi, lo
		}
		want := int64(-1)
		if i := bytes.Index(data[lo:hi], pat); i >= 0 {
			want = int64(lo + i)
		}
		got, err := IndexRange(bytes.NewReader(data), int64(lo), int64(hi), pat)
		if err != nil || got != want {
			t.Fatalf("IndexRange(%d, %d) = %d, %v; want %d", lo, hi, got, err, want)
		}
	})
}
