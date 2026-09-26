package iobuf

import (
	"bytes"
	"errors"
	"testing"
)

// errWriter fails every Write.
type errWriter struct{ err error }

func (e errWriter) Write([]byte) (int, error) { return 0, e.err }

func TestMagicWriterPatchesWithoutMutatingInput(t *testing.T) {
	t.Parallel()
	in := []byte{'I', 'I', 0x2A, 0x00, 1, 2, 3}
	orig := bytes.Clone(in)
	var out bytes.Buffer
	mw := &MagicWriter{W: &out, Magic: [2]byte{'R', 'O'}}
	n, err := mw.Write(in)
	if err != nil || n != len(in) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if _, err := mw.Write([]byte{4, 5}); err != nil {
		t.Fatal(err)
	}
	want := []byte{'I', 'I', 'R', 'O', 1, 2, 3, 4, 5}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("out = %v, want %v", out.Bytes(), want)
	}
	if !bytes.Equal(in, orig) {
		t.Fatal("MagicWriter mutated the caller's slice")
	}
	if !mw.Started() {
		t.Fatal("Started() = false after a write")
	}
}

func TestMagicWriterShortFirstWrite(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	mw := &MagicWriter{W: &out}
	if _, err := mw.Write([]byte{1, 2, 3}); !errors.Is(err, ErrShortFirstWrite) {
		t.Fatalf("err = %v, want ErrShortFirstWrite", err)
	}
	if out.Len() != 0 || mw.Started() {
		t.Fatal("short first write must not reach the destination")
	}
}

func TestMagicWriterRecordsDestinationError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("disk full")
	mw := &MagicWriter{W: errWriter{sentinel}}
	if _, err := mw.Write([]byte{1, 2, 3, 4, 5}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
	if !errors.Is(mw.Err, sentinel) {
		t.Fatalf("Err = %v, want %v", mw.Err, sentinel)
	}
}
