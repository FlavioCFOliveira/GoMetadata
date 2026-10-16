package tiff

// nikoncaptureoffsets_test.go — task #355: the Nikon Capture offsets
// (MakerNote tag 0x0E0E) follow the structures they name when a NEF write moves
// them. Layout and policy: see nikoncaptureoffsets.go.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// appendOrder is a byte order that can also append integers.
type appendOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

// captureBlob builds a Nikon Type-3 MakerNote value with the embedded TIFF in
// order mnOrder: a MakerNote IFD0 with the PreviewIFD pointer (0x0011) and the
// Nikon Capture offsets (0x0E0E, the entries given), a PreviewIFD, and the
// offsets value. Offsets inside it count from the embedded TIFF header (8).
func captureBlob(mnOrder appendOrder, version string, entries [][3]uint32) []byte {
	const (
		ifd0Rel    = 8
		previewRel = ifd0Rel + 2 + 2*12 + 4 // 38
		valueRel   = previewRel + 2 + 12 + 4
	)
	valLen := 4 + 2 + 12*len(entries)
	b := []byte("Nikon\x00\x02\x10")
	if mnOrder == binary.LittleEndian {
		b = append(b, "II"...)
	} else {
		b = append(b, "MM"...)
	}
	b = mnOrder.AppendUint16(b, 42)
	b = mnOrder.AppendUint32(b, ifd0Rel)
	// MakerNote IFD0.
	b = mnOrder.AppendUint16(b, 2)
	b = mnOrder.AppendUint16(b, uint16(nikonTagPreviewIFD))
	b = mnOrder.AppendUint16(b, 4)
	b = mnOrder.AppendUint32(b, 1)
	b = mnOrder.AppendUint32(b, previewRel)
	b = mnOrder.AppendUint16(b, uint16(nikonTagCaptureOffsets))
	b = mnOrder.AppendUint16(b, 7)
	b = mnOrder.AppendUint32(b, uint32(valLen)) //nolint:gosec // G115: test sizes are small
	b = mnOrder.AppendUint32(b, valueRel)
	b = mnOrder.AppendUint32(b, 0)
	// PreviewIFD: one SHORT entry.
	b = mnOrder.AppendUint16(b, 1)
	b = mnOrder.AppendUint16(b, 0x0103)
	b = mnOrder.AppendUint16(b, 3)
	b = mnOrder.AppendUint32(b, 1)
	b = mnOrder.AppendUint32(b, 6)
	b = mnOrder.AppendUint32(b, 0)
	// The offsets value.
	b = append(b, version...)
	b = mnOrder.AppendUint16(b, uint16(len(entries))) //nolint:gosec // G115: test sizes are small
	for _, e := range entries {
		b = mnOrder.AppendUint32(b, e[0])
		b = mnOrder.AppendUint32(b, e[1])
		b = mnOrder.AppendUint32(b, e[2]) // the four bytes of unknown meaning
	}
	return b
}

// captureTIFF assembles an outer TIFF: IFD0 (a SubIFDs array of two
// directories, the ExifIFD pointer, and pad ASCII bytes that move everything
// after IFD0), ExifIFD with the MakerNote blob.
func captureTIFF(t *testing.T, order binary.ByteOrder, blob []byte, pad string) []byte {
	t.Helper()
	sub0 := &tNode{entries: []tEntry{{tag: 0x00FE, typ: 4, count: 1, val: u32(order, 1)}}}
	sub1 := &tNode{entries: []tEntry{{tag: 0x00FE, typ: 4, count: 1, val: u32(order, 0)}}}
	exifIFD := &tNode{entries: []tEntry{{tag: uint16(exif.TagMakerNote), typ: uint16(exif.TypeUndefined), count: uint32(len(blob)), val: blob}}} //nolint:gosec // G115: test sizes are small
	ifd0 := &tNode{entries: []tEntry{
		ptrEntry(uint16(exif.TagSubIFDs), uint16(exif.TypeLong), sub0, sub1),
		ptrEntry(uint16(exif.TagExifIFDPointer), uint16(exif.TypeLong), exifIFD),
		asciiEntry(0x010E, pad),
	}}
	return (&tAsm{order: order}).bytes(t, ifd0)
}

// captureValues returns the (identifier, offset) pairs of the Nikon Capture
// offsets of tiff, a rebuilt header whose MakerNote value is the extended one.
func captureValues(t *testing.T, tiff []byte) (pairs [][2]uint32, p nikonCapturePlace) {
	t.Helper()
	return captureValuesIn(t, tiff, true)
}

// TestTask355PatchNikonCaptureOffsets: the three known entries follow the
// structures they name when those move, whatever the byte orders of the outer
// TIFF and of the MakerNote; every other byte of the value is untouched.
// NEF-CAPTURE-OFFSETS-01.
func TestTask355PatchNikonCaptureOffsets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		outer, inner appendOrder
	}{
		{"II outer, II MakerNote", binary.LittleEndian, binary.LittleEndian},
		{"MM outer, MM MakerNote", binary.BigEndian, binary.BigEndian},
		{"II outer, MM MakerNote", binary.LittleEndian, binary.BigEndian},
		{"MM outer, II MakerNote", binary.BigEndian, binary.LittleEndian},
	} {
		t.Run("NEF-CAPTURE-OFFSETS-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			// Learn the structure positions of the source, then write them into
			// the value, as Nikon Capture does.
			entries := [][3]uint32{{1, 0, 0x01010000}, {2, 0, 0x01010001}, {3, 0, 0x01010002}, {99, 777, 5}}
			blob := captureBlob(tc.inner, "0100", entries)
			src := captureTIFF(t, tc.outer, blob, "")
			_, place := captureValues(t, src)
			entries[0][1], entries[1][1], entries[2][1] = place.ifd0, place.previewIFD, place.subIFD
			if place.previewIFD == 0 || place.subIFD == 0 {
				t.Fatalf("fixture: structures not found: %+v", place)
			}
			blob = captureBlob(tc.inner, "0100", entries)
			src = captureTIFF(t, tc.outer, blob, "")
			srcPairs, old := captureValues(t, src)
			if srcPairs[0][1] != old.ifd0 || srcPairs[1][1] != old.previewIFD || srcPairs[2][1] != old.subIFD {
				t.Fatalf("fixture: values %v do not name the structures %+v", srcPairs, old)
			}

			// A rebuilt header: the same MakerNote bytes, every structure after
			// IFD0 moved by a longer ASCII entry.
			out := captureTIFF(t, tc.outer, blob, "a long description that moves the structures after IFD0")
			if _, moved := captureValues(t, out); moved.subIFD == old.subIFD || moved.previewIFD == old.previewIFD {
				t.Fatal("fixture: the structures did not move")
			}
			patchNikonCaptureOffsets(out, old)

			got, cur := captureValues(t, out)
			want := [][2]uint32{{1, cur.ifd0}, {2, cur.previewIFD}, {3, cur.subIFD}, {99, 777}}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("entry %d = %v, want %v", i, got[i], want[i])
				}
			}
			// The four unknown bytes of every entry are as they were.
			for i, e := range entries {
				at := cur.entriesAt + i*nikonCaptureOffsetsEntryLen + 8
				if v := cur.order.Uint32(out[at:]); v != e[2] {
					t.Errorf("entry %d: unknown bytes = %#x, want %#x", i, v, e[2])
				}
			}
			// The offsets name the same structures: IFD0 is IFD0, the SubIFD is
			// the first directory of the array, the PreviewIFD is the one of
			// the MakerNote.
			if first := firstSubIFD(out, cur.ifd0, tc.outer); got[2][1] != first {
				t.Errorf("SubIFD offset %d, but the first SubIFD is at %d", got[2][1], first)
			}
			if n := tc.inner.Uint16(out[got[1][1]:]); n != 1 {
				t.Errorf("the PreviewIFD offset names a directory with %d entries, want 1", n)
			}
		})
	}
}

// TestTask355PatchLeavesWhatItDoesNotUnderstand: a value that is not
// "0100...", names no known structure of the source, is truncated, or sits in
// a MakerNote that is not Nikon Type 3 is carried unchanged. NEF-CAPTURE-OFFSETS-02.
func TestTask355PatchLeavesWhatItDoesNotUnderstand(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	build := func(version string, entries [][3]uint32) (src, out []byte, old nikonCapturePlace) {
		blob := captureBlob(le, version, entries)
		src = captureTIFF(t, le, blob, "")
		out = captureTIFF(t, le, blob, "a long description that moves the structures after IFD0")
		old, _ = locateNikonCapture(src, false)
		return src, out, old
	}
	stale := [][3]uint32{{1, 11111, 0}, {2, 22222, 0}, {3, 33333, 0}} // match no structure of the source (the corpus files are like this)
	t.Run("NEF-CAPTURE-OFFSETS-02/stale values", func(t *testing.T) {
		t.Parallel()
		_, out, old := build("0100", stale)
		before := bytes.Clone(out)
		patchNikonCaptureOffsets(out, old)
		if !bytes.Equal(out, before) {
			t.Error("values that name no structure of the source were rewritten")
		}
	})
	t.Run("NEF-CAPTURE-OFFSETS-02/other version", func(t *testing.T) {
		t.Parallel()
		src, out, _ := build("0200", stale)
		if _, ok := locateNikonCapture(src, false); ok {
			t.Fatal("a value that does not start with 0100 was located")
		}
		before := bytes.Clone(out)
		patchNikonCaptureOffsets(out, nikonCapturePlace{count: 3})
		if !bytes.Equal(out, before) {
			t.Error("a value that does not start with 0100 was rewritten")
		}
	})
	t.Run("NEF-CAPTURE-OFFSETS-02/truncated count", func(t *testing.T) {
		t.Parallel()
		blob := captureBlob(le, "0100", [][3]uint32{{1, 8, 0}})
		le.PutUint16(blob[len(blob)-12-2:], 9) // the count claims more entries than the value holds
		src := captureTIFF(t, le, blob, "")
		if _, ok := locateNikonCapture(src, false); ok {
			t.Error("a value shorter than its entry count was located")
		}
	})
	t.Run("NEF-CAPTURE-OFFSETS-02/no MakerNote", func(t *testing.T) {
		t.Parallel()
		plain := (&tAsm{order: le}).bytes(t, &tNode{entries: []tEntry{asciiEntry(0x010E, "x")}})
		before := bytes.Clone(plain)
		patchNikonCaptureOffsets(plain, nikonCapturePlace{count: 3})
		if !bytes.Equal(plain, before) {
			t.Error("a file without the tag was modified")
		}
	})
}

// TestTask355CorpusNEF runs the write on the corpus NEF files whose MakerNote
// carries tag 0x0E0E. Their values name no structure of their own file (the
// files are pruned or were edited after Nikon Capture wrote them), so the write
// must carry the value byte for byte; a copy whose values are made consistent
// first must follow the structures. NEF-CAPTURE-OFFSETS-03.
func TestTask355CorpusNEF(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "testdata", "corpus", "raw", "exiftool", "Nikon.nef")
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	write := func(t *testing.T, in []byte) []byte {
		t.Helper()
		e, perr := exif.Parse(in)
		if perr != nil {
			t.Fatalf("exif.Parse: %v", perr)
		}
		var out bytes.Buffer
		if werr := InjectWithEXIFNEFStream(bytes.NewReader(in), in, true, e, nil, testXMP, &out); werr != nil {
			t.Fatalf("InjectWithEXIFNEFStream: %v", werr)
		}
		return out.Bytes()
	}

	t.Run("NEF-CAPTURE-OFFSETS-03/stale values are carried", func(t *testing.T) {
		t.Parallel()
		want, _ := captureValuesIn(t, src, false)
		got, _ := captureValuesIn(t, write(t, src), true)
		if len(got) != len(want) {
			t.Fatalf("%d entries after the write, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("entry %d = %v, want %v", i, got[i], want[i])
			}
		}
	})

	t.Run("NEF-CAPTURE-OFFSETS-03/consistent values follow the structures", func(t *testing.T) {
		t.Parallel()
		in := bytes.Clone(src)
		_, p := captureValuesIn(t, in, false)
		for i := range p.count {
			at := p.entriesAt + i*nikonCaptureOffsetsEntryLen
			switch p.order.Uint32(in[at:]) {
			case nikonCaptureIFD0Offset:
				p.order.PutUint32(in[at+4:], p.ifd0)
			case nikonCapturePreviewIFDOfs:
				p.order.PutUint32(in[at+4:], p.previewIFD)
			case nikonCaptureSubIFDOffset:
				p.order.PutUint32(in[at+4:], p.subIFD)
			}
		}
		out := write(t, in)
		got, cur := captureValuesIn(t, out, true)
		want := map[uint32]uint32{nikonCaptureIFD0Offset: cur.ifd0, nikonCapturePreviewIFDOfs: cur.previewIFD, nikonCaptureSubIFDOffset: cur.subIFD}
		for _, g := range got {
			if g[1] != want[g[0]] {
				t.Errorf("identifier %d = %d, want %d", g[0], g[1], want[g[0]])
			}
		}
		if cur.subIFD == p.subIFD || cur.previewIFD == p.previewIFD {
			t.Fatal("the structures did not move: the test proves nothing")
		}
		// The offsets address the same structures as before the write: the
		// directories at the SubIFD and PreviewIFD offsets list the same tags in
		// the source and in the output (IFD0 gains the XMP tag, and stays at 8).
		_, srcP := captureValuesIn(t, in, false)
		for _, c := range []struct {
			name        string
			srcOff, off uint32
			srcOrd, ord binary.ByteOrder
		}{
			{"PreviewIFD", srcP.previewIFD, cur.previewIFD, srcP.order, cur.order},
			{"SubIFD", srcP.subIFD, cur.subIFD, orderOf(in), orderOf(out)},
		} {
			a, b := ifdTags(in, c.srcOff, c.srcOrd), ifdTags(out, c.off, c.ord)
			if !bytes.Equal(a, b) {
				t.Errorf("%s at %d in the output lists tags %x, the source's at %d lists %x", c.name, c.off, b, c.srcOff, a)
			}
		}
	})
}

// captureValuesIn is captureValues for a stream whose MakerNote value may be
// the declared one (a source) or the extended one (an output).
func captureValuesIn(t *testing.T, tiff []byte, inBlob bool) ([][2]uint32, nikonCapturePlace) {
	t.Helper()
	p, ok := locateNikonCapture(tiff, inBlob)
	if !ok {
		t.Fatal("no Nikon Capture offsets in the stream")
	}
	pairs := make([][2]uint32, 0, p.count)
	for i := range p.count {
		at := p.entriesAt + i*nikonCaptureOffsetsEntryLen
		pairs = append(pairs, [2]uint32{p.order.Uint32(tiff[at:]), p.order.Uint32(tiff[at+4:])})
	}
	return pairs, p
}

func orderOf(b []byte) binary.ByteOrder {
	o, _, _ := classicTIFFHeader(b)
	return o
}

// ifdTags returns the tag IDs of the directory at off in b, as big-endian bytes.
func ifdTags(b []byte, off uint32, o binary.ByteOrder) []byte {
	n := int(o.Uint16(b[off:]))
	var tags []byte
	for i := range n {
		tags = binary.BigEndian.AppendUint16(tags, o.Uint16(b[int(off)+2+12*i:]))
	}
	return tags
}

// FuzzPatchNikonCaptureOffsets: arbitrary bytes never panic locateNikonCapture
// or patchNikonCaptureOffsets, the patch never changes the length, and it
// touches only the four bytes of an offset field.
func FuzzPatchNikonCaptureOffsets(f *testing.F) {
	t := &testing.T{}
	le := binary.LittleEndian
	entries := [][3]uint32{{1, 8, 7}, {2, 100, 8}, {3, 200, 9}, {99, 5, 6}}
	for _, order := range []appendOrder{binary.LittleEndian, binary.BigEndian} {
		f.Add(captureTIFF(t, le, captureBlob(order, "0100", entries), ""))
		f.Add(captureTIFF(t, binary.BigEndian, captureBlob(order, "0100", entries), "padding to move the structures"))
	}
	f.Add([]byte("II*\x00\x08\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		old, ok := locateNikonCapture(data, false)
		out := bytes.Clone(data)
		patchNikonCaptureOffsets(out, old)
		if len(out) != len(data) {
			t.Fatalf("the patch changed the length: %d -> %d", len(data), len(out))
		}
		if !ok {
			return
		}
		changed := 0
		for i := range data {
			if data[i] != out[i] {
				changed++
			}
		}
		if changed > 4*old.count {
			t.Fatalf("%d bytes changed for %d entries", changed, old.count)
		}
	})
}
