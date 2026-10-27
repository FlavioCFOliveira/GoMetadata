package trailer

// source.go — the trailer finders, written once over a Source so that a region
// held in memory (PlanFromRegion; the TIFF writer) and a region that is read
// from the file as needed (PlanFromSource; the JPEG writer, task #393) find the
// same trailers by the same rules. All offsets here are absolute offsets of the
// file. A read that falls outside the region the finders look at is the same
// as the bytes not being there: the candidate is rejected, as the bounds checks
// of the in-memory implementation rejected it.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
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

// index returns the offset of the first occurrence of pat at or after from
// inside the view, or -1.
func (v view[S]) index(from int64, pat []byte) (int64, error) {
	if from < v.base {
		from = v.base
	}
	if v.end-from < int64(len(pat)) {
		return -1, nil
	}
	return v.src.Index(from, v.end, pat)
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

// Index implements Source.
func (r regionSource) Index(from, to int64, pat []byte) (int64, error) {
	i := bytes.Index(r.b[from-r.base:to-r.base], pat)
	if i < 0 {
		return -1, nil
	}
	return from + int64(i), nil
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

// collectAFCPSigs appends to dst the AFCP signatures of the view in ascending
// order of offset. It returns nil when there are more than cap(dst); a
// non-nil dst[:0] therefore always yields a non-nil result.
func collectAFCPSigs[S Source](dst []afcpSig, v view[S]) ([]afcpSig, error) {
	sigs := dst[:0]
	from := v.base
	for from < v.end {
		at, err := v.index(from, afcpSigPrefix)
		if err != nil {
			return nil, err
		}
		if at < 0 {
			break
		}
		from = at + 1
		quad, ok, err := v.at(at, 4)
		if err != nil {
			return nil, err
		}
		if !ok || (quad[3] != '!' && quad[3] != '*') {
			continue
		}
		if len(sigs) == cap(sigs) {
			return nil, nil
		}
		sigs = append(sigs, afcpSig{at: at, kind: quad[3]})
		from = at + 4 // a signature cannot overlap another one
	}
	return sigs, nil
}

// scanAFCP returns the AFCP trailer of the view and whether there is one: see
// FindAFCP.
func scanAFCP[S Source](v view[S]) (t *AFCP, found bool, err error) {
	var buf [maxAFCPSignatures]afcpSig
	sigs, err := collectAFCPSigs(buf[:0], v)
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

// scanFotoStation returns the FotoStation records that follow each other from
// the absolute offset from, and the range they cover: see FindFotoStation.
func scanFotoStation[S Source](v view[S], from int64) (recs []FotoRecord, start, end int64, err error) {
	cur := from
	for cur >= v.base && cur <= v.end {
		rec, next, ok, err := nextFotoRecord(v, cur)
		if err != nil {
			return nil, 0, 0, err
		}
		if !ok {
			break
		}
		recs = append(recs, rec)
		cur = next
	}
	if len(recs) == 0 {
		return nil, 0, 0, nil
	}
	return recs, from, cur, nil
}

// nextFotoRecord returns the FotoStation record that starts at the absolute
// offset cur and the offset where the next one starts. A record is found by its
// footer, the first signature at or after cur.
func nextFotoRecord[S Source](v view[S], cur int64) (rec FotoRecord, next int64, ok bool, err error) {
	at, err := v.index(cur, fotoSig)
	if err != nil || at < 0 || at-cur < 6 {
		return rec, 0, false, err
	}
	footer := at - 6
	foot, _, err := v.at(footer, 6)
	if err != nil {
		return rec, 0, false, err
	}
	size := int64(binary.BigEndian.Uint32(foot[2:]))
	if size < 10 || footer+10-size != cur {
		return rec, 0, false, nil
	}
	data, _, err := v.at(cur, footer-cur)
	if err != nil {
		return rec, 0, false, err
	}
	return FotoRecord{Tag: binary.BigEndian.Uint16(foot), Data: data}, footer + 10, true, nil
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
func scanSamsungQDIO[S Source](v view[S]) (sp Span, audioStart int64, ok bool, err error) {
	for from := v.base; ; {
		q, err := v.index(from, samsungQDIOSig) // offset of the type field of the QDIO block
		if err != nil {
			return sp, audioStart, false, err
		}
		if q < 0 {
			return sp, audioStart, ok, nil
		}
		from = q + 1
		cand, start, valid, err := samsungSpanAt(v, q)
		if err != nil {
			return sp, audioStart, false, err
		}
		if valid {
			sp, audioStart, ok = cand, start, true
		}
	}
}

// samsungSpanAt validates the QDIO block whose type field starts at the
// absolute offset q and returns the span of its audio offsets and the start of
// the audio file.
func samsungSpanAt[S Source](v view[S], q int64) (sp Span, audioStart int64, ok bool, err error) {
	blockStart := q - samsungQDIOBlockLen + 4 // start of the QDIO data
	if blockStart < v.base+12 || q+int64(len(samsungQDIOSig)) > v.end {
		return Span{}, 0, false, nil
	}
	// The bytes from 8 before the QDIO data to the end of the signature: the
	// length of the directory block and its type, the QDIO data, and the type
	// of the QDIO block.
	win, ok, err := v.at(blockStart-8, q+int64(len(samsungQDIOSig))-(blockStart-8))
	if err != nil || !ok {
		return Span{}, 0, false, err
	}
	if ok, err = samsungBlocksValid(v, win, blockStart, q); err != nil || !ok {
		return Span{}, 0, false, err
	}
	data := win[8 : 8+samsungQDIODataLen]
	start := int64(binary.LittleEndian.Uint32(data[samsungOffsetsAt:]))
	end := int64(binary.LittleEndian.Uint32(data[samsungOffsetsAt+4:]))
	if start < v.base || end < start || end > blockStart {
		return Span{}, 0, false, nil
	}
	return samsungSpan(blockStart+samsungOffsetsAt, start, end), start, true, nil
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

// samsungBlocksValid reports whether the QDIO block with its data at blockStart
// and its type field at q has the layout of the package comment, and follows a
// directory block ("SEFT", whose data starts with "SEFH"). win holds the bytes
// from blockStart-8 to the end of the QDIO signature.
func samsungBlocksValid[S Source](v view[S], win []byte, blockStart, q int64) (bool, error) {
	le := binary.LittleEndian
	qdio := win[8:] // the QDIO data and the type field of the block
	if string(qdio[:4]) != "QDIO" || le.Uint32(win[q-4-(blockStart-8):]) != samsungQDIODataLen {
		return false, nil
	}
	if !bytes.Equal(win[4:8], samsungSEFT) {
		return false, nil
	}
	dirLen := int64(le.Uint32(win))
	rel := blockStart - v.base // blockStart relative to the view, as the in-memory region was indexed
	if dirLen < 12 || dirLen > rel-8 {
		return false, nil
	}
	head, ok, err := v.at(blockStart-8-dirLen, 4)
	if err != nil || !ok {
		return false, err
	}
	return bytes.Equal(head, samsungSEFH), nil
}
