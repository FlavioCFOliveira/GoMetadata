package exif

// makernote_edit_index.go — the overlap index of the MakerNote editor (task
// #416).
//
// The editor must not zero or overwrite bytes that another entry of the note
// uses (the value of another entry, the sub-IFD that another entry addresses,
// see mnEdit.protectedAgainst). Asked of every entry in turn, that question is
// linear in the number of entries, and a note whose table holds thousands of
// entries of the edited tag (a malformed or hostile file; Parse keeps one of
// them) made an edit quadratic: 86 s for the deletion of 65,000 entries. The
// index answers the question in logarithmic time and follows the changes that
// an edit makes: the entries whose value an edit moves or deletes stop
// protecting the bytes they used, as they did when every entry was asked (the
// bytes that a value written in place gives up to a smaller one stay protected,
// which is never less than asking every entry).
//
// A table of up to mnIndexThreshold entries is searched range by range, which
// is as fast as the trees for a table of that size and costs nothing to set up
// (a MakerNote has a few dozen entries); a larger table gets the trees.
//
// The index holds one range per entry (its out-of-line value, or the table of
// the sub-IFD it addresses inline; an entry has at most one), in two sorted
// arrays, the starts and the ends of the ranges, with a Fenwick tree
// (P. M. Fenwick, "A new data structure for cumulative frequency tables", 1994)
// over each that counts the ranges still in force. A range [s, e) of a query
// overlaps the ranges whose start is before e, less those whose end is at or
// before s (such a range starts before s, so it is among the first ones); that
// is a difference of two prefix counts.

import "sort"

// maxMakerNoteGrowth bounds the bytes that one edit adds to a MakerNote: the
// size of the largest value and the two pad bytes that a value may need. An edit of many entries that would add more (a
// large value written once for each of thousands of entries of a malformed
// note) is refused instead of allocating a multiple of the value.
const maxMakerNoteGrowth = maxMakerNoteValue + 2

// mnIndexThreshold is the number of entries from which the index uses sorted
// arrays and trees instead of a scan of the ranges.
const mnIndexThreshold = 64

// maxMakerNoteExtraRanges bounds the ranges that an edit adds to the index
// beyond the ones the note had: the sub-IFD that a replaced pointer addresses
// (replacing the value of a sub-IFD tag is the only edit that creates one).
const maxMakerNoteExtraRanges = 64

// mnRange is the range [start, end) of the bytes of a note; end == 0 is no range.
type mnRange struct{ start, end int }

// overlaps reports whether r and [start, end) have a byte in common.
func (r mnRange) overlaps(start, end int) bool { return r.end != 0 && start < r.end && r.start < end }

// mnOwn is the range of an entry, and whether it is in force.
type mnOwn struct {
	mnRange
	active bool
}

// mnRangeIndex answers whether a range of the bytes of a note overlaps the
// range of an entry that is still in force. See the file comment.
type mnRangeIndex struct {
	own      []mnOwn   // the range of each entry of the top IFD, by position in the table
	ranked   bool      // the sorted arrays and the trees are built
	starts   []int     // the starts of the ranges, sorted
	ends     []int     // the ends of the ranges, sorted
	startPos []int     // position of the range of an entry in starts (1-based, for the tree)
	endPos   []int     // position of the range of an entry in ends (1-based)
	startCnt []int     // Fenwick tree over starts: the ranges in force
	endCnt   []int     // Fenwick tree over ends
	extra    []mnRange // ranges added by the edit, in force
	// appendFrom is the offset where the edit first added a value (0: none); the
	// bytes from it to the end of the values are protected as a whole.
	appendFrom int
}

// newMnRangeIndex indexes the entries of the top IFD of m.buf as they are.
func (m *mnEdit) newMnRangeIndex() mnRangeIndex {
	n := m.count()
	ix := mnRangeIndex{own: make([]mnOwn, n)}
	nLive := 0
	for i := range n {
		ep := m.entryAt(i)
		s, e, ok := m.entryRange(ep)
		if ok && e > s {
			ix.own[i] = mnOwn{mnRange{s, e}, true}
			nLive++
		}
	}
	if n <= mnIndexThreshold {
		return ix
	}
	live := make([]int, 0, nLive) // the entries that have a range
	for i := range ix.own {
		if ix.own[i].active {
			live = append(live, i)
		}
	}
	ix.ranked = true
	ix.startPos, ix.startCnt, ix.starts = rankRanges(live, n, func(i int) int { return ix.own[i].start })
	ix.endPos, ix.endCnt, ix.ends = rankRanges(live, n, func(i int) int { return ix.own[i].end })
	return ix
}

// rankRanges sorts the entries live by key and returns, for each entry, its
// 1-based position in the order, a Fenwick tree that counts every one of them,
// and the sorted keys.
func rankRanges(live []int, n int, key func(int) int) (pos, tree, sorted []int) {
	order := append([]int(nil), live...)
	sort.Slice(order, func(a, b int) bool { return key(order[a]) < key(order[b]) })
	pos = make([]int, n)
	sorted = make([]int, len(order))
	for p, i := range order {
		pos[i] = p + 1
		sorted[p] = key(i)
	}
	tree = make([]int, len(order)+1)
	for p := 1; p <= len(order); p++ { // linear-time build: every position holds 1
		tree[p]++
		if q := p + p&-p; q <= len(order) {
			tree[q] += tree[p]
		}
	}
	return pos, tree, sorted
}

// fenwickAdd adds d at the 1-based position p.
func fenwickAdd(tree []int, p, d int) {
	for ; p < len(tree); p += p & -p {
		tree[p] += d
	}
}

// fenwickSum returns the sum of the positions 1..p.
func fenwickSum(tree []int, p int) int {
	s := 0
	for ; p > 0; p -= p & -p {
		s += tree[p]
	}
	return s
}

// overlapsOther reports whether [start, end) overlaps the range of an entry in
// force other than skip, or a range the edit added.
func (ix *mnRangeIndex) overlapsOther(start, end, skip int) bool {
	if start >= end {
		return false
	}
	if ix.overlapsEntry(start, end, skip) {
		return true
	}
	for _, r := range ix.extra {
		if r.overlaps(start, end) {
			return true
		}
	}
	return false
}

// overlapsEntry reports whether [start, end) overlaps the range of an entry in
// force other than skip: by a scan of the ranges for a small table, and by
// counting through the trees for a large one.
func (ix *mnRangeIndex) overlapsEntry(start, end, skip int) bool {
	if !ix.ranked {
		for i := range ix.own {
			if o := &ix.own[i]; i != skip && o.active && o.overlaps(start, end) {
				return true
			}
		}
		return false
	}
	before := sort.SearchInts(ix.starts, end) // ranges that start before end
	upTo := sort.Search(len(ix.ends), func(i int) bool { return ix.ends[i] > start })
	n := fenwickSum(ix.startCnt, before) - fenwickSum(ix.endCnt, upTo)
	if skip >= 0 && skip < len(ix.own) && ix.own[skip].active && ix.own[skip].overlaps(start, end) {
		n--
	}
	return n > 0
}

// release takes the range of entry i out of force: its bytes are no longer
// used by it (the entry was deleted, or its value moved or became inline).
func (ix *mnRangeIndex) release(i int) {
	if i < 0 || i >= len(ix.own) || !ix.own[i].active {
		return
	}
	ix.own[i].active = false
	if ix.ranked {
		fenwickAdd(ix.startCnt, ix.startPos[i], -1)
		fenwickAdd(ix.endCnt, ix.endPos[i], -1)
	}
}

// appended reports whether [start, end) overlaps the bytes that the edit added
// to the note, which end at valuesEnd.
func (ix *mnRangeIndex) appended(start, end, valuesEnd int) bool {
	return ix.appendFrom != 0 && start < valuesEnd && ix.appendFrom < end
}

// entryRange returns the range of the entry at offset ep: its out-of-line value,
// or the table of the sub-IFD it addresses inline.
func (m *mnEdit) entryRange(ep int) (start, end int, ok bool) {
	if start, end, ok = m.valueRange(ep); ok {
		return start, end, true
	}
	return m.subIFDExtent(ep)
}

// refresh updates the index for entry k, whose bytes an edit has changed: the
// range the entry has now replaces the one it had. It returns false when the
// edit would exceed maxMakerNoteExtraRanges.
func (m *mnEdit) refresh(k int) bool {
	ix := &m.ix
	ep := m.entryAt(k)
	cur, curEnd, ok := m.entryRange(ep)
	if ix.own[k].active && ok && ix.own[k].start == cur && curEnd <= ix.own[k].end {
		// Written in place, in the whole value or in the first bytes of it. The
		// range stays as it was, so the bytes that a smaller value gave up stay
		// protected: the index is then at least as protective as asking each entry
		// as it is, and never less.
		return true
	}
	ix.release(k)
	// A value moved to the end of the note is covered by the appended range.
	if ok && curEnd > cur && (ix.appendFrom == 0 || cur < ix.appendFrom) {
		if len(ix.extra) >= maxMakerNoteExtraRanges {
			return false
		}
		ix.extra = append(ix.extra, mnRange{cur, curEnd})
	}
	return true
}
