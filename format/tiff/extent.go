package tiff

// extent.go — task #289 (Sprint 44, Batch F): compute the exact byte range a
// TIFF-family file's METADATA occupies (every IFD in IFD0's next-IFD chain,
// ExifIFD, GPSIFD, InteropIFD, and SubIFDs — precisely the set exif.Parse
// itself traverses — plus every out-of-line tag value within them), so
// Extract can read and retain only that prefix instead of the whole file.
//
// Real TIFF/CR2/NEF/ARW/DNG files place this metadata in the first fraction
// of a percent of the file; the remainder is strip/tile image data (and, for
// NEF/ARW-family previews, embedded JPEG thumbnails) that this package's
// EXIF-only Extract has never needed to read.
//
// Correctness contract: the scanner walks EXACTLY the offsets exif.Parse
// would follow, extending the needed extent for every out-of-line entry's
// declared (type × count) byte range — including StripOffsets/
// StripByteCounts/TileOffsets/TileByteCounts/JPEGInterchangeFormat*, which
// ARE parsed tag values exif.Parse stores, even though their VALUES describe
// image data this scanner never follows. Only the pixel/thumbnail bytes
// those values point to are excluded — and they are excluded "for free",
// because nothing here ever treats a StripOffsets/JPEGInterchangeFormat
// VALUE as an offset to recurse into. A MakerNote's OWN internal structure
// (e.g. Nikon's embedded TIFP) is never walked either: exif.Parse itself
// only ever slices a MakerNote's OWN declared (type × count) blob out of the
// outer buffer before handing it to a manufacturer-specific sub-parser, so
// including that declared range — which the generic out-of-line-entry rule
// already does, with no special-casing — is sufficient for parity with
// whole-file parsing, matching whatever exif.Parse itself would have done
// with that same declared range on a full-file read.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// extentInitialPrefixSize is the size of the first, unconditional read
// Extract performs before any extent scanning begins. Chosen generously
// enough that the overwhelming majority of real files' IFD0 (and its
// immediate out-of-line values) fit within it, so scanMetadataExtent's
// growth loop converges in a single pass for the common case; files smaller
// than this are read in full by this one read; larger files rarely need more
// than one or two growth iterations, per the extent-to-file-size ratios
// (0.03-1.1%) tiffextent.py's profiling-round measurements found.
const extentInitialPrefixSize = 65536

// smallFileWholeReadThreshold: files at or below this size skip the extent
// scanner entirely and are read whole (see Extract). See scanMetadataExtent's
// doc comment for why the scanner's own machinery costs more than it saves
// below this size. A corpus-wide per-file Read benchmark bounded every
// regressed (>1.20x head) file at ~2.6 MiB; 4 MiB leaves comfortable headroom
// above that without giving up any of the scanner's benefit for real camera
// RAW files, which are tens of MB (NEF/ARW/DNG/CR2 in this repo's own e2e
// fixtures range 22-41 MB) — a plain whole-file read of a few MiB is already
// cheap in absolute terms regardless of how small that file's own metadata
// happens to be, so there is no real file size range this threshold
// sacrifices meaningful savings for.
const smallFileWholeReadThreshold = 4 << 20 // 4 MiB

// maxExtentTraverseIFDs bounds the number of next-IFD chain links a single
// walkChain call will follow. Mirrors exif's own unexported
// maxTraverseChainIFDs (exif/ifd.go) so a crafted next-IFD cycle cannot drive
// unbounded work; combined with seenIFD's cycle guard, a chain that loops
// back on itself is caught immediately regardless of this ceiling.
const maxExtentTraverseIFDs = 512

// maxExtentIFDEntries bounds the entry count read from a single IFD, exactly
// as extractTagValuesBigTIFF already does for the same reason: classic TIFF's
// own count field is a uint16 (max 65535) so this is a hard ceiling there;
// BigTIFF's is a uint64 and needs the explicit cap.
const maxExtentIFDEntries = 65535

// maxExtentSubIFDs bounds the number of pointers a single SubIFDs (0x014A)
// array contributes to the scan. Real DNG/TIFF-EP files declare 1-3; this is
// generous headroom against a crafted huge count, mirroring (at read-scale,
// not write-scale) relocate.go's maxSubIFDsPerEntry.
const maxExtentSubIFDs = 1024

// maxExtentGrowthPasses bounds the number of grow-and-rescan iterations
// scanMetadataExtent performs. The traversal itself is already depth- and
// chain-length-bounded (maxSubIFDDepth, maxExtentTraverseIFDs), so in
// practice this converges in 1-3 passes; this ceiling is defence-in-depth
// against a pathological, deliberately-crafted file designed to force one
// additional IFD to become visible per growth step.
const maxExtentGrowthPasses = 64

// extentScan holds the mutable state of a single metadata-extent walk over
// buf. need accumulates the largest "one past the end" byte position any
// visited offset or out-of-line value implies; seenIFD is a scan-wide cycle
// guard shared across the IFD0 chain and every ExifIFD/GPSIFD/InteropIFD/
// SubIFD branch reached from it (so a cycle that crosses branches, e.g. a
// SubIFD pointing back to IFD0, is caught too).
type extentScan struct {
	buf     []byte
	order   binary.ByteOrder
	bigTIFF bool
	need    uint64
	seenIFD map[uint64]bool
}

// grow records that end bytes are needed, if larger than any previously
// recorded requirement.
func (s *extentScan) grow(end uint64) {
	if end > s.need {
		s.need = end
	}
}

// fits reports whether a width-byte value starting at off lies entirely
// within a buffer of length n, computed without any risk of overflow: off
// and width are read from (or derived from) untrusted file content and can
// individually be as large as MaxUint64, so a naive `off+width > n` check
// can itself overflow and wrap around to a small value, incorrectly
// reporting a huge, out-of-bounds offset as "fits". This was caught by
// FuzzTIFFExtract during development (a crafted BigTIFF ifd0Off of
// MaxUint64 made off+countW wrap to a value smaller than len(buf), reaching
// s.buf[off:] and panicking).
func fits(off, width, n uint64) bool {
	if off > n {
		return false
	}
	return width <= n-off
}

// readUint reads a width-byte (2, 4, or 8) unsigned integer at off. Callers
// must have already verified fits(off, width, len(s.buf)).
func (s *extentScan) readUint(off, width uint64) uint64 {
	switch width {
	case 2:
		return uint64(s.order.Uint16(s.buf[off:]))
	case 4:
		return uint64(s.order.Uint32(s.buf[off:]))
	case 8:
		return s.order.Uint64(s.buf[off:])
	default:
		return 0
	}
}

// walkChain follows the next-IFD chain starting at off (IFD0's own chain, or
// the — conventionally absent, but structurally possible, and exif.Parse
// does follow it via the same generic traverse — chain off an ExifIFD/
// GPSIFD/InteropIFD pointer). depth is the SubIFD/sub-pointer NESTING depth
// (not the chain length, which is bounded separately by
// maxExtentTraverseIFDs iterations below).
//
// materializesThumbnail must be true only when every IFD reached from off is
// one exif.Parse itself decodes into a *exif.IFD with a ThumbnailData field —
// IFD0, its own Next chain, and the ExifIFD/GPSIFD/InteropIFD pointer
// targets. It must be false for a SubIFDs (0x014A) array target: exif.Parse
// never materialises SubIFDs as *exif.IFD objects at all (enumerateSubIFDs
// re-scans base directly at write time instead — see relocate.go), so a
// JPEGInterchangeFormat pair declared INSIDE a SubIFD is never read back into
// any ThumbnailData field my ThumbnailData-driven growth (see walkIFD) exists
// to protect. Including it anyway would pointlessly balloon the prefix for
// any DNG/NEF/CR2 whose SubIFD carries a multi-megabyte medium-resolution
// preview — found via BenchmarkRead/nef on testdata/corpus/raw/
// metadata-extractor/Nikon D810.nef (prefix grew from ~800 KB to ~2.7 MB with
// no matching gain: every top-level IFD's ThumbnailData was nil either way).
func (s *extentScan) walkChain(off uint64, depth int, materializesThumbnail bool) {
	for range maxExtentTraverseIFDs {
		if off == 0 || s.seenIFD[off] {
			return
		}
		s.seenIFD[off] = true
		next, ok := s.walkIFD(off, depth, materializesThumbnail)
		if !ok {
			return
		}
		off = next
	}
}

// jifScratch accumulates a single IFD's TagJPEGInterchangeFormat (0x0201) /
// TagJPEGInterchangeFormatLength (0x0202) pair while walkIFD iterates that
// IFD's entries, mirroring exif.Parse's own extractJPEGThumbnail (exif/ifd.go),
// which "runs on every parsed IFD" and requires both tags before extracting
// anything. It is scoped to a single walkIFD call (one instance per IFD, not
// shared across IFDs), since two different IFDs (e.g. IFD0 and IFD1) can each
// declare their own independent embedded thumbnail.
type jifScratch struct {
	off, length  uint64
	offOK, lenOK bool
}

// walkIFD reads the IFD at off — classic: 2-byte count + 12-byte entries +
// 4-byte next; BigTIFF: 8-byte count + 20-byte entries + 8-byte next —
// extends s.need for every entry's value range (via walkEntry) and returns
// the next-IFD offset. ok is false when off's own count field or its entries
// are not yet fully within s.buf: s.grow has already recorded what is
// needed, and the caller stops so the next pass (after the buffer grows)
// can retry this exact IFD from scratch.
func (s *extentScan) walkIFD(off uint64, depth int, materializesThumbnail bool) (next uint64, ok bool) {
	countW, entryW, nextW := uint64(2), uint64(12), uint64(4)
	if s.bigTIFF {
		countW, entryW, nextW = 8, 20, 8
	}
	n := uint64(len(s.buf))
	if !fits(off, countW, n) {
		// off itself may be adversarially huge (a crafted ifd0Off, next-IFD
		// pointer, or sub-pointer): grow toward it only when doing so cannot
		// itself overflow (off <= maxFileSize is enforced by the caller
		// clamping s.need before the next growBuffer call; here we only
		// ever grow to off+countW when that sum is representable).
		if off <= ^uint64(0)-countW {
			s.grow(off + countW)
		}
		return 0, false
	}
	count := min(s.readUint(off, countW), maxExtentIFDEntries)
	// off <= n (proven by fits above) and n <= maxFileSize (enforced by
	// Extract before scanning ever starts), so the following arithmetic —
	// entirely in terms of off, the small fixed widths, and the
	// already-clamped count — cannot overflow uint64.
	entriesEnd := off + countW + count*entryW
	nextOff := entriesEnd + nextW
	if nextOff > n {
		s.grow(nextOff)
		return 0, false
	}
	s.grow(nextOff)

	var jif jifScratch
	for i := range count {
		s.walkEntry(off+countW+i*entryW, entryW, depth, &jif)
	}
	s.growForJIFThumbnail(jif, materializesThumbnail)
	return s.readUint(entriesEnd, nextW), true
}

// growForJIFThumbnail extends s.need to cover an IFD's declared embedded JPEG
// thumbnail bytes, split out of walkIFD to keep its cyclomatic complexity
// within the project's gocyclo threshold.
//
// EXIF §4.5.5 / exif.Parse's extractJPEGThumbnail (exif/ifd.go): when an IFD
// carries both 0x0201 and 0x0202 with a non-zero declared length, exif.Parse
// slices exactly those bytes out of its parse buffer into IFD.ThumbnailData —
// a field exif.Encode re-embeds verbatim on write. Without extending s.need
// here, a prefix that omits those bytes makes extractJPEGThumbnail's bounds
// check fail silently (ThumbnailData ends up nil), which does not corrupt the
// file but does make the embedded thumbnail take a different,
// non-HEAD-identical code path (and file position) on write:
// enumerateImageBlocks treats 0x0201/0x0202 as a generic image block instead
// of trusting exif.Encode to have already embedded ThumbnailData, relocating
// the same bytes to a different offset. Found via #289's golden-hash gate on
// testdata/corpus/raw/metadata-extractor/Canon EOS 70D.cr2 (and 2 other
// corpus files): Write output differed from HEAD by file layout only — the
// thumbnail bytes themselves were present and correct in both, just at
// different offsets — but the AC requires byte-identical output, and
// robustness should never depend on this coincidence.
//
// materializesThumbnail gates this entirely off for a SubIFD (see walkChain's
// doc comment): exif.Parse never materialises a SubIFD as a *exif.IFD with
// its own ThumbnailData field, so growing for a JIF pair declared inside one
// would only inflate the prefix with no round-trip benefit.
func (s *extentScan) growForJIFThumbnail(jif jifScratch, materializesThumbnail bool) {
	if !materializesThumbnail || !jif.offOK || !jif.lenOK || jif.length == 0 {
		return
	}
	const maxU64 = ^uint64(0)
	if jif.off <= maxU64-jif.length {
		s.grow(jif.off + jif.length)
	}
}

// entryValue is a decoded IFD entry's value location and shape, resolved by
// resolveEntryValue.
type entryValue struct {
	tag   exif.TagID
	count uint64
	sz    uint64 // width of one element, in bytes
	off   uint64 // file offset where the count*sz value bytes actually live
}

// resolveEntryValue decodes the entry at e (already validated to fit within
// s.buf) and extends s.need to cover its declared out-of-line value range
// when it has one. ok is false when decoding must wait for a future pass (an
// out-of-line value's own pointer field isn't in s.buf yet — s.grow has
// already recorded what is needed) or when the entry is unusable (zero
// size/count, or a count*size product that would overflow uint64).
func (s *extentScan) resolveEntryValue(e uint64) (ev entryValue, ok bool) {
	n := uint64(len(s.buf))
	tag := exif.TagID(s.order.Uint16(s.buf[e:]))
	typ := s.order.Uint16(s.buf[e+2:])

	var count, sz, voOff, voWidth uint64
	if s.bigTIFF {
		count = s.order.Uint64(s.buf[e+4:])
		sz = typeSizeBigTIFF(typ)
		voOff, voWidth = e+12, 8
	} else {
		count = uint64(s.order.Uint32(s.buf[e+4:]))
		sz = uint64(typeSize(typ))
		voOff, voWidth = e+8, 4
	}
	if sz == 0 || count == 0 {
		return entryValue{}, false
	}
	const maxU64 = ^uint64(0)
	if count > maxU64/sz {
		return entryValue{}, false // corrupt/adversarial: sz*count would overflow uint64
	}
	total := sz * count
	inlineLimit := uint64(4)
	if s.bigTIFF {
		inlineLimit = 8
	}

	// arrayOff is where the entry's actual value(s) live: at voOff itself
	// when inline, or at the file offset voOff points to when out-of-line.
	// TIFF 6.0 §2 / BigTIFF spec §2: the inline-vs-out-of-line rule itself.
	arrayOff := voOff
	if total > inlineLimit {
		// voOff is derived from the already-validated e, so fits(voOff,
		// voWidth, n) cannot overflow here.
		if !fits(voOff, voWidth, n) {
			s.grow(voOff + voWidth)
			return entryValue{}, false
		}
		arrayOff = s.readUint(voOff, voWidth)
		if arrayOff > maxU64-total {
			return entryValue{}, false // corrupt/adversarial: arrayOff+total would overflow uint64
		}
		s.grow(arrayOff + total)
	}
	// Inline entries need nothing beyond what walkIFD already required for
	// the whole entry (e+entryW); arrayOff==voOff is already inside it.
	return entryValue{tag: tag, count: count, sz: sz, off: arrayOff}, true
}

// recordJIFValue reads ev's resolved value (a TagJPEGInterchangeFormat or
// TagJPEGInterchangeFormatLength entry) into jif, growing s.need first if the
// value bytes are not yet within s.buf. ev.off already accounts for both the
// inline and out-of-line cases (resolveEntryValue), so the same fits+read
// pattern walkEntry uses for the ExifIFD/GPSIFD/InteropIFD pointer tags
// applies here unchanged. Split out of walkEntry to keep its cyclomatic
// complexity within the project's gocyclo threshold.
func (s *extentScan) recordJIFValue(ev entryValue, n uint64, jif *jifScratch) {
	if !fits(ev.off, ev.sz, n) {
		const maxU64 = ^uint64(0)
		if ev.off <= maxU64-ev.sz {
			s.grow(ev.off + ev.sz)
		}
		return
	}
	val := s.readUint(ev.off, ev.sz)
	if ev.tag == exif.TagJPEGInterchangeFormat {
		jif.off, jif.offOK = val, true
	} else {
		jif.length, jif.lenOK = val, true
	}
}

// walkEntry inspects a single IFD entry at e (entryW bytes: 12 for classic,
// 20 for BigTIFF), extends s.need for its declared out-of-line value range
// (via resolveEntryValue), records a TagJPEGInterchangeFormat(Length) value
// into jif (see jifScratch), then recurses into ExifIFD/GPSIFD/InteropIFD/
// SubIFDs pointer tags.
//
// entryW is passed rather than re-derived from s.bigTIFF purely to avoid a
// second branch; the field-layout offsets within the entry (tag@0, type@2,
// count@4, val-or-off@8 classic / @12 BigTIFF) are fixed by entryW's value.
func (s *extentScan) walkEntry(e, entryW uint64, depth int, jif *jifScratch) {
	n := uint64(len(s.buf))
	if !fits(e, entryW, n) {
		s.grow(e + entryW) // e is derived from an already-validated off (walkIFD); cannot overflow
		return
	}
	ev, ok := s.resolveEntryValue(e)
	if !ok {
		return
	}

	// Recorded unconditionally (not gated on depth): TagJPEGInterchangeFormat
	// and TagJPEGInterchangeFormatLength are leaf values, never a recursion
	// trigger, and exif.Parse's extractJPEGThumbnail applies to every parsed
	// IFD regardless of nesting depth.
	if ev.tag == exif.TagJPEGInterchangeFormat || ev.tag == exif.TagJPEGInterchangeFormatLength {
		s.recordJIFValue(ev, n, jif)
		return
	}

	if depth >= maxSubIFDDepth {
		return
	}
	s.walkRecursiveTag(ev, n, depth)
}

// walkRecursiveTag dispatches an entry's ExifIFD/GPSIFD/InteropIFD/SubIFDs
// pointer tag to the appropriate recursive walk. Split out of walkEntry to
// keep its cyclomatic complexity within the project's gocyclo threshold.
func (s *extentScan) walkRecursiveTag(ev entryValue, n uint64, depth int) {
	switch ev.tag {
	case exif.TagExifIFDPointer, exif.TagGPSIFDPointer, exif.TagInteropIFDPointer:
		// EXIF §4.6.3: always a single LONG/LONG8/IFD8 pointer value
		// (count==1); ev.sz is that pointer's own declared width. ev.off MAY
		// be an adversarially huge, untrusted file offset (resolveEntryValue
		// reads it straight from file content for an out-of-line entry), so
		// this bounds check uses fits, never a raw addition.
		const maxU64 = ^uint64(0)
		if !fits(ev.off, ev.sz, n) {
			if ev.off <= maxU64-ev.sz {
				s.grow(ev.off + ev.sz)
			}
			return
		}
		if ptr := s.readUint(ev.off, ev.sz); ptr != 0 {
			// true: ExifIFD/GPSIFD/InteropIFD are materialised as *exif.IFD
			// fields on exif.EXIF, each independently eligible for its own
			// extractJPEGThumbnail call (see walkChain's doc comment).
			s.walkChain(ptr, depth+1, true)
		}
	case exif.TagSubIFDs:
		// TIFF Extension §F / Adobe DNG Spec §4: an array of ev.count
		// pointers, each ev.sz bytes wide (the entry's own declared type
		// width).
		s.walkSubIFDArray(ev.off, ev.count, ev.sz, depth+1)
	}
}

// walkSubIFDArray follows each of (up to maxExtentSubIFDs) count pointers in
// a SubIFDs (0x014A) array, each elemWidth bytes wide, starting at arrayOff.
// arrayOff is an untrusted file offset (read from file content by the
// caller), so every offset derived from it here is bounds-checked with fits,
// never a raw addition.
func (s *extentScan) walkSubIFDArray(arrayOff, count, elemWidth uint64, depth int) {
	if count > maxExtentSubIFDs {
		count = maxExtentSubIFDs
	}
	n := uint64(len(s.buf))
	const maxU64 = ^uint64(0)
	for i := range count {
		if arrayOff > maxU64-i*elemWidth {
			return // corrupt/adversarial: arrayOff+i*elemWidth would overflow uint64
		}
		p := arrayOff + i*elemWidth
		if !fits(p, elemWidth, n) {
			if p <= maxU64-elemWidth {
				s.grow(p + elemWidth)
			}
			return
		}
		if ptr := s.readUint(p, elemWidth); ptr != 0 {
			// false: a SubIFD is never materialised as a *exif.IFD by
			// exif.Parse (see walkChain's doc comment) — nothing ever reads
			// a ThumbnailData field back from it, so growing for its own
			// JPEGInterchangeFormat pair would only inflate the prefix with
			// no round-trip benefit.
			s.walkChain(ptr, depth, false)
		}
	}
}

// scanExtentPass runs one full walk of everything currently reachable within
// buf and returns the largest byte position the walk determined is needed —
// which may exceed len(buf) when the walk had to stop early for lack of data.
func scanExtentPass(buf []byte, ifd0Off uint64, order binary.ByteOrder, bigTIFF bool) uint64 {
	s := &extentScan{buf: buf, order: order, bigTIFF: bigTIFF, seenIFD: make(map[uint64]bool, 16)}
	// true: IFD0 and every IFD in its own Next chain (IFD1, IFD2, ...) are
	// exactly what exif.Parse walks and materialises with a ThumbnailData
	// field (see walkChain's doc comment).
	s.walkChain(ifd0Off, 0, true)
	return s.need
}

// growBuffer extends buf to at least need bytes by reading ONLY the delta
// (bytes [len(buf):need)) from r and appending it — never re-reading bytes
// buf already holds. If r has fewer than need bytes available (e.g. need was
// computed from a corrupt offset that overruns the real file, or need
// exceeds maxFileSize and was clamped by the caller), the short read is not
// an error here: it returns whatever grown buffer resulted, and the caller's
// own convergence check (len(grown) <= len(buf)) detects that no further
// progress is possible.
func growBuffer(r io.ReadSeeker, buf []byte, need uint64) ([]byte, error) {
	if need <= uint64(len(buf)) {
		return buf, nil
	}
	if _, err := r.Seek(int64(len(buf)), io.SeekStart); err != nil {
		return buf, fmt.Errorf("tiff: seek: %w", err)
	}
	grown := make([]byte, need)
	copy(grown, buf)
	n, err := io.ReadFull(r, grown[len(buf):])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return buf, fmt.Errorf("tiff: read: %w", err)
	}
	return grown[:len(buf)+n], nil
}

// scanMetadataExtent computes and returns a prefix of the file (starting
// from byte 0, since initial already does) that covers every IFD and
// out-of-line value exif.Parse would need for a full parse, growing initial
// by re-reading from r as needed (bounded by maxFileSize and
// maxExtentGrowthPasses). fileSize is the source's total size in bytes.
// Precondition, enforced by the sole caller (Extract, tiff.go): 0 < fileSize
// <= maxFileSize — seekFileSize routes a reader that cannot report its own
// size to extractWholeFile instead of ever reaching this function, and
// Extract itself rejects fileSize > maxFileSize before scanning begins.
//
// Each pass computes a target buffer size via, in order:
//
//  1. Safety clamp to fileSize, then to maxFileSize as an independent,
//     redundant second bound (given the precondition above, fileSize is
//     already <= maxFileSize, so this second clamp is never load-bearing in
//     practice — it exists only so a future violation of that precondition
//     degrades to a bounded value instead of an unbounded one): no
//     legitimate metadata "need" can ever exceed how many bytes the file
//     actually has, so capping at fileSize FIRST — before the much looser
//     maxFileSize (256 MiB) bound — stops a corrupt or adversarial offset in
//     a file of any size from driving a wastefully huge allocation in
//     growBuffer before the short read that follows discovers the real file
//     had nothing near that many bytes to give. Found via a corpus-wide
//     per-file Read benchmark:
//     testdata/corpus/tiff/exiv2/2018-01-09-exiv2-crash-002.tiff (325 bytes)
//     measured ~268 MB (~maxFileSize) allocated per Read before this fix; 0
//     extra bytes after it (its own 325-byte initial prefix already
//     satisfies every real requirement, so no growBuffer call is even made).
//  2. Large-fraction snap: once a single pass's need already accounts for at
//     least 1/largeFractionDenom (10%) of the whole file, there is little
//     "prefix" benefit left worth protecting with further incremental
//     growth, so need is raised to fileSize outright. This is the signal
//     that distinguishes two patterns a pass-count- or fixed-factor-only
//     policy cannot tell apart, both of which need several small-increment
//     passes to fully resolve an IFD chain (each IFD's location is only
//     knowable after growing far enough to read the IFD that points to it):
//     real camera/RAW files' metadata stays a small, STABLE fraction of a
//     large file across every pass (e.g. raw/metadata-extractor/
//     Nikon D810.nef: need stays ~0.62% of the 40.7 MB file for all 3 of its
//     passes — this check never fires, so NEF keeps the small growth factor
//     below and converges at ~253 KB, nowhere near the 1 MiB Read AC); some
//     real, non-adversarial multi-IFD/multi-page TIFFs instead reveal a need
//     that GROWS toward a large fraction across passes (e.g.
//     tiff/metadata-extractor/m1-8110934bb3b18d0e87ccc1ddfc5f0107.tif: 15% ->
//     15% -> 31% on successive passes) — this check fires as soon as that
//     crossing happens, short-circuiting straight to a single final read
//     instead of chasing the remainder through several more small steps. A
//     first draft of this policy escalated on PASS NUMBER instead of this
//     fraction (any 2nd-or-later pass got a large growth factor); it
//     mis-fired on NEF precisely because NEF's later passes are real but
//     still tiny relative to fileSize, ballooning B/op past the 1 MiB Read
//     AC for a file whose actual need never remotely approached fileSize.
//  3. Tail-snap: if fewer than one initial-prefix-chunk's worth
//     (extentInitialPrefixSize, 64 KiB) of the file would remain unread
//     after satisfying the current pass's need, need is raised to fileSize —
//     catching the case (2) leaves uncovered: a need that is already close
//     to fileSize in absolute terms but happens to fall under the 10%
//     relative threshold only because fileSize itself is small. Observed on
//     testdata/corpus/tiff/metadata-extractor/Epson PerfectionV800.tiff (a
//     synthetic fixture where declared metadata is ~100% of an ~800 KB
//     file): the first pass's need (816,076 B, 99.6% of fileSize) is already
//     caught by check (2) here too, but this check is what would catch a
//     need that stalled at, say, 85%-95% instead.
//  4. Otherwise, plain doubling — max(need, 2×len(buf)), capped at fileSize
//     — bounds the total copied bytes summed across every pass to
//     O(final extent) instead of O(final extent × pass count) for a file
//     whose need happens to grow in many small increments without ever
//     crossing the (2)/(3) thresholds early. Found via the same corpus-wide
//     benchmark: testdata/corpus/tiff/exampletiffs/mri.tif (230,578 B, real,
//     non-adversarial file) needed enough small-increment passes to
//     allocate ~2.97 MB total (≈13× its own size) under a flat,
//     exact-need-sized growth policy; doubling converges the same file in a
//     single pass (its first pass's need, 67,088 B ≈ 29% of fileSize, is
//     actually caught by check (2) above — doubling alone is the fallback
//     for files whose fraction never climbs that high).
//
// Returns the final buffer. Reaching maxExtentGrowthPasses or maxFileSize
// without full convergence is not treated as an error: the caller compares
// the returned buffer's length against the walk's own final "need" figure
// (recomputed once more, cheaply) to decide whether to fall back to a
// whole-file read — see Extract.
func scanMetadataExtent(r io.ReadSeeker, initial []byte, ifd0Off uint64, order binary.ByteOrder, bigTIFF bool, fileSize uint64) ([]byte, error) {
	buf := initial
	for range maxExtentGrowthPasses {
		need := scanExtentPass(buf, ifd0Off, order, bigTIFF)
		if need <= uint64(len(buf)) {
			return buf, nil
		}
		target := nextGrowthTarget(need, uint64(len(buf)), fileSize)
		grown, err := growBuffer(r, buf, target)
		if err != nil {
			return buf, err
		}
		if uint64(len(grown)) <= uint64(len(buf)) {
			// No progress: r has no more data to offer (need pointed past
			// the real end of the stream). Stop; the caller's own
			// convergence check will decide what to do with this buffer.
			return grown, nil
		}
		buf = grown
	}
	return buf, nil
}

// nextGrowthTarget computes how large scanMetadataExtent's next growBuffer
// call should target, given this pass's raw need, the current buffer length,
// and the file's total size. Split out of scanMetadataExtent to keep its
// cyclomatic complexity within the project's gocyclo threshold; see
// scanMetadataExtent's own doc comment for the full rationale and
// corpus-measured evidence behind each of the four steps applied here (via
// clampNeed) and below.
func nextGrowthTarget(need, bufLen, fileSize uint64) uint64 {
	need = clampNeed(need, fileSize)
	target := need
	if geometric := bufLen * 2; geometric > target {
		target = geometric
	}
	if fileSize > 0 && target > fileSize {
		target = fileSize
	}
	if target > uint64(maxFileSize) { //nolint:gosec // G115: same rationale as clampNeed's own maxFileSize clamp — redundant given the fileSize>0-and-<=maxFileSize precondition documented on scanMetadataExtent, kept as an independent second bound
		target = uint64(maxFileSize) //nolint:gosec // G115: same rationale
	}
	return target
}

// clampNeed applies nextGrowthTarget's first three steps — the safety clamp,
// the large-fraction snap, and the tail-snap — to a single pass's raw need.
// Split out of nextGrowthTarget to keep its own cyclomatic complexity within
// the project's gocyclo threshold.
func clampNeed(need, fileSize uint64) uint64 {
	if need > fileSize {
		need = fileSize
	}
	// This second clamp is never load-bearing given the fileSize>0-and-<=
	// maxFileSize precondition documented on scanMetadataExtent (the clamp
	// above already bounds need to fileSize, itself already <= maxFileSize):
	// it is an independent, redundant second bound, not a fallback for a
	// fileSize of 0 or otherwise unknown — that case cannot occur here.
	if need > uint64(maxFileSize) { //nolint:gosec // G115: maxFileSize is a fixed, always-positive package constant (256 MiB)
		need = uint64(maxFileSize) //nolint:gosec // G115: same rationale
	}
	// Large-fraction snap: once a single pass's need already accounts for a
	// substantial fraction of the whole file (>= 1/largeFractionDenom, e.g.
	// 10%), read the rest of the file now instead of continuing incremental
	// growth toward it.
	const largeFractionDenom = 10 // need*10 >= fileSize <=> need >= 10% of fileSize
	if fileSize > 0 && need*largeFractionDenom >= fileSize {
		need = fileSize
	}
	// Tail-snap: if fewer than one initial-prefix-chunk's worth of the file
	// would remain unread after satisfying this pass's need, finish it now
	// in this same grow rather than leaving a near-certain follow-up pass
	// for the plain-doubling step below to eventually reach by overshoot.
	if fileSize > 0 && need < fileSize {
		if remaining := fileSize - need; remaining <= extentInitialPrefixSize {
			need = fileSize
		}
	}
	return need
}
