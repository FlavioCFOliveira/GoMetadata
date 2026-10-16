package tiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"testing"
)

// blk builds an image block for the layout tests.
func blk(src, size uint64) *imageBlock { return &imageBlock{srcOffset: src, size: size} }

// TestPlanBlocks proves the coalescing of source ranges (security audit F-07a,
// task #379): overlapping and duplicate ranges are copied once, disjoint ranges
// keep their slice order, and touching ranges are not merged.
func TestPlanBlocks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		blocks []*imageBlock
		ranges []srcRange
		of     []int32
	}{
		{"empty", nil, []srcRange{}, []int32{}},
		{"disjoint ascending", []*imageBlock{blk(10, 5), blk(20, 5)}, []srcRange{{10, 15}, {20, 25}}, []int32{0, 1}},
		{"disjoint descending keeps slice order", []*imageBlock{blk(20, 5), blk(10, 5)}, []srcRange{{20, 25}, {10, 15}}, []int32{0, 1}},
		{"touching are not merged", []*imageBlock{blk(10, 5), blk(15, 5)}, []srcRange{{10, 15}, {15, 20}}, []int32{0, 1}},
		{"duplicate", []*imageBlock{blk(10, 5), blk(10, 5), blk(10, 5)}, []srcRange{{10, 15}}, []int32{0, 0, 0}},
		{"strip with tiles inside (Sony shape)", []*imageBlock{blk(100, 40), blk(100, 10), blk(110, 10), blk(130, 10)}, []srcRange{{100, 140}}, []int32{0, 0, 0, 0}},
		{"partial overlap", []*imageBlock{blk(10, 10), blk(15, 10)}, []srcRange{{10, 25}}, []int32{0, 0}},
		{"overlap listed after a later range", []*imageBlock{blk(50, 5), blk(10, 10), blk(15, 10)}, []srcRange{{50, 55}, {10, 25}}, []int32{0, 1, 1}},
		{"chain merges transitively", []*imageBlock{blk(30, 10), blk(10, 10), blk(15, 20)}, []srcRange{{10, 40}}, []int32{0, 0, 0}},
		{"empty blocks have no range", []*imageBlock{blk(10, 0), blk(10, 5), blk(12, 0)}, []srcRange{{10, 15}}, []int32{-1, 0, -1}},
		{"end past MaxUint64 saturates", []*imageBlock{blk(math.MaxUint64-1, 10)}, []srcRange{{math.MaxUint64 - 1, math.MaxUint64}}, []int32{0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := planBlocks(tc.blocks)
			if !slices.Equal(p.ranges, tc.ranges) {
				t.Errorf("ranges = %v, want %v", p.ranges, tc.ranges)
			}
			if !slices.Equal(p.of, tc.of) {
				t.Errorf("of = %v, want %v", p.of, tc.of)
			}
		})
	}
}

// TestAssignNewOffsetsOverlap proves that every block of an overlapping set is
// rebased into the single copy of its range, at its source distance from the
// range start, and that disjoint sets are laid out as before.
func TestAssignNewOffsetsOverlap(t *testing.T) {
	t.Parallel()

	t.Run("strip and tiles over the same bytes", func(t *testing.T) {
		t.Parallel()
		blocks := []*imageBlock{blk(1000, 400), blk(1000, 100), blk(1100, 100), blk(1300, 100), blk(5000, 10)}
		assignNewOffsets(blocks, 64)
		want := []uint64{64, 64, 164, 364, 464}
		for i, b := range blocks {
			if b.newOffset != want[i] {
				t.Errorf("block %d newOffset = %d, want %d", i, b.newOffset, want[i])
			}
		}
		if end := relocatedEnd(64, blocks); end != 474 {
			t.Errorf("relocatedEnd = %d, want 474 (400 + 10 copied bytes after 64)", end)
		}
	})

	t.Run("disjoint descending keeps the slice order", func(t *testing.T) {
		t.Parallel()
		blocks := []*imageBlock{blk(500, 10), blk(100, 20), blk(0, 0), blk(300, 5)}
		assignNewOffsets(blocks, 8)
		want := []uint64{8, 18, 38, 38}
		for i, b := range blocks {
			if b.newOffset != want[i] {
				t.Errorf("block %d newOffset = %d, want %d", i, b.newOffset, want[i])
			}
		}
		if end := relocatedEnd(8, blocks); end != 43 {
			t.Errorf("relocatedEnd = %d, want 43", end)
		}
	})

	t.Run("saturates past 4 GiB", func(t *testing.T) {
		t.Parallel()
		blocks := []*imageBlock{blk(100, 10), blk(0, 20), blk(5, 20)}
		assignNewOffsets(blocks, math.MaxUint32-15)
		if blocks[2].newOffset != math.MaxUint32 || blocks[1].newOffset != math.MaxUint32 {
			t.Errorf("newOffsets = %d, %d, want MaxUint32 for the range that does not fit", blocks[1].newOffset, blocks[2].newOffset)
		}
	})
}

// TestWriteHeaderAndBlocksOverlap proves that overlapping blocks are written
// as their coalesced union, from a whole-file prefix and from a stream, and that
// a block outside the source is still rejected.
func TestWriteHeaderAndBlocksOverlap(t *testing.T) {
	t.Parallel()
	src := make([]byte, 200)
	for i := range src {
		src[i] = byte(i*7 + 3)
	}
	header := []byte("HDR")
	blocks := []*imageBlock{blk(100, 60), blk(100, 20), blk(140, 20), blk(10, 5), blk(100, 60)}
	want := slices.Concat(header, src[100:160], src[10:15])

	for _, whole := range []bool{true, false} {
		var out bytes.Buffer
		prefix := src
		if !whole {
			prefix = src[:8]
		}
		if err := writeHeaderAndBlocks(bytes.NewReader(src), &out, header, blocks, prefix, whole); err != nil {
			t.Fatalf("wholeFile=%v: %v", whole, err)
		}
		if !bytes.Equal(out.Bytes(), want) {
			t.Errorf("wholeFile=%v: output differs from header + union", whole)
		}
	}

	var out bytes.Buffer
	err := writeHeaderAndBlocks(bytes.NewReader(src), &out, header, []*imageBlock{blk(150, 100), blk(150, 10)}, src, true)
	if !errors.Is(err, ErrBlockOutOfBounds) {
		t.Errorf("block past the end of the source: error = %v, want ErrBlockOutOfBounds", err)
	}
	if out.Len() != 0 {
		t.Errorf("wrote %d bytes before rejecting an out-of-bounds block", out.Len())
	}
}

// auditOverlapTiles builds the shape of a Sony ARW: one StripOffsets strip of
// tiles*tileLen bytes and TileOffsets tiles over the same bytes. The data bytes
// are distinct so that a misplaced tile is detected.
func auditOverlapTiles(tiles, tileLen int) (file []byte, dataStart int) {
	le := binary.LittleEndian
	const nent = 5
	ifd0 := 8
	tileOffArr := ifd0 + 2 + nent*12 + 4
	tileCntArr := tileOffArr + 4*tiles
	dataStart = tileCntArr + 4*tiles
	b := make([]byte, dataStart+tiles*tileLen)
	copy(b, "II*\x00")
	le.PutUint32(b[4:], uint32(ifd0))
	le.PutUint16(b[ifd0:], nent)
	put := func(i int, tag uint16, cnt, val uint32) { // every entry is a LONG (type 4)
		e := ifd0 + 2 + i*12
		le.PutUint16(b[e:], tag)
		le.PutUint16(b[e+2:], 4)
		le.PutUint32(b[e+4:], cnt)
		le.PutUint32(b[e+8:], val)
	}
	put(0, 0x0111, 1, uint32(dataStart))              //nolint:gosec // G115: test sizes are small
	put(1, 0x0116, 1, uint32(tiles*tileLen))          //nolint:gosec // G115: test sizes are small
	put(2, 0x0117, 1, uint32(tiles*tileLen))          //nolint:gosec // G115: test sizes are small
	put(3, 0x0144, uint32(tiles), uint32(tileOffArr)) //nolint:gosec // G115: test sizes are small
	put(4, 0x0145, uint32(tiles), uint32(tileCntArr)) //nolint:gosec // G115: test sizes are small
	for i := range tiles {
		le.PutUint32(b[tileOffArr+4*i:], uint32(dataStart+i*tileLen)) //nolint:gosec // G115: test sizes are small
		le.PutUint32(b[tileCntArr+4*i:], uint32(tileLen))             //nolint:gosec // G115: test sizes are small
	}
	for i := dataStart; i < len(b); i++ {
		b[i] = byte(i*131 + i>>8) //nolint:gosec // G115: the low byte of a pattern is the point
	}
	return b, dataStart
}

// ifd0Longs returns the LONG values of tag in IFD0 of the little-endian TIFF b.
func ifd0Longs(t *testing.T, b []byte, tag uint16) []uint32 {
	t.Helper()
	le := binary.LittleEndian
	ifd0 := int(le.Uint32(b[4:]))
	n := int(le.Uint16(b[ifd0:]))
	for i := range n {
		e := ifd0 + 2 + i*12
		if le.Uint16(b[e:]) != tag {
			continue
		}
		cnt := int(le.Uint32(b[e+4:]))
		at := e + 8
		if cnt > 1 {
			at = int(le.Uint32(b[e+8:]))
		}
		vals := make([]uint32, cnt)
		for k := range vals {
			vals[k] = le.Uint32(b[at+4*k:])
		}
		return vals
	}
	t.Fatalf("tag 0x%04X not found in IFD0", tag)
	return nil
}
