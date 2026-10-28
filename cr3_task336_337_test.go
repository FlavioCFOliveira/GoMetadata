package gometadata

// cr3_task336_337_test.go — tasks #336 and #337: Read parses the CMT2 stream
// of a CR3 file as the Exif IFD, a write updates it in place (no second Exif
// IFD in CMT1), and deleting the GPS IFD removes the GPS data.
//
// Reference counts: ExifTool 13, `exiftool -a -u -G3:1 -ExifIFD:all`, lists
// 39 main-document ExifIFD tags for every corpus file (the 40th line that
// `-G1` shows belongs to the embedded CTMD timed-metadata document, not to
// CMT2).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/cr3"
)

// cr3ExifIFDEntries is the number of entries of the CMT2 Exif IFD of every CR3
// corpus file (ExifTool: "ExifIFD directory with 39 entries").
const cr3ExifIFDEntries = 39

// writeCR3 writes m into src and returns the output.
func writeCR3(t *testing.T, src []byte, m *Metadata) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return out.Bytes()
}

// cr3Chunks returns the 16 bytes at every stco/co64 chunk offset of file
// (the first 4 entries of each table), so that two files can be compared chunk
// by chunk.
func cr3Chunks(t *testing.T, file []byte) [][]byte {
	t.Helper()
	var out [][]byte
	var walk func(data []byte, depth int)
	walk = func(data []byte, depth int) {
		for pos := 0; pos+8 <= len(data); {
			size := uint64(binary.BigEndian.Uint32(data[pos:]))
			hl := 8
			if size == 1 {
				size, hl = binary.BigEndian.Uint64(data[pos+8:]), 16
			}
			if size == 0 {
				size = uint64(len(data) - pos)
			}
			if size < uint64(hl) || size > uint64(len(data)-pos) {
				t.Fatalf("malformed box at %d", pos)
			}
			typ, body := string(data[pos+4:pos+8]), data[pos+hl:pos+int(size)] //nolint:gosec // G115: bounded by len(data)
			pos += int(size)                                                   //nolint:gosec // G115: bounded by len(data)
			switch typ {
			case "moov", "trak", "mdia", "minf", "stbl":
				if depth < 8 {
					walk(body, depth+1)
				}
			case "stco", "co64":
				w := 4
				if typ == "co64" {
					w = 8
				}
				n := min(int(binary.BigEndian.Uint32(body[4:])), 4)
				for i := range n {
					var off uint64
					if w == 4 {
						off = uint64(binary.BigEndian.Uint32(body[8+i*4:]))
					} else {
						off = binary.BigEndian.Uint64(body[8+i*8:])
					}
					if off > uint64(len(file)) {
						// A pruned corpus file keeps chunk offsets past its
						// end; the shift is checked by the other chunks.
						out = append(out, nil)
						continue
					}
					out = append(out, file[off:min(off+16, uint64(len(file)))])
				}
			}
		}
	}
	walk(file, 0)
	return out
}

// countTag returns how many Exif IFD entries with tag the streams of seg
// hold: CMT1 (including an ExifIFD reached through a pointer) and CMT2.
func countTag(t *testing.T, seg cr3.Segments, tag exif.TagID) int {
	t.Helper()
	n := 0
	e, err := exif.Parse(seg.EXIF)
	if err != nil {
		t.Fatalf("parse CMT1: %v", err)
	}
	if e.ExifIFD != nil && e.ExifIFD.Get(tag) != nil {
		n++
	}
	if seg.ExifIFD != nil {
		ifd, _, _, err := cr3.ParseExifStream(seg.ExifIFD)
		if err != nil {
			t.Fatalf("parse CMT2: %v", err)
		}
		for _, ent := range ifd.Entries {
			if ent.Tag == tag {
				n++
			}
		}
	}
	return n
}

// TestCR3_Task336_CMT2Read is the CR3-CMT2-READ-01 corpus gate: CMT2 is
// parsed as the Exif IFD (39 entries, matching ExifTool).
func TestCR3_Task336_CMT2Read(t *testing.T) {
	t.Parallel()
	for _, rel := range cr3CorpusFiles {
		t.Run("CR3-CMT2-READ-01/"+rel, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(readCR3Corpus(t, rel)))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if m.EXIF == nil || m.EXIF.ExifIFD == nil {
				t.Fatal("Exif IFD (CMT2) not read")
			}
			if n := len(m.EXIF.ExifIFD.Entries); n != cr3ExifIFDEntries {
				t.Errorf("Exif IFD has %d entries, want %d (ExifTool ExifIFD:all)", n, cr3ExifIFDEntries)
			}
			if _, ok := m.DateTimeOriginal(); !ok {
				t.Error("DateTimeOriginal not available")
			}
			if len(m.ParseWarnings) != 0 {
				t.Errorf("ParseWarnings = %v", m.ParseWarnings)
			}
		})
	}
}

// TestCR3_Task336_CMT2Write is the CR3-CMT2-WRITE-01 corpus gate: setting the
// original date time writes exactly one ExifIFD:DateTimeOriginal, in CMT2,
// and nothing else changes except what SetDateTimeOriginal itself documents
// (OffsetTimeOriginal is set and the now-stale SubSecTimeOriginal removed).
// CMT3 and CMT4 are untouched.
func TestCR3_Task336_CMT2Write(t *testing.T) {
	t.Parallel()
	want := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, rel := range cr3CorpusFiles {
		t.Run("CR3-CMT2-WRITE-01/"+rel, func(t *testing.T) {
			t.Parallel()
			src := readCR3Corpus(t, rel)
			before, err := cr3.ExtractSegments(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			beforeIFD, _, _, err := cr3.ParseExifStream(before.ExifIFD)
			if err != nil {
				t.Fatal(err)
			}
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			m.SetDateTimeOriginal(want)
			out := writeCR3(t, src, m)

			after, err := cr3.ExtractSegments(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			if n := countTag(t, after, exif.TagDateTimeOriginal); n != 1 {
				t.Errorf("%d DateTimeOriginal entries in the Exif data, want exactly 1", n)
			}
			ifd, _, _, err := cr3.ParseExifStream(after.ExifIFD)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(ifd.Get(exif.TagDateTimeOriginal).Value); got != "2031:02:03 04:05:06\x00" {
				t.Errorf("CMT2 DateTimeOriginal = %q", got)
			}
			// SetDateTimeOriginal removes SubSecTimeOriginal (0x9291); every
			// other source entry survives and none is added.
			if len(ifd.Entries) != cr3ExifIFDEntries-1 || ifd.Get(exif.TagSubSecTimeOriginal) != nil {
				t.Errorf("CMT2 has %d entries after the write (SubSecTimeOriginal present: %v), want %d without it",
					len(ifd.Entries), ifd.Get(exif.TagSubSecTimeOriginal) != nil, cr3ExifIFDEntries-1)
			}
			for _, e := range beforeIFD.Entries {
				if e.Tag != exif.TagSubSecTimeOriginal && ifd.Get(e.Tag) == nil {
					t.Errorf("CMT2 entry %#x was lost", uint16(e.Tag))
				}
			}
			if !bytes.Equal(after.GPS, before.GPS) || !bytes.Equal(after.MakerNote, before.MakerNote) {
				t.Error("CMT3 or CMT4 changed although neither was modified")
			}
			m2, err := Read(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("re-read: %v", err)
			}
			if got, ok := m2.DateTimeOriginal(); !ok || !got.Equal(want) {
				t.Errorf("re-read DateTimeOriginal = %v (%v), want %v", got, ok, want)
			}
			if !chunksEqual(cr3Chunks(t, src), cr3Chunks(t, out)) {
				t.Error("chunk offsets no longer address the same media data")
			}
		})
	}
}

// chunksEqual reports whether two chunk lists hold the same bytes.
func chunksEqual(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// TestCR3_Task337_GPSDelete is the CR3-CMT4-DELETE-01 corpus gate: deleting
// the GPS IFD removes the GPS data (CMT4), as ExifTool's -GPS:all= does, and
// nothing else changes. The corpus files all carry the Canon "no fix" CMT4
// (GPSVersionID only), which a re-read exposes as a GPS IFD.
func TestCR3_Task337_GPSDelete(t *testing.T) {
	t.Parallel()
	for _, rel := range cr3CorpusFiles {
		t.Run("CR3-CMT4-DELETE-01/"+rel, func(t *testing.T) {
			t.Parallel()
			src := readCR3Corpus(t, rel)
			before, err := cr3.ExtractSegments(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if m.EXIF == nil || m.EXIF.GPSIFD == nil {
				t.Fatal("source has no GPS IFD to delete")
			}
			m.EXIF.GPSIFD = nil
			m.MarkModified(SegmentEXIF) // task #387: the IFD was removed through the exported field
			out := writeCR3(t, src, m)

			after, err := cr3.ExtractSegments(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			if after.GPS != nil {
				t.Error("CMT4 still present after the GPS IFD was deleted")
			}
			m2, err := Read(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("re-read: %v", err)
			}
			if m2.EXIF == nil || m2.EXIF.GPSIFD != nil {
				t.Error("re-read still exposes a GPS IFD")
			}
			if _, _, ok := m2.GPS(); ok {
				t.Error("re-read still reports a GPS position")
			}
			// Everything else is untouched: CMT2 (unchanged IFD, kept byte for
			// byte), CMT3, and the media data the chunk offsets address.
			if !bytes.Equal(after.ExifIFD, before.ExifIFD) || !bytes.Equal(after.MakerNote, before.MakerNote) {
				t.Error("CMT2 or CMT3 changed")
			}
			if !chunksEqual(cr3Chunks(t, src), cr3Chunks(t, out)) {
				t.Error("chunk offsets no longer address the same media data after the box was removed")
			}
		})
	}
}

// TestCR3_Task337_GPSUnchangedKeepsCMT4 is the second half of the contract: a
// write that leaves the GPS IFD alone keeps CMT4 byte for byte.
func TestCR3_Task337_GPSUnchangedKeepsCMT4(t *testing.T) {
	t.Parallel()
	for _, rel := range cr3CorpusFiles {
		t.Run("CR3-CMT4-DELETE-01/unchanged/"+rel, func(t *testing.T) {
			t.Parallel()
			src := readCR3Corpus(t, rel)
			before, err := cr3.ExtractSegments(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			m.SetCaption("unchanged GPS")
			after, err := cr3.ExtractSegments(bytes.NewReader(writeCR3(t, src, m)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after.GPS, before.GPS) || !bytes.Equal(after.ExifIFD, before.ExifIFD) {
				t.Error("CMT4 or CMT2 changed although neither was modified")
			}
		})
	}
}

// cr3Box assembles an ISOBMFF box.
func cr3Box(typ string, body []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(8+len(body))), append([]byte(typ), body...)...) //nolint:gosec // G115: small test box
}

// cr3File assembles ftyp + moov{Canon uuid{CMT boxes}} from named streams.
func cr3File(cmts ...[]byte) []byte {
	canon := []byte{0x85, 0xC0, 0xB6, 0x87, 0x82, 0x0F, 0x11, 0xE0, 0x81, 0x11, 0xF4, 0xCE, 0x46, 0x2B, 0x6A, 0x48}
	var body []byte
	for i, c := range cmts {
		if c != nil {
			body = append(body, cr3Box([]string{"CMT1", "CMT2", "CMT3", "CMT4"}[i], c)...)
		}
	}
	uuid := cr3Box("uuid", append(append([]byte{}, canon...), body...))
	return append([]byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 1}, cr3Box("moov", uuid)...)
}

// ascii returns an ASCII entry.
func ascii(tag exif.TagID, s string) exif.IFDEntry {
	return exif.IFDEntry{Tag: tag, Type: exif.TypeASCII, Count: uint32(len(s) + 1), Value: append([]byte(s), 0)} //nolint:gosec // G115: tiny test string
}

// stream encodes an EXIF model, failing t on error.
func stream(t *testing.T, e *exif.EXIF) []byte {
	t.Helper()
	b, err := exif.Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCR3_Task336_LegacyExifIFDInCMT1: files written by GoMetadata before
// task #336 hold an Exif IFD in CMT1 (behind an ExifIFD pointer) next to the
// untouched CMT2. Read keeps both: the CMT1 values, the later edits, win, and
// no CMT2 entry is lost; a write consolidates them into CMT2 alone.
func TestCR3_Task336_LegacyExifIFDInCMT1(t *testing.T) {
	t.Parallel()
	t.Run("CR3-CMT2-READ-01/legacy duplicate", func(t *testing.T) {
		t.Parallel()
		cmt1 := stream(t, &exif.EXIF{ByteOrder: binary.LittleEndian,
			IFD0:    &exif.IFD{Entries: []exif.IFDEntry{ascii(exif.TagModel, "Canon EOS R8")}},
			ExifIFD: &exif.IFD{Entries: []exif.IFDEntry{ascii(exif.TagDateTimeOriginal, "2030:01:01 00:00:00")}}})
		cmt2 := stream(t, &exif.EXIF{ByteOrder: binary.LittleEndian, IFD0: &exif.IFD{Entries: []exif.IFDEntry{
			ascii(exif.TagDateTimeOriginal, "2018:02:21 12:08:56"), ascii(exif.TagLensModel, "RF24-70mm")}}})
		src := cr3File(cmt1, cmt2)

		m, err := Read(bytes.NewReader(src))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got := string(m.EXIF.ExifIFD.Get(exif.TagDateTimeOriginal).Value); got != "2030:01:01 00:00:00\x00" {
			t.Errorf("DateTimeOriginal = %q, want the CMT1 (later) value", got)
		}
		if m.EXIF.ExifIFD.Get(exif.TagLensModel) == nil {
			t.Error("the CMT2 LensModel entry was lost")
		}
		if len(m.EXIF.ExifIFD.Entries) != 2 {
			t.Errorf("Exif IFD has %d entries, want 2 (no duplicate DateTimeOriginal)", len(m.EXIF.ExifIFD.Entries))
		}

		m.SetCaption("consolidate")
		out := writeCR3(t, src, m)
		seg, err := cr3.ExtractSegments(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if n := countTag(t, seg, exif.TagDateTimeOriginal); n != 1 {
			t.Errorf("%d DateTimeOriginal entries after the write, want 1", n)
		}
		if e, err := exif.Parse(seg.EXIF); err != nil || e.ExifIFD != nil {
			t.Errorf("CMT1 still holds an Exif IFD after the write (err=%v)", err)
		}
	})
}

// TestCR3_Task336_CMT2ByteOrderMismatch: a CMT2 stream in the other byte
// order than CMT1 is reported and not attached, as for CMT4, and a write does
// not remove it (the caller never saw it).
func TestCR3_Task336_CMT2ByteOrderMismatch(t *testing.T) {
	t.Parallel()
	t.Run("CR3-CMT2-READ-01/byte order", func(t *testing.T) {
		t.Parallel()
		cmt1 := stream(t, &exif.EXIF{ByteOrder: binary.LittleEndian, IFD0: &exif.IFD{Entries: []exif.IFDEntry{ascii(exif.TagModel, "M")}}})
		be := exif.IFDEntry{Tag: exif.TagISOSpeedRatings, Type: exif.TypeShort, Count: 1, Value: []byte{0x01, 0x90}}
		cmt2 := stream(t, &exif.EXIF{ByteOrder: binary.BigEndian, IFD0: &exif.IFD{Entries: []exif.IFDEntry{be}}})
		src := cr3File(cmt1, cmt2)

		m, err := Read(bytes.NewReader(src))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if m.EXIF.ExifIFD != nil {
			t.Fatal("an Exif IFD in the other byte order was attached")
		}
		found := false
		for _, w := range m.ParseWarnings {
			found = found || errors.Is(w, errCR3ExifByteOrder)
		}
		if !found {
			t.Errorf("ParseWarnings = %v, want errCR3ExifByteOrder", m.ParseWarnings)
		}
		if _, err := Read(bytes.NewReader(src), Strict()); !errors.Is(err, errCR3ExifByteOrder) {
			t.Errorf("strict Read error = %v, want errCR3ExifByteOrder", err)
		}
		m.SetCaption("keep CMT2")
		seg, err := cr3.ExtractSegments(bytes.NewReader(writeCR3(t, src, m)))
		if err != nil || !bytes.Equal(seg.ExifIFD, cmt2) {
			t.Errorf("CMT2 changed by a write that never exposed it (err=%v)", err)
		}
	})
}
