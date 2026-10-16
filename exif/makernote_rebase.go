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
// The stream also carries the private IFD that IFD0 tag 0x8290 (KodakIFD)
// points to, with the values it addresses, after the end of the stream
// (private_ifd.go, task #362). A MakerNote offset that addresses data outside
// the stream (Olympus PreviewImageStart) is copied as it is: ExternalOffsets
// finds it for the writer of the container, which knows where the data lands.
func EncodeRebased(e *EXIF, src []byte) ([]byte, error) {
	plan := planMakerNote(e, src)
	out, _, err := serialise(nil, e, false, plan)
	if err != nil {
		return nil, err
	}
	return appendPrivateIFD(out, e, src), nil // the private IFD of IFD0, if any (private_ifd.go)
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

// planMakerNote walks the MakerNote of e and returns the repair, or nil when
// the MakerNote needs none (absent, inline, not an IFD, BigTIFF, or a layout
// that moves unchanged and addresses nothing outside itself). src is the
// stream e was parsed from, or nil.
func planMakerNote(e *EXIF, src []byte) *mnPlan {
	mn, mk := makerNoteOf(e)
	if mn == nil {
		return nil
	}
	lay, ok := makerNoteLayoutOf(mn, mk, e.ifd0ByteOrder())
	if !ok {
		return nil
	}
	mnOff := makerNoteSourceOffset(e)
	if lay.layout != mnNikon3 && mnOff > 0 {
		lay.TIFFRelative = offsetsCountFromTIFF(mn, lay, int64(mnOff)) //nolint:gosec // G115: a classic TIFF offset, below 2^32
	}
	if lay.TIFFRelative && mnOff == 0 {
		return nil // the source position is unknown: no delta to apply
	}
	var w mnWalker
	w.init(mn, lay, int64(mnOff)) //nolint:gosec // G115: a classic TIFF offset, below 2^32
	if holdsMakerNote(src, mnOff, mn) {
		w.src = src // the data to carry
		w.budget = min(len(src), maxMakerNoteCarry)
	}
	w.walkIFD(lay.IFDOffset, 0)
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
