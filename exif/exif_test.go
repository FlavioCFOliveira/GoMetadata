package exif

import (
	"encoding/binary"
	"testing"
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
