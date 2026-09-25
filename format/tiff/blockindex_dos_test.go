package tiff

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// buildSubIFDOrphanTileTIFF builds a classic LE TIFF whose IFD0 points
// (0x014A) at one SubIFD carrying n strips (StripOffsets/StripByteCounts,
// all zero) plus a TileOffsets SHORT array of count c with no
// TileByteCounts, so no tile blocks exist for the c array elements.
func buildSubIFDOrphanTileTIFF(n, c int) []byte {
	le := binary.LittleEndian
	type ent struct {
		tag, typ uint16
		cnt, val uint32
	}
	ifd := func(es []ent) []byte {
		b := make([]byte, 2+12*len(es)+4)
		le.PutUint16(b, uint16(len(es))) //nolint:gosec // G115: test fixture, few entries
		for i, e := range es {
			p := 2 + 12*i
			le.PutUint16(b[p:], e.tag)
			le.PutUint16(b[p+2:], e.typ)
			le.PutUint32(b[p+4:], e.cnt)
			le.PutUint32(b[p+8:], e.val)
		}
		return b
	}
	const subOff = 64
	subLen := 2 + 12*3 + 4
	aStrip := subOff + subLen
	aCnt := aStrip + 4*n
	aTile := aCnt + 4*n

	buf := make([]byte, subOff, aTile+2*c)
	copy(buf, "II*\x00")
	le.PutUint32(buf[4:], 8)
	copy(buf[8:], ifd([]ent{{0x0100, 3, 1, 1}, {0x0101, 3, 1, 1}, {0x014A, 4, 1, subOff}}))
	buf = append(buf, ifd([]ent{
		{0x0111, 4, uint32(n), uint32(aStrip)}, //nolint:gosec // G115: test fixture sizes fit uint32
		{0x0117, 4, uint32(n), uint32(aCnt)},   //nolint:gosec // G115: test fixture sizes fit uint32
		{0x0144, 3, uint32(c), uint32(aTile)},  //nolint:gosec // G115: test fixture sizes fit uint32
	})...)
	return append(buf, make([]byte, 8*n+2*c)...)
}

// TestRelocateOrphanTileArrayIsLinear is the regression test for a CPU DoS in
// patchRawIFDOffsets: a SubIFD offset array whose elements have no matching
// image blocks must not trigger a scan of every block per element
// (O(count × blocks)). With 65536 strips and a 2^20-element orphan
// TileOffsets array the quadratic form takes tens of seconds; the linear form
// takes milliseconds. The bound is deliberately generous to stay non-flaky
// under -race and on slow CI machines.
func TestRelocateOrphanTileArrayIsLinear(t *testing.T) {
	t.Parallel()
	src := buildSubIFDOrphanTileTIFF(65536, 1<<20)

	start := time.Now()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, []byte(testXMPPacket), false); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("relocate took %v; per-element block lookup is not O(1)", elapsed)
	}
}

// TestBlockIndexFindMissIsNil verifies that a lookup with no matching block
// returns nil, both for an absent tag and for an index outside the run.
func TestBlockIndexFindMissIsNil(t *testing.T) {
	t.Parallel()
	arr := make([]imageBlock, 4)
	blocks := make([]*imageBlock, len(arr))
	for i := range arr {
		arr[i] = imageBlock{entryTag: exif.TagStripOffsets, index: i}
		blocks[i] = &arr[i]
	}
	x := newBlockIndex(blocks)
	for i := range arr {
		if got := x.find(exif.TagStripOffsets, i); got != &arr[i] {
			t.Fatalf("find(strip, %d) = %p, want %p", i, got, &arr[i])
		}
	}
	for _, tc := range []struct {
		tag   exif.TagID
		index int
	}{
		{exif.TagTileOffsets, 0},
		{exif.TagStripOffsets, 4},
		{exif.TagStripOffsets, -1},
		{exif.TagJPEGInterchangeFormat, 0},
	} {
		if got := x.find(tc.tag, tc.index); got != nil {
			t.Errorf("find(0x%04X, %d) = %p, want nil", tc.tag, tc.index, got)
		}
	}
}
