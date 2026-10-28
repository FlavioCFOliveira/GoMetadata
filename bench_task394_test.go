package gometadata

// bench_task394_test.go — task #394 (sprint 50): Read of JPEG files that carry
// Extended XMP must not allocate a multiple of the file size.

import (
	"bytes"
	"path/filepath"
	"testing"
)

// BenchmarkRead_ExtendedXMP measures Read of corpus JPEGs that carry Extended
// XMP (Adobe XMP Specification Part 3 §1.1.3.1). The acceptance bound for
// exiv2-bug922.jpg (1.97 MB) is < 8 MB/op, down from 23.5 MB/op.
func BenchmarkRead_ExtendedXMP(b *testing.B) {
	for _, c := range []struct{ name, path string }{
		{"exiv2-bug922", filepath.Join("testdata", "corpus", "jpeg", "exiv2", "exiv2-bug922.jpg")},
		{"GoogleCardboard", filepath.Join("testdata", "corpus", "jpeg", "metadata-extractor", "Google Cardboard.jpg")},
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
		b.Run(c.name+"/WithoutXMP", func(b *testing.B) {
			data := benchFile(b, c.path)
			r := bytes.NewReader(data)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				_, _ = r.Seek(0, 0)
				if _, err := Read(r, WithoutXMP()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
