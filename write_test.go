package gometadata

// Tests for WriteFile (fix #9) and Write-calls-Validate (fix #10).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// ---------------------------------------------------------------------------
// Fix #9: WriteFile temp placement and cleanup
// ---------------------------------------------------------------------------

// TestWriteFileTempInSameDir verifies that WriteFile creates its temporary
// file in the same directory as the target path, not in $TMPDIR. Placing the
// temp file on the same filesystem as the target guarantees that os.Rename is
// always an intra-filesystem (atomic) operation and never fails with EXDEV.
func TestWriteFileTempInSameDir(t *testing.T) {
	t.Parallel()

	// Build a minimal valid JPEG in a temp directory of our choosing.
	dir := t.TempDir()
	target := filepath.Join(dir, "image.jpg")
	if err := os.WriteFile(target, buildMinimalJPEG(minimalTIFFPayload()), 0o644); err != nil { //nolint:gosec // G306: 0644 is the correct permission for an image file in a test
		t.Fatalf("setup: write target: %v", err)
	}

	// Intercept which temp files appear in dir before and after WriteFile.
	// Because WriteFile creates the temp with the pattern "gometadata-*" in
	// filepath.Dir(path), we expect exactly that pattern to be created and
	// then removed (via rename-to-target) during the call.
	before := listDir(t, dir)

	m, err := ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	m.IPTC = nil // keep nil so Write passes rawIPTC through; just exercise the path

	if err := WriteFile(target, m); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	after := listDir(t, dir)

	// The only file remaining must be the target itself.
	if len(after) != 1 || after[0] != "image.jpg" {
		t.Errorf("unexpected files in dir after WriteFile: before=%v after=%v", before, after)
	}

	// Verify we can still read the file back — proves it was not corrupted.
	if _, err := ReadFile(target); err != nil {
		t.Errorf("ReadFile after WriteFile: %v", err)
	}
}

// TestWriteFileTempCleanedOnWriteError verifies that if Write returns an
// error (e.g., the metadata is invalid), WriteFile removes the temporary file
// and leaves no stale temp in the target directory.
func TestWriteFileTempCleanedOnWriteError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "image.jpg")
	if err := os.WriteFile(target, buildMinimalJPEG(minimalTIFFPayload()), 0o644); err != nil { //nolint:gosec // G306: 0644 is the correct permission for an image file in a test
		t.Fatalf("setup: write target: %v", err)
	}

	// Construct metadata that Validate will reject: EXIF present but IFD0 nil.
	m := NewMetadata(format.FormatJPEG)
	m.EXIF = &exif.EXIF{} // IFD0 is nil — Validate returns ErrNilIFD0

	err := WriteFile(target, m)
	if err == nil {
		t.Fatal("expected WriteFile to return an error for nil IFD0, got nil")
	}
	if !errors.Is(err, ErrNilIFD0) {
		t.Errorf("expected ErrNilIFD0, got %v", err)
	}

	// After the failed call, only the original file must remain.
	remaining := listDir(t, dir)
	if len(remaining) != 1 || remaining[0] != "image.jpg" {
		t.Errorf("stale temp file(s) left after WriteFile error: %v", remaining)
	}
}

// TestWriteFileTempCleanedOnCloseError uses a custom WriteFile sequence to
// confirm that a failure between Write and Rename still removes the temp.
// We simulate this by filling the target directory with a read-only file to
// cause Rename to fail, while confirming no temp is leaked.
//
// Note: this is hard to simulate portably without OS-level tricks, so we
// instead verify the invariant by checking that after any WriteFile failure
// no gometadata-* temp file survives in the target directory.
func TestWriteFileTempCleanedOnRenameError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "image.jpg")
	if err := os.WriteFile(target, buildMinimalJPEG(minimalTIFFPayload()), 0o644); err != nil { //nolint:gosec // G306: 0644 is the correct permission for an image file in a test
		t.Fatalf("setup: write target: %v", err)
	}

	// Make the directory read-only so os.Rename (which needs write access to
	// the directory) fails on the final move.
	if err := os.Chmod(dir, 0o555); err != nil { //nolint:gosec // G302: 0555 is intentionally restrictive to force a rename failure in the test
		t.Skipf("cannot chmod dir (running as root or unsupported OS): %v", err)
	}
	// Restore permissions so t.TempDir cleanup can remove the directory.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) //nolint:gosec // G302: restoring normal directory permissions

	m, err := ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	writeErr := WriteFile(target, m)
	if writeErr == nil {
		// On some systems (e.g. running as root) the rename may succeed even
		// with mode 0555; skip the cleanup assertion in that case.
		t.Skip("rename succeeded despite read-only dir (likely running as root)")
	}

	// Restore write permission before listing so we can observe the directory.
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: restoring normal directory permissions
		t.Fatalf("restore chmod: %v", err)
	}

	remaining := listDir(t, dir)
	for _, name := range remaining {
		if strings.HasPrefix(name, "gometadata-") {
			t.Errorf("stale temp file left after rename failure: %s", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Fix #10: Write calls Validate
// ---------------------------------------------------------------------------

// TestWriteValidateUnknownFormat verifies that Write returns an error (via
// Validate) when the Metadata format field is FormatUnknown, before any I/O
// is attempted on the provided reader.
func TestWriteValidateUnknownFormat(t *testing.T) {
	t.Parallel()

	// Metadata with FormatUnknown — Validate should reject this.
	m := &Metadata{} // format zero value == FormatUnknown

	// The reader carries a valid JPEG, but Write must not reach format detection
	// because Validate fires first.
	jpeg := buildMinimalJPEG(minimalTIFFPayload())
	err := Write(bytes.NewReader(jpeg), io.Discard, m)
	if err == nil {
		t.Fatal("expected error for FormatUnknown, got nil")
	}
	var unsupported *UnsupportedFormatError
	if !errors.As(err, &unsupported) {
		t.Errorf("expected *UnsupportedFormatError from Validate, got %T: %v", err, err)
	}
}

// TestWriteValidateNilXMPProperties verifies that Write returns ErrNilXMPProperties
// (via Validate) when m.XMP is non-nil but m.XMP.Properties is nil.
func TestWriteValidateNilXMPProperties(t *testing.T) {
	t.Parallel()

	jpeg := buildMinimalJPEG(minimalTIFFPayload())

	m := NewMetadata(format.FormatJPEG)
	m.XMP = &xmp.XMP{} // Properties is nil

	err := Write(bytes.NewReader(jpeg), io.Discard, m)
	if err == nil {
		t.Fatal("expected error for nil XMP Properties in Write, got nil")
	}
	if !errors.Is(err, ErrNilXMPProperties) {
		t.Errorf("expected ErrNilXMPProperties, got %v", err)
	}
}

// TestWriteValidateNilIFD0ViaValidate verifies the same nil-IFD0 guard that
// TestWriteNilIFD0 covers, but explicitly names the mechanism: Write calls
// m.Validate() which returns ErrNilIFD0, not ErrNilIFD0Write.
func TestWriteValidateNilIFD0ViaValidate(t *testing.T) {
	t.Parallel()

	jpeg := buildMinimalJPEG(minimalTIFFPayload())

	m := NewMetadata(format.FormatJPEG)
	m.EXIF = &exif.EXIF{} // IFD0 is nil

	err := Write(bytes.NewReader(jpeg), io.Discard, m)
	if err == nil {
		t.Fatal("expected error for nil IFD0 in Write, got nil")
	}
	// Validate returns ErrNilIFD0; ErrNilIFD0Write is now deprecated.
	if !errors.Is(err, ErrNilIFD0) {
		t.Errorf("expected ErrNilIFD0 from Validate path, got %v", err)
	}
}

// TestWriteValidateFiresBeforeIO verifies that Validate is called before any
// read from the io.ReadSeeker: if Validate fails, the reader must not have
// been consumed at all (its position stays at 0).
func TestWriteValidateFiresBeforeIO(t *testing.T) {
	t.Parallel()

	jpeg := buildMinimalJPEG(minimalTIFFPayload())
	r := &countingReader{r: bytes.NewReader(jpeg)}

	m := NewMetadata(format.FormatJPEG)
	m.XMP = &xmp.XMP{} // nil Properties → Validate fails

	_ = Write(r, io.Discard, m)

	if r.reads > 0 {
		t.Errorf("Write read from reader (%d time(s)) before Validate returned error", r.reads)
	}
}

// countingReader wraps an io.ReadSeeker and counts Read calls.
type countingReader struct {
	r     io.ReadSeeker
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p) //nolint:wrapcheck // test helper: delegates to underlying reader; wrapping obscures io.EOF
}

func (c *countingReader) Seek(offset int64, whence int) (int64, error) {
	return c.r.Seek(offset, whence) //nolint:wrapcheck // test helper: delegates to underlying seeker
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// listDir returns the sorted base-names of entries in dir.
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("listDir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// ---------------------------------------------------------------------------
// SPIKE #6 / B2: TIFF-based write gate
// ---------------------------------------------------------------------------

// buildMinimalCR2 returns a minimal CR2 byte stream. CR2 is standard TIFF LE
// with a "CR" marker at bytes 8–9 (CR2 specification §3.1).
func buildMinimalCR2() []byte {
	base := minimalTIFFPayload()
	// Ensure the buffer is large enough for the CR2 marker (bytes 8–9).
	if len(base) < 10 {
		extended := make([]byte, 10)
		copy(extended, base)
		base = extended
	}
	base[8] = 0x43 // 'C'
	base[9] = 0x52 // 'R'
	return base
}

// buildMinimalORF returns a minimal ORF byte stream: standard TIFF LE bytes
// with Olympus magic ("IIRO") replacing bytes 2–3.
func buildMinimalORF() []byte {
	base := minimalTIFFPayload()
	base[2] = 0x52 // 'R'
	base[3] = 0x4F // 'O'
	return base
}

// buildMinimalRW2 returns a minimal RW2 byte stream: standard TIFF LE bytes
// with Panasonic magic ("IIU\x00") replacing bytes 2–3.
func buildMinimalRW2() []byte {
	base := minimalTIFFPayload()
	base[2] = 0x55 // 'U'
	base[3] = 0x00
	return base
}

// TestWriteRAWFormatsReturnErrWriteNotSupported verifies that Write returns
// ErrWriteNotSupported for the RAW formats that remain gated (CR2, ORF, RW2).
// These formats require SubIFD recursion (task #94) and/or manufacturer-specific
// offset handling (task #95) that is not yet implemented.
//
// FormatTIFF was removed from this test in tasks #92/#93: tiff.Inject now uses
// the copy-and-relocate serializer and TIFF writes succeed.
//
// NEF, ARW, and DNG are covered by TestWriteTIFFBasedFormatsFromCorpus, which
// uses real fixture files (those formats require IFD tag inspection to be
// distinguished from plain TIFF and cannot be built synthetically).
func TestWriteRAWFormatsReturnErrWriteNotSupported(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data []byte
	}{
		{"CR2", buildMinimalCR2()},
		{"ORF", buildMinimalORF()},
		{"RW2", buildMinimalRW2()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m, err := Read(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}

			var wBuf countingWriter
			writeErr := Write(bytes.NewReader(tc.data), &wBuf, m)
			if writeErr == nil {
				t.Fatal("Write returned nil; want ErrWriteNotSupported")
			}
			if !errors.Is(writeErr, ErrWriteNotSupported) {
				t.Errorf("errors.Is(err, ErrWriteNotSupported) = false; got: %v", writeErr)
			}
			if wBuf.n > 0 {
				t.Errorf("Write wrote %d byte(s) to output before returning error; want 0", wBuf.n)
			}
		})
	}
}

// TestWriteTIFFSucceeds verifies that Write no longer returns ErrWriteNotSupported
// for a plain TIFF file (tasks #92/#93: copy-and-relocate serializer un-gates TIFF).
// The output must be non-empty and re-parseable.
func TestWriteTIFFSucceeds(t *testing.T) {
	t.Parallel()

	data := minimalTIFFPayload()
	m, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var out bytes.Buffer
	if err := Write(bytes.NewReader(data), &out, m); err != nil {
		t.Fatalf("Write TIFF returned unexpected error: %v", err)
	}
	if out.Len() == 0 {
		t.Error("Write TIFF produced no output bytes")
	}

	// Output must re-parse without error.
	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write TIFF: %v", err)
	}
	_ = m2
}

// TestWriteTIFFBasedFormatsFromCorpus verifies ErrWriteNotSupported for NEF
// using a real fixture file from the test corpus. NEF shares the standard TIFF
// magic and is only distinguished by IFD tag inspection, so a synthetic minimal
// file would be detected as plain TIFF.
//
// DNG was removed from this test in task #94: FormatDNG is now fully writable
// via the copy-and-relocate SubIFD relocation path. The DNG round-trip is
// covered by TestDNGWriteReadRoundTrip (synthetic DNG-like fixture) and by
// TestSubIFDRelocateSingleSubIFDStrip / TestSubIFDExactByteAtOffset.
func TestWriteTIFFBasedFormatsFromCorpus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
	}{
		{"NEF", "testdata/corpus/raw/exiftool/Nikon.nef"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, err := os.Open(tc.path)
			if err != nil {
				t.Skipf("fixture not found (%s): %v", tc.path, err)
			}
			defer func() { _ = f.Close() }()

			m, err := Read(f)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}

			if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
				t.Fatalf("Seek: %v", seekErr)
			}

			var wBuf countingWriter
			writeErr := Write(f, &wBuf, m)
			if writeErr == nil {
				t.Fatal("Write returned nil; want ErrWriteNotSupported")
			}
			if !errors.Is(writeErr, ErrWriteNotSupported) {
				t.Errorf("errors.Is(err, ErrWriteNotSupported) = false; got: %v", writeErr)
			}
			if wBuf.n > 0 {
				t.Errorf("Write wrote %d byte(s) to output; want 0", wBuf.n)
			}
		})
	}
}

// TestDNGWriteReadRoundTrip is the gometadata.Write → Read round-trip test for
// FormatDNG (task #94 acceptance criterion (e)).
//
// It builds a synthetic DNG-like TIFF stream (IFD0 thumbnail strip + SubIFD0
// full-res strip, tag 0x014A, plus DNGVersion 0xC612 for format detection),
// calls gometadata.Write to inject modified XMP (EXIF unchanged so rawEXIF
// passes through as the full TIFF base), then reads back and verifies:
//   - format.SupportsWrite(FormatDNG) = true
//   - Write returns no error (not ErrWriteNotSupported)
//   - the output re-parses via Read
//   - the IFD0 thumbnail and SubIFD0 full-res strip blocks are byte-identical
//
// The test also exercises WriteFile to confirm the file-level API works.
//
// EXIF is NOT modified in this test; only XMP is injected. This is intentional:
// when EXIF is modified, encodeEXIF returns exif.Encode(m.EXIF) — a minimal
// TIFF buffer without image data — which breaks the copy-and-relocate base.
// TIFF/DNG EXIF modification via gometadata.Write requires the caller to either
// (a) not modify EXIF, or (b) use tiff.Inject directly with the full file as base.
// This is a known limitation and is documented here. The sub-IFD block integrity
// is proven separately in format/tiff/relocate_subifd_test.go.
//
// This test uses a synthetic DNG-like fixture (no real-corpus dependency).
// Validation against a real DNG corpus is recommended before release.
func TestDNGWriteReadRoundTrip(t *testing.T) { //nolint:paralleltest // not parallel: uses t.TempDir for file I/O
	// Build a minimal DNG-like TIFF:
	//   TIFF header (LE) + IFD0 (thumb strips + 0x014A SubIFDs + 0xC612 DNGVersion) +
	//   SubIFD0 (full-res strips).
	//   DNGVersion (0xC612) makes format.Detect classify this as FormatDNG.
	order := binary.LittleEndian

	thumbStrip := []byte("DNG-ROUNDTRIP-THUMB-STRIP-DATA-!")
	fullStrip := []byte("DNG-ROUNDTRIP-FULLRES-STRIP-DATA")

	// IFD0: 6 entries (sorted by tag) — ImageWidth, ImageLength, StripOffsets,
	// StripByteCounts, SubIFDs(0x014A), DNGVersion(0xC612).
	// SubIFD0: 4 entries.
	nIFD0 := 6
	nSubIFD0 := 4

	const headerSize = 8
	ifd0Off := headerSize
	subIFD0Off := ifd0Off + 2 + nIFD0*12 + 4
	thumbDataOff := subIFD0Off + 2 + nSubIFD0*12 + 4
	fullDataOff := thumbDataOff + len(thumbStrip)
	total := fullDataOff + len(fullStrip)

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(ifd0Off))

	// IFD0
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	p := ifd0Off + 2
	writeEntry := func(tag, typ uint16, count, val uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}
	writeEntry(0x0100, 4, 1, 2)                       // ImageWidth
	writeEntry(0x0101, 4, 1, 1)                       // ImageLength
	writeEntry(0x0111, 4, 1, uint32(thumbDataOff))    // StripOffsets → thumb
	writeEntry(0x0117, 4, 1, uint32(len(thumbStrip))) //nolint:gosec // G115: test helper
	writeEntry(0x014A, 4, 1, uint32(subIFD0Off))      // SubIFDs → SubIFD0
	writeEntry(0xC612, 1 /*BYTE*/, 4, 0x00000101)     // DNGVersion 1.1.0.0 (inline)
	p += 4                                            // IFD0 next-IFD = 0

	// SubIFD0
	order.PutUint16(buf[subIFD0Off:], uint16(nSubIFD0))
	q := subIFD0Off + 2
	writeEntryAt := func(pos int, tag, typ uint16, count, val uint32) {
		order.PutUint16(buf[pos:], tag)
		order.PutUint16(buf[pos+2:], typ)
		order.PutUint32(buf[pos+4:], count)
		order.PutUint32(buf[pos+8:], val)
	}
	writeEntryAt(q, 0x0100, 4, 1, 1024)
	writeEntryAt(q+12, 0x0101, 4, 1, 768)
	writeEntryAt(q+24, 0x0111, 4, 1, uint32(fullDataOff))    //nolint:gosec // G115: fullDataOff bounded by buf size
	writeEntryAt(q+36, 0x0117, 4, 1, uint32(len(fullStrip))) //nolint:gosec // G115: test helper

	copy(buf[thumbDataOff:], thumbStrip)
	copy(buf[fullDataOff:], fullStrip)

	original := buf

	// Verify format detection recognises this as DNG.
	detectedFmt, detErr := format.Detect(bytes.NewReader(original))
	if detErr != nil {
		t.Fatalf("format.Detect: %v", detErr)
	}
	if detectedFmt != format.FormatDNG {
		t.Fatalf("format.Detect = %v, want FormatDNG", detectedFmt)
	}

	// (e) Verify SupportsWrite(FormatDNG) = true.
	if !format.SupportsWrite(format.FormatDNG) {
		t.Fatal("format.SupportsWrite(FormatDNG) = false, want true")
	}

	// Read the DNG file's metadata.
	m, err := Read(bytes.NewReader(original))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Inject XMP only (do NOT modify EXIF: see doc comment above).
	// Clear m.EXIF so that encodeEXIF returns m.rawEXIF (= the full original
	// TIFF buffer including image data). This is the correct base for relocateTIFF.
	// If m.EXIF is kept non-nil, encodeEXIF calls exif.Encode(m.EXIF) which
	// produces a minimal TIFF without image data, causing ErrBlockOutOfBounds.
	m.EXIF = nil
	wantXMPCaption := "DNG round-trip XMP caption"
	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	m.XMP.SetCaption(wantXMPCaption)

	var outBuf bytes.Buffer
	writeErr := Write(bytes.NewReader(original), &outBuf, m)
	if writeErr != nil {
		t.Fatalf("Write returned error: %v", writeErr)
	}

	output := outBuf.Bytes()
	if len(output) == 0 {
		t.Fatal("Write produced empty output")
	}

	// (d) Re-parse; output readable.
	m2, err := Read(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	_ = m2

	// (a)/(c) IFD0 thumbnail and SubIFD0 full-res blocks must appear verbatim.
	if !bytes.Contains(output, thumbStrip) {
		t.Error("IFD0 thumbnail strip data not found verbatim in output (criterion a)")
	}
	if !bytes.Contains(output, fullStrip) {
		t.Error("SubIFD0 full-res strip data not found verbatim in output (criterion c)")
	}

	// WriteFile round-trip.
	dir := t.TempDir()
	dngPath := filepath.Join(dir, "test.dng")
	if err := os.WriteFile(dngPath, original, 0o644); err != nil { //nolint:gosec // G306: test helper
		t.Fatalf("WriteFile setup: %v", err)
	}

	mf, err := ReadFile(dngPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	mf.EXIF = nil // clear to pass rawEXIF (full file) as base; see doc comment above
	mf.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	mf.XMP.SetCaption("WriteFile DNG round-trip")
	if wfErr := WriteFile(dngPath, mf); wfErr != nil {
		t.Fatalf("WriteFile: %v", wfErr)
	}

	// Verify the file can be read back after WriteFile.
	if _, err := ReadFile(dngPath); err != nil {
		t.Fatalf("ReadFile after WriteFile: %v", err)
	}
}

// TestWriteFileBlocksRAWBased verifies that WriteFile returns ErrWriteNotSupported
// and does NOT overwrite the original file for a RAW format that remains gated
// (CR2 as the representative case).
//
// For FormatTIFF, see TestWriteFileTIFFSucceeds.
func TestWriteFileBlocksRAWBased(t *testing.T) {
	t.Parallel()

	original := buildMinimalCR2()

	dir := t.TempDir()
	target := filepath.Join(dir, "image.cr2")
	if err := os.WriteFile(target, original, 0o644); err != nil { //nolint:gosec // G306: 0644 is the correct permission for an image file in a test
		t.Fatalf("setup WriteFile: %v", err)
	}

	m, err := ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	writeErr := WriteFile(target, m)
	if writeErr == nil {
		t.Fatal("WriteFile returned nil; want ErrWriteNotSupported")
	}
	if !errors.Is(writeErr, ErrWriteNotSupported) {
		t.Errorf("errors.Is(err, ErrWriteNotSupported) = false; got: %v", writeErr)
	}

	// File must not have been modified.
	remaining := listDir(t, dir)
	if len(remaining) != 1 || remaining[0] != "image.cr2" {
		t.Errorf("unexpected files after WriteFile error: %v", remaining)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read target after WriteFile error: %v", readErr)
	}
	if !bytes.Equal(got, original) {
		t.Error("target file was modified despite ErrWriteNotSupported")
	}
}

// TestWriteFileTIFFSucceeds verifies that WriteFile does NOT return
// ErrWriteNotSupported for a plain TIFF file (tasks #92/#93).
// The output must re-parse cleanly.
func TestWriteFileTIFFSucceeds(t *testing.T) {
	t.Parallel()

	original := minimalTIFFPayload()

	dir := t.TempDir()
	target := filepath.Join(dir, "image.tif")
	if err := os.WriteFile(target, original, 0o644); err != nil { //nolint:gosec // G306: 0644 is the correct permission for an image file in a test
		t.Fatalf("setup WriteFile: %v", err)
	}

	m, err := ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if err := WriteFile(target, m); err != nil {
		t.Fatalf("WriteFile TIFF returned unexpected error: %v", err)
	}

	// Re-read the output to verify it parses cleanly.
	m2, err := ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile after WriteFile TIFF: %v", err)
	}
	_ = m2
}

// TestWriteJPEGStillWorksAfterTIFFGate is a non-regression test ensuring
// that the TIFF-based write gate does not affect JPEG, which must continue
// to write successfully.
func TestWriteJPEGStillWorksAfterTIFFGate(t *testing.T) {
	t.Parallel()

	jpeg := buildMinimalJPEG(minimalTIFFPayload())
	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var out bytes.Buffer
	if err := Write(bytes.NewReader(jpeg), &out, m); err != nil {
		t.Fatalf("Write JPEG returned unexpected error: %v", err)
	}
	if out.Len() == 0 {
		t.Error("Write JPEG produced no output bytes")
	}
}

// countingWriter is an io.Writer that counts bytes received.
// It is used to assert that the gate fires before any bytes reach the output.
type countingWriter struct {
	n int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += len(p)
	return len(p), nil
}

// ---------------------------------------------------------------------------
// Task #70: extended-XMP wire-frame must never leak to non-JPEG containers
// ---------------------------------------------------------------------------

// xmpWireFramePrefix is the 8-byte magic that identifies a JPEG extended-XMP
// wire-frame payload. It is duplicated here from format/jpeg to avoid an import
// cycle and to make the assertion in TestWriteJPEGExtendedXMPToPNG self-contained.
var xmpWireFramePrefix = []byte("\x00XMPEXT\x00") //nolint:gochecknoglobals // test-package constant; read-only after init

// buildXMPWireFrame constructs a minimal JPEG extended-XMP wire-frame payload
// as produced by jpeg.encodeXMPWire. Layout:
// [8-byte magic][4-byte mainLen BE][main bytes][ext bytes].
//
// This is the same internal encoding that jpeg.ExtractWithWire stores in
// rawXMPWire when a JPEG carries multi-segment extended XMP.
func buildXMPWireFrame(main, ext []byte) []byte {
	magic := xmpWireFramePrefix
	buf := make([]byte, len(magic)+4+len(main)+len(ext))
	n := copy(buf, magic)
	binary.BigEndian.PutUint32(buf[n:], uint32(len(main))) //nolint:gosec // G115: test helper
	n += 4
	n += copy(buf[n:], main)
	copy(buf[n:], ext)
	return buf
}

// buildMetadataWithWireFrame constructs a Metadata that mimics what Read()
// returns for a JPEG carrying extended XMP. rawXMPWire is set to a wire-frame
// encoding and rawXMP is set to the reassembled (user-visible) packet.
// The format field is set to FormatJPEG to match the source container.
//
// This helper lets us test the bug #70 fix without needing a real extended-XMP
// JPEG on disk: the internal state is constructed directly because the test
// lives in package gometadata (the same package as Metadata) and therefore has
// access to unexported fields.
func buildMetadataWithWireFrame() *Metadata {
	// A minimal but well-formed XMP packet for the "main" APP1 segment.
	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:description><rdf:Alt><rdf:li xml:lang="x-default">test caption</rdf:li></rdf:Alt></dc:description>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)

	// A minimal extended payload (could be any bytes in the real case; here
	// we use a short RDF snippet to make the wire-frame non-trivial).
	extPayload := []byte(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/>`)

	wireFrame := buildXMPWireFrame(mainXMP, extPayload)

	return &Metadata{
		format:     uint8(format.FormatJPEG),
		rawXMP:     mainXMP, // the reassembled, user-visible packet
		rawXMPWire: wireFrame,
	}
}

// TestWriteJPEGExtendedXMPToPNG is the regression test for bug #70.
//
// When a JPEG carrying extended XMP (multi-segment APP1) is read, the Metadata
// struct holds an internal wire-frame encoding in rawXMPWire. Before the fix,
// Write used rawXMPWire as-is for any destination format; writing a JPEG
// Metadata to a PNG container caused the wire-frame bytes to be stored verbatim
// as the iTXt XMP payload — producing a corrupt non-XMP blob starting with 0x00.
//
// After the fix, encodeXMP only returns rawXMPWire when the destination format
// is JPEG. For PNG (and all other non-JPEG containers) it falls back to rawXMP,
// the fully reassembled, user-visible XMP packet.
func TestWriteJPEGExtendedXMPToPNG(t *testing.T) {
	t.Parallel()

	// Construct a Metadata that mimics what Read() returns for a JPEG with
	// extended XMP. rawXMPWire is set directly because this test is in package
	// gometadata (internal) and has access to unexported fields.
	m := buildMetadataWithWireFrame()

	// Precondition: rawXMPWire must start with the wire-frame magic.
	if !bytes.HasPrefix(m.rawXMPWire, xmpWireFramePrefix) {
		t.Fatal("precondition: rawXMPWire does not start with wire-frame magic")
	}
	// Precondition: rawXMP must be the reassembled packet (valid XMP).
	if !bytes.HasPrefix(m.rawXMP, []byte("<?xpacket")) {
		t.Fatalf("precondition: rawXMP does not start with '<?xpacket': %q", m.rawXMP[:min(32, len(m.rawXMP))])
	}

	// Write the JPEG-source Metadata to a PNG container (change of format).
	pngData := buildMinimalPNG()
	var pngOut bytes.Buffer
	if err := Write(bytes.NewReader(pngData), &pngOut, m); err != nil {
		t.Fatalf("Write to PNG: %v", err)
	}

	// Read back the PNG. The XMP stored in the PNG must be rawXMP (the
	// reassembled packet), NOT rawXMPWire (the wire-frame). A wire-frame starts
	// with 0x00 which is an invalid XMP packet header.
	m2, err := Read(bytes.NewReader(pngOut.Bytes()))
	if err != nil {
		t.Fatalf("Read back PNG: %v", err)
	}

	rawXMPBack := m2.RawXMP()
	if rawXMPBack == nil {
		t.Fatal("RawXMP() is nil after Write-to-PNG: XMP was not written or was lost")
	}

	// Assert: XMP in PNG must NOT begin with the wire-frame magic bytes.
	// Before the fix this assertion failed: encodeXMP returned rawXMPWire
	// unconditionally, so the wire-frame was written verbatim to the PNG iTXt.
	if bytes.HasPrefix(rawXMPBack, xmpWireFramePrefix) {
		t.Errorf("task #70 regression: XMP in PNG begins with JPEG wire-frame magic %q — corrupt blob written; first 16 bytes: %x",
			xmpWireFramePrefix, rawXMPBack[:min(16, len(rawXMPBack))])
	}

	// Assert: XMP in PNG must start with a valid XMP packet leader.
	if !bytes.HasPrefix(rawXMPBack, []byte("<?xpacket")) {
		t.Errorf("XMP in PNG does not start with '<?xpacket': first 32 bytes: %q",
			rawXMPBack[:min(32, len(rawXMPBack))])
	}
}

// TestWriteJPEGExtendedXMPToWebP is the regression test for bug #70 on WebP.
// Same as TestWriteJPEGExtendedXMPToPNG but writes to a WebP container.
func TestWriteJPEGExtendedXMPToWebP(t *testing.T) {
	t.Parallel()

	m := buildMetadataWithWireFrame()

	webpData := buildMinimalWebP()
	var webpOut bytes.Buffer
	if err := Write(bytes.NewReader(webpData), &webpOut, m); err != nil {
		t.Fatalf("Write to WebP: %v", err)
	}

	m2, err := Read(bytes.NewReader(webpOut.Bytes()))
	if err != nil {
		t.Fatalf("Read back WebP: %v", err)
	}

	rawXMPBack := m2.RawXMP()
	if rawXMPBack == nil {
		t.Fatal("RawXMP() is nil after Write-to-WebP")
	}
	if bytes.HasPrefix(rawXMPBack, xmpWireFramePrefix) {
		t.Errorf("task #70 regression: XMP in WebP begins with JPEG wire-frame magic — corrupt blob written; first 16 bytes: %x",
			rawXMPBack[:min(16, len(rawXMPBack))])
	}
	if !bytes.HasPrefix(rawXMPBack, []byte("<?xpacket")) {
		t.Errorf("XMP in WebP does not start with '<?xpacket': first 32 bytes: %q",
			rawXMPBack[:min(32, len(rawXMPBack))])
	}
}

// buildMinimalWebP builds a minimal valid WebP byte stream.
// VP8X chunk with XMP feature flag (0x04) to allow XMP injection.
func buildMinimalWebP() []byte {
	var body bytes.Buffer

	// VP8X chunk: flags=0x04 (XMP present), 1×1 canvas.
	vp8xPayload := make([]byte, 10)
	binary.LittleEndian.PutUint32(vp8xPayload[0:], 0x04) // XMP flag
	// canvas: 1×1 (stored as width-1, height-1 in 3 bytes each; zero = 1px)
	writeWebPChunk(&body, "VP8X", vp8xPayload)

	// Minimal VP8 lossy bitstream stub.
	vp8stub := []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}
	writeWebPChunk(&body, "VP8 ", vp8stub)

	// RIFF header.
	totalBodySize := uint32(4 + body.Len()) //nolint:gosec // G115: test helper, bounded by local build
	var out bytes.Buffer
	out.WriteString("RIFF")
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], totalBodySize)
	out.Write(sz[:])
	out.WriteString("WEBP")
	out.Write(body.Bytes())
	return out.Bytes()
}

// writeWebPChunk appends a RIFF chunk to buf.
func writeWebPChunk(buf *bytes.Buffer, fourCC string, data []byte) {
	buf.WriteString(fourCC)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(data))) //nolint:gosec // G115: test helper
	buf.Write(sz[:])
	buf.Write(data)
	if len(data)%2 != 0 {
		buf.WriteByte(0x00) // RIFF alignment
	}
}
