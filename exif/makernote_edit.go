package exif

// makernote_edit.go — delete a MakerNote tag, replace its value with one of the
// same size, and replace it with one of another size (tasks #398 and #399).
//
// Why the MakerNote is edited as bytes. The MakerNote (tag 0x927C, UNDEFINED;
// Exif 3.1 §4.6.6.4.1, p. 49: "The contents are up to the manufacturer") is an
// opaque blob, and Encode copies it verbatim. Its IFD has entries that are
// 12 bytes long (TIFF 6.0 §2), values of more than 4 bytes behind pointers,
// and in several layouts structures of the camera that hold their own
// pointers (a Canon footer, Nikon and Olympus sub-IFDs). Rebuilding the note
// from its parsed entries would have to know every one of those structures;
// a reader that finds one it does not know in the rebuilt note loses data
// silently. The edit therefore changes the bytes of the note in place and moves
// nothing that exists:
//
//   - Delete removes the entry from the entry table, closing the gap the entry
//     leaves (the entries after it move up by 12 bytes) and zeroing the value
//     it addressed. Every pointer of the note still addresses what it did: the
//     values stay where they are and the 12 bytes the table gave up stay as a
//     zeroed gap in front of the first value (TIFF 6.0 §2 does not require
//     values to follow the table).
//   - A replacement of the same size overwrites the value where it is.
//   - A smaller replacement is written where the old value was and the rest is
//     zeroed; a larger one is added at the end of the note (in front of Canon's
//     footer) and the pointer of its entry is set to it; the old value is
//     zeroed. Nothing else in the note moves.
//
// References, studied and not copied (GPL for Exiv2 and ExifTool; Apache-2.0
// for the Adobe XMP Toolkit, whose code is not used either). The rule that a
// value which fits the space of the old one is written where the old one was is
// that of the non-intrusive writer of Exiv2 (tiffvisitor_int.cpp,
// TiffEncoder::encodeOffsetEntry and encodeDataEntry: the new bytes are copied
// into the existing area and the rest is zero-filled). Exiv2 turns to an
// intrusive rewrite of the directory (setDirty) when the value does not fit or
// a tag is deleted; this package does not rewrite the MakerNote, as that needs
// knowledge of every structure inside it, and adds the larger value at the end
// instead, which is the model of the Adobe XMP Toolkit (TIFF_FileWriter::
// UpdateMemByAppend: changes are appended and existing data never moves) and of
// the "frozen" MakerNote of Apache Commons Imaging. The gap that a delete leaves
// in front of the first value is what ExifTool measures in MakerNotes.pm
// (FixBase: the distance between the end of the IFD and the first value is
// normal from 0 to 16 bytes, even; beyond it ExifTool reports "Possibly
// incorrect maker notes offsets" and, for the layouts that set FixBase, adjusts
// the base); the corpus files whose first value is removed or moved show the
// minor warning, and every tag is still read correctly.
//
// An edit is refused, with ErrMakerNoteNotEditable, when the layout of the note
// is not one whose offset base is known (a layout that no signature or Make
// selects, or the binary layouts of Kodak and Reconyx, which hold no IFD), when
// the position of a TIFF-relative note in its source is unknown, or when the
// note addresses data outside its own bytes (Olympus C-720UZ, Nikon Coolpix
// P7000): EncodeRebased carries that data from the source stream only while
// the note is the one the stream holds, and an edited note is not, so the
// data would be lost. Every edit is checked before it is accepted: the entries
// that a reader finds in the edited note must be those of the note before
// the edit, less the deleted entries and with the replaced values, the layout
// and the origin of the pointers must be the ones the note had, and no pointer
// may leave the note. A note that fails the check is not changed.
//
// EXIF.MakerNote and the MakerNote entry of the Exif IFD are replaced by the
// edited copy: the bytes the parsed EXIF aliases (the buffer given to Parse)
// are never written to.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// maxMakerNoteValue bounds the size of a value written into a MakerNote: far
// above any camera value (the largest are preview images of a few hundred
// kilobytes) and well under the 32-bit pointers of the layouts.
const maxMakerNoteValue = 16 << 20

// MakerNoteEditOption modifies SetMakerNoteTag.
type MakerNoteEditOption func(*makerNoteEditConfig)

type makerNoteEditConfig struct {
	fixedSize bool
}

// FixedSize makes SetMakerNoteTag accept only a value of exactly the size of
// the one it replaces, so that nothing in the MakerNote moves or grows; any
// other size returns ErrMakerNoteSizeChange. It is what a writer that cannot
// relocate the MakerNote of its container needs.
func FixedSize() MakerNoteEditOption {
	return func(c *makerNoteEditConfig) { c.fixedSize = true }
}

// mnEdit is an edit under way of the MakerNote of one EXIF. It works on a
// private copy of the note and changes e only in commit.
type mnEdit struct {
	e      *EXIF
	ent    *IFDEntry // the MakerNote entry of the Exif IFD
	lay    MakerNoteLayout
	mk     []byte
	mnOff  uint64
	origin int64  // TIFF offset that the pointers of a TIFF-relative note count from
	orig   []byte // the note as it was
	buf    []byte // the private copy that the edit changes
	footer bool   // buf ends with the Canon footer
	walker mnWalker
}

// notEditable returns ErrMakerNoteNotEditable with the reason.
func notEditable(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMakerNoteNotEditable, fmt.Sprintf(format, args...))
}

// locateMakerNote returns the MakerNote entry of the Exif IFD of e and the
// note itself with the Make, or the reason there is nothing to edit.
func locateMakerNote(e *EXIF) (ent *IFDEntry, mn, mk []byte, err error) {
	if e == nil {
		return nil, nil, nil, ErrNilEXIF
	}
	if e.BigTIFF {
		return nil, nil, nil, notEditable("the MakerNote of a BigTIFF file has an unknown offset base")
	}
	ent = e.ExifIFD.Get(TagMakerNote)
	if ent == nil || (len(ent.Value) == 0 && len(e.MakerNote) == 0) {
		if e.MakerNoteIFD != nil {
			// The tags are there but not as the Exif MakerNote tag: Canon CR3 keeps
			// them in a box of its own.
			return nil, nil, nil, notEditable("the MakerNote is not stored as the Exif MakerNote tag (Canon CR3 keeps it in a box of its own)")
		}
		return nil, nil, nil, ErrNoMakerNote
	}
	mn, mk = makerNoteOf(e)
	if mn == nil {
		return nil, nil, nil, notEditable("the MakerNote is too short to hold any tag")
	}
	return ent, mn, mk, nil
}

// newMakerNoteEdit prepares an edit of the MakerNote of e, or returns the
// reason it cannot be edited (see the file comment).
func newMakerNoteEdit(e *EXIF) (*mnEdit, error) {
	ent, mn, mk, err := locateMakerNote(e)
	if err != nil {
		return nil, err
	}
	lay, mnOff, ok := resolveMakerNote(e, mn, mk)
	switch {
	case !ok || lay.layout == mnNone:
		return nil, notEditable("the manufacturer layout of the MakerNote is not known, so the base of its offsets is not")
	case lay.layout == mnKyocera:
		return nil, notEditable("the offsets of a Kyocera MakerNote count from their own entries, which an edit does not keep")
	case lay.TIFFRelative && mnOff == 0:
		return nil, notEditable("the position of the MakerNote in the file it was read from is not known")
	}
	m := &mnEdit{e: e, ent: ent, lay: lay, mk: mk, mnOff: mnOff, orig: mn}
	if err := m.checkSelfContained(); err != nil {
		return nil, err
	}
	m.buf = append(make([]byte, 0, len(mn)+64), mn...)
	m.footer = lay.layout == mnCanon && len(mn) > canonFooterLen && isTIFFHeaderMark(mn[len(mn)-canonFooterLen:], lay.ByteOrder)
	if err := m.checkTable(); err != nil {
		return nil, err
	}
	return m, nil
}

// checkTable refuses a note whose tag table is empty or cut, or on which a value
// lies.
func (m *mnEdit) checkTable() error {
	n, ok := ifdEntryCount(m.buf, m.lay.IFDOffset, m.lay.ByteOrder)
	if !ok || n == 0 || m.count() != n {
		return notEditable("the tag table of the MakerNote is empty or truncated")
	}
	if m.valueOverlapsTable() {
		return notEditable("a value of the MakerNote lies on its tag table, so the offsets of the MakerNote are not read as the camera wrote them")
	}
	return nil
}

// checkSelfContained walks the note and records the origin of its pointers; it
// refuses a note that addresses data outside its own bytes, or whose pointers
// disagree with its length.
func (m *mnEdit) checkSelfContained() error {
	m.walker.init(m.orig, m.lay, int64(m.mnOff)) //nolint:gosec // G115: a classic TIFF offset, below 2^32
	m.walker.walkIFD(m.lay.IFDOffset, 0)
	if m.walker.lost > 0 {
		return notEditable("the MakerNote refers to data outside its own bytes, which an edited MakerNote would lose")
	}
	if m.walker.noCarry {
		// A value of a TIFF-relative note starts inside it and ends past its end
		// (ExifTool: "Possibly incorrect maker notes offsets"): the pointers are off
		// by a constant, and the data they address is not where they say.
		return notEditable("the offsets of the MakerNote are inconsistent with its length")
	}
	m.origin = m.walker.origin
	return nil
}

// valueOverlapsTable reports whether the value of an entry lies on the header
// or the tag table of the note: the pointers are then off by a constant (the
// Pentax Optio 330 and 430 of the corpus), and writing the value would write
// into the table.
func (m *mnEdit) valueOverlapsTable() bool {
	// The next-IFD offset is left out: a layout that has none starts its values
	// where it would be (Sony DSC-H9).
	tableEnd := m.entryAt(m.count())
	for i := range m.count() {
		if s, e, ok := m.valueRange(m.entryAt(i)); ok && s < tableEnd && e > 0 {
			return true
		}
	}
	return false
}

// count returns the number of entries of the top IFD of buf.
func (m *mnEdit) count() int {
	return int(m.lay.ByteOrder.Uint16(m.buf[m.lay.IFDOffset:]))
}

// entryAt returns the offset of buf where entry i of the top IFD starts.
func (m *mnEdit) entryAt(i int) int { return m.lay.IFDOffset + 2 + 12*i }

// targetOf converts the value of a pointer into an offset of the note.
func (m *mnEdit) targetOf(ptr uint32) int64 {
	if m.lay.TIFFRelative {
		return int64(ptr) - m.origin
	}
	return int64(ptr) + int64(m.lay.Base)
}

// pointerFor returns the value of a pointer that addresses the offset pos of
// the note.
func (m *mnEdit) pointerFor(pos int) (uint32, bool) {
	v := int64(pos) - int64(m.lay.Base)
	if m.lay.TIFFRelative {
		v = m.origin + int64(pos)
	}
	return uint32(v), v >= 0 && v <= math.MaxUint32 //nolint:gosec // G115: checked in the same expression list
}

// valueRange returns the range [start, end) of buf that the out-of-line value
// of entry at offset ep occupies, and false for an inline value or a pointer
// that leaves the note.
func (m *mnEdit) valueRange(ep int) (start, end int, ok bool) {
	e := readMNEntry(m.buf, ep, m.lay.ByteOrder)
	if e.total <= 4 {
		return 0, 0, false
	}
	t := m.targetOf(m.lay.ByteOrder.Uint32(m.buf[e.field:]))
	if t < 0 || t+e.total > int64(len(m.buf)) {
		return 0, 0, false
	}
	return int(t), int(t + e.total), true
}

// protectedAgainst reports whether [start, end) overlaps something that is not
// the value of entry skip: the header and the entry table of the IFD, the
// value of another entry, the sub-IFD that another entry addresses inline
// (Nikon PreviewIFD, Olympus sub-IFDs), or the Canon footer. Zeroing such a
// range would damage the note.
func (m *mnEdit) protectedAgainst(start, end, skip int) bool {
	return m.overlapsStructure(start, end, skip, true)
}

// overlapsStructure is protectedAgainst; withTable false leaves the header and
// the entry table out, to ask whether a layout keeps a value where the next-IFD
// offset would be.
func (m *mnEdit) overlapsStructure(start, end, skip int, withTable bool) bool {
	overlap := func(a, b int) bool { return start < b && a < end }
	tableEnd := m.entryAt(m.count()) // the next-IFD offset is not part of it: a layout without one keeps a value there
	if withTable && overlap(0, tableEnd) {
		return true
	}
	if m.footer && overlap(len(m.buf)-canonFooterLen, len(m.buf)) {
		return true
	}
	for i := range m.count() {
		if i == skip {
			continue
		}
		if m.entryOverlaps(m.entryAt(i), overlap) {
			return true
		}
	}
	return false
}

// entryOverlaps reports whether the value of the entry at offset ep, or the
// sub-IFD it addresses inline (Nikon PreviewIFD, Olympus sub-IFDs), overlaps
// the range that overlap tests.
func (m *mnEdit) entryOverlaps(ep int, overlap func(a, b int) bool) bool {
	if s, e, ok := m.valueRange(ep); ok && overlap(s, e) {
		return true
	}
	s, e, ok := m.subIFDExtent(ep)
	return ok && overlap(s, e)
}

// subIFDExtent returns the range of the note that the IFD addressed by the inline
// pointer of the entry at offset ep occupies (its table, as the extent of the
// values it points to is not known), and false when the entry does not address
// an IFD or the IFD leaves the note.
func (m *mnEdit) subIFDExtent(ep int) (start, end int, ok bool) {
	e := readMNEntry(m.buf, ep, m.lay.ByteOrder)
	if e.total == 0 || e.total > 4 || !m.walker.isSubIFDTag(e.tag) || e.cnt != 1 || (e.typ != TypeLong && e.typ != TypeIFD) {
		return 0, 0, false
	}
	t := m.targetOf(m.lay.ByteOrder.Uint32(m.buf[e.field:]))
	if t < 0 || t+2 > int64(len(m.buf)) {
		return 0, 0, false
	}
	span := 2 + 12*int64(m.lay.ByteOrder.Uint16(m.buf[t:])) + 4
	return int(t), int(min(t+span, int64(len(m.buf)))), true
}

// zeroValue clears the value of entry skip when it is out-of-line and nothing
// else uses the bytes, so that a deleted or replaced value does not stay in the
// file (a serial number that is deleted must be gone).
func (m *mnEdit) zeroValue(ep, skip int) {
	if s, e, ok := m.valueRange(ep); ok && !m.protectedAgainst(s, e, skip) {
		clear(m.buf[s:e])
	}
}

// deleteEntry removes entry k from the table (see the file comment).
func (m *mnEdit) deleteEntry(k int) error {
	n := m.count()
	if n <= 1 {
		return notEditable("the tag is the last one of the MakerNote")
	}
	order := m.lay.ByteOrder
	ep := m.entryAt(k)
	m.zeroValue(ep, k) // before the table changes: the ranges are those of the entries as they are
	tableEnd := m.entryAt(n)
	// The next-IFD offset follows the entries. A layout that has none (its first
	// value starts at tableEnd) keeps those bytes: they are a value.
	slot := tableEnd
	slotUsed := m.overlapsStructure(slot, slot+4, k, false) || slot+4 > len(m.buf)
	var next [4]byte
	if !slotUsed {
		copy(next[:], m.buf[slot:slot+4])
	}
	copy(m.buf[ep:], m.buf[ep+12:tableEnd])
	newEnd := tableEnd - 12
	clear(m.buf[newEnd:tableEnd])
	if !slotUsed {
		copy(m.buf[newEnd:], next[:])
		clear(m.buf[slot : slot+4])
	}
	order.PutUint16(m.buf[m.lay.IFDOffset:], uint16(n-1)) //nolint:gosec // G115: n-1 < n <= 0xFFFF
	return nil
}

// replaceEntry writes value (count elements of the type of the entry,
// encoded in the byte order of the note) as the value of entry k.
func (m *mnEdit) replaceEntry(k int, value []byte, count uint32, fixedSize bool) error {
	order := m.lay.ByteOrder
	ep := m.entryAt(k)
	e := readMNEntry(m.buf, ep, order)
	newTotal := int64(len(value))
	start, end, outOfLine := m.valueRange(ep)
	// A value that shares bytes with another entry (Kyocera FC-S3: the second
	// value starts inside the first) is not written in place: that would change
	// the other entry. It is replaced by a copy at the end of the note.
	shared := outOfLine && m.protectedAgainst(start, end, k)
	if err := replaceAllowed(e, newTotal, outOfLine, shared, fixedSize); err != nil {
		return err
	}
	switch {
	case newTotal <= 4: // inline: the field holds the value, left-justified
		if outOfLine {
			m.zeroValue(ep, k)
		}
		clear(m.buf[e.field : e.field+4])
		copy(m.buf[e.field:], value)
	case outOfLine && !shared && newTotal <= int64(end-start): // fits the old place
		copy(m.buf[start:], value)
		clear(m.buf[start+len(value) : end])
	default:
		if err := m.moveToEnd(k, value, outOfLine); err != nil {
			return err
		}
	}
	order.PutUint32(m.buf[ep+4:], count)
	return nil
}

// replaceAllowed checks that the entry e, whose value is out of line or not and
// shares bytes with another entry or not, can take a value of newTotal bytes.
func replaceAllowed(e mnEntry, newTotal int64, outOfLine, shared, fixedSize bool) error {
	switch {
	case fixedSize && newTotal != e.total:
		return fmt.Errorf("%w: the value has %d bytes and the one it replaces %d", ErrMakerNoteSizeChange, newTotal, e.total)
	case newTotal > maxMakerNoteValue:
		return notEditable("a value of %d bytes is larger than the limit of %d bytes", newTotal, maxMakerNoteValue)
	case e.total > 4 && !outOfLine:
		return notEditable("a pointer of the MakerNote leaves its bytes") // cannot happen after newMakerNoteEdit; kept as the guard of the invariant
	case shared && fixedSize:
		return notEditable("the value shares bytes with another tag, so it cannot be replaced without moving it")
	}
	return nil
}

// moveToEnd stores value at the end of the note, in front of the footer, makes
// entry k point to it and clears the old value when the entry had one out of
// line.
func (m *mnEdit) moveToEnd(k int, value []byte, hadValue bool) error {
	ep := m.entryAt(k)
	field := ep + 8
	pos, err := m.appendValue(value)
	if err != nil {
		return err
	}
	if hadValue {
		m.zeroValue(ep, k)
	}
	ptr, ok := m.pointerFor(pos)
	if !ok {
		return notEditable("the offset of the new value does not fit 32 bits")
	}
	m.lay.ByteOrder.PutUint32(m.buf[field:], ptr)
	return nil
}

// appendValue adds value to the end of the note, in front of Canon's footer,
// on a word boundary (TIFF 6.0 §2: values begin on a word boundary), and
// returns where it is.
func (m *mnEdit) appendValue(value []byte) (int, error) {
	end := len(m.buf)
	var foot [canonFooterLen]byte
	if m.footer {
		end -= canonFooterLen
		copy(foot[:], m.buf[end:])
	}
	if int64(end)+int64(len(value))+int64(canonFooterLen)+2 > math.MaxUint32 {
		return 0, notEditable("the MakerNote would be larger than 4 GiB")
	}
	b := m.buf[:end]
	if len(b)&1 == 1 {
		b = append(b, 0)
	}
	pos := len(b)
	b = append(b, value...)
	if len(b)&1 == 1 {
		b = append(b, 0)
	}
	if m.footer {
		b = append(b, foot[:]...)
	}
	m.buf = b
	return pos, nil
}

// topEntry is an entry of the top IFD as a reader sees it.
type topEntry struct {
	tag TagID
	typ DataType
	cnt uint32
	val []byte
}

// topEntries reads the entries of the top IFD of buf, with the values they
// address, for the layout and the origin of m. ok is false when a value leaves
// buf.
func (m *mnEdit) topEntries(buf []byte) (res []topEntry, ok bool) {
	order := m.lay.ByteOrder
	n, fits := ifdEntryCount(buf, m.lay.IFDOffset, order)
	if !fits {
		return nil, false
	}
	res = make([]topEntry, 0, n)
	for i := range n {
		e := readMNEntry(buf, m.lay.IFDOffset+2+12*i, order)
		te := topEntry{tag: e.tag, typ: e.typ, cnt: e.cnt}
		switch {
		case e.total <= 4:
			te.val = buf[e.field : e.field+int(e.total)]
		default:
			t := m.targetOf(order.Uint32(buf[e.field:]))
			if t < 0 || t+e.total > int64(len(buf)) {
				return nil, false
			}
			te.val = buf[t : t+e.total]
		}
		res = append(res, te)
	}
	return res, true
}

// verify checks the edited note against the one before the edit: want is the
// list of entries it must hold. It reports the first difference.
func (m *mnEdit) verify(want []topEntry) error {
	if err := m.verifyLayout(); err != nil {
		return err
	}
	got, ok := m.topEntries(m.buf)
	if !ok || len(got) != len(want) {
		return notEditable("the edited MakerNote does not hold the expected tags")
	}
	for i := range got {
		if got[i].tag != want[i].tag || got[i].typ != want[i].typ || got[i].cnt != want[i].cnt || !bytes.Equal(got[i].val, want[i].val) {
			return notEditable("the edited MakerNote does not hold the expected value of tag %#04x", uint16(got[i].tag))
		}
	}
	return nil
}

// verifyLayout checks that a writer finds the layout and the offset base of
// the edited note as it found them in the note before the edit, and that no
// pointer leaves the note: the writers decide both from the bytes of the note
// (resolveMakerNote), so an edit that changes the decision would make them
// place the note wrongly.
func (m *mnEdit) verifyLayout() error {
	probe := &EXIF{ByteOrder: m.e.ByteOrder, IFD0: m.e.IFD0, MakerNoteOffset: m.e.MakerNoteOffset, MakerNoteOffset64: m.e.MakerNoteOffset64}
	lay, mnOff, ok := resolveMakerNote(probe, m.buf, m.mk)
	if !ok || lay.IFDOffset != m.lay.IFDOffset || lay.ByteOrder != m.lay.ByteOrder || lay.TIFFRelative != m.lay.TIFFRelative ||
		lay.Base != m.lay.Base || lay.layout != m.lay.layout || mnOff != m.mnOff {
		return notEditable("the offset base of this MakerNote is ambiguous (the camera's layout and its data disagree), so this edit could change how its offsets are read afterwards; replacing a value with one of the same size does not")
	}
	var w mnWalker
	w.init(m.buf, lay, int64(mnOff)) //nolint:gosec // G115: a classic TIFF offset, below 2^32
	w.walkIFD(lay.IFDOffset, 0)
	if w.lost > 0 || w.origin != m.origin {
		return notEditable("a pointer of the edited MakerNote leaves its bytes or its offset base changed")
	}
	return nil
}

// commit installs the edited note in e, after the check of verify: the
// MakerNote entry of the Exif IFD and EXIF.MakerNote get the edited copy, and
// the parsed MakerNote IFD, when e has one, the edited entries.
func (m *mnEdit) commit(deleted map[TagID]bool, replaced map[TagID]topEntry) {
	m.ent.Value = m.buf
	m.ent.Count = uint32(len(m.buf)) //nolint:gosec // G115: bounded by the 32-bit pointers, see appendValue
	m.e.MakerNote = m.buf
	ifd := m.e.MakerNoteIFD
	if ifd == nil {
		return
	}
	kept := ifd.Entries[:0]
	for _, ent := range ifd.Entries {
		if deleted[ent.Tag] {
			continue
		}
		if r, ok := replaced[ent.Tag]; ok {
			ent.Count = r.cnt
			ent.Value = bytes.Clone(r.val)
		}
		kept = append(kept, ent)
	}
	clear(ifd.Entries[len(kept):]) // release the values of the removed entries
	ifd.Entries = kept
}

// DeleteMakerNoteTag removes every entry with the given tag ID from the
// MakerNote of e, together with the value it holds, so that the value is not
// left in the file (for example a camera or lens serial number). The entries
// that remain keep their values and their positions in the MakerNote: no
// offset inside it changes, which makes the edit safe for every layout whose
// note is self-contained (see ErrMakerNoteNotEditable).
//
// It returns ErrMakerNoteTagNotFound when no entry has the tag, ErrNoMakerNote
// when e has no MakerNote, and ErrMakerNoteNotEditable, wrapped with the reason,
// when the note cannot be edited safely. On any error e is unchanged.
//
// The MakerNote bytes of the image are not modified until the EXIF is encoded.
// Metadata.DeleteMakerNoteTag applies the edit to a read image and marks the
// EXIF segment as modified.
func (e *EXIF) DeleteMakerNoteTag(tag TagID) error {
	return e.DeleteMakerNoteTags([]TagID{tag})
}

// DeleteMakerNoteTags is DeleteMakerNoteTag for several tags at once: all are
// deleted, or none is. Every tag must be present in the MakerNote.
func (e *EXIF) DeleteMakerNoteTags(tags []TagID) error {
	m, err := newMakerNoteEdit(e)
	if err != nil {
		return err
	}
	want, ok := m.topEntries(m.orig) // the note as it was: m.buf is about to change
	if !ok {
		return notEditable("a value of the MakerNote leaves its bytes")
	}
	kept, del, err := withoutTags(want, tags)
	if err != nil {
		return err
	}
	for k := m.count() - 1; k >= 0; k-- {
		if del[TagID(m.lay.ByteOrder.Uint16(m.buf[m.entryAt(k):]))] {
			if err := m.deleteEntry(k); err != nil {
				return err
			}
		}
	}
	if err := m.verify(kept); err != nil {
		return err
	}
	m.commit(del, nil)
	return nil
}

// withoutTags returns the entries of want that do not have one of tags, and the
// set of tags; it fails when a tag is not among the entries.
func withoutTags(want []topEntry, tags []TagID) (kept []topEntry, del map[TagID]bool, err error) {
	del = make(map[TagID]bool, len(tags))
	for _, t := range tags {
		del[t] = true
	}
	present := make(map[TagID]bool, len(tags))
	kept = make([]topEntry, 0, len(want))
	for _, te := range want {
		if del[te.tag] {
			present[te.tag] = true
			continue
		}
		kept = append(kept, te)
	}
	for _, t := range tags {
		if !present[t] {
			return nil, nil, fmt.Errorf("%w: %#04x", ErrMakerNoteTagNotFound, uint16(t))
		}
	}
	return kept, del, nil
}

// SetMakerNoteTag replaces the value of every entry of the MakerNote of e that
// has the given tag ID. The type of the entry is kept; value must fit it:
//
//   - string for an ASCII entry (the terminating NUL is added; the string must
//     not contain NUL);
//   - []byte for an entry of any type: the raw bytes of the value in the byte
//     order of the MakerNote (their count must be a multiple of the size of
//     one element of the type);
//   - an integer, []uint16 or []uint32 for a BYTE, SHORT, LONG or signed
//     variant, range-checked against the type.
//
// A value of the same byte size as the old one is written where the old one
// was. Another size is accepted unless FixedSize is given: a smaller or equal
// one is written in place, a larger one is added at the end of the MakerNote
// and the entry points to it (see the file comment); no existing value moves.
// A value of 4 bytes or less is stored in the entry itself.
//
// It returns ErrMakerNoteTagNotFound when no entry has the tag (a tag cannot be
// added: that would move every value of the note), ErrMakerNoteValueType when
// value does not fit the type of the entry, ErrMakerNoteSizeChange for another
// size under FixedSize, ErrNoMakerNote when e has no MakerNote and
// ErrMakerNoteNotEditable, wrapped with the reason, when the note cannot be edited
// safely. On any error e is unchanged.
func (e *EXIF) SetMakerNoteTag(tag TagID, value any, opts ...MakerNoteEditOption) error {
	var cfg makerNoteEditConfig
	for _, o := range opts {
		o(&cfg)
	}
	m, err := newMakerNoteEdit(e)
	if err != nil {
		return err
	}
	want, ok := m.topEntries(m.orig) // the note as it was: m.buf is about to change
	if !ok {
		return notEditable("a value of the MakerNote leaves its bytes")
	}
	// Every value is encoded before anything is written, so a value that does not
	// fit the type is reported before the private copy is touched.
	replaced, err := encodeReplacements(want, tag, value, m.lay.ByteOrder)
	if err != nil {
		return err
	}
	for k := range want {
		if want[k].tag != tag {
			continue
		}
		if err := m.replaceEntry(k, want[k].val, want[k].cnt, cfg.fixedSize); err != nil {
			return err
		}
	}
	if err := m.verify(want); err != nil {
		return err
	}
	m.commit(nil, replaced)
	return nil
}

// encodeReplacements replaces, in want, the value of every entry of tag by value
// encoded for the type of the entry, and returns the replacement.
func encodeReplacements(want []topEntry, tag TagID, value any, order binary.ByteOrder) (map[TagID]topEntry, error) {
	replaced := map[TagID]topEntry{}
	for k := range want {
		if want[k].tag != tag {
			continue
		}
		raw, count, err := encodeMakerNoteValue(want[k].typ, value, order)
		if err != nil {
			return nil, err
		}
		want[k] = topEntry{tag: tag, typ: want[k].typ, cnt: count, val: raw}
		replaced[tag] = want[k]
	}
	if len(replaced) == 0 {
		return nil, fmt.Errorf("%w: %#04x", ErrMakerNoteTagNotFound, uint16(tag))
	}
	return replaced, nil
}

// encodeMakerNoteValue encodes value for an entry of type typ in order, and
// returns the bytes and the element count (see SetMakerNoteTag).
func encodeMakerNoteValue(typ DataType, value any, order binary.ByteOrder) (raw []byte, count uint32, err error) {
	size := int(typeSize(typ))
	if size == 0 {
		return nil, 0, fmt.Errorf("%w: the tag has a type that cannot be edited", ErrMakerNoteValueType)
	}
	switch v := value.(type) {
	case string:
		return encodeText(typ, v)
	case []byte:
		return encodeRaw(typ, size, v)
	case []uint16:
		return encodeInts(typ, order, len(v), func(i int) int64 { return int64(v[i]) })
	case []uint32:
		return encodeInts(typ, order, len(v), func(i int) int64 { return int64(v[i]) })
	}
	n, ok := asInt64(value)
	if !ok {
		return nil, 0, fmt.Errorf("%w: unsupported value type", ErrMakerNoteValueType)
	}
	return encodeInts(typ, order, 1, func(int) int64 { return n })
}

// encodeText encodes a string for a text tag: NUL-terminated (TIFF 6.0 §2, p. 15).
func encodeText(typ DataType, v string) (raw []byte, count uint32, err error) {
	switch {
	case typ != TypeASCII && typ != TypeUTF8:
		return nil, 0, fmt.Errorf("%w: a string fits only a text tag", ErrMakerNoteValueType)
	case strings.IndexByte(v, 0) >= 0:
		return nil, 0, fmt.Errorf("%w: a text value must not contain NUL", ErrMakerNoteValueType)
	case len(v)+1 > maxMakerNoteValue:
		return nil, 0, fmt.Errorf("%w: the text is too long", ErrMakerNoteValueType)
	}
	raw = append([]byte(v), 0)
	return raw, uint32(len(raw)), nil //nolint:gosec // G115: bounded by maxMakerNoteValue
}

// encodeRaw checks the raw bytes v of a value of a tag of type typ (elements of
// size bytes) and returns a copy.
func encodeRaw(typ DataType, size int, v []byte) (raw []byte, count uint32, err error) {
	switch {
	case typ == TypeASCII || typ == TypeUTF8:
		return nil, 0, fmt.Errorf("%w: a text tag takes a string", ErrMakerNoteValueType)
	case len(v) == 0 || len(v)%size != 0 || len(v) > maxMakerNoteValue:
		return nil, 0, fmt.Errorf("%w: the byte count must be a non-zero multiple of the element size of the tag and within the limit", ErrMakerNoteValueType)
	}
	return bytes.Clone(v), uint32(len(v) / size), nil //nolint:gosec // G115: bounded by maxMakerNoteValue
}

// asInt64 converts an integer of any Go integer type.
//
//nolint:gocyclo // a flat type switch, one case per Go integer type
func asInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint:
		return int64(x), uint64(x) <= math.MaxInt64 //nolint:gosec // G115: checked in the same expression list; uint64 keeps the constant in range on 32-bit targets
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		return int64(x), x <= math.MaxInt64 //nolint:gosec // G115: checked in the same expression list
	}
	return 0, false
}

// intRange returns the range of the values and the size of an element of the
// integer type typ, and false for a type that is not an integer.
func intRange(typ DataType) (lo, hi int64, size int, ok bool) {
	switch typ { //nolint:exhaustive // only the integer types are editable this way
	case TypeByte:
		return 0, math.MaxUint8, 1, true
	case TypeSByte:
		return math.MinInt8, math.MaxInt8, 1, true
	case TypeShort:
		return 0, math.MaxUint16, 2, true
	case TypeSShort:
		return math.MinInt16, math.MaxInt16, 2, true
	case TypeLong, TypeIFD:
		return 0, math.MaxUint32, 4, true
	case TypeSLong:
		return math.MinInt32, math.MaxInt32, 4, true
	}
	return 0, 0, 0, false
}

// encodeInts encodes n integers (get(i)) as elements of the integer type typ.
func encodeInts(typ DataType, order binary.ByteOrder, n int, get func(int) int64) ([]byte, uint32, error) {
	lo, hi, size, ok := intRange(typ)
	if !ok {
		return nil, 0, fmt.Errorf("%w: an integer does not fit the type of the tag", ErrMakerNoteValueType)
	}
	if n == 0 || n > maxMakerNoteValue/size {
		return nil, 0, fmt.Errorf("%w: the number of values is zero or too large", ErrMakerNoteValueType)
	}
	raw := make([]byte, n*size)
	for i := range n {
		x := get(i)
		if x < lo || x > hi {
			return nil, 0, fmt.Errorf("%w: %d is outside the range of the type of the tag", ErrMakerNoteValueType, x)
		}
		switch size {
		case 1:
			raw[i] = byte(x) //nolint:gosec // G115: range-checked
		case 2:
			order.PutUint16(raw[i*2:], uint16(x)) //nolint:gosec // G115: range-checked
		default:
			order.PutUint32(raw[i*4:], uint32(x)) //nolint:gosec // G115: range-checked
		}
	}
	return raw, uint32(n), nil //nolint:gosec // G115: bounded above
}

// MakerNoteTagIDs resolves tag, the name of a MakerNote tag in the table of the
// manufacturer of e (see TagNameIn) or a numeric ID written as "0x00A0", to the
// IDs of the entries of the MakerNote that it names. A name that several tags of
// the table share ("CameraSerialNumber" in the Nikon table) resolves to all of
// them; the result holds only IDs that the MakerNote has, in the order of its
// tags. It returns ErrNoMakerNote when e has no MakerNote and
// ErrMakerNoteTagNotFound when the MakerNote has no tag of that name or ID.
func (e *EXIF) MakerNoteTagIDs(tag string) ([]TagID, error) {
	if e == nil || e.MakerNoteIFD == nil {
		return nil, ErrNoMakerNote
	}
	ids, err := e.candidateTagIDs(tag)
	if err != nil {
		return nil, err
	}
	res := ids[:0]
	for _, id := range ids {
		if e.MakerNoteIFD.Get(id) != nil {
			res = append(res, id)
		}
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrMakerNoteTagNotFound, tag)
	}
	return res, nil
}

// candidateTagIDs returns the tag IDs that tag names in the MakerNote table of
// e, or the ID that it spells as hexadecimal.
func (e *EXIF) candidateTagIDs(tag string) ([]TagID, error) {
	if len(tag) > 2 && tag[0] == '0' && (tag[1] == 'x' || tag[1] == 'X') {
		id, err := strconv.ParseUint(tag[2:], 16, 16)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not a tag name or a hexadecimal tag ID", ErrMakerNoteTagNotFound, tag)
		}
		return []TagID{TagID(id)}, nil
	}
	ctx, ok := e.MakerNoteContext()
	if !ok {
		return nil, nil
	}
	var ids []TagID
	for _, t := range makerNoteTables[ctx-ContextMakerNoteCanon] {
		if t.name == tag {
			ids = append(ids, TagID(t.id))
		}
	}
	return ids, nil
}
