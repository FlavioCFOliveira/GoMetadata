package exif

import "testing"

// BenchmarkTextAccessors measures the text accessors of the tags that cameras
// pad with blanks (tasks #386 and #401): Model, LensModel and Artist, each with
// a padded value and with a value that has no padding.
func BenchmarkTextAccessors(b *testing.B) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"padded", "DSC-HX9V                       \x00\x00\x00\x00"},
		{"plain", "EF24-70mm f/2.8L II USM\x00"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			d := &IFD{}
			v := []byte(tc.value)
			d.set(TagModel, TypeASCII, uint32(len(v)), v, false)  //nolint:gosec // G115: tiny benchmark value
			d.set(TagArtist, TypeASCII, uint32(len(v)), v, false) //nolint:gosec // G115: tiny benchmark value
			x := &IFD{}
			x.set(TagLensModel, TypeASCII, uint32(len(v)), v, false) //nolint:gosec // G115: tiny benchmark value
			e := &EXIF{IFD0: d, ExifIFD: x}
			b.ReportAllocs()
			for b.Loop() {
				_ = e.CameraModel()
				_ = e.LensModel()
				_ = e.Creator()
			}
		})
	}
}

// BenchmarkCameraModel measures the one text accessor that already removed
// trailing blanks before task #401 (task #386), with the padding of a camera.
func BenchmarkCameraModel(b *testing.B) {
	d := &IFD{}
	v := []byte("DSC-HX9V                       \x00\x00\x00\x00")
	d.set(TagModel, TypeASCII, uint32(len(v)), v, false) //nolint:gosec // G115: tiny benchmark value
	e := &EXIF{IFD0: d}
	b.ReportAllocs()
	for b.Loop() {
		_ = e.CameraModel()
	}
}
