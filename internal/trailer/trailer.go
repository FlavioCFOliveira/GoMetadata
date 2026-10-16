// Package trailer rewrites the trailers that some image files carry after
// their image data, so that a metadata write keeps them valid.
//
// Three trailers are handled (task #322 for the first two, #353 for the
// third). The first two hold a second copy of the IPTC-IIM record next to the
// one in the main metadata: the AXS File Concatenation Protocol (AFCP) trailer
// and the FotoWare FotoStation trailer. A write that replaces the IIM would
// leave both copies with the old values, and would leave the absolute offsets
// of the AFCP trailer pointing at the wrong bytes once the data before it
// changes size. The third, the Samsung "Sound & Shot" trailer, holds absolute
// offsets of its embedded audio file. Plan rewrites the IIM copies and every
// absolute offset for the position the trailer takes in the output (the
// policy chosen for #322: update the copies, as ExifTool does; CLAUDE.md §5: a
// write must not corrupt other embedded structures). Every other trailer is
// copied byte for byte by the caller; see the "Trailers that need no
// rewrite" note below.
//
// The package is shared by the JPEG writer (the trailers follow the EOI
// marker) and the TIFF writer (the trailers follow all TIFF data).
//
// Formats, from the AFCP specification (AXS File Concatenation Protocol,
// April 1, 1992, Optical Technology Resource, Inc.; archived at
// http://www.tocarte.com/media/axs_afcp_spec.pdf), whose diagram this code
// follows, and from the FotoStation and Samsung layouts that ExifTool
// documents (lib/Image/ExifTool/FotoStation.pm and Samsung.pm, sub
// ProcessSamsung; study references only, no ExifTool code is used):
//
//	AFCP trailer = header | directory | data | EOF record
//	header       = "AXS!" (Motorola byte order) or "AXS*" (Intel byte order)
//	             | version (2) | directory entry count (2) | reserved (4)
//	directory    = count * (data set identifier (4 ASCII) | byte count (4)
//	             | offset (4), counted from the start of the file)
//	EOF record   = "AXS!" or "AXS*" | offset of the header (4) | reserved (4)
//
//	FotoStation trailer = records, each: data | tag (2, big endian)
//	             | record size including these 10 bytes (4, big endian)
//	             | 0xA1B2C3D4
//
//	Samsung trailer = blocks, the last two being the directory and the QDIO
//	             block; every block is: data | data length (4) | type (4),
//	             all integers little endian
//	directory    = type "SEFT"; data = "SEFH" | version (4) | entry count (4)
//	             | count * (0 (2) | type (2) | offset (4) | size (4)); an
//	             offset counts back from the start of the directory data to
//	             the start of the block it names (relative: it needs no
//	             rewrite when the trailer moves)
//	QDIO block   = type "QDIO"; data (20) = "QDIO" | version (4) | 1 (4)
//	             | offset of the audio file start (4) | offset of the audio
//	             file end (4), both ABSOLUTE offsets from the start of the
//	             file; the block is followed by the 2 bytes "BS"
//
// The Samsung audio file, the blocks after it and the QDIO block form one rigid
// piece: the two audio offsets are shifted by the movement of the QDIO block,
// which names the same audio bytes only when everything from the start of the
// audio file to the QDIO block is copied unchanged and in one piece. An AFCP or
// FotoStation trailer that intersects that piece is therefore part of the audio
// file as far as the Samsung trailer is concerned and is copied unchanged
// (task #361). Such a layout is not written by any producer known to the
// formats (ExifTool Samsung.pm ProcessSamsung derives the audio position from
// the directory of blocks, which the audio file always precedes), so the rule
// only has to keep the audio addressable, never to rewrite a trailer inside it.
// An AFCP or FotoStation trailer before the piece or after it is rewritten as
// usual and the offsets stay valid: the movement of the QDIO block includes
// the size change of the trailers before it.
//
// The data set "IPTC" (FotoStation: tag 1) holds the IIM stream as is. The
// reserved fields are written as zero, as ExifTool does: the specification
// leaves them for a validation checksum that it does not define.
//
// Trailers that need no rewrite. CanonVRD ("CANON OPTIONAL DATA": a header
// and a footer that each hold the length of the data between them, followed by
// length-prefixed records), PhotoMechanic (a footer holding the trailer
// length), Vivo and OnePlus (offsets counted back from the end of the
// trailer), and MIE (relative lengths) hold no absolute offset (ExifTool
// CanonVRD.pm ProcessCanonVRD, PhotoMechanic.pm, Trailer.pm, checked while
// writing #353), so copying them keeps them valid when they move.
//
// Scope: AFCP and FotoStation trailers are located only when they start
// inside the region the caller passes to PlanFromRegion (JPEG: everything
// after EOI) and only when their structure is consistent (an AFCP trailer
// whose EOF record points back at its header). Anything else is left to the
// caller to copy unchanged, as before.
package trailer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// MaxRegion bounds the bytes after the image data that are read to look for
// trailers (a preview image in an AFCP trailer is the largest legitimate
// content). A larger region is copied through unchanged.
const MaxRegion = 16 << 20

// maxAFCPEntries bounds the directory of an AFCP trailer that is accepted.
const maxAFCPEntries = 4096

// afcpIPTCTag is the data set identifier of the IPTC data of an AFCP trailer.
const afcpIPTCTag = "IPTC"

var (
	afcpSigBE = []byte("AXS!")                 //nolint:gochecknoglobals // package-level constant bytes
	afcpSigLE = []byte("AXS*")                 //nolint:gochecknoglobals // package-level constant bytes
	fotoSig   = []byte{0xA1, 0xB2, 0xC3, 0xD4} //nolint:gochecknoglobals // package-level constant bytes
)

// FotoSig is the 4-byte signature that ends every FotoStation record.
func FotoSig() []byte { return fotoSig }

// ErrOverflow is returned when a rewritten trailer would need an offset that
// does not fit the 32-bit fields of the trailer format.
var ErrOverflow = errors.New("trailer: offsets exceed 32 bits")

// Span is a byte range [Start, End) of the input that a writer replaces by the
// result of Build, evaluated with the position the range takes in the output.
// Build reports false when the range is to be copied unchanged.
type Span struct {
	Start, End int64
	Build      func(outPos int64) (b []byte, changed bool, err error)
}

// Plan lists the trailer ranges to rewrite, in input order. Region holds the
// bytes of the input from the absolute offset Base to its end, which the
// spans refer to. The zero value is an empty plan.
type Plan struct {
	Spans  []Span
	Region []byte
	Base   int64
}

// PlanFromRegion finds the trailers in region, the bytes of the file from the
// absolute offset base to its end, and plans the rewrite of their IPTC copies
// with newIPTC and of their absolute offsets. The plan has no spans when there
// is nothing to do.
//
// A nil newIPTC leaves the IPTC copies as they are (as for the raw profiles
// of PNG, task #314), but the absolute offsets are still rewritten when a
// trailer moves.
func PlanFromRegion(region []byte, base int64, newIPTC []byte) Plan {
	plan := Plan{Region: region, Base: base}
	afcp := FindAFCP(region, base)
	from := base
	if afcp != nil {
		plan.Spans = append(plan.Spans, afcp.span(newIPTC))
		from = afcp.End
	}
	if recs, start, end := FindFotoStation(region, base, from); recs != nil {
		plan.Spans = append(plan.Spans, fotoSpan(recs, start, end, newIPTC))
	}
	if afcp != nil {
		// A FotoStation trailer that precedes the AFCP trailer.
		if recs, start, end := FindFotoStation(region, base, base); recs != nil && end <= afcp.Start {
			plan.Spans = append(plan.Spans, fotoSpan(recs, start, end, newIPTC))
		}
	}
	// Task #361: the audio file, the blocks after it and the QDIO block move
	// as one rigid piece, so that the two offsets name the same audio bytes
	// when they are shifted by the movement of the QDIO block. Every other
	// span that intersects that piece (an AFCP or FotoStation trailer found
	// among the audio bytes or the directory) belongs to the audio file as far
	// as the Samsung trailer is concerned: it is copied unchanged, not
	// rewritten, because rewriting it could change the size of the piece. A
	// span before the piece or after it does not matter: the movement of the
	// QDIO block already includes the size change of the spans before it, and
	// the spans after it move nothing the offsets name.
	if sp, rigidStart, ok := findSamsungQDIO(region, base); ok {
		kept := plan.Spans[:0]
		for _, other := range plan.Spans {
			if other.End <= rigidStart || other.Start >= sp.End {
				kept = append(kept, other)
			}
		}
		plan.Spans = append(kept, sp)
	}
	plan.Spans = disjoint(sortSpans(plan.Spans))
	return plan
}

// sortSpans orders spans by start (at most a handful).
func sortSpans(s []Span) []Span {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Start < s[j-1].Start; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s
}

// disjoint drops every span that overlaps an earlier one of the sorted s, so
// that the spans of a plan never address the same bytes twice.
func disjoint(s []Span) []Span {
	out := s[:0]
	end := int64(math.MinInt64)
	for _, sp := range s {
		if sp.Start < end {
			continue
		}
		out = append(out, sp)
		end = sp.End
	}
	return out
}

// OutWriter counts the bytes written through it, so that the position of a
// trailer in the output is known when its absolute offsets are written.
type OutWriter struct {
	W io.Writer
	N int64
}

// Write writes p to the underlying writer and counts the bytes written.
func (c *OutWriter) Write(p []byte) (int, error) {
	n, err := c.W.Write(p)
	c.N += int64(n)
	return n, err
}

// WriteRegion writes the whole region of the plan to w: every span replaced by
// its rewritten bytes (or copied when it does not change) and the bytes
// between and after the spans copied as they are. out must be the writer that
// has seen every byte of the output so far (w itself, or w wrapped by out):
// the absolute offsets of a trailer are those of its position in the output.
func (p *Plan) WriteRegion(w io.Writer, out *OutWriter) error {
	cur := int64(0) // offset in Region of the first byte not yet written
	for _, sp := range p.Spans {
		s, e := sp.Start-p.Base, sp.End-p.Base
		if s < cur || e > int64(len(p.Region)) {
			continue // not inside the unwritten part of the region: leave it as it is
		}
		if _, err := w.Write(p.Region[cur:s]); err != nil {
			return fmt.Errorf("trailer: write: %w", err)
		}
		b, changed, err := sp.Build(out.N)
		if err != nil {
			return err
		}
		if !changed {
			b = p.Region[s:e]
		}
		if _, err := w.Write(b); err != nil {
			return fmt.Errorf("trailer: write: %w", err)
		}
		cur = e
	}
	if _, err := w.Write(p.Region[cur:]); err != nil {
		return fmt.Errorf("trailer: write: %w", err)
	}
	return nil
}

// Order is the byte order of an AFCP trailer: binary.BigEndian or
// binary.LittleEndian.
type Order interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// AFCPEntry is one directory entry of an AFCP trailer.
type AFCPEntry struct {
	Tag  [4]byte
	Data []byte
}

// AFCP is a parsed AFCP trailer; Start and End are absolute offsets.
type AFCP struct {
	Start, End int64
	Sig        [4]byte
	Order      Order
	Version    [2]byte
	Entries    []AFCPEntry
}

// FindAFCP returns the AFCP trailer in region, the bytes of the file from the
// absolute offset base to its end, or nil. A candidate must start with an
// AFCP signature, hold a directory whose entries lie after it, and be closed
// by an EOF record that carries the offset of the candidate (AFCP
// specification: the EOF record "points to AFCP header").
func FindAFCP(region []byte, base int64) *AFCP {
	for from := 0; from < len(region); {
		i := indexAFCPSig(region[from:])
		if i < 0 {
			return nil
		}
		at := from + i
		if t := parseAFCP(region, base, at); t != nil {
			return t
		}
		from = at + 1
	}
	return nil
}

// indexAFCPSig returns the index of the first AFCP signature of b, or -1.
func indexAFCPSig(b []byte) int {
	be, le := bytes.Index(b, afcpSigBE), bytes.Index(b, afcpSigLE)
	switch {
	case be < 0:
		return le
	case le < 0:
		return be
	}
	return min(be, le)
}

// parseAFCP parses the candidate AFCP trailer at region[at:], or returns nil.
func parseAFCP(region []byte, base int64, at int) *AFCP {
	rem := region[at:]
	if len(rem) < 24 { // header and EOF record
		return nil
	}
	t := &AFCP{Start: base + int64(at)}
	copy(t.Sig[:], rem[:4])
	t.Order = binary.LittleEndian
	if rem[3] == '!' {
		t.Order = binary.BigEndian
	}
	copy(t.Version[:], rem[4:6])
	dataEnd, ok := t.parseDirectory(rem)
	if !ok {
		return nil
	}
	rec, ok := t.findEOFRecord(rem, dataEnd)
	if !ok {
		return nil
	}
	t.End = t.Start + rec + 12
	return t
}

// parseDirectory reads the directory entries of the trailer whose bytes start
// at rem[0], and returns the end of the data they name, relative to the
// trailer. The entries must lie after the directory and inside rem.
func (t *AFCP) parseDirectory(rem []byte) (dataEnd int64, ok bool) {
	count := int(t.Order.Uint16(rem[6:]))
	dirEnd := 12 + 12*count
	if count > maxAFCPEntries || dirEnd+12 > len(rem) {
		return 0, false
	}
	dataEnd = int64(dirEnd)
	for i := range count {
		d := rem[12+12*i:]
		var e AFCPEntry
		copy(e.Tag[:], d[:4])
		n := int64(t.Order.Uint32(d[4:]))
		off := int64(t.Order.Uint32(d[8:])) - t.Start // relative to the trailer
		if off < int64(dirEnd) || n > int64(len(rem)) || off > int64(len(rem))-n {
			return 0, false
		}
		e.Data = rem[off : off+n]
		t.Entries = append(t.Entries, e)
		dataEnd = max(dataEnd, off+n)
	}
	return dataEnd, true
}

// findEOFRecord returns the offset, relative to the trailer, of the EOF
// record: a record that follows the data (a gap with other bytes is allowed)
// and carries the offset of the header.
func (t *AFCP) findEOFRecord(rem []byte, dataEnd int64) (int64, bool) {
	for q := dataEnd; q+12 <= int64(len(rem)); {
		j := bytes.Index(rem[q:], t.Sig[:])
		if j < 0 || q+int64(j)+12 > int64(len(rem)) {
			return 0, false
		}
		rec := q + int64(j)
		if int64(t.Order.Uint32(rem[rec+4:])) == t.Start {
			return rec, true
		}
		q = rec + 1
	}
	return 0, false
}

// span returns the range of the trailer and its replacement.
func (t *AFCP) span(newIPTC []byte) Span {
	return Span{Start: t.Start, End: t.End, Build: func(outPos int64) ([]byte, bool, error) {
		changed := outPos != t.Start
		for i := range t.Entries {
			if string(t.Entries[i].Tag[:]) == afcpIPTCTag && newIPTC != nil && !bytes.Equal(t.Entries[i].Data, newIPTC) {
				changed = true
			}
		}
		if !changed {
			return nil, false, nil
		}
		b, err := t.build(outPos, newIPTC)
		return b, true, err
	}}
}

// build returns the trailer as it is laid out at outPos in the output: the
// directory entries in their original order with fresh offsets, the "IPTC"
// data set replaced by newIPTC when that is not nil, and an EOF record that
// points at the new header position.
func (t *AFCP) build(outPos int64, newIPTC []byte) ([]byte, error) {
	dirLen := int64(12 * len(t.Entries))
	total := 12 + dirLen + 12
	for i := range t.Entries {
		total += int64(len(t.dataOf(i, newIPTC)))
	}
	if outPos < 0 || outPos+total > math.MaxUint32 {
		return nil, fmt.Errorf("%w: AFCP trailer at %d, %d bytes", ErrOverflow, outPos, total)
	}
	o := t.Order
	out := make([]byte, 0, total)
	out = append(out, t.Sig[:]...)
	out = append(out, t.Version[:]...)
	out = o.AppendUint16(out, uint16(len(t.Entries))) //nolint:gosec // G115: the count came from a 16-bit field
	out = append(out, 0, 0, 0, 0)                     // reserved
	off := uint32(outPos + 12 + dirLen)               //nolint:gosec // G115: bounded by the check above
	for i := range t.Entries {
		n := uint32(len(t.dataOf(i, newIPTC))) //nolint:gosec // G115: bounded by the check above
		out = append(out, t.Entries[i].Tag[:]...)
		out = o.AppendUint32(out, n)
		out = o.AppendUint32(out, off)
		off += n
	}
	for i := range t.Entries {
		out = append(out, t.dataOf(i, newIPTC)...)
	}
	out = append(out, t.Sig[:]...)
	out = o.AppendUint32(out, uint32(outPos)) //nolint:gosec // G115: bounded by the check above
	return append(out, 0, 0, 0, 0), nil       // reserved
}

// dataOf returns the data of entry i, replaced by newIPTC for "IPTC".
func (t *AFCP) dataOf(i int, newIPTC []byte) []byte {
	if string(t.Entries[i].Tag[:]) == afcpIPTCTag && newIPTC != nil {
		return newIPTC
	}
	return t.Entries[i].Data
}

// FotoRecord is one record of a FotoStation trailer.
type FotoRecord struct {
	Tag  uint16
	Data []byte
}

// FindFotoStation returns the FotoStation records that follow each other from
// the absolute offset from, and the range they cover. A record is found by its
// footer: the signature 0xA1B2C3D4 preceded by the tag and the record size,
// which locates the record's first byte; the record must start where the
// previous one ended.
func FindFotoStation(region []byte, base, from int64) (recs []FotoRecord, start, end int64) {
	cur := from
	for {
		rel := cur - base
		if rel < 0 || rel > int64(len(region)) {
			break
		}
		i := bytes.Index(region[rel:], fotoSig)
		if i < 6 {
			break
		}
		footer := rel + int64(i) - 6
		tag := binary.BigEndian.Uint16(region[footer:])
		size := int64(binary.BigEndian.Uint32(region[footer+2:]))
		if size < 10 || footer+10-size != rel {
			break
		}
		recs = append(recs, FotoRecord{Tag: tag, Data: region[rel:footer]})
		cur = base + footer + 10
	}
	if len(recs) == 0 {
		return nil, 0, 0
	}
	return recs, from, cur
}

// fotoSpan returns the range of a FotoStation trailer and its replacement,
// in which the IPTC record (tag 1) holds newIPTC.
func fotoSpan(recs []FotoRecord, start, end int64, newIPTC []byte) Span {
	return Span{Start: start, End: end, Build: func(int64) ([]byte, bool, error) {
		changed := false
		for _, r := range recs {
			if r.Tag == 1 && newIPTC != nil && !bytes.Equal(r.Data, newIPTC) {
				changed = true
			}
		}
		if !changed {
			return nil, false, nil
		}
		var out []byte
		for _, r := range recs {
			data := r.Data
			if r.Tag == 1 && newIPTC != nil {
				data = newIPTC
			}
			if int64(len(data))+10 > math.MaxUint32 {
				return nil, false, fmt.Errorf("%w: FotoStation record of %d bytes", ErrOverflow, len(data))
			}
			out = append(out, data...)
			out = binary.BigEndian.AppendUint16(out, r.Tag)
			out = binary.BigEndian.AppendUint32(out, uint32(len(data)+10)) //nolint:gosec // G115: bounded by the check above
			out = append(out, fotoSig...)
		}
		return out, true, nil
	}}
}

// Samsung trailer constants (layout in the package comment).
const (
	samsungQDIODataLen  = 20 // data of the QDIO block
	samsungQDIOBlockLen = samsungQDIODataLen + 8
	samsungOffsetsAt    = 12 // offset of the audio start in the QDIO data
)

var (
	samsungQDIOSig = []byte("QDIOBS") //nolint:gochecknoglobals // package-level constant bytes: type of the QDIO block and the "BS" that ends the trailer
	samsungSEFT    = []byte("SEFT")   //nolint:gochecknoglobals // package-level constant bytes
	samsungSEFH    = []byte("SEFH")   //nolint:gochecknoglobals // package-level constant bytes
)

// findSamsungQDIO returns the span of the two absolute audio offsets of the
// Samsung trailer in region (the bytes of the file from base to its end), the
// absolute offset of the start of the audio file, or false when there is no
// such trailer. The QDIO block is found by its end, "QDIO" + "BS", and
// accepted only when it has the layout of the package comment and follows a
// directory block ("SEFT" whose data starts with "SEFH"); the last such block
// of the region is used. The audio file offsets are rewritten only when the
// audio file lies in the region (at or after base, before the QDIO block), as
// it does in a file written by the camera: anything else is not understood and
// is left alone.
func findSamsungQDIO(region []byte, base int64) (sp Span, audioStart int64, ok bool) {
	for from := 0; ; {
		i := bytes.Index(region[from:], samsungQDIOSig)
		if i < 0 {
			return sp, audioStart, ok
		}
		q := from + i // index of the QDIO block type
		from = q + 1
		if cand, start, valid := samsungSpanAt(region, base, q); valid {
			sp, audioStart, ok = cand, start, true
		}
	}
}

// samsungSpanAt validates the QDIO block whose type field starts at region[q]
// and returns the span of its audio offsets and the start of the audio file.
func samsungSpanAt(region []byte, base int64, q int) (sp Span, audioStart int64, ok bool) {
	blockStart := q - samsungQDIOBlockLen + 4 // start of the QDIO data
	if blockStart < 12 || q+len(samsungQDIOSig) > len(region) || !samsungBlocksValid(region, blockStart, q) {
		return Span{}, 0, false
	}
	le := binary.LittleEndian
	data := region[blockStart : blockStart+samsungQDIODataLen]
	start := int64(le.Uint32(data[samsungOffsetsAt:]))
	end := int64(le.Uint32(data[samsungOffsetsAt+4:]))
	qdioAbs := base + int64(blockStart)
	if start < base || end < start || end > qdioAbs {
		return Span{}, 0, false
	}
	spanStart := qdioAbs + samsungOffsetsAt
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
	}}, start, true
}

// samsungBlocksValid reports whether the QDIO block with its data at
// region[blockStart] and its type field at region[q] has the layout of the
// package comment, and follows a directory block ("SEFT", whose data starts
// with "SEFH").
func samsungBlocksValid(region []byte, blockStart, q int) bool {
	le := binary.LittleEndian
	if string(region[blockStart:blockStart+4]) != "QDIO" || le.Uint32(region[q-4:]) != samsungQDIODataLen {
		return false
	}
	if !bytes.Equal(region[blockStart-4:blockStart], samsungSEFT) {
		return false
	}
	dirLen := int(le.Uint32(region[blockStart-8:]))
	return dirLen >= 12 && dirLen <= blockStart-8 && bytes.Equal(region[blockStart-8-dirLen:][:4], samsungSEFH)
}
