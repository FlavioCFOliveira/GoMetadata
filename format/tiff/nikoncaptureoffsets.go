package tiff

// Nikon Capture offsets (task #355). Nikon Capture writes the MakerNote tag
// 0x0E0E ("NikonCaptureOffsets") into NEF files: copies of the positions of
// three structures of the OUTER TIFF. A NEF write moves those structures (the
// IFD0 stays at offset 8, but the SubIFDs and the PreviewIFD do not), so the
// copies must follow, or they point at unrelated bytes.
//
// The layout is not published by Nikon. It is described here from the format
// notes of ExifTool (lib/Image/ExifTool/Nikon.pm, tag 0x0E0E, the
// CaptureOffsets table and ProcessNikonCaptureOffsets; a study reference only:
// it is GPL/Artistic licensed, no code is used, and ExifTool itself documents
// that it does NOT update these offsets) and was checked against the corpus
// file testdata/corpus/raw/exiftool/Nikon.nef, whose tag holds 42 bytes:
//
//	value = "0100" (4 ASCII bytes) | count n (2) | n entries of 12 bytes
//	entry = identifier (4) | offset (4) | 4 bytes of unknown meaning
//
// The integers are in the byte order of the MakerNote (its own embedded TIFF
// header), which may differ from the outer TIFF's. The identifiers:
//
//	1  IFD0_Offset        offset of IFD0, counted from the start of the file
//	2  PreviewIFD_Offset  offset of the Nikon PreviewIFD, counted from the
//	                      start of the file; the structure is the IFD that
//	                      MakerNote tag 0x0011 names relative to the MakerNote
//	3  SubIFD_Offset      offset of the first SubIFD of IFD0 (tag 0x014A)
//
// In the corpus file the values are 8, 11362 and 1440, and each names the
// structure the identifier says (IFD0, the PreviewIFD inside the MakerNote
// blob, the first SubIFD).
//
// Policy, which never corrupts what it does not understand: an entry is
// rewritten only when its identifier is known AND its value equals the
// position of the structure in the SOURCE file (so a value that has some other
// meaning in a camera model this code has not seen is left alone) AND the
// structure exists in the output. Entries with unknown identifiers, the four
// unknown bytes of every entry, a value that does not start with "0100", and a
// value that is not complete are carried unchanged. The PreviewIFD moves with
// the MakerNote blob (it lies inside the blob the NEF writer extends to hold
// it), so its new position is the new start of the MakerNote's embedded TIFF
// plus the relative offset of tag 0x0011, which does not change.

import (
	"bytes"
	"encoding/binary"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

const (
	// nikonTagCaptureOffsets is the MakerNote tag of the Nikon Capture offsets.
	nikonTagCaptureOffsets exif.TagID = 0x0E0E
	// nikonCaptureOffsetsEntryLen is the size of one entry of the value.
	nikonCaptureOffsetsEntryLen = 12
	// nikonCaptureOffsetsHeaderLen is the size of the "0100" version and the
	// entry count that precede the entries.
	nikonCaptureOffsetsHeaderLen = 6
	// Identifiers of the entries this package knows.
	nikonCaptureIFD0Offset     = 1
	nikonCapturePreviewIFDOfs  = 2
	nikonCaptureSubIFDOffset   = 3
	nikonCaptureOffsetsVersion = "0100"
	nikonCaptureMaxEntries     = 64 // a real value holds 3; bounds the scan
	nikonCaptureMaxIFDEntries  = 4096
)

// nikonCapturePlace is where the Nikon Capture offsets of one TIFF stream are,
// and the positions of the structures they name.
type nikonCapturePlace struct {
	order      binary.ByteOrder // byte order of the MakerNote
	entriesAt  int              // offset in the stream of the first entry
	count      int              // number of entries
	ifd0       uint32           // offset of IFD0
	previewIFD uint32           // offset of the PreviewIFD; 0 when there is none
	subIFD     uint32           // offset of the first SubIFD; 0 when there is none
}

// nikonMakerNote is the Nikon Type-3 MakerNote of a TIFF stream.
type nikonMakerNote struct {
	off, len uint32           // the MakerNote value in the outer stream
	order    binary.ByteOrder // byte order of its embedded TIFF
	base     uint64           // offset of its embedded TIFF header: its offsets count from here
	ifd0     uint64           // offset of its first IFD
}

// locateNikonMakerNote finds the Nikon Type-3 MakerNote of the classic TIFF
// stream tiff, whose outer byte order is outer and whose IFD0 is at ifd0.
func locateNikonMakerNote(tiff []byte, outer binary.ByteOrder, ifd0 uint32) (mn nikonMakerNote, ok bool) {
	exifOff, _, ok := ifdEntryValue(tiff, ifd0, outer, uint16(exif.TagExifIFDPointer))
	if !ok {
		return mn, false
	}
	// MakerNote: an out-of-line UNDEFINED value.
	off, n, ok := ifdEntryValue(tiff, exifOff, outer, uint16(exif.TagMakerNote))
	if !ok || uint64(off)+uint64(n) > uint64(len(tiff)) {
		return mn, false
	}
	blob := tiff[off : off+n]
	if !isNikonType3Blob(blob) {
		return mn, false
	}
	hdr, order := findNikonMNTIFFHeader(blob)
	if order == nil {
		return mn, false
	}
	base := uint64(off) + uint64(hdr) //nolint:gosec // G115: hdr is the offset of the embedded TIFF header, at most nikonMNPrefixMaxLen
	return nikonMakerNote{off: off, len: n, order: order, base: base, ifd0: base + uint64(order.Uint32(blob[hdr+4:]))}, true
}

// locateNikonCapture finds the Nikon Capture offsets in tiff, a classic TIFF
// stream (a whole file, a metadata prefix, or a rebuilt header), and the
// positions of the structures they name. ok is false when tiff has no Nikon
// Type-3 MakerNote with a complete tag 0x0E0E that starts with "0100".
//
// The PreviewIFD of a SOURCE file lies beyond the declared bytes of the
// MakerNote value (the NEF writer extends the value to cover it), so its
// position is only computed there; in a rebuilt header (inBlob) it must lie
// inside the MakerNote value, or it is reported as absent.
func locateNikonCapture(tiff []byte, inBlob bool) (p nikonCapturePlace, ok bool) {
	outer, ifd0, ok := classicTIFFHeader(tiff)
	if !ok {
		return p, false
	}
	mn, ok := locateNikonMakerNote(tiff, outer, ifd0)
	if !ok {
		return p, false
	}
	p.order, p.ifd0 = mn.order, ifd0
	if p.entriesAt, p.count, ok = captureEntries(tiff, mn); !ok {
		return p, false
	}
	// The PreviewIFD: tag 0x0011 of the MakerNote IFD0, relative to the
	// MakerNote's embedded TIFF, and inside the value (which is how the NEF
	// writer carries it).
	if rel, found := inlineLong(tiff, mn.ifd0, mn.order, uint16(nikonTagPreviewIFD)); found {
		if abs := mn.base + uint64(rel); abs <= 0xFFFFFFFF && (!inBlob || abs < uint64(mn.off)+uint64(mn.len)) {
			p.previewIFD = uint32(abs)
		}
	}
	// The first SubIFD: the first value of tag 0x014A of IFD0.
	p.subIFD = firstSubIFD(tiff, ifd0, outer)
	return p, true
}

// captureEntries returns the offset of the first entry of the Nikon Capture
// offsets value (tag 0x0E0E) of mn and the number of entries, or false when the
// value is missing, does not start with "0100", or is not complete.
func captureEntries(tiff []byte, mn nikonMakerNote) (entriesAt, count int, ok bool) {
	capOff, capLen, ok := mnEntryValue(tiff, mn.ifd0, mn.base, mn.order, uint16(nikonTagCaptureOffsets))
	if !ok || capLen < nikonCaptureOffsetsHeaderLen || uint64(capOff)+uint64(capLen) > uint64(len(tiff)) ||
		!bytes.HasPrefix(tiff[capOff:], []byte(nikonCaptureOffsetsVersion)) {
		return 0, 0, false
	}
	count = int(mn.order.Uint16(tiff[capOff+4:]))
	if count == 0 || count > nikonCaptureMaxEntries || nikonCaptureOffsetsHeaderLen+count*nikonCaptureOffsetsEntryLen > int(capLen) {
		return 0, 0, false
	}
	return int(capOff) + nikonCaptureOffsetsHeaderLen, count, true
}

// patchNikonCaptureOffsets rewrites the Nikon Capture offsets of out, the
// rebuilt header of a NEF write, for the positions the structures they name
// have in out. old is the placement found in the source (locateNikonCapture):
// an entry is rewritten only when it equals the source position of the
// structure it names (see the policy in the file comment). out is edited in
// place and is the caller's.
func patchNikonCaptureOffsets(out []byte, old nikonCapturePlace) {
	cur, ok := locateNikonCapture(out, true)
	if !ok || cur.count != old.count {
		return
	}
	for i := range cur.count {
		at := cur.entriesAt + i*nikonCaptureOffsetsEntryLen
		id := cur.order.Uint32(out[at:])
		val := cur.order.Uint32(out[at+4:])
		var oldPos, newPos uint32
		switch id {
		case nikonCaptureIFD0Offset:
			oldPos, newPos = old.ifd0, cur.ifd0
		case nikonCapturePreviewIFDOfs:
			oldPos, newPos = old.previewIFD, cur.previewIFD
		case nikonCaptureSubIFDOffset:
			oldPos, newPos = old.subIFD, cur.subIFD
		default:
			continue // unknown identifier: carried unchanged
		}
		if oldPos != 0 && newPos != 0 && val == oldPos {
			cur.order.PutUint32(out[at+4:], newPos)
		}
	}
}

// classicTIFFHeader returns the byte order and the IFD0 offset of the classic
// TIFF stream b, or false.
func classicTIFFHeader(b []byte) (binary.ByteOrder, uint32, bool) {
	if len(b) < 8 {
		return nil, 0, false
	}
	var o binary.ByteOrder
	switch string(b[:2]) {
	case "II":
		o = binary.LittleEndian
	case "MM":
		o = binary.BigEndian
	default:
		return nil, 0, false
	}
	if o.Uint16(b[2:]) != 42 {
		return nil, 0, false
	}
	return o, o.Uint32(b[4:]), true
}

// ifdEntryValue scans the IFD at ifd in b (outer-TIFF offsets) for tag and
// returns, for a value stored out of line, its offset and byte length; for
// a single inline LONG (count 1) its value and 4. ok is false when the tag is
// missing or malformed.
func ifdEntryValue(b []byte, ifd uint32, o binary.ByteOrder, tag uint16) (off, n uint32, ok bool) {
	e, ok := findIFDEntry(b, uint64(ifd), o, tag)
	if !ok {
		return 0, 0, false
	}
	typ, count := o.Uint16(b[e+2:]), o.Uint32(b[e+4:])
	size := typeSize(typ)
	if size == 0 {
		return 0, 0, false
	}
	total := uint64(size) * uint64(count)
	if total <= 4 {
		return o.Uint32(b[e+8:]), 4, true // inline: the value itself (a pointer tag)
	}
	if total > 0xFFFFFFFF {
		return 0, 0, false
	}
	return o.Uint32(b[e+8:]), uint32(total), true
}

// mnEntryValue is ifdEntryValue for an IFD of the MakerNote, whose offsets
// count from base.
func mnEntryValue(b []byte, ifd, base uint64, o binary.ByteOrder, tag uint16) (off, n uint32, ok bool) {
	e, ok := findIFDEntry(b, ifd, o, tag)
	if !ok {
		return 0, 0, false
	}
	typ, count := o.Uint16(b[e+2:]), o.Uint32(b[e+4:])
	size := typeSize(typ)
	total := uint64(size) * uint64(count)
	abs := base + uint64(o.Uint32(b[e+8:]))
	if size == 0 || total <= 4 || total > 0xFFFFFFFF || abs > 0xFFFFFFFF {
		return 0, 0, false
	}
	return uint32(abs), uint32(total), true
}

// findIFDEntry returns the offset in b of the entry of tag in the IFD at ifd,
// whose 12 bytes lie inside b.
func findIFDEntry(b []byte, ifd uint64, o binary.ByteOrder, tag uint16) (int, bool) {
	if ifd+2 > uint64(len(b)) {
		return 0, false
	}
	count := int(o.Uint16(b[ifd:]))
	if count > nikonCaptureMaxIFDEntries || ifd+2+uint64(count)*12 > uint64(len(b)) {
		return 0, false
	}
	for i := range count {
		e := int(ifd) + 2 + i*12 //nolint:gosec // G115: ifd+2+12*count was checked to lie inside b above
		if o.Uint16(b[e:]) == tag {
			return e, true
		}
	}
	return 0, false
}

// inlineLong returns the value of the LONG entry of tag, count 1, in the IFD
// at ifd of b.
func inlineLong(b []byte, ifd uint64, o binary.ByteOrder, tag uint16) (uint32, bool) {
	e, ok := findIFDEntry(b, ifd, o, tag)
	if !ok || o.Uint16(b[e+2:]) != 4 || o.Uint32(b[e+4:]) != 1 {
		return 0, false
	}
	return o.Uint32(b[e+8:]), true
}

// firstSubIFD returns the offset of the first SubIFD of the IFD0 at ifd0, or
// 0 when it has none.
func firstSubIFD(b []byte, ifd0 uint32, o binary.ByteOrder) uint32 {
	e, ok := findIFDEntry(b, uint64(ifd0), o, uint16(exif.TagSubIFDs))
	if !ok {
		return 0
	}
	count := o.Uint32(b[e+4:])
	if count == 0 || (o.Uint16(b[e+2:]) != 4 && o.Uint16(b[e+2:]) != 13) {
		return 0
	}
	if count == 1 {
		return o.Uint32(b[e+8:])
	}
	arr := uint64(o.Uint32(b[e+8:]))
	if arr+4 > uint64(len(b)) {
		return 0
	}
	return o.Uint32(b[arr:])
}
