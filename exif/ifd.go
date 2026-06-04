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
type IFDEntry struct {
	Tag   TagID
	Type  DataType
	Count uint32
	// Value holds the decoded value. For types whose total size fits in 4 bytes
	// the raw bytes are stored inline; otherwise this is a []byte slice into
	// the original buffer (zero-copy).
	Value     []byte
	byteOrder binary.ByteOrder
	// rawOffset is the TIFF-stream offset stored in the 4-byte value-or-offset
	// field when the value is out-of-line (totalSize > 4).  Zero for inline
	// values.  Used by parseExifSubIFDs to record MakerNoteOffset without
	// resorting to unsafe pointer arithmetic.
	rawOffset uint32
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
func parseIFDEntry(b []byte, e int, order binary.ByteOrder) (IFDEntry, bool) {
	// Each entry is exactly 12 bytes; verify the slice is long enough.
	if e+12 > len(b) {
		return IFDEntry{}, false
	}

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
			return IFDEntry{}, false
		}
		value = b[valOff:end]
		return IFDEntry{
			Tag:       tag,
			Type:      typ,
			Count:     cnt,
			Value:     value,
			byteOrder: order,
			rawOffset: valOff, // TIFF §2: offset to value data; used by MakerNoteOffset
		}, true
	default:
		// Value is inline, left-justified in the 4-byte field (TIFF §2).
		value = b[e+8 : e+8+int(totalSize)]
	}

	return IFDEntry{
		Tag:       tag,
		Type:      typ,
		Count:     cnt,
		Value:     value,
		byteOrder: order,
	}, true
}

// parseSingleIFD parses all entries at a single IFD offset within b and
// returns the parsed IFD, the next-IFD offset (0 if absent or unreadable),
// and whether parsing succeeded. It does not follow the next-IFD chain.
//
// Callers are responsible for cycle detection before calling this function.
func parseSingleIFD(b []byte, offset uint32, order binary.ByteOrder) (*IFD, uint32, bool) {
	// Use uint64 arithmetic to avoid int truncation on 32-bit platforms
	// (GOARCH=386/arm): int(uint32 >= 2^31) is negative, which would cause the
	// guard to pass while the subsequent slice panics. Performing the comparison
	// in uint64 is safe on all platforms (task #74).
	if uint64(offset)+2 > uint64(len(b)) {
		return nil, 0, false
	}

	count := order.Uint16(b[offset:])
	pos := int(offset) + 2

	if pos+int(count)*12 > len(b) {
		// Truncated entry list — treat as unreadable.
		return nil, 0, false
	}

	// Cap initial capacity to avoid over-allocating on crafted large counts.
	// The loop below will only append entries that fit within the validated buffer range.
	const maxIFDEntryPrealloc = 1024
	preallocCap := min(int(count), maxIFDEntryPrealloc)
	ifd := &IFD{Entries: make([]IFDEntry, 0, preallocCap)}
	for i := 0; i < int(count); i++ { //nolint:intrange // binary parser: loop variable is a byte-slice offset multiplier
		entry, ok := parseIFDEntry(b, pos+i*12, order)
		if !ok {
			continue
		}
		ifd.Entries = append(ifd.Entries, entry)
	}

	// Sort entries by tag so Get() can use binary search (TIFF §7).
	// Real cameras produce sorted IFDs, but non-compliant files may not.
	sortEntries(ifd.Entries)

	// Extract JPEG thumbnail bytes when both JPEGInterchangeFormat (0x0201) and
	// JPEGInterchangeFormatLength (0x0202) are present (EXIF §4.5.5).
	ifd.ThumbnailData = extractJPEGThumbnail(b, ifd, order)

	// Read the next-IFD pointer (4 bytes after the last entry, TIFF §2).
	nextPtrPos := pos + int(count)*12
	if nextPtrPos+4 > len(b) {
		return ifd, 0, true
	}
	return ifd, order.Uint32(b[nextPtrPos:]), true
}

// extractJPEGThumbnail extracts the raw JPEG thumbnail bytes from b when the
// parsed ifd contains both TagJPEGInterchangeFormat (0x0201) and
// TagJPEGInterchangeFormatLength (0x0202) with valid values (EXIF §4.5.5).
// Returns nil when either tag is absent, malformed, or the indicated byte range
// falls outside b.  The returned slice is an independent copy so the IFD is not
// tied to the original parse buffer.
func extractJPEGThumbnail(b []byte, ifd *IFD, order binary.ByteOrder) []byte {
	jifEntry := ifd.Get(TagJPEGInterchangeFormat)
	if jifEntry == nil || len(jifEntry.Value) < 4 {
		return nil
	}
	jifLenEntry := ifd.Get(TagJPEGInterchangeFormatLength)
	if jifLenEntry == nil || len(jifLenEntry.Value) < 4 {
		return nil
	}
	jifOff := order.Uint32(jifEntry.Value)
	jifLen := order.Uint32(jifLenEntry.Value)
	end := uint64(jifOff) + uint64(jifLen)
	if jifLen == 0 || end > uint64(len(b)) {
		return nil
	}
	// Copy: the IFD must be independent of the original parse buffer so that
	// callers can discard the input bytes after Parse returns (TIFF §2).
	thumb := make([]byte, jifLen)
	copy(thumb, b[jifOff:end])
	return thumb
}

// traverse walks the IFD chain starting at offset within b, using the given
// byte order. It returns the root IFD.
//
// The next-IFD pointer chain is followed iteratively (not recursively) to
// prevent stack overflows on cyclic or deeply nested inputs (fuzz safety).
func traverse(b []byte, offset uint32, order binary.ByteOrder) (*IFD, error) {
	// Use uint64 arithmetic to avoid int truncation on 32-bit platforms
	// (GOARCH=386/arm): int(uint32 >= 2^31) is negative, which would cause the
	// guard to pass while the subsequent slice panics. Performing the comparison
	// in uint64 is safe on all platforms (task #74).
	if uint64(offset)+2 > uint64(len(b)) {
		return nil, &metaerr.CorruptMetadataError{
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
	cur := offset

	for cur != 0 {
		if visited[cur] {
			break // cycle detected — stop following the chain
		}
		visited[cur] = true

		ifd, next, ok := parseSingleIFD(b, cur, order)
		if !ok {
			break
		}

		// Link into the chain.
		if root == nil {
			root = ifd
		} else {
			current.Next = ifd
		}
		current = ifd
		cur = next
	}

	if root == nil {
		return nil, &metaerr.CorruptMetadataError{
			Format: "EXIF",
			Reason: fmt.Sprintf("IFD at offset %d could not be parsed (buf len %d)", offset, len(b)),
		}
	}
	return root, nil
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

// Uint16 decodes the first SHORT value.
func (e *IFDEntry) Uint16() uint16 {
	if (e.Type != TypeShort && e.Type != TypeSShort) || len(e.Value) < 2 {
		return 0
	}
	return e.byteOrder.Uint16(e.Value)
}

// Uint32 decodes the first LONG value.
func (e *IFDEntry) Uint32() uint32 {
	if (e.Type != TypeLong && e.Type != TypeSLong) || len(e.Value) < 4 {
		return 0
	}
	return e.byteOrder.Uint32(e.Value)
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
	return [2]uint32{
		e.byteOrder.Uint32(e.Value[off:]),
		e.byteOrder.Uint32(e.Value[off+4:]),
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
	return [2]int32{
		int32(e.byteOrder.Uint32(e.Value[off:])),   //nolint:gosec // G115: intentional bit-reinterpretation of uint32 as signed int32 per EXIF TypeSRational
		int32(e.byteOrder.Uint32(e.Value[off+4:])), //nolint:gosec // G115: intentional bit-reinterpretation of uint32 as signed int32 per EXIF TypeSRational
	}
}

// Int16 decodes the first SSHORT value.
func (e *IFDEntry) Int16() int16 {
	if e.Type != TypeSShort || len(e.Value) < 2 {
		return 0
	}
	return int16(e.byteOrder.Uint16(e.Value)) //nolint:gosec // G115: intentional bit-reinterpretation per EXIF TypeSShort
}

// Int32 decodes the first SLONG value.
func (e *IFDEntry) Int32() int32 {
	if e.Type != TypeSLong || len(e.Value) < 4 {
		return 0
	}
	return int32(e.byteOrder.Uint32(e.Value)) //nolint:gosec // G115: intentional bit-reinterpretation per EXIF TypeSLong
}

// Float32 decodes the first FLOAT value (IEEE 754 single-precision).
func (e *IFDEntry) Float32() float32 {
	if e.Type != TypeFloat || len(e.Value) < 4 {
		return 0
	}
	bits := e.byteOrder.Uint32(e.Value)
	return math.Float32frombits(bits)
}

// Float64 decodes the first DOUBLE value (IEEE 754 double-precision).
func (e *IFDEntry) Float64() float64 {
	if e.Type != TypeDouble || len(e.Value) < 8 {
		return 0
	}
	bits := e.byteOrder.Uint64(e.Value)
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
	order := binary.ByteOrder(binary.LittleEndian)
	if len(ifd.Entries) > 0 {
		order = ifd.Entries[0].byteOrder
	}
	entry := IFDEntry{Tag: tag, Type: typ, Count: count, Value: value, byteOrder: order}
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

// filterEntries returns a copy of ifd.Entries with the given tags removed,
// with capacity extended by extraCap to allow callers to append without
// triggering a reallocation.
//
// All callers pass at most 3 tags, so a linear scan over the exclude slice
// is cheaper than a map allocation (no heap escape, no hashing overhead).
//
// Fast path: when none of the excluded tags are present (checked via binary
// search) the function still returns a copy because callers append to the
// result — returning the original slice would corrupt the source IFD.
func filterEntries(ifd *IFD, extraCap int, exclude ...TagID) []IFDEntry {
	if ifd == nil {
		return nil
	}
	// Fast path: check whether any excluded tag is actually present before
	// allocating the filtered result. Binary search is O(log n) per tag.
	anyPresent := false
	for _, tag := range exclude {
		if hasEntry(ifd.Entries, tag) {
			anyPresent = true
			break
		}
	}
	if !anyPresent {
		// No excluded tags present — return a copy with extraCap spare slots so
		// callers can append without triggering a reallocation.
		out := make([]IFDEntry, len(ifd.Entries), len(ifd.Entries)+extraCap)
		copy(out, ifd.Entries)
		return out
	}
	result := make([]IFDEntry, 0, len(ifd.Entries)+extraCap)
	for _, entry := range ifd.Entries {
		if !slices.Contains(exclude, entry.Tag) {
			result = append(result, entry)
		}
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
	sz := uint64(2 + len(entries)*12 + 4)
	for _, e := range entries {
		ts := typeSize(e.Type)
		if ts == 0 {
			continue
		}
		total := uint64(ts) * uint64(e.Count)
		if total > 4 {
			sz += total
			// Saturate at MaxUint32 rather than wrapping. Any value above
			// MaxUint32 cannot be represented in a 32-bit TIFF stream; callers
			// that see MaxUint32 must treat the IFD as un-encodable.
			if sz > math.MaxUint32 {
				return math.MaxUint32
			}
		}
	}
	return uint32(sz) //nolint:gosec // G115: sz <= MaxUint32 is enforced by the saturation check above
}

// writeIFD appends the serialised IFD block to out and returns the extended slice.
// startOff is the absolute file offset at which the IFD block begins (used to
// compute value-area offsets). nextIFDOffset is written as the next-IFD pointer
// (TIFF §2); pass 0 to indicate no further IFDs.
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
			order.PutUint32(entryBuf[p+8:], curOff)
			valueArea = append(valueArea, e.Value...)
			curOff += uint32(len(e.Value)) //nolint:gosec // G115: value length bounded by input
		}
	}

	out = append(out, entryBuf...)
	// Write next-IFD pointer (TIFF §2).
	var nextB [4]byte
	order.PutUint32(nextB[:], nextIFDOffset)
	out = append(out, nextB[:]...)
	out = append(out, valueArea...)
	return out
}
