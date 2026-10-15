package jpeg

// Trailers with IPTC copies (task #322). A JPEG may carry data after its EOI
// marker. Two such trailers hold a second copy of the IPTC-IIM record next to
// the one in the Photoshop APP13 segment: the AXS File Concatenation Protocol
// (AFCP) trailer and the FotoWare FotoStation trailer. A write that replaces
// the IIM of APP13 would leave both copies with the old values, and would
// leave the absolute offsets of the AFCP trailer pointing at the wrong
// bytes once the metadata before the image data changes size. Inject
// therefore rewrites both copies with the IIM it writes, and rewrites the AFCP
// offsets for the position the trailer takes in the output (the policy chosen
// for #322: update the copies, as ExifTool does; CLAUDE.md §5: a write must
// not corrupt other embedded structures).
//
// Formats, from the AFCP specification (AXS File Concatenation Protocol,
// April 1, 1992, Optical Technology Resource, Inc.; archived at
// http://www.tocarte.com/media/axs_afcp_spec.pdf), whose diagram this code
// follows, and from the FotoStation layout that ExifTool documents
// (lib/Image/ExifTool/FotoStation.pm; study reference only, no ExifTool code
// is used):
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
// The data set "IPTC" (FotoStation: tag 1) holds the IIM stream as is. The
// reserved fields are written as zero, as ExifTool does: the specification
// leaves them for a validation checksum that it does not define.
//
// Scope: both trailers are located only when they start right after the EOI
// marker or, for FotoStation, right after the AFCP trailer, and only when
// their structure is consistent (an AFCP trailer whose EOF record points back
// at its header). Anything else is copied through unchanged, as before.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// maxTrailerRegion bounds the bytes after EOI that are read to look for the
// trailers (a preview image in an AFCP trailer is the largest legitimate
// content). A larger region is copied through unchanged.
const maxTrailerRegion = 16 << 20

// maxAFCPEntries bounds the directory of an AFCP trailer that is accepted.
const maxAFCPEntries = 4096

// afcpIPTCTag is the data set identifier of the IPTC data of an AFCP trailer.
const afcpIPTCTag = "IPTC"

var (
	afcpSigBE = []byte("AXS!")                 //nolint:gochecknoglobals // package-level constant bytes
	afcpSigLE = []byte("AXS*")                 //nolint:gochecknoglobals // package-level constant bytes
	fotoSig   = []byte{0xA1, 0xB2, 0xC3, 0xD4} //nolint:gochecknoglobals // package-level constant bytes
)

// errTrailerOverflow is returned when a rewritten trailer would need an
// offset that does not fit the 32-bit fields of the AFCP format.
var errTrailerOverflow = errors.New("jpeg: AFCP trailer offsets exceed 32 bits")

// trailerSpan is a byte range [start, end) of the input that Inject replaces
// by the result of build, evaluated with the position the range takes in the
// output. build reports false when the range is to be copied unchanged.
type trailerSpan struct {
	start, end int64
	build      func(outPos int64) (b []byte, changed bool, err error)
}

// trailerPlan lists the trailer ranges to rewrite, in input order. region
// backs the data the spans refer to; out counts the bytes written to the
// output, which the AFCP offsets depend on. The zero value is an empty plan.
type trailerPlan struct {
	spans  []trailerSpan
	region []byte
	out    *trailerOutWriter
}

// startTrailerPlan looks for trailers to rewrite in the JPEG that r holds. It
// returns the plan and the writer to use for the rest of the write: w itself
// when there is nothing to do (plan is nil), and otherwise a counting wrapper
// around w. A JPEG that ends with EOI costs one two-byte read.
func startTrailerPlan(r io.ReadSeeker, w io.Writer, scratch *[]byte, newIPTC []byte) (*trailerPlan, io.Writer, error) {
	plan, err := planTrailers(r, scratch, newIPTC)
	if err != nil || len(plan.spans) == 0 {
		return nil, w, err
	}
	// Only now does the plan reach the heap: the common case allocates nothing.
	p := new(trailerPlan)
	*p = plan
	p.out = &trailerOutWriter{w: w}
	return p, p.out, nil
}

// planTrailers looks for AFCP and FotoStation trailers after the EOI marker
// of the JPEG that r holds and returns the plan to rewrite their IPTC copies
// with newIPTC; the plan has no spans when there is nothing to do. scratch is
// the caller's pooled buffer. r is left at an unspecified position.
//
// A nil newIPTC leaves the IPTC copies as they are (as for the raw profiles
// of PNG, task #314), but the AFCP offsets are still rewritten when the
// trailer moves.
func planTrailers(r io.ReadSeeker, scratch *[]byte, newIPTC []byte) (trailerPlan, error) {
	region, base, err := readTrailerRegion(r, scratch)
	if err != nil || region == nil {
		return trailerPlan{}, err
	}
	return planFromRegion(region, base, newIPTC), nil
}

// readTrailerRegion returns the bytes after the EOI marker of the JPEG in r
// and the absolute offset of the first of them, or a nil region when there
// are none to look at: the JPEG ends with EOI (the common case, found with
// one seek and one two-byte read), has no EOI, or the area is larger than
// maxTrailerRegion.
func readTrailerRegion(r io.ReadSeeker, scratch *[]byte) (region []byte, base int64, err error) { //nolint:gocyclo // a sequence of cheap exits, each with the reason it is not an error
	// One seek finds the length and the position of the last two bytes.
	pos, err := r.Seek(-2, io.SeekEnd)
	if err != nil || pos < 4 {
		return nil, 0, nil //nolint:nilerr // a stream too short to hold a trailer has none to rewrite
	}
	size := pos + 2
	tail := (*scratch)[:2]
	if n, _ := r.Read(tail); n == 2 && tail[0] == 0xFF && tail[1] == markerEOI {
		return nil, 0, nil
	}
	eoi, err := findEOI(r, scratch)
	if err != nil || eoi < 0 || eoi+2 >= size || size-(eoi+2) > maxTrailerRegion {
		return nil, 0, err
	}
	base = eoi + 2
	region = make([]byte, size-base)
	if _, err := r.Seek(base, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("jpeg: seek: %w", err)
	}
	if _, err := io.ReadFull(r, region); err != nil {
		return nil, 0, nil //nolint:nilerr // a short read means a malformed trailer area: copy it unchanged
	}
	return region, base, nil
}

// planFromRegion finds the trailers in region, the bytes of the file from the
// absolute offset base to its end, and plans their rewrite.
func planFromRegion(region []byte, base int64, newIPTC []byte) trailerPlan {
	plan := trailerPlan{region: region}
	afcp := findAFCP(region, base)
	from := base
	if afcp != nil {
		plan.spans = append(plan.spans, afcp.span(newIPTC))
		from = afcp.end
	}
	if recs, start, end := findFotoStation(region, base, from); recs != nil {
		plan.spans = append(plan.spans, fotoSpan(recs, start, end, newIPTC))
	}
	if afcp != nil {
		// A FotoStation trailer that precedes the AFCP trailer.
		if recs, start, end := findFotoStation(region, base, base); recs != nil && end <= afcp.start {
			plan.spans = append(plan.spans, fotoSpan(recs, start, end, newIPTC))
		}
	}
	sortSpans(plan.spans)
	return plan
}

// sortSpans orders spans by start (at most three).
func sortSpans(s []trailerSpan) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].start < s[j-1].start; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// findEOI returns the offset of the EOI marker that ends the image data of the
// JPEG in r, or -1 when there is none. It walks the segments up to the first
// SOS, then looks for the first 0xFF 0xD9: inside entropy-coded data every
// 0xFF is followed by 0x00, a restart marker or a marker, so 0xFF 0xD9 is the
// end of the image (JPEG ISO/IEC 10918-1 §B.1.1.5, §F.1.2.3).
func findEOI(r io.ReadSeeker, scratch *[]byte) (int64, error) {
	if _, err := r.Seek(2, io.SeekStart); err != nil {
		return -1, fmt.Errorf("jpeg: seek: %w", err)
	}
	for {
		marker, _, err := readSegment(r, scratch)
		if err != nil {
			if errors.Is(err, ErrFileTooLarge) {
				return -1, err
			}
			return -1, nil
		}
		switch marker {
		case markerEOI:
			pos, serr := r.Seek(0, io.SeekCurrent)
			if serr != nil {
				return -1, fmt.Errorf("jpeg: seek: %w", serr)
			}
			return pos - 2, nil
		case markerSOS:
			return scanForEOI(r)
		}
	}
}

// scanForEOI reads from the current position of r, the start of entropy-coded
// data, and returns the offset of the first 0xFF 0xD9, or -1.
func scanForEOI(r io.ReadSeeker) (int64, error) {
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return -1, fmt.Errorf("jpeg: seek: %w", err)
	}
	bufPtr := iobuf.Get(iobuf.StreamCopyBufSize)
	defer iobuf.Put(bufPtr)
	buf := *bufPtr
	prevFF := false
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if prevFF && chunk[0] == markerEOI {
				return pos - 1, nil
			}
			if i := bytes.Index(chunk, []byte{0xFF, markerEOI}); i >= 0 {
				return pos + int64(i), nil
			}
			prevFF = chunk[n-1] == 0xFF
			pos += int64(n)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return -1, nil
			}
			if errors.Is(rerr, ErrFileTooLarge) {
				return -1, fmt.Errorf("jpeg: read image data: %w", rerr)
			}
			return -1, nil
		}
	}
}

// trailerOrder is the byte order of an AFCP trailer: binary.BigEndian or
// binary.LittleEndian.
type trailerOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// afcpEntry is one directory entry of an AFCP trailer.
type afcpEntry struct {
	tag  [4]byte
	data []byte
}

// afcpTrailer is a parsed AFCP trailer; start and end are absolute offsets.
type afcpTrailer struct {
	start, end int64
	sig        [4]byte
	order      trailerOrder
	version    [2]byte
	entries    []afcpEntry
}

// findAFCP returns the AFCP trailer in region, the bytes of the file from the
// absolute offset base to its end, or nil. A candidate must start with an
// AFCP signature, hold a directory whose entries lie after it, and be closed
// by an EOF record that carries the offset of the candidate (AFCP
// specification: the EOF record "points to AFCP header").
func findAFCP(region []byte, base int64) *afcpTrailer {
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
func parseAFCP(region []byte, base int64, at int) *afcpTrailer {
	rem := region[at:]
	if len(rem) < 24 { // header and EOF record
		return nil
	}
	t := &afcpTrailer{start: base + int64(at)}
	copy(t.sig[:], rem[:4])
	t.order = binary.LittleEndian
	if rem[3] == '!' {
		t.order = binary.BigEndian
	}
	copy(t.version[:], rem[4:6])
	dataEnd, ok := t.parseDirectory(rem)
	if !ok {
		return nil
	}
	rec, ok := t.findEOFRecord(rem, dataEnd)
	if !ok {
		return nil
	}
	t.end = t.start + rec + 12
	return t
}

// parseDirectory reads the directory entries of the trailer whose bytes start
// at rem[0], and returns the end of the data they name, relative to the
// trailer. The entries must lie after the directory and inside rem.
func (t *afcpTrailer) parseDirectory(rem []byte) (dataEnd int64, ok bool) {
	count := int(t.order.Uint16(rem[6:]))
	dirEnd := 12 + 12*count
	if count > maxAFCPEntries || dirEnd+12 > len(rem) {
		return 0, false
	}
	dataEnd = int64(dirEnd)
	for i := range count {
		d := rem[12+12*i:]
		var e afcpEntry
		copy(e.tag[:], d[:4])
		n := int64(t.order.Uint32(d[4:]))
		off := int64(t.order.Uint32(d[8:])) - t.start // relative to the trailer
		if off < int64(dirEnd) || n > int64(len(rem)) || off > int64(len(rem))-n {
			return 0, false
		}
		e.data = rem[off : off+n]
		t.entries = append(t.entries, e)
		dataEnd = max(dataEnd, off+n)
	}
	return dataEnd, true
}

// findEOFRecord returns the offset, relative to the trailer, of the EOF
// record: a record that follows the data (a gap with other bytes is allowed)
// and carries the offset of the header.
func (t *afcpTrailer) findEOFRecord(rem []byte, dataEnd int64) (int64, bool) {
	for q := dataEnd; q+12 <= int64(len(rem)); {
		j := bytes.Index(rem[q:], t.sig[:])
		if j < 0 || q+int64(j)+12 > int64(len(rem)) {
			return 0, false
		}
		rec := q + int64(j)
		if int64(t.order.Uint32(rem[rec+4:])) == t.start {
			return rec, true
		}
		q = rec + 1
	}
	return 0, false
}

// span returns the range of the trailer and its replacement.
func (t *afcpTrailer) span(newIPTC []byte) trailerSpan {
	return trailerSpan{start: t.start, end: t.end, build: func(outPos int64) ([]byte, bool, error) {
		changed := outPos != t.start
		for i := range t.entries {
			if string(t.entries[i].tag[:]) == afcpIPTCTag && newIPTC != nil && !bytes.Equal(t.entries[i].data, newIPTC) {
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
func (t *afcpTrailer) build(outPos int64, newIPTC []byte) ([]byte, error) {
	dirLen := int64(12 * len(t.entries))
	total := 12 + dirLen + 12
	for i := range t.entries {
		total += int64(len(t.dataOf(i, newIPTC)))
	}
	if outPos < 0 || outPos+total > math.MaxUint32 {
		return nil, fmt.Errorf("%w: trailer at %d, %d bytes", errTrailerOverflow, outPos, total)
	}
	o := t.order
	out := make([]byte, 0, total)
	out = append(out, t.sig[:]...)
	out = append(out, t.version[:]...)
	out = o.AppendUint16(out, uint16(len(t.entries))) //nolint:gosec // G115: the count came from a 16-bit field
	out = append(out, 0, 0, 0, 0)                     // reserved
	off := uint32(outPos + 12 + dirLen)               //nolint:gosec // G115: bounded by the check above
	for i := range t.entries {
		n := uint32(len(t.dataOf(i, newIPTC))) //nolint:gosec // G115: bounded by the check above
		out = append(out, t.entries[i].tag[:]...)
		out = o.AppendUint32(out, n)
		out = o.AppendUint32(out, off)
		off += n
	}
	for i := range t.entries {
		out = append(out, t.dataOf(i, newIPTC)...)
	}
	out = append(out, t.sig[:]...)
	out = o.AppendUint32(out, uint32(outPos)) //nolint:gosec // G115: bounded by the check above
	return append(out, 0, 0, 0, 0), nil       // reserved
}

// dataOf returns the data of entry i, replaced by newIPTC for "IPTC".
func (t *afcpTrailer) dataOf(i int, newIPTC []byte) []byte {
	if string(t.entries[i].tag[:]) == afcpIPTCTag && newIPTC != nil {
		return newIPTC
	}
	return t.entries[i].data
}

// fotoRecord is one record of a FotoStation trailer.
type fotoRecord struct {
	tag  uint16
	data []byte
}

// findFotoStation returns the FotoStation records that follow each other from
// the absolute offset from, and the range they cover. A record is found by its
// footer: the signature 0xA1B2C3D4 preceded by the tag and the record size,
// which locates the record's first byte; the record must start where the
// previous one ended.
func findFotoStation(region []byte, base, from int64) (recs []fotoRecord, start, end int64) {
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
		recs = append(recs, fotoRecord{tag: tag, data: region[rel:footer]})
		cur = base + footer + 10
	}
	if len(recs) == 0 {
		return nil, 0, 0
	}
	return recs, from, cur
}

// fotoSpan returns the range of a FotoStation trailer and its replacement,
// in which the IPTC record (tag 1) holds newIPTC.
func fotoSpan(recs []fotoRecord, start, end int64, newIPTC []byte) trailerSpan {
	return trailerSpan{start: start, end: end, build: func(int64) ([]byte, bool, error) {
		changed := false
		for _, r := range recs {
			if r.tag == 1 && newIPTC != nil && !bytes.Equal(r.data, newIPTC) {
				changed = true
			}
		}
		if !changed {
			return nil, false, nil
		}
		var out []byte
		for _, r := range recs {
			data := r.data
			if r.tag == 1 && newIPTC != nil {
				data = newIPTC
			}
			if int64(len(data))+10 > math.MaxUint32 {
				return nil, false, fmt.Errorf("%w: FotoStation record of %d bytes", errTrailerOverflow, len(data))
			}
			out = append(out, data...)
			out = binary.BigEndian.AppendUint16(out, r.tag)
			out = binary.BigEndian.AppendUint32(out, uint32(len(data)+10)) //nolint:gosec // G115: bounded by the check above
			out = append(out, fotoSig...)
		}
		return out, true, nil
	}}
}

// trailerOutWriter counts the bytes written through it, so that the position of
// a trailer in the output is known when its AFCP offsets are written.
type trailerOutWriter struct {
	w io.Writer
	n int64
}

func (c *trailerOutWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err //nolint:wrapcheck // io.Writer contract: errors are returned unwrapped
}

// writeSOSTrailers is writeSOS for a JPEG with trailers to rewrite: it copies
// the compressed data, replaces each span of plan by its rewritten bytes (or
// copies it when it does not change), and copies the rest. w must be
// plan.out, which has seen every byte of the output so far.
func writeSOSTrailers(r io.ReadSeeker, w io.Writer, data []byte, plan *trailerPlan) error { //nolint:gocyclo // copy, rewrite or skip for each span, with the error handling of each step
	cw := plan.out
	if err := writeSegmentCopy(w, markerSOS, data); err != nil {
		return err
	}
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("jpeg: seek: %w", err)
	}
	for _, sp := range plan.spans {
		if sp.start < pos {
			continue // overlaps what was copied: leave it as it is
		}
		if err := iobuf.StreamCopyN(r, w, sp.start-pos); err != nil {
			return fmt.Errorf("jpeg: copy image data: %w", err)
		}
		b, changed, berr := sp.build(cw.n)
		if berr != nil {
			return berr
		}
		if !changed {
			pos = sp.start
			continue
		}
		if _, err := w.Write(b); err != nil {
			return fmt.Errorf("jpeg: write trailer: %w", err)
		}
		if _, err := r.Seek(sp.end, io.SeekStart); err != nil {
			return fmt.Errorf("jpeg: seek: %w", err)
		}
		pos = sp.end
	}
	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("jpeg: copy image data: %w", err)
	}
	return nil
}
