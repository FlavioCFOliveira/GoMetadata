package exif

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"sort"
	"sync"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
	"github.com/FlavioCFOliveira/GoMetadata/internal/metaerr"
)

// visitedPool recycles the maps used by traverse() to track visited IFD
// offsets. Reusing these maps avoids one allocation per Parse call on the
// hot path. The map is cleared before being returned to the pool.
var visitedPool = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
	New: func() any { return make(map[uint32]bool) },
}

// visitedPoolBigTIFF recycles the maps used by traverseBigTIFF() to track
// visited IFD offsets. BigTIFF uses uint64 offsets, so a separate pool is
// needed to avoid a type assertion in the hot path.
var visitedPoolBigTIFF = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure on BigTIFF parse path
	New: func() any { return make(map[uint64]bool) },
}

// maxArenaHints is the maximum number of per-IFD entry-count hints that can be
// stored in parseArena without a heap allocation. 16 covers all realistic camera
// EXIF trees (IFD0, IFD1, ExifIFD, InteropIFD, GPSIFD + spares).
const maxArenaHints = 16

// parseArena is a per-Parse-call allocation arena for IFD structs and their
// entry backing slices. It is created once in Parse (after a cheap pre-scan)
// and threaded to all traverseWithArena calls, eliminating the per-IFD
// *IFD + []IFDEntry pair of allocations.
//
// Arena safety contract (task #198, performance audit 2026-06-10):
//   - ifdBatch is allocated with exact capacity (ifdN == len); it must NEVER
//     be grown after construction — taking &ifdBatch[i] is safe only when
//     the slice header cannot move.
//   - Per-IFD entry sub-slices are cap-clamped (s[:0:count]) so any
//     slices.Insert beyond cap reallocates outside the arena and cannot
//     overwrite a neighbour's entry region.
//   - MakerNote IFDs (allocated from a separate MakerNote blob) are NOT
//     tracked by this arena; they use the regular per-call allocators.
//
// hints is a fixed-size array of per-IFD clamped entry counts, populated by
// scanAllClassicIFDs in the same IFD traversal order that Parse will use.
// traverseWithArena reads the next hint instead of re-reading the count field
// from the buffer, eliminating a duplicate memory access per IFD.
//
// Lifetime: parseArena is stack-allocated in Parse and escapes to the heap
// only if the Go escape analyser decides so (unavoidable because traverseWithArena
// takes a pointer). The IFD and IFDEntry backing slices within it outlive Parse
// and are retained by the caller through the EXIF struct fields.
type parseArena struct {
	ifdBatch   []IFD              // pre-allocated backing for all IFD structs
	entryBatch []IFDEntry         // pre-allocated backing for all IFDEntry slices
	hints      [maxArenaHints]int // per-IFD clamped entry counts, in traversal order
	ifdN       int                // next free slot in ifdBatch
	entryN     int                // next free position in entryBatch
	hintN      int                // number of valid hints populated by scanAllClassicIFDs
	hintRead   int                // next hint index to read in traverseWithArena
}

// allocIFD returns the next free IFD slot from the arena, or nil if the arena
// is nil or exhausted. The returned pointer is stable for the arena's lifetime.
func (a *parseArena) allocIFD() *IFD {
	if a == nil || a.ifdN >= len(a.ifdBatch) {
		return nil
	}
	p := &a.ifdBatch[a.ifdN]
	a.ifdN++
	return p
}

// allocEntries returns a length-zero, cap-clamped sub-slice of entryBatch for
// exactly count entries. Returns nil when the arena is nil or has fewer than
// count entries remaining (callers must fall back to make in that case).
//
// Cap-clamping (s[lo:lo:lo+count]) ensures that any append beyond cap
// reallocates outside the arena and cannot overwrite a neighbouring IFD's
// entry region — arena safety invariant. task #198.
func (a *parseArena) allocEntries(count int) []IFDEntry {
	if a == nil || count <= 0 || a.entryN+count > len(a.entryBatch) {
		return nil
	}
	lo := a.entryN
	a.entryN += count
	return a.entryBatch[lo : lo : lo+count]
}

// recordHint appends a clamped entry count hint for the next IFD in traversal
// order. Silently dropped when the hints array is full (fallback: traverseWithArena
// re-reads the count from the buffer).
func (a *parseArena) recordHint(count int) {
	if a.hintN < maxArenaHints {
		a.hints[a.hintN] = count
		a.hintN++
	}
}

// nextHint returns the pre-scanned clamped entry count for the current IFD and
// advances the hint cursor. Returns (0, false) when no more hints are available
// (traverseWithArena falls back to re-reading the count from the buffer).
func (a *parseArena) nextHint() (int, bool) {
	if a == nil || a.hintRead >= a.hintN {
		return 0, false
	}
	v := a.hints[a.hintRead]
	a.hintRead++
	return v, true
}

// EXIF sub-IFD pointer tag IDs used by scanSubIFDs.
// EXIF §4.6.3: ExifIFD pointer = 0x8769; GPS IFD pointer = 0x8825;
// Interop IFD pointer = 0xA005.
const (
	tagExifIFDPointer    TagID = 0x8769
	tagGPSIFDPointer     TagID = 0x8825
	tagInteropIFDPointer TagID = 0xA005
)

// scanSubIFDs counts IFDs and entries for the EXIF sub-IFDs (ExifIFD,
// InteropIFD, GPSIFD) reachable from already-known pointer offsets.
//
// This is the lazy-arena variant used when IFD0 has already been parsed by
// traverse and the caller wants to co-allocate only the sub-IFD structs and
// entry backing arrays.  Unlike scanAllClassicIFDs it does NOT re-scan IFD0
// (its entries are already in memory as IFDEntry values).
//
// exifOff and gpsOff are the raw uint32 offset values from the corresponding
// IFD pointer tags in IFD0 (0 means absent).  InteropIFD (0xA005) is found
// by scanning ExifIFD's entries.
//
// Arena hints are populated in the SAME order that traverseWithArena will
// consume them, which matches the call order in parseExifSubIFDs and
// parseGPSSubIFD:
//
//  1. ExifIFD (parseExifSubIFDs → traverseWithArena at the top of the function)
//  2. InteropIFD (parseExifSubIFDs → traverseWithArena inside the interop block)
//  3. GPSIFD (parseGPSSubIFD → traverseWithArena)
//
// Assumes sub-IFDs are single IFDs (no next-IFD chain) — this is universally
// true for compliant files and is the same assumption scanAllClassicIFDs makes
// in its fast path.  Count clamping matches parseSingleIFD.
//
// CIPA DC-008-2019 §4.5.2; EXIF §4.6.3; TIFF 6.0 §2.
func scanSubIFDs(b []byte, exifOff, gpsOff uint32, order binary.ByteOrder, arena *parseArena) (nSubIFDs, totalSubEntries int) { //nolint:gocyclo,cyclop // per-sub-IFD bounds+clamp guards are inherent; count is low
	const maxPrealloc = 1024

	// --- ExifIFD (hint slot 0) ---
	var interopOff uint32
	if exifOff != 0 && uint64(exifOff)+2 <= uint64(len(b)) { //nolint:nestif // bounds+fallback+interop scan are inherent in a safe pre-scan; complexity is low
		rawCount := int(order.Uint16(b[exifOff:]))
		exifPos := int(exifOff) + 2
		count := rawCount
		if exifPos+count*12 > len(b) {
			available := (len(b) - exifPos) / 12
			if available > 0 {
				count = available
			} else {
				goto gpsSection
			}
		}
		count = min(count, maxPrealloc)

		nSubIFDs++
		totalSubEntries += count
		arena.recordHint(count) // hint[0]: ExifIFD entry count

		// Scan ExifIFD for InteropIFD pointer (tag 0xA005).
		// Most entries are below 0xA005; the unsigned comparison provides a fast
		// exit for the common case.  InteropIFD can appear at most once.
		for i := 0; i < count; i++ { //nolint:intrange // binary parser: loop variable is a byte-slice offset multiplier
			eOff := exifPos + i*12
			if eOff+2 > len(b) {
				break
			}
			tag := TagID(order.Uint16(b[eOff:]))
			if tag != tagInteropIFDPointer {
				continue
			}
			if eOff+12 > len(b) {
				break
			}
			typ := DataType(order.Uint16(b[eOff+2:]))
			cnt := order.Uint32(b[eOff+4:])
			if cnt == 1 && (typ == TypeLong || typ == TypeShort) {
				interopOff = order.Uint32(b[eOff+8:])
			}
			break
		}
	}

	// --- InteropIFD (hint slot 1, if present) ---
	// Must be recorded BEFORE GPSIFD to match parseExifSubIFDs' call order
	// (InteropIFD traversal happens inside parseExifSubIFDs, before parseGPSSubIFD).
	if interopOff != 0 && uint64(interopOff)+2 <= uint64(len(b)) {
		rawCount := int(order.Uint16(b[interopOff:]))
		interopPos := int(interopOff) + 2
		count := rawCount
		if interopPos+count*12 > len(b) {
			available := (len(b) - interopPos) / 12
			if available > 0 {
				count = available
			} else {
				goto gpsSection
			}
		}
		count = min(count, maxPrealloc)

		nSubIFDs++
		totalSubEntries += count
		arena.recordHint(count) // hint[1]: InteropIFD entry count
	}

gpsSection:
	// --- GPSIFD (last hint slot) ---
	if gpsOff != 0 && uint64(gpsOff)+2 <= uint64(len(b)) {
		rawCount := int(order.Uint16(b[gpsOff:]))
		gpsPos := int(gpsOff) + 2
		count := rawCount
		if gpsPos+count*12 > len(b) {
			available := (len(b) - gpsPos) / 12
			if available > 0 {
				count = available
			} else {
				return nSubIFDs, totalSubEntries
			}
		}
		count = min(count, maxPrealloc)

		nSubIFDs++
		totalSubEntries += count
		arena.recordHint(count) // hint[last]: GPSIFD entry count
	}

	return nSubIFDs, totalSubEntries
}

// IFD represents a TIFF Image File Directory (TIFF §2).
// Entries must remain sorted by Tag in ascending order (TIFF §7) so that
// Get() can use binary search. Use set() to modify entries; code that
// appends to Entries directly must call sortEntries() afterwards.
//
// ThumbnailData holds the raw JPEG thumbnail bytes when an IFD carries a
// JPEG-compressed thumbnail (EXIF §4.5.5, tags 0x0201 JPEGInterchangeFormat
// and 0x0202 JPEGInterchangeFormatLength).  It is populated by parseSingleIFD
// so that Encode can re-append the bytes at the correct new offset and patch
// tag 0x0201 accordingly.  Nil means no JPEG thumbnail is attached.
type IFD struct {
	Entries       []IFDEntry
	Next          *IFD   // linked IFDs (e.g. IFD1 for thumbnail)
	ThumbnailData []byte // raw JPEG thumbnail bytes; non-nil when present
}

// IFDEntry represents a single TIFF directory entry (TIFF §2, 12 bytes each).
//
// Byte-order encoding (task #199, performance audit 2026-06-10):
// The byte order is stored as a 1-byte bool flag (bigEndian) instead of a
// 16-byte binary.ByteOrder interface.  The interface held a GC-scannable
// pointer on every entry, inflating arena backing arrays and GC scan work.
//
// Flag semantics:
//   - false = little-endian (zero value; matches the library's default LE
//     convention used throughout setters, ensureExifIFD, and ifd0ByteOrder).
//   - true  = big-endian (e.g. Motorola-order EXIF streams).
//
// No third state (nil) is needed: the old nil interface was a latent panic —
// any decoder method called on a nil-byteOrder entry would immediately crash.
// All Parse paths always set a concrete order; the ifd0ByteOrder() fallback
// to LittleEndian exists only for zero-valued programmatic EXIF structs (which
// never have their entries' decoder methods called with a nil byteOrder through
// a normal code path).  The zero value of bool (false = LE) preserves the same
// observable default behaviour.
//
// Decoder methods call the package-level e.order() helper which returns the
// binary.BigEndian or binary.LittleEndian singleton — both are zero-size globals,
// so no allocation occurs.  CIPA DC-008-2023 §4.6.2; TIFF 6.0 §2.
type IFDEntry struct {
	Tag   TagID
	Type  DataType
	Count uint32
	// Value holds the decoded value. For types whose total size fits in 4 bytes
	// the raw bytes are stored inline; otherwise this is a []byte slice into
	// the original buffer (zero-copy).
	Value     []byte
	bigEndian bool
	// rawOffset is the TIFF-stream offset stored in the value-or-offset field
	// when the value is out-of-line (totalSize > 4 for classic TIFF, > 8 for
	// BigTIFF).  Zero for inline values.  Stored as uint64 to preserve BigTIFF
	// offsets above 4 GiB without truncation; classic TIFF offsets fit in the
	// lower 32 bits.  Used by parseExifSubIFDs/parseExifSubIFDsBigTIFF to
	// record MakerNoteOffset/MakerNoteOffset64 without unsafe pointer arithmetic.
	rawOffset uint64
}

// order returns the binary.ByteOrder corresponding to the entry's bigEndian flag.
// binary.BigEndian and binary.LittleEndian are zero-size package-level singletons;
// returning them through the interface incurs no heap allocation.
//
// CIPA DC-008-2023 §4.6.2: TIFF byte order is uniform per stream; the flag
// records which stream this entry was parsed from or constructed for.
func (e *IFDEntry) order() binary.ByteOrder {
	if e.bigEndian {
		return binary.BigEndian
	}
	return binary.LittleEndian
}

// parseIFDEntry decodes a single 12-byte IFD entry starting at byte offset e
// within b, using the given byte order.
//
// Tag layout (TIFF §2, EXIF 2.32 CIPA DC-008-2019 §4.6.2):
//
//	bytes 0-1  tag ID (uint16)
//	bytes 2-3  data type (uint16)
//	bytes 4-7  value count (uint32)
//	bytes 8-11 value or offset: inline when totalSize ≤ 4, otherwise a uint32
//	           file offset pointing to the value data
//
// For unknown types (sz == 0) the raw 4-byte field is stored verbatim.
// Returns (zero, false) on any out-of-bounds condition.
//
// ifdStart and ifdEnd are the byte range [ifdStart, ifdEnd) that covers the
// IFD directory itself (count field + entry area + next-IFD pointer). When an
// OOL value offset falls inside this range, it aliases the directory bytes —
// a real-world defect; a warning string is returned and the entry is kept
// (lenient parse). A non-empty warning string indicates #132 value aliasing.
// Audit finding #132; TIFF 6.0 §2: value data must not overlap the directory.
func parseIFDEntry(b []byte, e, ifdStart, ifdEnd int, order binary.ByteOrder) (IFDEntry, bool, string) {
	// Each entry is exactly 12 bytes; verify the slice is long enough.
	if e+12 > len(b) {
		return IFDEntry{}, false, ""
	}

	// task #199: record byte order as a 1-bit flag instead of a 16-byte interface.
	// binary.BigEndian and binary.LittleEndian are package-level singletons (zero size);
	// comparing by identity is both safe and allocation-free.
	// CIPA DC-008-2023 §4.6.2; TIFF 6.0 §2.
	isBig := order == binary.BigEndian

	tag := TagID(order.Uint16(b[e:]))
	typ := DataType(order.Uint16(b[e+2:]))
	cnt := order.Uint32(b[e+4:])

	sz := typeSize(typ)
	totalSize := uint64(sz) * uint64(cnt)

	var value []byte
	switch {
	case sz == 0:
		// Unknown type: store the raw 4-byte offset/value field verbatim.
		value = b[e+8 : e+12]
	case totalSize > 4:
		// Value is out-of-line; bytes 8-11 are the file offset (TIFF §2).
		valOff := order.Uint32(b[e+8:])
		end := uint64(valOff) + totalSize
		if end > uint64(len(b)) {
			// Out-of-bounds offset: skip this entry gracefully.
			return IFDEntry{}, false, ""
		}
		// Audit finding #132: detect when the OOL value offset falls inside the
		// IFD directory region — the value bytes alias the directory structure.
		// TIFF 6.0 §2: value data must reside outside the directory area.
		// Lenient parse: keep the entry but surface a ParseWarning so the caller
		// can inspect the anomaly. Do not skip: the bytes may still be readable.
		var warn string
		if int(valOff) >= ifdStart && int(valOff) < ifdEnd {
			warn = fmt.Sprintf("exif: tag 0x%04X OOL value offset %d overlaps IFD directory [%d,%d); value bytes may be corrupt", tag, valOff, ifdStart, ifdEnd)
		}
		value = b[valOff:end]
		return IFDEntry{
			Tag:       tag,
			Type:      typ,
			Count:     cnt,
			Value:     value,
			bigEndian: isBig,
			rawOffset: uint64(valOff), // TIFF §2: offset to value data; used by MakerNoteOffset
		}, true, warn
	default:
		// Value is inline, left-justified in the 4-byte field (TIFF §2).
		value = b[e+8 : e+8+int(totalSize)]
	}

	return IFDEntry{
		Tag:       tag,
		Type:      typ,
		Count:     cnt,
		Value:     value,
		bigEndian: isBig,
	}, true, ""
}

// parseSingleIFD parses all entries at a single IFD offset within b and
// returns the parsed IFD, the next-IFD offset (0 if absent or unreadable),
// whether parsing succeeded, and any diagnostic warnings accumulated during
// parsing. It does not follow the next-IFD chain.
//
// Callers are responsible for cycle detection before calling this function.
//
// Lenient recovery rules (per-finding):
//
//   - #130 (CIPA DC-008 §4.5.2 / ExifTool behaviour / conformance R-05):
//     When count×12 exceeds the remaining buffer, the count is clamped to
//     floor(remaining/12). The entries that fit are parsed; a ParseWarning is
//     appended. This matches ExifTool and libexif's lenient behaviour — a hard
//     reject would silently discard all metadata in files with a one-off count.
//
//   - #126 (ExifTool / Exiv2 behaviour): Duplicate tags (same TagID appearing
//     more than once in an IFD) are deduped by retaining the FIRST occurrence
//     and dropping subsequent ones. A ParseWarning is appended for each dropped
//     duplicate. TIFF 6.0 §2 does not explicitly permit duplicate tags; ExifTool
//     and Exiv2 both keep the first occurrence, which is the de-facto standard.
//
//   - #132 (TIFF 6.0 §2): OOL value offsets that alias the IFD directory region
//     produce a ParseWarning but the entry is kept (lenient parse).
func parseSingleIFD(b []byte, offset uint32, order binary.ByteOrder) (*IFD, uint32, bool, []string) { //nolint:cyclop // lenient-parse recovery branches (R-05/#126/#132) are inherent in the spec-driven logic
	// Use uint64 arithmetic to avoid int truncation on 32-bit platforms
	// (GOARCH=386/arm): int(uint32 >= 2^31) is negative, which would cause the
	// guard to pass while the subsequent slice panics. Performing the comparison
	// in uint64 is safe on all platforms (task #74).
	if uint64(offset)+2 > uint64(len(b)) {
		return nil, 0, false, nil
	}

	count := int(order.Uint16(b[offset:]))
	pos := int(offset) + 2

	var warnings []string

	// Audit finding #130 (CIPA DC-008 §4.5.2; conformance rule R-05):
	// When count×12 exceeds the remaining buffer, clamp to the number of
	// complete entries that actually fit rather than hard-rejecting the whole
	// IFD. ExifTool and libexif both use this lenient strategy.
	if pos+count*12 > len(b) {
		available := (len(b) - pos) / 12
		if available <= 0 {
			// No entries fit at all — reject the IFD as unreadable.
			return nil, 0, false, nil
		}
		warnings = append(warnings, fmt.Sprintf(
			"exif: IFD at offset %d declares %d entries but only %d fit in the buffer (len %d); clamped — lenient parse (CIPA DC-008 §4.5.2)",
			offset, count, available, len(b)))
		count = available
	}

	// ifdStart/ifdEnd mark the IFD directory region [pos-2 .. pos+count*12+4)
	// (count field + entry area + next-IFD pointer) so parseIFDEntry can detect
	// OOL offsets that alias the directory (audit finding #132, TIFF 6.0 §2).
	ifdStart := int(offset) // starts at the count field
	ifdEnd := pos + count*12 + 4

	// Cap initial capacity to avoid over-allocating on crafted large counts.
	// The loop below will only append entries that fit within the validated buffer range.
	const maxIFDEntryPrealloc = 1024
	preallocCap := min(count, maxIFDEntryPrealloc)
	ifd := &IFD{Entries: make([]IFDEntry, 0, preallocCap)}
	return fillIFD(b, ifd, offset, pos, count, ifdStart, ifdEnd, order, warnings)
}

// parseSingleIFDInto is the arena-aware variant of parseSingleIFD.
// Instead of allocating a new *IFD and a fresh []IFDEntry backing array, it
// writes into the caller-supplied ifd (a pointer into a pre-allocated []IFD
// batch from parseArena) and uses entrySlice (a capped sub-slice of a
// pre-allocated []IFDEntry batch from parseArena) as the initial Entries
// backing store.
//
// The entrySlice must have len == 0 and cap == the clamped entry count for
// this IFD (as returned by parseArena.allocEntries). The cap-clamping ensures
// that any later slices.Insert on ifd.Entries reallocates outside the arena,
// never bleeding into a neighbour's region (task #198 arena safety contract).
//
// Callers must NOT re-use or grow the ifdBatch slice after calling this
// function — pointers to batch elements are retained by the caller.
func parseSingleIFDInto(b []byte, ifd *IFD, entrySlice []IFDEntry, offset uint32, order binary.ByteOrder) (uint32, bool, []string) { //nolint:cyclop // same lenient-parse branches as parseSingleIFD
	if uint64(offset)+2 > uint64(len(b)) {
		return 0, false, nil
	}

	count := int(order.Uint16(b[offset:]))
	pos := int(offset) + 2

	var warnings []string

	if pos+count*12 > len(b) {
		available := (len(b) - pos) / 12
		if available <= 0 {
			return 0, false, nil
		}
		warnings = append(warnings, fmt.Sprintf(
			"exif: IFD at offset %d declares %d entries but only %d fit in the buffer (len %d); clamped — lenient parse (CIPA DC-008 §4.5.2)",
			offset, count, available, len(b)))
		count = available
	}

	ifdStart := int(offset)
	ifdEnd := pos + count*12 + 4

	// Use the caller-supplied arena slice as the initial backing store.
	// entrySlice has len=0 and cap=slotCount (via arena.allocEntries).
	// We use entrySlice[:0:cap(entrySlice)] so ifd.Entries starts with the
	// correct capacity — using len(entrySlice) would give cap=0 since
	// allocEntries returns a zero-length slice.
	//
	// task #198: arena safety — cap-clamped sub-slice ensures append beyond
	// cap reallocates outside the arena and cannot overwrite a neighbour's region.
	ifd.Entries = entrySlice[:0:cap(entrySlice)]
	_, next, _, w := fillIFD(b, ifd, offset, pos, count, ifdStart, ifdEnd, order, warnings)
	return next, true, w
}

// fillIFD populates ifd.Entries from the IFD at the given offset/pos and
// applies the sort+dedup pass and thumbnail extraction. It returns ifd, the
// next-IFD offset, ok=true, and accumulated warnings.
//
// This function is the shared body of parseSingleIFD and parseSingleIFDInto,
// extracted to avoid code duplication. It assumes all count-clamping and OOB
// checks have already been performed by the caller.
//
// ifd.Entries must already be set to a slice with len==0 and the correct cap
// before this function is called.
func fillIFD(b []byte, ifd *IFD, offset uint32, pos, count, ifdStart, ifdEnd int, order binary.ByteOrder, warnings []string) (*IFD, uint32, bool, []string) { //nolint:cyclop // lenient-parse branches (#126/#132) are inherent
	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
		entry, ok, warn := parseIFDEntry(b, pos+i*12, ifdStart, ifdEnd, order)
		if warn != "" {
			warnings = append(warnings, warn)
		}
		if !ok {
			continue
		}
		ifd.Entries = append(ifd.Entries, entry)
	}

	// Sort entries by tag so Get() can use binary search (TIFF §7).
	// Real cameras produce sorted IFDs, but non-compliant files may not.
	// Use a stable sort so that duplicate tags preserve their original file order
	// before the dedup pass removes later occurrences (audit finding #126).
	// TIFF 6.0 §2 does not define behaviour for duplicate tags; ExifTool and
	// Exiv2 both keep the first occurrence — this is the de-facto standard.
	slices.SortStableFunc(ifd.Entries, func(a, b IFDEntry) int {
		return cmp.Compare(a.Tag, b.Tag)
	})

	// Audit finding #126: dedupe duplicate tags, keeping the FIRST occurrence.
	// After the stable sort, duplicates are adjacent. Walk forward and remove
	// any entry whose tag equals its predecessor. Record a warning for each drop.
	//
	// Arena safety (task #198): ifd.Entries is cap-clamped (s[:0:n]) so this
	// in-place compaction — which only shrinks the slice — stays within the
	// slice's own cap region and cannot corrupt a neighbouring IFD's entries.
	if len(ifd.Entries) > 1 {
		out := ifd.Entries[:1]
		for i := 1; i < len(ifd.Entries); i++ {
			if ifd.Entries[i].Tag == ifd.Entries[i-1].Tag {
				warnings = append(warnings, fmt.Sprintf(
					"exif: IFD at offset %d contains duplicate tag 0x%04X; keeping first occurrence (ExifTool/Exiv2 behaviour)",
					offset, ifd.Entries[i].Tag))
			} else {
				out = append(out, ifd.Entries[i])
			}
		}
		ifd.Entries = out
	}

	// Extract JPEG thumbnail bytes when both JPEGInterchangeFormat (0x0201) and
	// JPEGInterchangeFormatLength (0x0202) are present (EXIF §4.5.5).
	// task #199: extractJPEGThumbnail now derives order from the entry's bigEndian flag.
	ifd.ThumbnailData = extractJPEGThumbnail(b, ifd)

	// Read the next-IFD pointer (4 bytes after the last entry, TIFF §2).
	// Use the original (unclamped) count stored in the IFD count field for the
	// next-pointer position calculation — we always read it at pos+clampedCount*12
	// which is the end of the entries we parsed.
	nextPtrPos := pos + count*12
	if nextPtrPos+4 > len(b) {
		return ifd, 0, true, warnings
	}
	return ifd, order.Uint32(b[nextPtrPos:]), true, warnings
}

// extractJPEGThumbnail extracts the raw JPEG thumbnail bytes from b when the
// parsed ifd contains both TagJPEGInterchangeFormat (0x0201) and
// TagJPEGInterchangeFormatLength (0x0202) with valid values (EXIF §4.5.5).
// Returns nil when either tag is absent, malformed, or the indicated byte range
// falls outside b.  The returned slice is an independent copy so the IFD is not
// tied to the original parse buffer.
//
// BigTIFF awareness (#141): JPEGInterchangeFormat may carry a TypeLong8 (8-byte)
// offset in BigTIFF files.  When Value is 8 bytes we read the full uint64 offset
// via order.Uint64 so that thumbnails above 4 GiB are located correctly.  Classic
// TIFF (TypeLong, 4-byte offset) continues to use order.Uint32.  The length tag
// (0x0202) likewise accepts TypeLong8 — real BigTIFF thumbnail sizes will always
// fit in a uint32, but we read 64-bit and range-check before narrowing.
// BigTIFF spec §2 (Aware Systems / libtiff); EXIF §4.5.5.
//
// task #199: order is now read from jifEntry.order() (the entry's bigEndian flag)
// rather than a caller-supplied binary.ByteOrder parameter.  Both entries must
// belong to the same TIFF stream, so they carry the same order flag; the per-entry
// call is equivalent to the previous stream-level parameter at zero extra cost.
func extractJPEGThumbnail(b []byte, ifd *IFD) []byte { //nolint:gocyclo,cyclop // BigTIFF-aware type dispatch for TypeLong8/TypeLong offset and length; branches are inherent in the two-type handling
	jifEntry := ifd.Get(TagJPEGInterchangeFormat)
	if jifEntry == nil {
		return nil
	}
	jifLenEntry := ifd.Get(TagJPEGInterchangeFormatLength)
	if jifLenEntry == nil {
		return nil
	}

	// Derive byte order from the entry's own flag (task #199).
	// All entries in a TIFF IFD share the same stream byte order; reading it
	// from the entry is equivalent to the previous caller-supplied parameter.
	// CIPA DC-008-2023 §4.6.2; TIFF 6.0 §2.
	ord := jifEntry.order()

	// Read the offset: TypeLong8 (BigTIFF) = 8 bytes; TypeLong (classic) = 4 bytes.
	// BigTIFF spec §2: JPEGInterchangeFormat may be stored as TypeLong8 in BigTIFF
	// containers.  Callers must read 8 bytes for TypeLong8 or the high 32 bits are lost.
	var jifOff uint64
	switch {
	case jifEntry.Type == TypeLong8 && len(jifEntry.Value) >= 8:
		jifOff = ord.Uint64(jifEntry.Value)
	case len(jifEntry.Value) >= 4:
		jifOff = uint64(ord.Uint32(jifEntry.Value))
	default:
		return nil
	}

	// Read the length using the same type-aware logic.
	lenOrd := jifLenEntry.order()
	var jifLen64 uint64
	switch {
	case jifLenEntry.Type == TypeLong8 && len(jifLenEntry.Value) >= 8:
		jifLen64 = lenOrd.Uint64(jifLenEntry.Value)
	case len(jifLenEntry.Value) >= 4:
		jifLen64 = uint64(lenOrd.Uint32(jifLenEntry.Value))
	default:
		return nil
	}

	if jifLen64 == 0 || jifLen64 > uint64(len(b)) {
		return nil
	}
	// Narrow length to uint32: thumbnail byte counts that do not fit in uint32
	// are degenerate; reject rather than wrap.
	if jifLen64 > 1<<32-1 {
		return nil
	}
	jifLen := uint32(jifLen64) // jifLen64 ≤ 2^32-1 verified by the guard above

	end := jifOff + uint64(jifLen)
	if end > uint64(len(b)) {
		return nil
	}
	// Copy: the IFD must be independent of the original parse buffer so that
	// callers can discard the input bytes after Parse returns (TIFF §2).
	thumb := make([]byte, jifLen)
	copy(thumb, b[jifOff:end])
	return thumb
}

// traverse walks the IFD chain starting at offset within b, using the given
// byte order. It returns the root IFD and any diagnostic warnings accumulated
// during traversal.
//
// The next-IFD pointer chain is followed iteratively (not recursively) to
// prevent stack overflows on cyclic or deeply nested inputs (fuzz safety).
//
// Audit finding #131: when a non-zero next-IFD pointer is unreadable (out of
// bounds), a ParseWarning is recorded but parsing is not aborted — the IFDs
// already parsed are returned successfully. This matches ExifTool's behaviour
// of surfacing what it can rather than discarding a good IFD0 because IFD1's
// pointer is corrupt. TIFF 6.0 §2: next-IFD pointer 0 = end of chain.
//
// traverse is the allocation path for IFD chain traversal using individual
// per-IFD allocations (parseSingleIFD).  Used for MakerNote parsers,
// ParseIFDAt, and the lazy Parse path for IFD0 (task #198: IFD0 is parsed
// before we know whether sub-IFDs exist; the lazy sub-IFD arena handles those
// separately via traverseWithArena).
//
// Use uint64 arithmetic to avoid int truncation on 32-bit platforms
// (GOARCH=386/arm): int(uint32 >= 2^31) is negative, which would cause the
// guard to pass while the subsequent slice panics. Performing the comparison
// in uint64 is safe on all platforms (task #74).
//
// TIFF §2: the next-IFD pointer value 0 signals end-of-chain; it must NOT
// be treated as "no IFD" when offset=0 is the *starting* offset.  Canon,
// Sony, DJI, Samsung, Casio and Leica Type-0 MakerNotes are valid plain
// IFDs at file offset 0.  Using `first` lets us enter the loop exactly once
// regardless of offset, and then stop only when the *returned* next-IFD
// pointer is 0 (i.e., end-of-chain) or a cycle is detected.
func traverse(b []byte, offset uint32, order binary.ByteOrder) (*IFD, []string, error) { //nolint:gocyclo,cyclop // IFD chain traversal with per-finding warning paths; branches are inherent in the spec-driven logic
	if uint64(offset)+2 > uint64(len(b)) {
		return nil, nil, &metaerr.CorruptMetadataError{
			Format: "EXIF",
			Reason: fmt.Sprintf("IFD offset %d out of bounds (buf len %d)", offset, len(b)),
		}
	}

	// visited tracks offsets we have already started parsing to detect cycles.
	// Obtained from visitedPool to avoid a per-call allocation on the hot path.
	visited := visitedPool.Get().(map[uint32]bool) //nolint:forcetypeassert,revive // visitedPool.New always stores map[uint32]bool; pool invariant
	defer func() {
		for k := range visited {
			delete(visited, k)
		}
		visitedPool.Put(visited)
	}()

	var root, current *IFD
	var warnings []string
	cur := offset

	first := true
	for cur != 0 || first {
		first = false
		if visited[cur] {
			break // cycle detected — stop following the chain
		}
		visited[cur] = true

		ifd, next, ok, ifdWarnings := parseSingleIFD(b, cur, order)
		warnings = append(warnings, ifdWarnings...)
		if !ok {
			// Audit finding #131 (TIFF 6.0 §2): a non-zero next-IFD pointer that
			// cannot be parsed is reported as a ParseWarning. If this is the very
			// first IFD (root == nil) the parse fails normally; if we already have
			// at least one valid IFD, we stop the chain but return what we have.
			if root != nil && cur != 0 {
				warnings = append(warnings, fmt.Sprintf(
					"exif: next-IFD pointer 0x%08X is unreadable (out of bounds or corrupt); IFD chain terminated (TIFF 6.0 §2)",
					cur))
			}
			break
		}

		// Link into the chain.
		if root == nil {
			root = ifd
		} else {
			current.Next = ifd
		}
		current = ifd

		// Audit finding #131: if the next pointer is non-zero but falls outside the
		// buffer, record a warning and stop following the chain. parseSingleIFD will
		// handle it on the next iteration (returning !ok), but we can detect the
		// condition early here to attach a cleaner message.
		if next != 0 && uint64(next)+2 > uint64(len(b)) {
			warnings = append(warnings, fmt.Sprintf(
				"exif: next-IFD pointer 0x%08X points outside the buffer (len %d); IFD chain terminated (TIFF 6.0 §2)",
				next, len(b)))
			cur = 0 // stop the chain; IFDs parsed so far are valid
		} else {
			cur = next
		}
	}

	if root == nil {
		return nil, warnings, &metaerr.CorruptMetadataError{
			Format: "EXIF",
			Reason: fmt.Sprintf("IFD at offset %d could not be parsed (buf len %d)", offset, len(b)),
		}
	}
	return root, warnings, nil
}

// traverseWithArena is the arena-aware IFD chain traversal function.
// When arena is non-nil, IFD structs and their entry backing slices are drawn
// from the pre-allocated arena rather than being individually heap-allocated.
// When arena is nil (or exhausted) the function falls back to individual
// allocations, preserving correctness in all cases.
//
// Arena safety contract (task #198, performance audit 2026-06-10):
//   - parseArena.allocIFD() returns a stable pointer into ifdBatch; callers
//     may hold this pointer indefinitely.
//   - parseArena.allocEntries(count) returns a cap-clamped sub-slice (len=0,
//     cap=count) so any append beyond cap reallocates outside the arena and
//     cannot overwrite a neighbour's entry region.
//   - The arena is created ONCE per Parse call (in Parse) after a global pre-scan
//     (scanAllClassicIFDs) determines the exact total IFD and entry counts.
//     All subsequent traverseWithArena calls for that Parse draw from the same
//     arena, replacing the per-IFD pair of allocations (*IFD + []IFDEntry).
//
// This function also carries all the robustness guards from the previous
// traverse implementation: cycle detection via a pooled map, OOB guards on
// every buffer access, and lenient recovery on unreadable next-IFD pointers.
//
// TIFF §2: the next-IFD pointer value 0 signals end-of-chain; it must NOT
// be treated as "no IFD" when offset=0 is the *starting* offset (Canon, Sony,
// DJI, Samsung, Casio and Leica Type-0 MakerNotes are valid plain IFDs at
// file offset 0). The `first` flag ensures we enter the loop exactly once.
func traverseWithArena(b []byte, offset uint32, order binary.ByteOrder, arena *parseArena) (*IFD, []string, error) { //nolint:gocyclo,cyclop,funlen // IFD chain traversal with per-finding warning paths; branches are inherent in the spec-driven logic
	// Use uint64 arithmetic to avoid int truncation on 32-bit platforms
	// (GOARCH=386/arm): int(uint32 >= 2^31) is negative, which would cause the
	// guard to pass while the subsequent slice panics. Performing the comparison
	// in uint64 is safe on all platforms (task #74).
	if uint64(offset)+2 > uint64(len(b)) {
		return nil, nil, &metaerr.CorruptMetadataError{
			Format: "EXIF",
			Reason: fmt.Sprintf("IFD offset %d out of bounds (buf len %d)", offset, len(b)),
		}
	}

	// visited tracks offsets we have already started parsing to detect cycles.
	// Obtained from visitedPool to avoid a per-call allocation on the hot path.
	visited := visitedPool.Get().(map[uint32]bool) //nolint:forcetypeassert,revive // visitedPool.New always stores map[uint32]bool; pool invariant
	defer func() {
		for k := range visited {
			delete(visited, k)
		}
		visitedPool.Put(visited)
	}()

	var root, current *IFD
	var warnings []string
	cur := offset

	first := true
	for cur != 0 || first {
		first = false
		if visited[cur] {
			break // cycle detected — stop following the chain
		}
		visited[cur] = true

		var ifd *IFD
		var next uint32
		var ok bool
		var ifdWarnings []string

		// Arena path: attempt to allocate IFD slot and entry backing from the
		// pre-allocated arena.  allocIFD / allocEntries return nil when the arena
		// is nil or exhausted; in that case we fall through to individual allocs.
		if ifdPtr := arena.allocIFD(); ifdPtr != nil { //nolint:nestif // arena+hint+fallback branches are inherent in safe arena allocation with graceful degradation
			// Read the clamped entry count from the pre-scanned hint (avoids a
			// duplicate buffer read of the count field).  If no hint is available
			// (arena exhausted, or non-pre-scanned call), fall back to re-reading.
			var slotCount int
			if hint, hasHint := arena.nextHint(); hasHint {
				slotCount = hint
			} else if uint64(cur)+2 <= uint64(len(b)) {
				rawCount := int(order.Uint16(b[cur:]))
				pos := int(cur) + 2
				count := rawCount
				const maxPrealloc = 1024
				if pos+count*12 > len(b) {
					count = (len(b) - pos) / 12
				}
				if count < 0 {
					count = 0
				}
				slotCount = min(count, maxPrealloc)
			}

			if entrySlice := arena.allocEntries(slotCount); entrySlice != nil {
				// Both IFD slot and entry backing are available from the arena.
				next, ok, ifdWarnings = parseSingleIFDInto(b, ifdPtr, entrySlice, cur, order)
				if !ok {
					// Arena slot was consumed but not usable; undo the IFD alloc.
					// Entry alloc cannot be undone, but that region will just be unused.
					arena.ifdN--
					if root != nil {
						warnings = append(warnings, fmt.Sprintf(
							"exif: next-IFD pointer 0x%08X is unreadable (out of bounds or corrupt); IFD chain terminated (TIFF 6.0 §2)",
							cur))
					}
					warnings = append(warnings, ifdWarnings...)
					break
				}
				ifd = ifdPtr
			} else {
				// Entry backing exhausted: undo IFD alloc and fall through to
				// individual alloc so no partial state is left in the arena.
				arena.ifdN--
				var parsedIFD *IFD
				parsedIFD, next, ok, ifdWarnings = parseSingleIFD(b, cur, order)
				if !ok {
					if root != nil {
						warnings = append(warnings, fmt.Sprintf(
							"exif: next-IFD pointer 0x%08X is unreadable (out of bounds or corrupt); IFD chain terminated (TIFF 6.0 §2)",
							cur))
					}
					warnings = append(warnings, ifdWarnings...)
					break
				}
				ifd = parsedIFD
			}
		} else {
			// Arena nil or IFD batch exhausted: allocate individually (MakerNote
			// parsers, ParseIFDAt, and any chain IFD beyond the pre-scan count).
			var parsedIFD *IFD
			parsedIFD, next, ok, ifdWarnings = parseSingleIFD(b, cur, order)
			if !ok {
				if root != nil && cur != 0 {
					warnings = append(warnings, fmt.Sprintf(
						"exif: next-IFD pointer 0x%08X is unreadable (out of bounds or corrupt); IFD chain terminated (TIFF 6.0 §2)",
						cur))
				}
				warnings = append(warnings, ifdWarnings...)
				break
			}
			ifd = parsedIFD
		}

		warnings = append(warnings, ifdWarnings...)

		// Link into the chain.
		if root == nil {
			root = ifd
		} else {
			current.Next = ifd
		}
		current = ifd

		// Audit finding #131: if the next pointer is non-zero but falls outside the
		// buffer, record a warning and stop following the chain. parseSingleIFD will
		// handle it on the next iteration (returning !ok), but we can detect the
		// condition early here to attach a cleaner message.
		if next != 0 && uint64(next)+2 > uint64(len(b)) {
			warnings = append(warnings, fmt.Sprintf(
				"exif: next-IFD pointer 0x%08X points outside the buffer (len %d); IFD chain terminated (TIFF 6.0 §2)",
				next, len(b)))
			cur = 0 // stop the chain; IFDs parsed so far are valid
		} else {
			cur = next
		}
	}

	if root == nil {
		return nil, warnings, &metaerr.CorruptMetadataError{
			Format: "EXIF",
			Reason: fmt.Sprintf("IFD at offset %d could not be parsed (buf len %d)", offset, len(b)),
		}
	}
	return root, warnings, nil
}

// ---------------------------------------------------------------------------
// BigTIFF IFD traversal (BigTIFF spec §2, Aware Systems / libtiff)
// ---------------------------------------------------------------------------
//
// BigTIFF uses 64-bit offsets and a 20-byte IFD entry layout:
//
//	bytes 0-1   tag (uint16)
//	bytes 2-3   type (uint16)
//	bytes 4-11  count (uint64)
//	bytes 12-19 value-or-offset (uint64): inline when typeSizeBigTIFF(type)*count ≤ 8
//
// The IFD count field is 8 bytes (uint64) and the next-IFD pointer is 8 bytes.
// The inline threshold is 8 bytes (vs 4 in classic TIFF).
//
// The classic paths (parseIFDEntry, parseSingleIFD, traverse) are left
// completely unchanged to preserve their zero-overhead hot path.
//
// BigTIFF-only type codes (16/17/18) produce an IFDEntry whose Type field
// carries the BigTIFF type code; callers that decode values must account for
// the 8-byte element size. All sub-IFD pointer tags (ExifIFDPointer=0x8769,
// GPSIFDPointer=0x8825, InteropIFDPointer=0xA005) may use either TypeLong (4)
// or TypeIFD8/TypeLong8 (8) in BigTIFF files produced by libtiff/tiffcp;
// readBigTIFFOffset handles both.

// bigTIFFMaxEntries caps the entry count per IFD to guard against DoS via
// crafted huge uint64 counts. Classic TIFF uses uint16 (max 65535) for its
// entry count; applying the same cap here prevents OOM on malicious inputs.
const bigTIFFMaxEntries = 65535

// parseIFDEntryBigTIFF decodes a single 20-byte BigTIFF IFD entry starting at
// byte offset e within b (BigTIFF spec §2, Aware Systems / libtiff).
//
// BigTIFF entry layout (20 bytes):
//
//	bytes 0-1:   tag (uint16)
//	bytes 2-3:   type (uint16)
//	bytes 4-11:  count (uint64)
//	bytes 12-19: value-or-offset (uint64)
//	             inline when typeSizeBigTIFF(type)*count ≤ 8 (BigTIFF §2)
//	             otherwise: uint64 file offset to the value data
//
// For unknown/BigTIFF-only types whose element size fits in 8 bytes
// (e.g. TypeLong8=16), the value-or-offset field stores the value inline when
// count*8 ≤ 8 (i.e. count == 1). Out-of-line LONG8/SLONG8/IFD8 arrays have
// their offset stored in the 8-byte field.
//
// For completely unknown types (sz == 0), the raw 8-byte field is stored as
// the value verbatim — the caller cannot compute the true size, so no OOL
// fetch is attempted.
//
// Anti-DoS: count*sz overflow is checked in uint64 before multiplication.
// Returns (zero, false) on any out-of-bounds or overflow condition.
func parseIFDEntryBigTIFF(b []byte, e int, order binary.ByteOrder) (IFDEntry, bool) {
	// BigTIFF spec §2: each entry is exactly 20 bytes.
	if e+20 > len(b) {
		return IFDEntry{}, false
	}

	// task #199: record byte order as a 1-bit flag — see parseIFDEntry.
	// CIPA DC-008-2023 §4.6.2; BigTIFF spec §2.
	isBig := order == binary.BigEndian

	tag := TagID(order.Uint16(b[e:]))
	typ := DataType(order.Uint16(b[e+2:]))
	cnt := order.Uint64(b[e+4:])

	// Anti-DoS: reject pathological counts early — no real IFD entry has
	// more than a few thousand elements.  This also prevents the count*sz
	// overflow check below from needing to handle wrap-around when cnt
	// is near MaxUint64.
	const maxBigTIFFCount = uint64(1 << 30) // 1 GiB elements max; still DoS-safe
	if cnt > maxBigTIFFCount {
		return IFDEntry{}, false
	}

	sz := typeSizeBigTIFF(typ)
	if sz == 0 {
		// Unknown type: store the raw 8-byte value-or-offset field verbatim.
		// We cannot determine whether this is inline or OOL, so treat it as
		// an inline value of at most 8 bytes. This preserves the tag for
		// callers without attempting a potentially bogus OOL fetch.
		return IFDEntry{
			Tag:       tag,
			Type:      typ,
			Count:     uint32(cnt), // safe: cnt ≤ maxBigTIFFCount (2^30) < MaxUint32
			Value:     b[e+12 : e+20],
			bigEndian: isBig,
		}, true
	}

	// Anti-DoS: guard count*sz against uint64 overflow before multiplying.
	// sz ≤ 8; cnt ≤ maxBigTIFFCount ≤ 2^30, so cnt*8 ≤ 2^33 — no overflow.
	// The check is explicit for clarity and future-proofing.
	const maxUint64 = ^uint64(0)
	if cnt > maxUint64/sz {
		return IFDEntry{}, false
	}
	totalSize := sz * cnt

	// BigTIFF spec §2: inline threshold is 8 bytes (vs 4 in classic TIFF).
	if totalSize <= 8 {
		// Value fits inline in the 8-byte value-or-offset field.
		// Left-justified: the first totalSize bytes are the value.
		end := e + 12 + int(totalSize) // safe: totalSize ≤ 8 (inline threshold)
		return IFDEntry{
			Tag:       tag,
			Type:      typ,
			Count:     uint32(cnt), // safe: cnt ≤ maxBigTIFFCount (2^30) < MaxUint32
			Value:     b[e+12 : end],
			bigEndian: isBig,
		}, true
	}

	// Out-of-line: the 8-byte field is a uint64 file offset.
	valOff := order.Uint64(b[e+12:])
	// Anti-DoS: bounds check before slicing.
	if valOff > uint64(len(b)) || totalSize > uint64(len(b))-valOff {
		return IFDEntry{}, false
	}
	return IFDEntry{
		Tag:       tag,
		Type:      typ,
		Count:     uint32(cnt), // safe: cnt ≤ maxBigTIFFCount (2^30) < MaxUint32
		Value:     b[valOff : valOff+totalSize],
		bigEndian: isBig,
		rawOffset: valOff, // BigTIFF spec §2: full 64-bit offset preserved; used by MakerNoteOffset64 (#142)
	}, true
}

// parseSingleIFDBigTIFF parses all entries at a single BigTIFF IFD offset
// within b and returns the parsed IFD, the next-IFD offset (0 if absent or
// unreadable), whether parsing succeeded, and any diagnostic warnings.
// It does not follow the next-IFD chain — callers are responsible for cycle
// detection.
//
// BigTIFF spec §2: IFD layout — count(8) + entries(count×20) + nextIFD(8).
//
// Applies the same duplicate-tag dedup logic as parseSingleIFD (audit
// finding #126): first occurrence wins, a warning is appended for each drop.
func parseSingleIFDBigTIFF(b []byte, offset uint64, order binary.ByteOrder) (*IFD, uint64, bool, []string) {
	// Guard: IFD offset + 8-byte count field must fit in b.
	if offset > uint64(len(b)) || uint64(len(b))-offset < 8 {
		return nil, 0, false, nil
	}

	count := order.Uint64(b[offset:])
	// Cap count to bigTIFFMaxEntries to prevent DoS (same cap as extractTagValuesBigTIFF).
	count = min(count, bigTIFFMaxEntries)

	// Each BigTIFF entry is 20 bytes; validate the total entry area fits.
	const bigTIFFEntrySize = 20
	maxEntries := (uint64(len(b)) - offset - 8) / bigTIFFEntrySize
	count = min(count, maxEntries)

	pos := offset + 8 // first entry starts after the 8-byte count field

	const maxPrealloc = 1024
	preallocCap := min(int(count), maxPrealloc) //nolint:gosec // G115: count ≤ bigTIFFMaxEntries (65535) so fits int on all supported platforms
	ifd := &IFD{Entries: make([]IFDEntry, 0, preallocCap)}
	return fillIFDBigTIFF(b, ifd, offset, pos, count, order, nil)
}

// fillIFDBigTIFF is the body of parseSingleIFDBigTIFF.
// ifd.Entries must already be initialised (len==0, correct cap) before this
// function is called.
func fillIFDBigTIFF(b []byte, ifd *IFD, offset, pos, count uint64, order binary.ByteOrder, warnings []string) (*IFD, uint64, bool, []string) { //nolint:cyclop // BigTIFF sort+dedup mirrors fillIFD; inherent complexity
	const bigTIFFEntrySize = uint64(20)

	for i := uint64(0); i < count; i++ { //nolint:intrange,modernize // BigTIFF parser: loop variable is a byte-slice offset multiplier
		entry, ok := parseIFDEntryBigTIFF(b, int(pos+i*bigTIFFEntrySize), order) //nolint:gosec // G115: pos+i*20 bounded by count clamping above
		if !ok {
			continue
		}
		ifd.Entries = append(ifd.Entries, entry)
	}

	// Sort entries by tag so Get() can use binary search (TIFF §7).
	// Use stable sort so duplicate tags preserve their original file order
	// before the dedup pass (audit finding #126).
	slices.SortStableFunc(ifd.Entries, func(a, b IFDEntry) int {
		return cmp.Compare(a.Tag, b.Tag)
	})

	// Audit finding #126: dedupe duplicate tags keeping the first occurrence.
	// Arena safety (task #198): cap-clamped slice; compaction stays within own cap.
	if len(ifd.Entries) > 1 {
		out := ifd.Entries[:1]
		for i := 1; i < len(ifd.Entries); i++ {
			if ifd.Entries[i].Tag == ifd.Entries[i-1].Tag {
				warnings = append(warnings, fmt.Sprintf(
					"exif: BigTIFF IFD at offset %d contains duplicate tag 0x%04X; keeping first occurrence",
					offset, ifd.Entries[i].Tag))
			} else {
				out = append(out, ifd.Entries[i])
			}
		}
		ifd.Entries = out
	}

	// BigTIFF JPEG thumbnails (if any) — reuse the same extraction logic;
	// the thumbnail offset stored as TypeLong (4-byte) will be read correctly
	// by extractJPEGThumbnail using the entry's own bigEndian flag (task #199).
	ifd.ThumbnailData = extractJPEGThumbnail(b, ifd)

	// Read the next-IFD pointer (8 bytes after the last entry, BigTIFF spec §2).
	nextPtrPos := pos + count*bigTIFFEntrySize
	if nextPtrPos+8 > uint64(len(b)) {
		return ifd, 0, true, warnings
	}
	return ifd, order.Uint64(b[nextPtrPos:]), true, warnings
}

// traverseBigTIFF walks the BigTIFF IFD chain starting at offset within b,
// returning the root IFD and any diagnostic warnings. The next-IFD chain is
// followed iteratively to prevent stack overflows. Cycle detection uses a
// uint64 visited-offset set recycled from visitedPoolBigTIFF to avoid
// per-call allocations.
//
// BigTIFF files are rare in practice; this path uses per-IFD allocation
// (*IFD + []IFDEntry) rather than a Parse-level arena. The allocation
// overhead is acceptable given how seldom BigTIFF is encountered.
//
// Audit finding #131: non-zero next-IFD pointers that are out of bounds
// generate a ParseWarning but do not abort parsing of already-parsed IFDs.
func traverseBigTIFF(b []byte, offset uint64, order binary.ByteOrder) (*IFD, []string, error) { //nolint:gocyclo,cyclop // BigTIFF IFD chain traversal; same inherent branching as classic-TIFF traverse
	if offset > uint64(len(b)) || uint64(len(b))-offset < 8 {
		return nil, nil, &metaerr.CorruptMetadataError{
			Format: "EXIF",
			Reason: fmt.Sprintf("BigTIFF IFD offset %d out of bounds (buf len %d)", offset, len(b)),
		}
	}

	// Recycle the visited map from the pool to avoid per-call allocation.
	visited := visitedPoolBigTIFF.Get().(map[uint64]bool) //nolint:forcetypeassert,revive // visitedPoolBigTIFF.New always stores map[uint64]bool; pool invariant
	defer func() {
		for k := range visited {
			delete(visited, k)
		}
		visitedPoolBigTIFF.Put(visited)
	}()

	var root, current *IFD
	var warnings []string
	cur := offset

	for cur != 0 {
		if visited[cur] {
			break // cycle detected
		}
		visited[cur] = true

		ifd, next, ok, ifdWarnings := parseSingleIFDBigTIFF(b, cur, order)
		if !ok {
			if root != nil && cur != 0 {
				warnings = append(warnings, fmt.Sprintf(
					"exif: BigTIFF next-IFD pointer 0x%016X is unreadable; IFD chain terminated (BigTIFF spec §2)",
					cur))
			}
			warnings = append(warnings, ifdWarnings...)
			break
		}

		warnings = append(warnings, ifdWarnings...)

		if root == nil {
			root = ifd
		} else {
			current.Next = ifd
		}
		current = ifd

		// Audit finding #131: next pointer OOB → warn and stop.
		if next != 0 && (next > uint64(len(b)) || uint64(len(b))-next < 8) {
			warnings = append(warnings, fmt.Sprintf(
				"exif: BigTIFF next-IFD pointer 0x%016X points outside the buffer (len %d); IFD chain terminated (BigTIFF spec §2)",
				next, len(b)))
			cur = 0
		} else {
			cur = next
		}
	}

	if root == nil {
		return nil, warnings, &metaerr.CorruptMetadataError{
			Format: "EXIF",
			Reason: fmt.Sprintf("BigTIFF IFD at offset %d could not be parsed (buf len %d)", offset, len(b)),
		}
	}
	return root, warnings, nil
}

// readBigTIFFSubIFDOffset reads a sub-IFD pointer value from an IFDEntry,
// handling TypeShort (16-bit), TypeLong (32-bit), and the BigTIFF 64-bit types
// (TypeLong8=16, TypeIFD8=18).
//
// While EXIF §4.6.3 specifies TypeLong for ExifIFD/GPS/InteropIFD pointer tags,
// some BigTIFF writers produce TypeShort pointer entries when the target IFD fits
// in a 16-bit offset (e.g. small experimental files). Accepting TypeShort here
// ensures those files are parsed correctly rather than silently losing the sub-IFD.
// BigTIFF spec §2 (Aware Systems / libtiff) does not forbid SHORT pointer values.
//
// tiffcp / libtiff write sub-IFD pointers as TypeLong (4-byte) even in BigTIFF
// files for many tags; some newer writers use TypeIFD8 (18). Both are valid.
//
// Returns (offset, true) when the entry carries a readable pointer, or
// (0, false) when the entry is nil, has an unrecognised type, or has an
// insufficient value length.
func readBigTIFFSubIFDOffset(e *IFDEntry) (uint64, bool) {
	if e == nil {
		return 0, false
	}
	switch e.Type { //nolint:exhaustive // TypeShort/TypeLong/TypeLong8/TypeIFD8 are the only meaningful sub-IFD pointer types; all others fall through to (0,false)
	case TypeShort:
		// TypeShort (2 bytes): some BigTIFF writers store pointer values as SHORT
		// when the target IFD offset fits in 16 bits.  BigTIFF inline threshold
		// is 8 bytes so SHORT entries are always stored inline in the entry field.
		// #143: without this case, ExifIFD/GPS IFD pointers stored as TypeShort
		// were silently ignored, leaving ExifIFD and GPSIFD nil.
		// BigTIFF spec §2; EXIF §4.6.3.
		if len(e.Value) < 2 {
			return 0, false
		}
		return uint64(e.order().Uint16(e.Value)), true
	case TypeLong:
		// TypeLong (4 bytes): used by tiffcp / libtiff even in BigTIFF containers.
		if len(e.Value) < 4 {
			return 0, false
		}
		return uint64(e.order().Uint32(e.Value)), true
	case TypeLong8, TypeIFD8:
		// TypeLong8/TypeIFD8 (8 bytes): BigTIFF-native pointer types.
		if len(e.Value) < 8 {
			return 0, false
		}
		return e.order().Uint64(e.Value), true
	}
	return 0, false
}

// Get returns the entry matching tag, or nil if not found.
// Entries must be sorted by tag (maintained by traverse and set); Get uses
// binary search — O(log n) with zero allocations (sort.Search).
func (ifd *IFD) Get(tag TagID) *IFDEntry {
	if ifd == nil {
		return nil
	}
	entries := ifd.Entries
	i := sort.Search(len(entries), func(i int) bool { return entries[i].Tag >= tag })
	if i < len(entries) && entries[i].Tag == tag {
		return &entries[i]
	}
	return nil
}

// String decodes the entry value as a NUL-terminated string.
// Handles TypeASCII (TIFF §2, US-ASCII) and TypeUTF8 (CIPA DC-008-2023 §4.6.3,
// UTF-8 encoded Unicode). Both types use element size 1 and NUL termination.
func (e *IFDEntry) String() string {
	if (e.Type != TypeASCII && e.Type != TypeUTF8) || len(e.Value) == 0 {
		return ""
	}
	// bytes.TrimRight avoids a byte-by-byte loop; returns a sub-slice (no allocation).
	return string(bytes.TrimRight(e.Value, "\x00"))
}

// Uint16 decodes the first SHORT (unsigned 16-bit) value.
// Returns 0 when the entry type is not TypeShort or the value is shorter than
// 2 bytes.
//
// CIPA DC-008-2023 §4.6.3: SHORT (type 3) and SSHORT (type 8) are distinct
// types with different sign semantics. Uint16 accepts only TypeShort (unsigned).
// For TypeSShort entries use Int16(), which performs the correct signed
// bit-reinterpretation. Calling Uint16 on a TypeSShort entry would silently
// return the two's-complement bit-pattern of a negative int16 as a large uint16
// (e.g. −100 → 65436). Audit finding #129.
func (e *IFDEntry) Uint16() uint16 {
	if e.Type != TypeShort || len(e.Value) < 2 {
		return 0
	}
	return e.order().Uint16(e.Value)
}

// Uint32 decodes the first LONG (unsigned 32-bit) value.
// Returns 0 when the entry type is not TypeLong or the value is shorter than
// 4 bytes.
//
// CIPA DC-008-2023 §4.6.3: LONG (type 4) and SLONG (type 9) are distinct
// types with different sign semantics. Uint32 accepts only TypeLong (unsigned).
// For TypeSLong entries use Int32(), which performs the correct signed
// bit-reinterpretation. Calling Uint32 on a TypeSLong entry would silently
// return the two's-complement bit-pattern of a negative int32 as a large uint32
// (e.g. −1 → 4294967295). Audit finding #129.
func (e *IFDEntry) Uint32() uint32 {
	if e.Type != TypeLong || len(e.Value) < 4 {
		return 0
	}
	return e.order().Uint32(e.Value)
}

// Rational decodes the i-th RATIONAL value as [numerator, denominator].
// Returns [2]uint32{} when the entry type is not TypeRational or the index is
// out of range (including negative indices).
//
// EXIF 2.32 CIPA DC-008-2023 §4.6.3: RATIONAL and SRATIONAL are distinct types
// with different sign semantics. Rational() accepts only TypeRational (unsigned
// numerator and denominator). For TypeSRational entries — such as
// ExposureBiasValue (0x9204), ShutterSpeedValue (0x9201), and BrightnessValue
// (0x9203) — use SRational() instead, which performs the correct int32
// bit-reinterpretation. Calling Rational() on a TypeSRational entry would
// silently return the two's-complement bit-pattern of a negative int32 as a
// large uint32 (e.g. −2 → 4294967294).
func (e *IFDEntry) Rational(i int) [2]uint32 {
	// EXIF 2.32 CIPA DC-008-2023 §4.6.3: only TypeRational is accepted here.
	// TypeSRational is intentionally excluded; callers must use SRational().
	if e.Type != TypeRational {
		return [2]uint32{}
	}
	// Task #87: reject negative indices before computing the byte offset.
	// Without this guard, off = i*8 = -8 for i=-1, making off+8 = 0, which
	// passes the "off+8 > len(e.Value)" check (0 > len is false for any
	// non-empty slice) and causes e.Value[-8:] to panic with a negative index.
	if i < 0 {
		return [2]uint32{}
	}
	off := i * 8
	if off+8 > len(e.Value) {
		return [2]uint32{}
	}
	ord := e.order()
	return [2]uint32{
		ord.Uint32(e.Value[off:]),
		ord.Uint32(e.Value[off+4:]),
	}
}

// SRational decodes the i-th SRATIONAL value as [numerator, denominator].
// Returns [0, 0] on out-of-range access (including negative indices).
// Use this instead of Rational for signed tags such as ShutterSpeedValue (0x9201),
// BrightnessValue (0x9203), and ExposureBiasValue (0x9204) (EXIF 2.x §4.6.3).
func (e *IFDEntry) SRational(i int) [2]int32 {
	if e.Type != TypeSRational {
		return [2]int32{}
	}
	// Task #87: reject negative indices before computing the byte offset.
	// Without this guard, off = i*8 = -8 for i=-1, making off+8 = 0, which
	// passes the "off+8 > len(e.Value)" check (0 > len is false for any
	// non-empty slice) and causes e.Value[-8:] to panic with a negative index.
	if i < 0 {
		return [2]int32{}
	}
	off := i * 8
	if off+8 > len(e.Value) {
		return [2]int32{}
	}
	ord := e.order()
	return [2]int32{
		int32(ord.Uint32(e.Value[off:])),   //nolint:gosec // G115: intentional bit-reinterpretation of uint32 as signed int32 per EXIF TypeSRational
		int32(ord.Uint32(e.Value[off+4:])), //nolint:gosec // G115: intentional bit-reinterpretation of uint32 as signed int32 per EXIF TypeSRational
	}
}

// Int16 decodes the first SSHORT value.
func (e *IFDEntry) Int16() int16 {
	if e.Type != TypeSShort || len(e.Value) < 2 {
		return 0
	}
	return int16(e.order().Uint16(e.Value)) //nolint:gosec // G115: intentional bit-reinterpretation per EXIF TypeSShort
}

// Int32 decodes the first SLONG value.
func (e *IFDEntry) Int32() int32 {
	if e.Type != TypeSLong || len(e.Value) < 4 {
		return 0
	}
	return int32(e.order().Uint32(e.Value)) //nolint:gosec // G115: intentional bit-reinterpretation per EXIF TypeSLong
}

// Float32 decodes the first FLOAT value (IEEE 754 single-precision).
func (e *IFDEntry) Float32() float32 {
	if e.Type != TypeFloat || len(e.Value) < 4 {
		return 0
	}
	bits := e.order().Uint32(e.Value)
	return math.Float32frombits(bits)
}

// Float64 decodes the first DOUBLE value (IEEE 754 double-precision).
func (e *IFDEntry) Float64() float64 {
	if e.Type != TypeDouble || len(e.Value) < 8 {
		return 0
	}
	bits := e.order().Uint64(e.Value)
	return math.Float64frombits(bits)
}

// Bytes returns the raw value bytes, suitable for TypeUndefined and TypeByte.
func (e *IFDEntry) Bytes() []byte {
	return e.Value
}

// Byte returns the first byte of a TypeByte or TypeSByte entry.
// Returns 0 if the entry has no bytes.
func (e *IFDEntry) Byte() byte {
	if len(e.Value) == 0 {
		return 0
	}
	return e.Value[0]
}

// Uint8s returns all bytes of a TypeByte entry as a slice.
// The returned slice aliases the entry's internal Value buffer; do not modify.
func (e *IFDEntry) Uint8s() []byte {
	return e.Value
}

// Len returns the number of values in the entry (Count field).
func (e *IFDEntry) Len() int {
	return int(e.Count)
}

// set inserts or replaces an entry in the IFD. The byteOrder field of the
// new entry is inherited from the existing entries in the IFD (or defaults
// to binary.LittleEndian for an empty IFD). Entries are kept sorted by tag
// so that Get() can use binary search.
//
// Insertion uses sort.Search to find the insertion point and slices.Insert to
// place the new entry in O(n) time instead of re-sorting the whole slice (O(n
// log n)), making bulk IFD construction O(n²) in the worst case instead of
// the previous O(n² log n).
func (ifd *IFD) set(tag TagID, typ DataType, count uint32, value []byte) {
	// Inherit byte order from the first existing entry (false = LE is the zero-value
	// default matching the library convention; see IFDEntry.bigEndian comment).
	// task #199: bigEndian bool replaces binary.ByteOrder interface.
	var isBig bool
	if len(ifd.Entries) > 0 {
		isBig = ifd.Entries[0].bigEndian
	}
	entry := IFDEntry{Tag: tag, Type: typ, Count: count, Value: value, bigEndian: isBig}
	// Binary search for the insertion point (entries are always sorted by tag).
	i := sort.Search(len(ifd.Entries), func(i int) bool { return ifd.Entries[i].Tag >= tag })
	if i < len(ifd.Entries) && ifd.Entries[i].Tag == tag {
		// Replace existing entry in-place: sort order is preserved.
		ifd.Entries[i] = entry
		return
	}
	// New tag: insert at position i to maintain the sorted invariant without a
	// full re-sort. slices.Insert is O(n) (one memmove) vs. sort O(n log n).
	ifd.Entries = slices.Insert(ifd.Entries, i, entry)
}

// asciiValue encodes s as a NUL-terminated ASCII byte slice suitable for
// IFDEntry.Value (TypeASCII, TIFF §2).
func asciiValue(s string) []byte {
	v := make([]byte, len(s)+1)
	copy(v, s)
	// v[len(s)] is already 0 (NUL terminator).
	return v
}

// utf8Value encodes s as a NUL-terminated UTF-8 byte slice suitable for
// IFDEntry.Value (TypeUTF8 = 13, CIPA DC-008-2023 §4.6.3). Analogous to
// asciiValue but for the EXIF 3.0 UTF-8 string type.
func utf8Value(s string) []byte {
	v := make([]byte, len(s)+1)
	copy(v, s)
	// v[len(s)] is already 0 (NUL terminator).
	return v
}

// --- helpers used by encode ---

// filterEntriesInto copies ifd.Entries into the pooled scratch buffer *dst
// (resliced to 0), omitting any tag in the exclude list, and returns a slice
// that aliases *dst's backing array.  It is the encode path's replacement for
// the former allocating filterEntries function.
//
// Design note: this function is called exclusively by buildIFD0Entries and
// buildExifIFDEntries in write.go, which obtain *dst from entrySlicePool.
// After use the caller must sync *dst = result and then call putEntrySlice
// so that the zero-before-Put step covers all live elements.
//
// The function is equivalent to the removed filterEntries except that it
// reuses the provided backing array instead of allocating a fresh one:
//
//   - All callers pass at most 3 exclude tags, so a linear scan (no map
//     allocation, no hashing overhead) is cheaper than a hash-based approach.
//   - Fast path: when no excluded tag is present, a bulk copy is used so the
//     entire entries array can be transferred with a single memcopy.
//   - Returning the original slice is NOT safe (callers append pointer entries
//     and sort the result, which would mutate the source IFD) — that is why a
//     copy is always made, either via the pooled buffer or a fresh allocation
//     when the pooled cap is exceeded.
//   - extraCap spare slots are ensured beyond the filtered length so callers
//     can append pointer placeholder entries without triggering a reallocation.
//
// Performance audit 2026-06-10, finding F41: this replaces the two per-Encode
// make([]IFDEntry) calls that together accounted for 9.35% of alloc_space on
// the TIFF relocate profile.
func filterEntriesInto(ifd *IFD, dst *[]IFDEntry, extraCap int, exclude ...TagID) []IFDEntry {
	if ifd == nil {
		return nil
	}
	// Reuse the pooled backing array.  Reset to length 0 while preserving capacity.
	result := (*dst)[:0]

	// Fast path: no excluded tag present — bulk-copy all entries.
	anyPresent := false
	for _, tag := range exclude {
		if hasEntry(ifd.Entries, tag) {
			anyPresent = true
			break
		}
	}
	if !anyPresent {
		// Ensure capacity for len+extraCap entries, then bulk-copy.
		need := len(ifd.Entries) + extraCap
		if cap(result) < need {
			result = make([]IFDEntry, len(ifd.Entries), need)
		} else {
			result = result[:len(ifd.Entries)]
		}
		copy(result, ifd.Entries)
		return result
	}

	// Filtered path: copy only the non-excluded entries.
	for _, entry := range ifd.Entries {
		if !slices.Contains(exclude, entry.Tag) {
			result = append(result, entry)
		}
	}
	// Ensure the capacity guarantee (extraCap spare slots) is met.
	if cap(result) < len(result)+extraCap {
		grown := make([]IFDEntry, len(result), len(result)+extraCap)
		copy(grown, result)
		result = grown
	}
	return result
}

// hasEntry reports whether entries contains an entry with the given tag.
// Entries must be sorted by tag (invariant maintained by set and traverse).
// Uses binary search — O(log n), zero allocations.
func hasEntry(entries []IFDEntry, tag TagID) bool {
	i := sort.Search(len(entries), func(i int) bool { return entries[i].Tag >= tag })
	return i < len(entries) && entries[i].Tag == tag
}

// sortEntries sorts entries by tag ID in ascending order (TIFF §7 requirement).
// slices.SortFunc is used instead of sort.Slice because it avoids the
// reflect-based Swapper allocation that sort.Slice incurs on every call.
func sortEntries(entries []IFDEntry) {
	slices.SortFunc(entries, func(a, b IFDEntry) int {
		return cmp.Compare(a.Tag, b.Tag)
	})
}

// ifdTotalSize returns the total bytes occupied by the serialised IFD block:
// 2 (entry count) + len(entries)*12 (entry list) + 4 (next-IFD pointer) + value area.
//
// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
// Word boundary = 2-byte alignment.
//
// ifdTotalSize mirrors the padding strategy used by writeIFD:
//
//  1. Within the value area, a 1-byte pad is inserted before each out-of-line
//     value that would otherwise start at an odd running offset.
//
//  2. The returned total is rounded up to the nearest even number (by adding a
//     trailing pad byte if the total would otherwise be odd). This guarantees
//     that every IFD block occupies an even number of bytes, so that the next
//     IFD block placed immediately after also starts at an even file offset —
//     provided the first IFD (IFD0 at offset 8) also starts at an even offset.
//     Together, these two invariants ensure that every out-of-line value in
//     every IFD begins at an even (word-aligned) file offset.
//
// The assumption that startOff is even (which is always true for the layout
// generated by serialise: header=8 is even, and every subsequent IFD block
// starts at an even offset because the preceding block has an even total size)
// allows ifdTotalSize to compute the correct padding without knowing startOff.
//
// Accumulation is performed in uint64 to avoid uint32 wrap-around when an
// IFDEntry has a manually-constructed Count value close to math.MaxUint32
// (e.g. Count=0xFFFFFFFF, TypeRational gives a value area of ~34 GiB).
// If the true size would exceed math.MaxUint32, the function returns
// math.MaxUint32 (saturated). This prevents the caller (writeTIFFHeader,
// computeIFDOffsets) from using a falsely small size to pre-allocate buffers
// or compute sub-IFD offsets, which would produce silently corrupt output.
//
// Note: parsed IFDs are bounded by their input buffer (JPEG APP1 ≤ 65533 bytes;
// TIFF limited by file size) so overflow can only occur when the caller manually
// constructs an IFD with an extreme Count value.
func ifdTotalSize(entries []IFDEntry) uint32 {
	// Use uint64 throughout to avoid wrap-around on extreme Count values.
	// fixed = 2 (count) + n*12 (entries) + 4 (next-IFD pointer).
	// fixed is always even: 6 + 12n = even + even*n = even.
	sz := uint64(2 + len(entries)*12 + 4)

	// Track the running value-area size parity.  Because startOff is always
	// even (see doc comment) and fixed is always even, the first OOL value
	// always starts at an even absolute offset.  Parity starts at 0 (even).
	valueParity := uint64(0)

	for _, e := range entries {
		ts := typeSize(e.Type)
		if ts == 0 {
			continue
		}
		total := uint64(ts) * uint64(e.Count)
		if total > 4 {
			// TIFF 6.0 §2: word-align before each out-of-line value.
			// Insert 1 padding byte if the running value-area offset is odd.
			if valueParity == 1 {
				sz++ // alignment pad byte
			}
			sz += total
			// Update parity: only the low bit of total matters.
			// (If we inserted a pad byte above, the +1 is absorbed here since
			// valueParity transitions: 1 → pad → 0 → total&1 = total&1.)
			valueParity = total & 1
			// Saturate at MaxUint32 rather than wrapping. Any value above
			// MaxUint32 cannot be represented in a 32-bit TIFF stream; callers
			// that see MaxUint32 must treat the IFD as un-encodable.
			if sz > math.MaxUint32 {
				return math.MaxUint32
			}
		}
	}

	// Round up to the nearest even byte count so that the next IFD block
	// placed immediately after also starts at an even file offset.
	if sz&1 == 1 {
		sz++
	}
	if sz > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(sz) // sz <= math.MaxUint32 is enforced by the saturation checks above
}

// writeIFD appends the serialised IFD block to out and returns the extended slice.
// startOff is the absolute file offset at which the IFD block begins (used to
// compute value-area offsets). nextIFDOffset is written as the next-IFD pointer
// (TIFF §2); pass 0 to indicate no further IFDs.
//
// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
// (Word = 2 bytes.) writeIFD enforces this by inserting a single 0x00 padding
// byte in the value area before each out-of-line value that would otherwise
// start at an odd offset. ifdTotalSize mirrors this padding arithmetic so that
// computeIFDOffsets keeps the following IFD blocks correctly positioned.
func writeIFD(out []byte, entries []IFDEntry, order binary.ByteOrder, startOff, nextIFDOffset uint32) []byte {
	n := len(entries)
	// value area begins right after: 2 (count) + n*12 (entries) + 4 (next-IFD).
	valueOff := startOff + uint32(2+n*12+4) //nolint:gosec // G115: IFD size bounded by validated entry count

	var countB [2]byte
	order.PutUint16(countB[:], uint16(n)) //nolint:gosec // G115: IFD entry count bounded by parser-validated input
	out = append(out, countB[:]...)

	scratchPtr := iobuf.Get(n * 12)
	entryBuf := (*scratchPtr)[:n*12]
	// Zero the scratch buffer before writing: pooled slices may contain stale
	// bytes from a prior encode call.  Without this, inline values shorter than
	// 4 bytes (e.g. TypeShort, TypeByte) leave the unused padding bytes
	// non-deterministic, leaking data across encode calls and producing
	// non-reproducible output.  clear() compiles to a single memclr — no hot-path
	// cost on the fast path.  TIFF §2: the value-or-offset field is always 4
	// bytes; unused bytes must be zero-filled.
	clear(entryBuf)
	defer iobuf.Put(scratchPtr)
	var valueArea []byte
	curOff := valueOff

	for i, e := range entries {
		p := i * 12
		order.PutUint16(entryBuf[p:], uint16(e.Tag))
		order.PutUint16(entryBuf[p+2:], uint16(e.Type))
		order.PutUint32(entryBuf[p+4:], e.Count)

		ts := typeSize(e.Type)
		total := uint64(ts) * uint64(e.Count)

		if ts == 0 || total <= 4 {
			// Inline value: copy into the 4-byte field (TIFF §2).
			copy(entryBuf[p+8:p+12], e.Value)
		} else {
			// TIFF 6.0 §2: "Each data item (field value) must begin on a word
			// boundary."  Insert a single 0x00 alignment pad byte if the running
			// offset is odd before writing this out-of-line value.
			if curOff&1 == 1 {
				valueArea = append(valueArea, 0x00)
				curOff++
			}
			order.PutUint32(entryBuf[p+8:], curOff)
			valueArea = append(valueArea, e.Value...)
			// TIFF §2: the value area for this entry must be exactly
			// Count * typeSize bytes.  When len(Value) < total (e.g. TypeLong
			// IPTC padded to the next 4-byte boundary), zero-fill the gap so
			// that subsequent entries receive correct offsets and TIFF readers
			// see a conformant value area.
			if uint64(len(e.Value)) < total {
				pad := total - uint64(len(e.Value))
				valueArea = append(valueArea, make([]byte, pad)...)
			}
			curOff += uint32(total) //nolint:gosec // G115: total bounded by Count*typeSize ≤ MaxUint32 enforced by ifdTotalSize
		}
	}

	out = append(out, entryBuf...)
	// Write next-IFD pointer (TIFF §2).
	var nextB [4]byte
	order.PutUint32(nextB[:], nextIFDOffset)
	out = append(out, nextB[:]...)
	out = append(out, valueArea...)

	// Trailing word-alignment pad: ensure the IFD block occupies an even number
	// of bytes so that the next IFD block placed immediately after starts at an
	// even file offset (TIFF 6.0 §2). ifdTotalSize mirrors this padding.
	if len(out)&1 == 1 {
		out = append(out, 0x00)
	}
	return out
}
