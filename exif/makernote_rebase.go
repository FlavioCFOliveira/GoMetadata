package exif

// makernote_rebase.go — keep a MakerNote valid when Encode moves it (tasks
// #317 and #318).
//
// Problem. The MakerNote (tag 0x927C, UNDEFINED; Exif 3.1 §4.6.6.4.1, p. 49:
// "The contents are up to the manufacturer") is an opaque blob that most
// cameras fill with an IFD. Encode copies the blob to a new position in the
// stream, so two things break for the camera's IFD:
//
//  1. Its value-or-offset fields that count from the TIFF header (Canon, Sony,
//     Panasonic, Casio, Olympus "OLYMP\0", Nikon Type 2, and every layout
//     MakerNoteLayout reports with TIFFRelative; TIFF 6.0 §2: an offset counts
//     from the start of the TIFF header) point at the old position.
//  2. Values and sub-IFDs that the IFD addresses OUTSIDE the declared byte
//     count of the blob (Olympus C-720UZ keeps its values after the Exif
//     data, the Nikon Coolpix P7000 keeps its 37 KB preview image 17 KB past
//     its 4.7 KB blob) are not part of the copy.
//
// Method (adapted in concept from ExifTool RebuildMakerNotes, WriteExif.pl,
// and Exiv2 TiffIfdMakernote::doWrite, which re-serialise the MakerNote at its
// new position with a fixup; studied, no code copied): the MakerNote IFD is
// walked once in the source stream (makernote_walk.go). Every out-of-line
// value that lies inside the blob keeps its place inside the blob; every one
// that lies outside is copied from the source stream after the end of the
// blob, so the blob grows to hold all the data its IFD references, and
// nothing else. Then every pointer is set to the position of its data: for a
// layout counting from the TIFF header, after the blob has a position in the
// output (finish); for a layout counting from a point inside the blob, in the
// copy of the blob. Canon's footer (the TIFF offset of the MakerNote itself)
// follows the blob.
//
// The data copied is bounded by the size of the source stream and by
// maxMakerNoteCarry; a pointer that cannot be resolved is left as it is.
// Only classic TIFF is handled: a MakerNote in a BigTIFF has no known
// real-world occurrence (task #270).

import (
	"bytes"
	"encoding/binary"
	"math"
)

// EncodeRebased encodes e like Encode and, besides, makes the MakerNote valid
// at the position the encoded stream gives it: the value offsets of a
// MakerNote layout that counts from the TIFF header are set for the new
// position, the data that its IFD addresses outside the declared MakerNote
// bytes is carried, and Canon's footer follows (Exif 3.1 §4.6.6.4.1, p. 49,
// tag 0x927C; TIFF 6.0 §2). It is the encoder for a container that holds the
// stream on its own (JPEG APP1, PNG, WebP, HEIF/AVIF).
//
// src is the stream e was parsed from (the buffer passed to Parse). It is
// where the data outside the MakerNote is read from; a nil src, or one that
// no longer matches the MakerNote of e, limits the repair to the offsets of
// data inside the MakerNote. The result equals Encode(e) when e has no
// MakerNote, or one that moves unchanged (a layout that counts its offsets from
// a point inside the MakerNote and addresses nothing outside it).
//
// Encode itself copies the MakerNote verbatim, which is what the TIFF-family
// writers of this library need: they place the MakerNote and then fix it for
// the position they chose.
//
// The stream also carries the private IFDs that IFD0 points to (tag 0x8290
// KodakIFD and the other pointers that are certain to address an IFD, see
// private_ifd.go, tasks #362 and #370), with the values they address, after the
// end of the stream. A MakerNote offset that addresses data outside the
// stream (Olympus PreviewImageStart) is copied as it is: ExternalOffsets finds
// it for the writer of the container, which knows where the data lands. The
// strips of an uncompressed thumbnail are written by Encode itself when Parse
// read them (CarryThumbnailStrips, task #368).
//
// The data carried from src, for the MakerNote and the private IFDs together,
// is bounded by the length of src (16 MiB at most); a stream whose private IFDs
// address more makes EncodeRebased return ErrCarriedDataTooLarge instead of
// writing pointers that no longer address their data.
func EncodeRebased(e *EXIF, src []byte) ([]byte, error) {
	// One allowance for everything carried from src, the MakerNote data and
	// every private IFD together: valid data are disjoint ranges of src, so a
	// valid stream never needs more than len(src) (security audit F-06b: 40
	// private IFDs, each with its own 16 MiB allowance, grew a 15 MiB stream to
	// 639 MiB).
	carry := min(len(src), maxMakerNoteCarry)
	plan := planMakerNote(e, src, &carry)
	out, _, err := serialise(nil, e, false, plan)
	if err != nil {
		return nil, err
	}
	return appendPrivateIFD(out, e, src, &carry) // the private IFDs of IFD0, if any (private_ifd.go)
}

// mnFix is one pointer of a TIFF-relative MakerNote IFD: the 4-byte field at
// offset field of the blob must address the blob byte at offset target.
type mnFix struct {
	field  int
	target int
}

// mnPlan is the repair of the MakerNote of one EXIF (see planMakerNote).
type mnPlan struct {
	order   binary.ByteOrder // byte order of the MakerNote IFD
	blob    []byte           // the MakerNote to write: the original, or a copy that has been extended or patched
	changed bool             // blob is such a copy
	origLen int              // length of the original MakerNote
	tiffRel bool
	fixes   []mnFix // pointers to set once the position is known (TIFF-relative layouts)
}

// makerNoteOf returns the MakerNote bytes of e (the value of the Exif IFD
// entry, which is what Encode writes) and the trimmed Make, or nil when e has
// no MakerNote that holds a pointer (absent, inline, BigTIFF).
func makerNoteOf(e *EXIF) (mn, mk []byte) {
	if e == nil || e.BigTIFF || e.ExifIFD == nil {
		return nil, nil
	}
	mn = e.MakerNote
	if ent := e.ExifIFD.Get(TagMakerNote); ent != nil {
		mn = ent.Value
	}
	if len(mn) <= 4 { // an inline value holds no pointer
		return nil, nil
	}
	if e.IFD0 != nil {
		if me := e.IFD0.Get(TagMake); me != nil {
			mk = trimMake(me.Value)
		}
	}
	return mn, mk
}

// makerNoteSourceOffset returns where e says its MakerNote was in the source
// stream: the full offset of a classic TIFF fits 32 bits.
func makerNoteSourceOffset(e *EXIF) uint64 {
	if e.MakerNoteOffset64 == 0 || e.MakerNoteOffset64 > math.MaxUint32 {
		return uint64(e.MakerNoteOffset)
	}
	return e.MakerNoteOffset64
}

// holdsMakerNote reports whether src still holds mn at offset off: the caller
// may have replaced the MakerNote of the EXIF since it was parsed.
func holdsMakerNote(src []byte, off uint64, mn []byte) bool {
	return src != nil && off+uint64(len(mn)) <= uint64(len(src)) && bytes.Equal(src[off:off+uint64(len(mn))], mn)
}

// resolveMakerNote returns the layout of the MakerNote mn of e (the Make of
// IFD0 trimmed is mk) with the offset base decided as the repair of a moved
// MakerNote decides it (offsetsCountFromTIFF), and the position of the
// MakerNote in the source stream. ok is false when mn holds no IFD this package
// reads. The editor of MakerNote values (makernote_edit.go) uses the same
// decision, so that an edit and the write that follows read the offsets alike.
func resolveMakerNote(e *EXIF, mn, mk []byte) (lay MakerNoteLayout, mnOff uint64, ok bool) {
	mnOff = makerNoteSourceOffset(e)
	lay, ok = resolveLayout(detectLayout(mn, mk), mn, e.ifd0ByteOrder(), mnOff)
	if !ok {
		return lay, 0, false
	}
	return lay, mnOff, true
}

// resolveLayout is the one place that decides how the offsets of the MakerNote
// mn of the detected layout l, at offset mnOff of the source stream, are based:
// Parse (resolvedBase), the repair of a moved MakerNote and the MakerNote
// editor all read the offsets of a note alike (task #412). ok is false when mn
// holds no IFD this package reads. An unknown position (mnOff 0) keeps the
// table.
func resolveLayout(l mnLayout, mn []byte, order binary.ByteOrder, mnOff uint64) (lay MakerNoteLayout, ok bool) {
	lay, ok = layoutOf(l, mn, order)
	if !ok {
		return lay, false
	}
	if lay.layout != mnNikon3 && mnOff > 0 {
		lay.TIFFRelative = dataBaseIsTIFF(mn, lay, int64(mnOff)) //nolint:gosec // G115: a classic TIFF offset, below 2^32
	}
	return lay, true
}

// planMakerNote walks the MakerNote of e and returns the repair, or nil when
// the MakerNote needs none (absent, inline, not an IFD, BigTIFF, or a layout
// that moves unchanged and addresses nothing outside itself). src is the
// stream e was parsed from, or nil.
//
// carry is the allowance of bytes that may be copied from src; the walk spends
// from it.
func planMakerNote(e *EXIF, src []byte, carry *int) *mnPlan {
	mn, mk := makerNoteOf(e)
	if mn == nil {
		return nil
	}
	lay, mnOff, ok := resolveMakerNote(e, mn, mk)
	if !ok {
		return nil
	}
	if lay.TIFFRelative && mnOff == 0 {
		return nil // the source position is unknown: no delta to apply
	}
	if lay.layout == mnKyocera {
		return nil // its offsets count from their own entries: it moves as it is (makernote_kyocera_base.go)
	}
	var w mnWalker
	w.init(mn, lay, int64(mnOff)) //nolint:gosec // G115: a classic TIFF offset, below 2^32
	if holdsMakerNote(src, mnOff, mn) {
		w.src = src // the data to carry
	}
	w.budget = *carry // spent only when w.src can be read, see mnWalker.carry
	w.walkIFD(lay.IFDOffset, 0)
	*carry = w.budget
	if !w.tiffRel && !w.copied {
		return nil // nothing changed: the MakerNote moves as it is
	}
	return &mnPlan{
		order:   lay.ByteOrder,
		blob:    w.buf,
		changed: w.copied,
		origLen: len(mn),
		tiffRel: lay.TIFFRelative,
		fixes:   w.fixes,
	}
}

// install gives the MakerNote entry of the Exif IFD entries the blob to write.
// It does nothing on a nil plan.
func (p *mnPlan) install(exifIFDEntries []IFDEntry) {
	if p == nil || !p.changed {
		return
	}
	i, ok := binarySearchTag(exifIFDEntries, TagMakerNote)
	if !ok {
		return
	}
	exifIFDEntries[i].Value = p.blob
	exifIFDEntries[i].Count = uint32(len(p.blob)) //nolint:gosec // G115: bounded by maxMakerNoteCarry and the source size
}

// binarySearchTag returns the index of the entry of tag in the sorted entries.
func binarySearchTag(entries []IFDEntry, tag TagID) (int, bool) {
	lo, hi := 0, len(entries)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if entries[m].Tag < tag {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo, lo < len(entries) && entries[lo].Tag == tag
}

// finish sets the TIFF-relative pointers of the MakerNote in out, the stream
// that serialise wrote, for the position the MakerNote has in it, and fixes
// Canon's footer. It does nothing on a nil plan.
func (p *mnPlan) finish(out []byte, order binary.ByteOrder) {
	if p == nil || !p.tiffRel || len(out) < 8 {
		return
	}
	mnAbs, ok := findMakerNoteAbs(out, order)
	if !ok || mnAbs+len(p.blob) > len(out) {
		return
	}
	for _, f := range p.fixes {
		v := uint64(mnAbs) + uint64(f.target) //nolint:gosec // G115: non-negative offsets
		if v > math.MaxUint32 || f.field+4 > len(p.blob) {
			continue
		}
		p.order.PutUint32(out[mnAbs+f.field:], uint32(v))
	}
	fixMakerNoteFooter(out, mnAbs, p.origLen, p.order, uint64(mnAbs)) //nolint:gosec // G115: non-negative offset
}

// fixMakerNoteFooter updates the TIFF footer that Canon writes at the end of
// its MakerNotes: the 8 bytes "II*\0" or "MM\0*" (a TIFF header in the
// MakerNote byte order) followed by the 32-bit TIFF offset of the MakerNote
// itself. Readers compare that offset with the MakerNote's actual position to
// detect a MakerNote moved without rebasing (ExifTool reports a footer that
// disagrees as "Adjusted MakerNotes base by" or "Canon maker note footer may
// be invalid"). The MakerNote of n bytes is at mnAbs in buf, and the pointers
// of its IFD were just set for that position, so the footer is set to newAbs
// (the position) whatever it held: a footer that was already out of step is
// brought in step with them. A MakerNote without the footer is left alone
// (task #310).
func fixMakerNoteFooter(buf []byte, mnAbs, n int, order binary.ByteOrder, newAbs uint64) {
	const footerLen = 8
	if n <= footerLen || mnAbs < 0 || mnAbs+n > len(buf) {
		return
	}
	f := buf[mnAbs+n-footerLen : mnAbs+n]
	if !isTIFFHeaderMark(f, order) || newAbs > math.MaxUint32 {
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

// findMakerNoteAbs returns the offset of the MakerNote value in the classic
// TIFF stream out (IFD0 -> Exif IFD -> tag 0x927C), and false when it cannot
// be found or is stored inline.
func findMakerNoteAbs(out []byte, order binary.ByteOrder) (int, bool) {
	exifOff, ok := findEntryField(out, int(order.Uint32(out[4:])), TagExifIFDPointer, order)
	if !ok {
		return 0, false
	}
	mnOff, ok := findEntryField(out, int(exifOff), TagMakerNote, order)
	return int(mnOff), ok
}

// findEntryField returns the value-or-offset field of the entry of tag in the
// classic IFD at offset ifd of out.
func findEntryField(out []byte, ifd int, tag TagID, order binary.ByteOrder) (uint32, bool) {
	if ifd < 0 || ifd+2 > len(out) {
		return 0, false
	}
	n := int(order.Uint16(out[ifd:]))
	for i := range n {
		ep := ifd + 2 + i*12
		if ep+12 > len(out) {
			break
		}
		if TagID(order.Uint16(out[ep:])) == tag {
			return order.Uint32(out[ep+8:]), true
		}
	}
	return 0, false
}

// RebaseMakerNoteSubIFDs sets, in the classic TIFF stream tiff, the pointers
// that lie inside the sub-IFDs of the MakerNote of e, for a MakerNote whose
// offsets count from the TIFF header and that holds the sub-IFDs the walk of
// this package knows (the Olympus "OLYMP\0" layout: Equipment, CameraSettings,
// RawDevelopment, ImageProcessing and FocusInfo, stored as UNDEFINED values by
// the E-1 and the compacts of its generation, or as offsets by later bodies;
// ExifTool Olympus.pm, tags 0x2010-0x2050 and 0x3000; task #370). mnAt is the
// position in tiff of the MakerNote, copied there byte for byte from the stream
// e was parsed from; the pointers address the data inside it at its new
// position (TIFF 6.0 §2: a value offset counts from the TIFF header).
//
// It is for the writers of the TIFF family (format/tiff), which copy the
// MakerNote and rebase the pointers of its top IFD by the distance it moved:
// the pointers of the top IFD that address out-of-line values (more than 4
// bytes) are left to them, and the offsets of the sub-IFDs (inline) and every
// pointer deeper are set here. Data outside the MakerNote is not carried, as
// for the top IFD. It reports whether tiff holds such a MakerNote and a pointer
// was set. tiff is modified only at the pointers; e is not modified.
func (e *EXIF) RebaseMakerNoteSubIFDs(tiff []byte, mnAt int) bool {
	lay, mnOff, ok := olympTIFFRelative(e)
	if !ok || mnAt < 0 || mnAt+len(e.MakerNote) > len(tiff) {
		return false
	}
	var w mnWalker
	w.init(e.MakerNote, lay, int64(mnOff)) //nolint:gosec // G115: the MakerNote lies inside the source stream
	w.walkIFD(lay.IFDOffset, 0)
	set := false
	for _, f := range w.fixes {
		if !isTopOOLField(e.MakerNote, lay, f.field) && setPointer(tiff, mnAt, f, lay.ByteOrder) {
			set = true
		}
	}
	return set
}

// olympTIFFRelative returns the layout and the source position of the MakerNote
// of e when it is an Olympus "OLYMP\0" note, whose offsets count from the TIFF
// header. The prefix test comes first: it is all that most MakerNotes need, and
// detecting the layout is the dearer part.
func olympTIFFRelative(e *EXIF) (lay MakerNoteLayout, mnOff uint64, ok bool) {
	if e == nil || e.BigTIFF || !bytes.HasPrefix(e.MakerNote, []byte("OLYMP\x00")) {
		return lay, 0, false
	}
	lay, ok = e.MakerNoteLayout()
	mnOff = makerNoteSourceOffset(e)
	return lay, mnOff, ok && lay.TIFFRelative && lay.layout == mnOlympusOld && mnOff != 0
}

// setPointer sets the 4-byte pointer of fix f of the MakerNote at mnAt of tiff to
// address the MakerNote byte f.target; false when the field or the value does not fit.
func setPointer(tiff []byte, mnAt int, f mnFix, order binary.ByteOrder) bool {
	pos := int64(mnAt) + int64(f.target)
	if f.field < 0 || mnAt+f.field+4 > len(tiff) || pos > math.MaxUint32 {
		return false
	}
	order.PutUint32(tiff[mnAt+f.field:], uint32(pos)) //nolint:gosec // G115: checked against MaxUint32 above
	return true
}

// isTopOOLField reports whether the 4-byte field at offset field of mn is the
// value-or-offset field of an out-of-line entry of the top IFD of the
// MakerNote.
func isTopOOLField(mn []byte, lay MakerNoteLayout, field int) bool {
	n, ok := ifdEntryCount(mn, lay.IFDOffset, lay.ByteOrder)
	if !ok {
		return false
	}
	d := field - (lay.IFDOffset + 2 + 8) // entry i has its field at IFDOffset+2+12*i+8
	if d < 0 || d%12 != 0 || d/12 >= n {
		return false
	}
	return readMNEntry(mn, lay.IFDOffset+2+d, lay.ByteOrder).total > 4
}

// CarryMakerNoteTail extends the MakerNote of e, which was parsed from src, with
// the data that its IFD addresses after the end of the bytes it declares, and
// reports whether it did. It is for the Samsung SRW files of the NX series:
// the PreviewIFD of the note and the preview JPEG behind it lie after the
// declared length, and every offset counts from the first MakerNote byte, so a
// writer that moves the note as declared drops the preview and leaves the
// PreviewIFD pointing at nothing (task #408). The note keeps its offsets: the
// tail is appended, so what addressed it still does. Other layouts are left to
// their own writers. src must be the buffer e was parsed from; e is modified only
// when the result is true (e.MakerNote and the MakerNote entry of the Exif IFD).
func (e *EXIF) CarryMakerNoteTail(src []byte) bool {
	mn, mk := makerNoteOf(e)
	if mn == nil {
		return false
	}
	if lay, _, ok := resolveMakerNote(e, mn, mk); !ok || lay.layout != mnSamsung || lay.TIFFRelative {
		return false
	}
	carry := min(len(src), maxMakerNoteCarry)
	p := planMakerNote(e, src, &carry)
	if p == nil || !p.changed {
		return false
	}
	ent := e.ExifIFD.Get(TagMakerNote)
	if ent == nil {
		return false
	}
	ent.Value = p.blob
	ent.Count = uint32(len(p.blob)) //nolint:gosec // G115: bounded by maxMakerNoteCarry and the source size
	e.MakerNote = p.blob
	return true
}
