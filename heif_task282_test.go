package gometadata

// heif_task282_test.go — task #282: the iloc parser of format/heif omitted
// the 16-bit data_reference_index (ISO/IEC 14496-12 §8.11.3.2), so no EXIF
// or XMP item of a real HEIF/HEIC/AVIF file was ever located, and a caption
// write produced an Exif item ExifTool reported as "No Extents".

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// heifCorpusModels lists every HEIF/HEIC/AVIF corpus file for which
// ExifTool 13 reports an EXIF Model (exiftool -T -Model), with that value.
var heifCorpusModels = map[string]string{ //nolint:gochecknoglobals // read-only test table
	"exif-samples/IMG_5195.HEIC":                     "iPhone 11 Pro Max",
	"exif-samples/iphone_13_pro_max.HEIC":            "iPhone 13 Pro Max",
	"exiv2/2021-02-13-1929.heic":                     "iPhone XR",
	"exiv2/IMG_3578.heic":                            "iPhone XR",
	"exiv2/Stonehenge.heic":                          "NIKON D5300",
	"metadata-extractor/exif-at-eof_2.heic":          "iPhone 11 Pro",
	"metadata-extractor/exif-at-eof.heic":            "iPhone 11 Pro",
	"metadata-extractor/IMG_1034_2.heic":             "iPhone 8",
	"metadata-extractor/IMG_1034.heic":               "iPhone 8",
	"metadata-extractor/IMG_2927_2.HEIC":             "iPhone 11 Pro",
	"metadata-extractor/IMG_2927.HEIC":               "iPhone 11 Pro",
	"metadata-extractor/IMG_2928_2.HEIC":             "iPhone 11 Pro",
	"metadata-extractor/IMG_2928.HEIC":               "iPhone 11 Pro",
	"metadata-extractor/IMG_2929_2.HEIC":             "iPhone 11 Pro",
	"metadata-extractor/IMG_2929.HEIC":               "iPhone 11 Pro",
	"metadata-extractor/Issue 263 dotnet_2.heic":     "iPhone XR",
	"metadata-extractor/Issue 263 dotnet.heic":       "iPhone XR",
	"metadata-extractor/Issue 437 (dotnet)_2.heic":   "Galaxy S24 FE",
	"metadata-extractor/Issue 437 (dotnet).heic":     "Galaxy S24 FE",
	"metadata-extractor/Issue 487_2.heic":            "SM-G975F",
	"metadata-extractor/Issue 487.heic":              "SM-G975F",
	"metadata-extractor/Issue 649_2.avif":            "Canon EOS 70D",
	"metadata-extractor/Issue 649.avif":              "Canon EOS 70D",
	"metadata-extractor/Sony ILCE-7M4 (A7M4)_2.heif": "ILCE-7M4",
	"metadata-extractor/Sony ILCE-7M4 (A7M4).heif":   "ILCE-7M4",
}

// readHEIFCorpus returns the bytes of a HEIF corpus file, skipping the test
// when the optional corpus is absent (docs/TESTING.md §2.1a).
func readHEIFCorpus(t *testing.T, rel string) []byte {
	t.Helper()
	path := filepath.Join("testdata", "corpus", "heif", rel)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// TestHEIF_Task282_CorpusCameraModel: CameraModel equals the ExifTool Model
// for every corpus file where ExifTool reads one.
func TestHEIF_Task282_CorpusCameraModel(t *testing.T) {
	t.Parallel()
	for rel, want := range heifCorpusModels {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(readHEIFCorpus(t, rel)))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got := m.CameraModel(); got != want {
				t.Errorf("CameraModel = %q, want %q (ExifTool Model)", got, want)
			}
		})
	}
}

// TestHEIF_Task282_IPhoneCaptionWrite: a caption write into an iPhone HEIC
// produces a box-structured file whose Exif item resolves, holds the new
// caption, and keeps every other EXIF tag of the source unchanged.
func TestHEIF_Task282_IPhoneCaptionWrite(t *testing.T) {
	t.Parallel()
	src := readHEIFCorpus(t, "exif-samples/iphone_13_pro_max.HEIC")
	before, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read source: %v", err)
	}
	if before.EXIF == nil || before.XMP == nil {
		t.Fatalf("source: EXIF=%v XMP=%v, want both", before.EXIF != nil, before.XMP != nil)
	}

	out := writeCaption(t, src, "task 282")
	assertTopLevelBoxesContiguous(t, out)

	after, err := Read(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Read output: %v", err)
	}
	if after.EXIF == nil {
		t.Fatal("output: the Exif item no longer resolves")
	}
	if got := after.Caption(); got != "task 282" {
		t.Errorf("output Caption = %q, want %q", got, "task 282")
	}
	if got := after.CameraModel(); got != "iPhone 13 Pro Max" {
		t.Errorf("output CameraModel = %q, want %q", got, "iPhone 13 Pro Max")
	}
	assertIFDEqual(t, "IFD0", before.EXIF.IFD0, after.EXIF.IFD0, exif.TagImageDescription)
	assertIFDEqual(t, "ExifIFD", before.EXIF.ExifIFD, after.EXIF.ExifIFD)
	assertIFDEqual(t, "GPS", before.EXIF.GPSIFD, after.EXIF.GPSIFD)
}

// assertIFDEqual fails t when the two IFDs differ in any entry other than
// the ignored tags (and the sub-IFD pointer tags, whose values are offsets).
func assertIFDEqual(t *testing.T, name string, want, got *exif.IFD, ignore ...exif.TagID) {
	t.Helper()
	if (want == nil) != (got == nil) {
		t.Fatalf("%s: present before=%v after=%v", name, want != nil, got != nil)
	}
	if want == nil {
		return
	}
	skip := func(tag exif.TagID) bool {
		switch tag {
		case exif.TagExifIFDPointer, exif.TagGPSIFDPointer, exif.TagInteropIFDPointer:
			return true
		}
		for _, i := range ignore {
			if tag == i {
				return true
			}
		}
		return false
	}
	n := 0
	for _, e := range want.Entries {
		if skip(e.Tag) {
			continue
		}
		n++
		g := got.Get(e.Tag)
		if g == nil {
			t.Errorf("%s: tag 0x%04X lost", name, uint16(e.Tag))
			continue
		}
		if g.Type != e.Type || g.Count != e.Count || !bytes.Equal(g.Value, e.Value) {
			t.Errorf("%s: tag 0x%04X changed", name, uint16(e.Tag))
		}
	}
	m := 0
	for _, e := range got.Entries {
		if !skip(e.Tag) {
			m++
		}
	}
	if n != m {
		t.Errorf("%s: %d entries before, %d after", name, n, m)
	}
}

// assertTopLevelBoxesContiguous fails t unless data is a sequence of
// ISO/IEC 14496-12 §4.2 boxes with explicit sizes ending exactly at EOF.
func assertTopLevelBoxesContiguous(t *testing.T, data []byte) {
	t.Helper()
	pos := uint64(0)
	for pos < uint64(len(data)) {
		if pos+8 > uint64(len(data)) {
			t.Fatalf("truncated box header at %d", pos)
		}
		size := uint64(binary.BigEndian.Uint32(data[pos:]))
		if size == 1 && pos+16 <= uint64(len(data)) {
			size = binary.BigEndian.Uint64(data[pos+8:])
		}
		if size < 8 || pos+size > uint64(len(data)) {
			t.Fatalf("box at %d has size %d (file length %d)", pos, size, len(data))
		}
		pos += size
	}
}
