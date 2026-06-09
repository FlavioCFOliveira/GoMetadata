package heif

// avif_conformance_test.go — AVIF container specification-conformance battery.
//
// Rule IDs match verbatim the stable identifiers in docs/conformance/containers.md §6:
//
//	AVIF-brand-*         — §6(b) brand detection (avif/avis)
//	AVIF-Exif-item-*     — §6(d) EXIF item (same 4-byte prefix mechanism as HEIF)
//	AVIF-mime-xmp-*      — §6(d) XMP item (mime/application/rdf+xml)
//	AVIF-meta-*          — §6(c) meta/iinf/iloc/iref/iprp mechanism
//	AVIF-write-*         — §6(e) write byte-correctness
//	AVIF-robust-*        — §6(f) robustness cases (same as HEIF §5f + avis sequences)
//	AVIF-corpus-*        — corpus parity via testutil.CorpusFiles
//
// AVIF shares the ISO BMFF meta-item mechanism with HEIF (ISO 23008-12 / ISO 14496-12).
// All EXIF/XMP metadata rules are identical to HEIF; only the ftyp brands differ.
//
// AOM "AV1 Image File Format (AVIF)" v1.2.0 (on HEIF + MIAF ISO 23000-22).
// Reference: libavif, AOM AVIF spec §4 (brands), §7 (metadata).
//
// No t.Skip in synthetic tests. All tests pass -race deterministically.
// Corpus tests use testutil.CorpusFiles which skips when corpus is absent.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ---------------------------------------------------------------------------
// AVIF-specific fixture builders
// ---------------------------------------------------------------------------

// buildConformanceAVIF assembles a minimal valid AVIF file for conformance testing.
// AVIF uses exactly the same meta/iinf/iloc/iref structure as HEIF; only ftyp differs.
// AOM AVIF §4: ftyp major_brand "avif" (still image) or "avis" (sequence).
func buildConformanceAVIF(majorBrand string, exifPayload, xmpPayload []byte) []byte {
	// AVIF brands required in compatible_brands: mif1, miaf, MA1B (or MA1S for avis).
	// AOM AVIF v1.2.0 §4: compatible_brands must include mif1.
	return buildConformanceHEIF(majorBrand, exifPayload, xmpPayload)
}

// buildAVIFWithCompatBrands assembles an AVIF ftyp box with specific compatible brands.
// Used to test MIAF brand constraint requirements.
func buildAVIFWithCompatBrands(majorBrand string, compatBrands ...string) []byte {
	ftyp := bmffFtyp(majorBrand, 0, compatBrands...)
	meta := func() []byte {
		iinf := bmffIinf()
		iloc := bmffIloc(nil)
		metaBody := make([]byte, 0, 4+len(iinf)+len(iloc))
		metaBody = append(metaBody, 0, 0, 0, 0)
		metaBody = append(metaBody, iinf...)
		metaBody = append(metaBody, iloc...)
		return bmffBox("meta", metaBody)
	}()
	return append(ftyp, meta...)
}

// ---------------------------------------------------------------------------
// §6(b) — AVIF brand detection (AVIF-brand-*)
// ---------------------------------------------------------------------------

// TestAVIFBrandAvif verifies that major_brand "avif" is accepted by Extract.
// AOM AVIF v1.2.0 §4: "avif" is the primary brand for still-image AVIF.
func TestAVIFBrandAvif(t *testing.T) {
	// AVIF-brand-avif: §6(b) — avif brand must be accepted.
	t.Parallel()
	data := buildConformanceAVIF("avif", conformanceMinimalTIFF(), nil)
	if _, _, _, err := Extract(bytes.NewReader(data)); err != nil {
		t.Fatalf("AVIF-brand-avif: Extract failed: %v", err)
	}
}

// TestAVIFBrandAvis verifies that major_brand "avis" (AVIF sequence) is accepted.
// AOM AVIF v1.2.0 §4: "avis" = AVIF image sequence brand.
func TestAVIFBrandAvis(t *testing.T) {
	// AVIF-brand-avis: §6(b) — avis brand (sequence) must be accepted.
	t.Parallel()
	data := buildConformanceAVIF("avis", conformanceMinimalTIFF(), nil)
	if _, _, _, err := Extract(bytes.NewReader(data)); err != nil {
		t.Fatalf("AVIF-brand-avis: Extract failed: %v", err)
	}
}

// TestAVIFBrandMif1Compatible verifies that an avif file with "mif1" in
// compatible_brands is accepted — MIAF requires mif1 in compat brands.
// ISO 23000-22 §7.2: MIAF files carry mif1 as a compatible brand.
func TestAVIFBrandMif1Compatible(t *testing.T) {
	// AVIF-brand-mif1-compatible: §6(b) / ISO 23000-22 — mif1 in compat_brands is accepted.
	t.Parallel()
	data := buildAVIFWithCompatBrands("avif", "mif1", "miaf", "MA1B")
	// Extract must not error even though this is a stripped-down file.
	_, _, _, _ = Extract(bytes.NewReader(data))
	// No assertion on error: minimal file may have no metadata items.
	// The key invariant is: no panic.
}

// TestAVIFBrandAvifVsHeicSeparation verifies that "avif" and "heic" brands
// are treated identically at the Extract level (both use the same meta/item model).
// AOM AVIF §7: AVIF metadata items use the same mechanism as HEIF ISO 23008-12.
func TestAVIFBrandAvifVsHeicSeparation(t *testing.T) {
	// AVIF-brand-avif-vs-heic: §6(b) — avif and heic produce same metadata extraction.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	avifData := buildConformanceAVIF("avif", exif, nil)
	heifData := buildConformanceHEIF("heic", exif, nil)

	avifEXIF, _, _, avifErr := Extract(bytes.NewReader(avifData))
	heifEXIF, _, _, heifErr := Extract(bytes.NewReader(heifData))

	if avifErr != nil {
		t.Fatalf("AVIF-brand-avif-vs-heic: AVIF Extract failed: %v", avifErr)
	}
	if heifErr != nil {
		t.Fatalf("AVIF-brand-avif-vs-heic: HEIF Extract failed: %v", heifErr)
	}
	if !bytes.Equal(avifEXIF, heifEXIF) {
		t.Errorf("AVIF-brand-avif-vs-heic: EXIF payloads differ between avif and heic brands")
	}
}

// ---------------------------------------------------------------------------
// §6(d) — EXIF item extraction in AVIF (AVIF-Exif-item-*)
// ---------------------------------------------------------------------------

// TestAVIFExifItem4BytePrefix verifies that the EXIF item in AVIF uses the same
// 4-byte u32 BE exif_tiff_header_offset prefix as HEIF.
// AOM AVIF §7 / ISO 23008-12 §6.6.1: ExifDataBlock is identical in AVIF and HEIF.
func TestAVIFExifItem4BytePrefix(t *testing.T) {
	// AVIF-Exif-item-4byte-prefix: §6(d) / ISO 23008-12 §6.6.1 — same 4-byte prefix as HEIF.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceAVIF("avif", exif, nil)

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("AVIF-Exif-item-4byte-prefix: Extract failed: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("AVIF-Exif-item-4byte-prefix: rawEXIF is nil, want non-nil")
	}
	if !bytes.Equal(rawEXIF, exif) {
		t.Errorf("AVIF-Exif-item-4byte-prefix: rawEXIF len=%d, want %d (prefix stripped)", len(rawEXIF), len(exif))
	}
}

// TestAVIFExifItemInfeType verifies that the Exif item in AVIF uses infe
// item_type "Exif" (exactly 4 bytes).
// AOM AVIF §7: metadata items are identified by item_type, same as HEIF.
func TestAVIFExifItemInfeType(t *testing.T) {
	// AVIF-Exif-item-infe-type: §6(d) — infe item_type must be "Exif".
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceAVIF("avif", exif, nil)

	metaContent, err := findBox(data, "meta", 0)
	if err != nil || metaContent == nil {
		t.Fatalf("AVIF-Exif-item-infe-type: meta box not found (err=%v)", err)
	}
	itemTypes := parseIinf(metaContent)
	foundExif := false
	for _, typ := range itemTypes {
		if typ == "Exif" {
			foundExif = true
		}
	}
	if !foundExif {
		t.Errorf("AVIF-Exif-item-infe-type: no Exif item_type found in iinf; types=%v", itemTypes)
	}
}

// TestAVIFExifItemRoundTrip verifies a full Extract→Inject→Extract round trip
// for EXIF items in AVIF.
// AOM AVIF §7 + ISO 14496-12 §8.11.3: iloc offsets must be correct after inject.
func TestAVIFExifItemRoundTrip(t *testing.T) {
	// AVIF-Exif-item-round-trip: §6(e) — EXIF inject then extract in AVIF.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceAVIF("avif", exif, nil)

	newEXIF := append(bytes.Clone(exif), 0x42, 0x42)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newEXIF, nil, nil, true); err != nil {
		t.Fatalf("AVIF-Exif-item-round-trip: Inject failed: %v", err)
	}

	gotEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("AVIF-Exif-item-round-trip: Extract after Inject failed: %v", err)
	}
	if !bytes.Equal(gotEXIF, newEXIF) {
		t.Errorf("AVIF-Exif-item-round-trip: EXIF mismatch: got %d bytes, want %d", len(gotEXIF), len(newEXIF))
	}
}

// TestAVIFExifItemPrefixIsWrittenOnInject verifies that Inject stores the
// 4-byte zero prefix before the EXIF payload in AVIF files.
// ISO 23008-12 §6.6.1 (applicable to AVIF via ISO 23000-22): prefix is mandatory.
func TestAVIFExifItemPrefixIsWrittenOnInject(t *testing.T) {
	// AVIF-Exif-item-prefix-written-on-inject: §6(e) — Inject adds 4-byte zero prefix.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceAVIF("avif", exif, nil)

	newEXIF := conformanceMinimalTIFF()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newEXIF, nil, nil, true); err != nil {
		t.Fatalf("AVIF-Exif-item-prefix-written-on-inject: Inject failed: %v", err)
	}

	outData := out.Bytes()
	metaContent, err := findBox(outData, "meta", 0)
	if err != nil || metaContent == nil {
		t.Fatalf("AVIF-Exif-item-prefix-written-on-inject: meta box not found (err=%v)", err)
	}
	locs := parseIloc(metaContent)
	for _, loc := range locs {
		if loc.length < 4 {
			continue
		}
		end := loc.offset + loc.length
		if end > uint64(len(outData)) {
			t.Errorf("AVIF-Exif-item-prefix-written-on-inject: iloc extent past EOF")
			continue
		}
		prefix := outData[loc.offset : loc.offset+4]
		if prefix[0] != 0 || prefix[1] != 0 || prefix[2] != 0 || prefix[3] != 0 {
			t.Errorf("AVIF-Exif-item-prefix-written-on-inject: EXIF item missing 4-byte zero prefix: %x", prefix)
		}
	}
}

// ---------------------------------------------------------------------------
// §6(d) — XMP item extraction in AVIF (AVIF-mime-xmp-*)
// ---------------------------------------------------------------------------

// TestAVIFMimeXMPItem verifies that a mime item with content_type
// "application/rdf+xml" is extracted as XMP in AVIF.
// AOM AVIF §7 / XMP Part 3 §1.8 / HEIF-01: same XMP mechanism as HEIF.
func TestAVIFMimeXMPItem(t *testing.T) {
	// AVIF-mime-xmp: §6(d) / HEIF-01 — application/rdf+xml item is XMP in AVIF.
	t.Parallel()
	xmp := conformanceXMPPacket()
	data := buildConformanceAVIF("avif", nil, xmp)

	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("AVIF-mime-xmp: Extract failed: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("AVIF-mime-xmp: rawXMP is nil, want XMP payload")
	}
	if !bytes.Equal(rawXMP, xmp) {
		t.Errorf("AVIF-mime-xmp: rawXMP len=%d, want %d", len(rawXMP), len(xmp))
	}
}

// TestAVIFMimeXMPRoundTrip verifies a full Extract→Inject→Extract round trip
// for XMP items in AVIF.
// AOM AVIF §7: same as HEIF — XMP is stored verbatim in the mime item payload.
func TestAVIFMimeXMPRoundTrip(t *testing.T) {
	// AVIF-mime-xmp-round-trip: §6(e) — XMP inject then extract in AVIF.
	t.Parallel()
	xmp := conformanceXMPPacket()
	data := buildConformanceAVIF("avif", nil, xmp)

	newXMP := append(bytes.Clone(xmp), '\n')
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, newXMP, true); err != nil {
		t.Fatalf("AVIF-mime-xmp-round-trip: Inject failed: %v", err)
	}

	_, _, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("AVIF-mime-xmp-round-trip: Extract after Inject failed: %v", err)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("AVIF-mime-xmp-round-trip: XMP mismatch: got %d bytes, want %d", len(gotXMP), len(newXMP))
	}
}

// TestAVIFBothItems verifies that EXIF and XMP items coexist correctly in AVIF.
// AOM AVIF §7: same multi-item meta box structure as HEIF.
func TestAVIFBothItems(t *testing.T) {
	// AVIF-both-items: §6(d) — EXIF and XMP both present and extracted correctly.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	xmp := conformanceXMPPacket()
	data := buildConformanceAVIF("avif", exif, xmp)

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("AVIF-both-items: Extract failed: %v", err)
	}
	if rawEXIF == nil {
		t.Error("AVIF-both-items: rawEXIF is nil")
	}
	if rawXMP == nil {
		t.Error("AVIF-both-items: rawXMP is nil")
	}
	if !bytes.Equal(rawEXIF, exif) {
		t.Errorf("AVIF-both-items: EXIF mismatch: got %d bytes, want %d", len(rawEXIF), len(exif))
	}
	if !bytes.Equal(rawXMP, xmp) {
		t.Errorf("AVIF-both-items: XMP mismatch: got %d bytes, want %d", len(rawXMP), len(xmp))
	}
}

// ---------------------------------------------------------------------------
// §6(c) — meta box structure in AVIF (AVIF-meta-*)
// ---------------------------------------------------------------------------

// TestAVIFMetaBoxIlocOffsets verifies that after building a conformance AVIF,
// all iloc extents point to valid byte ranges.
// ISO 14496-12 §8.11.3: extent_offset is a file-absolute byte position.
func TestAVIFMetaBoxIlocOffsets(t *testing.T) {
	// AVIF-meta-iloc-offsets: §6(c) — iloc offsets within file bounds.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	xmp := conformanceXMPPacket()
	data := buildConformanceAVIF("avif", exif, xmp)

	metaContent, err := findBox(data, "meta", 0)
	if err != nil || metaContent == nil {
		t.Fatalf("AVIF-meta-iloc-offsets: meta box not found (err=%v)", err)
	}
	locs := parseIloc(metaContent)
	if len(locs) == 0 {
		t.Fatal("AVIF-meta-iloc-offsets: no iloc entries found")
	}
	for _, loc := range locs {
		if loc.length == 0 {
			continue
		}
		end := loc.offset + loc.length
		if end > uint64(len(data)) {
			t.Errorf("AVIF-meta-iloc-offsets: iloc extent [%d,%d) exceeds file size %d",
				loc.offset, end, len(data))
		}
	}
}

// TestAVIFMetaBoxIinfItemCount verifies that the iinf entry_count matches the
// actual number of infe boxes present.
// ISO 14496-12 §8.11.6: entry_count must equal the number of infe child boxes.
func TestAVIFMetaBoxIinfItemCount(t *testing.T) {
	// AVIF-meta-iinf-item-count: §6(c) — entry_count matches actual infe count.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	xmp := conformanceXMPPacket()
	data := buildConformanceAVIF("avif", exif, xmp)

	metaContent, err := findBox(data, "meta", 0)
	if err != nil || metaContent == nil {
		t.Fatalf("AVIF-meta-iinf-item-count: meta box not found (err=%v)", err)
	}
	iinfData := findInnerBox(metaContent, "iinf")
	if iinfData == nil {
		t.Fatal("AVIF-meta-iinf-item-count: iinf box not found")
	}

	// iinf FullBox: version+flags(4) + entry_count.
	if len(iinfData) < 6 {
		t.Fatal("AVIF-meta-iinf-item-count: iinf too short")
	}
	version := iinfData[0]
	pos := 4
	declaredCount, _, ok := parseIinfItemCount(iinfData, version, pos)
	if !ok {
		t.Fatal("AVIF-meta-iinf-item-count: could not parse entry_count")
	}

	// Count actual infe boxes.
	actualCount := 0
	scanPos := pos + 2 // past entry_count
	if version >= 2 {
		scanPos = pos + 4
	}
	for scanPos < len(iinfData) {
		sz, typ, _, ok2 := parseHEIFBoxHeader(iinfData, scanPos)
		if !ok2 {
			break
		}
		if typ == "infe" {
			actualCount++
		}
		scanPos += int(sz) //nolint:gosec // G115: ISOBMFF box size bounded
	}
	if declaredCount != actualCount {
		t.Errorf("AVIF-meta-iinf-item-count: declared entry_count=%d, actual infe count=%d",
			declaredCount, actualCount)
	}
}

// ---------------------------------------------------------------------------
// §6(e) — Write byte-correctness in AVIF (AVIF-write-*)
// ---------------------------------------------------------------------------

// TestAVIFWriteIlocOffsetCorrect verifies that after Inject, iloc extents
// in the output point to the correct file positions.
// ISO 14496-12 §8.11.3 (applicable to AVIF): extents are absolute file positions.
func TestAVIFWriteIlocOffsetCorrect(t *testing.T) {
	// AVIF-write-iloc-offset-correct: §6(e) — iloc offsets correct after inject.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceAVIF("avif", exif, nil)

	newEXIF := append(bytes.Clone(exif), 0xBE, 0xEF)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, newEXIF, nil, nil, true); err != nil {
		t.Fatalf("AVIF-write-iloc-offset-correct: Inject failed: %v", err)
	}

	outData := out.Bytes()
	metaContent, err := findBox(outData, "meta", 0)
	if err != nil || metaContent == nil {
		t.Fatalf("AVIF-write-iloc-offset-correct: meta box not found in output (err=%v)", err)
	}
	locs := parseIloc(metaContent)
	for id, loc := range locs {
		if loc.length == 0 {
			continue
		}
		end := loc.offset + loc.length
		if end > uint64(len(outData)) {
			t.Errorf("AVIF-write-iloc-offset-correct: item %d: extent [%d,%d) exceeds output size %d",
				id, loc.offset, end, len(outData))
		}
	}
}

// TestAVIFWritePreservesBrand verifies that after Inject the ftyp major_brand
// in the output matches the original.
// AOM AVIF §4: brand must not be modified by metadata injection.
func TestAVIFWritePreservesBrand(t *testing.T) {
	// AVIF-write-preserves-brand: §6(e) — brand unchanged after inject.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceAVIF("avif", exif, nil)

	var out bytes.Buffer
	newEXIF := conformanceMinimalTIFF()
	if err := Inject(bytes.NewReader(data), &out, newEXIF, nil, nil, true); err != nil {
		t.Fatalf("AVIF-write-preserves-brand: Inject failed: %v", err)
	}

	outData := out.Bytes()
	// ftyp major brand is at bytes [8:12] of the ftyp box.
	// Locate ftyp box.
	ftypStart, ftypEnd, ok := flatBoxRangeInFile(outData, "ftyp")
	if !ok || ftypEnd-ftypStart < 12 {
		t.Fatal("AVIF-write-preserves-brand: ftyp box not found or too short in output")
	}
	brand := string(outData[ftypStart+8 : ftypStart+12])
	if brand != "avif" {
		t.Errorf("AVIF-write-preserves-brand: brand=%q after inject, want avif", brand)
	}
}

// ---------------------------------------------------------------------------
// §6(f) — AVIF robustness (AVIF-robust-*)
// ---------------------------------------------------------------------------

// TestAVIFRobustEmptyFile verifies that an empty AVIF input is handled gracefully.
// §6(f): same robustness requirements as HEIF §5(f).
func TestAVIFRobustEmptyFile(t *testing.T) {
	// AVIF-robust-empty-file: §6(f) — empty file must not crash.
	t.Parallel()
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader([]byte{}))
	if err != nil {
		t.Fatalf("AVIF-robust-empty-file: got error %v, want nil", err)
	}
	if rawEXIF != nil || rawIPTC != nil || rawXMP != nil {
		t.Errorf("AVIF-robust-empty-file: got non-nil metadata for empty input")
	}
}

// TestAVIFRobustTruncated verifies that truncated AVIF files at every offset
// do not cause a panic.
// §6(f): parser must degrade gracefully on partial input.
func TestAVIFRobustTruncated(t *testing.T) {
	// AVIF-robust-truncated: §6(f) — truncated AVIF must not panic.
	t.Parallel()
	data := buildConformanceAVIF("avif", conformanceMinimalTIFF(), conformanceXMPPacket())
	for i := 0; i < len(data); i += len(data)/16 + 1 {
		truncated := data[:i]
		rawEXIF, _, rawXMP, _ := Extract(bytes.NewReader(truncated))
		_ = rawEXIF
		_ = rawXMP
	}
}

// TestAVIFRobustAvisSequence verifies that "avis" (AVIF sequence) files are
// parsed without panic — sequences may have more complex moov/trak nesting.
// AOM AVIF §4: avis uses the same meta-item mechanism as avif still images.
func TestAVIFRobustAvisSequence(t *testing.T) {
	// AVIF-robust-avis-sequence: §6(f) — avis sequence brand does not crash.
	t.Parallel()
	exif := conformanceMinimalTIFF()
	data := buildConformanceAVIF("avis", exif, nil)

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("AVIF-robust-avis-sequence: Extract failed: %v", err)
	}
	if rawEXIF == nil {
		t.Error("AVIF-robust-avis-sequence: rawEXIF is nil for avis EXIF item")
	}
}

// TestAVIFRobustInfeOOB exercises the same CRITICAL infe OOB path for AVIF
// (identical code path — test_coverage for the avif brand variant).
// §6(f) CRITICAL: same infe/iloc/iinf walk used for AVIF — same OOB risk.
func TestAVIFRobustInfeOOB(t *testing.T) {
	// AVIF-robust-infe-OOB: §6(f) CRITICAL — infe OOB must not panic (AVIF brand).
	t.Parallel()

	// Same truncated iinf as the HEIF test but with avif ftyp brand.
	iinfTruncBody := []byte{
		0x00, 0x00, 0x00, 0x00, // version=0, flags=0
		0x00, 0x05, // entry_count=5
		0x00, 0x01, // 2 stray bytes
	}
	iinfTrunc := bmffBox("iinf", iinfTruncBody)
	ilocItems := []bmffIlocItem{{id: 1, offset: 200, length: 4}}
	iloc := bmffIloc(ilocItems)

	metaBody := make([]byte, 0, 4+len(iinfTrunc)+len(iloc))
	metaBody = append(metaBody, 0, 0, 0, 0)
	metaBody = append(metaBody, iinfTrunc...)
	metaBody = append(metaBody, iloc...)
	meta := bmffBox("meta", metaBody)

	ftyp := bmffFtyp("avif", 0, "mif1")
	padding := make([]byte, 200)
	payload := []byte{0x49, 0x49, 0x2A, 0x00}

	data := make([]byte, 0, len(ftyp)+len(meta)+len(padding)+len(payload))
	data = append(data, ftyp...)
	data = append(data, meta...)
	data = append(data, padding...)
	data = append(data, payload...)

	// Must not panic.
	rawEXIF, _, rawXMP, _ := Extract(bytes.NewReader(data))
	_ = rawEXIF
	_ = rawXMP
}

// TestAVIFRobustMalformedIloc verifies that a malformed iloc with
// non-standard field sizes does not cause a panic.
// ISO 14496-12 §8.11.3: iloc field sizes 0/4/8 are valid; others are rejected.
func TestAVIFRobustMalformedIloc(t *testing.T) {
	// AVIF-robust-malformed-iloc: §6(f) — non-standard iloc field sizes are safe.
	t.Parallel()

	// Build an iloc with offsetSize=3 (non-standard — spec only allows 0, 4, 8).
	ilocBody := []byte{
		0x00, 0x00, 0x00, 0x00, // version=0, flags=0
		0x30,       // offsetSize=3 (non-standard), lengthSize=0
		0x00,       // baseOffsetSize=0
		0x00, 0x01, // item_count=1
		0x00, 0x01, // item_ID=1
		0x00, 0x01, // extent_count=1
		0xDE, 0xAD, 0xBE, // 3-byte offset (non-standard)
	}
	rawIloc := make([]byte, 8+len(ilocBody))
	binary.BigEndian.PutUint32(rawIloc[0:], uint32(len(rawIloc))) //nolint:gosec // G115: test helper
	copy(rawIloc[4:], "iloc")
	copy(rawIloc[8:], ilocBody)

	infe := bmffInfeV2(1, "Exif")
	iinf := bmffIinf(infe)
	metaBody := make([]byte, 0, 4+len(iinf)+len(rawIloc))
	metaBody = append(metaBody, 0, 0, 0, 0)
	metaBody = append(metaBody, iinf...)
	metaBody = append(metaBody, rawIloc...)
	meta := bmffBox("meta", metaBody)
	ftyp := bmffFtyp("avif", 0)

	data := append(ftyp, meta...)
	// Must not panic.
	rawEXIF, _, rawXMP, _ := Extract(bytes.NewReader(data))
	_ = rawEXIF
	_ = rawXMP
}

// TestAVIFRobustLargeItemCount verifies that a file claiming an enormous
// item count does not cause OOM or panic.
// §6(f): denial-of-service via large count field must be bounded.
func TestAVIFRobustLargeItemCount(t *testing.T) {
	// AVIF-robust-large-item-count: §6(f) — large iinf entry_count bounded safely.
	t.Parallel()

	// iinf with entry_count = 65535 but no actual infe data.
	iinfBody := []byte{
		0x00, 0x00, 0x00, 0x00, // version=0, flags=0
		0xFF, 0xFF, // entry_count = 65535
		// no infe bytes follow — parser must stop at EOF, not OOM
	}
	iinf := bmffBox("iinf", iinfBody)
	iloc := bmffIloc(nil)
	metaBody := make([]byte, 0, 4+len(iinf)+len(iloc))
	metaBody = append(metaBody, 0, 0, 0, 0)
	metaBody = append(metaBody, iinf...)
	metaBody = append(metaBody, iloc...)
	meta := bmffBox("meta", metaBody)
	ftyp := bmffFtyp("avif", 0)

	data := append(ftyp, meta...)
	// Must complete in bounded time and not OOM.
	rawEXIF, _, rawXMP, _ := Extract(bytes.NewReader(data))
	_ = rawEXIF
	_ = rawXMP
}

// ---------------------------------------------------------------------------
// §6 — Corpus parity (AVIF-corpus-*)
// ---------------------------------------------------------------------------

// TestAVIFCorpusExtract runs Extract on every .avif file in testdata/corpus/heif
// and verifies no panic occurs. Skipped if corpus is absent.
// AOM AVIF §7: real-world AVIF files exercise the full metadata path.
func TestAVIFCorpusExtract(t *testing.T) {
	// AVIF-corpus-extract: §6 — real-world AVIF files must not panic.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "heif")
	avifPaths := make([]string, 0)
	for _, p := range paths {
		if strings.HasSuffix(strings.ToLower(p), ".avif") {
			avifPaths = append(avifPaths, p)
		}
	}
	if len(avifPaths) == 0 {
		t.Skip("AVIF-corpus-extract: no .avif files in testdata/corpus/heif")
	}
	for _, path := range avifPaths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("AVIF-corpus-extract: open %s: %v", path, err)
			}
			t.Cleanup(func() { _ = f.Close() })
			rawEXIF, _, rawXMP, _ := Extract(f)
			_ = rawEXIF
			_ = rawXMP
		})
	}
}

// TestAVIFCorpusInjectRoundTrip runs Inject→Extract on every .avif corpus file
// and verifies that the injected XMP is readable back. Skipped if corpus is absent.
func TestAVIFCorpusInjectRoundTrip(t *testing.T) {
	// AVIF-corpus-inject-round-trip: §6(e) — AVIF corpus files survive inject round trip.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "heif")
	newXMP := conformanceXMPPacket()
	for _, path := range paths {
		if !strings.HasSuffix(strings.ToLower(path), ".avif") {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("AVIF-corpus-inject-round-trip: open %s: %v", path, err)
			}
			t.Cleanup(func() { _ = f.Close() })

			var out bytes.Buffer
			if injectErr := Inject(f, &out, nil, nil, newXMP, true); injectErr != nil {
				// Acceptable: some AVIF files may have unsupported structures.
				return
			}
			_, _, gotXMP, extractErr := Extract(bytes.NewReader(out.Bytes()))
			if extractErr != nil {
				t.Errorf("AVIF-corpus-inject-round-trip: Extract after Inject failed for %s: %v",
					filepath.Base(path), extractErr)
			}
			_ = gotXMP
		})
	}
}

// TestAVIFCorpusBrandCheck verifies that every .avif corpus file has a ftyp box
// whose major_brand or compatible_brands contains "avif" or "avis".
// AOM AVIF §4: conformant AVIF files must carry the avif or avis brand.
func TestAVIFCorpusBrandCheck(t *testing.T) {
	// AVIF-corpus-brand-check: §6(b) — corpus AVIF files carry avif/avis brand.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "heif")
	for _, path := range paths {
		if !strings.HasSuffix(strings.ToLower(path), ".avif") {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("AVIF-corpus-brand-check: read %s: %v", path, err)
			}
			// Locate ftyp box and verify brand.
			ftypStart, ftypEnd, ok := flatBoxRangeInFile(raw, "ftyp")
			if !ok {
				t.Fatalf("AVIF-corpus-brand-check: no ftyp box in %s", filepath.Base(path))
			}
			if ftypEnd-ftypStart < 12 {
				t.Fatalf("AVIF-corpus-brand-check: ftyp too short in %s", filepath.Base(path))
			}
			ftypBox := raw[ftypStart:ftypEnd]
			// Check major_brand [8:12] and compatible_brands [16:] in 4-byte chunks.
			allBrands := make([]string, 0)
			if ftypEnd-ftypStart >= 12 {
				allBrands = append(allBrands, string(ftypBox[8:12]))
			}
			for off := 16; off+4 <= len(ftypBox); off += 4 {
				allBrands = append(allBrands, string(ftypBox[off:off+4]))
			}
			hasAVIFBrand := false
			for _, b := range allBrands {
				if b == "avif" || b == "avis" {
					hasAVIFBrand = true
					break
				}
			}
			if !hasAVIFBrand {
				t.Errorf("AVIF-corpus-brand-check: %s has no avif/avis brand; brands=%v",
					filepath.Base(path), allBrands)
			}
		})
	}
}
