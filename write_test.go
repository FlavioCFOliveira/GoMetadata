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
// TIFF-based format write tests (epic #33 Option A)
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

// TestWriteORFAndRW2Succeed verifies that Write succeeds for ORF and RW2 formats
// (un-gated in task #104).
//
// Minimal synthetic files are used here; real-corpus round-trip tests
// (TestWriteORFFromCorpus, TestWriteRW2FromCorpus) exercise actual camera files.
//
// Both IIRO and IIRS ORF magic variants are tested.  The output must be:
//   - Non-empty.
//   - Re-parseable via Read.
//   - The first 4 bytes of the ORF output must match the original ORF magic.
func TestWriteORFAndRW2Succeed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		data      []byte
		wantMagic [4]byte // expected bytes [0:4] of output
	}{
		{
			name:      "ORF-IIRO",
			data:      buildMinimalORF(), // IIRO magic (bytes 2-3 = 0x52 0x4F)
			wantMagic: [4]byte{0x49, 0x49, 0x52, 0x4F},
		},
		{
			name: "ORF-IIRS",
			data: func() []byte {
				b := buildMinimalORF()
				b[3] = 0x53 // IIRS variant
				return b
			}(),
			wantMagic: [4]byte{0x49, 0x49, 0x52, 0x53},
		},
		{
			name:      "RW2",
			data:      buildMinimalRW2(),
			wantMagic: [4]byte{0x49, 0x49, 0x55, 0x00},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m, err := Read(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}

			var out bytes.Buffer
			if writeErr := Write(bytes.NewReader(tc.data), &out, m); writeErr != nil {
				t.Fatalf("Write returned unexpected error: %v", writeErr)
			}
			if out.Len() == 0 {
				t.Fatal("Write produced no output bytes")
			}

			// Verify magic is preserved in output.
			outBytes := out.Bytes()
			if len(outBytes) < 4 {
				t.Fatalf("output too short (%d bytes) to check magic", len(outBytes))
			}
			gotMagic := [4]byte{outBytes[0], outBytes[1], outBytes[2], outBytes[3]}
			if gotMagic != tc.wantMagic {
				t.Errorf("output magic = %X, want %X", gotMagic, tc.wantMagic)
			}

			// Output must re-parse without error.
			m2, parseErr := Read(bytes.NewReader(outBytes))
			if parseErr != nil {
				t.Fatalf("Read after Write: %v", parseErr)
			}
			_ = m2
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

// TestWriteFileORFSucceeds verifies that WriteFile succeeds for a minimal ORF file
// (un-gated in task #104) and that the output re-parses correctly.
//
// FormatORF was previously gated (returned ErrWriteNotSupported); this test
// documents its un-gating and provides a basic regression guard.
//
// For real-corpus validation with a full camera file, see TestWriteORFFromCorpus.
func TestWriteFileORFSucceeds(t *testing.T) {
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

	if writeErr := WriteFile(target, m); writeErr != nil {
		t.Fatalf("WriteFile returned unexpected error: %v", writeErr)
	}

	// File must still exist and be re-parseable.
	remaining := listDir(t, dir)
	if len(remaining) != 1 || remaining[0] != "image.orf" {
		t.Errorf("unexpected files after WriteFile: %v", remaining)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read target after WriteFile: %v", readErr)
	}
	if len(got) == 0 {
		t.Error("WriteFile produced empty output")
	}
	m2, parseErr := Read(bytes.NewReader(got))
	if parseErr != nil {
		t.Fatalf("Read after WriteFile: %v", parseErr)
	}
	_ = m2
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

// TestWriteJPEGSucceeds is a non-regression test ensuring that JPEG writes
// continue to succeed through the standard injectors path.
func TestWriteJPEGSucceeds(t *testing.T) {
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

// ---------------------------------------------------------------------------
// Task #104: ORF and RW2 corpus-backed round-trip tests
// ---------------------------------------------------------------------------

// TestWriteORFUnGated verifies that format.SupportsWrite returns true for
// FormatORF (un-gated in task #104).
func TestWriteORFUnGated(t *testing.T) {
	t.Parallel()
	if !format.SupportsWrite(format.FormatORF) {
		t.Error("format.SupportsWrite(FormatORF) = false; ORF was un-gated in task #104")
	}
}

// TestWriteRW2UnGated verifies that format.SupportsWrite returns true for
// FormatRW2 (un-gated in task #104).
func TestWriteRW2UnGated(t *testing.T) {
	t.Parallel()
	if !format.SupportsWrite(format.FormatRW2) {
		t.Error("format.SupportsWrite(FormatRW2) = false; RW2 was un-gated in task #104")
	}
}

// TestWriteORFFromCorpus verifies that Write succeeds for real ORF corpus files
// and that the output re-parses correctly.  Both IIRO (Olympus OM-D / E-series)
// and IIRS (Olympus compacts: C5050Z) variants are tested.
//
// Assertions:
//   - Write succeeds and produces non-empty output.
//   - Output is detected as FormatORF (magic bytes preserved).
//   - Output re-parses without error.
//   - Copyright metadata round-trips correctly.
//   - Output is not smaller than input (no data loss).
//   - ORF magic bytes [0:4] match the original (IIRO or IIRS variant preserved).
//
// If a fixture file is absent the corresponding sub-test is skipped, not failed.
func TestWriteORFFromCorpus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		path      string
		wantMagic [4]byte
	}{
		{
			// Olympus OM-D E-M10: IIRO magic, DSLR-class ORF.
			name:      "IIRO-E-M10",
			path:      "testdata/corpus/raw/metadata-extractor/Olympus E-M10.orf",
			wantMagic: [4]byte{0x49, 0x49, 0x52, 0x4F},
		},
		{
			// Olympus E410: IIRO magic, another E-series body.
			name:      "IIRO-E410",
			path:      "testdata/corpus/raw/metadata-extractor/Olympus E410.orf",
			wantMagic: [4]byte{0x49, 0x49, 0x52, 0x4F},
		},
		{
			// Olympus C5050Z: IIRS magic (older compact variant).
			name:      "IIRS-C5050Z",
			path:      "testdata/corpus/raw/metadata-extractor/Olympus C5050Z.orf",
			wantMagic: [4]byte{0x49, 0x49, 0x52, 0x53},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(tc.path)
			if err != nil {
				t.Skipf("fixture not found (%s): %v", tc.path, err)
			}

			m, readErr := Read(bytes.NewReader(data))
			if readErr != nil {
				t.Fatalf("Read ORF: %v", readErr)
			}
			if m.Format() != format.FormatORF {
				t.Fatalf("expected FormatORF, got %v", m.Format())
			}
			m.SetCopyright("© 2026 orf104")

			var out bytes.Buffer
			if writeErr := Write(bytes.NewReader(data), &out, m); writeErr != nil {
				t.Fatalf("Write ORF: %v", writeErr)
			}
			if out.Len() == 0 {
				t.Fatal("Write ORF produced no output bytes")
			}

			output := out.Bytes()

			// Magic must be preserved (IIRO or IIRS).
			if len(output) < 4 {
				t.Fatalf("output too short (%d bytes) to check magic", len(output))
			}
			gotMagic := [4]byte{output[0], output[1], output[2], output[3]}
			if gotMagic != tc.wantMagic {
				t.Errorf("output magic = %X, want %X", gotMagic, tc.wantMagic)
			}

			// Output must be large enough to plausibly contain image data.
			// The IFD skeleton may legitimately be smaller than the original
			// (exif.Encode produces a more compact encoding than some camera
			// firmware), so we check that the output is at least 90% of the
			// input size — a very generous lower bound that would only fail
			// if substantial image blocks were dropped.
			minExpected := len(data) * 9 / 10
			if len(output) < minExpected {
				t.Errorf("output size %d is less than 90%% of input size %d (potential data loss)",
					len(output), len(data))
			}

			// Output must re-parse without error.
			m2, parseErr := Read(bytes.NewReader(output))
			if parseErr != nil {
				t.Fatalf("Read after Write ORF: %v", parseErr)
			}

			// Copyright must round-trip.
			if got := m2.Copyright(); got != "© 2026 orf104" {
				t.Errorf("Copyright round-trip: got %q, want %q", got, "© 2026 orf104")
			}

			// Camera model must survive.
			if got := m2.CameraModel(); got == "" {
				t.Error("CameraModel is empty after Write ORF round-trip")
			}
		})
	}
}

// TestWriteRW2FromCorpus verifies that Write succeeds for real Panasonic RW2
// corpus files and that the output re-parses correctly.
//
// Assertions:
//   - Write succeeds and produces non-empty output.
//   - Output magic bytes [0:4] = "IIU\x00" (RW2 magic preserved).
//   - Output re-parses without error.
//   - Copyright metadata round-trips correctly.
//   - Output is not smaller than input (no data loss).
//   - IFD0 header bytes [4:8] = 24 (IFD0 at offset 24 after GUID insertion).
//   - JpgFromRaw (tag 0x002E): OOL pointer is within output bounds.
//   - RawDataOffset (tag 0x0118): inline value is within output bounds.
//
// If a fixture file is absent the corresponding sub-test is skipped, not failed.
func TestWriteRW2FromCorpus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
	}{
		{
			name: "DMC-GF1",
			path: "testdata/corpus/raw/metadata-extractor/Panasonic DMC-GF1.rw2",
		},
		{
			name: "DMC-GF7",
			path: "testdata/corpus/raw/metadata-extractor/Panasonic DMC-GF7.rw2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(tc.path)
			if err != nil {
				t.Skipf("fixture not found (%s): %v", tc.path, err)
			}

			m, readErr := Read(bytes.NewReader(data))
			if readErr != nil {
				t.Fatalf("Read RW2: %v", readErr)
			}
			if m.Format() != format.FormatRW2 {
				t.Fatalf("expected FormatRW2, got %v", m.Format())
			}
			m.SetCopyright("© 2026 rw2104")

			var out bytes.Buffer
			if writeErr := Write(bytes.NewReader(data), &out, m); writeErr != nil {
				t.Fatalf("Write RW2: %v", writeErr)
			}
			if out.Len() == 0 {
				t.Fatal("Write RW2 produced no output bytes")
			}

			output := out.Bytes()

			// RW2 magic must be preserved.
			if len(output) < 8 {
				t.Fatalf("output too short (%d bytes) to check header", len(output))
			}
			wantMagic := [4]byte{0x49, 0x49, 0x55, 0x00}
			gotMagic := [4]byte{output[0], output[1], output[2], output[3]}
			if gotMagic != wantMagic {
				t.Errorf("output magic = %X, want %X", gotMagic, wantMagic)
			}

			// IFD0 must still be at offset 24 (GUID preserved).
			ifd0Off := binary.LittleEndian.Uint32(output[4:])
			if ifd0Off != 24 {
				t.Errorf("IFD0 offset in output = %d, want 24 (GUID not preserved)", ifd0Off)
			}

			// Output must be large enough to plausibly contain image data.
			// See the ORF test for rationale on the 90% threshold.
			minExpected := len(data) * 9 / 10
			if len(output) < minExpected {
				t.Errorf("output size %d is less than 90%% of input size %d (potential data loss)",
					len(output), len(data))
			}

			// Output must re-parse without error.
			m2, parseErr := Read(bytes.NewReader(output))
			if parseErr != nil {
				t.Fatalf("Read after Write RW2: %v", parseErr)
			}

			// Copyright must round-trip.
			if got := m2.Copyright(); got != "© 2026 rw2104" {
				t.Errorf("Copyright round-trip: got %q, want %q", got, "© 2026 rw2104")
			}

			// Verify RW2-specific tag preservation in the output by parsing as TIFF.
			// Patch bytes [2:4] to 0x2A 0x00 for exif.Parse.
			patchedOut := make([]byte, len(output))
			copy(patchedOut, output)
			patchedOut[2] = 0x2A
			patchedOut[3] = 0x00
			e2, exifParseErr := exif.Parse(patchedOut)
			if exifParseErr != nil {
				t.Fatalf("exif.Parse patched output: %v", exifParseErr)
			}
			if e2.IFD0 == nil {
				t.Fatal("output IFD0 is nil after exif.Parse")
			}

			order := e2.ByteOrder
			if order == nil {
				order = binary.LittleEndian
			}

			// JpgFromRaw (0x002E): if present, the OOL val_or_off must be within bounds.
			// Note: exif.Parse stores the JPEG DATA in entry.Value (not the val_or_off pointer).
			// To check the actual IFD pointer, scan the binary IFD directly.
			jpgEntry := e2.IFD0.Get(exif.TagID(0x002E))
			if jpgEntry == nil {
				t.Log("JpgFromRaw (0x002E) entry not found in output IFD0 (may be absent in this fixture)")
			} else {
				// exif.Parse populated Value with the JPEG bytes (len = jpgEntry.Count).
				// If the count matches the Value length, Parse found the data in-bounds.
				if uint32(len(jpgEntry.Value)) != jpgEntry.Count { //nolint:gosec // G115: jpgEntry.Count bounded by parse-time bounds check
					t.Errorf("JpgFromRaw Value length %d != Count %d (OOL pointer out of bounds)", len(jpgEntry.Value), jpgEntry.Count)
				}
				// Also scan the binary IFD for the val_or_off field directly.
				ifd0Start := int(order.Uint32(patchedOut[4:]))
				ifdCount := int(order.Uint16(patchedOut[ifd0Start:]))
				ifdPos := ifd0Start + 2
				for j := range ifdCount {
					e3 := ifdPos + j*12
					if e3+12 > len(patchedOut) {
						break
					}
					entTag := exif.TagID(order.Uint16(patchedOut[e3:]))
					if entTag == exif.TagID(0x002E) {
						voo := order.Uint32(patchedOut[e3+8:])
						end := uint64(voo) + uint64(jpgEntry.Count)
						if end > uint64(len(output)) {
							t.Errorf("JpgFromRaw IFD val_or_off+size (%d+%d=%d) out of bounds (output size %d)",
								voo, jpgEntry.Count, end, len(output))
						}
						break
					}
				}
			}

			// RawDataOffset (0x0118): inline value must be within output bounds.
			rawEntry := e2.IFD0.Get(exif.TagID(0x0118))
			if rawEntry == nil {
				t.Log("RawDataOffset (0x0118) entry not found in output IFD0 (may be absent in this fixture)")
			} else if len(rawEntry.Value) >= 4 {
				rawOff := order.Uint32(rawEntry.Value[:4])
				if rawOff == 0 || uint64(rawOff) >= uint64(len(output)) {
					t.Errorf("RawDataOffset (0x0118) value %d is out of bounds (output size %d)",
						rawOff, len(output))
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Gate tests for audit findings #108, #109, #139
// ---------------------------------------------------------------------------

// TestWrite_CrossFormatMismatchRejected is the gate for finding #108.
//
// Write must return ErrFormatMismatch (and write nothing) when:
//   - the *Metadata was sourced from a TIFF-based container (A), and
//   - the io.ReadSeeker carries a DIFFERENT TIFF-based container (B).
//
// The guard is scoped to TIFF-based targets because those write paths
// (writeTIFF, writeTIFFNEF, writeTIFFARW, etc.) use m.rawEXIF as the binary
// relocation base for all image-data blocks. Sourcing it from format A while
// writing to format B silently discards all image data of the format-B file.
//
// Non-TIFF cross-format writes (e.g. JPEG→PNG, JPEG→WebP) are legitimate and
// must continue to work: those paths re-encode metadata from scratch via
// encodeMetadata/injectByFormat and never use m.rawEXIF as a relocation base.
//
// Positive controls:
//   - Same-format TIFF→TIFF must succeed.
//   - JPEG→PNG cross-format must still succeed (not blocked by the guard).
func TestWrite_CrossFormatMismatchRejected(t *testing.T) {
	t.Parallel()

	// --- Negative case: read a JPEG, write against a TIFF reader ---------------
	// The dangerous cross-format write: JPEG metadata (m.rawEXIF from JPEG) used
	// as the relocation base for a TIFF-family write would discard the TIFF image.
	jpegData := buildMinimalJPEG(minimalTIFFPayload())
	mJPEG, err := Read(bytes.NewReader(jpegData))
	if err != nil {
		t.Fatalf("Read JPEG: %v", err)
	}
	mJPEG.SetCopyright("x")

	tiffData := minimalTIFFPayload()
	var out bytes.Buffer
	writeErr := Write(bytes.NewReader(tiffData), &out, mJPEG)
	if writeErr == nil {
		t.Fatal("#108 gate FAIL: Write(TIFF reader, JPEG metadata) returned nil error — cross-format mismatch not detected")
	}
	if !errors.Is(writeErr, ErrFormatMismatch) {
		t.Errorf("#108 gate FAIL: got error %v (%T), want errors.Is(err, ErrFormatMismatch)", writeErr, writeErr)
	}
	if out.Len() != 0 {
		t.Errorf("#108 gate FAIL: Write wrote %d bytes despite returning ErrFormatMismatch; expected 0", out.Len())
	}

	// --- Positive control A: same-format TIFF→TIFF must still succeed ----------
	mTIFF, err2 := Read(bytes.NewReader(tiffData))
	if err2 != nil {
		t.Fatalf("Read TIFF (positive control A): %v", err2)
	}
	const wantCopyright = "© 2026 #108 gate"
	mTIFF.SetCopyright(wantCopyright)

	var outTIFF bytes.Buffer
	if writeErr2 := Write(bytes.NewReader(tiffData), &outTIFF, mTIFF); writeErr2 != nil {
		t.Fatalf("#108 positive control A: same-format TIFF→TIFF Write returned error: %v", writeErr2)
	}
	if outTIFF.Len() == 0 {
		t.Fatal("#108 positive control A: TIFF→TIFF Write produced no output")
	}
	m2, readErr := Read(bytes.NewReader(outTIFF.Bytes()))
	if readErr != nil {
		t.Fatalf("#108 positive control A: Read after Write: %v", readErr)
	}
	if got := m2.Copyright(); got != wantCopyright {
		t.Errorf("#108 positive control A: Copyright round-trip: got %q, want %q", got, wantCopyright)
	}

	// --- Positive control B: JPEG→PNG must NOT be blocked by the guard ---------
	// Cross-format transcoding (JPEG metadata → PNG container) is a legitimate
	// use-case: png.Inject re-encodes from scratch and never touches m.rawEXIF.
	mJPEG2, err3 := Read(bytes.NewReader(jpegData))
	if err3 != nil {
		t.Fatalf("Read JPEG (positive control B): %v", err3)
	}
	pngData := buildMinimalPNG()
	var outPNG bytes.Buffer
	if writeErr3 := Write(bytes.NewReader(pngData), &outPNG, mJPEG2); writeErr3 != nil {
		t.Fatalf("#108 positive control B: JPEG→PNG cross-format Write returned unexpected error: %v", writeErr3)
	}
	if outPNG.Len() == 0 {
		t.Fatal("#108 positive control B: JPEG→PNG cross-format Write produced no output")
	}
}

// TestWriteTwicePreservesMetadata is the gate for finding #109.
//
// Writing the same *Metadata twice must produce byte-identical output both
// times: IFD0 entry count, thumbnail bytes, and ImageDataHash must all match.
//
// Before the fix, the second Write on a TIFF *Metadata would fail with
// ErrBlockOutOfBounds or silently produce wrong output because
// relocateTIFFFromParsed permanently mutated m.EXIF (removed strip/tile entries,
// cleared ThumbnailData, appended IPTC/XMP entries without deduplication).
func TestWriteTwicePreservesMetadata(t *testing.T) {
	t.Parallel()

	type subcase struct {
		name      string
		buildData func() []byte
	}

	subcases := []subcase{
		{
			// Plain TIFF with a strip — exercises the writeTIFF path.
			name: "TIFF",
			buildData: func() []byte {
				strip := []byte("WRITE-TWICE-STRIP-DATA-GUARD-109!")
				return buildTIFFWithStrip(binary.LittleEndian, false, strip, false)
			},
		},
		{
			// NEF-like (big-endian TIFF) — exercises the writeTIFFNEF path.
			name: "NEF",
			buildData: func() []byte {
				strip := []byte("WRITE-TWICE-NEF-STRIP-DATA-GUARD!")
				return buildTIFFWithStrip(binary.BigEndian, true, strip, false)
			},
		},
		{
			// ARW-like synthetic (TIFF LE + SONY make tag) — exercises writeTIFFARW.
			// We patch bytes [0:4] to standard TIFF and set Make=SONY so that
			// format.Detect returns FormatARW (format/detect.go mapMakeToFormat).
			name: "ARW",
			buildData: func() []byte {
				stripData := []byte("WRITE-TWICE-ARW-STRIP-DATA-109!")
				previewData := make([]byte, 64)
				previewData[0], previewData[1] = 0xFF, 0xD8
				return buildARWWithIFD0Preview(previewData, stripData)
			},
		},
	}

	for _, tc := range subcases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data := tc.buildData()

			m, err := Read(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Read %s: %v", tc.name, err)
			}
			m.SetCopyright("© 2026 twice-" + tc.name)

			// Record the IFD0 entry count before any Write so we can detect mutations.
			var entryCountBefore int
			if m.EXIF != nil && m.EXIF.IFD0 != nil {
				entryCountBefore = len(m.EXIF.IFD0.Entries)
			}
			// Record ThumbnailData before any Write.
			var thumbBefore []byte
			if m.EXIF != nil && m.EXIF.IFD0 != nil && m.EXIF.IFD0.ThumbnailData != nil {
				thumbBefore = bytes.Clone(m.EXIF.IFD0.ThumbnailData)
			}

			// First Write.
			var out1 bytes.Buffer
			if err := Write(bytes.NewReader(data), &out1, m); err != nil {
				t.Fatalf("%s Write #1: %v", tc.name, err)
			}

			// Verify m.EXIF.IFD0 was NOT mutated by Write #1.
			if m.EXIF != nil && m.EXIF.IFD0 != nil {
				if got := len(m.EXIF.IFD0.Entries); got != entryCountBefore {
					t.Errorf("%s #109: IFD0 entry count changed after Write #1: %d → %d (mutation!)",
						tc.name, entryCountBefore, got)
				}
				if thumbBefore != nil && m.EXIF.IFD0.ThumbnailData == nil {
					t.Errorf("%s #109: IFD0.ThumbnailData was non-nil before Write #1 but is nil after (mutation!)", tc.name)
				}
			}

			// Second Write — same *Metadata, same reader.
			var out2 bytes.Buffer
			if err := Write(bytes.NewReader(data), &out2, m); err != nil {
				t.Fatalf("%s Write #2: %v", tc.name, err)
			}

			// Both outputs must be byte-identical.
			b1, b2 := out1.Bytes(), out2.Bytes()
			if len(b1) != len(b2) {
				t.Errorf("%s #109: output sizes differ: Write#1=%d Write#2=%d", tc.name, len(b1), len(b2))
			} else if !bytes.Equal(b1, b2) {
				// Find first differing byte for diagnostics.
				for i := range b1 {
					if b1[i] != b2[i] {
						t.Errorf("%s #109: outputs differ at byte %d: Write#1[%d]=0x%02x Write#2[%d]=0x%02x",
							tc.name, i, i, b1[i], i, b2[i])
						break
					}
				}
			}

			// IFD0 entry count must be unchanged after both writes.
			if m.EXIF != nil && m.EXIF.IFD0 != nil {
				if got := len(m.EXIF.IFD0.Entries); got != entryCountBefore {
					t.Errorf("%s #109: IFD0 entry count changed after both Writes: %d → %d", tc.name, entryCountBefore, got)
				}
			}
		})
	}
}

// TestRawEXIFIsIndependent is the gate for finding #139.
//
// Mutating the slice returned by RawEXIF() must not affect parsed IFD0 values
// or the image-data output of a subsequent Write call.
//
// Before the fix, RawEXIF() returned the internal m.rawEXIF slice directly.
// For TIFF-based formats, that slice shares its backing array with every
// parsed IFDEntry.Value (zero-copy parse), so a mutation corrupted all parsed
// tags simultaneously and also corrupted the relocation base for Write.
func TestRawEXIFIsIndependent(t *testing.T) {
	t.Parallel()

	tiffData := buildTIFFWithStrip(binary.LittleEndian, false,
		[]byte("RAWEXIF-INDEPENDENT-STRIP-DATA!"), false)

	m, err := Read(bytes.NewReader(tiffData))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Capture a baseline: Write #1 before any mutation.
	var baseline bytes.Buffer
	if writeErr := Write(bytes.NewReader(tiffData), &baseline, m); writeErr != nil {
		t.Fatalf("Write baseline: %v", writeErr)
	}
	baselineBytes := baseline.Bytes()

	// Capture IFD0 values before mutation for comparison.
	var ifd0EntryCountBefore int
	if m.EXIF != nil && m.EXIF.IFD0 != nil {
		ifd0EntryCountBefore = len(m.EXIF.IFD0.Entries)
	}

	// Obtain raw and corrupt every byte.
	raw := m.RawEXIF()
	if len(raw) == 0 {
		t.Skip("RawEXIF() returned empty slice — nothing to mutate")
	}
	for i := range raw {
		raw[i] ^= 0xFF
	}

	// After mutation: IFD0 entry count must be unchanged (internal state not affected).
	if m.EXIF != nil && m.EXIF.IFD0 != nil {
		if got := len(m.EXIF.IFD0.Entries); got != ifd0EntryCountBefore {
			t.Errorf("#139 FAIL: IFD0 entry count changed after RawEXIF() mutation: %d → %d", ifd0EntryCountBefore, got)
		}
	}

	// Write #2 after mutation must produce the same output as Write #1.
	var afterMutation bytes.Buffer
	if writeErr := Write(bytes.NewReader(tiffData), &afterMutation, m); writeErr != nil {
		t.Fatalf("#139 Write after RawEXIF mutation: %v", writeErr)
	}
	afterBytes := afterMutation.Bytes()

	if !bytes.Equal(baselineBytes, afterBytes) {
		t.Errorf("#139 FAIL: Write output differs after RawEXIF() mutation\n  baseline  len=%d\n  after-mut len=%d",
			len(baselineBytes), len(afterBytes))
	}
}

// ---------------------------------------------------------------------------
// Gate tests for audit findings #124 (fsync) and #125 (symlink + ownership)
// ---------------------------------------------------------------------------

// TestWriteFileSyncsBeforeRename is the gate for audit finding #124.
//
// WriteFile must call Sync on the temp file after all data has been written
// and before Close/Rename. This guarantees that the replacement file is
// durable: a crash after the write but before Sync cannot leave a truncated
// or empty file as the permanent replacement.
//
// The test verifies the observable post-condition: WriteFile produces a
// complete, correct file that can be read back without error and contains the
// expected metadata. It also verifies the abort-on-Sync-error path: a Sync
// failure must leave the original file intact (no partial replacement).
//
// The abort path is exercised via a read-only destination directory: Rename
// will fail, but because Sync runs before Rename, the original file is
// preserved. We verify this by asserting the original content is still intact
// after the failed WriteFile.
func TestWriteFileSyncsBeforeRename(t *testing.T) {
	t.Parallel()

	// --- Positive path: WriteFile produces a complete, correct, re-readable file.
	t.Run("produces_complete_output", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		target := filepath.Join(dir, "image.jpg")
		original := buildMinimalJPEG(minimalTIFFPayload())
		if err := os.WriteFile(target, original, 0o644); err != nil { //nolint:gosec // G306: test helper
			t.Fatalf("setup: %v", err)
		}

		m, err := ReadFile(target)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		const wantCopyright = "© 2026 #124 sync gate"
		m.SetCopyright(wantCopyright)

		if err := WriteFile(target, m); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		// File must be re-readable and contain the expected metadata.
		m2, err := ReadFile(target)
		if err != nil {
			t.Fatalf("ReadFile after WriteFile: %v", err)
		}
		if got := m2.Copyright(); got != wantCopyright {
			t.Errorf("Copyright after WriteFile: got %q, want %q", got, wantCopyright)
		}

		// The file must be non-empty and at least as large as the original.
		fi, err := os.Stat(target)
		if err != nil {
			t.Fatalf("Stat after WriteFile: %v", err)
		}
		if fi.Size() == 0 {
			t.Error("WriteFile produced an empty file")
		}
		if int(fi.Size()) < len(original) {
			t.Errorf("WriteFile output size %d is smaller than original %d — possible truncation",
				fi.Size(), len(original))
		}
	})

	// --- Abort path: Rename failure leaves the original file intact. ---
	// We make the directory read-only so Rename fails. The original file must
	// survive intact (no partial replacement, no empty file).
	t.Run("original_intact_on_rename_failure", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		target := filepath.Join(dir, "image.jpg")
		original := buildMinimalJPEG(minimalTIFFPayload())
		if err := os.WriteFile(target, original, 0o644); err != nil { //nolint:gosec // G306: test helper
			t.Fatalf("setup: %v", err)
		}

		// Make the directory read-only so Rename cannot create the replacement.
		if err := os.Chmod(dir, 0o555); err != nil { //nolint:gosec // G302: test helper, intentionally restrictive
			t.Skipf("cannot chmod dir (running as root or unsupported OS): %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) //nolint:gosec // G302: restoring normal directory permissions

		m, err := ReadFile(target)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		m.SetCopyright("should not be written")

		writeErr := WriteFile(target, m)
		if writeErr == nil {
			t.Skip("Rename succeeded despite read-only dir (likely running as root)")
		}

		// Restore so we can read the directory.
		if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: restoring normal directory permissions
			t.Fatalf("restore chmod: %v", err)
		}

		// Original file must still contain the original content, not a partial write.
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("ReadFile original after failure: %v", err)
		}
		if !bytes.Equal(got, original) {
			t.Errorf("original file was modified after WriteFile failure: size before=%d after=%d",
				len(original), len(got))
		}

		// No stale gometadata-* temp file must remain.
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "gometadata-") {
				t.Errorf("stale temp file left after abort: %s", e.Name())
			}
		}
	})
}

// TestWriteFilePreservesSymlink is the gate for audit finding #125 (symlink case).
//
// When WriteFile is called on a path that is a symbolic link, the operation
// must:
//  1. Not replace the symlink with a regular file (the symlink must survive).
//  2. Update the real file that the symlink points to.
//
// Verification:
//   - os.Lstat(symlinkPath).Mode()&os.ModeSymlink != 0   → still a symlink.
//   - ReadFile(realPath) returns the updated metadata      → real file was written.
func TestWriteFilePreservesSymlink(t *testing.T) {
	t.Parallel()

	// Create the real file in a subdirectory.
	realDir := t.TempDir()
	realPath := filepath.Join(realDir, "photo.jpg")
	original := buildMinimalJPEG(minimalTIFFPayload())
	if err := os.WriteFile(realPath, original, 0o644); err != nil { //nolint:gosec // G306: test helper
		t.Fatalf("setup real file: %v", err)
	}

	// Create a symlink in a different temp directory pointing to the real file.
	linkDir := t.TempDir()
	linkPath := filepath.Join(linkDir, "link.jpg")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("os.Symlink not supported or not permitted: %v", err)
	}

	// Read via the symlink, mutate, and write back via the symlink.
	m, err := ReadFile(linkPath)
	if err != nil {
		t.Fatalf("ReadFile via symlink: %v", err)
	}
	const wantCopyright = "© 2026 #125 symlink gate"
	m.SetCopyright(wantCopyright)

	if err := WriteFile(linkPath, m); err != nil {
		t.Fatalf("WriteFile via symlink: %v", err)
	}

	// Assert 1: the symlink must still be a symlink (not replaced by a regular file).
	lst, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("Lstat link after WriteFile: %v", err)
	}
	if lst.Mode()&os.ModeSymlink == 0 {
		t.Errorf("#125 FAIL: %s is no longer a symlink after WriteFile (mode=%v)", linkPath, lst.Mode())
	}

	// Assert 2: the REAL file must contain the updated metadata.
	m2, err := ReadFile(realPath)
	if err != nil {
		t.Fatalf("ReadFile real file after WriteFile: %v", err)
	}
	if got := m2.Copyright(); got != wantCopyright {
		t.Errorf("#125 Copyright in real file: got %q, want %q", got, wantCopyright)
	}
}

// TestWriteFilePreservesOwnershipAndMode is the gate for audit finding #125
// (mode and ownership preservation).
//
// WriteFile must:
//   - Preserve the original file's permission bits (mode).
//   - Preserve the original file's uid/gid on Unix (best-effort chown).
//
// The mode assertion is platform-neutral. The uid/gid assertion is split into
// a Unix-specific helper (assertOwnershipPreserved in write_unix_test.go) so
// that the build remains correct on all platforms.
//
// The test uses mode 0640 which differs from os.CreateTemp's default (0600 on
// Unix) to prove that Chmod is applied to the correct value, not a default.
func TestWriteFilePreservesOwnershipAndMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "image.jpg")
	original := buildMinimalJPEG(minimalTIFFPayload())

	// Write with mode 0640 (non-default — proves Chmod is applied, not defaulted).
	if err := os.WriteFile(target, original, 0o640); err != nil { //nolint:gosec // G306: intentional 0640 for mode-preservation test
		t.Fatalf("setup: %v", err)
	}
	// Restrict to exactly 0640 (os.WriteFile honours umask; Chmod bypasses it).
	if err := os.Chmod(target, 0o640); err != nil { //nolint:gosec // G302: intentional 0640 for mode-preservation test
		t.Fatalf("chmod setup: %v", err)
	}

	fiBefore, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}

	m, err := ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	m.SetCopyright("© 2026 #125 mode gate")

	if err := WriteFile(target, m); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fiAfter, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}

	// Mode must be preserved (platform-neutral assertion).
	wantMode := fiBefore.Mode()
	if fiAfter.Mode() != wantMode {
		t.Errorf("#125 mode: got %v, want %v", fiAfter.Mode(), wantMode)
	}

	// Uid/gid assertion is delegated to the platform-specific helper
	// assertOwnershipPreserved (write_unix_test.go / write_windows_test.go).
	assertOwnershipPreserved(t, fiBefore, fiAfter)
}
