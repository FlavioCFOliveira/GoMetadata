package exif

import (
	"encoding/binary"
	"testing"
	"time"
)

// TestTask308_SetDateTimeOriginalQualifiers proves that SetDateTimeOriginal
// keeps the tags that qualify DateTimeOriginal consistent with t: 0x9011
// OffsetTimeOriginal holds the offset of t ("+00:00" for UTC; CIPA
// DC-008-2023 §4.6.5, ASCII count 7), and a stale 0x9291 SubSecTimeOriginal
// is removed, so that the date reads back as the same instant, in both byte
// orders.
func TestTask308_SetDateTimeOriginalQualifiers(t *testing.T) {
	t.Parallel()
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, ts := range []time.Time{
			time.Date(2026, 3, 14, 15, 9, 26, 0, time.FixedZone("", 5*3600+30*60)),
			time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
			time.Date(2024, 1, 2, 3, 4, 5, 0, time.FixedZone("", -8*3600)),
		} {
			e, err := Parse(minimalTIFF(order, [][4]uint32{{uint32(TagImageWidth), uint32(TypeLong), 1, 1}}))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			// Stale qualifiers from an earlier date.
			e.ensureExifIFD()
			e.ExifIFD.set(TagSubSecTimeOriginal, TypeASCII, 3, []byte("54\x00"), order == binary.BigEndian)
			e.ExifIFD.set(TagOffsetTimeOriginal, TypeASCII, 7, []byte("+09:00\x00"), order == binary.BigEndian)

			e.SetDateTimeOriginal(ts)
			for _, got := range []*EXIF{e, exifRoundTrip(t, e)} {
				off := got.ExifIFD.Get(TagOffsetTimeOriginal)
				if off == nil || off.Count != 7 || off.String() != ts.Format("-07:00") {
					t.Errorf("%v %v: OffsetTimeOriginal = %+v, want %q", order, ts, off, ts.Format("-07:00"))
				}
				if got.ExifIFD.Get(TagSubSecTimeOriginal) != nil {
					t.Errorf("%v %v: SubSecTimeOriginal survived", order, ts)
				}
				if d, ok := got.DateTimeOriginal(); !ok || !d.Equal(ts) {
					t.Errorf("%v %v: DateTimeOriginal() = %v, %v", order, ts, d, ok)
				}
			}
		}
	}
}

// TestTask308_IFDRemove covers IFD.remove: present, absent, first, last.
func TestTask308_IFDRemove(t *testing.T) {
	t.Parallel()
	ifd := &IFD{}
	for _, tag := range []TagID{0x0100, 0x0110, 0x0132, 0x8298} {
		ifd.set(tag, TypeShort, 1, []byte{1, 0}, false)
	}
	ifd.remove(0x0999) // absent
	ifd.remove(0x0110)
	ifd.remove(0x0100) // first
	ifd.remove(0x8298) // last
	if len(ifd.Entries) != 1 || ifd.Entries[0].Tag != 0x0132 {
		t.Errorf("entries after remove = %+v, want only 0x0132", ifd.Entries)
	}
	ifd.remove(0x0132)
	ifd.remove(0x0132)
	if len(ifd.Entries) != 0 {
		t.Errorf("entries = %+v, want none", ifd.Entries)
	}
}
