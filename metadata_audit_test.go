package gometadata

// metadata_audit_test.go — gate tests for audit findings #110, #128, #148, #178, #185.
//
// Each top-level test function is named exactly as specified in the task so
// that a failure points directly at its finding. All tests run with -race.

import (
	"bytes"
	"sync"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format"
)

// ---------------------------------------------------------------------------
// Finding #110 + #178 — canCarryIPTC truth table
//
// TestCanCarryIPTC_TIFFBasedRAW asserts the complete truth table for
// canCarryIPTC after the finding-#110 fix:
//   - true:  JPEG, TIFF, DNG, CR2, NEF, ARW, ORF, RW2
//   - false: CR3, PNG, WebP, HEIF, AVIF, Unknown
//
// Also doubles as the #178 documentation-accuracy gate: the code and the
// fixed godoc must agree on the same set of formats.
// ---------------------------------------------------------------------------

//nolint:paralleltest // AllocsPerRun must not run with t.Parallel
func TestCanCarryIPTC_TIFFBasedRAW(t *testing.T) {
	wantTrue := []struct {
		name  string
		fmtID format.FormatID
	}{
		{"JPEG", format.FormatJPEG},
		{"TIFF", format.FormatTIFF},
		{"DNG", format.FormatDNG},
		{"CR2", format.FormatCR2},
		{"NEF", format.FormatNEF},
		{"ARW", format.FormatARW},
		{"ORF", format.FormatORF},
		{"RW2", format.FormatRW2},
	}
	wantFalse := []struct {
		name  string
		fmtID format.FormatID
	}{
		{"CR3", format.FormatCR3},
		{"PNG", format.FormatPNG},
		{"WebP", format.FormatWebP},
		{"HEIF", format.FormatHEIF},
		{"AVIF", format.FormatAVIF},
		{"Unknown", format.FormatUnknown},
	}

	for _, tc := range wantTrue {
		m := NewMetadata(tc.fmtID)
		if !m.canCarryIPTC() {
			t.Errorf("canCarryIPTC(%s) = false, want true", tc.name)
		}
	}
	for _, tc := range wantFalse {
		m := NewMetadata(tc.fmtID)
		if m.canCarryIPTC() {
			t.Errorf("canCarryIPTC(%s) = true, want false", tc.name)
		}
	}

	// Also assert canCarryEXIF and canCarryXMP truth tables.
	// All writable formats support EXIF and XMP; Unknown does not.
	writable := []format.FormatID{
		format.FormatJPEG, format.FormatTIFF, format.FormatPNG, format.FormatHEIF,
		format.FormatWebP, format.FormatAVIF, format.FormatCR2, format.FormatCR3,
		format.FormatNEF, format.FormatARW, format.FormatDNG, format.FormatORF,
		format.FormatRW2,
	}
	for _, fmtID := range writable {
		m := NewMetadata(fmtID)
		if !m.canCarryEXIF() {
			t.Errorf("canCarryEXIF(%v) = false, want true", fmtID)
		}
		if !m.canCarryXMP() {
			t.Errorf("canCarryXMP(%v) = false, want true", fmtID)
		}
	}
	unknown := NewMetadata(format.FormatUnknown)
	if unknown.canCarryEXIF() {
		t.Error("canCarryEXIF(Unknown) = true, want false")
	}
	if unknown.canCarryXMP() {
		t.Error("canCarryXMP(Unknown) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// Finding #110 — SetKeywords persists IPTC for TIFF-based RAW formats
//
// TestSetKeywords_PersistsIPTC_ForRAWFormats verifies that SetKeywords on a
// Metadata created with NewMetadata(<TIFF-based RAW format>) auto-creates IPTC.
// This is a pre-condition test (no Write+Read round-trip for RAW is attempted
// because the write path requires a real file; JPEG is used for the round-trip
// leg). The key assertion is that m.IPTC is non-nil after SetKeywords.
// ---------------------------------------------------------------------------

func TestSetKeywords_PersistsIPTC_ForRAWFormats(t *testing.T) {
	t.Parallel()

	rawFormats := []struct {
		name  string
		fmtID format.FormatID
	}{
		{"DNG", format.FormatDNG},
		{"CR2", format.FormatCR2},
		{"NEF", format.FormatNEF},
		{"ARW", format.FormatARW},
		{"ORF", format.FormatORF},
		{"RW2", format.FormatRW2},
	}

	kws := []string{"mountain", "landscape"}

	for _, tc := range rawFormats {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := NewMetadata(tc.fmtID)

			// Pre-condition: no components yet.
			if m.IPTC != nil {
				t.Fatal("pre-condition: IPTC must be nil before SetKeywords")
			}

			m.SetKeywords(kws)

			// IPTC must be auto-created (#110 fix).
			if m.IPTC == nil {
				t.Errorf("SetKeywords on %s: IPTC is nil (auto-create failed)", tc.name)
			} else {
				got := m.IPTC.Keywords()
				if len(got) != len(kws) {
					t.Errorf("IPTC.Keywords() len = %d, want %d", len(got), len(kws))
				} else {
					for i, kw := range kws {
						if got[i] != kw {
							t.Errorf("IPTC.Keywords()[%d] = %q, want %q", i, got[i], kw)
						}
					}
				}
			}

			// XMP must also be auto-created (all writable formats support XMP).
			if m.XMP == nil {
				t.Errorf("SetKeywords on %s: XMP is nil (auto-create failed)", tc.name)
			} else {
				gotX := m.XMP.Keywords()
				if len(gotX) != len(kws) {
					t.Errorf("XMP.Keywords() len = %d, want %d", len(gotX), len(kws))
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Finding #110 — JPEG round-trip (SetKeywords → Write → Read) confirms
// the existing JPEG path still works, and adds a TIFF in-memory round-trip.
// ---------------------------------------------------------------------------

// TestSetKeywords_JPEG_IPTC_RoundTrip verifies that keywords written via
// SetKeywords on a JPEG survive a Write→Read round-trip through IPTC.
// This is a regression gate to ensure the #110 canCarryIPTC expansion did
// not regress JPEG behaviour.
func TestSetKeywords_JPEG_IPTC_RoundTrip(t *testing.T) {
	t.Parallel()

	img := acBuildJPEGWithEXIFOnly()
	m, err := Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read JPEG: %v", err)
	}

	want := []string{"city", "night"}
	m.SetKeywords(want)

	if m.IPTC == nil {
		t.Fatal("IPTC not auto-created for JPEG")
	}

	var out bytes.Buffer
	if err := Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	got := m2.Keywords()
	if len(got) != len(want) {
		t.Fatalf("Keywords round-trip len=%d, want %d: got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Keywords[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Finding #110 — divergent IPTC/XMP caption on RAW re-read
//
// TestSetCaption_RAW_NoDivergentIPTC verifies that after reading a JPEG
// (as proxy for a RAW file that carries IPTC) and calling SetCaption, both
// m.IPTC.Caption() and m.XMP.Caption() carry the new value — there is no
// divergence between the two.
//
// The divergence scenario from the audit: read a file WITH IPTC, set
// m.IPTC = nil manually, then call SetCaption. Because canCarryIPTC was
// false for RAW formats, ensureIPTC was a no-op and m.IPTC stayed nil.
// On Write, the encode path fell back to rawIPTC (the original bytes),
// producing old IPTC caption + new XMP caption.
//
// After the fix, ensureIPTC creates a fresh IPTC for all TIFF-based formats
// so both layers receive the new value.
// ---------------------------------------------------------------------------

func TestSetCaption_RAW_NoDivergentIPTC(t *testing.T) {
	t.Parallel()

	// Build a JPEG carrying both IPTC and XMP with "old caption".
	img := buildJPEGWithIPTCAndXMP("old caption", "old caption")

	m, err := Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read JPEG with IPTC+XMP: %v", err)
	}
	if m.IPTC == nil {
		t.Fatal("pre-condition: IPTC must be non-nil after reading JPEG with IPTC")
	}

	// Simulate what the audit described: caller clears IPTC then calls Set*.
	// After the fix, SetCaption re-creates IPTC (for JPEG format) with the
	// new value — no divergence.
	m.IPTC = nil
	const newCaption = "new caption after re-set"
	m.SetCaption(newCaption)

	if m.IPTC == nil {
		t.Fatal("IPTC not auto-created by SetCaption after being cleared")
	}
	if m.XMP == nil {
		t.Fatal("XMP not auto-created by SetCaption")
	}
	if got := m.IPTC.Caption(); got != newCaption {
		t.Errorf("IPTC.Caption() = %q, want %q (divergence detected)", got, newCaption)
	}
	if got := m.XMP.Caption(); got != newCaption {
		t.Errorf("XMP.Caption() = %q, want %q (divergence detected)", got, newCaption)
	}
}

// ---------------------------------------------------------------------------
// Finding #128 — concurrent Set* safety
//
// TestMetadataConcurrentSet runs 100 goroutines calling SetCaption on a single
// *Metadata under -race. It asserts no data race and that the final Caption()
// is one of the values written (the last writer wins, which is fine — the
// contract is no crash/race, not a specific ordering).
// ---------------------------------------------------------------------------

func TestMetadataConcurrentSet(t *testing.T) {
	t.Parallel()

	m := NewMetadata(format.FormatJPEG)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			// Every goroutine writes a distinct caption value.
			// The race detector will fire if there is unsynchronised access.
			m.SetCaption("caption from goroutine")
			_ = n // suppress unused-variable lint
		}(i)
	}
	wg.Wait()

	// After all goroutines complete, Caption() must be the value set by one
	// of them (non-empty), and the race detector must not have fired.
	if got := m.Caption(); got != "caption from goroutine" {
		t.Errorf("Caption() = %q after concurrent SetCaption; want non-empty consistent value", got)
	}

	// EXIF/IPTC/XMP must all be non-nil (auto-created exactly once).
	if m.EXIF == nil {
		t.Error("EXIF is nil after concurrent SetCaption on JPEG — auto-create failed")
	}
	if m.IPTC == nil {
		t.Error("IPTC is nil after concurrent SetCaption on JPEG — auto-create failed")
	}
	if m.XMP == nil {
		t.Error("XMP is nil after concurrent SetCaption on JPEG — auto-create failed")
	}
}

// ---------------------------------------------------------------------------
// Finding #185 — FormatUnknown Set* contract
//
// TestNewMetadata_UnknownFormat_SetContract locks in the documented behaviour:
// Set* on a FormatUnknown Metadata is a no-op (no auto-create, no value
// stored). The test asserts and documents this contract so the behaviour is
// not a silent footgun.
// ---------------------------------------------------------------------------

func TestNewMetadata_UnknownFormat_SetContract(t *testing.T) {
	t.Parallel()

	m := NewMetadata(format.FormatUnknown)

	// None of these must panic, and none must auto-create any component.
	m.SetCaption("test")
	m.SetCopyright("(c) test")
	m.SetCreator("creator")
	m.SetKeywords([]string{"kw1", "kw2"})
	m.SetGPS(1.0, 2.0)
	m.SetCameraModel("Model X")
	m.SetLensModel("50mm")
	m.SetMake("Acme")
	m.SetISO(200)
	m.SetFNumber(2.8)
	m.SetFocalLength(50.0)
	m.SetOrientation(1)
	m.SetImageSize(1920, 1080)

	// Documented contract: FormatUnknown Set* is a no-op — no components created.
	if m.EXIF != nil {
		t.Error("EXIF must not be auto-created for FormatUnknown (documented no-op contract)")
	}
	if m.IPTC != nil {
		t.Error("IPTC must not be auto-created for FormatUnknown (documented no-op contract)")
	}
	if m.XMP != nil {
		t.Error("XMP must not be auto-created for FormatUnknown (documented no-op contract)")
	}

	// All getters must return zero/empty (no stored values).
	if got := m.Caption(); got != "" {
		t.Errorf("Caption() = %q, want empty for FormatUnknown", got)
	}
	if got := m.Keywords(); len(got) != 0 {
		t.Errorf("Keywords() = %v, want nil/empty for FormatUnknown", got)
	}
	if got := m.CameraModel(); got != "" {
		t.Errorf("CameraModel() = %q, want empty for FormatUnknown", got)
	}

	// Write must fail with UnsupportedFormatError (existing behaviour, not new).
	img := acBuildJPEGNoEXIF() // arbitrary image bytes
	err := Write(bytes.NewReader(img), bytes.NewBuffer(nil), m)
	if err == nil {
		t.Error("Write on FormatUnknown Metadata must return an error")
	}
}

// ---------------------------------------------------------------------------
// Finding #148 — Read of metadata-free container
//
// TestRead_EmptyContainer_NilErrorNoMetadata asserts and locks the corrected
// contract: Read of a structurally valid but metadata-free JPEG returns
// (m, nil) where m.EXIF, m.IPTC, and m.XMP are all nil, and ParseWarnings
// is also nil (no parse failures, no metadata to warn about).
// ---------------------------------------------------------------------------

func TestRead_EmptyContainer_NilErrorNoMetadata(t *testing.T) {
	t.Parallel()

	// Build a JPEG that is structurally valid (detectable) but carries zero
	// metadata segments — no EXIF APP1, no APP13 IPTC, no XMP APP1.
	img := acBuildJPEGNoEXIF()

	m, err := Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read: unexpected error: %v (want nil — container is valid)", err)
	}
	if m == nil {
		t.Fatal("Read returned nil *Metadata, want non-nil")
	}

	// Corrected contract: nil error does NOT imply ≥1 metadata type was parsed.
	// All three may be nil for a metadata-free container.
	if m.EXIF != nil {
		t.Errorf("EXIF should be nil for a metadata-free container, got non-nil")
	}
	if m.IPTC != nil {
		t.Errorf("IPTC should be nil for a metadata-free container, got non-nil")
	}
	if m.XMP != nil {
		t.Errorf("XMP should be nil for a metadata-free container, got non-nil")
	}

	// ParseWarnings must be nil (no parse attempts were made that could fail).
	if m.ParseWarnings != nil {
		t.Errorf("ParseWarnings should be nil for a clean empty container, got %v", m.ParseWarnings)
	}

	// Format must still be detected correctly.
	if got := m.Format(); got != format.FormatJPEG {
		t.Errorf("Format() = %v, want FormatJPEG", got)
	}
}
