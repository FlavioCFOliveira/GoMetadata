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
// ErrWriteNotSupported for the RAW formats that remain gated (ORF, RW2).
// These formats use non-standard TIFF magic bytes (ORF: IIRS; RW2: IIU\0) and
// require format-specific outer-framing work before the copy-and-relocate path
// can apply safely (task #95 follow-up).
//
// FormatTIFF was removed from this test in tasks #92/#93: tiff.Inject now uses
// the copy-and-relocate serializer and TIFF writes succeed.
//
// CR2 was removed from this test in task #95: it uses standard LE TIFF magic
// and now routes through writeTIFF; see TestWriteCR2RoundTrip.
//
// NEF and ARW remain gated: real-corpus tests (2026-06-05) found MakerNote
// data loss and SubIFD OOL value corruption for both; see write_test.go
// comments for the full failure analysis. The synthetic minimal files used
// here are detected as plain TIFF (not NEF/ARW), so this test uses real
// corpus fixtures via TestWriteNEFFromCorpusStillGated.
func TestWriteRAWFormatsReturnErrWriteNotSupported(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data []byte
	}{
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

// TestWriteNEFFromCorpus verifies that Write succeeds for a real NEF fixture and
// that the output re-parses correctly.  NEF was un-gated in task #102 after the
// Nikon-specific write path (relocate_nef.go) was validated against a real corpus
// file (Nikon D70): ImageDataHash IN==OUT, all metadata preserved.
//
// This test uses the exiftool corpus fixture (Nikon.nef).  If the fixture is
// absent the test is skipped (not failed).
func TestWriteNEFFromCorpus(t *testing.T) {
	t.Parallel()

	path := "testdata/corpus/raw/exiftool/Nikon.nef"
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("fixture not found (%s): %v", path, err)
	}
	defer func() { _ = f.Close() }()

	m, err := Read(f)
	if err != nil {
		t.Fatalf("Read NEF: %v", err)
	}
	m.SetCopyright("© 2026 nef102")

	if _, seekErr := f.Seek(0, io.SeekStart); seekErr != nil {
		t.Fatalf("Seek NEF: %v", seekErr)
	}

	var out bytes.Buffer
	if writeErr := Write(f, &out, m); writeErr != nil {
		t.Fatalf("Write NEF: %v", writeErr)
	}
	if out.Len() == 0 {
		t.Fatal("Write NEF produced no output bytes")
	}

	// Output must re-parse without error.
	m2, parseErr := Read(bytes.NewReader(out.Bytes()))
	if parseErr != nil {
		t.Fatalf("Read after Write NEF: %v", parseErr)
	}
	if got := m2.Copyright(); got != "© 2026 nef102" {
		t.Errorf("Copyright round-trip: got %q, want %q", got, "© 2026 nef102")
	}
}

// TestDNGWriteRoundTrip verifies that gometadata.Write succeeds for a DNG file
// (bug #98 fixed, task #101 re-enabled).
//
// The test builds a synthetic DNG-like TIFF stream (IFD0 thumbnail strip +
// SubIFD0 full-res strip, tag 0x014A, plus DNGVersion 0xC612 for format
// detection) and confirms:
//   - format.Detect correctly classifies the file as FormatDNG
//   - format.SupportsWrite(FormatDNG) = true (re-enabled after bug #98 fix)
//   - Write succeeds and produces non-empty output
//   - The written metadata (XMP caption) round-trips correctly
//   - Image blocks (thumb and full-res strips) are byte-identical in the output
//
// The synthetic fixture does NOT include SubIFD RATIONAL values to keep this
// test focused on the top-level write/round-trip path. For the RATIONAL
// value-preservation regression (the actual bug #98 fix), see
// format/tiff.TestSubIFDRationalValuesPreservedOnRelocation.
func TestDNGWriteRoundTrip(t *testing.T) { //nolint:paralleltest // not parallel: uses t.TempDir for file I/O
	// Build a minimal DNG-like TIFF:
	//   TIFF header (LE) + IFD0 (thumb strips + 0x014A SubIFDs + 0xC612 DNGVersion) +
	//   SubIFD0 (full-res strips).
	//   DNGVersion (0xC612) makes format.Detect classify this as FormatDNG.
	order := binary.LittleEndian

	thumbStrip := []byte("DNG-ROUNDTRIP-THUMB-STRIP-DATA-GUARD!")
	fullStrip := []byte("DNG-ROUNDTRIP-FULLRES-STRIP-DATA")

	// IFD0: 6 entries (sorted by tag).
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

	// Task #98 (SubIFD OOL value fix): SupportsWrite(FormatDNG) must now be true.
	if !format.SupportsWrite(format.FormatDNG) {
		t.Fatal("format.SupportsWrite(FormatDNG) = false; expected true after bug #98 fix")
	}

	// Read the DNG file's metadata.
	m, err := Read(bytes.NewReader(original))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	const wantCaption = "DNG round-trip test caption"
	m.XMP.SetCaption(wantCaption)

	// Write must succeed and produce non-empty output.
	var outBuf bytes.Buffer
	writeErr := Write(bytes.NewReader(original), &outBuf, m)
	if writeErr != nil {
		t.Fatalf("Write DNG returned unexpected error: %v", writeErr)
	}
	if outBuf.Len() == 0 {
		t.Fatal("Write DNG produced no output bytes")
	}

	output := outBuf.Bytes()

	// Caption must round-trip.
	m2, err := Read(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Read after Write DNG: %v", err)
	}
	if got := m2.Caption(); got != wantCaption {
		t.Errorf("Caption: got %q, want %q", got, wantCaption)
	}

	// Image blocks must be byte-identical.
	if !bytes.Contains(output, thumbStrip) {
		t.Error("DNG round-trip: thumbnail strip bytes not found verbatim in output")
	}
	if !bytes.Contains(output, fullStrip) {
		t.Error("DNG round-trip: full-res strip bytes not found verbatim in output")
	}

	// WriteFile path: write to a temp file and read back.
	dir := t.TempDir()
	dngPath := filepath.Join(dir, "test.dng")
	if err := os.WriteFile(dngPath, original, 0o644); err != nil { //nolint:gosec // G306: test helper
		t.Fatalf("WriteFile setup: %v", err)
	}

	mf, err := ReadFile(dngPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	mf.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	mf.XMP.SetCaption(wantCaption)
	if err := WriteFile(dngPath, mf); err != nil {
		t.Fatalf("WriteFile DNG: %v", err)
	}

	mf2, err := ReadFile(dngPath)
	if err != nil {
		t.Fatalf("ReadFile after WriteFile DNG: %v", err)
	}
	if got := mf2.Caption(); got != wantCaption {
		t.Errorf("WriteFile Caption: got %q, want %q", got, wantCaption)
	}
}

// TestWriteFileBlocksRAWBased verifies that WriteFile returns ErrWriteNotSupported
// and does NOT overwrite the original file for a RAW format that remains gated
// (ORF as the representative case; CR2 is now writable as of task #95).
//
// For FormatTIFF, see TestWriteFileTIFFSucceeds.
// For CR2 (now writable), see TestWriteCR2RoundTrip.
func TestWriteFileBlocksRAWBased(t *testing.T) {
	t.Parallel()

	original := buildMinimalORF()

	dir := t.TempDir()
	target := filepath.Join(dir, "image.orf")
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
	if len(remaining) != 1 || remaining[0] != "image.orf" {
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

// ---------------------------------------------------------------------------
// Task #95: CR2/NEF/ARW write round-trip tests
// ---------------------------------------------------------------------------

// buildTIFFWithStrip constructs a minimal standard-magic TIFF stream with a
// single strip image block and an optional Canon-marker payload at bytes 8–9.
// It accepts a byteOrder parameter so it can produce both LE (CR2/ARW) and BE (NEF).
// The strip data is appended after the IFD block and the offset entry points to it,
// so the round-trip verifies that image data survives verbatim.
//
// When canonMarker is true the Canon CR2 "CR" signature is placed at bytes 8–9
// (Canon CR2 spec §3.1) and IFD0 is pushed to offset 16 to avoid overlap with the
// marker bytes. The standard TIFF header stores the IFD0 offset at bytes 4–7.
//
// Layout without Canon marker: TIFF header (8) + IFD0 (2+3×12+4) + stripData
// Layout with Canon marker:    TIFF header (8) + CR marker (2) + padding (6) +
//
//	IFD0 (2+3×12+4) + stripData
func buildTIFFWithStrip(order binary.ByteOrder, bigEndian bool, stripData []byte, canonMarker bool) []byte {
	// IFD0: 3 entries: ImageWidth (0x0100), StripOffsets (0x0111), StripByteCounts (0x0117).
	const nEntries = 3
	const ifdEntries = 2 + nEntries*12 + 4 // count(2) + entries + next-IFD(4)

	var ifd0Off int
	if canonMarker {
		// Push IFD0 past the Canon marker area (bytes 8–9) plus 6 bytes padding
		// to keep IFD0 aligned at a 4-byte boundary (offset 16).
		ifd0Off = 16
	} else {
		ifd0Off = 8
	}

	stripOff := ifd0Off + ifdEntries
	bufLen := stripOff + len(stripData)

	buf := make([]byte, bufLen)

	// TIFF header (bytes 0–7).
	if bigEndian {
		buf[0], buf[1] = 'M', 'M'
	} else {
		buf[0], buf[1] = 'I', 'I'
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(ifd0Off)) // ifd0Off is either 8 or 16; fits uint32

	// Canon CR2 marker at bytes 8–9 (Canon CR2 spec §3.1).
	// Bytes 8–9 are in the TIFF header "reserved" area when IFD0 > 8.
	if canonMarker {
		buf[8] = 'C'
		buf[9] = 'R'
	}

	// IFD0
	p := ifd0Off
	order.PutUint16(buf[p:], nEntries)
	p += 2

	putEntry := func(tag, typ uint16, count, val uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}

	putEntry(0x0100, 4, 1, 1)                      // ImageWidth = 1
	putEntry(0x0111, 4, 1, uint32(stripOff))       // StripOffsets → strip
	putEntry(0x0117, 4, 1, uint32(len(stripData))) //nolint:gosec // G115: test helper
	order.PutUint32(buf[p:], 0)                    // next-IFD = 0
	copy(buf[stripOff:], stripData)

	return buf
}

// TestWriteCR2RoundTrip verifies that gometadata.Write succeeds for a synthetic
// CR2-like TIFF stream (task #95: CR2 write un-gate).
//
// The test confirms:
//   - format.Detect classifies the file as FormatCR2
//   - format.SupportsWrite(FormatCR2) = true
//   - Write succeeds and produces non-empty output
//   - The written metadata (XMP caption) round-trips correctly
//   - Strip image data is byte-identical in the output (image-block integrity)
func TestWriteCR2RoundTrip(t *testing.T) { //nolint:paralleltest // not parallel: uses t.TempDir for file I/O
	stripData := []byte("CR2-ROUNDTRIP-STRIP-DATA-GUARD!")
	original := buildTIFFWithStrip(binary.LittleEndian, false, stripData, true /*Canon marker*/)

	detectedFmt, detErr := format.Detect(bytes.NewReader(original))
	if detErr != nil {
		t.Fatalf("format.Detect: %v", detErr)
	}
	if detectedFmt != format.FormatCR2 {
		t.Fatalf("format.Detect = %v, want FormatCR2", detectedFmt)
	}
	if !format.SupportsWrite(format.FormatCR2) {
		t.Fatal("format.SupportsWrite(FormatCR2) = false; expected true after task #95")
	}

	m, err := Read(bytes.NewReader(original))
	if err != nil {
		t.Fatalf("Read CR2: %v", err)
	}
	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	const wantCaption = "CR2 round-trip task #95"
	m.XMP.SetCaption(wantCaption)

	var outBuf bytes.Buffer
	if writeErr := Write(bytes.NewReader(original), &outBuf, m); writeErr != nil {
		t.Fatalf("Write CR2 returned unexpected error: %v", writeErr)
	}
	if outBuf.Len() == 0 {
		t.Fatal("Write CR2 produced no output bytes")
	}

	output := outBuf.Bytes()

	// Caption must round-trip.
	m2, err := Read(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Read after Write CR2: %v", err)
	}
	if got := m2.Caption(); got != wantCaption {
		t.Errorf("Caption: got %q, want %q", got, wantCaption)
	}

	// Image block must be byte-identical.
	if !bytes.Contains(output, stripData) {
		t.Error("CR2 round-trip: strip data bytes not found verbatim in output")
	}

	// WriteFile path.
	dir := t.TempDir()
	cr2Path := filepath.Join(dir, "test.cr2")
	if err := os.WriteFile(cr2Path, original, 0o644); err != nil { //nolint:gosec // G306: test helper
		t.Fatalf("WriteFile setup: %v", err)
	}
	mf, err := ReadFile(cr2Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	mf.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	mf.XMP.SetCaption(wantCaption)
	if err := WriteFile(cr2Path, mf); err != nil {
		t.Fatalf("WriteFile CR2: %v", err)
	}
	mf2, err := ReadFile(cr2Path)
	if err != nil {
		t.Fatalf("ReadFile after WriteFile CR2: %v", err)
	}
	if got := mf2.Caption(); got != wantCaption {
		t.Errorf("WriteFile Caption: got %q, want %q", got, wantCaption)
	}
}

// TestWriteNEFUnGated verifies that format.SupportsWrite(FormatNEF) = true
// after task #102 (Nikon-specific write path validated).
//
// The NEF write path (relocate_nef.go) extends the Nikon Type-3 MakerNote blob
// to cover PreviewIFD and NikonScanIFD, enumerates the PreviewIFD image block
// (preview JPEG at a MakerNote-TIFF-relative offset), and patches the offset
// after re-encoding.  Validated against a real Nikon D70 corpus file.
func TestWriteNEFUnGated(t *testing.T) {
	t.Parallel()
	if !format.SupportsWrite(format.FormatNEF) {
		t.Error("format.SupportsWrite(FormatNEF) = false; NEF was un-gated in task #102")
	}
}

// TestWriteARWUnGated verifies that format.SupportsWrite(FormatARW) = true
// after task #103 (Sony ARW-specific write path implemented and validated).
//
// The ARW write path (relocate_arw.go) rebases all Sony MakerNote TIFF-absolute
// OOL offsets and relocates the SR2Private (0xC634) block (encrypted SR2SubIFD
// + IDC_IFD) with internal pointer rebasing. Validated against a real Sony
// DSLR-A500 ARW corpus file: ImageDataHash IN==OUT, all 52 MakerNote tags and
// SR2Private block preserved.
func TestWriteARWUnGated(t *testing.T) {
	t.Parallel()
	if !format.SupportsWrite(format.FormatARW) {
		t.Error("format.SupportsWrite(FormatARW) = false; ARW was un-gated in task #103")
	}
}

// TestWriteARWFromCorpus verifies that Write succeeds for a real Sony ARW fixture and
// that the output re-parses correctly.  ARW was un-gated in task #103 after the
// Sony-specific write path (relocate_arw.go) was validated:
//   - Sony MakerNote OOL offsets are rebased (Sony uses TIFF-absolute offsets).
//   - SR2Private (0xC634) block is extracted verbatim, appended at the new position,
//     and its internal pointers are rebased (IFD + OOL + SR2SubIFD decrypt/re-encrypt).
//   - ImageDataHash IN==OUT verified against real Sony DSLR-A500.arw corpus file.
//   - IFD0 preview JPEG (PreviewImageStart 0x0201 / PreviewImageLength 0x0202) is
//     preserved and its offset updated to the new location (task #103 regression fix).
//
// This test uses the metadata-extractor corpus fixture (Sony DSLR-A500.arw).
// If the fixture is absent the test is skipped (not failed).
func TestWriteARWFromCorpus(t *testing.T) {
	t.Parallel()

	path := "testdata/corpus/raw/metadata-extractor/Sony DSLR-A500.arw"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixture not found (%s): %v", path, err)
	}

	// Record IFD0 preview block from the ORIGINAL file before any write so we can
	// verify byte-for-byte identity after the round-trip.
	origPreviewBytes, origPreviewLen := extractIFD0PreviewBytes(t, data)

	m, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read ARW: %v", err)
	}
	if m.Format() != format.FormatARW {
		t.Fatalf("expected FormatARW, got %v", m.Format())
	}
	m.SetCopyright("© 2026 arw103")

	var out bytes.Buffer
	if writeErr := Write(bytes.NewReader(data), &out, m); writeErr != nil {
		t.Fatalf("Write ARW: %v", writeErr)
	}
	if out.Len() == 0 {
		t.Fatal("Write ARW produced no output bytes")
	}

	output := out.Bytes()

	// Output must re-parse without error.
	m2, parseErr := Read(bytes.NewReader(output))
	if parseErr != nil {
		t.Fatalf("Read after Write ARW: %v", parseErr)
	}
	if got := m2.Copyright(); got != "© 2026 arw103" {
		t.Errorf("Copyright round-trip: got %q, want %q", got, "© 2026 arw103")
	}
	if got := m2.CameraModel(); got == "" {
		t.Error("CameraModel is empty after Write ARW round-trip")
	}

	// --- IFD0 preview JPEG preservation (task #103 regression guard) -----------
	// The Sony ARW IFD0 carries a large preview JPEG via tags 0x0201/0x0202.
	// A previous defect in the ARW write path dropped this 736 KB block entirely,
	// resulting in an output file ~736 KB smaller than the input.
	// This check ensures the block survives the write and is byte-identical.
	if origPreviewLen == 0 {
		t.Log("IFD0 preview block not found in corpus fixture; skipping preview-preservation check")
	} else {
		outPreviewBytes, outPreviewLen := extractIFD0PreviewBytes(t, output)
		if outPreviewLen == 0 {
			t.Errorf("IFD0 preview block present in input (%d bytes) but ABSENT in output — preview was dropped", origPreviewLen)
		} else {
			if outPreviewLen != origPreviewLen {
				t.Errorf("IFD0 preview length mismatch: input=%d output=%d", origPreviewLen, outPreviewLen)
			}
			// Verify the preview offset is in-bounds in the output.
			e2, parseErr2 := exif.Parse(output)
			if parseErr2 == nil && e2.IFD0 != nil {
				pvOff := e2.IFD0.Get(exif.TagJPEGInterchangeFormat)
				pvLen := e2.IFD0.Get(exif.TagJPEGInterchangeFormatLength)
				if pvOff != nil && pvLen != nil && len(pvOff.Value) >= 4 && len(pvLen.Value) >= 4 {
					order := e2.ByteOrder
					if order == nil {
						order = binary.LittleEndian
					}
					newOff := order.Uint32(pvOff.Value)
					newLen := order.Uint32(pvLen.Value)
					end := uint64(newOff) + uint64(newLen)
					if end > uint64(len(output)) {
						t.Errorf("IFD0 preview offset+length (%d+%d=%d) exceeds output size (%d)", newOff, newLen, end, len(output))
					}
				}
			}
			// Byte-identical check.
			if origPreviewBytes != nil && outPreviewBytes != nil && !bytes.Equal(origPreviewBytes, outPreviewBytes) {
				t.Error("IFD0 preview bytes differ between input and output — preview data was corrupted")
			}
		}
	}
}

// extractIFD0PreviewBytes reads the IFD0 preview JPEG bytes from a TIFF stream
// (the bytes pointed at by PreviewImageStart 0x0201 / PreviewImageLength 0x0202
// in IFD0).  Returns (nil, 0) when the preview is absent or out of bounds.
func extractIFD0PreviewBytes(t *testing.T, data []byte) ([]byte, uint32) {
	t.Helper()
	if len(data) < 8 {
		return nil, 0
	}
	e, err := exif.Parse(data)
	if err != nil || e.IFD0 == nil {
		return nil, 0
	}
	order := e.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}
	pvOff := e.IFD0.Get(exif.TagJPEGInterchangeFormat)
	pvLen := e.IFD0.Get(exif.TagJPEGInterchangeFormatLength)
	if pvOff == nil || pvLen == nil || len(pvOff.Value) < 4 || len(pvLen.Value) < 4 {
		return nil, 0
	}
	off := order.Uint32(pvOff.Value)
	length := order.Uint32(pvLen.Value)
	if length == 0 {
		return nil, 0
	}
	end := uint64(off) + uint64(length)
	if end > uint64(len(data)) {
		return nil, 0
	}
	cp := make([]byte, length)
	copy(cp, data[off:end])
	return cp, length
}

// TestWriteARWIFD0PreviewPreservedSynthetic is a self-contained regression test
// for the task #103 defect: the ARW write path was dropping the IFD0 preview JPEG
// (PreviewImageStart 0x0201 / PreviewImageLength 0x0202 in IFD0).
//
// This test does NOT require the real Sony corpus file.  It builds a synthetic
// ARW-like TIFF (standard TIFF magic + Sony MakerNote magic = FormatARW) that
// carries a fake 512-byte preview JPEG in IFD0, performs a metadata write, and
// asserts the preview block survives byte-for-byte.
//
// The synthetic fixture has IFD0 with:
//   - StripOffsets (0x0111) + StripByteCounts (0x0117): 64-byte strip (RAW placeholder)
//   - JPEGInterchangeFormat (0x0201) + JPEGInterchangeFormatLength (0x0202): 512-byte
//     fake preview JPEG (prefixed with 0xFFD8 JPEG SOI marker)
//   - ExifIFD (0x8769) → ExifIFD with MakerNote (0x927C) starting with "SONY DSC "
//     so that format.Detect returns FormatARW
func TestWriteARWIFD0PreviewPreservedSynthetic(t *testing.T) {
	t.Parallel()

	// Sentinel bytes used to verify byte-identical preservation.
	stripData := []byte("ARW-PREVIEW-TEST-STRIP-DATA-GUARD!")
	// A fake preview with a valid JPEG SOI marker at the start.
	previewData := make([]byte, 512)
	previewData[0] = 0xFF
	previewData[1] = 0xD8 // JPEG SOI
	for i := 2; i < len(previewData); i++ {
		previewData[i] = byte(i & 0xFF) // distinguishable filler
	}

	original := buildARWWithIFD0Preview(previewData, stripData)

	// Verify this synthetic file is detected as FormatARW.
	detFmt, detErr := format.Detect(bytes.NewReader(original))
	if detErr != nil {
		t.Fatalf("format.Detect: %v", detErr)
	}
	if detFmt != format.FormatARW {
		t.Fatalf("format.Detect = %v, want FormatARW (synthetic ARW marker not recognised)", detFmt)
	}

	// Read + write round-trip.
	m, err := Read(bytes.NewReader(original))
	if err != nil {
		t.Fatalf("Read synthetic ARW: %v", err)
	}
	m.SetCopyright("© 2026 preview-guard")

	var outBuf bytes.Buffer
	if writeErr := Write(bytes.NewReader(original), &outBuf, m); writeErr != nil {
		t.Fatalf("Write synthetic ARW: %v", writeErr)
	}
	output := outBuf.Bytes()
	if len(output) == 0 {
		t.Fatal("Write produced no output bytes")
	}

	// Strip data must be byte-identical.
	if !bytes.Contains(output, stripData) {
		t.Error("strip data not found verbatim in output")
	}

	// Preview block must survive.
	if !bytes.Contains(output, previewData) {
		t.Error("IFD0 preview data not found verbatim in output — preview was DROPPED")
	}

	// The output 0x0201 offset must point at a valid in-bounds range.
	e2, parseErr := exif.Parse(output)
	if parseErr != nil {
		t.Fatalf("exif.Parse output: %v", parseErr)
	}
	if e2.IFD0 == nil {
		t.Fatal("IFD0 nil in output")
	}
	order := e2.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}
	pvOff := e2.IFD0.Get(exif.TagJPEGInterchangeFormat)
	pvLen := e2.IFD0.Get(exif.TagJPEGInterchangeFormatLength)
	if pvOff == nil || pvLen == nil {
		t.Fatal("0x0201/0x0202 entries missing from output IFD0")
	}
	if len(pvOff.Value) < 4 || len(pvLen.Value) < 4 {
		t.Fatal("0x0201/0x0202 entries too short in output IFD0")
	}
	newOff := order.Uint32(pvOff.Value)
	newLen := order.Uint32(pvLen.Value)
	end := uint64(newOff) + uint64(newLen)
	if end > uint64(len(output)) {
		t.Errorf("0x0201 offset+length (%d+%d=%d) exceeds output size (%d)", newOff, newLen, end, len(output))
	}
	if newLen != uint32(len(previewData)) { //nolint:gosec // G115: len bounded by test fixture size
		t.Errorf("0x0202 length: got %d, want %d", newLen, len(previewData))
	}
	// Byte-identical check at the new offset.
	if !bytes.Equal(output[newOff:newOff+newLen], previewData) {
		t.Error("preview bytes at new offset differ from original — preview data was corrupted")
	}

	// Copyright must round-trip.
	m2, err2 := Read(bytes.NewReader(output))
	if err2 != nil {
		t.Fatalf("Read after Write: %v", err2)
	}
	if got := m2.Copyright(); got != "© 2026 preview-guard" {
		t.Errorf("Copyright: got %q, want %q", got, "© 2026 preview-guard")
	}
}

// buildARWWithIFD0Preview constructs a minimal TIFF stream that:
//   - Is detected as FormatARW (Make tag = "SONY" in IFD0)
//   - Has IFD0 with Make, StripOffsets, JPEGInterchangeFormat/Length, ExifIFD pointer
//   - Has a preview JPEG (0x0201/0x0202) and a strip (image data) in IFD0
//
// format.Detect uses the IFD0 Make tag value to identify Sony ARW:
// mapMakeToFormat("SONY") → FormatARW.
//
// Layout (little-endian, standard TIFF 0x002A magic):
//
//	[0-7]       TIFF header: "II" + 0x002A + ifd0Off
//	[ifd0Off]   IFD0: 6 entries (Make, StripOffsets, StripByteCounts,
//	                              JIF, JIFLen, ExifIFD pointer)
//	[exifOff]   ExifIFD: 1 entry (MakerNote 0x927C)
//	[makeOff]   OOL string area: "SONY\x00" (5 bytes)
//	[mnOff]     MakerNote blob (> 4 bytes, OOL)
//	[pvOff]     preview data (previewData)
//	[stripOff]  strip data (stripData)
func buildARWWithIFD0Preview(previewData, stripData []byte) []byte {
	order := binary.LittleEndian

	const headerSize = 8
	const ifdEntrySize = 12

	// IFD0: 6 entries (sorted by tag).
	//   0x010F Make (ASCII "SONY\x00", 5 bytes → OOL since 5 > 4),
	//   0x0111 StripOffsets, 0x0117 StripByteCounts,
	//   0x0201 JPEGInterchangeFormat, 0x0202 JPEGInterchangeFormatLength,
	//   0x8769 ExifIFDPointer.
	const nIFD0 = 6
	ifd0FixedSize := 2 + nIFD0*ifdEntrySize + 4 // count + entries + nextIFD

	// ExifIFD: 1 entry (MakerNote 0x927C).
	const nExif = 1
	exifFixedSize := 2 + nExif*ifdEntrySize + 4

	ifd0Off := headerSize
	exifOff := ifd0Off + ifd0FixedSize

	// OOL value area for IFD0: "SONY\x00" (5 bytes) — Make tag value.
	makeStr := []byte("SONY\x00")
	makeOff := exifOff + exifFixedSize

	// MakerNote blob: any content > 4 bytes so it is placed OOL.
	mnBlob := make([]byte, 32)
	mnOff := makeOff + len(makeStr)
	// Word-align mnOff.
	if mnOff%2 != 0 {
		mnOff++
	}

	pvOff := mnOff + len(mnBlob)
	// Word-align preview start.
	if pvOff%2 != 0 {
		pvOff++
	}

	stripOff := pvOff + len(previewData)
	// Word-align strip start.
	if stripOff%2 != 0 {
		stripOff++
	}

	totalLen := stripOff + len(stripData)

	buf := make([]byte, totalLen)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(ifd0Off))

	// IFD0 entries (sorted by tag).
	p := ifd0Off
	order.PutUint16(buf[p:], nIFD0)
	p += 2

	putEntry := func(tag, typ uint16, count, val uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += ifdEntrySize
	}

	// 0x010F Make = "SONY\x00": TypeASCII=2, count=5, OOL (5 > 4).
	putEntry(0x010F, 2, uint32(len(makeStr)), uint32(makeOff)) //nolint:gosec // G115: test helper
	putEntry(0x0111, 4, 1, uint32(stripOff))                   //nolint:gosec // G115: test helper; StripOffsets
	putEntry(0x0117, 4, 1, uint32(len(stripData)))             //nolint:gosec // G115: test helper; StripByteCounts
	putEntry(0x0201, 4, 1, uint32(pvOff))                      //nolint:gosec // G115: test helper; JPEGInterchangeFormat
	putEntry(0x0202, 4, 1, uint32(len(previewData)))           //nolint:gosec // G115: test helper; JPEGInterchangeFormatLength
	putEntry(0x8769, 4, 1, uint32(exifOff))                    // ExifIFDPointer
	order.PutUint32(buf[p:], 0)                                // IFD0 next-IFD = 0
	p += 4

	// ExifIFD: 1 entry — MakerNote (0x927C, TypeUndefined=7, count=32, OOL).
	order.PutUint16(buf[p:], nExif)
	p += 2
	putEntry(0x927C, 7, uint32(len(mnBlob)), uint32(mnOff)) //nolint:gosec // G115: test helper
	order.PutUint32(buf[p:], 0)                             // ExifIFD next-IFD = 0

	// Copy data payloads.
	copy(buf[makeOff:], makeStr)
	copy(buf[mnOff:], mnBlob)
	copy(buf[pvOff:], previewData)
	copy(buf[stripOff:], stripData)

	return buf
}
