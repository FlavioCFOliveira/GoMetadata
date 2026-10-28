package exif

// makernote_kyocera_base.go — read a Kyocera MakerNote (task #412).
//
// The value offset of an entry of a Kyocera MakerNote counts from the position
// of that entry, not from the TIFF header or from the MakerNote: in the corpus
// file "Kyocera FC-S3.jpg" the first entry, at byte 24 of the MakerNote, holds
// the offset 24 and its value starts at byte 48, and the second entry, at byte
// 36, holds the offset 55172 and its value starts at byte 55208. ExifTool reads
// the note so (MakerNotes.pm, MakerNoteKyocera: Start => $valuePtr + 22, Base =>
// $start + 2, EntryBased => 1; Exif.pm ProcessExif: "$valuePtr += $entry");
// studied, no code copied. Neither of the bases the other layouts use (the TIFF
// header, the first MakerNote byte) finds those values: the former reads the
// first entry as the value of the tag, the latter reads bytes that are not the
// values.
//
// The note is read from a private copy whose offsets count from its first byte;
// the bytes of the note itself are not changed. A note that moves keeps its
// offsets valid, as they are relative to the entries, so a writer copies it as
// it is and the editor of MakerNote values declines it (makernote_edit.go).

import "encoding/binary"

// kyoceraIFDOffset is the offset of the IFD in a Kyocera MakerNote: the
// "KYOCERA" header takes 22 bytes (ExifTool MakerNotes.pm, MakerNoteKyocera).
const kyoceraIFDOffset = 22

// kyoceraRebasedCopy returns a copy of the Kyocera MakerNote mn in which the
// offset of every out-of-line value counts from the first byte of the note
// (the offset of the entry is added to it), and true; false when the note has no
// out-of-line value, which needs no copy. mn is not modified.
func kyoceraRebasedCopy(mn []byte, parent binary.ByteOrder) ([]byte, bool) {
	if len(mn) < kyoceraIFDOffset+2 {
		return nil, false
	}
	order, _ := countOrder(mn[kyoceraIFDOffset], mn[kyoceraIFDOffset+1], parent)
	n, ok := ifdEntryCount(mn, kyoceraIFDOffset, order)
	if !ok {
		return nil, false
	}
	var c []byte
	for i := range n {
		ep := kyoceraIFDOffset + 2 + i*12
		e := readMNEntry(mn, ep, order)
		if e.total <= 4 {
			continue
		}
		if c == nil {
			c = append([]byte(nil), mn...)
		}
		// An offset that no longer fits 32 bits addresses nothing in a note of
		// at most 4 GiB: it becomes the largest one, which the IFD reader drops.
		v := min(int64(order.Uint32(mn[e.field:]))+int64(ep), 0xFFFF_FFFF)
		order.PutUint32(c[e.field:], uint32(v)) //nolint:gosec // G115: at most 0xFFFFFFFF
	}
	return c, c != nil
}
