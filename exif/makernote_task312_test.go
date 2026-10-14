package exif

// makernote_task312_test.go — regression tests for task #312: MakerNote
// value offsets are resolved against the offset base of the detected layout
// (the TIFF header for Canon, Sony, Olympus "OLYMP\0", Panasonic, ...), so a
// value stored outside the MakerNote is no longer dropped; plus the
// byte-swapped entry count heuristic, the Fujifilm IFD offset, and
// EXIF.MakerNoteLayout, which the TIFF writers use to rebase moved MakerNotes
// (task #310).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// mnTIFF builds a classic TIFF stream (byte order order) whose IFD0 holds a
// Make string and an Exif IFD pointer, whose Exif IFD holds one MakerNote, and
// which ends with extra bytes placed after the MakerNote. mn receives the
// TIFF offsets of the MakerNote and of the extra bytes, so that it can write
// TIFF-relative pointers, and returns the MakerNote bytes, whose length must
// be mnLen.
func mnTIFF(t *testing.T, order binary.ByteOrder, cameraMake string, mnLen int, extra []byte, mn func(mnOff, extraOff uint32) []byte) []byte {
	t.Helper()
	makeVal := append([]byte(cameraMake), 0)
	const (
		ifd0Off = 8
		ifd0Len = 2 + 2*12 + 4
		exifOff = ifd0Off + ifd0Len
		exifLen = 2 + 1*12 + 4
		makeOff = exifOff + exifLen
	)
	mnOff := makeOff + len(makeVal)
	mnOff += mnOff & 1
	extraOff := mnOff + mnLen
	extraOff += extraOff & 1
	buf := make([]byte, extraOff+len(extra))
	if order == binary.LittleEndian {
		copy(buf, "II")
	} else {
		copy(buf, "MM")
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)
	entry := func(at int, tag uint16, typ DataType, count, val uint32) {
		order.PutUint16(buf[at:], tag)
		order.PutUint16(buf[at+2:], uint16(typ))
		order.PutUint32(buf[at+4:], count)
		order.PutUint32(buf[at+8:], val)
	}
	order.PutUint16(buf[ifd0Off:], 2)
	if len(makeVal) <= 4 {
		t.Fatal("mnTIFF: the Make must be longer than 3 bytes")
	}
	entry(ifd0Off+2, uint16(TagMake), TypeASCII, uint32(len(makeVal)), uint32(makeOff)) //nolint:gosec // G115: test sizes are small
	entry(ifd0Off+14, uint16(TagExifIFDPointer), TypeLong, 1, exifOff)
	order.PutUint16(buf[exifOff:], 1)
	entry(exifOff+2, uint16(TagMakerNote), TypeUndefined, uint32(mnLen), uint32(mnOff)) //nolint:gosec // G115: test sizes are small
	copy(buf[makeOff:], makeVal)
	blob := mn(uint32(mnOff), uint32(extraOff)) //nolint:gosec // G115: test sizes are small
	if len(blob) != mnLen {
		t.Fatalf("mnTIFF: MakerNote is %d bytes, declared %d", len(blob), mnLen)
	}
	copy(buf[mnOff:], blob)
	copy(buf[extraOff:], extra)
	return buf
}

// putEntry writes one 12-byte IFD entry at b[at:]. A single SHORT value is
// stored left-justified in the value field (TIFF 6.0 §2); any other val is
// stored as a 32-bit value or offset.
func putEntry(b []byte, at int, order binary.ByteOrder, tag uint16, typ DataType, count, val uint32) {
	order.PutUint16(b[at:], tag)
	order.PutUint16(b[at+2:], uint16(typ))
	order.PutUint32(b[at+4:], count)
	if typ == TypeShort && count == 1 {
		order.PutUint16(b[at+8:], uint16(val)) //nolint:gosec // G115: test SHORT values are small
		return
	}
	order.PutUint32(b[at+8:], val)
}

// TestParse_MakerNoteValueOutsideMakerNote (task #312): an "OLYMP\0"
// MakerNote whose out-of-line value lies after the MakerNote, at a
// TIFF-relative offset (the Olympus C-720UZ layout: SpecialMode at TIFF
// offset 0x0C84, after a 758-byte MakerNote that starts at 0x026C). Before the
// fix the IFD was parsed on the MakerNote bytes alone and the entry was
// dropped.
func TestParse_MakerNoteValueOutsideMakerNote(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	special := []byte{0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3}
	const mnLen = 8 + 2 + 2*12 + 4
	data := mnTIFF(t, le, "OLYMPUS OPTICAL CO.,LTD", mnLen, special, func(_, extraOff uint32) []byte {
		mn := make([]byte, mnLen)
		copy(mn, "OLYMP\x00\x01\x00")
		le.PutUint16(mn[8:], 2)
		putEntry(mn, 10, le, 0x0200, TypeLong, 3, extraOff) // SpecialMode, outside the MakerNote
		putEntry(mn, 22, le, 0x0201, TypeShort, 1, 2)       // Quality, inline
		return mn
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.MakerNoteIFD == nil {
		t.Fatal("MakerNoteIFD = nil")
	}
	if n := len(e.MakerNoteIFD.Entries); n != 2 {
		t.Fatalf("MakerNote entries = %d, want 2", n)
	}
	got := e.MakerNoteIFD.Get(0x0200)
	if got == nil || !bytes.Equal(got.Value, special) {
		t.Fatalf("SpecialMode = %v, want %x (value stored after the MakerNote)", got, special)
	}
}

// TestParse_MakerNoteCanonTIFFRelative (task #312): a Canon MakerNote is a
// plain IFD whose value offsets count from the TIFF header
// (metadata-extractor processMakernote). The value is inside the MakerNote,
// but its offset is TIFF-relative, so reading it relative to the MakerNote
// (the former behaviour) returned other bytes.
func TestParse_MakerNoteCanonTIFFRelative(t *testing.T) {
	t.Parallel()
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(order.String(), func(t *testing.T) {
			t.Parallel()
			imageType := []byte("Canon EOS 40D\x00\x00\x00")
			const valAt = 2 + 1*12 + 4
			mnLen := valAt + len(imageType) + 8
			data := mnTIFF(t, order, "Canon", mnLen, nil, func(mnOff, _ uint32) []byte {
				mn := make([]byte, mnLen)
				order.PutUint16(mn, 1)
				putEntry(mn, 2, order, 0x0006, TypeASCII, uint32(len(imageType)), mnOff+valAt) //nolint:gosec // G115: test sizes are small
				copy(mn[valAt:], imageType)
				return mn
			})
			e, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := e.MakerNoteIFD.Get(0x0006)
			if got == nil || !bytes.Equal(got.Value, imageType) {
				t.Fatalf("CanonImageType = %v, want %q", got, imageType)
			}
		})
	}
}

// TestParse_MakerNoteByteSwappedCount: a MakerNote whose byte order differs
// from the one the parser assumes reads as a count above 255 with a zero low
// byte; the other byte order is used (metadata-extractor
// TiffReader.processIfd). Corpus cases: a Canon "II" note inside a re-saved
// "MM" TIFF, and a big-endian Panasonic note (layout otherwise little-endian).
func TestParse_MakerNoteByteSwappedCount(t *testing.T) {
	t.Parallel()
	le, be := binary.LittleEndian, binary.BigEndian
	cases := []struct {
		name, cameraMake, prefix string
		tiff, note               binary.ByteOrder
	}{
		{"Canon II note in MM TIFF", "Canon", "", be, le},
		{"Panasonic BE note", "Panasonic", "Panasonic\x00\x00\x00", le, be},
		{"Sigma BE note", "SIGMA", "SIGMA\x00\x00\x00\x01\x00", le, be},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := len(tc.prefix)
			mnLen := p + 2 + 2*12 + 4
			data := mnTIFF(t, tc.tiff, tc.cameraMake, mnLen, nil, func(_, _ uint32) []byte {
				mn := make([]byte, mnLen)
				copy(mn, tc.prefix)
				tc.note.PutUint16(mn[p:], 2)
				putEntry(mn, p+2, tc.note, 0x0001, TypeShort, 1, 7)
				putEntry(mn, p+14, tc.note, 0x0002, TypeShort, 1, 9)
				return mn
			})
			e, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if e.MakerNoteIFD == nil || len(e.MakerNoteIFD.Entries) != 2 {
				t.Fatalf("MakerNoteIFD = %v, want 2 entries", e.MakerNoteIFD)
			}
			if v := e.MakerNoteIFD.Get(0x0002); v == nil || v.Uint16() != 9 {
				t.Errorf("tag 0x0002 = %v, want 9", v)
			}
		})
	}
}

// TestParse_MakerNoteFujifilmIFDOffset (task #312): the 4 bytes after
// "FUJIFILM" are the IFD offset (metadata-extractor processMakernote; corpus
// bytes "FUJIFILM\x0c\x00\x00\x00"), not bytes 12-15.
func TestParse_MakerNoteFujifilmIFDOffset(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	mn := make([]byte, 12+2+12+4)
	copy(mn, "FUJIFILM")
	le.PutUint32(mn[8:], 12)
	le.PutUint16(mn[12:], 1)
	putEntry(mn, 14, le, 0x0000, TypeUndefined, 4, binary.LittleEndian.Uint32([]byte("0130")))
	ifd := parseMakerNoteIFD(mn, "FUJIFILM", le)
	if ifd == nil || len(ifd.Entries) != 1 || string(ifd.Entries[0].Value) != "0130" {
		t.Fatalf("Fujifilm IFD = %v, want the version entry \"0130\"", ifd)
	}
}

// TestMakerNoteLayout (tasks #310, #312): the layout the writers rebase on.
func TestMakerNoteLayout(t *testing.T) {
	t.Parallel()
	le, be := binary.LittleEndian, binary.BigEndian
	plain := func(order binary.ByteOrder, prefix string) []byte {
		p := len(prefix)
		mn := make([]byte, p+2+12+4)
		copy(mn, prefix)
		order.PutUint16(mn[p:], 1)
		putEntry(mn, p+2, order, 0x0001, TypeShort, 1, 1)
		return mn
	}
	nikon3 := append([]byte("Nikon\x00\x02\x10\x00\x00II*\x00\x08\x00\x00\x00"), plain(le, "")...)
	cases := []struct {
		name, cameraMake string
		tiffOrder        binary.ByteOrder
		mn               []byte
		ok, rel          bool
		off              int
		order            binary.ByteOrder
	}{
		{"canon", "Canon", le, plain(le, ""), true, true, 0, le},
		{"canon swapped", "Canon", be, plain(le, ""), true, true, 0, le},
		{"sony", "SONY", le, plain(le, ""), true, true, 0, le},
		{"olymp", "", le, plain(le, "OLYMP\x00\x01\x00"), true, true, 8, le},
		{"panasonic", "", le, plain(le, "Panasonic\x00\x00\x00"), true, true, 12, le},
		{"olympus II", "", be, plain(le, "OLYMPUS\x00II\x03\x00"), true, false, 12, le},
		{"nikon type 3", "NIKON CORPORATION", be, nikon3, true, false, 18, le},
		{"unknown plain IFD", "Acme", be, plain(be, ""), true, true, 0, be},
		{"unknown garbage", "Acme", le, []byte("not an IFD at all"), false, false, 0, nil},
		{"kodak binary", "", le, []byte("KDK INFO and binary payload"), false, false, 0, nil},
		{"no makernote", "Canon", le, nil, false, false, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &EXIF{ByteOrder: tc.tiffOrder, IFD0: &IFD{}, MakerNote: tc.mn}
			if tc.cameraMake != "" {
				e.IFD0.Entries = []IFDEntry{{Tag: TagMake, Type: TypeASCII, Count: uint32(len(tc.cameraMake) + 1), Value: append([]byte(tc.cameraMake), 0)}} //nolint:gosec // G115: test sizes are small
			}
			l, ok := e.MakerNoteLayout()
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (layout %+v)", ok, tc.ok, l)
			}
			if !ok {
				return
			}
			if l.TIFFRelative != tc.rel || l.IFDOffset != tc.off || l.ByteOrder != tc.order {
				t.Errorf("layout = %+v, want TIFFRelative %v IFDOffset %d ByteOrder %v", l, tc.rel, tc.off, tc.order)
			}
		})
	}
	var nilEXIF *EXIF
	if _, ok := nilEXIF.MakerNoteLayout(); ok {
		t.Error("nil EXIF reported a layout")
	}
}

// TestParse_CorpusOlympusC720UZMakerNote (task #312, corpus): the Olympus
// C-720UZ MakerNote holds 62 entries per ExifTool 13.55 ("exiftool -v1":
// "MakerNotes directory with 62 entries"); 22 of their values lie outside the
// MakerNote and were dropped (40 entries) before the fix.
func TestParse_CorpusOlympusC720UZMakerNote(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "testdata", "corpus", "jpeg", "metadata-extractor", "Olympus C720UZ.jpg")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	e, err := corpusEXIF(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.MakerNoteIFD == nil || len(e.MakerNoteIFD.Entries) != 62 {
		t.Fatalf("MakerNote entries = %v, want 62 (ExifTool)", e.MakerNoteIFD)
	}
	// ExifTool: CameraID (0x0209) = "OLYMPUS DIGITAL CAMERA" padded to 32 bytes.
	if id := e.MakerNoteIFD.Get(0x0209); id == nil || !bytes.HasPrefix(id.Value, []byte("OLYMPUS DIGITAL CAMERA")) {
		t.Errorf("CameraID = %v", id)
	}
}

// BenchmarkMakerNoteDispatchTIFFBase measures MakerNote dispatch with the
// TIFF stream as offset base (the Parse path since task #312).
func BenchmarkMakerNoteDispatchTIFFBase(b *testing.B) {
	le := binary.LittleEndian
	const mnOff = 64
	tiff := make([]byte, mnOff+2+4*12+4+64)
	mn := tiff[mnOff:]
	le.PutUint16(mn, 4)
	for i := range 4 {
		putEntry(mn, 2+12*i, le, uint16(i+1), TypeASCII, 16, uint32(mnOff+2+4*12+4+16*i))
	}
	makeValue := []byte("Canon\x00")
	b.ReportAllocs()
	for range b.N {
		if makerNoteDispatchAt(makeValue, le, mn, tiff, mnOff) == nil {
			b.Fatal("nil IFD")
		}
	}
}
