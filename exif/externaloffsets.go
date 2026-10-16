package exif

// externaloffsets.go — the MakerNote values that address data OUTSIDE the EXIF
// stream (task #362).
//
// The MakerNote (tag 0x927C, Exif 3.1 §4.6.6.4.1, p. 49: "The contents are up
// to the manufacturer") is outside the standard, and a camera may store in it
// an offset to data that is not in the EXIF stream at all, counted from a point
// of its own. The Olympus E-series bodies and the compacts of the same
// generation (E-M1, E-M5, TG-850, SH-1, SP-100EE in the corpus) do so for the
// preview image: CameraSettings (the sub-IFD of MakerNote tag 0x2020) holds
// PreviewImageStart (0x0101) and PreviewImageLength (0x0102), the start counts
// from the first byte of the MakerNote ("OLYMPUS\0"; ExifTool Olympus.pm,
// MakerNoteOlympus2: Base => '$start - 8', and CameraSettings tags 0x101 and
// 0x102 with IsOffset, OffsetPair and DataTag PreviewImage: study only), and the
// preview is a JPEG stream that follows the end of the image in the file
// (ExifTool: "PreviewImage trailer (562439 bytes at offset 0xa78d00)" for the
// corpus E-M1, where the MakerNote is at file offset 3632 and PreviewImageStart
// is 10976976: 3632 + 10976976 = 0xa78d00).
//
// Such an offset points outside the stream, so Encode and EncodeRebased copy
// it as it is, and it is right only while the preview keeps its distance from
// the MakerNote. Whoever writes the stream into a container knows where the
// MakerNote and the preview land; EXIF cannot. ExternalOffsets finds the
// fields, and the writer of the container rewrites them (format/jpeg).

import (
	"bytes"
	"encoding/binary"
)

// ExternalOffset is a MakerNote value that holds the offset, counted from the
// byte Base of the EXIF stream, of Length bytes that are outside the stream.
type ExternalOffset struct {
	// Field is the position, in the TIFF stream the offset was found in, of its
	// 4-byte value.
	Field int
	// Value is the offset as stored.
	Value uint32
	// Length is the number of bytes the offset addresses.
	Length uint32
	// Base is the position in the stream of the byte the offset counts from:
	// the first byte of the MakerNote for the layouts reported.
	Base int
	// Order is the byte order of the field.
	Order binary.ByteOrder
}

// The Olympus tags ExternalOffsets reads: the MakerNote tag of the
// CameraSettings sub-IFD (ExifTool Olympus.pm: 0x2020 CameraSettings, the
// offset of an IFD for the bodies after the E-1), and the tags of its
// PreviewImageStart and PreviewImageLength.
const (
	olympusCameraSettingsTag = TagID(0x2020)
	olympusPreviewStartTag   = TagID(0x0101)
	olympusPreviewLengthTag  = TagID(0x0102)
)

// ExternalOffsets returns the MakerNote values of the classic TIFF stream tiff
// that address data outside the stream: the Olympus PreviewImageStart with its
// PreviewImageLength (see the top of this file). The result is nil for a stream
// without such a value, which is nearly every stream; it costs one search of the
// stream then. tiff is not modified.
//
// A value is reported when its IFD is where the MakerNote layout puts it (the
// Olympus layouts "OLYMPUS\0" and "OM SYSTEM\0", whose offsets count from the
// start of the MakerNote), both entries are LONGs of count 1, and the length is
// not 0.
func ExternalOffsets(tiff []byte) []ExternalOffset {
	// Every Olympus layout that stores such an offset starts its MakerNote with
	// "OLYMPUS" or "OM SYSTEM"; a stream without either holds none.
	if !bytes.Contains(tiff, []byte("OLYMPUS")) && !bytes.Contains(tiff, []byte("OM SYSTEM")) {
		return nil
	}
	e, err := Parse(tiff)
	if err != nil {
		return nil
	}
	base, order, ok := olympusBase(e, tiff)
	if !ok {
		return nil
	}
	ent := e.MakerNoteIFD.Get(olympusCameraSettingsTag)
	if ent == nil || ent.Count != 1 || (ent.Type != TypeLong && ent.Type != TypeIFD) || len(ent.Value) < 4 {
		return nil
	}
	return previewOffsets(tiff, int64(base)+int64(order.Uint32(ent.Value)), base, order)
}

// olympusBase returns the position in tiff of the byte that the offsets of the
// Olympus MakerNote of e count from (its first byte) and the byte order of the
// MakerNote, or false when e has no Olympus MakerNote of a layout whose offsets
// count from the MakerNote, found where e says it was.
//
//nolint:cyclop,gocyclo // a chain of independent validity tests of the MakerNote, each with its own reason
func olympusBase(e *EXIF, tiff []byte) (base int, order binary.ByteOrder, ok bool) {
	if e == nil || e.BigTIFF || len(e.MakerNote) == 0 || e.MakerNoteIFD == nil {
		return 0, nil, false
	}
	lay, ok := e.MakerNoteLayout()
	if !ok || lay.TIFFRelative || (lay.layout != mnOlympus && lay.layout != mnOMSystem && lay.layout != mnOlympusMake) {
		return 0, nil, false
	}
	mnOff := makerNoteSourceOffset(e)
	if mnOff == 0 || !holdsMakerNote(tiff, mnOff, e.MakerNote) {
		return 0, nil, false
	}
	return int(mnOff) + lay.Base, lay.ByteOrder, true //nolint:gosec // G115: the MakerNote lies inside tiff, so its offset is below len(tiff)
}

// previewOffsets reads the IFD at offset ifd of tiff and returns its
// PreviewImageStart with the length that PreviewImageLength gives.
//
//nolint:cyclop,gocyclo // one pass over the IFD with a test per entry kind
func previewOffsets(tiff []byte, ifd int64, base int, order binary.ByteOrder) []ExternalOffset {
	if ifd < 0 || ifd+2 > int64(len(tiff)) {
		return nil
	}
	var start, length *ExternalOffset
	var st, ln ExternalOffset
	for i := range int(order.Uint16(tiff[ifd:])) {
		ep := ifd + 2 + int64(i)*12
		if ep+12 > int64(len(tiff)) {
			break
		}
		tag := TagID(order.Uint16(tiff[ep:]))
		if tag != olympusPreviewStartTag && tag != olympusPreviewLengthTag {
			continue
		}
		// TIFF 6.0 §2: type 4 is LONG; a count of 1 holds the value inline.
		if DataType(order.Uint16(tiff[ep+2:])) != TypeLong || order.Uint32(tiff[ep+4:]) != 1 {
			return nil
		}
		f := ExternalOffset{Field: int(ep + 8), Value: order.Uint32(tiff[ep+8:]), Base: base, Order: order}
		if tag == olympusPreviewStartTag {
			st, start = f, &st
		} else {
			ln, length = f, &ln
		}
	}
	if start == nil || length == nil || length.Value == 0 {
		return nil
	}
	st.Length = length.Value
	return []ExternalOffset{st}
}
