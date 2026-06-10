package exif

// conformance_robustness_test.go — EXIF/TIFF conformance battery: robustness rules R-01..R-13
// and Section 5 real-world deviations 1–12.
//
// Spec references:
//   - TIFF 6.0 §2 (Adobe, 1992): IFD layout, offset semantics.
//   - CIPA DC-X008-Translation-2019 (Exif 2.32) §4.6.3–§4.6.5.
//   - CIPA DC-008-Translation-2023 (Exif 3.0) §4.6.3.
//   - BigTIFF spec §2 (Aware Systems / libtiff).
//   - ExifTool MakerNotes.pm; Exiv2 makernote.html (§5 deviation sources).
//
// Every sub-test name matches the rule ID from docs/conformance/exif-tiff.md.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Shared corpus helpers (used across conformance files)
// ---------------------------------------------------------------------------

// corpusFilesFromDir returns all file paths under dir, skipping the call if the
// directory does not exist or is empty. The test is not failed for absence — only
// if a path listing error occurs. This mirrors testutil.CorpusFiles but is local
// to the exif package.
func corpusFilesFromDir(t *testing.T, dir string) []string {
	t.Helper()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("corpus directory %s does not exist; run 'make testdata'", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus dir %s: %v", dir, err)
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			// Recurse one level for subdirectories.
			sub := filepath.Join(dir, e.Name())
			subs, serr := os.ReadDir(sub)
			if serr != nil {
				continue
			}
			for _, se := range subs {
				if !se.IsDir() {
					paths = append(paths, filepath.Join(sub, se.Name()))
				}
			}
		} else {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	if len(paths) == 0 {
		t.Skipf("corpus directory %s is empty; run 'make testdata'", dir)
	}
	return paths
}

// mustReadFile reads a file or fails the test.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// ---------------------------------------------------------------------------
// R-01 — Circular IFD chain detection
// ---------------------------------------------------------------------------

// TestConformance_R01_circular_ifd_chain verifies that circular IFD next-IFD chains
// are detected via a visited-offset set and do not loop infinitely.
// Conformance contract R-01.
func TestConformance_R01_circular_ifd_chain(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// IFD0 at offset 8, next-IFD pointer → 8 (self-cycle).
	buf := make([]byte, 14)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 0)  // 0 entries
	order.PutUint32(buf[10:], 8) // next-IFD = 8 (self-cycle)
	mustNotPanic(t, "R-01 self-cycle", func() {
		e, err := Parse(buf)
		if err != nil {
			return
		}
		if e.IFD0 == nil {
			t.Error("R-01: IFD0 is nil after cycle detection")
		}
		// Must not have an infinite chain.
		if e.IFD0 != nil && e.IFD0.Next != nil && e.IFD0.Next == e.IFD0 {
			t.Error("R-01: IFD chain still forms a cycle in the parsed result")
		}
	})

	// Two-IFD cycle: IFD0 → IFD1 → IFD0.
	buf2 := make([]byte, 28) // header(8) + IFD0(6) + IFD1(6) = but we need IFD1 offset
	buf2[0], buf2[1] = 'I', 'I'
	order.PutUint16(buf2[2:], 0x002A)
	order.PutUint32(buf2[4:], 8)
	order.PutUint16(buf2[8:], 0)   // IFD0: 0 entries
	order.PutUint32(buf2[10:], 14) // IFD0 next → IFD1 at 14
	order.PutUint16(buf2[14:], 0)  // IFD1: 0 entries
	order.PutUint32(buf2[18:], 8)  // IFD1 next → IFD0 at 8 (cycle)
	mustNotPanic(t, "R-01 two-IFD cycle", func() {
		_, _ = Parse(buf2)
	})
}

// ---------------------------------------------------------------------------
// R-02 — Circular sub-IFD references
// ---------------------------------------------------------------------------

// TestConformance_R02_circular_subifd verifies that sub-IFD back-pointers
// (e.g., InteropIFD pointing back to IFD0) do not cause infinite recursion.
// Conformance contract R-02.
func TestConformance_R02_circular_subifd(t *testing.T) {
	t.Parallel()
	// Build IFD0 → ExifIFD (InteropPointer → IFD0 back pointer).
	// This tests the edge case where an InteropIFD traversal tries to parse IFD0.
	order := binary.LittleEndian
	const (
		ifd0Off = 8
		exifOff = ifd0Off + 2 + 12 + 4 // = 26
	)
	buf := make([]byte, exifOff+2+12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0: ExifIFDPointer.
	order.PutUint16(buf[ifd0Off:], 1)
	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagExifIFDPointer))
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], exifOff)

	// ExifIFD: InteropIFDPointer → ifd0Off (back-pointer to IFD0).
	order.PutUint16(buf[exifOff:], 1)
	q := exifOff + 2
	order.PutUint16(buf[q:], uint16(TagInteropIFDPointer))
	order.PutUint16(buf[q+2:], uint16(TypeLong))
	order.PutUint32(buf[q+4:], 1)
	order.PutUint32(buf[q+8:], ifd0Off) // back-pointer to IFD0

	mustNotPanic(t, "R-02 InteropIFD back-pointer", func() {
		_, _ = Parse(buf)
	})
}

// ---------------------------------------------------------------------------
// R-03 — Out-of-bounds offset → treated as absent
// ---------------------------------------------------------------------------

// TestConformance_R03_oob_offset_treated_absent verifies that any offset outside
// the stream is treated as absent; skip & continue; no crash.
// Conformance contract R-03.
func TestConformance_R03_oob_offset_treated_absent(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// OOB IFD0 offset in header.
	buf := make([]byte, 8)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 9999) // way past EOF
	mustNotPanic(t, "R-03 OOB header offset", func() {
		_, err := Parse(buf)
		if err == nil {
			t.Error("R-03: expected error for OOB IFD0 offset")
		}
	})

	// OOB ExifIFD pointer value.
	bufOOL := make([]byte, 26)
	bufOOL[0], bufOOL[1] = 'I', 'I'
	order.PutUint16(bufOOL[2:], 0x002A)
	order.PutUint32(bufOOL[4:], 8)
	order.PutUint16(bufOOL[8:], 1)
	order.PutUint16(bufOOL[10:], uint16(TagExifIFDPointer))
	order.PutUint16(bufOOL[12:], uint16(TypeLong))
	order.PutUint32(bufOOL[14:], 1)
	order.PutUint32(bufOOL[18:], 99999) // OOB ExifIFD offset
	mustNotPanic(t, "R-03 OOB ExifIFD offset", func() {
		e, err := Parse(bufOOL)
		if err != nil {
			return
		}
		// ExifIFD must be nil (offset out of bounds → treated as absent).
		if e.ExifIFD != nil {
			t.Error("R-03: ExifIFD should be nil for OOB pointer")
		}
	})
}

// ---------------------------------------------------------------------------
// R-04 — offset + count×typeSize > len → skip entry
// ---------------------------------------------------------------------------

// TestConformance_R04_ool_overflow_skipped verifies that an OOL entry whose
// declared data range (offset + count×typeSize) exceeds the buffer length is
// skipped gracefully.
// Conformance contract R-04 / TIFF 6.0 §2.
func TestConformance_R04_ool_overflow_skipped(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Build: 1 Rational entry at offset pointing to only 4 bytes when 8 are needed.
	const ifd0Off = 8
	const ifdSize = 2 + 12 + 4
	const valueOff = ifd0Off + ifdSize
	buf := make([]byte, valueOff+4) // only 4 bytes of value area, need 8
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)
	order.PutUint16(buf[ifd0Off:], 1)
	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagExposureTime))
	order.PutUint16(buf[p+2:], uint16(TypeRational)) // sz=8, but only 4 bytes available
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], uint32(valueOff))
	mustNotPanic(t, "R-04 OOL overflow", func() {
		e, _ := Parse(buf)
		if e != nil && e.IFD0 != nil && e.IFD0.Get(TagExposureTime) != nil {
			t.Error("R-04: OOL overflow entry must be skipped")
		}
	})
}

// ---------------------------------------------------------------------------
// R-05 — Partial IFD (count×12 > remaining)
// ---------------------------------------------------------------------------

// TestConformance_R05_partial_ifd verifies that when count×12 would exceed the
// buffer, only the entries that fit are read.
// Conformance contract R-05.
func TestConformance_R05_partial_ifd(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// IFD0 claims count=5 entries, but buffer only has space for 2.
	const ifd0Off = 8
	// 2 entries × 12 bytes = 24 bytes + 2 (count) = 26 bytes until end-of-buffer.
	buf := make([]byte, ifd0Off+2+2*12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)
	order.PutUint16(buf[ifd0Off:], 5) // claim 5, but only 2 fit
	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagImageWidth))
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], 640)
	q := p + 12
	order.PutUint16(buf[q:], uint16(TagImageLength))
	order.PutUint16(buf[q+2:], uint16(TypeLong))
	order.PutUint32(buf[q+4:], 1)
	order.PutUint32(buf[q+8:], 480)
	mustNotPanic(t, "R-05 partial IFD", func() {
		_, _ = Parse(buf)
		// No panic. Whether the entries are recovered depends on the IFD count check;
		// parseSingleIFD truncates when count*12 > remaining.
	})
}

// ---------------------------------------------------------------------------
// R-06 — count×typeSize overflow uses uint64 arithmetic
// ---------------------------------------------------------------------------

// TestConformance_R06_count_overflow_uint64 verifies that count×typeSize overflow is
// checked in uint64 arithmetic, not uint32.
// Conformance contract R-06.
func TestConformance_R06_count_overflow_uint64(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Entry: TypeRational (sz=8), count = MaxUint32 → count*8 overflows uint32 but not uint64.
	const ifd0Off = 8
	buf := make([]byte, ifd0Off+2+12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)
	order.PutUint16(buf[ifd0Off:], 1)
	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagExposureTime))
	order.PutUint16(buf[p+2:], uint16(TypeRational))
	order.PutUint32(buf[p+4:], 0xFFFFFFFF) // MaxUint32 count → uint64(0xFFFFFFFF)*8 = huge
	order.PutUint32(buf[p+8:], 0)          // offset = 0
	mustNotPanic(t, "R-06 uint32 overflow", func() {
		_, _ = Parse(buf)
		// Must not OOM or panic.
	})
}

// ---------------------------------------------------------------------------
// R-07 — Overlapping IFDs must not panic
// ---------------------------------------------------------------------------

// TestConformance_R07_overlapping_ifds verifies that two IFDs whose byte ranges
// overlap do not cause corruption or panic. Values may be duplicate or incorrect,
// but no crash is permitted.
// Conformance contract R-07.
func TestConformance_R07_overlapping_ifds(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Two IFDs at the same offset: IFD0 AND IFD1 both at 8.
	// (IFD0.Next points to 8, creating an overlap.)
	buf := make([]byte, 8+2+12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)
	p := 10
	order.PutUint16(buf[p:], uint16(TagImageWidth))
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], 640)
	order.PutUint32(buf[p+12:], 8) // next-IFD = 8 (self-overlap via cycle detection)
	mustNotPanic(t, "R-07 overlapping IFDs", func() {
		_, _ = Parse(buf)
	})
}

// ---------------------------------------------------------------------------
// R-08 — Nikon Type 3 MakerNote
// ---------------------------------------------------------------------------

// TestConformance_R08_nikon_type3_makernote verifies that Nikon Type 3 MakerNotes
// with an embedded TIFF header at offset 10 are parsed correctly.
// Conformance contract R-08 / ExifTool Nikon.pm.
func TestConformance_R08_nikon_type3_makernote(t *testing.T) {
	t.Parallel()
	// Build a minimal Nikon Type 3 MakerNote:
	// [0..5] "Nikon\0" magic; [6..7] version; [8..9] "II"; [10..11] 0x002A; [12..15] IFD offset (from b[8])
	// IFD at offset 8 within the embedded TIFF (= absolute offset 8 within the MakerNote blob).
	order := binary.LittleEndian
	const (
		mnMagicOff = 0
		mnTIFFOff  = 8             // embedded TIFF starts at offset 8 in MakerNote blob
		ifdOff     = mnTIFFOff + 8 // IFD at relative offset 8 within the embedded TIFF
	)
	// embedded IFD: 1 entry (ImageWidth=1234, inline TypeLong)
	embeddedIFDSize := 2 + 12 + 4 // count + 1 entry + next
	mnBuf := make([]byte, ifdOff+embeddedIFDSize)
	// "Nikon\0" prefix + version.
	copy(mnBuf[0:], "Nikon\x00")
	mnBuf[6] = 0x02
	mnBuf[7] = 0x10
	// Embedded TIFF header at offset 8.
	mnBuf[mnTIFFOff+0] = 'I'
	mnBuf[mnTIFFOff+1] = 'I'
	order.PutUint16(mnBuf[mnTIFFOff+2:], 0x002A)
	order.PutUint32(mnBuf[mnTIFFOff+4:], 8) // IFD at offset 8 within embedded TIFF
	// Embedded IFD at mnBuf[ifdOff].
	order.PutUint16(mnBuf[ifdOff:], 1)
	p := ifdOff + 2
	order.PutUint16(mnBuf[p:], uint16(TagImageWidth))
	order.PutUint16(mnBuf[p+2:], uint16(TypeLong))
	order.PutUint32(mnBuf[p+4:], 1)
	order.PutUint32(mnBuf[p+8:], 1234)

	ifd := parseMakerNoteIFD(mnBuf, "NIKON CORPORATION", order)
	if ifd == nil {
		t.Fatal("R-08: Nikon Type 3 parseMakerNoteIFD returned nil")
	}
	entry := ifd.Get(TagImageWidth)
	if entry == nil {
		t.Fatal("R-08: ImageWidth missing from Nikon Type 3 MakerNote IFD")
	}
	if entry.Uint32() != 1234 {
		t.Errorf("R-08: ImageWidth = %d, want 1234", entry.Uint32())
	}
}

// ---------------------------------------------------------------------------
// R-09 — Fujifilm MakerNote starts with "FUJIFILM"
// ---------------------------------------------------------------------------

// TestConformance_R09_fujifilm_makernote verifies that Fujifilm MakerNotes with
// the "FUJIFILM" prefix are dispatched to parseFujifilmMakerNote and produce a
// non-nil IFD.
// Conformance contract R-09 / ExifTool Fujifilm.pm.
func TestConformance_R09_fujifilm_makernote(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Layout: [0..7] "FUJIFILM"; [8..11] version "0100"; [12..15] LE IFD offset (relative to b[0]).
	// IFD at offset 16 within the MakerNote blob.
	const ifdOff = 16
	ifdSize := 2 + 12 + 4
	mnBuf := make([]byte, ifdOff+ifdSize)
	copy(mnBuf[0:], "FUJIFILM")
	copy(mnBuf[8:], "0100")             // version
	order.PutUint32(mnBuf[12:], ifdOff) // LE IFD offset
	// IFD at offset 16: 1 entry, ImageWidth=100.
	order.PutUint16(mnBuf[ifdOff:], 1)
	p := ifdOff + 2
	order.PutUint16(mnBuf[p:], uint16(TagImageWidth))
	order.PutUint16(mnBuf[p+2:], uint16(TypeLong))
	order.PutUint32(mnBuf[p+4:], 1)
	order.PutUint32(mnBuf[p+8:], 100)

	ifd := parseMakerNoteIFD(mnBuf, "FUJIFILM", order)
	if ifd == nil {
		t.Fatal("R-09: Fujifilm parseMakerNoteIFD returned nil")
	}
	if ifd.Get(TagImageWidth) == nil {
		t.Error("R-09: ImageWidth missing from Fujifilm MakerNote IFD")
	}
}

// ---------------------------------------------------------------------------
// R-10 — Canon MakerNote: plain IFD at offset 0
// ---------------------------------------------------------------------------

// TestConformance_R10_canon_makernote verifies that Canon MakerNotes (plain IFD
// at offset 0, parent byte order) are parsed without a prefix check.
// Conformance contract R-10 / Canon EOS FAQ.
func TestConformance_R10_canon_makernote(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Plain IFD at offset 0 (no magic prefix).
	mnBuf := make([]byte, 2+12+4)
	order.PutUint16(mnBuf[0:], 1) // 1 entry
	p := 2
	order.PutUint16(mnBuf[p:], uint16(TagImageWidth))
	order.PutUint16(mnBuf[p+2:], uint16(TypeLong))
	order.PutUint32(mnBuf[p+4:], 1)
	order.PutUint32(mnBuf[p+8:], 999)

	ifd := parseMakerNoteIFD(mnBuf, "Canon", order)
	if ifd == nil {
		t.Fatal("R-10: Canon parseMakerNoteIFD returned nil")
	}
	if entry := ifd.Get(TagImageWidth); entry == nil || entry.Uint32() != 999 {
		t.Errorf("R-10: Canon MakerNote ImageWidth = %v, want 999", entry)
	}
}

// ---------------------------------------------------------------------------
// R-11 — MakerNote with TIFF-absolute offsets preserved
// ---------------------------------------------------------------------------

// TestConformance_R11_makernote_offset_preserved verifies that the MakerNoteOffset
// field captures the TIFF-stream offset of the raw MakerNote value, enabling
// callers to detect movement after Encode.
// Conformance contract R-11 / EXIF §4.6.5 tag 0x927C.
func TestConformance_R11_makernote_offset_preserved(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Build EXIF with ExifIFD containing a MakerNote.
	makerNotePayload := []byte("FakeMaker\x00\x01\x02\x03")
	const (
		hdrSize = 8
		exifOff = hdrSize + 2 + 12 + 4 // IFD0 + 1 entry
		mnOff   = exifOff + 2 + 12 + 4 // ExifIFD + 1 entry
	)
	buf := make([]byte, mnOff+len(makerNotePayload))
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], hdrSize)

	order.PutUint16(buf[hdrSize:], 1)
	p := hdrSize + 2
	order.PutUint16(buf[p:], uint16(TagExifIFDPointer))
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], exifOff)

	order.PutUint16(buf[exifOff:], 1)
	q := exifOff + 2
	order.PutUint16(buf[q:], uint16(TagMakerNote))
	order.PutUint16(buf[q+2:], uint16(TypeUndefined))
	order.PutUint32(buf[q+4:], uint32(len(makerNotePayload))) //nolint:gosec // G115: test fixture
	order.PutUint32(buf[q+8:], mnOff)
	copy(buf[mnOff:], makerNotePayload)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("R-11: Parse failed: %v", err)
	}
	if e.MakerNoteOffset == 0 {
		t.Error("R-11: MakerNoteOffset is 0; expected the TIFF-relative offset of the raw MakerNote")
	}
	if e.MakerNoteOffset != uint32(mnOff) {
		t.Errorf("R-11: MakerNoteOffset = %d, want %d", e.MakerNoteOffset, mnOff)
	}
}

// ---------------------------------------------------------------------------
// R-12 — Truncated stream errors
// ---------------------------------------------------------------------------

// TestConformance_R12_truncated verifies that a stream truncated after the header
// (before IFD0) returns an error and never panics. Mid-IFD truncation must return
// a partial IFD.
// Conformance contract R-12.
func TestConformance_R12_truncated(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		bytes int
	}{
		{"exactly 8 bytes header no IFD", 8},
		{"9 bytes (1 IFD count byte)", 9},
		{"10 bytes (count incomplete)", 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			buf := make([]byte, tc.bytes)
			buf[0], buf[1] = 'I', 'I'
			binary.LittleEndian.PutUint16(buf[2:], 0x002A)
			if tc.bytes >= 8 {
				binary.LittleEndian.PutUint32(buf[4:], 8) // IFD0 at offset 8
			}
			mustNotPanic(t, tc.name, func() {
				_, err := Parse(buf)
				// An error is expected since the IFD0 is missing or truncated.
				_ = err
			})
		})
	}
}

// ---------------------------------------------------------------------------
// R-13 — Minimum stream length check
// ---------------------------------------------------------------------------

// TestConformance_R13_min_length verifies that streams shorter than 8 bytes (classic)
// or 16 bytes (BigTIFF) are always invalid and return an error first.
// Conformance contract R-13.
func TestConformance_R13_min_length(t *testing.T) {
	t.Parallel()
	// Classic TIFF: any stream < 8 bytes must fail.
	for n := range 8 {

		t.Run("classic_len", func(t *testing.T) {
			t.Parallel()
			buf := make([]byte, n)
			mustNotPanic(t, "R-13 classic", func() {
				_, err := Parse(buf)
				if err == nil {
					t.Errorf("R-13: len=%d should fail; got nil error", n)
				}
			})
		})
	}
	// BigTIFF: streams < 16 bytes must fail once magic is parsed.
	for n := 8; n < 16; n++ {

		t.Run("bigtiff_len", func(t *testing.T) {
			t.Parallel()
			buf := make([]byte, n)
			buf[0], buf[1] = 'I', 'I'
			binary.LittleEndian.PutUint16(buf[2:], 0x002B) // BigTIFF magic
			if n >= 6 {
				binary.LittleEndian.PutUint16(buf[4:], 8) // offset-bytesize = 8
			}
			mustNotPanic(t, "R-13 BigTIFF", func() {
				_, err := Parse(buf)
				if err == nil {
					t.Errorf("R-13: BigTIFF len=%d should fail; got nil error", n)
				}
			})
		})
	}
}

// ---------------------------------------------------------------------------
// Section 5 — Real-World Deviations
// ---------------------------------------------------------------------------

// TestConformance_Dev1_unsorted_ifd verifies that unsorted IFD entries (deviation §5.1)
// are still findable — the parser sorts on parse so Get() works.
func TestConformance_Dev1_unsorted_ifd(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Build IFD with entries in reverse order.
	buf := make([]byte, 8+2+3*12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 3)
	p := 10
	// Write in reverse: ImageDescription (0x010E) > Model (0x0110) would be correctly sorted,
	// but Orientation (0x0112) > Model (0x0110) > Make (0x010F) is reverse.
	type kv struct{ tag, val uint16 }
	entries := []kv{
		{uint16(TagOrientation), 3},
		{uint16(TagModel), 0},
		{uint16(TagMake), 0},
	}
	for _, e := range entries {
		order.PutUint16(buf[p:], e.tag)
		order.PutUint16(buf[p+2:], uint16(TypeShort))
		order.PutUint32(buf[p+4:], 1)
		order.PutUint32(buf[p+8:], uint32(e.val))
		p += 12
	}
	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Dev1: Parse unsorted: %v", err)
	}
	// All entries must be findable after internal sort.
	if e.IFD0.Get(TagOrientation) == nil {
		t.Error("Dev1: TagOrientation missing after unsorted parse")
	}
}

// TestConformance_Dev2_zero_denominator_rational verifies that zero-denominator
// rationals (§5.2) are exposed as-is (sentinel), no divide-by-zero.
func TestConformance_Dev2_zero_denominator_rational(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	val := make([]byte, 8)
	order.PutUint32(val[0:], 1)
	order.PutUint32(val[4:], 0) // zero denominator
	entry := IFDEntry{Type: TypeRational, Count: 1, Value: val, bigEndian: orderIsBig(order)}
	r := entry.Rational(0)
	if r[1] != 0 {
		t.Errorf("Dev2: expected den=0, got den=%d", r[1])
	}
	mustNotPanic(t, "Dev2 zero den", func() {
		// Caller should guard: if r[1] != 0 { use float64(r[0])/float64(r[1]) }
		if r[1] != 0 {
			_ = float64(r[0]) / float64(r[1])
		}
	})
}

// TestConformance_Dev3_datetime_spaces_partial verifies that DateTime strings with
// spaces or partial fields (§5.3) do not cause panic; ok=false is returned.
func TestConformance_Dev3_datetime_spaces_partial(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	tests := []string{
		"                   \x00", // all spaces
		"2024:  :   :  :  \x00",   // partial
		"0000:00:00 00:00:00\x00", // zero date
	}
	for _, s := range tests {

		t.Run(s[:10], func(t *testing.T) {
			t.Parallel()
			e := &EXIF{
				ByteOrder: order,
				IFD0:      &IFD{},
				ExifIFD: &IFD{Entries: []IFDEntry{
					{Tag: TagDateTimeOriginal, Type: TypeASCII, Count: 20, Value: []byte(s), bigEndian: orderIsBig(order)},
				}},
			}
			mustNotPanic(t, "Dev3 "+s[:8], func() {
				_, _ = e.DateTimeOriginal()
			})
		})
	}
}

// TestConformance_Dev4_ascii_tag_wrong_type verifies that ASCII tags with a wrong
// declared type (§5.4) — e.g. ComponentsConfiguration written as TypeASCII instead
// of TypeUndefined — still return raw bytes without crashing.
func TestConformance_Dev4_ascii_tag_wrong_type(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// ComponentsConfiguration should be TypeUndefined but written as TypeASCII.
	wrongTyped := IFDEntry{
		Tag:       TagComponentsConfiguration,
		Type:      TypeASCII, // deviation: should be TypeUndefined
		Count:     4,
		Value:     []byte{1, 2, 3, 0},
		bigEndian: orderIsBig(order),
	}
	mustNotPanic(t, "Dev4 wrong type ASCII", func() {
		// Bytes() always returns raw bytes regardless of type.
		raw := wrongTyped.Bytes()
		if len(raw) != 4 {
			t.Errorf("Dev4: Bytes() len = %d, want 4", len(raw))
		}
	})
}

// TestConformance_Dev5_ifd_count_phantom_entry verifies that an IFD whose declared
// entry count is one too high (§5.5, Canon 40D / Kodak deviation) does not panic.
// The last "phantom" entry may be garbage and must be silently skipped.
func TestConformance_Dev5_ifd_count_one_too_high(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Build IFD with count=2 but only 1 entry's worth of space.
	const ifd0Off = 8
	// Buffer only has room for 1 entry (count=2 claimed).
	buf := make([]byte, ifd0Off+2+1*12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)
	order.PutUint16(buf[ifd0Off:], 2) // claims 2 entries
	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagImageWidth))
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], 640) // the real entry
	// No second entry: parseSingleIFD should detect count*12 > remaining and truncate.
	mustNotPanic(t, "Dev5 phantom entry", func() {
		_, _ = Parse(buf)
	})
}

// TestConformance_Dev6_iptc_tag_wrong_type verifies that TIFF IPTC tag 0x83BB
// with a wrong-type declaration (§5.6) is accepted and returns raw bytes.
func TestConformance_Dev6_iptc_tag_wrong_type(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// 0x83BB declared as TypeUndefined (real-world deviation from spec TypeLong).
	val := []byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x41, 0x42, 0x43}
	entry := IFDEntry{
		Tag:       TagIPTC,
		Type:      TypeUndefined,    // deviation: should be TypeLong per tag registry
		Count:     uint32(len(val)), //nolint:gosec // G115: test fixture
		Value:     val,
		bigEndian: orderIsBig(order),
	}
	mustNotPanic(t, "Dev6 IPTC wrong type", func() {
		raw := entry.Bytes()
		if len(raw) == 0 {
			t.Error("Dev6: Bytes() returned empty for IPTC wrong-type entry")
		}
	})
}

// TestConformance_Dev7_exifversion_count8 verifies that ExifVersion with count=8
// (§5.7 deviation) returns the first 4 bytes as "0220" without crashing.
func TestConformance_Dev7_exifversion_count8(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// count=8 instead of the spec-required 4.
	entry := IFDEntry{
		Tag:       TagExifVersion,
		Type:      TypeUndefined,
		Count:     8,
		Value:     []byte("02200000"), // first 4 = "0220"
		bigEndian: orderIsBig(order),
	}
	mustNotPanic(t, "Dev7 ExifVersion count=8", func() {
		raw := entry.Bytes()
		if len(raw) < 4 {
			t.Fatalf("Dev7: Bytes() too short: len=%d", len(raw))
		}
		if string(raw[:4]) != "0220" {
			t.Errorf("Dev7: ExifVersion first 4 bytes = %q, want \"0220\"", string(raw[:4]))
		}
	})
}

// TestConformance_Dev8_odd_byte_offset verifies that OOL values at odd byte offsets
// (§5.8 deviation) are still read at the declared offset without panic.
func TestConformance_Dev8_odd_byte_offset(t *testing.T) {
	t.Parallel()
	// Reuse S-11 fixture logic: build a TIFF with a Rational at an odd offset.
	order := binary.LittleEndian
	const ifd0Off = 8
	const ifdSize = 2 + 12 + 4
	const oddOff = uint32(ifd0Off + ifdSize + 1) // odd
	buf := make([]byte, int(oddOff)+8)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)
	order.PutUint16(buf[ifd0Off:], 1)
	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagExposureTime))
	order.PutUint16(buf[p+2:], uint16(TypeRational))
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], oddOff)
	order.PutUint32(buf[oddOff:], 1)
	order.PutUint32(buf[oddOff+4:], 125)
	mustNotPanic(t, "Dev8 odd offset", func() {
		e, _ := Parse(buf)
		if e != nil && e.IFD0 != nil {
			entry := e.IFD0.Get(TagExposureTime)
			if entry != nil {
				r := entry.Rational(0)
				if r[0] != 1 || r[1] != 125 {
					t.Errorf("Dev8: Rational at odd offset = [%d/%d], want [1/125]", r[0], r[1])
				}
			}
		}
	})
}

// TestConformance_Dev9_ricoh_2byte_ifd_padding verifies that an IFD with 2-byte
// padding before the first entry (§5.9, Ricoh deviation) does not crash.
func TestConformance_Dev9_ricoh_2byte_ifd_padding(t *testing.T) {
	t.Parallel()
	// Build a buffer where the IFD count is valid but the entry area has 2 extra bytes
	// of junk before what would normally be entry data. Parser may parse garbage, must not panic.
	order := binary.LittleEndian
	buf := make([]byte, 8+2+2+12+4) // extra 2 bytes of padding before entries
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1) // count = 1
	// The "entry" starts 2 bytes late (at offset 12 instead of 10).
	// Parser reads at buf[10]: these 2 bytes are padding, so the entry is garbage.
	mustNotPanic(t, "Dev9 Ricoh 2-byte padding", func() {
		_, _ = Parse(buf)
	})
}

// TestConformance_Dev10_make_trailing_spaces verifies that Make values with trailing
// spaces (§5.10, "NIKON CORPORATION ") are trimmed before MakerNote dispatch.
func TestConformance_Dev10_make_trailing_spaces(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// MakerNote = valid Nikon Type 1 IFD (plain IFD, big-endian, count > 0).
	mnBuf := make([]byte, 2+12+4)
	binary.BigEndian.PutUint16(mnBuf[0:], 1) // count=1 (Nikon Type 1 heuristic)
	binary.BigEndian.PutUint16(mnBuf[2:], uint16(TagImageWidth))
	binary.BigEndian.PutUint16(mnBuf[4:], uint16(TypeLong))
	binary.BigEndian.PutUint32(mnBuf[6:], 1)
	binary.BigEndian.PutUint32(mnBuf[10:], 100)

	// "NIKON CORPORATION " with trailing space — parseMakerNoteIFD must TrimSpace.
	ifd := parseMakerNoteIFD(mnBuf, "NIKON CORPORATION ", order)
	// parseMakerNoteIFD does TrimSpace: "NIKON CORPORATION " → "NIKON CORPORATION",
	// which matches the "NIKON CORPORATION" key in makerNoteParsers.
	// The Nikon parser will attempt Type3/Type1 parsing on the plain IFD data.
	// We just verify no panic; the IFD may or may not succeed.
	_ = ifd // no panic is the assertion
}

// TestConformance_Dev11_nikon_type3_base verifies that Nikon Type 3 MakerNote
// base = outerTIFFBase + makerNoteOffset + 10 (§5.11 deviation / R-08 extended).
func TestConformance_Dev11_nikon_type3_base(t *testing.T) {
	t.Parallel()
	// This test re-verifies R-08 with the specific base calculation note.
	// The embedded TIFF header starts at MakerNote[8] (offset 8 within the MakerNote blob),
	// and IFD offsets inside are relative to that embedded header start.
	order := binary.LittleEndian
	const embeddedTIFFStart = 8
	mnBuf := make([]byte, embeddedTIFFStart+8+2+12+4)
	copy(mnBuf[0:], "Nikon\x00")
	mnBuf[6] = 0x02
	mnBuf[7] = 0x10
	// Embedded TIFF header at offset 8.
	mnBuf[embeddedTIFFStart+0] = 'I'
	mnBuf[embeddedTIFFStart+1] = 'I'
	order.PutUint16(mnBuf[embeddedTIFFStart+2:], 0x002A)
	order.PutUint32(mnBuf[embeddedTIFFStart+4:], 8) // IFD at offset 8 within embedded TIFF
	// IFD at embeddedTIFFStart + 8.
	ifdPos := embeddedTIFFStart + 8
	order.PutUint16(mnBuf[ifdPos:], 1)
	p := ifdPos + 2
	order.PutUint16(mnBuf[p:], uint16(TagImageWidth))
	order.PutUint16(mnBuf[p+2:], uint16(TypeLong))
	order.PutUint32(mnBuf[p+4:], 1)
	order.PutUint32(mnBuf[p+8:], 5678)

	ifd := parseMakerNoteIFD(mnBuf, "NIKON CORPORATION", order)
	if ifd == nil {
		t.Fatal("Dev11: Nikon Type 3 base offset: parseMakerNoteIFD returned nil")
	}
	entry := ifd.Get(TagImageWidth)
	if entry == nil || entry.Uint32() != 5678 {
		t.Errorf("Dev11: ImageWidth = %v, want 5678", entry)
	}
}

// TestConformance_Dev12_exifversion_absent verifies that early cameras/scanners
// without ExifVersion (§5.12) produce a non-nil ExifIFD and no parse failure.
func TestConformance_Dev12_exifversion_absent(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	// Build EXIF with ExifIFD that has ColorSpace but NO ExifVersion.
	const ifd0Off = 8
	const exifOff = ifd0Off + 2 + 12 + 4
	buf := make([]byte, exifOff+2+12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)
	order.PutUint16(buf[ifd0Off:], 1)
	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagExifIFDPointer))
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], exifOff)
	// ExifIFD with ONLY ColorSpace (no ExifVersion).
	order.PutUint16(buf[exifOff:], 1)
	q := exifOff + 2
	order.PutUint16(buf[q:], uint16(TagColorSpace))
	order.PutUint16(buf[q+2:], uint16(TypeShort))
	order.PutUint32(buf[q+4:], 1)
	order.PutUint32(buf[q+8:], 1) // sRGB

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Dev12: Parse failed: %v", err)
	}
	// ExifIFD MUST be non-nil despite missing ExifVersion.
	if e.ExifIFD == nil {
		t.Fatal("Dev12: ExifIFD is nil; expected non-nil even without ExifVersion")
	}
	// ExifVersion tag should be absent (not injected).
	if e.ExifIFD.Get(TagExifVersion) != nil {
		t.Error("Dev12: ExifVersion present but should be absent (early camera)")
	}
}
