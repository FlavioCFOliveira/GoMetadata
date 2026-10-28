package gometadata

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// nikonLensEXIF parses a synthetic Nikon stream whose MakerNote holds the
// Lens tag "50mm f/1.8".
func nikonLensEXIF(t *testing.T) *exif.EXIF {
	t.Helper()
	rat := func(v ...uint32) []byte {
		var b []byte
		for _, x := range v {
			b = binary.LittleEndian.AppendUint32(b, x)
		}
		return b
	}
	tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
		Make: "NIKON CORPORATION", Order: binary.LittleEndian, TIFFRelative: true,
		Entries: []testutil.MNEntry{{Tag: 0x0084, Type: 5, Count: 4, Value: rat(50, 1, 50, 1, 18, 10, 18, 10)}},
	})
	e, err := exif.Parse(tiff)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	return e
}

const auxLensPacket = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:aux="http://ns.adobe.com/exif/1.0/aux/" aux:Lens="XMP Lens 35mm"/></rdf:RDF></x:xmpmeta>`

// TestLensModel_MakerNotePrecedence (task #396): the MakerNote is the last
// source of LensModel: the EXIF tag 0xA434 and the XMP lens win, and a file
// without any of them falls back to the MakerNote.
func TestLensModel_MakerNotePrecedence(t *testing.T) {
	t.Parallel()
	t.Run("MakerNote only", func(t *testing.T) {
		t.Parallel()
		m := &Metadata{EXIF: nikonLensEXIF(t)}
		if got := m.LensModel(); got != "50mm f/1.8" {
			t.Errorf("LensModel = %q, want the MakerNote lens", got)
		}
	})
	t.Run("EXIF tag wins", func(t *testing.T) {
		t.Parallel()
		e := nikonLensEXIF(t)
		e.SetLensModel("EXIF Lens 24mm")
		x, err := xmp.Parse([]byte(auxLensPacket))
		if err != nil {
			t.Fatal(err)
		}
		m := &Metadata{EXIF: e, XMP: x}
		if got := m.LensModel(); got != "EXIF Lens 24mm" {
			t.Errorf("LensModel = %q, want the EXIF tag", got)
		}
	})
	t.Run("XMP wins over the MakerNote", func(t *testing.T) {
		t.Parallel()
		x, err := xmp.Parse([]byte(auxLensPacket))
		if err != nil {
			t.Fatal(err)
		}
		m := &Metadata{EXIF: nikonLensEXIF(t), XMP: x}
		if got := m.LensModel(); got != "XMP Lens 35mm" {
			t.Errorf("LensModel = %q, want the XMP lens", got)
		}
	})
	t.Run("nothing", func(t *testing.T) {
		t.Parallel()
		if got := (&Metadata{}).LensModel(); got != "" {
			t.Errorf("empty Metadata LensModel = %q", got)
		}
		if got := (&Metadata{EXIF: &exif.EXIF{}}).LensModel(); got != "" {
			t.Errorf("EXIF without MakerNote LensModel = %q", got)
		}
	})
}

// corpusLens reads a corpus file (path relative to testdata/corpus) and
// returns its LensModel. The test is skipped when the file is absent.
func corpusLens(t *testing.T, rel string) string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "corpus", filepath.FromSlash(rel)))
	if err != nil {
		t.Skipf("corpus file %s absent: %v", rel, err)
	}
	defer func() { _ = f.Close() }()
	m, err := Read(f)
	if err != nil {
		t.Fatalf("Read %s: %v", rel, err)
	}
	return m.LensModel()
}

// TestLensModel_CorpusMakerNote (task #396): the files whose lens only the
// MakerNote names return the value that ExifTool 13.x reports for the same
// file (Nikon:Lens, Canon:LensModel). The expected values in lensGained were
// captured once with ExifTool; the test does not run it. Before the fallback LensModel returned "" for each of
// these files.
func TestLensModel_CorpusMakerNote(t *testing.T) {
	t.Parallel()
	for _, r := range lensGained {
		t.Run(r.path, func(t *testing.T) {
			t.Parallel()
			if got := corpusLens(t, r.path); got != r.want {
				t.Errorf("LensModel = %q, want %q (ExifTool)", got, r.want)
			}
		})
	}
	t.Logf("%d corpus files checked", len(lensGained))
}

// lensRemainingReasons documents why a file of
// lensRemaining still has an empty LensModel although ExifTool reports a lens
// for it (task #396).
var lensRemainingReasons = map[string]string{ //nolint:gochecknoglobals // test table
	"canon-id-only": "the MakerNote holds only the numeric LensType (no LensModel tag 0x0095); the id-to-name table of metadata-extractor is converted from ExifTool and is not copied",
	"canon-base-shifted": "the file has a LensModel tag but the value offsets of its Canon MakerNote are shifted (ExifTool: \"Adjusted MakerNotes base by -164\"); " +
		"the library reads the value from the wrong position and finds zeros",
	"sony-id-only":       "the Sony MakerNote holds only the numeric LensID (tag 0xB027); metadata-extractor has no lens table for it",
	"other-manufacturer": "a manufacturer outside the scope of the fallback (Canon, Nikon, Sony), or a lens in a non-MakerNote directory (for example FLIR)",
}

// TestLensModel_CorpusRemaining (task #396): every corpus file for which
// ExifTool reports a lens and LensModel is still empty is listed with a reason.
// A listed file that now returns a lens fails the test: remove it from the list.
func TestLensModel_CorpusRemaining(t *testing.T) {
	t.Parallel()
	counts := map[string]int{}
	for _, r := range lensRemaining {
		if _, ok := lensRemainingReasons[r.reason]; !ok {
			t.Fatalf("%s: unknown reason %q", r.path, r.reason)
		}
		counts[r.reason]++
		t.Run(r.path, func(t *testing.T) {
			t.Parallel()
			if got := corpusLens(t, r.path); got != "" {
				t.Errorf("LensModel = %q: the file is listed as without a lens (%s); remove it from lensRemaining", got, r.reason)
			}
		})
	}
	t.Logf("%d files without a lens: %v", len(lensRemaining), counts)
}
