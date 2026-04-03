package imgmetadata

import (
	"math"
	"testing"
	"time"

	"github.com/flaviocfo/img-metadata/exif"
	"github.com/flaviocfo/img-metadata/iptc"
	"github.com/flaviocfo/img-metadata/xmp"
)

// newTestMetadata builds a Metadata whose three components are all non-nil
// and ready for writing.  EXIF is initialised from a minimal parse so that
// IFD0 is valid.
func newTestMetadata(t *testing.T) *Metadata {
	t.Helper()

	// Build a minimal LE TIFF so EXIF.IFD0 is valid.
	const ifdOff = uint32(8)
	buf := make([]byte, 8+2+12+4) // header + 1 IFD entry + next-ptr
	buf[0], buf[1] = 'I', 'I'
	buf[2], buf[3] = 0x2A, 0x00
	buf[4] = byte(ifdOff)
	// 1 entry
	buf[8], buf[9] = 0x01, 0x00
	// entry: tag=0x0100 (ImageWidth), type=4 (Long), count=1, value=1
	buf[10], buf[11] = 0x00, 0x01 // tag LE
	buf[12], buf[13] = 0x04, 0x00 // type LE
	buf[14], buf[15], buf[16], buf[17] = 0x01, 0x00, 0x00, 0x00 // count LE
	buf[18], buf[19], buf[20], buf[21] = 0x01, 0x00, 0x00, 0x00 // value LE
	// next IFD offset = 0
	buf[22], buf[23], buf[24], buf[25] = 0x00, 0x00, 0x00, 0x00

	e, err := exif.Parse(buf)
	if err != nil {
		t.Fatalf("newTestMetadata: exif.Parse: %v", err)
	}

	i := &iptc.IPTC{Records: make(map[uint8][]iptc.Dataset)}
	x := &xmp.XMP{Properties: make(map[string]map[string]string)}

	return &Metadata{EXIF: e, IPTC: i, XMP: x}
}

// TestMetadataSetters verifies that every Metadata setter writes through to
// the underlying components and that getters return the expected values.
func TestMetadataSetters(t *testing.T) {
	t.Run("SetCaption", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetCaption("golden hour")
		if got := m.EXIF.Caption(); got != "golden hour" {
			t.Errorf("EXIF.Caption = %q, want %q", got, "golden hour")
		}
		if got := m.IPTC.Caption(); got != "golden hour" {
			t.Errorf("IPTC.Caption = %q, want %q", got, "golden hour")
		}
		if got := m.XMP.Caption(); got != "golden hour" {
			t.Errorf("XMP.Caption = %q, want %q", got, "golden hour")
		}
		// Metadata getter returns XMP value (highest priority).
		if got := m.Caption(); got != "golden hour" {
			t.Errorf("m.Caption() = %q, want %q", got, "golden hour")
		}
	})

	t.Run("SetCopyright", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetCopyright("(c) 2024 Alice")
		if got := m.EXIF.Copyright(); got != "(c) 2024 Alice" {
			t.Errorf("EXIF.Copyright = %q", got)
		}
		if got := m.IPTC.Copyright(); got != "(c) 2024 Alice" {
			t.Errorf("IPTC.Copyright = %q", got)
		}
		if got := m.XMP.Copyright(); got != "(c) 2024 Alice" {
			t.Errorf("XMP.Copyright = %q", got)
		}
	})

	t.Run("SetCreator", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetCreator("Bob")
		if got := m.EXIF.Creator(); got != "Bob" {
			t.Errorf("EXIF.Creator = %q", got)
		}
		if got := m.IPTC.Creator(); got != "Bob" {
			t.Errorf("IPTC.Creator = %q", got)
		}
		if got := m.XMP.Creator(); got != "Bob" {
			t.Errorf("XMP.Creator = %q", got)
		}
	})

	t.Run("SetCameraModel", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetCameraModel("Canon EOS R5")
		if got := m.EXIF.CameraModel(); got != "Canon EOS R5" {
			t.Errorf("EXIF.CameraModel = %q", got)
		}
		if got := m.XMP.CameraModel(); got != "Canon EOS R5" {
			t.Errorf("XMP.CameraModel = %q", got)
		}
	})

	t.Run("SetGPS", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetGPS(51.5074, -0.1278)
		lat, lon, ok := m.EXIF.GPS()
		if !ok {
			t.Fatal("EXIF.GPS() ok=false after SetGPS")
		}
		if math.Abs(lat-51.5074) > 0.001 {
			t.Errorf("EXIF lat = %f, want ~51.5074", lat)
		}
		if math.Abs(lon-(-0.1278)) > 0.001 {
			t.Errorf("EXIF lon = %f, want ~-0.1278", lon)
		}
		xlat, xlon, xok := m.XMP.GPS()
		if !xok {
			t.Fatal("XMP.GPS() ok=false after SetGPS")
		}
		if math.Abs(xlat-51.5074) > 0.001 {
			t.Errorf("XMP lat = %f, want ~51.5074", xlat)
		}
		if math.Abs(xlon-(-0.1278)) > 0.001 {
			t.Errorf("XMP lon = %f, want ~-0.1278", xlon)
		}
	})

	t.Run("SetKeywords", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetKeywords([]string{"travel", "street"})
		if kws := m.IPTC.Keywords(); len(kws) != 2 {
			t.Errorf("IPTC.Keywords count = %d, want 2", len(kws))
		}
		if kws := m.XMP.Keywords(); len(kws) != 2 {
			t.Errorf("XMP.Keywords count = %d, want 2", len(kws))
		}
	})

	t.Run("SetLensModel", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetLensModel("EF 50mm f/1.2L")
		if got := m.EXIF.LensModel(); got != "EF 50mm f/1.2L" {
			t.Errorf("EXIF.LensModel = %q", got)
		}
		if got := m.XMP.LensModel(); got != "EF 50mm f/1.2L" {
			t.Errorf("XMP.LensModel = %q", got)
		}
	})

	t.Run("SetMake", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetMake("Canon")
		if got := m.Make(); got != "Canon" {
			t.Errorf("Make = %q, want Canon", got)
		}
	})

	t.Run("SetDateTimeOriginal", func(t *testing.T) {
		m := newTestMetadata(t)
		ts := time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC)
		m.SetDateTimeOriginal(ts)
		got, ok := m.EXIF.DateTimeOriginal()
		if !ok {
			t.Fatal("EXIF.DateTimeOriginal missing after set")
		}
		if !got.Equal(ts) {
			t.Errorf("EXIF.DateTimeOriginal = %v, want %v", got, ts)
		}
		xmpStr := m.XMP.DateTimeOriginal()
		if xmpStr == "" {
			t.Error("XMP.DateTimeOriginal empty after set")
		}
	})

	t.Run("SetExposureTime", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetExposureTime(1, 250)
		num, den, ok := m.ExposureTime()
		if !ok {
			t.Fatal("ExposureTime missing after set")
		}
		if num != 1 || den != 250 {
			t.Errorf("ExposureTime = %d/%d, want 1/250", num, den)
		}
	})

	t.Run("SetFNumber", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetFNumber(4.0)
		f, ok := m.FNumber()
		if !ok {
			t.Fatal("FNumber missing after set")
		}
		if math.Abs(f-4.0) > 0.001 {
			t.Errorf("FNumber = %f, want 4.0", f)
		}
	})

	t.Run("SetISO", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetISO(800)
		iso, ok := m.ISO()
		if !ok {
			t.Fatal("ISO missing after set")
		}
		if iso != 800 {
			t.Errorf("ISO = %d, want 800", iso)
		}
	})

	t.Run("SetFocalLength", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetFocalLength(85.0)
		fl, ok := m.FocalLength()
		if !ok {
			t.Fatal("FocalLength missing after set")
		}
		if math.Abs(fl-85.0) > 0.001 {
			t.Errorf("FocalLength = %f, want 85.0", fl)
		}
	})

	t.Run("SetOrientation", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetOrientation(6)
		v, ok := m.Orientation()
		if !ok {
			t.Fatal("Orientation missing after set")
		}
		if v != 6 {
			t.Errorf("Orientation = %d, want 6", v)
		}
	})

	t.Run("SetImageSize", func(t *testing.T) {
		m := newTestMetadata(t)
		m.SetImageSize(1920, 1080)
		w, h, ok := m.ImageSize()
		if !ok {
			t.Fatal("ImageSize missing after set")
		}
		if w != 1920 || h != 1080 {
			t.Errorf("ImageSize = %dx%d, want 1920x1080", w, h)
		}
	})
}

// TestMetadataSettersNilComponents verifies that every Metadata setter is a
// no-op (and does not panic) when all component pointers are nil.
func TestMetadataSettersNilComponents(t *testing.T) {
	m := &Metadata{} // all components nil
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	m.SetCaption("x")
	m.SetCopyright("x")
	m.SetCreator("x")
	m.SetCameraModel("x")
	m.SetGPS(0, 0)
	m.SetKeywords([]string{"a"})
	m.SetLensModel("x")
	m.SetMake("x")
	m.SetDateTimeOriginal(ts)
	m.SetExposureTime(1, 100)
	m.SetFNumber(1.4)
	m.SetISO(100)
	m.SetFocalLength(50)
	m.SetOrientation(1)
	m.SetImageSize(100, 100)
	// reaching here without panic is the pass condition
}
