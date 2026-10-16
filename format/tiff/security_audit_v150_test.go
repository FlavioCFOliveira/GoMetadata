package tiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Security audit 2026-10-16 (v1.5.0 pre-clearance), findings F-07a and F-07b.
// Sub-test names are the finding IDs.

func mustParseEXIF(t *testing.T, b []byte) *exif.EXIF {
	t.Helper()
	e, err := exif.Parse(b)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	return e
}

// countingWriter counts the bytes written to it.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// auditDuplicateStrips builds a little-endian TIFF whose StripOffsets holds n
// entries that all address the same dataLen-byte range (the F-07a PoC).
func auditDuplicateStrips(n, dataLen int) []byte {
	le := binary.LittleEndian
	const nent = 3
	ifd0 := 8
	offArr := ifd0 + 2 + nent*12 + 4
	cntArr := offArr + 4*n
	data := cntArr + 4*n
	b := make([]byte, data+dataLen)
	copy(b, "II*\x00")
	le.PutUint32(b[4:], uint32(ifd0))
	le.PutUint16(b[ifd0:], nent)
	put := func(i int, tag, typ uint16, cnt, val uint32) {
		e := ifd0 + 2 + i*12
		le.PutUint16(b[e:], tag)
		le.PutUint16(b[e+2:], typ)
		le.PutUint32(b[e+4:], cnt)
		le.PutUint32(b[e+8:], val)
	}
	put(0, 0x0111, 4, uint32(n), uint32(offArr)) //nolint:gosec // G115: test sizes are small
	put(1, 0x0116, 4, 1, 1)
	put(2, 0x0117, 4, uint32(n), uint32(cntArr)) //nolint:gosec // G115: test sizes are small
	for i := range n {
		le.PutUint32(b[offArr+4*i:], uint32(data))    //nolint:gosec // G115: test sizes are small
		le.PutUint32(b[cntArr+4*i:], uint32(dataLen)) //nolint:gosec // G115: test sizes are small
	}
	return b
}

// auditSubIFDChains builds a little-endian TIFF whose IFD0 holds a 0x014A entry
// with heads SubIFD pointers, each the head of a chain of chain empty IFDs
// (the F-07b PoC).
func auditSubIFDChains(heads, chain int) []byte {
	le := binary.LittleEndian
	ifd0 := 8
	arr := ifd0 + 2 + 12 + 4
	pool := arr + 4*heads
	b := make([]byte, pool+heads*chain*6)
	copy(b, "II*\x00")
	le.PutUint32(b[4:], uint32(ifd0))
	le.PutUint16(b[ifd0:], 1)
	e := ifd0 + 2
	le.PutUint16(b[e:], 0x014A)
	le.PutUint16(b[e+2:], 4)
	le.PutUint32(b[e+4:], uint32(heads)) //nolint:gosec // G115: test sizes are small
	le.PutUint32(b[e+8:], uint32(arr))
	for h := range heads {
		le.PutUint32(b[arr+4*h:], uint32(pool+h*chain*6)) //nolint:gosec // G115: test sizes are small
		for j := range chain {
			p := pool + (h*chain+j)*6
			le.PutUint16(b[p:], 0)
			if j < chain-1 {
				le.PutUint32(b[p+2:], uint32(p+6)) //nolint:gosec // G115: test sizes are small
			}
		}
	}
	return b
}

func TestSecurityAuditV150(t *testing.T) {
	t.Parallel()

	// F-07a: 2000 StripOffsets entries addressing one 1 MiB range made Write
	// emit 2000 MiB from a 1 MiB file. The range is copied once now.
	t.Run("F-07a", func(t *testing.T) {
		t.Parallel()
		in := auditDuplicateStrips(2000, 1<<20)
		var out countingWriter
		if err := Inject(bytes.NewReader(in), &out, in, []byte("iptc"), nil, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		if out.n > int64(len(in))+64<<10 {
			t.Errorf("Inject wrote %d bytes for a %d-byte file (limit input+64 KiB)", out.n, len(in))
		}
	})

	// F-07a: the same stream through the in-memory entry point.
	t.Run("F-07a-bytes", func(t *testing.T) {
		t.Parallel()
		in := auditDuplicateStrips(300, 1<<16)
		var out countingWriter
		if err := InjectWithEXIF(in, mustParseEXIF(t, in), []byte("iptc"), nil, &out); err != nil {
			t.Fatalf("InjectWithEXIF: %v", err)
		}
		if out.n > int64(len(in))+64<<10 {
			t.Errorf("InjectWithEXIF wrote %d bytes for a %d-byte file", out.n, len(in))
		}
	})

	// F-07a-overlap: a strip and tiles over the same bytes (the shape of a Sony
	// ARW, which a valid camera file has) is written, with the bytes once, every
	// tile at its place in the copy of the strip.
	t.Run("F-07a-overlap", func(t *testing.T) {
		t.Parallel()
		const tiles, tileLen = 140, 4096
		in, _ := auditOverlapTiles(tiles, tileLen)
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(in), &out, in, []byte("iptc"), nil, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		if out.Len() > len(in)+64<<10 {
			t.Errorf("output %d bytes for a %d-byte file (limit input+64 KiB)", out.Len(), len(in))
		}
		got := out.Bytes()
		inOffs := ifd0Longs(t, in, 0x0144)
		outOffs, outCnts := ifd0Longs(t, got, 0x0144), ifd0Longs(t, got, 0x0145)
		if len(outOffs) != tiles || len(outCnts) != tiles {
			t.Fatalf("tile entries: %d offsets, %d counts, want %d", len(outOffs), len(outCnts), tiles)
		}
		for i := range tiles {
			if outCnts[i] != tileLen {
				t.Fatalf("tile %d count = %d, want %d", i, outCnts[i], tileLen)
			}
			want := in[inOffs[i] : inOffs[i]+tileLen]
			if !bytes.Equal(got[outOffs[i]:outOffs[i]+tileLen], want) {
				t.Fatalf("tile %d: bytes at its output offset %d differ from the source", i, outOffs[i])
			}
		}
		stripOff, stripCnt := ifd0Longs(t, got, 0x0111)[0], ifd0Longs(t, got, 0x0117)[0]
		if stripCnt != tiles*tileLen || !bytes.Equal(got[stripOff:stripOff+stripCnt], in[len(in)-tiles*tileLen:]) {
			t.Errorf("strip bytes at %d (%d bytes) differ from the source", stripOff, stripCnt)
		}
		// One copy: tile i sits at the strip offset plus its source distance.
		for i := range tiles {
			if outOffs[i] != stripOff+uint32(i*tileLen) {
				t.Fatalf("tile %d at %d, want strip start %d + %d", i, outOffs[i], stripOff, i*tileLen)
			}
		}
	})

	// F-07a: strips that are disjoint ranges of the file (every valid file) are
	// written in full.
	t.Run("F-07a-valid", func(t *testing.T) {
		t.Parallel()
		in := buildMultiStripTIFFN(50)
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(in), &out, in, []byte("iptc"), nil, true); err != nil {
			t.Fatalf("Inject of a 50-strip TIFF: %v", err)
		}
		if out.Len() < len(in) {
			t.Errorf("output %d bytes, fewer than the %d of the input", out.Len(), len(in))
		}
	})

	// F-07b: 1024 SubIFD heads x 512-IFD chains built 524288 SubIFDs from 3 MiB.
	t.Run("F-07b", func(t *testing.T) {
		t.Parallel()
		in := auditSubIFDChains(1024, 512)
		err := Inject(bytes.NewReader(in), io.Discard, in, []byte("iptc"), nil, true)
		if !errors.Is(err, ErrTooManyImageBlocks) {
			t.Errorf("Inject error = %v, want ErrTooManyImageBlocks", err)
		}
	})

	// F-07b: a realistic SubIFD chain is still written.
	t.Run("F-07b-valid", func(t *testing.T) {
		t.Parallel()
		in := auditSubIFDChains(4, 16)
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(in), &out, in, []byte("iptc"), nil, true); err != nil {
			t.Errorf("Inject of 4 SubIFD chains of 16: %v", err)
		}
	})
}

// TestSecurityAuditV150Alloc measures process-wide allocation, so it is NOT
// parallel: a sequential test never overlaps the parallel ones.
func TestSecurityAuditV150Alloc(t *testing.T) { //nolint:paralleltest // measures process-wide allocation; it must not overlap the parallel tests
	t.Run("F-07b", func(t *testing.T) {
		in := auditSubIFDChains(1024, 512)
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		_ = Inject(bytes.NewReader(in), io.Discard, in, []byte("iptc"), nil, true)
		runtime.ReadMemStats(&m1)
		// The unfixed code allocated 206 MiB for this 3 MiB file.
		if got := m1.TotalAlloc - m0.TotalAlloc; got > 20*uint64(len(in)) {
			t.Errorf("Inject allocated %d MiB for a %d KiB file (limit 20x)", got>>20, len(in)>>10)
		}
	})
}

// BenchmarkInjectAuditV150 measures the write of a valid multi-strip TIFF, the
// path that checkBlocks now guards.
func BenchmarkInjectAuditV150(b *testing.B) {
	in := buildMultiStripTIFFN(2000)
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for range b.N {
		if err := Inject(bytes.NewReader(in), io.Discard, in, []byte("iptc"), nil, true); err != nil {
			b.Fatal(err)
		}
	}
}
