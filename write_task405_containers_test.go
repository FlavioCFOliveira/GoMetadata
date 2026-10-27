package gometadata

// write_task405_containers_test.go — task #405 audit: in every container, a
// write that modifies one segment leaves the segments it does not modify as the
// image has them. The check runs on the committed fixtures and on the optional
// corpus (testdata/download.sh): Read, MarkModified of one segment the image
// holds, Write, Read again, and compare the other segments.
//
// MarkModified marks exactly one segment, which the Set* methods cannot do for
// every container (SetCaption touches all three, SetGPS two), so the single
// segment cases use it; the PNG test of write_task405_test.go also runs setters.

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// t405TagIDs returns the tag IDs of the entries of the IFDs of e that are not
// the IPTC, XMP, Photoshop or image-block tags, which a TIFF-based write moves
// or rebuilds by design, as "ifd:tag" strings in IFD order.
func t405TagIDs(e *exif.EXIF) []string {
	if e == nil {
		return nil
	}
	var out []string
	for _, ifd := range []struct {
		name string
		ifd  *exif.IFD
	}{{"0", e.IFD0}, {"exif", e.ExifIFD}, {"gps", e.GPSIFD}, {"interop", e.InteropIFD}} {
		for i := ifd.ifd; i != nil; i = i.Next {
			for _, en := range i.Entries {
				switch en.Tag {
				case 0x83BB, 0x8649, 0x02BC: // IPTC-NAA, Photoshop IRB, XMP
					continue
				}
				out = append(out, ifd.name+":"+string(rune('A'+en.Tag>>12))+string(rune('A'+en.Tag>>8&15))+string(rune('A'+en.Tag>>4&15))+string(rune('A'+en.Tag&15)))
			}
		}
	}
	return out
}

// t405InvalidExifIFD are corpus TIFFs whose Exif IFD pointer leads to garbage
// (ExifTool reads no ExifIFD tag from them, and says "Bad ExifIFD directory" of
// one): a TIFF write drops the pointer and the garbage entries, which is not the
// loss of a segment, so their EXIF tags are not compared.
var t405InvalidExifIFD = []string{ //nolint:gochecknoglobals // immutable test table
	"0c84d07e1b22b76f24cccc70d8788e4a", "m3-42c19f8e79e582bef107f372f18a074b", "3755b21390a93c1474aef88f4162de17",
}

// t405Listed reports whether the base name of path starts with one of names.
func t405Listed(path string, names []string) bool {
	base := filepath.Base(path)
	for _, n := range names {
		if strings.HasPrefix(base, n) {
			return true
		}
	}
	return false
}

// TestTask405_UnmodifiedSegmentsSurviveInEveryContainer is the per-container
// audit of tasks #405: no container removes or changes a segment that was not
// modified. EXIF of a TIFF-based container is compared by its tags, not its
// bytes: the writer lays its IFDs out again whichever segment changed (TIFF 6.0
// §2: offsets are absolute); every other segment is compared by its bytes.
func TestTask405_UnmodifiedSegmentsSurviveInEveryContainer(t *testing.T) {
	t.Parallel()
	var files []string
	for _, root := range []string{filepath.Join("testdata", "fixtures"), filepath.Join("testdata", "corpus")} {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, filepath.ToSlash(p))
			}
			return err
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	sort.Strings(files)
	pinned := readCorpusRejects(t)

	type key struct{ format, seg string }
	cases := map[key]int{}
	diffs := map[key][]string{}
	segName := map[Segment]string{SegmentEXIF: "EXIF", SegmentIPTC: "IPTC", SegmentXMP: "XMP"}
	for _, f := range files {
		if _, ok := pinned[f]; ok {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []Segment{SegmentEXIF, SegmentIPTC, SegmentXMP} {
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				break
			}
			if (s == SegmentEXIF && m.EXIF == nil) || (s == SegmentIPTC && m.IPTC == nil) || (s == SegmentXMP && m.XMP == nil) {
				continue
			}
			k := key{m.Format().String(), segName[s]}
			cases[k]++
			rawE, rawI, rawX := m.rawEXIF, m.rawIPTC, m.rawXMP
			tagsBefore := t405TagIDs(m.EXIF)
			tiffBased := isTIFFBasedFormat(m.Format())
			m.MarkModified(s)
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src), &out, m); err != nil {
				t.Errorf("%s: modify %s: Write: %v", f, k.seg, err)
				continue
			}
			m2, err := Read(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Errorf("%s: modify %s: Read of the output: %v", f, k.seg, err)
				continue
			}
			var bad []string
			if s != SegmentEXIF {
				if tiffBased {
					if a, b := strings.Join(tagsBefore, ","), strings.Join(t405TagIDs(m2.EXIF), ","); a != b && !t405Listed(f, t405InvalidExifIFD) {
						bad = append(bad, "EXIF tags")
					}
				} else if !bytes.Equal(rawE, m2.rawEXIF) {
					bad = append(bad, "EXIF")
				}
			}
			// A TIFF holds the IPTC as LONG values, which a reader trims of zero
			// padding (tiffscan.TrimIPTCLongPadding); the writer stores tag 0x83BB as
			// TypeLong, so the bytes it writes back are those plus padding.
			if tiffBased {
				rawI, m2.rawIPTC = bytes.TrimRight(rawI, "\x00"), bytes.TrimRight(m2.rawIPTC, "\x00")
			}
			if s != SegmentIPTC && !bytes.Equal(rawI, m2.rawIPTC) {
				bad = append(bad, "IPTC")
			}
			if s != SegmentXMP && !bytes.Equal(rawX, m2.rawXMP) {
				bad = append(bad, "XMP")
			}
			if len(bad) > 0 {
				diffs[k] = append(diffs[k], f+": "+strings.Join(bad, ","))
			}
		}
	}
	keys := make([]key, 0, len(cases))
	for k := range cases {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].format+keys[i].seg < keys[j].format+keys[j].seg })
	for _, k := range keys {
		t.Logf("%-5s modified=%-4s cases=%-4d changed-unmodified-segment=%d", k.format, k.seg, cases[k], len(diffs[k]))
		for _, d := range diffs[k] {
			t.Errorf("%s modified: %s", k.format+" "+k.seg, d)
		}
	}
}
