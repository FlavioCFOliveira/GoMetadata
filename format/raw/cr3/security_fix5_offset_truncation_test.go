package cr3

// security_fix5_offset_truncation_test.go — regression gate for security audit
// FIX 5 (CWE-681/190): 32-bit int(offset) truncation in findExifIFDOffset and
// mergeCMT.
//
// Root cause: both functions convert an untrusted uint32 file offset to int
// before comparing it against a buffer length. On a 32-bit platform
// (GOARCH=386/arm, int = 32 bits):
//   - findExifIFDOffset's guard `int(ifd0Off)+2 > len(buf)` incorrectly
//     passes for ifd0Off >= 0x80000000 (the cast wraps negative), so the
//     subsequent `buf[pos:]` read panics with "slice bounds out of range".
//   - mergeCMT's check `int(exifIFDOffset) < len(cmt1)` incorrectly evaluates
//     true for any exifIFDOffset >= 0x80000000 (negative < any non-negative
//     length), silently skipping the CMT1+CMT2 merge and leaving the ExifIFD
//     pointer dangling past the end of cmt1 — not a panic, but a logic
//     corruption sharing the identical root cause.
//
// Both are fixed by comparing in uint64 before ever converting the offset to
// int, mirroring the #74 fix in format/detect.go's parseClassicTIFFIFD0 and
// the #45 fix in format/jpeg's parseIRBEntry.
//
// On this (64-bit) test machine, int(uint32) never wraps — both guards
// already evaluate correctly regardless of the fix, so these tests cannot
// demonstrate a fail-before/pass-after distinction here (as
// format/detect_test.go's own TestDetectTIFFHighIFD0Offset documents for the
// identical pattern). What they do prove, on any platform, is that the
// uint64-based guard logic takes the safe path (reject / merge) rather than
// indexing incorrectly — the exact property that makes the fix 32-bit-safe
// by construction (uint64 arithmetic never truncates on either platform).

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestFindExifIFDOffsetHighOffset is the regression gate for security audit
// FIX 5 applied to findExifIFDOffset.
func TestFindExifIFDOffsetHighOffset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ifd0Off uint32
	}{
		{"0x80000000", 0x80000000},
		{"0xFFFFFFFF", 0xFFFFFFFF},
	}

	order := binary.LittleEndian
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A tiny buffer: the declared ifd0Off is always far beyond it, so
			// findExifIFDOffset must reject it gracefully (return 0) instead
			// of indexing buf[pos:].
			buf := make([]byte, 12)

			// Must not panic.
			if got := findExifIFDOffset(buf, tc.ifd0Off, order); got != 0 {
				t.Errorf("findExifIFDOffset() = 0x%08X, want 0 for out-of-range ifd0Off=0x%08X", got, tc.ifd0Off)
			}
		})
	}
}

// buildTIFFWithExifIFDPointer builds a minimal little-endian TIFF whose IFD0
// carries a single entry: TagExifIFDPointer (0x8769), TypeLong, count=1,
// value=exifOff.
func buildTIFFWithExifIFDPointer(exifOff uint32) []byte {
	order := binary.LittleEndian
	// header(8) + ifd_count(2) + 1 entry(12) + next_ifd(4) = 26 bytes.
	buf := make([]byte, 26)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at offset 8

	order.PutUint16(buf[8:], 1)       // 1 entry
	order.PutUint16(buf[10:], 0x8769) // TagExifIFDPointer
	order.PutUint16(buf[12:], 4)      // TypeLong
	order.PutUint32(buf[14:], 1)      // count = 1
	order.PutUint32(buf[18:], exifOff)
	order.PutUint32(buf[22:], 0) // next IFD = 0
	return buf
}

// TestMergeCMTHighExifIFDOffset is the regression gate for security audit
// FIX 5 applied to mergeCMT's ExifIFD-offset-within-CMT1 comparison.
//
// cmt1 declares an ExifIFD pointer far beyond its own length (0xFFFFFFFF),
// so mergeCMT must treat the pointer as extending into cmt2 and concatenate
// the two buffers — not silently return cmt1 unchanged, which is what the
// pre-fix int-truncated comparison would do on a 32-bit platform.
func TestMergeCMTHighExifIFDOffset(t *testing.T) {
	t.Parallel()

	cmt1 := buildTIFFWithExifIFDPointer(0xFFFFFFFF)
	cmt2 := []byte("EXIF-IFD-BYTES-LIVING-IN-CMT2!!")

	got := mergeCMT(cmt1, cmt2)
	wantLen := len(cmt1) + len(cmt2)
	if len(got) != wantLen {
		t.Fatalf("mergeCMT() returned %d bytes, want %d (cmt1+cmt2 concatenated; ExifIFD offset 0xFFFFFFFF is far beyond cmt1)",
			len(got), wantLen)
	}
	if !bytes.Equal(got[len(cmt1):], cmt2) {
		t.Errorf("mergeCMT() tail = %q, want cmt2 %q appended verbatim", got[len(cmt1):], cmt2)
	}
}
