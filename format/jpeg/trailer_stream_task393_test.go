package jpeg

// trailer_stream_task393_test.go — task #393: the bytes after the EOI marker are
// not held in memory. A trailer is found with a streaming scan and copied from
// the source; only a trailer that is rewritten is read.

import (
	"bytes"
	"io"
	"runtime"
	"testing"
)

// TestInjectLargeTrailerNotHeld: a JPEG with a 12 MiB Samsung "Sound & Shot"
// trailer (below the 16 MiB limit of trailer.MaxRegion, which used to be read
// into memory whole) is written with the audio offsets rewritten, allocating
// less than 256 KiB; the trailer bytes are the source's.
//
//nolint:paralleltest // measures the heap of the process: no other test may allocate meanwhile
func TestInjectLargeTrailerNotHeld(t *testing.T) {
	audio := bytes.Repeat([]byte("0123456789abcdef"), 12<<16) // 12 MiB
	base := int64(len(jpegWithTail(nil)))
	src := jpegWithTail(samsungOf(base, audio))
	exif := []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00")

	var out bytes.Buffer
	out.Grow(len(src) + 1<<16) // not counted: the caller's buffer
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := Inject(bytes.NewReader(src), &out, exif, newIIM, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	runtime.ReadMemStats(&after)
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 256<<10 {
		t.Errorf("Inject of a JPEG with a %d MiB trailer allocated %d bytes, want < 256 KiB", len(audio)>>20, grown)
	}

	got := out.Bytes()
	s, e := samsungAudioOffsets(t, got)
	if s0, _ := samsungAudioOffsets(t, src); s == s0 {
		t.Fatal("the trailer did not move: the test proves nothing")
	}
	if e-s != int64(len(audio)) || !bytes.Equal(got[s:e], audio) {
		t.Errorf("the QDIO offsets %d-%d do not name the audio file", s, e)
	}
	// The trailer is the source's but for the 8 offset bytes.
	srcTrailer := src[base:]
	gotTrailer := got[len(got)-len(srcTrailer):]
	diff := 0
	for i := range srcTrailer {
		if srcTrailer[i] != gotTrailer[i] {
			diff++
		}
	}
	if diff == 0 || diff > 8 {
		t.Errorf("%d trailer bytes differ, want only the audio offsets (1 to 8 bytes)", diff)
	}
}

// shrinkingSource delivers the source for the first `full` reads and then
// reports end of file: a file that was truncated while it was being written.
type shrinkingSource struct {
	data  []byte
	pos   int64
	reads int
	full  int
}

func (s *shrinkingSource) Read(p []byte) (int, error) {
	s.reads++
	if s.reads > s.full || s.pos >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += int64(n)
	return n, nil
}

func (s *shrinkingSource) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = off
	case io.SeekCurrent:
		s.pos += off
	case io.SeekEnd:
		s.pos = int64(len(s.data)) + off
	}
	return s.pos, nil
}

// TestInjectTrailerSourceShrinks: a source that ends early while the trailer
// is looked for or copied never makes Inject hang or panic, at any point; the
// outcome is an error or a shorter output, as for any truncated JPEG.
func TestInjectTrailerSourceShrinks(t *testing.T) {
	t.Parallel()
	base := int64(len(jpegWithTail(nil)))
	src := jpegWithTail(samsungOf(base, bytes.Repeat([]byte("audio"), 40000)))
	exif := []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00")
	for full := 1; full < 60; full++ {
		var out bytes.Buffer
		_ = Inject(&shrinkingSource{data: src, full: full}, &out, exif, newIIM, nil, true)
	}
}
