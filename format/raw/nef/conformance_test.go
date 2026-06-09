package nef

// conformance_test.go — Nikon NEF specification-conformance test battery.
// Task #164.
//
// Rule IDs are used verbatim as sub-test names and cite the authoritative
// specification clause for each assertion.
//
// Sources:
//   - TIFF Revision 6.0 (Adobe, 1992)               §2 — IFD layout, header
//   - CIPA DC-X008-Translation-2019 (Exif 2.32)     §4.6 — IFD entry encoding
//   - ExifTool Nikon.pm                              — Nikon MakerNote structures
//   - docs/conformance/containers.md §8              — NEF detection / write rules
//   - docs/conformance/exif-tiff.md R-08             — Nikon Type-3 TIFF-in-TIFF rebase
//   - docs/conformance/exif-tiff.md §4 real-world 10 — Make trailing spaces
//
// Test categories:
//   NEF-detect   — Format detection: Make value and byte-order variants
//   NEF-type3    — Nikon Type-3 MakerNote embedded-TIFF parsing and rebase
//   NEF-write    — Write byte-correctness and round-trip fidelity
//   NEF-robust   — Robustness: truncation, corrupt headers, no panic
//   Corpus       — Parity over testdata/corpus/raw against real NEF files

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixture helpers
// ─────────────────────────────────────────────────────────────────────────────

// buildNEFWithMake constructs a minimal TIFF whose IFD0 holds a Make tag
// (0x010F, TypeASCII) pointing to the given make string stored OOL.
//
// Containers §8: NEF detection relies on IFD0 Make == "NIKON CORPORATION" /
// "Nikon". TIFF 6.0 §2: ASCII field count includes the NUL terminator.
func buildNEFWithMake(order binary.ByteOrder, makeStr string) []byte {
	const (
		hdrLen = 8
		// IFD: count(2) + 1 entry(12) + next-IFD(4) = 18 bytes
		ifdLen = 2 + 12 + 4
	)
	oolOff := uint32(hdrLen + ifdLen)
	makeBytes := append([]byte(makeStr), 0x00)
	total := int(oolOff) + len(makeBytes)

	buf := make([]byte, total)
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(hdrLen))

	// IFD0: 1 entry.
	order.PutUint16(buf[hdrLen:], 1)
	e0 := hdrLen + 2
	order.PutUint16(buf[e0:], 0x010F)                   // tag: Make
	order.PutUint16(buf[e0+2:], 2)                      // type: ASCII
	order.PutUint32(buf[e0+4:], uint32(len(makeBytes))) //nolint:gosec // G115: bounded by buf
	order.PutUint32(buf[e0+8:], oolOff)
	order.PutUint32(buf[e0+12:], 0) // next-IFD = 0

	copy(buf[oolOff:], makeBytes)
	return buf
}

// buildNikonType3MakerNote constructs a Nikon Type-3 MakerNote blob.
//
// ExifTool Nikon.pm: Nikon Type-3 layout:
//
//	[0..5]     "Nikon\x00"     magic prefix
//	[6..7]     version          (e.g. 0x02 0x10)
//	[tiffStart..tiffStart+7]  embedded TIFF header: BOM + 0x002A + IFD0 offset
//
// tiffStart is 8 for version 0x0210 cameras and 10 for the D70 (version 0x0200,
// which adds 2 padding bytes at [8..9] before the TIFF header).
//
// All value offsets in entries are relative to the embedded TIFF header start
// (R-08).  tailData is appended immediately after the IFD block within the
// embedded TIFF, providing the OOL value area for any OOL entries.
func buildNikonType3MakerNote(
	embeddedOrder binary.ByteOrder,
	version [2]byte,
	tiffStart int,
	entries [][4]uint32,
	tailData []byte,
) []byte {
	n := len(entries)
	// Embedded TIFF: 8-byte header then IFD at offset 8.
	ifdRelOff := uint32(8)
	ifdSize := 2 + n*12 + 4
	blobLen := tiffStart + 8 + ifdSize + len(tailData)

	blob := make([]byte, blobLen)

	// Nikon magic prefix and version.
	copy(blob[0:6], "Nikon\x00")
	blob[6] = version[0]
	blob[7] = version[1]
	// For tiffStart=10 there are 2 padding bytes at [8..9]; already zero.

	// Embedded TIFF header at blob[tiffStart].
	tiff := blob[tiffStart:]
	if embeddedOrder == binary.LittleEndian {
		tiff[0], tiff[1] = 'I', 'I'
	} else {
		tiff[0], tiff[1] = 'M', 'M'
	}
	embeddedOrder.PutUint16(tiff[2:], 0x002A)
	embeddedOrder.PutUint32(tiff[4:], ifdRelOff)

	// IFD entries within the embedded TIFF (at ifdRelOff from the embedded TIFF start).
	ifdStart := tiff[ifdRelOff:]
	embeddedOrder.PutUint16(ifdStart[0:], uint16(n)) //nolint:gosec // G115: n bounded
	for i, e := range entries {
		p := 2 + i*12
		embeddedOrder.PutUint16(ifdStart[p:], uint16(e[0]))   //nolint:gosec // G115: tag
		embeddedOrder.PutUint16(ifdStart[p+2:], uint16(e[1])) //nolint:gosec // G115: type
		embeddedOrder.PutUint32(ifdStart[p+4:], e[2])
		embeddedOrder.PutUint32(ifdStart[p+8:], e[3])
	}
	// next-IFD pointer.
	embeddedOrder.PutUint32(ifdStart[2+n*12:], 0)

	// Tail data (OOL values) appended after the IFD block.
	if len(tailData) > 0 {
		copy(blob[tiffStart+int(ifdRelOff)+ifdSize:], tailData)
	}
	return blob
}

// buildNEFWithMakerNote constructs a minimal big-endian TIFF whose IFD0 holds:
//   - Make (0x010F): "NIKON CORPORATION"
//   - ExifIFD pointer (0x8769) → ExifIFD containing MakerNote (0x927C)
//
// Returns (tiffBytes, makerNoteFileOff) where makerNoteFileOff is the
// absolute offset in tiffBytes at which the MakerNote blob starts.
//
// EXIF §4.6.5 tag 0x927C; TIFF 6.0 §2 IFD entry layout.
func buildNEFWithMakerNote(order binary.ByteOrder, makerNoteBlob []byte) ([]byte, uint32) {
	const hdrLen = 8

	makeStr := "NIKON CORPORATION\x00"
	makeLen := uint32(len(makeStr)) //nolint:gosec // G115: bounded

	// IFD0: Make(0x010F) + ExifIFD ptr(0x8769) = 2 entries.
	// Layout: hdr(8) + ifd0(2+2×12+4=30) + makeOOL + exifIFD(2+1×12+4=18) + makerNoteBlob.
	const ifd0EntryCount = 2
	const ifd0Len = 2 + ifd0EntryCount*12 + 4
	makeOff := uint32(hdrLen + ifd0Len)
	exifIFDOff := makeOff + makeLen
	const exifIFDLen = 2 + 1*12 + 4
	mnOff := exifIFDOff + exifIFDLen

	totalLen := int(mnOff) + len(makerNoteBlob)
	buf := make([]byte, totalLen)

	// TIFF header.
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(hdrLen))

	// IFD0.
	order.PutUint16(buf[hdrLen:], ifd0EntryCount)
	e0 := hdrLen + 2
	// Make tag (0x010F, ASCII, OOL).
	order.PutUint16(buf[e0:], 0x010F)
	order.PutUint16(buf[e0+2:], 2) // ASCII
	order.PutUint32(buf[e0+4:], makeLen)
	order.PutUint32(buf[e0+8:], makeOff)
	// ExifIFD pointer (0x8769, LONG, inline value = exifIFDOff).
	e1 := e0 + 12
	order.PutUint16(buf[e1:], 0x8769)
	order.PutUint16(buf[e1+2:], 4) // LONG
	order.PutUint32(buf[e1+4:], 1)
	order.PutUint32(buf[e1+8:], exifIFDOff)
	// next-IFD = 0.
	order.PutUint32(buf[e1+12:], 0)

	// Make string OOL value.
	copy(buf[makeOff:], makeStr)

	// ExifIFD: 1 MakerNote entry (0x927C, UNDEFINED, OOL).
	order.PutUint16(buf[exifIFDOff:], 1) // entry count
	em := int(exifIFDOff) + 2
	order.PutUint16(buf[em:], 0x927C)
	order.PutUint16(buf[em+2:], 7)                          // UNDEFINED
	order.PutUint32(buf[em+4:], uint32(len(makerNoteBlob))) //nolint:gosec // G115: bounded
	order.PutUint32(buf[em+8:], mnOff)
	order.PutUint32(buf[em+12:], 0) // next-IFD = 0

	// MakerNote blob.
	copy(buf[mnOff:], makerNoteBlob)

	return buf, mnOff
}

// ─────────────────────────────────────────────────────────────────────────────
// NEF-detect — detection rules
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_NEF_detect_magic_BE verifies that a big-endian TIFF
// (MM\0*) with Make="NIKON CORPORATION" is extracted without error.
//
// Containers §8: NEF standard TIFF magic; Nikon cameras usually use
// big-endian (MM) byte order. TIFF 6.0 §2: "MM" = Motorola byte order.
func TestConformance_NEF_detect_magic_BE(t *testing.T) {
	t.Parallel()
	data := buildNEFWithMake(binary.BigEndian, "NIKON CORPORATION")
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NEF-detect-magic-BE: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("NEF-detect-magic-BE: rawEXIF is nil")
	}
}

// TestConformance_NEF_detect_magic_LE verifies that a little-endian TIFF
// (II*\0) with Make="NIKON CORPORATION" is extracted without error.
//
// Containers §8: NEF uses standard TIFF magic; not all Nikon models use BE.
// TIFF 6.0 §2: "II" = Intel byte order (little-endian).
func TestConformance_NEF_detect_magic_LE(t *testing.T) {
	t.Parallel()
	data := buildNEFWithMake(binary.LittleEndian, "NIKON CORPORATION")
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NEF-detect-magic-LE: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("NEF-detect-magic-LE: rawEXIF is nil")
	}
}

// TestConformance_NEF_detect_make_nikon_corporation verifies that
// Make="NIKON CORPORATION" is the primary detection string.
//
// Containers §8: Make="NIKON CORPORATION" is the canonical Nikon Make value
// found in D-series and Z-series professional bodies.
func TestConformance_NEF_detect_make_nikon_corporation(t *testing.T) {
	t.Parallel()
	data := buildNEFWithMake(binary.BigEndian, "NIKON CORPORATION")
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NEF-detect-make-nikon-corporation: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("NEF-detect-make-nikon-corporation: rawEXIF is nil")
	}
}

// TestConformance_NEF_detect_make_nikon verifies that Make="Nikon" (short
// form) also routes correctly through the TIFF delegation path.
//
// Containers §8: Make="Nikon" is the secondary detection string
// (older Coolpix bodies and early DSLRs).
func TestConformance_NEF_detect_make_nikon(t *testing.T) {
	t.Parallel()
	data := buildNEFWithMake(binary.LittleEndian, "Nikon")
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NEF-detect-make-nikon: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("NEF-detect-make-nikon: rawEXIF is nil")
	}
}

// TestConformance_NEF_detect_make_trailing_spaces verifies that Make values
// with trailing spaces ("NIKON CORPORATION ") are still accepted.
//
// Exif-TIFF conformance §4 real-world deviation 10: Make trailing spaces must
// be TrimSpaced before MakerNote dispatch.  Extract does not perform detection
// itself (it delegates to tiff.Extract); this test confirms no panic occurs.
func TestConformance_NEF_detect_make_trailing_spaces(t *testing.T) {
	t.Parallel()
	data := buildNEFWithMake(binary.BigEndian, "NIKON CORPORATION ")
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NEF-detect-make-trailing-spaces: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("NEF-detect-make-trailing-spaces: rawEXIF is nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// NEF-type3 — Nikon Type-3 MakerNote parsing and embedded-TIFF rebase
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_NEF_type3_makernote_rebase verifies that the Nikon Type-3
// MakerNote embedded TIFF is parsed correctly and that the MakerNoteIFD is
// populated.
//
// Exif-TIFF R-08: Nikon Type 3 MakerNote has an embedded TIFF header at
// MakerNote+10 (D70 variant) or MakerNote+8 (standard); internal offsets
// are relative to that embedded base.
//
// ExifTool Nikon.pm: layout is "Nikon\x00" + version(2) + [padding] +
// embedded TIFF header; all IFD value offsets are relative to the embedded
// TIFF header start.
func TestConformance_NEF_type3_makernote_rebase(t *testing.T) {
	t.Parallel()

	// Build a Nikon Type-3 MakerNote (version 0x0210, tiffStart=8) with one
	// inline entry (tag 0x0001, type SHORT, count=1, val=42).
	// R-08: internal offsets relative to embedded TIFF base at blob[8].
	entries := [][4]uint32{
		{0x0001, 3 /*SHORT*/, 1, 42},
	}
	mnBlob := buildNikonType3MakerNote(
		binary.BigEndian,
		[2]byte{0x02, 0x10},
		8,
		entries,
		nil,
	)

	tiffData, _ := buildNEFWithMakerNote(binary.BigEndian, mnBlob)

	parsed, err := exif.Parse(tiffData)
	if err != nil {
		t.Fatalf("NEF-type3-makernote-rebase: exif.Parse: %v", err)
	}
	if parsed.MakerNote == nil {
		t.Fatal("NEF-type3-makernote-rebase: MakerNote bytes are nil")
	}
	// MakerNote bytes must begin with the Nikon Type-3 signature.
	// ExifTool Nikon.pm: "Nikon\x00" prefix identifies Type-3.
	if !bytes.HasPrefix(parsed.MakerNote, []byte("Nikon\x00")) {
		t.Errorf("NEF-type3-makernote-rebase: MakerNote does not start with 'Nikon\\x00': %q",
			parsed.MakerNote[:min(12, len(parsed.MakerNote))])
	}
	// MakerNoteIFD must have been decoded by parseMakerNoteIFD dispatch.
	if parsed.MakerNoteIFD == nil {
		t.Fatal("NEF-type3-makernote-rebase: MakerNoteIFD is nil; exif.Parse should have dispatched to parseNikonMakerNote")
	}
}

// TestConformance_NEF_type3_d70_variant_tiffstart10 verifies that the Nikon
// D70 variant (version 0x0200, TIFF header at offset 10, with 2 padding bytes
// at [8..9]) is correctly detected and parsed.
//
// ExifTool Nikon.pm: D70 firmware stored a 2-byte padding word before the
// embedded TIFF header, placing it at offset 10 instead of 8.
// relocate_nef.go findNikonMNTIFFHeader: scans [6..nikonMNPrefixMaxLen) for
// the "II"/"MM" byte-order mark.
func TestConformance_NEF_type3_d70_variant_tiffstart10(t *testing.T) {
	t.Parallel()

	entries := [][4]uint32{
		{0x0001, 3 /*SHORT*/, 1, 7},
	}
	// D70 variant: version 0x0200, tiffStart=10.
	mnBlob := buildNikonType3MakerNote(
		binary.BigEndian,
		[2]byte{0x02, 0x00}, // D70 version
		10,                  // tiffStart (2-byte padding at [8..9])
		entries,
		nil,
	)

	tiffData, _ := buildNEFWithMakerNote(binary.BigEndian, mnBlob)

	parsed, err := exif.Parse(tiffData)
	if err != nil {
		t.Fatalf("NEF-type3-d70-variant-tiffstart10: exif.Parse: %v", err)
	}
	if !bytes.HasPrefix(parsed.MakerNote, []byte("Nikon\x00")) {
		t.Errorf("NEF-type3-d70-variant-tiffstart10: MakerNote prefix wrong: %q",
			parsed.MakerNote[:min(12, len(parsed.MakerNote))])
	}
	if parsed.MakerNoteIFD == nil {
		t.Fatal("NEF-type3-d70-variant-tiffstart10: MakerNoteIFD is nil")
	}
}

// TestConformance_NEF_type3_le_embedded_tiff verifies that a Nikon Type-3
// MakerNote whose embedded TIFF uses little-endian byte order ("II") is
// parsed correctly even when the outer TIFF is big-endian ("MM").
//
// ExifTool Nikon.pm: the embedded MakerNote TIFF may have an independent
// byte order from the outer TIFF. The embedded BOM governs parsing of all
// internal IFD offsets and values.
func TestConformance_NEF_type3_le_embedded_tiff(t *testing.T) {
	t.Parallel()

	entries := [][4]uint32{
		{0x0001, 3 /*SHORT*/, 1, 99},
	}
	// Outer TIFF = BE, embedded TIFF = LE.
	mnBlob := buildNikonType3MakerNote(
		binary.LittleEndian,
		[2]byte{0x02, 0x10},
		8,
		entries,
		nil,
	)

	tiffData, _ := buildNEFWithMakerNote(binary.BigEndian, mnBlob)

	parsed, err := exif.Parse(tiffData)
	if err != nil {
		t.Fatalf("NEF-type3-le-embedded-tiff: exif.Parse: %v", err)
	}
	if parsed.MakerNoteIFD == nil {
		t.Fatal("NEF-type3-le-embedded-tiff: MakerNoteIFD is nil; LE embedded TIFF not parsed")
	}
}

// TestConformance_NEF_type3_ool_entry_decoded_correctly verifies that an OOL
// string value stored inside the Nikon Type-3 embedded TIFF is decoded at the
// correct MakerNote-relative offset, not the outer-TIFF-absolute offset.
//
// R-08: offsets within the embedded TIFF are relative to the embedded TIFF
// header start (mnTIFFBase = makerNoteFileOff + tiffStart). If the library
// mistakenly used outer-TIFF-absolute offsets, the OOL value would point to
// garbage and the decoded string would be wrong.
func TestConformance_NEF_type3_ool_entry_decoded_correctly(t *testing.T) {
	t.Parallel()

	// We place an OOL ASCII string at offset 26 within the embedded TIFF:
	//   [0..7]   embedded TIFF header (BOM + magic + ifd0_off=8)
	//   [8..]    IFD: count(2) + 1 entry(12) + next-IFD(4) = 18 bytes  → ends at 26
	//   [26..]   OOL value: "NikonModel\x00" (11 bytes)
	//
	// tag 0x0110 (Model), type ASCII, count=11, valOrOff=26 (relative to embedded TIFF).
	const oolRelOff = uint32(26)
	modelStr := "NikonModel\x00"
	entries := [][4]uint32{
		{0x0110, 2 /*ASCII*/, uint32(len(modelStr)), oolRelOff}, //nolint:gosec // G115: len bounded
	}
	mnBlob := buildNikonType3MakerNote(
		binary.BigEndian,
		[2]byte{0x02, 0x10},
		8,
		entries,
		[]byte(modelStr), // tail data = OOL value appended at the end of the blob
	)

	tiffData, _ := buildNEFWithMakerNote(binary.BigEndian, mnBlob)

	parsed, err := exif.Parse(tiffData)
	if err != nil {
		t.Fatalf("NEF-type3-ool-entry-decoded-correctly: exif.Parse: %v", err)
	}
	if parsed.MakerNoteIFD == nil {
		t.Fatal("NEF-type3-ool-entry-decoded-correctly: MakerNoteIFD is nil")
	}

	// Retrieve tag 0x0110 from the MakerNote IFD.
	entry := parsed.MakerNoteIFD.Get(exif.TagID(0x0110))
	if entry == nil {
		t.Fatal("NEF-type3-ool-entry-decoded-correctly: tag 0x0110 not found in MakerNoteIFD")
	}

	// TIFF 6.0 S-20: ASCII MUST be NUL-terminated; parser strips trailing NUL.
	got := string(bytes.TrimRight(entry.Value, "\x00"))
	want := strings.TrimRight(modelStr, "\x00")
	if got != want {
		t.Errorf("NEF-type3-ool-entry-decoded-correctly: tag 0x0110 = %q, want %q", got, want)
	}
}

// TestConformance_NEF_type3_without_magic_no_panic verifies that a MakerNote
// blob that does not start with "Nikon\x00" does not panic.
//
// exif.parseMakerNoteIFD must fall through to parseNikonType1 (or return nil)
// for MakerNote blobs lacking the Type-3 signature.  No panic must occur.
func TestConformance_NEF_type3_without_magic_no_panic(t *testing.T) {
	t.Parallel()

	// A MakerNote blob starting with random bytes (not "Nikon\x00").
	badMN := []byte("NotNikon\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	tiffData, _ := buildNEFWithMakerNote(binary.BigEndian, badMN)

	// Must not panic; result may be nil or a Type-1 parse attempt.
	parsed, err := exif.Parse(tiffData)
	if err != nil {
		t.Fatalf("NEF-type3-without-magic-no-panic: exif.Parse: %v", err)
	}
	_ = parsed
}

// TestConformance_NEF_type3_makernote_offset_recorded verifies that
// exif.EXIF.MakerNoteOffset is populated after parsing a Nikon NEF.
//
// relocate_nef.go extractNikonPreviewInfo uses MakerNoteOffset to locate the
// MakerNote blob within the outer file for patching.  It must be non-zero for
// the NEF-specific relocation path to work correctly (instead of falling back
// to the slow linear scan).
func TestConformance_NEF_type3_makernote_offset_recorded(t *testing.T) {
	t.Parallel()

	entries := [][4]uint32{
		{0x0001, 3 /*SHORT*/, 1, 1},
	}
	mnBlob := buildNikonType3MakerNote(
		binary.BigEndian,
		[2]byte{0x02, 0x10},
		8,
		entries,
		nil,
	)

	tiffData, mnOff := buildNEFWithMakerNote(binary.BigEndian, mnBlob)

	parsed, err := exif.Parse(tiffData)
	if err != nil {
		t.Fatalf("NEF-type3-makernote-offset-recorded: exif.Parse: %v", err)
	}
	if parsed.MakerNoteOffset == 0 {
		t.Errorf("NEF-type3-makernote-offset-recorded: MakerNoteOffset=0; want %d", mnOff)
	}
	if parsed.MakerNoteOffset != mnOff {
		t.Errorf("NEF-type3-makernote-offset-recorded: MakerNoteOffset=%d, want %d",
			parsed.MakerNoteOffset, mnOff)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// NEF-write — Write byte-correctness and round-trip fidelity
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_NEF_write_pass_through verifies that a NEF round-trip via
// Inject with nil metadata returns a parseable stream.
//
// Containers §8(e): round-trip must not corrupt image data.
func TestConformance_NEF_write_pass_through(t *testing.T) {
	t.Parallel()

	data := buildNEFWithMake(binary.BigEndian, "NIKON CORPORATION")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("NEF-write-pass-through: Inject: %v", err)
	}
	result := out.Bytes()
	if len(result) == 0 {
		t.Fatal("NEF-write-pass-through: Inject produced empty output")
	}
	rawEXIF2, _, _, err := Extract(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("NEF-write-pass-through: Extract after Inject: %v", err)
	}
	if rawEXIF2 == nil {
		t.Error("NEF-write-pass-through: rawEXIF nil after round-trip")
	}
}

// TestConformance_NEF_write_iptc_xmp_injection verifies that IPTC and XMP
// payloads are correctly injected into a NEF stream and survive a round-trip.
//
// Containers §8(d): NEF stores XMP via TIFF tag 0x02BC (TypeByte) and IPTC
// via tag 0x83BB.  After Inject, those tags must be readable by Extract.
func TestConformance_NEF_write_iptc_xmp_injection(t *testing.T) {
	t.Parallel()

	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x06, 'N', 'i', 'k', 'o', 'n', '!'}
	wantXMP := []byte(`<?xpacket begin="" uid="nef-conformance"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	data := buildNEFWithMake(binary.BigEndian, "NIKON CORPORATION")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, wantIPTC, wantXMP, true); err != nil {
		t.Fatalf("NEF-write-iptc-xmp-injection: Inject: %v", err)
	}

	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("NEF-write-iptc-xmp-injection: Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("NEF-write-iptc-xmp-injection: rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("NEF-write-iptc-xmp-injection: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestConformance_NEF_write_le_round_trip verifies that a little-endian NEF
// stream round-trips correctly through Extract → Inject → Extract.
//
// Containers §8: NEF may be little-endian or big-endian; both byte orders
// must be handled correctly on write.
func TestConformance_NEF_write_le_round_trip(t *testing.T) {
	t.Parallel()

	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x04, 'T', 'E', 'S', 'T'}
	wantXMP := []byte("<xmpmeta/>")

	data := buildNEFWithMake(binary.LittleEndian, "NIKON CORPORATION")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, wantIPTC, wantXMP, true); err != nil {
		t.Fatalf("NEF-write-le-round-trip: Inject: %v", err)
	}

	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("NEF-write-le-round-trip: Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("NEF-write-le-round-trip: rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("NEF-write-le-round-trip: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestConformance_NEF_write_be_round_trip verifies that a big-endian NEF
// stream round-trips through Extract → Inject → Extract with no data loss.
func TestConformance_NEF_write_be_round_trip(t *testing.T) {
	t.Parallel()

	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x06, 'N', 'I', 'K', 'O', 'N', '!'}
	wantXMP := []byte(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/>`)

	data := buildNEFWithMake(binary.BigEndian, "NIKON CORPORATION")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, wantIPTC, wantXMP, true); err != nil {
		t.Fatalf("NEF-write-be-round-trip: Inject: %v", err)
	}

	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("NEF-write-be-round-trip: Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("NEF-write-be-round-trip: rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("NEF-write-be-round-trip: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestConformance_NEF_write_makernote_preserved verifies that the Nikon
// Type-3 MakerNote blob is preserved (the "Nikon\x00" prefix remains) after
// a pass-through Inject.
//
// Containers §8(e): MakerNote blobs must be copied verbatim; relocate.go
// states "MakerNote blobs are copied verbatim". Loss of MakerNote would
// corrupt lens/exposure metadata required by Nikon capture software.
func TestConformance_NEF_write_makernote_preserved(t *testing.T) {
	t.Parallel()

	entries := [][4]uint32{
		{0x0001, 3 /*SHORT*/, 1, 0xABCD},
	}
	mnBlob := buildNikonType3MakerNote(
		binary.BigEndian,
		[2]byte{0x02, 0x10},
		8,
		entries,
		nil,
	)
	tiffData, _ := buildNEFWithMakerNote(binary.BigEndian, mnBlob)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(tiffData), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("NEF-write-makernote-preserved: Inject: %v", err)
	}

	result := out.Bytes()
	// The Nikon magic prefix must still be present in the output.
	if !bytes.Contains(result, []byte("Nikon\x00")) {
		t.Error("NEF-write-makernote-preserved: 'Nikon\\x00' prefix not found in round-tripped output")
	}
}

// TestConformance_NEF_write_error_prefix verifies that Inject wraps errors
// with the "nef:" prefix, consistent with the NEF delegation layer.
//
// nef.Inject delegates to tiff.Inject and prefixes errors with "nef:".
func TestConformance_NEF_write_error_prefix(t *testing.T) {
	t.Parallel()
	bad := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0}
	var out bytes.Buffer
	err := Inject(bytes.NewReader(bad), &out, bad, []byte("iptc"), nil, true)
	if err == nil {
		t.Fatal("NEF-write-error-prefix: expected error for corrupt input, got nil")
	}
	if !strings.HasPrefix(err.Error(), "nef:") {
		t.Errorf("NEF-write-error-prefix: error prefix = %q, want 'nef:'", err.Error())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// NEF-robust — Robustness: no panics under adversarial input
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_NEF_robust_empty_input verifies that an empty input does
// not panic and returns an error.
//
// R-13: Classic stream < 8 bytes always invalid; check min length first.
func TestConformance_NEF_robust_empty_input(t *testing.T) {
	t.Parallel()
	_, _, _, err := Extract(bytes.NewReader([]byte{}))
	if err == nil {
		t.Error("NEF-robust-empty-input: expected error for empty input, got nil")
	}
}

// TestConformance_NEF_robust_too_short verifies that inputs shorter than
// 8 bytes return an error without panicking.
//
// R-13: stream < 8 bytes → error, no panic.
func TestConformance_NEF_robust_too_short(t *testing.T) {
	t.Parallel()
	for n := range 8 {
		buf := make([]byte, n)
		if n >= 2 {
			buf[0], buf[1] = 'M', 'M'
		}
		_, _, _, err := Extract(bytes.NewReader(buf))
		if err == nil {
			t.Errorf("NEF-robust-too-short: %d bytes: expected error, got nil", n)
		}
	}
}

// TestConformance_NEF_robust_invalid_bom verifies that a stream with an
// invalid byte-order marker does not panic and returns an error.
//
// TIFF 6.0 §2 / S-01: only "II" and "MM" are valid BOMs.
// Exif-TIFF S-01: fixture header "ZZ" → error.
func TestConformance_NEF_robust_invalid_bom(t *testing.T) {
	t.Parallel()
	for _, bom := range [][]byte{
		{0x00, 0x00}, {0x4D, 0x49}, {0xFF, 0xFF}, {0x49, 0x4D},
	} {
		buf := make([]byte, 8)
		buf[0], buf[1] = bom[0], bom[1]
		_, _, _, err := Extract(bytes.NewReader(buf))
		if err == nil {
			t.Errorf("NEF-robust-invalid-bom: BOM %02X %02X: expected error, got nil",
				bom[0], bom[1])
		}
	}
}

// TestConformance_NEF_robust_truncated_after_header verifies that a valid
// 8-byte TIFF header whose IFD0 offset points to EOF does not crash.
//
// R-12: truncated after header before IFD0 → no panic; rawEXIF returned.
func TestConformance_NEF_robust_truncated_after_header(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 8)
	buf[0], buf[1] = 'M', 'M'
	binary.BigEndian.PutUint16(buf[2:], 0x002A)
	binary.BigEndian.PutUint32(buf[4:], 8) // IFD0 at EOF

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("NEF-robust-truncated-after-header: rawEXIF must not be nil for header-only TIFF")
	}
}

// TestConformance_NEF_robust_ifd0_offset_past_eof verifies that an IFD0
// offset beyond the file length is handled gracefully.
//
// R-03: any offset outside the stream → treat as absent; no crash.
// S-03: IFD0 offset past EOF → no panic.
func TestConformance_NEF_robust_ifd0_offset_past_eof(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 8)
	buf[0], buf[1] = 'M', 'M'
	binary.BigEndian.PutUint16(buf[2:], 0x002A)
	binary.BigEndian.PutUint32(buf[4:], 0xFFFFFFFF)

	rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(buf))
	// Must not panic; metadata must be absent.
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("NEF-robust-ifd0-offset-past-eof: expected nil IPTC/XMP; got IPTC=%v XMP=%v",
			rawIPTC, rawXMP)
	}
	_ = rawEXIF
}

// TestConformance_NEF_robust_ifd_count_partial verifies that a TIFF claiming
// more IFD entries than the buffer can hold does not panic.
//
// R-05: entry count×12 > remaining bytes → read only entries that fit; no OOB.
// Exif-TIFF S-07: partial IFD must be tolerated.
func TestConformance_NEF_robust_ifd_count_partial(t *testing.T) {
	t.Parallel()
	// Header + IFD count claiming 100 entries but only 8 bytes follow.
	buf := make([]byte, 18)
	buf[0], buf[1] = 'M', 'M'
	binary.BigEndian.PutUint16(buf[2:], 0x002A)
	binary.BigEndian.PutUint32(buf[4:], 8)
	binary.BigEndian.PutUint16(buf[8:], 100) // claims 100 entries; only 8 bytes of buffer remain

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("NEF-robust-ifd-count-partial: rawEXIF must not be nil")
	}
}

// TestConformance_NEF_robust_ifd_next_circular verifies that a self-referential
// next-IFD pointer does not cause an infinite loop.
//
// R-01: circular IFD chains must be detected; break, no infinite loop.
// Note: the test relies on the library's own loop-detection guard — if that
// guard is missing the test will hang until -timeout kills it.
func TestConformance_NEF_robust_ifd_next_circular(t *testing.T) {
	t.Parallel()
	// IFD0 at offset 8; next-IFD pointer points back to 8 (self-reference).
	buf := make([]byte, 14)
	buf[0], buf[1] = 'M', 'M'
	binary.BigEndian.PutUint16(buf[2:], 0x002A)
	binary.BigEndian.PutUint32(buf[4:], 8)
	binary.BigEndian.PutUint16(buf[8:], 0)  // 0 entries
	binary.BigEndian.PutUint32(buf[10:], 8) // next-IFD = 8 (self-reference)

	// If the library loops, the test process hangs and -timeout kills it.
	_, _, _, _ = Extract(bytes.NewReader(buf))
}

// TestConformance_NEF_robust_makernote_truncated verifies that a Nikon
// Type-3 MakerNote that is shorter than the minimum valid length (18 bytes)
// does not panic.
//
// R-12: truncated MakerNote → nil MakerNoteIFD, no crash.
// ExifTool Nikon.pm: minimum Type-3 blob is 18 bytes; shorter → skip.
func TestConformance_NEF_robust_makernote_truncated(t *testing.T) {
	t.Parallel()

	// MakerNote: "Nikon\x00" + 2-byte version only (8 bytes); embedded TIFF header absent.
	truncMN := []byte("Nikon\x00\x02\x10")
	tiffData, _ := buildNEFWithMakerNote(binary.BigEndian, truncMN)

	// Must not panic.
	parsed, err := exif.Parse(tiffData)
	if err != nil {
		t.Fatalf("NEF-robust-makernote-truncated: exif.Parse: %v", err)
	}
	_ = parsed
}

// TestConformance_NEF_robust_makernote_bad_embedded_magic verifies that a
// Nikon Type-3 blob with a corrupt embedded TIFF magic (not 0x002A) does not
// panic.
//
// parseNikonType3: if embedded magic != 0x002A → return nil (no panic).
func TestConformance_NEF_robust_makernote_bad_embedded_magic(t *testing.T) {
	t.Parallel()

	// "Nikon\x00" + version 0x0210 + embedded "II" BOM + bad magic 0xFFFF + padding.
	badMN := []byte{
		'N', 'i', 'k', 'o', 'n', 0x00, // magic prefix
		0x02, 0x10, // version 0x0210
		'I', 'I', // LE BOM
		0xFF, 0xFF, // corrupt magic
		0x00, 0x00, 0x00, 0x08, // IFD offset
		0x00, 0x00, // IFD count = 0
		0x00, 0x00, 0x00, 0x00, // next-IFD = 0
	}
	tiffData, _ := buildNEFWithMakerNote(binary.BigEndian, badMN)

	parsed, err := exif.Parse(tiffData)
	if err != nil {
		t.Fatalf("NEF-robust-makernote-bad-embedded-magic: exif.Parse: %v", err)
	}
	// MakerNoteIFD may be nil (correct for corrupt magic) or non-nil (Type-1
	// fallback). What must NOT happen: a panic.
	_ = parsed
}

// TestConformance_NEF_robust_entry_overflow_count verifies that an IFD entry
// whose count×typeSize overflows is handled without a panic or OOB access.
//
// R-06: count×typeSize overflow must be checked with uint64 arithmetic.
func TestConformance_NEF_robust_entry_overflow_count(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 26)
	order := binary.BigEndian
	buf[0], buf[1] = 'M', 'M'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)
	// MakerNote entry with overflow count (MaxUint32), OOL offset past buffer.
	order.PutUint16(buf[10:], 0x927C)     // tag MakerNote
	order.PutUint16(buf[12:], 7)          // UNDEFINED
	order.PutUint32(buf[14:], 0xFFFFFFFF) // count = MaxUint32
	order.PutUint32(buf[18:], 26)         // OOL offset = just past buffer
	order.PutUint32(buf[22:], 0)          // next-IFD = 0

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("NEF-robust-entry-overflow-count: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("NEF-robust-entry-overflow-count: rawEXIF must not be nil")
	}
}

// TestConformance_NEF_robust_next_ifd_out_of_bounds verifies that an
// out-of-range next-IFD pointer is treated as end-of-chain.
//
// S-13: next-IFD pointer outside stream → treat as end of chain; no crash.
func TestConformance_NEF_robust_next_ifd_out_of_bounds(t *testing.T) {
	t.Parallel()
	data := buildNEFWithMake(binary.BigEndian, "NIKON CORPORATION")
	// Patch next-IFD pointer: IFD0 at 8; count=1; entries=12; next-IFD at offset 22.
	if len(data) >= 26 {
		binary.BigEndian.PutUint32(data[22:], 0xDEADBEEF)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NEF-robust-next-ifd-out-of-bounds: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("NEF-robust-next-ifd-out-of-bounds: rawEXIF must not be nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Corpus — parity over real-world NEF files
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_NEF_corpus_parse_no_crash verifies that Extract completes
// without panicking for every .nef file in the corpus directory.
//
// testutil.CorpusFiles skips the test when no corpus files are found.
// Containers §8(f): robustness against real-world camera output.
func TestConformance_NEF_corpus_parse_no_crash(t *testing.T) {
	t.Parallel()
	paths := testutil.CorpusFiles(t, "raw")
	var nefPaths []string
	for _, p := range paths {
		if strings.HasSuffix(strings.ToLower(p), ".nef") {
			nefPaths = append(nefPaths, p)
		}
	}
	if len(nefPaths) == 0 {
		t.Skip("no .nef files in testdata/corpus/raw; run 'make testdata' to download")
	}
	for _, path := range nefPaths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("Corpus NEF open(%s): %v", path, err)
			}
			rawEXIF, _, _, parseErr := Extract(bytes.NewReader(data))
			if parseErr != nil {
				t.Fatalf("Corpus NEF Extract(%s): %v", path, parseErr)
			}
			if rawEXIF == nil {
				t.Errorf("Corpus NEF Extract(%s): rawEXIF is nil", path)
			}
		})
	}
}

// TestConformance_NEF_corpus_makernote_parsed verifies that every .nef
// corpus file produces a non-nil MakerNote after exif.Parse.
//
// A nil MakerNote from a real Nikon camera file indicates a dispatch failure
// (Make lookup in makerNoteParsers or MakerNote tag 0x927C absent from ExifIFD).
func TestConformance_NEF_corpus_makernote_parsed(t *testing.T) {
	t.Parallel()
	paths := testutil.CorpusFiles(t, "raw")
	var nefPaths []string
	for _, p := range paths {
		if strings.HasSuffix(strings.ToLower(p), ".nef") {
			nefPaths = append(nefPaths, p)
		}
	}
	if len(nefPaths) == 0 {
		t.Skip("no .nef files in testdata/corpus/raw")
	}
	for _, path := range nefPaths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("Corpus NEF open(%s): %v", path, err)
			}
			parsed, parseErr := exif.Parse(data)
			if parseErr != nil {
				t.Fatalf("Corpus NEF exif.Parse(%s): %v", path, parseErr)
			}
			if parsed.MakerNote == nil {
				t.Errorf("Corpus NEF MakerNote nil for %s", path)
			}
			if parsed.MakerNoteIFD == nil {
				// May be nil for rare Type-1 bodies; log rather than fail.
				t.Logf("Corpus NEF MakerNoteIFD nil for %s (Type-1/unrecognised variant)", path)
			}
		})
	}
}

// TestConformance_NEF_corpus_round_trip verifies that every .nef corpus file
// survives an Extract → Inject (pass-through) cycle without error and that
// the result is parseable.
//
// Containers §8(e): round-trip must not corrupt the output stream.
func TestConformance_NEF_corpus_round_trip(t *testing.T) {
	t.Parallel()
	paths := testutil.CorpusFiles(t, "raw")
	var nefPaths []string
	for _, p := range paths {
		if strings.HasSuffix(strings.ToLower(p), ".nef") {
			nefPaths = append(nefPaths, p)
		}
	}
	if len(nefPaths) == 0 {
		t.Skip("no .nef files in testdata/corpus/raw")
	}
	for _, path := range nefPaths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("Corpus NEF open(%s): %v", path, err)
			}
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
				t.Fatalf("Corpus NEF Inject(%s): %v", path, err)
			}
			result := out.Bytes()
			if len(result) == 0 {
				t.Fatalf("Corpus NEF Inject(%s): empty output", path)
			}
			if _, _, _, parseErr := Extract(bytes.NewReader(result)); parseErr != nil {
				t.Errorf("Corpus NEF Extract after Inject(%s): %v", path, parseErr)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Benchmark
// ─────────────────────────────────────────────────────────────────────────────

// BenchmarkNEFExtractMakerNote measures Extract on a synthetic NEF with a
// Nikon Type-3 MakerNote, covering both outer TIFF parse and the embedded
// MakerNote IFD traversal.
//
// This hot path must remain allocation-efficient; the benchmark reports allocs
// via b.ReportAllocs().
func BenchmarkNEFExtractMakerNote(b *testing.B) {
	entries := [][4]uint32{
		{0x0001, 3, 1, 42},
		{0x0004, 4, 1, 0x12345678},
	}
	mnBlob := buildNikonType3MakerNote(
		binary.BigEndian,
		[2]byte{0x02, 0x10},
		8,
		entries,
		nil,
	)
	data, _ := buildNEFWithMakerNote(binary.BigEndian, mnBlob)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(data))
	}
}
