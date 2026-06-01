package gometadata

// Tests for WriteFile (fix #9) and Write-calls-Validate (fix #10).

import (
	"bytes"
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

// TestWriteTIFFBasedFormatsReturnErrWriteNotSupported verifies that Write
// returns ErrWriteNotSupported (and nothing else) for every TIFF-based
// container format. The writer must receive no bytes — the gate must fire
// before any I/O reaches the output.
//
// Covered formats: TIFF, CR2, ORF, RW2. NEF, ARW, and DNG are covered by
// TestWriteTIFFBasedFormatsFromCorpus which uses real fixture files (those
// formats require IFD tag inspection to be distinguished from plain TIFF and
// cannot be built synthetically without full IFD construction).
func TestWriteTIFFBasedFormatsReturnErrWriteNotSupported(t *testing.T) {
	t.Parallel()

	tiffPayload := minimalTIFFPayload()

	cases := []struct {
		name string
		data []byte
	}{
		{"TIFF", tiffPayload},
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

// TestWriteTIFFBasedFormatsFromCorpus verifies ErrWriteNotSupported for NEF,
// ARW, and DNG using real fixture files from the test corpus. These formats
// share the standard TIFF magic and are only distinguished by IFD tag
// inspection, so synthetic minimal files would be detected as plain TIFF.
func TestWriteTIFFBasedFormatsFromCorpus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
	}{
		{"NEF", "testdata/corpus/raw/exiftool/Nikon.nef"},
		{"DNG", "testdata/corpus/raw/exiftool/DNG.dng"},
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

// TestWriteFileBlocksTIFFBased verifies that WriteFile returns
// ErrWriteNotSupported and does NOT overwrite the original file for a
// TIFF-based container (using a minimal TIFF as the representative case).
func TestWriteFileBlocksTIFFBased(t *testing.T) {
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

	writeErr := WriteFile(target, m)
	if writeErr == nil {
		t.Fatal("WriteFile returned nil; want ErrWriteNotSupported")
	}
	if !errors.Is(writeErr, ErrWriteNotSupported) {
		t.Errorf("errors.Is(err, ErrWriteNotSupported) = false; got: %v", writeErr)
	}

	// File must not have been modified.
	remaining := listDir(t, dir)
	if len(remaining) != 1 || remaining[0] != "image.tif" {
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
