package gometadata

// bench_task389_test.go — task #389 (sprint 50): Read of TIFF-family files
// whose IFDs lie after the image data must use memory proportional to the
// metadata, not to the file.

import (
	"bytes"
	"path/filepath"
	"testing"
)

// BenchmarkRead_TIFFTrailingIFD measures Read of corpus files whose metadata
// structures lie after (or among) the image data: a 22.6 MB DNG and a large
// TIFF. The acceptance bound for the DNG is < 1 MiB/op.
func BenchmarkRead_TIFFTrailingIFD(b *testing.B) {
	for _, c := range []struct{ name, path string }{
		{"DNG-PentaxQS1", filepath.Join("testdata", "corpus", "raw", "metadata-extractor", "Pentax QS1.dng")},
	} {
		b.Run(c.name, func(b *testing.B) {
			data := benchFile(b, c.path)
			r := bytes.NewReader(data)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				_, _ = r.Seek(0, 0)
				if _, err := Read(r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
