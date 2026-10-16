package tiff

// relocate_blocks.go — coalescing of the source ranges of the image blocks of a
// relocated write (security audit F-07a, task #379).

import (
	"cmp"
	"slices"
)

// srcRange is the half-open range [start, end) of the source file.
type srcRange struct{ start, end uint64 }

// blockPlan is the union of the source ranges of a set of image blocks.
type blockPlan struct {
	// ranges are the disjoint ranges to copy, in the order they are placed in the
	// output: the order of the first block of each range in the block slice.
	ranges []srcRange
	// of[i] is the index in ranges of the range that holds block i, or -1 for a
	// block without bytes.
	of []int32
}

// disjointAscending reports whether the blocks, in slice order, are ascending
// and do not overlap: each non-empty block starts at or after the end of the
// previous non-empty one. It allocates nothing and is the fast path of every
// consumer of planBlocks: the output is then the blocks one after the other.
func disjointAscending(blocks []*imageBlock) bool {
	var end uint64
	for _, b := range blocks {
		if b.size == 0 {
			continue
		}
		if b.srcOffset < end {
			return false
		}
		end = saturatingAdd(b.srcOffset, b.size)
	}
	return true
}

// planBlocks coalesces the source ranges of blocks: overlapping and duplicate
// ranges become one, so that every source byte is copied once. Ranges that
// merely touch stay separate, which makes the plan of a set of disjoint blocks
// exactly those blocks in slice order. The copied bytes are at most the union
// of the ranges, so at most the source length once checkBlocks has run.
//
// Reference: libtiff tif_dirwrite.c (TIFFRewriteDirectory keeps the strip data
// where it is and rewrites only the offsets) and Exiv2 TiffImage, which writes
// the data addressed by an entry once and rebases the entries onto it.
func planBlocks(blocks []*imageBlock) blockPlan {
	// The index space is bounded by maxAggregateImageBlocks (262144) < 2^31.
	idx := make([]int32, 0, len(blocks))
	of := make([]int32, len(blocks))
	for i, b := range blocks {
		of[i] = -1
		if b.size > 0 {
			idx = append(idx, int32(i))
		}
	}
	slices.SortFunc(idx, func(a, b int32) int {
		return cmp.Compare(blocks[a].srcOffset, blocks[b].srcOffset)
	})

	// Sweep the blocks by start offset, growing the last range while the next
	// block starts inside it.
	unions := make([]srcRange, 0, len(idx))
	for _, i := range idx {
		b := blocks[i]
		end := saturatingAdd(b.srcOffset, b.size)
		if n := len(unions); n > 0 && b.srcOffset < unions[n-1].end {
			unions[n-1].end = max(unions[n-1].end, end)
		} else {
			unions = append(unions, srcRange{b.srcOffset, end})
		}
		of[i] = int32(len(unions) - 1) //nolint:gosec // G115: bounded by len(blocks)
	}

	// Order the ranges by the first block that uses each.
	place := make([]int32, len(unions))
	for i := range place {
		place[i] = -1
	}
	ranges := make([]srcRange, 0, len(unions))
	for i, u := range of {
		if u < 0 {
			continue
		}
		if place[u] < 0 {
			place[u] = int32(len(ranges)) //nolint:gosec // G115: bounded by len(blocks)
			ranges = append(ranges, unions[u])
		}
		of[i] = place[u]
	}
	return blockPlan{ranges: ranges, of: of}
}
