package tiff

// relocate_task310_test.go — regression tests for task #310: after a write,
// a MakerNote whose value offsets count from the TIFF header (Canon, the
// MakerNote of every CR2; Panasonic) still reads back with the values it had,
// and the Canon MakerNote footer names the MakerNote's new position. Before
// the fix the CR2 writer moved the Canon MakerNote without rebasing it
// (ExifTool: "Adjusted MakerNotes base by 2456", "Invalid Canon... data").

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// canonMakerNote returns a Canon MakerNote of fixed length that starts at the
// TIFF offset mnOff: a plain IFD whose out-of-line values (a SHORT array and
// two strings) are addressed by TIFF-relative offsets inside the MakerNote,
// followed by the Canon footer ("II*\0"/"MM\0*" and mnOff).
func canonMakerNote(order binary.ByteOrder, mnOff uint32) []byte {
	const n = 3
	settings := []byte{0, 1, 0, 2, 0, 3, 0, 4} // 4 SHORTs (byte order irrelevant for the comparison)
	imageType := []byte("Canon EOS TEST\x00\x00")
	firmware := []byte("Firmware 1.0\x00\x00")
	dirLen := 2 + n*12 + 4
	mn := make([]byte, dirLen+len(settings)+len(imageType)+len(firmware)+8)
	order.PutUint16(mn, n)
	at := dirLen
	for i, v := range []struct {
		tag   uint16
		typ   exif.DataType
		count int
		val   []byte
	}{{0x0001, exif.TypeShort, 4, settings}, {0x0006, exif.TypeASCII, len(imageType), imageType}, {0x0007, exif.TypeASCII, len(firmware), firmware}} {
		e := 2 + 12*i
		order.PutUint16(mn[e:], v.tag)
		order.PutUint16(mn[e+2:], uint16(v.typ))
		order.PutUint32(mn[e+4:], uint32(v.count)) //nolint:gosec // G115: test sizes are small
		order.PutUint32(mn[e+8:], mnOff+uint32(at))
		copy(mn[at:], v.val)
		at += len(v.val)
	}
	if order == binary.LittleEndian {
		copy(mn[at:], "II*\x00")
	} else {
		copy(mn[at:], "MM\x00*")
	}
	order.PutUint32(mn[at+4:], mnOff)
	return mn
}

// canonValues are the string values canonMakerNote writes.
var canonValues = map[uint16][]byte{ //nolint:gochecknoglobals // read-only test fixture
	0x0006: []byte("Canon EOS TEST\x00\x00"),
	0x0007: []byte("Firmware 1.0\x00\x00"),
}

// canonTIFF builds a TIFF (cr2: a CR2 with its "CR\x02\x00" header and four
// chained IFDs) whose Exif IFD holds mn(mnOff), a MakerNote of fixed length
// addressed by TIFF-relative offsets; cameraMake is IFD0's Make.
func canonTIFF(t *testing.T, order binary.ByteOrder, cr2 bool, cameraMake string, mn func(mnOff uint32) []byte) []byte {
	t.Helper()
	build := func(blob []byte) []byte {
		exifIFD := &tNode{entries: []tEntry{{tag: uint16(exif.TagMakerNote), typ: uint16(exif.TypeUndefined), count: uint32(len(blob)), val: blob}}} //nolint:gosec // G115: test sizes are small
		ifd0 := &tNode{strip: []byte("preview jpeg"), entries: []tEntry{asciiEntry(uint16(exif.TagMake), cameraMake), ptrEntry(uint16(exif.TagExifIFDPointer), uint16(exif.TypeLong), exifIFD)}}
		a := &tAsm{order: order}
		if cr2 {
			ifd3 := &tNode{strip: []byte("raw sensor data")}
			ifd0.next = &tNode{strip: []byte("thumb"), next: &tNode{strip: []byte("rgb"), next: ifd3}}
			a.head = []byte("CR\x02\x00\x00\x00\x00\x00")
			b := a.bytes(t, ifd0)
			order.PutUint32(b[12:], ifd3.off) // CR2 header: RAW IFD offset (lclevy)
			return b
		}
		return a.bytes(t, ifd0)
	}
	probe := mn(0)
	e, err := exif.Parse(build(probe))
	if err != nil || e.MakerNoteOffset == 0 {
		t.Fatalf("canonTIFF probe: %v", err)
	}
	return build(mn(e.MakerNoteOffset))
}

// assertMakerNoteKept parses in and out and fails unless both MakerNote IFDs
// hold the same entries with the same values, the output entries listed in
// want hold the given values (the values the test wrote, independent of how
// the parser resolves offsets), and, when footer is set, the output
// MakerNote ends with a footer naming its own offset.
func assertMakerNoteKept(t *testing.T, in, out []byte, footer bool, want map[uint16][]byte) {
	t.Helper()
	ei, err := exif.Parse(in)
	if err != nil {
		t.Fatalf("Parse(in): %v", err)
	}
	eo, err := exif.Parse(out)
	if err != nil {
		t.Fatalf("Parse(out): %v", err)
	}
	if ei.MakerNoteIFD == nil || eo.MakerNoteIFD == nil {
		t.Fatalf("MakerNoteIFD in %v out %v", ei.MakerNoteIFD != nil, eo.MakerNoteIFD != nil)
	}
	if len(ei.MakerNoteIFD.Entries) != len(eo.MakerNoteIFD.Entries) {
		t.Fatalf("MakerNote entries: in %d, out %d", len(ei.MakerNoteIFD.Entries), len(eo.MakerNoteIFD.Entries))
	}
	for i, a := range ei.MakerNoteIFD.Entries {
		b := eo.MakerNoteIFD.Entries[i]
		if a.Tag != b.Tag || a.Type != b.Type || a.Count != b.Count || !bytes.Equal(a.Value, b.Value) {
			t.Errorf("MakerNote tag 0x%04X: in %x, out %x", a.Tag, a.Value, b.Value)
		}
	}
	for tag, v := range want {
		if got := eo.MakerNoteIFD.Get(exif.TagID(tag)); got == nil || !bytes.Equal(got.Value, v) {
			t.Errorf("output MakerNote tag 0x%04X = %v, want %x", tag, got, v)
		}
	}
	if eo.MakerNoteOffset == ei.MakerNoteOffset {
		t.Fatal("the MakerNote did not move; the test exercises nothing")
	}
	if footer {
		f := eo.MakerNote[len(eo.MakerNote)-8:]
		if got := eo.ByteOrder.Uint32(f[4:]); got != eo.MakerNoteOffset {
			t.Errorf("Canon footer offset = %d, want the MakerNote offset %d", got, eo.MakerNoteOffset)
		}
	}
}

// TestInjectCR2_CanonMakerNoteRebased (task #310): the CR2 writer rebases the
// Canon MakerNote twice — for the move done by the re-encode and for the
// 8-byte CR2 marker insertion — and updates its footer.
func TestInjectCR2_CanonMakerNoteRebased(t *testing.T) {
	t.Parallel()
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(order.String(), func(t *testing.T) {
			t.Parallel()
			in := canonTIFF(t, order, true, "Canon", func(off uint32) []byte { return canonMakerNote(order, off) })
			e, err := exif.Parse(in)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			var out bytes.Buffer
			if err := InjectWithEXIFCR2(in, e, nil, testXMP, &out); err != nil {
				t.Fatalf("InjectWithEXIFCR2: %v", err)
			}
			assertMakerNoteKept(t, in, out.Bytes(), true, canonValues)
			assertSameTree(t, in, out.Bytes())
		})
	}
}

// TestInjectTIFF_CanonMakerNoteRebased (task #310): the same MakerNote in a
// plain TIFF, written through the standard relocation path.
func TestInjectTIFF_CanonMakerNoteRebased(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	in := canonTIFF(t, le, false, "Canon", func(off uint32) []byte { return canonMakerNote(le, off) })
	assertMakerNoteKept(t, in, writeXMP(t, in), true, canonValues)
}

// TestInjectTIFF_PanasonicMakerNoteRebased (task #310): Panasonic MakerNote
// value offsets count from the TIFF header (metadata-extractor
// processMakernote; corpus Panasonic_DMC-FZ30.jpg: tag 0x0021 at TIFF offset
// 1296, where ExifTool reads it), so they are rebased when the MakerNote moves.
func TestInjectTIFF_PanasonicMakerNoteRebased(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	val := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x12, 0x34}
	in := canonTIFF(t, le, false, "Panasonic", func(off uint32) []byte {
		mn := make([]byte, 12+2+12+4+len(val))
		copy(mn, "Panasonic\x00\x00\x00")
		le.PutUint16(mn[12:], 1)
		le.PutUint16(mn[14:], 0x0021)
		le.PutUint16(mn[16:], uint16(exif.TypeUndefined))
		le.PutUint32(mn[18:], uint32(len(val))) //nolint:gosec // G115: test sizes are small
		le.PutUint32(mn[22:], off+30)           // TIFF-relative
		copy(mn[30:], val)
		return mn
	})
	assertMakerNoteKept(t, in, writeXMP(t, in), false, map[uint16][]byte{0x0021: val})
}

// TestInjectCR2_CorpusCanonMakerNote (task #310, corpus): for each corpus CR2,
// a write keeps every Canon MakerNote value and the footer names the new
// MakerNote position. "exiftool -validate -warning" reports "OK" on every
// written file (base cfe80d1: "Adjusted MakerNotes base by ~2.4 KB" and
// "Invalid Canon... data").
func TestInjectCR2_CorpusCanonMakerNote(t *testing.T) {
	t.Parallel()
	files, _ := filepath.Glob(filepath.Join("..", "..", "testdata", "corpus", "raw", "*", "*.[cC][rR]2"))
	if len(files) == 0 {
		t.Skip("corpus file absent (run testdata/download.sh): no CR2 under testdata/corpus/raw")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
			}
			if err != nil {
				t.Fatal(err)
			}
			e, err := exif.Parse(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			var out bytes.Buffer
			if err := InjectWithEXIFCR2Stream(bytes.NewReader(data), data, true, e, nil, testXMP, &out); err != nil {
				t.Fatalf("InjectWithEXIFCR2Stream: %v", err)
			}
			assertMakerNoteKept(t, data, out.Bytes(), true, nil)
		})
	}
}
