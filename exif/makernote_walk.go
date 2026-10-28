package exif

// makernote_walk.go — the walk of a MakerNote IFD that EncodeRebased plans its
// repair with (see makernote_rebase.go), and the tests that decide how the
// offsets of a note are to be read.

import (
	"bytes"
	"encoding/binary"
)

// maxMakerNoteCarry bounds the bytes that planMakerNote copies from the source
// stream after the end of the MakerNote.
const maxMakerNoteCarry = 16 << 20

// maxMakerNoteSteps bounds the IFD entries planMakerNote visits, against a
// crafted MakerNote with many entries that address overlapping ranges.
const maxMakerNoteSteps = 1 << 16

// Sub-IFD tags of the MakerNote layouts whose sub-IFDs are carried.
const (
	// Nikon: offsets relative to the embedded TIFF header (ExifTool Nikon.pm,
	// PreviewIFD and NikonScanIFD; format/tiff/relocate_nef.go).
	nikonTagPreviewIFD = TagID(0x0011)
	nikonTagScanIFD    = TagID(0x0E10)

	// Ricoh: the value of this tag is "[Ricoh Camera Info]" and a version byte
	// (20 bytes) followed by an IFD (ExifTool Ricoh.pm, 0x2001 RicohSubdir,
	// Validate /^\[Ricoh Camera Info\]/, Start => $valuePtr + 20, big endian).
	ricohTagSubdir    = TagID(0x2001)
	ricohSubdirMagic  = "[Ricoh Camera Info]"
	ricohSubdirHeader = len(ricohSubdirMagic) + 1
)

// mnEntry is one entry of a MakerNote IFD under walk.
type mnEntry struct {
	field int // offset in buf of the 4-byte value-or-offset field
	tag   TagID
	typ   DataType
	cnt   uint32
	total int64 // byte size of the value; 0 for a type this package does not know
}

// readMNEntry decodes the entry at offset ep of buf, with the byte order of
// the note.
func readMNEntry(buf []byte, ep int, order binary.ByteOrder) mnEntry {
	typ := DataType(order.Uint16(buf[ep+2:]))
	cnt := order.Uint32(buf[ep+4:])
	return mnEntry{
		field: ep + 8,
		tag:   TagID(order.Uint16(buf[ep:])),
		typ:   typ,
		cnt:   cnt,
		total: int64(typeSize(typ)) * int64(cnt), // at most 8 x 2^32
	}
}

// ifdEntryCount returns the number of entries the IFD at offset off of buf can
// hold: its declared count, limited to the entries that fit in buf; false when
// the IFD does not start inside buf.
func ifdEntryCount(buf []byte, off int, order binary.ByteOrder) (int, bool) {
	if off < 0 || off+2 > len(buf) {
		return 0, false
	}
	return min(int(order.Uint16(buf[off:])), (len(buf)-off-2)/12), true
}

// eachOOL calls fn(pointer, total) for every out-of-line entry (a value of
// more than 4 bytes) of the top IFD of mn, in the byte order of the layout.
func eachOOL(mn []byte, lay MakerNoteLayout, fn func(ptr, total int64)) {
	n, ok := ifdEntryCount(mn, lay.IFDOffset, lay.ByteOrder)
	if !ok {
		return
	}
	for i := range n {
		e := readMNEntry(mn, lay.IFDOffset+2+i*12, lay.ByteOrder)
		if e.total > 4 {
			fn(int64(lay.ByteOrder.Uint32(mn[e.field:])), e.total)
		}
	}
}

// offsetsCountFromTIFF reports whether the offsets of the MakerNote IFD of mn
// count from the TIFF header (true) or from the MakerNote (false). The layout
// table says so for each vendor, and it follows metadata-extractor
// (ExifTiffHandler.processMakernote), which is wrong for some files: the
// ExifTool corpus file Sanyo.jpg holds TIFF offsets (1168, 1180, ...) in a
// MakerNote at TIFF offset 770, which ExifTool reads with its default base
// (MakerNotes.pm, MakerNoteSanyo: Start => $valuePtr + 8, no Base), and the
// Samsung NX1 holds offsets from the start of the MakerNote (438, 454, ...)
// in a MakerNote at TIFF offset 802 although the table says TIFF header. The
// interpretation under which more out-of-line values lie inside the MakerNote
// decides; a tie keeps the table. mnOff is the offset of mn in the source
// stream.
func offsetsCountFromTIFF(mn []byte, lay MakerNoteLayout, mnOff int64) bool {
	tiff := valuesInside(mn, lay, true, mnOff)
	if lay.TIFFRelative {
		tiff = max(tiff, valuesInside(mn, lay, true, pointerOrigin(mn, lay, mnOff)))
	}
	blob := valuesInside(mn, lay, false, 0)
	switch {
	case tiff > blob:
		return true
	case blob > tiff:
		return false
	}
	return lay.TIFFRelative
}

// valuesInside counts the out-of-line values of the MakerNote IFD of mn that
// lie wholly inside mn when its offsets are read as counting from the TIFF
// header whose position, relative to the MakerNote, is -origin (tiff true), or
// from the point lay.Base of the MakerNote (tiff false).
func valuesInside(mn []byte, lay MakerNoteLayout, tiff bool, origin int64) int {
	in := 0
	eachOOL(mn, lay, func(ptr, total int64) {
		t := ptr + int64(lay.Base)
		if tiff {
			t = ptr - origin
		}
		if t >= 0 && t+total <= int64(len(mn)) {
			in++
		}
	})
	return in
}

// pointerOrigin returns the TIFF offset that the TIFF-relative pointers of the
// MakerNote mn (at offset mnOff of the source stream) were written for. It is
// mnOff, except for a Canon MakerNote re-saved by software that moved it
// without updating its pointers: Canon ends its notes with a footer, "II*\0"
// or "MM\0*" and the 32-bit TIFF offset of the MakerNote itself, which
// ExifTool uses to correct the base (MakerNotes.pm, FixBase: "Adjusted
// MakerNotes base by"). The footer offset is taken when the last value of the
// note ends where the footer starts under it and not under mnOff (ExifTool's
// own test, which also keeps the notes of Picasa and ACDSee, which updated the
// pointers and not the footer), or else when more of the values lie inside the
// MakerNote read against it.
func pointerOrigin(mn []byte, lay MakerNoteLayout, mnOff int64) int64 {
	const footerLen = 8
	if len(mn) <= footerLen {
		return mnOff
	}
	f := mn[len(mn)-footerLen:]
	if !isTIFFHeaderMark(f, lay.ByteOrder) {
		return mnOff
	}
	foot := int64(lay.ByteOrder.Uint32(f[4:]))
	if foot == mnOff {
		return mnOff
	}
	endsAtFooter := func(origin int64) bool {
		d := int64(len(mn)-footerLen) - lastValueEnd(mn, lay, origin)
		return d == 0 || d == 1 // give or take a pad byte
	}
	switch fo, mo := endsAtFooter(foot), endsAtFooter(mnOff); {
	case fo && !mo:
		return foot
	case mo && !fo:
		return mnOff
	}
	if valuesInside(mn, lay, true, foot) > valuesInside(mn, lay, true, mnOff) {
		return foot
	}
	return mnOff
}

// lastValueEnd returns the offset of mn where the last out-of-line value of
// its IFD ends, reading the offsets as counting from the TIFF header at
// -origin relative to mn; -1 when there is none.
func lastValueEnd(mn []byte, lay MakerNoteLayout, origin int64) int64 {
	end := int64(-1)
	eachOOL(mn, lay, func(ptr, total int64) { end = max(end, ptr-origin+total) })
	return end
}

// countCrossing counts the out-of-line values of the MakerNote IFD of mn that
// start inside it and end past its end, reading the offsets as counting from
// the TIFF header at -origin relative to mn. In a TIFF-relative note whose
// pointers are right, every value lies wholly inside the note (Canon), or
// wholly outside it (the Olympus C-720UZ keeps its values after the Exif
// data). A value that straddles the end means the pointers are off by a
// constant, as ExifTool's "Possibly incorrect maker notes offsets (fix by
// N?)" reports for corpus files like Canon PowerShot S330 and 1054323: the
// data that its pointers address is not where they say, and copying it would
// invent values.
func countCrossing(mn []byte, lay MakerNoteLayout, origin int64) int {
	c := 0
	eachOOL(mn, lay, func(ptr, total int64) {
		if t := ptr - origin; t >= 0 && t < int64(len(mn)) && t+total > int64(len(mn)) {
			c++
		}
	})
	return c
}

// mnWalker walks a MakerNote IFD, collecting the pointers to repair and the
// data to carry (see planMakerNote).
type mnWalker struct {
	order   binary.ByteOrder
	tiffRel bool
	base    int    // origin of the value offsets inside the blob (layouts that are not TIFF-relative)
	buf     []byte // the blob; a private copy once it is patched or extended
	origLen int
	mnOff   int64  // position of the blob in src
	origin  int64  // TIFF offset that the TIFF-relative pointers were written for: mnOff, or the Canon footer
	src     []byte // the source stream; nil when it cannot be trusted
	budget  int    // bytes that may still be carried
	over    bool   // a value to carry was refused because it exceeds budget
	steps   int
	copied  bool
	nikon   bool // a Nikon note: Type 1, 2 (headerless) or 3
	olympus bool
	ricoh   bool
	noCarry bool // the pointers are inconsistent: carry nothing from outside
	lost    int  // pointers that address data outside the blob and were left as they are (makernote_edit.go tests it to be 0)
	fixes   []mnFix
	carried map[[2]int64]int // source range -> position in buf, so one value is carried once
}

// init prepares w to walk the MakerNote mn of layout lay at offset mnOff of
// the source stream. The walker lives on the caller's stack.
func (w *mnWalker) init(mn []byte, lay MakerNoteLayout, mnOff int64) {
	*w = mnWalker{
		order:   lay.ByteOrder,
		tiffRel: lay.TIFFRelative,
		base:    lay.Base,
		buf:     mn,
		origLen: len(mn),
		mnOff:   mnOff,
		nikon:   lay.layout == mnNikon1 || lay.layout == mnNikon2 || lay.layout == mnNikon3,
		olympus: lay.layout == mnOlympusOld || lay.layout == mnOlympus || lay.layout == mnOMSystem || lay.layout == mnOlympusMake,
		ricoh:   lay.layout == mnRicoh || lay.layout == mnRicohMake,
	}
	w.origin = mnOff
	if lay.TIFFRelative {
		w.origin = pointerOrigin(mn, lay, mnOff)
		w.noCarry = countCrossing(mn, lay, w.origin) > 0
		if n, ok := ifdEntryCount(mn, lay.IFDOffset, lay.ByteOrder); ok {
			w.fixes = make([]mnFix, 0, min(n, 128)) // one fix per out-of-line value, at most
		}
	}
}

// own makes buf a private, extendable copy of the MakerNote.
func (w *mnWalker) own() {
	if !w.copied {
		w.buf = append(make([]byte, 0, len(w.buf)+len(w.buf)/4+64), w.buf...)
		w.copied = true
	}
}

// isSubIFDTag reports whether tag of the top IFD of the MakerNote holds an IFD
// for the layout being walked: the Nikon PreviewIFD and NikonScanIFD, the
// Olympus sub-IFDs Equipment, CameraSettings, RawDevelopment, RawDev2,
// ImageProcessing, FocusInfo and RawInfo (ExifTool Olympus.pm, tags
// 0x2010-0x2050 and 0x3000; stored as out-of-line UNDEFINED data by the E-1,
// as an offset by later bodies) and the Ricoh sub-directory.
func (w *mnWalker) isSubIFDTag(tag TagID) bool {
	switch {
	case w.nikon:
		return tag == nikonTagPreviewIFD || tag == nikonTagScanIFD
	case w.olympus:
		switch tag {
		case 0x2010, 0x2020, 0x2030, 0x2031, 0x2040, 0x2050, 0x3000:
			return true
		}
	case w.ricoh:
		return tag == ricohTagSubdir
	}
	return false
}

// targetOf converts the value of a pointer into an offset of buf (negative or
// beyond the blob when it addresses data outside it).
func (w *mnWalker) targetOf(old int64) int64 {
	if w.tiffRel {
		return old - w.origin
	}
	return old + int64(w.base)
}

// jpegPair collects the JPEGInterchangeFormat and ...Length entries of a
// Nikon PreviewIFD (Exif 3.1 §4.6.5, p. 39, tags 0x0201 and 0x0202): the
// preview image is the data they address.
type jpegPair struct {
	field  int // offset of the pointer field, or -1
	length int64
}

// walkIFD visits the IFD at offset off of buf: TIFF 6.0 §2 layout, a 2-byte
// count and 12-byte entries.
func (w *mnWalker) walkIFD(off, depth int) {
	n, ok := ifdEntryCount(w.buf, off, w.order)
	if !ok || depth > 2 {
		return
	}
	pair := jpegPair{field: -1}
	for i := range n {
		if w.steps++; w.steps > maxMakerNoteSteps {
			return
		}
		w.entry(readMNEntry(w.buf, off+2+i*12, w.order), depth, &pair)
	}
	if pair.field >= 0 && pair.length > 0 {
		w.ref(pair.field, pair.length)
	}
}

// entry handles one entry of the IFD under walk.
func (w *mnWalker) entry(e mnEntry, depth int, pair *jpegPair) {
	switch {
	case e.total == 0:
		return // a type this package does not know
	case e.total > 4:
		w.valueEntry(e, depth)
	case depth == 0 && w.isSubIFDTag(e.tag) && e.cnt == 1 && (e.typ == TypeLong || e.typ == TypeIFD):
		w.subIFD(e.field, depth)
	case w.nikon && depth > 0:
		w.jpegEntry(e, pair)
	}
}

// valueEntry handles an out-of-line entry: its value is repaired, and when it
// is an IFD stored as the value (Olympus E-1 sub-IFDs, Ricoh) that IFD is
// walked.
func (w *mnWalker) valueEntry(e mnEntry, depth int) {
	loc, ok := w.ref(e.field, e.total)
	if ok && depth == 0 && w.isSubIFDTag(e.tag) && (e.typ == TypeUndefined || e.typ == TypeByte) {
		w.walkValueIFD(loc, depth)
	}
}

// jpegEntry records the entries of a Nikon PreviewIFD that locate the preview
// image: the inline LONGs 0x0201 (offset) and 0x0202 (length).
func (w *mnWalker) jpegEntry(e mnEntry, pair *jpegPair) {
	if e.typ != TypeLong || e.cnt != 1 {
		return
	}
	switch e.tag {
	case TagJPEGInterchangeFormat:
		pair.field = e.field
	case TagJPEGInterchangeFormatLength:
		pair.length = int64(w.order.Uint32(w.buf[e.field:]))
	}
}

// walkValueIFD walks the IFD that is the out-of-line value at offset loc of
// buf (Olympus E-1; Ricoh: after the 20-byte header).
func (w *mnWalker) walkValueIFD(loc, depth int) {
	if w.ricoh {
		if !bytes.HasPrefix(w.buf[loc:], []byte(ricohSubdirMagic)) {
			return
		}
		loc += ricohSubdirHeader
	}
	w.walkIFD(loc, depth+1)
}

// ref handles the pointer in the 4-byte field at offset field of buf, which
// addresses total bytes. It returns the offset of buf where the data is after
// the repair (inside the blob, or carried), and false when it is left alone.
func (w *mnWalker) ref(field int, total int64) (int, bool) {
	target := w.targetOf(int64(w.order.Uint32(w.buf[field:])))
	if w.tiffRel && target < 0 {
		w.lost++
		return 0, false // before the MakerNote: a stale or zero pointer, left as it is
	}
	if target >= 0 && target+total <= int64(w.origLen) {
		w.point(field, int(target)) // inside the blob
		return int(target), true
	}
	if w.noCarry {
		// A TIFF-relative note whose pointers are inconsistent (see
		// countCrossing): every pointer follows the blob by the same
		// distance, which keeps the note exactly as inconsistent as it was,
		// and so exactly as ExifTool's FixBase reads it.
		if w.tiffRel && target >= 0 {
			w.point(field, int(target))
			return int(target), true
		}
		w.lost++
		return 0, false
	}
	if loc, ok := w.carry(target, total); ok {
		w.point(field, loc)
		return loc, true
	}
	w.lost++
	return 0, false
}

// subIFD handles the offset of a sub-IFD stored in the field at offset field.
// An IFD that lies wholly inside the blob is walked in place. One that lies
// outside, or whose bytes cross the end of the blob (the Nikon Coolpix P7000
// declares 4721 bytes and its PreviewIFD ends at 4732), is carried whole to the
// end of the blob, the offset is set to the copy, and the copy is walked.
func (w *mnWalker) subIFD(field, depth int) {
	target := w.targetOf(int64(w.order.Uint32(w.buf[field:])))
	var n int64
	switch {
	case target >= 0 && target+2 <= int64(w.origLen):
		n = int64(w.order.Uint16(w.buf[target:]))
	case w.src != nil && target >= 0 && w.mnOff+target+2 <= int64(len(w.src)):
		n = int64(w.order.Uint16(w.src[w.mnOff+target:]))
	default:
		w.lost++
		return
	}
	span := 2 + 12*n + 4 // TIFF 6.0 §2: count, entries, next-IFD offset
	if target+span <= int64(w.origLen) {
		w.point(field, int(target))
		w.walkIFD(int(target), depth+1)
		return
	}
	if loc, ok := w.carry(target, span); ok {
		w.point(field, loc)
		w.walkIFD(loc, depth+1)
		return
	}
	w.lost++
}

// carry copies total bytes that lie at offset target of the MakerNote (outside
// it) from the source stream to the end of buf, and returns where they went.
func (w *mnWalker) carry(target, total int64) (int, bool) {
	if w.src == nil {
		return 0, false
	}
	pos := w.mnOff + target
	if pos < 0 || total > int64(len(w.src)) || pos > int64(len(w.src))-total {
		return 0, false
	}
	if total > int64(w.budget) {
		w.over = true
		return 0, false
	}
	key := [2]int64{pos, total}
	if loc, ok := w.carried[key]; ok {
		return loc, true
	}
	w.own()
	if len(w.buf)&1 == 1 { // TIFF 6.0 §2: values begin on a word boundary
		w.buf = append(w.buf, 0)
	}
	loc := len(w.buf)
	w.buf = append(w.buf, w.src[pos:pos+total]...)
	w.budget -= int(total)
	if w.carried == nil {
		w.carried = make(map[[2]int64]int, 4)
	}
	w.carried[key] = loc
	return loc, true
}

// point records that the pointer in the field at offset field must address
// the blob byte at offset target. A layout that counts from a point inside the
// blob is patched now, in the private copy; a TIFF-relative one is patched by
// finish, when the blob has a position.
func (w *mnWalker) point(field, target int) {
	if w.tiffRel {
		w.fixes = append(w.fixes, mnFix{field: field, target: target})
		return
	}
	v := target - w.base
	if v < 0 || int64(v) > 0xFFFFFFFF || w.order.Uint32(w.buf[field:]) == uint32(v) {
		return
	}
	w.own()
	w.order.PutUint32(w.buf[field:], uint32(v))
}
