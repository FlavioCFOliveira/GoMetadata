package exif

// private_ifd.go — the private IFD that IFD0 of some Kodak images points to
// (task #362).
//
// IFD0 tag 0x8290 (ExifTool Exif.pm: "KodakIFD", "used in various types of
// Kodak images", a SubIFD whose table is Image::ExifTool::Kodak::IFD; study
// only) is a LONG that holds the offset of a standard IFD (TIFF 6.0 §2) with
// vendor tags: the Kodak ProBack of the corpus has KodakVersion (0x0000),
// TextualInfo (0x03F1) and the HostSoftware tags (0x0CE6, 0x0CE7), with the
// values after the rest of the EXIF data. Exif 3.1 Table 17, p. 93-94 knows no
// such tag; the MakerNote walker of this package (makernote_walk.go) reads
// the same structure for a MakerNote, and is reused.
//
// Encode keeps IFD0 entries as they are, so the pointer would address another
// block of the new stream (ExifTool: "KodakIFD pointer references previous
// KodakIFD directory", or an IFD with no entries) and the IFD and its values
// would be lost. EncodeRebased carries the IFD and the values it addresses
// from the source stream to the end of the new stream and points the entry
// at the copy, as it does for the data a MakerNote addresses outside itself.

import (
	"bytes"
	"encoding/binary"
	"math"
)

// tagKodakIFD is the IFD0 tag whose LONG value is the offset of a private IFD
// (see the top of this file).
const tagKodakIFD = TagID(0x8290)

// maxPrivateIFDEntries bounds the entries of a private IFD that is carried
// (the largest IFD of a real file has a few hundred; MakerNote IFDs are below
// 256 for metadata-extractor, TiffReader.processIfd).
const maxPrivateIFDEntries = 255

// appendPrivateIFD carries the private IFD that IFD0 of e points to (IFD0 tag
// 0x8290) from the source stream src to the end of out, the stream serialise
// wrote, sets its pointers for the new position and points the IFD0 entry at it.
// It returns out unchanged when e has no such entry, the entry does not address
// a plausible IFD in src, or src is not the stream e was parsed from (nil).
// BigTIFF is not handled, as for the MakerNote.
func appendPrivateIFD(out []byte, e *EXIF, src []byte) []byte {
	if e == nil || e.BigTIFF || e.IFD0 == nil || src == nil || len(out) < 8 {
		return out
	}
	order := e.ifd0ByteOrder()
	w, ok := carryPrivateIFD(e, src, order)
	if !ok {
		return out
	}
	return placePrivateIFD(out, w, order)
}

// placePrivateIFD appends the block of w to out at an even offset, sets its
// pointers for that position and points the IFD0 entry of out at it. It returns
// out unchanged when IFD0 has no such entry or the block would not fit 32 bits.
func placePrivateIFD(out []byte, w *mnWalker, order binary.ByteOrder) []byte {
	field, ok := findIFD0EntryField(out, tagKodakIFD, order)
	if !ok {
		return out
	}
	if len(out)&1 == 1 { // TIFF 6.0 §2: an IFD begins on a word boundary
		out = append(out, 0)
	}
	pos := uint64(len(out))
	if pos+uint64(len(w.buf)) > math.MaxUint32 {
		return out
	}
	for _, f := range w.fixes {
		if f.field+4 <= len(w.buf) {
			order.PutUint32(w.buf[f.field:], uint32(pos)+uint32(f.target)) //nolint:gosec // G115: bounded by the check above, and target is an offset in the block
		}
	}
	order.PutUint32(out[field:], uint32(pos)) //nolint:gosec // G115: bounded by the check above
	return append(out, w.buf...)
}

// carryPrivateIFD walks the private IFD that IFD0 of e points to and returns
// the walker that holds its bytes, with the values it addresses outside it
// appended, and the pointers to set once the block has a position.
func carryPrivateIFD(e *EXIF, src []byte, order binary.ByteOrder) (*mnWalker, bool) {
	ent := e.IFD0.Get(tagKodakIFD)
	if ent == nil || ent.Type != TypeLong || ent.Count != 1 || len(ent.Value) < 4 {
		return nil, false
	}
	off := int64(order.Uint32(ent.Value))
	n, ok := privateIFDEntries(src, off, order)
	if !ok {
		return nil, false
	}
	size := 2 + 12*n + 4 // TIFF 6.0 §2: count, entries, next-IFD offset
	blob := bytes.Clone(src[off : off+int64(size)])
	clear(blob[size-4:]) // the next-IFD offset addresses a chain that is not carried

	lay := MakerNoteLayout{ByteOrder: order, TIFFRelative: true}
	w := new(mnWalker)
	w.init(blob, lay, off)
	w.origin = off // the pointers count from the TIFF header, whatever the end of the IFD holds
	w.noCarry = countCrossing(blob, lay, w.origin) > 0
	w.src = src
	w.budget = min(len(src), maxMakerNoteCarry)
	w.walkIFD(0, 0)
	return w, true
}

// privateIFDEntries returns the entry count of the plausible IFD at offset off
// of src: at least one entry, at most maxPrivateIFDEntries, all inside src, and
// every entry of a type TIFF 6.0 §2 defines (1-12, or 13 IFD).
func privateIFDEntries(src []byte, off int64, order binary.ByteOrder) (int, bool) {
	if off < 8 || off+2 > int64(len(src)) {
		return 0, false
	}
	n := int(order.Uint16(src[off:]))
	if n == 0 || n > maxPrivateIFDEntries || off+2+12*int64(n)+4 > int64(len(src)) {
		return 0, false
	}
	for i := range n {
		if t := order.Uint16(src[off+2+12*int64(i)+2:]); t == 0 || t > uint16(TypeIFD) {
			return 0, false
		}
	}
	return n, true
}

// findIFD0EntryField returns the position, in the classic TIFF stream out, of
// the value field of the IFD0 entry of tag.
func findIFD0EntryField(out []byte, tag TagID, order binary.ByteOrder) (int, bool) {
	ifd := int(order.Uint32(out[4:]))
	if ifd < 8 || ifd+2 > len(out) {
		return 0, false
	}
	for i := range int(order.Uint16(out[ifd:])) {
		ep := ifd + 2 + i*12
		if ep+12 > len(out) {
			break
		}
		if TagID(order.Uint16(out[ep:])) == tag {
			return ep + 8, true
		}
	}
	return 0, false
}
