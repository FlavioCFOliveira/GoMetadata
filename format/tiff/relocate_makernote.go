package tiff

// relocate_makernote.go — MakerNote value-offset rebasing for the standard
// TIFF relocation path (relocateTIFFFromParsed) and the CR2 marker insertion.
//
// Problem (#127, #310): a MakerNote IFD whose value offsets count from the
// start of the TIFF stream holds stale pointers once the relocator moves the
// MakerNote: every out-of-line value is read from the wrong place. ExifTool
// reports it as "Adjusted MakerNotes base by N" and Canon tags as invalid.
//
// Model (task #310). The MakerNote is copied verbatim and its out-of-line
// value-or-offset fields are rebased by the distance the MakerNote moved,
// when, and only when, its layout counts offsets from the TIFF header. The
// layout (IFD position, byte order, offset base) comes from
// exif.EXIF.MakerNoteLayout, which detects it exactly as Parse does
// (exif/makernote_detect.go, adapted from metadata-extractor
// ExifTiffHandler.processMakernote). TIFF-relative layouts include Canon,
// Sony, Panasonic, Casio, Samsung, DJI, Kyocera, Sigma, Minolta, Nikon
// Type 2 and Olympus "OLYMP\0"; layouts whose offsets count from a point
// inside the MakerNote (Nikon Type 3, Olympus "OLYMPUS\0", Fujifilm, Pentax,
// Apple, Ricoh, Sanyo) move unchanged.
//
// Alternative considered: keeping the MakerNote at its original absolute
// offset, as Apache Commons Imaging (TiffImageWriterLossless, "frozen"
// MakerNote field) and the Adobe XMP Toolkit (TIFF_FileWriter::
// UpdateMemByAppend, which appends changes and never moves existing data) do.
// Both rely on an update-in-place layout that leaves the original bytes
// where they were; this relocator rebuilds the stream (IFDs first, image
// data after), so the original offset of the MakerNote is generally occupied
// by other data. Rebasing is the model of writers that rebuild the stream
// (Exiv2 and ExifTool rewrite MakerNote offsets; studied, not copied).
//
// Limitation: a value the camera stored outside the MakerNote (Olympus
// C-720UZ) is not carried by the verbatim copy; its pointer is rebased like
// the others but the bytes it addresses are not the original value.
//
// Spec references:
//   - EXIF §4.6.5, tag 0x927C (MakerNote).
//   - TIFF 6.0 §2: IFD entry layout — val_or_off is absolute when total > 4 bytes.
//   - metadata-extractor ExifTiffHandler.processMakernote: offset base of each
//     layout (tiffHeaderOffset or makernoteOffset).

import (
	"encoding/binary"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// rebaseGenericMakerNote rebases the out-of-line value-or-offset fields of a
// TIFF-relative MakerNote that exif.Encode placed at a new absolute position
// in finalTIFF (see the model at the top of this file).
//
// Parameters:
//   - finalTIFF: the re-encoded TIFF output (produced by exif.Encode in step 9).
//   - e: the parsed EXIF (provides MakerNote, MakerNoteOffset and the Make
//     used for layout detection).
//   - order: the outer TIFF byte order.
//
// The function is a no-op when e has no MakerNote, when the layout is not
// TIFF-relative or not an IFD, when the MakerNote did not move, or when the
// MakerNote entry cannot be found in the re-encoded ExifIFD.
//
// #127, #310.
func rebaseGenericMakerNote(finalTIFF []byte, e *exif.EXIF, order binary.ByteOrder) {
	layout, ok := tiffRelativeMakerNote(e)
	if !ok || len(finalTIFF) < 8 {
		return
	}
	newMNAbs, found := encodedMakerNoteAbs(finalTIFF, order)
	// EXIF.MakerNoteOffset is the source position of the MakerNote value
	// (exif.Parse records the out-of-line offset of tag 0x927C).
	oldMNAbs := uint64(e.MakerNoteOffset)
	newAbs := uint64(newMNAbs) //nolint:gosec // G115: a non-negative buffer offset
	if !found || oldMNAbs == 0 || newAbs == oldMNAbs {
		return
	}
	rebaseMakerNoteIFD(finalTIFF, newMNAbs+layout.IFDOffset, layout.ByteOrder, int64(newAbs)-int64(oldMNAbs), oldMNAbs) //nolint:gosec // G115: both are offsets in a classic TIFF, below 2^32
	e.RebaseMakerNoteSubIFDs(finalTIFF, newMNAbs)                                                                       // task #370: the sub-IFDs of an Olympus "OLYMP\0" note
	fixMakerNoteFooter(finalTIFF, newMNAbs, len(e.MakerNote), layout.ByteOrder, oldMNAbs, newAbs)
}

// tiffRelativeMakerNote returns the layout of the MakerNote of e when it is a
// TIFF-relative IFD that rebaseGenericMakerNote handles, and false otherwise.
//
// #270 documented fail-safe deferral: the outer-container scan of
// rebaseGenericMakerNote (finalTIFF[4:] for the IFD0 offset, 12-byte entries
// in the Exif IFD) is classic-TIFF-only. A TIFF-relative MakerNote in a
// BigTIFF container has no known real-world occurrence (BigTIFF serves large
// scientific and aerial imagery, not camera files); declining is a safe no-op.
func tiffRelativeMakerNote(e *exif.EXIF) (exif.MakerNoteLayout, bool) {
	if e == nil || len(e.MakerNote) == 0 || e.ExifIFD == nil || e.BigTIFF {
		return exif.MakerNoteLayout{}, false
	}
	layout, ok := e.MakerNoteLayout()
	return layout, ok && layout.TIFFRelative
}

// encodedMakerNoteAbs returns the offset of the MakerNote value in the
// classic TIFF stream finalTIFF (IFD0 → Exif IFD → tag 0x927C), and false
// when it cannot be located or is stored inline.
func encodedMakerNoteAbs(finalTIFF []byte, order binary.ByteOrder) (int, bool) {
	ifd0Off := int(order.Uint32(finalTIFF[4:]))
	if ifd0Off+2 > len(finalTIFF) {
		return 0, false
	}
	exifIFDOff, found := scanIFDForTagValOrOff(finalTIFF, ifd0Off, uint16(exif.TagExifIFDPointer), order)
	if !found || uint64(exifIFDOff)+2 > uint64(len(finalTIFF)) {
		return 0, false
	}
	return findOOLEntryOffset(finalTIFF, int(exifIFDOff), uint16(exif.TagMakerNote), order)
}

// fixMakerNoteFooter updates the TIFF footer that Canon writes at the end of
// its MakerNotes: the 8 bytes "II*\0" or "MM\0*" (a TIFF header in the
// MakerNote byte order) followed by the 32-bit TIFF offset of the MakerNote
// itself. Readers compare that offset with the MakerNote's actual position to
// detect a MakerNote moved without rebasing (ExifTool reports a footer that
// disagrees as "Canon maker note footer may be invalid"). The MakerNote of n
// bytes is at mnAbs in buf. The footer is rewritten to newAbs only when it is
// present and holds oldAbs, the position the MakerNote had, so a footer that
// was already wrong or a MakerNote without one is left untouched (task #310).
func fixMakerNoteFooter(buf []byte, mnAbs, n int, order binary.ByteOrder, oldAbs, newAbs uint64) {
	const footerLen = 8
	if n <= footerLen || mnAbs < 0 || mnAbs+n > len(buf) {
		return
	}
	f := buf[mnAbs+n-footerLen : mnAbs+n]
	if !isTIFFHeaderMark(f, order) || uint64(order.Uint32(f[4:])) != oldAbs || newAbs > math.MaxUint32 {
		return
	}
	order.PutUint32(f[4:], uint32(newAbs))
}

// isTIFFHeaderMark reports whether b starts with the TIFF header mark of
// order: "II*\0" (little-endian) or "MM\0*" (big-endian), TIFF 6.0 §2.
func isTIFFHeaderMark(b []byte, order binary.ByteOrder) bool {
	if len(b) < 4 {
		return false
	}
	if order == binary.LittleEndian {
		return string(b[:4]) == "II*\x00"
	}
	return string(b[:4]) == "MM\x00*"
}

// rebaseMakerNoteIFD adds delta to the value-or-offset field of every
// out-of-line entry (total size > 4 bytes, TIFF 6.0 §2) of the classic
// MakerNote IFD at ifdStart in buf, read in order. A field whose value is
// below minOld (it cannot address the MakerNote's own data: a zero or stale
// pointer), or whose rebased value would leave buf, is left unchanged.
//
// #127 upper-bound note: the source pointer is NOT limited to the declared
// MakerNote size, because some Sony and Olympus "OLYMP" bodies store values
// just past it; only the rebased value is bounds-checked.
func rebaseMakerNoteIFD(buf []byte, ifdStart int, order binary.ByteOrder, delta int64, minOld uint64) {
	if ifdStart < 0 || ifdStart+2 > len(buf) {
		return
	}
	count := int(order.Uint16(buf[ifdStart:]))
	pos := ifdStart + 2
	for i := range count {
		ep := pos + i*12
		if ep+12 > len(buf) {
			break
		}
		sz := typeSize(order.Uint16(buf[ep+2:]))
		total := uint64(sz) * uint64(order.Uint32(buf[ep+4:]))
		if sz == 0 || total <= 4 {
			continue // unknown type or inline value: no pointer
		}
		old := uint64(order.Uint32(buf[ep+8:]))
		if old < minOld {
			continue
		}
		v := int64(old) + delta //nolint:gosec // G115: old < 2^32 and |delta| < 2^32
		if v < 0 || uint64(v)+total > uint64(len(buf)) {
			continue
		}
		order.PutUint32(buf[ep+8:], uint32(v)) //nolint:gosec // G115: 0 <= v < len(buf), a classic TIFF offset
	}
}
