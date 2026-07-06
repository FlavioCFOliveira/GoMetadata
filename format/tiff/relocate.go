package tiff

// relocate.go — TIFF copy-and-relocate serializer (epic #33, tasks #92/#93/#94).
//
// Problem (SPIKE #6 Option A): TIFF stores image data (strips, tiles, JPEG
// thumbnails in non-thumbnail IFDs) at absolute file offsets embedded in
// IFD entries — StripOffsets (0x0111), TileOffsets (0x0144), and
// JPEGInterchangeFormat (0x0201) for main-image JPEG-in-TIFF. When the IFD
// block is rebuilt (e.g. to upsert IPTC/XMP tags) those absolute offsets
// become stale and the image is corrupted.
//
// Solution: enumerate every image-data block referenced by offset tags,
// rebuild the TIFF IFD structure, append each block at a new position, and
// patch the offset tags to point to the new positions.
//
// Covered by this implementation:
//   - IFD0 and all IFDs in the IFD1 chain (linked via next-IFD pointer):
//     StripOffsets/StripByteCounts and TileOffsets/TileByteCounts.
//   - Main-image JPEGInterchangeFormat (0x0201) in IFD0 only (JPEG-in-TIFF
//     main image; rare but valid per TIFF 6.0 §8.1).
//   - IFD1 JPEG thumbnails are handled by exif.Encode (which already
//     relocates ThumbnailData via patchThumbnailEntries); this layer does
//     not touch them.
//   - MakerNote blobs are copied verbatim. Per SPIKE #24, manufacturers
//     that store MakerNote-internal offsets relative to the blob itself
//     (Canon, Sony, Panasonic, Olympus, Pentax AOC) are move-safe. Nikon
//     bodies that embed a TIFF-relative internal offset are NOT fully safe;
//     full offset rebasing is deferred to a future epic.
//   - SubIFDs (tag 0x014A, task #94): DNG stores its full-resolution image
//     data in one or more SubIFDs referenced from IFD0 via the SubIFDs
//     (0x014A) pointer array. Each SubIFD may carry its own StripOffsets /
//     TileOffsets. This layer recursively follows 0x014A chains (supporting
//     multi-SubIFD and nested SubIFD-of-SubIFD), enumerates their image
//     blocks, and relocates both the SubIFD structures and the image-data
//     blocks they reference. The 0x014A pointer array in IFD0 is patched in
//     the finalTIFF output to point at the relocated SubIFD positions.
//
// Deferred:
//   - Deep multi-level SubIFD chains inside non-DNG RAW variants.
//
// Spec references:
//   - TIFF 6.0 §2: TIFF header, IFD structure, next-IFD chain.
//   - TIFF 6.0 §7: StripOffsets/StripByteCounts (split-strip images).
//   - TIFF 6.0 §8.1: JPEGInterchangeFormat / JPEGInterchangeFormatLength.
//   - TIFF 6.0 §15: TileOffsets/TileByteCounts (tiled images).
//   - TIFF Extension §F: SubIFDs tag (0x014A) — array of LONG offsets, each
//     pointing to an independent IFD that is a child of the current IFD.
//   - Adobe DNG Specification 1.7 §4: IFD0 holds a thumbnail; the
//     full-resolution image is in one or more SubIFDs (tag 0x014A).
//   - EXIF §4.5.5: JPEGInterchangeFormat in IFD1 (thumbnail); handled by
//     exif.Encode, not by this package.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Sentinel errors for the relocate subsystem.
var (
	// ErrBlockOutOfBounds is returned when an image-data block offset+size
	// exceeds the source TIFF buffer length.
	ErrBlockOutOfBounds = errors.New("tiff: image block out of bounds")

	// ErrUnsupportedOffsetType is returned when an offset array entry has an
	// unsupported TIFF type (not TypeLong/TypeShort).
	ErrUnsupportedOffsetType = errors.New("tiff: unsupported offset/count type")

	// ErrTruncatedOffsetArray is returned when an offset or bytecount value
	// array is shorter than expected for its declared Count.
	ErrTruncatedOffsetArray = errors.New("tiff: offset/bytecount array truncated")

	// ErrUnsupportedElemSize is returned when readUint encounters a byte-width
	// other than 2 or 4.
	ErrUnsupportedElemSize = errors.New("tiff: unsupported element size (only 2 or 4 supported)")

	// ErrBlockIndexMismatch is an internal invariant violation indicating that
	// image blocks are not ordered by index within their group.
	ErrBlockIndexMismatch = errors.New("tiff: block index mismatch")

	// ErrSubIFDPointerArrayOOB is returned when the 0x014A SubIFDs pointer
	// array in the re-encoded TIFF stream falls outside the buffer bounds.
	ErrSubIFDPointerArrayOOB = errors.New("tiff: SubIFDs pointer array out of bounds in re-encoded stream")

	// ErrSubIFDEntryNotFound is returned when the 0x014A SubIFDs entry is
	// absent from IFD0 of the re-encoded TIFF stream, preventing pointer patching.
	ErrSubIFDEntryNotFound = errors.New("tiff: 0x014A SubIFDs entry not found in re-encoded IFD0")
)

// maxSubIFDDepth is the maximum SubIFD recursion depth allowed by the IFD
// walker functions (enumerateSubIFDsAt, rebaseAllIFDsAfterGUID,
// rebaseAllIFDsAfterCR2Marker). A crafted file can chain sub-IFD pointers
// arbitrarily deep; capping at 8 prevents stack overflow while covering every
// real-world nesting depth (DNG spec only mandates one level).
//
// TIFF Extension §F: SubIFDs form a tree; real files have at most 2-3 levels.
// #172: depth cap for unbounded recursive IFD walker.
const maxSubIFDDepth = 8

// ---------------------------------------------------------------------------
// GM-W1 (CWE-770/CWE-405): image-block and SubIFD count budget
// ---------------------------------------------------------------------------
//
// Confirmed finding GM-W1: extractParallelOffsetBlocks and
// enumerateSubIFDsAt each allocate proportionally to an attacker-controlled
// Count field (StripOffsets/StripByteCounts/TileOffsets/TileByteCounts for
// the former, the 0x014A SubIFDs entry for the latter) with no upper bound
// beyond "does the declared array actually fit in the source buffer". Each
// array element costs only 2-4 bytes of genuine file content but drives one
// heap-allocated *imageBlock (or *subIFDInfo) plus, for SubIFDs, a
// map[uint32]bool sized with that same count as a pre-allocation hint. A
// crafted file well under maxFileSize (256 MiB) can therefore declare an
// implausibly large element count and drive multi-gigabyte allocation and
// seconds of CPU before any real image data is copied — reachable from the
// public Write/WriteFile API on every TIFF-based container (TIFF, DNG, CR2,
// NEF, ARW, ORF, RW2) via a plain Read->Write round-trip, with no metadata
// modification required.
//
// The fix mirrors exif/ifd.go's read-path traverseBudget: a fixed per-entry
// ceiling (maxImageBlocksPerOffsetEntry, maxSubIFDsPerEntry) rejects any ONE
// entry that is implausibly large before doing per-element work, and a
// single per-relocate-call aggregate budget (maxAggregateImageBlocks,
// imageBlockBudget) bounds the SUM across every entry, IFD1-chain link, and
// SubIFD-recursion level visited — closing the residual amplification where
// many entries, each individually within the per-entry cap, are chained
// together (up to maxTraverseChainIFDs=512 next-IFD links and/or
// maxSubIFDDepth=8 nested SubIFD levels).
// ---------------------------------------------------------------------------

// maxImageBlocksPerOffsetEntry caps the number of image-data blocks (strips
// or tiles) accepted from a single StripOffsets/StripByteCounts or
// TileOffsets/TileByteCounts entry pair, checked before
// extractParallelOffsetBlocks allocates anything proportional to the
// declared count.
//
// TIFF 6.0 §7 (strip organisation): NumberOfStrips = ceil(ImageLength /
// RowsPerStrip); with the spec's own guidance of ~8 KB per strip, even a
// large (100+ megapixel) sensor with RowsPerStrip=1 needs at most a few tens
// of thousands of strips. TIFF 6.0 §15 (tiled images) is bounded the same
// way for any tile size a real camera/scanner produces. 65536 (2^16) is a
// two-order-of-magnitude safety margin above any legitimate strip/tile count
// this library's target formats (TIFF, DNG, CR2, NEF, ARW, ORF, RW2) are
// known to produce, while bounding a single extractParallelOffsetBlocks call
// to at most a few MiB of heap.
const maxImageBlocksPerOffsetEntry = 65536

// maxSubIFDsPerEntry caps the number of SubIFD pointers accepted from a
// single 0x014A (SubIFDs) entry, at any recursion depth, checked before
// enumerateSubIFDsAt sizes the visited-set map or recurses further.
//
// TIFF Extension §F / Adobe DNG Spec §4: SubIFDs attach a small number of
// alternate-resolution or preview IFDs to a parent IFD — DNG typically has
// one, and multi-resolution previews raise that to at most a handful. 1024
// is a generous ceiling that no known real-world file approaches even 1% of,
// while bounding both the SubIFD walk and the visited-set map to a small,
// fixed cost.
const maxSubIFDsPerEntry = 1024

// maxAggregateImageBlocks bounds the cumulative number of image blocks
// (strips, tiles, JPEG previews) and SubIFD entries enumerated across every
// IFD, IFD1-chain link, and SubIFD visited within a single relocate call.
//
// maxImageBlocksPerOffsetEntry and maxSubIFDsPerEntry already bound any ONE
// entry, but a crafted file can still chain up to maxTraverseChainIFDs (512,
// exif/ifd.go) next-IFD links and/or maxSubIFDDepth (8) levels of nested
// SubIFDs, each individually within its own per-entry cap, to multiply the
// total block count far beyond what any real file needs. This budget closes
// that gap: it is charged once per enumerated entry (via
// imageBlockBudget.spend), so exhausting it aborts enumeration immediately
// rather than after the fact.
//
// A real TIFF/DNG/CR2/NEF/ARW/ORF/RW2 file carries at most a few IFDs (IFD0,
// an IFD1 thumbnail, and — for DNG — a handful of SubIFDs), each with at
// most a few thousand strips/tiles; the combined total across a whole file
// is in the low tens of thousands. 262144 (2^18, four times the per-entry
// strip/tile cap) is a two-order-of-magnitude safety margin above that,
// while bounding worst-case allocation across an entire write to a low
// tens-of-MiB figure regardless of how an attacker shapes the input.
const maxAggregateImageBlocks = 262144

// imageBlockBudget is the per-relocate-call accounting object for
// maxAggregateImageBlocks. See that constant's doc comment for the full
// rationale. A single imageBlockBudget is shared across the
// enumerateImageBlocks and enumerateSubIFDs calls made by each relocate
// entry point (relocateTIFFFromParsed and the CR2/NEF/ARW/ORF/RW2 variants),
// so the cap applies to their combined output, not to each call
// independently.
type imageBlockBudget struct {
	remaining int
}

// newImageBlockBudget returns a freshly sized budget for one relocate call.
func newImageBlockBudget() *imageBlockBudget {
	return &imageBlockBudget{remaining: maxAggregateImageBlocks}
}

// spend charges n units against the budget and reports whether the charge
// was accepted. Once a charge is rejected the budget stays exhausted (it is
// never restored), so callers must stop enumerating and return
// ErrTooManyImageBlocks as soon as spend returns false.
//
// A nil budget is treated as unbounded — every call site in this package
// always constructs a non-nil budget via newImageBlockBudget, so this is a
// defensive fallback, never the production behaviour.
func (bud *imageBlockBudget) spend(n int) bool {
	if bud == nil {
		return true
	}
	if n < 0 || n > bud.remaining {
		return false
	}
	bud.remaining -= n
	return true
}

// imageBlock describes a contiguous range of image bytes in the source TIFF
// and records the new position assigned to it in the rebuilt stream.
//
// ifd and entryTag identify which IFD and which offset tag owns this block so
// that patchIFDBlocks can write the new offset back into the correct entry.
// When the owning offset tag holds an array (multi-strip, multi-tile), index
// is the position within that array; for scalar offsets index is always 0.
//
// srcOffset/size/newOffset are uint64 (task #270) so that a BigTIFF source
// whose StripOffsets/TileOffsets/ByteCounts entries use LONG8 (type 16, 8
// bytes/element) round-trip without value truncation. In practice this
// package's own maxFileSize cap (256 MiB) bounds every value these fields
// ever hold well within uint32, but the wire format itself must still be
// represented faithfully — see offElemSz/cntElemSz below.
type imageBlock struct {
	srcOffset uint64     // absolute offset in the original TIFF buffer
	size      uint64     // byte length of the block
	newOffset uint64     // filled in by assignNewOffsets
	ifdPtr    *exif.IFD  // owning IFD (pointer to the parsed IFD)
	entryTag  exif.TagID // which offset tag owns this block
	index     int        // position within the offset array (0 for scalar)

	// offElemSz/cntElemSz record the SOURCE entry's element width (2, 4, or 8
	// bytes — SHORT, LONG, or LONG8 respectively) for the offset tag and its
	// bytecount partner. insertPlaceholders preserves this width verbatim
	// when re-inserting the placeholder entry, instead of always forcing
	// TypeLong, so that a BigTIFF file using LONG8 for its strip/tile arrays
	// (see testdata/corpus/tiff/metadata-extractor/BigTIFFLong8.tif) is not
	// silently downgraded to LONG on write.
	offElemSz uint8
	cntElemSz uint8
}

// groupKey identifies a (IFD pointer, offset tag) pair used to group blocks.
type groupKey struct {
	ifd *exif.IFD
	tag exif.TagID
}

// subIFDInfo describes a single SubIFD entry encountered while parsing the
// 0x014A SubIFDs array from a raw TIFF buffer.
//
// srcOffset is the absolute byte position of the SubIFD's IFD block in the
// original buffer (as stored in the 0x014A value array). The SubIFD's image
// blocks (strips/tiles) are accumulated in the same imageBlock pool as the
// main-IFD blocks; the subIFD-local *exif.IFD parsed below is only used to
// enumerate those blocks and collect their newOffset values.
//
// rawBytes holds a verbatim copy of the source SubIFD block (count word +
// entries + next-IFD pointer + value area) taken directly from base[]. It is
// appended to the output after the main exif.Encode skeleton and before the
// image blocks. The strip/tile offset entries within rawBytes are patched
// in-place after assignNewOffsets() to reflect the relocated block positions.
//
// parentOffsetArrayPos is the absolute position within the *finalTIFF* output
// buffer of the uint32 in the 0x014A value array that points at this SubIFD.
// It is patched after the SubIFD block is appended to finalTIFF so that the
// 0x014A pointer correctly names the new position.
type subIFDInfo struct {
	srcOffset uint64    // original SubIFD offset in base
	ifd       *exif.IFD // parsed SubIFD (for block enumeration)
	rawBytes  []byte    // verbatim copy of source SubIFD bytes
	newOffset uint64    // filled in when SubIFD is appended to output
	// Note: the 0x014A pointer array is patched via patchSubIFDPointers which
	// scans the re-encoded finalTIFF — no per-SubIFD pointer slot is tracked here.
}

// relocateTIFF is the core TIFF copy-and-relocate serializer.
//
// It parses base as a TIFF stream, upserts rawIPTC and/or rawXMP into IFD0,
// enumerates every image-data block referenced by offset/bytecount tag pairs
// (including SubIFD chains via tag 0x014A), re-encodes the IFD structure via
// exif.Encode, appends each SubIFD block, and finally appends each image block
// at a new absolute offset. The result is a well-formed TIFF stream with
// byte-identical image data and updated metadata.
//
// Algorithm:
//  1. exif.Parse(base) → EXIF struct with all IFDs.
//  2. Upsert IPTC (0x83BB) / XMP (0x02BC) in IFD0.
//  3. Enumerate image blocks from IFD0 + IFD1 chain (strips, tiles, main-image JPEG).
//  4. Parse SubIFDs (0x014A) from raw base and enumerate their image blocks.
//  5. Remove the stale image-data offset entries from each IFD (main IFDs only;
//     SubIFD entries are patched at the raw-byte level).
//  6. Re-insert placeholder entries (correct type and Count = N, zero values)
//     and call exif.Encode to measure the true IFD structure size (ifdEnd).
//  7. Assign new absolute offsets: SubIFDs first, then image blocks.
//  8. Update placeholder value bytes (main IFDs) and patch SubIFD offset
//     entries (in rawBytes) with the final image-block offsets.
//  9. Re-encode via exif.Encode → finalTIFF (same size as step 6).
//
// 10. Patch the 0x014A array in finalTIFF to point at the new SubIFD positions.
// 11. Append each SubIFD's raw bytes.
// 12. Append each image block's bytes from base.
//
// Two calls to exif.Encode are needed (steps 6 and 9). The IFD structure size
// does not change between calls because only value bytes are updated.
//
// relocateTIFF is a thin wrapper that parses base first then delegates to
// relocateTIFFFromParsed. Callers with a pre-parsed *exif.EXIF (e.g. the write
// path where the struct was already parsed by Read and subsequently mutated by
// Set* methods) should call relocateTIFFFromParsed directly to avoid the
// redundant parse and to ensure IFD edits (copyright, GPS, etc.) are not lost.
func relocateTIFF(base []byte, rawIPTC, rawXMP []byte) ([]byte, error) {
	e, err := exif.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("tiff: parse for relocation: %w", err)
	}
	return relocateTIFFFromParsed(base, e, rawIPTC, rawXMP)
}

// relocateTIFFFromParsed is the implementation of the TIFF copy-and-relocate
// serializer. Unlike relocateTIFF it accepts a pre-parsed *exif.EXIF instead
// of re-parsing base. This is critical for the write path:
//
//   - base must be the ORIGINAL TIFF bytes (image data at original offsets).
//   - e must be the MODIFIED *exif.EXIF struct (EXIF tags already set by the
//     caller, e.g. SetCopyright, SetGPS). Its StripOffsets/TileOffsets still
//     point at the original offsets in base — which is correct, because we read
//     image blocks from base and assign new offsets in the output.
//
// If e is nil, the function falls back to parsing base (same behaviour as
// relocateTIFF).
func relocateTIFFFromParsed(base []byte, e *exif.EXIF, rawIPTC, rawXMP []byte) ([]byte, error) { //nolint:cyclop,gocyclo,funlen // complex by necessity: TIFF structural rewriting requires handling all image-block patterns in one function
	if e == nil {
		var err error
		e, err = exif.Parse(base)
		if err != nil {
			return nil, fmt.Errorf("tiff: parse for relocation: %w", err)
		}
	}

	order := e.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}

	// Step 2: upsert metadata tags in IFD0.
	if e.IFD0 == nil {
		// Should not happen for a parseable TIFF, but guard defensively.
		e.IFD0 = &exif.IFD{}
	}
	// #190 fix: guard on len>0 rather than !=nil. A non-nil but zero-length
	// rawIPTC slice (empty encoding artifact) must not produce a zero-length
	// 0x83BB tag. encodeIPTC already normalises empty→nil, but this guard
	// provides belt-and-suspenders safety for any caller that bypasses encodeIPTC.
	if len(rawIPTC) > 0 {
		// Adobe XMP Spec / ExifTool convention: IPTC-NAA (0x83BB) as TypeLong.
		// upsertIFD0Entry pads value to 4-byte boundary; Count = nLongs.
		upsertIFD0Entry(e.IFD0, exif.TagIPTC, exif.TypeLong, rawIPTC)
	}
	if len(rawXMP) > 0 {
		// Adobe XMP Spec (TIFF Technical Note 3): XMP (0x02BC) as TypeByte.
		// #190 fix: same belt-and-suspenders guard for consistency.
		upsertIFD0Entry(e.IFD0, exif.TagXMP, exif.TypeByte, rawXMP)
	}

	// Step 2.5: clear IFD0.ThumbnailData before block enumeration.
	//
	// exif.Parse sets IFD0.ThumbnailData when IFD0 has both 0x0201
	// (JPEGInterchangeFormat) and 0x0202 (JPEGInterchangeFormatLength), because
	// extractJPEGThumbnail runs on every parsed IFD, not just the IFD1 chain.
	// Some camera formats (e.g. Sony ARW) store a large preview JPEG in IFD0 via
	// these tags.  enumerateIFDBlocks skips JPEG blocks when ThumbnailData != nil,
	// assuming exif.Encode handles them — but exif.Encode only processes
	// ThumbnailData for the IFD1 chain (IFD0.Next), never for IFD0 itself.
	// Clearing ThumbnailData from IFD0 forces the preview to be enumerated as a
	// standard imageBlock and relocated correctly.  exif.Encode is unaffected
	// because it never reads IFD0.ThumbnailData.
	//
	// EXIF §4.5.5 / TIFF 6.0 §8.1: JPEGInterchangeFormat (0x0201) in IFD0 is a
	// preview JPEG (not the IFD1 thumbnail) and must be treated as an image block.
	if e.IFD0 != nil {
		e.IFD0.ThumbnailData = nil
	}

	// Step 3: enumerate image blocks from the IFD chain.
	// Image block offsets in e point into base (the original TIFF bytes), which
	// is correct: we copy bytes from base[blk.srcOffset : blk.srcOffset+blk.size]
	// in step 12 below.
	//
	// GM-W1: budget is shared across both enumeration calls below so that the
	// cumulative number of image blocks + SubIFD entries for this entire
	// relocate call is bounded, not merely each call independently.
	bigTIFF := e.BigTIFF
	budget := newImageBlockBudget()
	blocks, enumerateErr := enumerateImageBlocks(base, e, order, bigTIFF, budget)
	if enumerateErr != nil {
		return nil, fmt.Errorf("tiff: enumerate image blocks: %w", enumerateErr)
	}

	// Step 4: parse SubIFDs (tag 0x014A) from the raw base buffer.
	//
	// TIFF Extension §F / Adobe DNG Spec §4: SubIFDs tag (0x014A) holds an
	// array of LONG (or, in BigTIFF, LONG8/IFD/IFD8) offsets, each pointing to
	// an independent child IFD. DNG stores its full-resolution image data in
	// one or more SubIFDs. Each SubIFD may carry its own StripOffsets /
	// TileOffsets blocks that must be relocated.
	//
	// #270: enumerateSubIFDs re-scans base directly rather than trusting the
	// already-parsed *exif.IFDEntry for 0x014A — see its doc comment.
	subIFDs, subBlocks, subErr := enumerateSubIFDs(base, e, order, budget)
	if subErr != nil {
		return nil, fmt.Errorf("tiff: enumerate SubIFDs: %w", subErr)
	}
	blocks = append(blocks, subBlocks...)

	// Short-circuit when there are no image blocks to relocate and no SubIFDs.
	if len(blocks) == 0 && len(subIFDs) == 0 {
		out, encErr := exif.Encode(e)
		if encErr != nil {
			return nil, fmt.Errorf("tiff: encode (no image blocks): %w", encErr)
		}
		// Still apply MakerNote OOL rebasing even on the short-circuit path.
		// exif.Encode may have moved the MakerNote blob to a different absolute
		// offset within the re-encoded IFD section.
		// #127: MakerNote OOL offset rebasing incomplete on write.
		rebaseGenericMakerNote(out, e, order)
		return out, nil
	}

	// Step 5: remove stale image-data offset entries from main IFDs only.
	// SubIFD entries are handled at the raw-byte level (steps 8/11).
	// Blocks that belong to SubIFDs (ifdPtr points to a sub-IFD parsed by
	// enumerateSubIFDs) must not be passed to removeImageOffsetEntries,
	// because those IFDs are NOT part of the exif.Encode model — mutating
	// them has no effect on the output and only clutters the map.
	mainBlocks := filterMainBlocks(blocks, subIFDs)
	removeImageOffsetEntries(mainBlocks)

	// Step 6: re-insert placeholder entries for main-IFD blocks and encode
	// to learn the exact IFD structure size.
	offsetValueSlices := insertPlaceholders(mainBlocks)

	skeleton, skelErr := exif.Encode(e)
	if skelErr != nil {
		return nil, fmt.Errorf("tiff: encode placeholder: %w", skelErr)
	}
	ifdEnd := uint64(len(skeleton))

	// Step 7: assign new absolute offsets.
	// SubIFD blocks are placed first (SubIFD structures after the main EXIF
	// block), then main image blocks follow after all SubIFD structures.
	subIFDsSize := computeSubIFDsSize(subIFDs)
	// Guard against overflow: ifdEnd + subIFDsSize > MaxUint32 means the
	// combined IFD+SubIFD region alone is already beyond the classic TIFF
	// 4 GiB limit. TIFF 6.0 §2: classic offsets are uint32; a value that
	// wraps around zero would make StripOffsets/TileOffsets point into the
	// IFD region, corrupting the output.
	//
	// This ceiling is NOT re-litigated for BigTIFF: it is many orders of
	// magnitude above anything this package's own maxFileSize cap (256 MiB)
	// can ever produce, so it remains a harmless defence-in-depth guard for
	// both containers rather than a real BigTIFF limitation (BigTIFF's own
	// format ceiling is 2^64, per BigTIFF spec §2).
	imageStart := ifdEnd + subIFDsSize
	if imageStart > math.MaxUint32 {
		return nil, fmt.Errorf("tiff: ifdEnd=%d subIFDsSize=%d: %w", ifdEnd, subIFDsSize, ErrOffsetOverflow)
	}
	assignNewOffsets(blocks, imageStart)
	assignSubIFDOffsets(subIFDs, ifdEnd)

	// Step 8a: update placeholder value bytes (main-IFD blocks).
	updatePlaceholders(mainBlocks, offsetValueSlices, order)

	// Step 8b: patch SubIFD raw bytes — update strip/tile offset entries in
	// each SubIFD's rawBytes to point at the newly assigned image-block offsets.
	patchSubIFDImageOffsets(subIFDs, blocks, bigTIFF, order)

	// Step 9: re-encode → finalTIFF. Same IFD layout as step 6.
	finalTIFF, finalErr := exif.Encode(e)
	if finalErr != nil {
		return nil, fmt.Errorf("tiff: encode final: %w", finalErr)
	}

	// Step 9.5: rebase OOL MakerNote pointers for makers that store TIFF-absolute
	// val_or_off fields (Sony plain-IFD, Olympus OLYMP-type).
	//
	// exif.Encode copies the MakerNote blob verbatim. When the blob moves to a
	// different absolute position in the new TIFF stream, any TIFF-absolute OOL
	// pointer inside the MakerNote IFD becomes stale. rebaseGenericMakerNote
	// detects the maker convention from the blob prefix and patches all OOL
	// val_or_off fields in-place. Blob-relative makers (Canon, Panasonic, Nikon
	// Type-3, Olympus OLYMPUS-type, Pentax) are no-ops in that function.
	//
	// Note: ARW-path Sony rebasing is performed later by rebaseSonyMakerNote in
	// the ARW-specific relocator (relocate_arw.go Step 9.5). This step handles
	// the standard TIFF path only.
	//
	// #127: MakerNote OOL offset rebasing incomplete on write.
	// #270: rebaseGenericMakerNote itself declines (documented fail-safe
	// no-op) when e.BigTIFF is true — see that function's doc comment.
	rebaseGenericMakerNote(finalTIFF, e, order)

	// Step 10: patch the 0x014A SubIFDs pointer array in finalTIFF.
	// exif.Encode preserves 0x014A's original declared Type/Count/Value
	// verbatim (it is generic pass-through for tags it does not specially
	// interpret). We must overwrite the value bytes with the new SubIFD
	// positions before appending anything.
	if len(subIFDs) > 0 {
		if err := patchSubIFDPointers(finalTIFF, subIFDs, bigTIFF, order); err != nil {
			return nil, fmt.Errorf("tiff: patch SubIFD pointers: %w", err)
		}
	}

	// Step 11: append each SubIFD's raw bytes (with already-patched image
	// offsets from step 8b).
	// TIFF 6.0 §2: each SubIFD block must start at a word (even) boundary.
	// assignSubIFDOffsets already reserved space for the 0x00 pad byte;
	// insert it here to keep finalTIFF and the assigned offsets in sync.
	for _, si := range subIFDs {
		if len(finalTIFF)&1 == 1 {
			finalTIFF = append(finalTIFF, 0x00)
		}
		finalTIFF = append(finalTIFF, si.rawBytes...)
	}

	// Step 12: append each image block's bytes from the source buffer.
	for _, blk := range blocks {
		end := blk.srcOffset + blk.size
		if end > uint64(len(base)) {
			return nil, fmt.Errorf("tiff: image block offset=%d size=%d: %w",
				blk.srcOffset, blk.size, ErrBlockOutOfBounds)
		}
		finalTIFF = append(finalTIFF, base[blk.srcOffset:end]...)
	}

	return finalTIFF, nil
}

// filterMainBlocks returns only the blocks whose owning IFD is NOT one of the
// SubIFDs parsed by enumerateSubIFDs. This is necessary because SubIFD-owned
// blocks are relocated at the raw-byte level and must not be passed to
// removeImageOffsetEntries (which tries to remove entries from the exif.IFD
// structs — those structs are only consulted by exif.Encode for main IFDs).
func filterMainBlocks(blocks []*imageBlock, subIFDs []*subIFDInfo) []*imageBlock {
	if len(subIFDs) == 0 {
		return blocks
	}
	// Build a set of sub-IFD pointers for O(1) lookup.
	subIFDSet := make(map[*exif.IFD]struct{}, len(subIFDs))
	for _, si := range subIFDs {
		if si.ifd != nil {
			subIFDSet[si.ifd] = struct{}{}
		}
	}
	out := make([]*imageBlock, 0, len(blocks))
	for _, blk := range blocks {
		if _, isSubIFD := subIFDSet[blk.ifdPtr]; !isSubIFD {
			out = append(out, blk)
		}
	}
	return out
}

// enumerateImageBlocks scans IFD0 and the IFD1 chain for image-data blocks
// referenced by offset/bytecount tag pairs. SubIFD recursion (tag 0x014A)
// is handled separately by enumerateSubIFDs (task #94).
//
// bigTIFF selects the element-size table used to interpret StripOffsets/
// TileOffsets/ByteCounts arrays that may legitimately use LONG8 (task #270):
// see extractParallelOffsetBlocks.
//
// GM-W1: budget bounds the cumulative number of image blocks accepted across
// every IFD in the chain (up to maxTraverseChainIFDs=512); see
// maxAggregateImageBlocks for the full rationale.
func enumerateImageBlocks(base []byte, e *exif.EXIF, order binary.ByteOrder, bigTIFF bool, budget *imageBlockBudget) ([]*imageBlock, error) {
	var blocks []*imageBlock
	for ifd := e.IFD0; ifd != nil; ifd = ifd.Next {
		iblocks, err := enumerateIFDBlocks(base, ifd, order, bigTIFF, budget)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, iblocks...)
	}
	return blocks, nil
}

// enumerateIFDBlocks enumerates image-data blocks from a single IFD.
// It handles three tag-pair patterns:
//
//	StripOffsets (0x0111) + StripByteCounts (0x0117)
//	TileOffsets  (0x0144) + TileByteCounts  (0x0145)
//	JPEGInterchangeFormat (0x0201) + JPEGInterchangeFormatLength (0x0202)
//
// For IFDs whose ThumbnailData is non-nil, JPEGInterchangeFormat is already
// managed by exif.Encode and must be skipped here.
//
// GM-W1: budget bounds the cumulative number of blocks accepted from both
// the strip and tile entry pairs; see maxAggregateImageBlocks.
func enumerateIFDBlocks(base []byte, ifd *exif.IFD, order binary.ByteOrder, bigTIFF bool, budget *imageBlockBudget) ([]*imageBlock, error) { //nolint:cyclop // handles all three tag-pair patterns in a single linear scan; splitting would hurt readability
	var blocks []*imageBlock

	stripOff := ifd.Get(exif.TagStripOffsets)
	stripLen := ifd.Get(exif.TagStripByteCounts)
	if stripOff != nil && stripLen != nil {
		sb, err := extractParallelOffsetBlocks(base, ifd, exif.TagStripOffsets, stripOff, stripLen, order, bigTIFF, budget)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, sb...)
	}

	tileOff := ifd.Get(exif.TagTileOffsets)
	tileLen := ifd.Get(exif.TagTileByteCounts)
	if tileOff != nil && tileLen != nil {
		tb, err := extractParallelOffsetBlocks(base, ifd, exif.TagTileOffsets, tileOff, tileLen, order, bigTIFF, budget)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, tb...)
	}

	// JPEGInterchangeFormat: skip when ThumbnailData is non-nil (exif.Encode handles it).
	if ifd.ThumbnailData == nil {
		blocks = appendJPEGBlock(len(base), ifd, blocks, bigTIFF, order)
	}

	return blocks, nil
}

// appendJPEGBlock appends a JPEGInterchangeFormat block to blocks if present.
// Extracted to reduce nestif complexity in enumerateIFDBlocks.
// baseLen is the length of the source TIFF buffer; it is used to skip entries
// whose indicated offset+size would fall outside the buffer.
func appendJPEGBlock(baseLen int, ifd *exif.IFD, blocks []*imageBlock, bigTIFF bool, order binary.ByteOrder) []*imageBlock { //nolint:gocyclo // width-aware (task #270) bounds-checking on two scalar fields; splitting further would hurt readability
	jpegOff := ifd.Get(exif.TagJPEGInterchangeFormat)
	jpegLen := ifd.Get(exif.TagJPEGInterchangeFormatLength)
	if jpegOff == nil || jpegLen == nil {
		return blocks
	}
	offElemSz := elemSizeFor(uint16(jpegOff.Type), bigTIFF)
	cntElemSz := elemSizeFor(uint16(jpegLen.Type), bigTIFF)
	if offElemSz == 0 || cntElemSz == 0 || uint64(len(jpegOff.Value)) < offElemSz || uint64(len(jpegLen.Value)) < cntElemSz {
		return blocks
	}
	off, err := readUint(jpegOff.Value, int(offElemSz), order) //nolint:gosec // G115: offElemSz ∈ {2,4,8}
	if err != nil {
		return blocks
	}
	size, err := readUint(jpegLen.Value, int(cntElemSz), order) //nolint:gosec // G115: cntElemSz ∈ {2,4,8}
	if err != nil || size == 0 {
		return blocks
	}
	// Skip entries whose indicated range falls outside the source buffer.
	// (Bounds are re-verified in step 12 of relocateTIFF before appending bytes.)
	end := off + size
	if end > uint64(baseLen) { //nolint:gosec // G115: baseLen = len([]byte), always non-negative
		return blocks
	}
	return append(blocks, &imageBlock{
		srcOffset: off,
		size:      size,
		ifdPtr:    ifd,
		entryTag:  exif.TagJPEGInterchangeFormat,
		index:     0,
		offElemSz: uint8(offElemSz), //nolint:gosec // G115: offElemSz ∈ {2,4,8}
		cntElemSz: uint8(cntElemSz), //nolint:gosec // G115: cntElemSz ∈ {2,4,8}
	})
}

// extractParallelOffsetBlocks extracts imageBlock records from parallel
// offset/bytecount array entries.
//
// TIFF 6.0 §7: StripOffsets[i] is an absolute offset; StripByteCounts[i] is
// the byte length of that strip. Same for TileOffsets/TileByteCounts.
//
// bigTIFF selects typeSizeBigTIFF over typeSize (task #270) so that a BigTIFF
// source declaring these arrays as LONG8 (type 16, 8 bytes/element — see
// testdata/corpus/tiff/metadata-extractor/BigTIFFLong8.tif) is read correctly
// instead of being rejected with ErrUnsupportedOffsetType. offElemSz/cntElemSz
// are recorded on each imageBlock so insertPlaceholders can preserve the
// source's element width on write.
//
// GM-W1 (CWE-770/CWE-405): n is rejected outright once it exceeds
// maxImageBlocksPerOffsetEntry, and the accepted n is additionally charged
// against budget (maxAggregateImageBlocks), both BEFORE the
// `blocks := make([]*imageBlock, 0, n)` allocation below — a crafted file
// with an implausibly large StripOffsets/TileOffsets Count must not be able
// to drive proportional heap allocation.
func extractParallelOffsetBlocks( //nolint:cyclop,gocyclo // bounds-checking on two parallel arrays requires several branches; splitting further reduces clarity
	base []byte,
	ifd *exif.IFD,
	offsetTag exif.TagID,
	offsetEntry, countEntry *exif.IFDEntry,
	order binary.ByteOrder,
	bigTIFF bool,
	budget *imageBlockBudget,
) ([]*imageBlock, error) {
	if offsetEntry.Count != countEntry.Count {
		// Mismatched counts are a format error; skip silently to avoid blocking writes.
		return nil, nil
	}
	n := int(offsetEntry.Count)
	if n == 0 {
		return nil, nil
	}
	if n > maxImageBlocksPerOffsetEntry {
		return nil, fmt.Errorf("tiff: tag 0x%04X: count=%d exceeds %d: %w",
			offsetTag, n, maxImageBlocksPerOffsetEntry, ErrTooManyImageBlocks)
	}
	if !budget.spend(n) {
		return nil, fmt.Errorf("tiff: tag 0x%04X: count=%d: %w", offsetTag, n, ErrTooManyImageBlocks)
	}

	offElemSz64 := elemSizeFor(uint16(offsetEntry.Type), bigTIFF)
	cntElemSz64 := elemSizeFor(uint16(countEntry.Type), bigTIFF)
	if offElemSz64 == 0 || cntElemSz64 == 0 {
		return nil, fmt.Errorf("tiff: tag 0x%04X: %w", offsetTag, ErrUnsupportedOffsetType)
	}
	offElemSz, cntElemSz := int(offElemSz64), int(cntElemSz64) //nolint:gosec // G115: elemSizeFor returns 0 (rejected above), 1, 2, 4, or 8 — always fits int

	if len(offsetEntry.Value) < n*offElemSz {
		return nil, fmt.Errorf("tiff: tag 0x%04X: %w", offsetTag, ErrTruncatedOffsetArray)
	}
	if len(countEntry.Value) < n*cntElemSz {
		return nil, fmt.Errorf("tiff: bytecount for tag 0x%04X: %w", offsetTag, ErrTruncatedOffsetArray)
	}

	blocks := make([]*imageBlock, 0, n)
	for i := range n {
		off, err := readUint(offsetEntry.Value[i*offElemSz:], offElemSz, order)
		if err != nil {
			return nil, fmt.Errorf("tiff: read offset[%d] tag 0x%04X: %w", i, offsetTag, err)
		}
		size, err := readUint(countEntry.Value[i*cntElemSz:], cntElemSz, order)
		if err != nil {
			return nil, fmt.Errorf("tiff: read bytecount[%d] tag 0x%04X: %w", i, offsetTag, err)
		}

		blk := &imageBlock{
			srcOffset: off,
			size:      0,
			ifdPtr:    ifd,
			entryTag:  offsetTag,
			index:     i,
			offElemSz: uint8(offElemSz), //nolint:gosec // G115: offElemSz ∈ {2,4,8}, validated by elemSizeFor above
			cntElemSz: uint8(cntElemSz), //nolint:gosec // G115: cntElemSz ∈ {2,4,8}, validated by elemSizeFor above
		}
		if size == 0 {
			blocks = append(blocks, blk)
			continue
		}

		// uint64 bounds check (TIFF 6.0 §2 / BigTIFF spec §2: offsets index
		// into the file's own byte length, which this package's own
		// maxFileSize cap already bounds well within uint64 range).
		end := off + size
		if end > uint64(len(base)) {
			return nil, fmt.Errorf("tiff: tag 0x%04X[%d] offset=%d size=%d: %w",
				offsetTag, i, off, size, ErrBlockOutOfBounds)
		}
		blk.size = size
		blocks = append(blocks, blk)
	}
	return blocks, nil
}

// readUint reads a little- or big-endian unsigned integer of elemSz bytes
// (2, 4, or 8) from b, returned widened to uint64.
//
// elemSz=8 support (task #270) reads BigTIFF's LONG8 type (16 — element size
// 8 bytes), used by StripOffsets/StripByteCounts/TileOffsets/TileByteCounts
// when a BigTIFF source declares them as LONG8 rather than LONG or SHORT.
func readUint(b []byte, elemSz int, order binary.ByteOrder) (uint64, error) {
	switch elemSz {
	case 2:
		if len(b) < 2 {
			return 0, fmt.Errorf("tiff: need 2 bytes, have %d: %w", len(b), ErrTruncatedOffsetArray)
		}
		return uint64(order.Uint16(b)), nil
	case 4:
		if len(b) < 4 {
			return 0, fmt.Errorf("tiff: need 4 bytes, have %d: %w", len(b), ErrTruncatedOffsetArray)
		}
		return uint64(order.Uint32(b)), nil
	case 8:
		if len(b) < 8 {
			return 0, fmt.Errorf("tiff: need 8 bytes, have %d: %w", len(b), ErrTruncatedOffsetArray)
		}
		return order.Uint64(b), nil
	}
	return 0, fmt.Errorf("tiff: elem size %d: %w", elemSz, ErrUnsupportedElemSize)
}

// removeImageOffsetEntries removes image-data offset tags from each IFD.
// The entries are removed so that exif.Encode can produce the skeleton.
// They are re-inserted with corrected values by insertPlaceholders.
func removeImageOffsetEntries(blocks []*imageBlock) {
	// Build a set of (ifd, tag) pairs to remove.
	type ifdTagKey struct {
		ifd *exif.IFD
		tag exif.TagID
	}
	toRemove := make(map[ifdTagKey]struct{}, len(blocks)*2)
	for _, blk := range blocks {
		toRemove[ifdTagKey{blk.ifdPtr, blk.entryTag}] = struct{}{}
		if countTag := bytecountTagFor(blk.entryTag); countTag != 0 {
			toRemove[ifdTagKey{blk.ifdPtr, countTag}] = struct{}{}
		}
	}
	for key := range toRemove {
		removeEntryFromIFD(key.ifd, key.tag)
	}
}

// removeEntryFromIFD removes the entry with the given tag from ifd.Entries.
// If absent, the function is a no-op. Sorted order is preserved.
func removeEntryFromIFD(ifd *exif.IFD, tag exif.TagID) {
	i := sort.Search(len(ifd.Entries), func(j int) bool { return ifd.Entries[j].Tag >= tag })
	if i < len(ifd.Entries) && ifd.Entries[i].Tag == tag {
		ifd.Entries = append(ifd.Entries[:i], ifd.Entries[i+1:]...)
	}
}

// bytecountTagFor returns the byte-count partner of the given offset tag.
// Returns 0 for unknown tags.
func bytecountTagFor(offsetTag exif.TagID) exif.TagID {
	switch offsetTag {
	case exif.TagStripOffsets:
		return exif.TagStripByteCounts
	case exif.TagTileOffsets:
		return exif.TagTileByteCounts
	case exif.TagJPEGInterchangeFormat:
		return exif.TagJPEGInterchangeFormatLength
	}
	return 0
}

// assignNewOffsets assigns a new absolute file offset to each image block,
// placing them contiguously starting at imageStart.
//
// TIFF 6.0 §2: all offsets are measured from byte 0 of the TIFF stream.
//
// The math.MaxUint32 saturation ceiling below is deliberately kept identical
// for BigTIFF sources too (task #270): this package's own maxFileSize cap
// (256 MiB) means cur can never realistically approach it for either
// container, so it remains a harmless defence-in-depth guard rather than an
// artificial BigTIFF limitation — see the comment in relocateTIFFFromParsed
// at the ifdEnd/subIFDsSize overflow check for the full rationale.
func assignNewOffsets(blocks []*imageBlock, imageStart uint64) {
	cur := imageStart
	for _, blk := range blocks {
		blk.newOffset = cur
		if blk.size > math.MaxUint32-cur {
			// Saturate to prevent overflow; caller will detect the invalid offset.
			blk.newOffset = math.MaxUint32
		} else {
			cur += blk.size
		}
	}
}

// widthToType returns the TIFF type code whose element size is w bytes, for
// re-inserting an offset/bytecount placeholder entry with the SAME width the
// source entry declared (task #270: preserve SHORT/LONG/LONG8 verbatim rather
// than always forcing TypeLong, so a BigTIFF source using LONG8 for its
// strip/tile arrays is not silently downgraded on write).
//
// Falls back to TypeLong for any unexpected width (defensive; offElemSz/
// cntElemSz are always populated from elemSizeFor's {2,4,8} result set).
func widthToType(w uint8) exif.DataType {
	switch w {
	case 2:
		return exif.TypeShort
	case 8:
		return exif.TypeLong8
	default:
		return exif.TypeLong
	}
}

// insertPlaceholders inserts image-data offset and bytecount entries with
// zeroed value bytes into each owning IFD. The placeholder Count = N
// (number of elements) and Type match the SOURCE entry's declared width
// (widthToType), so that exif.Encode accounts for the exact final
// value-area space and preserves the source's SHORT/LONG/LONG8 type choice.
//
// Returns a map from groupKey to the pair of value slices [offVals, cntVals].
// updatePlaceholders writes the real values into these slices in-place;
// since IFDEntry.Value points to the same backing arrays, the updated values
// are visible to exif.Encode without re-insertion.
func insertPlaceholders(blocks []*imageBlock) map[groupKey][2][]byte {
	// Collect unique groups in stable insertion order, along with the first
	// block seen for each group (its offElemSz/cntElemSz apply to the whole
	// group, since all blocks in a group came from the same source entry).
	seen := make(map[groupKey]int)
	var keys []groupKey
	var widths []*imageBlock
	for _, blk := range blocks {
		k := groupKey{blk.ifdPtr, blk.entryTag}
		if _, ok := seen[k]; !ok {
			seen[k] = len(keys)
			keys = append(keys, k)
			widths = append(widths, blk)
		}
	}

	counts := make([]int, len(keys))
	for _, blk := range blocks {
		k := groupKey{blk.ifdPtr, blk.entryTag}
		counts[seen[k]]++
	}

	result := make(map[groupKey][2][]byte, len(keys))
	for i, k := range keys {
		n := counts[i]
		offW := int(widths[i].offElemSz)
		cntW := int(widths[i].cntElemSz)
		offVals := make([]byte, n*offW)
		cntVals := make([]byte, n*cntW)

		// TIFF 6.0 §2 / BigTIFF spec §2: Count = number of values (not bytes).
		upsertIFDEntryWithCount(k.ifd, k.tag, widthToType(widths[i].offElemSz), uint32(n), offVals) //nolint:gosec // G115: n bounded by strip/tile count, < 2^32
		if countTag := bytecountTagFor(k.tag); countTag != 0 {
			upsertIFDEntryWithCount(k.ifd, countTag, widthToType(widths[i].cntElemSz), uint32(n), cntVals) //nolint:gosec // G115: same
		}

		result[k] = [2][]byte{offVals, cntVals}
	}
	return result
}

// updatePlaceholders writes the real new offsets and block sizes into the
// value slices inserted by insertPlaceholders. Because IFDEntry.Value slices
// share backing arrays, exif.Encode in step 9 sees the updated values.
//
// Each block's own offElemSz/cntElemSz select the write width (2, 4, or 8
// bytes), preserving the source entry's declared type (task #270).
//
// TIFF 6.0 §2: StripOffsets values are absolute byte offsets from byte 0 of
// the TIFF stream, encoded in the TIFF file byte order.
func updatePlaceholders(blocks []*imageBlock, slices map[groupKey][2][]byte, order binary.ByteOrder) {
	for _, blk := range blocks {
		k := groupKey{blk.ifdPtr, blk.entryTag}
		pair, ok := slices[k]
		if !ok {
			continue
		}
		putWidth(pair[0][blk.index*int(blk.offElemSz):], blk.offElemSz, order, blk.newOffset)
		putWidth(pair[1][blk.index*int(blk.cntElemSz):], blk.cntElemSz, order, blk.size)
	}
}

// putWidth writes v into dst using w bytes (2, 4, or 8), matching readUint's
// element-size convention. Falls back to a 4-byte write (LONG) for an
// unexpected width, matching widthToType's default.
func putWidth(dst []byte, w uint8, order binary.ByteOrder, v uint64) {
	switch w {
	case 2:
		order.PutUint16(dst, uint16(v)) //nolint:gosec // G115: caller guarantees v fits SHORT when offElemSz/cntElemSz==2 (source declared SHORT)
	case 8:
		order.PutUint64(dst, v)
	default:
		order.PutUint32(dst, uint32(v)) //nolint:gosec // G115: values bounded by maxFileSize, always < 2^32
	}
}

// upsertIFDEntryWithCount inserts or replaces an entry in ifd with the given
// tag, type, count, and value bytes. This differs from upsertIFD0Entry in
// that the Count is provided explicitly rather than inferred from len(value).
//
// TIFF 6.0 §2 / BigTIFF spec §2: Count = number of elements (not bytes). For a
// 2-element LONG array Count=2, value is 8 bytes (16 for LONG8).
//
// The sorted-by-tag invariant is maintained using binary search insertion.
func upsertIFDEntryWithCount(ifd *exif.IFD, tag exif.TagID, typ exif.DataType, count uint32, value []byte) {
	entry := exif.IFDEntry{
		Tag:   tag,
		Type:  typ,
		Count: count,
		Value: value,
	}

	n := len(ifd.Entries)
	lo, hi := 0, n
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if ifd.Entries[mid].Tag < tag {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	i := lo

	if i < n && ifd.Entries[i].Tag == tag {
		ifd.Entries[i] = entry
		return
	}
	// Insert at sorted position (one memmove, O(n)).
	ifd.Entries = append(ifd.Entries, exif.IFDEntry{})
	copy(ifd.Entries[i+1:], ifd.Entries[i:])
	ifd.Entries[i] = entry
}

// ---------------------------------------------------------------------------
// SubIFD support (task #94)
// ---------------------------------------------------------------------------

// enumerateSubIFDs parses the SubIFDs (0x014A) pointer array from IFD0 in the
// raw base buffer, builds a subIFDInfo for each referenced SubIFD, and
// enumerates their image blocks (strips/tiles).
//
// TIFF Extension §F / Adobe DNG Spec §4: SubIFDs (tag 0x014A) is typically a
// LONG array; each element is an absolute file offset pointing to an IFD that
// is a child of the current IFD. DNG's IFD0 typically points to one or more
// SubIFDs that carry the full-resolution image data. Real-world files also
// legitimately declare 0x014A as type IFD (13) or, in BigTIFF, LONG8 (16) /
// IFD8 (18) — see testdata/corpus/tiff/metadata-extractor/BigTIFFSubIFD4.tif
// and BigTIFFSubIFD8.tif.
//
// #270: this function re-scans base directly via findEntryInIFD/
// decodeOffsetArray rather than trusting ifd0.Get(exif.TagSubIFDs) (the
// already-parsed *exif.IFDEntry). exif.Parse follows CIPA DC-008-2023 and
// treats type code 13 as TypeUTF8 (1 byte/element) — correct for EXIF-registry
// tags, but wrong for 0x014A, where TIFF Extensions assign the SAME code 13
// to IFD (4 bytes/element). Relying on the exif-parsed Value for a type=13
// 0x014A entry would silently read a truncated 1-byte pointer instead of the
// true 4-byte offset. Re-reading raw bytes with format/tiff's own corrected
// type table (typeSize/typeSizeBigTIFF in tiff.go) sidesteps the collision
// entirely and, as a side effect, also handles LONG8/IFD8 correctly for
// BigTIFF sources.
//
// This function handles nested SubIFD-of-SubIFD chains up to maxSubIFDDepth
// levels deep (DNG spec only mandates one level; deeper nesting is a defensive
// extension). It silently skips SubIFD entries whose offsets are out of bounds
// or whose IFD cannot be parsed — a lenient-parse policy consistent with
// enumerateIFDBlocks.
//
// Returns:
//   - subIFDs: slice of *subIFDInfo in encounter order (outermost first);
//     each entry holds the source offset, parsed IFD, raw bytes, and new offset.
//   - blocks: image-data blocks from all SubIFDs; ifdPtr fields point to the
//     corresponding subIFDInfo.ifd.
//
// GM-W1: budget bounds the cumulative number of SubIFD entries and image
// blocks accepted across this call and every nested recursion level; see
// maxAggregateImageBlocks.
func enumerateSubIFDs(base []byte, e *exif.EXIF, order binary.ByteOrder, budget *imageBlockBudget) ([]*subIFDInfo, []*imageBlock, error) {
	if e == nil || e.IFD0 == nil {
		return nil, nil, nil
	}
	bigTIFF := e.BigTIFF
	ifd0Off, ok := readIFD0Offset(base, bigTIFF, order)
	if !ok {
		return nil, nil, nil
	}
	entry, found := findEntryInIFD(base, ifd0Off, uint16(exif.TagSubIFDs), bigTIFF, order)
	if !found || entry.count == 0 {
		return nil, nil, nil
	}

	// GM-W1 (CWE-770/CWE-405): reject an implausibly large Count BEFORE
	// decodeOffsetArray allocates a []uint64 proportional to it. This mirrors
	// enumerateSubIFDsAt's own per-entry cap and MUST be checked here too, now
	// that decoding happens in this function rather than after entering
	// enumerateSubIFDsAt (task #270 refactor) — see
	// TestEnumerateSubIFDsRejectsHugeCountFast for the regression proof.
	if entry.count > maxSubIFDsPerEntry {
		return nil, nil, fmt.Errorf("tiff: 0x014A SubIFDs: count=%d exceeds %d: %w",
			entry.count, maxSubIFDsPerEntry, ErrTooManyImageBlocks)
	}

	elemSz := elemSizeFor(entry.typ, bigTIFF)
	if elemSz == 0 {
		// Unknown type for 0x014A — skip gracefully.
		return nil, nil, nil
	}
	offsets, ok := decodeOffsetArray(base, entry.valField, entry.count, elemSz, bigTIFF, order)
	if !ok {
		return nil, nil, nil
	}

	return enumerateSubIFDsAt(base, offsets, order, bigTIFF, 0, maxSubIFDDepth, budget)
}

// enumerateSubIFDsAt recursively enumerates SubIFDs given their already-decoded
// absolute offsets. depth is the current nesting depth; maxDepth prevents
// unbounded recursion on crafted inputs.
//
// GM-W1 (CWE-770/CWE-405): len(offsets) is rejected outright once it exceeds
// maxSubIFDsPerEntry, and the accepted count is additionally charged against
// budget (maxAggregateImageBlocks), both BEFORE the visited-set map below is
// sized — a crafted file with an implausibly large 0x014A Count must not be
// able to drive proportional heap allocation, at any recursion depth.
func enumerateSubIFDsAt( //nolint:cyclop,gocyclo,funlen // SubIFD recursion, cycle detection, and BigTIFF-vs-classic child-IFD parsing dispatch require several branches; complexity is inherent to the algorithm
	base []byte,
	offsets []uint64,
	order binary.ByteOrder,
	bigTIFF bool,
	depth, maxDepth int,
	budget *imageBlockBudget,
) ([]*subIFDInfo, []*imageBlock, error) {
	if depth > maxDepth {
		return nil, nil, nil
	}
	n := len(offsets)
	if n > maxSubIFDsPerEntry {
		return nil, nil, fmt.Errorf("tiff: 0x014A SubIFDs: count=%d exceeds %d: %w",
			n, maxSubIFDsPerEntry, ErrTooManyImageBlocks)
	}
	if !budget.spend(n) {
		return nil, nil, fmt.Errorf("tiff: 0x014A SubIFDs: count=%d: %w", n, ErrTooManyImageBlocks)
	}

	var subIFDs []*subIFDInfo
	var allBlocks []*imageBlock

	// Belt-and-suspenders (task convention: see extractRawIFD): the map hint is
	// clamped independently of the per-entry cap above, so a future change to
	// that cap cannot silently reintroduce an oversized map pre-allocation. n
	// is already ≤ maxSubIFDsPerEntry at this point, so min() is a no-op on
	// the hot path.
	visited := make(map[uint64]bool, min(n, maxSubIFDsPerEntry))

	for _, off := range offsets {
		if off == 0 {
			continue
		}
		if visited[off] {
			continue // cycle guard
		}
		visited[off] = true

		// Parse the SubIFD at this offset.
		rawIFD := extractRawIFD(base, off, bigTIFF, order)
		if rawIFD == nil {
			// Out-of-bounds or unreadable; skip gracefully.
			continue
		}

		var parsedIFD *exif.IFD
		if bigTIFF {
			// #270: a SubIFD nested in a BigTIFF file inherits the file-wide
			// 20-byte-entry/8-byte-field format (BigTIFF spec §2). exif.ParseIFDAt
			// is classic-TIFF-only, so it cannot be used here.
			var pok bool
			parsedIFD, pok = parseIFDAtBigTIFF(base, off, order)
			if !pok {
				continue
			}
		} else {
			var pok bool
			parsedIFD, _, pok = exif.ParseIFDAt(base, uint32(off), order) //nolint:gosec // G115: classic path; off < 2^32 (bounded by maxFileSize, verified by extractRawIFD above)
			if !pok || parsedIFD == nil {
				continue
			}
		}

		// Clear ThumbnailData on the SubIFD before block enumeration.
		//
		// parseSingleIFD (via ParseIFDAt) sets IFD.ThumbnailData when it finds
		// both tag 0x0201 (JPEGInterchangeFormat) and 0x0202 in the same IFD,
		// because those tags normally indicate a JPEG thumbnail in IFD1.
		// For SubIFDs (tag 0x014A), 0x0201/0x0202 hold the JpgFromRaw offset
		// (Nikon NEF SubIFD[0]) or another JPEG image, NOT a thumbnail managed
		// by exif.Encode. parseIFDAtBigTIFF never sets ThumbnailData at all, so
		// this assignment is a defensive no-op on that path.
		//
		// enumerateIFDBlocks → appendJPEGBlock skips the block when ThumbnailData
		// is non-nil (assuming exif.Encode handles it).  That assumption is wrong
		// for SubIFDs: exif.Encode never sees SubIFD IFDs — they are raw bytes.
		// Clearing ThumbnailData forces appendJPEGBlock to enumerate the JPEG
		// as a relocatable imageBlock.  The ThumbnailData bytes themselves are
		// NOT needed for SubIFDs because the data lives in base[off:off+size]
		// and is copied verbatim in step 12.
		parsedIFD.ThumbnailData = nil

		si := &subIFDInfo{
			srcOffset: off,
			ifd:       parsedIFD,
			rawBytes:  rawIFD,
		}
		subIFDs = append(subIFDs, si)

		// Enumerate image blocks from this SubIFD.
		iblocks, err := enumerateIFDBlocks(base, parsedIFD, order, bigTIFF, budget)
		if err != nil {
			return nil, nil, fmt.Errorf("tiff: SubIFD at offset %d: %w", off, err)
		}
		// Re-point the ifdPtr to the parsedIFD so filterMainBlocks can identify them.
		for _, blk := range iblocks {
			blk.ifdPtr = parsedIFD
		}
		allBlocks = append(allBlocks, iblocks...)

		// Recurse into any nested SubIFDs (0x014A) within this SubIFD, using the
		// SAME raw-rescan approach (re-reading from base at this SubIFD's own
		// offset) rather than parsedIFD.Get(exif.TagSubIFDs), for identical
		// reasons to the outer-level lookup above.
		nestedEntry, nestedFound := findEntryInIFD(base, off, uint16(exif.TagSubIFDs), bigTIFF, order)
		// GM-W1 (CWE-770/CWE-405): reject an implausibly large nested Count
		// BEFORE decodeOffsetArray allocates a []uint64 proportional to it —
		// same rationale as the top-level check in enumerateSubIFDs.
		if nestedFound && nestedEntry.count > maxSubIFDsPerEntry {
			return nil, nil, fmt.Errorf("tiff: 0x014A SubIFDs: count=%d exceeds %d: %w",
				nestedEntry.count, maxSubIFDsPerEntry, ErrTooManyImageBlocks)
		}
		if !nestedFound || nestedEntry.count == 0 {
			continue
		}
		nestedElemSz := elemSizeFor(nestedEntry.typ, bigTIFF)
		if nestedElemSz == 0 {
			continue
		}
		nestedOffsets, nok := decodeOffsetArray(base, nestedEntry.valField, nestedEntry.count, nestedElemSz, bigTIFF, order)
		if !nok {
			continue
		}
		nestSubs, nestBlocks, nestErr := enumerateSubIFDsAt(
			base, nestedOffsets, order, bigTIFF, depth+1, maxDepth, budget,
		)
		if nestErr != nil {
			return nil, nil, nestErr
		}
		subIFDs = append(subIFDs, nestSubs...)
		allBlocks = append(allBlocks, nestBlocks...)
	}

	return subIFDs, allBlocks, nil
}

// extractRawIFD returns a byte slice containing the complete raw IFD block
// starting at offset off within base: count field + entries + next-IFD
// pointer + out-of-line value area, using classic (12-byte entry, uint32
// fields) or BigTIFF (20-byte entry, uint64 fields) widths per bigTIFF. The
// returned slice is a copy (independent of base), safe to mutate for offset
// patching.
//
// Returns nil if off is out of bounds or the IFD is malformed.
//
// Important: the value area includes the TileOffsets/StripOffsets array data
// (these are index arrays, NOT image pixel data). Image pixel data is excluded
// because it is referenced by the offset values stored in those arrays and is
// handled by the imageBlock relocation mechanism. Only the arrays themselves
// (the lists of offsets and byte counts) are included here, so that
// patchRawIFDOffsets can overwrite the array elements in-place.
func extractRawIFD(base []byte, off uint64, bigTIFF bool, order binary.ByteOrder) []byte { //nolint:cyclop // IFD scanning requires bounds-checking branches; complexity is inherent to raw binary parsing
	_, entryWidth, valFieldWidth := ifdWidths(bigTIFF)
	nextIFDWidth := valFieldWidth // next-IFD pointer is the same width as a value-or-offset field

	count, entriesStart, ok := ifdEntryTable(base, off, bigTIFF, order)
	if !ok {
		return nil
	}
	if count > bigTIFFMaxIFDEntries {
		count = bigTIFFMaxIFDEntries
	}
	entriesEnd := entriesStart + count*uint64(entryWidth) + uint64(nextIFDWidth) //nolint:gosec // G115: entryWidth/nextIFDWidth are compile-time constants (12/20, 4/8) from ifdWidths, never negative
	if entriesEnd > uint64(len(base)) {
		return nil
	}

	// First pass: compute the extent of all out-of-line value areas that need
	// to be included in rawBytes.
	//
	// We include the value arrays for ALL entries — including the
	// TileOffsets/StripOffsets/TileByteCounts/StripByteCounts arrays.
	// These arrays are metadata (index tables), not pixel data. We will
	// patch the element values in-place after assignNewOffsets.
	//
	// We DO NOT include the data that those arrays POINT TO — the actual
	// image pixels — because those are handled by the imageBlock mechanism.
	// In other words: include the "index" (the array of offsets), but not
	// the "image" (the bytes at those offsets).
	//
	// For JPEGInterchangeFormat the out-of-line value is the JPEG thumbnail
	// data itself, which we also exclude (imageBlock handles it). Only if a
	// JPEG offset tag's value array is OOL (which is impossible for scalar
	// entries: count=1, inline) would we need to think about this.
	valueAreaEnd := entriesEnd
	threshold := inlineThreshold(bigTIFF)
	for i := range count {
		entry, ok := readRawEntryAt(base, entriesStart+i*uint64(entryWidth), bigTIFF, order) //nolint:gosec // G115: entryWidth is a compile-time constant (12/20) from ifdWidths, never negative
		if !ok {
			break
		}
		sz := elemSizeFor(entry.typ, bigTIFF)
		if sz == 0 {
			continue
		}
		total := sz * entry.count
		if total <= threshold {
			continue // inline, no value area
		}
		valOff := fieldAsU64(entry.valField, bigTIFF, order)
		valEnd := valOff + total
		if valEnd > uint64(len(base)) {
			continue
		}
		if valEnd > valueAreaEnd {
			valueAreaEnd = valEnd
		}
	}

	// Compute total rawBytes size: valueAreaEnd already floors at entriesEnd
	// (the fixed count+entries+next-IFD block), capped at len(base).
	totalLen := min(valueAreaEnd, uint64(len(base)))

	raw := make([]byte, totalLen-off)
	copy(raw, base[off:totalLen])
	return raw
}

// computeSubIFDsSize returns the total byte size of all SubIFD raw blocks
// including any inter-block word-alignment padding, which is needed to compute
// where image blocks will land after the SubIFDs.
//
// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
// Each SubIFD block (an IFD structure) is a data item and must start at an
// even file offset. A 1-byte pad is counted before any block that would
// otherwise start at an odd offset.
func computeSubIFDsSize(subIFDs []*subIFDInfo) uint64 {
	var total uint64
	for _, si := range subIFDs {
		// Word-align before this SubIFD block.
		if total&1 == 1 {
			total++ // alignment pad byte
		}
		total += uint64(len(si.rawBytes))
	}
	return total
}

// assignSubIFDOffsets assigns new file offsets to each SubIFD, placing them
// contiguously starting at ifdEnd (just after the main EXIF block).
//
// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
// Each SubIFD block is a data item and must begin at an even file offset.
// A single 0x00 alignment pad byte is counted (and later inserted by the
// append loop in step 11) before any SubIFD that would otherwise start at an
// odd offset.
//
// The math.MaxUint32 saturation ceiling is kept identical for BigTIFF sources
// too — see assignNewOffsets' doc comment for the rationale (maxFileSize
// bounds every value this package computes well within uint32 in practice).
func assignSubIFDOffsets(subIFDs []*subIFDInfo, ifdEnd uint64) {
	cur := ifdEnd
	for _, si := range subIFDs {
		// Word-align: skip to even offset before placing this SubIFD block.
		if cur&1 == 1 {
			cur++ // account for the alignment pad byte inserted in step 11
		}
		si.newOffset = cur
		sz := uint64(len(si.rawBytes))
		if sz > math.MaxUint32-cur {
			si.newOffset = math.MaxUint32
		} else {
			cur += sz
		}
	}
}

// patchSubIFDImageOffsets patches the strip/tile offset entries within each
// SubIFD's rawBytes to point at the final image-block positions.
//
// Algorithm: for each subIFDInfo, scan the fixed IFD block in rawBytes.
// For each offset entry (0x0111/0x0144), find the corresponding imageBlocks
// and overwrite:
//
//	(1) For COUNT=1 inline entries: the 4-byte value-or-offset field.
//	(2) For COUNT>1 out-of-line entries: the array elements within rawBytes,
//	    AND the 4-byte value-or-offset field (the pointer to the array) is
//	    updated to reflect the new absolute position of the array in the output
//	    (= si.newOffset + relOff).
//
// This function must be called after assignNewOffsets and assignSubIFDOffsets
// have filled blk.newOffset and si.newOffset respectively.
func patchSubIFDImageOffsets(subIFDs []*subIFDInfo, blocks []*imageBlock, bigTIFF bool, order binary.ByteOrder) {
	if len(subIFDs) == 0 || len(blocks) == 0 {
		return
	}

	// Build a lookup: parsedIFD → list of blocks owned by that SubIFD.
	blocksByIFD := make(map[*exif.IFD][]*imageBlock, len(subIFDs))
	for _, blk := range blocks {
		blocksByIFD[blk.ifdPtr] = append(blocksByIFD[blk.ifdPtr], blk)
	}

	for _, si := range subIFDs {
		ifdBlocks := blocksByIFD[si.ifd]
		if len(ifdBlocks) == 0 {
			continue
		}
		patchRawIFDOffsets(si.rawBytes, ifdBlocks, si.srcOffset, si.newOffset, bigTIFF, order)
	}
}

// patchRawIFDOffsets patches all IFD entries in the raw SubIFD bytes that hold
// out-of-line value areas, updating their value-or-offset pointer fields to
// reflect the new absolute position of those value areas in the output.
//
// The raw SubIFD block (rawBytes) is a verbatim copy of the original file's IFD
// block starting at srcOff. Entry offset fields reference original file positions.
//
// newSubIFDOff is the absolute position at which rawBytes will be written in the
// output buffer. It is used to compute the new absolute file position of any
// out-of-line value areas that live within rawBytes.
//
// Two classes of OOL entries are handled:
//
// (1) Strip/tile image-data offset and bytecount tags (0x0111/0x0117/0x0144/0x0145):
//   - Total size > 4 (out-of-line array): update each array element with the
//     new image-block offset/size, AND update the entry's valOrOff field to
//     newSubIFDOff + relOff.
//   - Total size ≤ 4 (inline scalar): overwrite the valOrOff field directly.
//
// (2) ALL other OOL entries (RATIONAL, SRATIONAL, DOUBLE, long ASCII, etc.):
//   - Task #98 fix: value bytes ARE captured verbatim in rawBytes by extractRawIFD,
//     but the entry's valOrOff pointer still holds the OLD absolute file offset.
//     After rawBytes is appended at newSubIFDOff the pointer is stale, causing
//     readers to follow it into garbage (XResolution/YResolution → "undef").
//   - Fix: rewrite the valOrOff field to newSubIFDOff + relOff, where relOff is
//     (origFileOff − srcOff), the position of the value area within rawBytes.
//     The value bytes themselves are already correct — only the pointer changes.
//
// Spec references:
//   - TIFF 6.0 §2: classic IFD entry layout — tag(2)+type(2)+count(4)+valOrOff(4);
//     valOrOff holds the value inline when total byte size ≤ 4, otherwise it is
//     a file-absolute offset to the value area.
//   - BigTIFF spec §2: BigTIFF IFD entry layout — tag(2)+type(2)+count(8)+
//     valOrOff(8); inline threshold is 8 bytes. bigTIFF selects which layout
//     rawBytes uses (task #270) — a SubIFD nested inside a BigTIFF file
//     inherits the file-wide BigTIFF entry shape.
func patchRawIFDOffsets(rawBytes []byte, blocks []*imageBlock, srcOff, newSubIFDOff uint64, bigTIFF bool, order binary.ByteOrder) { //nolint:cyclop,gocyclo,funlen // patch loop requires multiple tag-specific branches; inline/OOL branching is inherent to the spec
	_, entryWidth, _ := ifdWidths(bigTIFF)
	count, pos, ok := ifdEntryTable(rawBytes, 0, bigTIFF, order)
	if !ok {
		return
	}

	// Group blocks by (offsetTag, index) for fast lookup.
	// Key: the OFFSET tag (not bytecount tag) so both the offset and bytecount
	// entries can look up the same block record.
	type blockKey struct {
		tag   exif.TagID
		index int
	}
	blkMap := make(map[blockKey]*imageBlock, len(blocks))
	for _, blk := range blocks {
		blkMap[blockKey{blk.entryTag, blk.index}] = blk
	}

	threshold := inlineThreshold(bigTIFF)
	for i := range count {
		entry, ok := readRawEntryAt(rawBytes, pos+i*uint64(entryWidth), bigTIFF, order) //nolint:gosec // G115: entryWidth is a compile-time constant (12/20) from ifdWidths, never negative
		if !ok {
			break
		}
		entryTag := exif.TagID(entry.tag)
		elemSz := elemSizeFor(entry.typ, bigTIFF)
		if elemSz == 0 || entry.count == 0 {
			continue
		}

		total := elemSz * entry.count
		if total <= threshold { //nolint:nestif // inline path for strip/tile scalar entries; branching is inherent to TIFF §2 inline/OOL duality
			// Inline value: no valOrOff pointer to update for non-image-data entries.
			// Strip/tile offset tags and JPEGInterchangeFormat with a single inline
			// value are handled below.
			//
			// Tag 0x0201 (JPEGInterchangeFormat) in a SubIFD (not IFD1) represents a
			// full-resolution JPEG image (e.g. Nikon NEF JpgFromRaw), not a thumbnail.
			isImageOffsetTag := entryTag == exif.TagStripOffsets || entryTag == exif.TagTileOffsets ||
				entryTag == exif.TagStripByteCounts || entryTag == exif.TagTileByteCounts ||
				entryTag == exif.TagJPEGInterchangeFormat || entryTag == exif.TagJPEGInterchangeFormatLength
			if !isImageOffsetTag {
				continue
			}

			// Single inline strip/tile/JPEG offset or bytecount — patch in-place.
			isByteCount := entryTag == exif.TagStripByteCounts || entryTag == exif.TagTileByteCounts ||
				entryTag == exif.TagJPEGInterchangeFormatLength
			offsetTag := entryTag
			if isByteCount {
				switch entryTag {
				case exif.TagStripByteCounts:
					offsetTag = exif.TagStripOffsets
				case exif.TagTileByteCounts:
					offsetTag = exif.TagTileOffsets
				case exif.TagJPEGInterchangeFormatLength:
					offsetTag = exif.TagJPEGInterchangeFormat
				}
			}
			blk := blkMap[blockKey{offsetTag, 0}]
			if blk == nil {
				continue
			}
			if isByteCount {
				putFieldU64(entry.valField, bigTIFF, order, blk.size)
			} else {
				putFieldU64(entry.valField, bigTIFF, order, blk.newOffset)
			}
			continue
		}

		// Out-of-line value: entry.valField is a file-absolute pointer to the
		// value area. The value bytes are already captured verbatim in rawBytes
		// by extractRawIFD. We must update the pointer to the new absolute position.
		origFileOff := fieldAsU64(entry.valField, bigTIFF, order)
		if origFileOff < srcOff {
			// Value area precedes the SubIFD start — not captured in rawBytes. Skip.
			continue
		}
		relOff := origFileOff - srcOff // relative offset of value area within rawBytes
		if relOff+total > uint64(len(rawBytes)) {
			continue // out of rawBytes bounds — skip
		}

		// Update the valOrOff pointer to the new absolute position of the value area.
		// Task #98 regression: RATIONAL/SRATIONAL/DOUBLE/long-ASCII/etc. entries
		// had their value bytes preserved verbatim in rawBytes but their valOrOff
		// pointers left pointing at the old file position → "undef" on read.
		putFieldU64(entry.valField, bigTIFF, order, newSubIFDOff+relOff)

		// For strip/tile image-data offset and bytecount tags, additionally update
		// each array element with the new image-block offset/size assigned by
		// assignNewOffsets.
		isImageOffsetTag := entryTag == exif.TagStripOffsets || entryTag == exif.TagTileOffsets ||
			entryTag == exif.TagStripByteCounts || entryTag == exif.TagTileByteCounts
		if !isImageOffsetTag {
			// Non-image-data OOL entry: valOrOff pointer already updated above.
			// The value bytes are verbatim-correct; no element-level patching needed.
			continue
		}

		isByteCount := entryTag == exif.TagStripByteCounts || entryTag == exif.TagTileByteCounts
		offsetTag := entryTag
		if isByteCount {
			switch entryTag {
			case exif.TagStripByteCounts:
				offsetTag = exif.TagStripOffsets
			case exif.TagTileByteCounts:
				offsetTag = exif.TagTileOffsets
			}
		}

		for j := range entry.count {
			blk := blkMap[blockKey{offsetTag, int(j)}]
			if blk == nil {
				continue
			}
			valPos := relOff + j*elemSz
			if valPos+elemSz > uint64(len(rawBytes)) {
				continue
			}
			v := blk.newOffset
			if isByteCount {
				v = blk.size
			}
			switch elemSz {
			case 8:
				order.PutUint64(rawBytes[valPos:], v)
			case 4:
				order.PutUint32(rawBytes[valPos:], uint32(v)) //nolint:gosec // G115: source entry declared 4-byte elements; value bounded by maxFileSize
			case 2:
				// TypeShort: new value must fit in uint16. The guard ensures the
				// value fits; a value that doesn't (implausible given maxFileSize)
				// is left unpatched rather than silently wrapping.
				if v <= 0xFFFF {
					order.PutUint16(rawBytes[valPos:], uint16(v))
				}
			}
		}
	}
}

// patchSubIFDPointers locates the 0x014A SubIFDs entry in the final TIFF
// output and overwrites the pointer array values with the new SubIFD offsets.
//
// exif.Encode preserves 0x014A's original declared Type/Count/Value verbatim
// (it is generic pass-through for tags it does not specially interpret).
// After re-encoding we must scan IFD0 in finalTIFF to find the 0x014A entry
// and patch its value bytes to reflect the new SubIFD positions, using
// whatever element width (4 bytes for LONG/IFD, 8 bytes for LONG8/IFD8) the
// entry actually declares — task #270; do not assume LONG.
//
// bigTIFF selects the header/entry/field widths used to scan finalTIFF
// (TIFF 6.0 §2 for classic; BigTIFF spec §2 for BigTIFF).
func patchSubIFDPointers(finalTIFF []byte, subIFDs []*subIFDInfo, bigTIFF bool, order binary.ByteOrder) error { //nolint:cyclop,gocyclo // linear IFD scan with inline/OOL branching; splitting further would reduce clarity
	ifd0Off, ok := readIFD0Offset(finalTIFF, bigTIFF, order)
	if !ok {
		return nil
	}
	entry, found := findEntryInIFD(finalTIFF, ifd0Off, uint16(exif.TagSubIFDs), bigTIFF, order)
	if !found {
		// 0x014A entry not found in IFD0 of the re-encoded output. This can
		// happen when exif.Encode drops entries it doesn't understand
		// (unknown types). In practice 0x014A is always a recognised numeric
		// type so it should be preserved. If it is absent, we cannot fix the
		// SubIFD pointers — return an error so the caller can detect the problem.
		return fmt.Errorf("%w", ErrSubIFDEntryNotFound)
	}

	declaredCount := int(entry.count) //nolint:gosec // G115: entry.count bounded by bigTIFFMaxIFDEntries/uint32 IFD-entry semantics in practice
	actualCount := len(subIFDs)

	// Task #116 regression: bound the write loop by min(declaredCount, actualCount) to
	// prevent either:
	//   (a) writing beyond the allocated pointer array in finalTIFF
	//       (declaredCount > actualCount would index subIFDs[j] OOB), or
	//   (b) writing beyond the allocated pointer array in finalTIFF
	//       when actualCount > declaredCount (the slot at j=declaredCount is
	//       not allocated in the re-encoded IFD and writing it corrupts the
	//       subsequent IFD or value area).
	//
	// Return ErrSubIFDCountMismatch when the counts differ so callers can
	// log or propagate the discrepancy rather than relying on silent patching.
	//
	// TIFF Extension §F: 0x014A holds exactly as many elements as there are
	// SubIFDs; exif.Encode preserves the original Count faithfully.
	patchCount := min(declaredCount, actualCount)
	var mismatchErr error
	if declaredCount != actualCount {
		mismatchErr = fmt.Errorf("%w (declared=%d actual=%d)", ErrSubIFDCountMismatch, declaredCount, actualCount)
	}

	elemSz := elemSizeFor(entry.typ, bigTIFF)
	if elemSz == 0 || patchCount == 0 {
		return mismatchErr
	}

	total := elemSz * uint64(patchCount) //nolint:gosec // G115: patchCount bounded by IFD entry count ceiling
	if total <= inlineThreshold(bigTIFF) && patchCount == 1 {
		// Single SubIFD: inline value in the value-or-offset field.
		putFieldU64(entry.valField, bigTIFF, order, subIFDs[0].newOffset)
	} else if patchCount > 0 {
		// Multiple SubIFDs: the value-or-offset field is an offset to the value array.
		arrOff := fieldAsU64(entry.valField, bigTIFF, order)
		if arrOff+uint64(patchCount)*elemSz > uint64(len(finalTIFF)) {
			return fmt.Errorf("%w (offset=%d, len=%d)", ErrSubIFDPointerArrayOOB,
				arrOff, len(finalTIFF))
		}
		for j := range patchCount {
			elemPos := arrOff + uint64(j)*elemSz
			switch elemSz {
			case 8:
				order.PutUint64(finalTIFF[elemPos:], subIFDs[j].newOffset) //nolint:gosec // G602: elemPos+8 <= len(finalTIFF) is guaranteed by the arrOff+patchCount*elemSz bounds check above (elemSz==8 in this case), for every j < patchCount
			default: // 4 (LONG/IFD); 0x014A never legitimately uses SHORT
				order.PutUint32(finalTIFF[elemPos:], uint32(subIFDs[j].newOffset)) //nolint:gosec // G115: newOffset bounded by maxFileSize, always < 2^32
			}
		}
	}
	// Return mismatch error after patching so that the partial write is visible
	// but the caller knows the SubIFD array was not fully consistent.
	return mismatchErr
}
