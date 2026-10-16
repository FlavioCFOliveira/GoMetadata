package tiff

// relocate_odd_thumbnail_test.go — regression tests for tasks #279 and #280.
//
// TIFF 6.0 §2: every IFD and every value must begin on a word boundary.
//   - #279: exif.Encode appended an odd-length thumbnail directly after its
//     IFD, so the next IFD started at an odd offset while the pointers to it
//     assumed an even start; every IFD behind the thumbnail became
//     unreachable (corpus file 2b27b742…tif: 23 directories before the write,
//     5 after, per "exiftool -v1").
//   - #280: the relocator derived the SubIFD alignment padding from 0 instead
//     of from the real end of the encoded IFD structure, so with an odd end
//     every image-data offset was one byte early (the strip read "\x00MAIN…").

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// TestRelocate_OddThumbnailWithSubIFD (task #280) relocates a TIFF whose last
// IFD holds a thumbnail of odd or even length together with a SubIFD, and
// requires every image-data offset to read the original bytes.
func TestRelocate_OddThumbnailWithSubIFD(t *testing.T) {
	t.Parallel()
	for _, thumbLen := range []int{11, 12} {
		t.Run(map[int]string{11: "odd", 12: "even"}[thumbLen], func(t *testing.T) {
			t.Parallel()
			main := []byte("MAINMAINMAIN")
			sub := []byte("SUBSUBSUBSUB")
			thumb := append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte{0x77}, thumbLen-2)...)

			// Layout: header | IFD0 | IFD1 | SubIFD | thumb | main | sub.
			ifd0Len := 2 + 5*12 + 4
			ifd1Len := 2 + 2*12 + 4
			subLen := 2 + 2*12 + 4
			ifd0Off := 8
			ifd1Off := ifd0Off + ifd0Len
			subOff := ifd1Off + ifd1Len
			thumbOff := subOff + subLen
			mainOff := thumbOff + len(thumb)
			subDataOff := mainOff + len(main)
			buf := make([]byte, subDataOff+len(sub))
			buf[0], buf[1] = 'I', 'I'
			binary.LittleEndian.PutUint16(buf[2:], 42)
			binary.LittleEndian.PutUint32(buf[4:], uint32(ifd0Off))
			putFixtureIFD(buf, ifd0Off, []fixtureEntry{
				{0x0100, 4, 1, 4}, {0x0101, 4, 1, 1},
				{0x0111, 4, 1, uint32(mainOff)}, {0x0117, 4, 1, uint32(len(main))}, //nolint:gosec // G115: fixture offsets are small
				{0x014A, 4, 1, uint32(subOff)},
			}, uint32(ifd1Off))
			putFixtureIFD(buf, ifd1Off, []fixtureEntry{
				{0x0201, 4, 1, uint32(thumbOff)}, {0x0202, 4, 1, uint32(len(thumb))}, //nolint:gosec // G115: fixture offsets are small
			}, 0)
			putFixtureIFD(buf, subOff, []fixtureEntry{
				{0x0111, 4, 1, uint32(subDataOff)}, {0x0117, 4, 1, uint32(len(sub))}, //nolint:gosec // G115: fixture offsets are small
			}, 0)
			copy(buf[thumbOff:], thumb)
			copy(buf[mainOff:], main)
			copy(buf[subDataOff:], sub)

			e, err := exif.Parse(buf)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			e.SetCopyright("regression #280")
			var out bytes.Buffer
			if err := InjectWithEXIFStream(bytes.NewReader(buf), buf, true, e, nil, nil, &out); err != nil {
				t.Fatalf("InjectWithEXIFStream: %v", err)
			}
			got := imageBlocksPerIFD(t, out.Bytes(), true)
			var flat []byte
			for _, b := range got {
				flat = append(flat, b...)
			}
			for _, want := range [][]byte{main, sub, thumb} {
				if !bytes.Contains(flat, want) {
					t.Errorf("relocated output lost image data %q (thumbLen %d)", want, thumbLen)
				}
			}
			if len(got) != 2 {
				t.Errorf("reachable chained IFDs = %d, want 2 (IFD0, IFD1)", len(got))
			}
			if !bytes.HasPrefix(got[0], main) {
				t.Errorf("IFD0 strip reads %q, want prefix %q", got[0], main)
			}
		})
	}
}

// TestComputeSubIFDsSize_UsesAbsoluteParity (task #280) pins the primitive:
// the SubIFD padding depends on the parity of ifdEnd.
func TestComputeSubIFDsSize_UsesAbsoluteParity(t *testing.T) {
	t.Parallel()
	subs := []*subIFDInfo{{rawBytes: make([]byte, 18)}, {rawBytes: make([]byte, 7)}, {rawBytes: make([]byte, 4)}}
	for _, ifdEnd := range []uint64{100, 101} {
		got := computeSubIFDsSize(ifdEnd, subs)
		// Replay by hand: pad to even before each block.
		pos := ifdEnd
		for _, s := range subs {
			pos += pos & 1
			pos += uint64(len(s.rawBytes))
		}
		if want := pos - ifdEnd; got != want {
			t.Errorf("computeSubIFDsSize(%d) = %d, want %d", ifdEnd, got, want)
		}
	}
}

// TestInject_CorpusIFDChainPreserved (tasks #279, #280, corpus): for every
// corpus TIFF/DNG/RAW that Inject accepts, the number of chained IFDs after the
// write equals the number before it. Before the fix, 11 corpus files lost
// their IFD chain behind an odd-length thumbnail (23 IFDs before, 5 after).
func TestInject_CorpusIFDChainPreserved(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "testdata", "corpus")
	var files []string
	for _, dir := range []string{"tiff", "raw"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				switch strings.ToLower(filepath.Ext(p)) {
				case ".tif", ".tiff", ".dng":
					files = append(files, p)
				}
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("corpus TIFF files absent (see testdata/download.sh)")
	}
	checked := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil || len(data) > 64<<20 {
			continue
		}
		before, err := exif.Parse(data)
		if err != nil || before.IFD0 == nil {
			continue
		}
		want := chainLen(before)
		var out bytes.Buffer
		again, _ := exif.Parse(data)
		again.SetCopyright("regression #279")
		if err := InjectWithEXIFStream(bytes.NewReader(data), data, true, again, nil, nil, &out); err != nil {
			continue // rejected inputs (malformed fixtures) are out of scope here
		}
		after, err := exif.Parse(out.Bytes())
		if err != nil {
			t.Errorf("%s: output does not parse: %v", filepath.Base(f), err)
			continue
		}
		if got := chainLen(after); got != want {
			t.Errorf("%s: %d chained IFDs after Inject, want %d", filepath.Base(f), got, want)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no corpus file was checked")
	}
}
