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
// Task #370 adds the other forms of the same value (ExifTool Olympus.pm, all
// IsOffset tags with an OffsetPair; study only):
//
//   - CameraSettings stored as a VALUE, not as a sub-IFD: the E-1 and E-300
//     write tag 0x2020 as UNDEFINED data that holds the entries of the IFD
//     (ExifTool: "Olympus really screwed up the format of the following
//     subdirectories (for the E-1 and E-300 anyway)"; the corpus OlympusE1.jpg
//     has it, PreviewImageStart 2556 and PreviewImageLength 26 counted from the
//     TIFF header, inside the MakerNote);
//   - the pair 0x1036 PreviewImageStart / 0x1037 PreviewImageLength in the
//     MakerNote IFD itself (the compacts: the corpus E-20, counted from the TIFF
//     header, "OLYMP\0" layout) and the read-only pair 0x0088 / 0x0089;
//   - the "OLYMP\0" layout, whose offsets count from the TIFF header, where the
//     offset may address data inside the MakerNote or outside the EXIF stream.
//
// Such an offset points outside the stream, or into the MakerNote, so Encode and
// EncodeRebased copy it as it is, and it is right only while the data keeps its
// distance from the byte the offset counts from. Whoever writes the stream into
// a container knows where the MakerNote and the data land; EXIF cannot.
// ExternalOffsets finds the fields, and the writer of the container rewrites them
// (format/jpeg, format/tiff).

import (
	"bytes"
	"encoding/binary"
)

// ExternalOffset is a MakerNote value that holds the offset, counted from the
// byte Base of the EXIF stream, of Length bytes that are outside the
// MakerNote's own IFD data: after the end of the image, in another segment of
// the file, or inside the MakerNote (see the top of this file).
type ExternalOffset struct {
	// Field is the position, in the TIFF stream the offset was found in, of its
	// 4-byte value. It lies inside the MakerNote.
	Field int
	// Value is the offset as stored.
	Value uint32
	// Length is the number of bytes the offset addresses.
	Length uint32
	// Base is the position in the stream of the byte the offset counts from: the
	// first byte of the MakerNote ("OLYMPUS\0" and "OM SYSTEM\0" layouts) or the
	// TIFF header, 0 ("OLYMP\0" layout).
	Base int
	// MNStart and MNLen are the position and the length in the stream of the
	// MakerNote that holds the field.
	MNStart, MNLen int
	// Order is the byte order of the field.
	Order binary.ByteOrder
}

// Target returns the position in the stream the offset addresses: Base + Value.
func (o ExternalOffset) Target() int64 { return int64(o.Base) + int64(o.Value) }

// The Olympus tags ExternalOffsets reads: the MakerNote tag of the
// CameraSettings (an IFD for the bodies after the E-1, its entries as a value
// for the E-1 and E-300; ExifTool Olympus.pm: 0x2020), and the tags of its
// PreviewImageStart and PreviewImageLength, and the pairs of the MakerNote IFD.
const (
	olympusCameraSettingsTag = TagID(0x2020)
	olympusPreviewStartTag   = TagID(0x0101)
	olympusPreviewLengthTag  = TagID(0x0102)
	olympusMainStartTag      = TagID(0x1036)
	olympusMainLengthTag     = TagID(0x1037)
	olympusOldStartTag       = TagID(0x0088)
	olympusOldLengthTag      = TagID(0x0089)
)

// ExternalOffsets returns the MakerNote values of the classic TIFF stream tiff
// that address data outside the MakerNote's own IFD data: the Olympus
// PreviewImageStart with its PreviewImageLength (see the top of this file). The
// result is nil for a stream without such a value, which is nearly every
// stream; it costs one search of the stream then. tiff is not modified. opts
// are the options tiff needs to be parsed (exif.AcceptRAWMagic for an ORF).
//
// A value is reported when its IFD is where the MakerNote layout puts it (the
// Olympus layouts "OLYMP\0", "OLYMPUS\0" and "OM SYSTEM\0"), both entries are
// LONGs of count 1, the field lies inside the MakerNote, and the length is not 0.
func ExternalOffsets(tiff []byte, opts ...ParseOption) []ExternalOffset {
	// Every Olympus layout that stores such an offset starts its MakerNote with
	// "OLYMP" or "OM SYSTEM"; a stream without either holds none.
	if !bytes.Contains(tiff, []byte("OLYMP")) && !bytes.Contains(tiff, []byte("OM SYSTEM")) {
		return nil
	}
	e, err := Parse(tiff, opts...)
	if err != nil {
		return nil
	}
	return e.ExternalOffsets(tiff)
}

// ExternalOffsets is the package function ExternalOffsets for an EXIF that was
// parsed from tiff: it does not parse again. The MakerNote of e must be the one
// tiff holds where Parse found it.
func (e *EXIF) ExternalOffsets(tiff []byte) []ExternalOffset {
	ctx, ok := olympusContext(e, tiff)
	if !ok {
		return nil
	}
	var out []ExternalOffset
	// The pairs of the MakerNote IFD itself (compacts; counted like every other
	// offset of the layout).
	out = appendPreview(out, tiff, ctx, int64(ctx.ifd), olympusMainStartTag, olympusMainLengthTag)
	out = appendPreview(out, tiff, ctx, int64(ctx.ifd), olympusOldStartTag, olympusOldLengthTag)
	// CameraSettings: the IFD (or the value that holds its entries) that tag
	// 0x2020 addresses; both are at the offset the entry stores.
	if at, ok := ctx.entryValueOffset(tiff, olympusCameraSettingsTag); ok {
		out = appendPreview(out, tiff, ctx, int64(ctx.base)+int64(at), olympusPreviewStartTag, olympusPreviewLengthTag)
	}
	return out
}

// olympusCtx is what ExternalOffsets knows about the Olympus MakerNote of a
// stream.
type olympusCtx struct {
	base    int // position of the byte the offsets of the layout count from
	order   binary.ByteOrder
	mnStart int
	mnLen   int
	ifd     int // position of the MakerNote IFD
}

// olympusContext returns the context of the Olympus MakerNote of e, found where
// e says it was in tiff, or false when e has none of a layout that stores such
// an offset.
//
//nolint:cyclop,gocyclo // a chain of independent validity tests of the MakerNote, each with its own reason
func olympusContext(e *EXIF, tiff []byte) (olympusCtx, bool) {
	if e == nil || e.BigTIFF || len(e.MakerNote) == 0 || e.MakerNoteIFD == nil {
		return olympusCtx{}, false
	}
	lay, ok := e.MakerNoteLayout()
	if !ok {
		return olympusCtx{}, false
	}
	// Only the Olympus layouts store such an offset. The Epson and Agfa notes
	// share the "OLYMP" layout, not its tags.
	isOlympus := lay.layout == mnOlympus || lay.layout == mnOMSystem || lay.layout == mnOlympusMake ||
		(lay.layout == mnOlympusOld && bytes.HasPrefix(e.MakerNote, []byte("OLYMP\x00")))
	if !isOlympus {
		return olympusCtx{}, false
	}
	mnOff := makerNoteSourceOffset(e)
	if mnOff == 0 || !holdsMakerNote(tiff, mnOff, e.MakerNote) {
		return olympusCtx{}, false
	}
	ctx := olympusCtx{
		order:   lay.ByteOrder,
		mnStart: int(mnOff), //nolint:gosec // G115: the MakerNote lies inside tiff, so its offset is below len(tiff)
		mnLen:   len(e.MakerNote),
		ifd:     int(mnOff) + lay.IFDOffset, //nolint:gosec // G115: as above
	}
	if !lay.TIFFRelative {
		ctx.base = ctx.mnStart + lay.Base
	}
	return ctx, true
}

// entryValueOffset returns the value of the entry of tag in the MakerNote IFD
// when it is an offset: a LONG or IFD of count 1, or an UNDEFINED or BYTE value
// longer than 4 bytes (the E-1 form), which both store the offset in the entry.
func (c olympusCtx) entryValueOffset(tiff []byte, tag TagID) (uint32, bool) {
	ifd := c.ifd
	if ifd < 0 || ifd+2 > len(tiff) {
		return 0, false
	}
	for i := range int(c.order.Uint16(tiff[ifd:])) {
		ep := ifd + 2 + i*12
		if ep+12 > len(tiff) {
			return 0, false
		}
		if TagID(c.order.Uint16(tiff[ep:])) != tag {
			continue
		}
		if !holdsOffset(DataType(c.order.Uint16(tiff[ep+2:])), c.order.Uint32(tiff[ep+4:])) {
			return 0, false
		}
		return c.order.Uint32(tiff[ep+8:]), true
	}
	return 0, false
}

// holdsOffset reports whether an entry of the type and count stores an offset in
// its value field: one LONG or IFD, or an UNDEFINED or BYTE value longer than 4
// bytes (TIFF 6.0 §2).
func holdsOffset(typ DataType, count uint32) bool {
	if typ == TypeLong || typ == TypeIFD {
		return count == 1
	}
	return (typ == TypeUndefined || typ == TypeByte) && count > 4
}

// appendPreview reads the IFD at position ifd of tiff and appends its
// preview offset (the start entry with the length the length entry gives) when
// both are LONGs of count 1 inside the MakerNote and the length is not 0.
//
//nolint:cyclop,gocyclo // one pass over the IFD with a test per entry kind
func appendPreview(out []ExternalOffset, tiff []byte, c olympusCtx, ifd int64, startTag, lengthTag TagID) []ExternalOffset {
	if ifd < 0 || ifd+2 > int64(len(tiff)) {
		return out
	}
	var start, length *ExternalOffset
	var st, ln ExternalOffset
	for i := range int(c.order.Uint16(tiff[ifd:])) {
		ep := ifd + 2 + int64(i)*12
		if ep+12 > int64(len(tiff)) {
			break
		}
		tag := TagID(c.order.Uint16(tiff[ep:]))
		if tag != startTag && tag != lengthTag {
			continue
		}
		// TIFF 6.0 §2: type 4 is LONG; a count of 1 holds the value inline. The
		// field must lie inside the MakerNote, which is what moves with it.
		if DataType(c.order.Uint16(tiff[ep+2:])) != TypeLong || c.order.Uint32(tiff[ep+4:]) != 1 ||
			int(ep+8) < c.mnStart || int(ep+12) > c.mnStart+c.mnLen {
			return out
		}
		f := ExternalOffset{Field: int(ep + 8), Value: c.order.Uint32(tiff[ep+8:]), Base: c.base, MNStart: c.mnStart, MNLen: c.mnLen, Order: c.order}
		if tag == startTag {
			st, start = f, &st
		} else {
			ln, length = f, &ln
		}
	}
	if start == nil || length == nil || length.Value == 0 {
		return out
	}
	st.Length = length.Value
	return append(out, st)
}
