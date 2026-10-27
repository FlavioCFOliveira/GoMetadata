package webp

// oom_gate_test.go — memory gates of Inject (#140, reworked by task #391).
//
// Inject never holds the file in memory: it reads the chunk headers of the
// source and streams every image chunk from the source to the destination.
// These tests check that no size limit but the 4 GiB of the RIFF size field
// applies, that memory use does not depend on the size of the file, and that a
// source that cannot deliver what it promised ends the write with an error.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// sparseWebP returns a WebP file of total bytes: a VP8X chunk and one 'ANMF'
// chunk of zero bytes that fills the file (a chunk is at most 2 GiB; with
// more than one filler the rest goes into further chunks).
func sparseWebP(total int64, fillers int) *testutil.SparseFile {
	var head bytes.Buffer
	head.WriteString("RIFF")
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(total-8)) //nolint:gosec // G115: test fixture, the field is only a hint
	head.Write(size[:])
	head.WriteString("WEBP")
	writeRIFFChunk(&head, fourCCVP8X, vp8xPayloadFlags(0x10))
	patches := make([]testutil.Patch, 1, 1+fillers)
	patches[0] = testutil.Patch{Off: 0, Data: head.Bytes()}
	pos := int64(head.Len())
	remaining := total - pos
	for i := range fillers {
		n := (remaining - int64(fillers-i)*8) / int64(fillers-i)
		n &^= 1 // keep every chunk word aligned
		hdr := make([]byte, 8)
		copy(hdr, "ANMF")
		binary.LittleEndian.PutUint32(hdr[4:], uint32(n)) //nolint:gosec // G115: n < 2 GiB by construction
		patches = append(patches, testutil.Patch{Off: pos, Data: hdr})
		pos += 8 + n
		remaining -= 8 + n
	}
	return testutil.NewSparseFile(total, patches...)
}

// TestInjectLargerThanFormerCap: a 300 MiB source (the former 256 MiB cap)
// is written, to the byte count the format gives, and the memory the write
// allocates does not grow with the file.
//
//nolint:paralleltest // measures the heap of the process: no other test may allocate meanwhile
func TestInjectLargerThanFormerCap(t *testing.T) {
	const total = 300 << 20
	src := sparseWebP(total, 1)
	exif := []byte("exif-payload-odd")
	xmp := []byte("<x:xmpmeta/>")

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var out testutil.CountingWriter
	if err := Inject(src, &out, exif, nil, xmp, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	runtime.ReadMemStats(&after)

	// The VP8X chunk is kept (the flags gain EXIF and XMP), the filler chunk
	// is copied, and the EXIF and XMP chunks are appended.
	want := int64(12) + chunkLen(vp8xChunkSize) + chunkLen(total-12-chunkLen(vp8xChunkSize)-8) +
		chunkLen(int64(len(exif))) + chunkLen(int64(len(xmp)))
	if out.N != want {
		t.Errorf("output = %d bytes, want %d", out.N, want)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 1<<20 {
		t.Errorf("Inject of a %d MiB file allocated %d bytes, want < 1 MiB", total>>20, grown)
	}
}

// TestInjectRIFFSizeFieldOverflow: an output larger than the 32-bit RIFF size
// field is refused with ErrFileTooLarge before anything is written.
func TestInjectRIFFSizeFieldOverflow(t *testing.T) {
	t.Parallel()
	const total = 4<<30 + 64<<20
	src := sparseWebP(total, 3)
	var out testutil.CountingWriter
	err := Inject(src, &out, []byte("x"), nil, nil, true)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Inject: err = %v, want ErrFileTooLarge", err)
	}
	if out.N != 0 {
		t.Errorf("%d bytes were written before the refusal, want none", out.N)
	}
}

// shortReader claims a size through Seek(SeekEnd) but delivers only data: the
// source of a file that was truncated while it was being written.
type shortReader struct {
	data  []byte
	claim int64
	pos   int64
}

func (s *shortReader) Read(p []byte) (int, error) {
	if s.pos >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += int64(n)
	return n, nil
}

func (s *shortReader) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = off
	case io.SeekCurrent:
		s.pos += off
	case io.SeekEnd:
		s.pos = s.claim + off
	}
	return s.pos, nil
}

// TestInjectTruncatedSource: a source that ends before the size it reported
// ends the write with an error, at every point of the file, without a hang or
// a panic (security: untrusted sizes are bounds checked).
func TestInjectTruncatedSource(t *testing.T) {
	t.Parallel()
	full := webpOf(rawChunk{"VP8X", vp8xPayloadFlags(0x10)}, rawChunk{"VP8 ", vp8Payload}, rawChunk{"ANMF", bytes.Repeat([]byte{7}, 100000)})
	for _, cut := range []int{12, 20, 30, 40, 50, 60, 1000, 50000, len(full) - 1} {
		src := &shortReader{data: full[:cut], claim: int64(len(full))}
		var out bytes.Buffer
		err := Inject(src, &out, []byte("exif"), nil, nil, true)
		if err == nil {
			t.Errorf("cut at %d of %d: Inject succeeded on a truncated source", cut, len(full))
			continue
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("cut at %d of %d: err = %v, want io.ErrUnexpectedEOF", cut, len(full), err)
		}
	}
}

// noSeek is a reader that cannot report its size.
type noSeek struct{ io.Reader }

func (noSeek) Seek(int64, int) (int64, error) { return 0, errors.New("seek not supported") }

// TestInjectNonSeekableSource: a source that cannot seek is refused with an
// error, never read into memory.
func TestInjectNonSeekableSource(t *testing.T) {
	t.Parallel()
	err := Inject(noSeek{bytes.NewReader(buildWebP(nil, nil, 0, 0, 0))}, io.Discard, []byte("exif"), nil, nil, true)
	if err == nil {
		t.Fatal("Inject on a source that cannot seek: expected an error")
	}
}

// TestInjectFileTooShort: fewer than the 12 bytes of the RIFF header.
func TestInjectFileTooShort(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 1, 11} {
		if err := Inject(bytes.NewReader(make([]byte, n)), io.Discard, []byte("exif"), nil, nil, true); !errors.Is(err, ErrFileTooShort) {
			t.Errorf("%d bytes: err = %v, want ErrFileTooShort", n, err)
		}
	}
}
