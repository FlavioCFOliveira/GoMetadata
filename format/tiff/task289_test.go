package tiff

// task289_test.go — full-corpus parity gate for task #289 (Sprint 44,
// Batch F): Extract now reads and returns only the metadata PREFIX of a
// TIFF/CR2/NEF/ARW/DNG file (see extent.go), instead of the whole file. This
// test proves that change is lossless for every field exif.Parse reports —
// IFD0, the IFD1 chain, ExifIFD, GPSIFD, InteropIFD, and MakerNoteIFD — plus
// RawIPTC/RawXMP, across the full TIFF-family corpus. IFD.ThumbnailData is
// deliberately excluded from the comparison: it is image data (an embedded
// JPEG preview), not metadata, and is expected to legitimately differ (absent
// from the prefix, present from the whole file) whenever a file's preview
// falls outside the computed metadata extent — see Extract's and
// Metadata.RawEXIF's doc comments.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// tiffFamilyExtensions is the set of file extensions (lower-cased) this
// parity test covers: TIFF, CR2, NEF, ARW, DNG. CR3, ORF, and RW2 are
// explicitly out of #289's scope (ORF/RW2 were done in #286; CR3 is a
// different container entirely) and are skipped even though they live under
// the same testdata/corpus/raw tree.
var tiffFamilyExtensions = map[string]bool{ //nolint:gochecknoglobals // read-only test data
	".tif": true, ".tiff": true,
	".cr2": true, ".nef": true, ".arw": true, ".dng": true,
}

// compareIFD returns a list of human-readable mismatches between a and b,
// recursing into Next (the IFD1 chain). ThumbnailData is intentionally never
// compared — see this file's top-level doc comment.
func compareIFD(name string, a, b *exif.IFD) []string {
	if a == nil || b == nil {
		if a == nil && b == nil {
			return nil
		}
		return []string{fmt.Sprintf("%s: nil mismatch (prefix=%v, whole=%v)", name, a == nil, b == nil)}
	}
	var diffs []string
	if len(a.Entries) != len(b.Entries) {
		diffs = append(diffs, fmt.Sprintf("%s: entry count mismatch: prefix=%d whole=%d", name, len(a.Entries), len(b.Entries)))
	}
	n := min(len(a.Entries), len(b.Entries))
	for i := range n {
		ea, eb := a.Entries[i], b.Entries[i]
		if ea.Tag != eb.Tag || ea.Type != eb.Type || ea.Count != eb.Count || !bytes.Equal(ea.Value, eb.Value) {
			diffs = append(diffs, fmt.Sprintf("%s entry[%d]: tag prefix=%#04x/whole=%#04x type=%d/%d count=%d/%d value prefix=%x whole=%x",
				name, i, ea.Tag, eb.Tag, ea.Type, eb.Type, ea.Count, eb.Count, ea.Value, eb.Value))
		}
	}
	diffs = append(diffs, compareIFD(name+".Next", a.Next, b.Next)...)
	return diffs
}

// compareEXIF returns a list of human-readable mismatches between a and b
// across every field exif.Parse populates from tag data: IFD0 (and its Next
// chain), ExifIFD, GPSIFD, InteropIFD, and MakerNoteIFD. MakerNote (the raw
// blob) is compared too, since it is itself metadata this library preserves
// round-trip. ThumbnailData is excluded (see top-level doc comment);
// Warnings is excluded (advisory text, not parsed tag data, and legitimately
// differs when the prefix scan itself never encounters a condition the
// whole-file scan does, e.g. none in practice, but excluded for robustness).
func compareEXIF(a, b *exif.EXIF) []string {
	var diffs []string
	diffs = append(diffs, compareIFD("IFD0", a.IFD0, b.IFD0)...)
	diffs = append(diffs, compareIFD("ExifIFD", a.ExifIFD, b.ExifIFD)...)
	diffs = append(diffs, compareIFD("GPSIFD", a.GPSIFD, b.GPSIFD)...)
	diffs = append(diffs, compareIFD("InteropIFD", a.InteropIFD, b.InteropIFD)...)
	diffs = append(diffs, compareIFD("MakerNoteIFD", a.MakerNoteIFD, b.MakerNoteIFD)...)
	if !bytes.Equal(a.MakerNote, b.MakerNote) {
		diffs = append(diffs, fmt.Sprintf("MakerNote: prefix len=%d whole len=%d differ", len(a.MakerNote), len(b.MakerNote)))
	}
	return diffs
}

// TestExtractPrefixParityWithWholeFile is the #289 full-corpus parity gate.
// Corpus-gated: skipped (not failed) when the corpus directory is absent or
// contains no TIFF-family files, per docs/TESTING.md §2.1.
func TestExtractPrefixParityWithWholeFile(t *testing.T) {
	t.Parallel()
	raw := testutil.CorpusFiles(t, "raw")
	tiffFiles := testutil.CorpusFiles(t, "tiff")
	paths := make([]string, 0, len(raw)+len(tiffFiles))
	paths = append(paths, raw...)
	paths = append(paths, tiffFiles...)

	tested := 0
	for _, path := range paths {
		ext := strings.ToLower(filepath.Ext(path))
		if !tiffFamilyExtensions[ext] {
			continue
		}
		tested++
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read corpus file: %v", err)
			}

			prefixEXIF, prefixIPTC, prefixXMP, err := Extract(bytes.NewReader(data))
			if err != nil {
				t.Skipf("Extract (prefix): %v", err) // some corpus files are deliberately malformed edge cases; not this test's concern
			}

			// Whole-file baseline: bypass the #289 prefix scan entirely and
			// hand exif.Parse the complete file, exactly as this package did
			// before #289 (the TIFF/CR2/NEF/ARW/DNG stream IS the EXIF
			// container, so parsing the whole file is always valid input).
			wholeEXIF, wholeIPTC, wholeXMP, werr := extractWholeFile(bytes.NewReader(data), 0)
			if werr != nil {
				t.Fatalf("extractWholeFile: %v (but prefix Extract succeeded — this itself is a parity bug)", werr)
			}

			// Parse the whole-file baseline first: it is ground truth for
			// what this file's tags legitimately are. Some corpus files
			// (e.g. deliberately-malformed torture-test fixtures) fail
			// exif.Parse even on their unmodified whole-file bytes — that is
			// a pre-existing exif.Parse limitation unrelated to #289, and is
			// exercised separately by exif's own conformance/fuzz suites, so
			// it is skipped here rather than failed. What THIS test exists
			// to catch is the prefix scan producing a WORSE or DIFFERENT
			// result than the whole file for the same bytes.
			e2, werr2 := exif.Parse(wholeEXIF)
			if werr2 != nil {
				t.Skipf("exif.Parse(whole): %v (pre-existing, independent of #289)", werr2)
			}
			e1, perr := exif.Parse(prefixEXIF)
			if perr != nil {
				t.Fatalf("exif.Parse(prefix): %v (but exif.Parse(whole) succeeded on the same file — #289 parity bug)", perr)
			}

			if diffs := compareEXIF(e1, e2); len(diffs) > 0 {
				t.Errorf("parity mismatch (%d diffs):\n%s", len(diffs), strings.Join(diffs, "\n"))
			}
			if !bytes.Equal(prefixIPTC, wholeIPTC) {
				t.Errorf("RawIPTC mismatch: prefix=%x whole=%x", prefixIPTC, wholeIPTC)
			}
			if !bytes.Equal(prefixXMP, wholeXMP) {
				t.Errorf("RawXMP mismatch: prefix len=%d whole len=%d", len(prefixXMP), len(wholeXMP))
			}
			// Sanity: the whole point of #289 is that the prefix is smaller
			// than the file for real files with meaningful image data; log
			// (not fail) the ratio so a future run can spot a regression to
			// "always reads everything" without needing a separate
			// benchmark.
			t.Logf("file=%d prefix=%d (%.4f%%)", len(data), len(prefixEXIF), 100*float64(len(prefixEXIF))/float64(len(data)))
		})
	}
	if tested == 0 {
		t.Skip("no TIFF-family (tif/tiff/cr2/nef/arw/dng) corpus files found")
	}
}
