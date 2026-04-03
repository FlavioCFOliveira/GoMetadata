package exif

import (
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
	order.PutUint16(buf[8:], uint16(n))
	for i, e := range entries {
		p := 10 + i*12
		order.PutUint16(buf[p:], uint16(e[0]))   // tag
		order.PutUint16(buf[p+2:], uint16(e[1])) // type
		order.PutUint32(buf[p+4:], e[2])          // count
		order.PutUint32(buf[p+8:], e[3])          // value/offset
	}
	return buf
}

func TestParseMinimalLE(t *testing.T) {
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
	data := []byte{0x00, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x00, 0x08}
	_, err := Parse(data)
	if err == nil {
		t.Error("expected error for invalid byte order marker")
	}
}

func TestParseTooShort(t *testing.T) {
	_, err := Parse([]byte{0x49, 0x49})
	if err == nil {
		t.Error("expected error for too-short input")
	}
}

func TestEncodeRoundTrip(t *testing.T) {
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
	_, err := Encode(nil)
	if err == nil {
		t.Error("expected error for nil EXIF")
	}
}

func TestEncodeWithExifIFD(t *testing.T) {
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
	order := binary.LittleEndian
	var neg1M int32 = -1_000_000
	val := make([]byte, 4)
	order.PutUint32(val, uint32(neg1M))
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
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	e := IFDEntry{Type: TypeUndefined, Count: 4, Value: payload, byteOrder: binary.LittleEndian}
	if got := e.Bytes(); string(got) != string(payload) {
		t.Errorf("Bytes() = %v, want %v", got, payload)
	}
}

func TestIFDEntryLen(t *testing.T) {
	e := IFDEntry{Type: TypeASCII, Count: 7, Value: []byte("hello\x00"), byteOrder: binary.LittleEndian}
	if got := e.Len(); got != 7 {
		t.Errorf("Len() = %d, want 7", got)
	}
}

// ---------------------------------------------------------------------------
// IFDEntry.SRational
// ---------------------------------------------------------------------------

func TestIFDEntrySRational(t *testing.T) {
	order := binary.LittleEndian
	// Encode two SRational values: -1/2 and 3/4.
	val := make([]byte, 16)
	var negOne, posTwo, posThree, posFour int32 = -1, 2, 3, 4
	order.PutUint32(val[0:], uint32(negOne))
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
	val := make([]byte, 8) // only 1 SRational
	e := IFDEntry{Type: TypeSRational, Count: 1, Value: val, byteOrder: binary.LittleEndian}
	r := e.SRational(1) // index 1 is out of range
	if r != ([2]int32{}) {
		t.Errorf("SRational out-of-range: got %v, want [0 0]", r)
	}
}

func TestIFDEntrySRationalWrongType(t *testing.T) {
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
	// Build a TIFF where IFD0's next-IFD pointer points back to IFD0 (offset 8).
	order := binary.LittleEndian
	buf := make([]byte, 8+2+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at offset 8
	order.PutUint16(buf[8:], 0) // 0 entries
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
		mnOffset = exifOff + 2 + 12 + 4  // ExifIFD: count(2) + 1 entry(12) + next(4) => MN value at 44
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
	order.PutUint32(buf[exifOff+6:], uint32(len(makerNotePayload)))
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
	if string(e2.MakerNote) != string(makerNotePayload) {
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
	for i := 0; i < b.N; i++ {
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

// BenchmarkEXIFEncode measures the serialisation cost of a small EXIF struct
// with three IFD0 entries and one ExifIFD pointer.
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
	for i := 0; i < b.N; i++ {
		_, _ = Encode(e)
	}
}
