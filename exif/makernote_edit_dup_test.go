package exif

// makernote_edit_dup_test.go — task #416: the edit of a MakerNote whose table
// repeats a tag (a malformed or hostile file; Parse keeps one entry of them)
// is linear in the table, not quadratic, and keeps the semantics of the edit of
// each entry on its own.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// dupEnt is an entry of the Canon-style note that dupNoteTIFF builds. A value
// of more than 4 bytes is at off bytes into the value area; otherwise it is
// val, in the entry.
type dupEnt struct {
	tag     uint16
	typ     uint16
	cnt     uint32
	off     int // -1: inline
	inlineV uint32
}

// dupNoteTIFF builds a little-endian TIFF whose Exif IFD holds the MakerNote of a
// Canon camera made of ents and the value area vals.
func dupNoteTIFF(ents []dupEnt, vals []byte) []byte {
	le := binary.LittleEndian
	put := func(b []byte, tag, typ uint16, cnt, val uint32) []byte {
		var e [12]byte
		le.PutUint16(e[0:], tag)
		le.PutUint16(e[2:], typ)
		le.PutUint32(e[4:], cnt)
		le.PutUint32(e[8:], val)
		return append(b, e[:]...)
	}
	const mnOff = 8 + 2 + 24 + 4 + 6 + 2 + 12 + 4
	n := len(ents)
	mn := []byte{byte(n), byte(n >> 8)} //nolint:gosec // G115: the low and the high byte of a count that is below 65536
	valBase := mnOff + 2 + 12*n + 4
	for _, e := range ents {
		v := e.inlineV
		if e.off >= 0 {
			v = uint32(valBase + e.off) //nolint:gosec // G115: test sizes are small
		}
		mn = put(mn, e.tag, e.typ, e.cnt, v)
	}
	mn = append(mn, 0, 0, 0, 0)
	mn = append(mn, vals...)
	b := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 2, 0}
	makeBytes := []byte("Canon\x00")
	b = put(b, 0x010F, 2, uint32(len(makeBytes)), 8+2+24+4)   //nolint:gosec // G115: test sizes are small
	b = put(b, 0x8769, 4, 1, uint32(8+2+24+4+len(makeBytes))) //nolint:gosec // G115: test sizes are small
	b = append(b, 0, 0, 0, 0)
	b = append(b, makeBytes...)
	b = append(b, 1, 0)
	b = put(b, 0x927C, 7, uint32(len(mn)), mnOff) //nolint:gosec // G115: test sizes are small
	b = append(b, 0, 0, 0, 0)
	return append(b, mn...)
}

// repeatedTag builds a note with n entries of the tag 0x0006 (ASCII, 8 bytes)
// and one entry of the tag 0x0001 (SHORT, inline) that is not edited. In the
// mode "ool" every repeated entry addresses the same value (the worst case for
// the protection of shared bytes), in "own" each has a value of its own, and
// otherwise the value is inline (2 bytes).
func repeatedTag(n int, mode string) []byte {
	ents := make([]dupEnt, 0, n+1)
	ents = append(ents, dupEnt{tag: 0x0001, typ: 3, cnt: 1, off: -1, inlineV: 7})
	var vals []byte
	for i := range n {
		switch mode {
		case "ool":
			ents = append(ents, dupEnt{tag: 0x0006, typ: 2, cnt: 8, off: 0})
		case "own":
			ents = append(ents, dupEnt{tag: 0x0006, typ: 2, cnt: 8, off: 8 * i})
		default:
			ents = append(ents, dupEnt{tag: 0x0006, typ: 2, cnt: 2, off: -1, inlineV: 0x61})
		}
	}
	switch mode {
	case "ool":
		vals = []byte("abcdefg\x00")
	case "own":
		vals = bytes.Repeat([]byte("abcdefg\x00"), n)
	}
	return dupNoteTIFF(ents, vals)
}

// parseNote parses tiff and returns the EXIF, which must have an editable MakerNote.
func parseNote(tb testing.TB, tiff []byte) *EXIF {
	tb.Helper()
	e, err := Parse(tiff)
	if err != nil || e == nil || e.MakerNoteIFD == nil {
		tb.Fatalf("Parse: %v", err)
	}
	if _, err := newMakerNoteEdit(e); err != nil {
		tb.Fatalf("fixture is not editable: %v", err)
	}
	return e
}

// noteEntries returns the entries of the top IFD of the MakerNote of e.
func noteEntries(tb testing.TB, e *EXIF) []topEntry {
	tb.Helper()
	m, err := newMakerNoteEdit(e)
	if err != nil {
		tb.Fatal(err)
	}
	te, ok := m.topEntries(m.orig)
	if !ok {
		tb.Fatal("a value of the MakerNote leaves its bytes")
	}
	return te
}

// dupBudget is the time that the edit of a table of 20,000 repeated tags may take
// (task #416). Linear in the table it takes a few milliseconds, and quadratic
// 20 seconds; the bound is far above the first, so that the race detector and a
// busy machine cannot trip it, and far below the second, so that the quadratic
// cost cannot pass. An absolute bound is used instead of the ratio between two
// sizes because the ratio of two timings of a few milliseconds is the noisier
// of the two.
const dupBudget = time.Second

// TestMakerNoteEdit_R416_RepeatedTagIsLinear: the deletion and the replacement of
// 20,000 entries of one tag complete within dupBudget, for values that are inline,
// each in its own place, and all at one place, and give the result the edit of the
// entries one at a time gives.
func TestMakerNoteEdit_R416_RepeatedTagIsLinear(t *testing.T) {
	t.Parallel()
	const n = 20000
	for _, mode := range []string{"inline", "own", "ool"} {
		tiff := repeatedTag(n, mode)

		e := parseNote(t, tiff)
		start := time.Now()
		err := e.DeleteMakerNoteTag(0x0006)
		if d := time.Since(start); d > dupBudget {
			t.Errorf("%s: DeleteMakerNoteTag of %d entries took %v, budget %v", mode, n, d, dupBudget)
		}
		if err != nil {
			t.Fatalf("%s: DeleteMakerNoteTag: %v", mode, err)
		}
		if got := noteEntries(t, e); len(got) != 1 || got[0].tag != 0x0001 {
			t.Errorf("%s: after the delete the note holds %d entries, want only tag 1", mode, len(got))
		}
		// The value of a deleted tag is not left in the file, also when the entries
		// shared it: the last of them to be deleted zeroes it.
		if bytes.Contains(e.MakerNote, []byte("abcdefg")) {
			t.Errorf("%s: the value of the deleted tag is still in the MakerNote", mode)
		}

		for _, v := range []string{"ABCDEFG", "A", strings.Repeat("G", 100)} { // same size, smaller, larger
			e = parseNote(t, tiff)
			start = time.Now()
			err = e.SetMakerNoteTag(0x0006, v)
			if d := time.Since(start); d > dupBudget {
				t.Errorf("%s: SetMakerNoteTag(%d bytes) of %d entries took %v, budget %v", mode, len(v), n, d, dupBudget)
			}
			if err != nil {
				t.Fatalf("%s: SetMakerNoteTag(%d bytes): %v", mode, len(v), err)
			}
			got := noteEntries(t, e)
			if len(got) != n+1 {
				t.Fatalf("%s: %d entries after the replacement, want %d", mode, len(got), n+1)
			}
			for i := range got {
				switch {
				case got[i].tag == 0x0001 && !bytes.Equal(got[i].val, []byte{7, 0}):
					t.Fatalf("%s: tag 1 changed", mode)
				case got[i].tag == 0x0006 && string(got[i].val) != v+"\x00":
					t.Fatalf("%s: entry %d = %q, want %q", mode, i, got[i].val, v+"\x00")
				}
			}
		}
	}
}

// TestMakerNoteEdit_R416_GrowthIsBounded: a value written for each of many
// entries is refused once the note would grow by more than maxMakerNoteGrowth
// bytes, with the EXIF unchanged, and the value is encoded once (not once per
// entry), so the memory the edit uses is a small multiple of the cap.
func TestMakerNoteEdit_R416_GrowthIsBounded(t *testing.T) {
	t.Parallel()
	e := parseNote(t, repeatedTag(2000, "own"))
	before := bytes.Clone(e.MakerNote)
	big := strings.Repeat("G", 16<<10) // 2000 x 16 KiB is 32 MiB
	err := e.SetMakerNoteTag(0x0006, big)
	if !errors.Is(err, ErrMakerNoteNotEditable) {
		t.Fatalf("error = %v, want ErrMakerNoteNotEditable", err)
	}
	if !bytes.Equal(before, e.MakerNote) {
		t.Fatal("a refused edit changed the note")
	}
	// A value as large as the limit is still accepted for one entry.
	e = parseNote(t, repeatedTag(1, "own"))
	if err := e.SetMakerNoteTag(0x0006, strings.Repeat("G", maxMakerNoteValue-1)); err != nil {
		t.Fatalf("a value of the largest size: %v", err)
	}
}

// TestMakerNoteEdit_R416_RangeIndex: the index answers the overlap question as a
// scan of the ranges does, while ranges are released.
func TestMakerNoteEdit_R416_RangeIndex(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 416)) //nolint:gosec // G404: deterministic seed for reproducible ranges, not a secret
	for trial := range 300 {
		n := 1 + rng.IntN(40)
		ix := &mnRangeIndex{own: make([]mnOwn, n), ranked: trial%2 == 0} // both ways to search
		var live []int
		for i := range n {
			if rng.IntN(5) == 0 {
				continue // an entry without a range
			}
			s := rng.IntN(100)
			ix.own[i] = mnOwn{mnRange{s, s + 1 + rng.IntN(30)}, true}
			live = append(live, i)
		}
		if ix.ranked {
			ix.startPos, ix.startCnt, ix.starts = rankRanges(live, n, func(i int) int { return ix.own[i].start })
			ix.endPos, ix.endCnt, ix.ends = rankRanges(live, n, func(i int) int { return ix.own[i].end })
		}
		for step := range 60 {
			s := rng.IntN(130)
			e := s + 1 + rng.IntN(30)
			skip := rng.IntN(n+1) - 1
			want := false
			for i := range n {
				if i != skip && ix.own[i].active && ix.own[i].overlaps(s, e) {
					want = true
				}
			}
			if got := ix.overlapsOther(s, e, skip); got != want {
				t.Fatalf("trial %d step %d: overlapsOther(%d,%d,skip %d) = %v, want %v", trial, step, s, e, skip, got, want)
			}
			if step%3 == 0 {
				ix.release(rng.IntN(n))
			}
		}
	}
}

// reference implementation of the edits, as they were before the index
// (task #398/#399 up to sprint 54): a scan of every entry per question and a
// deletion per entry. The tests compare the edits with it.

func (m *mnEdit) refProtected(start, end, skip int, withTable bool) bool {
	overlap := func(a, b int) bool { return start < b && a < end }
	tableEnd := m.entryAt(m.count())
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
		ep := m.entryAt(i)
		if s, e, ok := m.valueRange(ep); ok && overlap(s, e) {
			return true
		}
		if s, e, ok := m.subIFDExtent(ep); ok && overlap(s, e) {
			return true
		}
	}
	return false
}

func (m *mnEdit) refZero(ep, skip int) {
	if s, e, ok := m.valueRange(ep); ok && !m.refProtected(s, e, skip, true) {
		clear(m.buf[s:e])
	}
}

func (m *mnEdit) refDelete(k int) error {
	n := m.count()
	if n <= 1 {
		return notEditable("the tag is the last one of the MakerNote")
	}
	ep := m.entryAt(k)
	m.refZero(ep, k)
	tableEnd := m.entryAt(n)
	slot := tableEnd
	slotUsed := m.refProtected(slot, slot+4, k, false) || slot+4 > len(m.buf)
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
	m.lay.ByteOrder.PutUint16(m.buf[m.lay.IFDOffset:], uint16(n-1)) //nolint:gosec // G115: test sizes are small
	return nil
}

func (m *mnEdit) refReplace(k int, value []byte, count uint32) error {
	order := m.lay.ByteOrder
	ep := m.entryAt(k)
	newTotal := int64(len(value))
	start, end, outOfLine := m.valueRange(ep)
	shared := outOfLine && m.refProtected(start, end, k, true)
	e := readMNEntry(m.buf, ep, order)
	if err := replaceAllowed(e, newTotal, outOfLine, shared, false); err != nil {
		return err
	}
	switch {
	case newTotal <= 4:
		if outOfLine {
			m.refZero(ep, k)
		}
		clear(m.buf[e.field : e.field+4])
		copy(m.buf[e.field:], value)
	case outOfLine && !shared && newTotal <= int64(end-start):
		copy(m.buf[start:], value)
		clear(m.buf[start+len(value) : end])
	default:
		pos, err := m.appendValue(value)
		if err != nil {
			return err
		}
		if outOfLine {
			m.refZero(ep, k)
		}
		ptr, ok := m.pointerFor(pos)
		if !ok {
			return notEditable("pointer")
		}
		order.PutUint32(m.buf[ep+8:], ptr)
	}
	order.PutUint32(m.buf[ep+4:], count)
	return nil
}

// randomNote builds a note of 2 to 14 entries (big: more than mnIndexThreshold) over 3 tags, with values that are
// inline, each in its own place, shared, or overlapping, and returns the TIFF.
// disjoint keeps every value in a place of its own.
func randomNote(rng *rand.Rand, disjoint, big bool) []byte {
	n := 2 + rng.IntN(13)
	if big {
		n += mnIndexThreshold // the index of such a table is searched through its trees
	}
	var ents []dupEnt
	slots := 1 + rng.IntN(4)
	if big {
		slots += 8
	}
	for i := range n {
		tag := uint16(1 + rng.IntN(3)) //nolint:gosec // G115: 1 to 3
		switch rng.IntN(3) {
		case 0:
			ents = append(ents, dupEnt{tag: tag, typ: 3, cnt: 1, off: -1, inlineV: uint32(i)})
		default:
			off := 16 * i
			if !disjoint && i > 0 {
				switch rng.IntN(3) {
				case 0:
					off = 16 * rng.IntN(slots) // shared place
				case 1:
					off = 16*rng.IntN(slots) + 1 + rng.IntN(12) // overlapping place
				}
			}
			ents = append(ents, dupEnt{tag: tag, typ: 7, cnt: uint32(5 + rng.IntN(12)), off: off}) //nolint:gosec // G115: small
		}
	}
	vals := make([]byte, 16*n+32)
	for i := range vals {
		vals[i] = byte(1 + i%250)
	}
	return dupNoteTIFF(ents, vals)
}

// TestMakerNoteEdit_R416_AgreesWithEntryByEntry: on random notes that repeat
// tags, the edits give what the deletion and the replacement of one entry at a
// time give: the same refusals, the same entries, and, when no value shares
// bytes with another, the same bytes. When values share bytes, the edits protect
// at least what the one-at-a-time edits do, so a value they zero is zeroed there
// too.
func TestMakerNoteEdit_R416_AgreesWithEntryByEntry(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(416, 7)) //nolint:gosec // G404: deterministic seed for reproducible notes, not a secret
	compared := 0
	for trial := range 3000 {
		disjoint := trial%2 == 0
		tiff := randomNote(rng, disjoint, trial%4 >= 2)
		tag := TagID(1 + rng.IntN(3)) //nolint:gosec // G115: 1 to 3
		op := rng.IntN(3)
		value := strings.Repeat("v", []int{1, 3, 8, 40}[rng.IntN(4)])

		e1 := mustParseAny(tiff)
		e2 := mustParseAny(tiff)
		if e1 == nil {
			continue
		}
		m, err := newMakerNoteEdit(e2)
		if err != nil {
			continue
		}
		want, _ := m.topEntries(m.orig)
		m0, _ := newMakerNoteEdit(e1) // the note as it was, for the comparison of bytes

		var got error
		switch op {
		case 0:
			got = e1.DeleteMakerNoteTag(tag)
		default:
			got = e1.SetMakerNoteTag(tag, []byte(strings.Repeat("v", 4*(1+len(value)%7))))
		}

		var ref error
		switch op {
		case 0:
			kept, del, werr := withoutTags(want, []TagID{tag})
			if werr != nil {
				ref = werr
				break
			}
			for k := m.count() - 1; k >= 0; k-- {
				if del[TagID(m.lay.ByteOrder.Uint16(m.buf[m.entryAt(k):]))] {
					if ref = m.refDelete(k); ref != nil {
						break
					}
				}
			}
			if ref == nil {
				ref = m.verify(kept)
			}
		default:
			raw := []byte(strings.Repeat("v", 4*(1+len(value)%7)))
			replaced, rerr := encodeReplacements(want, tag, raw, m.lay.ByteOrder)
			_ = replaced
			if rerr != nil {
				ref = rerr
				break
			}
			for k := range want {
				if want[k].tag == tag {
					if ref = m.refReplace(k, want[k].val, want[k].cnt); ref != nil {
						break
					}
				}
			}
			if ref == nil {
				ref = m.verify(want)
			}
		}
		refBuf := m.buf

		if (got == nil) != (ref == nil) {
			// The index keeps the bytes that a value gave up protected, so a note whose
			// bytes are shared may be refused where the entry-by-entry edit was not,
			// never the other way round.
			if got == nil || disjoint {
				t.Fatalf("trial %d op %d tag %d: edit error %v, reference error %v", trial, op, tag, got, ref)
			}
			continue
		}
		if got != nil {
			continue
		}
		compared++
		if disjoint && !bytes.Equal(e1.MakerNote, refBuf) {
			t.Fatalf("trial %d op %d tag %d: the bytes differ from the one-at-a-time edit\n got %x\nwant %x", trial, op, tag, e1.MakerNote, refBuf)
		}
		// The entries are those the edit promises (verify, run by both, says so);
		// and a byte of a value that the edit zeroes is zeroed by the one-at-a-time
		// edit too, so the edit never zeroes what an entry of the note still uses.
		orig := m0.orig
		first := m0.entryAt(m0.count()) + 4
		for i := first; i < len(orig) && i < len(e1.MakerNote) && i < len(refBuf); i++ {
			if e1.MakerNote[i] == 0 && orig[i] != 0 && refBuf[i] != 0 {
				t.Fatalf("trial %d op %d tag %d: byte %d was zeroed by the edit but not by the one-at-a-time edit", trial, op, tag, i)
			}
		}
	}
	if compared < 500 {
		t.Fatalf("only %d of the random notes were compared: the generator is not exercising the edit", compared)
	}
}

// mustParseAny parses tiff and returns nil when it has no editable MakerNote.
func mustParseAny(tiff []byte) *EXIF {
	e, err := Parse(tiff)
	if err != nil || e == nil || e.MakerNoteIFD == nil {
		return nil
	}
	return e
}
