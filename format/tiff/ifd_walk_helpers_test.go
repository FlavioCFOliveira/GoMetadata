package tiff

// ifd_walk_helpers_test.go — independent IFD-chain walkers shared by the
// relocation regression tests of tasks #279, #280 and #281. They read the
// output bytes directly, without exif.Parse, so a writer bug cannot be masked
// by a reader bug with the same root cause.

import (
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// fixtureEntry is one inline IFD entry: tag, type, count and the raw 4-byte
// value-or-offset field.
type fixtureEntry struct {
	tag, typ uint16
	count    uint32
	val      uint32
}

// putFixtureIFD writes an IFD with inline-only entries at off.
func putFixtureIFD(buf []byte, off int, entries []fixtureEntry, next uint32) {
	o := binary.LittleEndian
	o.PutUint16(buf[off:], uint16(len(entries))) //nolint:gosec // G115: fixtures hold a handful of entries
	p := off + 2
	for _, e := range entries {
		o.PutUint16(buf[p:], e.tag)
		o.PutUint16(buf[p+2:], e.typ)
		o.PutUint32(buf[p+4:], e.count)
		o.PutUint32(buf[p+8:], e.val)
		p += 12
	}
	o.PutUint32(buf[p:], next)
}

// imageBlocksPerIFD walks the IFD chain of a classic little-endian TIFF stream
// (IFD0 offset taken from the header, so CR2's offset 16 works) and returns,
// per chained IFD, the concatenation of every image block it addresses
// through StripOffsets/StripByteCounts, TileOffsets/TileByteCounts and
// JPEGInterchangeFormat/Length. SubIFDs and sub-IFD pointers are followed
// too, folding their blocks into the IFD that references them. It fails the
// test on any pointer outside the stream, any IFD that overruns it, and, when
// strict is set, any IFD at an odd offset.
func imageBlocksPerIFD(t *testing.T, data []byte, strict bool) [][]byte {
	t.Helper()
	o := binary.LittleEndian
	var out [][]byte
	seen := map[uint32]bool{}
	var walk func(off uint32, chain bool) [][]byte
	walk = func(off uint32, chain bool) [][]byte {
		var res [][]byte
		for off != 0 {
			if strict && off&1 == 1 {
				t.Fatalf("IFD at odd offset %d (TIFF 6.0 §2)", off)
			}
			if uint64(off)+2 > uint64(len(data)) || seen[off] {
				t.Fatalf("IFD offset %d outside the stream or revisited (len %d)", off, len(data))
			}
			seen[off] = true
			n := int(o.Uint16(data[off:]))
			if uint64(off)+2+uint64(n)*12+4 > uint64(len(data)) { //nolint:gosec // G115: n is a uint16 entry count
				t.Fatalf("IFD at %d with %d entries overruns the stream (len %d)", off, n, len(data))
			}
			ents := map[uint16][]uint32{}
			var subs []uint32
			for i := range n {
				e := int(off) + 2 + i*12
				tag, typ, cnt := o.Uint16(data[e:]), o.Uint16(data[e+2:]), o.Uint32(data[e+4:])
				sz := map[uint16]uint32{3: 2, 4: 4}[typ]
				if sz == 0 {
					continue
				}
				at := uint32(e + 8) //nolint:gosec // G115: e is an offset inside data, which the walker bounds
				if sz*cnt > 4 {
					at = o.Uint32(data[e+8:])
				}
				if uint64(at)+uint64(sz*cnt) > uint64(len(data)) {
					t.Fatalf("tag 0x%04X value at %d+%d outside the stream", tag, at, sz*cnt)
				}
				vals := make([]uint32, cnt)
				for k := range vals {
					if sz == 2 {
						vals[k] = uint32(o.Uint16(data[at+uint32(k)*2:]))
					} else {
						vals[k] = o.Uint32(data[at+uint32(k)*4:])
					}
				}
				switch tag {
				case 0x0111, 0x0117, 0x0144, 0x0145, 0x0201, 0x0202:
					ents[tag] = vals
				case 0x014A, 0x8769, 0x8825, 0xA005:
					subs = append(subs, vals...)
				}
			}
			var blocks []byte
			for _, pair := range [][2]uint16{{0x0111, 0x0117}, {0x0144, 0x0145}, {0x0201, 0x0202}} {
				offs, lens := ents[pair[0]], ents[pair[1]]
				for k := range offs {
					if k >= len(lens) || uint64(offs[k])+uint64(lens[k]) > uint64(len(data)) {
						t.Fatalf("block %d of tag 0x%04X (%d+%d) outside the stream", k, pair[0], offs[k], lens[k])
					}
					blocks = append(blocks, data[offs[k]:offs[k]+lens[k]]...)
				}
			}
			res = append(res, blocks)
			for _, s := range subs {
				for _, b := range walk(s, false) {
					res[len(res)-1] = append(res[len(res)-1], b...)
				}
			}
			if !chain {
				break
			}
			off = o.Uint32(data[int(off)+2+n*12:])
		}
		return res
	}
	out = walk(o.Uint32(data[4:]), true)
	return out
}

// chainLen returns the number of IFDs chained from IFD0, as exif.Parse sees them.
func chainLen(e *exif.EXIF) int {
	n := 0
	for ifd := e.IFD0; ifd != nil; ifd = ifd.Next {
		n++
	}
	return n
}
