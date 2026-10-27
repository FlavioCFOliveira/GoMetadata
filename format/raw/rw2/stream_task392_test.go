package rw2

// stream_task392_test.go — task #392: the streaming Inject must write the bytes
// (and return the errors) of the whole-file implementation (oracleInject below)
// for every input: the corpus, synthetic files with image data before and after
// their IFDs, and truncations and corruptions of them; and it must not be
// limited by the size of the file or hold it in memory.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// oracleInject is the pre-#392 Inject: the whole file in memory, its magic
// patched to the standard TIFF one, written by tiff.Inject.
func oracleInject(data []byte, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if !bytes.HasPrefix(data, rw2Magic) {
		return ErrInvalidMagic
	}
	mw := &iobuf.MagicWriter{W: w, Magic: [2]byte{data[2], data[3]}}
	patched := bytes.Clone(data)
	patched[2], patched[3] = 0x2A, 0x00
	injectErr := tiff.Inject(bytes.NewReader(patched), mw, rawEXIF, rawIPTC, rawXMP, true)
	if mw.Err != nil {
		return errors.New("write: " + mw.Err.Error())
	}
	if errors.Is(injectErr, iobuf.ErrShortFirstWrite) {
		return ErrOutputTooShort
	}
	if injectErr != nil {
		return errors.New("inject: " + injectErr.Error())
	}
	if !mw.Started() {
		return ErrOutputTooShort
	}
	return nil
}

// strippedFile builds a classic TIFF with the container magic: IFD0 with a
// Make, a Compression and one strip of stripLen bytes. With ifdAtEnd the IFD
// follows the strip, otherwise it follows the header; trailing bytes follow
// everything.
func strippedFile(stripLen int, ifdAtEnd bool, trailing []byte) []byte {
	const entries = 4
	ifdLen := 2 + entries*12 + 4
	maker := []byte("OLYMPUS\x00")
	var ifdOff, stripOff int
	if ifdAtEnd {
		stripOff, ifdOff = 8, 8+stripLen
	} else {
		ifdOff = 8
		stripOff = ifdOff + ifdLen + len(maker)
	}
	le := binary.LittleEndian
	ifd := make([]byte, ifdLen)
	le.PutUint16(ifd, entries)
	entry := func(i int, tag, typ uint16, count, val uint32) {
		e := ifd[2+i*12:]
		le.PutUint16(e, tag)
		le.PutUint16(e[2:], typ)
		le.PutUint32(e[4:], count)
		le.PutUint32(e[8:], val)
	}
	makeOff := ifdOff + ifdLen
	entry(0, 0x0103, 3, 1, 1)                                // Compression
	entry(1, 0x010F, 2, uint32(len(maker)), uint32(makeOff)) //nolint:gosec // G115: test file sizes are small // Make, out of line
	entry(2, 0x0111, 4, 1, uint32(stripOff))                 // StripOffsets
	entry(3, 0x0117, 4, 1, uint32(stripLen))                 //nolint:gosec // G115: test strips are below 2^32                 // StripByteCounts
	out := make([]byte, 8, 8+stripLen+ifdLen+len(maker)+len(trailing))
	copy(out, rw2Magic)
	le.PutUint32(out[4:], uint32(ifdOff))
	if ifdAtEnd {
		out = append(out, bytes.Repeat([]byte{0xAB}, stripLen)...)
		out = append(out, ifd...)
		out = append(out, maker...)
	} else {
		out = append(out, ifd...)
		out = append(out, maker...)
		out = append(out, bytes.Repeat([]byte{0xAB}, stripLen)...)
	}
	return append(out, trailing...)
}

// streamCombos are the (rawEXIF, rawIPTC, rawXMP) triples every file is written with.
func streamCombos(data []byte) []struct {
	name            string
	exif, iptc, xmp []byte
} {
	xmp := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	iptc := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'a', 'b', 'c'}
	return []struct {
		name            string
		exif, iptc, xmp []byte
	}{
		{"xmp", nil, nil, xmp},
		{"iptc+xmp", nil, iptc, xmp},
		{"none", nil, nil, nil},
		{"exif-base", bytes.Clone(data[:min(len(data), 64)]), nil, xmp},
	}
}

// assertSameAsOracle writes data with both implementations and compares the
// output and the error.
func assertSameAsOracle(t *testing.T, name string, data []byte) {
	t.Helper()
	for _, c := range streamCombos(data) {
		var want, got bytes.Buffer
		wantErr := oracleInject(data, &want, c.exif, c.iptc, c.xmp)
		err := Inject(bytes.NewReader(data), &got, c.exif, c.iptc, c.xmp, true)
		if (err != nil) != (wantErr != nil) || (err != nil && !sameFailure(err, wantErr)) {
			t.Fatalf("%s/%s: err = %v, oracle err = %v", name, c.name, err, wantErr)
		}
		if err == nil && !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("%s/%s: output differs from the whole-file implementation (%d vs %d bytes)", name, c.name, got.Len(), want.Len())
		}
	}
}

// sameFailure reports whether err and the oracle's error describe the same
// failure: the same sentinel, or the same text after the package prefix.
func sameFailure(err, oracleErr error) bool {
	for _, s := range []error{ErrInvalidMagic, ErrOutputTooShort, ErrFileTooLarge, tiff.ErrCorruptXMP} {
		if errors.Is(err, s) || errors.Is(oracleErr, s) {
			return errors.Is(err, s) && errors.Is(oracleErr, s)
		}
	}
	return true // both failed for another reason
}

// TestInjectMatchesWholeFileImplementation compares the two implementations on
// the synthetic files, the corpus, and truncations and corruptions of them.
func TestInjectMatchesWholeFileImplementation(t *testing.T) {
	t.Parallel()
	seeds := map[string][]byte{
		"empty":            rw2Magic,
		"strip-small":      strippedFile(100, false, nil),
		"strip-odd":        strippedFile(101, false, []byte("trailer")),
		"ifd-at-end":       strippedFile(300, true, nil),
		"strip-large":      strippedFile(5<<20, false, []byte("AFCP-less trailer")),
		"ifd-at-end-large": strippedFile(5<<20, true, nil),
		"tag-in-ifd":       buildRW2WithTag(0x010F, 2, []byte("Panasonic\x00")),
	}
	for _, m := range mustGlob(t, "../../../testdata/corpus/raw/*/*.[rR][wW]2") {
		if data, err := os.ReadFile(m); err == nil {
			seeds[m] = data
		}
	}
	for name, data := range seeds {
		assertSameAsOracle(t, name, data)
		head := data[:min(len(data), 512)]
		for cut := range min(len(head), 120) {
			assertSameAsOracle(t, name+"/cut", head[:cut])
		}
		for _, frac := range []int{2, 3} {
			assertSameAsOracle(t, name+"/cutfrac", data[:len(data)/frac])
		}
		rng := rand.New(rand.NewPCG(uint64(len(data)), 392)) //nolint:gosec // G115,G404: deterministic seed for reproducible corruptions, not a secret
		for range 100 {
			mut := bytes.Clone(head)
			for range 1 + rng.IntN(3) {
				mut[rng.IntN(len(mut))] = byte(rng.IntN(256)) //nolint:gosec // G115: IntN(256) fits a byte
			}
			assertSameAsOracle(t, name+"/flip", mut)
		}
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// FuzzRW2InjectMatchesWholeFile: for any input and payloads the streaming
// Inject and the whole-file implementation agree (task #392).
func FuzzRW2InjectMatchesWholeFile(f *testing.F) {
	f.Add(strippedFile(40, false, nil), uint8(1))
	f.Add(strippedFile(40, true, []byte("t")), uint8(3))
	f.Add(rw2Magic, uint8(1))
	f.Fuzz(func(t *testing.T, data []byte, which uint8) {
		var iptc, xmp []byte
		if which&1 != 0 {
			xmp = []byte(`<x:xmpmeta/>`)
		}
		if which&2 != 0 {
			iptc = []byte{0x1C, 0x02, 0x78, 0x00, 0x01, 'a'}
		}
		var want, got bytes.Buffer
		wantErr := oracleInject(data, &want, nil, iptc, xmp)
		err := Inject(bytes.NewReader(data), &got, nil, iptc, xmp, true)
		if (err != nil) != (wantErr != nil) {
			t.Fatalf("err = %v, oracle err = %v", err, wantErr)
		}
		if err == nil && !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("output differs from the whole-file implementation")
		}
	})
}

// TestInjectLargerThanFormerCap: a 300 MiB source (the former 256 MiB cap) is
// written with the size the format gives, and the memory the write allocates
// does not grow with the file.
//
//nolint:paralleltest // measures the heap of the process: no other test may allocate meanwhile
func TestInjectLargerThanFormerCap(t *testing.T) {
	const strip = 300 << 20
	// The growth of the file by the write does not depend on the size of the strip:
	// measure it on a small file.
	small := strippedFile(1000, false, nil)
	var smallOut bytes.Buffer
	xmp := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	if err := Inject(bytes.NewReader(small), &smallOut, nil, nil, xmp, true); err != nil {
		t.Fatal(err)
	}
	head := strippedFile(0, false, nil)
	// The sparse file is the head of a file with the strip declared at its true
	// length, followed by zeros.
	patched := bytes.Clone(head)
	stripOff := binary.LittleEndian.Uint32(patched[8+2+2*12+8:])
	binary.LittleEndian.PutUint32(patched[8+2+3*12+8:], strip) // StripByteCounts
	src := testutil.NewSparseFile(int64(stripOff)+strip, testutil.Patch{Off: 0, Data: patched})

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var out testutil.CountingWriter
	if err := Inject(src, &out, nil, nil, xmp, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	runtime.ReadMemStats(&after)
	if want := int64(smallOut.Len()-len(small)) + int64(stripOff) + strip; out.N != want {
		t.Errorf("output = %d bytes, want %d", out.N, want)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 1<<20 {
		t.Errorf("Inject of a %d MiB file allocated %d bytes, want < 1 MiB", (int64(stripOff)+strip)>>20, grown)
	}

	// With nothing to write the source is copied unchanged, with no size limit.
	src = testutil.NewSparseFile(int64(stripOff)+strip, testutil.Patch{Off: 0, Data: patched})
	out = testutil.CountingWriter{}
	if err := Inject(src, &out, nil, nil, nil, true); err != nil {
		t.Fatalf("Inject (copy): %v", err)
	}
	if want := int64(stripOff) + strip; out.N != want {
		t.Errorf("copy = %d bytes, want %d", out.N, want)
	}
}

// TestInjectBeyondClassicTIFFOffsets: a source whose offsets cannot be
// addressed by a classic TIFF (4 GiB) is refused with ErrFileTooLarge before
// anything is written.
func TestInjectBeyondClassicTIFFOffsets(t *testing.T) {
	t.Parallel()
	src := testutil.NewSparseFile(5<<30, testutil.Patch{Off: 0, Data: strippedFile(0, false, nil)})
	var out testutil.CountingWriter
	err := Inject(src, &out, nil, nil, []byte("<x:xmpmeta/>"), true)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
	if out.N != 0 {
		t.Errorf("%d bytes written before the refusal", out.N)
	}
}

// shortSource claims a size through Seek(SeekEnd) but delivers only data.
type shortSource struct {
	data  []byte
	claim int64
	pos   int64
}

func (s *shortSource) Read(p []byte) (int, error) {
	if s.pos >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += int64(n)
	return n, nil
}

func (s *shortSource) Seek(off int64, whence int) (int64, error) {
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
// ends the write with an error, wherever it ends, without a hang or a panic
// (security: the copy of a range stops at the end of the data).
func TestInjectTruncatedSource(t *testing.T) {
	t.Parallel()
	full := strippedFile(6<<20, false, []byte("trailer"))
	for _, cut := range []int{10, 100, 200, 4 << 20, 5 << 20, len(full) - 1} {
		src := &shortSource{data: full[:cut], claim: int64(len(full))}
		var out bytes.Buffer
		if err := Inject(src, &out, nil, nil, []byte("<x:xmpmeta/>"), true); err == nil {
			t.Errorf("cut at %d of %d: Inject succeeded on a truncated source", cut, len(full))
		}
		src = &shortSource{data: full[:cut], claim: int64(len(full))}
		if err := Inject(src, io.Discard, nil, nil, nil, true); err == nil {
			t.Errorf("cut at %d of %d: the copy succeeded on a truncated source", cut, len(full))
		}
	}
}

// noSeek is a reader that can rewind but cannot report its size: every Seek
// but the rewind fails.
type noSeek struct{ r *bytes.Reader }

func (n noSeek) Read(p []byte) (int, error) { return n.r.Read(p) } //nolint:wrapcheck // test helper: deliberate pass-through
func (n noSeek) Seek(off int64, whence int) (int64, error) {
	if off == 0 && whence == io.SeekStart {
		return n.r.Seek(0, io.SeekStart) //nolint:wrapcheck // test helper: deliberate pass-through
	}
	return 0, errors.New("seek not supported beyond the rewind")
}

// TestInjectNonSeekableSource: a source that cannot report its size is
// buffered whole up to maxFileSize, as before (a bounded read), and written.
func TestInjectNonSeekableSource(t *testing.T) {
	t.Parallel()
	data := strippedFile(100, false, nil)
	var want, got bytes.Buffer
	if err := oracleInject(data, &want, nil, nil, []byte("<x:xmpmeta/>")); err != nil {
		t.Fatal(err)
	}
	if err := Inject(noSeek{bytes.NewReader(data)}, &got, nil, nil, []byte("<x:xmpmeta/>"), true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Error("output of a non-seekable source differs from the whole-file implementation")
	}
}
