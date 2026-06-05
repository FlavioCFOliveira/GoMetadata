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

// imageBlock describes a contiguous range of image bytes in the source TIFF
// and records the new position assigned to it in the rebuilt stream.
//
// ifd and entryTag identify which IFD and which offset tag owns this block so
// that patchIFDBlocks can write the new offset back into the correct entry.
// When the owning offset tag holds an array (multi-strip, multi-tile), index
// is the position within that array; for scalar offsets index is always 0.
type imageBlock struct {
	srcOffset uint32     // absolute offset in the original TIFF buffer
	size      uint32     // byte length of the block
	newOffset uint32     // filled in by assignNewOffsets
	ifdPtr    *exif.IFD  // owning IFD (pointer to the parsed IFD)
	entryTag  exif.TagID // which offset tag owns this block
	index     int        // position within the offset array (0 for scalar)
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
	srcOffset uint32    // original SubIFD offset in base
	ifd       *exif.IFD // parsed SubIFD (for block enumeration)
	rawBytes  []byte    // verbatim copy of source SubIFD bytes
	newOffset uint32    // filled in when SubIFD is appended to output
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
	if rawIPTC != nil {
		// Adobe XMP Spec / ExifTool convention: IPTC-NAA (0x83BB) as TypeLong.
		// upsertIFD0Entry pads value to 4-byte boundary; Count = nLongs.
		upsertIFD0Entry(e.IFD0, exif.TagIPTC, exif.TypeLong, rawIPTC)
	}
	if rawXMP != nil {
		// Adobe XMP Spec (TIFF Technical Note 3): XMP (0x02BC) as TypeByte.
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
	blocks, enumerateErr := enumerateImageBlocks(base, e, order)
	if enumerateErr != nil {
		return nil, fmt.Errorf("tiff: enumerate image blocks: %w", enumerateErr)
	}

	// Step 4: parse SubIFDs (tag 0x014A) from the raw base buffer.
	//
	// TIFF Extension §F / Adobe DNG Spec §4: SubIFDs tag (0x014A) holds an
	// array of LONG offsets, each pointing to an independent child IFD. DNG
	// stores its full-resolution image data in one or more SubIFDs. Each SubIFD
	// may carry its own StripOffsets / TileOffsets blocks that must be relocated.
	subIFDs, subBlocks, subErr := enumerateSubIFDs(base, e.IFD0, order)
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
	offsetValueSlices := insertPlaceholders(mainBlocks, order)

	skeleton, skelErr := exif.Encode(e)
	if skelErr != nil {
		return nil, fmt.Errorf("tiff: encode placeholder: %w", skelErr)
	}
	ifdEnd := uint32(len(skeleton)) //nolint:gosec // G115: len(skeleton) bounded by TIFF stream size, < 2^32

	// Step 7: assign new absolute offsets.
	// SubIFD blocks are placed first (SubIFD structures after the main EXIF
	// block), then main image blocks follow after all SubIFD structures.
	subIFDsSize := computeSubIFDsSize(subIFDs)
	imageStart := ifdEnd + subIFDsSize // image blocks start after all SubIFD raw bytes
	assignNewOffsets(blocks, imageStart)
	assignSubIFDOffsets(subIFDs, ifdEnd)

	// Step 8a: update placeholder value bytes (main-IFD blocks).
	updatePlaceholders(mainBlocks, offsetValueSlices, order)

	// Step 8b: patch SubIFD raw bytes — update strip/tile offset entries in
	// each SubIFD's rawBytes to point at the newly assigned image-block offsets.
	patchSubIFDImageOffsets(subIFDs, blocks, order)

	// Step 9: re-encode → finalTIFF. Same IFD layout as step 6.
	finalTIFF, finalErr := exif.Encode(e)
	if finalErr != nil {
		return nil, fmt.Errorf("tiff: encode final: %w", finalErr)
	}

	// Step 10: patch the 0x014A SubIFDs pointer array in finalTIFF.
	// exif.Encode treats 0x014A as a plain TypeLong value (the old offset
	// bytes from the original file). We must overwrite those bytes with the
	// new SubIFD positions before appending anything.
	if len(subIFDs) > 0 {
		if err := patchSubIFDPointers(finalTIFF, subIFDs, order); err != nil {
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
		end := uint64(blk.srcOffset) + uint64(blk.size)
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
func enumerateImageBlocks(base []byte, e *exif.EXIF, order binary.ByteOrder) ([]*imageBlock, error) {
	var blocks []*imageBlock
	for ifd := e.IFD0; ifd != nil; ifd = ifd.Next {
		iblocks, err := enumerateIFDBlocks(base, ifd, order)
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
func enumerateIFDBlocks(base []byte, ifd *exif.IFD, order binary.ByteOrder) ([]*imageBlock, error) { //nolint:cyclop // handles all three tag-pair patterns in a single linear scan; splitting would hurt readability
	var blocks []*imageBlock

	stripOff := ifd.Get(exif.TagStripOffsets)
	stripLen := ifd.Get(exif.TagStripByteCounts)
	if stripOff != nil && stripLen != nil {
		sb, err := extractParallelOffsetBlocks(base, ifd, exif.TagStripOffsets, stripOff, stripLen, order)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, sb...)
	}

	tileOff := ifd.Get(exif.TagTileOffsets)
	tileLen := ifd.Get(exif.TagTileByteCounts)
	if tileOff != nil && tileLen != nil {
		tb, err := extractParallelOffsetBlocks(base, ifd, exif.TagTileOffsets, tileOff, tileLen, order)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, tb...)
	}

	// JPEGInterchangeFormat: skip when ThumbnailData is non-nil (exif.Encode handles it).
	if ifd.ThumbnailData == nil {
		blocks = appendJPEGBlock(len(base), ifd, blocks, order)
	}

	return blocks, nil
}

// appendJPEGBlock appends a JPEGInterchangeFormat block to blocks if present.
// Extracted to reduce nestif complexity in enumerateIFDBlocks.
// baseLen is the length of the source TIFF buffer; it is used to skip entries
// whose indicated offset+size would fall outside the buffer.
func appendJPEGBlock(baseLen int, ifd *exif.IFD, blocks []*imageBlock, order binary.ByteOrder) []*imageBlock {
	jpegOff := ifd.Get(exif.TagJPEGInterchangeFormat)
	jpegLen := ifd.Get(exif.TagJPEGInterchangeFormatLength)
	if jpegOff == nil || jpegLen == nil || len(jpegOff.Value) < 4 || len(jpegLen.Value) < 4 {
		return blocks
	}
	off := order.Uint32(jpegOff.Value)
	size := order.Uint32(jpegLen.Value)
	if size == 0 {
		return blocks
	}
	// Skip entries whose indicated range falls outside the source buffer.
	// (Bounds are re-verified in step 12 of relocateTIFF before appending bytes.)
	end := uint64(off) + uint64(size)
	if end > uint64(baseLen) { //nolint:gosec // G115: baseLen = len([]byte), always non-negative
		return blocks
	}
	return append(blocks, &imageBlock{
		srcOffset: off,
		size:      size,
		ifdPtr:    ifd,
		entryTag:  exif.TagJPEGInterchangeFormat,
		index:     0,
	})
}

// extractParallelOffsetBlocks extracts imageBlock records from parallel
// offset/bytecount array entries.
//
// TIFF 6.0 §7: StripOffsets[i] is an absolute offset; StripByteCounts[i] is
// the byte length of that strip. Same for TileOffsets/TileByteCounts.
func extractParallelOffsetBlocks( //nolint:cyclop,gocyclo // bounds-checking on two parallel arrays requires several branches; splitting further reduces clarity
	base []byte,
	ifd *exif.IFD,
	offsetTag exif.TagID,
	offsetEntry, countEntry *exif.IFDEntry,
	order binary.ByteOrder,
) ([]*imageBlock, error) {
	if offsetEntry.Count != countEntry.Count {
		// Mismatched counts are a format error; skip silently to avoid blocking writes.
		return nil, nil
	}
	n := int(offsetEntry.Count)
	if n == 0 {
		return nil, nil
	}

	offElemSz := int(typeSize(uint16(offsetEntry.Type)))
	cntElemSz := int(typeSize(uint16(countEntry.Type)))
	if offElemSz == 0 || cntElemSz == 0 {
		return nil, fmt.Errorf("tiff: tag 0x%04X: %w", offsetTag, ErrUnsupportedOffsetType)
	}

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

		if size == 0 {
			blocks = append(blocks, &imageBlock{
				srcOffset: off,
				size:      0,
				ifdPtr:    ifd,
				entryTag:  offsetTag,
				index:     i,
			})
			continue
		}

		// uint64 bounds check (TIFF 6.0 §2: all offsets are uint32).
		end := uint64(off) + uint64(size)
		if end > uint64(len(base)) {
			return nil, fmt.Errorf("tiff: tag 0x%04X[%d] offset=%d size=%d: %w",
				offsetTag, i, off, size, ErrBlockOutOfBounds)
		}

		blocks = append(blocks, &imageBlock{
			srcOffset: off,
			size:      size,
			ifdPtr:    ifd,
			entryTag:  offsetTag,
			index:     i,
		})
	}
	return blocks, nil
}

// readUint reads a little- or big-endian unsigned integer of elemSz bytes
// (2 or 4) from b.
func readUint(b []byte, elemSz int, order binary.ByteOrder) (uint32, error) {
	switch elemSz {
	case 2:
		if len(b) < 2 {
			return 0, fmt.Errorf("tiff: need 2 bytes, have %d: %w", len(b), ErrTruncatedOffsetArray)
		}
		return uint32(order.Uint16(b)), nil
	case 4:
		if len(b) < 4 {
			return 0, fmt.Errorf("tiff: need 4 bytes, have %d: %w", len(b), ErrTruncatedOffsetArray)
		}
		return order.Uint32(b), nil
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
func assignNewOffsets(blocks []*imageBlock, imageStart uint32) {
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

// insertPlaceholders inserts image-data offset and bytecount entries with
// zeroed value bytes into each owning IFD. The placeholder Count = N
// (number of TypeLong elements) so that exif.Encode accounts for the exact
// final value-area space.
//
// Returns a map from groupKey to the pair of value slices [offVals, cntVals].
// updatePlaceholders writes the real values into these slices in-place;
// since IFDEntry.Value points to the same backing arrays, the updated values
// are visible to exif.Encode without re-insertion.
func insertPlaceholders(blocks []*imageBlock, _ binary.ByteOrder) map[groupKey][2][]byte {
	// Collect unique groups in stable insertion order.
	seen := make(map[groupKey]int)
	var keys []groupKey
	for _, blk := range blocks {
		k := groupKey{blk.ifdPtr, blk.entryTag}
		if _, ok := seen[k]; !ok {
			seen[k] = len(keys)
			keys = append(keys, k)
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
		offVals := make([]byte, n*4) // TypeLong: 4 bytes per element
		cntVals := make([]byte, n*4)

		// TIFF 6.0 §2: Count = number of values (not bytes).
		upsertIFDEntryWithCount(k.ifd, k.tag, uint32(n), offVals) //nolint:gosec // G115: n bounded by strip/tile count, < 2^32
		if countTag := bytecountTagFor(k.tag); countTag != 0 {
			upsertIFDEntryWithCount(k.ifd, countTag, uint32(n), cntVals) //nolint:gosec // G115: same
		}

		result[k] = [2][]byte{offVals, cntVals}
	}
	return result
}

// updatePlaceholders writes the real new offsets and block sizes into the
// value slices inserted by insertPlaceholders. Because IFDEntry.Value slices
// share backing arrays, exif.Encode in step 9 sees the updated values.
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
		order.PutUint32(pair[0][blk.index*4:], blk.newOffset)
		order.PutUint32(pair[1][blk.index*4:], blk.size)
	}
}

// upsertIFDEntryWithCount inserts or replaces an entry in ifd with the given
// tag, count, and TypeLong value bytes. This differs from upsertIFD0Entry in
// that the Count is provided explicitly rather than inferred from len(value).
//
// TIFF 6.0 §2: for TypeLong, Count = number of uint32 elements (not bytes).
// For a 2-element array Count=2, value is 8 bytes.
//
// The sorted-by-tag invariant is maintained using binary search insertion.
func upsertIFDEntryWithCount(ifd *exif.IFD, tag exif.TagID, count uint32, value []byte) {
	entry := exif.IFDEntry{
		Tag:   tag,
		Type:  exif.TypeLong,
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

// enumerateSubIFDs parses the SubIFDs (0x014A) pointer array from ifd0 in the
// raw base buffer, builds a subIFDInfo for each referenced SubIFD, and
// enumerates their image blocks (strips/tiles).
//
// TIFF Extension §F / Adobe DNG Spec §4: SubIFDs (tag 0x014A) is a TypeLong
// array; each uint32 element is an absolute file offset pointing to an IFD
// that is a child of the current IFD. DNG's IFD0 typically points to one or
// more SubIFDs that carry the full-resolution image data.
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
func enumerateSubIFDs(base []byte, ifd0 *exif.IFD, order binary.ByteOrder) ([]*subIFDInfo, []*imageBlock, error) {
	if ifd0 == nil {
		return nil, nil, nil
	}
	subEntry := ifd0.Get(exif.TagSubIFDs)
	if subEntry == nil {
		return nil, nil, nil
	}

	// 0x014A is TypeLong; each element is 4 bytes.
	// Count=1 value is stored inline (4 bytes in the IFD entry field per TIFF §2).
	n := int(subEntry.Count)
	if n == 0 {
		return nil, nil, nil
	}

	elemSz := int(typeSize(uint16(subEntry.Type)))
	if elemSz == 0 {
		// Unknown type for 0x014A — skip gracefully.
		return nil, nil, nil
	}
	if len(subEntry.Value) < n*elemSz {
		return nil, nil, nil
	}

	const maxSubIFDDepth = 8 // guard against pathological nesting
	return enumerateSubIFDsAt(base, subEntry, n, elemSz, order, 0, maxSubIFDDepth)
}

// enumerateSubIFDsAt recursively enumerates SubIFDs. depth is the current
// nesting depth; maxDepth prevents unbounded recursion on crafted inputs.
func enumerateSubIFDsAt( //nolint:cyclop,gocyclo // SubIFD recursion and cycle detection require several branches; complexity is inherent to the algorithm
	base []byte,
	subEntry *exif.IFDEntry,
	n, elemSz int,
	order binary.ByteOrder,
	depth, maxDepth int,
) ([]*subIFDInfo, []*imageBlock, error) {
	if depth > maxDepth {
		return nil, nil, nil
	}

	var subIFDs []*subIFDInfo
	var allBlocks []*imageBlock

	visited := make(map[uint32]bool, n)

	for i := range n {
		off, err := readUint(subEntry.Value[i*elemSz:], elemSz, order)
		if err != nil || off == 0 {
			continue
		}
		if visited[off] {
			continue // cycle guard
		}
		visited[off] = true

		// Parse the SubIFD at this offset.
		rawIFD := extractRawIFD(base, off, order)
		if rawIFD == nil {
			// Out-of-bounds or unreadable; skip gracefully.
			continue
		}

		parsedIFD, _, ok := exif.ParseIFDAt(base, off, order)
		if !ok || parsedIFD == nil {
			continue
		}

		// Clear ThumbnailData on the SubIFD before block enumeration.
		//
		// parseSingleIFD (via ParseIFDAt) sets IFD.ThumbnailData when it finds
		// both tag 0x0201 (JPEGInterchangeFormat) and 0x0202 in the same IFD,
		// because those tags normally indicate a JPEG thumbnail in IFD1.
		// For SubIFDs (tag 0x014A), 0x0201/0x0202 hold the JpgFromRaw offset
		// (Nikon NEF SubIFD[0]) or another JPEG image, NOT a thumbnail managed
		// by exif.Encode.
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
		iblocks, err := enumerateIFDBlocks(base, parsedIFD, order)
		if err != nil {
			return nil, nil, fmt.Errorf("tiff: SubIFD at offset %d: %w", off, err)
		}
		// Re-point the ifdPtr to the parsedIFD so filterMainBlocks can identify them.
		for _, blk := range iblocks {
			blk.ifdPtr = parsedIFD
		}
		allBlocks = append(allBlocks, iblocks...)

		// Recurse into any nested SubIFDs (0x014A) within this SubIFD.
		nestedEntry := parsedIFD.Get(exif.TagSubIFDs)
		if nestedEntry != nil && nestedEntry.Count > 0 {
			nestedElemSz := int(typeSize(uint16(nestedEntry.Type)))
			if nestedElemSz > 0 && len(nestedEntry.Value) >= int(nestedEntry.Count)*nestedElemSz {
				nestSubs, nestBlocks, nestErr := enumerateSubIFDsAt(
					base, nestedEntry, int(nestedEntry.Count), nestedElemSz,
					order, depth+1, maxDepth,
				)
				if nestErr != nil {
					return nil, nil, nestErr
				}
				subIFDs = append(subIFDs, nestSubs...)
				allBlocks = append(allBlocks, nestBlocks...)
			}
		}
	}

	return subIFDs, allBlocks, nil
}

// extractRawIFD returns a byte slice containing the complete raw IFD block
// starting at offset off within base: count(2) + entries(count×12) +
// nextIFD(4) + out-of-line value area. The returned slice is a copy
// (independent of base), safe to mutate for offset patching.
//
// Returns nil if off is out of bounds or the IFD is malformed.
//
// Important: the value area includes the TileOffsets/StripOffsets array data
// (these are index arrays, NOT image pixel data). Image pixel data is excluded
// because it is referenced by the offset values stored in those arrays and is
// handled by the imageBlock relocation mechanism. Only the arrays themselves
// (the lists of offsets and byte counts) are included here, so that
// patchRawIFDOffsets can overwrite the array elements in-place.
func extractRawIFD(base []byte, off uint32, order binary.ByteOrder) []byte { //nolint:cyclop // IFD scanning requires bounds-checking branches; complexity is inherent to raw binary parsing
	// Read entry count.
	if uint64(off)+2 > uint64(len(base)) {
		return nil
	}
	count := int(order.Uint16(base[off:]))
	entriesEnd := int(off) + 2 + count*12 + 4 // count + entries + next-IFD ptr
	if entriesEnd > len(base) {
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
	// In other words: include the "index" (the array of uint32 offsets),
	// but not the "image" (the bytes at those uint32 offsets).
	//
	// For JPEGInterchangeFormat the out-of-line value is the JPEG thumbnail
	// data itself, which we also exclude (imageBlock handles it). Only if a
	// JPEG offset tag's value array is OOL (which is impossible for scalar
	// entries: count=1, inline) would we need to think about this.
	var valueAreaEnd uint32
	pos := int(off) + 2
	for i := range count {
		e := pos + i*12
		if e+12 > len(base) {
			break
		}
		entryType := order.Uint16(base[e+2:])
		entryCount := order.Uint32(base[e+4:])
		sz := typeSize(entryType)
		if sz == 0 {
			continue
		}
		total := uint64(sz) * uint64(entryCount)
		if total <= 4 {
			continue // inline, no value area
		}
		valOff := order.Uint32(base[e+8:])
		valEnd := uint64(valOff) + total
		if valEnd > uint64(len(base)) {
			continue
		}
		// Use uint64 comparison to avoid gocritic truncateCmp warning.
		// valEnd is already uint64; valueAreaEnd is uint32. Cast valueAreaEnd
		// to uint64 for the comparison, then narrow back for storage (safe:
		// valEnd is bounded by len(base) which fits in uint32).
		if valEnd > uint64(valueAreaEnd) {
			valueAreaEnd = uint32(valEnd) //nolint:gosec // G115: valEnd bounded by len(base), < 2^32
		}
	}

	// Compute total rawBytes size: max(fixedBlock, valueAreaEnd), capped at len(base).
	totalLen := min(
		max(uint32(entriesEnd), valueAreaEnd), //nolint:gosec // G115: both terms bounded by len(base)
		uint32(len(base)),                     //nolint:gosec // G115: len(base) is non-negative
	)

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
func computeSubIFDsSize(subIFDs []*subIFDInfo) uint32 {
	var total uint32
	for _, si := range subIFDs {
		// Word-align before this SubIFD block.
		if total&1 == 1 {
			total++ // alignment pad byte
		}
		total += uint32(len(si.rawBytes)) //nolint:gosec // G115: len bounded by source buffer size
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
func assignSubIFDOffsets(subIFDs []*subIFDInfo, ifdEnd uint32) {
	cur := ifdEnd
	for _, si := range subIFDs {
		// Word-align: skip to even offset before placing this SubIFD block.
		if cur&1 == 1 {
			cur++ // account for the alignment pad byte inserted in step 11
		}
		si.newOffset = cur
		sz := uint32(len(si.rawBytes)) //nolint:gosec // G115: len bounded by source buffer size
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
func patchSubIFDImageOffsets(subIFDs []*subIFDInfo, blocks []*imageBlock, order binary.ByteOrder) {
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
		patchRawIFDOffsets(si.rawBytes, ifdBlocks, si.srcOffset, si.newOffset, order)
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
//   - Bug #98: value bytes ARE captured verbatim in rawBytes by extractRawIFD,
//     but the entry's valOrOff pointer still holds the OLD absolute file offset.
//     After rawBytes is appended at newSubIFDOff the pointer is stale, causing
//     readers to follow it into garbage (XResolution/YResolution → "undef").
//   - Fix: rewrite the valOrOff field to newSubIFDOff + relOff, where relOff is
//     (origFileOff − srcOff), the position of the value area within rawBytes.
//     The value bytes themselves are already correct — only the pointer changes.
//
// Spec references:
//   - TIFF 6.0 §2: IFD entry layout — tag(2)+type(2)+count(4)+valOrOff(4).
//     valOrOff holds the value inline when total byte size ≤ 4; otherwise it is
//     a file-absolute offset to the value area.
func patchRawIFDOffsets(rawBytes []byte, blocks []*imageBlock, srcOff, newSubIFDOff uint32, order binary.ByteOrder) { //nolint:cyclop,gocyclo,funlen // patch loop requires multiple tag-specific branches; inline/OOL branching is inherent to the spec
	if len(rawBytes) < 6 {
		return
	}
	// Read the entry count from the start of the IFD block (stored at rawBytes[0:2]).
	count := int(order.Uint16(rawBytes[0:]))

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

	pos := 2 // start of first entry within rawBytes
	for i := range count {
		e := pos + i*12
		if e+12 > len(rawBytes) {
			break
		}
		entryTag := exif.TagID(order.Uint16(rawBytes[e:]))
		entryType := order.Uint16(rawBytes[e+2:])
		entryCount := int(order.Uint32(rawBytes[e+4:]))

		elemSz := int(typeSize(entryType))
		if elemSz == 0 || entryCount == 0 {
			continue
		}

		total := uint64(elemSz) * uint64(entryCount) //nolint:gosec // G115: elemSz ≤ 8; entryCount is non-negative int
		if total <= 4 {                              //nolint:nestif // inline path for strip/tile scalar entries; branching is inherent to TIFF §2 inline/OOL duality
			// Inline value: no valOrOff pointer to update for non-image-data entries.
			// Strip/tile offset tags and JPEGInterchangeFormat with a single inline
			// value are handled below.
			//
			// Tag 0x0201 (JPEGInterchangeFormat) in a SubIFD (not IFD1) represents a
			// full-resolution JPEG image (e.g. Nikon NEF JpgFromRaw), not a thumbnail.
			// It is always TypeLong, Count=1 (4 bytes = inline), so total==4 here.
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
				order.PutUint32(rawBytes[e+8:], blk.size)
			} else {
				order.PutUint32(rawBytes[e+8:], blk.newOffset)
			}
			continue
		}

		// Out-of-line value: rawBytes[e+8:e+12] is a file-absolute pointer to
		// the value area. The value bytes are already captured verbatim in rawBytes
		// by extractRawIFD. We must update the pointer to the new absolute position.
		//
		// TIFF 6.0 §2: valOrOff = absolute file offset when total byte size > 4.
		origFileOff := order.Uint32(rawBytes[e+8:])
		if uint64(origFileOff) < uint64(srcOff) {
			// Value area precedes the SubIFD start — not captured in rawBytes. Skip.
			continue
		}
		relOff := int(origFileOff - srcOff) // relative offset of value area within rawBytes
		if relOff < 0 || relOff+entryCount*elemSz > len(rawBytes) {
			continue // out of rawBytes bounds — skip
		}

		// Update the valOrOff pointer to the new absolute position of the value area.
		// Task #98 regression: RATIONAL/SRATIONAL/DOUBLE/long-ASCII/etc. entries
		// had their value bytes preserved verbatim in rawBytes but their valOrOff
		// pointers left pointing at the old file position → "undef" on read.
		newArrAbsOff := newSubIFDOff + uint32(relOff) //nolint:gosec // G115: relOff bounded by rawBytes size
		order.PutUint32(rawBytes[e+8:], newArrAbsOff)

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

		// elemSz is 2 or 4 for strip/tile tags; validated by typeSize above.
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

		for j := range entryCount {
			blk := blkMap[blockKey{offsetTag, j}]
			if blk == nil {
				continue
			}
			valPos := relOff + j*elemSz
			switch elemSz {
			case 4:
				if isByteCount {
					order.PutUint32(rawBytes[valPos:], blk.size)
				} else {
					order.PutUint32(rawBytes[valPos:], blk.newOffset)
				}
			case 2:
				// TypeShort: new value must fit in uint16.
				// The guard ensures the value fits; the cast is safe.
				if isByteCount && blk.size <= 0xFFFF {
					order.PutUint16(rawBytes[valPos:], uint16(blk.size))
				} else if !isByteCount && blk.newOffset <= 0xFFFF {
					order.PutUint16(rawBytes[valPos:], uint16(blk.newOffset))
				}
			}
		}
	}
}

// patchSubIFDPointers locates the 0x014A SubIFDs entry in the final TIFF
// output and overwrites the pointer array values with the new SubIFD offsets.
//
// exif.Encode treats 0x014A as a plain value-or-offset field (TypeLong, the
// old file offsets). After re-encoding we must scan IFD0 in finalTIFF to find
// the 0x014A entry and patch its value bytes to reflect the new SubIFD positions.
//
// TIFF 6.0 §2: IFD0 starts at the offset stored in header bytes 4-7.
// Each IFD entry is 12 bytes: tag(2) + type(2) + count(4) + value-or-offset(4).
// For Count=1: the single uint32 is stored inline in bytes 8-11.
// For Count>1: bytes 8-11 are a file offset to the uint32 array (out-of-line).
func patchSubIFDPointers(finalTIFF []byte, subIFDs []*subIFDInfo, order binary.ByteOrder) error { //nolint:cyclop,gocyclo // linear IFD scan with inline/OOL branching; splitting further would reduce clarity
	if len(finalTIFF) < 8 {
		return nil
	}
	ifd0Off := int(order.Uint32(finalTIFF[4:]))
	if ifd0Off+2 > len(finalTIFF) {
		return nil
	}
	count := int(order.Uint16(finalTIFF[ifd0Off:]))
	pos := ifd0Off + 2

	for i := range count {
		e := pos + i*12
		if e+12 > len(finalTIFF) {
			break
		}
		tag := order.Uint16(finalTIFF[e:])
		if tag != uint16(exif.TagSubIFDs) {
			continue
		}

		// Found the 0x014A entry.
		entryCount := int(order.Uint32(finalTIFF[e+4:]))
		if entryCount != len(subIFDs) {
			// Count mismatch: subIFDs and the 0x014A entry disagree. This
			// should not happen unless exif.Encode truncated or dropped entries.
			// Patch as many as we can to avoid writing stale offsets.
			if entryCount > len(subIFDs) {
				entryCount = len(subIFDs)
			}
		}

		total := uint64(4) * uint64(entryCount) // TypeLong, each 4 bytes
		if total <= 4 && entryCount == 1 {
			// Single SubIFD: inline value in bytes 8-11.
			order.PutUint32(finalTIFF[e+8:], subIFDs[0].newOffset)
		} else {
			// Multiple SubIFDs: bytes 8-11 are an offset to the value array.
			arrOff := int(order.Uint32(finalTIFF[e+8:]))
			if arrOff+entryCount*4 > len(finalTIFF) {
				return fmt.Errorf("%w (offset=%d, len=%d)", ErrSubIFDPointerArrayOOB,
					arrOff, len(finalTIFF))
			}
			for j := range entryCount {
				order.PutUint32(finalTIFF[arrOff+j*4:], subIFDs[j].newOffset)
			}
		}
		return nil
	}
	// 0x014A entry not found in IFD0 of the re-encoded output. This can happen
	// when exif.Encode drops entries it doesn't understand (unknown types).
	// In practice 0x014A is TypeLong so it should be preserved. If it is absent,
	// we cannot fix the SubIFD pointers — return an error so the caller can
	// detect the problem.
	return fmt.Errorf("%w", ErrSubIFDEntryNotFound)
}
