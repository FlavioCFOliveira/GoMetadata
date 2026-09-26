package iobuf

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// onlyReader hides every method except Read.
type onlyReader struct{ r io.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }

// failSeeker is a non-seekable io.ReadSeeker: every Seek fails.
type failSeeker struct{ onlyReader }

func (failSeeker) Seek(int64, int) (int64, error) { return 0, errors.New("not seekable") }

// countingSeeker counts Read calls to prove the sized path reads once.
type countingSeeker struct {
	*bytes.Reader
	reads int
}

func (c *countingSeeker) Read(p []byte) (int, error) {
	c.reads++
	return c.Reader.Read(p)
}

func TestReadAll(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte{0xAB}, 1000)

	cases := []struct {
		name    string
		r       io.ReadSeeker
		limit   int64
		want    []byte
		wantErr error
	}{
		{"seekable_under_limit", bytes.NewReader(data), 1000, data, nil},
		{"seekable_over_limit", bytes.NewReader(data), 999, nil, ErrTooLarge},
		{"seekable_empty", bytes.NewReader(nil), 10, []byte{}, nil},
		{"nonseekable_under_limit", failSeeker{onlyReader{bytes.NewReader(data)}}, 1000, data, nil},
		{"nonseekable_over_limit", failSeeker{onlyReader{bytes.NewReader(data)}}, 999, nil, ErrTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadAll(tc.r, tc.limit)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && !bytes.Equal(got, tc.want) {
				t.Fatalf("got %d bytes, want %d", len(got), len(tc.want))
			}
		})
	}
}

// TestReadAllFromCurrentPosition verifies ReadAll reads from the current
// position, not from the start.
func TestReadAllFromCurrentPosition(t *testing.T) {
	t.Parallel()
	r := bytes.NewReader([]byte("0123456789"))
	if _, err := r.Seek(4, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := ReadAll(r, 100)
	if err != nil || string(got) != "456789" {
		t.Fatalf("ReadAll = %q, %v; want \"456789\", nil", got, err)
	}
}

// TestReadAllSizedSingleAllocation verifies the seekable path allocates a
// buffer of exactly the input size, filled without regrowth.
func TestReadAllSizedSingleAllocation(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte{1}, 1<<16)
	c := &countingSeeker{Reader: bytes.NewReader(data)}
	got, err := ReadAll(c, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(data) || cap(got) != len(data) {
		t.Fatalf("len=%d cap=%d, want both %d", len(got), cap(got), len(data))
	}
	if c.reads != 1 {
		t.Fatalf("reads = %d, want 1", c.reads)
	}
}

// TestReadAllOverLimitDoesNotRead verifies the size guard rejects a large
// seekable input before reading or allocating for it.
func TestReadAllOverLimitDoesNotRead(t *testing.T) {
	t.Parallel()
	c := &countingSeeker{Reader: bytes.NewReader(make([]byte, 4096))}
	if _, err := ReadAll(c, 4095); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if c.reads != 0 {
		t.Fatalf("reads = %d, want 0", c.reads)
	}
}

func BenchmarkReadAll(b *testing.B) {
	data := make([]byte, 16<<20)
	r := bytes.NewReader(data)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for range b.N {
		_, _ = r.Seek(0, io.SeekStart)
		_, _ = ReadAll(r, 256<<20)
	}
}
