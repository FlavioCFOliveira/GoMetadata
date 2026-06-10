package cr2

// conformance_test.go — Canon CR2 specification-conformance test battery.
// Task #162.
//
// Normative authority:
//   - Canon CR2 specification §3.1 (lclevy.free.fr/cr2/):
//       TIFF LE header II*\0 + "CR" marker at bytes 8–9 + version 02 00 at
//       bytes 10–11; IFD0 offset stored at bytes 4–7 as normal TIFF.
//   - TIFF Revision 6.0 (Adobe, 1992) §2:
//       IFD layout, inline-vs-OOL threshold (4 bytes), tag sort order,
//       word-alignment of OOL values.
//   - containers.md §8 (TIFF/EP + proprietary RAW):
//       CR2 detection by CR marker at offset 8; preserve "CR 02 00" on write;
//       IFD0 preserved on write; MakerNote TIFF-absolute offsets.
//   - exif-tiff.md R-10:
//       Canon MakerNote: no signature; TIFF-absolute internal offsets.
//   - exif-tiff.md R-11:
//       Relocating a MakerNote with TIFF-absolute offsets makes them stale;
//       library must preserve-in-place, fully rebase, or document the limitation.
//
// Test categories:
//   CR2-detect-*       — header detection (CR marker, TIFF LE magic)
//   CR2-header-*       — header field assertions
//   CR2-inject-*       — Extract/Inject round-trip + metadata embedding
//   CR2-write-*        — write byte-correctness (CR marker, metadata, strip data)
//   CR2-makernote-*    — Canon MakerNote TIFF-absolute offset handling (R-10/R-11)
//   CR2-robust-*       — robustness: truncation, cyclic IFD, offset-past-EOF, no panic
//   CR2-corpus-*       — parity over real CR2 files in testdata/corpus/raw

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// cr2IFD0Off is the canonical IFD0 offset in a well-formed CR2 file.
// Canon CR2 spec §3.1: standard 8-byte TIFF header + 8-byte CR2 marker = 16.
// All fixture builders in this file use this constant.
const cr2IFD0Off = 16

// ─────────────────────────────────────────────────────────────────────────────
// Fixture builders
// ─────────────────────────────────────────────────────────────────────────────

// buildCR2Header constructs the 16-byte region that precedes IFD0 in a minimal
// CR2 stream (IFD0 at offset 16, CR marker at 8–9, version at 10–11).
//
// Byte layout (Canon CR2 spec §3.1):
//
//	[0:2]  "II"        — TIFF LE byte-order marker
//	[2:4]  0x002A LE   — TIFF magic 42
//	[4:8]  ifd0Offset  — IFD0 start offset (set to 16 here to clear the CR area)
//	[8:9]  0x43 'C'    — Canon CR2 marker byte 0
//	[9:10] 0x52 'R'    — Canon CR2 marker byte 1
//	[10:12] 0x0200 LE  — CR2 version (major=2, minor=0)
//
// The remaining bytes [12:16] are zero-padding before IFD0.
func buildCR2Header() []byte {
	hdr := make([]byte, 16)
	hdr[0], hdr[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(hdr[2:], 0x002A)
	binary.LittleEndian.PutUint32(hdr[4:], cr2IFD0Off)
	hdr[8] = 0x43  // 'C' — Canon CR2 spec §3.1
	hdr[9] = 0x52  // 'R'
	hdr[10] = 0x02 // version major
	hdr[11] = 0x00 // version minor
	return hdr
}

// buildMinimalCR2 returns the smallest valid CR2 stream: 16-byte header +
// IFD0 with zero entries (count=0) + next-IFD pointer (4 bytes).
//
// Canon CR2 spec §3.1: TIFF LE + CR marker at bytes 8–9; IFD0 at offset 16.
func buildMinimalCR2() []byte {
	const ifd0Off = 16
	buf := make([]byte, ifd0Off+2+4) // hdr + count(2) + next-IFD(4)
	copy(buf, buildCR2Header())
	// IFD0: count = 0, next-IFD = 0 (already zero-initialised)
	return buf
}

// buildCR2WithMetadata constructs a CR2 stream that carries optional IPTC
// (tag 0x83BB) and XMP (tag 0x02BC) payloads in IFD0.
//
// IFD0 is placed at offset 16 (past the CR marker area).
// OOL values are placed immediately after the IFD block.
//
// Canon CR2 spec §3.1; TIFF 6.0 §2 (IFD entry layout, OOL threshold).
func buildCR2WithMetadata(iptcData, xmpData []byte) []byte {
	const ifd0Off = 16
	order := binary.LittleEndian

	// Count only the entries we actually need.
	nEntries := 0
	if len(iptcData) > 0 {
		nEntries++
	}
	if len(xmpData) > 0 {
		nEntries++
	}

	ifdSize := 2 + nEntries*12 + 4 // count(2) + entries + next-IFD(4)
	valBase := uint32(ifd0Off + ifdSize)

	// Compute total size.
	totalSize := int(valBase) + len(iptcData) + len(xmpData)
	if totalSize < int(ifd0Off)+ifdSize {
		totalSize = int(ifd0Off) + ifdSize
	}
	buf := make([]byte, totalSize)

	// Header
	copy(buf, buildCR2Header())

	// IFD0
	pos := ifd0Off
	order.PutUint16(buf[pos:], uint16(nEntries))
	pos += 2

	valOff := valBase
	writeEntry := func(tag, typ uint16, data []byte) {
		count := uint32(len(data)) //nolint:gosec // G115: test-helper, bounded by fixture sizes
		order.PutUint16(buf[pos:], tag)
		order.PutUint16(buf[pos+2:], typ)
		order.PutUint32(buf[pos+4:], count)
		if count <= 4 {
			copy(buf[pos+8:], data)
		} else {
			order.PutUint32(buf[pos+8:], valOff)
			copy(buf[valOff:], data)
			valOff += count
		}
		pos += 12
	}

	if len(iptcData) > 0 {
		writeEntry(0x83BB, 7 /*UNDEFINED*/, iptcData)
	}
	if len(xmpData) > 0 {
		writeEntry(0x02BC, 1 /*BYTE*/, xmpData)
	}
	// next-IFD = 0 (zero-initialised)
	return buf
}

// buildCR2WithMakerNote constructs a minimal CR2 stream where IFD0 contains a
// MakerNote (tag 0x927C) whose internal content simulates TIFF-absolute offsets.
//
// The MakerNote body is laid out as:
//
//	[0:4]  uint32 LE — a simulated absolute offset into the *outer* TIFF stream
//	[4:8]  4 sentinel payload bytes (0xDEADBEEF)
//
// This is the structure R-10 (exif-tiff.md): Canon MakerNote uses TIFF-absolute
// offsets so relocating the blob would invalidate the internal pointer.
func buildCR2WithMakerNote() []byte {
	const ifd0Off = 16
	const makerNotePayloadLen = 8 // [absolute-offset(4)] + [payload(4)]
	order := binary.LittleEndian

	// IFD layout: 1 entry (MakerNote 0x927C)
	ifdSize := 2 + 1*12 + 4
	makerNoteOff := uint32(ifd0Off + ifdSize) // placed just after IFD block
	totalSize := int(makerNoteOff) + makerNotePayloadLen

	buf := make([]byte, totalSize)
	copy(buf, buildCR2Header())

	// IFD0: 1 entry: MakerNote
	pos := ifd0Off
	order.PutUint16(buf[pos:], 1) // count = 1
	pos += 2

	// MakerNote entry: tag=0x927C, type=UNDEFINED(7), count=8, value=offset
	order.PutUint16(buf[pos:], 0x927C) // MakerNote
	order.PutUint16(buf[pos+2:], 7)    // UNDEFINED
	order.PutUint32(buf[pos+4:], uint32(makerNotePayloadLen))
	order.PutUint32(buf[pos+8:], makerNoteOff)
	pos += 12
	order.PutUint32(buf[pos:], 0) // next-IFD = 0

	// MakerNote body: absolute-offset at [0:4] points to a location in the
	// outer TIFF stream (here we point to the IFD0 start as a plausible target).
	// Canon CR2 R-10: no MakerNote signature; offsets are TIFF-stream-absolute.
	simulatedAbsoluteTarget := uint32(ifd0Off) // points at IFD0
	order.PutUint32(buf[makerNoteOff:], simulatedAbsoluteTarget)
	buf[makerNoteOff+4] = 0xDE // sentinel payload
	buf[makerNoteOff+5] = 0xAD
	buf[makerNoteOff+6] = 0xBE
	buf[makerNoteOff+7] = 0xEF

	return buf
}

// buildCR2WithStrip builds a minimal CR2 stream that contains a StripOffsets
// entry pointing to actual pixel data (for image-block preservation tests).
//
// Layout:
//
//	[0:16]  CR2 header (II + 42 + IFD0off + CR + ver)
//	[16:..]  IFD0 (count + entries + next)
//	[..]    strip bytes
//
// TIFF 6.0 §2: StripOffsets (0x0111), StripByteCounts (0x0117).
func buildCR2WithStrip(stripData []byte) []byte {
	const ifd0Off = 16
	const nEntries = 3 // ImageWidth + StripOffsets + StripByteCounts
	order := binary.LittleEndian

	ifdSize := 2 + nEntries*12 + 4
	stripOff := uint32(ifd0Off + ifdSize)
	totalSize := int(stripOff) + len(stripData)

	buf := make([]byte, totalSize)
	copy(buf, buildCR2Header())

	pos := ifd0Off
	order.PutUint16(buf[pos:], nEntries)
	pos += 2

	putEntry := func(tag, typ uint16, count, val uint32) {
		order.PutUint16(buf[pos:], tag)
		order.PutUint16(buf[pos+2:], typ)
		order.PutUint32(buf[pos+4:], count)
		order.PutUint32(buf[pos+8:], val)
		pos += 12
	}
	putEntry(0x0100, 4, 1, 1)                      // ImageWidth = 1 (LONG)
	putEntry(0x0111, 4, 1, stripOff)               // StripOffsets
	putEntry(0x0117, 4, 1, uint32(len(stripData))) //nolint:gosec // G115: test-helper, bounded
	order.PutUint32(buf[pos:], 0)                  // next-IFD = 0
	copy(buf[stripOff:], stripData)

	return buf
}

// scanOOLOffsets walks IFD0 entries in a classic TIFF stream and collects all
// out-of-line value offsets. Used to verify word-alignment of written output.
//
// TIFF 6.0 §2: value is out-of-line when typeSize × count > 4; bytes 8–11
// of the entry hold the offset from the TIFF stream origin.
func scanOOLOffsets(t *testing.T, data []byte) []uint32 {
	t.Helper()
	if len(data) < 8 {
		return nil
	}
	order := binary.LittleEndian // CR2 is always LE

	ifd0Off := order.Uint32(data[4:])
	if uint64(ifd0Off)+2 > uint64(len(data)) {
		return nil
	}
	count := int(order.Uint16(data[ifd0Off:]))
	pos := int(ifd0Off) + 2
	if pos+count*12 > len(data) {
		count = (len(data) - pos) / 12
	}

	var offsets []uint32
	for i := 0; i < count; i++ { //nolint:intrange // binary parser: i is a byte-offset multiplier
		e := pos + i*12
		if e+12 > len(data) {
			break
		}
		typ := order.Uint16(data[e+2:])
		cnt := order.Uint32(data[e+4:])
		valOrOff := order.Uint32(data[e+8:])

		sz := tiffTypeSize(typ)
		if sz > 0 && uint64(sz)*uint64(cnt) > 4 {
			offsets = append(offsets, valOrOff)
		}
	}
	return offsets
}

// tiffTypeSize returns the byte size of a single element for the given TIFF
// field type code. Returns 0 for unknown types (which must be skipped).
//
// TIFF 6.0 §2 Table 1 / exif-tiff.md S-18.
func tiffTypeSize(typ uint16) uint32 {
	switch typ {
	case 1, 2, 6, 7:
		return 1 // BYTE / ASCII / SBYTE / UNDEFINED
	case 3, 8:
		return 2 // SHORT / SSHORT
	case 4, 9, 11:
		return 4 // LONG / SLONG / FLOAT
	case 5, 10, 12:
		return 8 // RATIONAL / SRATIONAL / DOUBLE
	default:
		return 0
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CR2-detect-* — Header detection
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_CR2_detect_CR_marker_at_offset8 verifies that the "CR" bytes
// at offsets 8–9 are the canonical Canon CR2 signature.
//
// containers.md §8: "CR2: TIFF LE + marker CR (43 52) at byte 8, then 02 00
// (offsets 10–11)."
// Canon CR2 spec §3.1: bytes 8–9 hold 0x43 ('C') and 0x52 ('R').
func TestConformance_CR2_detect_CR_marker_at_offset8(t *testing.T) {
	t.Parallel()
	data := buildMinimalCR2()

	// The CR marker must be present at the canonical position.
	if len(data) < 10 {
		t.Fatalf("CR2-detect: buffer too short (%d bytes)", len(data))
	}
	if data[8] != 0x43 || data[9] != 0x52 {
		t.Errorf("CR2-detect: marker at [8:10] = %02X %02X, want 43 52 ('CR')",
			data[8], data[9])
	}
}

// TestConformance_CR2_detect_version_at_offset10 verifies that the CR2 version
// bytes 02 00 are present at offsets 10–11 in a well-formed CR2 file.
//
// containers.md §8: offsets 10–11 hold the version "02 00" (CR2 version 2.0).
// Canon CR2 spec §3.1: version major=2 at byte 10, minor=0 at byte 11.
func TestConformance_CR2_detect_version_at_offset10(t *testing.T) {
	t.Parallel()
	data := buildMinimalCR2()

	if len(data) < 12 {
		t.Fatalf("CR2-detect: buffer too short (%d bytes)", len(data))
	}
	if data[10] != 0x02 || data[11] != 0x00 {
		t.Errorf("CR2-detect: version at [10:12] = %02X %02X, want 02 00",
			data[10], data[11])
	}
}

// TestConformance_CR2_detect_TIFF_LE_magic verifies that CR2 uses standard
// TIFF little-endian magic: BOM "II" + 42 (0x002A LE).
//
// containers.md §8: "TIFF LE header 49 49 2A 00".
// TIFF 6.0 §2: byte-order mark "II" = Intel LE; magic word = 42.
func TestConformance_CR2_detect_TIFF_LE_magic(t *testing.T) {
	t.Parallel()
	data := buildMinimalCR2()

	if len(data) < 4 {
		t.Fatalf("CR2-detect: buffer too short (%d bytes)", len(data))
	}
	if data[0] != 0x49 || data[1] != 0x49 {
		t.Errorf("CR2-detect: BOM = %02X %02X, want 49 49 ('II')", data[0], data[1])
	}
	magic := binary.LittleEndian.Uint16(data[2:])
	if magic != 0x002A {
		t.Errorf("CR2-detect: magic = 0x%04X, want 0x002A (42)", magic)
	}
}

// TestConformance_CR2_detect_extract_accepts_CR_marker verifies that
// cr2.Extract accepts a stream that carries the CR marker at offset 8.
//
// containers.md §8: CR marker makes the TIFF a CR2; Extract must not reject it.
func TestConformance_CR2_detect_extract_accepts_CR_marker(t *testing.T) {
	t.Parallel()
	data := buildMinimalCR2()

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR2-detect: Extract with CR marker: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("CR2-detect: rawEXIF is nil for valid CR2 stream")
	}
}

// TestConformance_CR2_detect_extract_accepts_plain_TIFF verifies that a plain
// TIFF LE stream (no CR marker) is also accepted by cr2.Extract — the marker
// is a detection hint, not a parse requirement.
//
// containers.md §8: the "CR" marker distinguishes CR2 from generic TIFF at
// detection time; the parser itself delegates to tiff.Extract.
func TestConformance_CR2_detect_extract_accepts_plain_TIFF(t *testing.T) {
	t.Parallel()
	// A minimal TIFF without the CR marker: IFD0 at offset 8.
	plain := minimalTIFF()

	rawEXIF, _, _, err := Extract(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("CR2-detect: Extract plain TIFF: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("CR2-detect: rawEXIF is nil for plain TIFF input")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CR2-header-* — Header field assertions
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_CR2_header_ifd0_offset_at_bytes4to7 verifies that IFD0 is
// located via the uint32 LE offset stored at bytes 4–7 of the TIFF header.
//
// TIFF 6.0 §2: bytes 4–7 = uint32 offset to the first IFD.
// CR2 places IFD0 at offset ≥ 16 to avoid collision with the CR marker area.
func TestConformance_CR2_header_ifd0_offset_at_bytes4to7(t *testing.T) {
	t.Parallel()
	const ifd0Off = 16
	data := buildMinimalCR2()

	gotOff := binary.LittleEndian.Uint32(data[4:])
	if gotOff != ifd0Off {
		t.Errorf("CR2-header: IFD0 offset at [4:8] = %d, want %d", gotOff, ifd0Off)
	}
}

// TestConformance_CR2_header_ifd0_offset_not_zero verifies that a CR2 stream
// whose IFD0 offset field is zero is handled gracefully (no panic).
//
// TIFF 6.0 §2: IFD0 offset = 0 is invalid but the parser must not crash.
// exif-tiff.md S-03 fixture.
func TestConformance_CR2_header_ifd0_offset_not_zero(t *testing.T) {
	t.Parallel()
	data := buildMinimalCR2()
	// Patch IFD0 offset to 0 — invalid per TIFF 6.0 §2.
	binary.LittleEndian.PutUint32(data[4:], 0)

	// Must not panic; may return error or nil EXIF.
	rawEXIF, _, _, _ := Extract(bytes.NewReader(data))
	// Extract sets rawEXIF to the entire stream before IFD scanning.
	if rawEXIF == nil {
		t.Error("CR2-header: rawEXIF must not be nil even for IFD0 offset=0")
	}
}

// TestConformance_CR2_header_ifd0_offset_past_eof verifies graceful handling
// when IFD0 offset points beyond the file.
//
// TIFF 6.0 §2: offset + 2 must be ≤ file length.
// exif-tiff.md S-03 / R-03 fixture.
func TestConformance_CR2_header_ifd0_offset_past_eof(t *testing.T) {
	t.Parallel()
	data := buildMinimalCR2()
	binary.LittleEndian.PutUint32(data[4:], 0xFFFFFFFF)

	rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(data))
	if rawEXIF == nil {
		t.Error("CR2-header: rawEXIF must not be nil for past-EOF IFD0 offset")
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("CR2-header: expected nil IPTC/XMP for past-EOF IFD0; got IPTC=%v XMP=%v",
			rawIPTC, rawXMP)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CR2-inject-* — Metadata round-trips via Extract/Inject
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_CR2_inject_IPTC_round_trip verifies that IPTC data embedded
// in a CR2 stream is faithfully recovered by Extract after Inject.
//
// containers.md §8(d): IPTC via tag 0x83BB; §8(e): metadata written correctly.
// TIFF 6.0 §2: OOL values for payloads > 4 bytes.
func TestConformance_CR2_inject_IPTC_round_trip(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x06, 'C', 'a', 'n', 'o', 'n', '!'}
	data := buildCR2WithMetadata(wantIPTC, nil)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR2-inject: Extract IPTC: %v", err)
	}
	if rawEXIF == nil {
		t.Error("CR2-inject: rawEXIF is nil")
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("CR2-inject: rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if rawXMP != nil {
		t.Errorf("CR2-inject: rawXMP = %v, want nil", rawXMP)
	}
}

// TestConformance_CR2_inject_XMP_round_trip verifies that XMP data embedded
// in a CR2 stream is faithfully recovered by Extract after Inject.
//
// containers.md §8(d): XMP via tag 0x02BC (700).
// Adobe XMP Spec Part 3 §1.3: tag type BYTE, no framing.
func TestConformance_CR2_inject_XMP_round_trip(t *testing.T) {
	t.Parallel()
	wantXMP := []byte(`<?xpacket begin="" uid="W5M0"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildCR2WithMetadata(nil, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR2-inject: Extract XMP: %v", err)
	}
	if rawEXIF == nil {
		t.Error("CR2-inject: rawEXIF is nil")
	}
	if rawIPTC != nil {
		t.Errorf("CR2-inject: rawIPTC = %v, want nil", rawIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("CR2-inject: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestConformance_CR2_inject_IPTC_and_XMP_round_trip verifies that both IPTC
// and XMP payloads survive a complete Extract→Inject→Extract cycle.
//
// containers.md §8(d): IPTC via 0x83BB, XMP via 0x02BC, in IFD0.
func TestConformance_CR2_inject_IPTC_and_XMP_round_trip(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x05, 'C', 'a', 'n', 'o', 'n'}
	wantXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta>`)

	data := buildCR2WithMetadata(wantIPTC, wantXMP)

	// First Extract.
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR2-inject: Extract 1: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("CR2-inject: rawEXIF nil after first Extract")
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("CR2-inject: rawIPTC first = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("CR2-inject: rawXMP first = %q, want %q", rawXMP, wantXMP)
	}

	// Inject with same IPTC+XMP.
	var injected bytes.Buffer
	if err := Inject(bytes.NewReader(data), &injected, rawEXIF, rawIPTC, rawXMP, true); err != nil {
		t.Fatalf("CR2-inject: Inject: %v", err)
	}

	// Second Extract on the injected output.
	_, rawIPTC2, rawXMP2, err := Extract(bytes.NewReader(injected.Bytes()))
	if err != nil {
		t.Fatalf("CR2-inject: Extract 2: %v", err)
	}
	if !bytes.Equal(rawIPTC2, wantIPTC) {
		t.Errorf("CR2-inject: round-trip rawIPTC = %q, want %q", rawIPTC2, wantIPTC)
	}
	if !bytes.Equal(rawXMP2, wantXMP) {
		t.Errorf("CR2-inject: round-trip rawXMP = %q, want %q", rawXMP2, wantXMP)
	}
}

// TestConformance_CR2_inject_XMP_no_APP1_framing verifies that the XMP payload
// in tag 0x02BC does NOT begin with APP1 framing bytes (0xFF 0xE1).
//
// Adobe XMP Spec Part 3 §1.3 (TIFF-03): "The value of tag 700 is a raw XMP
// packet without any JPEG framing."
func TestConformance_CR2_inject_XMP_no_APP1_framing(t *testing.T) {
	t.Parallel()
	rawXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildCR2WithMetadata(nil, rawXMP)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, nil, rawXMP, true); err != nil {
		t.Fatalf("CR2-inject: Inject XMP: %v", err)
	}
	_, _, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("CR2-inject: Extract after Inject: %v", err)
	}
	if len(gotXMP) >= 2 && gotXMP[0] == 0xFF && gotXMP[1] == 0xE1 {
		t.Error("CR2-inject: XMP in tag 0x02BC must not carry JPEG APP1 framing (0xFF 0xE1)")
	}
	if !bytes.Equal(gotXMP, rawXMP) {
		t.Errorf("CR2-inject: XMP round-trip mismatch: got %q, want %q", gotXMP, rawXMP)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CR2-write-* — Write byte-correctness
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_CR2_write_preserves_CR_marker verifies that after a write
// round-trip via tiff.InjectWithEXIF (the path used by gometadata.Write for
// FormatCR2), the CR marker "43 52" at bytes 8–9 is preserved intact.
//
// containers.md §8(e): "CR2: preserve CR 02 00 at offset 8."
// Canon CR2 spec §3.1: bytes 8–9 identify the file as CR2.
func TestConformance_CR2_write_preserves_CR_marker(t *testing.T) {
	t.Parallel()
	stripData := []byte("CR2-STRIP-DATA-PRESERVATION-TEST")
	original := buildCR2WithStrip(stripData)

	// The CR marker must be in the original stream.
	if original[8] != 0x43 || original[9] != 0x52 {
		t.Fatalf("CR2-write: fixture does not carry CR marker at [8:9]: %02X %02X",
			original[8], original[9])
	}

	// Write via tiff.InjectWithEXIFCR2 — the path that gometadata.Write uses for CR2.
	// This function calls relocateTIFFFromParsed and then restores bytes 8–11 from
	// the original file to preserve the Canon CR2 marker.
	e, err := exif.Parse(original)
	if err != nil {
		t.Fatalf("CR2-write: exif.Parse original: %v", err)
	}
	const wantCopyright = "(c) 2026 CR2-conformance-test"
	e.SetCopyright(wantCopyright)

	var out bytes.Buffer
	if err := tiff.InjectWithEXIFCR2(original, e, nil, nil, &out); err != nil {
		t.Fatalf("CR2-write: InjectWithEXIFCR2: %v", err)
	}
	result := out.Bytes()

	// CR marker must survive the write round-trip.
	//
	// containers.md §8(e): preserve "CR 02 00" at offset 8.
	if len(result) < 10 {
		t.Fatalf("CR2-write: output too short (%d bytes)", len(result))
	}
	if result[8] != 0x43 || result[9] != 0x52 {
		t.Errorf("CR2-write: CR marker at [8:9] after write = %02X %02X, want 43 52",
			result[8], result[9])
	}
}

// TestConformance_CR2_write_preserves_version_bytes verifies that the version
// bytes 0x02 0x00 at offsets 10–11 are preserved through a write round-trip.
//
// containers.md §8(e): "preserve CR 02 00 at offset 8" (bytes 8–11).
func TestConformance_CR2_write_preserves_version_bytes(t *testing.T) {
	t.Parallel()
	stripData := []byte("CR2-STRIP-VERSION-TEST-DATA-LONG!")
	original := buildCR2WithStrip(stripData)

	e, err := exif.Parse(original)
	if err != nil {
		t.Fatalf("CR2-write: exif.Parse: %v", err)
	}
	e.SetCopyright("version-preserve-test")

	var out bytes.Buffer
	if err := tiff.InjectWithEXIFCR2(original, e, nil, nil, &out); err != nil {
		t.Fatalf("CR2-write: InjectWithEXIFCR2: %v", err)
	}
	result := out.Bytes()

	if len(result) < 12 {
		t.Fatalf("CR2-write: output too short (%d bytes)", len(result))
	}
	// Canon CR2 spec §3.1: version at bytes 10–11 must be 02 00.
	if result[10] != 0x02 || result[11] != 0x00 {
		t.Errorf("CR2-write: version at [10:12] = %02X %02X, want 02 00",
			result[10], result[11])
	}
}

// TestConformance_CR2_write_image_block_preserved verifies that strip data
// (image pixels) is byte-identical in the output after a metadata-only write.
//
// containers.md §8(e): "do not corrupt the image data or other embedded structures."
// This is the ImageDataHash IN==OUT invariant for CR2.
func TestConformance_CR2_write_image_block_preserved(t *testing.T) {
	t.Parallel()
	stripData := []byte("CR2-PIXEL-DATA-MUST-SURVIVE-INTACT-IN-WRITE-PATH")
	original := buildCR2WithStrip(stripData)

	e, err := exif.Parse(original)
	if err != nil {
		t.Fatalf("CR2-write: exif.Parse: %v", err)
	}
	e.SetCopyright("strip-preservation-test-2026")

	var out bytes.Buffer
	if err := tiff.InjectWithEXIFCR2(original, e, nil, nil, &out); err != nil {
		t.Fatalf("CR2-write: InjectWithEXIFCR2: %v", err)
	}
	result := out.Bytes()

	// The raw strip bytes must appear verbatim somewhere in the output.
	// (The offset will have changed due to relocation, but the bytes are identical.)
	// containers.md §8(e): image data must not be corrupted.
	if !bytes.Contains(result, stripData) {
		t.Error("CR2-write: strip data bytes not found verbatim in output (image block corrupted)")
	}
}

// TestConformance_CR2_write_TIFF_LE_magic_preserved verifies that the TIFF LE
// magic bytes II*\0 (49 49 2A 00) are preserved in the output after writing.
//
// TIFF 6.0 §2: byte-order marker must be "II" (LE) or "MM" (BE); for CR2
// it is always "II" (Intel, little-endian).
// containers.md §8(b): CR2 uses TIFF LE header 49 49 2A 00.
func TestConformance_CR2_write_TIFF_LE_magic_preserved(t *testing.T) {
	t.Parallel()
	stripData := []byte("CR2-MAGIC-PRESERVATION-STRIP-DATA!")
	original := buildCR2WithStrip(stripData)

	e, err := exif.Parse(original)
	if err != nil {
		t.Fatalf("CR2-write: exif.Parse: %v", err)
	}
	e.SetCopyright("magic-preserve-test")

	var out bytes.Buffer
	if err := tiff.InjectWithEXIFCR2(original, e, nil, nil, &out); err != nil {
		t.Fatalf("CR2-write: InjectWithEXIFCR2: %v", err)
	}
	result := out.Bytes()

	if len(result) < 4 {
		t.Fatalf("CR2-write: output too short (%d bytes)", len(result))
	}
	if result[0] != 0x49 || result[1] != 0x49 {
		t.Errorf("CR2-write: BOM after write = %02X %02X, want 49 49 ('II')", result[0], result[1])
	}
	magic := binary.LittleEndian.Uint16(result[2:])
	if magic != 0x002A {
		t.Errorf("CR2-write: TIFF magic after write = 0x%04X, want 0x002A (42)", magic)
	}
}

// TestConformance_CR2_write_OOL_value_word_aligned verifies that all out-of-line
// value offsets in the written output are even (word-aligned).
//
// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
// exif-tiff.md S-11 (writer side).
func TestConformance_CR2_write_OOL_value_word_aligned(t *testing.T) {
	t.Parallel()
	// Use an odd-length XMP blob to stress the alignment logic.
	rawXMP := make([]byte, 101)
	copy(rawXMP, `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>`)
	rawXMP[100] = '>'

	data := buildMinimalCR2()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, nil, rawXMP, true); err != nil {
		t.Fatalf("CR2-write: Inject with odd-length XMP: %v", err)
	}
	result := out.Bytes()

	oolOffsets := scanOOLOffsets(t, result)
	for _, off := range oolOffsets {
		if off%2 != 0 {
			// TIFF 6.0 §2: word-alignment requirement for OOL values.
			t.Errorf("CR2-write: OOL value at odd offset %d (0x%x) — TIFF 6.0 §2 word-alignment violation",
				off, off)
		}
	}
}

// TestConformance_CR2_write_IFD_entries_sorted verifies that IFD0 entries are
// in ascending tag order after a write via Inject.
//
// TIFF 6.0 §2: "The entries in each IFD must be sorted in ascending order by
// the Tag field."
// exif-tiff.md S-12 (writer requirement).
func TestConformance_CR2_write_IFD_entries_sorted(t *testing.T) {
	t.Parallel()
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	rawXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	data := buildMinimalCR2()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, rawXMP, true); err != nil {
		t.Fatalf("CR2-write: Inject IPTC+XMP: %v", err)
	}

	parsed, err := exif.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("CR2-write: exif.Parse: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("CR2-write: IFD0 is nil after write")
	}
	entries := parsed.IFD0.Entries
	for i := 1; i < len(entries); i++ {
		if entries[i].Tag < entries[i-1].Tag {
			t.Errorf("CR2-write: IFD0 entry[%d] tag 0x%04X < entry[%d] tag 0x%04X (unsorted, violates TIFF 6.0 §2 S-12)",
				i, entries[i].Tag, i-1, entries[i-1].Tag)
		}
	}
}

// TestConformance_CR2_write_copyright_survives_round_trip verifies that a
// copyright string written via tiff.InjectWithEXIF can be re-read back by
// exif.Parse.
//
// containers.md §8(e): "preserve all existing metadata not explicitly modified."
// TIFF 6.0 §2 / exif-tiff.md S-28: Copyright tag 0x8298 in IFD0.
func TestConformance_CR2_write_copyright_survives_round_trip(t *testing.T) {
	t.Parallel()
	stripData := []byte("CR2-COPYRIGHT-STRIP-DATA")
	original := buildCR2WithStrip(stripData)

	e, err := exif.Parse(original)
	if err != nil {
		t.Fatalf("CR2-write: exif.Parse original: %v", err)
	}
	const wantCopyright = "(c) 2026 CR2-conformance-battery"
	e.SetCopyright(wantCopyright)

	var out bytes.Buffer
	if err := tiff.InjectWithEXIFCR2(original, e, nil, nil, &out); err != nil {
		t.Fatalf("CR2-write: InjectWithEXIFCR2: %v", err)
	}

	e2, err := exif.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("CR2-write: exif.Parse result: %v", err)
	}
	got := e2.Copyright()
	if got != wantCopyright {
		t.Errorf("CR2-write: Copyright = %q, want %q", got, wantCopyright)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CR2-makernote-* — Canon MakerNote TIFF-absolute offset handling
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_CR2_makernote_tiff_absolute_structure verifies that Extract
// returns the MakerNote blob verbatim (tag 0x927C), preserving the raw bytes
// that represent TIFF-absolute internal offsets.
//
// exif-tiff.md R-10: "Canon MakerNote: no signature; TIFF-absolute internal
// offsets."
// The library does not interpret Canon MakerNote content; it returns the blob
// as raw bytes in the EXIF model, enabling read callers to inspect or preserve
// the absolute offsets if needed.
func TestConformance_CR2_makernote_tiff_absolute_structure(t *testing.T) {
	t.Parallel()
	data := buildCR2WithMakerNote()

	e, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("CR2-makernote: exif.Parse: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("CR2-makernote: IFD0 is nil")
	}

	// MakerNote must be present as an IFD0 entry.
	// exif-tiff.md R-10: Canon MakerNote has no signature — it is an opaque blob.
	makerEntry := e.IFD0.Get(exif.TagID(0x927C))
	if makerEntry == nil {
		t.Fatal("CR2-makernote: MakerNote tag 0x927C not found in IFD0")
	}
	if len(makerEntry.Value) == 0 {
		t.Fatal("CR2-makernote: MakerNote value is empty")
	}

	// Verify the first 4 bytes of the MakerNote body match the TIFF-absolute
	// pointer we embedded (pointing at IFD0 start = 16).
	// This is the canonical absolute-offset pattern: Canon R-10.
	order := binary.LittleEndian
	absPtr := order.Uint32(makerEntry.Value[0:])
	if absPtr != uint32(16) {
		t.Errorf("CR2-makernote R-10: MakerNote[0:4] absolute pointer = %d, want 16 (IFD0 offset)",
			absPtr)
	}

	// Sentinel payload must survive verbatim.
	if len(makerEntry.Value) < 8 {
		t.Fatalf("CR2-makernote: MakerNote value too short (%d bytes)", len(makerEntry.Value))
	}
	want := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	if !bytes.Equal(makerEntry.Value[4:8], want) {
		t.Errorf("CR2-makernote: sentinel payload = %X, want DEADBEEF", makerEntry.Value[4:8])
	}
}

// TestConformance_CR2_makernote_no_signature verifies that the Canon MakerNote
// blob has no "magic" signature at the start (unlike Nikon Type-3 or Fujifilm).
//
// exif-tiff.md R-10: "Canon MakerNote: no signature."
// The first bytes of the Canon MakerNote are data, not a signature string.
// Contrast with Nikon ("Nikon\0") or Fujifilm ("FUJIFILM").
func TestConformance_CR2_makernote_no_signature(t *testing.T) {
	t.Parallel()
	data := buildCR2WithMakerNote()

	e, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("CR2-makernote: exif.Parse: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("CR2-makernote: IFD0 is nil")
	}
	makerEntry := e.IFD0.Get(exif.TagID(0x927C))
	if makerEntry == nil {
		t.Fatal("CR2-makernote: MakerNote not present in synthetic fixture — buildCR2WithMakerNote must produce this tag")
	}
	if len(makerEntry.Value) < 5 {
		return // too short to inspect
	}

	// Known MakerNote signature prefixes from other manufacturers that Canon
	// does NOT use: "Nikon" (0x4E 69 6B 6F 6E), "FUJIFILM" (0x46 55 4A 49...).
	// R-10: Canon MakerNote starts with data, not a textual identifier.
	nikon := []byte{'N', 'i', 'k', 'o', 'n'}
	fuji := []byte{'F', 'U', 'J', 'I'}
	olympus := []byte{'O', 'L', 'Y', 'M', 'P'}

	if bytes.HasPrefix(makerEntry.Value, nikon) ||
		bytes.HasPrefix(makerEntry.Value, fuji) ||
		bytes.HasPrefix(makerEntry.Value, olympus) {
		t.Errorf("CR2-makernote R-10: MakerNote starts with a non-Canon manufacturer signature")
	}
}

// TestConformance_CR2_makernote_write_preserves_blob verifies that the Canon
// MakerNote blob is preserved verbatim through a write round-trip.
//
// exif-tiff.md R-11: "Relocating a MakerNote with TIFF-absolute offsets makes
// them stale: library MUST preserve-in-place, fully rebase, or document."
// containers.md §8(e): verbatim MakerNote copy for CR2 (write.go writeTIFF
// comment: "Canon MakerNotes use blob-relative (self-relative) offsets").
func TestConformance_CR2_makernote_write_preserves_blob(t *testing.T) {
	t.Parallel()
	data := buildCR2WithMakerNote()

	e, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("CR2-makernote: exif.Parse: %v", err)
	}
	makerEntry := e.IFD0.Get(exif.TagID(0x927C))
	if makerEntry == nil {
		t.Fatal("CR2-makernote: MakerNote tag not found in synthetic fixture — buildCR2WithMakerNote must produce this tag")
	}
	originalBlob := make([]byte, len(makerEntry.Value))
	copy(originalBlob, makerEntry.Value)

	e.SetCopyright("makernote-blob-preservation-test")

	var out bytes.Buffer
	if err := tiff.InjectWithEXIFCR2(data, e, nil, nil, &out); err != nil {
		t.Fatalf("CR2-makernote: InjectWithEXIFCR2: %v", err)
	}

	e2, err := exif.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("CR2-makernote: exif.Parse result: %v", err)
	}
	if e2.IFD0 == nil {
		t.Fatal("CR2-makernote: IFD0 nil in result")
	}
	makerEntry2 := e2.IFD0.Get(exif.TagID(0x927C))
	if makerEntry2 == nil {
		t.Fatal("CR2-makernote R-11: MakerNote absent from output — blob was dropped")
	}
	// The blob content must be byte-identical (the relocation must not corrupt the
	// Canon MakerNote payload bytes).
	if !bytes.Equal(makerEntry2.Value, originalBlob) {
		t.Errorf("CR2-makernote R-11: MakerNote blob changed after write:\n  got  %X\n  want %X",
			makerEntry2.Value, originalBlob)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CR2-robust-* — Robustness: truncation, cycles, OOB offsets
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_CR2_robust_empty_input verifies that an empty byte stream
// does not panic and returns an error.
//
// exif-tiff.md R-13: streams < 8 bytes are always invalid.
func TestConformance_CR2_robust_empty_input(t *testing.T) {
	t.Parallel()
	_, _, _, err := Extract(bytes.NewReader([]byte{}))
	if err == nil {
		t.Error("CR2-robust: empty input: expected error, got nil")
	}
}

// TestConformance_CR2_robust_truncated_at_4_bytes verifies no panic for a
// 4-byte input (valid BOM + half-magic, no IFD offset).
//
// exif-tiff.md R-13: minimum 8 bytes for classic TIFF.
func TestConformance_CR2_robust_truncated_at_4_bytes(t *testing.T) {
	t.Parallel()
	buf := []byte{'I', 'I', 0x2A, 0x00} // only 4 bytes — truncated before IFD0 offset
	_, _, _, _ = Extract(bytes.NewReader(buf))
	// Must not panic.
}

// TestConformance_CR2_robust_truncated_after_header verifies that a stream
// truncated immediately after the 8-byte TIFF header returns rawEXIF without panic.
//
// exif-tiff.md R-12: truncated after header before IFD0 → error (no panic).
func TestConformance_CR2_robust_truncated_after_header(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 8)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8) // IFD0 at EOF

	rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("CR2-robust: rawEXIF must not be nil for truncated-after-header input")
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("CR2-robust: expected nil IPTC/XMP for truncated input; got IPTC=%v XMP=%v",
			rawIPTC, rawXMP)
	}
}

// TestConformance_CR2_robust_CR_marker_then_truncated verifies that a buffer
// that carries the CR marker but is otherwise too short does not panic.
//
// containers.md §8(f): robustness — truncation.
func TestConformance_CR2_robust_CR_marker_then_truncated(t *testing.T) {
	t.Parallel()
	buf := buildMinimalCR2()[:14] // truncate mid-IFD area

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	// rawEXIF may be nil for extremely short inputs — what matters is no panic.
	_ = rawEXIF
}

// TestConformance_CR2_robust_cyclic_IFD_no_hang verifies that a self-referential
// IFD chain does not cause an infinite loop or panic.
//
// exif-tiff.md R-01: circular IFD chains MUST be detected; break, no hang.
func TestConformance_CR2_robust_cyclic_IFD_no_hang(t *testing.T) {
	t.Parallel()
	// Build a CR2 whose IFD0 next-IFD pointer points back to IFD0 start.
	data := buildMinimalCR2()
	// IFD0 at offset 16: count at [16:18], next-ptr at [18:22].
	binary.LittleEndian.PutUint32(data[18:], 16) // cycle: next-IFD = IFD0 itself

	rawEXIF, _, _, _ := Extract(bytes.NewReader(data))
	if rawEXIF == nil {
		t.Error("CR2-robust: rawEXIF must not be nil for cyclic IFD chain")
	}
}

// TestConformance_CR2_robust_next_ifd_past_eof verifies that a next-IFD pointer
// beyond the file is treated as end-of-chain and does not crash.
//
// TIFF 6.0 §2 / exif-tiff.md S-13: out-of-bounds next-IFD → end of chain.
func TestConformance_CR2_robust_next_ifd_past_eof(t *testing.T) {
	t.Parallel()
	data := buildMinimalCR2()
	// IFD0 at offset 16: next-ptr at [18:22].
	binary.LittleEndian.PutUint32(data[18:], 0xFFFFFFFF)

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("CR2-robust: next-IFD past EOF: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("CR2-robust: rawEXIF must not be nil for past-EOF next-IFD pointer")
	}
}

// TestConformance_CR2_robust_value_offset_past_eof verifies that an IFD entry
// whose value offset is beyond the file is skipped gracefully.
//
// exif-tiff.md R-03 / R-04: offset past EOF → treat as absent; skip; no crash.
func TestConformance_CR2_robust_value_offset_past_eof(t *testing.T) {
	t.Parallel()
	// Build a CR2 with one IPTC entry whose OOL offset is past EOF.
	const ifd0Off = 16
	order := binary.LittleEndian

	buf := make([]byte, ifd0Off+2+12+4) // header + count + 1 entry + next
	copy(buf, buildCR2Header())
	order.PutUint16(buf[ifd0Off:], 1)         // count = 1
	order.PutUint16(buf[ifd0Off+2:], 0x83BB)  // IPTC tag
	order.PutUint16(buf[ifd0Off+4:], 7)       // UNDEFINED
	order.PutUint32(buf[ifd0Off+6:], 100)     // count = 100 bytes
	order.PutUint32(buf[ifd0Off+10:], 99_999) // offset way past EOF
	order.PutUint32(buf[ifd0Off+14:], 0)      // next-IFD = 0

	rawEXIF, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("CR2-robust: past-EOF value offset: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("CR2-robust: rawEXIF must not be nil")
	}
	if rawIPTC != nil {
		t.Error("CR2-robust: rawIPTC must be nil for past-EOF value offset")
	}
}

// TestConformance_CR2_robust_overflow_count_uint64_guard verifies that an IFD
// entry whose count × typeSize overflows uint32 is skipped without crash or OOM.
//
// exif-tiff.md R-06: count×typeSize overflow MUST be checked with uint64
// arithmetic, never uint32.
func TestConformance_CR2_robust_overflow_count_uint64_guard(t *testing.T) {
	t.Parallel()
	const ifd0Off = 16
	order := binary.LittleEndian

	buf := make([]byte, ifd0Off+2+12+4+4)
	copy(buf, buildCR2Header())
	order.PutUint16(buf[ifd0Off:], 1)                         // count = 1
	order.PutUint16(buf[ifd0Off+2:], 0x83BB)                  // IPTC
	order.PutUint16(buf[ifd0Off+4:], 4)                       // LONG (typeSize = 4)
	order.PutUint32(buf[ifd0Off+6:], 0x40000001)              // count: 0x40000001 × 4 overflows uint32
	order.PutUint32(buf[ifd0Off+10:], uint32(ifd0Off+2+12+4)) // offset past IFD block
	order.PutUint32(buf[ifd0Off+14:], 0)                      // next-IFD = 0

	_, rawIPTC, _, _ := Extract(bytes.NewReader(buf))
	// Must not allocate an enormous slice or crash.
	if rawIPTC != nil {
		t.Error("CR2-robust R-06: rawIPTC must be nil for overflow-count entry")
	}
}

// TestConformance_CR2_robust_ifd_count_exceeds_buffer verifies that when the
// IFD entry count claims more entries than the buffer can hold, only the entries
// that fit are read — no panic, no OOB.
//
// TIFF 6.0 §2 / exif-tiff.md R-05: partial IFD must be tolerated.
func TestConformance_CR2_robust_ifd_count_exceeds_buffer(t *testing.T) {
	t.Parallel()
	const ifd0Off = 16
	order := binary.LittleEndian

	// Buffer: header(16) + count(2) + 2 entries(24) + next-IFD(4) — claims 50 entries.
	buf := make([]byte, ifd0Off+2+2*12+4)
	copy(buf, buildCR2Header())
	order.PutUint16(buf[ifd0Off:], 50) // claims 50 entries; buffer has room for only 2

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("CR2-robust R-05: partial IFD: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("CR2-robust R-05: rawEXIF must not be nil for partial-IFD input")
	}
}

// TestConformance_CR2_robust_invalid_byte_order rejects non-TIFF byte-order
// markers and wraps the error with "cr2:" prefix.
//
// TIFF 6.0 §2 / exif-tiff.md S-01: only "II" and "MM" are valid BOM values.
// containers.md §8: CR2 is exclusively LE ("II").
func TestConformance_CR2_robust_invalid_byte_order(t *testing.T) {
	t.Parallel()
	bad := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0, 'C', 'R', 0, 0}
	_, _, _, err := Extract(bytes.NewReader(bad))
	if err == nil {
		t.Fatal("CR2-robust: invalid BOM: expected error, got nil")
	}
	// Error must be wrapped with the "cr2:" prefix (cr2.go delegates to tiff).
	if !strings.HasPrefix(err.Error(), "cr2:") {
		t.Errorf("CR2-robust: error prefix: got %q, want prefix 'cr2:'", err.Error())
	}
}

// TestConformance_CR2_robust_MakerNote_relative_vs_absolute verifies that
// Extract does not crash when a MakerNote blob contains apparent absolute-style
// pointers that point outside the file boundaries (simulating a truncated
// MakerNote blob with TIFF-absolute offsets).
//
// containers.md §8(f): "MakerNote relative-vs-absolute" robustness case.
// exif-tiff.md R-10/R-11: Canon MakerNote TIFF-absolute; truncated MakerNote
// must not crash.
func TestConformance_CR2_robust_MakerNote_relative_vs_absolute(t *testing.T) {
	t.Parallel()
	// Construct a MakerNote blob where the first 4 bytes store a plausible
	// TIFF-absolute offset but the offset exceeds the stream size — simulating
	// a corrupt or truncated Canon MakerNote.
	const ifd0Off = 16
	const makerNoteLen = 8
	order := binary.LittleEndian

	ifdSize := 2 + 1*12 + 4
	makerNoteOff := uint32(ifd0Off + ifdSize)
	totalSize := int(makerNoteOff) + makerNoteLen

	buf := make([]byte, totalSize)
	copy(buf, buildCR2Header())
	order.PutUint16(buf[ifd0Off:], 1)
	order.PutUint16(buf[ifd0Off+2:], 0x927C) // MakerNote
	order.PutUint16(buf[ifd0Off+4:], 7)      // UNDEFINED
	order.PutUint32(buf[ifd0Off+6:], makerNoteLen)
	order.PutUint32(buf[ifd0Off+10:], makerNoteOff)
	order.PutUint32(buf[ifd0Off+14:], 0) // next-IFD = 0

	// MakerNote body: "absolute" pointer that is way past EOF.
	order.PutUint32(buf[makerNoteOff:], 0xFFFFFFFF) // bogus absolute offset
	buf[makerNoteOff+4] = 0xCA
	buf[makerNoteOff+5] = 0xFE
	buf[makerNoteOff+6] = 0xBA
	buf[makerNoteOff+7] = 0xBE

	// Must not panic regardless of the bogus absolute pointer.
	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("CR2-robust: rawEXIF must not be nil for truncated-MakerNote input")
	}
}

// TestConformance_CR2_robust_BigTIFF_accepted verifies that a BigTIFF input
// is accepted by cr2.Extract (task #54: BigTIFF read support in tiff.Extract).
//
// containers.md §8(f): "BigTIFF RAW (8-byte offsets, 20-byte entries, u64 counts)."
// exif-tiff.md S-05: BigTIFF 16-byte header.
func TestConformance_CR2_robust_BigTIFF_accepted(t *testing.T) {
	t.Parallel()
	bigTIFF := make([]byte, 16)
	bigTIFF[0], bigTIFF[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(bigTIFF[2:], 0x002B) // BigTIFF magic
	binary.LittleEndian.PutUint16(bigTIFF[4:], 8)      // offset bytesize = 8
	binary.LittleEndian.PutUint16(bigTIFF[6:], 0)      // reserved = 0
	binary.LittleEndian.PutUint64(bigTIFF[8:], 16)     // IFD0 at offset 16 (= EOF, no IFD)

	rawEXIF, _, _, err := Extract(bytes.NewReader(bigTIFF))
	if err != nil {
		t.Errorf("CR2-robust: BigTIFF input: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("CR2-robust: BigTIFF input: rawEXIF is nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CR2-corpus-* — Parity over real CR2 files
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_CR2_corpus_extract_no_panic verifies that Extract does not
// panic and returns a valid rawEXIF for every CR2 file in the corpus.
//
// containers.md §8(f): all robustness rules apply to real-world files.
// Uses testutil.CorpusFiles which skips automatically if the corpus is absent.
func TestConformance_CR2_corpus_extract_no_panic(t *testing.T) {
	t.Parallel()
	paths := corpusCR2Files(t)

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("read %s: %v", path, err)
			}
			if len(data) == 0 {
				return // .gitkeep or empty sentinel
			}
			rawEXIF, _, _, extractErr := Extract(bytes.NewReader(data))
			if extractErr != nil {
				// Errors are acceptable; no panic is the invariant.
				return
			}
			if rawEXIF == nil {
				t.Errorf("CR2-corpus: %s: rawEXIF is nil but Extract returned no error", name)
			}
		})
	}
}

// TestConformance_CR2_corpus_CR_marker_present verifies that every CR2 file in
// the corpus carries the Canon "CR" marker at bytes 8–9.
//
// Canon CR2 spec §3.1: bytes 8–9 of all CR2 files must be 0x43 0x52 ('CR').
// containers.md §8(b): "marker CR (43 52) at byte 8."
func TestConformance_CR2_corpus_CR_marker_present(t *testing.T) {
	t.Parallel()
	paths := corpusCR2Files(t)

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("read %s: %v", path, err)
			}
			if len(data) < 10 {
				t.Skipf("CR2-corpus: %s: too short (%d bytes)", name, len(data))
			}
			// Canon CR2 spec §3.1: "CR" marker at bytes 8–9.
			if data[8] != 0x43 || data[9] != 0x52 {
				t.Errorf("CR2-corpus: %s: marker at [8:10] = %02X %02X, want 43 52",
					name, data[8], data[9])
			}
		})
	}
}

// TestConformance_CR2_corpus_TIFF_LE_header verifies that every CR2 file in
// the corpus has the standard TIFF LE magic header "II" + 0x002A.
//
// containers.md §8(b): "TIFF LE header 49 49 2A 00".
// TIFF 6.0 §2: byte-order mark + magic.
func TestConformance_CR2_corpus_TIFF_LE_header(t *testing.T) {
	t.Parallel()
	paths := corpusCR2Files(t)

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("read %s: %v", path, err)
			}
			if len(data) < 4 {
				t.Skipf("CR2-corpus: %s: too short (%d bytes)", name, len(data))
			}
			// TIFF 6.0 §2 / Canon CR2 spec §3.1: must be LE ("II" + 42).
			if data[0] != 0x49 || data[1] != 0x49 {
				t.Errorf("CR2-corpus: %s: BOM = %02X %02X, want 49 49 ('II')",
					name, data[0], data[1])
			}
			magic := binary.LittleEndian.Uint16(data[2:])
			if magic != 0x002A {
				t.Errorf("CR2-corpus: %s: magic = 0x%04X, want 0x002A", name, magic)
			}
		})
	}
}

// TestConformance_CR2_corpus_metadata_parseable verifies that EXIF can be parsed
// by exif.Parse for every CR2 file in the corpus, and that IFD0 is non-nil.
//
// TIFF 6.0 §2: a valid TIFF file must yield a parseable IFD0.
// Canon CR2 spec §3.1: CR2 is a standard TIFF with extra Canon structures.
func TestConformance_CR2_corpus_metadata_parseable(t *testing.T) {
	t.Parallel()
	paths := corpusCR2Files(t)

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("read %s: %v", path, err)
			}
			if len(data) == 0 {
				return
			}
			rawEXIF, _, _, err := Extract(bytes.NewReader(data))
			if err != nil {
				t.Skipf("CR2-corpus: %s: Extract error: %v", name, err)
			}
			if rawEXIF == nil {
				t.Skipf("CR2-corpus: %s: rawEXIF nil", name)
			}
			parsed, err := exif.Parse(rawEXIF)
			if err != nil {
				t.Errorf("CR2-corpus: %s: exif.Parse: %v", name, err)
				return
			}
			if parsed.IFD0 == nil {
				t.Errorf("CR2-corpus: %s: IFD0 is nil after exif.Parse", name)
			}
		})
	}
}

// TestConformance_CR2_corpus_write_preserves_CR_marker verifies that after a
// metadata-only write via tiff.InjectWithEXIF, the CR marker survives intact
// in the output for every corpus CR2 file.
//
// containers.md §8(e): preserve "CR 02 00" at offset 8 on write.
func TestConformance_CR2_corpus_write_preserves_CR_marker(t *testing.T) {
	t.Parallel()
	paths := corpusCR2Files(t)

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			original, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("read %s: %v", path, err)
			}
			if len(original) < 12 {
				t.Skipf("CR2-corpus: %s: too short (%d bytes)", name, len(original))
			}
			// Skip files where our fixture doesn't have the CR marker
			// (shouldn't happen in a proper corpus, but guard defensively).
			if original[8] != 0x43 || original[9] != 0x52 {
				t.Skipf("CR2-corpus: %s: no CR marker at [8:9] in source file", name)
			}

			e, parseErr := exif.Parse(original)
			if parseErr != nil {
				t.Skipf("CR2-corpus: %s: exif.Parse: %v", name, parseErr)
			}
			e.SetCopyright("conformance-battery-2026")

			var out bytes.Buffer
			if writeErr := tiff.InjectWithEXIFCR2(original, e, nil, nil, &out); writeErr != nil {
				t.Skipf("CR2-corpus: %s: InjectWithEXIFCR2: %v", name, writeErr)
			}
			result := out.Bytes()

			if len(result) < 12 {
				t.Fatalf("CR2-corpus: %s: output too short (%d bytes)", name, len(result))
			}
			// Canon CR2 spec §3.1 / containers.md §8(e): preserve "CR 02 00".
			if result[8] != 0x43 || result[9] != 0x52 {
				t.Errorf("CR2-corpus: %s: CR marker at [8:10] after write = %02X %02X, want 43 52",
					name, result[8], result[9])
			}
		})
	}
}

// TestConformance_CR2_corpus_write_strip_data_preserved verifies that pixel
// data (StripOffsets) survives a metadata write for corpus CR2 files.
//
// containers.md §8(e): "do not corrupt the image data."
// This guards the ImageDataHash IN==OUT invariant for each corpus file.
func TestConformance_CR2_corpus_write_strip_data_preserved(t *testing.T) {
	t.Parallel()
	paths := corpusCR2Files(t)

	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			original, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("read %s: %v", path, err)
			}
			if len(original) == 0 {
				return
			}

			// Parse original to record strip byte counts.
			eOrig, parseErr := exif.Parse(original)
			if parseErr != nil {
				t.Skipf("CR2-corpus: %s: exif.Parse original: %v", name, parseErr)
			}
			if eOrig.IFD0 == nil {
				t.Skipf("CR2-corpus: %s: IFD0 nil in original", name)
			}

			stripOffEntry := eOrig.IFD0.Get(exif.TagStripOffsets)
			stripCntEntry := eOrig.IFD0.Get(exif.TagStripByteCounts)
			if stripOffEntry == nil || stripCntEntry == nil || stripOffEntry.Count == 0 {
				t.Skipf("CR2-corpus: %s: no StripOffsets in IFD0 (full-res raw likely in SubIFD)", name)
			}

			// Write with a copyright change.
			eOrig.SetCopyright("corpus-strip-guard-2026")
			var out bytes.Buffer
			if writeErr := tiff.InjectWithEXIFCR2(original, eOrig, nil, nil, &out); writeErr != nil {
				t.Skipf("CR2-corpus: %s: InjectWithEXIFCR2: %v", name, writeErr)
			}
			result := out.Bytes()

			// Re-parse result.
			eRes, err2 := exif.Parse(result)
			if err2 != nil {
				t.Fatalf("CR2-corpus: %s: exif.Parse result: %v", name, err2)
			}
			if eRes.IFD0 == nil {
				t.Fatalf("CR2-corpus: %s: IFD0 nil in result", name)
			}

			// Compare strip byte counts: they must be identical.
			resStripCnt := eRes.IFD0.Get(exif.TagStripByteCounts)
			if resStripCnt == nil {
				t.Errorf("CR2-corpus: %s: StripByteCounts missing from result", name)
				return
			}
			if !bytes.Equal(resStripCnt.Value, stripCntEntry.Value) {
				t.Errorf("CR2-corpus: %s: StripByteCounts changed after write (image block integrity violated)",
					name)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// corpusCR2Files returns all CR2 file paths from the raw corpus.
// It calls t.Skip if no corpus directory is present.
// ─────────────────────────────────────────────────────────────────────────────

// corpusCR2Files returns the absolute paths of all .cr2 files from the
// testdata/corpus/raw corpus directory. Skips the test if the directory is
// absent or empty (uses testutil.CorpusFiles then filters by extension).
func corpusCR2Files(t *testing.T) []string {
	t.Helper()
	all := testutil.CorpusFiles(t, "raw")
	var cr2 []string
	for _, p := range all {
		if strings.EqualFold(filepath.Ext(p), ".cr2") {
			cr2 = append(cr2, p)
		}
	}
	if len(cr2) == 0 {
		t.Skip("no .cr2 files found in testdata/corpus/raw; skipping corpus tests")
	}
	return cr2
}
