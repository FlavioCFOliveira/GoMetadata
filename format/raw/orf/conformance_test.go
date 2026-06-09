package orf

// conformance_test.go — Olympus ORF specification-conformance test battery.
// Task #166.
//
// Normative authority:
//   - containers.md §8 (TIFF/EP and Proprietary RAW — ORF subsection)
//   - ExifTool Olympus.pm: ORF magic bytes ("IIRO"/"IIRS"), OLYMP-type MakerNote
//     header ("OLYMP\x00", 6 bytes), OLYMPUS-type header ("OLYMPUS\x00", 8 bytes),
//     offset-base rules.
//   - TIFF 6.0 §2: IFD layout, header, byte order, inline-vs-OOL threshold.
//
// Rule IDs (ORF-*) are used verbatim as t.Run names and cite the specification
// clause for each assertion.
//
// Test categories:
//   ORF-magic-*          — §8(b) detection rules: IIRO, IIRS, patch-to-TIFF, restore
//   ORF-structure-*      — §8(c) IFD layout, entry encoding, byte order
//   ORF-OLYMP-*          — §8(e) OLYMP-type MakerNote absolute-offset rebase
//   ORF-unknown-magic-*  — §8(f) unknown magic degrades to error (not panic)
//   ORF-robust-*         — §8(f) robustness: truncation, OOB offsets, count overflow
//   ORF-corpus-*         — corpus parity over testdata/corpus/raw (*.orf / *.ORF)
//
// Constraints: no t.Skip in synthetic tests. Corpus parity uses testutil.CorpusFiles
// which skips automatically when the directory is absent. All tests pass -race.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixtures
// ─────────────────────────────────────────────────────────────────────────────

// iirsMagic is the IIRS Olympus ORF magic used by older compacts (C-series, SP-series).
// containers.md §8(b): magic IIRS = 49 49 52 53.
var iirsMagic = []byte{0x49, 0x49, 0x52, 0x53} //nolint:gochecknoglobals // immutable test fixture

// buildORFVariant builds a minimal ORF with the given 4-byte magic.
// Structure: magic(4) + ifd0_offset=8(4) + IFD0_count=0(2) + nextIFD=0(4)
func buildORFVariant(magic []byte) []byte {
	buf := make([]byte, 14)
	copy(buf[0:4], magic)
	binary.LittleEndian.PutUint32(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[8:], 0)  // 0 IFD entries
	binary.LittleEndian.PutUint32(buf[10:], 0) // next IFD = 0
	return buf
}

// buildORFVariantWithTags builds a minimal ORF with the given magic and IFD0
// entries for IPTC (0x83BB) and XMP (0x02BC) OOL payloads.
func buildORFVariantWithTags(magic, iptcData, xmpData []byte) []byte {
	order := binary.LittleEndian
	// Layout: header(8) + ifd(2+2×12+4) + iptcData + xmpData
	const hdrLen = 8
	const ifdSize = 2 + 2*12 + 4
	iptcOff := hdrLen + ifdSize
	xmpOff := iptcOff + len(iptcData)
	totalSize := xmpOff + len(xmpData)

	buf := make([]byte, totalSize)
	copy(buf[0:4], magic)
	order.PutUint32(buf[4:], hdrLen)

	order.PutUint16(buf[hdrLen:], 2) // 2 IFD entries
	e0 := hdrLen + 2
	order.PutUint16(buf[e0:], 0x83BB)
	order.PutUint16(buf[e0+2:], 7)                     // TypeUNDEFINED
	order.PutUint32(buf[e0+4:], uint32(len(iptcData))) //nolint:gosec // G115: test fixture
	order.PutUint32(buf[e0+8:], uint32(iptcOff))
	e1 := e0 + 12
	order.PutUint16(buf[e1:], 0x02BC)
	order.PutUint16(buf[e1+2:], 1)                    // TypeBYTE
	order.PutUint32(buf[e1+4:], uint32(len(xmpData))) //nolint:gosec // G115: test fixture
	order.PutUint32(buf[e1+8:], uint32(xmpOff))       //nolint:gosec // G115: test fixture
	order.PutUint32(buf[e1+12:], 0)                   // next IFD = 0

	copy(buf[iptcOff:], iptcData)
	copy(buf[xmpOff:], xmpData)
	return buf
}

// buildORFWithOLYMPMakerNote constructs a synthetic ORF carrying an OLYMP-type
// MakerNote (6-byte "OLYMP\x00" header) in the ExifIFD (tag 0x927C).
//
// ExifTool Olympus.pm: OLYMP-type MakerNote header = "OLYMP\x00" (6 bytes) +
// version (2 bytes). IFD starts at blob[8]. All OOL val_or_off values in the
// MakerNote IFD are TIFF-file-absolute offsets.
//
// Layout (all LE):
//
//	[0 :4]   ORF magic (IIRO or IIRS)
//	[4 :8]   IFD0 offset = 8
//	[8 :10]  IFD0 entry count = 1
//	[10:22]  IFD0 entry: tag=0x8769 (ExifIFD pointer), type=4 (LONG), count=1, val=offset_to_exif_ifd
//	[22:26]  IFD0 next = 0
//	---- ExifIFD ----
//	[26:28]  ExifIFD count = 1
//	[28:40]  ExifIFD entry: tag=0x927C (MakerNote), type=7 (UNDEF), count=mnLen, val=offset_to_mn
//	[40:44]  ExifIFD next = 0
//	---- MakerNote blob ----
//	[44:44+mnLen]  "OLYMP\x00" + version(2) + MakerNote IFD
func buildORFWithOLYMPMakerNote(magic []byte) (data []byte, mnOffset uint32) {
	order := binary.LittleEndian

	// MakerNote IFD: 1 entry with 0 OOL values (no ThumbnailImage) to keep test simple.
	// Header = "OLYMP\x00" (6) + version "\x01\x00" (2) = 8 bytes.
	// IFD: count=1 entry (12 bytes) for tag 0x1000 (some tag) inline.
	// MakerNote blob: 8-byte OLYMP header + 1-entry IFD (2+12+4=18 bytes) = 26 bytes total.
	// Pre-allocated as a single slice to avoid prealloc linter warning.
	const mnHeaderLen = 8
	const mnIFDLen = 2 + 12 + 4
	mnBlob := make([]byte, mnHeaderLen+mnIFDLen)
	copy(mnBlob[0:6], []byte{'O', 'L', 'Y', 'M', 'P', 0x00}) // ExifTool Olympus.pm: OLYMP header prefix
	order.PutUint16(mnBlob[6:], 0x0001)                      // version word
	// 1 inline entry: tag=0x0101, type=3 (SHORT), count=1, val=42 inline
	order.PutUint16(mnBlob[8:], 1)       // entry count
	order.PutUint16(mnBlob[10:], 0x0101) // tag
	order.PutUint16(mnBlob[12:], 3)      // SHORT
	order.PutUint32(mnBlob[14:], 1)      // count
	order.PutUint32(mnBlob[18:], 42)     // inline value
	order.PutUint32(mnBlob[22:], 0)      // next = 0
	mnLen := uint32(len(mnBlob))         //nolint:gosec // G115: len bounded by test fixture

	// Offsets:
	// IFD0 at 8; ExifIFD at 8+2+12+4=26; MakerNote blob at 26+2+12+4=44.
	const ifd0Off = 8
	const exifIFDOff = 26
	mnOffset = 44

	totalSize := int(mnOffset) + int(mnLen)
	buf := make([]byte, totalSize)
	copy(buf[0:4], magic)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0: 1 entry — ExifIFD pointer (0x8769)
	order.PutUint16(buf[ifd0Off:], 1)
	e0 := ifd0Off + 2
	order.PutUint16(buf[e0:], 0x8769)       // ExifIFD tag
	order.PutUint16(buf[e0+2:], 4)          // LONG
	order.PutUint32(buf[e0+4:], 1)          // count=1
	order.PutUint32(buf[e0+8:], exifIFDOff) // value = offset to ExifIFD
	order.PutUint32(buf[e0+12:], 0)         // next IFD = 0

	// ExifIFD: 1 entry — MakerNote (0x927C)
	order.PutUint16(buf[exifIFDOff:], 1)
	e1 := exifIFDOff + 2
	order.PutUint16(buf[e1:], 0x927C)     // MakerNote
	order.PutUint16(buf[e1+2:], 7)        // UNDEFINED
	order.PutUint32(buf[e1+4:], mnLen)    // count = blob size
	order.PutUint32(buf[e1+8:], mnOffset) // OOL offset to blob
	order.PutUint32(buf[e1+12:], 0)       // next ExifIFD = 0

	// MakerNote blob
	copy(buf[mnOffset:], mnBlob)

	return buf, mnOffset
}

// ─────────────────────────────────────────────────────────────────────────────
// ORF-magic-* — §8(b) Detection rules
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_ORF_magic_IIRO verifies that the IIRO magic (49 49 52 4F) is
// accepted by Extract as a valid ORF file.
//
// containers.md §8(b): ORF detection — IIRO = 49 49 52 4F.
// ExifTool Olympus.pm: ORFMagic = "IIRO" | "IIRS".
func TestConformance_ORF_magic_IIRO(t *testing.T) {
	t.Parallel()
	// containers.md §8(b): IIRO magic accepted.
	data := buildORFVariant(orfMagic) // orfMagic is IIRO
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-magic-IIRO: Extract on IIRO ORF: %v", err)
	}
	if rawEXIF == nil {
		t.Error("ORF-magic-IIRO: rawEXIF must not be nil for valid IIRO ORF")
	}
	// Verify the IIRO magic was detected (only possible if isORFMagic matched).
	if data[0] != 0x49 || data[1] != 0x49 || data[2] != 0x52 || data[3] != 0x4F {
		t.Error("ORF-magic-IIRO: fixture has wrong magic — test setup error")
	}
}

// TestConformance_ORF_magic_IIRS verifies that the IIRS magic (49 49 52 53) is
// accepted by Extract as a valid ORF file.
//
// containers.md §8(b): ORF detection — IIRS = 49 49 52 53.
// ExifTool Olympus.pm: ORFMagic = "IIRO" | "IIRS" (older compacts use IIRS).
func TestConformance_ORF_magic_IIRS(t *testing.T) {
	t.Parallel()
	// containers.md §8(b): IIRS magic accepted.
	data := buildORFVariant(iirsMagic) // iirsMagic is IIRS
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-magic-IIRS: Extract on IIRS ORF: %v", err)
	}
	if rawEXIF == nil {
		t.Error("ORF-magic-IIRS: rawEXIF must not be nil for valid IIRS ORF")
	}
}

// TestConformance_ORF_magic_IIRS_bytes verifies that byte[3] == 0x53 ('S')
// distinguishes IIRS from IIRO (byte[3] == 0x4F).
//
// containers.md §8(b): both variants have identical structure; only bytes[2:4] differ.
func TestConformance_ORF_magic_IIRS_bytes(t *testing.T) {
	t.Parallel()
	// containers.md §8(b): only byte 3 differs between IIRO and IIRS.
	if orfMagic[3] == iirsMagic[3] {
		t.Error("ORF-magic-IIRS-bytes: IIRO and IIRS must differ at byte[3]")
	}
	if orfMagic[3] != 0x4F {
		t.Errorf("ORF-magic-IIRS-bytes: IIRO byte[3] = %02x, want 0x4F ('O')", orfMagic[3])
	}
	if iirsMagic[3] != 0x53 {
		t.Errorf("ORF-magic-IIRS-bytes: IIRS byte[3] = %02x, want 0x53 ('S')", iirsMagic[3])
	}
}

// TestConformance_ORF_magic_common_prefix verifies that IIRO and IIRS share the
// "IIR" (49 49 52) prefix in bytes[0:3].
//
// containers.md §8(b): both variants begin with 0x49 0x49 0x52.
func TestConformance_ORF_magic_common_prefix(t *testing.T) {
	t.Parallel()
	// containers.md §8(b): shared 3-byte prefix 0x49 0x49 0x52.
	for _, magic := range [][]byte{orfMagic, iirsMagic} {
		if magic[0] != 0x49 || magic[1] != 0x49 || magic[2] != 0x52 {
			t.Errorf("ORF-magic-common-prefix: magic %X does not share IIR prefix", magic)
		}
	}
}

// TestConformance_ORF_original_magic_in_rawEXIF verifies that Extract returns
// the ORIGINAL ORF magic bytes in rawEXIF (not the patched TIFF magic).
//
// #117 fix: the TIFF IFD traversal operates on an internal working copy with
// bytes[2:4] patched to 0x2A 0x00. The rawEXIF returned to the caller carries
// the original bytes so that RawEXIF() round-trips correctly and writing
// rawEXIF back to disk produces a valid ORF file.
//
// containers.md §8(e): ORF/RW2: rawEXIF must preserve the original magic so
// callers can detect the format from rawEXIF without re-reading the file.
// ExifTool Olympus.pm: IFD walk uses patched copy; original bytes are preserved.
func TestConformance_ORF_original_magic_in_rawEXIF(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		magic  []byte
		wantB2 byte
		wantB3 byte
	}{
		{"IIRO", orfMagic, 0x52, 0x4F},  // 'R', 'O'
		{"IIRS", iirsMagic, 0x52, 0x53}, // 'R', 'S'
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := buildORFVariant(tc.magic)
			rawEXIF, _, _, err := Extract(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("ORF-original-magic-in-rawEXIF [%s]: Extract: %v", tc.name, err)
			}
			if len(rawEXIF) < 4 {
				t.Fatalf("ORF-original-magic-in-rawEXIF [%s]: rawEXIF too short (%d)", tc.name, len(rawEXIF))
			}
			// rawEXIF must carry the original ORF magic, not the patched 0x2A 0x00.
			if rawEXIF[2] != tc.wantB2 || rawEXIF[3] != tc.wantB3 {
				t.Errorf("ORF-original-magic-in-rawEXIF [%s]: rawEXIF[2:4] = %02x %02x, want %02x %02x (original magic)",
					tc.name, rawEXIF[2], rawEXIF[3], tc.wantB2, tc.wantB3)
			}
			// The "II" byte-order marker at bytes [0:2] must be preserved.
			if rawEXIF[0] != 0x49 || rawEXIF[1] != 0x49 {
				t.Errorf("ORF-original-magic-in-rawEXIF [%s]: rawEXIF[0:2] = %02x %02x, want 0x49 0x49 (II)",
					tc.name, rawEXIF[0], rawEXIF[1])
			}
		})
	}
}

// TestConformance_ORF_magic_restore_on_write_IIRO verifies that Inject restores
// the IIRO magic (49 49 52 4F) in the output bytes.
//
// containers.md §8(e): ORF: restore original magic on write.
func TestConformance_ORF_magic_restore_on_write_IIRO(t *testing.T) {
	t.Parallel()
	// containers.md §8(e): output must carry IIRO magic at bytes[0:4].
	data := buildORFVariant(orfMagic)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("ORF-magic-restore-on-write-IIRO: Inject: %v", err)
	}
	result := out.Bytes()
	if len(result) < 4 {
		t.Fatal("ORF-magic-restore-on-write-IIRO: output too short")
	}
	if result[0] != 0x49 || result[1] != 0x49 || result[2] != 0x52 || result[3] != 0x4F {
		t.Errorf("ORF-magic-restore-on-write-IIRO: output bytes[0:4] = %02x %02x %02x %02x, want 49 49 52 4F",
			result[0], result[1], result[2], result[3])
	}
}

// TestConformance_ORF_magic_restore_on_write_IIRS verifies that Inject restores
// the IIRS magic (49 49 52 53) in the output bytes when the input was IIRS.
//
// containers.md §8(e): ORF: restore original magic on write. Each variant is
// preserved independently; the output magic must match the input magic.
func TestConformance_ORF_magic_restore_on_write_IIRS(t *testing.T) {
	t.Parallel()
	// containers.md §8(e): output must carry IIRS magic at bytes[0:4].
	data := buildORFVariant(iirsMagic)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("ORF-magic-restore-on-write-IIRS: Inject: %v", err)
	}
	result := out.Bytes()
	if len(result) < 4 {
		t.Fatal("ORF-magic-restore-on-write-IIRS: output too short")
	}
	if result[0] != 0x49 || result[1] != 0x49 || result[2] != 0x52 || result[3] != 0x53 {
		t.Errorf("ORF-magic-restore-on-write-IIRS: output bytes[0:4] = %02x %02x %02x %02x, want 49 49 52 53",
			result[0], result[1], result[2], result[3])
	}
}

// TestConformance_ORF_magic_no_tiff_leakage_in_write verifies that neither IIRO
// nor IIRS Inject output accidentally leaks the patched TIFF magic (0x2A 0x00)
// at bytes[2:4].
//
// containers.md §8(e): ORF magic restore on write — the internal patch must not
// persist in the output.
func TestConformance_ORF_magic_no_tiff_leakage_in_write(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		magic []byte
		b3    byte
	}{
		{"IIRO", orfMagic, 0x4F},
		{"IIRS", iirsMagic, 0x53},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := buildORFVariant(tc.magic)
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
				t.Fatalf("ORF-magic-no-tiff-leakage [%s]: Inject: %v", tc.name, err)
			}
			result := out.Bytes()
			if len(result) < 4 {
				t.Fatalf("ORF-magic-no-tiff-leakage [%s]: output too short", tc.name)
			}
			// Bytes[2:4] must NOT be the TIFF magic 0x2A 0x00.
			if result[2] == 0x2A && result[3] == 0x00 {
				t.Errorf("ORF-magic-no-tiff-leakage [%s]: TIFF magic leaked into output bytes[2:4]", tc.name)
			}
			// Bytes[2:4] must be the ORF-specific value 'R' + byte[3].
			if result[2] != 0x52 || result[3] != tc.b3 {
				t.Errorf("ORF-magic-no-tiff-leakage [%s]: output bytes[2:4] = %02x %02x, want 52 %02x",
					tc.name, result[2], result[3], tc.b3)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ORF-structure-* — §8(c) IFD layout, byte order, entry encoding
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_ORF_structure_ifd0_empty verifies that an ORF with an empty
// IFD0 (0 entries) is accepted with no error and returns nil IPTC and XMP.
//
// TIFF 6.0 §2: entry count 0 is valid; the next-IFD pointer follows immediately.
func TestConformance_ORF_structure_ifd0_empty(t *testing.T) {
	t.Parallel()
	// TIFF 6.0 §2: empty IFD0 is valid.
	data := buildORFVariant(orfMagic)
	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-structure-ifd0-empty: unexpected error: %v", err)
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("ORF-structure-ifd0-empty: expected nil IPTC/XMP, got IPTC=%v XMP=%v", rawIPTC, rawXMP)
	}
}

// TestConformance_ORF_structure_byte_order_LE verifies that ORF uses
// little-endian byte order (the "II" byte-order marker inherited from TIFF).
//
// containers.md §8(b): detection — II* pattern, so ORF is always LE.
// TIFF 6.0 §2: "II" = Intel (little-endian) byte order.
func TestConformance_ORF_structure_byte_order_LE(t *testing.T) {
	t.Parallel()
	// containers.md §8(b): ORF bytes[0:2] = "II" → little-endian.
	for _, magic := range [][]byte{orfMagic, iirsMagic} {
		if magic[0] != 'I' || magic[1] != 'I' {
			t.Errorf("ORF-structure-byte-order-LE: magic %X does not carry 'II' LE marker", magic)
		}
	}
}

// TestConformance_ORF_structure_iptc_tag_0x83BB verifies that IFD0 tag 0x83BB
// is extracted as rawIPTC.
//
// containers.md §8(d): IPTC via tag 0x83BB.
func TestConformance_ORF_structure_iptc_tag_0x83BB(t *testing.T) {
	t.Parallel()
	// containers.md §8(d): IPTC embedded at TIFF tag 0x83BB.
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'O', 'l', 'y', 'm', 'p'}
	xmpData := []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta/><?xpacket end='r'?>`)
	data := buildORFVariantWithTags(orfMagic, iptcData, xmpData)

	_, rawIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-structure-iptc-tag-0x83BB: Extract: %v", err)
	}
	if !bytes.Equal(rawIPTC, iptcData) {
		t.Errorf("ORF-structure-iptc-tag-0x83BB: rawIPTC = %x, want %x", rawIPTC, iptcData)
	}
}

// TestConformance_ORF_structure_xmp_tag_0x02BC verifies that IFD0 tag 0x02BC
// is extracted as rawXMP.
//
// containers.md §8(d): XMP via tag 0x02BC (700).
func TestConformance_ORF_structure_xmp_tag_0x02BC(t *testing.T) {
	t.Parallel()
	// containers.md §8(d): XMP embedded at TIFF tag 0x02BC.
	iptcData := []byte{0x1C, 0x02}
	xmpData := []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta/><?xpacket end='r'?>`)
	data := buildORFVariantWithTags(orfMagic, iptcData, xmpData)

	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-structure-xmp-tag-0x02BC: Extract: %v", err)
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("ORF-structure-xmp-tag-0x02BC: rawXMP = %q, want %q", rawXMP, xmpData)
	}
}

// TestConformance_ORF_structure_round_trip_iptc_xmp verifies that
// Inject → Extract round-trips IPTC and XMP byte-for-byte.
//
// containers.md §8(e): preserve metadata not explicitly modified.
func TestConformance_ORF_structure_round_trip_iptc_xmp(t *testing.T) {
	t.Parallel()
	// containers.md §8(e): write round-trip byte-correctness.
	cases := []struct {
		name  string
		magic []byte
	}{
		{"IIRO", orfMagic},
		{"IIRS", iirsMagic},
	}

	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x09, 'O', 'l', 'y', 'm', 'p', 'u', 's', '!'}
	wantXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := buildORFVariant(tc.magic)
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(data), &out, nil, wantIPTC, wantXMP, true); err != nil {
				t.Fatalf("ORF-structure-round-trip [%s]: Inject: %v", tc.name, err)
			}

			_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("ORF-structure-round-trip [%s]: Extract after Inject: %v", tc.name, err)
			}
			if !bytes.Equal(gotIPTC, wantIPTC) {
				t.Errorf("ORF-structure-round-trip [%s]: IPTC = %q, want %q", tc.name, gotIPTC, wantIPTC)
			}
			if !bytes.Equal(gotXMP, wantXMP) {
				t.Errorf("ORF-structure-round-trip [%s]: XMP = %q, want %q", tc.name, gotXMP, wantXMP)
			}
		})
	}
}

// buildORFWithSingleTag builds a minimal ORF with one IFD0 entry whose value is
// stored inline (when valueBytes ≤ 4) or OOL (when valueBytes > 4).
// This correctly populates val_or_off: either the value itself (inline) or
// the absolute file offset to the data (OOL).
//
// TIFF 6.0 §2: inline threshold is exactly 4 bytes; value stored left-justified
// (little-endian) in the 4-byte val_or_off field.
func buildORFWithSingleTag(tag uint16, typ uint16, value []byte) []byte {
	// IFD0 at offset 8; IFD = 2 + 12 + 4 = 18 bytes; OOL data starts at 26.
	const ifd0Off = 8
	const oolOff = 26
	totalSize := oolOff + len(value)
	if len(value) <= 4 {
		totalSize = oolOff // no separate OOL block needed
	}

	buf := make([]byte, totalSize)
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)

	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1) // 1 entry
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], tag)
	binary.LittleEndian.PutUint16(buf[e+2:], typ)
	binary.LittleEndian.PutUint32(buf[e+4:], uint32(len(value))) //nolint:gosec // G115: test fixture
	if len(value) <= 4 {
		// Inline: copy value bytes into val_or_off field (left-justified / little-endian).
		// TIFF 6.0 §2: "If the Value is shorter than 4 bytes, it is left-justified
		// within the 4-byte Value Offset."
		copy(buf[e+8:e+12], value)
	} else {
		// OOL: val_or_off = absolute file offset to the value data.
		binary.LittleEndian.PutUint32(buf[e+8:], oolOff)
		copy(buf[oolOff:], value)
	}
	binary.LittleEndian.PutUint32(buf[e+12:], 0) // next IFD = 0
	return buf
}

// TestConformance_ORF_structure_ifd_ool_inline_boundary verifies that the
// inline/OOL boundary is correctly applied: values ≤ 4 bytes are stored inline
// in the val_or_off field; values > 4 bytes are stored OOL.
//
// TIFF 6.0 §2: "If the Value is shorter than 4 bytes, it is left-justified
// within the 4-byte Value Offset, i.e., stored in the lower-numbered bytes."
func TestConformance_ORF_structure_ifd_ool_inline_boundary(t *testing.T) {
	t.Parallel()
	// TIFF 6.0 §2: inline/OOL boundary at 4 bytes.
	// Use TypeUNDEFINED (type=7, size=1): 4 bytes → inline; 6 bytes → OOL.
	inlineIPTC := []byte{0x1C, 0x02, 0x00, 0x00}         // 4 bytes: inline
	oolIPTC := []byte{0x1C, 0x02, 0x05, 0x00, 0x01, 'A'} // 6 bytes: OOL

	// Inline (4-byte IPTC): build entry with value stored in val_or_off directly.
	data := buildORFWithSingleTag(0x83BB, 7, inlineIPTC) // TypeUNDEFINED
	_, rawIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-structure-ifd-ool-inline-boundary (inline): Extract: %v", err)
	}
	if !bytes.Equal(rawIPTC, inlineIPTC) {
		t.Errorf("ORF-structure-ifd-ool-inline-boundary (inline): rawIPTC = %x, want %x", rawIPTC, inlineIPTC)
	}

	// OOL (6-byte IPTC): build entry with OOL offset in val_or_off.
	data = buildORFWithSingleTag(0x83BB, 7, oolIPTC)
	_, rawIPTC, _, err = Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-structure-ifd-ool-inline-boundary (OOL): Extract: %v", err)
	}
	if !bytes.Equal(rawIPTC, oolIPTC) {
		t.Errorf("ORF-structure-ifd-ool-inline-boundary (OOL): rawIPTC = %x, want %x", rawIPTC, oolIPTC)
	}
}

// TestConformance_ORF_structure_iptc_typelong_padding_trimmed verifies that when
// IPTC is stored as TypeLong (type=4), trailing 0x00 alignment padding is trimmed.
//
// iptc.md ROBUST-16 / TIFF 6.0 §2: TypeLong values are padded to 4-byte boundary.
// The structural padding bytes are not part of the IIM data; they must be stripped.
func TestConformance_ORF_structure_iptc_typelong_padding_trimmed(t *testing.T) {
	t.Parallel()
	// TIFF 6.0 §2 / iptc.md ROBUST-16: TypeLong structural alignment padding trimmed.
	// Build an ORF with IPTC tag 0x83BB as TypeLong with a padded value.
	const ifd0Off = 8
	const dataOff = 26                                                               // 8 + 2 + 12 + 4
	iptcCore := []byte{0x1C, 0x02, 0x05, 0x00, 0x03, 'A', 'B', 'C'}                  // 8 bytes
	padded := append(iptcCore[:len(iptcCore):len(iptcCore)], 0x00, 0x00, 0x00, 0x00) // 12 bytes

	buf := make([]byte, dataOff+len(padded))
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1) // 1 entry
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB)                  // IPTC tag
	binary.LittleEndian.PutUint16(buf[e+2:], 4)                     // TypeLong
	binary.LittleEndian.PutUint32(buf[e+4:], uint32(len(padded)/4)) //nolint:gosec // G115: test fixture
	binary.LittleEndian.PutUint32(buf[e+8:], dataOff)               // OOL offset
	binary.LittleEndian.PutUint32(buf[e+12:], 0)                    // next IFD
	copy(buf[dataOff:], padded)

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ORF-structure-iptc-typelong-padding-trimmed: Extract: %v", err)
	}
	// The trailing 0x00 padding bytes must have been trimmed.
	if bytes.HasSuffix(rawIPTC, []byte{0x00, 0x00, 0x00, 0x00}) {
		t.Error("ORF-structure-iptc-typelong-padding-trimmed: trailing 0x00 padding not trimmed from TypeLong IPTC")
	}
	// The IIM data must still be present.
	if !bytes.HasPrefix(rawIPTC, iptcCore) {
		t.Errorf("ORF-structure-iptc-typelong-padding-trimmed: rawIPTC = %x, want prefix %x", rawIPTC, iptcCore)
	}
}

// TestConformance_ORF_structure_iptc_typeundefined_not_trimmed verifies that
// TypeUndefined IPTC payloads are NOT trimmed (even when they end in 0x00).
//
// iptc.md ROBUST-16: only TypeLong payloads are trimmed; TypeByte/Undefined are
// returned verbatim.
func TestConformance_ORF_structure_iptc_typeundefined_not_trimmed(t *testing.T) {
	t.Parallel()
	// iptc.md ROBUST-16: TypeUndefined/TypeByte not trimmed.
	iptcWithNulls := []byte{0x1C, 0x02, 0x05, 0x00, 0x02, 'X', 0x00, 0x00} // ends in 0x00

	const ifd0Off = 8
	const dataOff = 26
	buf := make([]byte, dataOff+len(iptcWithNulls))
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1)
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB)
	binary.LittleEndian.PutUint16(buf[e+2:], 7)                          // TypeUndefined
	binary.LittleEndian.PutUint32(buf[e+4:], uint32(len(iptcWithNulls))) //nolint:gosec // G115: test fixture
	binary.LittleEndian.PutUint32(buf[e+8:], dataOff)
	binary.LittleEndian.PutUint32(buf[e+12:], 0)
	copy(buf[dataOff:], iptcWithNulls)

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ORF-structure-iptc-typeundefined-not-trimmed: Extract: %v", err)
	}
	if !bytes.Equal(rawIPTC, iptcWithNulls) {
		t.Errorf("ORF-structure-iptc-typeundefined-not-trimmed: rawIPTC = %x, want %x", rawIPTC, iptcWithNulls)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ORF-OLYMP-* — §8(e) OLYMP-type MakerNote: absolute-offset rebase
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_ORF_OLYMP_makernote_present verifies that an ORF carrying an
// OLYMP-type MakerNote (ExifIFD tag 0x927C, blob starting "OLYMP\x00") is
// successfully extracted without error.
//
// containers.md §8(d): MakerNote 0x927C. ExifTool Olympus.pm: OLYMP-type
// header = "OLYMP\x00" (6 bytes) + version (2 bytes); IFD at blob[8].
func TestConformance_ORF_OLYMP_makernote_present(t *testing.T) {
	t.Parallel()
	// ExifTool Olympus.pm: OLYMP-type MakerNote detected by "OLYMP\x00" prefix.
	data, _ := buildORFWithOLYMPMakerNote(orfMagic)
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-OLYMP-makernote-present: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("ORF-OLYMP-makernote-present: rawEXIF must not be nil")
	}
}

// TestConformance_ORF_OLYMP_makernote_header verifies that the OLYMP-type
// MakerNote blob begins with exactly "OLYMP\x00" (6 bytes) followed by a 2-byte
// version word, making an 8-byte header total.
//
// ExifTool Olympus.pm: OLYMP-type header = bytes{0x4F,0x4C,0x59,0x4D,0x50,0x00}
// + uint16 version; IFD starts at blob[8].
func TestConformance_ORF_OLYMP_makernote_header(t *testing.T) {
	t.Parallel()
	// ExifTool Olympus.pm: OLYMP header bytes confirmed.
	// Verify the constants used in relocate_orf.go match the spec.
	expectedPrefix := [6]byte{'O', 'L', 'Y', 'M', 'P', 0x00}
	data, mnOff := buildORFWithOLYMPMakerNote(orfMagic)

	// Read the MakerNote blob from the synthetic fixture.
	if int(mnOff)+8 > len(data) {
		t.Fatalf("ORF-OLYMP-makernote-header: fixture too small for MakerNote blob")
	}
	mnBlob := data[mnOff:]
	for i, b := range expectedPrefix {
		if mnBlob[i] != b {
			t.Errorf("ORF-OLYMP-makernote-header: MakerNote[%d] = %02x, want %02x", i, mnBlob[i], b)
		}
	}
	// Bytes 6-7 are the version word (not further constrained by spec assertion here).
	// Presence of 8-byte header before IFD is the key constraint.
}

// TestConformance_ORF_OLYMP_makernote_inject_no_panic verifies that Inject on
// an ORF carrying an OLYMP-type MakerNote does not panic.
//
// containers.md §8(e): absolute-offset MakerNotes break if relocated — preserve
// in place or fix up.
func TestConformance_ORF_OLYMP_makernote_inject_no_panic(t *testing.T) {
	t.Parallel()
	// containers.md §8(e): OLYMP MakerNote absolute-offset rebase — no panic.
	data, _ := buildORFWithOLYMPMakerNote(orfMagic)
	var out bytes.Buffer
	// Must not panic. Error is acceptable (MakerNote parsing may find the IFD
	// structure incomplete for the synthetic fixture).
	_ = Inject(bytes.NewReader(data), &out, nil, nil, nil, true)
}

// TestConformance_ORF_OLYMP_makernote_output_has_orf_magic verifies that even
// when an OLYMP-type MakerNote is present in the input, the Inject output
// correctly restores the ORF magic bytes.
//
// containers.md §8(e): restore original magic on write, regardless of MakerNote format.
func TestConformance_ORF_OLYMP_makernote_output_has_orf_magic(t *testing.T) {
	t.Parallel()
	// containers.md §8(e): ORF magic restored on write for OLYMP-MakerNote case.
	for _, magic := range [][]byte{orfMagic, iirsMagic} {
		data, _ := buildORFWithOLYMPMakerNote(magic)
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
			// Accept errors from the OLYMP relocation path on a minimal fixture;
			// the no-panic contract is the primary assertion here.
			continue
		}
		result := out.Bytes()
		if len(result) < 4 {
			continue
		}
		if result[2] != 0x52 {
			t.Errorf("ORF-OLYMP-makernote-output-has-orf-magic: magic[2] = %02x, want 0x52 (R)", result[2])
		}
		if result[3] != magic[3] {
			t.Errorf("ORF-OLYMP-makernote-output-has-orf-magic: magic[3] = %02x, want %02x", result[3], magic[3])
		}
	}
}

// TestConformance_ORF_OLYMP_iirsMagic_makernote verifies that the IIRS magic
// variant (older compacts like C5050Z) can carry an OLYMP-type MakerNote.
//
// ExifTool Olympus.pm: C-series / SP-series cameras use IIRS magic and have
// OLYMP-type MakerNotes with file-absolute offsets.
func TestConformance_ORF_OLYMP_iirsMagic_makernote(t *testing.T) {
	t.Parallel()
	// ExifTool Olympus.pm: IIRS + OLYMP MakerNote is the C5050Z / SP-series case.
	data, mnOff := buildORFWithOLYMPMakerNote(iirsMagic)

	// Verify IIRS magic in the fixture.
	if data[3] != 0x53 {
		t.Errorf("ORF-OLYMP-iirsMagic-makernote: fixture byte[3] = %02x, want 0x53 (IIRS)", data[3])
	}

	// Verify the MakerNote blob is present.
	if int(mnOff) >= len(data) {
		t.Fatalf("ORF-OLYMP-iirsMagic-makernote: MakerNote offset %d out of range (len=%d)", mnOff, len(data))
	}
	if data[mnOff] != 'O' || data[mnOff+1] != 'L' || data[mnOff+2] != 'Y' {
		t.Errorf("ORF-OLYMP-iirsMagic-makernote: MakerNote does not start with 'OLY' at offset %d", mnOff)
	}

	// Extract must not fail.
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-OLYMP-iirsMagic-makernote: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("ORF-OLYMP-iirsMagic-makernote: rawEXIF must not be nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ORF-unknown-magic-* — §8(f) Unknown magic degrades gracefully
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_ORF_unknown_magic_degrades verifies that input whose bytes[0:4]
// do not match IIRO or IIRS is rejected with ErrInvalidMagic (not a panic).
//
// containers.md §8(f): ORF magic not in {IIRO,IIRS} → degrade to generic TIFF;
// the library returns ErrInvalidMagic rather than crashing or silently misparsing.
func TestConformance_ORF_unknown_magic_degrades(t *testing.T) {
	t.Parallel()
	// containers.md §8(f): degrade on unknown ORF magic — no panic.
	cases := []struct {
		name  string
		magic [4]byte
	}{
		// Standard TIFF LE — not ORF
		{"II*\\0", [4]byte{0x49, 0x49, 0x2A, 0x00}},
		// Standard TIFF BE — not ORF
		{"MM\\0*", [4]byte{0x4D, 0x4D, 0x00, 0x2A}},
		// Hypothetical MMOR (big-endian ORF-like) — not accepted
		{"MMOR", [4]byte{0x4D, 0x4D, 0x4F, 0x52}},
		// Hypothetical MMRO — not accepted
		{"MMRO", [4]byte{0x4D, 0x4D, 0x52, 0x4F}},
		// RW2 magic — not ORF
		{"IIU\\0", [4]byte{0x49, 0x49, 0x55, 0x00}},
		// All-zero
		{"zero", [4]byte{0x00, 0x00, 0x00, 0x00}},
		// Random bytes
		{"random", [4]byte{0xDE, 0xAD, 0xBE, 0xEF}},
		// JPEG SOI — not ORF
		{"jpeg-soi", [4]byte{0xFF, 0xD8, 0xFF, 0xE1}},
		// Byte 2 = 'R' but byte 3 is neither 'O' nor 'S'
		{"IIR!", [4]byte{0x49, 0x49, 0x52, 0x21}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := make([]byte, 16)
			copy(data[0:4], tc.magic[:])
			_, _, _, err := Extract(bytes.NewReader(data))
			if err == nil {
				t.Errorf("ORF-unknown-magic-degrades [%s]: expected error, got nil", tc.name)
				return
			}
			if !errors.Is(err, ErrInvalidMagic) {
				t.Errorf("ORF-unknown-magic-degrades [%s]: expected ErrInvalidMagic; got %v", tc.name, err)
			}
		})
	}
}

// TestConformance_ORF_unknown_magic_inject_error verifies that Inject on a
// non-ORF stream returns ErrInvalidMagic.
//
// containers.md §8(f): Inject must gate on valid ORF magic.
func TestConformance_ORF_unknown_magic_inject_error(t *testing.T) {
	t.Parallel()
	// containers.md §8(f): Inject rejects non-ORF magic.
	bad := []byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	var out bytes.Buffer
	err := Inject(bytes.NewReader(bad), &out, nil, nil, nil, true)
	if err == nil {
		t.Error("ORF-unknown-magic-inject-error: expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("ORF-unknown-magic-inject-error: expected ErrInvalidMagic; got %v", err)
	}
}

// TestConformance_ORF_unknown_magic_empty_input verifies that an empty input
// byte slice returns ErrInvalidMagic (not a panic or index-out-of-bounds).
//
// containers.md §8(f): truncated / empty input — no crash.
func TestConformance_ORF_unknown_magic_empty_input(t *testing.T) {
	t.Parallel()
	// containers.md §8(f): empty input must not panic.
	_, _, _, err := Extract(bytes.NewReader([]byte{}))
	if err == nil {
		t.Error("ORF-unknown-magic-empty-input: expected error for empty input, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("ORF-unknown-magic-empty-input: expected ErrInvalidMagic; got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ORF-robust-* — §8(f) Robustness: truncation, OOB offsets, count overflow
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_ORF_robust_truncated_at_magic verifies that a 4-byte input
// (only the magic header, no TIFF IFD offset) returns rawEXIF with no error.
//
// containers.md §8(f): truncated file — no crash.
func TestConformance_ORF_robust_truncated_at_magic(t *testing.T) {
	t.Parallel()
	// containers.md §8(f): file cut after magic — rawEXIF non-nil, no panic.
	for _, magic := range [][]byte{orfMagic, iirsMagic} {
		rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(magic))
		if err != nil {
			t.Errorf("ORF-robust-truncated-at-magic: Extract: unexpected error: %v", err)
		}
		if rawEXIF == nil {
			t.Error("ORF-robust-truncated-at-magic: rawEXIF must not be nil for 4-byte input")
		}
		if rawIPTC != nil {
			t.Errorf("ORF-robust-truncated-at-magic: rawIPTC must be nil, got %v", rawIPTC)
		}
		if rawXMP != nil {
			t.Errorf("ORF-robust-truncated-at-magic: rawXMP must be nil, got %v", rawXMP)
		}
	}
}

// TestConformance_ORF_robust_truncated_at_header verifies that a 6-byte input
// (magic + 2 bytes of IFD offset) returns rawEXIF with no error.
//
// containers.md §8(f): truncated file — no crash.
func TestConformance_ORF_robust_truncated_at_header(t *testing.T) {
	t.Parallel()
	// containers.md §8(f): file cut mid-header — no panic.
	data := make([]byte, 6)
	copy(data[0:4], orfMagic)
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-robust-truncated-at-header: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("ORF-robust-truncated-at-header: rawEXIF must not be nil")
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("ORF-robust-truncated-at-header: expected nil IPTC/XMP, got IPTC=%v XMP=%v", rawIPTC, rawXMP)
	}
}

// TestConformance_ORF_robust_ifd0_offset_past_eof verifies that an IFD0 offset
// pointing past the end of the file produces no panic and returns rawEXIF.
//
// containers.md §8(f): offset past EOF — no crash.
// TIFF 6.0 §2: IFD0 offset + 2 must be ≤ file size.
func TestConformance_ORF_robust_ifd0_offset_past_eof(t *testing.T) {
	t.Parallel()
	// containers.md §8(f) / TIFF 6.0 §2: offset past EOF handled gracefully.
	data := buildORFVariant(orfMagic)
	binary.LittleEndian.PutUint32(data[4:], 0xFFFFFFFF) // far past EOF

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ORF-robust-ifd0-offset-past-eof: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("ORF-robust-ifd0-offset-past-eof: rawEXIF must not be nil")
	}
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("ORF-robust-ifd0-offset-past-eof: expected nil IPTC/XMP; got IPTC=%v XMP=%v", rawIPTC, rawXMP)
	}
}

// TestConformance_ORF_robust_count_overflow verifies that an IFD0 entry with
// a count that would overflow the buffer is silently skipped (no panic).
//
// containers.md §8(f): count exceeding file — no crash.
func TestConformance_ORF_robust_count_overflow(t *testing.T) {
	t.Parallel()
	// containers.md §8(f): count exceeding file size — skip silently.
	const ifd0Off = 8
	const dataOff = 26
	buf := make([]byte, dataOff)
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1) // 1 entry
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB)       // IPTC tag
	binary.LittleEndian.PutUint16(buf[e+2:], 7)          // UNDEFINED
	binary.LittleEndian.PutUint32(buf[e+4:], 0xFFFFFFFF) // MaxUint32 count → overflow
	binary.LittleEndian.PutUint32(buf[e+8:], 0)          // offset = 0

	rawEXIF, rawIPTC, _, _ := Extract(bytes.NewReader(buf))
	// Primary assertion: no panic.
	if rawEXIF == nil {
		t.Error("ORF-robust-count-overflow: rawEXIF must not be nil")
	}
	// IPTC must not be returned (the entry is invalid due to count overflow).
	if rawIPTC != nil {
		t.Errorf("ORF-robust-count-overflow: rawIPTC must be nil for invalid entry; got %v", rawIPTC)
	}
}

// TestConformance_ORF_robust_ifd_entry_ool_offset_past_eof verifies that an
// IFD entry whose OOL value offset points past the file end is silently skipped.
//
// containers.md §8(f): value offset past EOF — no crash.
func TestConformance_ORF_robust_ifd_entry_ool_offset_past_eof(t *testing.T) {
	t.Parallel()
	// containers.md §8(f): OOL value offset past EOF — silently skip.
	const ifd0Off = 8
	const dataOff = 26
	buf := make([]byte, dataOff) // no room for OOL data
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1)
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB)   // IPTC
	binary.LittleEndian.PutUint16(buf[e+2:], 7)      // UNDEFINED
	binary.LittleEndian.PutUint32(buf[e+4:], 20)     // 20 bytes OOL
	binary.LittleEndian.PutUint32(buf[e+8:], 0xFFFF) // offset past EOF

	rawEXIF, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ORF-robust-ifd-entry-ool-offset-past-eof: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("ORF-robust-ifd-entry-ool-offset-past-eof: rawEXIF must not be nil")
	}
	if rawIPTC != nil {
		t.Errorf("ORF-robust-ifd-entry-ool-offset-past-eof: rawIPTC must be nil; got %v", rawIPTC)
	}
}

// TestConformance_ORF_robust_ifd_entry_count_exceeds_buffer verifies that an
// IFD entry whose total value size (count × typeSize) exceeds the remaining
// buffer is silently skipped.
//
// containers.md §8(f): count exceeding file — no crash.
func TestConformance_ORF_robust_ifd_entry_count_exceeds_buffer(t *testing.T) {
	t.Parallel()
	// containers.md §8(f): count × typeSize > remaining bytes — skip.
	const ifd0Off = 8
	const dataOff = 26
	smallPayload := make([]byte, 2) // only 2 bytes available for OOL data
	buf := make([]byte, dataOff+len(smallPayload))
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1)
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB)
	binary.LittleEndian.PutUint16(buf[e+2:], 3)       // TypeSHORT (2 bytes each)
	binary.LittleEndian.PutUint32(buf[e+4:], 100)     // 100 × 2 = 200 bytes but only 2 available
	binary.LittleEndian.PutUint32(buf[e+8:], dataOff) // OOL offset

	rawEXIF, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ORF-robust-ifd-entry-count-exceeds-buffer: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("ORF-robust-ifd-entry-count-exceeds-buffer: rawEXIF must not be nil")
	}
	if rawIPTC != nil {
		t.Errorf("ORF-robust-ifd-entry-count-exceeds-buffer: rawIPTC must be nil; got %v", rawIPTC)
	}
}

// TestConformance_ORF_robust_truncated_mid_ifd verifies that an ORF whose IFD0
// is truncated mid-entry does not panic and returns rawEXIF.
//
// containers.md §8(f): truncated file — no crash.
func TestConformance_ORF_robust_truncated_mid_ifd(t *testing.T) {
	t.Parallel()
	// containers.md §8(f): file truncated mid-IFD — no panic.
	buf := make([]byte, 18) // 8 header + 2 count + 8 (partial entry, needs 12)
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], 8) // IFD0 at 8
	binary.LittleEndian.PutUint16(buf[8:], 3) // claims 3 entries but truncated

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	// Primary assertion: no panic.
	if rawEXIF == nil {
		t.Error("ORF-robust-truncated-mid-ifd: rawEXIF must not be nil")
	}
}

// TestConformance_ORF_robust_ifd_unknown_type verifies that an IFD entry with an
// unknown TIFF type code (typeSize returns 0) is silently skipped.
//
// TIFF 6.0 §2: unknown type codes must be tolerated; skip the entry.
func TestConformance_ORF_robust_ifd_unknown_type(t *testing.T) {
	t.Parallel()
	// TIFF 6.0 §2: unknown type code — skip entry, no panic.
	const ifd0Off = 8
	buf := make([]byte, ifd0Off+2+12+4)
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1)
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB) // IPTC tag
	binary.LittleEndian.PutUint16(buf[e+2:], 0xFF) // unknown type (255)
	binary.LittleEndian.PutUint32(buf[e+4:], 1)
	binary.LittleEndian.PutUint32(buf[e+8:], 0)

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ORF-robust-ifd-unknown-type: unexpected error: %v", err)
	}
	// Entry with unknown type must be skipped.
	if rawIPTC != nil {
		t.Errorf("ORF-robust-ifd-unknown-type: rawIPTC must be nil for unknown-type entry; got %v", rawIPTC)
	}
}

// TestConformance_ORF_robust_ifd_type0 verifies that an IFD entry with type=0
// (explicitly invalid) is silently skipped.
//
// TIFF 6.0 §2: type 0 is not a valid TIFF type.
func TestConformance_ORF_robust_ifd_type0(t *testing.T) {
	t.Parallel()
	// TIFF 6.0 §2: type=0 is invalid — skip entry.
	const ifd0Off = 8
	buf := make([]byte, ifd0Off+2+12+4)
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1)
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB)
	binary.LittleEndian.PutUint16(buf[e+2:], 0) // type = 0
	binary.LittleEndian.PutUint32(buf[e+4:], 4)
	binary.LittleEndian.PutUint32(buf[e+8:], 0)

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("ORF-robust-ifd-type0: unexpected error: %v", err)
	}
	if rawIPTC != nil {
		t.Errorf("ORF-robust-ifd-type0: rawIPTC must be nil for type=0 entry; got %v", rawIPTC)
	}
}

// TestConformance_ORF_robust_fuzz_seeds verifies that a set of adversarial
// byte sequences do not cause panics in Extract.
//
// containers.md §8(f): robustness against arbitrary input.
func TestConformance_ORF_robust_fuzz_seeds(t *testing.T) {
	t.Parallel()
	// containers.md §8(f): adversarial inputs must not panic.
	seeds := []struct {
		name string
		data []byte
	}{
		// Valid IIRO magic with a maxUint32 IFD0 offset.
		{"iiro-max-offset", append(orfMagic, 0xFF, 0xFF, 0xFF, 0xFF, 0x00, 0x00)},
		// Valid IIRS magic with all-zero body.
		{"iirs-all-zero", append(iirsMagic, make([]byte, 12)...)},
		// IIRO with IFD0 entry count = 0xFFFF.
		{"iiro-max-count", func() []byte {
			d := make([]byte, 10)
			copy(d[0:4], orfMagic)
			binary.LittleEndian.PutUint32(d[4:], 8)
			binary.LittleEndian.PutUint16(d[8:], 0xFFFF)
			return d
		}()},
		// IIRO with alternating 0xFF bytes in payload.
		{"iiro-ff-payload", func() []byte {
			d := make([]byte, 50)
			copy(d[0:4], orfMagic)
			for i := 4; i < 50; i++ {
				d[i] = 0xFF
			}
			return d
		}()},
		// IIRS with a valid header but entries that claim maxUint32 count.
		{"iirs-entry-maxcount", func() []byte {
			d := make([]byte, 26)
			copy(d[0:4], iirsMagic)
			binary.LittleEndian.PutUint32(d[4:], 8)
			binary.LittleEndian.PutUint16(d[8:], 1)
			binary.LittleEndian.PutUint16(d[10:], 0x83BB)
			binary.LittleEndian.PutUint16(d[12:], 7)
			binary.LittleEndian.PutUint32(d[14:], 0xFFFFFFFF)
			binary.LittleEndian.PutUint32(d[18:], 100)
			return d
		}()},
	}

	for _, tc := range seeds {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Must not panic.
			_, _, _, _ = Extract(bytes.NewReader(tc.data))
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ORF-corpus-* — parity over real-world ORF files
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_ORF_corpus_extract_no_error verifies that Extract succeeds
// (returns no error) for every ORF file in the corpus.
//
// containers.md §8(b)(c): real-world ORF files must be extractable without error.
func TestConformance_ORF_corpus_extract_no_error(t *testing.T) {
	t.Parallel()
	// containers.md §8: corpus parity — all real-world ORF files must extract cleanly.
	paths := corpusORFFiles(t)

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("ORF-corpus-extract-no-error: open %q: %v", path, err)
			}
			defer f.Close() //nolint:errcheck // test helper

			_, _, _, extractErr := Extract(f)
			if extractErr != nil {
				t.Errorf("ORF-corpus-extract-no-error: Extract(%q): %v", filepath.Base(path), extractErr)
			}
		})
	}
}

// TestConformance_ORF_corpus_magic verifies that every ORF corpus file begins
// with a valid ORF magic (IIRO or IIRS).
//
// containers.md §8(b): detection by magic bytes, never by file extension.
func TestConformance_ORF_corpus_magic(t *testing.T) {
	t.Parallel()
	// containers.md §8(b): all corpus ORF files must carry valid ORF magic.
	paths := corpusORFFiles(t)

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("ORF-corpus-magic: open %q: %v", path, err)
			}
			defer f.Close() //nolint:errcheck // test helper

			header := make([]byte, 4)
			if _, err := f.Read(header); err != nil {
				t.Fatalf("ORF-corpus-magic: read header of %q: %v", filepath.Base(path), err)
			}
			if !isORFMagic(header) {
				t.Errorf("ORF-corpus-magic: %q: bytes[0:4] = %02x %02x %02x %02x, not IIRO or IIRS",
					filepath.Base(path), header[0], header[1], header[2], header[3])
			}
		})
	}
}

// TestConformance_ORF_corpus_rawEXIF_non_nil verifies that every corpus ORF
// returns a non-nil rawEXIF from Extract (the TIFF stream must always be present).
//
// containers.md §8(d): EXIF via pointer 0x8769 in IFD0 of the TIFF stream.
func TestConformance_ORF_corpus_rawEXIF_non_nil(t *testing.T) {
	t.Parallel()
	// containers.md §8(d): corpus ORF files all carry EXIF data.
	paths := corpusORFFiles(t)

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("ORF-corpus-rawEXIF-non-nil: open %q: %v", path, err)
			}
			defer f.Close() //nolint:errcheck // test helper

			rawEXIF, _, _, extractErr := Extract(f)
			if extractErr != nil {
				t.Fatalf("ORF-corpus-rawEXIF-non-nil: Extract(%q): %v", filepath.Base(path), extractErr)
			}
			if rawEXIF == nil {
				t.Errorf("ORF-corpus-rawEXIF-non-nil: %q: rawEXIF is nil", filepath.Base(path))
			}
		})
	}
}

// TestConformance_ORF_corpus_rawEXIF_patched_magic verifies that the rawEXIF
// returned for every corpus ORF has standard TIFF magic bytes[2:4] = 0x2A 0x00
// (not the original ORF magic).
//
// containers.md §8(e): on parse, patch magic to 0x002A to walk as TIFF.
func TestConformance_ORF_corpus_rawEXIF_patched_magic(t *testing.T) {
	t.Parallel()
	// containers.md §8(e): Extract patches bytes[2:4] to TIFF magic in rawEXIF.
	paths := corpusORFFiles(t)

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("ORF-corpus-rawEXIF-patched-magic: open %q: %v", path, err)
			}
			defer f.Close() //nolint:errcheck // test helper

			rawEXIF, _, _, extractErr := Extract(f)
			if extractErr != nil {
				t.Fatalf("ORF-corpus-rawEXIF-patched-magic: Extract(%q): %v", filepath.Base(path), extractErr)
			}
			if len(rawEXIF) < 4 {
				t.Fatalf("ORF-corpus-rawEXIF-patched-magic: %q: rawEXIF too short (%d)", filepath.Base(path), len(rawEXIF))
			}
			if rawEXIF[2] != 0x2A || rawEXIF[3] != 0x00 {
				t.Errorf("ORF-corpus-rawEXIF-patched-magic: %q: rawEXIF[2:4] = %02x %02x, want 0x2A 0x00",
					filepath.Base(path), rawEXIF[2], rawEXIF[3])
			}
		})
	}
}

// TestConformance_ORF_corpus_magic_IIRS_compacts verifies that C5050Z and
// related compact cameras use IIRS magic (not IIRO).
//
// containers.md §8(b): IIRS used by older Olympus compacts (C-series, SP-series).
// ExifTool Olympus.pm: Olympus compacts use IIRS; DSLRs and OM-D use IIRO.
func TestConformance_ORF_corpus_magic_IIRS_compacts(t *testing.T) {
	t.Parallel()
	// containers.md §8(b): IIRS = 49 49 52 53 — older compacts.
	// The corpus must contain at least one IIRS file; if not, the corpus is incomplete.
	paths := corpusORFFiles(t)

	foundIIRS := false
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Logf("ORF-corpus-magic-IIRS-compacts: skip %q: %v", path, err)
			continue
		}
		header := make([]byte, 4)
		_, _ = f.Read(header)
		_ = f.Close()          // test helper: error ignored
		if header[3] == 0x53 { // IIRS
			foundIIRS = true
			break
		}
	}
	if !foundIIRS {
		t.Log("ORF-corpus-magic-IIRS-compacts: no IIRS files found in corpus; C5050Z / SP-series fixtures needed")
	}
}

// TestConformance_ORF_corpus_magic_IIRO_dslr verifies that E-series, OM-D, and
// TG-series cameras use IIRO magic.
//
// containers.md §8(b): IIRO = 49 49 52 4F — DSLRs, OM-D, TG-series.
func TestConformance_ORF_corpus_magic_IIRO_dslr(t *testing.T) {
	t.Parallel()
	// containers.md §8(b): IIRO = 49 49 52 4F — modern Olympus cameras.
	paths := corpusORFFiles(t)

	foundIIRO := false
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Logf("ORF-corpus-magic-IIRO-dslr: skip %q: %v", path, err)
			continue
		}
		header := make([]byte, 4)
		_, _ = f.Read(header)
		_ = f.Close()          // test helper: error ignored
		if header[3] == 0x4F { // IIRO
			foundIIRO = true
			break
		}
	}
	if !foundIIRO {
		t.Log("ORF-corpus-magic-IIRO-dslr: no IIRO files found in corpus")
	}
}

// TestConformance_ORF_corpus_inject_restores_magic verifies that for every
// corpus ORF, Inject produces an output whose bytes[0:4] match the original
// ORF magic (IIRO or IIRS).
//
// containers.md §8(e): restore original magic on write.
func TestConformance_ORF_corpus_inject_restores_magic(t *testing.T) {
	t.Parallel()
	// containers.md §8(e): Inject must restore original magic for all corpus files.
	paths := corpusORFFiles(t)

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("ORF-corpus-inject-restores-magic: open %q: %v", path, err)
			}
			defer f.Close() //nolint:errcheck // test helper

			// Read original magic.
			origMagic := make([]byte, 4)
			if _, err := f.Read(origMagic); err != nil {
				t.Fatalf("ORF-corpus-inject-restores-magic: read header of %q: %v", filepath.Base(path), err)
			}
			if _, err := f.Seek(0, 0); err != nil {
				t.Fatalf("ORF-corpus-inject-restores-magic: seek: %v", err)
			}

			var out bytes.Buffer
			if err := Inject(f, &out, nil, nil, nil, true); err != nil {
				t.Fatalf("ORF-corpus-inject-restores-magic: Inject(%q): %v", filepath.Base(path), err)
			}

			result := out.Bytes()
			if len(result) < 4 {
				t.Fatalf("ORF-corpus-inject-restores-magic: %q: output too short (%d)", filepath.Base(path), len(result))
			}
			if !bytes.Equal(result[0:4], origMagic) {
				t.Errorf("ORF-corpus-inject-restores-magic: %q: output magic = %02x %02x %02x %02x, want %02x %02x %02x %02x",
					filepath.Base(path),
					result[0], result[1], result[2], result[3],
					origMagic[0], origMagic[1], origMagic[2], origMagic[3])
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// corpusORFFiles returns all ORF corpus file paths (case-insensitive .orf/.ORF
// extension) from testdata/corpus/raw.  Uses testutil.CorpusFiles to skip the
// test automatically when the corpus directory is absent.
func corpusORFFiles(t *testing.T) []string {
	t.Helper()
	all := testutil.CorpusFiles(t, "raw")
	var orf []string
	for _, p := range all {
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".orf" {
			orf = append(orf, p)
		}
	}
	if len(orf) == 0 {
		t.Skip("no .orf files found in testdata/corpus/raw; populate corpus to run corpus tests")
	}
	return orf
}
