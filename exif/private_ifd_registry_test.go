package exif

// private_ifd_registry_test.go — task #370, rule R-32 (docs/conformance/
// exif-tiff.md): which IFD0 pointers EncodeRebased carries.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"testing"
)

// ptrSpec is one IFD0 entry of privateStream: n offsets of the given tag and
// type, each addressing a private IFD of its own.
type ptrSpec struct {
	tag uint16
	typ uint16
	n   int
}

// privateStream builds a little-endian classic TIFF stream whose IFD0 holds the
// entries of specs (sorted by tag; one offset inline, several in an array after
// IFD0). Every offset addresses a private IFD of two entries: a BYTE inline with
// the identity of the IFD, and a text of 12 bytes stored after it. It returns
// the stream and, per spec, the offsets written.
func privateStream(specs ...ptrSpec) (stream []byte, offsets [][]uint32) {
	le := binary.LittleEndian
	sorted := append([]ptrSpec(nil), specs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].tag < sorted[j].tag })
	const text = "private-text"
	ifd0 := 8
	pos := ifd0 + 2 + 12*len(sorted) + 4
	arrays := make([]int, len(sorted))
	for i, s := range sorted {
		if s.n > 1 {
			arrays[i] = pos
			pos += 4 * s.n
		}
	}
	const privLen = 2 + 2*12 + 4 + len(text)
	offsets = make([][]uint32, len(sorted))
	privAt := make([][]int, len(sorted))
	for i, s := range sorted {
		for range s.n {
			if pos%2 == 1 {
				pos++
			}
			privAt[i] = append(privAt[i], pos)
			offsets[i] = append(offsets[i], uint32(pos)) //nolint:gosec // G115: tiny test offsets
			pos += privLen
		}
	}
	b := make([]byte, pos)
	copy(b, "II*\x00")
	le.PutUint32(b[4:], uint32(ifd0))
	le.PutUint16(b[ifd0:], uint16(len(sorted))) //nolint:gosec // G115: tiny test counts
	for i, s := range sorted {
		ep := ifd0 + 2 + 12*i
		le.PutUint16(b[ep:], s.tag)
		le.PutUint16(b[ep+2:], s.typ)
		le.PutUint32(b[ep+4:], uint32(s.n)) //nolint:gosec // G115: tiny test counts
		if s.n == 1 {
			le.PutUint32(b[ep+8:], uint32(privAt[i][0])) //nolint:gosec // G115: tiny test offsets
		} else {
			le.PutUint32(b[ep+8:], uint32(arrays[i])) //nolint:gosec // G115: tiny test offsets
			for k, at := range privAt[i] {
				le.PutUint32(b[arrays[i]+4*k:], uint32(at)) //nolint:gosec // G115: tiny test offsets
			}
		}
		for k, at := range privAt[i] {
			le.PutUint16(b[at:], 2)
			le.PutUint16(b[at+2:], 0x0001) // tag
			le.PutUint16(b[at+4:], 1)      // BYTE, inline
			le.PutUint32(b[at+6:], 1)
			b[at+10] = byte(0x10*i + k + 1)
			le.PutUint16(b[at+14:], 0x0002)
			le.PutUint16(b[at+16:], 2) // ASCII out of line
			le.PutUint32(b[at+18:], uint32(len(text)))
			le.PutUint32(b[at+22:], uint32(at+2+2*12+4)) //nolint:gosec // G115: tiny test offsets
			copy(b[at+2+2*12+4:], text)
		}
	}
	return b, offsets
}

// privateEntry reads the BYTE and the text of the private IFD at off of out.
func privateEntry(out []byte, off uint32) (id byte, text string, ok bool) {
	le := binary.LittleEndian
	if int(off)+2+24+4+12 > len(out) || le.Uint16(out[off:]) != 2 {
		return 0, "", false
	}
	id = out[off+2+8]
	tp := le.Uint32(out[off+2+12+8:])
	if int(tp)+12 > len(out) {
		return 0, "", false
	}
	return id, string(out[tp : tp+12]), true
}

// TestPrivateIFD_R32_Registry: the IFDs that the entries of the registry and of
// type IFD address are carried by EncodeRebased, each offset of an array
// separately, and the entries are pointed at the copies; an entry of an unknown
// tag, a LONG with a tag outside the registry, an array longer than the limit and
// a value that does not address an IFD are written as Encode writes them.
func TestPrivateIFD_R32_Registry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		specs []ptrSpec
		carry bool
	}{
		{"GlobalParametersIFD 0x0190", []ptrSpec{{0x0190, 4, 1}}, true},
		{"SubIFDs 0x014A, one", []ptrSpec{{0x014A, 4, 1}}, true},
		{"SubIFDs 0x014A, three", []ptrSpec{{0x014A, 4, 3}}, true},
		{"SubIFDs 0x014A of type IFD", []ptrSpec{{0x014A, 13, 2}}, true},
		{"KodakIFD 0x8290", []ptrSpec{{0x8290, 4, 1}}, true},
		{"LeafSubIFD 0x888A", []ptrSpec{{0x888A, 4, 1}}, true},
		{"KDC_IFD 0xFE00", []ptrSpec{{0xFE00, 4, 1}}, true},
		{"unknown tag of type IFD", []ptrSpec{{0xC000, 13, 1}}, true},
		{"several entries", []ptrSpec{{0x0190, 4, 1}, {0x8290, 4, 1}, {0xC000, 13, 2}}, true},
		{"unknown LONG tag", []ptrSpec{{0xC001, 4, 1}}, false},
		{"known tag of the wrong type", []ptrSpec{{0x8290, 3, 1}}, false},
		{"more offsets than the limit", []ptrSpec{{0x014A, 4, maxPrivateIFDsPerTag + 1}}, false},
	} {
		t.Run("R-32/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src, offsets := privateStream(tc.specs...)
			e, err := Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			out, err := EncodeRebased(e, src)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.carry {
				if !bytes.Equal(out, plain) {
					t.Errorf("EncodeRebased appended %d bytes to what Encode writes", len(out)-len(plain))
				}
				return
			}
			e2, err := Parse(out)
			if err != nil {
				t.Fatalf("Parse of the encoded stream: %v", err)
			}
			sorted := append([]ptrSpec(nil), tc.specs...)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i].tag < sorted[j].tag })
			for i, s := range sorted {
				ent := e2.IFD0.Get(TagID(s.tag))
				if ent == nil || int(ent.Count) != s.n {
					t.Fatalf("tag %#04x: entry %v, want %d offsets", s.tag, ent, s.n)
				}
				for k := range s.n {
					newAt := binary.LittleEndian.Uint32(ent.Value[4*k:])
					if newAt&1 == 1 {
						t.Errorf("tag %#04x offset %d: IFD at the odd offset %d (TIFF 6.0 §2)", s.tag, k, newAt)
					}
					wantID, wantText, _ := privateEntry(src, offsets[i][k])
					id, text, ok := privateEntry(out, newAt)
					if !ok || id != wantID || text != wantText {
						t.Errorf("tag %#04x offset %d: IFD at %d reads (%d, %q, %v), want (%d, %q)", s.tag, k, newAt, id, text, ok, wantID, wantText)
					}
				}
			}
			// Encode alone keeps the source offsets (what the rule repairs).
			if len(out) <= len(plain) {
				t.Errorf("EncodeRebased wrote %d bytes, Encode %d: nothing was carried", len(out), len(plain))
			}
		})
	}
}

// TestPrivateIFD_R32_ExifAndGPSPointersAreNotCarried: the pointers Encode writes
// itself are outside the rule, whatever their type.
func TestPrivateIFD_R32_ExifAndGPSPointersAreNotCarried(t *testing.T) {
	t.Parallel()
	for _, tag := range []TagID{TagExifIFDPointer, TagGPSIFDPointer, TagInteropIFDPointer} {
		t.Run(fmt.Sprintf("R-32/%#04x", uint16(tag)), func(t *testing.T) {
			t.Parallel()
			ent := &IFDEntry{Tag: tag, Type: TypeIFD, Count: 1, Value: []byte{1, 0, 0, 0}}
			if isPrivateIFDPointer(ent) {
				t.Errorf("tag %#04x is carried as a private IFD", uint16(tag))
			}
		})
	}
}
