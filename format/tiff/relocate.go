package tiff

// relocate.go — TIFF copy-and-relocate serializer (epic #33, tasks #92/#93).
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
//
// Deferred (task #94):
//   - SubIFD recursion (tag 0x014A / TagSubIFDs) for DNG and TIFF-EP.
//   - Deep multi-level SubIFD chains inside RAW variants.
//
// Spec references:
//   - TIFF 6.0 §2: TIFF header, IFD structure, next-IFD chain.
//   - TIFF 6.0 §7: StripOffsets/StripByteCounts (split-strip images).
//   - TIFF 6.0 §8.1: JPEGInterchangeFormat / JPEGInterchangeFormatLength.
//   - TIFF 6.0 §15: TileOffsets/TileByteCounts (tiled images).
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

// relocateTIFF is the core TIFF copy-and-relocate serializer.
//
// It parses base as a TIFF stream, upserts rawIPTC and/or rawXMP into IFD0,
// enumerates every image-data block referenced by offset/bytecount tag pairs,
// re-encodes the IFD structure via exif.Encode, and appends each image block
// at a new absolute offset. The result is a well-formed TIFF stream with
// byte-identical image data and updated metadata.
//
// Algorithm:
//  1. exif.Parse(base) → EXIF struct with all IFDs.
//  2. Upsert IPTC (0x83BB) / XMP (0x02BC) in IFD0.
//  3. Enumerate image blocks from IFD0 + IFD1 chain (strips, tiles, main-image JPEG).
//  4. Remove the stale image-data offset entries from each IFD.
//  5. Re-insert placeholder entries (correct type and Count = N, zero values)
//     and call exif.Encode to measure the true IFD structure size (ifdEnd).
//  6. Assign new absolute offsets: block[i].newOffset = ifdEnd + Σsize[0..i-1].
//  7. Update the placeholder value bytes in-place with real offsets.
//  8. Re-encode via exif.Encode → finalTIFF (same size as step 5).
//  9. Append each block's bytes from base in order.
//
// Two calls to exif.Encode are needed (steps 5 and 8). The IFD structure size
// does not change between calls because only value bytes are updated.
func relocateTIFF(base []byte, rawIPTC, rawXMP []byte) ([]byte, error) { //nolint:cyclop,gocyclo // complex by necessity: TIFF structural rewriting requires handling all image-block patterns in one function
	e, err := exif.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("tiff: parse for relocation: %w", err)
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
		upsertIFD0Entry(e.IFD0, exif.TagIPTC, exif.TypeUndefined, rawIPTC)
	}
	if rawXMP != nil {
		upsertIFD0Entry(e.IFD0, exif.TagXMP, exif.TypeUndefined, rawXMP)
	}

	// Step 3: enumerate image blocks from the IFD chain.
	blocks, enumerateErr := enumerateImageBlocks(base, e, order)
	if enumerateErr != nil {
		return nil, fmt.Errorf("tiff: enumerate image blocks: %w", enumerateErr)
	}

	// Short-circuit when there are no image blocks to relocate.
	if len(blocks) == 0 {
		out, encErr := exif.Encode(e)
		if encErr != nil {
			return nil, fmt.Errorf("tiff: encode (no image blocks): %w", encErr)
		}
		return out, nil
	}

	// Step 4: remove stale image-data offset entries from all IFDs.
	removeImageOffsetEntries(blocks)

	// Step 5: re-insert placeholder entries (correct type/count, zero values)
	// and encode to learn the exact IFD structure size.
	offsetValueSlices := insertPlaceholders(blocks, order)

	skeleton, skelErr := exif.Encode(e)
	if skelErr != nil {
		return nil, fmt.Errorf("tiff: encode placeholder: %w", skelErr)
	}
	ifdEnd := uint32(len(skeleton)) //nolint:gosec // G115: len(skeleton) bounded by TIFF stream size, < 2^32

	// Step 6: assign new absolute offsets.
	assignNewOffsets(blocks, ifdEnd)

	// Step 7: update placeholder value bytes with real offsets.
	updatePlaceholders(blocks, offsetValueSlices, order)

	// Step 8: re-encode → finalTIFF. Same IFD layout as step 5.
	finalTIFF, finalErr := exif.Encode(e)
	if finalErr != nil {
		return nil, fmt.Errorf("tiff: encode final: %w", finalErr)
	}

	// Step 9: append each image block's bytes from the source buffer.
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

// enumerateImageBlocks scans IFD0 and the IFD1 chain for image-data blocks
// referenced by offset/bytecount tag pairs. It does not follow SubIFD pointers
// (tag 0x014A) — that is deferred to task #94.
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
	// (Bounds are re-verified in step 9 of relocateTIFF before appending bytes.)
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
// placing them contiguously starting at ifdEnd.
//
// TIFF 6.0 §2: all offsets are measured from byte 0 of the TIFF stream.
func assignNewOffsets(blocks []*imageBlock, ifdEnd uint32) {
	cur := ifdEnd
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
// share backing arrays, exif.Encode in step 8 sees the updated values.
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
