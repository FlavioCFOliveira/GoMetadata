package tiff

// cr2_chain_rebase_test.go — regression tests for task #281.
//
// After the CR2 header extension is inserted at byte 8, every offset into the
// file moves by +8. rebaseAllIFDsAfterCR2Marker shifted neither the next-IFD
// pointers nor the strip/JPEG offsets, so IFD1-IFD3 and their image data
// were lost on every CR2 write, and the header's RAW IFD pointer (bytes
// 12-15, lclevy "Canon CR2 specifications") was zeroed.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// cr2Fixture builds a synthetic Canon CR2 following the lclevy CR2 layout:
// 16-byte header (TIFF header, "CR\x02\x00", RAW IFD offset), IFD0 (main
// JPEG preview), IFD1 (thumbnail), IFD2 (RGB strip), IFD3 (lossless-JPEG raw).
// The image blocks have odd and even lengths on purpose.
func cr2Fixture() (data []byte, blocks [][]byte) {
	blocks = [][]byte{
		bytes.Repeat([]byte{0xA1}, 33), // IFD0 preview
		bytes.Repeat([]byte{0xB2}, 20), // IFD1 thumbnail
		bytes.Repeat([]byte{0xC3}, 41), // IFD2 RGB
		bytes.Repeat([]byte{0xD4}, 64), // IFD3 raw
	}
	const ifd0Off = 16
	ifdLen := func(n int) int { return 2 + n*12 + 4 }
	ifd1Off := ifd0Off + ifdLen(4)
	ifd2Off := ifd1Off + ifdLen(2)
	ifd3Off := ifd2Off + ifdLen(2)
	pos := ifd3Off + ifdLen(2)
	offs := make([]int, 4)
	for i, b := range blocks {
		offs[i] = pos
		pos += len(b)
	}
	data = make([]byte, pos)
	o := binary.LittleEndian
	data[0], data[1] = 'I', 'I'
	o.PutUint16(data[2:], 42)
	o.PutUint32(data[4:], ifd0Off)
	copy(data[8:], []byte{'C', 'R', 2, 0})
	o.PutUint32(data[12:], uint32(ifd3Off))
	putFixtureIFD(data, ifd0Off, []fixtureEntry{
		{0x0100, 4, 1, 8}, {0x0101, 4, 1, 8},
		{0x0111, 4, 1, uint32(offs[0])}, {0x0117, 4, 1, uint32(len(blocks[0]))},
	}, uint32(ifd1Off))
	putFixtureIFD(data, ifd1Off, []fixtureEntry{
		{0x0201, 4, 1, uint32(offs[1])}, {0x0202, 4, 1, uint32(len(blocks[1]))},
	}, uint32(ifd2Off))
	putFixtureIFD(data, ifd2Off, []fixtureEntry{
		{0x0111, 4, 1, uint32(offs[2])}, {0x0117, 4, 1, uint32(len(blocks[2]))},
	}, uint32(ifd3Off))
	putFixtureIFD(data, ifd3Off, []fixtureEntry{
		{0x0111, 4, 1, uint32(offs[3])}, {0x0117, 4, 1, uint32(len(blocks[3]))},
	}, 0)
	for i, b := range blocks {
		copy(data[offs[i]:], b)
	}
	return data, blocks
}

// injectCR2 parses data, sets a copyright and runs InjectWithEXIFCR2Stream.
func injectCR2(t *testing.T, data []byte) []byte {
	t.Helper()
	e, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e.SetCopyright("regression #281")
	var out bytes.Buffer
	if err := InjectWithEXIFCR2Stream(bytes.NewReader(data), data, true, e, nil, nil, &out); err != nil {
		t.Fatalf("InjectWithEXIFCR2Stream: %v", err)
	}
	return out.Bytes()
}

// TestInjectCR2_SyntheticChainPreserved (task #281): all four IFDs and their
// image data survive a CR2 write, and the CR2 marker stays at bytes 8-11.
func TestInjectCR2_SyntheticChainPreserved(t *testing.T) {
	t.Parallel()
	data, want := cr2Fixture()
	out := injectCR2(t, data)
	if !bytes.Equal(out[8:12], []byte{'C', 'R', 2, 0}) {
		t.Errorf("CR2 marker = %x, want 43520200", out[8:12])
	}
	if got := binary.LittleEndian.Uint32(out[4:]); got != cr2IFD0Offset {
		t.Errorf("IFD0 offset = %d, want %d", got, cr2IFD0Offset)
	}
	got := imageBlocksPerIFD(t, out, true)
	if len(got) != 4 {
		t.Fatalf("reachable IFDs = %d, want 4", len(got))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("IFD%d image data differs: got %x, want %x", i, got[i], want[i])
		}
	}
	assertCR2RawIFDPointer(t, data, out)
}

// assertCR2RawIFDPointer checks that the RAW IFD pointer of the output CR2
// header (bytes 12-15) names the IFD at the same chain position as the input
// header does.
func assertCR2RawIFDPointer(t *testing.T, in, out []byte) {
	t.Helper()
	o := binary.LittleEndian
	idx, ok := chainIndexOf(in, o.Uint32(in[12:]))
	if !ok {
		t.Fatalf("input RAW IFD pointer %d is not a chained IFD", o.Uint32(in[12:]))
	}
	gotIdx, ok := chainIndexOf(out, o.Uint32(out[12:]))
	if !ok || gotIdx != idx {
		t.Errorf("output RAW IFD pointer %d names chain IFD %d (found %v), want IFD %d", o.Uint32(out[12:]), gotIdx, ok, idx)
	}
}

// chainIndexOf returns the position of the IFD at off in the little-endian
// IFD0 chain of data, walking the chain independently of the code under test.
func chainIndexOf(data []byte, off uint32) (int, bool) {
	o := binary.LittleEndian
	cur := o.Uint32(data[4:])
	for i := range 16 {
		if cur == 0 || uint64(cur)+2 > uint64(len(data)) {
			return 0, false
		}
		if cur == off {
			return i, true
		}
		at := uint64(cur) + 2 + uint64(o.Uint16(data[cur:]))*12
		if at+4 > uint64(len(data)) {
			return 0, false
		}
		cur = o.Uint32(data[at:])
	}
	return 0, false
}

// TestCR2RawIFDPointerHelpers covers the header-pointer helpers on malformed
// input: no pointer, a pointer outside the chain, truncated IFDs, and a
// cyclic chain.
func TestCR2RawIFDPointerHelpers(t *testing.T) {
	t.Parallel()
	data, _ := cr2Fixture()
	o := binary.LittleEndian
	if idx, ok := cr2RawIFDIndex(data); !ok || idx != 3 {
		t.Errorf("cr2RawIFDIndex(fixture) = %d, %v; want 3, true", idx, ok)
	}
	for name, mutate := range map[string]func([]byte){
		"zero pointer":      func(b []byte) { o.PutUint32(b[12:], 0) },
		"pointer off chain": func(b []byte) { o.PutUint32(b[12:], 17) },
		"bad byte order":    func(b []byte) { b[0], b[1] = 'X', 'X' },
		"ifd0 outside":      func(b []byte) { o.PutUint32(b[4:], uint32(len(b))) },
	} {
		b := bytes.Clone(data)
		mutate(b)
		if idx, ok := cr2RawIFDIndex(b); ok {
			t.Errorf("%s: cr2RawIFDIndex = %d, true; want false", name, idx)
		}
	}
	if _, ok := cr2RawIFDIndex(data[:12]); ok {
		t.Error("cr2RawIFDIndex accepted a 12-byte input")
	}
	// Cycle: IFD0's next pointer names IFD0 itself; the walk must stop.
	cyc := bytes.Clone(data)
	n := o.Uint16(cyc[cr2IFD0Offset:])
	o.PutUint32(cyc[cr2IFD0Offset+2+int(n)*12:], cr2IFD0Offset)
	o.PutUint32(cyc[12:], 999)
	if _, ok := cr2RawIFDIndex(cyc); ok {
		t.Error("cr2RawIFDIndex resolved a pointer through a cyclic chain")
	}
	if _, ok := nthCR2IFD(cyc, cr2MaxChain+1, o); ok {
		t.Error("nthCR2IFD walked past cr2MaxChain")
	}
	if off, ok := nthCR2IFD(data, 3, o); !ok || off != o.Uint32(data[12:]) {
		t.Errorf("nthCR2IFD(3) = %d, %v; want %d", off, ok, o.Uint32(data[12:]))
	}
	if _, ok := nthCR2IFD(data, 4, o); ok {
		t.Error("nthCR2IFD(4) found a fifth IFD in a four-IFD chain")
	}
	if _, ok := nthCR2IFD(data[:20], 1, o); ok {
		t.Error("nthCR2IFD followed a truncated IFD")
	}
}

// TestInjectCR2_RebaseChainAndStrips (task #281) exercises the rebase
// primitive directly: next-IFD pointers, inline and out-of-line strip/JPEG
// offsets, and a shared offset visited through two paths (shifted once).
func TestInjectCR2_RebaseChainAndStrips(t *testing.T) {
	t.Parallel()
	o := binary.LittleEndian
	// IFD0 at 16 holds an out-of-line StripOffsets array (2 LONGs) behind a
	// pointer, an inline JPEGInterchangeFormat, and a next-IFD pointer; the
	// pointers hold pre-shift values (marker already inserted physically).
	buf := make([]byte, 16+2+2*12+4+8+2+4+4)
	arr := 16 + 2 + 2*12 + 4 // array position in the shifted buffer
	ifd1 := arr + 8
	o.PutUint16(buf[16:], 2)
	e := 18
	o.PutUint16(buf[e:], 0x0111)
	o.PutUint16(buf[e+2:], 4)
	o.PutUint32(buf[e+4:], 2)
	o.PutUint32(buf[e+8:], uint32(arr-cr2MarkerLen)) // pre-shift pointer
	e += 12
	o.PutUint16(buf[e:], 0x0201)
	o.PutUint16(buf[e+2:], 4)
	o.PutUint32(buf[e+4:], 1)
	o.PutUint32(buf[e+8:], 1000)
	o.PutUint32(buf[e+12:], uint32(ifd1-cr2MarkerLen)) // next-IFD
	o.PutUint32(buf[arr:], 2000)
	o.PutUint32(buf[arr+4:], 3000)
	o.PutUint16(buf[ifd1:], 0) // IFD1: no entries, next 0
	rebaseAllIFDsAfterCR2Marker(buf, 16, o)

	check := func(name string, got, want uint32) {
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
	check("strip array pointer", o.Uint32(buf[18+8:]), uint32(arr))
	check("strip[0]", o.Uint32(buf[arr:]), 2008)
	check("strip[1]", o.Uint32(buf[arr+4:]), 3008)
	check("inline JPEG offset", o.Uint32(buf[30+8:]), 1008)
	check("next-IFD pointer", o.Uint32(buf[arr-4:]), uint32(ifd1))
}

// TestInjectCR2_CorpusChainPreserved (task #281, corpus): every corpus CR2
// keeps all its IFDs and byte-identical image data across Inject.
func TestInjectCR2_CorpusChainPreserved(t *testing.T) {
	t.Parallel()
	var files []string
	for _, pat := range []string{"*.cr2", "*.CR2"} {
		m, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "corpus", "raw", "*", pat))
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Skip("corpus CR2 files absent (see testdata/download.sh)")
	}
	for _, f := range files {
		t.Run(strings.ReplaceAll(filepath.Base(f), " ", "_"), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			want := imageBlocksPerIFD(t, data, false)
			out := injectCR2(t, data)
			got := imageBlocksPerIFD(t, out, true)
			if len(want) != 4 {
				t.Fatalf("input has %d IFDs, want 4", len(want))
			}
			if len(got) != len(want) {
				t.Fatalf("output has %d IFDs, want %d", len(got), len(want))
			}
			for i := range want {
				if !bytes.Equal(got[i], want[i]) {
					t.Errorf("IFD%d image data differs (%d vs %d bytes)", i, len(got[i]), len(want[i]))
				}
			}
			assertCR2RawIFDPointer(t, data, out)
		})
	}
}
