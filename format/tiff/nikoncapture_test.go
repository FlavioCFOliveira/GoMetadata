package tiff

// nikoncapture_test.go — regression tests for task #322: the IPTC copy in the
// Nikon Capture data (MakerNote tag 0x0E01) is rewritten with the IIM that a
// NEF write stores in tag 0x83BB, and nothing else in the MakerNote or in the
// Nikon Capture data changes (layout: see nikoncapture.go).

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

var (
	ncOldIIM = append([]byte{0x1C, 0x02, 0x78, 0x00, 0x07}, []byte("old cap")...)   //nolint:gochecknoglobals // immutable test fixture
	ncNewIIM = append([]byte{0x1C, 0x02, 0x78, 0x00, 0x09}, []byte("new! text")...) //nolint:gochecknoglobals // immutable test fixture
)

// ncEntry builds one Nikon Capture entry: tag, 14 bytes of its own, size, data.
func ncEntry(tag uint32, data []byte) []byte {
	b := binary.LittleEndian.AppendUint32(nil, tag)
	b = append(b, bytes.Repeat([]byte{0xEE}, 14)...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)+4)) //nolint:gosec // G115: test sizes are small
	return append(b, data...)
}

// ncData builds a Nikon Capture data block. variant 0: size S counts the size
// field and the entries; 1: S also counts the header (ViewNX 2.1.1); pad is
// appended after the block.
func ncData(variant int, pad int, entries ...[]byte) []byte {
	body := bytes.Join(entries, nil)
	b := binary.LittleEndian.AppendUint32(nil, nikonCaptureBlockTag)
	b = append(b, bytes.Repeat([]byte{0xDD}, 14)...)
	s := uint32(len(body) + 4) //nolint:gosec // G115: test sizes are small
	if variant == 1 {
		s += nikonCaptureHeader
	}
	b = binary.LittleEndian.AppendUint32(b, s)
	b = append(b, body...)
	return append(b, bytes.Repeat([]byte{0x99}, pad)...)
}

// ncCapacity returns n bytes whose first bytes are iim and the rest zero.
func ncCapacity(iim []byte, n int) []byte {
	return append(bytes.Clone(iim), make([]byte, n-len(iim))...)
}

// nikonBlob builds a Nikon Type-3 MakerNote (big-endian embedded TIFF header,
// version 0x0210) with the IFD: tag 0x0001 (inline), tag 0x0E01 (data at
// offset), tag 0x0011 (a long offset into the blob, after the data) and an
// out-of-line value after the Nikon Capture data. It returns the blob and
// the positions that must survive a rewrite.
func nikonBlob(capture []byte) (blob []byte, after int) {
	order := binary.BigEndian
	hdr := 10
	b := []byte("Nikon\x00\x02\x10\x00\x00")
	b = append(b, 'M', 'M', 0, 42, 0, 0, 0, 8)
	b = order.AppendUint16(b, 3)
	ifdAt := len(b)
	b = append(b, make([]byte, 3*12+4)...)
	dataOff := len(b) - hdr
	b = append(b, capture...)
	if len(b)&1 == 1 {
		b = append(b, 0)
	}
	after = len(b)
	b = append(b, []byte("TAIL-VALUE-AFTER")...)
	put := func(i int, tag, typ uint16, count, val uint32) {
		at := ifdAt + 12*i
		order.PutUint16(b[at:], tag)
		order.PutUint16(b[at+2:], typ)
		order.PutUint32(b[at+4:], count)
		order.PutUint32(b[at+8:], val)
	}
	put(0, 0x0001, 7, 4, 0x30323130)
	put(1, 0x0011, 4, 1, uint32(after-hdr)) //nolint:gosec // G115: test sizes are small
	put(2, 0x0E01, 7, uint32(len(capture)), uint32(dataOff))
	return b, after
}

// captureOf returns the Nikon Capture data and the IPTC entry data of blob.
func captureOf(t *testing.T, blob []byte) (data, iptc []byte) {
	t.Helper()
	hdr, order := findNikonMNTIFFHeader(blob)
	if order == nil {
		t.Fatal("no embedded TIFF header")
	}
	ifd := hdr + int(order.Uint32(blob[hdr+4:]))
	n := int(order.Uint16(blob[ifd:]))
	for i := range n {
		at := ifd + 2 + 12*i
		if order.Uint16(blob[at:]) == 0x0E01 {
			cnt, off := int(order.Uint32(blob[at+4:])), hdr+int(order.Uint32(blob[at+8:]))
			data = blob[off : off+cnt]
			p, c, ok := findNikonCaptureIPTC(data)
			if !ok {
				t.Fatal("no IPTC entry in the Nikon Capture data")
			}
			return data, data[p : p+c]
		}
	}
	t.Fatal("no tag 0x0E01")
	return nil, nil
}

func TestRewriteNikonMakerNoteIPTC(t *testing.T) {
	t.Parallel()
	t.Run("IIM-COPY-03", func(t *testing.T) {
		t.Parallel()
		other1, other2 := ncEntry(0x11111111, []byte("first")), ncEntry(0x22222222, []byte("second entry"))
		for _, variant := range []int{0, 1} {
			for _, pad := range []int{0, 6} {
				if variant == 1 && pad > 0 {
					continue // the size counts the whole block: nothing follows it
				}
				name := map[int]string{0: "S counts entries", 1: "S counts the header"}[variant]
				t.Run(fmt.Sprintf("%s/pad %d", name, pad), func(t *testing.T) {
					t.Parallel()
					t.Run("in place", func(t *testing.T) {
						t.Parallel()
						cap0 := ncEntry(nikonCaptureIPTCTag, ncCapacity(ncOldIIM, 40))
						blob, after := nikonBlob(ncData(variant, pad, other1, cap0, other2))
						orig := bytes.Clone(blob)
						out, ok := rewriteNikonMakerNoteIPTC(blob, ncNewIIM)
						if !ok {
							t.Fatal("not rewritten")
						}
						if !bytes.Equal(blob, orig) {
							t.Fatal("the input blob was modified")
						}
						if len(out) != len(blob) {
							t.Errorf("blob length %d, want %d", len(out), len(blob))
						}
						_, iptc := captureOf(t, out)
						if !bytes.Equal(iptc, ncCapacity(ncNewIIM, 40)) {
							t.Errorf("IPTC entry = %x", iptc)
						}
						// Only the bytes of the IPTC entry data differ.
						diff := 0
						for i := range out {
							if out[i] != blob[i] {
								diff++
							}
						}
						if diff > 40 || !bytes.Equal(out[after:], blob[after:]) {
							t.Errorf("%d bytes differ, or the data after the Nikon Capture data changed", diff)
						}
					})
					t.Run("grows", func(t *testing.T) {
						t.Parallel()
						cap0 := ncEntry(nikonCaptureIPTCTag, ncCapacity(ncOldIIM, 14))
						blob, after := nikonBlob(ncData(variant, pad, other1, cap0, other2))
						long := append(bytes.Clone(ncNewIIM), bytes.Repeat([]byte("L"), 100)...)
						out, ok := rewriteNikonMakerNoteIPTC(blob, long)
						if !ok {
							t.Fatal("not rewritten")
						}
						data, iptc := captureOf(t, out)
						if !bytes.Equal(iptc, long) {
							t.Errorf("IPTC entry = %q", iptc)
						}
						// The other entries and the padding survive, and every size is consistent.
						if !bytes.Contains(data, other1) || !bytes.Contains(data, other2) {
							t.Error("another Nikon Capture entry changed")
						}
						if p, c, ok := findNikonCaptureIPTC(data); !ok || p+c > len(data) {
							t.Error("the rebuilt Nikon Capture data does not parse")
						}
						if pad > 0 && !bytes.HasSuffix(data, bytes.Repeat([]byte{0x99}, pad)) {
							t.Error("the padding after the block was lost")
						}
						// Nothing else in the MakerNote moved: the prefix up to the data and the bytes after it are unchanged except the entry itself.
						if !bytes.Equal(out[after:len(blob)], blob[after:]) {
							t.Error("the bytes after the old data moved")
						}
						hdr, order := findNikonMNTIFFHeader(out)
						ifd := hdr + int(order.Uint32(out[hdr+4:]))
						for i := range 2 { // tags 0x0001 and 0x0011
							at := ifd + 2 + 12*i
							if !bytes.Equal(out[at:at+12], blob[at:at+12]) {
								t.Errorf("IFD entry %d changed", i)
							}
						}
						if cnt := int(order.Uint32(out[ifd+2+24+4:])); cnt != len(data) {
							t.Errorf("tag 0x0E01 count = %d, want %d", cnt, len(data))
						}
					})
				})
			}
		}
	})
}

func TestRewriteNikonMakerNoteIPTC_Untouched(t *testing.T) {
	t.Parallel()
	good := ncData(0, 0, ncEntry(nikonCaptureIPTCTag, ncCapacity(ncOldIIM, 30)))
	noIPTC := ncData(0, 0, ncEntry(0x33333333, []byte("x")))
	badSize := bytes.Clone(good)
	binary.LittleEndian.PutUint32(badSize[nikonCaptureHeader:], 5000)
	badEntry := bytes.Clone(good)
	binary.LittleEndian.PutUint32(badEntry[nikonCaptureEntryHeader+nikonCaptureHeader:], 9999)
	badTag := bytes.Clone(good)
	badTag[0] ^= 0xFF
	for name, capture := range map[string][]byte{
		"no IPTC entry":      noIPTC,
		"block size wrong":   badSize,
		"entry size wrong":   badEntry,
		"not a capture data": badTag,
		"too short":          good[:10],
	} {
		blob, _ := nikonBlob(capture)
		if out, ok := rewriteNikonMakerNoteIPTC(blob, ncNewIIM); ok {
			t.Errorf("%s: rewritten (%d bytes)", name, len(out))
		}
	}
	// The IIM it already holds, and a blob that is not a Nikon Type-3 MakerNote.
	blob, _ := nikonBlob(good)
	if _, ok := rewriteNikonMakerNoteIPTC(blob, ncOldIIM); ok {
		t.Error("the IIM that the entry already holds was rewritten")
	}
	if _, ok := rewriteNikonMakerNoteIPTC([]byte("not a nikon maker note at all"), ncNewIIM); ok {
		t.Error("a foreign MakerNote was rewritten")
	}
	rewriteNikonCaptureIPTC(nil, ncNewIIM)
	rewriteNikonCaptureIPTC(&exif.EXIF{}, ncNewIIM)
}

// TestNEF_NikonCaptureCorpus runs the reproduction of task #322 on the corpus
// NEF: after a write, the Nikon Capture copy equals the IIM of tag 0x83BB.
func TestNEF_NikonCaptureCorpus(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "testdata", "corpus", "raw", "exiftool", "Nikon.nef")
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	e, err := exif.Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for name, iim := range map[string][]byte{
		"fits":      {0x1C, 0x02, 0x78, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'},
		"overflows": append([]byte{0x1C, 0x02, 0x78, 0x01, 0x90}, bytes.Repeat([]byte("x"), 400)...),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := InjectWithEXIFNEFStream(bytes.NewReader(src), src, true, nil, iim, nil, &out); err != nil {
				t.Fatalf("InjectWithEXIFNEFStream: %v", err)
			}
			got, err := exif.Parse(out.Bytes())
			if err != nil {
				t.Fatalf("Parse output: %v", err)
			}
			_, iptc := captureOf(t, got.ExifIFD.Get(exif.TagMakerNote).Value)
			if !bytes.HasPrefix(iptc, iim) || len(bytes.TrimRight(iptc[len(iim):], "\x00")) != 0 {
				t.Errorf("Nikon Capture IPTC copy = %q, want the written IIM followed by zero bytes", iptc)
			}
			// The rest of the MakerNote parses as before.
			if len(got.MakerNote) == 0 || len(got.ExifIFD.Entries) != len(e.ExifIFD.Entries) {
				t.Error("the Exif IFD changed shape")
			}
		})
	}
}

// FuzzRewriteNikonMakerNoteIPTC: the blob is untrusted; the rewrite must not
// panic, must not modify its input, and a rewritten blob must read back with
// the new IIM.
func FuzzRewriteNikonMakerNoteIPTC(f *testing.F) {
	cap0 := ncEntry(nikonCaptureIPTCTag, ncCapacity(ncOldIIM, 40))
	for _, v := range []int{0, 1} {
		blob, _ := nikonBlob(ncData(v, 0, ncEntry(0x11111111, []byte("a")), cap0))
		f.Add(blob, 5)
		f.Add(blob, 100)
	}
	f.Fuzz(func(t *testing.T, blob []byte, n int) {
		n = min(max(n, 1), 300)
		iim := append([]byte{0x1C, 0x02, 0x78, 0x00, 0x00}, bytes.Repeat([]byte("z"), n)...)
		orig := bytes.Clone(blob)
		out, ok := rewriteNikonMakerNoteIPTC(blob, iim)
		if !bytes.Equal(blob, orig) {
			t.Fatal("input modified")
		}
		if !ok {
			return
		}
		_, got := captureOf(t, out)
		if !bytes.HasPrefix(got, iim) {
			t.Fatalf("rewritten entry %q does not start with the IIM", got)
		}
	})
}

// TestRewriteNikonMakerNoteIPTC_DataOverlapsIFD is the regression test of the
// task #371 fuzz finding: when the data named by tag 0x0E01 overlaps the IFD
// or the embedded TIFF header, clearing the old data destroyed the entry of
// the tag itself. Such a blob is malformed and must be left untouched.
func TestRewriteNikonMakerNoteIPTC_DataOverlapsIFD(t *testing.T) {
	t.Parallel()
	capture := ncData(0, 0, ncEntry(nikonCaptureIPTCTag, ncCapacity(ncOldIIM, 30)))
	long := append(bytes.Clone(ncNewIIM), bytes.Repeat([]byte("L"), 100)...)
	// Offsets are relative to the embedded TIFF header (10); the IFD is at 8.
	for name, off := range map[string]uint32{
		"data starts inside the IFD":         8 + 2 + 12,
		"data starts before the IFD":         2,
		"data starts at the TIFF header":     0,
		"data starts at the IFD entry table": 8 + 2,
	} {
		blob, _ := nikonBlob(capture)
		hdr, _ := findNikonMNTIFFHeader(blob)
		binary.BigEndian.PutUint32(blob[hdr+8+2+24+8:], off)
		orig := bytes.Clone(blob)
		for _, iim := range [][]byte{ncNewIIM, long} {
			if out, ok := rewriteNikonMakerNoteIPTC(blob, iim); ok {
				t.Errorf("%s: rewritten (%d bytes)", name, len(out))
			}
		}
		if !bytes.Equal(blob, orig) {
			t.Errorf("%s: input modified", name)
		}
	}
}

// TestRewriteNikonMakerNoteIPTC_ValidDataOverlapsIFD is the 4e472a028c803fe0
// fuzz finding of task #371 in well-formed form: the padding of a valid Nikon
// Capture data block contains the IFD of the MakerNote, so growing the block
// cleared the entry of tag 0x0E01 itself.
func TestRewriteNikonMakerNoteIPTC_ValidDataOverlapsIFD(t *testing.T) {
	t.Parallel()
	const ifdLen = 2 + 3*12 + 4
	capture := ncData(0, ifdLen, ncEntry(nikonCaptureIPTCTag, ncCapacity(ncOldIIM, 14)))
	b := []byte("Nikon\x00\x02\x10\x00\x00")
	b = append(b, 'M', 'M', 0, 42)
	b = binary.BigEndian.AppendUint32(b, uint32(8+len(capture)-ifdLen)) //nolint:gosec // G115: test sizes are small
	b = append(b, capture...)
	ifdAt := len(b) - ifdLen
	binary.BigEndian.PutUint16(b[ifdAt:], 3)
	for i, tag := range []uint16{0x0001, 0x0011, 0x0E01} {
		at := ifdAt + 2 + 12*i
		binary.BigEndian.PutUint16(b[at:], tag)
		binary.BigEndian.PutUint16(b[at+2:], 7)
		binary.BigEndian.PutUint32(b[at+4:], 1)
	}
	at := ifdAt + 2 + 24
	binary.BigEndian.PutUint32(b[at+4:], uint32(len(capture))) //nolint:gosec // G115: test sizes are small
	binary.BigEndian.PutUint32(b[at+8:], 8)
	long := append(bytes.Clone(ncNewIIM), bytes.Repeat([]byte("L"), 100)...)
	orig := bytes.Clone(b)
	for _, iim := range [][]byte{ncNewIIM, long} {
		if out, ok := rewriteNikonMakerNoteIPTC(b, iim); ok {
			t.Errorf("rewritten (%d bytes)", len(out))
		}
	}
	if !bytes.Equal(b, orig) {
		t.Error("input modified")
	}
}
