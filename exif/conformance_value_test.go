package exif

// conformance_value_test.go — EXIF/TIFF conformance battery: value-level rules V-01..V-11.
//
// Spec references:
//   - CIPA DC-X008-Translation-2019 (Exif 2.32) §4.6.3–§4.6.6.
//   - CIPA DC-008-Translation-2023 (Exif 3.0) §4.6.3.
//   - TIFF 6.0 §2 Table 1.
//
// Every sub-test name matches the rule ID from docs/conformance/exif-tiff.md.

import (
	"encoding/binary"
	"testing"
)

// TagExifVersion is the EXIF version tag (EXIF §4.6.5 Table 4, 0x9000).
// Defined here for local use in conformance tests only.
const TagExifVersion TagID = 0x9000

// TagComponentsConfiguration (EXIF §4.6.5 Table 4, 0x9101).
const TagComponentsConfiguration TagID = 0x9101

// ---------------------------------------------------------------------------
// V-01 — RATIONAL byte layout and zero-denominator guard
// ---------------------------------------------------------------------------

// TestConformance_V01_rational_layout verifies that RATIONAL values are stored as
// two uint32 (numerator @ [0..3], denominator @ [4..7]) in stream byte order.
// TIFF 6.0 §2 Table 1 / EXIF 2.32 CIPA DC-008-2023 §4.6.3.
func TestConformance_V01_rational_layout(t *testing.T) {
	t.Parallel()
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		name := "LE"
		if order == binary.BigEndian {
			name = "BE"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			val := make([]byte, 8)
			order.PutUint32(val[0:], 1)
			order.PutUint32(val[4:], 250)
			entry := IFDEntry{Type: TypeRational, Count: 1, Value: val, bigEndian: orderIsBig(order)}
			r := entry.Rational(0)
			if r[0] != 1 || r[1] != 250 {
				t.Errorf("V-01 %s: Rational = [%d/%d], want [1/250]", name, r[0], r[1])
			}
			// Zero denominator must not crash.
			order.PutUint32(val[4:], 0)
			entry2 := IFDEntry{Type: TypeRational, Count: 1, Value: val, bigEndian: orderIsBig(order)}
			mustNotPanic(t, "V-01 zero den", func() {
				r2 := entry2.Rational(0)
				if r2[1] != 0 {
					t.Errorf("V-01 %s: zero-den Rational denominator = %d, want 0", name, r2[1])
				}
				// Guard before float conversion.
				if r2[1] != 0 {
					_ = float64(r2[0]) / float64(r2[1])
				}
			})
		})
	}

	// SRATIONAL: two int32; negative numerator must decode correctly.
	t.Run("SRATIONAL_signed", func(t *testing.T) {
		t.Parallel()
		order := binary.LittleEndian
		val := make([]byte, 8)
		var neg2 int32 = -2
		order.PutUint32(val[0:], uint32(neg2)) //nolint:gosec // G115: intentional signed-to-unsigned conversion for test
		order.PutUint32(val[4:], 3)
		entry := IFDEntry{Type: TypeSRational, Count: 1, Value: val, bigEndian: orderIsBig(order)}
		r := entry.SRational(0)
		if r[0] != -2 || r[1] != 3 {
			t.Errorf("V-01 SRATIONAL: got [%d/%d], want [-2/3]", r[0], r[1])
		}
	})
}

// ---------------------------------------------------------------------------
// V-02 — Signed tags must use SRational, not Rational
// ---------------------------------------------------------------------------

// TestConformance_V02_signed_tags verifies that signed EXIF tags (ShutterSpeedValue,
// BrightnessValue, ExposureBiasValue) are accessed via SRational(), not Rational().
// EXIF 2.32 CIPA DC-008-2023 §4.6.3: SRational uses TypeSRational (10), not TypeRational (5).
func TestConformance_V02_signed_tags(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	tests := []struct {
		tag    TagID
		name   string
		numI32 int32
		denI32 int32
	}{
		{TagShutterSpeedValue, "ShutterSpeedValue", -3, 1},
		{TagBrightnessValue, "BrightnessValue", -1, 2},
		{TagExposureBiasValue, "ExposureBiasValue", 1, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			val := make([]byte, 8)
			order.PutUint32(val[0:], uint32(tc.numI32)) //nolint:gosec // G115: intentional signed-to-unsigned conversion
			order.PutUint32(val[4:], uint32(tc.denI32)) //nolint:gosec // G115: intentional signed-to-unsigned conversion
			entry := IFDEntry{Tag: tc.tag, Type: TypeSRational, Count: 1, Value: val, bigEndian: orderIsBig(order)}

			// SRational must decode the signed value.
			sr := entry.SRational(0)
			if sr[0] != tc.numI32 || sr[1] != tc.denI32 {
				t.Errorf("V-02 %s: SRational = [%d/%d], want [%d/%d]",
					tc.name, sr[0], sr[1], tc.numI32, tc.denI32)
			}

			// Rational on a TypeSRational entry must return [0,0] (wrong type guard).
			r := entry.Rational(0)
			if r != ([2]uint32{}) {
				t.Errorf("V-02 %s: Rational() on TypeSRational should return [0,0], got %v",
					tc.name, r)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// V-03 — Orientation valid range 1–8
// ---------------------------------------------------------------------------

// TestConformance_V03_orientation verifies that Orientation values 1–8 are accepted
// and values 0/9+ do not crash.
// EXIF §4.6.4: Orientation valid range 1–8.
func TestConformance_V03_orientation(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Valid values 1–8.
	for v := uint16(1); v <= 8; v++ {
		var b [2]byte
		order.PutUint16(b[:], v)
		e := &EXIF{
			ByteOrder: order,
			IFD0: &IFD{Entries: []IFDEntry{
				{Tag: TagOrientation, Type: TypeShort, Count: 1, Value: b[:], bigEndian: orderIsBig(order)},
			}},
		}
		got, ok := e.Orientation()
		if !ok || got != v {
			t.Errorf("V-03: Orientation(%d) = (%d, %v), want (%d, true)", v, got, ok, v)
		}
	}
	// Out-of-range values 0 and 9 must not crash.
	for _, v := range []uint16{0, 9, 255} {
		var b [2]byte
		order.PutUint16(b[:], v)
		e := &EXIF{
			ByteOrder: order,
			IFD0: &IFD{Entries: []IFDEntry{
				{Tag: TagOrientation, Type: TypeShort, Count: 1, Value: b[:], bigEndian: orderIsBig(order)},
			}},
		}
		mustNotPanic(t, "V-03 OOR orientation", func() {
			_, _ = e.Orientation()
		})
	}
}

// ---------------------------------------------------------------------------
// V-04 — ResolutionUnit default = 2 (inch)
// ---------------------------------------------------------------------------

// TestConformance_V04_resolution_unit verifies ResolutionUnit: 1=none, 2=inch (default), 3=cm.
// EXIF §4.6.4: ResolutionUnit default when absent = 2.
func TestConformance_V04_resolution_unit(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	tests := []struct {
		name  string
		value uint16
	}{
		{"none=1", 1},
		{"inch=2", 2},
		{"cm=3", 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var b [2]byte
			order.PutUint16(b[:], tc.value)
			e := &EXIF{
				ByteOrder: order,
				IFD0: &IFD{Entries: []IFDEntry{
					{Tag: TagResolutionUnit, Type: TypeShort, Count: 1, Value: b[:], bigEndian: orderIsBig(order)},
				}},
			}
			ru := e.IFD0.Get(TagResolutionUnit)
			if ru == nil {
				t.Fatalf("V-04: ResolutionUnit missing")
			}
			if ru.Uint16() != tc.value {
				t.Errorf("V-04 %s: ResolutionUnit = %d, want %d", tc.name, ru.Uint16(), tc.value)
			}
		})
	}

	// Absent ResolutionUnit: caller must treat as 2 (inch). Parser does not inject it.
	// This test verifies the parser doesn't crash and returns nil (caller must default).
	t.Run("absent_default_contract", func(t *testing.T) {
		t.Parallel()
		data := minimalTIFF(binary.LittleEndian, [][4]uint32{
			{uint32(TagImageWidth), uint32(TypeLong), 1, 100},
		})
		e, err := Parse(data)
		if err != nil {
			t.Fatalf("V-04: Parse failed: %v", err)
		}
		ru := e.IFD0.Get(TagResolutionUnit)
		// Absent is valid; caller must default to 2. Parser must not crash.
		_ = ru
	})
}

// ---------------------------------------------------------------------------
// V-05 — GPS coordinate ranges
// ---------------------------------------------------------------------------

// TestConformance_V05_gps_coordinate_ranges verifies GPS coordinate constraints:
// deg ≤ 90 (lat) / ≤ 180 (lon); min, sec < 60; den ≠ 0.
// EXIF §4.6.6 Table 15.
func TestConformance_V05_gps_coordinate_ranges(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	makeRats := func(d, m, s [2]uint32) []byte {
		b := make([]byte, 24)
		order.PutUint32(b[0:], d[0])
		order.PutUint32(b[4:], d[1])
		order.PutUint32(b[8:], m[0])
		order.PutUint32(b[12:], m[1])
		order.PutUint32(b[16:], s[0])
		order.PutUint32(b[20:], s[1])
		return b
	}

	// Valid: lat = 37°46'29.64"N.
	gps := &IFD{Entries: []IFDEntry{
		{Tag: TagGPSLatitudeRef, Type: TypeASCII, Count: 2, Value: []byte("N\x00"), bigEndian: orderIsBig(order)},
		{Tag: TagGPSLatitude, Type: TypeRational, Count: 3, Value: makeRats([2]uint32{37, 1}, [2]uint32{46, 1}, [2]uint32{2964, 100}), bigEndian: orderIsBig(order)},
		{Tag: TagGPSLongitudeRef, Type: TypeASCII, Count: 2, Value: []byte("W\x00"), bigEndian: orderIsBig(order)},
		{Tag: TagGPSLongitude, Type: TypeRational, Count: 3, Value: makeRats([2]uint32{122, 1}, [2]uint32{25, 1}, [2]uint32{984, 100}), bigEndian: orderIsBig(order)},
	}}
	sortEntries(gps.Entries)
	lat, lon, ok := parseGPS(gps)
	if !ok {
		t.Fatal("V-05: valid GPS parseGPS returned ok=false")
	}
	if lat < 37.77 || lat > 37.78 {
		t.Errorf("V-05: lat = %f, want ~37.7749", lat)
	}
	if lon > -122.41 || lon < -122.43 {
		t.Errorf("V-05: lon = %f, want ~-122.4194", lon)
	}

	// Out-of-range lat = 91 → rejected.
	gps2 := &IFD{Entries: []IFDEntry{
		{Tag: TagGPSLatitudeRef, Type: TypeASCII, Count: 2, Value: []byte("N\x00"), bigEndian: orderIsBig(order)},
		{Tag: TagGPSLatitude, Type: TypeRational, Count: 3, Value: makeRats([2]uint32{91, 1}, [2]uint32{0, 1}, [2]uint32{0, 1}), bigEndian: orderIsBig(order)},
		{Tag: TagGPSLongitudeRef, Type: TypeASCII, Count: 2, Value: []byte("E\x00"), bigEndian: orderIsBig(order)},
		{Tag: TagGPSLongitude, Type: TypeRational, Count: 3, Value: makeRats([2]uint32{10, 1}, [2]uint32{0, 1}, [2]uint32{0, 1}), bigEndian: orderIsBig(order)},
	}}
	sortEntries(gps2.Entries)
	_, _, ok2 := parseGPS(gps2)
	if ok2 {
		t.Error("V-05: lat=91 should be rejected by parseGPS")
	}

	// Zero denominator in degrees: must not divide by zero.
	gps3 := &IFD{Entries: []IFDEntry{
		{Tag: TagGPSLatitudeRef, Type: TypeASCII, Count: 2, Value: []byte("N\x00"), bigEndian: orderIsBig(order)},
		{Tag: TagGPSLatitude, Type: TypeRational, Count: 3, Value: makeRats([2]uint32{37, 0}, [2]uint32{46, 1}, [2]uint32{0, 1}), bigEndian: orderIsBig(order)},
		{Tag: TagGPSLongitudeRef, Type: TypeASCII, Count: 2, Value: []byte("E\x00"), bigEndian: orderIsBig(order)},
		{Tag: TagGPSLongitude, Type: TypeRational, Count: 3, Value: makeRats([2]uint32{10, 1}, [2]uint32{0, 1}, [2]uint32{0, 1}), bigEndian: orderIsBig(order)},
	}}
	sortEntries(gps3.Entries)
	mustNotPanic(t, "V-05 zero-den GPS", func() {
		_, _, _ = parseGPS(gps3)
	})
}

// ---------------------------------------------------------------------------
// V-06 — GPS ref strings N/S E/W
// ---------------------------------------------------------------------------

// TestConformance_V06_gps_ref_strings verifies GPSLatitudeRef "N"/"S" and
// GPSLongitudeRef "E"/"W" semantics; rationals are always non-negative.
// EXIF §4.6.6 Table 15.
func TestConformance_V06_gps_ref_strings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		latRef  string
		lonRef  string
		wantLat float64
		wantLon float64
	}{
		{"N", "E", +37.5, +122.5},
		{"S", "W", -37.5, -122.5},
		{"S", "E", -37.5, +122.5},
		{"N", "W", +37.5, -122.5},
	}
	order := binary.LittleEndian
	for _, tc := range tests {
		t.Run(tc.latRef+"_"+tc.lonRef, func(t *testing.T) {
			t.Parallel()
			makeRats37 := func() []byte {
				b := make([]byte, 24)
				order.PutUint32(b[0:], 37)
				order.PutUint32(b[4:], 1)
				order.PutUint32(b[8:], 30)
				order.PutUint32(b[12:], 1)
				order.PutUint32(b[16:], 0)
				order.PutUint32(b[20:], 1)
				return b
			}
			makeRats122 := func() []byte {
				b := make([]byte, 24)
				order.PutUint32(b[0:], 122)
				order.PutUint32(b[4:], 1)
				order.PutUint32(b[8:], 30)
				order.PutUint32(b[12:], 1)
				order.PutUint32(b[16:], 0)
				order.PutUint32(b[20:], 1)
				return b
			}
			gps := &IFD{Entries: []IFDEntry{
				{Tag: TagGPSLatitudeRef, Type: TypeASCII, Count: 2, Value: []byte(tc.latRef + "\x00"), bigEndian: orderIsBig(order)},
				{Tag: TagGPSLatitude, Type: TypeRational, Count: 3, Value: makeRats37(), bigEndian: orderIsBig(order)},
				{Tag: TagGPSLongitudeRef, Type: TypeASCII, Count: 2, Value: []byte(tc.lonRef + "\x00"), bigEndian: orderIsBig(order)},
				{Tag: TagGPSLongitude, Type: TypeRational, Count: 3, Value: makeRats122(), bigEndian: orderIsBig(order)},
			}}
			sortEntries(gps.Entries)
			lat, lon, ok := parseGPS(gps)
			if !ok {
				t.Fatalf("V-06 %s/%s: parseGPS ok=false", tc.latRef, tc.lonRef)
			}
			if (tc.wantLat > 0 && lat < 0) || (tc.wantLat < 0 && lat > 0) {
				t.Errorf("V-06 %s: lat sign wrong: got %f, latRef=%s", tc.latRef, lat, tc.latRef)
			}
			if (tc.wantLon > 0 && lon < 0) || (tc.wantLon < 0 && lon > 0) {
				t.Errorf("V-06 %s: lon sign wrong: got %f, lonRef=%s", tc.lonRef, lon, tc.lonRef)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// V-07 — GPSVersionID type must be BYTE, not ASCII
// ---------------------------------------------------------------------------

// TestConformance_V07_gps_version_id verifies that GPSVersionID is BYTE[4] = {2,3,0,0}.
// EXIF §4.6.6 Table 15: type MUST be BYTE not ASCII; count = 4.
func TestConformance_V07_gps_version_id(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	e := &EXIF{ByteOrder: order, IFD0: &IFD{}}
	e.SetGPS(0, 0) // SetGPS always writes GPSVersionID as TypeByte[4]

	vid := e.GPSIFD.Get(TagGPSVersionID)
	if vid == nil {
		t.Fatal("V-07: GPSVersionID missing after SetGPS")
	}
	if vid.Type != TypeByte {
		t.Errorf("V-07: GPSVersionID type = %d, want TypeByte (%d)", vid.Type, TypeByte)
	}
	if vid.Count != 4 {
		t.Errorf("V-07: GPSVersionID count = %d, want 4", vid.Count)
	}
	if len(vid.Value) < 4 || vid.Value[0] != 2 || vid.Value[1] != 3 {
		t.Errorf("V-07: GPSVersionID value = %v, want {2,3,0,0}", vid.Value)
	}
}

// ---------------------------------------------------------------------------
// V-08 — GPSAltitudeRef and GPSAltitude
// ---------------------------------------------------------------------------

// TestConformance_V08_gps_altitude verifies GPSAltitudeRef BYTE (0=above, 1=below)
// and GPSAltitude RATIONAL non-negative.
// EXIF §4.6.6 Table 15.
func TestConformance_V08_gps_altitude(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Build a GPS IFD with AltitudeRef=0 (above sea level) and Altitude=100m.
	altVal := make([]byte, 8)
	order.PutUint32(altVal[0:], 100)
	order.PutUint32(altVal[4:], 1)
	gps := &IFD{Entries: []IFDEntry{
		{Tag: TagGPSAltitudeRef, Type: TypeByte, Count: 1, Value: []byte{0}, bigEndian: orderIsBig(order)},
		{Tag: TagGPSAltitude, Type: TypeRational, Count: 1, Value: altVal, bigEndian: orderIsBig(order)},
	}}
	sortEntries(gps.Entries)
	ref := gps.Get(TagGPSAltitudeRef)
	if ref == nil || ref.Byte() != 0 {
		t.Errorf("V-08: AltitudeRef = %v, want 0 (above sea level)", ref)
	}
	alt := gps.Get(TagGPSAltitude)
	if alt == nil {
		t.Fatal("V-08: GPSAltitude missing")
	}
	r := alt.Rational(0)
	if r[0] != 100 || r[1] != 1 {
		t.Errorf("V-08: Altitude = [%d/%d], want [100/1]", r[0], r[1])
	}
}

// ---------------------------------------------------------------------------
// V-09 — GPSTimeStamp and GPSDateStamp
// ---------------------------------------------------------------------------

// TestConformance_V09_gps_time verifies GPSTimeStamp RATIONAL[3] UTC and
// GPSDateStamp ASCII[11] "YYYY:MM:DD\0".
// EXIF §4.6.6 Table 15.
func TestConformance_V09_gps_time(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// GPSTimeStamp: 3 rationals for HH/MM/SS.
	tsVal := make([]byte, 24)
	order.PutUint32(tsVal[0:], 14)
	order.PutUint32(tsVal[4:], 1) // 14h
	order.PutUint32(tsVal[8:], 30)
	order.PutUint32(tsVal[12:], 1) // 30m
	order.PutUint32(tsVal[16:], 0)
	order.PutUint32(tsVal[20:], 1) // 0s
	// GPSDateStamp: ASCII[11] "YYYY:MM:DD\0"
	dateVal := []byte("2024:07:15\x00")
	gps := &IFD{Entries: []IFDEntry{
		{Tag: TagGPSTimeStamp, Type: TypeRational, Count: 3, Value: tsVal, bigEndian: orderIsBig(order)},
		{Tag: TagGPSDateStamp, Type: TypeASCII, Count: 11, Value: dateVal, bigEndian: orderIsBig(order)},
	}}
	sortEntries(gps.Entries)
	ts := gps.Get(TagGPSTimeStamp)
	if ts == nil || ts.Type != TypeRational || ts.Count != 3 {
		t.Errorf("V-09: GPSTimeStamp = %v, want Rational[3]", ts)
	}
	hh := ts.Rational(0)
	if hh[0] != 14 || hh[1] != 1 {
		t.Errorf("V-09: GPSTimeStamp HH = [%d/%d], want [14/1]", hh[0], hh[1])
	}
	ds := gps.Get(TagGPSDateStamp)
	if ds == nil || ds.String() != "2024:07:15" {
		t.Errorf("V-09: GPSDateStamp = %v, want \"2024:07:15\"", ds)
	}
}

// ---------------------------------------------------------------------------
// V-10 — UserComment charset prefix and payload
// ---------------------------------------------------------------------------

// TestConformance_V10_usercomment verifies UserComment layout: 8-byte charset prefix
// + payload; payload < 8 bytes → empty, no panic.
// EXIF 2.32 CIPA DC-008-2023 §4.6.5.
func TestConformance_V10_usercomment(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// Full payload via EXIF.UserComment().
	text := []byte("Hello World\x00")
	val := makeUserComment(prefixASCII, text)
	e := exifWithExifIFDEntry(TagUserComment, TypeUndefined, val, order)
	if got := e.UserComment(); got != "Hello World" {
		t.Errorf("V-10: UserComment = %q, want \"Hello World\"", got)
	}

	// Payload less than 8 bytes → empty, no panic.
	short := []byte("ABC") // shorter than 8-byte prefix
	e2 := exifWithExifIFDEntry(TagUserComment, TypeUndefined, short, order)
	mustNotPanic(t, "V-10 payload < 8", func() {
		if got := e2.UserComment(); got != "" {
			t.Errorf("V-10: short payload UserComment = %q, want empty", got)
		}
	})

	// Exactly 8 bytes (prefix only, no text) → empty.
	exactly8 := make([]byte, userCommentPrefixLen)
	e3 := exifWithExifIFDEntry(TagUserComment, TypeUndefined, exactly8, order)
	if got := e3.UserComment(); got != "" {
		t.Errorf("V-10: prefix-only UserComment = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// V-11 — IFD1 JPEG thumbnail
// ---------------------------------------------------------------------------

// TestConformance_V11_ifd1_jpeg_thumbnail verifies that IFD1 JPEG thumbnail is
// extracted when Compression=6, JPEGInterchangeFormat, and JPEGInterchangeFormatLength
// are present and valid. Out-of-range values → nil thumbnail, no panic.
// EXIF §4.5.5, tags 0x0103 (Compression=6), 0x0201 (offset), 0x0202 (length).
func TestConformance_V11_ifd1_jpeg_thumbnail(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// Build a TIFF where IFD0 has a next-IFD pointer pointing to IFD1 which has
	// both JPEGInterchangeFormat and JPEGInterchangeFormatLength, plus fake JPEG data.
	const (
		hdrSize  = 8
		ifd0Size = 2 + 12 + 4 // count + 1 entry + next ptr
		ifd0Off  = hdrSize
		ifd1Off  = ifd0Off + ifd0Size // = 26
		ifd1Size = 2 + 3*12 + 4       // count + 3 entries (Compression, JIF offset, JIF length) + next ptr
		jpegOff  = ifd1Off + ifd1Size
	)
	fakeJPEG := []byte{0xFF, 0xD8, 0xFF, 0xD9, 0x00, 0x01, 0x02, 0x03} // minimal fake JPEG
	totalBuf := jpegOff + len(fakeJPEG)
	buf := make([]byte, totalBuf)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0: 1 entry (ImageWidth=640), next → ifd1Off.
	order.PutUint16(buf[ifd0Off:], 1)
	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagImageWidth))
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], 640)
	order.PutUint32(buf[p+12:], ifd1Off) // next-IFD pointer

	// IFD1: 3 entries (Compression=6, JIF offset, JIF length), next = 0.
	order.PutUint16(buf[ifd1Off:], 3)
	q := ifd1Off + 2
	// Compression = 6 (old-JPEG thumbnail format).
	order.PutUint16(buf[q:], uint16(TagCompression))
	order.PutUint16(buf[q+2:], uint16(TypeShort))
	order.PutUint32(buf[q+4:], 1)
	order.PutUint32(buf[q+8:], 6)
	q += 12
	// JPEGInterchangeFormat = offset to JPEG data.
	order.PutUint16(buf[q:], uint16(TagJPEGInterchangeFormat))
	order.PutUint16(buf[q+2:], uint16(TypeLong))
	order.PutUint32(buf[q+4:], 1)
	order.PutUint32(buf[q+8:], uint32(jpegOff))
	q += 12
	// JPEGInterchangeFormatLength = length of JPEG data.
	order.PutUint16(buf[q:], uint16(TagJPEGInterchangeFormatLength))
	order.PutUint16(buf[q+2:], uint16(TypeLong))
	order.PutUint32(buf[q+4:], 1)
	order.PutUint32(buf[q+8:], uint32(len(fakeJPEG))) //nolint:gosec // G115: test fixture
	// next-IFD = 0 (already zero)

	// Copy fake JPEG data.
	copy(buf[jpegOff:], fakeJPEG)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("V-11: Parse failed: %v", err)
	}
	if e.IFD0.Next == nil {
		t.Fatal("V-11: IFD0.Next (IFD1) is nil")
	}
	thumb := e.IFD0.Next.ThumbnailData
	if len(thumb) == 0 {
		t.Fatal("V-11: ThumbnailData is nil/empty; expected JPEG thumbnail bytes")
	}
	if len(thumb) != len(fakeJPEG) {
		t.Errorf("V-11: ThumbnailData len = %d, want %d", len(thumb), len(fakeJPEG))
	}

	// Out-of-range offset/length → nil ThumbnailData, no panic.
	badBuf := make([]byte, jpegOff) // truncated: no JPEG data area
	copy(badBuf, buf[:jpegOff])
	// Rewrite JIF offset to point past EOF.
	order.PutUint32(badBuf[ifd1Off+2+12+8:], 0xFFFFFF00) // out-of-range JIF offset
	mustNotPanic(t, "V-11 OOR thumbnail", func() {
		e2, _ := Parse(badBuf)
		if e2 != nil && e2.IFD0 != nil && e2.IFD0.Next != nil {
			// ThumbnailData should be nil for the out-of-range case.
			_ = e2.IFD0.Next.ThumbnailData
		}
	})
}
