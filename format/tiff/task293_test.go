package tiff

// task293_test.go — regression guard for task #293's follow-up fix: the
// large-fraction snap previously in clampNeed (extent.go) forced a full
// file read whenever a single pass's need reached >= 10% of fileSize, even
// when that fraction was STABLE (never growing across passes) and the file
// itself was orders of magnitude larger than the true metadata need. See
// extent.go's own doc comment (scanMetadataExtent, clampNeed) for the full
// corpus-wide evidence (114 real TIFF-family files, zero regressions,
// 42% average buffer reduction) this fix is based on.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// task293ORFReadBudget is the AC bound this test enforces: Extract's
// returned metadata prefix must stay within this budget for OM System
// TG-7.ORF, matching the e2e BenchmarkRead/orf AC (<= 2 MiB).
const task293ORFReadBudget = 2 << 20 // 2 MiB

// olympusORFMagic is bytes[2:4] of an Olympus ORF file's "IIRO" header
// variant — see format/raw/orf's own orfMagic and tiff.ExtractWithMagic's
// doc comment.
const olympusORFMagic = 0x4F52 // "RO"

// TestExtractORFDoesNotOverreadOnStableLargeFraction is the direct
// regression guard for the bug this fix closes: OM System TG-7.ORF's
// metadata need is a STABLE 11.5% of its 13.15 MB file (converges on the
// very first growth pass, never grows further across passes) — legitimately
// above the prior 10% relative snap threshold, which forced a full 13.15 MB
// read instead of the ~1.5 MB the file actually needs, blowing past the
// Read AC (<= 2 MiB, <= 60 us).
//
// Corpus-gated: skipped (not failed) when the corpus file is absent, per
// docs/TESTING.md §2.1.
func TestExtractORFDoesNotOverreadOnStableLargeFraction(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "testdata", "corpus", "raw", "metadata-extractor", "OM System TG-7.ORF")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip("corpus file \"OM System TG-7.ORF\" not present")
	}

	rawEXIF, _, _, err := ExtractWithMagic(bytes.NewReader(data), olympusORFMagic)
	if err != nil {
		t.Fatalf("ExtractWithMagic: %v", err)
	}
	if len(rawEXIF) > task293ORFReadBudget {
		t.Errorf("Extract read %d bytes of a %d-byte file (%.2f%%) — exceeds the %d-byte Read AC budget; the large-fraction snap regressed",
			len(rawEXIF), len(data), 100*float64(len(rawEXIF))/float64(len(data)), task293ORFReadBudget)
	}
	if len(rawEXIF) >= len(data) {
		t.Errorf("Extract read the WHOLE %d-byte file instead of a metadata prefix — the large-fraction snap regressed", len(data))
	}
}

// TestClampNeedNoLargeFractionSnap directly unit-tests clampNeed: a need
// that is a large RELATIVE fraction of fileSize (e.g. 20%) but leaves a
// large ABSOLUTE remainder (well above extentInitialPrefixSize) must NOT be
// widened to fileSize — only a need within one initial-prefix-chunk's worth
// of fileSize (the tail-snap) or already exceeding fileSize (the safety
// clamp) may be. This is the exact clampNeed contract
// TestExtractORFDoesNotOverreadOnStableLargeFraction depends on, expressed
// without needing the corpus file present.
func TestClampNeedNoLargeFractionSnap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		need          uint64
		fileSize      uint64
		wantWidened   bool // got != need (either clamped down or tail-snapped up)
		wantFileSized bool // got == fileSize
	}{
		// The exact regression this fix targets: a need that is a large
		// RELATIVE fraction (20%, 11.5%) of fileSize, but whose ABSOLUTE
		// remainder is far larger than extentInitialPrefixSize, must pass
		// through untouched — the prior 10%-of-fileSize rule would have
		// widened both of these to fileSize.
		{"20pct_of_10MB_untouched", 2_000_000, 10_000_000, false, false},
		{"11.5pct_of_13MB_untouched_ORF_shape", 1_514_496, 13_150_700, false, false},
		{"tiny_need_tiny_fraction_untouched", 100, 1_000_000, false, false},
		// The safety clamp: need already exceeds fileSize.
		{"need_exceeds_fileSize_clamped", 15_000_000, 10_000_000, true, true},
		// The tail-snap: remaining is within one initial-prefix-chunk.
		{"within_tail_snap_margin_widened", 9_990_000, 10_000_000, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := clampNeed(tc.need, tc.fileSize)
			if widened := got != tc.need; widened != tc.wantWidened {
				t.Errorf("clampNeed(%d, %d) = %d, widened=%v, want widened=%v",
					tc.need, tc.fileSize, got, widened, tc.wantWidened)
			}
			if fileSized := got == tc.fileSize; fileSized != tc.wantFileSized {
				t.Errorf("clampNeed(%d, %d) = %d, ==fileSize=%v, want ==fileSize=%v",
					tc.need, tc.fileSize, got, fileSized, tc.wantFileSized)
			}
		})
	}
}
