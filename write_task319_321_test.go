package gometadata

// write_task319_321_test.go — tasks #319 and #321 through the public API:
// the IFD1 pointers of a written JPEG are valid, and EXIF created by a write
// carries the tags that Exif 3.1 marks mandatory (S-46, S-47, S-48,
// docs/conformance/exif-tiff.md).

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
)

// jpegWithFrame builds a minimal JPEG whose frame header declares width x
// height, and no EXIF.
func jpegWithFrame(width, height uint16) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})
	sof := []byte{0xFF, 0xC0, 0x00, 0x11, 0x08}
	sof = binary.BigEndian.AppendUint16(sof, height)
	sof = binary.BigEndian.AppendUint16(sof, width)
	sof = append(sof, 3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1)
	b.Write(sof)
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return b.Bytes()
}

// exifOf returns the EXIF of the written file.
func exifOf(t *testing.T, data []byte) *exif.EXIF {
	t.Helper()
	m, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.EXIF == nil {
		t.Fatal("no EXIF in the output")
	}
	return m.EXIF
}

// TestWrite_Task321_CreatedEXIFHasTheMandatoryTags: SetCaption, SetGPS and
// SetDateTimeOriginal on a JPEG without EXIF create IFD0, the Exif IFD and the
// GPS IFD with the tags of Exif 3.1 Tables 17-19 for a compressed primary
// image, the pixel dimensions coming from the frame header of the JPEG.
func TestWrite_Task321_CreatedEXIFHasTheMandatoryTags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		set  func(m *Metadata)
	}{
		{"SetCaption", func(m *Metadata) { m.SetCaption("created") }},
		{"SetGPS", func(m *Metadata) { m.SetGPS(38.7, -9.1) }},
		{"SetDateTimeOriginal", func(m *Metadata) { m.SetDateTimeOriginal(time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)) }},
	} {
		t.Run("S-46/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := jpegWithFrame(640, 480)
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			tc.set(m)
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src), &out, m); err != nil {
				t.Fatalf("Write: %v", err)
			}
			e := exifOf(t, out.Bytes())
			if y := e.IFD0.Get(exif.TagYCbCrPositioning); y == nil || y.Uint16() != 1 {
				t.Errorf("IFD0 YCbCrPositioning = %v, want 1", y)
			}
			if e.ExifIFD == nil {
				t.Fatal("no Exif IFD (Table 17: the pointer is mandatory)")
			}
			if v := e.ExifIFD.Get(exif.TagExifVersion); v == nil || string(v.Value) != "0300" {
				t.Errorf("ExifVersion = %v, want 0300", v)
			}
			if c := e.ExifIFD.Get(exif.TagColorSpace); c == nil || c.Uint16() != 0xFFFF {
				t.Errorf("ColorSpace = %v, want 0xFFFF", c)
			}
			if c := e.ExifIFD.Get(exif.TagComponentsConfiguration); c == nil || !bytes.Equal(c.Value, []byte{1, 2, 3, 0}) {
				t.Errorf("ComponentsConfiguration = %v, want 1 2 3 0", c)
			}
			if w, h, ok := e.ImageSize(); !ok || w != 640 || h != 480 {
				t.Errorf("PixelX/YDimension = %d x %d (%v), want 640 x 480 from the frame header", w, h, ok)
			}
			if tc.name == "SetGPS" {
				if v := e.GPSIFD.Get(exif.TagGPSVersionID); v == nil || !bytes.Equal(v.Value, []byte{2, 4, 0, 0}) {
					t.Errorf("GPSVersionID = %v, want 2 4 0 0", v)
				}
			}
		})
	}
}

// TestWrite_Task321_OtherContainersGetOnlyWhatEveryImageNeeds: PNG and WebP are
// not JPEG-compressed primary images: the tags that Tables 17-18 do not allow
// for them (YCbCrPositioning, ComponentsConfiguration, PixelX/YDimension) are
// not written; ExifVersion and ColorSpace are.
func TestWrite_Task321_OtherContainersGetOnlyWhatEveryImageNeeds(t *testing.T) {
	t.Parallel()
	for name, src := range map[string][]byte{"PNG": buildMinimalPNG(), "WebP": buildMinimalWebP()} {
		t.Run("S-46/"+name, func(t *testing.T) {
			t.Parallel()
			e := exifOf(t, writeCaption(t, src, "created"))
			if e.IFD0.Get(exif.TagYCbCrPositioning) != nil {
				t.Error("YCbCrPositioning written for a primary image that is not JPEG")
			}
			if e.ExifIFD == nil || e.ExifIFD.Get(exif.TagExifVersion) == nil || e.ExifIFD.Get(exif.TagColorSpace) == nil {
				t.Fatal("the Exif IFD lacks ExifVersion or ColorSpace")
			}
			for _, tag := range []exif.TagID{exif.TagComponentsConfiguration, exif.TagPixelXDimension, exif.TagPixelYDimension} {
				if e.ExifIFD.Get(tag) != nil {
					t.Errorf("tag %#04x written for a primary image that is not JPEG", tag)
				}
			}
		})
	}
}

// TestWrite_Task321_ExistingEXIFIsNotCompleted (S-47): a JPEG whose EXIF lacks
// the mandatory tags keeps lacking them: a write changes the caption and
// nothing else in IFD0.
func TestWrite_Task321_ExistingEXIFIsNotCompleted(t *testing.T) {
	t.Parallel()
	src := buildMinimalJPEG(minimalTIFFPayload())
	before := exifOf(t, src)
	after := exifOf(t, writeCaption(t, src, "caption"))
	if after.ExifIFD != nil {
		t.Errorf("an Exif IFD appeared in an EXIF that had none: %v", after.ExifIFD.Entries)
	}
	if len(after.IFD0.Entries) != len(before.IFD0.Entries) {
		t.Errorf("IFD0 went from %d to %d entries", len(before.IFD0.Entries), len(after.IFD0.Entries))
	}
	for _, tag := range []exif.TagID{exif.TagYCbCrPositioning, exif.TagExifIFDPointer} {
		if after.IFD0.Get(tag) != nil {
			t.Errorf("tag %#04x added to an existing IFD0", tag)
		}
	}
	if got := after.IFD0.Get(exif.TagImageDescription); got == nil || got.String() != "caption" {
		t.Errorf("ImageDescription = %v, want the caption", got)
	}
}

// TestWrite_Task321_FrameHeaderUnreadableLeavesTheSizeOut: a JPEG with no
// frame header gets the other mandatory tags and no invented dimensions.
func TestWrite_Task321_FrameHeaderUnreadableLeavesTheSizeOut(t *testing.T) {
	t.Parallel()
	e := exifOf(t, writeCaption(t, buildMinimalJPEG(nil), "no frame"))
	if e.ExifIFD == nil || e.ExifIFD.Get(exif.TagPixelXDimension) != nil || e.ExifIFD.Get(exif.TagComponentsConfiguration) == nil {
		t.Errorf("Exif IFD = %v: want ComponentsConfiguration and no PixelXDimension", e.ExifIFD)
	}
}

// TestWrite_Task321_ReadSetsCompressedPrimary: a parsed JPEG EXIF without an
// Exif IFD gets the tags of a compressed primary image from the setters.
func TestWrite_Task321_ReadSetsCompressedPrimary(t *testing.T) {
	t.Parallel()
	src := buildMinimalJPEG(minimalTIFFPayload())
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if m.Format() != format.FormatJPEG || !m.EXIF.CompressedPrimary {
		t.Fatalf("format %v, CompressedPrimary %v", m.Format(), m.EXIF.CompressedPrimary)
	}
	m.SetISO(200)
	if m.EXIF.ExifIFD == nil || m.EXIF.ExifIFD.Get(exif.TagComponentsConfiguration) == nil || !m.EXIF.NeedsImageSize() {
		t.Errorf("Exif IFD created by SetISO: %v", m.EXIF.ExifIFD)
	}
}

// TestCorpus_Task321_ExifToolSeesNoMissingTag (task #321 AC): ExifTool's
// validation reports no "Missing required" tag for the output of SetCaption on
// corpus files without EXIF; it needs the corpus and the exiftool binary.
func TestCorpus_Task321_ExifToolSeesNoMissingTag(t *testing.T) {
	t.Parallel()
	bin := exiftoolPath(t)
	for _, name := range [][]string{
		{"jpeg", "metadata-extractor", "Google Cardboard.jpg"},
	} {
		t.Run("S-46/"+name[len(name)-1], func(t *testing.T) {
			t.Parallel()
			src := corpusBytes(t, name...)
			if m, err := Read(bytes.NewReader(src)); err != nil || m.EXIF != nil {
				t.Fatalf("corpus file %v is expected to carry no EXIF (read error %v): it exercises EXIF creation", name, err)
			}
			p := filepath.Join(t.TempDir(), "out.jpg")
			if err := os.WriteFile(p, writeCaption(t, src, "created"), 0o600); err != nil {
				t.Fatal(err)
			}
			out, _ := exec.Command(bin, "-validate", "-warning", "-a", p).Output() //nolint:gosec // G204: the binary is the tool this test exists to run
			if strings.Contains(string(out), "Missing required") {
				t.Errorf("ExifTool reports a missing mandatory tag:\n%s", out)
			}
		})
	}
}

// TestCorpus_Task319_Olympus800IFD1Pointers (task #319 AC): the IFD1 of the
// Olympus μ Digital 800, a copy of the primary image's tags with an Exif IFD
// and a GPS IFD of its own, is written with valid pointers: ExifTool reports
// no warning that the input did not draw, and the Exif IFD of IFD1 keeps its
// 23 entries.
func TestCorpus_Task319_Olympus800IFD1Pointers(t *testing.T) {
	t.Parallel()
	src := corpusBytes(t, "jpeg", "metadata-extractor", "Olympus μ Digital 800.JPG")
	out := writeCaption(t, src, "ifd1")
	e := exifOf(t, out)
	ifd1 := e.IFD0.Next
	if ifd1 == nil || ifd1.Sub == nil || ifd1.Sub.Exif == nil || ifd1.Sub.GPS == nil {
		t.Fatalf("IFD1 sub-IFDs after the write: %+v", ifd1)
	}
	before := exifOf(t, src).IFD0.Next.Sub
	if len(ifd1.Sub.Exif.Entries) != len(before.Exif.Entries) || len(ifd1.Sub.GPS.Entries) != len(before.GPS.Entries) {
		t.Errorf("IFD1 Exif/GPS entries: %d/%d after, %d/%d before",
			len(ifd1.Sub.Exif.Entries), len(ifd1.Sub.GPS.Entries), len(before.Exif.Entries), len(before.GPS.Entries))
	}
	bin := exiftoolPath(t)
	warn := func(data []byte) string {
		p := filepath.Join(t.TempDir(), "x.jpg")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		b, _ := exec.Command(bin, "-validate", "-warning", "-a", p).Output() //nolint:gosec // G204: the binary is the tool this test exists to run
		return string(b)
	}
	have := map[string]bool{}
	for _, l := range strings.Split(warn(src), "\n") {
		have[l] = true
	}
	for _, l := range strings.Split(warn(out), "\n") {
		if strings.HasPrefix(l, "Warning") && !have[l] {
			t.Errorf("new ExifTool warning: %s", l)
		}
	}
}

// TestWrite_Task321_ConcurrentWritesAndSetters: Write completes the EXIF of a
// Metadata (the pixel dimensions) while other goroutines write and set; run
// with -race.
func TestWrite_Task321_ConcurrentWritesAndSetters(t *testing.T) {
	t.Parallel()
	src := jpegWithFrame(320, 200)
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	m.SetCaption("concurrent")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%4 == 3 {
				m.SetISO(100)
				return
			}
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src), &out, m); err != nil {
				t.Errorf("Write: %v", err)
			}
		}()
	}
	wg.Wait()
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatal(err)
	}
	if w, h, ok := exifOf(t, out.Bytes()).ImageSize(); !ok || w != 320 || h != 200 {
		t.Errorf("ImageSize = %d x %d (%v), want 320 x 200", w, h, ok)
	}
}
