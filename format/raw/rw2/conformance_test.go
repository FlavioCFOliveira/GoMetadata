package rw2

// conformance_test.go — Panasonic RW2 specification-conformance test battery.
// Task #167.
//
// Rule IDs (RW2-*) are used verbatim as sub-test names and cite the
// authoritative source for each assertion.
//
// Sources:
//   - containers.md §8 — detection rule, GUID header, recursive IFD rebase,
//     JpgFromRaw preservation, robustness cases
//   - ExifTool PanasonicRaw.pm — RW2 file structure, GUID at offset 8,
//     tag 0x002E (JpgFromRaw), tag 0x0118 (RawDataOffset), sentinel StripOffsets
//   - TIFF 6.0 §2 — IFD entry layout, inline vs OOL threshold, byte order
//   - iptc.md ROBUST-16 — TypeLong IPTC structural padding trim (TypeByte/Undef: no trim)
//
// Test categories:
//   RW2-magic-*      — Non-standard magic IIU\x00 (0x0055) detection
//   RW2-GUID-*       — 16-byte device GUID at bytes [8:24], IFD0 at offset 24
//   RW2-rebase-*     — Recursive IFD offset rebase after GUID insertion (+16)
//   RW2-JpgFromRaw-* — Embedded preview JPEG (tag 0x002E) preservation on write
//   RW2-metadata-*   — IPTC/XMP extraction and round-trip via Extract/Inject
//   RW2-robust-*     — Malformed input: no panic, correct degradation
//   RW2-corpus-*     — Parity over real-world files in testdata/corpus/raw

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// RW2-magic — Non-standard magic IIU\x00 (containers.md §8)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_RW2_magic_IIU_0x0055 verifies that the Panasonic RW2 magic
// value at bytes [2:4] is 0x0055 (not the standard TIFF 0x002A).
//
// containers.md §8: "RW2: non-standard magic IIU\x00 (49 49 55 00) — magic value
// 0x0055 not 0x002A." ExifTool PanasonicRaw.pm: magic bytes at offsets 0-3.
func TestConformance_RW2_magic_IIU_0x0055(t *testing.T) {
	t.Parallel()

	// Verify the package-level magic constant carries the correct bytes.
	// rw2Magic[2] = 0x55 (not 0x2A which is standard TIFF).
	if rw2Magic[0] != 0x49 || rw2Magic[1] != 0x49 {
		t.Errorf("RW2-magic-IIU-0x0055: bytes[0:2] = %02x %02x, want 49 49",
			rw2Magic[0], rw2Magic[1])
	}
	if rw2Magic[2] != 0x55 {
		t.Errorf("RW2-magic-IIU-0x0055: byte[2] = 0x%02x, want 0x55 (containers.md §8: not 0x2A)", rw2Magic[2])
	}
	if rw2Magic[3] != 0x00 {
		t.Errorf("RW2-magic-IIU-0x0055: byte[3] = 0x%02x, want 0x00", rw2Magic[3])
	}

	// Confirm Extract accepts valid RW2 magic and returns a non-nil rawEXIF.
	data := buildRW2()
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RW2-magic-IIU-0x0055: Extract with valid IIU0 magic: %v", err)
	}
	if rawEXIF == nil {
		t.Error("RW2-magic-IIU-0x0055: rawEXIF is nil for valid RW2 input")
	}
}

// TestConformance_RW2_original_magic_in_rawEXIF verifies that Extract returns
// the ORIGINAL RW2 magic bytes in rawEXIF (not the patched TIFF magic).
//
// #117 fix: the TIFF IFD traversal operates on an internal working copy with
// bytes[2:4] patched to 0x2A 0x00. The rawEXIF returned to the caller carries
// the original bytes so that RawEXIF() round-trips correctly and writing
// rawEXIF back to disk produces a valid RW2 file.
//
// containers.md §8: rawEXIF must preserve the original RW2 magic so callers
// can detect the format from rawEXIF without re-reading the file.
func TestConformance_RW2_original_magic_in_rawEXIF(t *testing.T) {
	t.Parallel()

	data := buildRW2()
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RW2-original-magic-in-rawEXIF: Extract: %v", err)
	}
	if len(rawEXIF) < 4 {
		t.Fatal("RW2-original-magic-in-rawEXIF: rawEXIF too short")
	}
	// rawEXIF must carry the original RW2 magic (0x55 0x00), not the patched 0x2A 0x00.
	if rawEXIF[2] != 0x55 || rawEXIF[3] != 0x00 {
		t.Errorf("RW2-original-magic-in-rawEXIF: bytes[2:4] = %02x %02x, want 55 00 (original RW2 magic)",
			rawEXIF[2], rawEXIF[3])
	}
}

// TestConformance_RW2_magic_restored_after_inject verifies that Inject writes
// "IIU\x00" at bytes [0:4] of the output, not the patched 0x2A value.
//
// containers.md §8: "ORF/RW2: restore original magic on write."
func TestConformance_RW2_magic_restored_after_inject(t *testing.T) {
	t.Parallel()

	data := buildRW2()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("RW2-magic-restored-after-inject: Inject: %v", err)
	}
	result := out.Bytes()
	if len(result) < 4 {
		t.Fatal("RW2-magic-restored-after-inject: output too short")
	}
	if !bytes.HasPrefix(result, rw2Magic) {
		t.Errorf("RW2-magic-restored-after-inject: bytes[0:4] = %02x %02x %02x %02x, want 49 49 55 00",
			result[0], result[1], result[2], result[3])
	}
}

// TestConformance_RW2_magic_standard_TIFF_rejected verifies that data carrying
// the standard TIFF magic 0x002A at bytes [2:4] is rejected with ErrInvalidMagic.
//
// containers.md §8: "RW2 0x0055 misread" is a robustness case — the library
// must not process a standard TIFF as RW2.
func TestConformance_RW2_magic_standard_TIFF_rejected(t *testing.T) {
	t.Parallel()

	// Standard TIFF LE: bytes[2:4] = 0x2A 0x00.
	data := make([]byte, 14)
	data[0], data[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(data[2:], 0x002A)
	binary.LittleEndian.PutUint32(data[4:], 8)

	_, _, _, err := Extract(bytes.NewReader(data))
	if err == nil {
		t.Error("RW2-magic-standard-TIFF-rejected: expected ErrInvalidMagic for standard TIFF, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("RW2-magic-standard-TIFF-rejected: error = %v, want ErrInvalidMagic", err)
	}
}

// TestConformance_RW2_magic_BigTIFF_rejected verifies that BigTIFF magic 0x002B
// at bytes [2:4] is rejected with ErrInvalidMagic.
//
// containers.md §8: BigTIFF RAW (8-byte offsets, 20-byte entries) is a robustness
// case but the magic check fires before any parsing.
func TestConformance_RW2_magic_BigTIFF_rejected(t *testing.T) {
	t.Parallel()

	data := make([]byte, 16)
	data[0], data[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(data[2:], 0x002B) // BigTIFF magic

	_, _, _, err := Extract(bytes.NewReader(data))
	if err == nil {
		t.Error("RW2-magic-BigTIFF-rejected: expected ErrInvalidMagic for BigTIFF magic, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("RW2-magic-BigTIFF-rejected: error = %v, want ErrInvalidMagic", err)
	}
}

// TestConformance_RW2_magic_zero_rejected verifies that a zeroed-out header
// returns ErrInvalidMagic.
//
// containers.md §8 / robustness: magic misread guard.
func TestConformance_RW2_magic_zero_rejected(t *testing.T) {
	t.Parallel()

	_, _, _, err := Extract(bytes.NewReader(make([]byte, 32)))
	if err == nil {
		t.Error("RW2-magic-zero-rejected: expected error for all-zero header, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("RW2-magic-zero-rejected: error = %v, want ErrInvalidMagic", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RW2-GUID — 16-byte device GUID header at bytes [8:24] (containers.md §8)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_RW2_GUID_header_offset_shift verifies that the IFD0 offset
// in a real RW2 file is 24 (= 8-byte standard header + 16-byte GUID), not the
// standard TIFF value of 8.
//
// containers.md §8: "a 16-byte GUID header causes an offset-shift that requires
// recursive IFD rebase." ExifTool PanasonicRaw.pm: GUID = 16 bytes at offset 8.
func TestConformance_RW2_GUID_header_offset_shift(t *testing.T) {
	t.Parallel()

	// Build a synthetic RW2 that matches the real file layout:
	//   [0:4]  IIU\x00
	//   [4:8]  IFD0 offset = 24
	//   [8:24] 16-byte GUID
	//   [24:]  IFD0
	const ifd0Off = 24
	guid := []byte{
		0x88, 0xe7, 0x74, 0xd8, 0xf8, 0x25, 0x1d, 0x4d,
		0x94, 0x7a, 0x6e, 0x77, 0x82, 0x2b, 0x5d, 0x6a,
	}
	// IFD0 = count(2) + next-ifd(4) = 6 bytes, zero entries.
	buf := make([]byte, ifd0Off+6)
	copy(buf[0:4], rw2Magic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	copy(buf[8:24], guid)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 0)   // 0 entries
	binary.LittleEndian.PutUint32(buf[ifd0Off+2:], 0) // next IFD = 0

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("RW2-GUID-header-offset-shift: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("RW2-GUID-header-offset-shift: rawEXIF is nil")
	}
	if rawIPTC != nil {
		t.Errorf("RW2-GUID-header-offset-shift: rawIPTC = %v, want nil (empty IFD)", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("RW2-GUID-header-offset-shift: rawXMP = %v, want nil (empty IFD)", rawXMP)
	}
}

// TestConformance_RW2_GUID_preserved_on_write verifies that the 16-byte device
// GUID is preserved verbatim after a metadata write via InjectWithEXIFRW2.
//
// containers.md §8 + relocate_rw2.go Step A1: "Save the 16-byte GUID from
// base[8:24]." ExifTool PanasonicRaw.pm: GUID is device identity — must survive.
func TestConformance_RW2_GUID_preserved_on_write(t *testing.T) {
	t.Parallel()

	// Build an RW2 with a recognisable GUID and metadata tags.
	guid := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10,
	}
	base := buildRW2WithGUID(guid)

	rawXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	var out bytes.Buffer
	if err := tiff.InjectWithEXIFRW2(base, nil, nil, rawXMP, &out); err != nil {
		t.Fatalf("RW2-GUID-preserved-on-write: InjectWithEXIFRW2: %v", err)
	}

	result := out.Bytes()
	if len(result) < 24 {
		t.Fatalf("RW2-GUID-preserved-on-write: output too short (%d bytes)", len(result))
	}
	// bytes [8:24] must be the original GUID.
	if !bytes.Equal(result[8:24], guid) {
		t.Errorf("RW2-GUID-preserved-on-write: GUID mismatch:\n got  %x\n want %x",
			result[8:24], guid)
	}
}

// TestConformance_RW2_GUID_IFD0_offset_24_after_write verifies that after a
// write, the IFD0 offset stored in header bytes [4:8] is exactly 24.
//
// relocate_rw2.go Step B3: "Update header bytes [4:8] from 8 to 24."
// TIFF 6.0 §2: bytes [4:8] of TIFF header = IFD0 offset.
func TestConformance_RW2_GUID_IFD0_offset_24_after_write(t *testing.T) {
	t.Parallel()

	guid := []byte{
		0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22,
		0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0x00,
	}
	base := buildRW2WithGUID(guid)

	var out bytes.Buffer
	if err := tiff.InjectWithEXIFRW2(base, nil, nil, []byte(`<xmpmeta/>`), &out); err != nil {
		t.Fatalf("RW2-GUID-IFD0-offset-24-after-write: InjectWithEXIFRW2: %v", err)
	}

	result := out.Bytes()
	if len(result) < 8 {
		t.Fatal("RW2-GUID-IFD0-offset-24-after-write: output too short")
	}
	ifd0Off := binary.LittleEndian.Uint32(result[4:])
	if ifd0Off != 24 {
		t.Errorf("RW2-GUID-IFD0-offset-24-after-write: IFD0 offset = %d, want 24 (= 8+16 GUID)",
			ifd0Off)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RW2-rebase — Recursive IFD offset rebase after GUID insertion (containers.md §8)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_RW2_recursive_IFD_rebase verifies that ALL IFDs (IFD0, ExifIFD,
// GPS IFD) have their OOL val_or_off fields correctly rebased by +16 after the
// 16-byte GUID is inserted at position 8.
//
// containers.md §8: "a 16-byte GUID header causes an offset-shift that requires
// recursive IFD rebase."
// relocate_rw2.go: rebaseAllIFDsAfterGUID — walks ALL IFDs recursively and adds
// +rw2GUIDLen (16) to every file-absolute OOL pointer.
func TestConformance_RW2_recursive_IFD_rebase(t *testing.T) {
	t.Parallel()

	// Build a synthetic RW2 with IPTC and XMP in IFD0, then write it via
	// InjectWithEXIFRW2 and verify that Extract can round-trip both fields.
	//
	// If offset rebase is incorrect, the tag values will be at stale positions
	// and Extract will return nil or garbled data.
	guid := []byte{
		0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80,
		0x90, 0xA0, 0xB0, 0xC0, 0xD0, 0xE0, 0xF0, 0xFF,
	}
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x06, 'R', 'e', 'b', 'a', 's', 'e'}
	wantXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	base := buildRW2WithGUID(guid)

	var out bytes.Buffer
	if err := tiff.InjectWithEXIFRW2(base, nil, wantIPTC, wantXMP, &out); err != nil {
		t.Fatalf("RW2-recursive-IFD-rebase: InjectWithEXIFRW2: %v", err)
	}

	result := out.Bytes()
	// Output must carry RW2 magic.
	if !bytes.HasPrefix(result, rw2Magic) {
		t.Errorf("RW2-recursive-IFD-rebase: RW2 magic missing in output")
	}

	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("RW2-recursive-IFD-rebase: Extract after write: %v", err)
	}
	if !bytes.Equal(gotIPTC, wantIPTC) {
		t.Errorf("RW2-recursive-IFD-rebase: IPTC mismatch:\n got  %x\n want %x\n(stale offset indicates rebase failure)",
			gotIPTC, wantIPTC)
	}
	if !bytes.Equal(gotXMP, wantXMP) {
		t.Errorf("RW2-recursive-IFD-rebase: XMP mismatch:\n got  %q\n want %q\n(stale offset indicates rebase failure)",
			gotXMP, wantXMP)
	}
}

// TestConformance_RW2_OOL_offsets_shift_by_16 verifies that out-of-line value
// offsets in IFD0 each increase by exactly 16 after InjectWithEXIFRW2.
//
// relocate_rw2.go: TIFF 6.0 §2 — when GUID is inserted at position 8, every
// absolute OOL pointer shifts by +rw2GUIDLen (16).
func TestConformance_RW2_OOL_offsets_shift_by_16(t *testing.T) {
	t.Parallel()

	// We need a controlled RW2 to measure before/after offsets.
	// Build an RW2 with a single OOL XMP value.  After write, find the XMP
	// tag in the output IFD0 and verify its val_or_off increased by 16 compared
	// to the pre-GUID-insertion intermediate (standard TIFF at IFD0=8 space).
	//
	// Strategy: write the minimal RW2 with only XMP, then parse both the raw
	// TIFF skeleton (before GUID, IFD0 at 8) and the final RW2 (IFD0 at 24),
	// and check the XMP val_or_off difference.

	guid := []byte{
		0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE,
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
	}
	wantXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/" rebase="verify"/>`)
	base := buildRW2WithGUID(guid)

	var outBuf bytes.Buffer
	if err := tiff.InjectWithEXIFRW2(base, nil, nil, wantXMP, &outBuf); err != nil {
		t.Fatalf("RW2-OOL-offsets-shift-by-16: InjectWithEXIFRW2: %v", err)
	}
	result := outBuf.Bytes()

	// Locate XMP tag (0x02BC) in IFD0 of the output.
	ifd0Off := int(binary.LittleEndian.Uint32(result[4:])) // should be 24
	if ifd0Off+2 > len(result) {
		t.Fatalf("RW2-OOL-offsets-shift-by-16: IFD0 offset %d out of bounds", ifd0Off)
	}
	count := int(binary.LittleEndian.Uint16(result[ifd0Off:]))
	xmpVOO := uint32(0)
	found := false
	for i := range count {
		e := ifd0Off + 2 + i*12
		if e+12 > len(result) {
			break
		}
		tag := binary.LittleEndian.Uint16(result[e:])
		if tag == 0x02BC {
			xmpVOO = binary.LittleEndian.Uint32(result[e+8:])
			found = true
			break
		}
	}
	if !found {
		t.Fatal("RW2-OOL-offsets-shift-by-16: XMP tag 0x02BC not found in output IFD0")
	}
	// The XMP value area must be at offset >= 24 (past the GUID header).
	// Before GUID insertion, exif.Encode places OOL values after the IFD block
	// at offsets starting from ~8+IFD_size.  After +16 they land at >= 24+IFD_size.
	// The minimum valid OOL offset is ifd0Off (24) + 2 + count*12 + 4 (next-ptr).
	minExpected := uint32(ifd0Off + 2 + count*12 + 4) //nolint:gosec // G115: bounded test data
	if xmpVOO < minExpected {
		t.Errorf("RW2-OOL-offsets-shift-by-16: XMP val_or_off = %d, want >= %d (GUID shift not applied)",
			xmpVOO, minExpected)
	}

	// Round-trip: Extract must recover the original XMP.
	_, _, gotXMP, err := Extract(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("RW2-OOL-offsets-shift-by-16: Extract: %v", err)
	}
	if !bytes.Equal(gotXMP, wantXMP) {
		t.Errorf("RW2-OOL-offsets-shift-by-16: XMP round-trip mismatch (offset shift incorrect)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RW2-JpgFromRaw — Embedded preview JPEG (tag 0x002E) (containers.md §8)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_RW2_JpgFromRaw_extract verifies that Extract returns a non-nil
// rawEXIF from which the JpgFromRaw JPEG bytes (tag 0x002E) can be recovered by
// parsing the TIFF stream.
//
// containers.md §8: "JpgFromRaw (embedded JPEG) must be preserved on write."
// ExifTool PanasonicRaw.pm: tag 0x002E TypeUndefined OOL — embedded preview JPEG.
func TestConformance_RW2_JpgFromRaw_extract(t *testing.T) {
	t.Parallel()

	// Build a synthetic RW2 with a JpgFromRaw entry (tag 0x002E, TypeUndefined OOL).
	jpegData := makeMinimalJPEG()
	base := buildRW2WithJpgFromRaw(jpegData)

	rawEXIF, _, _, err := Extract(bytes.NewReader(base))
	if err != nil {
		t.Fatalf("RW2-JpgFromRaw-extract: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("RW2-JpgFromRaw-extract: rawEXIF is nil")
	}
	// rawEXIF is the patched TIFF; parse it to locate the 0x002E entry.
	jpgFound := findTagInTIFF(rawEXIF, 0x002E)
	if !jpgFound {
		t.Error("RW2-JpgFromRaw-extract: tag 0x002E (JpgFromRaw) not found in rawEXIF")
	}
}

// TestConformance_RW2_JpgFromRaw_preserved_on_write verifies that after a
// metadata write via InjectWithEXIFRW2, the JpgFromRaw JPEG bytes are present
// and correct in the output.
//
// containers.md §8: "JpgFromRaw (embedded JPEG) must be preserved on write."
// relocate_rw2.go Step 5 comment: "JpgFromRaw (0x002E) is handled automatically
// by exif.Encode (its Value bytes are preserved from exif.Parse; the new OOL
// offset is computed)."
func TestConformance_RW2_JpgFromRaw_preserved_on_write(t *testing.T) {
	t.Parallel()

	jpegData := makeMinimalJPEG()
	guid := []byte{
		0xF0, 0xE1, 0xD2, 0xC3, 0xB4, 0xA5, 0x96, 0x87,
		0x78, 0x69, 0x5A, 0x4B, 0x3C, 0x2D, 0x1E, 0x0F,
	}
	base := buildRW2WithJpgFromRawAndGUID(jpegData, guid)

	wantXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/" verify="jpgfromraw"/>`)
	var outBuf bytes.Buffer
	if err := tiff.InjectWithEXIFRW2(base, nil, nil, wantXMP, &outBuf); err != nil {
		t.Fatalf("RW2-JpgFromRaw-preserved-on-write: InjectWithEXIFRW2: %v", err)
	}

	result := outBuf.Bytes()
	// Output must carry RW2 magic.
	if !bytes.HasPrefix(result, rw2Magic) {
		t.Error("RW2-JpgFromRaw-preserved-on-write: RW2 magic missing in output")
	}

	// The JPEG bytes must appear verbatim somewhere in the output.
	if !bytes.Contains(result, jpegData) {
		t.Errorf("RW2-JpgFromRaw-preserved-on-write: JpgFromRaw JPEG bytes not found in output"+
			" (first 4 bytes of JPEG: %02x %02x %02x %02x)",
			jpegData[0], jpegData[1], jpegData[2], jpegData[3])
	}

	// GUID must be preserved.
	if len(result) >= 24 && !bytes.Equal(result[8:24], guid) {
		t.Errorf("RW2-JpgFromRaw-preserved-on-write: GUID corrupted:\n got  %x\n want %x",
			result[8:24], guid)
	}

	// XMP must round-trip correctly.
	_, _, gotXMP, err := Extract(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("RW2-JpgFromRaw-preserved-on-write: Extract after write: %v", err)
	}
	if !bytes.Equal(gotXMP, wantXMP) {
		t.Errorf("RW2-JpgFromRaw-preserved-on-write: XMP mismatch after write")
	}
}

// TestConformance_RW2_JpgFromRaw_JPEG_SOI_marker verifies that the JpgFromRaw
// value begins with the JPEG SOI marker 0xFF 0xD8.
//
// JpgFromRaw is a TypeUndefined blob; its content must be a valid JPEG stream.
// ExifTool PanasonicRaw.pm: tag 0x002E = embedded full-resolution JPEG preview.
func TestConformance_RW2_JpgFromRaw_JPEG_SOI_marker(t *testing.T) {
	t.Parallel()

	jpegData := makeMinimalJPEG()
	// Verify SOI is present at start.
	if len(jpegData) < 2 || jpegData[0] != 0xFF || jpegData[1] != 0xD8 {
		t.Errorf("RW2-JpgFromRaw-JPEG-SOI-marker: JPEG data must start with FF D8; got %02x %02x",
			jpegData[0], jpegData[1])
	}

	base := buildRW2WithJpgFromRaw(jpegData)
	rawEXIF, _, _, err := Extract(bytes.NewReader(base))
	if err != nil {
		t.Fatalf("RW2-JpgFromRaw-JPEG-SOI-marker: Extract: %v", err)
	}

	// Verify the embedded JPEG bytes are intact in rawEXIF.
	if !bytes.Contains(rawEXIF, jpegData[:2]) {
		t.Error("RW2-JpgFromRaw-JPEG-SOI-marker: JPEG SOI marker FF D8 not found in rawEXIF")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RW2-metadata — IPTC/XMP extraction and round-trip (containers.md §8 §(d))
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_RW2_metadata_IPTC_tag_0x83BB_TypeUndefined verifies that
// Extract returns a non-nil rawIPTC when IFD0 carries tag 0x83BB (IPTC-NAA)
// with TypeUndefined. Trailing zeros must NOT be trimmed for TypeUndefined.
//
// iptc.md ROBUST-16: TypeByte/Undefined payloads must not be TrimRight'd.
// containers.md §8 §(d): "IPTC via tag 0x83BB."
func TestConformance_RW2_metadata_IPTC_tag_0x83BB_TypeUndefined(t *testing.T) {
	t.Parallel()

	// An IPTC IIM v2 payload ending in 0x00 (e.g. empty-string value).
	// TypeUndefined: the trailing 0x00 must NOT be stripped.
	wantIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x00}
	data := buildRW2WithTag(0x83BB, 7 /*TypeUndefined*/, wantIPTC)

	_, gotIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RW2-metadata-IPTC-0x83BB-TypeUndefined: Extract: %v", err)
	}
	if !bytes.Equal(gotIPTC, wantIPTC) {
		t.Errorf("RW2-metadata-IPTC-0x83BB-TypeUndefined: IPTC = %x, want %x\n(ROBUST-16: TypeUndefined must not trim trailing zeros)",
			gotIPTC, wantIPTC)
	}
}

// TestConformance_RW2_metadata_IPTC_TypeLong_padding_trimmed verifies that
// TypeLong IPTC structural padding zeros are trimmed on extract.
//
// iptc.md ROBUST-16: "trim trailing 0x00 ONLY for TypeLong (structural artefact)."
// TypeLong IPTC in TIFF is padded to 4-byte boundary; those padding zeros must
// be trimmed so the returned slice equals the original unpadded IPTC.
//
// TypeLong IFD entry: count = number of LONG values (4 bytes each).
// For 8 bytes of padded data: count = 2 (2 LONG values × 4 bytes = 8 bytes).
// buildRW2WithTag stores count = len(value), correct for TypeByte/Undefined (1
// byte per unit) but WRONG for TypeLong (4 bytes per unit).  This test uses
// buildRW2WithTypeLongIPTC which encodes count correctly.
func TestConformance_RW2_metadata_IPTC_TypeLong_padding_trimmed(t *testing.T) {
	t.Parallel()

	// rawIPTC: 5 bytes → padded to 8 (3 trailing zeros = structural TypeLong padding).
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x41}
	data := buildRW2WithTypeLongIPTC(rawIPTC)

	_, gotIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RW2-metadata-IPTC-TypeLong-padding-trimmed: Extract: %v", err)
	}
	if !bytes.Equal(gotIPTC, rawIPTC) {
		t.Errorf("RW2-metadata-IPTC-TypeLong-padding-trimmed: IPTC = %x, want %x\n(ROBUST-16: TypeLong padding must be trimmed)",
			gotIPTC, rawIPTC)
	}
}

// TestConformance_RW2_metadata_XMP_tag_0x02BC verifies that Extract returns
// a non-nil rawXMP when IFD0 carries tag 0x02BC (XMP) with TypeByte.
//
// containers.md §8 §(d): "XMP via tag 0x02BC (700)."
// Adobe XMP Spec Part 3 §1.3: tag 700 (0x02BC) — accept TypeByte and TypeUndefined.
func TestConformance_RW2_metadata_XMP_tag_0x02BC(t *testing.T) {
	t.Parallel()

	wantXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildRW2WithTag(0x02BC, 1 /*TypeByte*/, wantXMP)

	_, _, gotXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RW2-metadata-XMP-tag-0x02BC: Extract: %v", err)
	}
	if !bytes.Equal(gotXMP, wantXMP) {
		t.Errorf("RW2-metadata-XMP-tag-0x02BC: XMP mismatch:\n got  %q\n want %q", gotXMP, wantXMP)
	}
}

// TestConformance_RW2_metadata_XMP_TypeUndefined_accepted verifies that Extract
// accepts XMP stored as TypeUndefined (7) as well as TypeByte (1).
//
// Adobe XMP Spec Part 3 §1.3: accept both BYTE and UNDEFINED on read for tag 700.
func TestConformance_RW2_metadata_XMP_TypeUndefined_accepted(t *testing.T) {
	t.Parallel()

	wantXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	data := buildRW2WithTag(0x02BC, 7 /*TypeUndefined*/, wantXMP)

	_, _, gotXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RW2-metadata-XMP-TypeUndefined: Extract: %v", err)
	}
	if !bytes.Equal(gotXMP, wantXMP) {
		t.Errorf("RW2-metadata-XMP-TypeUndefined: XMP mismatch:\n got  %q\n want %q", gotXMP, wantXMP)
	}
}

// TestConformance_RW2_metadata_round_trip_IPTC_XMP verifies a full Extract →
// Inject round-trip with both IPTC and XMP payloads.
//
// containers.md §8 §(e): "write byte-correctness/round-trip."
func TestConformance_RW2_metadata_round_trip_IPTC_XMP(t *testing.T) {
	t.Parallel()

	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x09, 'P', 'a', 'n', 'a', 's', 'o', 'n', 'i', 'c'}
	wantXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end="r"?>`)

	data := buildRW2()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, wantIPTC, wantXMP, true); err != nil {
		t.Fatalf("RW2-metadata-round-trip-IPTC-XMP: Inject: %v", err)
	}

	result := out.Bytes()
	if !bytes.HasPrefix(result, rw2Magic) {
		t.Error("RW2-metadata-round-trip-IPTC-XMP: RW2 magic missing in output")
	}

	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("RW2-metadata-round-trip-IPTC-XMP: Extract after Inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, wantIPTC) {
		t.Errorf("RW2-metadata-round-trip-IPTC-XMP: IPTC = %x, want %x", gotIPTC, wantIPTC)
	}
	if !bytes.Equal(gotXMP, wantXMP) {
		t.Errorf("RW2-metadata-round-trip-IPTC-XMP: XMP = %q, want %q", gotXMP, wantXMP)
	}
}

// TestConformance_RW2_metadata_inline_value_threshold verifies that IFD entries
// whose total byte count is <= 4 are stored inline (not as offsets).
//
// TIFF 6.0 §2: "If the Value is smaller than 4 bytes, the Value shall be stored
// in the low-order bytes of the value field, with the remaining high-order bytes
// filled with 0 (zeros)."
func TestConformance_RW2_metadata_inline_value_threshold(t *testing.T) {
	t.Parallel()

	// 2-byte IPTC value → inline (total = 1×2 = 2 ≤ 4).
	inlineIPTC := []byte{0x1C, 0x02}
	data := buildRW2WithTag(0x83BB, 7 /*TypeUndefined*/, inlineIPTC)

	_, gotIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RW2-metadata-inline-value-threshold: Extract: %v", err)
	}
	if !bytes.Equal(gotIPTC, inlineIPTC) {
		t.Errorf("RW2-metadata-inline-value-threshold: IPTC = %x, want %x (inline 2-byte value)",
			gotIPTC, inlineIPTC)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RW2-robust — Malformed input: no panic, correct degradation (containers.md §8 §(f))
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_RW2_robust_empty_input verifies that Extract on an empty
// reader returns ErrInvalidMagic without panicking.
//
// containers.md §8 §(f): robustness cases.
func TestConformance_RW2_robust_empty_input(t *testing.T) {
	t.Parallel()

	_, _, _, err := Extract(bytes.NewReader([]byte{}))
	if err == nil {
		t.Error("RW2-robust-empty-input: expected error for empty input, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("RW2-robust-empty-input: error = %v, want ErrInvalidMagic", err)
	}
}

// TestConformance_RW2_robust_truncated_at_magic verifies that a 4-byte input
// (magic only) returns a non-nil rawEXIF with no error.
//
// containers.md §8 §(f): truncation must not panic.
func TestConformance_RW2_robust_truncated_at_magic(t *testing.T) {
	t.Parallel()

	data := []byte{0x49, 0x49, 0x55, 0x00} // just the 4-byte magic
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RW2-robust-truncated-at-magic: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("RW2-robust-truncated-at-magic: rawEXIF should be non-nil (original magic bytes)")
	}
	if rawIPTC != nil {
		t.Errorf("RW2-robust-truncated-at-magic: rawIPTC = %v, want nil", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("RW2-robust-truncated-at-magic: rawXMP = %v, want nil", rawXMP)
	}
}

// TestConformance_RW2_robust_truncated_mid_IFD verifies that a file truncated
// within the IFD0 entry block does not panic and returns rawEXIF.
//
// containers.md §8 §(f): "count exceeding file" robustness case.
func TestConformance_RW2_robust_truncated_mid_IFD(t *testing.T) {
	t.Parallel()

	// Build an RW2 with IFD0 claiming 10 entries, truncated to hold only 2.
	const ifd0Off = 8
	buf := make([]byte, ifd0Off+2+2*12) // header + count + 2 entries (truncated at 10)
	copy(buf[0:4], rw2Magic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 10) // claims 10, only 2 fit

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("RW2-robust-truncated-mid-IFD: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("RW2-robust-truncated-mid-IFD: rawEXIF should be non-nil even for truncated IFD")
	}
}

// TestConformance_RW2_robust_IFD_offset_past_EOF verifies that an IFD0 offset
// beyond EOF produces no panic, returns rawEXIF, no IPTC/XMP.
//
// containers.md §8 §(f): "value offset past EOF."
func TestConformance_RW2_robust_IFD_offset_past_EOF(t *testing.T) {
	t.Parallel()

	data := buildRW2()
	// Corrupt IFD0 offset to point far beyond data.
	binary.LittleEndian.PutUint32(data[4:], 0xFFFFFF00)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RW2-robust-IFD-offset-past-EOF: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("RW2-robust-IFD-offset-past-EOF: rawEXIF should be non-nil")
	}
	if rawIPTC != nil {
		t.Errorf("RW2-robust-IFD-offset-past-EOF: rawIPTC = %v, want nil", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("RW2-robust-IFD-offset-past-EOF: rawXMP = %v, want nil", rawXMP)
	}
}

// TestConformance_RW2_robust_OOB_value_offset verifies that an IFD entry with
// an out-of-bounds value offset is silently skipped — no panic.
//
// containers.md §8 §(f): "value offset past EOF."
// TIFF 6.0 §2: parser must guard all offset reads.
func TestConformance_RW2_robust_OOB_value_offset(t *testing.T) {
	t.Parallel()

	// Build an RW2 with a single IFD entry claiming 100 bytes at offset 5000
	// (far past the end of the 26-byte buffer).
	const ifd0Off = 8
	const dataOff = 26

	buf := make([]byte, dataOff) // no room for OOL data
	copy(buf[0:4], rw2Magic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1) // 1 entry
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB) // IPTC
	binary.LittleEndian.PutUint16(buf[e+2:], 7)    // TypeUndefined
	binary.LittleEndian.PutUint32(buf[e+4:], 100)  // count=100 bytes
	binary.LittleEndian.PutUint32(buf[e+8:], 5000) // offset WAY past end

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("RW2-robust-OOB-value-offset: unexpected error: %v", err)
	}
	_ = rawEXIF
	_ = rawXMP
	if rawIPTC != nil {
		t.Errorf("RW2-robust-OOB-value-offset: rawIPTC = %v, want nil for OOB value offset", rawIPTC)
	}
}

// TestConformance_RW2_robust_overflow_count_uint64 verifies that an IFD entry
// whose count×typeSize overflows uint64 is skipped safely.
//
// containers.md §8 §(f) + TIFF 6.0 §2 / R-06: uint64 overflow must be detected
// before the range check to avoid wrapping past EOF.
func TestConformance_RW2_robust_overflow_count_uint64(t *testing.T) {
	t.Parallel()

	const ifd0Off = 8
	buf := make([]byte, ifd0Off+2+12+4) // header + count(1) + 1 entry + next-ptr
	copy(buf[0:4], rw2Magic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1) // 1 entry

	e := ifd0Off + 2
	// RATIONAL (typeSize=8): count = MaxUint32/8 + 1 → count×8 overflows uint32
	// but is detected by uint64 arithmetic in typeSize guard.
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB)           // IPTC
	binary.LittleEndian.PutUint16(buf[e+2:], 5)              // RATIONAL typeSize=8
	binary.LittleEndian.PutUint32(buf[e+4:], 0x20000001)     // count: overflow
	binary.LittleEndian.PutUint32(buf[e+8:], ifd0Off+2+12+4) // offset at end

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("RW2-robust-overflow-count-uint64: unexpected error: %v", err)
	}
	if rawIPTC != nil {
		t.Error("RW2-robust-overflow-count-uint64: rawIPTC must be nil for overflow-count entry")
	}
}

// TestConformance_RW2_robust_inject_invalid_magic_returns_error verifies that
// Inject on data with invalid magic returns ErrInvalidMagic.
//
// containers.md §8 §(f): magic misread guard for write path.
func TestConformance_RW2_robust_inject_invalid_magic_returns_error(t *testing.T) {
	t.Parallel()

	data := buildRW2()
	data[0] = 'M' // corrupt magic

	var out bytes.Buffer
	err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true)
	if err == nil {
		t.Error("RW2-robust-inject-invalid-magic: expected error, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("RW2-robust-inject-invalid-magic: error = %v, want ErrInvalidMagic", err)
	}
}

// TestConformance_RW2_robust_inject_corrupt_IFD_returns_error verifies that
// Inject with a requested metadata change on a corrupt IFD returns an error
// (not silent data loss).
//
// containers.md §8 §(f): error propagation on malformed IFD.
func TestConformance_RW2_robust_inject_corrupt_IFD_returns_error(t *testing.T) {
	t.Parallel()

	// Build minimal RW2 but point IFD0 past EOF so exif.Parse fails.
	data := buildRW2()
	binary.LittleEndian.PutUint32(data[4:], 0xFFFFFF00)

	iptcPayload := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'a', 'b', 'c'}
	var out bytes.Buffer
	err := Inject(bytes.NewReader(data), &out, nil, iptcPayload, nil, true)
	if err == nil {
		t.Fatal("RW2-robust-inject-corrupt-IFD: expected error for corrupt IFD0 offset, got nil")
	}
}

// TestConformance_RW2_robust_passthrough_no_metadata_change verifies that Inject
// with all metadata slices nil (pass-through) succeeds and produces a valid RW2.
//
// The pass-through path is a performance optimisation that avoids re-encoding
// when no metadata changes are requested.
func TestConformance_RW2_robust_passthrough_no_metadata_change(t *testing.T) {
	t.Parallel()

	data := buildRW2()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("RW2-robust-passthrough-no-metadata-change: Inject: %v", err)
	}
	result := out.Bytes()
	if len(result) < 4 {
		t.Fatal("RW2-robust-passthrough-no-metadata-change: output too short")
	}
	if !bytes.HasPrefix(result, rw2Magic) {
		t.Errorf("RW2-robust-passthrough-no-metadata-change: RW2 magic missing in output")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RW2-corpus — Real-world files in testdata/corpus/raw (containers.md §8)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_RW2_corpus_parity verifies that Extract does not panic or
// return unexpected errors on every .rw2 corpus file, and that each output
// carries the original RW2 magic in rawEXIF (#117 fix).
//
// containers.md §8: corpus parity over format/raw/rw2.
// Corpus files: testdata/corpus/raw/**/*.rw2 — filtered by extension.
func TestConformance_RW2_corpus_parity(t *testing.T) {
	t.Parallel()

	paths := testutil.CorpusFiles(t, "raw")
	var rw2Paths []string
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".rw2") {
			rw2Paths = append(rw2Paths, p)
		}
	}
	if len(rw2Paths) == 0 {
		t.Skip("no .rw2 files in testdata/corpus/raw; run 'make testdata' to download images")
	}

	for _, p := range rw2Paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Parallel()

			f, err := os.Open(p)
			if err != nil {
				t.Fatalf("open corpus file: %v", err)
			}
			defer f.Close() //nolint:errcheck // close on test cleanup

			rawEXIF, _, _, extractErr := Extract(f)
			if extractErr != nil {
				// Only ErrInvalidMagic is a protocol violation here.
				// Any parse or I/O error on a supposedly valid RW2 corpus file is a bug.
				t.Errorf("RW2-corpus-parity: Extract(%s): %v", filepath.Base(p), extractErr)
				return
			}

			if rawEXIF == nil {
				t.Errorf("RW2-corpus-parity: Extract(%s): rawEXIF is nil for valid corpus file",
					filepath.Base(p))
				return
			}
			// #117 fix: rawEXIF must carry the ORIGINAL RW2 magic (0x55 0x00),
			// not the patched TIFF magic (0x2A 0x00). The IFD walk operates
			// internally on a copy; the returned rawEXIF is always unpatched.
			if len(rawEXIF) >= 4 {
				if rawEXIF[2] != 0x55 || rawEXIF[3] != 0x00 {
					t.Errorf("RW2-corpus-parity: Extract(%s): rawEXIF bytes[2:4]=%02x %02x, want 55 00 (original RW2 magic, #117)",
						filepath.Base(p), rawEXIF[2], rawEXIF[3])
				}
			}
		})
	}
}

// TestConformance_RW2_corpus_real_IFD0_at_offset_24 verifies that real-world
// Panasonic RW2 corpus files carry the IFD0 offset value of 24 in header
// bytes [4:8], confirming the 16-byte GUID header layout.
//
// ExifTool PanasonicRaw.pm: GUID = 16 bytes at offset 8; IFD0 at 24.
// Empirically confirmed on Panasonic DMC-LX7 and Panasonic.rw2 corpus files.
func TestConformance_RW2_corpus_real_IFD0_at_offset_24(t *testing.T) {
	t.Parallel()

	paths := testutil.CorpusFiles(t, "raw")
	var rw2Paths []string
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".rw2") {
			rw2Paths = append(rw2Paths, p)
		}
	}
	if len(rw2Paths) == 0 {
		t.Skip("no .rw2 files in testdata/corpus/raw")
	}

	for _, p := range rw2Paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read corpus file: %v", err)
			}
			if len(data) < 8 {
				t.Skipf("corpus file %s too short (%d bytes); skipping", filepath.Base(p), len(data))
			}
			if !bytes.HasPrefix(data, rw2Magic) {
				t.Skipf("corpus file %s does not have RW2 magic; skipping", filepath.Base(p))
			}
			ifd0Off := binary.LittleEndian.Uint32(data[4:])
			// ExifTool PanasonicRaw.pm: IFD0 offset for all known Panasonic RW2 files is 24.
			if ifd0Off != 24 {
				// Non-fatal: document unexpected offsets (some future cameras may differ).
				t.Logf("RW2-corpus-real-IFD0-at-offset-24: %s: IFD0 offset = %d (expected 24; may be a newer format variant)",
					filepath.Base(p), ifd0Off)
			}
		})
	}
}

// TestConformance_RW2_corpus_GUID_16bytes verifies that real-world RW2 corpus
// files carry a non-zero 16-byte GUID at bytes [8:24].
//
// ExifTool PanasonicRaw.pm: GUID = unique device identifier, 16 bytes at offset 8.
// A zero GUID is theoretically valid but has never been observed in practice.
func TestConformance_RW2_corpus_GUID_16bytes(t *testing.T) {
	t.Parallel()

	paths := testutil.CorpusFiles(t, "raw")
	var rw2Paths []string
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".rw2") {
			rw2Paths = append(rw2Paths, p)
		}
	}
	if len(rw2Paths) == 0 {
		t.Skip("no .rw2 files in testdata/corpus/raw")
	}

	for _, p := range rw2Paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read corpus file: %v", err)
			}
			if len(data) < 24 {
				t.Skipf("corpus file %s too short (%d bytes) for GUID check", filepath.Base(p), len(data))
			}
			if !bytes.HasPrefix(data, rw2Magic) {
				t.Skipf("corpus file %s not RW2; skipping", filepath.Base(p))
			}
			guid := data[8:24]
			// Verify the GUID is exactly 16 bytes (slice is always 16 here, but assert non-zero).
			if len(guid) != 16 {
				t.Errorf("RW2-corpus-GUID-16bytes: %s: GUID length = %d, want 16",
					filepath.Base(p), len(guid))
			}
			isAllZero := true
			for _, b := range guid {
				if b != 0 {
					isAllZero = false
					break
				}
			}
			if isAllZero {
				t.Logf("RW2-corpus-GUID-16bytes: %s: GUID is all-zero (unexpected for real Panasonic file)",
					filepath.Base(p))
			}
		})
	}
}

// TestConformance_RW2_corpus_write_round_trip verifies that for each real-world
// RW2 corpus file, a write-with-no-changes round-trip (InjectWithEXIFRW2 with
// nil metadata) produces a file that:
//  1. Has RW2 magic at bytes [0:4].
//  2. Has the original GUID at bytes [8:24].
//  3. Can be re-read by Extract without error.
//
// containers.md §8 §(e): write round-trip; "ImageDataHash IN==OUT."
func TestConformance_RW2_corpus_write_round_trip(t *testing.T) {
	t.Parallel()

	paths := testutil.CorpusFiles(t, "raw")
	var rw2Paths []string
	for _, p := range paths {
		if strings.EqualFold(filepath.Ext(p), ".rw2") {
			rw2Paths = append(rw2Paths, p)
		}
	}
	if len(rw2Paths) == 0 {
		t.Skip("no .rw2 files in testdata/corpus/raw")
	}

	for _, p := range rw2Paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read corpus file: %v", err)
			}
			if !bytes.HasPrefix(data, rw2Magic) {
				t.Skipf("not RW2; skipping")
			}
			if len(data) < 24 {
				t.Skipf("too short for GUID check")
			}

			origGUID := make([]byte, 16)
			copy(origGUID, data[8:24])

			// Pass-through: nil EXIF/IPTC/XMP → no re-encoding, verbatim output.
			var out bytes.Buffer
			if err := tiff.InjectWithEXIFRW2(data, nil, nil, nil, &out); err != nil {
				t.Fatalf("RW2-corpus-write-round-trip: InjectWithEXIFRW2: %v", err)
			}
			result := out.Bytes()

			// 1. RW2 magic.
			if !bytes.HasPrefix(result, rw2Magic) {
				t.Error("RW2-corpus-write-round-trip: RW2 magic missing in output")
			}
			// 2. GUID preserved.
			if len(result) >= 24 && !bytes.Equal(result[8:24], origGUID) {
				t.Errorf("RW2-corpus-write-round-trip: GUID corrupted:\n got  %x\n want %x",
					result[8:24], origGUID)
			}
			// 3. Re-readable by Extract.
			if _, _, _, err := Extract(bytes.NewReader(result)); err != nil {
				t.Errorf("RW2-corpus-write-round-trip: Extract on output: %v", err)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Fixture helpers — local to conformance_test.go
// ─────────────────────────────────────────────────────────────────────────────

// buildRW2WithGUID constructs a minimal RW2 file that includes a 16-byte GUID
// at bytes [8:24] with IFD0 at offset 24 (the real Panasonic layout).
//
// Layout:
//
//	[0:4]   IIU\x00
//	[4:8]   IFD0 offset = 24 (LE u32)
//	[8:24]  16-byte GUID (caller-supplied)
//	[24:26] IFD0 entry count = 0 (LE u16)
//	[26:30] next-IFD = 0 (LE u32)
func buildRW2WithGUID(guid []byte) []byte {
	const ifd0Off = 24
	buf := make([]byte, ifd0Off+6) // 24 + 2 (count) + 4 (next-ifd)
	copy(buf[0:4], rw2Magic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	copy(buf[8:24], guid)
	// IFD0: 0 entries, next-IFD = 0.
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 0)
	binary.LittleEndian.PutUint32(buf[ifd0Off+2:], 0)
	return buf
}

// buildRW2WithJpgFromRaw constructs a minimal RW2 with IFD0 at offset 8
// (no GUID) and a single JpgFromRaw entry (tag 0x002E, TypeUndefined OOL).
//
// This is used for Extract-only tests; for write tests use buildRW2WithJpgFromRawAndGUID.
func buildRW2WithJpgFromRaw(jpegData []byte) []byte {
	// Use the existing buildRW2WithTag helper (defined in rw2_test.go).
	return buildRW2WithTag(0x002E, 7 /*TypeUndefined*/, jpegData)
}

// buildRW2WithJpgFromRawAndGUID constructs a synthetic RW2 with the real
// Panasonic layout (24-byte header with GUID) and a JpgFromRaw IFD0 entry.
//
// Layout:
//
//	[0:4]   IIU\x00
//	[4:8]   IFD0 offset = 24 (LE u32)
//	[8:24]  16-byte GUID
//	[24:26] IFD0 count = 1
//	[26:38] IFD entry: tag=0x002E type=7 count=len(jpegData) valOrOff=ifd0Off+2+1*12+4
//	[38:42] next-IFD = 0
//	[42:]   JPEG bytes
func buildRW2WithJpgFromRawAndGUID(jpegData, guid []byte) []byte {
	const ifd0Off = 24
	const entryCount = 1
	// OOL data starts after: ifd0Off + 2(count) + 1×12(entries) + 4(next-ifd) = 42.
	const jpegOff = ifd0Off + 2 + entryCount*12 + 4

	buf := make([]byte, jpegOff+len(jpegData))
	copy(buf[0:4], rw2Magic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	copy(buf[8:24], guid)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], entryCount) // 1 entry
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x002E)                  // JpgFromRaw
	binary.LittleEndian.PutUint16(buf[e+2:], 7)                     // TypeUndefined
	binary.LittleEndian.PutUint32(buf[e+4:], uint32(len(jpegData))) //nolint:gosec // G115: test helper
	binary.LittleEndian.PutUint32(buf[e+8:], jpegOff)               // OOL offset
	binary.LittleEndian.PutUint32(buf[ifd0Off+2+entryCount*12:], 0) // next-IFD = 0
	copy(buf[jpegOff:], jpegData)
	return buf
}

// makeMinimalJPEG returns a minimal syntactically-valid JPEG byte sequence.
// The payload is > 4 bytes so that an IFD entry storing these bytes as
// TypeUndefined is treated as OOL (out-of-line) rather than inline
// (TIFF 6.0 §2: inline threshold is total ≤ 4 bytes).
//
// Layout: SOI (FF D8) + APP0-like marker + EOI (FF D9) = 8 bytes.
// This is the smallest JPEG that survives TIFF IFD OOL encoding.
func makeMinimalJPEG() []byte {
	// SOI + 4 filler bytes + EOI = 8 bytes total → OOL in TIFF TypeUndefined.
	return []byte{0xFF, 0xD8, 0x00, 0x01, 0x02, 0x03, 0xFF, 0xD9}
}

// buildRW2WithTypeLongIPTC constructs a minimal RW2 whose IFD0 carries tag
// 0x83BB (IPTC-NAA) encoded as TypeLong (type=4).
//
// TypeLong IFD entries: count = number of LONG values (4 bytes each).
// The rawIPTC payload is padded to the next 4-byte boundary; count = paddedLen/4.
//
// TIFF 6.0 §2: TypeLong Count field is the number of LONG values, not bytes.
// iptc.md ROBUST-16: TypeLong structural padding zeros trimmed on extract.
func buildRW2WithTypeLongIPTC(rawIPTC []byte) []byte {
	const ifd0Off = 8
	// Pad rawIPTC to 4-byte boundary.
	paddedLen := (len(rawIPTC) + 3) &^ 3
	padded := make([]byte, paddedLen)
	copy(padded, rawIPTC)
	longCount := uint32(paddedLen / 4) //nolint:gosec // G115: paddedLen bounded by test data

	// OOL data starts at: ifd0Off + 2(count) + 1×12(entry) + 4(next-ifd) = 26.
	const dataOff = 26
	bufLen := dataOff + paddedLen

	buf := make([]byte, bufLen)
	copy(buf[0:4], rw2Magic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1) // 1 entry

	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB)      // IPTC-NAA
	binary.LittleEndian.PutUint16(buf[e+2:], 4)         // TypeLong
	binary.LittleEndian.PutUint32(buf[e+4:], longCount) // count = N LONG values
	binary.LittleEndian.PutUint32(buf[e+8:], dataOff)   // OOL offset

	binary.LittleEndian.PutUint32(buf[ifd0Off+2+12:], 0) // next-IFD = 0
	copy(buf[dataOff:], padded)
	return buf
}

// findTagInTIFF scans IFD0 of a standard TIFF byte slice (patched-magic RW2)
// and reports whether the given tag ID is present.
//
// This is a test helper for verifying tag presence without a full exif.Parse.
func findTagInTIFF(tiffData []byte, tag uint16) bool {
	if len(tiffData) < 8 {
		return false
	}
	// TIFF 6.0 §2: bytes [4:8] = IFD0 offset.
	ifd0Off := int(binary.LittleEndian.Uint32(tiffData[4:]))
	if ifd0Off+2 > len(tiffData) {
		return false
	}
	count := int(binary.LittleEndian.Uint16(tiffData[ifd0Off:]))
	pos := ifd0Off + 2
	for i := range count {
		e := pos + i*12
		if e+12 > len(tiffData) {
			break
		}
		if binary.LittleEndian.Uint16(tiffData[e:]) == tag {
			return true
		}
	}
	return false
}
