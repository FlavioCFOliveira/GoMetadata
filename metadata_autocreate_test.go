package gometadata

// Regression tests for task #88 — AUTO-CREATE policy in convenience setters.
//
// Before the fix, calling SetKeywords/SetCaption/SetGPS on a *Metadata whose
// target component(s) were nil was a silent no-op: Write produced an unchanged
// file with no error, and a subsequent Read returned no value.
//
// After the fix, the setter auto-creates the component (when the detected
// format can carry it), so Write persists the value and a re-Read returns it.
//
// Each test follows the canonical round-trip pattern:
//  1. Build a minimal valid container (JPEG or PNG) in memory.
//  2. Read it — this creates a *Metadata whose target components are nil.
//  3. Call the setter.
//  4. Write to an in-memory buffer.
//  5. Re-Read from that buffer.
//  6. Assert the value is present.

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// Minimal container builders (internal package — no import of roundtrip helpers)
// ---------------------------------------------------------------------------

// acBuildJPEGNoEXIF returns a minimal JPEG with no EXIF segment.
// The only APP segment is the JFIF APP0 marker so Read detects FormatJPEG.
func acBuildJPEGNoEXIF() []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	// JFIF APP0 marker so the container is a valid JPEG without any metadata.
	app0 := []byte{
		'J', 'F', 'I', 'F', 0x00, // identifier
		0x01, 0x01, // version
		0x00,                   // aspect-ratio units = none
		0x00, 0x01, 0x00, 0x01, // Xdensity, Ydensity
		0x00, 0x00, // no thumbnail
	}
	length := uint16(len(app0) + 2) //nolint:gosec // G115: test helper, length fits uint16
	buf.Write([]byte{0xFF, 0xE0})
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	buf.Write(lb[:])
	buf.Write(app0)

	// Minimal SOS + EOI.
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// acBuildJPEGWithEXIFOnly wraps a TIFF payload in an EXIF APP1 segment.
// The resulting JPEG has EXIF but no IPTC and no XMP.
func acBuildJPEGWithEXIFOnly() []byte {
	tiff := minimalTIFFPayload() // defined in read_test.go (same package)
	return buildMinimalJPEG(tiff)
}

// acWritePNGChunk appends a PNG chunk with correct CRC to buf.
func acWritePNGChunk(buf *bytes.Buffer, chunkType string, data []byte) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data))) //nolint:gosec // G115: test helper
	buf.Write(hdr[:])
	buf.WriteString(chunkType)
	buf.Write(data)
	h := crc32.NewIEEE()
	_, _ = h.Write([]byte(chunkType))
	_, _ = h.Write(data)
	binary.BigEndian.PutUint32(hdr[:], h.Sum32())
	buf.Write(hdr[:])
}

// acBuildMinimalPNG returns a minimal PNG with no metadata segments.
func acBuildMinimalPNG() []byte {
	sig := [8]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	var buf bytes.Buffer
	buf.Write(sig[:])

	// Minimal IHDR: 1×1 RGB8.
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 1) // width
	binary.BigEndian.PutUint32(ihdr[4:], 1) // height
	ihdr[8] = 8                             // bit depth
	ihdr[9] = 2                             // colour type RGB
	acWritePNGChunk(&buf, "IHDR", ihdr)
	acWritePNGChunk(&buf, "IEND", nil)
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// TestSetKeywordsAutoCreatesComponent — JPEG has only EXIF; no IPTC, no XMP.
// After SetKeywords the value must survive a Write→Read round-trip.
// ---------------------------------------------------------------------------

func TestSetKeywordsAutoCreatesComponent(t *testing.T) {
	t.Parallel()

	img := acBuildJPEGWithEXIFOnly()

	// Step 1: read — IPTC and XMP are both nil.
	m, err := Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read initial JPEG: %v", err)
	}
	if m.IPTC != nil {
		t.Fatal("pre-condition: IPTC should be nil in EXIF-only JPEG")
	}
	if m.XMP != nil {
		t.Fatal("pre-condition: XMP should be nil in EXIF-only JPEG")
	}

	// Step 2: call the setter — previously a silent no-op.
	want := []string{"travel", "street"}
	m.SetKeywords(want)

	// Step 3: after the setter both components must now be non-nil (JPEG supports both).
	if m.IPTC == nil {
		t.Error("IPTC should be auto-created by SetKeywords on JPEG format")
	}
	if m.XMP == nil {
		t.Error("XMP should be auto-created by SetKeywords on JPEG format")
	}

	// Step 4: write.
	var out bytes.Buffer
	if err := Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Step 5: re-read.
	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}

	// Step 6: assert keywords persisted.
	got := m2.Keywords()
	if len(got) != len(want) {
		t.Fatalf("Keywords after round-trip: got %v (len %d), want %v (len %d)",
			got, len(got), want, len(want))
	}
	for i, kw := range want {
		if got[i] != kw {
			t.Errorf("Keywords[%d]: got %q, want %q", i, got[i], kw)
		}
	}
}

// TestSetKeywordsXMPOnlyWhenIPTCNotSupported verifies that SetKeywords on a
// PNG *Metadata (which does not support IPTC) only auto-creates XMP, and that
// the value survives a Write→Read round-trip via the XMP pathway.
func TestSetKeywordsXMPOnlyWhenIPTCNotSupported(t *testing.T) {
	t.Parallel()

	img := acBuildMinimalPNG()

	m, err := Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read PNG: %v", err)
	}
	if m.IPTC != nil {
		t.Fatal("pre-condition: IPTC should be nil in PNG")
	}
	if m.XMP != nil {
		t.Fatal("pre-condition: XMP should be nil in minimal PNG")
	}

	want := []string{"landscape"}
	m.SetKeywords(want)

	// PNG cannot carry IPTC — ensureIPTC must NOT create it.
	if m.IPTC != nil {
		t.Error("SetKeywords on PNG must NOT auto-create IPTC")
	}
	// PNG can carry XMP — it must be created.
	if m.XMP == nil {
		t.Error("SetKeywords on PNG should auto-create XMP")
	}

	// Write → re-Read.
	var out bytes.Buffer
	if err := Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write PNG: %v", err)
	}
	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read PNG after Write: %v", err)
	}
	got := m2.Keywords()
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Keywords from PNG round-trip: got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// TestSetCaptionAutoCreatesComponent — JPEG with no metadata at all.
// After SetCaption the value must survive a Write→Read round-trip.
// ---------------------------------------------------------------------------

func TestSetCaptionAutoCreatesComponent(t *testing.T) {
	t.Parallel()

	// Use a JPEG with no EXIF so all three components start nil.
	img := acBuildJPEGNoEXIF()

	m, err := Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read JPEG no-EXIF: %v", err)
	}
	if m.EXIF != nil || m.IPTC != nil || m.XMP != nil {
		t.Fatalf("pre-condition: all components should be nil; got EXIF=%v IPTC=%v XMP=%v",
			m.EXIF, m.IPTC, m.XMP)
	}

	const want = "golden hour over the bay"
	m.SetCaption(want)

	// All three components must have been auto-created for JPEG.
	if m.EXIF == nil {
		t.Error("EXIF should be auto-created by SetCaption on JPEG")
	}
	if m.IPTC == nil {
		t.Error("IPTC should be auto-created by SetCaption on JPEG")
	}
	if m.XMP == nil {
		t.Error("XMP should be auto-created by SetCaption on JPEG")
	}

	var out bytes.Buffer
	if err := Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	if got := m2.Caption(); got != want {
		t.Errorf("Caption after round-trip: got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// TestSetGPSAutoCreatesEXIF — JPEG has no EXIF at all.
// After SetGPS the coordinates must survive a Write→Read round-trip via EXIF.
// ---------------------------------------------------------------------------

func TestSetGPSAutoCreatesEXIF(t *testing.T) {
	t.Parallel()

	img := acBuildJPEGNoEXIF()

	m, err := Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read JPEG no-EXIF: %v", err)
	}
	if m.EXIF != nil {
		t.Fatal("pre-condition: EXIF should be nil in no-EXIF JPEG")
	}

	const wantLat, wantLon = 48.8566, 2.3522 // Paris

	m.SetGPS(wantLat, wantLon)

	if m.EXIF == nil {
		t.Fatal("EXIF should be auto-created by SetGPS on JPEG")
	}
	if m.EXIF.IFD0 == nil {
		t.Fatal("auto-created EXIF must have non-nil IFD0")
	}

	var out bytes.Buffer
	if err := Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	lat, lon, ok := m2.GPS()
	if !ok {
		t.Fatal("GPS() ok=false after round-trip — value was not persisted")
	}
	if math.Abs(lat-wantLat) > 0.001 {
		t.Errorf("GPS lat after round-trip: got %f, want ~%f", lat, wantLat)
	}
	if math.Abs(lon-wantLon) > 0.001 {
		t.Errorf("GPS lon after round-trip: got %f, want ~%f", lon, wantLon)
	}
}

// ---------------------------------------------------------------------------
// TestSettersOnUnsupportedFormat — no panic, graceful behaviour when the
// detected format cannot carry the target component(s). FormatUnknown is
// used as a representative unsupported format.
// ---------------------------------------------------------------------------

func TestSettersOnUnsupportedFormat(t *testing.T) {
	t.Parallel()

	// A *Metadata with FormatUnknown simulates reading an unsupported container:
	// none of the ensure* helpers should create any component.
	m := NewMetadata(0) // 0 == FormatUnknown

	// None of these must panic.
	m.SetCaption("test")
	m.SetCopyright("(c) 2024")
	m.SetCreator("Alice")
	m.SetKeywords([]string{"a", "b"})
	m.SetGPS(1.0, 2.0)
	m.SetCameraModel("Model X")
	m.SetLensModel("50mm f/1.4")
	m.SetMake("Acme")
	m.SetISO(100)
	m.SetFocalLength(50.0)

	// All components must still be nil — nothing was created.
	if m.EXIF != nil {
		t.Error("EXIF should not be auto-created for FormatUnknown")
	}
	if m.IPTC != nil {
		t.Error("IPTC should not be auto-created for FormatUnknown")
	}
	if m.XMP != nil {
		t.Error("XMP should not be auto-created for FormatUnknown")
	}
}

// ---------------------------------------------------------------------------
// TestAutoCreatePreservesExistingComponents — when a component is already
// non-nil, auto-create must NOT replace it (no regression for the existing
// set-into-existing-component behaviour).
// ---------------------------------------------------------------------------

func TestAutoCreatePreservesExistingComponents(t *testing.T) {
	t.Parallel()

	// Build a Metadata that already has all three components populated.
	m := newTestMetadata(t) // defined in metadata_test.go (same package)
	origEXIF := m.EXIF
	origIPTC := m.IPTC
	origXMP := m.XMP

	// Call SetCaption — the pre-existing components must not be replaced.
	m.SetCaption("unchanged")

	if m.EXIF != origEXIF {
		t.Error("SetCaption must not replace a pre-existing EXIF component")
	}
	if m.IPTC != origIPTC {
		t.Error("SetCaption must not replace a pre-existing IPTC component")
	}
	if m.XMP != origXMP {
		t.Error("SetCaption must not replace a pre-existing XMP component")
	}

	// Value must have been written correctly.
	if got := m.Caption(); got != "unchanged" {
		t.Errorf("Caption() = %q, want %q", got, "unchanged")
	}
}
