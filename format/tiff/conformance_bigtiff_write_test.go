package tiff

// conformance_bigtiff_write_test.go — conformance battery for task #270
// (standalone BigTIFF container write / 64-bit relocation).
//
// Rule IDs S-40..S-43, V-15, R-18, R-19 are defined in
// docs/conformance/exif-tiff.md §2.8. Every sub-test name below matches one
// of those IDs so a failure points directly at the violated rule.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// forceRelocateIPTC is a non-empty IPTC payload used throughout this file to
// force tiff.Inject's copy-and-relocate path (relocateTIFFFromParsed) rather
// than the pass-through path — the relocate path is what task #270 hardens.
var forceRelocateIPTC = []byte("task-270-conformance-force-relocate-path") //nolint:gochecknoglobals // test-only constant byte slice

// ---------------------------------------------------------------------------
// S-40: outer-container structural lookups use BigTIFF widths
// ---------------------------------------------------------------------------

// TestConformance_S40_bigtiff_outer_container_widths proves that
// readIFD0Offset/findEntryInIFD select the 16-byte-header/20-byte-entry/
// 8-byte-field BigTIFF layout, and the 8-byte-header/12-byte-entry/4-byte-field
// classic layout, based purely on the bigTIFF flag — never conflating the two.
func TestConformance_S40_bigtiff_outer_container_widths(t *testing.T) {
	t.Parallel()

	le := binary.LittleEndian

	t.Run("classic", func(t *testing.T) {
		t.Parallel()
		buf := make([]byte, 26)
		buf[0], buf[1] = 'I', 'I'
		le.PutUint16(buf[2:], 0x002A)
		le.PutUint32(buf[4:], 8) // IFD0 @ 8
		le.PutUint16(buf[8:], 1) // 1 entry
		le.PutUint16(buf[10:], 0x0100)
		le.PutUint16(buf[12:], 4) // LONG
		le.PutUint32(buf[14:], 1)
		le.PutUint32(buf[18:], 42)

		off, ok := readIFD0Offset(buf, false, le)
		if !ok || off != 8 {
			t.Fatalf("readIFD0Offset(classic): off=%d ok=%v, want 8/true", off, ok)
		}
		entry, found := findEntryInIFD(buf, off, 0x0100, false, le)
		if !found || entry.typ != 4 || entry.count != 1 {
			t.Fatalf("findEntryInIFD(classic): found=%v entry=%+v", found, entry)
		}
	})

	t.Run("bigtiff", func(t *testing.T) {
		t.Parallel()
		buf := make([]byte, 46)
		buf[0], buf[1] = 'I', 'I'
		le.PutUint16(buf[2:], 0x002B)
		le.PutUint16(buf[4:], 8) // offset-bytesize = 8
		le.PutUint16(buf[6:], 0)
		le.PutUint64(buf[8:], 16) // IFD0 @ 16
		le.PutUint64(buf[16:], 1) // 1 entry
		le.PutUint16(buf[24:], 0x0100)
		le.PutUint16(buf[26:], 4) // LONG
		le.PutUint64(buf[28:], 1)
		le.PutUint64(buf[36:], 42)

		off, ok := readIFD0Offset(buf, true, le)
		if !ok || off != 16 {
			t.Fatalf("readIFD0Offset(bigtiff): off=%d ok=%v, want 16/true", off, ok)
		}
		entry, found := findEntryInIFD(buf, off, 0x0100, true, le)
		if !found || entry.typ != 4 || entry.count != 1 {
			t.Fatalf("findEntryInIFD(bigtiff): found=%v entry=%+v", found, entry)
		}

		// S-40 regression proof: interpreting this SAME buffer with bigTIFF=false
		// must NOT find the entry at the classic offset-8 position (which holds
		// unrelated bytes in the BigTIFF layout) — proving the two layouts are
		// never conflated.
		if off8, ok8 := readIFD0Offset(buf, false, le); ok8 {
			if _, found8 := findEntryInIFD(buf, off8, 0x0100, false, le); found8 {
				t.Fatalf("S-40 violation: classic-width scan of a BigTIFF buffer found tag 0x0100 at off=%d — layouts conflated", off8)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// S-41: LONG8-typed strip/tile arrays
// ---------------------------------------------------------------------------

// TestConformance_S41_bigtiff_long8_strip_arrays round-trips real BigTIFF
// fixtures whose StripOffsets/StripByteCounts (or TileOffsets/TileByteCounts)
// are declared as LONG8 (type 16), proving the relocator no longer returns
// ErrUnsupportedOffsetType for them and the strip/tile byte content survives
// the relocation unchanged.
func TestConformance_S41_bigtiff_long8_strip_arrays(t *testing.T) {
	t.Parallel()

	fixtures := []string{
		filepath.Join("testdata", "corpus", "tiff", "metadata-extractor", "BigTIFFLong8.tif"),
		filepath.Join("testdata", "corpus", "tiff", "metadata-extractor", "BigTIFFLong8Tiles.tif"),
		filepath.Join("testdata", "big_cramps_le.tif"),
		filepath.Join("testdata", "big_cramps_be.tif"),
	}

	for _, path := range fixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					t.Skipf("fixture %s not present", path)
				}
				t.Fatalf("read %s: %v", path, err)
			}
			assertBigTIFFRoundTrip(t, path, data)
		})
	}
}

// ---------------------------------------------------------------------------
// S-42: source element width preserved on write
// ---------------------------------------------------------------------------

// TestConformance_S42_element_width_preserved verifies that relocating
// BigTIFFLong8.tif (StripOffsets/StripByteCounts declared LONG8) produces an
// output whose StripOffsets/StripByteCounts entries are STILL type LONG8 —
// not silently downgraded to LONG.
func TestConformance_S42_element_width_preserved(t *testing.T) {
	t.Parallel()
	path := filepath.Join("testdata", "corpus", "tiff", "metadata-extractor", "BigTIFFLong8.tif")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("fixture %s not present", path)
		}
		t.Fatalf("read %s: %v", path, err)
	}

	out := relocateForTest(t, data)

	origE, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("Parse(orig): %v", err)
	}
	resultE, err := exif.Parse(out)
	if err != nil {
		t.Fatalf("Parse(result): %v", err)
	}

	origOff := origE.IFD0.Get(exif.TagStripOffsets)
	resultOff := resultE.IFD0.Get(exif.TagStripOffsets)
	if origOff == nil || resultOff == nil {
		t.Fatalf("StripOffsets missing: orig=%v result=%v", origOff, resultOff)
	}
	if origOff.Type != exif.TypeLong8 {
		t.Fatalf("test invariant broken: fixture StripOffsets type = %v, want TypeLong8", origOff.Type)
	}
	if resultOff.Type != exif.TypeLong8 {
		t.Errorf("S-42 violation: StripOffsets downgraded from TypeLong8 to %v on relocation", resultOff.Type)
	}
	origCnt := origE.IFD0.Get(exif.TagStripByteCounts)
	resultCnt := resultE.IFD0.Get(exif.TagStripByteCounts)
	if origCnt == nil || resultCnt == nil {
		t.Fatalf("StripByteCounts missing: orig=%v result=%v", origCnt, resultCnt)
	}
	if resultCnt.Type != exif.TypeLong8 {
		t.Errorf("S-42 violation: StripByteCounts downgraded from TypeLong8 to %v on relocation", resultCnt.Type)
	}
}

// ---------------------------------------------------------------------------
// S-43: SubIFDs (0x014A) type-code variants
// ---------------------------------------------------------------------------

// TestConformance_S43_subifd_pointer_type_variants round-trips the two
// BigTIFF SubIFD fixtures that exercise the type 13 (IFD) / type 18 (IFD8)
// distinction for tag 0x014A.
func TestConformance_S43_subifd_pointer_type_variants(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		path     string
		wantType exif.DataType
	}{
		{filepath.Join("testdata", "corpus", "tiff", "metadata-extractor", "BigTIFFSubIFD4.tif"), exif.DataType(13)},
		{filepath.Join("testdata", "corpus", "tiff", "metadata-extractor", "BigTIFFSubIFD8.tif"), exif.TypeIFD8},
	}

	for _, fx := range fixtures {
		t.Run(filepath.Base(fx.path), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(fx.path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					t.Skipf("fixture %s not present", fx.path)
				}
				t.Fatalf("read %s: %v", fx.path, err)
			}

			// Confirm the fixture actually exercises the type code under test
			// (test invariant, not a library assertion).
			origE, err := exif.Parse(data)
			if err != nil {
				t.Fatalf("Parse(orig): %v", err)
			}
			subEntry := origE.IFD0.Get(exif.TagSubIFDs)
			if subEntry == nil {
				t.Fatalf("test invariant broken: fixture has no 0x014A entry")
			}
			if subEntry.Type != fx.wantType {
				t.Fatalf("test invariant broken: fixture 0x014A type = %v, want %v", subEntry.Type, fx.wantType)
			}

			assertBigTIFFRoundTrip(t, fx.path, data)

			// S-43: the relocated 0x014A entry must still declare the SAME
			// type code — exif.Encode passes it through verbatim; the
			// relocator only patches the value bytes (patchSubIFDPointers).
			out := relocateForTest(t, data)
			resultE, err := exif.Parse(out)
			if err != nil {
				t.Fatalf("Parse(result): %v", err)
			}
			resultSubEntry := resultE.IFD0.Get(exif.TagSubIFDs)
			if resultSubEntry == nil {
				t.Fatalf("S-43 violation: 0x014A entry missing from relocated output")
			}
			if resultSubEntry.Type != fx.wantType {
				t.Errorf("S-43 violation: 0x014A type changed from %v to %v on relocation", fx.wantType, resultSubEntry.Type)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// S-42/S-43 synthetic fixtures (corpus-independent, always run)
// ---------------------------------------------------------------------------
//
// The corpus-backed tests above (TestConformance_S42_element_width_preserved,
// TestConformance_S43_subifd_pointer_type_variants) exercise real-world
// files but SKIP when testdata/corpus is absent (testdata/corpus/** is
// gitignored — see docs/TESTING.md §2.1's "corpus absent" allowed-skip
// category). The synthetic fixtures below give S-42/S-43 guaranteed,
// corpus-independent coverage, built by hand using this package's own width
// primitives (ifdWidths/elemSizeFor), mirroring the existing house
// convention of hand-crafted byte buffers used throughout this test suite
// (e.g. buildTIFFWithHugeSubIFDCount, buildMultiStripTIFFN).

// putInlineField writes v into field (exactly 4 or 8 bytes, the width
// returned by elemSizeFor) left-justified, matching the same "left-justified,
// zero-padded" inline convention documented in decodeOffsetArray.
func putInlineField(field []byte, width int, order binary.ByteOrder, v uint64) {
	switch width {
	case 8:
		order.PutUint64(field, v)
	default: // 4 (LONG/IFD)
		order.PutUint32(field[:4], uint32(v)) //nolint:gosec // G115: test helper; v is always a small offset/count in these synthetic fixtures
	}
}

// writeBigTIFFEntry writes one 20-byte BigTIFF IFD entry at buf[pos:pos+20]
// with an INLINE scalar value (count=1; every type used by this file's
// synthetic fixtures has elemSizeFor(typ,true) <= 8, so count=1 is always
// inline per the BigTIFF spec §2 threshold).
func writeBigTIFFEntry(buf []byte, pos int, tag, typ uint16, order binary.ByteOrder, inlineVal uint64) {
	order.PutUint16(buf[pos:], tag)
	order.PutUint16(buf[pos+2:], typ)
	order.PutUint64(buf[pos+4:], 1)      // count = 1
	width := int(elemSizeFor(typ, true)) //nolint:gosec // G115: elemSizeFor(_, true) returns 0, 1, 2, 4, or 8 — always fits int
	putInlineField(buf[pos+12:pos+20], width, order, inlineVal)
}

// buildSyntheticBigTIFF hand-crafts a minimal, valid, complete BigTIFF file
// (16-byte header, 20-byte entries) for S-42/S-43 coverage:
//
//   - IFD0: ImageWidth (0x0100, LONG, a stable non-relocatable tag proving
//     the generic per-tag byte-identity sweep still runs), StripOffsets/
//     StripByteCounts (0x0111/0x0117) using stripType, and — when
//     subIFDType != 0 — a SubIFDs (0x014A) entry of that type pointing at a
//     child IFD with its own LONG StripOffsets/StripByteCounts pair.
//   - Real strip payload bytes placed after all IFD structures, so
//     relocation genuinely moves them (proving S-41/R-18-style byte-content
//     preservation alongside the type-preservation checks in S-42/S-43).
//
// stripType must be exif.TypeLong (4) or exif.TypeLong8 (16).
// subIFDType may be 0 (no SubIFD), exif.TypeLong (4), 13 (IFD), 16 (LONG8),
// or exif.TypeIFD8 (18).
func buildSyntheticBigTIFF(order binary.ByteOrder, stripType, subIFDType uint16) []byte {
	const headerLen = 16
	hasSubIFD := subIFDType != 0

	ifd0EntryCount := 3
	if hasSubIFD {
		ifd0EntryCount = 4
	}
	ifd0Len := 8 + 20*ifd0EntryCount + 8
	ifd0Off := headerLen
	ifd0End := ifd0Off + ifd0Len

	childOff := 0
	childEnd := ifd0End
	const childEntryCount = 2
	childLen := 8 + 20*childEntryCount + 8
	if hasSubIFD {
		childOff = ifd0End
		childEnd = childOff + childLen
	}

	stripData := []byte("STRIP-DATA-1")
	stripOff := childEnd
	stripEnd := stripOff + len(stripData)

	var childStripData []byte
	childStripOff := 0
	total := stripEnd
	if hasSubIFD {
		childStripData = []byte("SUB-STRIP-DATA")
		childStripOff = stripEnd
		total = childStripOff + len(childStripData)
	}

	buf := make([]byte, total)

	// Header.
	buf[0], buf[1] = 'I', 'I'
	if order == binary.BigEndian {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002B)
	order.PutUint16(buf[4:], 8)
	order.PutUint16(buf[6:], 0)
	order.PutUint64(buf[8:], uint64(ifd0Off))

	// IFD0.
	order.PutUint64(buf[ifd0Off:], uint64(ifd0EntryCount))
	pos := ifd0Off + 8
	writeBigTIFFEntry(buf, pos, 0x0100, uint16(exif.TypeLong), order, 1234)
	pos += 20
	writeBigTIFFEntry(buf, pos, uint16(exif.TagStripOffsets), stripType, order, uint64(stripOff))
	pos += 20
	writeBigTIFFEntry(buf, pos, uint16(exif.TagStripByteCounts), stripType, order, uint64(len(stripData)))
	pos += 20
	if hasSubIFD {
		writeBigTIFFEntry(buf, pos, uint16(exif.TagSubIFDs), subIFDType, order, uint64(childOff))
		pos += 20
	}
	order.PutUint64(buf[pos:], 0) // next-IFD = 0

	// Child SubIFD.
	if hasSubIFD {
		order.PutUint64(buf[childOff:], childEntryCount)
		cpos := childOff + 8
		writeBigTIFFEntry(buf, cpos, uint16(exif.TagStripOffsets), uint16(exif.TypeLong), order, uint64(childStripOff))
		cpos += 20
		writeBigTIFFEntry(buf, cpos, uint16(exif.TagStripByteCounts), uint16(exif.TypeLong), order, uint64(len(childStripData)))
		cpos += 20
		order.PutUint64(buf[cpos:], 0) // next-IFD = 0
	}

	copy(buf[stripOff:stripEnd], stripData)
	if hasSubIFD {
		copy(buf[childStripOff:], childStripData)
	}
	return buf
}

// TestConformance_S42_synthetic_element_width_preserved is the
// corpus-independent counterpart of TestConformance_S42_element_width_preserved.
func TestConformance_S42_synthetic_element_width_preserved(t *testing.T) {
	t.Parallel()

	data := buildSyntheticBigTIFF(binary.LittleEndian, uint16(exif.TypeLong8), 0)
	assertBigTIFFRoundTrip(t, "synthetic-long8-strip", data)

	out := relocateForTest(t, data)
	resultE, err := exif.Parse(out)
	if err != nil {
		t.Fatalf("Parse(result): %v", err)
	}
	resultOff := resultE.IFD0.Get(exif.TagStripOffsets)
	resultCnt := resultE.IFD0.Get(exif.TagStripByteCounts)
	if resultOff == nil || resultCnt == nil {
		t.Fatalf("StripOffsets/StripByteCounts missing from result")
	}
	if resultOff.Type != exif.TypeLong8 {
		t.Errorf("S-42 violation: StripOffsets downgraded from TypeLong8 to %v on relocation", resultOff.Type)
	}
	if resultCnt.Type != exif.TypeLong8 {
		t.Errorf("S-42 violation: StripByteCounts downgraded from TypeLong8 to %v on relocation", resultCnt.Type)
	}
}

// TestConformance_S43_synthetic_subifd_pointer_type_variants is the
// corpus-independent counterpart of TestConformance_S43_subifd_pointer_type_variants,
// covering all four legitimate 0x014A type codes: LONG (4), IFD (13, the
// EXIF-3.0/TIFF-Extension collision case), LONG8 (16), and IFD8 (18).
func TestConformance_S43_synthetic_subifd_pointer_type_variants(t *testing.T) {
	t.Parallel()

	types := []struct {
		name string
		typ  uint16
	}{
		{"LONG", uint16(exif.TypeLong)},
		{"IFD", 13},
		{"LONG8", uint16(exif.TypeLong8)},
		{"IFD8", uint16(exif.TypeIFD8)},
	}
	for _, tc := range types {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := buildSyntheticBigTIFF(binary.LittleEndian, uint16(exif.TypeLong), tc.typ)

			origE, err := exif.Parse(data)
			if err != nil {
				t.Fatalf("Parse(orig): %v", err)
			}
			subEntry := origE.IFD0.Get(exif.TagSubIFDs)
			if subEntry == nil || subEntry.Type != exif.DataType(tc.typ) {
				t.Fatalf("test invariant broken: 0x014A entry = %v, want type %d", subEntry, tc.typ)
			}

			assertBigTIFFRoundTrip(t, "synthetic-subifd-"+tc.name, data)

			out := relocateForTest(t, data)
			resultE, err := exif.Parse(out)
			if err != nil {
				t.Fatalf("Parse(result): %v", err)
			}
			resultSubEntry := resultE.IFD0.Get(exif.TagSubIFDs)
			if resultSubEntry == nil {
				t.Fatalf("S-43 violation: 0x014A entry missing from relocated output")
			}
			if resultSubEntry.Type != exif.DataType(tc.typ) {
				t.Errorf("S-43 violation: 0x014A type changed from %d to %v on relocation", tc.typ, resultSubEntry.Type)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R-18: round-trip fidelity, curated fixtures + corpus-wide scan
// ---------------------------------------------------------------------------

// TestConformance_R18_bigtiff_roundtrip_fidelity_curated exercises the full
// task #264 BigTIFF fixture family (LONG, LONG8, LONG8-tiled, SubIFD4,
// SubIFD8, Motorola/BE variants) plus the exiftool BigTIFF_LE/BE.tif and the
// libtiff-derived big_cramps fixtures explicitly named in the task brief.
func TestConformance_R18_bigtiff_roundtrip_fidelity_curated(t *testing.T) {
	t.Parallel()

	names := []string{
		"BigTIFF.tif", "BigTIFF_2.tif",
		"BigTIFFLong.tif", "BigTIFFLong_2.tif",
		"BigTIFFLong8.tif", "BigTIFFLong8_2.tif",
		"BigTIFFLong8Tiles.tif", "BigTIFFLong8Tiles_2.tif",
		"BigTIFFMotorola.tif", "BigTIFFMotorola_2.tif",
		"BigTIFFMotorolaLongStrips.tif", "BigTIFFMotorolaLongStrips_2.tif",
		"BigTIFFSubIFD4.tif", "BigTIFFSubIFD4_2.tif",
		"BigTIFFSubIFD8.tif", "BigTIFFSubIFD8_2.tif",
	}
	for _, name := range names {
		path := filepath.Join("testdata", "corpus", "tiff", "metadata-extractor", name)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					t.Skipf("fixture %s not present", path)
				}
				t.Fatalf("read %s: %v", path, err)
			}
			assertBigTIFFRoundTrip(t, path, data)
		})
	}

	extra := []string{
		filepath.Join("testdata", "corpus", "tiff", "exiftool", "BigTIFF_LE.tif"),
		filepath.Join("testdata", "corpus", "tiff", "exiftool", "BigTIFF_BE.tif"),
		filepath.Join("testdata", "big_cramps_le.tif"),
		filepath.Join("testdata", "big_cramps_be.tif"),
	}
	for _, path := range extra {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					t.Skipf("fixture %s not present", path)
				}
				t.Fatalf("read %s: %v", path, err)
			}
			assertBigTIFFRoundTrip(t, path, data)
		})
	}
}

// TestConformance_R18_bigtiff_roundtrip_fidelity_corpus scans the entire tiff
// corpus for any file that parses with EXIF.BigTIFF == true (mirrors the
// task #264 corpus-wide R-14 test, at the container-relocation layer instead
// of the EXIF-blob-encode layer) and proves the same round-trip fidelity.
func TestConformance_R18_bigtiff_roundtrip_fidelity_corpus(t *testing.T) {
	t.Parallel()
	paths := testutil.CorpusFiles(t, "tiff")

	found := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		e, err := exif.Parse(data)
		if err != nil || !e.BigTIFF {
			continue
		}
		found++
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			assertBigTIFFRoundTrip(t, path, data)
		})
	}
	if found == 0 {
		t.Skip("no BigTIFF files found in testdata/corpus/tiff/**")
	}
}

// ---------------------------------------------------------------------------
// R-19: MakerNote + BigTIFF documented fail-safe no-op
// ---------------------------------------------------------------------------

// TestConformance_R19_makernote_bigtiff_failsafe_noop proves that
// rebaseGenericMakerNote is a byte-for-byte no-op when e.BigTIFF is true,
// even when a Sony-plain-IFD-shaped MakerNote blob is present — the
// documented fail-safe deferral (relocate_makernote.go), not an accident of
// the classic-only scan happening to fail closed.
func TestConformance_R19_makernote_bigtiff_failsafe_noop(t *testing.T) {
	t.Parallel()

	// A Sony plain-IFD MakerNote is just a small plain TIFF IFD with a
	// plausible entry count (isSonyPlainIFDMakerNote's heuristic: count in
	// (0,4096) and no recognised maker-prefix).
	le := binary.LittleEndian
	// count(2) + one 12-byte entry [tag(2)+type(2)+count(4)+valOrOff(4)] = 14.
	mn := make([]byte, 14)
	le.PutUint16(mn[0:], 1) // 1 entry: implausible-looking but within heuristic bounds
	le.PutUint16(mn[2:], 0x0001)
	le.PutUint16(mn[4:], 3) // SHORT
	le.PutUint32(mn[6:], 1) // count=1 (inline)
	le.PutUint16(mn[10:], 7)

	finalTIFF := make([]byte, 64)
	copy(finalTIFF, []byte{'I', 'I'})
	le.PutUint16(finalTIFF[2:], 0x002B)
	le.PutUint16(finalTIFF[4:], 8)
	le.PutUint16(finalTIFF[6:], 0)
	le.PutUint64(finalTIFF[8:], 16)

	before := make([]byte, len(finalTIFF))
	copy(before, finalTIFF)

	e := &exif.EXIF{
		BigTIFF:         true,
		MakerNote:       mn,
		MakerNoteOffset: 999, // non-zero so the (declined) function would otherwise proceed
		ExifIFD:         &exif.IFD{},
	}

	// Must not panic, and must not modify finalTIFF at all.
	rebaseGenericMakerNote(finalTIFF, e, le)

	if !bytes.Equal(finalTIFF, before) {
		t.Errorf("R-19 violation: rebaseGenericMakerNote modified finalTIFF when e.BigTIFF=true (want byte-for-byte no-op)")
	}
}

// ---------------------------------------------------------------------------
// V-15: uint64 arithmetic throughout BigTIFF relocation
// ---------------------------------------------------------------------------

// TestConformance_V15_uint64_relocation_arithmetic proves docs/conformance/
// exif-tiff.md rule V-15: every offset/size value flowing through the
// copy-and-relocate serializer's image-block and SubIFD bookkeeping
// (imageBlock.srcOffset/size/newOffset, subIFDInfo.srcOffset/newOffset,
// readUint, decodeOffsetArray) is carried in uint64, so a value legitimately
// sourced from a LONG8/IFD8 (8-byte) field is never pre-truncated to uint32
// at any intermediate step.
//
// V-15 is otherwise only exercised INDIRECTLY, by S-41/S-42/R-18 (see
// docs/conformance/exif-tiff.md §2.8) — this test gives the rule its own
// dedicated, directly-named sub-tests:
//
//   - "primitive": readUint's elemSz=8 branch, exercised with a synthetic
//     8-byte value ABOVE math.MaxUint32 — the one case S-41/S-42/R-18 cannot
//     reach through any real corpus fixture, because this package's own
//     maxFileSize cap (256 MiB, far under 4 GiB) means no legitimate
//     real-world file this package will ever process has a strip/SubIFD
//     offset that large (see V-15's own rationale in exif-tiff.md). A naive
//     uint32(order.Uint64(b)) truncation inside readUint would silently drop
//     the high bits and this sub-test would fail.
//   - "fixture": real BigTIFF fixtures with LONG8-typed StripOffsets/
//     StripByteCounts (task #264/#270) round-trip through the full
//     relocateTIFFFromParsed pipeline with every strip's exact 64-bit
//     offset/size decoded via this package's own decodeOffsetArray (the same
//     elemSz=8 code path production uses) and its content byte-identical at
//     the new position. Includes both the always-committed big_cramps_le/be.tif
//     (libtiff-derived, guaranteeing this rule is never skip-only in a normal
//     checkout) and the metadata-extractor corpus fixtures, which skip
//     gracefully when the corpus is absent, per docs/TESTING.md §2.1's
//     "corpus absent" allowed-skip category.
func TestConformance_V15_uint64_relocation_arithmetic(t *testing.T) {
	t.Parallel()

	t.Run("V-15/primitive-readUint-elemSz8-above-uint32-range", func(t *testing.T) {
		t.Parallel()
		const want uint64 = 0x1_2345_6789 // 4,886,718,345 — exceeds math.MaxUint32 (4,294,967,295).
		if want <= math.MaxUint32 {
			t.Fatalf("test invariant broken: want=%d must exceed math.MaxUint32=%d", want, uint32(math.MaxUint32))
		}

		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			buf := make([]byte, 8)
			order.PutUint64(buf, want)

			got, err := readUint(buf, 8, order)
			if err != nil {
				t.Fatalf("readUint(order=%v): %v", order, err)
			}
			if got != want {
				t.Errorf("V-15 violation: readUint(elemSz=8, order=%v) = %d, want %d (value was truncated to uint32 range: %d)",
					order, got, want, uint32(got)) //nolint:gosec // G115: intentional truncation to DEMONSTRATE the failure mode in the error message, not production code
			}
		}
	})

	fixtures := []string{
		filepath.Join("testdata", "big_cramps_le.tif"),
		filepath.Join("testdata", "big_cramps_be.tif"),
		filepath.Join("testdata", "corpus", "tiff", "metadata-extractor", "BigTIFFLong8.tif"),
		filepath.Join("testdata", "corpus", "tiff", "metadata-extractor", "BigTIFFLong8Tiles.tif"),
	}
	for _, path := range fixtures {
		t.Run("V-15/fixture-long8-offset-and-size-preserved-end-to-end/"+filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			assertV15LONG8OffsetsPreserved(t, path)
		})
	}
}

// assertV15LONG8OffsetsPreserved is the V-15 fixture workhorse: loads path
// (skipping gracefully if absent), decodes its LONG8-typed StripOffsets/
// StripByteCounts pair via this package's own decodeOffsetArray, relocates it
// through relocateTIFFFromParsed, decodes the relocated pair the same way,
// and proves every strip's exact 64-bit offset/size arithmetic and byte
// content survive unchanged (docs/conformance/exif-tiff.md V-15).
func assertV15LONG8OffsetsPreserved(t *testing.T, path string) { //nolint:cyclop // sequential decode/compare steps mirror assertBigTIFFRoundTrip's structure; splitting further would obscure the single V-15 comparison
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("fixture %s not present", path)
		}
		t.Fatalf("read %s: %v", path, err)
	}

	e, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("Parse(orig): %v", err)
	}
	if !e.BigTIFF {
		t.Fatalf("test invariant broken: fixture is not BigTIFF")
	}
	order := e.ByteOrder

	origIFD0Off, ok := readIFD0Offset(data, true, order)
	if !ok {
		t.Fatalf("readIFD0Offset(orig) failed")
	}
	origOff, ok := findEntryInIFD(data, origIFD0Off, uint16(exif.TagStripOffsets), true, order)
	if !ok {
		t.Fatalf("test invariant broken: fixture has no StripOffsets entry")
	}
	origCnt, ok := findEntryInIFD(data, origIFD0Off, uint16(exif.TagStripByteCounts), true, order)
	if !ok {
		t.Fatalf("test invariant broken: fixture has no StripByteCounts entry")
	}
	offElemSz := elemSizeFor(origOff.typ, true)
	if offElemSz != 8 {
		t.Fatalf("test invariant broken: fixture StripOffsets element size = %d, want 8 (TypeLong8)", offElemSz)
	}
	cntElemSz := elemSizeFor(origCnt.typ, true)

	origOffsets, ok1 := decodeOffsetArray(data, origOff.valField, origOff.count, offElemSz, true, order)
	origSizes, ok2 := decodeOffsetArray(data, origCnt.valField, origCnt.count, cntElemSz, true, order)
	if !ok1 || !ok2 {
		t.Fatalf("decodeOffsetArray(orig) failed (offsets ok=%v, sizes ok=%v)", ok1, ok2)
	}
	if len(origOffsets) != len(origSizes) {
		t.Fatalf("test invariant broken: StripOffsets count=%d != StripByteCounts count=%d", len(origOffsets), len(origSizes))
	}

	out := relocateForTest(t, data)

	resultE, err := exif.Parse(out)
	if err != nil {
		t.Fatalf("Parse(result): %v", err)
	}
	if !resultE.BigTIFF {
		t.Errorf("V-15 violation: relocated output is no longer BigTIFF-provenanced")
	}
	resultIFD0Off, ok := readIFD0Offset(out, true, order)
	if !ok {
		t.Fatalf("readIFD0Offset(result) failed")
	}
	resultOff, ok := findEntryInIFD(out, resultIFD0Off, uint16(exif.TagStripOffsets), true, order)
	if !ok {
		t.Fatalf("V-15 violation: relocated output has no StripOffsets entry")
	}
	resultCnt, ok := findEntryInIFD(out, resultIFD0Off, uint16(exif.TagStripByteCounts), true, order)
	if !ok {
		t.Fatalf("V-15 violation: relocated output has no StripByteCounts entry")
	}
	resultOffElemSz := elemSizeFor(resultOff.typ, true)
	if resultOffElemSz != 8 {
		t.Errorf("V-15 violation: relocated StripOffsets element size = %d, want 8 (TypeLong8 downgraded)", resultOffElemSz)
	}

	resultOffsets, ok3 := decodeOffsetArray(out, resultOff.valField, resultOff.count, resultOffElemSz, true, order)
	resultSizes, ok4 := decodeOffsetArray(out, resultCnt.valField, resultCnt.count, elemSizeFor(resultCnt.typ, true), true, order)
	if !ok3 || !ok4 {
		t.Fatalf("decodeOffsetArray(result) failed (offsets ok=%v, sizes ok=%v)", ok3, ok4)
	}

	n := min(len(origOffsets), len(resultOffsets))
	if n == 0 {
		t.Fatalf("test invariant broken: fixture has zero strips")
	}
	for i := range n {
		if origSizes[i] != resultSizes[i] {
			t.Errorf("V-15 violation: strip[%d] size changed across relocation: orig=%d result=%d", i, origSizes[i], resultSizes[i])
			continue
		}
		size := resultSizes[i]
		// uint64 arithmetic: if any intermediate step in the relocation
		// pipeline had narrowed newOffset/size to uint32, a source file large
		// enough to matter would see this sum wrap around; the addition and
		// the bounds check below run entirely in uint64, exercising the exact
		// arithmetic V-15 requires.
		end := resultOffsets[i] + size
		if end > uint64(len(out)) {
			t.Errorf("V-15 violation: strip[%d] relocated offset=%d size=%d end=%d exceeds output length %d",
				i, resultOffsets[i], size, end, len(out))
			continue
		}
		origEnd := origOffsets[i] + size
		if origEnd > uint64(len(data)) {
			t.Fatalf("test invariant broken: strip[%d] original offset=%d size=%d exceeds input length %d", i, origOffsets[i], size, len(data))
		}
		if !bytes.Equal(data[origOffsets[i]:origEnd], out[resultOffsets[i]:end]) {
			t.Errorf("V-15 violation: strip[%d] content changed across relocation (LONG8 offset/size decode or arithmetic mismatch)", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// relocateForTest runs data through tiff.Inject's copy-and-relocate path
// (forcing it via a non-nil IPTC payload) and returns the resulting bytes,
// failing the test on any error.
func relocateForTest(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, forceRelocateIPTC, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return out.Bytes()
}

// assertBigTIFFRoundTrip is the R-18 workhorse: relocates data (forcing the
// copy-and-relocate path via a metadata change), re-parses the result, and
// asserts:
//   - the output is still BigTIFF (magic 0x002B preserved for both byte orders);
//   - every IFD0 tag present in the original EXCEPT the relocatable
//     offset/bytecount/SubIFDs tags decodes to an identical (Type, Count,
//     Value) in the result;
//   - StripOffsets/StripByteCounts and TileOffsets/TileByteCounts arrays have
//     identical Count and byte-identical block CONTENT at their (possibly
//     relocated) positions;
//   - every SubIFD referenced by 0x014A round-trips the same way, recursively.
//
// The comparison walks RAW bytes via this package's own findEntryInIFD/
// readRawEntryAt/decodeOffsetArray primitives — deliberately NOT via
// exif.Parse's *exif.IFDEntry.Value — because exif.Parse follows CIPA
// DC-008-2023 §4.6.3 for type code 13 (TypeUTF8, 1 byte/element), which
// mis-sizes a 0x014A entry declared as the TIFF-Extension "IFD" type (also
// code 13, 4 bytes/element; see typeSize's doc comment in tiff.go). Using the
// same raw primitives the production relocator itself uses (relocate_bigtiff.go)
// keeps this verification correct for every type-13 SubIFD fixture while
// remaining an independent check of the WRITE output (relocateTIFFFromParsed's
// full pipeline produced resultBuf; this function only READS both buffers).
func assertBigTIFFRoundTrip(t *testing.T, label string, data []byte) {
	t.Helper()

	out := relocateForTest(t, data)

	e, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("%s: Parse(orig): %v", label, err)
	}
	if !e.BigTIFF {
		t.Fatalf("%s: test invariant broken: fixture is not BigTIFF", label)
	}
	order := e.ByteOrder
	resultE, err := exif.Parse(out)
	if err != nil {
		t.Fatalf("%s: Parse(result): %v", label, err)
	}
	if !resultE.BigTIFF {
		t.Errorf("%s: R-18 violation: relocated output is no longer BigTIFF-provenanced", label)
	}

	origIFD0Off, ok1 := readIFD0Offset(data, true, order)
	resultIFD0Off, ok2 := readIFD0Offset(out, true, order)
	if !ok1 || !ok2 {
		t.Fatalf("%s: readIFD0Offset failed (orig ok=%v, result ok=%v)", label, ok1, ok2)
	}

	compareIFDRoundTrip(t, label+" IFD0", data, origIFD0Off, out, resultIFD0Off, true, order)
}

// ignoredRoundTripTags are tags whose VALUE is expected to legitimately
// change across a relocate (image-block offsets move) or whose presence is
// the deliberate purpose of the relocate call in this test file (the forced
// IPTC payload). They are still checked for structural sanity elsewhere
// (S-41/S-42/S-43, byte-content checks below) — this list only controls the
// generic "every other tag is byte-identical" sweep.
var ignoredRoundTripTags = map[uint16]bool{ //nolint:gochecknoglobals // test-only lookup table, never mutated
	uint16(exif.TagStripOffsets):                true,
	uint16(exif.TagStripByteCounts):             true,
	uint16(exif.TagTileOffsets):                 true,
	uint16(exif.TagTileByteCounts):              true,
	uint16(exif.TagSubIFDs):                     true,
	uint16(exif.TagIPTC):                        true, // forceRelocateIPTC deliberately upserts this
	uint16(exif.TagJPEGInterchangeFormat):       true, // may relocate (main-image JPEG-in-TIFF)
	uint16(exif.TagJPEGInterchangeFormatLength): true,
}

// compareIFDRoundTrip performs the R-18 comparison for a single IFD (IFD0 or
// a SubIFD), recursing into any 0x014A SubIFDs pointer array. bigTIFF is
// always true when reached from assertBigTIFFRoundTrip's top-level call
// (S-40: BigTIFF is a whole-file container decision, so every nested SubIFD
// inherits the same 20-byte-entry/8-byte-field shape).
func compareIFDRoundTrip(t *testing.T, label string, origBuf []byte, origIFDOff uint64, resultBuf []byte, resultIFDOff uint64, bigTIFF bool, order binary.ByteOrder) { //nolint:cyclop // sequential per-tag comparisons; complexity is inherent to an exhaustive structural diff
	t.Helper()

	origCount, origPos, ok1 := ifdEntryTable(origBuf, origIFDOff, bigTIFF, order)
	resultCount, resultPos, ok2 := ifdEntryTable(resultBuf, resultIFDOff, bigTIFF, order)
	if !ok1 || !ok2 {
		t.Errorf("%s: ifdEntryTable failed (orig ok=%v @%d, result ok=%v @%d)", label, ok1, origIFDOff, ok2, resultIFDOff)
		return
	}
	_, entryWidth, _ := ifdWidths(bigTIFF)
	entryWidth64 := uint64(entryWidth) //nolint:gosec // G115: entryWidth is the compile-time constant 12 or 20 from ifdWidths, never negative

	origEntries := make(map[uint16]rawIFDEntry, origCount)
	for i := range origCount {
		e, ok := readRawEntryAt(origBuf, origPos+i*entryWidth64, bigTIFF, order)
		if !ok {
			break
		}
		origEntries[e.tag] = e
	}
	resultEntries := make(map[uint16]rawIFDEntry, resultCount)
	for i := range resultCount {
		e, ok := readRawEntryAt(resultBuf, resultPos+i*entryWidth64, bigTIFF, order)
		if !ok {
			break
		}
		resultEntries[e.tag] = e
	}

	for tag, oe := range origEntries {
		if ignoredRoundTripTags[tag] {
			continue
		}
		re, found := resultEntries[tag]
		if !found {
			t.Errorf("%s: tag 0x%04X present in original but missing from result", label, tag)
			continue
		}
		if oe.typ != re.typ || oe.count != re.count {
			t.Errorf("%s: tag 0x%04X: type/count changed: orig(type=%d,count=%d) result(type=%d,count=%d)",
				label, tag, oe.typ, oe.count, re.typ, re.count)
			continue
		}
		oVal, valOk1 := dereferenceEntryValue(origBuf, oe, bigTIFF, order)
		rVal, valOk2 := dereferenceEntryValue(resultBuf, re, bigTIFF, order)
		if !valOk1 || !valOk2 {
			t.Errorf("%s: tag 0x%04X: failed to dereference value (orig ok=%v result ok=%v)", label, tag, valOk1, valOk2)
			continue
		}
		if !bytes.Equal(oVal, rVal) {
			t.Errorf("%s: tag 0x%04X: value bytes changed (orig=% X result=% X)", label, tag, oVal, rVal)
		}
	}

	compareOffsetBlockContent(t, label, origBuf, origEntries, resultBuf, resultEntries, uint16(exif.TagStripOffsets), uint16(exif.TagStripByteCounts), bigTIFF, order)
	compareOffsetBlockContent(t, label, origBuf, origEntries, resultBuf, resultEntries, uint16(exif.TagTileOffsets), uint16(exif.TagTileByteCounts), bigTIFF, order)

	// Recurse into SubIFDs (0x014A), if present, comparing pointer-array
	// length and then the pointed-to IFDs themselves.
	origSub, origHasSub := origEntries[uint16(exif.TagSubIFDs)]
	if !origHasSub {
		return
	}
	resultSub, resultHasSub := resultEntries[uint16(exif.TagSubIFDs)]
	if !resultHasSub {
		t.Errorf("%s: 0x014A present in original but missing from result", label)
		return
	}
	if origSub.count != resultSub.count {
		t.Errorf("%s: 0x014A count changed: orig=%d result=%d", label, origSub.count, resultSub.count)
		return
	}

	origElemSz := elemSizeFor(origSub.typ, bigTIFF)
	resultElemSz := elemSizeFor(resultSub.typ, bigTIFF)
	origOffsets, ok1 := decodeOffsetArray(origBuf, origSub.valField, origSub.count, origElemSz, bigTIFF, order)
	resultOffsets, ok2 := decodeOffsetArray(resultBuf, resultSub.valField, resultSub.count, resultElemSz, bigTIFF, order)
	if !ok1 || !ok2 {
		t.Errorf("%s: 0x014A decodeOffsetArray failed (orig ok=%v result ok=%v)", label, ok1, ok2)
		return
	}

	n := min(len(origOffsets), len(resultOffsets))
	for i := range n {
		subLabel := label + "/SubIFD[" + strconv.Itoa(i) + "]"
		compareIFDRoundTrip(t, subLabel, origBuf, origOffsets[i], resultBuf, resultOffsets[i], bigTIFF, order)
	}
}

// dereferenceEntryValue returns the raw value bytes of entry e within buf:
// the inline field bytes (trimmed to the true total, since unused trailing
// bytes are zero-padding, not value) when inline, or the out-of-line value
// area when total exceeds the container's inline threshold. Returns the raw
// 4/8-byte field verbatim for an unrecognised type (mirrors V-14's "opaque
// raw field" round-trip policy for unknown types).
func dereferenceEntryValue(buf []byte, e rawIFDEntry, bigTIFF bool, order binary.ByteOrder) ([]byte, bool) {
	elemSz := elemSizeFor(e.typ, bigTIFF)
	if elemSz == 0 {
		return e.valField, true // unknown type: opaque field, verbatim
	}
	total := elemSz * e.count
	if total <= inlineThreshold(bigTIFF) {
		if total > uint64(len(e.valField)) {
			return nil, false
		}
		return e.valField[:total], true
	}
	off := fieldAsU64(e.valField, bigTIFF, order)
	if off+total > uint64(len(buf)) {
		return nil, false
	}
	return buf[off : off+total], true
}

// compareOffsetBlockContent verifies that the offset/bytecount tag pair
// (e.g. StripOffsets/StripByteCounts) has identical Count and byte-identical
// block content between orig and result, even though the blocks' absolute
// positions are expected to differ after relocation.
func compareOffsetBlockContent(t *testing.T, label string, origBuf []byte, origEntries map[uint16]rawIFDEntry, resultBuf []byte, resultEntries map[uint16]rawIFDEntry, offsetTag, countTag uint16, bigTIFF bool, order binary.ByteOrder) {
	t.Helper()
	origOff, ok := origEntries[offsetTag]
	origCnt, ok2 := origEntries[countTag]
	if !ok || !ok2 {
		return // this tag pair is not present in this IFD; nothing to check
	}
	resultOff, ok3 := resultEntries[offsetTag]
	resultCnt, ok4 := resultEntries[countTag]
	if !ok3 || !ok4 {
		t.Errorf("%s: tag 0x%04X/0x%04X present in original but missing from result", label, offsetTag, countTag)
		return
	}
	if origOff.count != resultOff.count {
		t.Errorf("%s: tag 0x%04X count changed: orig=%d result=%d", label, offsetTag, origOff.count, resultOff.count)
		return
	}

	origOffElemSz := elemSizeFor(origOff.typ, bigTIFF)
	origCntElemSz := elemSizeFor(origCnt.typ, bigTIFF)
	resultOffElemSz := elemSizeFor(resultOff.typ, bigTIFF)
	resultCntElemSz := elemSizeFor(resultCnt.typ, bigTIFF)

	origOffs, ok1 := decodeOffsetArray(origBuf, origOff.valField, origOff.count, origOffElemSz, bigTIFF, order)
	origSizes, ok2b := decodeOffsetArray(origBuf, origCnt.valField, origCnt.count, origCntElemSz, bigTIFF, order)
	resultOffs, ok3b := decodeOffsetArray(resultBuf, resultOff.valField, resultOff.count, resultOffElemSz, bigTIFF, order)
	resultSizes, ok4b := decodeOffsetArray(resultBuf, resultCnt.valField, resultCnt.count, resultCntElemSz, bigTIFF, order)
	if !ok1 || !ok2b || !ok3b || !ok4b {
		t.Errorf("%s: tag 0x%04X: decodeOffsetArray failed (offs ok=%v/%v sizes ok=%v/%v)",
			label, offsetTag, ok1, ok3b, ok2b, ok4b)
		return
	}

	n := min(len(origOffs), len(resultOffs))
	for i := range n {
		if origSizes[i] != resultSizes[i] {
			t.Errorf("%s: tag 0x%04X[%d]: size changed: orig=%d result=%d", label, offsetTag, i, origSizes[i], resultSizes[i])
			continue
		}
		size := origSizes[i]
		if size == 0 {
			continue
		}
		oEnd := origOffs[i] + size
		rEnd := resultOffs[i] + size
		if oEnd > uint64(len(origBuf)) || rEnd > uint64(len(resultBuf)) {
			t.Errorf("%s: tag 0x%04X[%d]: block out of bounds (orig %d+%d, result %d+%d)",
				label, offsetTag, i, origOffs[i], size, resultOffs[i], size)
			continue
		}
		if !bytes.Equal(origBuf[origOffs[i]:oEnd], resultBuf[resultOffs[i]:rEnd]) {
			t.Errorf("%s: tag 0x%04X[%d]: block content changed across relocation", label, offsetTag, i)
		}
	}
}
