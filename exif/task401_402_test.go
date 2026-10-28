package exif

import (
	"strings"
	"testing"
)

// TestGPSZeroDenominator covers task #402: a RATIONAL or SRATIONAL GPS value
// with a zero denominator is not a valid number (TIFF 6.0 §2, p. 15: a
// RATIONAL is the quotient of two LONGs), so the coordinate is reported as
// absent and Parse adds a warning. Each row puts the zero denominator in a
// different position (degrees, minutes, seconds) of latitude or longitude.
func TestGPSZeroDenominator(t *testing.T) {
	t.Parallel()

	okLat := [3][2]int32{{60, 1}, {8, 1}, {4812, 100}}
	okLon := [3][2]int32{{24, 1}, {54, 1}, {2450, 100}}
	tests := []struct {
		name     string
		lat, lon [3][2]int32
		wantTag  string
	}{
		{"latitude degrees", [3][2]int32{{60, 0}, {8, 1}, {0, 1}}, okLon, "0x0002"},
		{"latitude minutes (issue repro)", [3][2]int32{{60, 1}, {8, 0}, {0, 1}}, okLon, "0x0002"},
		{"latitude seconds", [3][2]int32{{60, 1}, {8, 1}, {0, 0}}, okLon, "0x0002"},
		{"longitude degrees", okLat, [3][2]int32{{24, 0}, {54, 1}, {1, 1}}, "0x0004"},
		{"longitude minutes", okLat, [3][2]int32{{24, 1}, {54, 0}, {1, 1}}, "0x0004"},
		{"longitude seconds", okLat, [3][2]int32{{24, 1}, {54, 1}, {1, 0}}, "0x0004"},
	}
	for typName, typ := range map[string]DataType{"RATIONAL": TypeRational, "SRATIONAL": TypeSRational} {
		for _, tc := range tests {
			t.Run(typName+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				latB, lonB := srat3(tc.lat), srat3(tc.lon)
				g := gpsIFDOf(typ, typ, latB, lonB, "N", "E")
				if lat, lon, ok := parseGPS(g); ok || lat != 0 || lon != 0 {
					t.Fatalf("parseGPS = (%v, %v, %v), want (0, 0, false)", lat, lon, ok)
				}
				raw := gpsTIFF(t, typ, latB, lonB)
				e, err := Parse(raw)
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				if _, _, ok := e.GPS(); ok {
					t.Error("GPS ok = true, want false")
				}
				if len(e.Warnings) != 1 || !strings.Contains(e.Warnings[0], tc.wantTag) {
					t.Errorf("Warnings = %q, want exactly one naming tag %s", e.Warnings, tc.wantTag)
				}
			})
		}
	}
}

// TestGPSZeroDenominatorAltitudeAndTime covers the other GPS RATIONAL fields
// of task #402: altitude and time stamp with a zero denominator are absent and
// produce a warning; a zero numerator over a valid denominator stays valid.
func TestGPSZeroDenominatorAltitudeAndTime(t *testing.T) {
	t.Parallel()
	g := &IFD{}
	g.set(TagGPSAltitude, TypeRational, 1, srat3([3][2]int32{{5, 0}})[:8], false)
	g.set(TagGPSTimeStamp, TypeRational, 3, srat3([3][2]int32{{13, 1}, {45, 0}, {0, 1}}), false)
	e := &EXIF{GPSIFD: g}
	if _, ok := e.GPSAltitude(); ok {
		t.Error("GPSAltitude ok = true for a zero denominator")
	}
	if _, _, _, ok := e.GPSTimeStamp(); ok {
		t.Error("GPSTimeStamp ok = true for a zero denominator")
	}
	w := gpsDeviationWarnings(g)
	if len(w) != 2 || !strings.Contains(w[0], "0x0006") || !strings.Contains(w[1], "0x0007") {
		t.Errorf("warnings = %q, want one for 0x0006 and one for 0x0007", w)
	}

	ok := &IFD{}
	ok.set(TagGPSAltitude, TypeRational, 1, srat3([3][2]int32{{0, 1}})[:8], false)
	if v, valid := (&EXIF{GPSIFD: ok}).GPSAltitude(); !valid || v != 0 {
		t.Errorf("GPSAltitude of 0/1 = (%v, %v), want (0, true)", v, valid)
	}
	if w := gpsDeviationWarnings(ok); len(w) != 0 {
		t.Errorf("warnings = %q, want none for 0/1", w)
	}
}

// TestBlankTrimmedText covers task #401: the text accessors end the value at
// the first NUL and remove trailing blanks (ExifTool Exif.pm RawConv
// s/\s+$//), keep leading and inner blanks, and never modify the stored bytes.
func TestBlankTrimmedText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		typ   DataType
		value string
		want  string
	}{
		{"trailing spaces then NUL", TypeASCII, "CASIO COMPUTER CO.,LTD   \x00", "CASIO COMPUTER CO.,LTD"},
		{"trailing spaces, no NUL", TypeASCII, "Ver.1.00   ", "Ver.1.00"},
		{"spaces after the first NUL", TypeASCII, "abc \x00   def  ", "abc"},
		{"tab and newline padding", TypeASCII, "abc\t\n\r\f\v ", "abc"},
		{"leading and inner blanks kept", TypeASCII, "  a  b \x00", "  a  b"},
		{"only blanks", TypeASCII, "    \x00", ""},
		{"empty", TypeASCII, "", ""},
		{"UTF-8 type", TypeUTF8, "caf\xc3\xa9  ", "café"},
		{"no trailing blank", TypeASCII, "Canon\x00", "Canon"},
		{"not a string type", TypeByte, "abc  ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := []byte(tc.value)
			orig := string(raw)
			mk := func(tag TagID) *IFD {
				d := &IFD{}
				d.set(tag, tc.typ, uint32(len(raw)), raw, false) //nolint:gosec // G115: tiny test value
				return d
			}
			e := &EXIF{
				IFD0:    mk(TagMake),
				ExifIFD: mk(TagLensModel),
			}
			e.IFD0.set(TagModel, tc.typ, uint32(len(raw)), raw, false)    //nolint:gosec // G115: tiny
			e.IFD0.set(TagSoftware, tc.typ, uint32(len(raw)), raw, false) //nolint:gosec // G115: tiny
			e.IFD0.set(TagArtist, tc.typ, uint32(len(raw)), raw, false)   //nolint:gosec // G115: tiny
			got := map[string]string{
				"Make": e.Make(), "Model": e.CameraModel(), "Software": e.Software(),
				"LensModel": e.LensModel(), "Artist": e.Creator(),
			}
			for name, v := range got {
				if v != tc.want {
					t.Errorf("%s = %q, want %q", name, v, tc.want)
				}
			}
			if string(raw) != orig {
				t.Errorf("stored bytes changed: %q, want %q", raw, orig)
			}
		})
	}

	t.Run("untrimmed accessors unchanged", func(t *testing.T) {
		t.Parallel()
		d := &IFD{}
		d.set(TagImageDescription, TypeASCII, 8, []byte("cap   \x00\x00"), false)
		d.set(TagCopyright, TypeASCII, 8, []byte("(c)   \x00\x00"), false)
		e := &EXIF{IFD0: d}
		if e.Caption() != "cap   " || e.Copyright() != "(c)   " {
			t.Errorf("Caption/Copyright = %q/%q, want the blanks kept", e.Caption(), e.Copyright())
		}
	})

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()
		var e *EXIF
		if e.Make() != "" || e.Software() != "" || e.CameraModel() != "" || e.LensModel() != "" || e.Creator() != "" {
			t.Error("nil EXIF accessor returned a non-empty string")
		}
		if (&EXIF{}).Make() != "" || (&EXIF{}).LensModel() != "" {
			t.Error("empty EXIF accessor returned a non-empty string")
		}
	})
}

// BenchmarkBlankTrimmedString measures the trimming accessor on a padded
// value (Make as cameras write it).
func BenchmarkBlankTrimmedString(b *testing.B) {
	d := &IFD{}
	d.set(TagMake, TypeASCII, 32, []byte("CASIO COMPUTER CO.,LTD         \x00\x00\x00"), false)
	e := &EXIF{IFD0: d}
	b.ReportAllocs()
	for b.Loop() {
		_ = e.Make()
	}
}

// TestBlankTrimmedStringRuns covers the word-at-a-time path of the trim: every
// length of padding around the eight-byte step, a run broken by another blank,
// and a value that is all blanks.
func TestBlankTrimmedStringRuns(t *testing.T) {
	t.Parallel()
	for n := range 41 {
		raw := []byte("ab" + strings.Repeat(" ", n))
		e := &IFDEntry{Tag: TagMake, Type: TypeASCII, Count: uint32(len(raw)), Value: raw} //nolint:gosec // G115: tiny test value
		if got := e.blankTrimmedString(); got != "ab" {
			t.Errorf("%d trailing spaces: got %q, want %q", n, got, "ab")
		}
		allBlank := &IFDEntry{Tag: TagMake, Type: TypeASCII, Count: uint32(n), Value: []byte(strings.Repeat(" ", n))}
		if got := allBlank.blankTrimmedString(); got != "" {
			t.Errorf("%d spaces only: got %q, want empty", n, got)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"a" + strings.Repeat(" ", 12) + "\t" + strings.Repeat(" ", 12), "a"},
		{"a\n" + strings.Repeat(" ", 9) + "\r\f\v", "a"},
		{"a" + strings.Repeat(" ", 20) + "b" + strings.Repeat(" ", 20), "a" + strings.Repeat(" ", 20) + "b"},
		{"a\x00" + strings.Repeat(" ", 16), "a"},
		{"a  ", "a "}, // U+00A0 is not one of the blanks ExifTool's \s removes in a byte string
	} {
		e := &IFDEntry{Tag: TagMake, Type: TypeASCII, Count: uint32(len(tc.in)), Value: []byte(tc.in)} //nolint:gosec // G115: tiny test value
		if got := e.blankTrimmedString(); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}
