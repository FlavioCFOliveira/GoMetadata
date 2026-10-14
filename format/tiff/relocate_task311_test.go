package tiff

// relocate_task311_test.go — regression tests for task #311: a TIFF write
// carries every SubIFD of every chained IFD (IFD1-IFDn included), every
// element of a SubIFDs array, SubIFD next-chains and nested SubIFDs, values
// stored before a SubIFD directory, and Exif/GPS IFD pointers of type 13
// (IFD). Each test compares the directory tree (dirTree: path, entry count,
// image-data hash) of the input with that of the written output.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// testXMP is a minimal XMP packet: writing it forces a full relocation.
var testXMP = []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end='w'?>`) //nolint:gochecknoglobals // read-only test fixture

// writeXMP writes testXMP into data through the TIFF write path used by
// gometadata.Write (InjectWithEXIFStream with the parsed EXIF).
func writeXMP(t *testing.T, data []byte) []byte {
	t.Helper()
	e, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	var out bytes.Buffer
	if err := InjectWithEXIFStream(bytes.NewReader(data), data, true, e, nil, testXMP, &out); err != nil {
		t.Fatalf("InjectWithEXIFStream: %v", err)
	}
	return out.Bytes()
}

// assertSameTree fails when the directory trees of in and out differ in
// anything but entry counts (the write adds the XMP tag to IFD0).
func assertSameTree(t *testing.T, in, out []byte) {
	t.Helper()
	strip := func(lines []string) []string {
		r := make([]string, len(lines))
		for i, l := range lines {
			f := bytes.Fields([]byte(l))
			r[i] = string(f[0]) + " " + string(f[len(f)-1])
		}
		return r
	}
	a, b := dirTree(t, in), dirTree(t, out)
	if !slices.Equal(strip(a), strip(b)) {
		t.Fatalf("directory tree changed by the write:\n in: %q\nout: %q", a, b)
	}
}

func sub(strip string, entries ...tEntry) *tNode {
	return &tNode{strip: []byte(strip), entries: append([]tEntry{{tag: 0x00FE, typ: uint16(exif.TypeLong), count: 1, val: []byte{1, 0, 0, 0}}}, entries...)}
}

// TestRelocate_SubIFDsOfChainedIFDs: SubIFDs of IFD1 and IFD2 (corpus
// 0c84d07e: one SubIFD under each of IFD1-IFD3) are carried; before the fix
// only the SubIFDs of IFD0 were, and the arrays of IFD1+ kept source offsets.
func TestRelocate_SubIFDsOfChainedIFDs(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	ifd2 := &tNode{strip: []byte("page-2"), entries: []tEntry{ptrEntry(uint16(exif.TagSubIFDs), uint16(exif.TypeLong), sub("page-2-reduced"))}}
	ifd1 := &tNode{strip: []byte("page-1"), next: ifd2, entries: []tEntry{ptrEntry(uint16(exif.TagSubIFDs), uint16(exif.TypeLong), sub("page-1-reduced"))}}
	ifd0 := &tNode{strip: []byte("page-0"), next: ifd1}
	in := (&tAsm{order: le}).bytes(t, ifd0)
	if n := len(dirTree(t, in)); n != 5 {
		t.Fatalf("fixture has %d directories, want 5", n)
	}
	assertSameTree(t, in, writeXMP(t, in))
}

// TestRelocate_SubIFDArrayWithNestedSubIFD: a three-element SubIFDs array
// whose first SubIFD has its own SubIFD. Before the fix the array was filled
// from the flat list of all SubIFDs, so element 1 pointed at the nested one.
func TestRelocate_SubIFDArrayWithNestedSubIFD(t *testing.T) {
	t.Parallel()
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(order.String(), func(t *testing.T) {
			t.Parallel()
			a := sub("A", ptrEntry(uint16(exif.TagSubIFDs), uint16(exif.TypeLong), sub("A1")))
			root := &tNode{strip: []byte("main"), entries: []tEntry{ptrEntry(uint16(exif.TagSubIFDs), 13, a, sub("B"), sub("C"))}}
			in := (&tAsm{order: order}).bytes(t, root)
			assertSameTree(t, in, writeXMP(t, in))
		})
	}
}

// TestRelocate_SubIFDChain: one SubIFDs element whose SubIFD links four more
// through next-IFD pointers (corpus f15fca5b, declared type IFD). Before the
// fix the next pointers were copied stale and four SubIFDs were lost.
func TestRelocate_SubIFDChain(t *testing.T) {
	t.Parallel()
	s4 := sub("s4")
	s3 := sub("s3")
	s3.next = s4
	s2 := sub("s2")
	s2.next = s3
	s1 := sub("s1")
	s1.next = s2
	s0 := sub("s0")
	s0.next = s1
	root := &tNode{strip: []byte("main"), entries: []tEntry{ptrEntry(uint16(exif.TagSubIFDs), 13, s0)}}
	in := (&tAsm{order: binary.BigEndian}).bytes(t, root)
	if n := len(dirTree(t, in)); n != 6 {
		t.Fatalf("fixture has %d directories, want 6", n)
	}
	out := writeXMP(t, in)
	assertSameTree(t, in, out)
	e, err := exif.Parse(out)
	if err != nil {
		t.Fatalf("Parse(out): %v", err)
	}
	if got := e.IFD0.Get(exif.TagSubIFDs); got == nil || got.Type != 13 {
		t.Errorf("SubIFDs entry type = %v, want 13 (IFD) preserved", got)
	}
}

// TestRelocate_SubIFDValuesBeforeDirectory: a SubIFD whose out-of-line values
// precede its directory (libtiff layout). The former verbatim copy started at
// the directory, so the values were lost and their pointers left stale.
func TestRelocate_SubIFDValuesBeforeDirectory(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	res := []byte{72, 0, 0, 0, 1, 0, 0, 0}
	s := sub("reduced",
		tEntry{tag: 0x011A, typ: uint16(exif.TypeRational), count: 1, val: res},
		asciiEntry(0x010E, "a description longer than four bytes"))
	s.valsFirst = true
	root := &tNode{strip: []byte("main"), entries: []tEntry{ptrEntry(uint16(exif.TagSubIFDs), uint16(exif.TypeLong), s)}}
	in := (&tAsm{order: le}).bytes(t, root)
	out := writeXMP(t, in)
	assertSameTree(t, in, out)
	e, err := exif.Parse(out)
	if err != nil {
		t.Fatalf("Parse(out): %v", err)
	}
	arr := e.IFD0.Get(exif.TagSubIFDs)
	if arr == nil || len(arr.Value) != 4 {
		t.Fatalf("SubIFDs = %v", arr)
	}
	si, _, ok := exif.ParseIFDAt(out, le.Uint32(arr.Value), le)
	if !ok {
		t.Fatal("SubIFD unreadable in the output")
	}
	if x := si.Get(0x011A); x == nil || !bytes.Equal(x.Value, res) {
		t.Errorf("XResolution = %v, want %x", x, res)
	}
	if d := si.Get(0x010E); d == nil || d.String() != "a description longer than four bytes" {
		t.Errorf("ImageDescription = %v", d)
	}
}

// TestRelocate_ExifGPSPointersOfTypeIFD: Exif and GPS IFD pointers declared
// with type 13 (corpus "Issue 151.tif"). exif.Parse used to read them as
// 1-byte strings, so the write dropped both IFDs.
func TestRelocate_ExifGPSPointersOfTypeIFD(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	exifIFD := &tNode{entries: []tEntry{{tag: 0x829A, typ: uint16(exif.TypeRational), count: 1, val: []byte{1, 0, 0, 0, 100, 0, 0, 0}}}}
	gps := &tNode{entries: []tEntry{{tag: 0x0000, typ: uint16(exif.TypeByte), count: 4, val: []byte{2, 2, 0, 0}}}}
	root := &tNode{strip: []byte("pixels"), entries: []tEntry{
		ptrEntry(uint16(exif.TagExifIFDPointer), 13, exifIFD),
		ptrEntry(uint16(exif.TagGPSIFDPointer), 13, gps),
	}}
	in := (&tAsm{order: le}).bytes(t, root)
	out := writeXMP(t, in)
	assertSameTree(t, in, out)
	if n := len(dirTree(t, out)); n != 3 {
		t.Errorf("output has %d directories, want 3 (IFD0, Exif, GPS)", n)
	}
}

// TestRelocate_CorpusDirectoryTrees (task #311, corpus): the four files of the
// task keep every directory and the image data of each, ExifTool 13.55
// directory counts ("exiftool -v1" IFD/SubIFD/ExifIFD/GPS) in parentheses:
// 0c84d07e (8), f15fca5b (7), "Issue 151.tif" (3), 9b286add (5).
func TestRelocate_CorpusDirectoryTrees(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]int{
		"0c84d07e1b22b76f24cccc70d8788e4a.tif": 8,
		"f15fca5bfdef640840d8ea30570c8cea.tif": 7,
		"Issue 151.tif":                        3,
		"9b286add70871bbbef1601997b429344.tif": 5,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("..", "..", "testdata", "corpus", "tiff", "metadata-extractor", name)
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if n := len(dirTree(t, data)); n != want {
				t.Fatalf("input has %d directories, ExifTool lists %d", n, want)
			}
			assertSameTree(t, data, writeXMP(t, data))
		})
	}
}
