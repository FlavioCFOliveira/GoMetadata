package gometadata

// write_orf_rw2_corpus_test.go — regression tests for task #104 (ORF/RW2 write).
//
// These tests exercise the full Read → SetCopyright → Write → Read cycle
// against every ORF and RW2 file in the corpus and assert:
//
//  1. No output block loss: the output is at least 50 % of the input size.
//     (The real threshold is much higher; 50 % guards against catastrophic
//     data loss without being brittle to legitimate metadata growth.)
//
//  2. Tag count non-decreased: the number of EXIF IFD entries in the output
//     must be >= the number in the input, because we only add the Copyright
//     tag (never remove existing tags).
//
//  3. MakerNote survival (OLYMP-type ORF only): the output EXIF must carry
//     a non-empty MakerNote.  This guards against the C5050Z-class regression where
//     the MakerNote ThumbnailImage block (external, file-absolute offsets)
//     was silently dropped.
//
//  4. ExifIFD pointer survives (RW2 only): the output EXIF must carry a
//     non-nil ExifIFD after re-parsing.  This guards against the task #104
//     regression where the 0x8769 pointer was off by 16 bytes after GUID
//     insertion, causing exiftool to report "Bad format for ExifIFD entry 0".
//
// All tests skip gracefully when the corpus directory is absent.
// Run `bash testdata/download.sh` to populate the full corpus.
//
// Spec references:
//   - TIFF 6.0 §2: IFD entry layout (tag + type + count + val_or_off).
//   - ExifTool Olympus.pm: OLYMP-type MakerNote header "OLYMP\x00" (6 bytes).
//   - Panasonic RW2 spec (task #104): 16-byte GUID at file offset [8:24].

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// patchNonStandardMagicForParse returns a copy of rawEXIF with bytes [2:4]
// patched to standard TIFF LE magic (0x2A 0x00) when the input carries a
// known non-standard magic that exif.Parse rejects.
//
// #117 fix: ORF rawEXIF has "IIRO"/"IIRS" magic; RW2 rawEXIF has "IIU\x00".
// exif.Parse requires 0x002A (classic TIFF) or 0x002B (BigTIFF). Patching the
// magic allows test helpers to call exif.Parse on an ORF/RW2 rawEXIF slice
// without patching the caller's buffer in-place.
func patchNonStandardMagicForParse(rawEXIF []byte) []byte {
	if len(rawEXIF) < 4 || rawEXIF[0] != 0x49 || rawEXIF[1] != 0x49 {
		return rawEXIF // not a LE variant with non-standard magic; return as-is
	}
	b2, b3 := rawEXIF[2], rawEXIF[3]
	needsPatch := (b2 == 0x52 && (b3 == 0x4F || b3 == 0x53)) || // ORF: IIRO/IIRS
		(b2 == 0x55 && b3 == 0x00) // RW2: IIU\x00
	if !needsPatch {
		return rawEXIF
	}
	patched := make([]byte, len(rawEXIF))
	copy(patched, rawEXIF)
	patched[2] = 0x2A
	patched[3] = 0x00
	return patched
}

// countExifIFDEntries returns the total number of IFD entries across all
// parsed IFDs (IFD0, ExifIFD, GPSIFD) for a given raw EXIF stream.
// Returns 0 if parse fails or rawEXIF is nil.
//
// #117: ORF/RW2 rawEXIF may have non-standard magic; patch before parsing.
func countExifIFDEntries(rawEXIF []byte) int {
	if len(rawEXIF) == 0 {
		return 0
	}
	e, err := exif.Parse(patchNonStandardMagicForParse(rawEXIF))
	if err != nil || e == nil {
		return 0
	}
	n := 0
	if e.IFD0 != nil {
		n += len(e.IFD0.Entries)
	}
	if e.ExifIFD != nil {
		n += len(e.ExifIFD.Entries)
	}
	if e.GPSIFD != nil {
		n += len(e.GPSIFD.Entries)
	}
	return n
}

// hasOLYMPMakerNote reports whether rawEXIF contains an OLYMP-type MakerNote
// (the blob begins with the 6-byte ASCII string "OLYMP\x00").
//
// #117: ORF rawEXIF may have non-standard magic; patch before parsing.
func hasOLYMPMakerNote(rawEXIF []byte) bool {
	if len(rawEXIF) == 0 {
		return false
	}
	e, err := exif.Parse(patchNonStandardMagicForParse(rawEXIF))
	if err != nil || e == nil || e.ExifIFD == nil {
		return false
	}
	mn := e.ExifIFD.Get(exif.TagMakerNote)
	if mn == nil || len(mn.Value) < 8 {
		return false
	}
	// "OLYMP\x00" (6 bytes)
	return mn.Value[0] == 'O' &&
		mn.Value[1] == 'L' &&
		mn.Value[2] == 'Y' &&
		mn.Value[3] == 'M' &&
		mn.Value[4] == 'P' &&
		mn.Value[5] == 0x00
}

// isOLYMPCorpusFile reports whether the filename suggests an OLYMP-type
// (older Olympus compact) camera that uses file-absolute MakerNote offsets.
//
// OLYMP-type cameras (empirically verified via ExifTool Olympus.pm):
// C5050Z, C8080, SP350, SP500UZ.
func isOLYMPCorpusFile(name string) bool {
	lc := strings.ToLower(name)
	return strings.Contains(lc, "c5050") ||
		strings.Contains(lc, "c8080") ||
		strings.Contains(lc, "sp350") ||
		strings.Contains(lc, "sp500")
}

// corpusORFFiles returns all .orf files under testdata/corpus/raw/.
// Skips the test if none are found.
func corpusORFFiles(t *testing.T) []string {
	t.Helper()
	return corpusFilesWithExt(t, "raw", ".orf")
}

// corpusRW2Files returns all .rw2 files under testdata/corpus/raw/.
// Skips the test if none are found.
func corpusRW2Files(t *testing.T) []string {
	t.Helper()
	return corpusFilesWithExt(t, "raw", ".rw2")
}

// corpusFilesWithExt returns all files with the given extension under
// testdata/corpus/<subdir>/. Skips the test if the directory is absent or
// no files with the given extension are found.
func corpusFilesWithExt(t *testing.T, subdir, ext string) []string {
	t.Helper()
	dir := filepath.Join("testdata", "corpus", subdir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("corpus directory absent (run testdata/download.sh): %s", dir)
	}
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(path), ext) {
			paths = append(paths, path)
		}
		return nil
	})
	if len(paths) == 0 {
		t.Skipf("no %s files found in %s (run testdata/download.sh)", ext, dir)
	}
	return paths
}

// TestWriteORFCorpusRoundTrip exercises the full Write round-trip for every
// ORF file in the corpus.
//
// Assertions (per acceptance criterion for task #104):
//  1. Write must not error.
//  2. Output size must be >= 50 % of input size (no catastrophic data loss).
//  3. EXIF IFD entry count must not decrease after the round-trip.
//  4. For OLYMP-type cameras (C5050Z, C8080, SP350, SP500UZ): the MakerNote
//     must be present in the output (guards against the external-thumbnail
//     block-loss issue fixed in task #104).
func TestWriteORFCorpusRoundTrip(t *testing.T) {
	t.Parallel()
	files := corpusORFFiles(t)
	t.Logf("ORF corpus: %d files", len(files))

	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}

			// Step 1: Read.
			m, err := Read(bytes.NewReader(original))
			if err != nil {
				// Graceful skip for files that are not yet fully parseable.
				var corrupt *CorruptMetadataError
				if isCorrupt(err, corrupt) {
					t.Errorf("CorruptMetadataError reading %s: %v", name, err)
				} else {
					t.Skipf("skip %s (read error, not corrupt): %v", name, err)
				}
				return
			}

			// Count input IFD entries before mutation.
			inCount := countExifIFDEntries(m.RawEXIF())

			// Step 2: set Copyright (the only mutation applied in the write path).
			const wantCopyright = "© task-104-regression"
			m.SetCopyright(wantCopyright)

			// Step 3: Write.
			var out bytes.Buffer
			if err := Write(bytes.NewReader(original), &out, m); err != nil {
				t.Fatalf("Write %s: %v", name, err)
			}
			result := out.Bytes()

			// Assertion 1: output size sanity (no catastrophic block loss).
			if len(result) < len(original)/2 {
				t.Errorf("output suspiciously small: original=%d output=%d (< 50%%)",
					len(original), len(result))
			}

			// Step 4: Re-read to validate output.
			m2, readErr := Read(bytes.NewReader(result))
			if readErr != nil {
				t.Fatalf("re-read after Write %s: %v", name, readErr)
			}

			// Assertion 2: tag count must not decrease.
			outCount := countExifIFDEntries(m2.RawEXIF())
			if outCount < inCount {
				t.Errorf("IFD entry count decreased: input=%d output=%d", inCount, outCount)
			}

			// Assertion 3: Copyright tag must survive.
			if got := m2.Copyright(); got != wantCopyright {
				t.Errorf("Copyright after round-trip: got %q, want %q", got, wantCopyright)
			}

			// Assertion 4 (OLYMP-type only): MakerNote must survive.
			// The C5050Z/C8080/SP350/SP500UZ store a JPEG thumbnail block at
			// a file-absolute offset referenced only from the MakerNote IFD.
			// The task #104 fix registers this as a standalone imageBlock.
			// If the MakerNote is absent in the output, the fix has regressed.
			if isOLYMPCorpusFile(name) {
				if !hasOLYMPMakerNote(m2.RawEXIF()) {
					t.Errorf("OLYMP-type MakerNote absent from output of %s (task #104 regression)", name)
				}
			}
		})
	}
}

// TestWriteRW2CorpusRoundTrip exercises the full Write round-trip for every
// RW2 file in the corpus.
//
// Assertions (per acceptance criterion for task #104):
//  1. Write must not error.
//  2. Output size must be >= 50 % of input size (no catastrophic data loss).
//  3. EXIF IFD entry count must not decrease after the round-trip.
//  4. ExifIFD must be present and non-nil in the re-parsed output.
//     This guards against the 0x8769-pointer-off-by-16 regression fixed in
//     task #104, which caused exiftool to report "Bad format (N) for ExifIFD
//     entry 0".
func TestWriteRW2CorpusRoundTrip(t *testing.T) {
	t.Parallel()
	files := corpusRW2Files(t)
	t.Logf("RW2 corpus: %d files", len(files))

	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}

			// Step 1: Read.
			m, err := Read(bytes.NewReader(original))
			if err != nil {
				var corrupt *CorruptMetadataError
				if isCorrupt(err, corrupt) {
					t.Errorf("CorruptMetadataError reading %s: %v", name, err)
				} else {
					t.Skipf("skip %s (read error, not corrupt): %v", name, err)
				}
				return
			}

			// Count input IFD entries before mutation.
			inCount := countExifIFDEntries(m.RawEXIF())

			// Step 2: set Copyright.
			const wantCopyright = "© task-104-regression"
			m.SetCopyright(wantCopyright)

			// Step 3: Write.
			var out bytes.Buffer
			if err := Write(bytes.NewReader(original), &out, m); err != nil {
				t.Fatalf("Write %s: %v", name, err)
			}
			result := out.Bytes()

			// Assertion 1: output size sanity.
			if len(result) < len(original)/2 {
				t.Errorf("output suspiciously small: original=%d output=%d (< 50%%)",
					len(original), len(result))
			}

			// Step 4: Re-read to validate output.
			m2, readErr := Read(bytes.NewReader(result))
			if readErr != nil {
				t.Fatalf("re-read after Write %s: %v", name, readErr)
			}

			// Assertion 2: tag count must not decrease.
			outCount := countExifIFDEntries(m2.RawEXIF())
			if outCount < inCount {
				t.Errorf("IFD entry count decreased: input=%d output=%d", inCount, outCount)
			}

			// Assertion 3: Copyright must survive.
			if got := m2.Copyright(); got != wantCopyright {
				t.Errorf("Copyright after round-trip: got %q, want %q", got, wantCopyright)
			}

			// Assertion 4: ExifIFD pointer must be valid in the output.
			// After GUID insertion by insertRW2GUIDAndShiftOffsets, the 0x8769
			// inline pointer must point to the ExifIFD (not to garbage memory).
			// A nil ExifIFD means the pointer was wrong and the IFD was not parsed.
			if m2.EXIF != nil && m2.EXIF.ExifIFD == nil {
				// Only flag this for files that had an ExifIFD in the input.
				if m != nil && m.EXIF != nil && m.EXIF.ExifIFD != nil {
					t.Errorf("ExifIFD lost after Write %s (task #104 regression: 0x8769 pointer off by 16)", name)
				}
			}
		})
	}
}

// isCorrupt is a helper that reports whether err wraps *CorruptMetadataError.
// The second argument is a nil typed pointer used only for type inference.
func isCorrupt(err error, _ *CorruptMetadataError) bool {
	var corrupt *CorruptMetadataError
	return err != nil && errors.As(err, &corrupt)
}
