package gometadata

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format"
)

// Task #388: the README says which containers carry IPTC; the code says the
// same (canCarryIPTC), and each TIFF-based RAW container round-trips IPTC.

// TestREADMEIPTCColumnMatchesCode checks the IPTC column of the "Supported
// formats" table of README.md against the code: "Yes" for the containers that
// can carry IPTC (canCarryIPTC) and the PNG raw profile read, "No" for the
// others.
func TestREADMEIPTCColumnMatchesCode(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	ipt := map[string]string{} // format name -> IPTC cell
	in, past := false, false
	for line := range strings.SplitSeq(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if !in {
			in = strings.HasPrefix(trimmed, "| Format |") && strings.Contains(trimmed, "IPTC")
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			break
		}
		if isTableSeparator(trimmed) {
			past = true
			continue
		}
		if cells := splitMarkdownRow(trimmed); past && len(cells) >= 6 {
			ipt[cells[0]] = stripFootnoteMarkers(cells[5])
		}
	}
	if len(ipt) == 0 {
		t.Fatal(`the "Supported formats" table with an IPTC column was not found in README.md`)
	}
	for name, id := range readmeFormatDisplayNames {
		cell, ok := ipt[name]
		if !ok {
			t.Errorf("%s: no row in the README table", name)
			continue
		}
		// PNG reads IPTC from raw profile text chunks but never creates it, so it
		// is "Yes" in the README while canCarryIPTC is false.
		want := NewMetadata(id).canCarryIPTC() || id == format.FormatPNG
		if got := cell == "Yes"; got != want {
			t.Errorf("%s: README IPTC cell %q, code says IPTC supported = %v", name, cell, want)
		}
	}
	for _, name := range []string{"Canon CR2", "Nikon NEF", "Sony ARW", "Adobe DNG", "Olympus ORF", "Panasonic RW2"} {
		if ipt[name] != "Yes" {
			t.Errorf("%s: README IPTC cell = %q, want Yes", name, ipt[name])
		}
	}
	for _, name := range []string{"Canon CR3", "HEIF", "AVIF", "WebP"} {
		if ipt[name] != "No" {
			t.Errorf("%s: README IPTC cell = %q, want No", name, ipt[name])
		}
	}
}

// TestIPTCKeywordsRoundTripRAW writes IPTC Keywords into each TIFF-based RAW
// format with SetKeywords, then reads them back with the library and, when
// exiftool is installed, with ExifTool (IPTC:Keywords).
func TestIPTCKeywordsRoundTripRAW(t *testing.T) {
	t.Parallel()
	files := []struct{ format, rel string }{
		{"TIFF", "tiff/exiftool/ExifTool.tif"},
		{"DNG", "raw/exiftool/DNG.dng"},
		{"NEF", "raw/exiftool/Nikon.nef"},
		{"CR2", "raw/exiftool/CanonRaw.cr2"},
		{"RW2", "raw/exiftool/Panasonic.rw2"},
		{"ARW", "raw/metadata-extractor/Sony ILCE-7M3 (A7M3).arw"},
		{"ORF", "raw/metadata-extractor/Olympus E-M10.orf"},
	}
	keywords := []string{"gm-iptc-alpha", "gm-iptc-beta"}
	exiftool, lookErr := exec.LookPath("exiftool")

	for _, f := range files {
		t.Run(f.format, func(t *testing.T) {
			t.Parallel()
			path := requireCorpusFile(t, f.rel)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			m.SetKeywords(keywords)
			if m.IPTC == nil {
				t.Fatalf("SetKeywords created no IPTC for %s", f.format)
			}
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src), &out, m); err != nil {
				t.Fatalf("Write: %v", err)
			}

			m2, err := Read(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("Read of the output: %v", err)
			}
			if m2.IPTC == nil || strings.Join(m2.IPTC.Keywords(), "|") != strings.Join(keywords, "|") {
				t.Fatalf("IPTC keywords after the round trip = %v, want %v", m2.IPTC, keywords)
			}

			if lookErr != nil {
				t.Logf("exiftool not installed (%v): the library read-back above is the only check", lookErr)
				return
			}
			tmp := filepath.Join(t.TempDir(), "out"+filepath.Ext(path))
			if err := os.WriteFile(tmp, out.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := exec.Command(exiftool, "-s3", "-sep", "|", "-IPTC:Keywords", tmp).Output() //nolint:gosec // G204: exiftool path from LookPath, tmp is a test temp file
			if err != nil {
				t.Fatalf("exiftool: %v", err)
			}
			if line := strings.TrimSpace(string(got)); line != strings.Join(keywords, "|") {
				t.Errorf("exiftool IPTC:Keywords = %q, want %v", line, keywords)
			}
		})
	}
}
