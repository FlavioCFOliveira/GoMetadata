package heif

// stream_task390_test.go — task #390: the streaming Inject must write the bytes
// (and return the errors) of the whole-file implementation (oracleInject,
// oracle_test.go) for every input: the corpus, synthetic files of every layout
// the write path has a branch for, and truncations and corruptions of them.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// genOpts selects the layout of a synthetic HEIF file.
type genOpts struct {
	moov          string // "", "before" (moov before meta), "after", "nested" (meta inside moov)
	moov2         bool   // a second moov box after the first
	stco64        bool   // a co64 box besides the stco box
	openEndedMdat bool   // the last box has size 0
	extMeta       bool   // the meta box has the 64-bit largesize header
	extMoov       bool   // the moov box has the 64-bit largesize header
	trailing      []byte // bytes after the last box
	items         string // "none", "exif", "xmp", "both", "image"
}

// genHEIF builds a synthetic file. The offsets (iloc, stco, co64) are absolute,
// so the file is built twice.
func genHEIF(o genOpts) []byte {
	image := []byte("IMAGE")
	exifItem := append([]byte{0, 0, 0, 0}, conformanceMinimalTIFF()...)
	xmpItem := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	payload := append(append(append([]byte{}, image...), exifItem...), xmpItem...)

	largesize := bmffLargesizeBox
	build := func(mdat uint64) []byte {
		infes := [][]byte{bmffInfeV2(1, "hvc1")}
		entries := []bmffIlocItem{{id: 1, offset: uint32(mdat), length: uint32(len(image))}} //nolint:gosec // G115: test file
		if o.items == "exif" || o.items == "both" {
			infes = append(infes, bmffInfeV2(2, "Exif"))
			entries = append(entries, bmffIlocItem{id: 2, offset: uint32(mdat) + uint32(len(image)), length: uint32(len(exifItem))}) //nolint:gosec // G115: test file
		}
		if o.items == "xmp" || o.items == "both" {
			infes = append(infes, bmffInfeV2WithContentType(3, "mime", "application/rdf+xml"))
			entries = append(entries, bmffIlocItem{id: 3, offset: uint32(mdat) + uint32(len(image)+len(exifItem)), length: uint32(len(xmpItem))}) //nolint:gosec // G115: test file
		}
		metaBody := append([]byte{0, 0, 0, 0}, bmffFullBox("pitm", 0, 0, []byte{0, 1})...)
		metaBody = append(metaBody, bmffIinf(infes...)...)
		metaBody = append(metaBody, bmffIloc(entries)...)
		meta := bmffBox("meta", metaBody)
		if o.extMeta {
			meta = largesize("meta", metaBody)
		}

		stcoBody := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 4), 4)
		stcoBody = binary.BigEndian.AppendUint32(stcoBody, uint32(mdat))   //nolint:gosec // G115: test file
		stcoBody = binary.BigEndian.AppendUint32(stcoBody, uint32(mdat)+3) //nolint:gosec // G115: test file
		stcoBody = binary.BigEndian.AppendUint32(stcoBody, 1<<20)          // beyond the file
		stbl := bmffFullBox("stco", 0, 0, stcoBody)
		if o.stco64 {
			co64 := binary.BigEndian.AppendUint32(nil, 2)
			co64 = binary.BigEndian.AppendUint64(co64, mdat+1)
			co64 = binary.BigEndian.AppendUint64(co64, mdat+2)
			stbl = append(stbl, bmffFullBox("co64", 0, 0, co64)...)
		}
		trak := bmffBox("trak", bmffBox("mdia", bmffBox("minf", bmffBox("stbl", stbl))))
		wrapMoov := func(body []byte) []byte {
			if o.extMoov {
				return largesize("moov", body)
			}
			return bmffBox("moov", body)
		}

		out := bmffFtyp("heic", 0, "mif1")
		switch o.moov {
		case "before":
			out = append(out, wrapMoov(trak)...)
			out = append(out, meta...)
		case "after":
			out = append(out, meta...)
			out = append(out, wrapMoov(trak)...)
		case "nested":
			out = append(out, wrapMoov(append(append([]byte{}, trak...), meta...))...)
		default:
			out = append(out, meta...)
		}
		if o.moov2 {
			out = append(out, wrapMoov(trak)...)
		}
		mdatBox := bmffBox("mdat", payload)
		if o.openEndedMdat {
			binary.BigEndian.PutUint32(mdatBox, 0)
		}
		out = append(out, mdatBox...)
		return append(out, o.trailing...)
	}
	first := build(0)
	// The payload starts 8 bytes after the start of the mdat box, which starts
	// where the first build's boxes before it end.
	return build(uint64(len(first) - len(o.trailing) - len(payload))) //nolint:gosec // G115: lengths are non-negative
}

// genVariants returns the synthetic files: every layout option in turn, and a
// combination of them.
func genVariants() map[string][]byte {
	v := map[string][]byte{}
	for _, moov := range []string{"", "before", "after", "nested"} {
		for _, items := range []string{"none", "exif", "xmp", "both", "image"} {
			v["moov="+moov+"/items="+items] = genHEIF(genOpts{moov: moov, items: items})
		}
	}
	for _, items := range []string{"exif", "both", "image"} {
		v["openended/"+items] = genHEIF(genOpts{moov: "before", items: items, openEndedMdat: true})
		v["trailing/"+items] = genHEIF(genOpts{moov: "after", items: items, trailing: []byte("trailing bytes that are not a box")})
		v["extmeta/"+items] = genHEIF(genOpts{moov: "before", items: items, extMeta: true})
		v["extmoov/"+items] = genHEIF(genOpts{moov: "nested", items: items, extMoov: true})
		v["moov2/"+items] = genHEIF(genOpts{moov: "before", moov2: true, items: items})
		v["co64/"+items] = genHEIF(genOpts{moov: "after", stco64: true, items: items})
		v["nested-openended/"+items] = genHEIF(genOpts{moov: "nested", items: items, openEndedMdat: true})
	}
	return v
}

// streamPayloads are the (rawEXIF, rawXMP) pairs every file is written with.
func streamPayloads() []struct {
	name      string
	exif, xmp []byte
} {
	big := bytes.Repeat([]byte("<x:xmpmeta/>"), 300)
	return []struct {
		name      string
		exif, xmp []byte
	}{
		{"exif", minimalTIFFExif(), nil},
		{"xmp", nil, []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF/></x:xmpmeta>`)},
		{"both", minimalTIFFExif(), []byte(`<x:xmpmeta/>`)},
		{"bigxmp", nil, big},
		{"empty", []byte{}, []byte{}},
	}
}

// assertSameAsOracle writes data with both implementations and compares the
// output and the error.
func assertSameAsOracle(t *testing.T, name string, data []byte) {
	t.Helper()
	for _, pc := range streamPayloads() {
		var want, got bytes.Buffer
		wantErr := oracleInject(bytes.NewReader(data), &want, pc.exif, nil, pc.xmp, true)
		err := Inject(bytes.NewReader(data), &got, pc.exif, nil, pc.xmp, true)
		if (err != nil) != (wantErr != nil) || (err != nil && err.Error() != wantErr.Error()) {
			t.Fatalf("%s/%s: err = %v, oracle err = %v", name, pc.name, err, wantErr)
		}
		if err == nil && !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("%s/%s: output differs from the whole-file implementation (%d vs %d bytes)", name, pc.name, got.Len(), want.Len())
		}
		if err != nil && got.Len() != 0 {
			t.Fatalf("%s/%s: %d bytes written before the error %v", name, pc.name, got.Len(), err)
		}
	}
}

// TestInjectMatchesWholeFileImplementation compares the two implementations on
// the synthetic files, the corpus, and truncations and corruptions of them.
func TestInjectMatchesWholeFileImplementation(t *testing.T) {
	t.Parallel()
	seeds := genVariants()
	for _, pattern := range []string{"testdata/*", "../../testdata/corpus/heif/*/*"} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if data, err := os.ReadFile(m); err == nil {
				seeds[m] = data
			}
		}
	}
	for name, data := range seeds {
		assertSameAsOracle(t, name, data)
		// Mutate only the head of a large file: the structures are there, and
		// every comparison copies the whole input twice.
		head := data[:min(len(data), 8192)]
		for cut := range min(len(head), 160) {
			assertSameAsOracle(t, name+"/cut"+strconv.Itoa(cut), head[:cut])
		}
		for _, frac := range []int{2, 3, 5} {
			assertSameAsOracle(t, name+"/cutfrac", data[:len(data)/frac])
			assertSameAsOracle(t, name+"/cutend", data[:len(data)-len(data)/frac])
		}
		rng := rand.New(rand.NewPCG(uint64(len(data)), 390)) //nolint:gosec // G115,G404: deterministic seed for reproducible corruptions, not a secret
		for range 120 {
			mut := bytes.Clone(head)
			for range 1 + rng.IntN(3) {
				mut[rng.IntN(len(mut))] = byte(rng.IntN(256)) //nolint:gosec // G115: IntN(256) fits a byte
			}
			assertSameAsOracle(t, name+"/flip", mut)
		}
	}
}

// FuzzInjectMatchesWholeFile: for any input and payloads the streaming Inject
// and the whole-file implementation agree on the output and the error (task
// #390).
func FuzzInjectMatchesWholeFile(f *testing.F) {
	for _, data := range genVariants() {
		f.Add(data, uint8(3))
	}
	f.Fuzz(func(t *testing.T, data []byte, which uint8) {
		var e, x []byte
		if which&1 != 0 {
			e = minimalTIFFExif()
		}
		if which&2 != 0 {
			x = []byte(`<x:xmpmeta/>`)
		}
		var want, got bytes.Buffer
		wantErr := oracleInject(bytes.NewReader(data), &want, e, nil, x, true)
		err := Inject(bytes.NewReader(data), &got, e, nil, x, true)
		if (err != nil) != (wantErr != nil) || (err != nil && err.Error() != wantErr.Error()) {
			t.Fatalf("err = %v, oracle err = %v", err, wantErr)
		}
		if err == nil && !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("output differs from the whole-file implementation")
		}
	})
}

// largeHEIF returns a sparse HEIF file of total bytes: the structures of a
// genHEIF file with a 'free' box that fills the space between the meta box and
// the mdat box (so that the chunk offsets and the item offsets move).
func largeHEIF(total int64, moovAfter bool) *testutil.SparseFile {
	small := genHEIF(genOpts{moov: map[bool]string{true: "after", false: "before"}[moovAfter], items: "both"})
	// Insert a free box before the mdat box: [ftyp meta moov] free mdat. The
	// file's own offsets point at the old mdat position, which is what the
	// write shifts (or not): the test checks sizes and memory, not the offsets.
	mdatAt := bytes.LastIndex(small, []byte("mdat")) - 4
	head, tail := small[:mdatAt], small[mdatAt:]
	free := make([]byte, 8)
	binary.BigEndian.PutUint32(free, uint32(total-int64(len(small)))) //nolint:gosec // G115: total is below 4 GiB
	copy(free[4:], "free")
	return testutil.NewSparseFile(total,
		testutil.Patch{Off: 0, Data: head},
		testutil.Patch{Off: int64(len(head)), Data: free},
		testutil.Patch{Off: total - int64(len(tail)), Data: tail})
}

// TestInjectLargerThanFormerCap: a 300 MiB source (the former 256 MiB cap) is
// written with the size the format gives, and the memory the write allocates
// does not grow with the file.
//
//nolint:paralleltest // measures the heap of the process: no other test may allocate meanwhile
func TestInjectLargerThanFormerCap(t *testing.T) {
	const total = 300 << 20
	for _, moovAfter := range []bool{false, true} {
		src := largeHEIF(total, moovAfter)
		small := genHEIF(genOpts{moov: map[bool]string{true: "after", false: "before"}[moovAfter], items: "both"})
		var smallOut bytes.Buffer
		if err := Inject(bytes.NewReader(small), &smallOut, minimalTIFFExif(), nil, []byte("<x:xmpmeta/>"), true); err != nil {
			t.Fatal(err)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		var out testutil.CountingWriter
		if err := Inject(src, &out, minimalTIFFExif(), nil, []byte("<x:xmpmeta/>"), true); err != nil {
			t.Fatalf("moovAfter=%v: Inject: %v", moovAfter, err)
		}
		runtime.ReadMemStats(&after)
		// The output is the source with the meta box and the 'mdat' payloads of the
		// small file's write: the same growth.
		if want := int64(total) + int64(smallOut.Len()-len(small)); out.N != want {
			t.Errorf("moovAfter=%v: output = %d bytes, want %d", moovAfter, out.N, want)
		}
		if grown := after.TotalAlloc - before.TotalAlloc; grown > 1<<20 {
			t.Errorf("moovAfter=%v: Inject of a %d MiB file allocated %d bytes, want < 1 MiB", moovAfter, total>>20, grown)
		}
	}
}

// truncatedSource claims a size through Seek(SeekEnd) but delivers only data.
type truncatedSource struct {
	data  []byte
	claim int64
	pos   int64
}

func (s *truncatedSource) Read(p []byte) (int, error) {
	if s.pos >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += int64(n)
	return n, nil
}

func (s *truncatedSource) Seek(off int64, whence int) (int64, error) {
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
// (security: untrusted sizes are bounds checked; the copy of a range stops at
// the end of the data).
func TestInjectTruncatedSource(t *testing.T) {
	t.Parallel()
	for name, data := range map[string][]byte{
		"plain":  genHEIF(genOpts{moov: "before", items: "both"}),
		"nested": genHEIF(genOpts{moov: "nested", items: "both", openEndedMdat: true}),
	} {
		for cut := 1; cut < len(data); cut += 7 {
			src := &truncatedSource{data: data[:cut], claim: int64(len(data))}
			var out bytes.Buffer
			err := Inject(src, &out, minimalTIFFExif(), nil, nil, true)
			if err == nil {
				t.Fatalf("%s: cut at %d of %d: Inject succeeded on a truncated source", name, cut, len(data))
			}
		}
	}
}

// noSeek is a reader that cannot report its size.
type noSeek struct{ io.Reader }

func (noSeek) Seek(int64, int) (int64, error) { return 0, errors.New("seek not supported") }

// TestInjectNonSeekableSource: a source that cannot seek is refused with an
// error and never read into memory.
func TestInjectNonSeekableSource(t *testing.T) {
	t.Parallel()
	data := genHEIF(genOpts{items: "both"})
	if err := Inject(noSeek{bytes.NewReader(data)}, io.Discard, minimalTIFFExif(), nil, nil, true); err == nil {
		t.Fatal("Inject on a source that cannot seek: expected an error")
	}
}

// TestInjectBoxOverLimit: a meta or moov box larger than maxFileSize is not
// held in memory: the write is refused with ErrFileTooLarge before any output.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectBoxOverLimit(t *testing.T) {
	for _, moov := range []string{"", "before"} {
		data := genHEIF(genOpts{moov: moov, items: "both"})
		setMaxFileSizeForTest(t, 40)
		var out bytes.Buffer
		err := Inject(bytes.NewReader(data), &out, minimalTIFFExif(), nil, nil, true)
		if !errors.Is(err, ErrFileTooLarge) {
			t.Errorf("moov=%q: err = %v, want ErrFileTooLarge", moov, err)
		}
		if out.Len() != 0 {
			t.Errorf("moov=%q: %d bytes written before the refusal", moov, out.Len())
		}
	}
}

// TestPatchBoxSize pins the edge cases of the size patch of a box that wraps the
// meta box: the 32-bit field is left alone when the new size does not fit
// (task #83: it must never be truncated), a non-positive size is never written,
// and the 64-bit largesize is patched in place.
func TestPatchBoxSize(t *testing.T) {
	t.Parallel()
	hdr32 := func(size uint32) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint32(b, size)
		copy(b[4:], "moov")
		return b
	}
	cases := []struct {
		name  string
		hdr   []byte
		size  uint64
		delta int
		want  uint64
		wide  bool
	}{
		{"grow", hdr32(100), 100, 4, 104, false},
		{"shrink", hdr32(100), 100, -4, 96, false},
		{"overflow keeps the field", hdr32(0xFFFFFFFF), 0xFFFFFFFF, 1, 0xFFFFFFFF, false},
		{"fits at the limit", hdr32(0xFFFFFFFE), 0xFFFFFFFE, 1, 0xFFFFFFFF, false},
		{"non-positive keeps the field", hdr32(8), 8, -8, 8, false},
		{"wide", append([]byte{0, 0, 0, 1, 'm', 'o', 'o', 'v'}, binary.BigEndian.AppendUint64(nil, 5000)...), 5000, 20, 5020, true},
		{"wide overflow past 32 bits is fine", append([]byte{0, 0, 0, 1, 'm', 'o', 'o', 'v'}, binary.BigEndian.AppendUint64(nil, 0xFFFFFFFF)...), 0xFFFFFFFF, 10, 0x100000009, true},
	}
	for _, tc := range cases {
		patchBoxSize(tc.hdr, tc.size, tc.delta)
		var got uint64
		if tc.wide {
			got = binary.BigEndian.Uint64(tc.hdr[8:])
		} else {
			got = uint64(binary.BigEndian.Uint32(tc.hdr))
		}
		if got != tc.want {
			t.Errorf("%s: size field = %d, want %d", tc.name, got, tc.want)
		}
	}
}
