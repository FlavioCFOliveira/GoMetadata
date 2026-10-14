package gometadata

// cr3_task300_test.go — task #300: Read returns the XMP packet of the
// top-level XMP uuid box of a CR3 file, the Canon MakerNote IFD (CMT3) and
// the GPS IFD (CMT4); a caption write stores the XMP in that uuid box, where
// ExifTool reads it, and keeps CMT4 unchanged.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/cr3"
	xmppkg "github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// cr3CorpusFiles lists the CR3 corpus files. ExifTool 13 reports
// XMP-xmp:Rating = 0 and GPS:GPSVersionID = 2.3.0.0 for every one of them
// (exiftool -XMP:all -GPS:all).
var cr3CorpusFiles = []string{ //nolint:gochecknoglobals // read-only test table
	"exiftool/CanonRaw.cr3",
	"exiv2/Canon-R6-pruned.CR3",
	"metadata-extractor/_MG_0039_2.CR3",
	"metadata-extractor/_MG_0039.CR3",
	"metadata-extractor/_MG_0045_2.CR3",
	"metadata-extractor/_MG_0045.CR3",
	"metadata-extractor/Canon EOS R3_2.cr3",
	"metadata-extractor/Canon EOS R3.cr3",
	"metadata-extractor/IMG_0036_2.CR3",
	"metadata-extractor/IMG_0036.CR3",
	"metadata-extractor/IMG_0041_2.CR3",
	"metadata-extractor/IMG_0041.CR3",
}

func readCR3Corpus(t *testing.T, rel string) []byte {
	t.Helper()
	path := filepath.Join("testdata", "corpus", "raw", rel)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCR3_Task300_CorpusRead(t *testing.T) {
	t.Parallel()
	for _, rel := range cr3CorpusFiles {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(readCR3Corpus(t, rel)))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if m.XMP == nil || m.XMP.Get(xmppkg.NSxmp, "Rating") != "0" {
				t.Errorf("XMP Rating = %v, want \"0\" (ExifTool XMP-xmp:Rating)", m.XMP)
			}
			if m.EXIF == nil || m.EXIF.GPSIFD == nil {
				t.Fatal("GPS IFD (CMT4) not read")
			}
			if v := m.EXIF.GPSIFD.Get(0x0000); v == nil || !bytes.Equal(v.Value, []byte{2, 3, 0, 0}) {
				t.Errorf("GPSVersionID = %v, want 2.3.0.0", v)
			}
			if m.EXIF.MakerNoteIFD == nil || len(m.EXIF.MakerNoteIFD.Entries) == 0 {
				t.Error("MakerNote IFD (CMT3) not read")
			}
		})
	}
}

func TestCR3_Task300_CaptionWrite(t *testing.T) {
	t.Parallel()
	src := readCR3Corpus(t, "metadata-extractor/_MG_0039.CR3")
	before, err := cr3.ExtractSegments(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	out := writeCaption(t, src, "task 300")
	after, err := cr3.ExtractSegments(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	x, err := xmppkg.Parse(after.XMP)
	if err != nil || x.Caption() != "task 300" || x.Get(xmppkg.NSxmp, "Rating") != "0" {
		t.Errorf("top-level XMP: caption %q, rating %q (%v); want the new caption and the kept rating", x.Caption(), x.Get(xmppkg.NSxmp, "Rating"), err)
	}
	if !bytes.Equal(after.GPS, before.GPS) || !bytes.Equal(after.MakerNote, before.MakerNote) {
		t.Error("CMT3 or CMT4 changed although neither was modified")
	}
	e, err := exif.Parse(after.EXIF)
	if err != nil || e.GPSIFD != nil {
		t.Errorf("CMT1: GPS IFD present=%v (%v); GPS belongs in CMT4", e != nil && e.GPSIFD != nil, err)
	}
	m, err := Read(bytes.NewReader(out))
	if err != nil || m.Caption() != "task 300" || m.CameraModel() != "Canon EOS R8" {
		t.Errorf("re-read: caption %q, model %q (%v)", m.Caption(), m.CameraModel(), err)
	}
}

// TestCR3_Task300_GPSByteOrderMismatch: a CMT4 stream in the other byte
// order than CMT1 is reported and not attached, because exif.Encode could
// not write it back intact.
func TestCR3_Task300_GPSByteOrderMismatch(t *testing.T) {
	t.Parallel()
	le, err := exif.Encode(&exif.EXIF{ByteOrder: binary.LittleEndian, IFD0: &exif.IFD{Entries: []exif.IFDEntry{{Tag: 0x0110, Type: exif.TypeASCII, Count: 2, Value: []byte("M\x00")}}}})
	if err != nil {
		t.Fatal(err)
	}
	be, err := exif.Encode(&exif.EXIF{ByteOrder: binary.BigEndian, IFD0: &exif.IFD{Entries: []exif.IFDEntry{{Tag: 0x0000, Type: exif.TypeByte, Count: 4, Value: []byte{2, 3, 0, 0}}}}})
	if err != nil {
		t.Fatal(err)
	}
	box := func(typ string, body []byte) []byte {
		return append(binary.BigEndian.AppendUint32(nil, uint32(8+len(body))), append([]byte(typ), body...)...) //nolint:gosec // G115: small test box
	}
	canonUUID := []byte{0x85, 0xC0, 0xB6, 0x87, 0x82, 0x0F, 0x11, 0xE0, 0x81, 0x11, 0xF4, 0xCE, 0x46, 0x2B, 0x6A, 0x48}
	uuid := box("uuid", append(append([]byte{}, canonUUID...), append(box("CMT1", le), box("CMT4", be)...)...))
	file := append([]byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 1}, box("moov", uuid)...)

	m, err := Read(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.EXIF == nil || m.EXIF.GPSIFD != nil {
		t.Fatal("a GPS IFD in the other byte order was attached")
	}
	found := false
	for _, w := range m.ParseWarnings {
		if errors.Is(w, errCR3GPSByteOrder) {
			found = true
		}
	}
	if !found {
		t.Errorf("ParseWarnings = %v, want errCR3GPSByteOrder", m.ParseWarnings)
	}
	if _, err := Read(bytes.NewReader(file), Strict()); !errors.Is(err, errCR3GPSByteOrder) {
		t.Errorf("strict Read error = %v, want errCR3GPSByteOrder", err)
	}
}
