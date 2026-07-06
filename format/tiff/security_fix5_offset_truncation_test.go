package tiff

// security_fix5_offset_truncation_test.go — regression gate for security audit
// FIX 5 (CWE-681/190): 32-bit int(ifd0Off) truncation in extractTagValues.
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

// TestExtractTagValuesHighIFD0Offset is the regression gate for security
// audit FIX 5 applied to format/tiff/tiff.go's extractTagValues.
func TestExtractTagValuesHighIFD0Offset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		order   binary.ByteOrder
		ifd0Off uint32
	}{
		{"LE 0x80000000", binary.LittleEndian, 0x80000000},
		{"BE 0x80000000", binary.BigEndian, 0x80000000},
		{"LE 0xFFFFFFFF", binary.LittleEndian, 0xFFFFFFFF},
		{"BE 0xFFFFFFFE", binary.BigEndian, 0xFFFFFFFE},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A tiny buffer: the declared ifd0Off is always far beyond it, so
			// extractTagValues must reject it gracefully (return nil, nil)
			// instead of indexing data[ifd0Off:].
			data := make([]byte, 12)

			// Must not panic.
			rawIPTC, rawXMP := extractTagValues(data, tc.ifd0Off, tc.order)
			if rawIPTC != nil {
				t.Errorf("extractTagValues rawIPTC = %v, want nil for out-of-range ifd0Off=0x%08X", rawIPTC, tc.ifd0Off)
			}
			if rawXMP != nil {
				t.Errorf("extractTagValues rawXMP = %v, want nil for out-of-range ifd0Off=0x%08X", rawXMP, tc.ifd0Off)
			}
		})
	}
}

// TestExtractHighIFD0OffsetEndToEnd exercises the same guard through the
// public Extract entry point, confirming the whole classic-TIFF read path
// (not just the internal helper) tolerates an out-of-range IFD0 offset
// without panicking.
func TestExtractHighIFD0OffsetEndToEnd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		magic   [2]byte
		order   binary.ByteOrder
		ifd0Off uint32
	}{
		{"LE 0x80000000", [2]byte{'I', 'I'}, binary.LittleEndian, 0x80000000},
		{"BE 0xFFFFFFFF", [2]byte{'M', 'M'}, binary.BigEndian, 0xFFFFFFFF},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Minimal 8-byte classic TIFF header: byte-order mark + magic +
			// an out-of-range IFD0 offset. extractTagValues is reached via
			// Extract's classic-TIFF branch, which uses rawEXIF=data
			// unconditionally, so the crash (if any) happens inside
			// extractTagValues regardless of the returned rawEXIF.
			data := make([]byte, 8)
			data[0], data[1] = tc.magic[0], tc.magic[1]
			tc.order.PutUint16(data[2:], 0x002A)
			tc.order.PutUint32(data[4:], tc.ifd0Off)

			rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Extract() unexpected error: %v", err)
			}
			if rawEXIF == nil {
				t.Error("Extract() rawEXIF = nil, want the full classic-TIFF buffer (TIFF §2)")
			}
			if rawIPTC != nil || rawXMP != nil {
				t.Errorf("Extract() rawIPTC=%v rawXMP=%v, want both nil for an out-of-range IFD0 offset", rawIPTC, rawXMP)
			}
		})
	}
}
