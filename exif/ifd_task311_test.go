package exif

// ifd_task311_test.go — task #311: type 13 on an IFD-pointer tag is the
// 4-byte TIFF IFD type (TIFF Tech Note 1; libtiff TIFF_IFD), not TypeUTF8.

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestParse_IFDPointerTypeIFD (task #311): type 13 on an IFD-pointer tag is
// the 4-byte TIFF IFD type (TIFF Tech Note 1), not a 1-byte string. The Exif
// and GPS pointers of corpus "Issue 151.tif" use it, and libtiff writes
// SubIFDs with it. The pointers are followed, and Encode writes a type-13
// SubIFDs array back unchanged.
func TestParse_IFDPointerTypeIFD(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	const (
		ifd0   = 8
		exifAt = ifd0 + 2 + 3*12 + 4 // 50
		gpsAt  = exifAt + 2 + 12 + 4 // 68
		subArr = gpsAt + 2 + 12 + 4  // 86
	)
	buf := make([]byte, subArr+3*4)
	copy(buf, "II*\x00")
	le.PutUint32(buf[4:], ifd0)
	le.PutUint16(buf[ifd0:], 3)
	putEntry(buf, ifd0+2, le, uint16(TagSubIFDs), TypeUTF8, 3, subArr)
	putEntry(buf, ifd0+14, le, uint16(TagExifIFDPointer), TypeUTF8, 1, exifAt)
	putEntry(buf, ifd0+26, le, uint16(TagGPSIFDPointer), TypeUTF8, 1, gpsAt)
	le.PutUint16(buf[exifAt:], 1)
	putEntry(buf, exifAt+2, le, 0x9201, TypeShort, 1, 5) // arbitrary short tag
	le.PutUint16(buf[gpsAt:], 1)
	putEntry(buf, gpsAt+2, le, 0x0000, TypeByte, 4, 0x00000202) // GPSVersionID
	for i, v := range []uint32{0x1000, 0x2000, 0x3000} {
		le.PutUint32(buf[subArr+4*i:], v)
	}

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.ExifIFD == nil || e.GPSIFD == nil {
		t.Fatalf("ExifIFD %v / GPSIFD %v not followed through type-13 pointers", e.ExifIFD, e.GPSIFD)
	}
	sub := e.IFD0.Get(TagSubIFDs)
	if sub == nil || sub.Type != TypeUTF8 || sub.Count != 3 || len(sub.Value) != 12 {
		t.Fatalf("SubIFDs entry = %+v, want type 13, count 3, 12 value bytes", sub)
	}
	out, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(Encode): %v", err)
	}
	got := back.IFD0.Get(TagSubIFDs)
	if got == nil || got.Type != TypeUTF8 || !bytes.Equal(got.Value, sub.Value) {
		t.Fatalf("re-encoded SubIFDs = %+v, want type 13 and value %x", got, sub.Value)
	}
	if back.ExifIFD == nil || back.GPSIFD == nil {
		t.Error("re-encoded stream lost the Exif or GPS IFD")
	}
}
