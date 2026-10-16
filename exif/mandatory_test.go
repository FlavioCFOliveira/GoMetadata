package exif

// mandatory_test.go — task #321: the tags that Exif 3.1 marks mandatory in
// the IFDs this package creates (Tables 17-19, pp. 93-97). Rule IDs S-46 and
// S-47 (docs/conformance/exif-tiff.md).

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestMandatory_S46_NewEXIFForACompressedPrimaryImage: New(true) carries the
// "Compressed" column of Tables 17-18: IFD0 YCbCrPositioning and the Exif IFD
// pointer; ExifVersion, ColorSpace and ComponentsConfiguration in the Exif IFD;
// PixelXDimension and PixelYDimension are pending until the image size is known.
func TestMandatory_S46_NewEXIFForACompressedPrimaryImage(t *testing.T) {
	t.Parallel()
	e := New(true)
	if !e.CompressedPrimary || !e.NeedsImageSize() {
		t.Fatalf("CompressedPrimary = %v, NeedsImageSize = %v, want true, true", e.CompressedPrimary, e.NeedsImageSize())
	}
	if y := e.IFD0.Get(TagYCbCrPositioning); y == nil || y.Type != TypeShort || y.Count != 1 || y.Uint16() != 1 {
		t.Errorf("IFD0 YCbCrPositioning = %+v, want SHORT 1 (centered)", y)
	}
	if e.IFD0.Get(TagExifIFDPointer) == nil || e.ExifIFD == nil {
		t.Error("IFD0 has no Exif IFD pointer, or there is no Exif IFD (Table 17: mandatory)")
	}
	if v := e.ExifIFD.Get(TagExifVersion); v == nil || v.Type != TypeUndefined || v.Count != 4 || string(v.Value) != "0300" {
		t.Errorf("ExifVersion = %+v, want UNDEFINED 4 \"0300\"", v)
	}
	if c := e.ExifIFD.Get(TagColorSpace); c == nil || c.Type != TypeShort || c.Uint16() != 0xFFFF {
		t.Errorf("ColorSpace = %+v, want SHORT 0xFFFF (uncalibrated)", c)
	}
	if c := e.ExifIFD.Get(TagComponentsConfiguration); c == nil || c.Type != TypeUndefined || !bytes.Equal(c.Value, []byte{1, 2, 3, 0}) {
		t.Errorf("ComponentsConfiguration = %+v, want UNDEFINED 1 2 3 0", c)
	}
	if e.ExifIFD.Get(TagPixelXDimension) != nil {
		t.Error("PixelXDimension was invented before the size is known")
	}
	e.SetImageSize(640, 480)
	if e.NeedsImageSize() {
		t.Error("NeedsImageSize still true after SetImageSize")
	}
	if w, h, ok := e.ImageSize(); !ok || w != 640 || h != 480 {
		t.Errorf("ImageSize = %d x %d, %v", w, h, ok)
	}
	// The result encodes and parses back with the same tags.
	out, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if e2.IFD0.Get(TagYCbCrPositioning) == nil || e2.ExifIFD.Get(TagExifVersion) == nil || e2.ExifIFD.Get(TagPixelYDimension) == nil {
		t.Error("a mandatory tag did not survive Encode")
	}
}

// TestMandatory_S46_UncompressedAddsOnlyWhatEveryImageNeeds: for a container
// whose primary image is not JPEG, the tags that Table 17-18 does not allow for
// uncompressed data ("N": YCbCrPositioning, ComponentsConfiguration,
// PixelX/YDimension) are not added; ExifVersion and ColorSpace are.
func TestMandatory_S46_UncompressedAddsOnlyWhatEveryImageNeeds(t *testing.T) {
	t.Parallel()
	e := New(false)
	if e.NeedsImageSize() {
		t.Error("NeedsImageSize for an uncompressed image")
	}
	if e.IFD0.Get(TagYCbCrPositioning) != nil {
		t.Error("YCbCrPositioning added for a primary image that is not compressed")
	}
	if e.ExifIFD.Get(TagComponentsConfiguration) != nil {
		t.Error("ComponentsConfiguration added for a primary image that is not compressed")
	}
	if e.ExifIFD.Get(TagExifVersion) == nil || e.ExifIFD.Get(TagColorSpace) == nil {
		t.Error("ExifVersion or ColorSpace missing (Table 18: mandatory for every column)")
	}
}

// TestMandatory_S46_SettersCreateTheIFDWithItsTags: a setter that has to create
// the Exif IFD of a parsed EXIF that lacks one adds the mandatory tags to it,
// following the container the EXIF was read from.
func TestMandatory_S46_SettersCreateTheIFDWithItsTags(t *testing.T) {
	t.Parallel()
	data := tiffWithOneEntry(binary.LittleEndian, TagImageDescription, TypeASCII, 4, 0x00636261, nil) // "abc\0"
	for _, compressed := range []bool{true, false} {
		e, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		e.CompressedPrimary = compressed
		e.SetISO(200)
		if e.ExifIFD == nil || e.ExifIFD.Get(TagExifVersion) == nil || e.ExifIFD.Get(TagColorSpace) == nil {
			t.Fatalf("compressed=%v: created Exif IFD lacks ExifVersion or ColorSpace", compressed)
		}
		if got := e.ExifIFD.Get(TagComponentsConfiguration) != nil; got != compressed {
			t.Errorf("compressed=%v: ComponentsConfiguration present = %v", compressed, got)
		}
		if e.NeedsImageSize() != compressed {
			t.Errorf("compressed=%v: NeedsImageSize = %v", compressed, e.NeedsImageSize())
		}
	}
}

// TestMandatory_S47_ExistingIFDsAreNeverCompleted: a parsed Exif IFD that
// lacks mandatory tags stays as it is when a setter adds a tag to it: the only
// difference is the tag that was set.
func TestMandatory_S47_ExistingIFDsAreNeverCompleted(t *testing.T) {
	t.Parallel()
	src := &EXIF{ByteOrder: binary.LittleEndian, CompressedPrimary: true,
		IFD0: &IFD{Entries: []IFDEntry{
			chainEnt(TagImageDescription, TypeASCII, 4, []byte("abc\x00")),
			chainEnt(TagExifIFDPointer, TypeLong, 1, le32(0)),
		}},
		ExifIFD: &IFD{Entries: []IFDEntry{chainEnt(TagFNumber, TypeRational, 1, []byte{2, 0, 0, 0, 1, 0, 0, 0})}},
	}
	out, err := Encode(src)
	if err != nil {
		t.Fatal(err)
	}
	e, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	e.CompressedPrimary = true
	beforeIFD0, beforeExif := len(e.IFD0.Entries), len(e.ExifIFD.Entries)
	e.SetISO(100)
	if len(e.IFD0.Entries) != beforeIFD0 {
		t.Errorf("IFD0 went from %d to %d entries: it is an existing IFD", beforeIFD0, len(e.IFD0.Entries))
	}
	if len(e.ExifIFD.Entries) != beforeExif+1 {
		t.Errorf("Exif IFD went from %d to %d entries, want exactly the ISO added", beforeExif, len(e.ExifIFD.Entries))
	}
	for _, tag := range []TagID{TagExifVersion, TagColorSpace, TagComponentsConfiguration, TagPixelXDimension} {
		if e.ExifIFD.Get(tag) != nil {
			t.Errorf("tag %#04x added to an existing Exif IFD", tag)
		}
	}
	if e.NeedsImageSize() {
		t.Error("NeedsImageSize for an Exif IFD that was not created here")
	}
}

// TestMandatory_S32_GPSVersionIDIs2400: SetGPS writes GPSVersionID 2.4.0.0
// (Exif 3.1 §4.6.7.1.1, p. 83) when absent and keeps the version of a GPS IFD
// that has one.
func TestMandatory_S32_GPSVersionIDIs2400(t *testing.T) {
	t.Parallel()
	e := New(true)
	e.SetGPS(38.7, -9.1)
	if v := e.GPSIFD.Get(TagGPSVersionID); v == nil || !bytes.Equal(v.Value, []byte{2, 4, 0, 0}) {
		t.Errorf("GPSVersionID = %v, want 2 4 0 0", v)
	}
	e2 := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}, GPSIFD: &IFD{Entries: []IFDEntry{chainEnt(TagGPSVersionID, TypeByte, 4, []byte{2, 2, 0, 0})}}}
	e2.SetGPS(1, 2)
	if v := e2.GPSIFD.Get(TagGPSVersionID); v == nil || !bytes.Equal(v.Value, []byte{2, 2, 0, 0}) {
		t.Errorf("an existing GPSVersionID was changed to %v", v)
	}
}

// TestMandatory_V16_Exif31Tags: the tags new in Exif 3.1 are registered with
// the names, types and counts of the standard (Table 9/18; §4.6.6.4.3 and
// §4.6.6.7.47-52).
func TestMandatory_V16_Exif31Tags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tag  TagID
		name string
		typ  DataType
	}{
		{0x9287, "LearningOptOutIn", TypeUndefined},
		{0xA40D, "DevelopmentType", TypeShort},
		{0xA40E, "DevelopmentTypeDescription", TypeUTF8},
		{0xA40F, "DistortionCorrection", TypeShort},
		{0xA410, "ChromaticAberrationCorrection", TypeShort},
		{0xA411, "ShadingCorrection", TypeShort},
		{0xA412, "NoiseReduction", TypeShort},
	} {
		if got := TagName(tc.tag); got != tc.name {
			t.Errorf("TagName(%#04x) = %q, want %q", tc.tag, got, tc.name)
		}
		if info, ok := tagRegistry[tc.tag]; !ok || info.Type != tc.typ {
			t.Errorf("tag %#04x registered as %+v, want type %d", tc.tag, info, tc.typ)
		}
	}
}
