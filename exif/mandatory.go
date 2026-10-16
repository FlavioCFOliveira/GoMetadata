package exif

import (
	"encoding/binary"
)

// mandatory.go — the tags that Exif 3.1 marks mandatory in the IFDs this
// package creates on its own, and the type of the text this package writes.
//
// Source: CIPA DC-008-2026 (Exif 3.1) §4.6.9 "Tag Support Levels", Tables 17,
// 18 and 19, pp. 93-97, column "Compressed" (the primary image is JPEG
// compressed data), and the tag descriptions cited at each tag below. The
// support levels of Exif 3.0 (CIPA DC-008-2024) are the same for these tags;
// Exif 2.3 marked XResolution, YResolution and ResolutionUnit mandatory too,
// and Exif 3.0 §4.6.9 (Table 17) made them optional.
//
// Rule applied (task #321): a tag is added only when this package CREATES the
// IFD it belongs to. An IFD that exists (a parsed one, or one the caller
// built) is never completed: a file that lacks a mandatory tag keeps lacking
// it, as it must not change data nobody asked to change.
//
//	IFD0     YCbCrPositioning   M compressed (Table 17, p. 93)      = 1
//	         Exif IFD pointer   M all columns (Table 17, p. 93)
//	ExifIFD  ExifVersion        M all (Table 18, p. 94)             = "0300"
//	         ColorSpace         M all (Table 18, p. 95)             = 0xFFFF
//	         ComponentsConfiguration  M compressed, N otherwise (p. 94) = 1 2 3 0
//	         PixelXDimension    M compressed, N otherwise (p. 95)   from the image
//	         PixelYDimension    M compressed, N otherwise (p. 95)   from the image
//	GPS IFD  GPSVersionID       mandatory when any GPS tag is recorded
//	                            (§4.6.7.1.1, p. 83; SetGPS)         = 2 4 0 0
//
// "M" is "shall be recorded", "N" is "not allowed to record" (Table 17 notation,
// pp. 93-94). The Exif IFD of IFD1 and the Interoperability IFD have no
// mandatory tag for a compressed primary image (Tables 20-21, pp. 98-99).

// exifVersionDefault is the ExifVersion this package writes: Exif 3.1
// §4.6.6.1.1, p. 47: "In according with conformance to this standard, this
// tag shall be recorded like "0300" as 4-byte ASCII. Since the type is
// UNDEFINED, it shall not be terminated with NULL." Exif 3.1 keeps the
// version "0300" of Exif 3.0 (it has no "0310"): its changes are optional
// tags. Divergence from ExifTool, which writes "0232": the target standard of
// this library is 3.1, and a file that holds the type UTF-8 or a 2.31 tag such
// as OffsetTimeOriginal needs at least the version that defines it (see
// version.go).
const exifVersionDefault = "0300"

// colorSpaceUncalibrated is the ColorSpace written when the colour space of
// the image is not known. Exif 3.1 §4.6.6.2.1, p. 47: "Normally sRGB (=1) is
// used ... If a color space other than sRGB is used, Uncalibrated (=FFFF.H)
// is set. Image data recorded as Uncalibrated may be treated as sRGB when it
// is converted to Flashpix". This package cannot tell the colour space of a
// pixel stream it does not decode, and sRGB would be a claim about the image.
const colorSpaceUncalibrated = 0xFFFF

// New returns an EXIF for an image that has none: little-endian, with the
// tags that Exif 3.1 marks mandatory in the IFDs it creates (see the table at
// the top of this file). compressedPrimary selects the column "Compressed"
// (JPEG) of the support-level tables; CompressedPrimary of the result holds
// it. When it is true the result needs the pixel dimensions of the image
// (NeedsImageSize), which only the container knows.
func New(compressedPrimary bool) *EXIF {
	e := &EXIF{
		ByteOrder:         binary.LittleEndian,
		IFD0:              &IFD{},
		CompressedPrimary: compressedPrimary,
	}
	if compressedPrimary {
		// Exif 3.1 §4.6.5.1.13, p. 38: Count 1, Default 1 = centered; "in the
		// case of Y:Cb:Cr = 4:2:0, the TIFF default (centered) is recommended".
		// The subsampling of the image is not known here.
		e.IFD0.Entries = make([]IFDEntry, 0, 2)
		e.IFD0.set(TagYCbCrPositioning, TypeShort, 1, []byte{1, 0}, false)
	}
	// The Exif IFD pointer is mandatory in IFD0 for every image (Table 17).
	e.ensureExifIFD()
	return e
}

// addExifIFDDefaults fills the freshly created ExifIFD with the mandatory
// tags (see the table at the top of this file). The entries are written in tag
// order into one slice and one value buffer: this runs for every EXIF a write
// creates.
func (e *EXIF) addExifIFDDefaults() {
	order := e.ifd0ByteOrder()
	big := order == binary.BigEndian
	// 4 (ExifVersion) + 2 (ColorSpace) + 4 (ComponentsConfiguration) bytes.
	vals := make([]byte, 10)
	copy(vals, exifVersionDefault)
	order.PutUint16(vals[4:], colorSpaceUncalibrated)
	entries := make([]IFDEntry, 0, 5) // room for PixelXDimension and PixelYDimension
	entries = append(entries, IFDEntry{Tag: TagExifVersion, Type: TypeUndefined, Count: 4, Value: vals[0:4:4], bigEndian: big})
	if e.CompressedPrimary {
		// Exif 3.1 §4.6.6.3.3, p. 49: "1 2 3 0 (other cases)" than RGB
		// uncompressed: Y, Cb, Cr, and an unused fourth component.
		copy(vals[6:], []byte{1, 2, 3, 0})
		entries = append(entries, IFDEntry{Tag: TagComponentsConfiguration, Type: TypeUndefined, Count: 4, Value: vals[6:10:10], bigEndian: big})
		e.sizePending = true // PixelXDimension, PixelYDimension: §4.6.6.3.1-2, p. 48
	}
	entries = append(entries, IFDEntry{Tag: TagColorSpace, Type: TypeShort, Count: 1, Value: vals[4:6:6], bigEndian: big})
	e.ExifIFD.Entries = entries
}

// NeedsImageSize reports whether the ExifIFD was created by this package for
// a compressed primary image and still lacks PixelXDimension and
// PixelYDimension, which Exif 3.1 §4.6.6.3.1-2, p. 48, and Table 18, p. 95,
// make mandatory there ("the valid width of the meaningful image shall be
// recorded"). The caller that knows the dimensions of the image supplies
// them with SetImageSize, which ends the need.
func (e *EXIF) NeedsImageSize() bool { return e != nil && e.sizePending }

// isASCII reports whether every byte of s is a 7-bit ASCII character.
func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// setText stores s in tag of ifd as a NUL-terminated string of a tag defined
// as "ASCII or UTF-8" (Exif 3.1 Table 6, pp. 31-32, and Table 9, p. 45).
//
// Type: TypeASCII when s is 7-bit ASCII. Exif 3.1 §4.6.5.4.1, p. 42: "2-byte
// or larger character codes can not be used when ASCII is set as Type. When a
// 2-byte or larger code is necessary, UTF-8 shall be set as Type. The count is
// the value including NULL terminations." So any other s is stored as
// TypeUTF8 (129), and the ExifVersion is raised to "0300" when it is lower
// (requireExifVersion): the type 129 exists only since Exif 3.0, and
// §4.2, p. 14, makes the upper part of the version change "when there is a
// possibility that a data reader implementing the old standard may operate
// incorrectly due to the new specifications".
func (e *EXIF) setText(ifd *IFD, tag TagID, s string) {
	big := e.ifd0BigEndian()
	if isASCII(s) {
		v := asciiValue(s)
		ifd.set(tag, TypeASCII, uint32(len(v)), v, big) //nolint:gosec // G115: string length bounded by input
		return
	}
	v := utf8Value(s)
	ifd.set(tag, TypeUTF8, uint32(len(v)), v, big) //nolint:gosec // G115: string length bounded by input
	e.requireExifVersion(exifVersionDefault)
}
