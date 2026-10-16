package tiff

// tiffasm_test.go — a small classic-TIFF assembler and a directory-tree
// walker shared by the task #310/#311/#314 tests. The assembler lays out IFD
// trees (chain, SubIFD arrays, SubIFD next-chains, Exif/GPS pointers) with
// strip data, optionally placing an IFD's out-of-line values BEFORE its
// directory, as libtiff does. The walker reads any classic TIFF and returns
// every directory it reaches with a SHA-256 of the image data each one
// addresses, so a test can compare the structure of an input and an output.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// tEntry is one IFD entry of the assembler. val holds the value bytes (in the
// file byte order); when ptrs is non-nil the value is instead the offsets of
// those nodes (4 bytes each), written once the layout is known.
type tEntry struct {
	tag   uint16
	typ   uint16
	count uint32
	val   []byte
	ptrs  []*tNode
}

// tNode is one IFD of the assembler.
type tNode struct {
	entries   []tEntry
	next      *tNode
	strip     []byte // when non-nil, StripOffsets/StripByteCounts are added
	valsFirst bool   // place the out-of-line values before the directory

	off      uint32 // assigned directory offset
	valOff   []uint32
	stripOff uint32
}

// tAsm assembles a classic TIFF stream.
type tAsm struct {
	order binary.ByteOrder
	head  []byte // bytes 8.. of the header (CR2 marker); nil for plain TIFF
	nodes []*tNode
}

// add registers n (and, recursively, the nodes it points to) in placement
// order.
func (a *tAsm) add(n *tNode) {
	if slices.Contains(a.nodes, n) {
		return
	}
	a.nodes = append(a.nodes, n)
	for _, e := range n.entries {
		for _, p := range e.ptrs {
			a.add(p)
		}
	}
	if n.next != nil {
		a.add(n.next)
	}
}

// sortedEntries returns the entries of n, with the strip pair when n has a
// strip, sorted by tag.
func (n *tNode) sortedEntries() []tEntry {
	es := slices.Clone(n.entries)
	if n.strip != nil {
		es = append(es,
			tEntry{tag: uint16(exif.TagStripOffsets), typ: uint16(exif.TypeLong), count: 1},
			tEntry{tag: uint16(exif.TagStripByteCounts), typ: uint16(exif.TypeLong), count: 1, val: u32(binary.LittleEndian, uint32(len(n.strip)))}) //nolint:gosec // G115: test sizes are small
	}
	slices.SortFunc(es, func(x, y tEntry) int { return int(x.tag) - int(y.tag) })
	return es
}

func u32(order binary.ByteOrder, v uint32) []byte {
	b := make([]byte, 4)
	order.PutUint32(b, v)
	return b
}

// valueLen is the byte size of the value of e.
func (e tEntry) valueLen() int {
	if e.ptrs != nil {
		return 4 * len(e.ptrs)
	}
	return len(e.val)
}

// bytes lays out and returns the TIFF stream whose IFD0 is root.
func (a *tAsm) bytes(t *testing.T, root *tNode) []byte {
	t.Helper()
	a.nodes = nil
	a.add(root)
	pos := uint32(8 + len(a.head)) //nolint:gosec // G115: test sizes are small
	even := func() {
		pos += pos & 1
	}
	// Layout pass.
	for _, n := range a.nodes {
		es := n.sortedEntries()
		n.valOff = make([]uint32, len(es))
		placeVals := func() {
			for i, e := range es {
				if e.valueLen() > 4 {
					even()
					n.valOff[i] = pos
					pos += uint32(e.valueLen()) //nolint:gosec // G115: test sizes are small
				}
			}
		}
		if n.valsFirst {
			placeVals()
		}
		even()
		n.off = pos
		pos += uint32(2 + 12*len(es) + 4) //nolint:gosec // G115: test sizes are small
		if !n.valsFirst {
			placeVals()
		}
		if n.strip != nil {
			even()
			n.stripOff = pos
			pos += uint32(len(n.strip)) //nolint:gosec // G115: test sizes are small
		}
	}
	buf := make([]byte, pos)
	if a.order == binary.LittleEndian {
		copy(buf, "II")
	} else {
		copy(buf, "MM")
	}
	a.order.PutUint16(buf[2:], 0x002A)
	copy(buf[8:], a.head)
	a.order.PutUint32(buf[4:], root.off)
	// Write pass.
	for _, n := range a.nodes {
		es := n.sortedEntries()
		a.order.PutUint16(buf[n.off:], uint16(len(es))) //nolint:gosec // G115: test sizes are small
		for i, e := range es {
			at := int(n.off) + 2 + 12*i
			a.order.PutUint16(buf[at:], e.tag)
			a.order.PutUint16(buf[at+2:], e.typ)
			a.order.PutUint32(buf[at+4:], e.count)
			val := e.val
			switch {
			case e.ptrs != nil:
				val = nil
				for _, p := range e.ptrs {
					val = append(val, u32(a.order, p.off)...)
				}
			case e.tag == uint16(exif.TagStripOffsets) && n.strip != nil:
				val = u32(a.order, n.stripOff)
			case e.tag == uint16(exif.TagStripByteCounts) && n.strip != nil:
				val = u32(a.order, uint32(len(n.strip))) //nolint:gosec // G115: test sizes are small
			}
			if len(val) > 4 {
				a.order.PutUint32(buf[at+8:], n.valOff[i])
				copy(buf[n.valOff[i]:], val)
			} else {
				copy(buf[at+8:], val)
			}
		}
		if n.next != nil {
			a.order.PutUint32(buf[int(n.off)+2+12*len(es):], n.next.off)
		}
		copy(buf[n.stripOff:], n.strip)
	}
	return buf
}

// asciiEntry returns a NUL-terminated ASCII entry.
func asciiEntry(tag uint16, s string) tEntry {
	v := append([]byte(s), 0)
	return tEntry{tag: tag, typ: uint16(exif.TypeASCII), count: uint32(len(v)), val: v} //nolint:gosec // G115: test sizes are small
}

// ptrEntry returns an entry whose value is the offsets of nodes, of type typ.
func ptrEntry(tag, typ uint16, nodes ...*tNode) tEntry {
	return tEntry{tag: tag, typ: typ, count: uint32(len(nodes)), ptrs: nodes} //nolint:gosec // G115: test sizes are small
}

// dirTree walks the classic TIFF b and returns one line per directory
// reached — its path, entry count and the SHA-256 of the strip/tile/JPEG data
// it addresses — following the IFD chain, SubIFD arrays (0x014A, any of the
// LONG/IFD types), SubIFD next-chains and the Exif/GPS/Interop pointers, the
// directories "exiftool -v1" lists. type 13 is the 4-byte IFD type here.
func dirTree(t *testing.T, b []byte) []string {
	t.Helper()
	order, err := byteOrder(b)
	if err != nil {
		t.Fatalf("dirTree: %v", err)
	}
	var out []string
	seen := map[uint32]bool{}
	var walk func(off uint32, path string, chain bool, depth int)
	walk = func(off uint32, path string, chain bool, depth int) {
		for k := 0; off != 0 && depth < 10 && k < 64; k++ {
			p := path
			if chain {
				p = fmt.Sprintf("%s%d", path, k)
			}
			if seen[off] || uint64(off)+2 > uint64(len(b)) {
				out = append(out, p+" BAD")
				return
			}
			seen[off] = true
			n := int(order.Uint16(b[off:]))
			if int(off)+2+12*n+4 > len(b) {
				out = append(out, p+" TRUNCATED")
				return
			}
			vals := map[uint16][]uint32{}
			for i := range n {
				e := int(off) + 2 + 12*i
				tag, typ, cnt := order.Uint16(b[e:]), order.Uint16(b[e+2:]), order.Uint32(b[e+4:])
				sz := typeSize(typ)
				if sz != 2 && sz != 4 {
					continue
				}
				total := uint64(sz) * uint64(cnt)
				at := uint64(e + 8)
				if total > 4 {
					at = uint64(order.Uint32(b[e+8:]))
				}
				if at+total > uint64(len(b)) {
					vals[tag] = nil
					continue
				}
				for j := range uint64(cnt) {
					if sz == 2 {
						vals[tag] = append(vals[tag], uint32(order.Uint16(b[at+2*j:])))
					} else {
						vals[tag] = append(vals[tag], order.Uint32(b[at+4*j:]))
					}
				}
			}
			h := sha256.New()
			for _, pair := range [][2]uint16{{0x0111, 0x0117}, {0x0144, 0x0145}, {0x0201, 0x0202}} {
				offs, lens := vals[pair[0]], vals[pair[1]]
				for j := range min(len(offs), len(lens)) {
					if uint64(offs[j])+uint64(lens[j]) <= uint64(len(b)) {
						_, _ = h.Write(b[offs[j] : offs[j]+lens[j]])
					}
				}
			}
			out = append(out, fmt.Sprintf("%s entries=%d data=%s", p, n, hex.EncodeToString(h.Sum(nil))[:12]))
			for _, sub := range []struct {
				tag  uint16
				name string
			}{{0x8769, "Exif"}, {0x8825, "GPS"}, {0xA005, "Interop"}} {
				if v := vals[sub.tag]; len(v) == 1 {
					walk(v[0], p+"/"+sub.name, false, depth+1)
				}
			}
			for j, s := range vals[0x014A] {
				walk(s, fmt.Sprintf("%s/Sub%d.", p, j), true, depth+1)
			}
			if !chain {
				return
			}
			nextAt := int(off) + 2 + 12*n
			off = order.Uint32(b[nextAt:])
		}
	}
	walk(order.Uint32(b[4:]), "IFD", true, 0)
	return out
}
