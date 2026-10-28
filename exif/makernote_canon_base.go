package exif

// makernote_canon_base.go — read a Canon MakerNote whose offset base is shifted
// (task #407).
//
// Problem. The value offsets of a Canon MakerNote count from the start of the
// TIFF stream (metadata-extractor, ExifTiffHandler.processMakernote). Software
// that rewrites the Exif block and moves the MakerNote without updating those
// offsets leaves them pointing at the wrong bytes: the Canon EOS REBEL T2i and
// T3i files of the corpus were re-saved this way, 164 bytes off, so every
// out-of-line value read from the TIFF stream is garbage (zeros). Canon ends
// its notes with a footer, the TIFF header mark "II*\0" or "MM\0*" followed by
// the 32-bit TIFF offset the MakerNote had when the camera wrote it; ExifTool
// reads that footer, finds that the note moved, and corrects the base
// (MakerNotes.pm, FixBase: "Adjusted MakerNotes base by -164"; studied, no
// code copied; the corrected origin is the one pointerOrigin computes for the
// write path, tasks #310 and #317).
//
// Method. The corrected origin is accepted only when it explains the layout
// better than the position of the note does: under it, every out-of-line value
// of the IFD lies inside the MakerNote, after the entry table and before the
// footer (Canon writes its values contiguously behind the IFD), and under the
// position of the note some does not. The IFD is then read from a private copy
// of the note whose pointers count from its first byte; the bytes of the note
// itself are not changed, and an edit or a write sees the original.
//
// Scope: Canon only. The heuristics of the other manufacturers are not part of
// this task.

import (
	"bytes"
	"encoding/binary"
)

// canonFooterLen is the length of the Canon MakerNote footer: the TIFF header
// mark (4 bytes) and the TIFF offset of the MakerNote (4 bytes).
const canonFooterLen = 8

// canonCorrectedOrigin returns the TIFF offset that the pointers of the Canon
// MakerNote mn (at offset mnOff of the TIFF stream, in the byte order the IFD
// count of mn selects, starting from parent) were written for, and true, when
// that is not mnOff and the note is consistent under it (see the file comment).
// It returns false for every note whose base is right, which is every note
// whose footer holds mnOff or that has no footer. It does not allocate.
func canonCorrectedOrigin(mn []byte, parent binary.ByteOrder, mnOff uint64) (origin int64, order binary.ByteOrder, ok bool) {
	if len(mn) < 2+canonFooterLen+4 || mnOff == 0 || mnOff > 0xFFFF_FFFF {
		return 0, nil, false
	}
	order, _ = countOrder(mn[0], mn[1], parent)
	f := mn[len(mn)-canonFooterLen:]
	// Exif 3.1 does not define the footer: it is the convention of the Canon
	// cameras (ExifTool Canon.pm, "Canon maker note footer").
	if !isTIFFHeaderMark(f, order) || uint64(order.Uint32(f[4:])) == mnOff {
		return 0, nil, false
	}
	lay := MakerNoteLayout{ByteOrder: order, TIFFRelative: true, layout: mnCanon}
	origin = pointerOrigin(mn, lay, int64(mnOff))
	if origin == int64(mnOff) {
		return 0, nil, false
	}
	// The note must be explained by the corrected origin and not by its
	// position: under the true origin the values follow the table and end at
	// the footer, under the wrong one some start inside the table, or end in or
	// behind the footer. A count of values inside the note would not tell
	// apart a shift of a few bytes.
	if n, all := canonValuesFit(mn, lay, origin); n == 0 || !all {
		return 0, nil, false
	}
	if _, all := canonValuesFit(mn, lay, int64(mnOff)); all {
		return 0, nil, false
	}
	return origin, order, true
}

// canonValuesFit counts the out-of-line values of the Canon note mn and reports
// whether every one of them lies after the entry table and before the footer
// when its pointer counts from the TIFF header at -origin relative to the note.
// A note whose entry table is cut reports no value.
func canonValuesFit(mn []byte, lay MakerNoteLayout, origin int64) (n int, all bool) {
	count, ok := ifdEntryCount(mn, 0, lay.ByteOrder)
	if !ok || int(lay.ByteOrder.Uint16(mn)) != count { // the entry table must be whole
		return 0, false
	}
	tableEnd := int64(2 + 12*count + 4) // TIFF 6.0 §2: count, entries, next-IFD offset
	valuesEnd := int64(len(mn) - canonFooterLen)
	all = true
	eachOOL(mn, lay, func(ptr, size int64) {
		n++
		if t := ptr - origin; t < tableEnd || t+size > valuesEnd {
			all = false
		}
	})
	return n, all
}

// canonRebasedCopy returns a copy of the Canon MakerNote mn in which the
// pointers of the out-of-line values of the IFD count from the first byte of
// the note, when its offset base is shifted (canonCorrectedOrigin); false when
// the note needs no correction. mn is not modified.
func canonRebasedCopy(mn []byte, parent binary.ByteOrder, mnOff uint64) ([]byte, bool) {
	origin, order, ok := canonCorrectedOrigin(mn, parent, mnOff)
	if !ok {
		return nil, false
	}
	c := bytes.Clone(mn)
	n, _ := ifdEntryCount(c, 0, order)
	for i := range n {
		e := readMNEntry(c, 2+i*12, order)
		if e.total > 4 {
			// canonCorrectedOrigin proved ptr-origin lies inside the note.
			order.PutUint32(c[e.field:], uint32(int64(order.Uint32(c[e.field:]))-origin)) //nolint:gosec // G115: 0 <= ptr-origin < len(mn)
		}
	}
	return c, true
}
