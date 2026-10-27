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
	afcpSigBE     = []byte("AXS!")                 //nolint:gochecknoglobals // package-level constant bytes
	afcpSigLE     = []byte("AXS*")                 //nolint:gochecknoglobals // package-level constant bytes
	afcpSigPrefix = []byte("AXS")                  //nolint:gochecknoglobals // package-level constant bytes
	fotoSig       = []byte{0xA1, 0xB2, 0xC3, 0xD4} //nolint:gochecknoglobals // package-level constant bytes
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

// Plan lists the trailer ranges to rewrite, in input order, and holds what is
// needed to write the bytes of the region around them. The spans address the
// input by absolute offsets; the region starts at Base.
//
// A plan made by PlanFromRegion has the bytes of the region in Region. A plan
// made by PlanFromSource (task #393) has none: the bytes between and after the
// spans are copied from the Source when the plan is written, so that a region
// that is mostly the payload of a trailer (the audio file of a Samsung trailer)
// is never held in memory. The zero value is an empty plan.
type Plan struct {
	Spans  []Span
	Region []byte
	Base   int64

	// end is the offset one past the last byte of the region, and src the source
	// the bytes between the spans are copied from; both are set by PlanFromSource.
	end int64
	src copier
}

// copier writes a range of a file to w.
type copier interface {
	CopyRange(w io.Writer, off, n int64) error
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
	plan, _ := PlanFromSource(regionSource{b: region, base: base}, base, int64(len(region)), newIPTC) // a region in memory cannot fail to read
	plan.Region = region
	return plan
}

// PlanFromSource is PlanFromRegion for a region that is not in memory: the
// bytes of the file from the absolute offset base to base+size, read through
// src (task #393). The plan finds the same trailers and spans, reading only
// what it has to inspect: the signatures are searched for with a streaming scan
// (Source.Index), and a trailer that is rewritten is read in full.
//
// A read error of src is returned; the plan is then incomplete and must not be
// used.
func PlanFromSource[S Source](src S, base, size int64, newIPTC []byte) (Plan, error) {
	v := view[S]{src: src, base: base, end: base + size}
	plan := Plan{Base: base, end: v.end, src: src}
	spans, err := planAFCPAndFoto(v, newIPTC)
	if err != nil {
		return Plan{}, err
	}
	plan.Spans = spans
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
	sp, rigidStart, ok, err := scanSamsungQDIO(v)
	if err != nil {
		return Plan{}, err
	}
	if ok {
		kept := plan.Spans[:0]
		for _, other := range plan.Spans {
			if other.End <= rigidStart || other.Start >= sp.End {
				kept = append(kept, other)
			}
		}
		plan.Spans = append(kept, sp)
	}
	plan.Spans = disjoint(sortSpans(plan.Spans))
	return plan, nil
}

// planAFCPAndFoto finds the AFCP trailer and the FotoStation trailers of the
// view and returns the spans that rewrite them (and the AFCP trailer).
func planAFCPAndFoto[S Source](v view[S], newIPTC []byte) (spans []Span, err error) {
	afcp, found, err := scanAFCP(v)
	if err != nil {
		return nil, err
	}
	from := v.base
	if found {
		spans = append(spans, afcp.span(newIPTC))
		from = afcp.End
	}
	recs, start, end, err := scanFotoStation(v, from)
	if err != nil {
		return nil, err
	}
	if recs != nil {
		spans = append(spans, fotoSpan(recs, start, end, newIPTC))
	}
	if !found {
		return spans, nil
	}
	// A FotoStation trailer that precedes the AFCP trailer.
	recs, start, end, err = scanFotoStation(v, v.base)
	if err != nil {
		return nil, err
	}
	if recs != nil && end <= afcp.Start {
		spans = append(spans, fotoSpan(recs, start, end, newIPTC))
	}
	return spans, nil
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
	src, end := p.src, p.end
	if src == nil { // a plan that holds its region (or was built by hand)
		src, end = regionSource{b: p.Region, base: p.Base}, p.Base+int64(len(p.Region))
	}
	cur := p.Base // the first byte of the region not yet written
	for _, sp := range p.Spans {
		if sp.Start < cur || sp.End > end {
			continue // not inside the unwritten part of the region: leave it as it is
		}
		if err := writeSpan(w, out, src, cur, sp); err != nil {
			return err
		}
		cur = sp.End
	}
	if err := src.CopyRange(w, cur, end-cur); err != nil {
		return fmt.Errorf("trailer: write: %w", err)
	}
	return nil
}

// writeSpan writes the region from cur to the end of the span: the bytes before
// it as they are, then the span rewritten, or as it is when it does not change.
func writeSpan(w io.Writer, out *OutWriter, src copier, cur int64, sp Span) error {
	if err := src.CopyRange(w, cur, sp.Start-cur); err != nil {
		return fmt.Errorf("trailer: write: %w", err)
	}
	b, changed, err := sp.Build(out.N)
	if err != nil {
		return err
	}
	if !changed {
		if err = src.CopyRange(w, sp.Start, sp.End-sp.Start); err != nil {
			return fmt.Errorf("trailer: write: %w", err)
		}
		return nil
	}
	if _, err = w.Write(b); err != nil {
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

// maxAFCPSignatures bounds the AFCP signatures ("AXS!" or "AXS*") that
// FindAFCP examines in one region. A real trailer holds two (the header and
// the EOF record) and the region follows the image data, so chance matches are
// as rare as a 4-byte value occurring by accident; a region with more is not an
// AFCP trailer but an attempt to make the candidate x EOF-record search
// quadratic (security audit F-03: 15 s for 512 KiB), and it is left to the
// caller to copy unchanged. The signatures are collected in ONE linear pass
// and every later step works on that list, so FindAFCP costs O(len(region) +
// maxAFCPSignatures^2 + maxAFCPSignatures*maxAFCPEntries) at worst.
// Reference: ExifTool bounds the same search by reading only the last 12 bytes
// of the file (lib/Image/ExifTool.pm IdentifyTrailer, AFCP.pm).
const maxAFCPSignatures = 256

// FindAFCP returns the AFCP trailer in region, the bytes of the file from the
// absolute offset base to its end, or nil. A candidate must start with an
// AFCP signature, hold a directory whose entries lie after it, and be closed
// by an EOF record that carries the offset of the candidate (AFCP
// specification: the EOF record "points to AFCP header").
func FindAFCP(region []byte, base int64) *AFCP {
	t, _, _ := scanAFCP(view[regionSource]{src: regionSource{b: region, base: base}, base: base, end: base + int64(len(region))}) // a region in memory cannot fail to read
	return t
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
	recs, start, end, _ = scanFotoStation(view[regionSource]{src: regionSource{b: region, base: base}, base: base, end: base + int64(len(region))}, from) // a region in memory cannot fail to read
	return recs, start, end
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
