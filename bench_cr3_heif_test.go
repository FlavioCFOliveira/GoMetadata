package gometadata

// bench_cr3_heif_test.go — root-level read and write benchmarks for the
// ISO BMFF containers (CR3, HEIC), added by sprint 46 effort E4 (tasks
// #334-#337) so that the CMT2 parse, the GPS removal rule and the
// largesize-aware meta lookup have measured evidence.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// benchFile returns the bytes of path, skipping the benchmark when a corpus
// file is absent (docs/TESTING.md §2.1a).
func benchFile(b *testing.B, path string) []byte {
	b.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		b.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	if err != nil {
		b.Fatal(err)
	}
	return data
}

// BenchmarkCR3ReadCorpus measures Read of a CR3 corpus file.
func BenchmarkCR3ReadCorpus(b *testing.B) {
	data := benchFile(b, filepath.Join("testdata", "corpus", "raw", "exiftool", "CanonRaw.cr3"))
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := Read(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCR3WriteCorpus measures Read-modify-Write of a CR3 corpus file.
func BenchmarkCR3WriteCorpus(b *testing.B) {
	data := benchFile(b, filepath.Join("testdata", "corpus", "raw", "exiftool", "CanonRaw.cr3"))
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			b.Fatal(err)
		}
		m.SetCaption("benchmark")
		if err := Write(bytes.NewReader(data), io.Discard, m); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkHEICReadWrite measures Read-modify-Write of the metadata-free HEIC
// fixture, which creates the Exif and XMP items.
func BenchmarkHEICReadWrite(b *testing.B) {
	data := benchFile(b, filepath.Join("format", "heif", "testdata", "no_metadata.heic"))
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			b.Fatal(err)
		}
		m.SetCaption("benchmark")
		if err := Write(bytes.NewReader(data), io.Discard, m); err != nil {
			b.Fatal(err)
		}
	}
}
