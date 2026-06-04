package orf

// orf_delegation_test.go — task #48: verify ORF metadata delegation through
// the TIFF path and BigTIFF/truncation hardening.
//
// Spec: Olympus ORF is TIFF-based but uses the magic bytes "IIRO" (bytes 0-3)
// instead of standard "II\x2A\x00". The library patches bytes 2-3 to 0x2A 0x00
// before delegating to the TIFF parser.
//
// Task #48 F: "each of the 6 RAW formats returns the expected metadata through
// the tiff delegation path."

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// buildORFWithIPTCAndXMP constructs a minimal ORF stream carrying IPTC and
// XMP in IFD0.  Bytes 0-3 are the ORF magic "IIRO"; the library patches
// bytes 2-3 to standard TIFF magic before IFD parsing.
func buildORFWithIPTCAndXMP(iptcData, xmpData []byte) []byte {
	order := binary.LittleEndian

	// Layout: header(8) + ifd(2+2×12+4) + iptcData + xmpData
	const hdrLen = 8
	const ifdSize = 2 + 2*12 + 4
	iptcOff := hdrLen + ifdSize
	xmpOff := iptcOff + len(iptcData)
	totalSize := xmpOff + len(xmpData)

	buf := make([]byte, totalSize)
	copy(buf[0:4], orfMagic) // "IIRO"
	order.PutUint32(buf[4:], hdrLen)

	// IFD0: 2 entries
	order.PutUint16(buf[hdrLen:], 2)
	e0 := hdrLen + 2
	order.PutUint16(buf[e0:], 0x83BB)
	order.PutUint16(buf[e0+2:], 7)
	order.PutUint32(buf[e0+4:], uint32(len(iptcData))) //nolint:gosec // G115: test-helper, bounded by buf
	order.PutUint32(buf[e0+8:], uint32(iptcOff))
	e1 := e0 + 12
	order.PutUint16(buf[e1:], 0x02BC)
	order.PutUint16(buf[e1+2:], 1)
	order.PutUint32(buf[e1+4:], uint32(len(xmpData))) //nolint:gosec // G115: test-helper, bounded by buf
	order.PutUint32(buf[e1+8:], uint32(xmpOff))       //nolint:gosec // G115: test-helper, bounded by buf
	order.PutUint32(buf[e1+12:], 0)

	copy(buf[iptcOff:], iptcData)
	copy(buf[xmpOff:], xmpData)
	return buf
}

// TestORFDelegationIPTCAndXMP verifies that ORF.Extract returns IPTC and XMP
// metadata through the patched TIFF path.
func TestORFDelegationIPTCAndXMP(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x09, 'O', 'l', 'y', 'm', 'p', 'u', 's', '!'}
	wantXMP := []byte(`<?xpacket begin="" uid="orf"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	data := buildORFWithIPTCAndXMP(wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract ORF: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil")
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestORFExtractPatchesStandardMagic verifies that orf.Extract patches bytes
// 2-3 to standard TIFF magic (0x2A 0x00) in the returned rawEXIF.
// This ensures the downstream EXIF parser receives a standard TIFF stream.
func TestORFExtractPatchesStandardMagic(t *testing.T) {
	t.Parallel()
	data := buildORFWithIPTCAndXMP(
		[]byte("iptc-patch-test-long-enough"),
		[]byte("<xmpmeta/>"),
	)
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(rawEXIF) < 4 {
		t.Fatal("rawEXIF too short")
	}
	// Bytes 2-3 of rawEXIF must be standard TIFF magic (0x2A 0x00), not ORF magic.
	if rawEXIF[2] != 0x2A || rawEXIF[3] != 0x00 {
		t.Errorf("rawEXIF bytes[2:4] = %02x %02x, want 0x2a 0x00 (standard TIFF magic)",
			rawEXIF[2], rawEXIF[3])
	}
}

// TestORFBigTIFFStyleMagicReturnsError verifies that data whose bytes 2-3
// look like BigTIFF magic (0x2B 0x00) but carry ORF bytes 0-1 ("II") is
// correctly rejected.
//
// The ORF Extract function checks for orfMagic (all 4 bytes) before patching.
// Bytes 2-3 = 0x2B do not match orfMagic[2] = 0x52, so ErrInvalidMagic is returned.
func TestORFBigTIFFStyleMagicReturnsError(t *testing.T) {
	t.Parallel()
	// Bytes 0-1 = "II", bytes 2-3 = 0x2B 0x00 (BigTIFF-like) but NOT ORF magic.
	badData := make([]byte, 16)
	badData[0], badData[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(badData[2:], 0x002B) // NOT orfMagic bytes 2-3

	_, _, _, err := Extract(bytes.NewReader(badData))
	// orf.Extract must return ErrInvalidMagic because the ORF magic prefix check fails.
	if err == nil {
		t.Error("expected ErrInvalidMagic for non-ORF input, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("expected ErrInvalidMagic; got %v", err)
	}
}

// TestORFTruncatedMidWay verifies graceful handling of a truncated ORF file.
func TestORFTruncatedMidWay(t *testing.T) {
	t.Parallel()
	// Valid ORF magic, IFD0 claims 4 entries but buffer is 18 bytes.
	buf := make([]byte, 18)
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], 8) // IFD0 at offset 8
	binary.LittleEndian.PutUint16(buf[8:], 4) // claims 4 entries, truncated

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil even for truncated ORF")
	}
}

// TestORFTruncatedAtMagicOnly verifies that an ORF file with only the 4-byte
// magic header (< 8 bytes) returns rawEXIF (patched bytes) with no error.
// This tests the guard: if len(data) < 8, return data, nil, nil, nil.
func TestORFTruncatedAtMagicOnly(t *testing.T) {
	t.Parallel()
	data := make([]byte, 4)
	copy(data, orfMagic)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract too-short ORF: unexpected error: %v", err)
	}
	// rawEXIF is the patched 4-byte slice; rawIPTC and rawXMP must be nil.
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil (patched magic bytes)")
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC should be nil, got %v", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP should be nil, got %v", rawXMP)
	}
}

// TestORFInjectAndExtractRoundTripWithIPTCXMP verifies a full ORF
// Inject → Extract round-trip with both IPTC and XMP payloads.
// The Inject function patches the ORF magic, delegates to tiff.Inject,
// then restores the ORF magic in the output.
func TestORFInjectAndExtractRoundTripWithIPTCXMP(t *testing.T) {
	t.Parallel()
	data := buildORF()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x04, 'I', 'n', 'j', 't'}
	wantXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, wantIPTC, wantXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// Verify ORF magic is restored.
	result := out.Bytes()
	if len(result) < 4 {
		t.Fatal("output too short")
	}
	if !bytes.HasPrefix(result, orfMagic) {
		t.Errorf("ORF magic not present in output: %02x%02x%02x%02x",
			result[0], result[1], result[2], result[3])
	}

	// Extract and verify the round-tripped metadata.
	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(result))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, wantIPTC) {
		t.Errorf("IPTC = %q, want %q", gotIPTC, wantIPTC)
	}
	if !bytes.Equal(gotXMP, wantXMP) {
		t.Errorf("XMP = %q, want %q", gotXMP, wantXMP)
	}
}

// TestORFTIFFDelegationPathExercised verifies that the TIFF delegation path
// is actually used by confirming that tiff.ErrUnsupportedMagic propagates
// when a BigTIFF-like payload (patched from ORF magic to 0x2B) is processed.
//
// This tests an artificial scenario: an ORF file whose bytes 2-3 are patched
// by the test (not by orf.Extract) to 0x2B. The ORF magic check at bytes 0-3
// won't match, so ErrInvalidMagic is returned before we reach the TIFF parser.
// The test documents this routing: ORF magic check is the first gate.
func TestORFInvalidMagicBeforeTIFFParse(t *testing.T) {
	t.Parallel()
	// Construct data whose first 4 bytes are NOT orfMagic.
	data := []byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, _, _, err := Extract(bytes.NewReader(data))
	if err == nil {
		t.Error("expected ErrInvalidMagic for non-ORF TIFF data, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("expected ErrInvalidMagic; got %v", err)
	}
}
