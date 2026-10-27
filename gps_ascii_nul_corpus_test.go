package gometadata

import (
	"bytes"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// readCorpusMetadata reads a corpus file with Read, skipping the test when the
// corpus is absent (docs/TESTING.md §2.1a).
func readCorpusMetadata(t *testing.T, rel string) (*Metadata, []byte) {
	t.Helper()
	data, err := os.ReadFile(requireCorpusFile(t, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	m, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read %s: %v", rel, err)
	}
	return m, data
}

// TestTask385NokiaSRationalGPS is the regression test of task #385: the GPS
// coordinates that HMD Nokia 8.3 5G cameras write as SRATIONAL are read. The
// expected values are those of ExifTool 13.55 (-n GPSLatitude GPSLongitude).
func TestTask385NokiaSRationalGPS(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file     string
		lat, lon float64
	}{
		{"jpeg/exif-samples/HMD_Nokia_8.3_5G.jpg", 60.14671, 24.90677},
		{"jpeg/exif-samples/HMD_Nokia_8.3_5G_2.jpg", 60.14671, 24.90677},
		{"jpeg/exif-samples/HMD_Nokia_8.3_5G_3.jpg", 60.14671, 24.90677},
		{"jpeg/exif-samples/HMD_Nokia_8.3_5G_hdr.jpg", 60.99138, 24.42419},
		{"jpeg/exif-samples/HMD_Nokia_8.3_5G_hdr_2.jpg", 60.99138, 24.42419},
		{"jpeg/exif-samples/HMD_Nokia_8.3_5G_hdr_3.jpg", 60.99138, 24.42419},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			m, data := readCorpusMetadata(t, tc.file)
			lat, lon, ok := m.GPS()
			if !ok || math.Abs(lat-tc.lat) > 1e-4 || math.Abs(lon-tc.lon) > 1e-4 {
				t.Fatalf("GPS() = (%v, %v, %v), want (%v, %v, true)", lat, lon, ok, tc.lat, tc.lon)
			}
			if m.EXIF.GPSIFD.Get(exif.TagGPSLatitude).Type != exif.TypeSRational {
				t.Fatal("fixture no longer holds an SRATIONAL latitude")
			}

			// Round trip: the GPS IFD keeps the original types and bytes.
			before := gpsEntries(m)
			m.MarkModified(SegmentEXIF)
			var out bytes.Buffer
			if err := Write(bytes.NewReader(data), &out, m); err != nil {
				t.Fatalf("Write: %v", err)
			}
			m2, err := Read(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("Read of the output: %v", err)
			}
			after := gpsEntries(m2)
			if len(before) == 0 || len(before) != len(after) {
				t.Fatalf("GPS IFD has %d entries before and %d after the write", len(before), len(after))
			}
			for tag, b := range before {
				a, found := after[tag]
				if !found || a.typ != b.typ || !bytes.Equal(a.value, b.value) {
					t.Errorf("GPS tag 0x%04X changed in the round trip: %+v -> %+v", tag, b, a)
				}
			}
			if lat2, lon2, ok2 := m2.GPS(); !ok2 || lat2 != lat || lon2 != lon {
				t.Errorf("GPS() after the write = (%v, %v, %v), want (%v, %v, true)", lat2, lon2, ok2, lat, lon)
			}
		})
	}
}

// gpsEntry is the type and value bytes of a GPS IFD entry.
type gpsEntry struct {
	typ   exif.DataType
	value []byte
}

// gpsEntries returns the entries of the GPS IFD of m, by tag.
func gpsEntries(m *Metadata) map[exif.TagID]gpsEntry {
	out := make(map[exif.TagID]gpsEntry)
	if m.EXIF == nil || m.EXIF.GPSIFD == nil {
		return out
	}
	for _, e := range m.EXIF.GPSIFD.Entries {
		out[e.Tag] = gpsEntry{e.Type, bytes.Clone(e.Value)}
	}
	return out
}

// TestTask386ASCIIStopsAtFirstNUL is the regression test of task #386 for EXIF
// and IPTC: a value ends at its first NUL, and the raw bytes are not changed.
func TestTask386ASCIIStopsAtFirstNUL(t *testing.T) {
	t.Parallel()

	t.Run("EXIF CameraModel", func(t *testing.T) {
		t.Parallel()
		tests := []struct{ file, want string }{
			{"jpeg/libexif-testsuite/cve-2012-2813.jpg", "DSC-HX9V"},
			{"jpeg/libexif-testsuite/cve-2012-2814.jpg", "COOLPIX L110"},
			{"jpeg/exif-samples/WWL_(Polaroid)_ION230.jpg", "ION230"},
			{"tiff/metadata-extractor/fa0aa927763ee4c7ebf43a5d05dfebc7.tif", "Kodak i200"},
		}
		for _, tc := range tests {
			t.Run(tc.file, func(t *testing.T) {
				t.Parallel()
				m, data := readCorpusMetadata(t, tc.file)
				got := m.CameraModel()
				if got != tc.want {
					t.Fatalf("CameraModel() = %q, want %q", got, tc.want)
				}
				if strings.IndexByte(got, 0) >= 0 {
					t.Fatal("CameraModel() holds a NUL")
				}

				// The raw entry keeps its NUL bytes through a write.
				orig := m.EXIF.IFD0.Get(exif.TagModel)
				origValue := bytes.Clone(orig.Value)
				m.MarkModified(SegmentEXIF)
				var out bytes.Buffer
				if err := Write(bytes.NewReader(data), &out, m); err != nil {
					t.Fatalf("Write: %v", err)
				}
				m2, err := Read(bytes.NewReader(out.Bytes()))
				if err != nil {
					t.Fatalf("Read of the output: %v", err)
				}
				if got2 := m2.EXIF.IFD0.Get(exif.TagModel); got2 == nil || !bytes.Equal(got2.Value, origValue) {
					t.Errorf("Model bytes changed by the write: %q -> %+v", origValue, got2)
				}
			})
		}
	})

	t.Run("IPTC Caption", func(t *testing.T) {
		t.Parallel()
		for _, file := range []string{
			"tiff/metadata-extractor/m10-76c43508fc007bcf5902b6a28e8055a5.tif",
			"tiff/metadata-extractor/m10-42c19f8e79e582bef107f372f18a074b.tif",
			"tiff/metadata-extractor/m11-76c43508fc007bcf5902b6a28e8055a5.tif",
			"tiff/metadata-extractor/m11-42c19f8e79e582bef107f372f18a074b.tif",
		} {
			t.Run(file, func(t *testing.T) {
				t.Parallel()
				m, _ := readCorpusMetadata(t, file)
				if m.IPTC == nil {
					t.Fatal("no IPTC parsed")
				}
				if got := m.IPTC.Caption(); got != "" {
					t.Errorf("IPTC Caption() = %q, want empty", got)
				}
				if got := m.Caption(); got != "" {
					t.Errorf("Caption() = %q, want empty", got)
				}
			})
		}
	})

	t.Run("IPTC embedded NUL", func(t *testing.T) {
		t.Parallel()
		m, _ := readCorpusMetadata(t, "jpeg/exiv2/issue_1847_poc.jpg")
		if m.IPTC == nil {
			t.Fatal("no IPTC parsed")
		}
		if got := m.IPTC.Caption(); strings.IndexByte(got, 0) >= 0 {
			t.Errorf("IPTC Caption() = %q holds a NUL", got)
		}
	})
}

// TestTask386IPTCTextCutAtNUL tests the IPTC accessors on synthetic datasets
// (no corpus needed): every text accessor ends at the first NUL, a value of NUL
// bytes only is empty, and the encoded stream keeps the bytes.
func TestTask386IPTCTextCutAtNUL(t *testing.T) {
	t.Parallel()
	ds := func(num uint8, v string) []byte {
		return append([]byte{0x1C, 2, num, byte(len(v) >> 8), byte(len(v))}, v...) //nolint:gosec // G115: tiny test values
	}
	stream := slices.Concat(
		ds(120, "caption\x00\x00junk"),
		ds(116, "\x00"),
		ds(80, "by\x00line"),
		ds(25, "kw1\x00"),
		ds(25, "\x00\x00"),
	)

	got, err := iptc.Parse(stream)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if v := got.Caption(); v != "caption" {
		t.Errorf("Caption() = %q, want %q", v, "caption")
	}
	if v := got.Copyright(); v != "" {
		t.Errorf("Copyright() = %q, want empty (a value of NULs only)", v)
	}
	if v := got.Creator(); v != "by" {
		t.Errorf("Creator() = %q, want %q", v, "by")
	}
	if v := got.Keywords(); len(v) != 2 || v[0] != "kw1" || v[1] != "" {
		t.Errorf("Keywords() = %q, want [kw1 \"\"]", v)
	}
	for _, d := range got.Records[2] {
		if d.DataSet == 120 && string(d.Value) != "caption\x00\x00junk" {
			t.Errorf("raw value changed: %q", d.Value)
		}
	}
}
