package exif

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"
)

// minimalTIFF constructs a minimal TIFF byte stream for testing.
// order is binary.LittleEndian or binary.BigEndian.
// entries is a list of (tag, type, count, value4) tuples where value4 is the
// inline 4-byte value or offset.
func minimalTIFF(order binary.ByteOrder, entries [][4]uint32) []byte {
	// Header: 8 bytes + IFD: 2+len*12+4
	ifdOff := uint32(8)
	n := len(entries)
	buf := make([]byte, 8+2+n*12+4)
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifdOff)
	order.PutUint16(buf[8:], uint16(n)) //nolint:gosec // G115: test helper, intentional type cast
	for i, e := range entries {
		p := 10 + i*12
		order.PutUint16(buf[p:], uint16(e[0]))   //nolint:gosec // G115: test helper, intentional type cast
		order.PutUint16(buf[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper, intentional type cast
		order.PutUint32(buf[p+4:], e[2])         // count
		order.PutUint32(buf[p+8:], e[3])         // value/offset
	}
	return buf
}

func TestParseMinimalLE(t *testing.T) {
	t.Parallel()
	data := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 640},
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.ByteOrder != binary.LittleEndian {
		t.Error("expected little-endian")
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}
	entry := e.IFD0.Get(TagImageWidth)
	if entry == nil {
		t.Fatal("TagImageWidth not found")
	}
	if entry.Uint32() != 640 {
		t.Errorf("got %d, want 640", entry.Uint32())
	}
}

func TestParseMinimalBE(t *testing.T) {
	t.Parallel()
	data := minimalTIFF(binary.BigEndian, [][4]uint32{
		{uint32(TagImageLength), uint32(TypeLong), 1, 480},
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.ByteOrder != binary.BigEndian {
		t.Error("expected big-endian")
	}
}

func TestParseInvalidByteOrder(t *testing.T) {
	t.Parallel()
	data := []byte{0x00, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x00, 0x08}
	_, err := Parse(data)
	if err == nil {
		t.Error("expected error for invalid byte order marker")
	}
}

func TestParseTooShort(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte{0x49, 0x49})
	if err == nil {
		t.Error("expected error for too-short input")
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	t.Parallel()
	// Build an EXIF with a couple of IFD0 entries.
	orig := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 1920},
		{uint32(TagImageLength), uint32(TypeLong), 1, 1080},
	})
	e, err := Parse(orig)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}

	for _, tag := range []TagID{TagImageWidth, TagImageLength} {
		orig := e.IFD0.Get(tag)
		got := e2.IFD0.Get(tag)
		if orig == nil || got == nil {
			t.Fatalf("tag %x missing after round-trip", tag)
		}
		if orig.Uint32() != got.Uint32() {
			t.Errorf("tag %x: got %d, want %d", tag, got.Uint32(), orig.Uint32())
		}
	}
}

func TestEncodeNilReturnsError(t *testing.T) {
	t.Parallel()
	_, err := Encode(nil)
	if err == nil {
		t.Error("expected error for nil EXIF")
	}
}

func TestEncodeWithExifIFD(t *testing.T) {
	t.Parallel()
	// Parse a minimal TIFF, add an ExifIFD pointer manually.
	data := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 100},
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Add a fake ExifIFD with one entry.
	e.ExifIFD = &IFD{
		Entries: []IFDEntry{
			{Tag: 0x9000, Type: TypeASCII, Count: 4, Value: []byte("0232"), byteOrder: binary.LittleEndian},
		},
	}

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse round-trip: %v", err)
	}
	if e2.ExifIFD == nil {
		t.Fatal("ExifIFD not preserved in round-trip")
	}
	if e2.ExifIFD.Get(0x9000) == nil {
		t.Error("ExifIFD entry 0x9000 not preserved")
	}
}

func TestGPSRoundTrip(t *testing.T) {
	t.Parallel()
	// Build a minimal TIFF with a GPS IFD.
	// GPS data: lat = 37.7749 N, lon = 122.4194 W
	data := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 100},
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Add GPS IFD entries manually (DMS: 37°46'29.64"N, 122°25'9.84"W).
	// Each rational = [numerator(4)][denominator(4)].
	order := binary.LittleEndian
	makeRationals := func(dms [3][2]uint32) []byte {
		b := make([]byte, 24)
		for i, r := range dms {
			order.PutUint32(b[i*8:], r[0])
			order.PutUint32(b[i*8+4:], r[1])
		}
		return b
	}
	latDMS := [3][2]uint32{{37, 1}, {46, 1}, {2964, 100}}
	lonDMS := [3][2]uint32{{122, 1}, {25, 1}, {984, 100}}

	e.GPSIFD = &IFD{
		Entries: []IFDEntry{
			{Tag: TagGPSLatitudeRef, Type: TypeASCII, Count: 2, Value: []byte("N\x00"), byteOrder: order},
			{Tag: TagGPSLatitude, Type: TypeRational, Count: 3, Value: makeRationals(latDMS), byteOrder: order},
			{Tag: TagGPSLongitudeRef, Type: TypeASCII, Count: 2, Value: []byte("W\x00"), byteOrder: order},
			{Tag: TagGPSLongitude, Type: TypeRational, Count: 3, Value: makeRationals(lonDMS), byteOrder: order},
		},
	}

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse round-trip: %v", err)
	}
	if e2.GPSIFD == nil {
		t.Fatal("GPSIFD not preserved")
	}
	lat, lon, ok := e2.GPS()
	if !ok {
		t.Fatal("GPS() returned ok=false")
	}
	// lat ≈ 37.7749, lon ≈ -122.4194
	if lat < 37.77 || lat > 37.78 {
		t.Errorf("lat = %f, want ~37.7749", lat)
	}
	if lon > -122.41 || lon < -122.43 {
		t.Errorf("lon = %f, want ~-122.4194", lon)
	}
}

func TestGPSRangeValidation(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	makeRationals := func(dms [3][2]uint32) []byte {
		b := make([]byte, 24)
		for i, r := range dms {
			order.PutUint32(b[i*8:], r[0])
			order.PutUint32(b[i*8+4:], r[1])
		}
		return b
	}

	// lat = 91 degrees N → should be rejected
	data := minimalTIFF(binary.LittleEndian, nil)
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e.GPSIFD = &IFD{
		Entries: []IFDEntry{
			{Tag: TagGPSLatitudeRef, Type: TypeASCII, Count: 2, Value: []byte("N\x00"), byteOrder: order},
			{Tag: TagGPSLatitude, Type: TypeRational, Count: 3, Value: makeRationals([3][2]uint32{{91, 1}, {0, 1}, {0, 1}}), byteOrder: order},
			{Tag: TagGPSLongitudeRef, Type: TypeASCII, Count: 2, Value: []byte("E\x00"), byteOrder: order},
			{Tag: TagGPSLongitude, Type: TypeRational, Count: 3, Value: makeRationals([3][2]uint32{{10, 1}, {0, 1}, {0, 1}}), byteOrder: order},
		},
	}
	_, _, ok := parseGPS(e.GPSIFD)
	if ok {
		t.Error("expected GPS to be rejected for lat=91")
	}
}

func TestIFD1ChainRoundTrip(t *testing.T) {
	t.Parallel()
	// Build EXIF with IFD0 → IFD1 chain.
	data := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 1920},
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Attach an IFD1 (thumbnail IFD).
	e.IFD0.Next = &IFD{
		Entries: []IFDEntry{
			{Tag: TagImageWidth, Type: TypeLong, Count: 1, Value: []byte{0x80, 0x00, 0x00, 0x00}, byteOrder: binary.LittleEndian},
		},
	}

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse round-trip: %v", err)
	}
	if e2.IFD0.Next == nil {
		t.Fatal("IFD1 chain not preserved after round-trip")
	}
	if e2.IFD0.Next.Get(TagImageWidth) == nil {
		t.Error("IFD1 TagImageWidth not found after round-trip")
	}
}

// ---------------------------------------------------------------------------
// Tests for public API convenience methods
// ---------------------------------------------------------------------------

func TestCameraModel(t *testing.T) {
	t.Parallel()
	e := &EXIF{
		IFD0: &IFD{Entries: []IFDEntry{
			{Tag: TagModel, Type: TypeASCII, Count: 6, Value: []byte("Canon\x00"), byteOrder: binary.LittleEndian},
		}},
	}
	if got := e.CameraModel(); got != "Canon" {
		t.Errorf("CameraModel() = %q, want \"Canon\"", got)
	}
	// Nil receiver must return empty string.
	var nilE *EXIF
	if got := nilE.CameraModel(); got != "" {
		t.Errorf("nil.CameraModel() = %q, want \"\"", got)
	}
}

func TestCopyright(t *testing.T) {
	t.Parallel()
	e := &EXIF{
		IFD0: &IFD{Entries: []IFDEntry{
			{Tag: TagCopyright, Type: TypeASCII, Count: 16, Value: []byte("2025 ACME Corp\x00"), byteOrder: binary.LittleEndian},
		}},
	}
	if got := e.Copyright(); got != "2025 ACME Corp" {
		t.Errorf("Copyright() = %q, want \"2025 ACME Corp\"", got)
	}
	var nilE *EXIF
	if got := nilE.Copyright(); got != "" {
		t.Errorf("nil.Copyright() = %q, want \"\"", got)
	}
}

func TestCaption(t *testing.T) {
	t.Parallel()
	e := &EXIF{
		IFD0: &IFD{Entries: []IFDEntry{
			{Tag: TagImageDescription, Type: TypeASCII, Count: 12, Value: []byte("Sunset view\x00"), byteOrder: binary.LittleEndian},
		}},
	}
	if got := e.Caption(); got != "Sunset view" {
		t.Errorf("Caption() = %q, want \"Sunset view\"", got)
	}
	var nilE *EXIF
	if got := nilE.Caption(); got != "" {
		t.Errorf("nil.Caption() = %q, want \"\"", got)
	}
}

func TestCreator(t *testing.T) {
	t.Parallel()
	e := &EXIF{
		IFD0: &IFD{Entries: []IFDEntry{
			{Tag: TagArtist, Type: TypeASCII, Count: 12, Value: []byte("Jane Doe\x00\x00\x00\x00"), byteOrder: binary.LittleEndian},
		}},
	}
	if got := e.Creator(); got != "Jane Doe" {
		t.Errorf("Creator() = %q, want \"Jane Doe\"", got)
	}
	var nilE *EXIF
	if got := nilE.Creator(); got != "" {
		t.Errorf("nil.Creator() = %q, want \"\"", got)
	}
}

func TestOrientation(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	e := &EXIF{
		IFD0: &IFD{Entries: []IFDEntry{
			{Tag: TagOrientation, Type: TypeShort, Count: 1, Value: []byte{0x06, 0x00}, byteOrder: order},
		}},
	}
	if got, ok := e.Orientation(); !ok || got != 6 {
		t.Errorf("Orientation() = (%d, %v), want (6, true)", got, ok)
	}
	// Missing tag returns ok=false.
	e2 := &EXIF{IFD0: &IFD{}}
	if _, ok := e2.Orientation(); ok {
		t.Error("Orientation() with missing tag: expected ok=false")
	}
	var nilE *EXIF
	if _, ok := nilE.Orientation(); ok {
		t.Error("nil.Orientation(): expected ok=false")
	}
}

func TestDateTimeOriginal(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	e := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagDateTimeOriginal, Type: TypeASCII, Count: 20, Value: []byte("2024:07:15 14:30:00\x00"), byteOrder: order},
		}},
	}
	ts, ok := e.DateTimeOriginal()
	if !ok {
		t.Fatal("DateTimeOriginal() returned ok=false")
	}
	if ts.Year() != 2024 || ts.Month() != 7 || ts.Day() != 15 {
		t.Errorf("DateTimeOriginal() date = %v, want 2024-07-15", ts)
	}
	if ts.Hour() != 14 || ts.Minute() != 30 {
		t.Errorf("DateTimeOriginal() time = %v, want 14:30", ts)
	}

	// Nil receiver returns ok=false.
	var nilE *EXIF
	if _, ok := nilE.DateTimeOriginal(); ok {
		t.Error("nil.DateTimeOriginal(): expected ok=false")
	}
}

func TestDateTimeOriginalWithTimezone(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	e := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagDateTimeOriginal, Type: TypeASCII, Count: 20, Value: []byte("2024:07:15 14:30:00\x00"), byteOrder: order},
			{Tag: TagOffsetTimeOriginal, Type: TypeASCII, Count: 7, Value: []byte("+02:00\x00"), byteOrder: order},
		}},
	}
	ts, ok := e.DateTimeOriginal()
	if !ok {
		t.Fatal("DateTimeOriginal() with TZ: returned ok=false")
	}
	_, offset := ts.Zone()
	if offset != 2*3600 {
		t.Errorf("timezone offset = %d seconds, want %d", offset, 2*3600)
	}
}

func TestParseExifTZ(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input      string
		wantOffset int // seconds east of UTC
		wantErr    bool
	}{
		{"+02:00", 2 * 3600, false},
		{"-05:00", -5 * 3600, false},
		{"+00:00", 0, false},
		{"+05:30", 5*3600 + 30*60, false},
		{"invalid", 0, true},
		{"", 0, true},
	}
	for _, tc := range tests {
		loc, err := parseExifTZ(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseExifTZ(%q): expected error, got nil", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseExifTZ(%q): unexpected error: %v", tc.input, err)
			continue
		}
		// Extract the UTC offset by creating a time value in the returned location.
		ts := time.Date(2024, 1, 1, 0, 0, 0, 0, loc)
		_, gotOffset := ts.Zone()
		if gotOffset != tc.wantOffset {
			t.Errorf("parseExifTZ(%q) offset = %d s, want %d s", tc.input, gotOffset, tc.wantOffset)
		}
	}
}
func TestExposureTime(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	val := make([]byte, 8)
	order.PutUint32(val[0:], 1)
	order.PutUint32(val[4:], 200)
	e := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagExposureTime, Type: TypeRational, Count: 1, Value: val, byteOrder: order},
		}},
	}
	num, den, ok := e.ExposureTime()
	if !ok || num != 1 || den != 200 {
		t.Errorf("ExposureTime() = (%d, %d, %v), want (1, 200, true)", num, den, ok)
	}
	var nilE *EXIF
	if _, _, ok := nilE.ExposureTime(); ok {
		t.Error("nil.ExposureTime(): expected ok=false")
	}
}

func TestFNumber(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	val := make([]byte, 8)
	order.PutUint32(val[0:], 28)
	order.PutUint32(val[4:], 10)
	e := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagFNumber, Type: TypeRational, Count: 1, Value: val, byteOrder: order},
		}},
	}
	f, ok := e.FNumber()
	if !ok || f < 2.79 || f > 2.81 {
		t.Errorf("FNumber() = (%f, %v), want (~2.8, true)", f, ok)
	}
	var nilE *EXIF
	if _, ok := nilE.FNumber(); ok {
		t.Error("nil.FNumber(): expected ok=false")
	}
}

func TestISO(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	e := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagISOSpeedRatings, Type: TypeShort, Count: 1, Value: []byte{0x64, 0x01}, byteOrder: order}, // 356
		}},
	}
	iso, ok := e.ISO()
	if !ok || iso != 356 {
		t.Errorf("ISO() = (%d, %v), want (356, true)", iso, ok)
	}
	var nilE *EXIF
	if _, ok := nilE.ISO(); ok {
		t.Error("nil.ISO(): expected ok=false")
	}
}

func TestFocalLength(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	val := make([]byte, 8)
	order.PutUint32(val[0:], 50)
	order.PutUint32(val[4:], 1)
	e := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagFocalLength, Type: TypeRational, Count: 1, Value: val, byteOrder: order},
		}},
	}
	fl, ok := e.FocalLength()
	if !ok || fl != 50.0 {
		t.Errorf("FocalLength() = (%f, %v), want (50, true)", fl, ok)
	}
	var nilE *EXIF
	if _, ok := nilE.FocalLength(); ok {
		t.Error("nil.FocalLength(): expected ok=false")
	}
}

func TestLensModel(t *testing.T) {
	t.Parallel()
	e := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagLensModel, Type: TypeASCII, Count: 14, Value: []byte("EF 50mm f/1.8\x00"), byteOrder: binary.LittleEndian},
		}},
	}
	if got := e.LensModel(); got != "EF 50mm f/1.8" {
		t.Errorf("LensModel() = %q, want \"EF 50mm f/1.8\"", got)
	}
	var nilE *EXIF
	if got := nilE.LensModel(); got != "" {
		t.Errorf("nil.LensModel() = %q, want \"\"", got)
	}
}

func TestImageSize(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	e := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagPixelXDimension, Type: TypeLong, Count: 1, Value: []byte{0x80, 0x07, 0x00, 0x00}, byteOrder: order}, // 1920
			{Tag: TagPixelYDimension, Type: TypeLong, Count: 1, Value: []byte{0x38, 0x04, 0x00, 0x00}, byteOrder: order}, // 1080
		}},
	}
	w, h, ok := e.ImageSize()
	if !ok || w != 1920 || h != 1080 {
		t.Errorf("ImageSize() = (%d, %d, %v), want (1920, 1080, true)", w, h, ok)
	}
	// SHORT variant.
	e2 := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagPixelXDimension, Type: TypeShort, Count: 1, Value: []byte{0x80, 0x07}, byteOrder: order}, // 1920
			{Tag: TagPixelYDimension, Type: TypeShort, Count: 1, Value: []byte{0x38, 0x04}, byteOrder: order}, // 1080
		}},
	}
	w2, h2, ok2 := e2.ImageSize()
	if !ok2 || w2 != 1920 || h2 != 1080 {
		t.Errorf("ImageSize() SHORT = (%d, %d, %v), want (1920, 1080, true)", w2, h2, ok2)
	}
	var nilE *EXIF
	if _, _, ok := nilE.ImageSize(); ok {
		t.Error("nil.ImageSize(): expected ok=false")
	}
}

// ---------------------------------------------------------------------------
// Tests for IFDEntry typed accessors
// ---------------------------------------------------------------------------

func TestIFDEntryInt16(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// -100 in little-endian signed short: 0x9C 0xFF
	e := IFDEntry{Type: TypeSShort, Count: 1, Value: []byte{0x9C, 0xFF}, byteOrder: order}
	if got := e.Int16(); got != -100 {
		t.Errorf("Int16() = %d, want -100", got)
	}
	// Wrong type returns 0.
	e2 := IFDEntry{Type: TypeShort, Count: 1, Value: []byte{0x64, 0x00}, byteOrder: order}
	if got := e2.Int16(); got != 0 {
		t.Errorf("Int16() with TypeShort = %d, want 0", got)
	}
}

func TestIFDEntryInt32(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	var neg1M int32 = -1_000_000
	val := make([]byte, 4)
	order.PutUint32(val, uint32(neg1M)) //nolint:gosec // G115: test helper, intentional type cast
	e := IFDEntry{Type: TypeSLong, Count: 1, Value: val, byteOrder: order}
	if got := e.Int32(); got != neg1M {
		t.Errorf("Int32() = %d, want %d", got, neg1M)
	}
	e2 := IFDEntry{Type: TypeLong, Count: 1, Value: val, byteOrder: order}
	if got := e2.Int32(); got != 0 {
		t.Errorf("Int32() with TypeLong = %d, want 0", got)
	}
}

func TestIFDEntryFloat32(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	val := make([]byte, 4)
	order.PutUint32(val, math.Float32bits(3.14))
	e := IFDEntry{Type: TypeFloat, Count: 1, Value: val, byteOrder: order}
	got := e.Float32()
	if got < 3.13 || got > 3.15 {
		t.Errorf("Float32() = %f, want ~3.14", got)
	}
	e2 := IFDEntry{Type: TypeDouble, Count: 1, Value: make([]byte, 8), byteOrder: order}
	if got := e2.Float32(); got != 0 {
		t.Errorf("Float32() with TypeDouble = %f, want 0", got)
	}
}

func TestIFDEntryFloat64(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	val := make([]byte, 8)
	order.PutUint64(val, math.Float64bits(2.718281828))
	e := IFDEntry{Type: TypeDouble, Count: 1, Value: val, byteOrder: order}
	got := e.Float64()
	if got < 2.718 || got > 2.719 {
		t.Errorf("Float64() = %f, want ~2.718", got)
	}
	e2 := IFDEntry{Type: TypeFloat, Count: 1, Value: val[:4], byteOrder: order}
	if got := e2.Float64(); got != 0 {
		t.Errorf("Float64() with TypeFloat = %f, want 0", got)
	}
}

func TestIFDEntryBytes(t *testing.T) {
	t.Parallel()
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	e := IFDEntry{Type: TypeUndefined, Count: 4, Value: payload, byteOrder: binary.LittleEndian}
	if got := e.Bytes(); !bytes.Equal(got, payload) {
		t.Errorf("Bytes() = %v, want %v", got, payload)
	}
}

func TestIFDEntryLen(t *testing.T) {
	t.Parallel()
	e := IFDEntry{Type: TypeASCII, Count: 7, Value: []byte("hello\x00"), byteOrder: binary.LittleEndian}
	if got := e.Len(); got != 7 {
		t.Errorf("Len() = %d, want 7", got)
	}
}

// ---------------------------------------------------------------------------
// IFDEntry.SRational
// ---------------------------------------------------------------------------

func TestIFDEntrySRational(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Encode two SRational values: -1/2 and 3/4.
	val := make([]byte, 16)
	var negOne, posTwo, posThree, posFour int32 = -1, 2, 3, 4
	order.PutUint32(val[0:], uint32(negOne)) //nolint:gosec // G115: intentional signed-to-unsigned reinterpretation for SRational test
	order.PutUint32(val[4:], uint32(posTwo))
	order.PutUint32(val[8:], uint32(posThree))
	order.PutUint32(val[12:], uint32(posFour))

	e := IFDEntry{Type: TypeSRational, Count: 2, Value: val, byteOrder: order}

	r0 := e.SRational(0)
	if r0[0] != -1 || r0[1] != 2 {
		t.Errorf("SRational(0) = %v, want [-1 2]", r0)
	}
	r1 := e.SRational(1)
	if r1[0] != 3 || r1[1] != 4 {
		t.Errorf("SRational(1) = %v, want [3 4]", r1)
	}
}

func TestIFDEntrySRationalOutOfRange(t *testing.T) {
	t.Parallel()
	val := make([]byte, 8) // only 1 SRational
	e := IFDEntry{Type: TypeSRational, Count: 1, Value: val, byteOrder: binary.LittleEndian}
	r := e.SRational(1) // index 1 is out of range
	if r != ([2]int32{}) {
		t.Errorf("SRational out-of-range: got %v, want [0 0]", r)
	}
}

func TestIFDEntrySRationalWrongType(t *testing.T) {
	t.Parallel()
	// Rational (unsigned) entry should not be decodable via SRational.
	val := make([]byte, 8)
	e := IFDEntry{Type: TypeRational, Count: 1, Value: val, byteOrder: binary.LittleEndian}
	r := e.SRational(0)
	if r != ([2]int32{}) {
		t.Errorf("SRational wrong type: got %v, want [0 0]", r)
	}
}

// ---------------------------------------------------------------------------
// IFD cycle detection
// ---------------------------------------------------------------------------

func TestIFDCycleDetection(t *testing.T) {
	t.Parallel()
	// Build a TIFF where IFD0's next-IFD pointer points back to IFD0 (offset 8).
	order := binary.LittleEndian
	buf := make([]byte, 8+2+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)  // IFD0 at offset 8
	order.PutUint16(buf[8:], 0)  // 0 entries
	order.PutUint32(buf[10:], 8) // next IFD = 8 → cycle back to IFD0

	// Must not hang or panic.
	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse with IFD cycle: unexpected error: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil after cycle detection")
	}
}

func TestMakerNotePreservedOnEncode(t *testing.T) {
	t.Parallel()
	// Build a minimal EXIF with a TagMakerNote entry in ExifIFD.
	// After encode→parse, the raw MakerNote bytes must be identical.
	makerNotePayload := []byte("FakeCanonMakerNote\x00\x01\x02\x03")

	// Build ExifIFD bytes: MakerNote as TypeUndefined at offset (since > 4 bytes).
	// We use minimalTIFFWithExifIFD helper but since it doesn't exist, we build
	// the EXIF manually using the parse→encode path.
	order := binary.LittleEndian

	// Build raw EXIF bytes manually with ExifIFD containing TagMakerNote.
	const (
		hdrSize  = 8
		exifOff  = hdrSize + 2 + 12 + 4 // IFD0: count(2) + 1 entry(12) + next(4) = 26 => exifIFD at 26
		mnOffset = exifOff + 2 + 12 + 4 // ExifIFD: count(2) + 1 entry(12) + next(4) => MN value at 44
	)

	buf := make([]byte, mnOffset+len(makerNotePayload))

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], hdrSize) // IFD0 at offset 8.

	// IFD0: 1 entry = ExifIFDPointer.
	order.PutUint16(buf[hdrSize:], 1)
	order.PutUint16(buf[hdrSize+2:], uint16(TagExifIFDPointer))
	order.PutUint16(buf[hdrSize+4:], uint16(TypeLong))
	order.PutUint32(buf[hdrSize+6:], 1)
	order.PutUint32(buf[hdrSize+10:], exifOff)
	order.PutUint32(buf[hdrSize+14:], 0) // next IFD = 0

	// ExifIFD: 1 entry = MakerNote (TypeUndefined, value at mnOffset).
	order.PutUint16(buf[exifOff:], 1)
	order.PutUint16(buf[exifOff+2:], uint16(TagMakerNote))
	order.PutUint16(buf[exifOff+4:], uint16(TypeUndefined))
	order.PutUint32(buf[exifOff+6:], uint32(len(makerNotePayload))) //nolint:gosec // G115: test helper, intentional type cast
	order.PutUint32(buf[exifOff+10:], mnOffset)
	order.PutUint32(buf[exifOff+14:], 0) // next IFD = 0

	// MakerNote payload.
	copy(buf[mnOffset:], makerNotePayload)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.MakerNote == nil {
		t.Fatal("MakerNote not populated after Parse")
	}

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse after encode: %v", err)
	}
	if e2.MakerNote == nil {
		t.Fatal("MakerNote is nil after encode→parse round-trip")
	}
	if !bytes.Equal(e2.MakerNote, makerNotePayload) {
		t.Errorf("MakerNote bytes mismatch after round-trip:\n  got  %x\n  want %x", e2.MakerNote, makerNotePayload)
	}
}

func BenchmarkEXIFParse(b *testing.B) {
	data := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 4000},
		{uint32(TagImageLength), uint32(TypeLong), 1, 3000},
		{uint32(TagOrientation), uint32(TypeShort), 1, 1},
	})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Parse(data)
	}
}

// ---------------------------------------------------------------------------
// P3-D: IFD1 thumbnail chain and SubIFD extraction
// ---------------------------------------------------------------------------

// TestIFD1ThumbnailChain builds a TIFF where IFD0 has a non-zero next-IFD
// pointer pointing to a second IFD (IFD1 / thumbnail IFD) and verifies that
// the parser follows the chain and exposes it via IFD0.Next.
func TestIFD1ThumbnailChain(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// Layout:
	//   offset 0–7:   TIFF header
	//   offset 8:     IFD0  (1 entry + next ptr pointing to IFD1)
	//   offset 26:    IFD1  (1 entry + next ptr = 0)
	//
	// IFD record size = 2 (count) + N*12 (entries) + 4 (next ptr)
	// 1 entry → 2 + 12 + 4 = 18 bytes per IFD.

	const ifd0Off = 8
	const ifd1Off = ifd0Off + 2 + 12 + 4 // = 26

	buf := make([]byte, ifd1Off+2+12+4)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0: 1 entry (ImageWidth=1920), next → ifd1Off.
	order.PutUint16(buf[ifd0Off:], 1)
	order.PutUint16(buf[ifd0Off+2:], uint16(TagImageWidth))
	order.PutUint16(buf[ifd0Off+4:], uint16(TypeLong))
	order.PutUint32(buf[ifd0Off+6:], 1)
	order.PutUint32(buf[ifd0Off+10:], 1920)
	order.PutUint32(buf[ifd0Off+14:], ifd1Off) // non-zero next pointer

	// IFD1: 1 entry (ImageWidth=160 for thumbnail), next = 0.
	order.PutUint16(buf[ifd1Off:], 1)
	order.PutUint16(buf[ifd1Off+2:], uint16(TagImageWidth))
	order.PutUint16(buf[ifd1Off+4:], uint16(TypeLong))
	order.PutUint32(buf[ifd1Off+6:], 1)
	order.PutUint32(buf[ifd1Off+10:], 160)
	order.PutUint32(buf[ifd1Off+14:], 0)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}
	if e.IFD0.Next == nil {
		t.Fatal("IFD0.Next is nil; expected IFD1 chain to be followed")
	}
	entry := e.IFD0.Next.Get(TagImageWidth)
	if entry == nil {
		t.Fatal("IFD1 TagImageWidth not found")
	}
	if entry.Uint32() != 160 {
		t.Errorf("IFD1 ImageWidth = %d, want 160", entry.Uint32())
	}
}

// TestSubIFDExtracted builds an EXIF block where IFD0 contains a
// TagExifIFDPointer entry pointing to an ExifIFD that holds TagColorSpace.
// After parsing, e.ExifIFD must be non-nil and contain the ColorSpace entry.
func TestSubIFDExtracted(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// Layout:
	//   0–7:    TIFF header
	//   8:      IFD0 with 1 entry (ExifIFDPointer → exifIFDOff), next=0
	//   26:     ExifIFD with 1 entry (ColorSpace=1), next=0

	const ifd0Off = 8
	const exifIFDOff = ifd0Off + 2 + 12 + 4 // = 26

	buf := make([]byte, exifIFDOff+2+12+4)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0: 1 entry = ExifIFDPointer, next = 0.
	order.PutUint16(buf[ifd0Off:], 1)
	order.PutUint16(buf[ifd0Off+2:], uint16(TagExifIFDPointer))
	order.PutUint16(buf[ifd0Off+4:], uint16(TypeLong))
	order.PutUint32(buf[ifd0Off+6:], 1)
	order.PutUint32(buf[ifd0Off+10:], exifIFDOff)
	order.PutUint32(buf[ifd0Off+14:], 0)

	// ExifIFD: 1 entry = TagColorSpace (0xA001) = 1 (sRGB), next = 0.
	order.PutUint16(buf[exifIFDOff:], 1)
	order.PutUint16(buf[exifIFDOff+2:], uint16(TagColorSpace))
	order.PutUint16(buf[exifIFDOff+4:], uint16(TypeShort))
	order.PutUint32(buf[exifIFDOff+6:], 1)
	order.PutUint32(buf[exifIFDOff+10:], 1) // sRGB
	order.PutUint32(buf[exifIFDOff+14:], 0)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.ExifIFD == nil {
		t.Fatal("ExifIFD is nil; expected sub-IFD to be extracted via ExifIFDPointer")
	}
	entry := e.ExifIFD.Get(TagColorSpace)
	if entry == nil {
		t.Fatal("TagColorSpace not found in ExifIFD")
	}
	// TagColorSpace is TypeShort; use Uint16() — Uint32() is reserved for TypeLong.
	if entry.Uint16() != 1 {
		t.Errorf("TagColorSpace = %d, want 1 (sRGB)", entry.Uint16())
	}
}

// ---------------------------------------------------------------------------
// Tests for write setters
// ---------------------------------------------------------------------------

// TestSetCameraModel verifies SetCameraModel sets and reads back the Model tag.
func TestSetCameraModel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"simple ASCII", "Nikon Z9"},
		{"with spaces", "Canon EOS R5"},
		{"empty string", ""},
		{"unicode-free long", "SONY ILCE-7RM5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &EXIF{
				ByteOrder: binary.LittleEndian,
				IFD0:      &IFD{},
			}
			e.SetCameraModel(tc.input)
			if got := e.CameraModel(); got != tc.input {
				t.Errorf("CameraModel() = %q, want %q", got, tc.input)
			}
		})
	}
	// Nil-safety: must not panic.
	var nilE *EXIF
	nilE.SetCameraModel("should not panic")

	// Nil IFD0: must not panic.
	e2 := &EXIF{}
	e2.SetCameraModel("should not panic either")
}

// TestSetCaption verifies SetCaption sets and reads back the ImageDescription tag.
func TestSetCaption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"typical caption", "Sunset over the Pacific"},
		{"empty", ""},
		{"single char", "X"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &EXIF{
				ByteOrder: binary.LittleEndian,
				IFD0:      &IFD{},
			}
			e.SetCaption(tc.input)
			if got := e.Caption(); got != tc.input {
				t.Errorf("Caption() = %q, want %q", got, tc.input)
			}
		})
	}
	var nilE *EXIF
	nilE.SetCaption("should not panic")
}

// TestSetCopyright verifies SetCopyright sets and reads back the Copyright tag.
func TestSetCopyright(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"year + owner", "2025 ACME Corp"},
		{"empty", ""},
		{"cc license", "CC-BY-SA 4.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &EXIF{
				ByteOrder: binary.LittleEndian,
				IFD0:      &IFD{},
			}
			e.SetCopyright(tc.input)
			if got := e.Copyright(); got != tc.input {
				t.Errorf("Copyright() = %q, want %q", got, tc.input)
			}
		})
	}
	var nilE *EXIF
	nilE.SetCopyright("should not panic")
}

// TestSetCreator verifies SetCreator sets and reads back the Artist tag.
func TestSetCreator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"full name", "Jane Doe"},
		{"empty", ""},
		{"handle", "@photographer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &EXIF{
				ByteOrder: binary.LittleEndian,
				IFD0:      &IFD{},
			}
			e.SetCreator(tc.input)
			if got := e.Creator(); got != tc.input {
				t.Errorf("Creator() = %q, want %q", got, tc.input)
			}
		})
	}
	var nilE *EXIF
	nilE.SetCreator("should not panic")
}

// TestSetOrientation verifies SetOrientation encodes and reads back the
// Orientation tag for both byte orders.
func TestSetOrientation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		order binary.ByteOrder
		value uint16
	}{
		{"LE normal", binary.LittleEndian, 1},
		{"LE rotated 90 CW", binary.LittleEndian, 6},
		{"LE rotated 180", binary.LittleEndian, 3},
		{"BE normal", binary.BigEndian, 1},
		{"BE rotated 90 CW", binary.BigEndian, 6},
		{"BE mirrored+180", binary.BigEndian, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Seed one entry so ifd0ByteOrder() resolves to tc.order.
			seed := make([]byte, 2)
			tc.order.PutUint16(seed, 0)
			e := &EXIF{
				ByteOrder: tc.order,
				IFD0: &IFD{Entries: []IFDEntry{
					{Tag: TagImageWidth, Type: TypeShort, Count: 1, Value: seed, byteOrder: tc.order},
				}},
			}
			e.SetOrientation(tc.value)
			got, ok := e.Orientation()
			if !ok {
				t.Fatalf("Orientation() ok=false after SetOrientation(%d)", tc.value)
			}
			if got != tc.value {
				t.Errorf("Orientation() = %d, want %d", got, tc.value)
			}
		})
	}
	// Nil-safety.
	var nilE *EXIF
	nilE.SetOrientation(1)

	e2 := &EXIF{}
	e2.SetOrientation(1)
}

// TestSetGPS verifies SetGPS stores coordinates that GPS() can recover within
// 0.0001 degrees of the original input.
func TestSetGPS(t *testing.T) {
	t.Parallel()
	const tolerance = 0.0001

	tests := []struct {
		name string
		lat  float64
		lon  float64
	}{
		{"San Francisco", 37.7749, -122.4194},
		{"Sydney", -33.8688, 151.2093},
		{"zero island", 0.0, 0.0},
		{"North Pole", 89.9999, 0.0},
		{"prime meridian south", -0.5, 0.0},
		{"antimeridian", 35.0, 179.9999},
		{"negative lat lon", -45.0, -90.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &EXIF{
				ByteOrder: binary.LittleEndian,
				IFD0:      &IFD{},
			}
			e.SetGPS(tc.lat, tc.lon)

			if e.GPSIFD == nil {
				t.Fatal("GPSIFD is nil after SetGPS")
			}

			// Verify the GPS pointer placeholder is present in IFD0.
			if e.IFD0.Get(TagGPSIFDPointer) == nil {
				t.Error("TagGPSIFDPointer not present in IFD0 after SetGPS")
			}

			gotLat, gotLon, ok := e.GPS()
			if !ok {
				t.Fatal("GPS() returned ok=false after SetGPS")
			}
			if diff := gotLat - tc.lat; diff > tolerance || diff < -tolerance {
				t.Errorf("lat = %f, want ~%f (diff %f)", gotLat, tc.lat, diff)
			}
			if diff := gotLon - tc.lon; diff > tolerance || diff < -tolerance {
				t.Errorf("lon = %f, want ~%f (diff %f)", gotLon, tc.lon, diff)
			}
		})
	}
	// Nil-safety.
	var nilE *EXIF
	nilE.SetGPS(37.7749, -122.4194)

	e2 := &EXIF{}
	e2.SetGPS(37.7749, -122.4194)
}

// TestEncodeRoundTripFull builds an EXIF with Make, Model, a rational (FNumber),
// and a GPS IFD via SetGPS, then encodes and re-parses, asserting all fields survive.
func TestEncodeRoundTripFull(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// Seed IFD0 with Make, Model, and FNumber in ExifIFD.
	fnumVal := make([]byte, 8)
	order.PutUint32(fnumVal[0:], 28)
	order.PutUint32(fnumVal[4:], 10)

	e := &EXIF{
		ByteOrder: order,
		IFD0: &IFD{Entries: []IFDEntry{
			{Tag: TagMake, Type: TypeASCII, Count: 6, Value: []byte("Nikon\x00"), byteOrder: order},
		}},
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagFNumber, Type: TypeRational, Count: 1, Value: fnumVal, byteOrder: order},
		}},
	}

	// Use the setter to add Model (exercises set() replace path too).
	e.SetCameraModel("Nikon Z9")
	e.SetGPS(48.8566, 2.3522) // Paris

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}

	// Make.
	makeEntry := e2.IFD0.Get(TagMake)
	if makeEntry == nil {
		t.Fatal("Make tag missing after round-trip")
	}
	if got := makeEntry.String(); got != "Nikon" {
		t.Errorf("Make = %q, want \"Nikon\"", got)
	}

	// Model.
	if got := e2.CameraModel(); got != "Nikon Z9" {
		t.Errorf("CameraModel() = %q, want \"Nikon Z9\"", got)
	}

	// FNumber.
	if e2.ExifIFD == nil {
		t.Fatal("ExifIFD nil after round-trip")
	}
	fn, ok := e2.FNumber()
	if !ok {
		t.Fatal("FNumber() ok=false after round-trip")
	}
	if fn < 2.79 || fn > 2.81 {
		t.Errorf("FNumber() = %f, want ~2.8", fn)
	}

	// GPS.
	lat, lon, ok := e2.GPS()
	if !ok {
		t.Fatal("GPS() ok=false after round-trip")
	}
	const tol = 0.0001
	if d := lat - 48.8566; d > tol || d < -tol {
		t.Errorf("GPS lat = %f, want ~48.8566", lat)
	}
	if d := lon - 2.3522; d > tol || d < -tol {
		t.Errorf("GPS lon = %f, want ~2.3522", lon)
	}
}

// cameraEntry is the entry type used by buildCameraEXIF and its helpers.
// inline4 is used when the total value size is ≤ 4 bytes; blob holds
// out-of-line values (nil for inline).
type cameraEntry struct {
	tag     uint16
	typ     uint16
	count   uint32
	inline4 uint32
	blob    []byte
}

// cameraASCIIBlob returns a NUL-terminated byte slice for an ASCII TIFF value.
func cameraASCIIBlob(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

// cameraRational encodes a single RATIONAL value as 8 bytes (LE).
func cameraRational(num, den uint32) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, num)
	binary.LittleEndian.PutUint32(b[4:], den)
	return b
}

// cameraRationals encodes multiple RATIONAL pairs as a contiguous byte slice (LE).
func cameraRationals(pairs ...[2]uint32) []byte {
	b := make([]byte, len(pairs)*8)
	for i, p := range pairs {
		binary.LittleEndian.PutUint32(b[i*8:], p[0])
		binary.LittleEndian.PutUint32(b[i*8+4:], p[1])
	}
	return b
}

// fixASCIICounts sets the count field on any cameraEntry whose blob is non-nil
// and count is still zero (i.e. ASCII entries built without an explicit count).
func fixASCIICounts(entries []cameraEntry) {
	for i := range entries {
		if entries[i].blob != nil && entries[i].count == 0 {
			entries[i].count = uint32(len(entries[i].blob)) //nolint:gosec // G115: test helper, intentional type cast
		}
	}
}

// cameraIFDSize returns the total byte size of a serialised IFD including its
// header, entries, next-IFD pointer, and all out-of-line blob values.
func cameraIFDSize(es []cameraEntry) uint32 {
	sz := uint32(2 + len(es)*12 + 4) //nolint:gosec // G115: test helper, intentional type cast
	for _, e := range es {
		if e.blob != nil {
			sz += uint32(len(e.blob)) //nolint:gosec // G115: test helper, intentional type cast
		}
	}
	return sz
}

// encodeIFD appends a serialised IFD (entries + blobs) to buf and returns it.
// startOff is the absolute TIFF offset at which this IFD begins; nextOff is
// written into the next-IFD pointer field.
func encodeIFD(buf []byte, es []cameraEntry, startOff, nextOff uint32) []byte {
	order := binary.LittleEndian
	n := len(es)
	valOff := startOff + uint32(2+n*12+4) //nolint:gosec // G115: test helper, intentional type cast

	var cnt [2]byte
	order.PutUint16(cnt[:], uint16(n)) //nolint:gosec // G115: test helper, intentional type cast
	buf = append(buf, cnt[:]...)

	var entry [12]byte
	curOff := valOff
	var blobs []byte
	for _, e := range es {
		order.PutUint16(entry[:], e.tag)
		order.PutUint16(entry[2:], e.typ)
		order.PutUint32(entry[4:], e.count)
		if e.blob != nil {
			order.PutUint32(entry[8:], curOff)
			blobs = append(blobs, e.blob...)
			curOff += uint32(len(e.blob)) //nolint:gosec // G115: test helper, intentional type cast
		} else {
			order.PutUint32(entry[8:], e.inline4)
		}
		buf = append(buf, entry[:]...)
	}
	var next [4]byte
	order.PutUint32(next[:], nextOff)
	buf = append(buf, next[:]...)
	buf = append(buf, blobs...)
	return buf
}

// buildCameraIFD0Entries returns the IFD0 entries for buildCameraEXIF.
// ExifIFDPointer and GPSIFDPointer inline4 values are left as 0; the caller
// must patch them once sub-IFD offsets are known.
func buildCameraIFD0Entries() []cameraEntry {
	entries := []cameraEntry{
		{uint16(TagImageWidth), uint16(TypeLong), 1, 6000, nil},
		{uint16(TagImageLength), uint16(TypeLong), 1, 4000, nil},
		{uint16(TagBitsPerSample), uint16(TypeShort), 1, 8, nil},
		{uint16(TagCompression), uint16(TypeShort), 1, 6, nil},
		{uint16(TagPhotometricInterp), uint16(TypeShort), 1, 2, nil},
		{uint16(TagOrientation), uint16(TypeShort), 1, 1, nil},
		{uint16(TagXResolution), uint16(TypeRational), 1, 0, cameraRational(72, 1)},
		{uint16(TagYResolution), uint16(TypeRational), 1, 0, cameraRational(72, 1)},
		{uint16(TagResolutionUnit), uint16(TypeShort), 1, 2, nil},
		{0x010f, uint16(TypeASCII), 0, 0, cameraASCIIBlob("Canon")},          // Make
		{0x0110, uint16(TypeASCII), 0, 0, cameraASCIIBlob("Canon EOS R5")},   // Model
		{0x0131, uint16(TypeASCII), 0, 0, cameraASCIIBlob("Firmware 1.8.2")}, // Software
		{0x013b, uint16(TypeASCII), 0, 0, cameraASCIIBlob("Test Author")},    // Artist
		{uint16(TagExifIFDPointer), uint16(TypeLong), 1, 0, nil},             // patched by caller
		{uint16(TagGPSIFDPointer), uint16(TypeLong), 1, 0, nil},              // patched by caller
	}
	fixASCIICounts(entries)
	return entries
}

// buildCameraExifEntries returns the ExifIFD entries for buildCameraEXIF.
func buildCameraExifEntries() []cameraEntry {
	entries := []cameraEntry{
		{0x829a, uint16(TypeRational), 1, 0, cameraRational(1, 200)},              // ExposureTime
		{0x829d, uint16(TypeRational), 1, 0, cameraRational(8, 10)},               // FNumber (f/8)
		{0x8822, uint16(TypeShort), 1, 0, nil},                                    // ExposureProgram=Manual
		{0x8827, uint16(TypeShort), 1, 400, nil},                                  // ISO 400
		{0x9003, uint16(TypeASCII), 0, 0, cameraASCIIBlob("2024:03:15 10:30:00")}, // DateTimeOriginal
		{0x9004, uint16(TypeASCII), 0, 0, cameraASCIIBlob("2024:03:15 10:30:00")}, // DateTimeDigitized
		{0x9201, uint16(TypeSRational), 1, 0, cameraRational(8, 1)},               // ShutterSpeedValue
		{0x9202, uint16(TypeRational), 1, 0, cameraRational(3, 1)},                // ApertureValue
		{0x9204, uint16(TypeSRational), 1, 0, cameraRational(0, 1)},               // ExposureBiasValue
		{0x9205, uint16(TypeRational), 1, 0, cameraRational(4, 1)},                // MaxApertureValue
		{0x9207, uint16(TypeShort), 1, 5, nil},                                    // MeteringMode=Pattern
		{0x9209, uint16(TypeShort), 1, 0, nil},                                    // Flash=no
		{0x920a, uint16(TypeRational), 1, 0, cameraRational(50, 1)},               // FocalLength 50mm
		{0xa001, uint16(TypeShort), 1, 1, nil},                                    // ColorSpace=sRGB
		{0xa002, uint16(TypeLong), 1, 6000, nil},                                  // PixelXDimension
		{0xa003, uint16(TypeLong), 1, 4000, nil},                                  // PixelYDimension
		{0xa20e, uint16(TypeRational), 1, 0, cameraRational(300, 1)},              // FocalPlaneXResolution
		{0xa20f, uint16(TypeRational), 1, 0, cameraRational(300, 1)},              // FocalPlaneYResolution
		{0xa210, uint16(TypeShort), 1, 3, nil},                                    // FocalPlaneResolutionUnit
		{0xa405, uint16(TypeShort), 1, 50, nil},                                   // FocalLengthIn35mmFilm
	}
	fixASCIICounts(entries)
	return entries
}

// buildCameraGPSEntries returns the GPS IFD entries for buildCameraEXIF.
func buildCameraGPSEntries() []cameraEntry {
	entries := []cameraEntry{
		{0x0001, uint16(TypeASCII), 2, 0, []byte("N\x00")},                                                         // GPSLatitudeRef
		{0x0002, uint16(TypeRational), 3, 0, cameraRationals([2]uint32{38, 1}, [2]uint32{43, 1}, [2]uint32{0, 1})}, // GPSLatitude
		{0x0003, uint16(TypeASCII), 2, 0, []byte("W\x00")},                                                         // GPSLongitudeRef
		{0x0004, uint16(TypeRational), 3, 0, cameraRationals([2]uint32{9, 1}, [2]uint32{8, 1}, [2]uint32{0, 1})},   // GPSLongitude
		{0x0005, uint16(TypeByte), 1, 0, nil},                                                                      // GPSAltitudeRef=above sea
		{0x0006, uint16(TypeRational), 1, 0, cameraRational(150, 1)},                                               // GPSAltitude 150m
		{0x0007, uint16(TypeRational), 3, 0, cameraRationals([2]uint32{10, 1}, [2]uint32{30, 1}, [2]uint32{0, 1})}, // GPSTimeStamp
		{0x001d, uint16(TypeASCII), 0, 0, cameraASCIIBlob("2024:03:15")},                                           // GPSDateStamp
	}
	fixASCIICounts(entries)
	return entries
}

// BenchmarkEXIFEncode measures the serialisation cost of a small EXIF struct
// with three IFD0 entries and one ExifIFD pointer.
// buildCameraEXIF constructs a realistic ~2 KB TIFF with IFD0 (15 entries),
// ExifIFD (20 entries), and GPS IFD (8 entries) to benchmark parsing cost on
// a real-world-sized payload.
func buildCameraEXIF() []byte {
	// Layout (all offsets relative to start of TIFF header):
	//   0   – 7:   TIFF header
	//   8   – ?:   IFD0  (15 entries)
	//   ?   – ?:   ExifIFD (20 entries)
	//   ?   – ?:   GPS IFD (8 entries)
	//   ?   – ?:   out-of-line value area

	ifd0Entries := buildCameraIFD0Entries()
	exifEntries := buildCameraExifEntries()
	gpsEntries := buildCameraGPSEntries()

	const headerSize = uint32(8)
	ifd0Size := cameraIFDSize(ifd0Entries)
	exifStart := headerSize + ifd0Size
	exifSize := cameraIFDSize(exifEntries)
	gpsStart := exifStart + exifSize

	// Patch IFD0 sub-IFD pointers.
	for i := range ifd0Entries {
		switch ifd0Entries[i].tag {
		case uint16(TagExifIFDPointer):
			ifd0Entries[i].inline4 = exifStart
		case uint16(TagGPSIFDPointer):
			ifd0Entries[i].inline4 = gpsStart
		}
	}

	var buf [8]byte
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], headerSize)
	out := append([]byte(nil), buf[:]...)
	out = encodeIFD(out, ifd0Entries, headerSize, 0)
	out = encodeIFD(out, exifEntries, exifStart, 0)
	out = encodeIFD(out, gpsEntries, gpsStart, 0)
	return out
}

func BenchmarkEXIFParse_Camera(b *testing.B) {
	data := buildCameraEXIF()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Parse(data)
	}
}

func BenchmarkIFDGet_Large(b *testing.B) {
	ifd := &IFD{Entries: make([]IFDEntry, 100)}
	for i := range ifd.Entries {
		ifd.Entries[i] = IFDEntry{Tag: TagID(i * 2)} // even tags 0..198
	}
	sortEntries(ifd.Entries)
	target := TagID(100) // mid-range tag — exercises log(100) ≈ 7 comparisons
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = ifd.Get(target)
	}
}

func BenchmarkEXIFEncode(b *testing.B) {
	data := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 4000},
		{uint32(TagImageLength), uint32(TypeLong), 1, 3000},
		{uint32(TagOrientation), uint32(TypeShort), 1, 1},
	})
	e, err := Parse(data)
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Encode(e)
	}
}

// newMinimalEXIF builds a minimal EXIF with IFD0 (no ExifIFD) using LE byte order.
func newMinimalEXIF(t *testing.T) *EXIF {
	t.Helper()
	data := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 1},
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("newMinimalEXIF: Parse: %v", err)
	}
	return e
}

// exifRoundTrip encodes e and parses the result, returning the new *EXIF.
func exifRoundTrip(t *testing.T, e *EXIF) *EXIF {
	t.Helper()
	b, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	e2, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}
	return e2
}

// testEXIFSetMake is a helper for TestEXIFSetters/SetMake.
func testEXIFSetMake(t *testing.T) {
	t.Helper()
	e := newMinimalEXIF(t)
	e.SetMake("Nikon")
	e2 := exifRoundTrip(t, e)
	entry := e2.IFD0.Get(TagMake)
	if entry == nil {
		t.Fatal("TagMake missing after round-trip")
	}
	if got := entry.String(); got != "Nikon" {
		t.Errorf("Make = %q, want %q", got, "Nikon")
	}
}

// testEXIFSetDateTimeOriginal is a helper for TestEXIFSetters/SetDateTimeOriginal.
func testEXIFSetDateTimeOriginal(t *testing.T) {
	t.Helper()
	e := newMinimalEXIF(t)
	ts := time.Date(2024, 3, 15, 10, 30, 0, 0, time.UTC)
	e.SetDateTimeOriginal(ts)
	e2 := exifRoundTrip(t, e)
	got, ok := e2.DateTimeOriginal()
	if !ok {
		t.Fatal("DateTimeOriginal missing after round-trip")
	}
	if !got.Equal(ts) {
		t.Errorf("DateTimeOriginal = %v, want %v", got, ts)
	}
}

// testEXIFSetExposureTime is a helper for TestEXIFSetters/SetExposureTime.
func testEXIFSetExposureTime(t *testing.T) {
	t.Helper()
	e := newMinimalEXIF(t)
	e.SetExposureTime(1, 1000) // 1/1000 s
	e2 := exifRoundTrip(t, e)
	num, den, ok := e2.ExposureTime()
	if !ok {
		t.Fatal("ExposureTime missing after round-trip")
	}
	if num != 1 || den != 1000 {
		t.Errorf("ExposureTime = %d/%d, want 1/1000", num, den)
	}
}

// testEXIFSetFNumber is a helper for TestEXIFSetters/SetFNumber.
func testEXIFSetFNumber(t *testing.T) {
	t.Helper()
	e := newMinimalEXIF(t)
	e.SetFNumber(2.8)
	e2 := exifRoundTrip(t, e)
	f, ok := e2.FNumber()
	if !ok {
		t.Fatal("FNumber missing after round-trip")
	}
	// Encoded as rational 280/100; tolerate float rounding.
	if math.Abs(f-2.8) > 0.001 {
		t.Errorf("FNumber = %f, want 2.8", f)
	}
}

// testEXIFSetISO is a helper for TestEXIFSetters/SetISO.
func testEXIFSetISO(t *testing.T) {
	t.Helper()
	e := newMinimalEXIF(t)
	e.SetISO(400)
	e2 := exifRoundTrip(t, e)
	iso, ok := e2.ISO()
	if !ok {
		t.Fatal("ISO missing after round-trip")
	}
	if iso != 400 {
		t.Errorf("ISO = %d, want 400", iso)
	}
}

// testEXIFSetFocalLength is a helper for TestEXIFSetters/SetFocalLength.
func testEXIFSetFocalLength(t *testing.T) {
	t.Helper()
	e := newMinimalEXIF(t)
	e.SetFocalLength(50.0)
	e2 := exifRoundTrip(t, e)
	fl, ok := e2.FocalLength()
	if !ok {
		t.Fatal("FocalLength missing after round-trip")
	}
	if math.Abs(fl-50.0) > 0.001 {
		t.Errorf("FocalLength = %f, want 50.0", fl)
	}
}

// testEXIFSetLensModel is a helper for TestEXIFSetters/SetLensModel.
func testEXIFSetLensModel(t *testing.T) {
	t.Helper()
	e := newMinimalEXIF(t)
	e.SetLensModel("AF-S NIKKOR 50mm f/1.8G")
	e2 := exifRoundTrip(t, e)
	if got := e2.LensModel(); got != "AF-S NIKKOR 50mm f/1.8G" {
		t.Errorf("LensModel = %q, want %q", got, "AF-S NIKKOR 50mm f/1.8G")
	}
}

// testEXIFSetImageSize is a helper for TestEXIFSetters/SetImageSize.
func testEXIFSetImageSize(t *testing.T) {
	t.Helper()
	e := newMinimalEXIF(t)
	e.SetImageSize(3840, 2160)
	e2 := exifRoundTrip(t, e)
	w, h, ok := e2.ImageSize()
	if !ok {
		t.Fatal("ImageSize missing after round-trip")
	}
	if w != 3840 || h != 2160 {
		t.Errorf("ImageSize = %dx%d, want 3840x2160", w, h)
	}
}

// testEXIFNilReceiverNoPanic verifies all setters are nil-safe.
func testEXIFNilReceiverNoPanic(_ *testing.T) {
	var e *EXIF
	e.SetMake("x")
	e.SetDateTimeOriginal(time.Now())
	e.SetExposureTime(1, 100)
	e.SetFNumber(1.4)
	e.SetISO(100)
	e.SetFocalLength(35)
	e.SetLensModel("x")
	e.SetImageSize(100, 100)
}

// testEXIFEnsureExifIFDCreatedOnce verifies that two ExifIFD setters share
// the same ExifIFD instance.
func testEXIFEnsureExifIFDCreatedOnce(t *testing.T) {
	t.Helper()
	e := newMinimalEXIF(t)
	if e.ExifIFD != nil {
		t.Fatal("ExifIFD should be nil before any ExifIFD setter")
	}
	e.SetISO(100)
	first := e.ExifIFD
	e.SetFNumber(1.4)
	if e.ExifIFD != first {
		t.Error("ensureExifIFD created a second ExifIFD instead of reusing the first")
	}
}

// TestEXIFSetters exercises every new setter added to EXIF.
// Each sub-test calls the setter, encodes, re-parses, then asserts the getter
// returns the expected value — proving full round-trip correctness.
func TestEXIFSetters(t *testing.T) {
	t.Parallel()
	t.Run("SetMake", func(t *testing.T) {
		t.Parallel()
		testEXIFSetMake(t)
	})
	t.Run("SetDateTimeOriginal", func(t *testing.T) {
		t.Parallel()
		testEXIFSetDateTimeOriginal(t)
	})
	t.Run("SetExposureTime", func(t *testing.T) {
		t.Parallel()
		testEXIFSetExposureTime(t)
	})
	t.Run("SetFNumber", func(t *testing.T) {
		t.Parallel()
		testEXIFSetFNumber(t)
	})
	t.Run("SetISO", func(t *testing.T) {
		t.Parallel()
		testEXIFSetISO(t)
	})
	t.Run("SetFocalLength", func(t *testing.T) {
		t.Parallel()
		testEXIFSetFocalLength(t)
	})
	t.Run("SetLensModel", func(t *testing.T) {
		t.Parallel()
		testEXIFSetLensModel(t)
	})
	t.Run("SetImageSize", func(t *testing.T) {
		t.Parallel()
		testEXIFSetImageSize(t)
	})
	t.Run("NilReceiverNoPanic", func(t *testing.T) {
		t.Parallel()
		testEXIFNilReceiverNoPanic(t)
	})
	t.Run("EnsureExifIFDCreatedOnce", func(t *testing.T) {
		t.Parallel()
		testEXIFEnsureExifIFDCreatedOnce(t)
	})
}

// TestIFDEntryStringUint16Uint32Rational exercises String, Uint16, Uint32 and
// Rational with both the correct-type path and the wrong-type guard path.
func TestIFDEntryStringUint16Uint32Rational(t *testing.T) {
	t.Parallel()

	t.Run("String_correct_type", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Type: TypeASCII, Value: []byte("Canon\x00"), byteOrder: binary.LittleEndian}
		if got := e.String(); got != "Canon" {
			t.Errorf("String() = %q, want %q", got, "Canon")
		}
	})
	t.Run("String_wrong_type", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Type: TypeShort, Value: []byte{0x01, 0x00}, byteOrder: binary.LittleEndian}
		if got := e.String(); got != "" {
			t.Errorf("String() on TypeShort = %q, want empty", got)
		}
	})
	t.Run("String_empty_value", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Type: TypeASCII, Value: []byte{}, byteOrder: binary.LittleEndian}
		if got := e.String(); got != "" {
			t.Errorf("String() on empty = %q, want empty", got)
		}
	})

	t.Run("Uint16_correct_type", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Type: TypeShort, Value: []byte{0x42, 0x00}, byteOrder: binary.LittleEndian}
		if got := e.Uint16(); got != 0x42 {
			t.Errorf("Uint16() = 0x%04X, want 0x42", got)
		}
	})
	t.Run("Uint16_wrong_type", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Type: TypeLong, Value: []byte{0x01, 0x00, 0x00, 0x00}, byteOrder: binary.LittleEndian}
		if got := e.Uint16(); got != 0 {
			t.Errorf("Uint16() on TypeLong = 0x%04X, want 0", got)
		}
	})

	t.Run("Uint32_correct_type", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Type: TypeLong, Value: []byte{0x10, 0x00, 0x00, 0x80}, byteOrder: binary.LittleEndian}
		if got := e.Uint32(); got != 0x80000010 {
			t.Errorf("Uint32() = 0x%08X, want 0x80000010", got)
		}
	})
	t.Run("Uint32_wrong_type", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Type: TypeShort, Value: []byte{0x01, 0x00}, byteOrder: binary.LittleEndian}
		if got := e.Uint32(); got != 0 {
			t.Errorf("Uint32() on TypeShort = 0x%08X, want 0", got)
		}
	})

	t.Run("Rational_correct_type", func(t *testing.T) {
		t.Parallel()
		// Rational: 8 bytes = num(4) + den(4), LE
		val := make([]byte, 8)
		binary.LittleEndian.PutUint32(val[0:], 1)
		binary.LittleEndian.PutUint32(val[4:], 500)
		e := &IFDEntry{Type: TypeRational, Value: val, byteOrder: binary.LittleEndian}
		r := e.Rational(0)
		if r[0] != 1 || r[1] != 500 {
			t.Errorf("Rational(0) = %v, want [1 500]", r)
		}
	})
	t.Run("Rational_wrong_type", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Type: TypeShort, Value: []byte{0x01, 0x00}, byteOrder: binary.LittleEndian}
		r := e.Rational(0)
		if r != ([2]uint32{}) {
			t.Errorf("Rational(0) on TypeShort = %v, want [0 0]", r)
		}
	})
	t.Run("Rational_out_of_range", func(t *testing.T) {
		t.Parallel()
		val := make([]byte, 8)
		e := &IFDEntry{Type: TypeRational, Value: val, byteOrder: binary.LittleEndian}
		r := e.Rational(5) // index 5 → offset 40, way beyond 8 bytes
		if r != ([2]uint32{}) {
			t.Errorf("Rational(5) OOB = %v, want [0 0]", r)
		}
	})
}

// TestIFDEntryByteAndUint8s exercises the Byte() and Uint8s() methods on
// IFDEntry, which have 0% coverage.
func TestIFDEntryByteAndUint8s(t *testing.T) {
	t.Parallel()
	t.Run("Byte_nonempty", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Value: []byte{0x42, 0x43}, byteOrder: binary.LittleEndian}
		if got := e.Byte(); got != 0x42 {
			t.Errorf("Byte() = 0x%02X, want 0x42", got)
		}
	})
	t.Run("Byte_empty", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Value: []byte{}, byteOrder: binary.LittleEndian}
		if got := e.Byte(); got != 0 {
			t.Errorf("Byte() on empty = 0x%02X, want 0", got)
		}
	})
	t.Run("Uint8s", func(t *testing.T) {
		t.Parallel()
		payload := []byte{1, 2, 3, 4}
		e := &IFDEntry{Value: payload, byteOrder: binary.LittleEndian}
		got := e.Uint8s()
		if len(got) != len(payload) {
			t.Fatalf("Uint8s() len = %d, want %d", len(got), len(payload))
		}
		for i, b := range payload {
			if got[i] != b {
				t.Errorf("Uint8s()[%d] = %d, want %d", i, got[i], b)
			}
		}
	})
}

// buildPlainIFD constructs a plain TIFF IFD byte blob (count(2) + n*12 entries)
// with all values small enough to be stored inline. order determines endianness.
func buildPlainIFD(order binary.ByteOrder, entries [][4]uint32) []byte {
	n := len(entries)
	buf := make([]byte, 2+n*12)
	order.PutUint16(buf[0:], uint16(n)) //nolint:gosec // G115: test helper
	for i, e := range entries {
		p := 2 + i*12
		order.PutUint16(buf[p:], uint16(e[0]))   //nolint:gosec // G115: test helper
		order.PutUint16(buf[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper
		order.PutUint32(buf[p+4:], e[2])
		order.PutUint32(buf[p+8:], e[3])
	}
	return buf
}

// TestParseMakerNoteIFD_Canon tests Canon MakerNote parsing via parseMakerNoteIFD.
// Note: traverse(b, 0, order) returns nil when offset=0 (loop invariant), so
// Canon MakerNotes embedded with IFD at byte 0 return nil without panic.
// This test verifies the function is called and returns without panic.
func TestParseMakerNoteIFD_Canon(t *testing.T) {
	t.Parallel()
	ifd := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0001, uint32(TypeShort), 1, 100},
		{0x001C, uint32(TypeLong), 1, 0x80000010},
		{0x0095, uint32(TypeASCII), 0, 0},
	})
	// parseMakerNoteIFD dispatches to parseCanonMakerNote which calls traverse(b,0,...).
	// traverse with offset=0 returns nil (loop condition: cur != 0). No panic expected.
	_ = parseMakerNoteIFD(ifd, "Canon", binary.LittleEndian)
}

// TestParseMakerNoteIFD_CanonTooShort covers the too-short guard in parseCanonMakerNote.
func TestParseMakerNoteIFD_CanonTooShort(t *testing.T) {
	t.Parallel()
	result := parseMakerNoteIFD([]byte{0x01}, "Canon", binary.LittleEndian)
	if result != nil {
		t.Error("Canon too short: expected nil IFD")
	}
}

// TestParseMakerNoteIFD_NikonType3 tests Nikon Type 3 parsing.
func TestParseMakerNoteIFD_NikonType3(t *testing.T) {
	t.Parallel()
	// Build Nikon Type 3: "Nikon\0" + version(2) + embedded TIFF (header + IFD).
	ifdEntries := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0002, uint32(TypeShort), 1, 400}, // ISO
	})
	tiff := make([]byte, 0, 8+len(ifdEntries))
	tiff = append(tiff, 'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00)
	tiff = append(tiff, ifdEntries...)

	b := make([]byte, 0, 8+len(tiff))
	b = append(b, 'N', 'i', 'k', 'o', 'n', 0x00, 0x02, 0x10)
	b = append(b, tiff...)

	result := parseMakerNoteIFD(b, "NIKON CORPORATION", binary.LittleEndian)
	if result == nil {
		t.Error("Nikon Type3: expected non-nil IFD")
	}
}

// TestParseMakerNoteIFD_NikonType1 tests Nikon Type 1 (big-endian plain IFD) path.
// parseNikonType1 calls traverse(b, 0, order) — offset=0 means the loop won't execute,
// so the result is nil. Test verifies the dispatch happens without panic.
func TestParseMakerNoteIFD_NikonType1(t *testing.T) {
	t.Parallel()
	ifd := buildPlainIFD(binary.BigEndian, [][4]uint32{
		{0x0002, uint32(TypeShort), 1, 400},
	})
	// isNikonType3 returns false (no "Nikon\0" prefix), so parseNikonType1 is called.
	_ = parseMakerNoteIFD(ifd, "Nikon", binary.BigEndian)
}

// TestParseMakerNoteIFD_NikonInvalidByteOrder covers the bad byte order guard in
// parseNikonType3.
func TestParseMakerNoteIFD_NikonBadTIFFOrder(t *testing.T) {
	t.Parallel()
	// Nikon Type3 with bad byte order in embedded TIFF.
	b := make([]byte, 20)
	copy(b[:6], "Nikon\x00")
	b[6], b[7] = 0x02, 0x10
	b[8], b[9] = 'X', 'X' // invalid byte order
	result := parseMakerNoteIFD(b, "NIKON CORPORATION", binary.LittleEndian)
	if result != nil {
		t.Error("Nikon bad byte order: expected nil IFD")
	}
}

// TestParseMakerNoteIFD_Sony tests Sony MakerNote parsing.
// traverse(b, 0, order) returns nil (offset=0 loop invariant); test verifies no panic.
func TestParseMakerNoteIFD_Sony(t *testing.T) {
	t.Parallel()
	ifd := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0102, uint32(TypeShort), 1, 1},
	})
	_ = parseMakerNoteIFD(ifd, "SONY", binary.LittleEndian)
}

// TestParseMakerNoteIFD_SonyTooShort covers the too-short guard.
func TestParseMakerNoteIFD_SonyTooShort(t *testing.T) {
	t.Parallel()
	result := parseMakerNoteIFD([]byte{0x01}, "SONY", binary.LittleEndian)
	if result != nil {
		t.Error("Sony too short: expected nil IFD")
	}
}

// TestParseMakerNoteIFD_Fujifilm tests Fujifilm MakerNote parsing.
func TestParseMakerNoteIFD_Fujifilm(t *testing.T) {
	t.Parallel()
	// Layout: "FUJIFILM"(8) + version(4) + ifdOffset(4=16) + IFD
	ifdEntries := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x1000, uint32(TypeShort), 1, 1},
	})
	b := make([]byte, 16+len(ifdEntries))
	copy(b[0:], "FUJIFILM")
	copy(b[8:], "0100")
	binary.LittleEndian.PutUint32(b[12:], 16) // IFD starts at byte 16
	copy(b[16:], ifdEntries)

	result := parseMakerNoteIFD(b, "FUJIFILM", binary.LittleEndian)
	if result == nil {
		t.Error("Fujifilm: expected non-nil IFD")
	}
}

// TestParseMakerNoteIFD_FujifilmBadMagic covers bad magic rejection.
func TestParseMakerNoteIFD_FujifilmBadMagic(t *testing.T) {
	t.Parallel()
	b := make([]byte, 20)
	copy(b[0:], "BADMAGIC")
	result := parseMakerNoteIFD(b, "FUJIFILM", binary.LittleEndian)
	if result != nil {
		t.Error("Fujifilm bad magic: expected nil")
	}
}

// TestParseMakerNoteIFD_Olympus tests Olympus MakerNote parsing.
func TestParseMakerNoteIFD_Olympus(t *testing.T) {
	t.Parallel()
	ifdEntries := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0100, uint32(TypeShort), 1, 1},
	})
	b := make([]byte, 12+len(ifdEntries))
	copy(b[0:], "OLYMPUS\x00")
	b[8], b[9] = 'I', 'I' // LE
	copy(b[12:], ifdEntries)

	result := parseMakerNoteIFD(b, "OLYMPUS IMAGING CORP.", binary.LittleEndian)
	if result == nil {
		t.Error("Olympus: expected non-nil IFD")
	}
}

// TestParseMakerNoteIFD_OlympusBadByteOrder covers bad byte order in Olympus MakerNote.
func TestParseMakerNoteIFD_OlympusBadByteOrder(t *testing.T) {
	t.Parallel()
	b := make([]byte, 20)
	copy(b[0:], "OLYMPUS\x00")
	b[8], b[9] = 'X', 'X' // invalid
	result := parseMakerNoteIFD(b, "Olympus", binary.LittleEndian)
	if result != nil {
		t.Error("Olympus bad byte order: expected nil")
	}
}

// TestParseMakerNoteIFD_PentaxAOC tests Pentax AOC format.
func TestParseMakerNoteIFD_PentaxAOC(t *testing.T) {
	t.Parallel()
	ifdEntries := buildPlainIFD(binary.BigEndian, [][4]uint32{
		{0x0001, uint32(TypeShort), 1, 1},
	})
	b := make([]byte, 6+len(ifdEntries))
	copy(b[0:], "AOC\x00")
	copy(b[6:], ifdEntries)

	result := parseMakerNoteIFD(b, "PENTAX Corporation", binary.BigEndian)
	if result == nil {
		t.Error("Pentax AOC: expected non-nil IFD")
	}
}

// TestParseMakerNoteIFD_PentaxPENTAX tests Pentax PENTAX format.
func TestParseMakerNoteIFD_PentaxPENTAX(t *testing.T) {
	t.Parallel()
	ifdEntries := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0001, uint32(TypeShort), 1, 1},
	})
	b := make([]byte, 12+len(ifdEntries))
	copy(b[0:], "PENTAX \x00")
	b[8], b[9] = 'I', 'I' // LE
	copy(b[12:], ifdEntries)

	result := parseMakerNoteIFD(b, "Ricoh", binary.LittleEndian)
	if result == nil {
		t.Error("Pentax PENTAX format: expected non-nil IFD")
	}
}

// TestParseMakerNoteIFD_PentaxPENTAXBadByteOrder tests bad byte order in PENTAX format.
func TestParseMakerNoteIFD_PentaxPENTAXBadByteOrder(t *testing.T) {
	t.Parallel()
	b := make([]byte, 20)
	copy(b[0:], "PENTAX \x00")
	b[8], b[9] = 'Z', 'Z' // invalid
	result := parseMakerNoteIFD(b, "Ricoh", binary.LittleEndian)
	if result != nil {
		t.Error("Pentax bad byte order: expected nil")
	}
}

// TestParseMakerNoteIFD_PentaxNeitherFormat covers the nil path when prefix matches neither.
func TestParseMakerNoteIFD_PentaxNeitherFormat(t *testing.T) {
	t.Parallel()
	b := make([]byte, 20)
	copy(b[0:], "BADPREFIX")
	result := parseMakerNoteIFD(b, "PENTAX Corporation", binary.BigEndian)
	if result != nil {
		t.Error("Pentax neither format: expected nil")
	}
}

// TestParseMakerNoteIFD_Panasonic tests Panasonic MakerNote.
func TestParseMakerNoteIFD_Panasonic(t *testing.T) {
	t.Parallel()
	ifdEntries := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0001, uint32(TypeShort), 1, 1},
	})
	b := make([]byte, 12+len(ifdEntries))
	copy(b[0:], "Panasonic\x00\x00\x00")
	copy(b[12:], ifdEntries)

	result := parseMakerNoteIFD(b, "Panasonic", binary.LittleEndian)
	if result == nil {
		t.Error("Panasonic: expected non-nil IFD")
	}
}

// TestParseMakerNoteIFD_PanasonicBadMagic tests bad magic.
func TestParseMakerNoteIFD_PanasonicBadMagic(t *testing.T) {
	t.Parallel()
	b := make([]byte, 20)
	copy(b[0:], "BADMAGICXXX\x00")
	result := parseMakerNoteIFD(b, "Panasonic", binary.LittleEndian)
	if result != nil {
		t.Error("Panasonic bad magic: expected nil")
	}
}

// TestParseMakerNoteIFD_LeicaWithPrefix tests Leica with "LEICA\x00" prefix.
func TestParseMakerNoteIFD_LeicaWithPrefix(t *testing.T) {
	t.Parallel()
	ifdEntries := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0100, uint32(TypeShort), 1, 1},
	})
	b := make([]byte, 8+len(ifdEntries))
	copy(b[0:], "LEICA\x00\x01\x00")
	copy(b[8:], ifdEntries)

	result := parseMakerNoteIFD(b, "LEICA CAMERA AG", binary.LittleEndian)
	if result == nil {
		t.Error("Leica with prefix: expected non-nil IFD")
	}
}

// TestParseMakerNoteIFD_LeicaPlain tests Leica plain IFD (Type 0, no prefix) path.
// traverse(b, 0, order) with offset=0 returns nil (loop invariant); test verifies no panic.
func TestParseMakerNoteIFD_LeicaPlain(t *testing.T) {
	t.Parallel()
	ifd := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0100, uint32(TypeShort), 1, 1},
	})
	_ = parseMakerNoteIFD(ifd, "Leica", binary.LittleEndian)
}

// TestParseMakerNoteIFD_LeicaTooShort covers the too-short guard.
func TestParseMakerNoteIFD_LeicaTooShort(t *testing.T) {
	t.Parallel()
	result := parseMakerNoteIFD([]byte{0x01}, "LEICA", binary.LittleEndian)
	if result != nil {
		t.Error("Leica too short: expected nil")
	}
}

// TestParseMakerNoteIFD_DJI tests DJI MakerNote parsing path.
// traverse(b, 0, order) returns nil (offset=0 loop invariant); test verifies no panic.
func TestParseMakerNoteIFD_DJI(t *testing.T) {
	t.Parallel()
	ifd := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0001, uint32(TypeASCII), 0, 0},
	})
	_ = parseMakerNoteIFD(ifd, "DJI", binary.LittleEndian)
}

// TestParseMakerNoteIFD_DJITooShort covers the too-short guard.
func TestParseMakerNoteIFD_DJITooShort(t *testing.T) {
	t.Parallel()
	result := parseMakerNoteIFD([]byte{0x01}, "DJI", binary.LittleEndian)
	if result != nil {
		t.Error("DJI too short: expected nil")
	}
}

// TestParseMakerNoteIFD_Samsung tests Samsung MakerNote parsing path.
// traverse(b, 0, order) returns nil (offset=0 loop invariant); test verifies no panic.
func TestParseMakerNoteIFD_Samsung(t *testing.T) {
	t.Parallel()
	ifd := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0001, uint32(TypeShort), 1, 1},
	})
	_ = parseMakerNoteIFD(ifd, "SAMSUNG", binary.LittleEndian)
}

// TestParseMakerNoteIFD_SamsungTooShort covers the too-short guard.
func TestParseMakerNoteIFD_SamsungTooShort(t *testing.T) {
	t.Parallel()
	result := parseMakerNoteIFD([]byte{0x01}, "SAMSUNG", binary.LittleEndian)
	if result != nil {
		t.Error("Samsung too short: expected nil")
	}
}

// TestParseMakerNoteIFD_Sigma tests Sigma SIGMA prefix.
func TestParseMakerNoteIFD_Sigma(t *testing.T) {
	t.Parallel()
	ifdEntries := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0001, uint32(TypeShort), 1, 1},
	})
	b := make([]byte, 10+len(ifdEntries))
	copy(b[0:], "SIGMA\x00\x00\x00")
	b[8], b[9] = 0x01, 0x00
	copy(b[10:], ifdEntries)

	result := parseMakerNoteIFD(b, "SIGMA", binary.LittleEndian)
	if result == nil {
		t.Error("Sigma: expected non-nil IFD")
	}
}

// TestParseMakerNoteIFD_SigmaFOVEON tests Sigma FOVEON prefix.
func TestParseMakerNoteIFD_SigmaFOVEON(t *testing.T) {
	t.Parallel()
	ifdEntries := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0001, uint32(TypeShort), 1, 1},
	})
	b := make([]byte, 10+len(ifdEntries))
	copy(b[0:], "FOVEON\x00\x00")
	b[8], b[9] = 0x01, 0x00
	copy(b[10:], ifdEntries)

	result := parseMakerNoteIFD(b, "SIGMA", binary.LittleEndian)
	if result == nil {
		t.Error("Sigma FOVEON: expected non-nil IFD")
	}
}

// TestParseMakerNoteIFD_SigmaBadMagic covers bad magic in Sigma.
func TestParseMakerNoteIFD_SigmaBadMagic(t *testing.T) {
	t.Parallel()
	b := make([]byte, 20)
	copy(b[0:], "UNKNOWN!")
	result := parseMakerNoteIFD(b, "SIGMA", binary.LittleEndian)
	if result != nil {
		t.Error("Sigma bad magic: expected nil")
	}
}

// TestParseMakerNoteIFD_Casio tests Casio MakerNote parsing path.
// traverse(b, 0, order) returns nil (offset=0 loop invariant); test verifies no panic.
func TestParseMakerNoteIFD_Casio(t *testing.T) {
	t.Parallel()
	ifd := buildPlainIFD(binary.LittleEndian, [][4]uint32{
		{0x0001, uint32(TypeShort), 1, 1},
	})
	_ = parseMakerNoteIFD(ifd, "CASIO COMPUTER CO.,LTD.", binary.LittleEndian)
}

// TestParseMakerNoteIFD_CasioTooShort covers the too-short guard in parseCasioMakerNote.
func TestParseMakerNoteIFD_CasioTooShort(t *testing.T) {
	t.Parallel()
	result := parseMakerNoteIFD([]byte{0x01}, "CASIO COMPUTER CO.,LTD.", binary.LittleEndian)
	if result != nil {
		t.Error("Casio too short: expected nil")
	}
}

// TestParseMakerNoteIFD_Unknown covers the unknown make path (nil return).
func TestParseMakerNoteIFD_Unknown(t *testing.T) {
	t.Parallel()
	result := parseMakerNoteIFD([]byte{0x01, 0x00}, "UnknownBrand", binary.LittleEndian)
	if result != nil {
		t.Error("unknown make: expected nil")
	}
}

// TestIsNikonType3 exercises isNikonType3 directly.
func TestIsNikonType3(t *testing.T) {
	t.Parallel()
	t.Run("valid Type3", func(t *testing.T) {
		t.Parallel()
		b := make([]byte, 20)
		copy(b[:6], "Nikon\x00")
		if !isNikonType3(b) {
			t.Error("isNikonType3: expected true")
		}
	})
	t.Run("too short", func(t *testing.T) {
		t.Parallel()
		if isNikonType3([]byte("Nik")) {
			t.Error("isNikonType3 on short slice: expected false")
		}
	})
	t.Run("wrong prefix", func(t *testing.T) {
		t.Parallel()
		b := make([]byte, 20)
		copy(b[:6], "Canon\x00")
		if isNikonType3(b) {
			t.Error("isNikonType3 wrong prefix: expected false")
		}
	})
}

// TestRefByte exercises refByte for nil entry and empty Value.
func TestRefByte(t *testing.T) {
	t.Parallel()
	t.Run("nil entry returns false", func(t *testing.T) {
		t.Parallel()
		_, ok := refByte(nil)
		if ok {
			t.Error("refByte(nil) ok = true, want false")
		}
	})
	t.Run("empty Value returns false", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Value: []byte{}}
		_, ok := refByte(e)
		if ok {
			t.Error("refByte(empty Value) ok = true, want false")
		}
	})
	t.Run("valid byte", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Value: []byte{'N'}}
		b, ok := refByte(e)
		if !ok || b != 'N' {
			t.Errorf("refByte = (%c, %v), want (N, true)", b, ok)
		}
	})
}

// TestDMSToDecimalZeroDenominator covers the zero-denominator guard in dmsToDecimal.
func TestDMSToDecimalZeroDenominator(t *testing.T) {
	t.Parallel()
	// Zero denominator in degrees field — should return 0.
	var dms [3][2]uint32
	dms[0] = [2]uint32{48, 0} // zero denominator
	dms[1] = [2]uint32{0, 1}
	dms[2] = [2]uint32{0, 1}
	if got := dmsToDecimal(dms, 'N'); got != 0 {
		t.Errorf("dmsToDecimal zero denom = %f, want 0", got)
	}
}

// TestDecodeCoordinateEdgeCases tests decodeCoordinate nil and wrong count.
func TestDecodeCoordinateEdgeCases(t *testing.T) {
	t.Parallel()
	t.Run("nil entry", func(t *testing.T) {
		t.Parallel()
		_, ok := decodeCoordinate(nil)
		if ok {
			t.Error("decodeCoordinate(nil) ok = true, want false")
		}
	})
	t.Run("wrong count", func(t *testing.T) {
		t.Parallel()
		e := &IFDEntry{Count: 2}
		_, ok := decodeCoordinate(e)
		if ok {
			t.Error("decodeCoordinate(count!=3) ok = true, want false")
		}
	})
}

// TestEXIFMethodsNilExifIFD verifies that ExifIFD-dependent methods return
// their zero/false values when EXIF.ExifIFD is nil (tag list not populated).
func TestEXIFMethodsNilExifIFD(t *testing.T) {
	t.Parallel()

	e := &EXIF{IFD0: &IFD{}} // ExifIFD is nil

	t.Run("FNumber nil ExifIFD", func(t *testing.T) {
		t.Parallel()
		_, ok := e.FNumber()
		if ok {
			t.Error("FNumber: expected ok=false with nil ExifIFD")
		}
	})

	t.Run("FocalLength nil ExifIFD", func(t *testing.T) {
		t.Parallel()
		_, ok := e.FocalLength()
		if ok {
			t.Error("FocalLength: expected ok=false with nil ExifIFD")
		}
	})

	t.Run("ISO nil ExifIFD", func(t *testing.T) {
		t.Parallel()
		_, ok := e.ISO()
		if ok {
			t.Error("ISO: expected ok=false with nil ExifIFD")
		}
	})

	t.Run("ExposureTime nil ExifIFD", func(t *testing.T) {
		t.Parallel()
		_, _, ok := e.ExposureTime()
		if ok {
			t.Error("ExposureTime: expected ok=false with nil ExifIFD")
		}
	})

	t.Run("LensModel nil ExifIFD", func(t *testing.T) {
		t.Parallel()
		if got := e.LensModel(); got != "" {
			t.Errorf("LensModel: got %q, want empty string", got)
		}
	})

	t.Run("ImageSize nil ExifIFD", func(t *testing.T) {
		t.Parallel()
		_, _, ok := e.ImageSize()
		if ok {
			t.Error("ImageSize: expected ok=false with nil ExifIFD")
		}
	})

	t.Run("DateTimeOriginal nil ExifIFD", func(t *testing.T) {
		t.Parallel()
		_, ok := e.DateTimeOriginal()
		if ok {
			t.Error("DateTimeOriginal: expected ok=false with nil ExifIFD")
		}
	})
}

// TestEXIFMethodsMissingTags verifies methods return zero/false when the IFD
// exists but the specific tag is absent.
func TestEXIFMethodsMissingTags(t *testing.T) {
	t.Parallel()

	// EXIF with non-nil ExifIFD and IFD0 but no relevant tags.
	e := &EXIF{
		IFD0:    &IFD{},
		ExifIFD: &IFD{},
	}

	t.Run("FNumber entry nil", func(t *testing.T) {
		t.Parallel()
		_, ok := e.FNumber()
		if ok {
			t.Error("FNumber: expected ok=false when tag absent")
		}
	})

	t.Run("FocalLength entry nil", func(t *testing.T) {
		t.Parallel()
		_, ok := e.FocalLength()
		if ok {
			t.Error("FocalLength: expected ok=false when tag absent")
		}
	})

	t.Run("LensModel entry nil", func(t *testing.T) {
		t.Parallel()
		if got := e.LensModel(); got != "" {
			t.Errorf("LensModel: got %q, want empty string", got)
		}
	})

	t.Run("CameraModel entry nil", func(t *testing.T) {
		t.Parallel()
		if got := e.CameraModel(); got != "" {
			t.Errorf("CameraModel: got %q, want empty string", got)
		}
	})

	t.Run("Copyright entry nil", func(t *testing.T) {
		t.Parallel()
		if got := e.Copyright(); got != "" {
			t.Errorf("Copyright: got %q, want empty string", got)
		}
	})

	t.Run("Caption entry nil", func(t *testing.T) {
		t.Parallel()
		if got := e.Caption(); got != "" {
			t.Errorf("Caption: got %q, want empty string", got)
		}
	})

	t.Run("Creator entry nil", func(t *testing.T) {
		t.Parallel()
		if got := e.Creator(); got != "" {
			t.Errorf("Creator: got %q, want empty string", got)
		}
	})
}

// TestFNumberZeroDenominator verifies that FNumber returns ok=false when the
// rational value has a zero denominator.
func TestFNumberZeroDenominator(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	val := make([]byte, 8)
	order.PutUint32(val[0:], 28) // numerator
	order.PutUint32(val[4:], 0)  // zero denominator
	e := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagFNumber, Type: TypeRational, Count: 1, Value: val, byteOrder: order},
		}},
	}
	_, ok := e.FNumber()
	if ok {
		t.Error("FNumber with zero denominator: expected ok=false")
	}
}

// TestFocalLengthZeroDenominator verifies that FocalLength returns ok=false
// when the rational value has a zero denominator.
func TestFocalLengthZeroDenominator(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	val := make([]byte, 8)
	order.PutUint32(val[0:], 50) // numerator
	order.PutUint32(val[4:], 0)  // zero denominator
	e := &EXIF{
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagFocalLength, Type: TypeRational, Count: 1, Value: val, byteOrder: order},
		}},
	}
	_, ok := e.FocalLength()
	if ok {
		t.Error("FocalLength with zero denominator: expected ok=false")
	}
}

// TestGPSNilGPSIFD verifies that GPS() returns ok=false when GPSIFD is nil.
func TestGPSNilGPSIFD(t *testing.T) {
	t.Parallel()
	e := &EXIF{IFD0: &IFD{}, GPSIFD: nil}
	_, _, ok := e.GPS()
	if ok {
		t.Error("GPS with nil GPSIFD: expected ok=false")
	}
}

// TestTagName exercises TagName for known and unknown tags.
// TestSkipMakerNoteOption verifies that SkipMakerNote returns a ParseOption
// that correctly sets skipMakerNote=true on the parse config.
func TestSkipMakerNoteOption(t *testing.T) {
	t.Parallel()
	// Build a minimal TIFF with no MakerNote tag.
	tiff := minimalTIFF(binary.LittleEndian, nil)
	// Parse with SkipMakerNote option — must not panic or error.
	_, err := Parse(tiff, SkipMakerNote())
	if err != nil {
		t.Fatalf("Parse with SkipMakerNote: %v", err)
	}
}

func TestTagName(t *testing.T) {
	t.Parallel()
	t.Run("known tag", func(t *testing.T) {
		t.Parallel()
		if got := TagName(TagImageWidth); got != "ImageWidth" {
			t.Errorf("TagName(TagImageWidth) = %q, want %q", got, "ImageWidth")
		}
	})
	t.Run("unknown tag", func(t *testing.T) {
		t.Parallel()
		if got := TagName(0xFFFF); got != "" {
			t.Errorf("TagName(0xFFFF) = %q, want empty", got)
		}
	})
}
