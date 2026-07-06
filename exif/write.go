package exif

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"sync"
)

// entrySlicePool recycles the scratch []IFDEntry slices used by
// buildIFD0Entries and buildExifIFDEntries during serialise.
//
// Performance audit 2026-06-10, finding F41: filterEntries allocated
// 652 MB (6.68% of alloc_space on the read/write profile) and was the
// #2 flat allocator on the TIFF relocate profile (2450 MB = 9.35%).
// It runs twice per Encode (once for IFD0, once for ExifIFD), and twice
// more per relocate before task #220 eliminates the double-encode.
//
// The pool stores *[]IFDEntry (pointer to a slice header) so that the
// backing array survives GC and is reused across Encode calls.  The
// element type (IFDEntry, 48 B after task #199) makes even moderate-sized
// IFDs cheap to recycle: a 60-entry camera IFD0 is 60×48 = 2880 B, well
// under the iobuf largeSize threshold.
//
// Safety contract (buffer-lifetime risk class, cf. audit finding #56/#72):
//   - Get/Put are always paired within a single serialise call.
//   - Elements are zeroed before Put (clear) so IFDEntry.Value aliases —
//     which point into the caller's live IFD data — do not prevent GC of
//     caller-owned byte slices between Encode calls.
//   - The scratch slice never escapes serialise: every consumer
//     (ifdTotalSize, computeIFDOffsets, patchPointers, writeTIFFHeader,
//     writeIFD, writeSubIFDs) reads from it within the same call frame and
//     retains no reference after returning.
//
//nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure; identical pattern to visitedPool in ifd.go
var entrySlicePool = sync.Pool{
	New: func() any {
		// 64 entries covers typical camera IFD0 (≤20) + ExifIFD (≤40) with room
		// to spare.  Capacity is grown by append if a real IFD exceeds this; the
		// enlarged backing array is discarded rather than pooled (see putEntrySlice).
		s := make([]IFDEntry, 0, 64)
		return &s
	},
}

// getEntrySlice returns a pooled *[]IFDEntry reset to length 0.
// The caller must call putEntrySlice when finished.
func getEntrySlice() *[]IFDEntry {
	p := entrySlicePool.Get().(*[]IFDEntry) //nolint:forcetypeassert,revive // entrySlicePool.New always stores *[]IFDEntry; pool invariant
	*p = (*p)[:0]
	return p
}

// putEntrySlice zeros the elements of *p (releasing Value aliases) and
// returns *p to the pool.  Slices whose backing array grew beyond the
// canonical capacity (128 entries = 6144 B after task #199) are discarded
// to prevent a runaway encode from permanently inflating the pool.
//
// The canonical cap threshold (128) is chosen to be ≥2× the 64-entry New
// default and large enough to absorb real-world IFDs (no known camera
// produces an IFD0 with more than 60 entries, and ExifIFD with more than 60)
// without leaving oversized arrays in the pool.
func putEntrySlice(p *[]IFDEntry) {
	if p == nil {
		return
	}
	const maxPooledCap = 128
	if cap(*p) > maxPooledCap {
		// Discard: the array grew too large; do not return it to the pool.
		return
	}
	// Zero the live elements so IFDEntry.Value byte-slice aliases are released.
	// This prevents caller-owned Value data from being pinned in the pool across
	// Encode calls.  clear() compiles to a single memclr — negligible cost.
	clear(*p)
	*p = (*p)[:0]
	entrySlicePool.Put(p)
}

// maxBigTIFFEncodeSize is the explicit, documented sanity ceiling applied to
// the total encoded size of a BigTIFF EXIF payload (checked in
// writeTIFFHeaderBigTIFF before any allocation proportional to it is made).
//
// Classic-TIFF Encode never needs an equivalent guard: its offset fields are
// 32 bits wide, so ifdTotalSize saturates at math.MaxUint32 (~4 GiB) and no
// larger buffer can ever be requested — the wire format itself is the
// ceiling. BigTIFF's 64-bit offset fields have no such natural ceiling, so an
// EXIF struct with a manually constructed, pathological IFDEntry.Count value
// (e.g. Count near math.MaxUint32 on a single TypeDouble/TypeLong8 entry,
// total ≈ 34 GiB) could otherwise direct Encode to attempt an unbounded
// make([]byte, 0, N) allocation — a memory-exhaustion DoS (CWE-400). This
// ceiling is generous relative to any known real-world camera or scanner
// BigTIFF EXIF payload (kilobytes to low megabytes; BigTIFF is used for the
// overall TIFF/DNG image raster, not the EXIF metadata block itself) while
// still bounding the worst case to a single-digit-GiB allocation attempt
// rather than an attacker-chosen multi-terabyte one.
//
// Declared as a var (not a const), mirroring the maxFileSize (root package,
// format/webp) and maxDatasetValueLen (iptc package) pattern used elsewhere in
// this project, so tests can lower it and exercise ErrBigTIFFEncodeSizeExceeded
// without allocating a multi-gigabyte buffer. Production code must never
// mutate it.
//
// BigTIFF spec §2 (Aware Systems / libtiff); task #264.
var maxBigTIFFEncodeSize uint64 = 4 << 30 //nolint:gochecknoglobals // test-overridable cap; never mutated in production paths

// serialise encodes e to a raw EXIF byte stream beginning with the TIFF
// header. The caller is responsible for prepending the "Exif\x00\x00"
// identifier required by JPEG APP1 (EXIF §4.5.4).
//
// Both classic TIFF (e.BigTIFF == false) and BigTIFF (e.BigTIFF == true,
// BigTIFF spec §2) sources are supported; the dispatch happens after
// buildIFD0Entries/buildExifIFDEntries because those two builders are
// container-agnostic (sub-IFD pointer tags stay TypeLong regardless of
// container — EXIF §4.6.3) and are shared between both paths (task #264).
//
// Round-trip fidelity for IFD entries:
//   - Known-type entries (any TIFF type code with a defined byte size) whose
//     total value size is ≤ 4 bytes (inline) are perfectly preserved.
//   - Known-type entries whose total value size > 4 bytes (out-of-line) are
//     re-serialised into a fresh value area; their data is preserved exactly.
//   - Unknown-type entries (type codes not defined in TIFF 6.0 §2) are stored
//     during parsing as their raw 4-byte IFD field (see ifd.go traverse()).
//     On re-encode that 4-byte field is written back verbatim as an inline
//     value. If the original field was an offset into a private data blob, that
//     blob is NOT copied — the offset in the new file would be stale. This is
//     an inherent constraint: without knowing the type size we cannot locate or
//     copy the pointed-to data. Callers that embed private data using unknown
//     type codes must re-inject that data after calling Encode.
func serialise(e *EXIF) ([]byte, error) {
	if e == nil {
		return nil, ErrNilEXIF
	}

	order := e.ByteOrder
	// Task #59: e.ByteOrder is a nil interface value when the caller constructs
	// an EXIF struct without setting ByteOrder (zero value for interface). Every
	// internal setter uses ifd0ByteOrder() which defaults to LittleEndian, so
	// a freshly-built EXIF is always LE unless the caller explicitly chose BE.
	// Default here to match that convention so Encode never panics on a nil
	// interface dereference (first triggered at order.PutUint32 in patchPointers).
	if order == nil {
		order = binary.LittleEndian
	}

	// Stack-allocated arrays avoid one heap allocation per sub-IFD pointer.
	var exifPtrBuf, gpsPtrBuf, interopPtrBuf [4]byte

	// Acquire pooled scratch slices for the IFD0 and ExifIFD entry lists.
	// Both are returned to the pool (with elements zeroed) via deferred puts,
	// which fire on ALL return paths — including every BigTIFF error return
	// below.  This single-ownership discipline ensures each pooled pointer is
	// Put exactly once.
	//
	// Performance audit 2026-06-10, finding F41: filterEntries was the #2
	// flat allocator on the TIFF relocate profile.  Pooling eliminates both
	// per-Encode make([]IFDEntry) calls.
	ifd0Ptr := getEntrySlice()
	defer putEntrySlice(ifd0Ptr)
	exifPtr := getEntrySlice()
	defer putEntrySlice(exifPtr)

	// buildIFD0Entries/buildExifIFDEntries are container-agnostic: sub-IFD
	// pointer tags stay TypeLong regardless of container (EXIF §4.6.3), so the
	// classic and BigTIFF paths share this entry-building step unchanged
	// (task #264).
	ifd0Entries := buildIFD0Entries(e, order, &exifPtrBuf, &gpsPtrBuf, ifd0Ptr)
	exifIFDEntries := buildExifIFDEntries(e, order, &interopPtrBuf, exifPtr)

	// BigTIFF spec §2 (Aware Systems / libtiff); task #264: Encode natively
	// supports BigTIFF sources rather than downgrading them to classic TIFF
	// (which would truncate every 64-bit offset to 32 bits — audit finding
	// #107). serialiseBigTIFF widens the header, IFD layout, and offset
	// arithmetic to 64 bits throughout; see its doc comment for the full
	// BigTIFF write path.
	if e.BigTIFF {
		return serialiseBigTIFF(e, order, ifd0Entries, exifIFDEntries)
	}

	exifStart, gpsStart, interopStart, ifd1Start := computeIFDOffsets(e, ifd0Entries, exifIFDEntries)

	patchPointers(ifd0Entries, exifIFDEntries, order, exifStart, gpsStart, interopStart)

	out := writeTIFFHeader(e, order, ifd0Entries, exifIFDEntries)

	// IFD0: next-IFD pointer points to IFD1 if present.
	ifd0NextPtr := uint32(0)
	if e.IFD0 != nil && e.IFD0.Next != nil {
		ifd0NextPtr = ifd1Start
	}
	out = writeIFD(out, ifd0Entries, order, uint32(len(out)), ifd0NextPtr) //nolint:gosec // G115: output offset bounded by buffer size

	out = writeSubIFDs(out, e, exifIFDEntries, order)

	return out, nil
}

// serialiseBigTIFF is the BigTIFF counterpart of the classic-TIFF tail of
// serialise (from computeIFDOffsets onward). ifd0Entries and exifIFDEntries
// have already been built by the shared, container-agnostic
// buildIFD0Entries/buildExifIFDEntries helpers.
//
// Sub-IFD pointer tags (ExifIFDPointer 0x8769, GPSIFDPointer 0x8825,
// InteropIFDPointer 0xA005) and thumbnail pointer tags (JPEGInterchangeFormat
// 0x0201, JPEGInterchangeFormatLength 0x0202) are fixed EXIF LONG (4-byte)
// fields regardless of container (EXIF §4.6.3, §4.5.5; task #264 §4). Before
// patching any of them, serialiseBigTIFF range-checks the target offset
// against math.MaxUint32 and returns ErrBigTIFFPointerOverflow rather than
// truncating — see ErrBigTIFFPointerOverflow's doc comment. The thumbnail
// pointer check happens inside writeSubIFDsBigTIFF, which is where the
// thumbnail offset becomes known.
func serialiseBigTIFF(e *EXIF, order binary.ByteOrder, ifd0Entries, exifIFDEntries []IFDEntry) ([]byte, error) { //nolint:gocyclo,cyclop // R-16 overflow guards for exifStart/gpsStart/interopStart are inherent, mirroring classic serialise's IFD dispatch chain
	exifStart, gpsStart, interopStart, ifd1Start := computeIFDOffsetsBigTIFF(e, ifd0Entries, exifIFDEntries)

	// R-16: only range-check the offsets that correspond to a pointer tag
	// actually being written. computeIFDOffsetsBigTIFF always returns a
	// layout position for exifStart/gpsStart/interopStart even when the
	// corresponding sub-IFD is absent (e.ExifIFD/GPSIFD/InteropIFD == nil); in
	// that case buildIFD0Entries/buildExifIFDEntries never added a pointer
	// entry for it, so there is nothing to truncate and no error is warranted
	// — an oversized IFD0 alone is instead caught by the aggregate size
	// ceiling in writeTIFFHeaderBigTIFF (R-15).
	if e.ExifIFD != nil && exifStart > math.MaxUint32 {
		return nil, fmt.Errorf("%w: ExifIFDPointer target offset %d", ErrBigTIFFPointerOverflow, exifStart)
	}
	if e.GPSIFD != nil && gpsStart > math.MaxUint32 {
		return nil, fmt.Errorf("%w: GPSIFDPointer target offset %d", ErrBigTIFFPointerOverflow, gpsStart)
	}
	if e.InteropIFD != nil && interopStart > math.MaxUint32 {
		return nil, fmt.Errorf("%w: InteropIFDPointer target offset %d", ErrBigTIFFPointerOverflow, interopStart)
	}

	// patchPointers (unchanged, task #264 §NO CHANGE list) still writes a
	// 4-byte uint32 into the placeholder Value slices reserved by
	// buildIFD0Entries/buildExifIFDEntries; the casts below are truncation-free
	// because the three guards above already proved each offset fits in 32 bits.
	patchPointers(ifd0Entries, exifIFDEntries, order, uint32(exifStart), uint32(gpsStart), uint32(interopStart)) //nolint:gosec // G115: truncation-free, guarded above

	out, err := writeTIFFHeaderBigTIFF(e, order, ifd0Entries, exifIFDEntries)
	if err != nil {
		return nil, err
	}

	// IFD0: next-IFD pointer points to IFD1 if present.
	ifd0NextPtr := uint64(0)
	if e.IFD0 != nil && e.IFD0.Next != nil {
		ifd0NextPtr = ifd1Start
	}
	out = writeIFDBigTIFF(out, ifd0Entries, order, uint64(len(out)), ifd0NextPtr)

	out, err = writeSubIFDsBigTIFF(out, e, exifIFDEntries, order)
	if err != nil {
		return nil, err
	}

	return out, nil
}

// appendUint16Order appends the 2-byte encoding of v (per order) to out and
// returns the extended slice.
//
// PERF-201-LOW hardening (task #247): task #201 introduced a direct,
// non-comma-ok type assertion (order.(binary.AppendByteOrder)) at the two call
// sites below in order to reach binary.AppendByteOrder's allocation-free
// Append* methods. That assertion panics for any binary.ByteOrder
// implementation that does not also implement AppendByteOrder — unreachable
// from any file this package parses (order is always the binary.LittleEndian
// or binary.BigEndian singleton inside serialise), but reachable if a caller
// hand-builds &EXIF{ByteOrder: <custom implementation>} and calls Encode.
//
// The comma-ok form below preserves the task #201 fast path exactly: both
// binary.LittleEndian and binary.BigEndian implement AppendByteOrder, so for
// every order this package's own parse/encode paths ever produce, the branch
// taken and the bytes appended are byte-identical to the former
// implementation — no allocation, no behavioural change. Any other
// binary.ByteOrder implementation falls back to a small on-stack scratch
// array + PutUint16 + append, which works for any implementation and never
// panics.
func appendUint16Order(out []byte, order binary.ByteOrder, v uint16) []byte {
	if ao, ok := order.(binary.AppendByteOrder); ok {
		return ao.AppendUint16(out, v)
	}
	var b [2]byte
	order.PutUint16(b[:], v)
	return append(out, b[:]...)
}

// appendUint32Order is the 4-byte counterpart of appendUint16Order; see its
// doc comment for the fast-path/fallback rationale (task #247).
func appendUint32Order(out []byte, order binary.ByteOrder, v uint32) []byte {
	if ao, ok := order.(binary.AppendByteOrder); ok {
		return ao.AppendUint32(out, v)
	}
	var b [4]byte
	order.PutUint32(b[:], v)
	return append(out, b[:]...)
}

// appendUint64Order is the 8-byte counterpart of appendUint16Order/
// appendUint32Order; see appendUint16Order's doc comment for the
// fast-path/fallback rationale (task #247). Added for BigTIFF write support
// (task #264): BigTIFF headers, IFD entry counts, per-entry counts,
// value-or-offset fields, and next-IFD pointers are all 8 bytes wide
// (BigTIFF spec §2, Aware Systems / libtiff).
func appendUint64Order(out []byte, order binary.ByteOrder, v uint64) []byte {
	if ao, ok := order.(binary.AppendByteOrder); ok {
		return ao.AppendUint64(out, v)
	}
	var b [8]byte
	order.PutUint64(b[:], v)
	return append(out, b[:]...)
}

// writeTIFFHeader builds the initial output buffer containing the TIFF header
// (TIFF §2): byte order mark, magic 0x002A, and the IFD0 offset. It also
// pre-allocates capacity for the IFD0, ExifIFD, GPS IFD, InteropIFD, and IFD1
// chain blocks (including any JPEG thumbnail data).
//
// Performance (task #201): the previous implementation allocated a [8]byte
// stack array and passed hdr[:] to append, which caused the compiler to move
// hdr to the heap (escape analysis: "hdr escapes to heap in writeTIFFHeader").
// The replacement builds the 8-byte header directly into the output slice via
// binary.AppendByteOrder (Go 1.21+), eliminating the heap escape entirely.
// The type assertion to binary.AppendByteOrder is performed once per call;
// both binary.LittleEndian and binary.BigEndian implement the interface, and
// order is always one of these two values (guaranteed by serialise which
// defaults a nil order to binary.LittleEndian before calling writeTIFFHeader).
func writeTIFFHeader(e *EXIF, order binary.ByteOrder, ifd0Entries, exifIFDEntries []IFDEntry) []byte {
	const headerSize = uint32(8)

	ifd0Size := ifdTotalSize(ifd0Entries)
	exifSize := uint32(0)
	if e.ExifIFD != nil {
		exifSize = ifdTotalSize(exifIFDEntries)
	}
	gpsSize := uint32(0)
	if e.GPSIFD != nil {
		gpsSize = ifdTotalSize(e.GPSIFD.Entries)
	}
	interopSize := uint32(0)
	if e.InteropIFD != nil {
		interopSize = ifdTotalSize(e.InteropIFD.Entries)
	}

	// Include the IFD1 chain (thumbnail IFDs + embedded JPEG data) in the
	// capacity estimate to avoid reallocation when appending thumbnail bytes.
	ifd1ChainSize := uint32(0)
	if e.IFD0 != nil {
		for ifd := e.IFD0.Next; ifd != nil; ifd = ifd.Next {
			ifd1ChainSize += ifdTotalSize(ifd.Entries)
			ifd1ChainSize += uint32(len(ifd.ThumbnailData)) //nolint:gosec // G115: thumbnail size bounded by parse-time validation
		}
	}

	out := make([]byte, 0, headerSize+ifd0Size+exifSize+gpsSize+interopSize+ifd1ChainSize)

	// TIFF §2: byte order mark (II = little-endian, MM = big-endian).
	if order == binary.LittleEndian {
		out = append(out, 'I', 'I')
	} else {
		out = append(out, 'M', 'M')
	}
	// TIFF §2: magic number 0x002A, then IFD0 offset immediately after the header.
	// task #201/#247: appendUint16Order/appendUint32Order reach
	// binary.AppendByteOrder's allocation-free Append* methods via a
	// comma-ok assertion (see their doc comment above) — zero-alloc and
	// byte-identical to the former direct-assertion implementation for
	// binary.LittleEndian/binary.BigEndian, and panic-free for any other
	// binary.ByteOrder implementation.
	out = appendUint16Order(out, order, 0x002A)
	out = appendUint32Order(out, order, headerSize) // IFD0 starts right after the header
	return out
}

// writeTIFFHeaderBigTIFF is the BigTIFF counterpart of writeTIFFHeader.
//
// BigTIFF spec §2 (Aware Systems / libtiff) 16-byte header layout:
//
//	bytes [0:2]  byte-order mark ("II"/"MM", same as classic)
//	bytes [2:4]  magic 0x002B (43)
//	bytes [4:6]  bytesize-of-offsets, MUST = 8
//	bytes [6:8]  reserved/constant, MUST = 0
//	bytes [8:16] first-IFD offset (uint64) — always 16 here (IFD0 immediately
//	             follows the header, matching the classic layout's offset 8)
//
// Before allocating the output buffer, the aggregate encoded size (header +
// IFD0 + ExifIFD + GPSIFD + InteropIFD + the IFD1 chain, all computed via
// ifdTotalSizeBigTIFF — the #1 correctness rule, see its doc comment) is
// checked against maxBigTIFFEncodeSize and rejected with
// ErrBigTIFFEncodeSizeExceeded (R-15) rather than handed to make([]byte, 0, N)
// unchecked; see maxBigTIFFEncodeSize's doc comment for the memory-exhaustion
// rationale this guards against.
func writeTIFFHeaderBigTIFF(e *EXIF, order binary.ByteOrder, ifd0Entries, exifIFDEntries []IFDEntry) ([]byte, error) {
	const headerSize = uint64(16)

	ifd0Size := ifdTotalSizeBigTIFF(ifd0Entries)
	exifSize := uint64(0)
	if e.ExifIFD != nil {
		exifSize = ifdTotalSizeBigTIFF(exifIFDEntries)
	}
	gpsSize := uint64(0)
	if e.GPSIFD != nil {
		gpsSize = ifdTotalSizeBigTIFF(e.GPSIFD.Entries)
	}
	interopSize := uint64(0)
	if e.InteropIFD != nil {
		interopSize = ifdTotalSizeBigTIFF(e.InteropIFD.Entries)
	}

	// Include the IFD1 chain (thumbnail IFDs + embedded JPEG data) in the
	// capacity estimate, mirroring writeTIFFHeader's classic-path logic.
	ifd1ChainSize := uint64(0)
	if e.IFD0 != nil {
		for ifd := e.IFD0.Next; ifd != nil; ifd = ifd.Next {
			ifd1ChainSize += ifdTotalSizeBigTIFF(ifd.Entries)
			ifd1ChainSize += uint64(len(ifd.ThumbnailData))
		}
	}

	total := headerSize + ifd0Size + exifSize + gpsSize + interopSize + ifd1ChainSize
	// R-15: explicit, documented sanity ceiling — see maxBigTIFFEncodeSize's
	// doc comment. Checked BEFORE the make() below so a pathological Count
	// value on a caller-constructed IFDEntry cannot trigger an unbounded
	// allocation attempt.
	if total > maxBigTIFFEncodeSize {
		return nil, fmt.Errorf("%w: %d bytes (ceiling %d)", ErrBigTIFFEncodeSizeExceeded, total, maxBigTIFFEncodeSize)
	}

	out := make([]byte, 0, total)

	// BigTIFF spec §2: byte order mark (same encoding as classic TIFF).
	if order == binary.LittleEndian {
		out = append(out, 'I', 'I')
	} else {
		out = append(out, 'M', 'M')
	}
	// BigTIFF spec §2: magic 43 (0x002B).
	out = appendUint16Order(out, order, 0x002B)
	// BigTIFF spec §2: bytesize-of-offsets MUST = 8.
	out = appendUint16Order(out, order, 8)
	// BigTIFF spec §2: reserved/constant MUST = 0.
	out = appendUint16Order(out, order, 0)
	// BigTIFF spec §2: IFD0 offset immediately after the 16-byte header.
	out = appendUint64Order(out, order, headerSize)
	return out, nil
}

// writeSubIFDs appends the ExifIFD, GPS IFD, InteropIFD, and IFD1 chain blocks
// to out in the order mandated by the TIFF layout (TIFF §2 / EXIF §4.5.4).
// Returns the extended slice.
//
// For IFDs in the IFD1 chain that carry a JPEG thumbnail (ThumbnailData != nil),
// the thumbnail bytes are appended immediately after the IFD block and the
// JPEGInterchangeFormat (0x0201) entry value is patched to the new offset.
// This fixes stale offset corruption that occurs when the TIFF is re-serialised
// to a different position in the byte stream (EXIF §4.5.5, TIFF §2).
func writeSubIFDs(out []byte, e *EXIF, exifIFDEntries []IFDEntry, order binary.ByteOrder) []byte {
	if e.ExifIFD != nil {
		out = writeIFD(out, exifIFDEntries, order, uint32(len(out)), 0) //nolint:gosec // G115: output offset bounded by buffer size
	}
	if e.GPSIFD != nil {
		out = writeIFD(out, e.GPSIFD.Entries, order, uint32(len(out)), 0) //nolint:gosec // G115: output offset bounded by buffer size
	}
	if e.InteropIFD != nil {
		out = writeIFD(out, e.InteropIFD.Entries, order, uint32(len(out)), 0) //nolint:gosec // G115: output offset bounded by buffer size
	}

	// Serialise the IFD1 chain (thumbnail IFDs, TIFF §2).
	// Task #58: guard matches the identical nil-check in writeTIFFHeader (line 88).
	// e.IFD0 is nil when the caller sets only ExifIFD/GPSIFD without IFD0; the
	// IFD1 chain is unreachable in that case so skipping it is correct.
	if e.IFD0 == nil {
		return out
	}
	for ifd := e.IFD0.Next; ifd != nil; ifd = ifd.Next {
		// For IFDs with JPEG thumbnail data: the thumbnail bytes are placed
		// immediately after the IFD block.  Compute the thumbnail offset now,
		// patch the entries, then append the data.
		//
		// EXIF §4.5.5: JPEGInterchangeFormat (0x0201, TypeLong, Count=1) holds
		// the absolute byte offset within the TIFF stream to the JPEG thumbnail.
		// JPEGInterchangeFormatLength (0x0202, TypeLong, Count=1) holds its byte
		// length.  Both are inline (4-byte) values in the IFD entry (TIFF §2:
		// value stored inline when total size ≤ 4 bytes).
		entries := ifd.Entries
		if ifd.ThumbnailData != nil {
			entries = patchThumbnailEntries(ifd.Entries, order,
				uint32(len(out))+ifdTotalSize(ifd.Entries), //nolint:gosec // G115: output offset bounded by buffer size
				uint32(len(ifd.ThumbnailData)),             //nolint:gosec // G115: thumbnail length bounded by parse-time validation
			)
		}

		nextPtr := uint32(0)
		if ifd.Next != nil {
			// nextPtr must skip past both the IFD block and any thumbnail data
			// that follows it before the next IFD begins.
			nextPtr = uint32(len(out)) + ifdTotalSize(entries) + uint32(len(ifd.ThumbnailData)) //nolint:gosec // G115: output offset bounded by buffer size
		}
		out = writeIFD(out, entries, order, uint32(len(out)), nextPtr) //nolint:gosec // G115: output offset bounded by buffer size

		// Append thumbnail bytes after the IFD block.
		if ifd.ThumbnailData != nil {
			out = append(out, ifd.ThumbnailData...)
		}
	}
	return out
}

// writeSubIFDsBigTIFF is the BigTIFF counterpart of writeSubIFDs; see its doc
// comment for the overall layout rationale. All offsets are uint64
// (BigTIFF spec §2).
//
// JPEGInterchangeFormat (0x0201) and JPEGInterchangeFormatLength (0x0202)
// remain fixed EXIF LONG (4-byte) fields even in BigTIFF (task #264 §4); the
// existing patchThumbnailEntries helper (unchanged) is reused directly once
// the computed uint64 offset/length have been range-checked against
// math.MaxUint32 — see ErrBigTIFFPointerOverflow's doc comment. Returns a
// non-nil error instead of truncating when the check fails.
func writeSubIFDsBigTIFF(out []byte, e *EXIF, exifIFDEntries []IFDEntry, order binary.ByteOrder) ([]byte, error) { //nolint:gocyclo,cyclop // mirrors classic writeSubIFDs' dispatch chain plus the R-16 thumbnail-pointer overflow guard; branches are inherent
	if e.ExifIFD != nil {
		out = writeIFDBigTIFF(out, exifIFDEntries, order, uint64(len(out)), 0)
	}
	if e.GPSIFD != nil {
		out = writeIFDBigTIFF(out, e.GPSIFD.Entries, order, uint64(len(out)), 0)
	}
	if e.InteropIFD != nil {
		out = writeIFDBigTIFF(out, e.InteropIFD.Entries, order, uint64(len(out)), 0)
	}

	// Serialise the IFD1 chain (thumbnail IFDs, BigTIFF spec §2), mirroring
	// writeSubIFDs' identical nil-guard: e.IFD0 is nil when the caller sets
	// only ExifIFD/GPSIFD without IFD0, so the IFD1 chain is unreachable.
	if e.IFD0 == nil {
		return out, nil
	}
	for ifd := e.IFD0.Next; ifd != nil; ifd = ifd.Next {
		entries := ifd.Entries
		if ifd.ThumbnailData != nil {
			// EXIF §4.5.5: JPEGInterchangeFormat holds the absolute stream
			// offset to the JPEG thumbnail; JPEGInterchangeFormatLength holds
			// its byte length. Both stay TypeLong (32-bit) fields (task #264
			// §4) — range-check before truncating (R-16).
			thumbOff := uint64(len(out)) + ifdTotalSizeBigTIFF(ifd.Entries)
			thumbLen := uint64(len(ifd.ThumbnailData))
			if thumbOff > math.MaxUint32 || thumbLen > math.MaxUint32 {
				return nil, fmt.Errorf("%w: JPEGInterchangeFormat offset %d / length %d", ErrBigTIFFPointerOverflow, thumbOff, thumbLen)
			}
			entries = patchThumbnailEntries(ifd.Entries, order, uint32(thumbOff), uint32(thumbLen))
		}

		nextPtr := uint64(0)
		if ifd.Next != nil {
			// nextPtr must skip past both the IFD block and any thumbnail data
			// that follows it before the next IFD begins.
			nextPtr = uint64(len(out)) + ifdTotalSizeBigTIFF(entries) + uint64(len(ifd.ThumbnailData))
		}
		out = writeIFDBigTIFF(out, entries, order, uint64(len(out)), nextPtr)

		// Append thumbnail bytes after the IFD block.
		if ifd.ThumbnailData != nil {
			out = append(out, ifd.ThumbnailData...)
		}
	}
	return out, nil
}

// patchThumbnailEntries returns a shallow copy of entries with
// TagJPEGInterchangeFormat (0x0201) and TagJPEGInterchangeFormatLength (0x0202)
// patched to the provided newOffset and newLength values.
//
// Both tags are TypeLong / Count=1 (total size = 4 bytes, stored inline in the
// IFD entry field per TIFF §2).  The [4]byte backing arrays are stack-allocated
// in the caller's frame; the returned slice aliases them so no heap allocation
// is needed.
//
// EXIF §4.5.5: JPEGInterchangeFormat is the absolute TIFF-stream offset to the
// JPEG thumbnail; JPEGInterchangeFormatLength is its byte length.
func patchThumbnailEntries(entries []IFDEntry, order binary.ByteOrder, newOffset, newLength uint32) []IFDEntry {
	// Shallow-copy the slice so we don't mutate the IFD's original entries.
	patched := make([]IFDEntry, len(entries))
	copy(patched, entries)

	for i := range patched {
		switch patched[i].Tag {
		case TagJPEGInterchangeFormat:
			// Replace the stale offset with the new one.  Allocate a fresh
			// [4]byte value so the original IFDEntry.Value slice is not modified.
			v := make([]byte, 4)
			order.PutUint32(v, newOffset)
			patched[i].Value = v
		case TagJPEGInterchangeFormatLength:
			// Update length in case ThumbnailData was trimmed or replaced.
			v := make([]byte, 4)
			order.PutUint32(v, newLength)
			patched[i].Value = v
		}
	}
	return patched
}

// buildIFD0Entries assembles the IFD0 entry slice for encoding.
// It strips existing sub-IFD pointer tags, conditionally appends placeholder
// entries for ExifIFD and GPS IFD (using the caller-supplied stack buffers),
// and returns a sorted slice. The placeholder values are patched later by
// patchPointers once the target offsets are known.
//
// scratch is a pooled *[]IFDEntry obtained by the caller via getEntrySlice.
// buildIFD0Entries populates *scratch (resliced to 0 before use) and returns
// a slice that aliases *scratch's backing array.  The caller must not Put
// scratch until all consumers of the returned slice have finished.
func buildIFD0Entries(e *EXIF, order binary.ByteOrder, exifPtrBuf, gpsPtrBuf *[4]byte, scratch *[]IFDEntry) []IFDEntry {
	entries := filterEntriesInto(e.IFD0, scratch, 2,
		TagExifIFDPointer, TagGPSIFDPointer, TagInteropIFDPointer)

	// Reserve pointer entries so ifdTotalSize accounts for them correctly.
	// TypeLong / Count 1 → value fits inline (4 bytes); no value-area impact.
	// task #199: bigEndian flag replaces binary.ByteOrder interface.
	isBig := order == binary.BigEndian
	if e.ExifIFD != nil {
		entries = append(entries, IFDEntry{Tag: TagExifIFDPointer, Type: TypeLong, Count: 1, Value: exifPtrBuf[:], bigEndian: isBig})
	}
	if e.GPSIFD != nil {
		entries = append(entries, IFDEntry{Tag: TagGPSIFDPointer, Type: TypeLong, Count: 1, Value: gpsPtrBuf[:], bigEndian: isBig})
	}
	sortEntries(entries)
	// Sync the pooled slice header so putEntrySlice can zero the live elements.
	*scratch = entries
	return entries
}

// buildExifIFDEntries assembles the ExifIFD entry slice for encoding.
// Returns nil when e.ExifIFD is nil. It strips the existing InteropIFD pointer,
// re-adds a placeholder (using interopPtrBuf) when InteropIFD is present,
// preserves raw MakerNote bytes if they are not already present in the entries,
// and returns a sorted slice. Placeholder values are patched later by patchPointers.
//
// MakerNote bytes are preserved verbatim by exif.Encode. Offset correctness
// after relocation depends on the maker's offset convention:
//
//   - Blob-relative (safe, no rebasing needed): Canon (plain IFD at offset 0),
//     Panasonic ("Panasonic\0\0\0" prefix, IFD at offset 12, all val_or_off
//     relative to blob start), Nikon Type-3 (embedded TIFF header inside the
//     blob; all internal offsets relative to the embedded TIFF base which moves
//     with the blob), Olympus "OLYMPUS\0" format (IFD at offset 12, blob-relative
//     val_or_off), Pentax AOC / PENTAX.
//
//   - TIFF-absolute (offsets relative to the outer TIFF base — become stale when
//     the blob moves): Sony plain-IFD MakerNotes (DSLR-A series, ILCE, SLT —
//     plain IFD at offset 0, all OOL val_or_off are outer-TIFF-absolute), and
//     Olympus "OLYMP\0" MakerNotes (older compacts such as C5050Z — "OLYMP\0"
//     prefix, IFD at offset 8, all OOL val_or_off are outer-TIFF-absolute).
//     Nikon Type-1 (legacy D1, plain IFD at offset 0, big-endian, OOL offsets
//     are outer-TIFF-absolute) is a documented limitation: rebasing is not
//     implemented because Type-1 cameras are extremely rare and empirically
//     produce no OOL MakerNote entries in practice (ExifTool Nikon.pm confirms
//     Type-1 has no known OOL sub-structures; see relocate_makernote.go).
//
// Rebasing for Sony and Olympus OLYMP-type is performed by the TIFF relocators
// in format/tiff/relocate_makernote.go after exif.Encode places the blob.
// EXIF.MakerNoteOffset records the original TIFF offset so relocators can
// compute the movement delta.
//
// Reference: EXIF §4.6.5 tag 0x927C; ExifTool Sony.pm, Olympus.pm, Panasonic.pm,
// Nikon.pm; empirical analysis per #127.
func buildExifIFDEntries(e *EXIF, order binary.ByteOrder, interopPtrBuf *[4]byte, scratch *[]IFDEntry) []IFDEntry {
	if e.ExifIFD == nil {
		return nil
	}

	// Strip existing InteropIFD pointer; we will re-add it with a freshly
	// computed offset when InteropIFD is present (EXIF §4.6.3, tag 0xA005
	// lives in ExifIFD, not IFD0).
	// task #199: bigEndian flag replaces binary.ByteOrder interface.
	isBig := order == binary.BigEndian
	entries := filterEntriesInto(e.ExifIFD, scratch, 2, TagInteropIFDPointer)
	if e.InteropIFD != nil {
		entries = append(entries, IFDEntry{Tag: TagInteropIFDPointer, Type: TypeLong, Count: 1, Value: interopPtrBuf[:], bigEndian: isBig})
	}
	if e.MakerNote != nil && !hasEntry(entries, TagMakerNote) {
		entries = append(entries, IFDEntry{
			Tag:       TagMakerNote,
			Type:      TypeUndefined,
			Count:     uint32(len(e.MakerNote)), //nolint:gosec // G115: MakerNote length bounded by input
			Value:     e.MakerNote,
			bigEndian: isBig,
		})
	}
	sortEntries(entries)
	// Sync the pooled slice header so putEntrySlice can zero the live elements.
	*scratch = entries
	return entries
}

// computeIFDOffsets derives the byte offset at which each sub-IFD block begins
// within the final encoded output.
//
// Layout (TIFF §2 / EXIF §4.5.4):
//
//	[8-byte TIFF header][IFD0 block][ExifIFD block][GPS IFD block][InteropIFD block][IFD1 chain…]
//
// Returns exifStart, gpsStart, interopStart, ifd1Start (all absolute offsets
// from the beginning of the TIFF data, i.e. from byte 0 of the encoded output).
func computeIFDOffsets(e *EXIF, ifd0Entries, exifIFDEntries []IFDEntry) (exifStart, gpsStart, interopStart, ifd1Start uint32) {
	const headerSize = uint32(8)

	ifd0Size := ifdTotalSize(ifd0Entries)
	exifStart = headerSize + ifd0Size

	exifSize := uint32(0)
	if e.ExifIFD != nil {
		exifSize = ifdTotalSize(exifIFDEntries)
	}
	gpsStart = exifStart + exifSize

	gpsSize := uint32(0)
	if e.GPSIFD != nil {
		gpsSize = ifdTotalSize(e.GPSIFD.Entries)
	}
	interopStart = gpsStart + gpsSize

	interopSize := uint32(0)
	if e.InteropIFD != nil {
		interopSize = ifdTotalSize(e.InteropIFD.Entries)
	}
	ifd1Start = interopStart + interopSize

	return exifStart, gpsStart, interopStart, ifd1Start
}

// computeIFDOffsetsBigTIFF is the BigTIFF counterpart of computeIFDOffsets;
// see its doc comment for the layout diagram (the only difference is the
// 16-byte BigTIFF header in place of the 8-byte classic header, and
// ifdTotalSizeBigTIFF in place of ifdTotalSize — the #1 correctness rule, see
// its doc comment). Returns exifStart, gpsStart, interopStart, ifd1Start as
// absolute uint64 offsets from byte 0 of the encoded output.
func computeIFDOffsetsBigTIFF(e *EXIF, ifd0Entries, exifIFDEntries []IFDEntry) (exifStart, gpsStart, interopStart, ifd1Start uint64) {
	const headerSize = uint64(16)

	ifd0Size := ifdTotalSizeBigTIFF(ifd0Entries)
	exifStart = headerSize + ifd0Size

	exifSize := uint64(0)
	if e.ExifIFD != nil {
		exifSize = ifdTotalSizeBigTIFF(exifIFDEntries)
	}
	gpsStart = exifStart + exifSize

	gpsSize := uint64(0)
	if e.GPSIFD != nil {
		gpsSize = ifdTotalSizeBigTIFF(e.GPSIFD.Entries)
	}
	interopStart = gpsStart + gpsSize

	interopSize := uint64(0)
	if e.InteropIFD != nil {
		interopSize = ifdTotalSizeBigTIFF(e.InteropIFD.Entries)
	}
	ifd1Start = interopStart + interopSize

	return exifStart, gpsStart, interopStart, ifd1Start
}

// patchPointers writes the now-known target offsets into the placeholder
// IFDEntry.Value slices that were reserved by buildIFD0Entries and
// buildExifIFDEntries. Because Value slices point into the stack-allocated
// [4]byte arrays passed to the build helpers, this is a direct in-place
// write with no allocation.
//
// Entries are sorted by tag (invariant maintained by buildIFD0Entries and
// buildExifIFDEntries), so binary search locates each pointer in O(log n).
func patchPointers(ifd0Entries, exifIFDEntries []IFDEntry, order binary.ByteOrder, exifStart, gpsStart, interopStart uint32) {
	patchEntry := func(entries []IFDEntry, tag TagID, val uint32) {
		i := sort.Search(len(entries), func(i int) bool { return entries[i].Tag >= tag })
		if i < len(entries) && entries[i].Tag == tag {
			order.PutUint32(entries[i].Value, val)
		}
	}
	patchEntry(ifd0Entries, TagExifIFDPointer, exifStart)
	patchEntry(ifd0Entries, TagGPSIFDPointer, gpsStart)
	patchEntry(exifIFDEntries, TagInteropIFDPointer, interopStart)
}
