package gometadata

// write_task358_365_test.go — tasks #362 (MakerNote data outside the EXIF
// stream: the Olympus preview offset, the Kodak private IFD), #363 (ExifVersion
// raised by the setters), #364 (headerless little-endian Nikon MakerNotes) and
// #365 (JPEG height from a DNL marker), through the public API.

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format/jpeg"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// olympusFiles are the corpus JPEGs whose MakerNote holds the offset of a
// preview image after the end of the image (Olympus E-M1, E-M5, TG-850, SH-1,
// SP-100EE; each is also in the corpus as a "_2" copy).
var olympusFiles = []string{"Olympus E-M1.jpg", "Olympus E-M5.jpg", "Olympus TG-850.jpg", "Olympus SH-1.jpg", "Olympus SP-100EE.jpg"} //nolint:gochecknoglobals // immutable test data

// extractRawEXIF returns the EXIF stream of the JPEG file.
func extractRawEXIF(t *testing.T, file []byte) []byte {
	t.Helper()
	raw, _, _, err := jpeg.Extract(bytes.NewReader(file))
	if err != nil || raw == nil {
		t.Fatalf("jpeg.Extract: %v", err)
	}
	return raw
}

// previewOf returns the bytes that the Olympus MakerNote offset of the JPEG
// file addresses.
func previewOf(t *testing.T, file []byte) []byte {
	t.Helper()
	raw := extractRawEXIF(t, file)
	offs := exif.ExternalOffsets(raw)
	if len(offs) != 1 {
		t.Fatalf("exif.ExternalOffsets = %v, want one offset", offs)
	}
	tiff := bytes.Index(file, []byte("Exif\x00\x00")) + 6
	at := tiff + offs[0].Base + int(offs[0].Value)
	if at < 0 || at+int(offs[0].Length) > len(file) {
		t.Fatalf("the offset addresses [%d,+%d), outside the %d-byte file", at, offs[0].Length, len(file))
	}
	return file[at : at+int(offs[0].Length)]
}

// TestWrite_Task362_OlympusPreviewFollowsTheWrite (task #362): after a
// caption write the MakerNote of each Olympus body addresses the preview image
// that follows the end of the image, byte for byte.
func TestWrite_Task362_OlympusPreviewFollowsTheWrite(t *testing.T) {
	t.Parallel()
	for _, name := range olympusFiles {
		t.Run("R-27/"+name, func(t *testing.T) {
			t.Parallel()
			src, out := corpusWrite(t, "jpeg", "metadata-extractor", name)
			want := previewOf(t, src)
			if !bytes.HasPrefix(want, []byte{0xFF, 0xD8}) {
				t.Fatal("the source offset does not address a JPEG")
			}
			if got := previewOf(t, out); !bytes.Equal(got, want) {
				t.Errorf("the offset of the output addresses %d bytes that are not the preview (%d bytes)", len(got), len(want))
			}
			if len(out) == len(src) {
				t.Error("the write changed nothing: the test proves nothing")
			}
		})
	}
}

// TestWrite_Task362_OlympusPreviewWithXMPOnly: a write that adds XMP and IPTC
// (and so moves the image data) keeps the preview addressed too.
func TestWrite_Task362_OlympusPreviewWithXMPOnly(t *testing.T) {
	t.Parallel()
	t.Run("R-27", func(t *testing.T) {
		t.Parallel()
		src := corpusBytes(t, "jpeg", "metadata-extractor", "Olympus E-M1.jpg")
		m, err := Read(bytes.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		m.SetKeywords([]string{"alpha", "beta"})
		var out bytes.Buffer
		if err := Write(bytes.NewReader(src), &out, m); err != nil {
			t.Fatal(err)
		}
		if got, want := previewOf(t, out.Bytes()), previewOf(t, src); !bytes.Equal(got, want) {
			t.Error("the preview is not where the MakerNote offset says after an XMP and IPTC write")
		}
	})
}

// writeTemp writes data to path (a temporary file of the test).
func writeTemp(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// exiftoolOut runs exiftool with args over path and returns its standard
// output.
func exiftoolOut(t *testing.T, bin string, args ...string) []byte {
	t.Helper()
	out, _ := exec.Command(bin, args...).Output() //nolint:gosec // G204: the binary is the tool this test exists to run
	return out
}

// TestCorpus_Task362_ExifToolReadsThePreviewAndTheKodakIFD: the ExifTool oracle
// of the acceptance criteria. For the five Olympus bodies "exiftool -b
// -PreviewImage" of the output equals the input's and draws no new warning; for
// the Kodak ProBack "exiftool -G1 -a -s -MakerNotes:all" lists the same tags
// and values.
func TestCorpus_Task362_ExifToolReadsThePreviewAndTheKodakIFD(t *testing.T) {
	t.Parallel()
	t.Run("R-27", func(t *testing.T) {
		t.Parallel()
		bin := exiftoolPath(t)
		dir := t.TempDir()
		for _, name := range append(append([]string{}, olympusFiles...), "Kodak ProBack.jpg") {
			src, out := corpusWrite(t, "jpeg", "metadata-extractor", name)
			in, o := dir+"/in-"+strings.ReplaceAll(name, " ", "_"), dir+"/out-"+strings.ReplaceAll(name, " ", "_")
			writeTemp(t, in, src)
			writeTemp(t, o, out)
			if strings.HasPrefix(name, "Kodak") {
				a := exiftoolOut(t, bin, "-G1", "-a", "-s", "-MakerNotes:all", in)
				b := exiftoolOut(t, bin, "-G1", "-a", "-s", "-MakerNotes:all", o)
				if len(a) == 0 || !bytes.Equal(a, b) {
					t.Errorf("%s: the MakerNote tags that ExifTool reads differ after the write:\nin:\n%s\nout:\n%s", name, a, b)
				}
				continue
			}
			a := exiftoolOut(t, bin, "-b", "-PreviewImage", in)
			b := exiftoolOut(t, bin, "-b", "-PreviewImage", o)
			if len(a) == 0 || !bytes.Equal(a, b) {
				t.Errorf("%s: ExifTool reads a preview of %d bytes after the write, %d before", name, len(b), len(a))
			}
		}
		w := exiftoolWarnings(t, bin, []string{dir + "/in-Olympus_E-M1.jpg", dir + "/out-Olympus_E-M1.jpg"})
		for msg := range w[dir+"/out-Olympus_E-M1.jpg"] {
			if strings.Contains(msg, "PreviewImage") {
				t.Errorf("ExifTool warns about the preview of the output: %s", msg)
			}
		}
	})
}

// TestWrite_Task363_SetDateTimeOriginalRaisesExifVersion (task #363) through the
// public API: a JPEG whose ExifVersion is 0220 gets OffsetTimeOriginal (Exif
// 2.31) from SetDateTimeOriginal and the version is raised to 0231; a higher
// version stays.
func TestWrite_Task363_SetDateTimeOriginalRaisesExifVersion(t *testing.T) {
	t.Parallel()
	when := time.Date(2024, 5, 6, 7, 8, 9, 0, time.FixedZone("", 3600))
	for _, tc := range []struct{ have, want string }{
		{"0220", "0231"}, {"0221", "0231"}, {"0230", "0231"}, {"0231", "0231"}, {"0232", "0232"}, {"0300", "0300"},
	} {
		t.Run("S-49/"+tc.have, func(t *testing.T) {
			t.Parallel()
			e := exif.New(true)
			e.ExifIFD.Get(exif.TagExifVersion).Value = []byte(tc.have)
			raw, err := exif.Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			var src bytes.Buffer
			if err := jpeg.Inject(bytes.NewReader(buildMinimalJPEG(nil)), &src, raw, nil, nil, true); err != nil {
				t.Fatal(err)
			}
			m, err := Read(bytes.NewReader(src.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			m.SetDateTimeOriginal(when)
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src.Bytes()), &out, m); err != nil {
				t.Fatal(err)
			}
			m2, err := Read(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if m2.EXIF.ExifIFD.Get(exif.TagOffsetTimeOriginal) == nil {
				t.Fatal("OffsetTimeOriginal was not written")
			}
			if v := m2.EXIF.ExifIFD.Get(exif.TagExifVersion); v == nil || string(v.Value) != tc.want {
				t.Errorf("ExifVersion = %v, want %q", v, tc.want)
			}
		})
	}
}

// TestRead_Task364_HeaderlessLittleEndianNikon (task #364 AC): the corpus
// Nikon E990, E5000 and Coolpix 775 write their MakerNote IFD without a header
// and in little-endian order; ReadFile gives the IFD with as many entries as
// "exiftool -v3" reports for the MakerNotes directory (16, 21, 18).
func TestRead_Task364_HeaderlessLittleEndianNikon(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		entries int
	}{
		{"Nikon E990.jpg", 16},
		{"Nikon E5000.jpg", 21},
		{"Nikon Coolpix 775.jpg", 18},
	} {
		t.Run("R-30/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := corpusBytes(t, "jpeg", "metadata-extractor", tc.name)
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			if m.EXIF == nil || m.EXIF.MakerNoteIFD == nil {
				t.Fatal("no MakerNote IFD")
			}
			if got := len(m.EXIF.MakerNoteIFD.Entries); got != tc.entries {
				t.Errorf("MakerNote entries = %d, want %d", got, tc.entries)
			}
		})
	}
}

// TestWrite_Task365_JPEGHeightFromDNL (task #365 AC): beach.jpg declares zero
// lines in its frame header and the 200 lines in a DNL segment after the scan
// (ITU-T T.81 B.2.5). A caption write creates the EXIF with PixelXDimension and
// PixelYDimension, as for any other JPEG (Exif 3.1 Table 18, p. 95).
func TestWrite_Task365_JPEGHeightFromDNL(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-DNL-01", func(t *testing.T) {
		t.Parallel()
		src := corpusBytes(t, "jpeg", "metadata-extractor", "beach.jpg")
		m, err := Read(bytes.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		if m.EXIF != nil {
			t.Skip("beach.jpg carries EXIF in this corpus: the write would not create the dimensions")
		}
		out := writeCaption(t, src, "beach")
		m2, err := Read(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if w, h, ok := m2.ImageSize(); !ok || w != 200 || h != 200 {
			t.Errorf("ImageSize = %d x %d (ok %v), want 200 x 200", w, h, ok)
		}
		if m2.EXIF.ExifIFD.Get(exif.TagPixelXDimension) == nil || m2.EXIF.ExifIFD.Get(exif.TagPixelYDimension) == nil {
			t.Error("PixelXDimension or PixelYDimension missing")
		}
	})
}

// TestWrite_Task362_OlympusSyntheticThroughWrite: a JPEG built here, with a
// MakerNote preview offset and a preview after the image, keeps the preview
// addressed through Write.
func TestWrite_Task362_OlympusSyntheticThroughWrite(t *testing.T) {
	t.Parallel()
	t.Run("R-27", func(t *testing.T) {
		t.Parallel()
		le := binary.LittleEndian
		preview := append([]byte{0xFF, 0xD8, 0xFF, 0xDB, 0x00, 0x03, 0x00}, append(bytes.Repeat([]byte("p"), 64), 0xFF, 0xD9)...)
		build := func(start uint32) []byte {
			tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
				Make: "OLYMPUS IMAGING CORP.", Order: le, Prefix: []byte("OLYMPUS\x00II\x03\x00"), PadBefore: 6,
				Entries: []testutil.MNEntry{{Tag: 0x2020, Sub: &testutil.MNSub{Entries: []testutil.MNEntry{
					{Tag: 0x0101, Type: 4, Count: 1, Value: le.AppendUint32(nil, start)},
					{Tag: 0x0102, Type: 4, Count: 1, Value: le.AppendUint32(nil, uint32(len(preview)))}, //nolint:gosec // G115: a few dozen bytes
				}}}},
			})
			var b bytes.Buffer
			if err := jpeg.Inject(bytes.NewReader(buildMinimalJPEG(nil)), &b, tiff, nil, nil, true); err != nil {
				t.Fatal(err)
			}
			return append(b.Bytes(), preview...)
		}
		// The preview sits at len(file)-len(preview); its offset counts from the
		// MakerNote, which is at the TIFF offset the builder chose, 12 bytes into
		// the file.
		probe := build(0)
		e0, err := exif.Parse(extractRawEXIF(t, probe))
		if err != nil {
			t.Fatal(err)
		}
		src := build(uint32(len(probe)-len(preview)) - 12 - e0.MakerNoteOffset) //nolint:gosec // G115: tiny test sizes
		if got := previewOf(t, src); !bytes.Equal(got, preview) {
			t.Fatalf("fixture: the offset addresses %q", got)
		}
		out := writeCaption(t, src, "a caption that makes the EXIF bigger")
		if got := previewOf(t, out); !bytes.Equal(got, preview) {
			t.Errorf("after Write the offset addresses %q, want the preview", got)
		}
	})
}
