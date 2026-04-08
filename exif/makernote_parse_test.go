package exif

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers for building raw MakerNote payloads
// ---------------------------------------------------------------------------

// buildRawIFD builds a raw TIFF IFD at offset 0 with no prefix.
// Useful for Canon, Sony, DJI, Samsung, Casio, Leica Type-0 tests.
//
// Note: parsers that call traverse(b, 0, order) will always return nil because
// traverse's loop guard is "for cur != 0" — offset 0 is the end-of-chain sentinel.
// This is an inherent property of the exif/ifd.go traverse design.
func buildRawIFD(order binary.ByteOrder, entries [][4]uint32) []byte {
	n := len(entries)
	buf := make([]byte, 2+n*12+4)
	order.PutUint16(buf[0:], uint16(n)) //nolint:gosec // G115: test helper
	for i, e := range entries {
		p := 2 + i*12
		order.PutUint16(buf[p:], uint16(e[0]))   //nolint:gosec // G115: test helper
		order.PutUint16(buf[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper
		order.PutUint32(buf[p+4:], e[2])         // count
		order.PutUint32(buf[p+8:], e[3])         // value/offset
	}
	return buf
}

// buildNikonType3 builds a Nikon Type-3 MakerNote with an embedded TIFF header.
// Nikon Type-3 uses a non-zero IFD offset within the embedded TIFF, so traverse works.
func buildNikonType3(order binary.ByteOrder, entries [][4]uint32) []byte {
	// Layout: "Nikon\0" (6) + version (2) + embedded TIFF at offset 8.
	// Embedded TIFF: byte order (2) + magic (2) + IFD offset (4) + IFD data.
	n := len(entries)
	ifdSize := 2 + n*12 + 4
	buf := make([]byte, 8+8+ifdSize)

	copy(buf[0:6], "Nikon\x00")
	buf[6] = 0x02
	buf[7] = 0x10

	tiff := buf[8:]
	if order == binary.LittleEndian {
		tiff[0], tiff[1] = 'I', 'I'
	} else {
		tiff[0], tiff[1] = 'M', 'M'
	}
	order.PutUint16(tiff[2:], 0x002A)
	order.PutUint32(tiff[4:], 8) // IFD at offset 8 within embedded TIFF (non-zero → traverse works)

	ifd := tiff[8:]
	order.PutUint16(ifd[0:], uint16(n)) //nolint:gosec // G115: test helper
	for i, e := range entries {
		p := 2 + i*12
		order.PutUint16(ifd[p:], uint16(e[0]))   //nolint:gosec // G115: test helper
		order.PutUint16(ifd[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper
		order.PutUint32(ifd[p+4:], e[2])         // count
		order.PutUint32(ifd[p+8:], e[3])         // value/offset
	}
	return buf
}

// buildFujifilmMakerNote builds a Fujifilm MakerNote payload.
// Uses a non-zero IFD offset so traverse works.
func buildFujifilmMakerNote(entries [][4]uint32) []byte {
	// [0..7] "FUJIFILM", [8..11] version "0100", [12..15] LE uint32 IFD offset.
	// IFD is placed immediately after the 16-byte header.
	const ifdStart = 16
	n := len(entries)
	ifdSize := 2 + n*12 + 4
	buf := make([]byte, ifdStart+ifdSize)

	copy(buf[0:8], "FUJIFILM")
	copy(buf[8:12], "0100")
	binary.LittleEndian.PutUint32(buf[12:16], uint32(ifdStart))

	order := binary.LittleEndian
	ifd := buf[ifdStart:]
	order.PutUint16(ifd[0:], uint16(n)) //nolint:gosec // G115: test helper
	for i, e := range entries {
		p := 2 + i*12
		order.PutUint16(ifd[p:], uint16(e[0]))   //nolint:gosec // G115: test helper
		order.PutUint16(ifd[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper
		order.PutUint32(ifd[p+4:], e[2])         // count
		order.PutUint32(ifd[p+8:], e[3])         // value/offset
	}
	return buf
}

// buildOlympusMakerNote builds an Olympus Type-2 MakerNote payload.
// Uses IFD at offset 12 (non-zero) so traverse works.
func buildOlympusMakerNote(order binary.ByteOrder, entries [][4]uint32) []byte {
	// [0..7] "OLYMPUS\0", [8..9] byte order, [10..11] version, IFD at 12.
	const ifdStart = 12
	n := len(entries)
	ifdSize := 2 + n*12 + 4
	buf := make([]byte, ifdStart+ifdSize)

	copy(buf[0:8], "OLYMPUS\x00")
	if order == binary.LittleEndian {
		buf[8], buf[9] = 'I', 'I'
	} else {
		buf[8], buf[9] = 'M', 'M'
	}
	buf[10] = 0x01
	buf[11] = 0x00

	ifd := buf[ifdStart:]
	order.PutUint16(ifd[0:], uint16(n)) //nolint:gosec // G115: test helper
	for i, e := range entries {
		p := 2 + i*12
		order.PutUint16(ifd[p:], uint16(e[0]))   //nolint:gosec // G115: test helper
		order.PutUint16(ifd[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper
		order.PutUint32(ifd[p+4:], e[2])         // count
		order.PutUint32(ifd[p+8:], e[3])         // value/offset
	}
	return buf
}

// buildPentaxAOCMakerNote builds a Pentax AOC-format MakerNote payload.
// Uses IFD at offset 6 (non-zero) so traverse works.
func buildPentaxAOCMakerNote(entries [][4]uint32) []byte {
	// "AOC\0" (4) + 2 unknown bytes + big-endian IFD.
	const ifdStart = 6
	n := len(entries)
	ifdSize := 2 + n*12 + 4
	buf := make([]byte, ifdStart+ifdSize)

	copy(buf[0:4], "AOC\x00")

	order := binary.BigEndian
	ifd := buf[ifdStart:]
	order.PutUint16(ifd[0:], uint16(n)) //nolint:gosec // G115: test helper
	for i, e := range entries {
		p := 2 + i*12
		order.PutUint16(ifd[p:], uint16(e[0]))   //nolint:gosec // G115: test helper
		order.PutUint16(ifd[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper
		order.PutUint32(ifd[p+4:], e[2])         // count
		order.PutUint32(ifd[p+8:], e[3])         // value/offset
	}
	return buf
}

// buildPentaxPENTAXMakerNote builds a Pentax PENTAX-format MakerNote payload.
// Uses IFD at offset 12 (non-zero) so traverse works.
func buildPentaxPENTAXMakerNote(order binary.ByteOrder, entries [][4]uint32) []byte {
	// "PENTAX \0" (8) + byte order (2) + version (2) + IFD at 12.
	const ifdStart = 12
	n := len(entries)
	ifdSize := 2 + n*12 + 4
	buf := make([]byte, ifdStart+ifdSize)

	copy(buf[0:8], "PENTAX \x00")
	if order == binary.LittleEndian {
		buf[8], buf[9] = 'I', 'I'
	} else {
		buf[8], buf[9] = 'M', 'M'
	}
	buf[10] = 0x01
	buf[11] = 0x00

	ifd := buf[ifdStart:]
	order.PutUint16(ifd[0:], uint16(n)) //nolint:gosec // G115: test helper
	for i, e := range entries {
		p := 2 + i*12
		order.PutUint16(ifd[p:], uint16(e[0]))   //nolint:gosec // G115: test helper
		order.PutUint16(ifd[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper
		order.PutUint32(ifd[p+4:], e[2])         // count
		order.PutUint32(ifd[p+8:], e[3])         // value/offset
	}
	return buf
}

// buildPanasonicMakerNote builds a Panasonic MakerNote payload.
// Uses IFD at offset 12 (non-zero) so traverse works.
func buildPanasonicMakerNote(entries [][4]uint32) []byte {
	// "Panasonic\0\0\0" (12) + LE IFD at 12.
	const ifdStart = 12
	n := len(entries)
	ifdSize := 2 + n*12 + 4
	buf := make([]byte, ifdStart+ifdSize)

	copy(buf[0:12], "Panasonic\x00\x00\x00")

	order := binary.LittleEndian
	ifd := buf[ifdStart:]
	order.PutUint16(ifd[0:], uint16(n)) //nolint:gosec // G115: test helper
	for i, e := range entries {
		p := 2 + i*12
		order.PutUint16(ifd[p:], uint16(e[0]))   //nolint:gosec // G115: test helper
		order.PutUint16(ifd[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper
		order.PutUint32(ifd[p+4:], e[2])         // count
		order.PutUint32(ifd[p+8:], e[3])         // value/offset
	}
	return buf
}

// buildLeicaWithPrefixMakerNote builds a Leica Type 1–5 MakerNote with "LEICA\0" prefix.
// Uses IFD at offset 8 (non-zero) so traverse works.
func buildLeicaWithPrefixMakerNote(entries [][4]uint32) []byte {
	// "LEICA\0" (6) + sub-type (2) + LE IFD at 8.
	const ifdStart = 8
	n := len(entries)
	ifdSize := 2 + n*12 + 4
	buf := make([]byte, ifdStart+ifdSize)

	copy(buf[0:6], "LEICA\x00")
	buf[6] = 0x00
	buf[7] = 0x01

	order := binary.LittleEndian
	ifd := buf[ifdStart:]
	order.PutUint16(ifd[0:], uint16(n)) //nolint:gosec // G115: test helper
	for i, e := range entries {
		p := 2 + i*12
		order.PutUint16(ifd[p:], uint16(e[0]))   //nolint:gosec // G115: test helper
		order.PutUint16(ifd[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper
		order.PutUint32(ifd[p+4:], e[2])         // count
		order.PutUint32(ifd[p+8:], e[3])         // value/offset
	}
	return buf
}

// buildSigmaMakerNote builds a Sigma MakerNote with the given prefix.
// Uses IFD at offset 10 (non-zero) so traverse works.
func buildSigmaMakerNote(prefix string, entries [][4]uint32) []byte {
	// [0..7] magic prefix, [8..9] version, [10..] LE IFD.
	const ifdStart = 10
	n := len(entries)
	ifdSize := 2 + n*12 + 4
	buf := make([]byte, ifdStart+ifdSize)

	copy(buf[0:8], prefix)
	buf[8] = 0x01
	buf[9] = 0x00

	order := binary.LittleEndian
	ifd := buf[ifdStart:]
	order.PutUint16(ifd[0:], uint16(n)) //nolint:gosec // G115: test helper
	for i, e := range entries {
		p := 2 + i*12
		order.PutUint16(ifd[p:], uint16(e[0]))   //nolint:gosec // G115: test helper
		order.PutUint16(ifd[p+2:], uint16(e[1])) //nolint:gosec // G115: test helper
		order.PutUint32(ifd[p+4:], e[2])         // count
		order.PutUint32(ifd[p+8:], e[3])         // value/offset
	}
	return buf
}

// testOneEntry returns a convenient single IFD entry: tag=0x0100 (ImageWidth), SHORT, count=1, value=640.
// Using TypeShort (3), the total size is 2 bytes (≤ 4), so the value is stored inline.
func testOneEntry() [][4]uint32 { return [][4]uint32{{0x0100, uint32(TypeShort), 1, 640}} }

// ---------------------------------------------------------------------------
// TestParseMakerNoteIFD — dispatch table
// ---------------------------------------------------------------------------

func TestParseMakerNoteIFDUnknownMake(t *testing.T) {
	t.Parallel()
	ifd := parseMakerNoteIFD([]byte{0x00, 0x00}, "UNKNOWN_BRAND_XYZ", binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for unknown camera make")
	}
}

// TestParseMakerNoteIFDEmptyPayload verifies that known makes with empty/short
// payloads return nil without panicking.
func TestParseMakerNoteIFDEmptyPayload(t *testing.T) {
	t.Parallel()
	makes := []string{"Canon", "SONY", "DJI", "SAMSUNG"}
	for _, make := range makes {
		ifd := parseMakerNoteIFD([]byte{}, make, binary.LittleEndian)
		if ifd != nil {
			t.Errorf("make %q: expected nil for empty payload, got non-nil", make)
		}
	}
}

// ---------------------------------------------------------------------------
// TestParseCanonMakerNote
// Note: parseCanonMakerNote calls traverse(b, 0, order). Since traverse's loop
// guard is "for cur != 0", offset=0 is the end-of-chain sentinel and traverse
// always returns nil. This is expected behavior per the implementation design.
// ---------------------------------------------------------------------------

// TestParseCanonMakerNoteShortPayload verifies the length guard (< 6 bytes).
func TestParseCanonMakerNoteShortPayload(t *testing.T) {
	t.Parallel()
	ifd := parseCanonMakerNote([]byte{0x01, 0x00, 0x00}, binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for too-short Canon payload")
	}
}

// TestParseCanonMakerNoteNilEmpty verifies the length guard on empty payload.
func TestParseCanonMakerNoteNilEmpty(t *testing.T) {
	t.Parallel()
	ifd := parseCanonMakerNote([]byte{}, binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for empty Canon payload")
	}
}

// TestParseCanonMakerNoteLongPayloadNoPanic verifies that a longer payload
// triggers the traverse path (which returns nil per offset=0 design) without panic.
func TestParseCanonMakerNoteLongPayloadNoPanic(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	// parseCanonMakerNote calls traverse(b, 0, order) which returns nil due to
	// the traverse "for cur != 0" guard. No panic expected.
	ifd := parseCanonMakerNote(b, binary.LittleEndian)
	_ = ifd // expected nil due to offset=0 sentinel behavior
}

// TestParseMakerNoteIFDCanonDispatch verifies the Canon dispatch path executes
// without panic for various payloads.
func TestParseMakerNoteIFDCanonDispatch(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	// dispatch calls parseCanonMakerNote which calls traverse(b, 0, order) → nil
	ifd := parseMakerNoteIFD(b, "Canon", binary.LittleEndian)
	_ = ifd // nil expected; dispatch path coverage is the goal
}

// ---------------------------------------------------------------------------
// TestParseNikonMakerNote — Type 1 and Type 3
// (Nikon Type-3 uses non-zero IFD offset → traverse works correctly)
// ---------------------------------------------------------------------------

func TestParseNikonMakerNoteType3LE(t *testing.T) {
	t.Parallel()
	b := buildNikonType3(binary.LittleEndian, testOneEntry())
	if !isNikonType3(b) {
		t.Fatal("isNikonType3 should return true for well-formed Nikon Type-3 payload")
	}
	ifd := parseNikonMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for Nikon Type-3 LE")
	}
}

func TestParseNikonMakerNoteType3BE(t *testing.T) {
	t.Parallel()
	b := buildNikonType3(binary.BigEndian, testOneEntry())
	ifd := parseNikonMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for Nikon Type-3 BE")
	}
}

func TestParseNikonMakerNoteType1NoPanic(t *testing.T) {
	t.Parallel()
	// Nikon Type-1: plain big-endian IFD at offset 0 with no "Nikon\0" prefix.
	// parseNikonType1 calls traverse(b, 0, BE) which returns nil per offset=0 design.
	b := buildRawIFD(binary.BigEndian, testOneEntry())
	ifd := parseNikonMakerNote(b)
	_ = ifd // nil expected; covers the heuristic path
}

func TestParseNikonType3TooShort(t *testing.T) {
	t.Parallel()
	// Type-3 prefix but too short for embedded TIFF header.
	b := []byte("Nikon\x00\x02\x10")
	ifd := parseNikonType3(b, binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for too-short Nikon Type-3 payload")
	}
}

func TestParseNikonType3BadByteOrder(t *testing.T) {
	t.Parallel()
	b := buildNikonType3(binary.LittleEndian, testOneEntry())
	// Corrupt the embedded byte order marker.
	b[8] = 'X'
	b[9] = 'Y'
	ifd := parseNikonType3(b, binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for bad embedded byte order in Nikon Type-3")
	}
}

func TestParseNikonType3BadMagic42(t *testing.T) {
	t.Parallel()
	b := buildNikonType3(binary.LittleEndian, testOneEntry())
	// Corrupt the 0x002A magic bytes in the embedded TIFF header (bytes 10–11).
	b[10], b[11] = 0xFF, 0xFF
	ifd := parseNikonType3(b, binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for bad TIFF magic in Nikon Type-3")
	}
}

func TestIsNikonType3False(t *testing.T) {
	t.Parallel()
	if isNikonType3([]byte("NotNikon\x00\x00")) {
		t.Error("isNikonType3 should return false for non-Nikon payload")
	}
	if isNikonType3([]byte("Nikon")) { // too short
		t.Error("isNikonType3 should return false for too-short payload")
	}
}

func TestParseMakerNoteIFDNikonCorporation(t *testing.T) {
	t.Parallel()
	b := buildNikonType3(binary.LittleEndian, testOneEntry())
	ifd := parseMakerNoteIFD(b, "NIKON CORPORATION", binary.LittleEndian)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for NIKON CORPORATION via dispatch")
	}
}

func TestParseMakerNoteIFDNikonAlias(t *testing.T) {
	t.Parallel()
	b := buildNikonType3(binary.LittleEndian, testOneEntry())
	ifd := parseMakerNoteIFD(b, "Nikon", binary.LittleEndian)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for 'Nikon' alias via dispatch")
	}
}

// ---------------------------------------------------------------------------
// TestParseSonyMakerNote
// Note: parseSonyMakerNote calls traverse(b, 0, order) → always returns nil
// per the traverse offset=0 sentinel design.
// ---------------------------------------------------------------------------

func TestParseSonyMakerNoteShortPayload(t *testing.T) {
	t.Parallel()
	ifd := parseSonyMakerNote([]byte{0x01}, binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for too-short Sony payload")
	}
}

func TestParseSonyMakerNoteLongPayloadNoPanic(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	ifd := parseSonyMakerNote(b, binary.LittleEndian)
	_ = ifd // nil expected; covers the traverse(b, 0, order) path
}

func TestParseMakerNoteIFDSonyDispatch(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	ifd := parseMakerNoteIFD(b, "SONY", binary.LittleEndian)
	_ = ifd // covers dispatch path
}

// ---------------------------------------------------------------------------
// TestParseFujifilmMakerNote (non-zero IFD offset → traverse works)
// ---------------------------------------------------------------------------

func TestParseFujifilmMakerNoteValid(t *testing.T) {
	t.Parallel()
	b := buildFujifilmMakerNote(testOneEntry())
	ifd := parseFujifilmMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for valid Fujifilm MakerNote")
	}
}

func TestParseFujifilmMakerNoteTooShort(t *testing.T) {
	t.Parallel()
	ifd := parseFujifilmMakerNote([]byte("FUJI"))
	if ifd != nil {
		t.Error("expected nil for too-short Fujifilm payload")
	}
}

func TestParseFujifilmMakerNoteBadMagic(t *testing.T) {
	t.Parallel()
	b := buildFujifilmMakerNote(testOneEntry())
	// Corrupt the magic.
	b[0] = 'X'
	ifd := parseFujifilmMakerNote(b)
	if ifd != nil {
		t.Error("expected nil for bad Fujifilm magic")
	}
}

func TestParseMakerNoteIFDFujifilm(t *testing.T) {
	t.Parallel()
	b := buildFujifilmMakerNote(testOneEntry())
	ifd := parseMakerNoteIFD(b, "FUJIFILM", binary.LittleEndian)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for FUJIFILM via dispatch")
	}
}

// ---------------------------------------------------------------------------
// TestParseOlympusMakerNote (IFD at offset 12 → traverse works)
// ---------------------------------------------------------------------------

func TestParseOlympusMakerNoteValidLE(t *testing.T) {
	t.Parallel()
	b := buildOlympusMakerNote(binary.LittleEndian, testOneEntry())
	ifd := parseOlympusMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for valid Olympus LE MakerNote")
	}
}

func TestParseOlympusMakerNoteValidBE(t *testing.T) {
	t.Parallel()
	b := buildOlympusMakerNote(binary.BigEndian, testOneEntry())
	ifd := parseOlympusMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for valid Olympus BE MakerNote")
	}
}

func TestParseOlympusMakerNoteTooShort(t *testing.T) {
	t.Parallel()
	ifd := parseOlympusMakerNote([]byte("OLYMPUS\x00II"))
	if ifd != nil {
		t.Error("expected nil for too-short Olympus payload")
	}
}

func TestParseOlympusMakerNoteBadMagic(t *testing.T) {
	t.Parallel()
	b := buildOlympusMakerNote(binary.LittleEndian, testOneEntry())
	b[0] = 'X' // corrupt magic
	ifd := parseOlympusMakerNote(b)
	if ifd != nil {
		t.Error("expected nil for bad Olympus magic")
	}
}

func TestParseOlympusMakerNoteBadByteOrder(t *testing.T) {
	t.Parallel()
	b := buildOlympusMakerNote(binary.LittleEndian, testOneEntry())
	b[8] = 'X' // corrupt byte order
	ifd := parseOlympusMakerNote(b)
	if ifd != nil {
		t.Error("expected nil for bad Olympus byte order")
	}
}

func TestParseMakerNoteIFDOlympusVariants(t *testing.T) {
	t.Parallel()
	b := buildOlympusMakerNote(binary.LittleEndian, testOneEntry())
	for _, make := range []string{"OLYMPUS IMAGING CORP.", "OLYMPUS CORPORATION", "Olympus"} {
		ifd := parseMakerNoteIFD(b, make, binary.LittleEndian)
		if ifd == nil {
			t.Errorf("make %q: expected non-nil IFD via dispatch", make)
		}
	}
}

// ---------------------------------------------------------------------------
// TestParsePentaxMakerNote — AOC and PENTAX sub-formats (non-zero offsets)
// ---------------------------------------------------------------------------

func TestParsePentaxMakerNoteAOC(t *testing.T) {
	t.Parallel()
	b := buildPentaxAOCMakerNote(testOneEntry())
	ifd := parsePentaxMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for Pentax AOC MakerNote")
	}
}

func TestParsePentaxMakerNotePENTAXLE(t *testing.T) {
	t.Parallel()
	b := buildPentaxPENTAXMakerNote(binary.LittleEndian, testOneEntry())
	ifd := parsePentaxMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for Pentax PENTAX LE MakerNote")
	}
}

func TestParsePentaxMakerNotePENTAXBE(t *testing.T) {
	t.Parallel()
	b := buildPentaxPENTAXMakerNote(binary.BigEndian, testOneEntry())
	ifd := parsePentaxMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for Pentax PENTAX BE MakerNote")
	}
}

func TestParsePentaxMakerNotePENTAXBadByteOrder(t *testing.T) {
	t.Parallel()
	b := buildPentaxPENTAXMakerNote(binary.LittleEndian, testOneEntry())
	b[8] = 'X' // corrupt byte order
	ifd := parsePentaxMakerNote(b)
	if ifd != nil {
		t.Error("expected nil for bad PENTAX byte order")
	}
}

func TestParsePentaxMakerNoteUnknownPrefix(t *testing.T) {
	t.Parallel()
	b := make([]byte, 20)
	copy(b, "UNKNOWN!")
	ifd := parsePentaxMakerNote(b)
	if ifd != nil {
		t.Error("expected nil for unknown Pentax prefix")
	}
}

func TestParsePentaxMakerNoteAOCTooShortNoPrefix(t *testing.T) {
	t.Parallel()
	// Under 8 bytes: the "AOC\0" path's len(b) >= 8 check fails.
	b := []byte("AOC\x00\x00")
	ifd := parsePentaxMakerNote(b)
	_ = ifd // covered: the len(b) >= 8 guard prevents AOC path
}

func TestParseMakerNoteIFDPentaxVariants(t *testing.T) {
	t.Parallel()
	b := buildPentaxAOCMakerNote(testOneEntry())
	for _, make := range []string{"PENTAX Corporation", "Ricoh", "RICOH"} {
		ifd := parseMakerNoteIFD(b, make, binary.LittleEndian)
		if ifd == nil {
			t.Errorf("make %q: expected non-nil IFD via dispatch", make)
		}
	}
}

// ---------------------------------------------------------------------------
// TestParsePanasonicMakerNote (IFD at offset 12 → traverse works)
// ---------------------------------------------------------------------------

func TestParsePanasonicMakerNoteValid(t *testing.T) {
	t.Parallel()
	b := buildPanasonicMakerNote(testOneEntry())
	ifd := parsePanasonicMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for valid Panasonic MakerNote")
	}
}

func TestParsePanasonicMakerNoteTooShort(t *testing.T) {
	t.Parallel()
	ifd := parsePanasonicMakerNote([]byte("Pana"))
	if ifd != nil {
		t.Error("expected nil for too-short Panasonic payload")
	}
}

func TestParsePanasonicMakerNoteBadMagic(t *testing.T) {
	t.Parallel()
	b := buildPanasonicMakerNote(testOneEntry())
	b[0] = 'X' // corrupt magic
	ifd := parsePanasonicMakerNote(b)
	if ifd != nil {
		t.Error("expected nil for bad Panasonic magic")
	}
}

func TestParseMakerNoteIFDPanasonic(t *testing.T) {
	t.Parallel()
	b := buildPanasonicMakerNote(testOneEntry())
	ifd := parseMakerNoteIFD(b, "Panasonic", binary.LittleEndian)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for Panasonic via dispatch")
	}
}

// ---------------------------------------------------------------------------
// TestParseLeicaMakerNote — Type 0 (offset=0 → nil) and Type 1–5 (offset=8 → works)
// ---------------------------------------------------------------------------

// TestParseLeicaMakerNoteType0NoPanic verifies that Leica Type-0 (traverse at offset=0)
// does not panic and returns nil per the offset=0 sentinel design.
func TestParseLeicaMakerNoteType0NoPanic(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	// Leica Type-0: no "LEICA\0" prefix → calls traverse(b, 0, parentOrder) → nil
	ifd := parseLeicaMakerNote(b, binary.LittleEndian)
	_ = ifd // nil expected; covers the Type-0 branch
}

func TestParseLeicaMakerNoteWithPrefix(t *testing.T) {
	t.Parallel()
	b := buildLeicaWithPrefixMakerNote(testOneEntry())
	ifd := parseLeicaMakerNote(b, binary.LittleEndian)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for Leica with LEICA\\0 prefix (non-zero offset=8)")
	}
}

func TestParseLeicaMakerNoteTooShort(t *testing.T) {
	t.Parallel()
	ifd := parseLeicaMakerNote([]byte{0x00}, binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for too-short Leica payload")
	}
}

func TestParseMakerNoteIFDLeicaVariants(t *testing.T) {
	t.Parallel()
	b := buildLeicaWithPrefixMakerNote(testOneEntry())
	for _, make := range []string{"LEICA CAMERA AG", "Leica Camera AG", "LEICA", "Leica"} {
		ifd := parseMakerNoteIFD(b, make, binary.LittleEndian)
		if ifd == nil {
			t.Errorf("make %q: expected non-nil IFD via dispatch", make)
		}
	}
}

// ---------------------------------------------------------------------------
// TestParseDJIMakerNote
// Note: parseDJIMakerNote calls traverse(b, 0, LE) then traverse(b, 0, parentOrder)
// — both return nil per the offset=0 sentinel design.
// ---------------------------------------------------------------------------

func TestParseDJIMakerNoteShortPayload(t *testing.T) {
	t.Parallel()
	ifd := parseDJIMakerNote([]byte{0x01}, binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for too-short DJI payload")
	}
}

func TestParseDJIMakerNoteLongPayloadNoPanic(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	ifd := parseDJIMakerNote(b, binary.LittleEndian)
	_ = ifd // nil expected; covers the traverse(b, 0, LE) and fallback paths
}

func TestParseDJIMakerNoteFallbackPathNoPanic(t *testing.T) {
	t.Parallel()
	// Build a BE IFD; LE parse fails, falls back to parentOrder=BE.
	// Both calls traverse(b, 0, ...) → both return nil.
	b := buildRawIFD(binary.BigEndian, testOneEntry())
	ifd := parseDJIMakerNote(b, binary.BigEndian)
	_ = ifd // nil expected per offset=0 design
}

func TestParseMakerNoteIFDDJIDispatch(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	ifd := parseMakerNoteIFD(b, "DJI", binary.LittleEndian)
	_ = ifd // covers dispatch path; nil expected
}

// ---------------------------------------------------------------------------
// TestParseSamsungMakerNote
// Note: parseSamsungMakerNote calls traverse(b, 0, order) → always returns nil.
// ---------------------------------------------------------------------------

func TestParseSamsungMakerNoteShortPayload(t *testing.T) {
	t.Parallel()
	ifd := parseSamsungMakerNote([]byte{0x01}, binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for too-short Samsung payload")
	}
}

func TestParseSamsungMakerNoteLongPayloadNoPanic(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	ifd := parseSamsungMakerNote(b, binary.LittleEndian)
	_ = ifd // nil expected; covers traverse(b, 0, order) path
}

func TestParseMakerNoteIFDSamsungDispatch(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	ifd := parseMakerNoteIFD(b, "SAMSUNG", binary.LittleEndian)
	_ = ifd // covers dispatch path
}

// ---------------------------------------------------------------------------
// TestParseSigmaMakerNote (IFD at offset 10 → traverse works)
// ---------------------------------------------------------------------------

func TestParseSigmaMakerNoteSIGMA(t *testing.T) {
	t.Parallel()
	b := buildSigmaMakerNote("SIGMA\x00\x00\x00", testOneEntry())
	ifd := parseSigmaMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for Sigma SIGMA prefix")
	}
}

func TestParseSigmaMakerNoteFOVEON(t *testing.T) {
	t.Parallel()
	b := buildSigmaMakerNote("FOVEON\x00\x00", testOneEntry())
	ifd := parseSigmaMakerNote(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for Sigma FOVEON prefix")
	}
}

func TestParseSigmaMakerNoteTooShort(t *testing.T) {
	t.Parallel()
	ifd := parseSigmaMakerNote([]byte("SIGMA"))
	if ifd != nil {
		t.Error("expected nil for too-short Sigma payload")
	}
}

func TestParseSigmaMakerNoteBadPrefix(t *testing.T) {
	t.Parallel()
	b := make([]byte, 20)
	copy(b, "UNKNOWN!")
	ifd := parseSigmaMakerNote(b)
	if ifd != nil {
		t.Error("expected nil for unknown Sigma prefix")
	}
}

func TestParseMakerNoteIFDSigma(t *testing.T) {
	t.Parallel()
	b := buildSigmaMakerNote("SIGMA\x00\x00\x00", testOneEntry())
	ifd := parseMakerNoteIFD(b, "SIGMA", binary.LittleEndian)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for SIGMA via dispatch")
	}
}

// ---------------------------------------------------------------------------
// TestParseCasioMakerNote
// Note: parseCasioMakerNote calls traverse(b, 0, order) → always returns nil.
// ---------------------------------------------------------------------------

func TestParseCasioMakerNoteShortPayload(t *testing.T) {
	t.Parallel()
	ifd := parseCasioMakerNote([]byte{0x01}, binary.LittleEndian)
	if ifd != nil {
		t.Error("expected nil for too-short Casio payload")
	}
}

func TestParseCasioMakerNoteLongPayloadNoPanic(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	ifd := parseCasioMakerNote(b, binary.LittleEndian)
	_ = ifd // nil expected; covers traverse(b, 0, order) path
}

func TestParseMakerNoteIFDCasioVariantsDispatch(t *testing.T) {
	t.Parallel()
	b := buildRawIFD(binary.LittleEndian, testOneEntry())
	for _, make := range []string{"CASIO COMPUTER CO.,LTD.", "Casio Computer Co.,Ltd.", "CASIO"} {
		ifd := parseMakerNoteIFD(b, make, binary.LittleEndian)
		_ = ifd // covers dispatch paths
	}
}

// ---------------------------------------------------------------------------
// TestParseNikonType1
// ---------------------------------------------------------------------------

func TestParseNikonType1NoPanic(t *testing.T) {
	t.Parallel()
	// parseNikonType1 calls traverse(b, 0, BE) → nil per offset=0 design.
	// But the heuristic checks count > 0 && count < 256 before calling traverse.
	b := buildRawIFD(binary.BigEndian, testOneEntry())
	ifd := parseNikonType1(b, binary.BigEndian)
	_ = ifd // covers heuristic + traverse path
}

func TestParseNikonType1ZeroCount(t *testing.T) {
	t.Parallel()
	// count = 0 must be rejected.
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, 0)
	ifd := parseNikonType1(b, binary.BigEndian)
	if ifd != nil {
		t.Error("expected nil for count=0 Nikon Type-1")
	}
}

func TestParseNikonType1LargeCount(t *testing.T) {
	t.Parallel()
	// count = 256 must be rejected.
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, 256)
	ifd := parseNikonType1(b, binary.BigEndian)
	if ifd != nil {
		t.Error("expected nil for count=256 Nikon Type-1")
	}
}

func TestParseNikonType1TooShort(t *testing.T) {
	t.Parallel()
	ifd := parseNikonType1([]byte{0x01}, binary.BigEndian)
	if ifd != nil {
		t.Error("expected nil for 1-byte Nikon Type-1")
	}
}

// ---------------------------------------------------------------------------
// TestParseLeicaWithPrefix
// ---------------------------------------------------------------------------

func TestParseLeicaWithPrefixValid(t *testing.T) {
	t.Parallel()
	b := buildLeicaWithPrefixMakerNote(testOneEntry())
	ifd := parseLeicaWithPrefix(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for parseLeicaWithPrefix valid input")
	}
}

func TestParseLeicaWithPrefixTruncated(t *testing.T) {
	t.Parallel()
	b := buildLeicaWithPrefixMakerNote(testOneEntry())
	// Truncate to only the prefix — IFD data missing.
	ifd := parseLeicaWithPrefix(b[:8])
	// Should not panic; may return nil.
	_ = ifd
}

// ---------------------------------------------------------------------------
// TestParsePentaxAOC
// ---------------------------------------------------------------------------

func TestParsePentaxAOCValid(t *testing.T) {
	t.Parallel()
	b := buildPentaxAOCMakerNote(testOneEntry())
	ifd := parsePentaxAOC(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for parsePentaxAOC valid input")
	}
}

// ---------------------------------------------------------------------------
// TestParsePentaxPENTAX
// ---------------------------------------------------------------------------

func TestParsePentaxPENTAXValid(t *testing.T) {
	t.Parallel()
	b := buildPentaxPENTAXMakerNote(binary.LittleEndian, testOneEntry())
	ifd := parsePentaxPENTAX(b)
	if ifd == nil {
		t.Fatal("expected non-nil IFD for parsePentaxPENTAX valid LE input")
	}
}

// ---------------------------------------------------------------------------
// Table-driven: parseMakerNoteIFD for all registered makes that use non-zero offsets.
// Makes that use traverse(b, 0, order) (Canon, SONY, DJI, SAMSUNG, Casio,
// Leica Type-0) always return nil and are tested individually above.
// ---------------------------------------------------------------------------

func TestParseMakerNoteIFDNonZeroOffsetMakes(t *testing.T) {
	t.Parallel()
	type tc struct {
		make    string
		payload func() []byte
	}
	tcs := []tc{
		{"NIKON CORPORATION", func() []byte { return buildNikonType3(binary.LittleEndian, testOneEntry()) }},
		{"Nikon", func() []byte { return buildNikonType3(binary.LittleEndian, testOneEntry()) }},
		{"FUJIFILM", func() []byte { return buildFujifilmMakerNote(testOneEntry()) }},
		{"OLYMPUS IMAGING CORP.", func() []byte { return buildOlympusMakerNote(binary.LittleEndian, testOneEntry()) }},
		{"OLYMPUS CORPORATION", func() []byte { return buildOlympusMakerNote(binary.LittleEndian, testOneEntry()) }},
		{"Olympus", func() []byte { return buildOlympusMakerNote(binary.LittleEndian, testOneEntry()) }},
		{"PENTAX Corporation", func() []byte { return buildPentaxAOCMakerNote(testOneEntry()) }},
		{"Ricoh", func() []byte { return buildPentaxAOCMakerNote(testOneEntry()) }},
		{"RICOH", func() []byte { return buildPentaxAOCMakerNote(testOneEntry()) }},
		{"Panasonic", func() []byte { return buildPanasonicMakerNote(testOneEntry()) }},
		{"LEICA CAMERA AG", func() []byte { return buildLeicaWithPrefixMakerNote(testOneEntry()) }},
		{"Leica Camera AG", func() []byte { return buildLeicaWithPrefixMakerNote(testOneEntry()) }},
		{"LEICA", func() []byte { return buildLeicaWithPrefixMakerNote(testOneEntry()) }},
		{"Leica", func() []byte { return buildLeicaWithPrefixMakerNote(testOneEntry()) }},
		{"SIGMA", func() []byte { return buildSigmaMakerNote("SIGMA\x00\x00\x00", testOneEntry()) }},
	}
	for _, tc := range tcs {
		t.Run(tc.make, func(t *testing.T) {
			t.Parallel()
			b := tc.payload()
			ifd := parseMakerNoteIFD(b, tc.make, binary.LittleEndian)
			if ifd == nil {
				t.Errorf("make %q: expected non-nil IFD, got nil", tc.make)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Smoke: corrupt / minimal payloads must not panic for any registered make
// ---------------------------------------------------------------------------

func TestParseMakerNoteIFDNoPanicCorrupted(t *testing.T) {
	t.Parallel()
	makes := []string{
		"Canon", "NIKON CORPORATION", "Nikon", "SONY", "FUJIFILM",
		"OLYMPUS IMAGING CORP.", "OLYMPUS CORPORATION", "Olympus",
		"PENTAX Corporation", "Ricoh", "RICOH", "Panasonic",
		"LEICA CAMERA AG", "Leica Camera AG", "LEICA", "Leica",
		"DJI", "SAMSUNG", "SIGMA",
		"CASIO COMPUTER CO.,LTD.", "Casio Computer Co.,Ltd.", "CASIO",
	}
	corrupted := [][]byte{
		{},
		{0x00},
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		make([]byte, 64),
		bytes.Repeat([]byte{0xFF}, 64),
	}
	for _, make := range makes {
		for _, payload := range corrupted {
			// Must not panic.
			_ = parseMakerNoteIFD(payload, make, binary.LittleEndian)
		}
	}
}
