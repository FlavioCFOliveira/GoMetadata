package exif

// utf8_type_test.go — task #320: the Exif 3.0/3.1 UTF-8 field type is 129
// (Exif 3.1 §4.6.2, p. 28); code 13 is the TIFF IFD type (TIFF Tech Note 1).
// Rule IDs S-18, S-21, S-44, S-45 (docs/conformance/exif-tiff.md).

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestUTF8Type_S21_Code129IsUTF8: an IFD0 ImageDescription of type 129, count
// 7 ("héllo\0": the count is the bytes including the NUL, Exif 3.1 §4.6.2,
// p. 28) parses as that string and is written back with the same type
// and bytes, in both byte orders and in BigTIFF.
func TestUTF8Type_S21_Code129IsUTF8(t *testing.T) {
	t.Parallel()
	payload := []byte("héllo\x00") // 7 bytes: é is two
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run("S-21/classic/"+order.String(), func(t *testing.T) {
			t.Parallel()
			data := tiffOOLEntry(order, TagImageDescription, TypeUTF8, uint32(len(payload)), payload) //nolint:gosec // G115: tiny
			e, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			ent := e.IFD0.Get(TagImageDescription)
			if ent == nil || ent.Type != TypeUTF8 || ent.Count != 7 || !bytes.Equal(ent.Value, payload) {
				t.Fatalf("entry = type %d count %d value %q, want type 129, count 7, value %q", ent.Type, ent.Count, ent.Value, payload)
			}
			if got := ent.String(); got != "héllo" {
				t.Errorf("String() = %q, want %q", got, "héllo")
			}
			// S-44: the type byte and the value survive Encode.
			out, err := Encode(e)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			e2, err := Parse(out)
			if err != nil {
				t.Fatal(err)
			}
			ent2 := e2.IFD0.Get(TagImageDescription)
			if ent2 == nil || ent2.Type != TypeUTF8 || ent2.Count != 7 || !bytes.Equal(ent2.Value, payload) {
				t.Errorf("S-44: re-encoded entry does not have type 129 and the same value: %v", ent2)
			}
			if got := order.Uint16(out[10+2:]); got != 129 {
				t.Errorf("S-44: type field in the stream = %d, want 129", got)
			}
		})
	}
	t.Run("S-21/BigTIFF", func(t *testing.T) {
		t.Parallel()
		e := &EXIF{ByteOrder: binary.LittleEndian, BigTIFF: true, IFD0: &IFD{Entries: []IFDEntry{
			{Tag: TagImageDescription, Type: TypeUTF8, Count: 9, Value: []byte("héllo wo\x00")},
		}}}
		out, err := Encode(e)
		if err != nil {
			t.Fatal(err)
		}
		e2, err := Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		if ent := e2.IFD0.Get(TagImageDescription); ent == nil || ent.Type != TypeUTF8 || ent.String() != "héllo wo" {
			t.Errorf("BigTIFF entry = %+v, want UTF-8 type 129", ent)
		}
	})
}

// TestUTF8Type_S21_Code13IsIFD: code 13 is the 4-byte IFD type on every tag,
// not a string: a count-1 entry holds a 4-byte offset, and String() is empty.
func TestUTF8Type_S21_Code13IsIFD(t *testing.T) {
	t.Parallel()
	if typeSize(TypeIFD) != 4 || typeSizeBigTIFF(TypeIFD) != 4 {
		t.Fatalf("TypeIFD sizes = %d, %d, want 4, 4", typeSize(TypeIFD), typeSizeBigTIFF(TypeIFD))
	}
	if typeSize(TypeUTF8) != 1 || typeSizeBigTIFF(TypeUTF8) != 1 {
		t.Fatalf("TypeUTF8 sizes = %d, %d, want 1, 1", typeSize(TypeUTF8), typeSizeBigTIFF(TypeUTF8))
	}
	if TypeUTF8 != 129 || TypeIFD != 13 {
		t.Fatalf("TypeUTF8 = %d, TypeIFD = %d, want 129, 13", TypeUTF8, TypeIFD)
	}
	data := tiffWithOneEntry(binary.LittleEndian, TagImageDescription, TypeIFD, 1, 0x61626364, nil)
	e, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	ent := e.IFD0.Get(TagImageDescription)
	if ent == nil || ent.Type != TypeIFD || len(ent.Value) != 4 {
		t.Fatalf("entry = %+v, want type 13 with 4 value bytes", ent)
	}
	if got := ent.String(); got != "" {
		t.Errorf("String() of an IFD-typed entry = %q, want empty", got)
	}
}

// TestUTF8Type_S45_SettersChooseTheType: a setter of an "ASCII or UTF-8" tag
// writes ASCII for 7-bit text, UTF-8 (129) otherwise, and with UTF-8 raises an
// ExifVersion below "0300" (Exif 3.1 §4.6.5.4.1, p. 42; §4.2, p. 14).
func TestUTF8Type_S45_SettersChooseTheType(t *testing.T) {
	t.Parallel()
	t.Run("S-45/7-bit text stays ASCII", func(t *testing.T) {
		t.Parallel()
		e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}}
		e.SetCaption("plain")
		if ent := e.IFD0.Get(TagImageDescription); ent == nil || ent.Type != TypeASCII {
			t.Errorf("entry = %+v, want ASCII", ent)
		}
		if e.ExifIFD != nil {
			t.Error("a 7-bit caption created an Exif IFD")
		}
	})
	t.Run("S-45/non-ASCII text is UTF-8 and sets ExifVersion", func(t *testing.T) {
		t.Parallel()
		e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}}
		e.SetCaption("Ångström")
		ent := e.IFD0.Get(TagImageDescription)
		if ent == nil || ent.Type != TypeUTF8 || ent.String() != "Ångström" || int(ent.Count) != len("Ångström")+1 {
			t.Fatalf("entry = %+v, want UTF-8 with the count including the NUL", ent)
		}
		if v := e.ExifIFD.Get(TagExifVersion); v == nil || string(v.Value) != "0300" {
			t.Errorf("ExifVersion = %v, want 0300", v)
		}
	})
	t.Run("S-45/an older ExifVersion is raised, a newer one kept", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ have, want string }{{"0230", "0300"}, {"0232", "0300"}, {"0300", "0300"}, {"0400", "0400"}, {"xyz!", "0300"}} {
			e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}, ExifIFD: &IFD{Entries: []IFDEntry{
				{Tag: TagExifVersion, Type: TypeUndefined, Count: 4, Value: []byte(tc.have)},
			}}}
			e.SetCopyright("© Åsa")
			if v := e.ExifIFD.Get(TagExifVersion); v == nil || string(v.Value) != tc.want {
				t.Errorf("ExifVersion %q -> %v, want %q", tc.have, v, tc.want)
			}
		}
	})
	t.Run("S-45/ASCII text leaves an older ExifVersion alone", func(t *testing.T) {
		t.Parallel()
		e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}, ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: TagExifVersion, Type: TypeUndefined, Count: 4, Value: []byte("0230")},
		}}}
		e.SetMake("Canon")
		e.SetLensModel("EF 50mm")
		if v := e.ExifIFD.Get(TagExifVersion); v == nil || string(v.Value) != "0230" {
			t.Errorf("ExifVersion = %v, want the untouched 0230", v)
		}
	})
	t.Run("S-45/every text setter", func(t *testing.T) {
		t.Parallel()
		e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}}
		e.SetMake("Fabricação")
		e.SetCameraModel("Câmara")
		e.SetCreator("João")
		e.SetLensModel("Objetiva ç")
		for _, c := range []struct {
			ifd *IFD
			tag TagID
		}{{e.IFD0, TagMake}, {e.IFD0, TagModel}, {e.IFD0, TagArtist}, {e.ExifIFD, TagLensModel}} {
			if ent := c.ifd.Get(c.tag); ent == nil || ent.Type != TypeUTF8 {
				t.Errorf("tag %#04x = %+v, want UTF-8", c.tag, ent)
			}
		}
		out, err := Encode(e)
		if err != nil {
			t.Fatal(err)
		}
		e2, err := Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		if got := e2.CameraModel(); got != "Câmara" {
			t.Errorf("CameraModel after the round trip = %q", got)
		}
	})
}
