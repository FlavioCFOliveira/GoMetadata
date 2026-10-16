package exif

import (
	"testing"
	"time"
)

// withVersion returns an EXIF whose Exif IFD holds the ExifVersion v.
func withVersion(v string) *EXIF {
	e := New(true)
	e.ExifIFD.set(TagExifVersion, TypeUndefined, 4, []byte(v), false)
	return e
}

// version returns the ExifVersion of e as text.
func version(t *testing.T, e *EXIF) string {
	t.Helper()
	v := e.ExifIFD.Get(TagExifVersion)
	if v == nil {
		t.Fatal("no ExifVersion")
	}
	return string(v.Value)
}

// TestMinExifVersionTable: the table of the tags that a version of the standard
// introduced (version.go). S-49.
func TestMinExifVersionTable(t *testing.T) {
	t.Parallel()
	t.Run("S-49", func(t *testing.T) {
		t.Parallel()
		type tc struct {
			tag  TagID
			want string
		}
		var cases []tc
		add := func(lo, hi TagID, want string) {
			for tag := lo; tag <= hi; tag++ {
				cases = append(cases, tc{tag, want})
			}
		}
		add(0x8830, 0x8835, "0230")               // SensitivityType .. ISOSpeedLatitudezzz
		add(0xA430, 0xA435, "0230")               // CameraOwnerName .. LensSerialNumber
		add(0x9010, 0x9012, "0231")               // OffsetTime, OffsetTimeOriginal, OffsetTimeDigitized
		add(0x9400, 0x9405, "0231")               // Temperature .. CameraElevationAngle
		add(0xA460, 0xA462, "0232")               // CompositeImage .. SourceExposureTimesOfCompositeImage
		add(0xA436, 0xA43C, "0300")               // ImageTitle .. MetadataEditingSoftware
		add(0xA40D, 0xA412, "0300")               // DevelopmentType .. NoiseReduction (Exif 3.1)
		cases = append(cases, tc{0x9287, "0300"}) // LearningOptOutIn
		for _, tag := range []TagID{TagExifVersion, TagDateTimeOriginal, TagExposureTime, TagFNumber, TagISOSpeedRatings, TagFocalLength, TagPixelXDimension, TagColorSpace, 0x9209, 0xA500, 0x9013, 0x9406, 0xA463, 0x8829, 0x8836, 0xA42F, 0xA43D} {
			cases = append(cases, tc{tag, ""})
		}
		for _, c := range cases {
			if got := minExifVersion(c.tag); got != c.want {
				t.Errorf("minExifVersion(%#04x) = %q, want %q", uint16(c.tag), got, c.want)
			}
		}
	})
}

// TestRequireExifVersionRaisesAndNeverLowers: S-49.
func TestRequireExifVersionRaisesAndNeverLowers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		have, min, want string
	}{
		{"0220", "0231", "0231"},
		{"0221", "0230", "0230"},
		{"0230", "0231", "0231"},
		{"0231", "0231", "0231"},
		{"0231", "0232", "0232"},
		{"0232", "0300", "0300"},
		{"0232", "0231", "0232"}, // never lowered
		{"0300", "0231", "0300"},
		{"0300", "0230", "0300"},
		{"0310", "0300", "0310"},
		{"0100", "0230", "0230"},
		{"01xx", "0231", "0231"}, // not four digits: cannot be compared, replaced
		{"0220\x00", "0231", "0231"},
	} {
		t.Run("S-49/"+tc.have+"+"+tc.min, func(t *testing.T) {
			t.Parallel()
			e := withVersion(tc.have)
			e.ExifIFD.Get(TagExifVersion).Count = uint32(len(tc.have)) //nolint:gosec // G115: a 4-byte test string
			e.requireExifVersion(tc.min)
			if got := version(t, e); got != tc.want {
				t.Errorf("ExifVersion = %q, want %q", got, tc.want)
			}
			if v := e.ExifIFD.Get(TagExifVersion); tc.want != tc.have && v.Count != 4 {
				t.Errorf("raised ExifVersion has count %d, want 4", v.Count)
			}
		})
	}
}

// TestVersionRaisedBySetters: every setter that writes a version-gated tag
// raises a lower ExifVersion to the minimum of the tag and keeps a higher one.
// S-49.
func TestVersionRaisedBySetters(t *testing.T) {
	t.Parallel()
	when := time.Date(2024, 5, 6, 7, 8, 9, 0, time.FixedZone("", 3600))
	for _, tc := range []struct {
		name string
		set  func(e *EXIF)
		min  string
	}{
		{"SetDateTimeOriginal writes OffsetTimeOriginal", func(e *EXIF) { e.SetDateTimeOriginal(when) }, "0231"},
		{"SetLensModel writes LensModel", func(e *EXIF) { e.SetLensModel("Lens") }, "0230"},
		{"SetCaption with UTF-8 text", func(e *EXIF) { e.SetCaption("legenda: ção") }, "0300"},
		{"SetLensModel with UTF-8 text", func(e *EXIF) { e.SetLensModel("Objetiva ç") }, "0300"},
		{"SetMake with UTF-8 text", func(e *EXIF) { e.SetMake("Fabricante ç") }, "0300"},
		{"SetCaption with ASCII text leaves it", func(e *EXIF) { e.SetCaption("caption") }, ""},
	} {
		for _, have := range []string{"0100", "0220", "0221", "0230", "0231", "0232", "0300"} {
			t.Run("S-49/"+tc.name+"/"+have, func(t *testing.T) {
				t.Parallel()
				e := withVersion(have)
				tc.set(e)
				want := have
				if tc.min != "" && have < tc.min {
					want = tc.min
				}
				if got := version(t, e); got != want {
					t.Errorf("ExifVersion = %q, want %q", got, want)
				}
			})
		}
	}
}

// TestVersionRaiseCreatesNothingForUngatedSetters: a setter of an ungated tag
// leaves the ExifVersion of a file as it is, however old it is.
func TestVersionRaiseCreatesNothingForUngatedSetters(t *testing.T) {
	t.Parallel()
	t.Run("S-49", func(t *testing.T) {
		t.Parallel()
		e := withVersion("0210")
		e.SetExposureTime(1, 100)
		e.SetFNumber(2.8)
		e.SetISO(200)
		e.SetFocalLength(35)
		e.SetImageSize(10, 20)
		e.SetOrientation(1)
		if got := version(t, e); got != "0210" {
			t.Errorf("ExifVersion = %q, want it left at 0210", got)
		}
	})
}
