package gometadata

// write_task366_test.go — task #366: the Sony DSC-HX5V (2).jpg IFD0 holds an
// entry of tag 0x0001 and type 0 in place of XResolution. Tag order put it
// first in the written IFD0, and ExifTool takes an IFD whose first entry has
// an invalid type for corrupted (Exif.pm ProcessExif), so it read no IFD0, no
// ExifIFD and no MakerNote from the output. TIFF 6.0 §2, p. 16: "The reader
// should skip over the field".

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteKeepsMakerNoteReadable_UnknownTypeEntry(t *testing.T) {
	t.Parallel()
	path := filepath.Join("testdata", "corpus", "jpeg", "metadata-extractor", "Sony DSC-HX5V (2).jpg")
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}

	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if m.EXIF == nil || len(m.EXIF.MakerNote) == 0 {
		t.Fatal("the fixture has no MakerNote")
	}
	wantMN := bytes.Clone(m.EXIF.MakerNote)

	m.SetCaption("caption")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatal(err)
	}

	// The first IFD0 entry of the written EXIF must have a type ExifTool
	// accepts (TIFF 6.0 §2 Table 1, types 1-12; 13 IFD; 129 UTF-8).
	b := out.Bytes()
	i := bytes.Index(b, []byte("Exif\x00\x00"))
	if i < 0 {
		t.Fatal("no EXIF APP1 in the output")
	}
	tiff := b[i+6:]
	if len(tiff) < 20 {
		t.Fatal("EXIF APP1 too short")
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if tiff[0] == 'M' {
		bo = binary.BigEndian
	}
	ifd0 := bo.Uint32(tiff[4:])
	if typ := bo.Uint16(tiff[ifd0+4:]); typ == 0 || (typ > 13 && typ != 129) {
		t.Fatalf("the first IFD0 entry of the output has type %d", typ)
	}

	m2, err := Read(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	// The Sony MakerNote offsets are TIFF-relative, so the writer rebases
	// them and the bytes differ; the length and the 12-byte header stay.
	if m2.EXIF == nil || len(m2.EXIF.MakerNote) != len(wantMN) || !bytes.HasPrefix(m2.EXIF.MakerNote, wantMN[:12]) {
		t.Fatal("the MakerNote of the output differs in length or header from the source")
	}
}
