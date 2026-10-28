package exif

import (
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"strings"
	"testing"
)

// srat3 encodes three (numerator, denominator) pairs of an SRATIONAL (or
// RATIONAL) value in little-endian order.
func srat3(pairs [3][2]int32) []byte {
	b := make([]byte, 24)
	for i, p := range pairs {
		binary.LittleEndian.PutUint32(b[i*8:], uint32(p[0]))   //nolint:gosec // G115: test helper, intentional bit-reinterpretation
		binary.LittleEndian.PutUint32(b[i*8+4:], uint32(p[1])) //nolint:gosec // G115: test helper, intentional bit-reinterpretation
	}
	return b
}

// gpsIFDOf returns a GPS IFD holding lat/lon of the given types.
func gpsIFDOf(latType, lonType DataType, lat, lon []byte, latRef, lonRef string) *IFD {
	g := &IFD{}
	g.set(TagGPSLatitudeRef, TypeASCII, 2, []byte(latRef+"\x00"), false)
	g.set(TagGPSLatitude, latType, 3, lat, false)
	g.set(TagGPSLongitudeRef, TypeASCII, 2, []byte(lonRef+"\x00"), false)
	g.set(TagGPSLongitude, lonType, 3, lon, false)
	return g
}

// TestGPSSRational covers task #385: the SRATIONAL GPS coordinates that Nokia
// cameras write are accepted (Exif 3.1 §4.6.6 Table 15 defines RATIONAL; the
// deviation is documented on decodeGPSRational), a negative numerator or a
// non-positive denominator is not a valid value, and no input panics.
func TestGPSSRational(t *testing.T) {
	t.Parallel()

	// 60° 8' 48.12" N, 24° 54' 24.5" E: the Nokia 8.3 5G sample, about
	// 60.1467, 24.9068.
	nokiaLat := srat3([3][2]int32{{60, 1}, {8, 1}, {4812, 100}})
	nokiaLon := srat3([3][2]int32{{24, 1}, {54, 1}, {2450, 100}})

	tests := []struct {
		name             string
		latType, lonType DataType
		lat, lon         []byte
		latRef, lonRef   string
		wantLat, wantLon float64
		wantOK           bool
	}{
		{"SRATIONAL positive", TypeSRational, TypeSRational, nokiaLat, nokiaLon, "N", "E", 60 + 8.0/60 + 48.12/3600, 24 + 54.0/60 + 24.5/3600, true},
		{"SRATIONAL southern and western", TypeSRational, TypeSRational, nokiaLat, nokiaLon, "S", "W", -(60 + 8.0/60 + 48.12/3600), -(24 + 54.0/60 + 24.5/3600), true},
		{"SRATIONAL mixed with RATIONAL", TypeSRational, TypeRational, nokiaLat, nokiaLon, "N", "E", 60 + 8.0/60 + 48.12/3600, 24 + 54.0/60 + 24.5/3600, true},
		{"RATIONAL unchanged", TypeRational, TypeRational, nokiaLat, nokiaLon, "N", "E", 60 + 8.0/60 + 48.12/3600, 24 + 54.0/60 + 24.5/3600, true},
		{"SRATIONAL zero coordinate is valid", TypeSRational, TypeSRational, srat3([3][2]int32{{0, 1}, {0, 1}, {0, 1}}), srat3([3][2]int32{{0, 1}, {0, 1}, {0, 1}}), "N", "E", 0, 0, true},
		{"SRATIONAL negative degrees", TypeSRational, TypeSRational, srat3([3][2]int32{{-60, 1}, {8, 1}, {0, 1}}), nokiaLon, "N", "E", 0, 0, false},
		{"SRATIONAL negative minutes", TypeSRational, TypeSRational, nokiaLat, srat3([3][2]int32{{24, 1}, {-1, 1}, {0, 1}}), "N", "E", 0, 0, false},
		{"SRATIONAL zero denominator", TypeSRational, TypeSRational, srat3([3][2]int32{{60, 1}, {8, 0}, {0, 1}}), nokiaLon, "N", "E", 0, 0, false},
		{"SRATIONAL negative denominator", TypeSRational, TypeSRational, srat3([3][2]int32{{60, -1}, {8, 1}, {0, 1}}), nokiaLon, "N", "E", 0, 0, false},
		{"SRATIONAL longitude invalid", TypeSRational, TypeSRational, nokiaLat, srat3([3][2]int32{{24, 0}, {0, 1}, {0, 1}}), "N", "E", 0, 0, false},
		{"SSHORT is not a coordinate", TypeSShort, TypeSShort, nokiaLat, nokiaLon, "N", "E", 0, 0, false},
		{"truncated SRATIONAL value", TypeSRational, TypeSRational, nokiaLat[:16], nokiaLon, "N", "E", 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := gpsIFDOf(tc.latType, tc.lonType, tc.lat, tc.lon, tc.latRef, tc.lonRef)
			lat, lon, ok := parseGPS(g)
			if ok != tc.wantOK {
				t.Fatalf("parseGPS ok = %v, want %v (lat=%v lon=%v)", ok, tc.wantOK, lat, lon)
			}
			if !ok {
				if lat != 0 || lon != 0 {
					t.Errorf("parseGPS = (%v, %v, false), want (0, 0, false)", lat, lon)
				}
				return
			}
			if math.Abs(lat-tc.wantLat) > 1e-9 || math.Abs(lon-tc.wantLon) > 1e-9 {
				t.Errorf("parseGPS = (%.9f, %.9f), want (%.9f, %.9f)", lat, lon, tc.wantLat, tc.wantLon)
			}
		})
	}
}

// TestGPSAltitudeAndTimeStampSRational covers GPSAltitude and GPSTimeStamp of
// task #385.
func TestGPSAltitudeAndTimeStampSRational(t *testing.T) {
	t.Parallel()

	alt := func(typ DataType, num, den int32, ref byte) *EXIF {
		g := &IFD{}
		g.set(TagGPSAltitudeRef, TypeByte, 1, []byte{ref}, false)
		g.set(TagGPSAltitude, typ, 1, srat3([3][2]int32{{num, den}})[:8], false)
		return &EXIF{GPSIFD: g}
	}
	ts := func(typ DataType, p [3][2]int32) *EXIF {
		g := &IFD{}
		g.set(TagGPSTimeStamp, typ, 3, srat3(p), false)
		return &EXIF{GPSIFD: g}
	}

	altTests := []struct {
		name   string
		e      *EXIF
		want   float64
		wantOK bool
	}{
		{"RATIONAL above sea level", alt(TypeRational, 12345, 100, 0), 123.45, true},
		{"RATIONAL below sea level", alt(TypeRational, 500, 100, 1), -5, true},
		{"SRATIONAL above sea level", alt(TypeSRational, 12345, 100, 0), 123.45, true},
		{"SRATIONAL below sea level", alt(TypeSRational, 500, 100, 1), -5, true},
		{"SRATIONAL negative numerator", alt(TypeSRational, -500, 100, 0), 0, false},
		{"SRATIONAL zero denominator", alt(TypeSRational, 500, 0, 0), 0, false},
		{"RATIONAL zero denominator", alt(TypeRational, 500, 0, 0), 0, false},
		{"absent", &EXIF{GPSIFD: &IFD{}}, 0, false},
		{"nil EXIF", nil, 0, false},
		{"nil GPS IFD", &EXIF{}, 0, false},
	}
	for _, tc := range altTests {
		got, ok := tc.e.GPSAltitude()
		if ok != tc.wantOK || (ok && math.Abs(got-tc.want) > 1e-9) {
			t.Errorf("%s: GPSAltitude = (%v, %v), want (%v, %v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}

	good := [3][2]int32{{13, 1}, {45, 1}, {3050, 100}}
	tsTests := []struct {
		name    string
		e       *EXIF
		h, m, s float64
		wantOK  bool
	}{
		{"RATIONAL", ts(TypeRational, good), 13, 45, 30.5, true},
		{"SRATIONAL", ts(TypeSRational, good), 13, 45, 30.5, true},
		{"SRATIONAL negative numerator", ts(TypeSRational, [3][2]int32{{13, 1}, {-45, 1}, {0, 1}}), 0, 0, 0, false},
		{"SRATIONAL zero denominator", ts(TypeSRational, [3][2]int32{{13, 1}, {45, 1}, {0, 0}}), 0, 0, 0, false},
		{"RATIONAL zero denominator", ts(TypeRational, [3][2]int32{{13, 0}, {45, 1}, {0, 1}}), 0, 0, 0, false},
		{"absent", &EXIF{GPSIFD: &IFD{}}, 0, 0, 0, false},
		{"nil EXIF", nil, 0, 0, 0, false},
	}
	for _, tc := range tsTests {
		h, m, s, ok := tc.e.GPSTimeStamp()
		if ok != tc.wantOK || (ok && (math.Abs(h-tc.h) > 1e-9 || math.Abs(m-tc.m) > 1e-9 || math.Abs(s-tc.s) > 1e-9)) {
			t.Errorf("%s: GPSTimeStamp = (%v, %v, %v, %v), want (%v, %v, %v, %v)", tc.name, h, m, s, ok, tc.h, tc.m, tc.s, tc.wantOK)
		}
	}
}

// gpsTIFF returns an EXIF stream whose GPS IFD holds lat/lon of the given
// types, built by Encode, so that Parse sees a real stream.
func gpsTIFF(t *testing.T, typ DataType, lat, lon []byte) []byte {
	t.Helper()
	ifd0 := &IFD{}
	ifd0.set(TagMake, TypeASCII, 6, []byte("Nokia\x00"), false)
	e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: ifd0, GPSIFD: gpsIFDOf(typ, typ, lat, lon, "N", "E")}
	b, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return b
}

// TestGPSSRationalParseAndRoundTrip covers the Parse side of task #385: a
// valid SRATIONAL coordinate is read without a warning, an invalid one is read
// with a warning and ok=false, and re-encoding keeps the type and the bytes of
// the GPS entries (the write path is unchanged).
func TestGPSSRationalParseAndRoundTrip(t *testing.T) {
	t.Parallel()

	nokiaLat := srat3([3][2]int32{{60, 1}, {8, 1}, {4812, 100}})
	nokiaLon := srat3([3][2]int32{{24, 1}, {54, 1}, {2450, 100}})

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		raw := gpsTIFF(t, TypeSRational, nokiaLat, nokiaLon)
		e, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(e.Warnings) != 0 {
			t.Errorf("Warnings = %v, want none for a valid SRATIONAL coordinate", e.Warnings)
		}
		lat, lon, ok := e.GPS()
		if !ok || math.Abs(lat-60.1467) > 1e-4 || math.Abs(lon-24.9068) > 1e-4 {
			t.Errorf("GPS = (%v, %v, %v), want about (60.1467, 24.9068, true)", lat, lon, ok)
		}
		// Round trip: same bytes, same types.
		again, err := Encode(e)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if !bytes.Equal(again, raw) {
			t.Error("Encode(Parse(raw)) differs from raw: the GPS IFD bytes changed")
		}
		for _, tag := range []TagID{TagGPSLatitude, TagGPSLongitude} {
			if ent := e.GPSIFD.Get(tag); ent == nil || ent.Type != TypeSRational {
				t.Errorf("tag 0x%04X: entry %+v, want the original type SRATIONAL", tag, ent)
			}
		}
	})

	t.Run("invalid gives a warning", func(t *testing.T) {
		t.Parallel()
		bad := srat3([3][2]int32{{60, 1}, {-8, 1}, {0, 1}})
		raw := gpsTIFF(t, TypeSRational, bad, nokiaLon)
		e, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if _, _, ok := e.GPS(); ok {
			t.Error("GPS ok = true for a negative SRATIONAL numerator")
		}
		if len(e.Warnings) != 1 || !strings.Contains(e.Warnings[0], "0x0002") || !strings.Contains(e.Warnings[0], "SRATIONAL") {
			t.Errorf("Warnings = %q, want one warning naming tag 0x0002 and SRATIONAL", e.Warnings)
		}
	})

	t.Run("RATIONAL gives no warning", func(t *testing.T) {
		t.Parallel()
		e, err := Parse(gpsTIFF(t, TypeRational, nokiaLat, nokiaLon))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(e.Warnings) != 0 {
			t.Errorf("Warnings = %v, want none", e.Warnings)
		}
	})
}

// FuzzGPSDecode feeds arbitrary GPS value bytes of every type to the GPS
// accessors (task #385: no input may panic).
func FuzzGPSDecode(f *testing.F) {
	f.Add(uint16(10), srat3([3][2]int32{{60, 1}, {8, 1}, {4812, 100}}), byte('N'))
	f.Add(uint16(5), srat3([3][2]int32{{1, 0}, {0, 0}, {-1, -1}}), byte('s'))
	f.Add(uint16(10), []byte{1, 2, 3}, byte(0))
	f.Fuzz(func(t *testing.T, typ uint16, val []byte, ref byte) {
		g := &IFD{}
		g.set(TagGPSLatitudeRef, TypeASCII, 2, []byte{ref, 0}, false)
		g.set(TagGPSLongitudeRef, TypeASCII, 2, []byte{ref, 0}, false)
		g.set(TagGPSLatitude, DataType(typ), 3, val, false)
		g.set(TagGPSLongitude, DataType(typ), 3, val, false)
		g.set(TagGPSAltitude, DataType(typ), 1, val, false)
		g.set(TagGPSTimeStamp, DataType(typ), 3, val, false)
		e := &EXIF{GPSIFD: g}
		lat, lon, ok := e.GPS()
		if ok && (lat < -90 || lat > 90 || lon < -180 || lon > 180 || math.IsNaN(lat) || math.IsNaN(lon)) {
			t.Fatalf("GPS = (%v, %v, true) is out of range", lat, lon)
		}
		_, _ = e.GPSAltitude()
		_, _, _, _ = e.GPSTimeStamp()
		_ = gpsDeviationWarnings(g)
	})
}

// TestIFDEntryStringStopsAtFirstNUL covers task #386: an ASCII value ends at
// its first NUL byte (TIFF 6.0 §2), and the raw bytes stay untouched.
func TestIFDEntryStringStopsAtFirstNUL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		typ   DataType
		value string
		want  string
		multi []string
	}{
		{"terminated", TypeASCII, "DSC-HX9V\x00", "DSC-HX9V", []string{"DSC-HX9V"}},
		{"no terminator", TypeASCII, "abc", "abc", []string{"abc"}},
		{"padding then garbage (cve-2012-2813)", TypeASCII, "DSC-HX9V   \x00\x00\x00\x00H\x00\x00\x00", "DSC-HX9V   ", []string{"DSC-HX9V   ", "", "", "", "H"}},
		{"two strings", TypeASCII, "a\x00b\x00", "a", []string{"a", "b"}},
		{"two strings and padding", TypeASCII, "a\x00b\x00\x00\x00", "a", []string{"a", "b"}},
		{"empty string between", TypeASCII, "a\x00\x00b\x00", "a", []string{"a", "", "b"}},
		{"leading NUL", TypeASCII, "\x00abc", "", []string{"", "abc"}},
		{"only NULs", TypeASCII, "\x00\x00\x00", "", nil},
		{"empty value", TypeASCII, "", "", nil},
		{"UTF-8 type", TypeUTF8, "caf\xc3\xa9\x00junk", "café", []string{"café", "junk"}},
		{"not a string type", TypeByte, "abc\x00def", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := []byte(tc.value)
			e := &IFDEntry{Tag: TagModel, Type: tc.typ, Count: uint32(len(raw)), Value: raw} //nolint:gosec // G115: test value is tiny
			if got := e.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
			if got := e.Strings(); !reflect.DeepEqual(got, tc.multi) {
				t.Errorf("Strings() = %q, want %q", got, tc.multi)
			}
			if string(e.Value) != tc.value {
				t.Error("the entry bytes were modified")
			}
		})
	}
}

// TestCameraModelCutsAtNULAndTrimsBlanks covers the CameraModel of task #386:
// "DSC-HX9V" padded with blanks, NULs and garbage reads as "DSC-HX9V".
func TestCameraModelCutsAtNULAndTrimsBlanks(t *testing.T) {
	t.Parallel()
	raw := []byte("DSC-HX9V" + strings.Repeat(" ", 15) + "\x00\x00\x00\x00H\x00\x00\x00\x01")
	ifd0 := &IFD{}
	ifd0.set(TagModel, TypeASCII, uint32(len(raw)), raw, false) //nolint:gosec // G115: test value is tiny
	e := &EXIF{IFD0: ifd0}
	if got := e.CameraModel(); got != "DSC-HX9V" {
		t.Errorf("CameraModel() = %q, want %q", got, "DSC-HX9V")
	}
}

// TestTrimMakeCutsAtFirstNUL keeps the allocation-free MakerNote dispatch key
// in step with (*IFDEntry).String (task #386).
func TestTrimMakeCutsAtFirstNUL(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"Canon\x00", "Canon \x00\x00", "NIKON CORPORATION\x00junk", " Sony\x00", "\x00Canon"} {
		e := &IFDEntry{Type: TypeASCII, Value: []byte(in)}
		want := strings.TrimSpace(e.String())
		if got := string(trimMake([]byte(in))); got != want {
			t.Errorf("trimMake(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzIFDEntryStrings checks String and Strings against each other for any
// bytes (task #386).
func FuzzIFDEntryStrings(f *testing.F) {
	f.Add([]byte("a\x00b\x00"))
	f.Add([]byte("\x00\x00"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, b []byte) {
		e := &IFDEntry{Type: TypeASCII, Value: b}
		s := e.String()
		if strings.IndexByte(s, 0) >= 0 {
			t.Fatalf("String() = %q holds a NUL", s)
		}
		all := e.Strings()
		if len(all) > 0 && all[0] != s {
			t.Fatalf("Strings()[0] = %q, String() = %q", all[0], s)
		}
		if len(all) == 0 && s != "" {
			t.Fatalf("Strings() empty but String() = %q", s)
		}
		for _, x := range all {
			if strings.IndexByte(x, 0) >= 0 {
				t.Fatalf("Strings() item %q holds a NUL", x)
			}
		}
	})
}
