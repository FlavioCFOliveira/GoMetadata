package gometadata

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// largestCorpusFile returns the largest file of testdata/corpus/dir with the
// extension (lower case, with the dot) that Read accepts and Write can write
// after a SetCaption, or "" when the corpus is absent.
func largestCorpusFile(b *testing.B, dir, ext string) string {
	b.Helper()
	matches, _ := filepath.Glob(filepath.Join("testdata", "corpus", dir, "*", "*"+ext))
	sort.Slice(matches, func(i, j int) bool {
		si, _ := os.Stat(matches[i])
		sj, _ := os.Stat(matches[j])
		return si.Size() > sj.Size() || si.Size() == sj.Size() && matches[i] < matches[j]
	})
	for _, f := range matches {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			continue
		}
		m.SetCaption("bench")
		if Write(bytes.NewReader(data), &bytes.Buffer{}, m) == nil {
			return f
		}
	}
	return ""
}

// BenchmarkTask400_403_Write measures Write after a modification, for a JPEG
// whose EXIF is replaced in place (Canon_40D fixture), a JPEG and a PNG of the
// corpus that gain an XMP packet. The output buffer is reused, so allocations
// are the library's.
func BenchmarkTask400_403_Write(b *testing.B) {
	type bench struct {
		name   string
		path   string
		modify func(*Metadata)
	}
	benches := []bench{{"JPEG-Canon_40D-SetISO", "testdata/fixtures/Canon_40D.jpg", func(m *Metadata) { m.SetISO(1234) }}}
	if f := largestCorpusFile(b, "jpeg", ".jpg"); f != "" {
		benches = append(benches, bench{"JPEG-largest-SetCaption", f, func(m *Metadata) { m.SetCaption("bench") }})
	}
	if f := largestCorpusFile(b, "png", ".png"); f != "" {
		benches = append(benches, bench{"PNG-largest-SetCaption", f, func(m *Metadata) { m.SetCaption("bench") }})
	}
	for _, bc := range benches {
		b.Run(bc.name, func(b *testing.B) {
			data, err := os.ReadFile(bc.path)
			if err != nil {
				b.Fatal(err)
			}
			m, err := Read(bytes.NewReader(data))
			if err != nil {
				b.Fatal(err)
			}
			bc.modify(m)
			var out bytes.Buffer
			out.Grow(len(data) + 1<<16)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for range b.N {
				out.Reset()
				if err := Write(bytes.NewReader(data), &out, m); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
