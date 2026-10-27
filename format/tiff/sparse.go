package tiff

// sparse.go — task #389 (sprint 50): read the metadata of a classic TIFF or
// DNG whose IFDs lie after, or among, the image data without reading the
// image data.
//
// exif.Parse needs one contiguous buffer addressed by TIFF offsets, so the
// prefix scanner of extent.go (#289) reads [0, end of the last IFD) — the whole
// file for a TIFF whose Exif IFD, or IFD0 itself, follows the strips (the
// Pentax QS1 DNG of the corpus: 22.6 MB read for 130 KB of metadata). This file
// builds the same buffer, compacted: only the bytes the parse reads — the IFD
// directories, their out-of-line values and the JPEG thumbnails — copied side
// by side, with the offsets that exif.Parse follows (the IFD0 offset in the
// header, the next-IFD offsets, the out-of-line value offsets, the Exif, GPS and
// Interoperability pointers, the JPEGInterchangeFormat offset) rewritten to the
// new positions. Parse reads that stream exactly as it would read the file; the
// caller then calls Restore, which writes the original offsets back, so that
// every parsed value (including the pointer values) is the one in the file.
//
// Design source: bep/imagemeta (MIT; io.go, imagedecoder_tif.go) reads a TIFF
// through an offset-aware reader that seeks to each IFD and value, and
// evanoberholster/imagemeta (MIT; imagemeta.go) walks the IFDs the same way;
// neither retains the bytes. Here the walk reads the directories by seeking
// (the structure of that walk follows extent.go, which mirrors exif.Parse), and
// the bytes are retained compacted, because exif.Parse and the zero-copy values
// of exif.IFDEntry.Value need a buffer.
//
// Equivalence is guaranteed by construction and by a fallback, not assumed:
//
//   - The walk collects every range exif.Parse can read (a superset: it follows
//     the pointer tags of every IFD it reaches, as extent.go does).
//   - A file that the walk cannot represent exactly is not compacted
//     (ExtractCompact returns the whole-prefix result of Extract): overlapping
//     IFD directories or values, an IFD that does not fit in the file, an
//     out-of-line pointer value, a MakerNote outside the first region (its
//     TIFF-relative offsets must keep their meaning), BigTIFF.
//   - gometadata.Read discards the compacted result for the whole-prefix one
//     when exif.Parse reports an error or a warning on it (a warning carries
//     offsets of the stream it parsed).
//
// TIFF 6.0 §2 (IFD layout), Adobe DNG Specification 1.7.1.0 §4 (DNG is classic
// TIFF; its SubIFDs are not read by exif.Parse and are not part of the stream).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"

	"github.com/FlavioCFOliveira/GoMetadata/internal/tiffscan"
)

// sparseTriggerGap is the growth, beyond the bytes already read, that makes the
// prefix scanner give up growing the prefix and try the compacted stream: a
// metadata structure more than 1 MiB away from the bytes read. Reading up to
// 1 MiB more is as cheap as the walk, so a file whose structures are all
// within reach never pays for the walk.
const sparseTriggerGap = 1 << 20

// sparseMinSaving is the least number of bytes the compacted stream must save
// over the prefix the scanner would read for the compaction to be used: below
// it the plain prefix is as cheap and has no offsets to restore.
const sparseMinSaving = 1 << 20

// maxSparseBytes caps the compacted stream. A file whose metadata (directories,
// values and thumbnails of the IFDs exif.Parse reads) is larger than this is
// not compacted: the prefix path bounds it by maxFileSize.
const maxSparseBytes = 64 << 20

// maxSparseDirBytes caps the IFD directories one walk reads, which bounds its
// CPU time on an input that declares many large IFDs (a legitimate file has a
// few hundred bytes of directories per IFD).
const maxSparseDirBytes = 8 << 20

// maxSparseRanges caps the ranges (directories and values) one walk collects.
const maxSparseRanges = 1 << 16

// sparseMergeGap is the largest gap between two ranges that is read rather than
// skipped: the padding between a directory and its values is a few bytes, and a
// seek and read per range costs more than the bytes.
const sparseMergeGap = 1024

// pointer tags, EXIF 3.1 §4.6.3 Table 7 (0x8769 Exif IFD, 0x8825 GPS Info IFD)
// and §4.6.6.3 Table 9 (0xA005 Interoperability IFD).
const (
	tagMakerNote    = 0x927C
	tagExifPointer  = 0x8769
	tagGPSPointer   = 0x8825
	tagInteropPtr   = 0xA005
	tagJPEGInterFmt = 0x0201 // JPEGInterchangeFormat, EXIF §4.6.4 Table 3
	tagJPEGInterLen = 0x0202 // JPEGInterchangeFormatLength
)

// errNotCompactable marks a file the walk cannot represent exactly.
var errNotCompactable = errors.New("tiff: metadata layout cannot be compacted")

// Sparse is the compacted metadata of a classic TIFF whose IFDs are scattered
// in the file (see ExtractCompact). Parse reads the stream between the
// extraction and Restore; Restore must be called once the parse is done.
type Sparse struct {
	stream   []byte
	headLen  int
	patches  []sparsePatch
	rawIPTC  []byte
	rawXMP   []byte
	restored bool
}

// sparsePatch is a 4-byte offset field of the stream that was rewritten, and
// the bytes it held in the file.
type sparsePatch struct {
	pos  uint32
	orig [4]byte
}

// Stream returns the compacted TIFF stream, in which every offset that
// exif.Parse follows addresses the stream itself. It is valid for exif.Parse
// and for the Photoshop digest scan only until Restore is called; after Restore
// its bytes are the bytes of the file (see Head).
func (s *Sparse) Stream() []byte { return s.stream }

// IPTC returns the IPTC-NAA payload of IFD0 (nil when absent); it aliases the
// stream. It is identical to what Extract returns for the file.
func (s *Sparse) IPTC() []byte { return s.rawIPTC }

// XMP returns the XMP packet of IFD0 (nil when absent); it aliases the stream.
func (s *Sparse) XMP() []byte { return s.rawXMP }

// Head returns the first bytes of the file, from offset 0 to the end of the
// first group of metadata structures: a byte-for-byte prefix of the file once
// Restore has been called. It is not a stream that exif.Parse can read when
// the IFD0 offset lies beyond it.
func (s *Sparse) Head() []byte { return s.stream[:s.headLen:s.headLen] }

// Restore writes the original offsets back into the stream, so that the bytes
// are those of the file and the values that exif.Parse took from the stream
// (every IFDEntry.Value aliases it) hold the offsets of the file. It is
// idempotent.
func (s *Sparse) Restore() {
	if s.restored {
		return
	}
	s.restored = true
	for _, p := range s.patches {
		copy(s.stream[p.pos:p.pos+4], p.orig[:])
	}
}

// sparseParams are the thresholds of the compaction; tests lower them.
type sparseParams struct {
	minFileSize int64  // files up to this size are read whole
	trigger     uint64 // growth beyond the bytes read that tries the compaction
	minSaving   uint64 // least saving over the prefix for the compaction to be used
	always      bool   // try the compaction whatever the growth of the scan (tests, fuzzing)
}

var defaultSparseParams = sparseParams{ //nolint:gochecknoglobals // read-only thresholds
	minFileSize: smallFileWholeReadThreshold,
	trigger:     sparseTriggerGap,
	minSaving:   sparseMinSaving,
}

// ExtractCompact is Extract for a classic TIFF or DNG, except that a file whose
// IFDs lie far from each other (after the image data, among it) is returned as
// a Sparse: the compacted stream that exif.Parse reads, and no more bytes than
// the metadata occupies, not the image data between the structures. sp is nil
// when the file is read as Extract reads it: rawEXIF, rawIPTC and rawXMP are
// then Extract's results. When sp is not nil, rawEXIF is sp.Stream() and the
// caller must call sp.Restore() after parsing it.
func ExtractCompact(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, sp *Sparse, err error) {
	return extractCompactWith(r, defaultSparseParams)
}

func extractCompactWith(r io.ReadSeeker, p sparseParams) (rawEXIF, rawIPTC, rawXMP []byte, sp *Sparse, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("tiff: seek: %w", err)
	}
	fileSize, err := seekFileSize(r)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if fileSize < 0 || fileSize > maxFileSize || fileSize <= p.minFileSize {
		// Not seekable to its end, too large, or small enough to be read whole:
		// Extract decides (and reports the error for the large file).
		rawEXIF, rawIPTC, rawXMP, err = extractTIFFWithMagic(r, 0)
		return rawEXIF, rawIPTC, rawXMP, nil, err
	}
	size := uint64(fileSize)
	initial, err := readInitialPrefix(r, fileSize)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(initial) >= 8 {
		if order, oerr := byteOrder(initial); oerr == nil && order.Uint16(initial[2:]) == 0x002A {
			return extractCompactClassic(r, initial, order, size, p)
		}
	}
	// Another container variant (BigTIFF, invalid header, ...): Extract's path,
	// from the bytes already read.
	rawEXIF, rawIPTC, rawXMP, err = extractPrefixedFrom(r, initial, size, 0)
	return rawEXIF, rawIPTC, rawXMP, nil, err
}

// extractCompactClassic is extractClassicPrefix with the compaction: the prefix
// scan runs until it would grow the prefix by more than p.trigger bytes, and the
// compaction is tried then (or at once when p.always is set).
func extractCompactClassic(r io.ReadSeeker, initial []byte, order binary.ByteOrder, size uint64, p sparseParams) (rawEXIF, rawIPTC, rawXMP []byte, sp *Sparse, err error) {
	ifd0Off := order.Uint32(initial[4:])
	buf, jumped, need, err := scanMetadataExtentJump(r, initial, uint64(ifd0Off), order, false, size, p.trigger)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if jumped || p.always {
		if p.always {
			need = size
		}
		if sparse := buildSparse(r, buf, order, size, need, p); sparse != nil {
			return sparse.stream, sparse.rawIPTC, sparse.rawXMP, sparse, nil
		}
		// Not compactable: continue the prefix scan from the bytes read.
		if buf, err = scanMetadataExtent(r, buf, uint64(ifd0Off), order, false, size); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	rawIPTC, rawXMP = extractTagValues(buf, ifd0Off, order)
	return buf, rawIPTC, rawXMP, nil, nil
}

// sparseRange is the half-open byte range [start, end) of the file.
type sparseRange struct{ start, end uint64 }

// sparseField is a 4-byte offset field of the file (at field) that holds target.
type sparseField struct{ field, target uint64 }

// sparseWalker collects what exif.Parse reads from a classic TIFF: the ranges,
// and the offset fields that address them.
type sparseWalker struct {
	r        io.ReadSeeker
	initial  []byte // file bytes [0, len(initial)), already read
	order    binary.ByteOrder
	size     uint64 // file size
	dirs     []sparseRange
	data     []sparseRange // out-of-line values and thumbnails
	mn       []sparseRange // MakerNote values
	fields   []sparseField
	seen     map[uint64]struct{} // directory starts
	dirBytes uint64
	// dataBytes sums the lengths of the data ranges (an upper bound of their
	// union), and limit is the most the compacted stream may hold for the
	// compaction to save sparseMinSaving bytes: a walk that exceeds it stops.
	dataBytes uint64
	limit     uint64
	chunk     []byte // arena for the directories read from the file
}

// buildSparse walks the IFDs that exif.Parse reads and returns the compacted
// stream, or nil when the file is not compactable or the compaction does not
// save sparseMinSaving bytes over the prefix of need bytes. initial holds the
// first bytes of the file, which are not read again.
func buildSparse(r io.ReadSeeker, initial []byte, order binary.ByteOrder, size, need uint64, p sparseParams) *Sparse {
	w := &sparseWalker{
		r: r, initial: initial, order: order, size: size,
		seen: make(map[uint64]struct{}, 8),
		dirs: make([]sparseRange, 0, 16), data: make([]sparseRange, 0, 64), fields: make([]sparseField, 0, 64),
	}
	if need = min(need, size); need > p.minSaving {
		w.limit = need - p.minSaving
	} // else: no saving is possible; the walk below stops at its first range
	ifd0 := uint64(order.Uint32(initial[4:]))
	if err := w.chain(ifd0, 0); err != nil {
		return nil
	}
	w.fields = append(w.fields, sparseField{field: 4, target: ifd0}) // header: offset of IFD0
	regions, ok := w.regions()
	if !ok {
		return nil
	}
	total := regionsBytes(regions)
	if total > maxSparseBytes || total+p.minSaving > min(need, size) {
		return nil
	}
	return w.build(regions, total)
}

// chain walks the IFD chain that starts at off (an IFD0, or the IFD a pointer
// tag addresses), like extentScan.walkChain; depth is the pointer nesting.
func (w *sparseWalker) chain(off uint64, depth int) error {
	for range maxExtentTraverseIFDs {
		if off == 0 {
			return nil
		}
		if _, dup := w.seen[off]; dup {
			return nil // a cycle, or an IFD reached twice: it is walked once
		}
		next, err := w.walkIFD(off, depth)
		if err != nil {
			return err
		}
		off = next
	}
	// A chain longer than exif.Parse follows (its budget stops it with a warning).
	return errNotCompactable
}

// readAt returns the n file bytes at off, which must lie inside the file. The
// slice is valid until the walk ends (it is a view of initial, or a fresh copy).
func (w *sparseWalker) readAt(off, n uint64) ([]byte, error) {
	if off+n <= uint64(len(w.initial)) {
		return w.initial[off : off+n], nil
	}
	// Directories are carved from chunks, not allocated one by one: a file has a
	// few dozen of them, and the chunk lives as long as the walk's views of it.
	if uint64(len(w.chunk)) < n {
		w.chunk = make([]byte, max(n, 4096))
	}
	buf := w.chunk[:n:n]
	w.chunk = w.chunk[n:]
	if _, err := w.r.Seek(int64(off), io.SeekStart); err != nil { //nolint:gosec // G115: off < size <= maxFileSize
		return nil, fmt.Errorf("tiff: seek: %w", err)
	}
	if _, err := io.ReadFull(w.r, buf); err != nil {
		return nil, fmt.Errorf("tiff: read: %w", err)
	}
	return buf, nil
}

// walkIFD collects the directory at off and the ranges and pointers of its
// entries, and returns the next-IFD offset. TIFF 6.0 §2: count (2) + count × 12
// entry bytes + next-IFD offset (4).
func (w *sparseWalker) walkIFD(off uint64, depth int) (next uint64, err error) {
	dir, count, err := w.readDirectory(off)
	if err != nil {
		return 0, err
	}
	dirLen := uint64(len(dir))
	w.seen[off] = struct{}{}
	w.dirs = append(w.dirs, sparseRange{off, off + dirLen})
	if err := w.walkEntries(dir, off, count, depth); err != nil {
		return 0, err
	}
	next = uint64(w.order.Uint32(dir[dirLen-4:]))
	if next != 0 {
		// The next-IFD offset is the last 4 bytes of the directory.
		w.fields = append(w.fields, sparseField{field: off + dirLen - 4, target: next})
	}
	return next, nil
}

// readDirectory reads the directory at off and charges it to the walk's budgets.
func (w *sparseWalker) readDirectory(off uint64) (dir []byte, count uint64, err error) {
	// A directory must lie after the 8-byte header and wholly inside the file:
	// exif.Parse clamps (and warns about) a count that overruns the buffer, and
	// the compacted stream has no such overrun to clamp against.
	if off < 8 || off+2 > w.size {
		return nil, 0, errNotCompactable
	}
	cnt, err := w.readAt(off, 2)
	if err != nil {
		return nil, 0, err
	}
	count = uint64(w.order.Uint16(cnt))
	dirLen := 2 + count*12 + 4
	if off+dirLen > w.size {
		return nil, 0, errNotCompactable
	}
	w.dirBytes += dirLen
	if w.dirBytes > maxSparseDirBytes || len(w.dirs)+len(w.data) >= maxSparseRanges || w.dirBytes+w.dataBytes > w.limit {
		return nil, 0, errNotCompactable
	}
	dir, err = w.readAt(off, dirLen)
	return dir, count, err
}

// walkEntries walks the count entries of the directory dir at file offset off,
// then adds the thumbnail range its JPEGInterchangeFormat entries describe.
func (w *sparseWalker) walkEntries(dir []byte, off, count uint64, depth int) error {
	var jif jifFields
	for i := range count {
		e := 2 + i*12
		if err := w.walkEntry(dir[e:e+12], off+e, depth, &jif); err != nil {
			return err
		}
	}
	return w.collectThumbnail(jif)
}

// jifFields gathers the JPEGInterchangeFormat (0x0201) and ...Length (0x0202)
// entries of one IFD, as exif.extractJPEGThumbnail needs both.
type jifFields struct {
	offField, lenField uint64 // file offsets of the 4-byte value fields
	offVal, lenVal     uint64
	nOff, nLen         int
	short              bool // an entry whose value is shorter than 4 bytes
}

// walkEntry handles one 12-byte entry (at file offset pos): its out-of-line
// value range, and the pointer tags. exif.parseIFDEntry: a value of more than 4
// bytes is out-of-line, at the offset in bytes 8-11; an unknown type keeps its
// 4-byte field as the value.
func (w *sparseWalker) walkEntry(e []byte, pos uint64, depth int, jif *jifFields) error {
	tag := w.order.Uint16(e)
	typ := w.order.Uint16(e[2:])
	cnt := uint64(w.order.Uint32(e[4:]))
	field := pos + 8
	sz := uint64(typeSize(typ))
	valueLen := sz * cnt
	if sz == 0 {
		valueLen = 4 // exif.parseIFDEntry: the raw field of an unknown type is the Value
	}
	isPtr := tag == tagExifPointer || tag == tagGPSPointer || tag == tagInteropPtr
	isJIF := tag == tagJPEGInterFmt || tag == tagJPEGInterLen

	if valueLen > 4 { // out-of-line
		return w.walkOutOfLine(tag, w.order.Uint32(e[8:]), valueLen, field, isPtr || isJIF)
	}
	if isJIF {
		w.recordJIF(tag, e, field, valueLen, jif)
		return nil
	}
	if isPtr && valueLen == 4 {
		return w.walkPointer(w.order.Uint32(e[8:]), field, depth)
	}
	return nil
}

// walkOutOfLine records the range of an out-of-line value. exif.parseIFDEntry
// drops an entry whose range overruns the buffer without a warning; the field
// is then left as it is, which the stream reads as an overrun too (it is
// shorter than the file). A pointer or thumbnail tag whose value is out-of-line
// is not compactable: exif.Parse would read the pointer from the value.
func (w *sparseWalker) walkOutOfLine(tag uint16, valOff uint32, total, field uint64, special bool) error {
	end := uint64(valOff) + total
	if end > w.size {
		return nil
	}
	if special {
		return errNotCompactable
	}
	if w.dataBytes += total; w.dirBytes+w.dataBytes > w.limit {
		return errNotCompactable
	}
	rg := sparseRange{uint64(valOff), end}
	w.data = append(w.data, rg)
	if tag == tagMakerNote {
		w.mn = append(w.mn, rg)
	}
	w.fields = append(w.fields, sparseField{field: field, target: uint64(valOff)})
	return nil
}

// walkPointer follows an Exif, GPS or Interoperability pointer (EXIF §4.6.3).
// extentScan recurses to maxSubIFDDepth; a deeper pointer is not compactable
// (exif.Parse would follow it), and neither is a zero pointer, which exif.Parse
// reads as an IFD at offset 0, the header.
func (w *sparseWalker) walkPointer(ptr uint32, field uint64, depth int) error {
	if ptr == 0 || depth >= maxSubIFDDepth {
		return errNotCompactable
	}
	if err := w.chain(uint64(ptr), depth+1); err != nil {
		return err
	}
	w.fields = append(w.fields, sparseField{field: field, target: uint64(ptr)})
	return nil
}

// recordJIF notes a JPEGInterchangeFormat(Length) entry. exif.extractJPEGThumbnail
// reads the first 4 bytes of the Value of each: an inline 4-byte value.
func (w *sparseWalker) recordJIF(tag uint16, e []byte, field, valueLen uint64, jif *jifFields) {
	if valueLen < 4 {
		jif.short = true
		return
	}
	v := uint64(w.order.Uint32(e[8:]))
	if tag == tagJPEGInterFmt {
		jif.offField, jif.offVal = field, v
		jif.nOff++
	} else {
		jif.lenField, jif.lenVal = field, v
		jif.nLen++
	}
}

// collectThumbnail adds the thumbnail range of an IFD that carries both
// JPEGInterchangeFormat and its length, EXIF §4.5.5, as exif.extractJPEGThumbnail
// reads it. A repeated entry is resolved by exif.Parse in a way (first one wins,
// with a warning) that the walk does not mirror: not compactable.
func (w *sparseWalker) collectThumbnail(jif jifFields) error {
	if jif.nOff > 1 || jif.nLen > 1 {
		return errNotCompactable
	}
	if jif.nOff == 0 || jif.nLen == 0 || jif.short || jif.lenVal == 0 {
		return nil
	}
	end := jif.offVal + jif.lenVal
	if end > w.size {
		return nil // exif.Parse leaves ThumbnailData nil: the range is not read
	}
	if w.dataBytes += jif.lenVal; w.dirBytes+w.dataBytes > w.limit {
		return errNotCompactable
	}
	w.data = append(w.data, sparseRange{jif.offVal, end})
	w.fields = append(w.fields, sparseField{field: jif.offField, target: jif.offVal})
	return nil
}

// sparseRegion is a run of file bytes copied to the stream.
type sparseRegion struct {
	start, end uint64 // in the file
	pos        uint64 // in the stream
}

// regions checks that the ranges can be compacted and merges them into the
// regions of the stream, in file order. The header [0, 8) is always in the
// first region, at stream offset 0.
func (w *sparseWalker) regions() ([]sparseRegion, bool) {
	slices.SortFunc(w.dirs, func(a, b sparseRange) int { return cmpU64(a.start, b.start) })
	for i := 1; i < len(w.dirs); i++ {
		if w.dirs[i].start < w.dirs[i-1].end {
			return nil, false // overlapping directories: the stream cannot carry both
		}
	}
	data := mergeRanges(w.data, 0)
	if overlapsAny(w.dirs, data) || (len(data) > 0 && data[0].start < 8) {
		return nil, false // a value over a directory or the header: the offset fields could not be rewritten safely
	}
	// The MakerNote must stay at its offset: layouts that count their offsets from
	// the TIFF header resolve them against the stream (exif makerNoteView), so it
	// must lie in the first region, which keeps the file's offsets.
	all := make([]sparseRange, 0, len(w.dirs)+len(data)+1)
	all = append(all, sparseRange{0, 8})
	all = append(all, w.dirs...)
	all = append(all, data...)
	merged := mergeRanges(all, sparseMergeGap)
	for i := range merged {
		merged[i].end = min(merged[i].end, w.size)
	}
	first := merged[0]
	for _, m := range w.mn {
		if m.end > first.end {
			return nil, false
		}
	}
	regions := make([]sparseRegion, len(merged))
	var pos uint64
	for i, m := range merged {
		regions[i] = sparseRegion{m.start, m.end, pos}
		pos += m.end - m.start
	}
	return regions, true
}

func cmpU64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// mergeRanges sorts rs and joins the ranges that overlap or lie within gap
// bytes of each other.
func mergeRanges(rs []sparseRange, gap uint64) []sparseRange {
	out := slices.Clone(rs)
	slices.SortFunc(out, func(a, b sparseRange) int { return cmpU64(a.start, b.start) })
	n := 0
	for _, r := range out {
		if n > 0 && r.start <= out[n-1].end+gap {
			out[n-1].end = max(out[n-1].end, r.end)
			continue
		}
		out[n] = r
		n++
	}
	return out[:n]
}

// overlapsAny reports whether a range of dirs (sorted, disjoint) overlaps one of
// data (sorted, disjoint).
func overlapsAny(dirs, data []sparseRange) bool {
	for _, d := range dirs {
		i := sort.Search(len(data), func(i int) bool { return data[i].end > d.start })
		if i < len(data) && data[i].start < d.end {
			return true
		}
	}
	return false
}

func regionsBytes(rs []sparseRegion) uint64 {
	last := rs[len(rs)-1]
	return last.pos + last.end - last.start
}

// mapOffset returns the stream position of the file offset off, which lies in
// one of the regions.
func mapOffset(rs []sparseRegion, off uint64) (uint64, bool) {
	i := sort.Search(len(rs), func(i int) bool { return rs[i].end > off })
	if i == len(rs) || rs[i].start > off {
		return 0, false
	}
	return rs[i].pos + off - rs[i].start, true
}

// build reads the regions and rewrites the offset fields. It returns nil when a
// region cannot be read (the file changed under the walk).
func (w *sparseWalker) build(regions []sparseRegion, total uint64) *Sparse {
	stream := make([]byte, total)
	for _, rg := range regions {
		dst := stream[rg.pos : rg.pos+rg.end-rg.start]
		n := uint64(0)
		if rg.start < uint64(len(w.initial)) {
			n = uint64(copy(dst, w.initial[rg.start:])) //nolint:gosec // G115: copy returns a count >= 0
		}
		if n < uint64(len(dst)) {
			if _, err := w.r.Seek(int64(rg.start+n), io.SeekStart); err != nil { //nolint:gosec // G115: bounded by the file size
				return nil
			}
			if _, err := io.ReadFull(w.r, dst[n:]); err != nil {
				return nil
			}
		}
	}
	sp := &Sparse{stream: stream, headLen: int(regions[0].end - regions[0].start), patches: make([]sparsePatch, 0, len(w.fields))} //nolint:gosec // G115: first region <= total <= maxSparseBytes
	for _, f := range w.fields {
		at, ok1 := mapOffset(regions, f.field)
		to, ok2 := mapOffset(regions, f.target)
		if !ok1 || !ok2 {
			return nil
		}
		p := sparsePatch{pos: uint32(at)} //nolint:gosec // G115: at < total <= maxSparseBytes
		copy(p.orig[:], stream[at:at+4])
		sp.patches = append(sp.patches, p)
		w.order.PutUint32(stream[at:at+4], uint32(to)) //nolint:gosec // G115: to < total <= maxSparseBytes
	}
	sp.rawIPTC, sp.rawXMP = tiffscan.ExtractTagValues(stream, w.order.Uint32(stream[4:]), w.order)
	return sp
}
