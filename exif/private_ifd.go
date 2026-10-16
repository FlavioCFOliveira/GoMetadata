package exif

// private_ifd.go — the private IFDs that IFD0 of some images points to (tasks
// #362 and #370).
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
//
// Which IFD0 entries are pointers (task #370). The value of an unknown LONG is
// a number, and rewriting it as an offset could corrupt it, so the rule is
// closed: an entry is carried only when it is certain to address an IFD, that
// is, when it is
//
//   - of type IFD (13), the 4-byte offset of an IFD (Adobe PageMaker 6.0 TIFF
//     Technical Notes, TIFF Tech Note 1: "IFD = 13"; libtiff TIFF_IFD; see
//     TypeIFD), whatever the tag is; or
//   - a LONG whose tag ExifTool's Exif.pm marks "Flags => 'SubIFD'" with its own
//     table: 0x0190 GlobalParametersIFD, 0x014A SubIFDs (TIFF/EP and DNG; up to
//     10 offsets, "MaxSubdirs => 10"), 0x8290 KodakIFD, 0x888A LeafSubIFD and
//     0xFE00 KDC_IFD (study only: these are the registry of the tags that real
//     files hold; the corpus has only 0x8290).
//
// The pointers that Encode writes itself (Exif IFD 0x8769 and GPS IFD 0x8825,
// whose IFDs are in the model) are not touched. An entry that does not address a
// plausible IFD of the source (see privateIFDEntries) is written as Encode
// writes it, as is an entry of any other tag and type.

import (
	"bytes"
	"encoding/binary"
	"math"
	"slices"
)

// tagKodakIFD is the IFD0 tag whose LONG value is the offset of the private IFD
// of Kodak images (see the top of this file).
const tagKodakIFD = TagID(0x8290)

// privateIFDTags are the LONG tags of IFD0 that hold the offset of an IFD
// without being of type IFD (see the top of this file).
var privateIFDTags = [...]TagID{TagSubIFDs, 0x0190, tagKodakIFD, 0x888A, 0xFE00} //nolint:gochecknoglobals // read-only registry

// maxPrivateIFDsPerTag bounds the offsets of one entry that are carried
// (ExifTool: MaxSubdirs => 10 for SubIFD).
const maxPrivateIFDsPerTag = 10

// isPrivateIFDPointer reports whether the IFD0 entry ent is a pointer that
// EncodeRebased carries: 1 to maxPrivateIFDsPerTag offsets, of type IFD, or LONG
// with a tag of privateIFDTags.
func isPrivateIFDPointer(ent *IFDEntry) bool {
	if ent.Tag == TagExifIFDPointer || ent.Tag == TagGPSIFDPointer || ent.Tag == TagInteropIFDPointer {
		return false
	}
	if ent.Count == 0 || ent.Count > maxPrivateIFDsPerTag || len(ent.Value) < 4*int(ent.Count) {
		return false
	}
	if ent.Type == TypeIFD {
		return true
	}
	return ent.Type == TypeLong && slices.Contains(privateIFDTags[:], ent.Tag)
}

// maxPrivateIFDEntries bounds the entries of a private IFD that is carried
// (the largest IFD of a real file has a few hundred; MakerNote IFDs are below
// 256 for metadata-extractor, TiffReader.processIfd).
const maxPrivateIFDEntries = 255

// appendPrivateIFD carries the private IFDs that IFD0 of e points to (see the
// top of this file) from the source stream src to the end of out, the stream
// serialise wrote, sets their pointers for the new position and points the IFD0
// entries at them. It returns out unchanged when e has no such entry, an entry
// does not address a plausible IFD in src (that entry is left as it is), or src
// is not the stream e was parsed from (nil). BigTIFF is not handled, as for the
// MakerNote.
func appendPrivateIFD(out []byte, e *EXIF, src []byte) []byte {
	if e == nil || e.BigTIFF || e.IFD0 == nil || src == nil || len(out) < 8 {
		return out
	}
	order := e.ifd0ByteOrder()
	for i := range e.IFD0.Entries {
		if ent := &e.IFD0.Entries[i]; isPrivateIFDPointer(ent) {
			out = appendEntryIFDs(out, ent, src, order)
		}
	}
	return out
}

// appendEntryIFDs carries the IFD that each offset of the IFD0 entry ent
// addresses (see appendPrivateIFD).
func appendEntryIFDs(out []byte, ent *IFDEntry, src []byte, order binary.ByteOrder) []byte {
	field, ok := findIFD0EntryField(out, ent.Tag, order)
	if !ok {
		return out
	}
	for k := range int(ent.Count) {
		w, ok := carryPrivateIFD(ent, k, src, order)
		if !ok {
			continue
		}
		if at, ok := elementField(out, field, int(ent.Count), k, order); ok {
			out = placePrivateIFD(out, w, at, order)
		}
	}
	return out
}

// elementField returns the position in out of the 4-byte offset k of the entry
// of count offsets whose value field is at field: the field itself for a single
// offset, otherwise element k of the array it points to (TIFF 6.0 §2).
func elementField(out []byte, field, count, k int, order binary.ByteOrder) (int, bool) {
	if count == 1 {
		return field, field+4 <= len(out)
	}
	arr := int(order.Uint32(out[field:]))
	at := arr + 4*k
	return at, arr >= 8 && at+4 <= len(out)
}

// placePrivateIFD appends the block of w to out at an even offset, sets its
// pointers for that position and points the 4-byte field at of out at it. It
// returns out unchanged when the block would not fit 32 bits.
func placePrivateIFD(out []byte, w *mnWalker, at int, order binary.ByteOrder) []byte {
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
	order.PutUint32(out[at:], uint32(pos)) //nolint:gosec // G115: bounded by the check above
	return append(out, w.buf...)
}

// carryPrivateIFD walks the IFD that offset k of the IFD0 entry ent points to
// and returns the walker that holds its bytes, with the values it addresses
// outside it appended, and the pointers to set once the block has a position.
func carryPrivateIFD(ent *IFDEntry, k int, src []byte, order binary.ByteOrder) (*mnWalker, bool) {
	off := int64(order.Uint32(ent.Value[4*k:]))
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
