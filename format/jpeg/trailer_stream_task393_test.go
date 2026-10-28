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
// The deterministic measure of "not held" is the largest read from the source
// and the largest write to the output, which stay far below the trailer size
// whatever the runtime does. The heap bound depends on sync.Pool, which the race
// detector makes drop puts at random (so that pooled buffers are allocated
// again), and so it is asserted only when the race detector is off (task #419).
//
//nolint:paralleltest // measures the heap of the process: no other test may allocate meanwhile
func TestInjectLargeTrailerNotHeld(t *testing.T) {
	audio := bytes.Repeat([]byte("0123456789abcdef"), 12<<16) // 12 MiB
	base := int64(len(jpegWithTail(nil)))
	src := jpegWithTail(samsungOf(base, audio))
	exif := []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00")

	var buf bytes.Buffer
	buf.Grow(len(src) + 1<<16) // not counted: the caller's buffer
	out := &spanWriter{w: &buf}
	in := &spanReader{r: bytes.NewReader(src)}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := Inject(in, out, exif, newIIM, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	runtime.ReadMemStats(&after)
	// A trailer held in memory is read or written whole, in one call.
	if limit := 1 << 20; in.maxRead > limit || out.maxWrite > limit {
		t.Errorf("largest read %d and largest write %d bytes, want at most %d (the trailer is %d)", in.maxRead, out.maxWrite, limit, len(audio))
	}
	if grown := after.TotalAlloc - before.TotalAlloc; !raceEnabled && grown > 256<<10 {
		t.Errorf("Inject of a JPEG with a %d MiB trailer allocated %d bytes, want < 256 KiB", len(audio)>>20, grown)
	}

	got := buf.Bytes()
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

// spanReader records the largest read of a source; it hides the fast paths
// that a *bytes.Reader would give.
type spanReader struct {
	r       io.ReadSeeker
	maxRead int
}

func (s *spanReader) Read(p []byte) (int, error) {
	s.maxRead = max(s.maxRead, len(p))
	return s.r.Read(p) //nolint:wrapcheck // a test double: the error is the reader's
}

func (s *spanReader) Seek(off int64, whence int) (int64, error) {
	return s.r.Seek(off, whence) //nolint:wrapcheck // a test double: the error is the seeker's
}

// spanWriter records the largest write to a sink.
type spanWriter struct {
	w        io.Writer
	maxWrite int
}

func (s *spanWriter) Write(p []byte) (int, error) {
	s.maxWrite = max(s.maxWrite, len(p))
	return s.w.Write(p) //nolint:wrapcheck // a test double: the error is the writer's
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
