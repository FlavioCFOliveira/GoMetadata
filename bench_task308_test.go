package gometadata

import (
	"bytes"
	"os"
	"testing"
	"time"
)

// The task #308 benchmarks use only the public API, so that the same file
// measures the release before the mapping table (benchstat comparison).

// benchTask308Metadata reads the IPTC Photo Metadata reference image, which
// carries EXIF, IPTC and XMP.
func benchTask308Metadata(b *testing.B) (*Metadata, []byte) {
	b.Helper()
	src, err := os.ReadFile("testdata/fixtures/IPTC-PhotometadataRef-Std2021.1.jpg")
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		b.Fatalf("Read: %v", err)
	}
	return m, src
}

// BenchmarkTask308_Getters measures the reconciled getters on a file with all
// three metadata forms.
func BenchmarkTask308_Getters(b *testing.B) {
	m, _ := benchTask308Metadata(b)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.Caption()
		_ = m.Copyright()
		_ = m.Creator()
		_ = m.Keywords()
		_, _ = m.DateTimeOriginal()
		_ = m.CameraModel()
		_ = m.LensModel()
		_, _ = m.Orientation()
	}
}

// BenchmarkTask308_Setters measures every convenience setter once per
// iteration on a file with all three metadata forms.
func BenchmarkTask308_Setters(b *testing.B) {
	m, _ := benchTask308Metadata(b)
	ts := time.Date(2026, 3, 14, 15, 9, 26, 0, time.FixedZone("", 19800))
	creators := []string{"Alice", "Bob"}
	keywords := []string{"alpha", "beta"}
	b.ReportAllocs()
	for b.Loop() {
		m.SetCaption("caption")
		m.SetCopyright("copyright")
		m.SetCreator("creator")
		m.SetCreators(creators)
		m.SetKeywords(keywords)
		m.SetDateTimeOriginal(ts)
		m.SetCameraModel("model")
		m.SetMake("make")
		m.SetLensModel("lens")
		m.SetGPS(38.7223, -9.1393)
		m.SetExposureTime(1, 250)
		m.SetFNumber(2.8)
		m.SetISO(400)
		m.SetFocalLength(50)
		m.SetOrientation(6)
		m.SetImageSize(4000, 3000)
	}
}

// BenchmarkTask308_SetAndWrite measures setting every field and writing the
// file, the end-to-end cost of one reconciled edit.
func BenchmarkTask308_SetAndWrite(b *testing.B) {
	_, src := benchTask308Metadata(b)
	ts := time.Date(2026, 3, 14, 15, 9, 26, 0, time.FixedZone("", 19800))
	var out bytes.Buffer
	b.ReportAllocs()
	for b.Loop() {
		m, err := Read(bytes.NewReader(src))
		if err != nil {
			b.Fatal(err)
		}
		m.SetCaption("caption")
		m.SetCreators([]string{"Alice", "Bob"})
		m.SetDateTimeOriginal(ts)
		m.SetOrientation(6)
		out.Reset()
		if err := Write(bytes.NewReader(src), &out, m); err != nil {
			b.Fatal(err)
		}
	}
}
