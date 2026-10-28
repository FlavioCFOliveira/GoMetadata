package trailer

// source.go — the trailer finders, written once over a Source so that a region
// held in memory (PlanFromRegion; the TIFF writer) and a region that is read
// from the file as needed (PlanFromSource; the JPEG writer, task #393) find the
// same trailers by the same rules. All offsets here are absolute offsets of the
// file. A read that falls outside the region the finders look at is the same
// as the bytes not being there: the candidate is rejected, as the bounds checks
// of the in-memory implementation rejected it.
//
// Each finder reads the region once, forward (Source.IndexEach), whatever the
// number of candidates it holds: a read for each candidate made a region of
// short ones quadratic (task #417).

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// view is the part [base, end) of a Source that the finders look at.
type view[S Source] struct {
	src       S
	base, end int64
}

// at returns the n bytes at the absolute offset off, or false when they are not
// all inside the view.
func (v view[S]) at(off, n int64) ([]byte, bool, error) {
	if n < 0 || off < v.base || off > v.end || n > v.end-off {
		return nil, false, nil
	}
	b, err := v.src.Range(off, n)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// indexEach is Source.IndexEach for the part of the Source that the view holds:
// the search starts at the base of the view, or at from when that is later.
func (v view[S]) indexEach(from int64, pat []byte, behind, ahead int, vis iobuf.ScanVisitor) error {
	if from < v.base {
		from = v.base
	}
	if v.end-from < int64(len(pat)) {
		return nil
	}
	return v.src.IndexEach(from, v.end, pat, behind, ahead, vis)
}

// indexEachBytes is Source.IndexEach over b, whose first byte is at the
// absolute offset base: bytes.Index from each starting point, the reference that
// iobuf.ScanRange follows.
func indexEachBytes(b []byte, base, from, to int64, pat []byte, behind, ahead int, vis iobuf.ScanVisitor) error {
	for searchAt := from; searchAt < to && to-searchAt >= int64(len(pat)); {
		i := bytes.Index(b[searchAt-base:to-base], pat)
		if i < 0 {
			return nil
		}
		at := searchAt + int64(i)
		var win []byte
		if at-int64(behind) >= from && at+int64(ahead) <= to {
			win = b[at-int64(behind)-base : at+int64(ahead)-base]
		}
		next, err := vis.Visit(at, win)
		if err != nil || next < 0 {
			return err
		}
		searchAt = max(next, at+1)
	}
	return nil
}

// regionSource is a Source over a region held in memory that starts at the
// absolute offset base.
type regionSource struct {
	b    []byte
	base int64
}

// Range implements Source.
func (r regionSource) Range(off, n int64) ([]byte, error) {
	return r.b[off-r.base : off-r.base+n : off-r.base+n], nil
}

// IndexEach implements Source.
func (r regionSource) IndexEach(from, to int64, pat []byte, behind, ahead int, vis iobuf.ScanVisitor) error {
	return indexEachBytes(r.b, r.base, from, to, pat, behind, ahead, vis)
}

// CopyRange implements Source.
func (r regionSource) CopyRange(w io.Writer, off, n int64) error {
	if n <= 0 {
		return nil
	}
	_, err := w.Write(r.b[off-r.base : off-r.base+n])
	return err
}

// afcpSig is the offset and the fourth byte ('!' or '*') of an AFCP signature.
type afcpSig struct {
	at   int64
	kind byte
}

// afcpScan is the state of the search for the AFCP signatures of a view. It is
// pooled: the search allocates nothing in the usual case (task #417).
type afcpScan struct {
	buf  [maxAFCPSignatures]afcpSig
	sigs []afcpSig
	over bool // there are more signatures than buf holds
}

var afcpScanPool = sync.Pool{New: func() any { return new(afcpScan) }} //nolint:gochecknoglobals // pool of scan states

// Visit implements iobuf.ScanVisitor for an "AXS" candidate: quad is the
// candidate and the byte that follows it, nil when that is outside the view.
func (st *afcpScan) Visit(at int64, quad []byte) (int64, error) {
	if len(quad) < 4 || (quad[3] != '!' && quad[3] != '*') {
		return at + 1, nil
	}
	if len(st.sigs) == cap(st.sigs) {
		st.over = true
		return -1, nil
	}
	st.sigs = append(st.sigs, afcpSig{at: at, kind: quad[3]})
	return at + 4, nil // a signature cannot overlap another one
}

// collectAFCPSigs returns, with the state st, the AFCP signatures of the view in ascending order of offset,
// valid until st is reused. It returns nil when there are more than
// maxAFCPSignatures; the result of a search that finds none is not nil.
//
// The view is scanned once (task #417): a candidate "AXS" that is not followed
// by '!' or '*' costs the bytes it covers, not a read of its own, so a region
// made of such candidates is linear in its size.
func collectAFCPSigs[S Source](st *afcpScan, v view[S]) ([]afcpSig, error) {
	st.sigs, st.over = st.buf[:0], false
	if err := v.indexEach(v.base, afcpSigPrefix, 0, 4, st); err != nil || st.over {
		return nil, err
	}
	return st.sigs, nil
}

// scanAFCP returns the AFCP trailer of the view and whether there is one: see
// FindAFCP.
func scanAFCP[S Source](v view[S]) (t *AFCP, found bool, err error) {
	st := afcpScanPool.Get().(*afcpScan) //nolint:forcetypeassert,revive // the pool holds only *afcpScan
	defer afcpScanPool.Put(st)
	sigs, err := collectAFCPSigs(st, v)
	if err != nil || sigs == nil {
		return nil, false, err // more than maxAFCPSignatures signatures: see the constant
	}
	for ci, sig := range sigs {
		t, ok, err := parseAFCP(v, sig.at, sigs[ci+1:])
		if err != nil || ok {
			return t, ok, err
		}
	}
	return nil, false, nil
}

// parseAFCP parses the candidate AFCP trailer at the absolute offset at. later
// holds the signatures that follow at, the places the EOF record can be.
func parseAFCP[S Source](v view[S], at int64, later []afcpSig) (t *AFCP, ok bool, err error) {
	rem := v.end - at // bytes of the view from the candidate
	if rem < 24 {     // header and EOF record
		return nil, false, nil
	}
	hdr, ok, err := v.at(at, 12)
	if err != nil || !ok {
		return nil, false, err
	}
	t = &AFCP{Start: at}
	copy(t.Sig[:], hdr[:4])
	t.Order = binary.LittleEndian
	if hdr[3] == '!' {
		t.Order = binary.BigEndian
	}
	copy(t.Version[:], hdr[4:6])
	count := int64(t.Order.Uint16(hdr[6:]))
	dataEnd, dir, ok, err := checkDirectory(v, t, count, rem)
	if err != nil || !ok {
		return nil, false, err
	}
	rec, ok, err := findEOFRecord(v, t, dataEnd, later)
	if err != nil || !ok {
		return nil, false, err
	}
	t.End = t.Start + rec + 12
	return t, true, readEntries(v, t, dir, count) // only a trailer that is accepted reads the data
}

// checkDirectory validates the directory entries of the trailer t, which has
// count entries and rem bytes in the view, and returns the end of the data they
// name, relative to the trailer, and the directory itself. The entries must lie
// after the directory and inside the view.
func checkDirectory[S Source](v view[S], t *AFCP, count, rem int64) (dataEnd int64, dir []byte, ok bool, err error) {
	dirEnd := 12 + 12*count
	if count > maxAFCPEntries || dirEnd+12 > rem {
		return 0, nil, false, nil
	}
	dir, ok, err = v.at(t.Start+12, 12*count)
	if err != nil || !ok {
		return 0, nil, false, err
	}
	dataEnd = dirEnd
	for i := range count {
		d := dir[12*i:]
		n := int64(t.Order.Uint32(d[4:]))
		off := int64(t.Order.Uint32(d[8:])) - t.Start // relative to the trailer
		if off < dirEnd || n > rem || off > rem-n {
			return 0, nil, false, nil
		}
		dataEnd = max(dataEnd, off+n)
	}
	return dataEnd, dir, true, nil
}

// readEntries fills t.Entries from the directory dir that checkDirectory
// accepted.
func readEntries[S Source](v view[S], t *AFCP, dir []byte, count int64) error {
	if count == 0 {
		return nil
	}
	t.Entries = make([]AFCPEntry, count)
	for i := range count {
		d := dir[12*i:]
		e := &t.Entries[i]
		copy(e.Tag[:], d[:4])
		n := int64(t.Order.Uint32(d[4:]))
		off := int64(t.Order.Uint32(d[8:]))
		data, _, err := v.at(off, n) // inside the view: checkDirectory
		if err != nil {
			return err
		}
		e.Data = data
	}
	return nil
}

// findEOFRecord returns the offset, relative to the trailer, of the EOF
// record: a record that follows the data (a gap with other bytes is allowed),
// carries the same signature as the header, and carries the offset of the
// header. later lists the signatures after the header, so the search never
// rescans the region.
func findEOFRecord[S Source](v view[S], t *AFCP, dataEnd int64, later []afcpSig) (int64, bool, error) {
	for _, rec := range later {
		rel := rec.at - t.Start
		if rel < dataEnd || rec.kind != t.Sig[3] {
			continue
		}
		if rec.at+12 > v.end {
			return 0, false, nil // this and every later signature is too close to the end
		}
		b, _, err := v.at(rec.at+4, 4)
		if err != nil {
			return 0, false, err
		}
		if int64(t.Order.Uint32(b)) == t.Start {
			return rel, true, nil
		}
	}
	return 0, false, nil
}

// fotoScan is the state of the walk along the FotoStation records of a view.
// It is pooled (task #417).
type fotoScan struct {
	cur     int64        // where the record being looked for starts
	recs    []FotoRecord // the records found
	footers []int64      // offset of the footer of each record
}

var fotoScanPool = sync.Pool{New: func() any { return new(fotoScan) }} //nolint:gochecknoglobals // pool of scan states

// Visit implements iobuf.ScanVisitor for a FotoStation signature: win is the
// footer (tag, size) and the signature that ends it.
func (st *fotoScan) Visit(at int64, win []byte) (int64, error) {
	if win == nil || at-st.cur < 6 {
		return -1, nil
	}
	footer := at - 6
	size := int64(binary.BigEndian.Uint32(win[2:]))
	if size < 10 || footer+10-size != st.cur {
		return -1, nil
	}
	st.recs = append(st.recs, FotoRecord{Tag: binary.BigEndian.Uint16(win)})
	st.footers = append(st.footers, footer)
	st.cur = footer + 10
	return st.cur, nil
}

// scanFotoStation returns the FotoStation records that follow each other from
// the absolute offset from, and the range they cover: see FindFotoStation.
//
// A record is found by its footer, the first signature at or after the end of
// the previous record, so the chain is one forward pass over the view (task
// #417: a read per record is quadratic when the records are short). The pass
// takes the tag and the size from the bytes around each signature; the data of
// the records, which is contiguous, is then read once.
func scanFotoStation[S Source](v view[S], from int64) (recs []FotoRecord, start, end int64, err error) {
	if from < v.base || from > v.end {
		return nil, 0, 0, nil
	}
	st := fotoScanPool.Get().(*fotoScan) //nolint:forcetypeassert,revive // the pool holds only *fotoScan
	defer func() {
		st.recs, st.footers = nil, st.footers[:0] // the records belong to the caller
		fotoScanPool.Put(st)
	}()
	st.cur, st.recs, st.footers = from, nil, st.footers[:0]
	if err = v.indexEach(from, fotoSig, 6, len(fotoSig), st); err != nil || len(st.recs) == 0 {
		return nil, 0, 0, err
	}
	blob, _, err := v.at(from, st.cur-from) // inside the view: every footer is
	if err != nil {
		return nil, 0, 0, err
	}
	rs := from
	for i, footer := range st.footers {
		st.recs[i].Data = blob[rs-from : footer-from : footer-from]
		rs = footer + 10
	}
	return st.recs, from, st.cur, nil
}

// scanSamsungQDIO returns the span of the two absolute audio offsets of the
// Samsung trailer of the view, the absolute offset of the start of the audio
// file, or false when there is no such trailer. The QDIO block is found by its
// end, "QDIO" + "BS", and accepted only when it has the layout of the package
// comment and follows a directory block ("SEFT" whose data starts with "SEFH");
// the last such block of the view is used. The audio file offsets are rewritten
// only when the audio file lies in the view (at or after its base, before the
// QDIO block), as it does in a file written by the camera: anything else is not
// understood and is left alone.
//
// The view is scanned once (task #417): the checks that need only the bytes
// around the signature run during the scan, and the one read that does not (the
// head of the directory block) runs afterwards for the candidates that passed
// them, newest first, so a region of signatures costs time proportional to its
// size. Only the last maxSamsungCandidates candidates are kept: a file that
// has more block-shaped candidates than that is not one a camera wrote, and its
// last valid block is looked for among the newest of them.
func scanSamsungQDIO[S Source](v view[S]) (sp Span, audioStart int64, ok bool, err error) {
	st := samsungScanPool.Get().(*samsungScan) //nolint:forcetypeassert,revive // the pool holds only *samsungScan
	defer samsungScanPool.Put(st)
	st.base, st.cands, st.total = v.base, st.cands[:0], 0
	if scanErr := v.indexEach(v.base, samsungQDIOSig, samsungQDIOBlockLen+4, len(samsungQDIOSig), st); scanErr != nil {
		return sp, audioStart, false, scanErr
	}
	for i := st.total - 1; i >= 0 && i >= st.total-maxSamsungCandidates; i-- {
		c := st.cands[i%maxSamsungCandidates]
		head, inView, err := v.at(c.blockStart-8-c.dirLen, 4)
		if err != nil {
			return sp, audioStart, false, err
		}
		if inView && bytes.Equal(head, samsungSEFH) {
			return samsungSpan(c.blockStart+samsungOffsetsAt, c.start, c.end), c.start, true, nil
		}
	}
	return sp, audioStart, false, nil
}

// samsungScan is the state of the search for the QDIO blocks of a view. It is
// pooled (task #417).
type samsungScan struct {
	base  int64         // the base of the view
	cands []samsungCand // the candidates, the newest overwriting the oldest once there are maxSamsungCandidates
	total int           // the candidates found
}

var samsungScanPool = sync.Pool{New: func() any { return new(samsungScan) }} //nolint:gochecknoglobals // pool of scan states

// Visit implements iobuf.ScanVisitor for a "QDIOBS" signature at q, with the
// window that samsungCandidateAt takes.
func (st *samsungScan) Visit(q int64, win []byte) (int64, error) {
	if c, valid := samsungCandidateAt(st.base, q, win); valid {
		if len(st.cands) < maxSamsungCandidates {
			st.cands = append(st.cands, c)
		} else {
			st.cands[st.total%maxSamsungCandidates] = c
		}
		st.total++
	}
	return q + 1, nil
}

// maxSamsungCandidates bounds the QDIO block candidates that scanSamsungQDIO
// keeps for the final check.
const maxSamsungCandidates = 256

// samsungCand is a candidate QDIO block that passed the checks of
// samsungCandidateAt.
type samsungCand struct {
	blockStart int64 // start of the QDIO data
	dirLen     int64 // length of the directory block
	start, end int64 // the audio file
}

// samsungCandidateAt checks the QDIO block whose type field starts at the
// absolute offset q against the layout of the package comment, using win, the
// bytes from 8 before the QDIO data to the end of the signature (the length of
// the directory block and its type, the QDIO data, and the type of the QDIO
// block), or nil when they are not all inside the view that starts at base.
// Only the read of the head of the directory block is left to the caller.
func samsungCandidateAt(base, q int64, win []byte) (samsungCand, bool) {
	blockStart := q - samsungQDIOBlockLen + 4 // start of the QDIO data
	if win == nil || blockStart < base+12 || !samsungWindowValid(win, q, blockStart) {
		return samsungCand{}, false
	}
	le := binary.LittleEndian
	dirLen := int64(le.Uint32(win))
	rel := blockStart - base // blockStart relative to the view, as the in-memory region was indexed
	if dirLen < 12 || dirLen > rel-8 {
		return samsungCand{}, false
	}
	data := win[8 : 8+samsungQDIODataLen]
	start := int64(le.Uint32(data[samsungOffsetsAt:]))
	end := int64(le.Uint32(data[samsungOffsetsAt+4:]))
	if start < base || end < start || end > blockStart {
		return samsungCand{}, false
	}
	return samsungCand{blockStart: blockStart, dirLen: dirLen, start: start, end: end}, true
}

// samsungWindowValid reports whether win (see samsungCandidateAt) holds a QDIO
// block of the layout of the package comment, preceded by the type of a
// directory block ("SEFT").
func samsungWindowValid(win []byte, q, blockStart int64) bool {
	qdio := win[8:] // the QDIO data and the type field of the block
	return string(qdio[:4]) == "QDIO" &&
		binary.LittleEndian.Uint32(win[q-4-(blockStart-8):]) == samsungQDIODataLen &&
		bytes.Equal(win[4:8], samsungSEFT)
}

// samsungSpan returns the span of the two absolute audio offsets, which are at
// spanStart and name the audio file [start, end): it builds the offsets for the
// position the span takes in the output.
func samsungSpan(spanStart, start, end int64) Span {
	le := binary.LittleEndian
	return Span{Start: spanStart, End: spanStart + 8, Build: func(outPos int64) ([]byte, bool, error) {
		delta := outPos - spanStart
		if delta == 0 {
			return nil, false, nil
		}
		ns, ne := start+delta, end+delta
		if ns < 0 || ne > math.MaxUint32 {
			return nil, false, fmt.Errorf("%w: Samsung audio file at %d-%d", ErrOverflow, ns, ne)
		}
		var b [8]byte
		le.PutUint32(b[:], uint32(ns))  //nolint:gosec // G115: bounded by the check above
		le.PutUint32(b[4:], uint32(ne)) //nolint:gosec // G115: bounded by the check above
		return b[:], true, nil
	}}
}
