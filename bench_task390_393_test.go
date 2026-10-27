package gometadata

// bench_task390_393_test.go — tasks #390-#393 (sprint 50): Write of HEIF/AVIF,
// WebP, ORF, RW2 and JPEG files must allocate memory proportional to the
// metadata, not to the file. The public Write is measured for every container;
// the container-level Inject (the entry point the other packages expose) is
// measured for the containers whose Inject is not reached by Write.

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/heif"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/orf"
	"github.com/FlavioCFOliveira/GoMetadata/format/webp"
)

// writeBenchCase is one corpus file measured by BenchmarkWrite_Streaming.
type writeBenchCase struct {
	name string
	path string
}

// writeBenchCases lists the largest corpus file of each container of tasks
// #390-#393.
func writeBenchCases() []writeBenchCase {
	corpus := func(parts ...string) string {
		return filepath.Join(append([]string{"testdata", "corpus"}, parts...)...)
	}
	return []writeBenchCase{
		{"HEIC-IMG_2928", corpus("heif", "metadata-extractor", "IMG_2928.HEIC")},
		{"HEIC-IMG_2929", corpus("heif", "metadata-extractor", "IMG_2929.HEIC")},
		{"WebP-VP8X", corpus("webp", "metadata-extractor", "webpvp8x.webp")},
		{"WebP-VP8L", corpus("webp", "metadata-extractor", "webpvp8l.webp")},
		{"ORF-TG-4", corpus("raw", "metadata-extractor", "Olympus TG-4.orf")},
		{"RW2-DMC-GF7", corpus("raw", "metadata-extractor", "Panasonic DMC-GF7.rw2")},
		{"JPEG-samsung-s9", corpus("jpeg", "libexif-testsuite", "samsung-s9.jpg")},
	}
}

// BenchmarkWrite_Streaming measures Write (SetCaption) of the largest corpus
// file of each container, from an in-memory source. The output buffer is
// reused, so the reported allocations are the library's.
func BenchmarkWrite_Streaming(b *testing.B) {
	for _, c := range writeBenchCases() {
		b.Run(c.name, func(b *testing.B) {
			data := benchFile(b, c.path)
			m, err := Read(bytes.NewReader(data))
			if err != nil {
				b.Fatal(err)
			}
			m.SetCaption("bench")
			var out bytes.Buffer
			out.Grow(len(data) + 1<<16)
			r := bytes.NewReader(data)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for b.Loop() {
				out.Reset()
				_, _ = r.Seek(0, io.SeekStart)
				if err := Write(r, &out, m); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkInject_Streaming measures the container-level Inject (the entry
// point the format packages export) of HEIF, WebP and ORF, with a new XMP
// packet. ORF and RW2 are written by Write through the TIFF relocator, not
// through Inject; RW2 is not measured here: Inject of a real RW2 file fails
// for the reason the baseline did (its RawDataOffset block lies beyond the
// strips the generic TIFF writer knows), which is not this benchmark's subject.
func BenchmarkInject_Streaming(b *testing.B) {
	xmp := []byte(`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="w"?>`)
	type injector func(io.ReadSeeker, io.Writer, []byte, []byte, []byte, bool) error
	for _, c := range []struct {
		name string
		path string
		fn   injector
	}{
		{"HEIC-IMG_2928", writeBenchCases()[0].path, heif.Inject},
		{"WebP-VP8X", writeBenchCases()[2].path, webp.Inject},
		{"ORF-TG-4", writeBenchCases()[4].path, orf.Inject},
	} {
		b.Run(c.name, func(b *testing.B) {
			data := benchFile(b, c.path)
			var out bytes.Buffer
			out.Grow(len(data) + 1<<16)
			r := bytes.NewReader(data)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for b.Loop() {
				out.Reset()
				_, _ = r.Seek(0, io.SeekStart)
				if err := c.fn(r, &out, nil, nil, xmp, true); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
