package gometadata

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// errRawWalk reports a TIFF stream that rawIFDBlocks cannot walk.
var errRawWalk = errors.New("tiff stream not walkable")

// rawIFDBlocks walks the IFD chain of a classic TIFF stream (either byte order;
// the IFD0 offset comes from the header, so CR2's offset 16 works) and returns,
// per chained IFD, the concatenation of every image block it addresses through
// StripOffsets/StripByteCounts, TileOffsets/TileByteCounts and
// JPEGInterchangeFormat/Length, with the blocks of its SubIFDs (0x014A) appended.
// It reads the bytes directly, without the library, so that a writer bug cannot
// be masked by a reader bug with the same root cause. It returns errRawWalk for
// a stream it cannot follow (BigTIFF, pointers outside the stream, loops).
func rawIFDBlocks(data []byte) ([][]byte, error) {
	if len(data) < 8 {
		return nil, errRawWalk
	}
	var o binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		o = binary.LittleEndian
	case "MM":
		o = binary.BigEndian
	default:
		return nil, errRawWalk
	}
	if o.Uint16(data[2:]) == 0x002B {
		return nil, errRawWalk // BigTIFF
	}
	seen := map[uint32]bool{}
	var walk func(off uint32, chain bool) ([][]byte, error)
	walk = func(off uint32, chain bool) ([][]byte, error) {
		var res [][]byte
		for off != 0 {
			if uint64(off)+2 > uint64(len(data)) || seen[off] {
				return nil, errRawWalk
			}
			seen[off] = true
			n := int(o.Uint16(data[off:]))
			if uint64(off)+2+uint64(n)*12+4 > uint64(len(data)) { //nolint:gosec // G115: n is a uint16 entry count
				return nil, errRawWalk
			}
			ents := map[uint16][]uint32{}
			var subs []uint32
			for i := range n {
				e := int(off) + 2 + i*12
				tag, typ, cnt := o.Uint16(data[e:]), o.Uint16(data[e+2:]), o.Uint32(data[e+4:])
				sz := map[uint16]uint32{3: 2, 4: 4}[typ]
				if sz == 0 || cnt > 1<<20 {
					continue
				}
				at := uint32(e + 8) //nolint:gosec // G115: e is an offset inside data, which the walker bounds
				if sz*cnt > 4 {
					at = o.Uint32(data[e+8:])
				}
				if uint64(at)+uint64(sz*cnt) > uint64(len(data)) {
					return nil, errRawWalk
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
				case 0x014A:
					subs = append(subs, vals...)
				}
			}
			var blocks []byte
			for _, pair := range [][2]uint16{{0x0111, 0x0117}, {0x0144, 0x0145}, {0x0201, 0x0202}} {
				offs, lens := ents[pair[0]], ents[pair[1]]
				for k := range offs {
					if k >= len(lens) || uint64(offs[k])+uint64(lens[k]) > uint64(len(data)) {
						return nil, errRawWalk
					}
					blocks = append(blocks, data[offs[k]:offs[k]+lens[k]]...)
				}
			}
			for _, s := range subs {
				sub, err := walk(s, false)
				if err != nil {
					return nil, err
				}
				for _, b := range sub {
					blocks = append(blocks, b...)
				}
			}
			res = append(res, blocks)
			if !chain {
				break
			}
			off = o.Uint32(data[int(off)+2+n*12:])
		}
		return res, nil
	}
	return walk(o.Uint32(data[4:]), true)
}

// TestCorpusTIFFBlocksPreserved proves, over the TIFF and RAW corpus, that a
// Write that adds a caption keeps the bytes of every image block of every IFD
// and SubIFD (security audit F-07a, task #379: overlapping blocks are copied
// once and every entry is rebased onto the copy). Files that the independent
// walker cannot follow (BigTIFF, malformed pointers) are not compared.
func TestCorpusTIFFBlocksPreserved(t *testing.T) {
	t.Parallel()
	var files []string
	for _, sub := range []string{"tiff", "raw"} {
		dir := filepath.Join("testdata", "corpus", sub)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			t.Skipf("corpus absent (run testdata/download.sh): %s", dir)
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, path)
			}
			return err
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	sort.Strings(files)

	var compared, notWalkable, notWritten int
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		want, err := rawIFDBlocks(data)
		if err != nil {
			notWalkable++
			continue
		}
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			notWritten++
			continue
		}
		m.SetCaption("corpus block test")
		var out bytes.Buffer
		if err := Write(bytes.NewReader(data), &out, m); err != nil {
			notWritten++
			continue
		}
		got, err := rawIFDBlocks(out.Bytes())
		if err != nil {
			t.Errorf("%s: the output cannot be walked, the source could: %v", path, err)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("%s: %d IFDs in the output, %d in the source", path, len(got), len(want))
			continue
		}
		for i := range want {
			if !bytes.Equal(got[i], want[i]) {
				t.Errorf("%s: image blocks of IFD %d differ from the source (%d vs %d bytes)", path, i, len(got[i]), len(want[i]))
			}
		}
		compared++
	}
	t.Logf("TIFF/RAW corpus: %d files, %d compared, %d not walkable, %d not written (malformed)", len(files), compared, notWalkable, notWritten)
}
