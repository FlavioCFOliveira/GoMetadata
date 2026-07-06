package rw2

// security_fix5_offset_truncation_test.go — regression gate for security audit
// FIX 5 (CWE-681/190): 32-bit int(ifd0Off) truncation in extractTIFFTags.
//
// Root cause: ifd0Off is a uint32 read directly from the file. The guard
// `int(ifd0Off)+2 > len(data)` is only 32-bit safe by accident on this
// platform's int width. On a 32-bit platform (GOARCH=386/arm, int = 32 bits),
// int(ifd0Off) for ifd0Off >= 0x80000000 wraps to a negative number, so the
// guard incorrectly passes and the subsequent `data[ifd0Off:]` slice
// expression panics with "slice bounds out of range". The fix compares in
// uint64 before ever converting ifd0Off to int, mirroring the #74 fix in
// format/detect.go's parseClassicTIFFIFD0 and the #45 fix in format/jpeg's
// parseIRBEntry.
//
// On this (64-bit) test machine, int(uint32) never wraps — the guard already
// evaluates correctly regardless of the fix, so this test cannot demonstrate
// a fail-before/pass-after distinction here (as format/detect_test.go's own
// TestDetectTIFFHighIFD0Offset documents for the identical pattern). What it
// does prove, on any platform, is that the uint64-based guard logic rejects
// an out-of-range ifd0Off and takes the safe not-found path rather than
// indexing — which is the exact property that makes the fix 32-bit-safe by
// construction (uint64 arithmetic never truncates on either platform).

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestExtractTIFFTagsHighIFD0Offset is the regression gate for security audit
// FIX 5 applied to format/raw/rw2/rw2.go's extractTIFFTags.
func TestExtractTIFFTagsHighIFD0Offset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ifd0Off uint32
	}{
		{"0x80000000", 0x80000000},
		{"0xFFFFFFFF", 0xFFFFFFFF},
		{"0xFFFFFFFE", 0xFFFFFFFE},
	}

	order := binary.LittleEndian
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A tiny buffer: the declared ifd0Off is always far beyond it, so
			// extractTIFFTags must reject it gracefully (return nil, nil)
			// instead of indexing data[ifd0Off:].
			data := make([]byte, 12)

			// Must not panic.
			rawIPTC, rawXMP := extractTIFFTags(data, tc.ifd0Off, order)
			if rawIPTC != nil {
				t.Errorf("extractTIFFTags rawIPTC = %v, want nil for out-of-range ifd0Off=0x%08X", rawIPTC, tc.ifd0Off)
			}
			if rawXMP != nil {
				t.Errorf("extractTIFFTags rawXMP = %v, want nil for out-of-range ifd0Off=0x%08X", rawXMP, tc.ifd0Off)
			}
		})
	}
}

// TestExtractHighIFD0OffsetEndToEnd exercises the same guard through the
// public Extract entry point, confirming the whole RW2 read path tolerates an
// out-of-range IFD0 offset without panicking.
func TestExtractHighIFD0OffsetEndToEnd(t *testing.T) {
	t.Parallel()

	// Minimal RW2 header: "IIU\x00" magic (bytes [0:4]) + an out-of-range
	// IFD0 offset at bytes [4:8].
	data := make([]byte, 8)
	copy(data[0:4], rw2Magic)
	binary.LittleEndian.PutUint32(data[4:], 0xFFFFFFFF)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract() unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("Extract() rawEXIF = nil, want the original RW2 bytes preserved")
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("Extract() rawIPTC=%v rawXMP=%v, want both nil for an out-of-range IFD0 offset", rawIPTC, rawXMP)
	}
}
