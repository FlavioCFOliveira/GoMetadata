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
	"slices"
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

// subIFDInfo describes one SubIFD carried by a relocation (see the SubIFD
// model comment above enumerateSubIFDs).
//
// srcOffset is the absolute byte position of the SubIFD's IFD block in the
// original buffer (as stored in a 0x014A value array). The SubIFD's image
// blocks (strips/tiles/JPEG) are accumulated in the same imageBlock pool as
// the main-IFD blocks; ifd, the SubIFD parsed from the source, is only used to
// enumerate those blocks.
//
// rawBytes is the SubIFD re-serialised by rawSubIFD: directory, zero next-IFD
// pointer, then every out-of-line value, with value-or-offset fields relative
// to the block start. It is appended to the output after the main
// exif.Encode skeleton and before the image blocks, at newOffset, and is
// completed in place by patchSubIFDImageOffsets once newOffset and the image
// block offsets are known.
type subIFDInfo struct {
	srcOffset uint64    // original SubIFD offset in base
	ifd       *exif.IFD // parsed SubIFD (for block enumeration)
	rawBytes  []byte    // re-serialised SubIFD block
	newOffset uint64    // filled in when SubIFD is appended to output
	// blocks holds this SubIFD's own image blocks in enumeration order; each
	// offset tag's blocks are contiguous and ordered by index.
	blocks []*imageBlock
	// children is this SubIFD's own 0x014A array, element by element (nil
	// for an element whose SubIFD is not carried).
	children []*subIFDInfo
	// next is the SubIFD the next-IFD pointer of this SubIFD links to, or nil.
	next *subIFDInfo
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
func relocateTIFF(base []byte, rawIPTC, rawXMP []byte) (header []byte, blocks []*imageBlock, err error) {
	e, err := exif.Parse(base)
	if err != nil {
		return nil, nil, fmt.Errorf("tiff: parse for relocation: %w", err)
	}
	// relocateTIFF's own callers (tiff.Inject) always supply the whole file as
	// base (see Inject's own doc comment), so fileLen == len(base) exactly.
	return relocateTIFFFromParsed(base, e, rawIPTC, rawXMP, uint64(len(base)))
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
// relocateTIFFFromParsed returns the relocated output split in two parts:
//
//   - header: the encoded IFD structure plus every SubIFD's raw bytes (word
//     alignment padding included) — everything up to, but excluding, the
//     image-data blocks (step 12 in the algorithm doc comment above). This is
//     always small (bounded by metadata size), regardless of source file size.
//   - blocks: every image-data block (strips, tiles, JPEG-in-TIFF), each
//     already carrying its final newOffset (assigned by assignNewOffsets).
//     blocks[i].newOffset == len(header) + sum(blocks[j].size for j < i): the
//     blocks, concatenated in slice order and appended immediately after
//     header, reproduce EXACTLY the single-buffer result this function used
//     to return directly (task #291).
//
// Callers stream each block's bytes from the ORIGINAL source (base, when it
// already holds the whole file, or the caller's own io.ReadSeeker otherwise)
// to the output in blocks' slice order — see writeRelocated in
// relocate_stream.go. base itself only needs to cover the metadata region
// (every IFD, SubIFD structure, and out-of-line offset/bytecount ARRAY this
// function reads) — never the image pixel bytes those arrays point to; see
// extractRawIFD's own doc comment for the identical "index, not image"
// distinction applied to SubIFD raw bytes.
func relocateTIFFFromParsed(base []byte, e *exif.EXIF, rawIPTC, rawXMP []byte, fileLen uint64) (header []byte, blocks []*imageBlock, err error) { //nolint:cyclop,gocyclo,funlen // complex by necessity: TIFF structural rewriting requires handling all image-block patterns in one function
	if e == nil {
		var err error
		e, err = exif.Parse(base)
		if err != nil {
			return nil, nil, fmt.Errorf("tiff: parse for relocation: %w", err)
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
		upsertIPTC(e.IFD0, rawIPTC, rawXMP)
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
	blocks, enumerateErr := enumerateImageBlocks(e, order, bigTIFF, budget, fileLen)
	if enumerateErr != nil {
		return nil, nil, fmt.Errorf("tiff: enumerate image blocks: %w", enumerateErr)
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
	subTree, subBlocks, subErr := enumerateSubIFDs(base, e, order, budget, fileLen)
	if subErr != nil {
		return nil, nil, fmt.Errorf("tiff: enumerate SubIFDs: %w", subErr)
	}
	subIFDs := subTree.all
	blocks = append(blocks, subBlocks...)

	// Short-circuit when there are no image blocks to relocate and no SubIFDs.
	if len(blocks) == 0 && len(subIFDs) == 0 && subTree.roots == nil {
		out, encErr := exif.Encode(e)
		if encErr != nil {
			return nil, nil, fmt.Errorf("tiff: encode (no image blocks): %w", encErr)
		}
		// Still apply MakerNote OOL rebasing even on the short-circuit path.
		// exif.Encode may have moved the MakerNote blob to a different absolute
		// offset within the re-encoded IFD section.
		// #127: MakerNote OOL offset rebasing incomplete on write.
		rebaseGenericMakerNote(out, e, order)
		return out, nil, nil
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

	ifdEndInt, skelErr := exif.EncodedSize(e)
	if skelErr != nil {
		return nil, nil, fmt.Errorf("tiff: encode placeholder: %w", skelErr)
	}
	ifdEnd := uint64(ifdEndInt) //nolint:gosec // G115: EncodedSize never returns a negative length

	// Step 7: assign new absolute offsets.
	// SubIFD blocks are placed first (SubIFD structures after the main EXIF
	// block), then main image blocks follow after all SubIFD structures.
	subIFDsSize := computeSubIFDsSize(ifdEnd, subIFDs)
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
		return nil, nil, fmt.Errorf("tiff: ifdEnd=%d subIFDsSize=%d: %w", ifdEnd, subIFDsSize, ErrOffsetOverflow)
	}
	assignNewOffsets(blocks, imageStart)
	assignSubIFDOffsets(subIFDs, ifdEnd)

	// Step 8a: update placeholder value bytes (main-IFD blocks).
	updatePlaceholders(mainBlocks, offsetValueSlices, order)

	// Step 8b: patch SubIFD raw bytes — update strip/tile offset entries in
	// each SubIFD's rawBytes to point at the newly assigned image-block offsets.
	patchSubIFDImageOffsets(subIFDs, bigTIFF, order)

	// Step 9: encode → finalTIFF. Same IFD layout as step 6. The buffer is
	// allocated once with the exact HEADER length (IFD structure + SubIFD
	// blocks — task #291 no longer includes image blocks here, see below),
	// so step 11 never regrows it.
	//
	// #291: finalTIFF's capacity now only needs to cover the header (metadata)
	// region, not image blocks — those are streamed by the caller (see this
	// function's own doc comment and writeRelocated in relocate_stream.go).
	// This is the core of #291's memory-efficiency win: a real camera file's
	// header is a tiny fraction of its size, so this allocation no longer
	// scales with file size at all.
	//
	// #285: cr2TrailingReserve extra bytes of SPARE CAPACITY (not length) are
	// reserved here unconditionally. This function is also the relocation
	// core for CR2 (via InjectWithEXIFCR2 → this function →
	// insertCR2MarkerAndShiftOffsets): that final step needs to shift every
	// byte from offset 8 onward right by cr2MarkerLen to make room for the
	// proprietary CR2 marker, and does so with an in-place, overlap-safe
	// copy() within finalTIFF's own backing array when spare capacity allows
	// it — avoiding a second allocation. The extra bytes are silently unused
	// capacity (not wasted allocation-wise: `make`'s size class rounding
	// usually absorbs a few extra bytes anyway) for the non-CR2 (TIFF/DNG)
	// callers of this function.
	headerLen := relocatedLen(ifdEnd, subIFDs)
	finalTIFF, finalErr := exif.EncodeInto(make([]byte, 0, finalCap(headerLen)+cr2MarkerLen), e)
	if finalErr != nil {
		return nil, nil, fmt.Errorf("tiff: encode final: %w", finalErr)
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
	if subTree.roots != nil {
		if err := patchSubIFDPointers(finalTIFF, subTree, bigTIFF, order); err != nil {
			return nil, nil, fmt.Errorf("tiff: patch SubIFD pointers: %w", err)
		}
	}

	// Step 11: append each SubIFD's raw bytes (with already-patched image
	// offsets from step 8b). finalTIFF is now the complete HEADER: everything
	// up to, but excluding, the image-data blocks.
	// TIFF 6.0 §2: each SubIFD block must start at a word (even) boundary.
	// assignSubIFDOffsets already reserved space for the 0x00 pad byte;
	// insert it here to keep finalTIFF and the assigned offsets in sync.
	for _, si := range subIFDs {
		if len(finalTIFF)&1 == 1 {
			finalTIFF = append(finalTIFF, 0x00)
		}
		finalTIFF = append(finalTIFF, si.rawBytes...)
	}

	// #291: step 12 (append each image block's bytes) is no longer performed
	// here — blocks is returned to the caller, which streams each block's
	// bytes from the original source (see this function's own doc comment).
	if uint64(len(finalTIFF)) != headerLen {
		return nil, nil, fmt.Errorf("tiff: relocated header length %d, computed %d: %w", len(finalTIFF), headerLen, errRelocateLayout)
	}
	return finalTIFF, blocks, nil
}

// errRelocateLayout reports an internal inconsistency between the computed
// and the produced relocate output length. It is never expected to occur.
var errRelocateLayout = errors.New("tiff: relocate layout mismatch")

// relocatedLen returns the exact length of the relocate output's HEADER: the
// encoded IFD structure (ifdEnd bytes), then each SubIFD block preceded by a
// 0x00 pad when the running length is odd (TIFF 6.0 §2 word alignment, as in
// step 11). #291: image blocks (the former step 12) are no longer part of
// this function's result — they are streamed separately by the caller (see
// writeRelocated in relocate_stream.go), so their sizes are never included.
func relocatedLen(ifdEnd uint64, subIFDs []*subIFDInfo) uint64 {
	n := ifdEnd
	for _, si := range subIFDs {
		if n&1 == 1 {
			n++
		}
		n += uint64(len(si.rawBytes))
	}
	return n
}

// maxPresizedOutput bounds the capacity finalCap pre-allocates. A classic TIFF
// output cannot address more than 4 GiB (TIFF 6.0 §2: 32-bit offsets); a
// larger computed length comes from duplicated block references and is left
// to grow on demand rather than allocated up front.
const maxPresizedOutput = math.MaxUint32

// finalCap returns the capacity to pre-allocate for a relocate output of
// finalLen bytes: finalLen itself, or 0 above maxPresizedOutput.
func finalCap(finalLen uint64) uint64 {
	if finalLen > maxPresizedOutput {
		return 0
	}
	return finalLen
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
	out := make([]*imageBlock, 0, len(blocks))
	if len(subIFDs) <= smallSetMax {
		// Small set: a linear scan is cheaper than building a map.
		for _, blk := range blocks {
			if !ownedBySubIFD(blk.ifdPtr, subIFDs) {
				out = append(out, blk)
			}
		}
		return out
	}
	subIFDSet := make(map[*exif.IFD]struct{}, len(subIFDs))
	for _, si := range subIFDs {
		if si.ifd != nil {
			subIFDSet[si.ifd] = struct{}{}
		}
	}
	for _, blk := range blocks {
		if _, isSubIFD := subIFDSet[blk.ifdPtr]; !isSubIFD {
			out = append(out, blk)
		}
	}
	return out
}

// smallSetMax is the set size up to which linear scans replace maps in the
// relocate helpers.
const smallSetMax = 16

// ownedBySubIFD reports whether ifd is the parsed IFD of one of subIFDs.
func ownedBySubIFD(ifd *exif.IFD, subIFDs []*subIFDInfo) bool {
	for _, si := range subIFDs {
		if si.ifd != nil && si.ifd == ifd {
			return true
		}
	}
	return false
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
//
// #291: fileLen is the TRUE total length of the original source file — NOT
// necessarily the length of the metadata PREFIX buffer the caller parsed e
// from (#289/#293). Every declared block's offset+size is bounds-checked
// against fileLen (a legitimate block routinely extends far beyond a prefix
// — that is the entire point of the prefix's existence). This function reads
// only from e's already-parsed *exif.IFD entries, never from a raw byte
// buffer, so no such buffer is a parameter here.
func enumerateImageBlocks(e *exif.EXIF, order binary.ByteOrder, bigTIFF bool, budget *imageBlockBudget, fileLen uint64) ([]*imageBlock, error) {
	var blocks []*imageBlock
	for ifd := e.IFD0; ifd != nil; ifd = ifd.Next {
		iblocks, err := enumerateIFDBlocks(ifd, order, bigTIFF, budget, fileLen)
		if err != nil {
			return nil, err
		}
		blocks = appendBlocks(blocks, iblocks)
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
func enumerateIFDBlocks(ifd *exif.IFD, order binary.ByteOrder, bigTIFF bool, budget *imageBlockBudget, fileLen uint64) ([]*imageBlock, error) { //nolint:cyclop // handles all three tag-pair patterns in a single linear scan; splitting would hurt readability
	var blocks []*imageBlock

	stripOff := ifd.Get(exif.TagStripOffsets)
	stripLen := ifd.Get(exif.TagStripByteCounts)
	if stripOff != nil && stripLen != nil {
		sb, err := extractParallelOffsetBlocks(ifd, exif.TagStripOffsets, stripOff, stripLen, order, bigTIFF, budget, fileLen)
		if err != nil {
			return nil, err
		}
		blocks = sb
	}

	tileOff := ifd.Get(exif.TagTileOffsets)
	tileLen := ifd.Get(exif.TagTileByteCounts)
	if tileOff != nil && tileLen != nil {
		tb, err := extractParallelOffsetBlocks(ifd, exif.TagTileOffsets, tileOff, tileLen, order, bigTIFF, budget, fileLen)
		if err != nil {
			return nil, err
		}
		blocks = appendBlocks(blocks, tb)
	}

	// JPEGInterchangeFormat: skip when ThumbnailData is non-nil (exif.Encode handles it).
	if ifd.ThumbnailData == nil {
		blocks = appendJPEGBlock(fileLen, ifd, blocks, bigTIFF, order)
	}

	return blocks, nil
}

// appendBlocks returns dst followed by src. When dst is empty src is returned
// as is, so the common single-source case copies nothing. The pointer slices
// may be shared this way because callers only append to them; the imageBlock
// values they point to are never moved.
func appendBlocks(dst, src []*imageBlock) []*imageBlock {
	if len(dst) == 0 {
		return src
	}
	if len(src) == 0 {
		return dst
	}
	// Full-slice expression: never write into spare capacity dst may share.
	return append(slices.Clip(dst), src...)
}

// appendJPEGBlock appends a JPEGInterchangeFormat block to blocks if present.
// Extracted to reduce nestif complexity in enumerateIFDBlocks.
// fileLen is the TRUE total length of the source file (#291: NOT necessarily
// len(base) — see enumerateImageBlocks's own doc comment); it is used to skip
// entries whose indicated offset+size would fall outside the real file.
func appendJPEGBlock(fileLen uint64, ifd *exif.IFD, blocks []*imageBlock, bigTIFF bool, order binary.ByteOrder) []*imageBlock { //nolint:gocyclo // width-aware (task #270) bounds-checking on two scalar fields; splitting further would hurt readability
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
	// Skip entries whose indicated range falls outside the real source file.
	// fits (extent.go) avoids the off+size overflow a raw addition risks for a
	// crafted BigTIFF LONG8 pair (e.g. off=MaxUint64-1, size=10 wraps to 8,
	// which then compares as "in bounds" against any fileLen).
	if !fits(off, size, fileLen) {
		return blocks
	}
	// Full-slice expression: blocks may alias a slice returned by
	// extractParallelOffsetBlocks; never write into its spare capacity.
	return append(slices.Clip(blocks), &imageBlock{
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
	ifd *exif.IFD,
	offsetTag exif.TagID,
	offsetEntry, countEntry *exif.IFDEntry,
	order binary.ByteOrder,
	bigTIFF bool,
	budget *imageBlockBudget,
	fileLen uint64,
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

	// One backing array for all n blocks instead of n separate allocations.
	// arr is fully sized here and never appended to, so &arr[i] stays valid
	// for the lifetime of the relocate call (insertPlaceholders and
	// patchSubIFDImageOffsets rely on pointer identity).
	arr := make([]imageBlock, n)
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

		blk := &arr[i]
		*blk = imageBlock{
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
		// #291: checked against fileLen (the TRUE file length), not len(base)
		// — base may be only a metadata prefix, and a legitimate block
		// routinely extends far beyond it (see enumerateImageBlocks's doc
		// comment). fits (extent.go), not a raw `off+size > fileLen`
		// comparison: a crafted BigTIFF LONG8 off/size pair (e.g.
		// off=MaxUint64-1, size=10) wraps a raw addition to 8, which then
		// incorrectly compares as in-bounds against any fileLen — the
		// resulting out-of-range imageBlock reaches writeRelocated's
		// wholeFile==true path (relocate_stream.go) and panics on
		// `prefix[blk.srcOffset:end]` (security audit finding, 2026-09-26).
		if !fits(off, size, fileLen) {
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
	// Distinct (ifd, offset tag) groups, in first-seen order. Blocks of one
	// group are contiguous, so the linear lookup runs only when the group
	// changes. Removal is per (ifd, tag) and order-independent.
	var groups []groupKey
	for _, blk := range blocks {
		k := groupKey{blk.ifdPtr, blk.entryTag}
		if n := len(groups); n > 0 && groups[n-1] == k {
			continue
		}
		if !slices.Contains(groups, k) {
			groups = append(groups, k)
		}
	}
	for _, k := range groups {
		removeEntryFromIFD(k.ifd, k.tag)
		if countTag := bytecountTagFor(k.tag); countTag != 0 {
			removeEntryFromIFD(k.ifd, countTag)
		}
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

// placeholderGroup holds the placeholder value bytes of one (IFD, offset
// tag) group: off backs the offset entry's Value and cnt the bytecount
// entry's Value. Both are sub-slices of one allocation.
type placeholderGroup struct {
	key      groupKey
	n        int   // number of blocks (elements) in the group
	offW     uint8 // offset element width in bytes
	cntW     uint8 // bytecount element width in bytes
	off, cnt []byte
}

// insertPlaceholders inserts image-data offset and bytecount entries with
// zeroed value bytes into each owning IFD. The placeholder Count = N
// (number of elements) and Type match the SOURCE entry's declared width
// (widthToType), so that exif.Encode accounts for the exact final
// value-area space and preserves the source's SHORT/LONG/LONG8 type choice.
//
// Returns one placeholderGroup per group in first-seen order.
// updatePlaceholders writes the real values into the groups' value slices
// in-place; since IFDEntry.Value is the same backing array, the updated
// values are visible to exif.Encode without re-insertion.
func insertPlaceholders(blocks []*imageBlock) []placeholderGroup {
	// Collect unique groups in stable insertion order. The first block of a
	// group supplies its widths: all blocks of a group come from the same
	// source entry.
	var groups []placeholderGroup
	for _, blk := range blocks {
		if g := findGroup(groups, groupKey{blk.ifdPtr, blk.entryTag}); g != nil {
			g.n++
			continue
		}
		groups = append(groups, placeholderGroup{
			key:  groupKey{blk.ifdPtr, blk.entryTag},
			n:    1,
			offW: blk.offElemSz,
			cntW: blk.cntElemSz,
		})
	}

	for i := range groups {
		g := &groups[i]
		offLen := g.n * int(g.offW)
		vals := make([]byte, offLen+g.n*int(g.cntW))
		// Full-slice expressions keep the two values from overlapping even if
		// a later append were applied to one of them.
		g.off = vals[:offLen:offLen]
		g.cnt = vals[offLen:]

		// TIFF 6.0 §2 / BigTIFF spec §2: Count = number of values (not bytes).
		upsertIFDEntryWithCount(g.key.ifd, g.key.tag, widthToType(g.offW), uint32(g.n), g.off) //nolint:gosec // G115: n bounded by strip/tile count, < 2^32
		if countTag := bytecountTagFor(g.key.tag); countTag != 0 {
			upsertIFDEntryWithCount(g.key.ifd, countTag, widthToType(g.cntW), uint32(g.n), g.cnt) //nolint:gosec // G115: same
		}
	}
	return groups
}

// findGroup returns the group with key k, or nil. Blocks of one group are
// contiguous, so the last group is checked first.
func findGroup(groups []placeholderGroup, k groupKey) *placeholderGroup {
	if n := len(groups); n > 0 && groups[n-1].key == k {
		return &groups[n-1]
	}
	for i := range groups {
		if groups[i].key == k {
			return &groups[i]
		}
	}
	return nil
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
func updatePlaceholders(blocks []*imageBlock, groups []placeholderGroup, order binary.ByteOrder) {
	for _, blk := range blocks {
		g := findGroup(groups, groupKey{blk.ifdPtr, blk.entryTag})
		if g == nil {
			continue
		}
		putWidth(g.off[blk.index*int(blk.offElemSz):], blk.offElemSz, order, blk.newOffset)
		putWidth(g.cnt[blk.index*int(blk.cntElemSz):], blk.cntElemSz, order, blk.size)
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
// SubIFD support (tasks #94, #311)
// ---------------------------------------------------------------------------
//
// Model (task #311). A SubIFDs entry (tag 0x014A) may sit in ANY IFD of the
// main chain (IFD0, IFD1, ... — e.g. one reduced-resolution SubIFD per page),
// may list several SubIFDs, and a SubIFD may carry its own 0x014A (TIFF
// Technical Note 1; Adobe DNG Specification 1.7.1.0 §"SubIFD Trees"). The
// relocator therefore carries the whole tree:
//
//   - subIFDTree.roots[k] lists, element by element, the SubIFDs of the 0x014A
//     array of the k-th IFD of the main chain; subIFDInfo.children does the
//     same for a SubIFD's own array. Every array is re-written with exactly
//     its declared number of elements: an element whose SubIFD could not be
//     read is written as 0, never as a stale offset.
//   - A SubIFD may also link further SubIFDs through its next-IFD pointer (a
//     SubIFD chain: libtiff reads the directories that follow a SubIFD this
//     way, and ExifTool lists them as SubIFD1, SubIFD2, ...; corpus
//     f15fca5b: one 0x014A element, five chained SubIFDs). The chain is
//     carried and subIFDInfo.next is written as the next-IFD pointer.
//   - subIFDTree.all lists every carried SubIFD once (depth first, in array
//     and chain order); a SubIFD referenced twice is carried once and both
//     pointers address it.
//   - Each SubIFD is re-serialised compactly (rawSubIFD): its directory, then
//     every out-of-line value copied behind it, wherever the value was in the
//     source (libtiff writes values before the directory). Pointers into that
//     value area are stored relative to the SubIFD and rebased once the
//     SubIFD's output offset is known (patchSubIFDImageOffsets).
//
// The 0x014A arrays of the main chain are written by exif.Encode with the
// values of the parsed EXIF and patched in the encoded stream
// (patchSubIFDPointers). Reference: libtiff tif_dirwrite.c
// (TIFFWriteDirectoryTagSubifd writes the array with one slot per SubIFD and
// fills each slot when that SubIFD is written).

// subIFDTree is the result of enumerateSubIFDs: every SubIFD reachable from
// the main IFD chain.
type subIFDTree struct {
	// all lists each carried SubIFD once, in output placement order.
	all []*subIFDInfo
	// roots[k] is the element-aligned 0x014A array of the k-th IFD of the
	// main chain (IFD0 is 0); nil when that IFD has no SubIFDs entry. A nil
	// element is a SubIFD that could not be read.
	roots [][]*subIFDInfo
}

// enumerateSubIFDs parses the SubIFDs (0x014A) arrays of every IFD of the
// main chain in the raw base buffer, builds a subIFDInfo for each referenced
// SubIFD (recursively, up to maxSubIFDDepth levels), and enumerates their
// image blocks (strips/tiles/JPEG).
//
// TIFF Extension §F / Adobe DNG Spec §4: SubIFDs (tag 0x014A) is typically a
// LONG array; each element is an absolute file offset pointing to an IFD that
// is a child of the current IFD. Real-world files also legitimately declare
// 0x014A as type IFD (13) or, in BigTIFF, LONG8 (16) / IFD8 (18) — see
// testdata/corpus/tiff/metadata-extractor/BigTIFFSubIFD4.tif and
// BigTIFFSubIFD8.tif.
//
// #270: the arrays are re-read from base with format/tiff's own type table
// (typeSize/typeSizeBigTIFF in tiff.go, which reads type 13 as the 4-byte IFD
// type) rather than taken from the parsed *exif.EXIF, so a damaged or
// implausible entry is judged on the bytes of the file. The k-th IFD of the
// raw chain is matched with the k-th IFD of e's chain, which exif.Encode
// writes in the same order; the raw walk stops at the length of e's chain.
//
// A SubIFD entry whose offset is zero, out of bounds, or unparsable is not
// carried (lenient-parse policy, consistent with enumerateIFDBlocks); its
// array element is written as 0.
//
// GM-W1: budget bounds the cumulative number of SubIFD entries and image
// blocks accepted across every array and recursion level; see
// maxAggregateImageBlocks. An array whose Count exceeds maxSubIFDsPerEntry is
// rejected with ErrTooManyImageBlocks before anything proportional to the
// Count is allocated.
func enumerateSubIFDs(base []byte, e *exif.EXIF, order binary.ByteOrder, budget *imageBlockBudget, fileLen uint64) (subIFDTree, []*imageBlock, error) {
	if e == nil || e.IFD0 == nil {
		return subIFDTree{}, nil, nil
	}
	// The walker holds a copy of the budget, written back below: storing the
	// caller's pointer in the walker made it escape to the heap (one
	// allocation per relocation), because the content of the walker leaks
	// through its recursive methods.
	w := subIFDWalker{base: base, order: order, bigTIFF: e.BigTIFF, budget: budgetValue(budget), fileLen: fileLen}
	off, ok := readIFD0Offset(base, w.bigTIFF, order)
	if !ok {
		return subIFDTree{}, nil, nil
	}
	chainLen := 0
	for ifd := e.IFD0; ifd != nil && chainLen < maxSubIFDChain; ifd = ifd.Next {
		chainLen++
	}
	roots, err := w.mainChain(off, chainLen)
	if budget != nil {
		*budget = w.budget
	}
	if err != nil {
		return subIFDTree{}, nil, err
	}
	if len(w.all) == 0 && roots == nil {
		return subIFDTree{}, nil, nil
	}
	return subIFDTree{all: w.all, roots: roots}, w.blocks, nil
}

// mainChain walks the first chainLen IFDs of the main chain of w.base, from
// the IFD at off, and returns the SubIFD array of each (subIFDTree.roots), or
// nil when none has one. A cyclic chain ends the walk.
func (w *subIFDWalker) mainChain(off uint64, chainLen int) ([][]*subIFDInfo, error) {
	var roots [][]*subIFDInfo
	var seenBuf [8]uint64
	seen := seenBuf[:0]
	ok := true
	for k := 0; ok && k < chainLen && off != 0 && !slices.Contains(seen, off); k++ {
		seen = append(seen, off)
		kids, err := w.array(off, 0)
		if err != nil {
			return nil, err
		}
		if kids != nil {
			if roots == nil {
				roots = make([][]*subIFDInfo, chainLen)
			}
			roots[k] = kids
		}
		off, ok = rawNextIFD(w.base, off, w.bigTIFF, w.order)
	}
	return roots, nil
}

// budgetValue returns *b, or an unbounded budget when b is nil (spend treats
// a nil budget as unbounded).
func budgetValue(b *imageBlockBudget) imageBlockBudget {
	if b == nil {
		return imageBlockBudget{remaining: math.MaxInt}
	}
	return *b
}

// maxSubIFDChain bounds the main-chain IFDs whose SubIFDs enumerateSubIFDs
// visits. exif.Parse follows at most 512 chained IFDs (maxTraverseChainIFDs);
// real files chain a handful (a CR2 chains four).
const maxSubIFDChain = 512

// subIFDWalker carries the state of one enumerateSubIFDs call.
type subIFDWalker struct {
	base    []byte
	order   binary.ByteOrder
	bigTIFF bool
	budget  imageBlockBudget // copy of the caller's budget; see enumerateSubIFDs
	fileLen uint64
	all     []*subIFDInfo
	byOff   map[uint64]*subIFDInfo // index of all once it outgrows smallSetMax
	blocks  []*imageBlock
}

// array decodes the 0x014A entry of the IFD at ifdOff in w.base and returns
// its SubIFDs, element-aligned (nil elements for SubIFDs that cannot be
// read), or nil when the IFD has no usable SubIFDs entry.
func (w *subIFDWalker) array(ifdOff uint64, depth int) ([]*subIFDInfo, error) {
	entry, found := findEntryInIFD(w.base, ifdOff, uint16(exif.TagSubIFDs), w.bigTIFF, w.order)
	if !found || entry.count == 0 {
		return nil, nil
	}
	// GM-W1 (CWE-770/CWE-405): reject an implausibly large Count BEFORE
	// decodeOffsetArray allocates a []uint64 proportional to it (see
	// TestEnumerateSubIFDsRejectsHugeCountFast).
	if entry.count > maxSubIFDsPerEntry {
		return nil, fmt.Errorf("tiff: 0x014A SubIFDs: count=%d exceeds %d: %w",
			entry.count, maxSubIFDsPerEntry, ErrTooManyImageBlocks)
	}
	elemSz := elemSizeFor(entry.typ, w.bigTIFF)
	if elemSz == 0 {
		return nil, nil // unknown type for 0x014A: skip gracefully
	}
	// The elements are read in place (offsetArrayBytes): decoding them into a
	// []uint64 first would cost one allocation per array.
	src, ok := offsetArrayBytes(w.base, entry.valField, entry.count, elemSz, w.bigTIFF, w.order)
	if !ok {
		return nil, nil
	}
	n := int(entry.count)
	if !w.budget.spend(n) {
		return nil, fmt.Errorf("tiff: 0x014A SubIFDs: count=%d: %w", n, ErrTooManyImageBlocks)
	}
	kids := make([]*subIFDInfo, n)
	for i := range kids {
		si, _, err := w.chain(elemAt(src, uint64(i), elemSz, w.order), depth)
		if err != nil {
			return nil, err
		}
		kids[i] = si
	}
	return kids, nil
}

// chain carries the SubIFD at off and the SubIFDs its next-IFD pointers link
// (see the SubIFD model above), and returns the first one; ok is false when it
// cannot be read. The walk is iterative; it stops at a zero or unreadable
// pointer, at a SubIFD already carried (the link then addresses it, so a cycle
// ends there), or after maxSubIFDChain links.
func (w *subIFDWalker) chain(off uint64, depth int) (head *subIFDInfo, ok bool, err error) {
	if seen := w.lookup(off); seen != nil {
		return seen, true, nil
	}
	if head, ok, err = w.visit(off, depth); !ok || err != nil {
		return nil, false, err
	}
	prev := head
	for range maxSubIFDChain {
		next, rok := rawNextIFD(w.base, prev.srcOffset, w.bigTIFF, w.order)
		if !rok || next == 0 {
			break
		}
		if seen := w.lookup(next); seen != nil {
			prev.next = seen
			break
		}
		cur, vok, verr := w.visit(next, depth)
		if verr != nil {
			return nil, false, verr
		}
		if !vok {
			break
		}
		prev.next, prev = cur, cur
	}
	return head, true, nil
}

// lookup returns the subIFDInfo already built for the SubIFD at off, or nil.
func (w *subIFDWalker) lookup(off uint64) *subIFDInfo {
	if w.byOff != nil {
		return w.byOff[off]
	}
	for _, si := range w.all {
		if si.srcOffset == off {
			return si
		}
	}
	return nil
}

// record adds si to w.all and to the offset index.
func (w *subIFDWalker) record(si *subIFDInfo) {
	w.all = append(w.all, si)
	switch {
	case w.byOff != nil:
		w.byOff[si.srcOffset] = si
	case len(w.all) > smallSetMax:
		w.byOff = make(map[uint64]*subIFDInfo, 2*len(w.all))
		for _, s := range w.all {
			w.byOff[s.srcOffset] = s
		}
	}
}

// visit returns the subIFDInfo of the SubIFD at off, building it (and its
// own SubIFDs, but not the SubIFDs its next-IFD pointer links: see chain) on
// first sight; ok is false when off is zero, too deep, or unreadable.
// A SubIFD is recorded before its children are visited, so a cyclic
// reference resolves to the existing record instead of recursing.
func (w *subIFDWalker) visit(off uint64, depth int) (si *subIFDInfo, ok bool, err error) {
	if off == 0 || depth > maxSubIFDDepth {
		return nil, false, nil
	}
	if found := w.lookup(off); found != nil {
		return found, true, nil
	}
	raw := rawSubIFD(w.base, off, w.bigTIFF, w.order)
	if raw == nil {
		return nil, false, nil // out of bounds or unreadable: skip gracefully
	}
	parsedIFD, ok := w.parseIFD(off)
	if !ok {
		return nil, false, nil
	}
	si = &subIFDInfo{srcOffset: off, ifd: parsedIFD, rawBytes: raw}
	w.record(si)

	iblocks, err := enumerateIFDBlocks(parsedIFD, w.order, w.bigTIFF, &w.budget, w.fileLen)
	if err != nil {
		return nil, false, fmt.Errorf("tiff: SubIFD at offset %d: %w", off, err)
	}
	for _, blk := range iblocks {
		blk.ifdPtr = parsedIFD // filterMainBlocks identifies SubIFD blocks by this
	}
	si.blocks = iblocks
	w.blocks = appendBlocks(w.blocks, iblocks)

	if si.children, err = w.array(off, depth+1); err != nil {
		return nil, false, err
	}
	return si, true, nil
}

// parseIFD parses the SubIFD at off in w.base for image-block enumeration.
//
// #270: a SubIFD nested in a BigTIFF file inherits the file-wide
// 20-byte-entry/8-byte-field format (BigTIFF spec §2); exif.ParseIFDAt is
// classic-TIFF-only, so parseIFDAtBigTIFF is used there.
//
// ParseIFDAt sets ThumbnailData when a SubIFD holds 0x0201/0x0202 (Nikon NEF
// SubIFD[0] JpgFromRaw). exif.Encode never sees SubIFDs, so the JPEG must be
// enumerated as an image block like any other: ThumbnailData is cleared so
// that appendJPEGBlock enumerates it.
func (w *subIFDWalker) parseIFD(off uint64) (*exif.IFD, bool) {
	var ifd *exif.IFD
	var ok bool
	if w.bigTIFF {
		ifd, ok = parseIFDAtBigTIFF(w.base, off, w.order)
	} else {
		ifd, _, ok = exif.ParseIFDAt(w.base, uint32(off), w.order) //nolint:gosec // G115: classic path; off < 2^32 (bounded by maxFileSize, verified by rawSubIFD)
	}
	if !ok || ifd == nil {
		return nil, false
	}
	ifd.ThumbnailData = nil
	return ifd, true
}

// rawNextIFD returns the next-IFD pointer of the IFD at off in buf (TIFF 6.0
// §2; BigTIFF spec §2), and false when the IFD does not fit in buf.
func rawNextIFD(buf []byte, off uint64, bigTIFF bool, order binary.ByteOrder) (uint64, bool) {
	count, entriesStart, ok := ifdEntryTable(buf, off, bigTIFF, order)
	if !ok || count > bigTIFFMaxIFDEntries {
		return 0, false
	}
	_, entryWidth, fieldWidth := ifdWidths(bigTIFF)
	at := entriesStart + count*uint64(entryWidth)        //nolint:gosec // G115: entryWidth is the constant 12 or 20
	if !fits(at, uint64(fieldWidth), uint64(len(buf))) { //nolint:gosec // G115: fieldWidth is the constant 4 or 8
		return 0, false
	}
	return fieldAsU64(buf[at:at+uint64(fieldWidth)], bigTIFF, order), true //nolint:gosec // G115: same constant
}

// rawSubIFD re-serialises the SubIFD at off in base into a self-contained
// block: the entry count, every entry, a zero next-IFD pointer, then the
// out-of-line value of each entry, word-aligned (TIFF 6.0 §2: "Each data item
// (field value) must begin on a word boundary"), in entry order. The
// value-or-offset field of each out-of-line entry holds the offset of its
// value RELATIVE to the start of the block; patchSubIFDImageOffsets adds the
// block's output offset once it is known. Classic (12-byte entries, 4-byte
// fields) or BigTIFF (20-byte entries, 8-byte fields) widths follow bigTIFF.
//
// Task #311: the former verbatim copy of [off, end of the furthest value)
// lost every value stored BEFORE the directory (libtiff writes values first)
// and copied whatever lay between the directory and its values. Entries are
// kept in source order; an entry of unknown type keeps its field verbatim
// (as exif.Encode does), and an out-of-line entry whose value lies outside
// base is dropped (as exif.Parse does). The next-IFD pointer is written as 0;
// patchRawIFDOffsets sets it when the SubIFD chain continues (subIFDInfo.next).
//
// Returns nil when the directory does not fit in base, or when the values
// would exceed len(base) in total (only overlapping, adversarial entries can
// need more space than the file holds).
func rawSubIFD(base []byte, off uint64, bigTIFF bool, order binary.ByteOrder) []byte { //nolint:cyclop,gocyclo // two passes over the entries with inline/out-of-line and keep/drop branches
	countWidth, entryWidth, fieldWidth := ifdWidths(bigTIFF)
	count, entriesStart, ok := ifdEntryTable(base, off, bigTIFF, order)
	if !ok {
		return nil
	}
	count = min(count, bigTIFFMaxIFDEntries)
	ew := uint64(entryWidth)                                                 //nolint:gosec // G115: entryWidth is the constant 12 or 20
	if !fits(entriesStart, count*ew+uint64(fieldWidth), uint64(len(base))) { //nolint:gosec // G115: fieldWidth is the constant 4 or 8
		return nil
	}
	threshold := inlineThreshold(bigTIFF)

	// Pass 1: count the kept entries and size the value area.
	kept, valLen := uint64(0), uint64(0)
	for i := range count {
		entry, rok := readRawEntryAt(base, entriesStart+i*ew, bigTIFF, order)
		if !rok {
			break
		}
		total, keep := rawEntrySize(entry, bigTIFF, threshold, base, order)
		if !keep {
			continue
		}
		kept++
		if total > threshold {
			valLen += valLen&1 + total
			if valLen > uint64(len(base)) {
				return nil
			}
		}
	}

	fixed := uint64(countWidth) + kept*ew + uint64(fieldWidth) //nolint:gosec // G115: widths are small constants
	out := make([]byte, fixed, fixed+valLen)
	if bigTIFF {
		order.PutUint64(out, kept)
	} else {
		order.PutUint16(out, uint16(kept))
	}

	// Pass 2: copy the entries and append their out-of-line values.
	pos := uint64(countWidth) //nolint:gosec // G115: countWidth is the constant 2 or 8
	for i := range count {
		entry, rok := readRawEntryAt(base, entriesStart+i*ew, bigTIFF, order)
		if !rok {
			break
		}
		total, keep := rawEntrySize(entry, bigTIFF, threshold, base, order)
		if !keep {
			continue
		}
		copy(out[pos:pos+ew], base[entry.pos:entry.pos+ew])
		field := pos + ew - uint64(fieldWidth) //nolint:gosec // G115: fieldWidth is the constant 4 or 8
		pos += ew
		if total <= threshold {
			continue
		}
		if len(out)&1 == 1 {
			out = append(out, 0x00) // word alignment
		}
		src := fieldAsU64(entry.valField, bigTIFF, order)
		rel := uint64(len(out))
		out = append(out, base[src:src+total]...)
		putFieldU64(out[field:], bigTIFF, order, rel)
	}
	// The next-IFD pointer (out[pos:pos+fieldWidth]) is already zero.
	return out
}

// rawEntrySize returns the byte size of the value of entry and whether
// rawSubIFD keeps the entry: entries of unknown type are kept (their field
// is copied verbatim, total 0); an entry whose value size overflows, or whose
// out-of-line value lies outside base, is dropped.
//
// Security audit finding (2026-09-26): entry.count is an 8-byte BigTIFF
// LONG8 field, attacker-controlled up to MaxUint64, so sz*entry.count is
// checked for overflow first, and fits (internal/boundscheck.Fits) guards
// the offset addition.
func rawEntrySize(entry rawIFDEntry, bigTIFF bool, threshold uint64, base []byte, order binary.ByteOrder) (total uint64, keep bool) {
	sz := elemSizeFor(entry.typ, bigTIFF)
	if sz == 0 {
		return 0, true
	}
	if entry.count > ^uint64(0)/sz {
		return 0, false
	}
	total = sz * entry.count
	if total <= threshold {
		return total, true
	}
	return total, fits(fieldAsU64(entry.valField, bigTIFF, order), total, uint64(len(base)))
}

// computeSubIFDsSize returns the number of bytes from ifdEnd (the end of the
// encoded main IFD structure) to the end of the last SubIFD block, including
// every word-alignment pad byte. imageStart = ifdEnd + computeSubIFDsSize is
// therefore the exact position where the image blocks begin.
//
// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
// Each SubIFD block (an IFD structure) is a data item and must start at an
// even file offset, so a 1-byte pad precedes any block that would otherwise
// start at an odd offset. Task #280: the pad parity depends on the ABSOLUTE
// position, so it is derived from ifdEnd (which is odd when the last IFD ends
// with an odd-length thumbnail), exactly as assignSubIFDOffsets and the
// append loop of step 11 do — never from a running total that starts at 0.
// The result equals relocatedLen(ifdEnd, subIFDs) - ifdEnd by construction.
func computeSubIFDsSize(ifdEnd uint64, subIFDs []*subIFDInfo) uint64 {
	return relocatedLen(ifdEnd, subIFDs) - ifdEnd
}

// assignSubIFDOffsets assigns new file offsets to each SubIFD, placing them
// contiguously starting at ifdEnd (just after the main EXIF block).
//
// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
// Each SubIFD block is a data item and must begin at an even file offset.
// A single 0x00 alignment pad byte is counted (and later inserted by the
// append loop in step 11) before any SubIFD that would otherwise start at an
// odd offset. rawSubIFD's value area relies on this: its values are aligned
// relative to the block start, so they are aligned in the file only when the
// block starts at an even offset.
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

// patchSubIFDImageOffsets completes each SubIFD block once assignNewOffsets
// and assignSubIFDOffsets have filled blk.newOffset and si.newOffset: see
// patchRawIFDOffsets.
func patchSubIFDImageOffsets(subIFDs []*subIFDInfo, bigTIFF bool, order binary.ByteOrder) {
	for _, si := range subIFDs {
		patchRawIFDOffsets(si.rawBytes, si.blocks, si.children, si.next, si.newOffset, bigTIFF, order)
	}
}

// patchRawIFDOffsets completes a SubIFD block built by rawSubIFD that will be
// written at the absolute offset newSubIFDOff:
//
//  1. every out-of-line value-or-offset field, which rawSubIFD left relative
//     to the block, becomes newSubIFDOff + relative offset (task #98: an OOL
//     RATIONAL/ASCII/... pointer left at its source value made readers read
//     garbage);
//  2. the strip/tile offset and byte-count arrays (0x0111/0x0117,
//     0x0144/0x0145) and the JPEGInterchangeFormat/Length pair (0x0201/0x0202,
//     a SubIFD JPEG such as Nikon JpgFromRaw) receive the relocated position
//     and size of each image block (blocks, from enumerateIFDBlocks);
//  3. the SubIFDs array (0x014A) receives the output offset of each child,
//     element by element, 0 for a child that is not carried (task #311);
//  4. the next-IFD pointer receives the output offset of next, the SubIFD
//     the source chain links (0 when nil).
//
// Spec references:
//   - TIFF 6.0 §2: classic IFD entry layout — tag(2)+type(2)+count(4)+valOrOff(4);
//     valOrOff holds the value inline when total byte size ≤ 4, otherwise it is
//     a file-absolute offset to the value area.
//   - BigTIFF spec §2: BigTIFF IFD entry layout — tag(2)+type(2)+count(8)+
//     valOrOff(8); inline threshold is 8 bytes.
func patchRawIFDOffsets(rawBytes []byte, blocks []*imageBlock, children []*subIFDInfo, next *subIFDInfo, newSubIFDOff uint64, bigTIFF bool, order binary.ByteOrder) { //nolint:cyclop,gocyclo // next pointer plus inline/out-of-line dispatch per entry; splitting would separate the two passes over one layout
	_, entryWidth, fieldWidth := ifdWidths(bigTIFF)
	count, pos, ok := ifdEntryTable(rawBytes, 0, bigTIFF, order)
	if !ok {
		return
	}
	if next != nil {
		at := pos + count*uint64(entryWidth)                     //nolint:gosec // G115: entryWidth is the constant 12 or 20
		if fits(at, uint64(fieldWidth), uint64(len(rawBytes))) { //nolint:gosec // G115: fieldWidth is the constant 4 or 8
			putFieldU64(rawBytes[at:], bigTIFF, order, next.newOffset)
		}
	}
	// Blocks are looked up by (OFFSET tag, index), so both the offset and the
	// bytecount entry resolve to the same block record.
	idx := newBlockIndex(blocks)
	threshold := inlineThreshold(bigTIFF)
	for i := range count {
		entry, ok := readRawEntryAt(rawBytes, pos+i*uint64(entryWidth), bigTIFF, order) //nolint:gosec // G115: entryWidth is a compile-time constant (12/20) from ifdWidths, never negative
		if !ok {
			break
		}
		elemSz := elemSizeFor(entry.typ, bigTIFF)
		if elemSz == 0 || entry.count == 0 || entry.count > ^uint64(0)/elemSz {
			continue
		}
		total := elemSz * entry.count
		elems := entry.valField
		if total > threshold {
			rel := fieldAsU64(entry.valField, bigTIFF, order)
			putFieldU64(entry.valField, bigTIFF, order, newSubIFDOff+rel)
			if !fits(rel, total, uint64(len(rawBytes))) {
				continue
			}
			elems = rawBytes[rel : rel+total]
		}
		patchRawEntryElements(exif.TagID(entry.tag), elems, entry.count, elemSz, idx, children, order)
	}
}

// patchRawEntryElements writes the relocated values of one SubIFD entry into
// elems, its count elements of elemSz bytes: image-block offsets or sizes for
// the strip, tile and JPEG tags, child SubIFD offsets for 0x014A. Other tags
// are left unchanged.
func patchRawEntryElements(tag exif.TagID, elems []byte, count, elemSz uint64, idx blockIndex, children []*subIFDInfo, order binary.ByteOrder) { //nolint:gocyclo // one case per offset-bearing tag of TIFF 6.0 §7/§15 and TIFF Tech Note 1
	var offsetTag exif.TagID
	size := false
	switch tag {
	case exif.TagStripOffsets, exif.TagTileOffsets, exif.TagJPEGInterchangeFormat:
		offsetTag = tag
	case exif.TagStripByteCounts:
		offsetTag, size = exif.TagStripOffsets, true
	case exif.TagTileByteCounts:
		offsetTag, size = exif.TagTileOffsets, true
	case exif.TagJPEGInterchangeFormatLength:
		offsetTag, size = exif.TagJPEGInterchangeFormat, true
	case exif.TagSubIFDs:
		for j := uint64(0); j < count && j < uint64(len(children)); j++ {
			var v uint64
			if c := children[j]; c != nil {
				v = c.newOffset
			}
			putElem(elems, j, elemSz, order, v)
		}
		return
	default:
		return
	}
	for j := range count {
		blk := idx.find(offsetTag, int(j))
		if blk == nil {
			continue
		}
		v := blk.newOffset
		if size {
			v = blk.size
		}
		putElem(elems, j, elemSz, order, v)
	}
}

// putElem writes v as element j (elemSz bytes: 2, 4 or 8) of elems, in the
// TIFF byte order. A value that does not fit a SHORT element is left
// unwritten rather than truncated; a write past elems is ignored.
func putElem(elems []byte, j, elemSz uint64, order binary.ByteOrder, v uint64) {
	at := j * elemSz
	if at+elemSz > uint64(len(elems)) {
		return
	}
	switch elemSz {
	case 8:
		order.PutUint64(elems[at:], v)
	case 4:
		order.PutUint32(elems[at:], uint32(v)) //nolint:gosec // G115: source declared 4-byte elements; value bounded by maxFileSize
	case 2:
		if v <= 0xFFFF {
			order.PutUint16(elems[at:], uint16(v))
		}
	}
}

// patchSubIFDPointers writes the output offsets of the SubIFDs into the 0x014A
// arrays of the main IFD chain of finalTIFF (the stream exif.Encode wrote):
// the array of the k-th chained IFD receives tree.roots[k], element by
// element, 0 for a SubIFD that is not carried (task #311: the arrays of IFD1
// and later IFDs used to keep their source offsets, and the array of IFD0 was
// filled from the flat list of all SubIFDs, nested ones included, so a
// multi-element array pointed at the wrong IFDs).
//
// exif.Encode writes 0x014A with the Type and Count of the parsed entry
// (generic pass-through), so the element width is read from the entry: 4 bytes
// for LONG/IFD, 8 for LONG8/IFD8 (task #270; do not assume LONG). bigTIFF
// selects the header/entry/field widths used to scan finalTIFF (TIFF 6.0 §2
// for classic; BigTIFF spec §2 for BigTIFF).
//
// Task #116: when the declared Count differs from the number of SubIFDs known
// for that IFD only min(declared, known) elements are written, so no write
// leaves the array, and ErrSubIFDCountMismatch is returned after patching.
// ErrSubIFDEntryNotFound is returned when an IFD with SubIFDs has no 0x014A
// entry in finalTIFF.
func patchSubIFDPointers(finalTIFF []byte, tree subIFDTree, bigTIFF bool, order binary.ByteOrder) error {
	off, ok := readIFD0Offset(finalTIFF, bigTIFF, order)
	var mismatch error
	for k := 0; ok && off != 0 && k < len(tree.roots); k++ {
		if kids := tree.roots[k]; kids != nil {
			if err := patchSubIFDArray(finalTIFF, off, kids, bigTIFF, order); err != nil {
				if !errors.Is(err, ErrSubIFDCountMismatch) {
					return err
				}
				mismatch = err
			}
		}
		off, ok = rawNextIFD(finalTIFF, off, bigTIFF, order)
	}
	return mismatch
}

// patchSubIFDArray patches the 0x014A array of the IFD at ifdOff in finalTIFF
// with the output offsets of kids; see patchSubIFDPointers.
func patchSubIFDArray(finalTIFF []byte, ifdOff uint64, kids []*subIFDInfo, bigTIFF bool, order binary.ByteOrder) error {
	entry, found := findEntryInIFD(finalTIFF, ifdOff, uint16(exif.TagSubIFDs), bigTIFF, order)
	if !found {
		return fmt.Errorf("%w (IFD at offset %d)", ErrSubIFDEntryNotFound, ifdOff)
	}
	declared := entry.count
	actual := uint64(len(kids))
	patchCount := min(declared, actual)
	var mismatchErr error
	if declared != actual {
		mismatchErr = fmt.Errorf("%w (declared=%d actual=%d)", ErrSubIFDCountMismatch, declared, actual)
	}
	elemSz := elemSizeFor(entry.typ, bigTIFF)
	if elemSz == 0 || patchCount == 0 {
		return mismatchErr
	}
	if declared > ^uint64(0)/elemSz {
		return fmt.Errorf("%w (count=%d)", ErrSubIFDPointerArrayOOB, declared)
	}
	elems := entry.valField
	if elemSz*declared > inlineThreshold(bigTIFF) {
		total := elemSz * patchCount
		arrOff := fieldAsU64(entry.valField, bigTIFF, order)
		if !fits(arrOff, total, uint64(len(finalTIFF))) {
			return fmt.Errorf("%w (offset=%d, len=%d)", ErrSubIFDPointerArrayOOB, arrOff, len(finalTIFF))
		}
		elems = finalTIFF[arrOff : arrOff+total]
	}
	for j := range patchCount {
		var v uint64
		if c := kids[j]; c != nil {
			v = c.newOffset
		}
		putElem(elems, j, elemSz, order, v)
	}
	return mismatchErr
}

// blockIndex finds the image block of one IFD by (offset tag, index) in
// O(1) without a map.
//
// Contiguity invariant: blocks is subIFDInfo.blocks, the result of a single
// enumerateIFDBlocks call for one IFD. That call yields at most one run per
// offset tag, in this order: the StripOffsets run and the TileOffsets run
// (each one extractParallelOffsetBlocks call, which appends every index
// 0..n-1 in ascending order, including zero-size blocks), then at most one
// JPEGInterchangeFormat block with index 0. Hence the block with index i of a
// tag, if it exists, is exactly at the tag's run start + i, and a miss at
// that position means no such block exists.
type blockIndex struct {
	blocks                         []*imageBlock
	stripStart, tileStart, jpegPos int
}

// newBlockIndex records the start of each offset tag's run in blocks.
func newBlockIndex(blocks []*imageBlock) blockIndex {
	x := blockIndex{blocks: blocks, stripStart: -1, tileStart: -1, jpegPos: -1}
	for i, blk := range blocks {
		switch {
		case blk.entryTag == exif.TagStripOffsets && x.stripStart < 0:
			x.stripStart = i
		case blk.entryTag == exif.TagTileOffsets && x.tileStart < 0:
			x.tileStart = i
		case blk.entryTag == exif.TagJPEGInterchangeFormat && x.jpegPos < 0:
			x.jpegPos = i
		}
	}
	return x
}

// runStart returns the index in x.blocks of the first block of tag, or -1.
func (x blockIndex) runStart(tag exif.TagID) int {
	switch tag {
	case exif.TagStripOffsets:
		return x.stripStart
	case exif.TagTileOffsets:
		return x.tileStart
	case exif.TagJPEGInterchangeFormat:
		return x.jpegPos
	}
	return -1
}

// find returns the block with the given offset tag and index, or nil.
func (x blockIndex) find(tag exif.TagID, index int) *imageBlock {
	if start := x.runStart(tag); start >= 0 && index >= 0 && index < len(x.blocks)-start {
		if blk := x.blocks[start+index]; blk.entryTag == tag && blk.index == index {
			return blk
		}
	}
	// By the contiguity invariant (see blockIndex) no other position can
	// hold the block, so a miss is definitive. Never scan: a crafted entry
	// with a large Count and no matching blocks would make patchRawIFDOffsets
	// quadratic.
	return nil
}
