package exif

// write_thumbnail_align_test.go — regression tests for task #279.
//
// TIFF 6.0 §2 (p. 13): an IFD offset must be even. writeSubIFDs appended an
// odd-length thumbnail directly after its IFD block, so the next IFD started
// at an odd offset while the next-IFD and JPEGInterchangeFormat pointers had
// been computed with ifdTotalSize, which assumes an even start. Pointers were
// written 1 byte off and every IFD behind the thumbnail became unreachable.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

// thumbChainEXIF builds IFD0 followed by one IFD per thumbnail length in
// lens, each IFD carrying a JPEG thumbnail of that length and one RATIONAL
// whose out-of-line value makes the IFD size depend on its start parity.
func thumbChainEXIF(order binary.ByteOrder, bigTIFF bool, lens ...int) (*EXIF, [][]byte) {
	rat := func() []byte {
		v := make([]byte, 8)
		order.PutUint32(v[0:], 72)
		order.PutUint32(v[4:], 1)
		return v
	}
	be := orderIsBig(order)
	long := func(tag TagID) IFDEntry {
		return IFDEntry{Tag: tag, Type: TypeLong, Count: 1, Value: make([]byte, 4), bigEndian: be}
	}
	mkIFD := func(thumb []byte) *IFD {
		ifd := &IFD{Entries: []IFDEntry{
			{Tag: TagXResolution, Type: TypeRational, Count: 1, Value: rat(), bigEndian: be},
			long(TagJPEGInterchangeFormat),
			long(TagJPEGInterchangeFormatLength),
		}, ThumbnailData: thumb}
		sortEntries(ifd.Entries)
		return ifd
	}
	e := &EXIF{ByteOrder: order, BigTIFF: bigTIFF, IFD0: &IFD{Entries: []IFDEntry{
		{Tag: TagXResolution, Type: TypeRational, Count: 1, Value: rat(), bigEndian: be},
	}}}
	thumbs := make([][]byte, len(lens))
	cur := e.IFD0
	for i, n := range lens {
		th := bytes.Repeat([]byte{byte(0xA0 + i)}, n)
		th[0], th[1] = 0xFF, 0xD8
		thumbs[i] = th
		cur.Next = mkIFD(th)
		cur = cur.Next
	}
	return e, thumbs
}

// walkChain follows the next-IFD chain of an encoded stream from IFD0 and
// returns, per IFD, its start offset and the (offset, length) pair of its
// JPEG thumbnail (zero when absent). It fails the test on any offset that is
// odd or out of range.
func walkChain(t *testing.T, data []byte) (starts []uint64, thumbs [][2]uint64) {
	t.Helper()
	var order binary.ByteOrder = binary.LittleEndian
	if data[0] == 'M' {
		order = binary.BigEndian
	}
	big := order.Uint16(data[2:]) == 43
	off := uint64(order.Uint32(data[4:]))
	if big {
		off = order.Uint64(data[8:])
	}
	for off != 0 {
		if off&1 == 1 {
			t.Fatalf("IFD #%d at odd offset %d (TIFF 6.0 §2)", len(starts), off)
		}
		if len(starts) > 64 {
			t.Fatal("IFD chain does not terminate")
		}
		var n, entSz, valAt uint64
		if big {
			n, entSz, valAt = order.Uint64(data[off:]), 20, 12
			off += 8
		} else {
			n, entSz, valAt = uint64(order.Uint16(data[off:])), 12, 8
			off += 2
		}
		var jif, jifLen uint64
		for i := range n {
			e := off + i*entSz
			switch tag := order.Uint16(data[e:]); tag {
			case uint16(TagJPEGInterchangeFormat):
				jif = uint64(order.Uint32(data[e+valAt:]))
			case uint16(TagJPEGInterchangeFormatLength):
				jifLen = uint64(order.Uint32(data[e+valAt:]))
			}
		}
		starts = append(starts, off-map[bool]uint64{true: 8, false: 2}[big])
		thumbs = append(thumbs, [2]uint64{jif, jifLen})
		next := off + n*entSz
		if big {
			off = order.Uint64(data[next:])
		} else {
			off = uint64(order.Uint32(data[next:]))
		}
	}
	return starts, thumbs
}

// TestEncode_OddThumbnailChain (task #279) encodes IFD chains whose
// thumbnails have odd and even lengths in every position and asserts that
// every IFD is reachable, word-aligned, and that each JPEGInterchangeFormat
// pointer addresses its own thumbnail bytes. EncodedSize must equal the
// length Encode produces.
func TestEncode_OddThumbnailChain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		lens []int
	}{
		{"odd-then-ifd", []int{11, 12}},
		{"even-then-ifd", []int{12, 12}},
		{"all-odd", []int{11, 13, 15, 17}},
		{"odd-odd-even-odd", []int{11, 13, 12, 7}},
		{"last-odd", []int{12, 11}},
		{"single-odd", []int{11}},
	}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, big := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%v/bigtiff=%v/%s", order, big, tc.name), func(t *testing.T) {
					t.Parallel()
					e, thumbs := thumbChainEXIF(order, big, tc.lens...)
					enc, err := Encode(e)
					if err != nil {
						t.Fatalf("Encode: %v", err)
					}
					size, err := EncodedSize(e)
					if err != nil {
						t.Fatalf("EncodedSize: %v", err)
					}
					if size != len(enc) {
						t.Errorf("EncodedSize = %d, len(Encode) = %d", size, len(enc))
					}
					starts, jifs := walkChain(t, enc)
					if want := len(tc.lens) + 1; len(starts) != want {
						t.Fatalf("reachable IFDs = %d, want %d", len(starts), want)
					}
					for i, th := range thumbs {
						off, n := jifs[i+1][0], jifs[i+1][1]
						if n != uint64(len(th)) || off+n > uint64(len(enc)) || !bytes.Equal(enc[off:off+n], th) {
							t.Errorf("IFD%d: JPEGInterchangeFormat %d/%d does not address its thumbnail", i+1, off, n)
						}
					}
					// Parse must see the same chain and thumbnails.
					got, err := Parse(enc)
					if err != nil {
						t.Fatalf("Parse: %v", err)
					}
					i := 0
					for ifd := got.IFD0.Next; ifd != nil; ifd = ifd.Next {
						if !bytes.Equal(ifd.ThumbnailData, thumbs[i]) {
							t.Errorf("parsed IFD%d thumbnail differs", i+1)
						}
						i++
					}
					if i != len(thumbs) {
						t.Errorf("parsed %d chained IFDs, want %d", i, len(thumbs))
					}
				})
			}
		}
	}
}
