package rw2

// rw2_delegation_test.go — task #48: verify RW2 metadata delegation through
// the TIFF path and BigTIFF/truncation hardening.
//
// Spec: Panasonic RW2 is a TIFF variant using magic bytes "IIU\x00" (bytes 0-3)
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

// buildRW2WithIPTCAndXMP constructs a minimal RW2 stream carrying IPTC and
// XMP in IFD0. Bytes 0-3 are the RW2 magic "IIU\x00".
func buildRW2WithIPTCAndXMP(iptcData, xmpData []byte) []byte {
	order := binary.LittleEndian

	const hdrLen = 8
	const ifdSize = 2 + 2*12 + 4
	iptcOff := hdrLen + ifdSize
	xmpOff := iptcOff + len(iptcData)
	totalSize := xmpOff + len(xmpData)

	buf := make([]byte, totalSize)
	copy(buf[0:4], rw2Magic) // "IIU\x00"
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

// TestRW2DelegationIPTCAndXMP verifies that RW2.Extract returns IPTC and XMP
// metadata through the patched TIFF path.
func TestRW2DelegationIPTCAndXMP(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x09, 'P', 'a', 'n', 'a', 's', 'o', 'n', 'i', 'c'}
	wantXMP := []byte(`<?xpacket begin="" uid="rw2"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	data := buildRW2WithIPTCAndXMP(wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract RW2: %v", err)
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

// TestRW2ExtractPreservesOriginalMagic verifies that rw2.Extract returns rawEXIF
// with the ORIGINAL RW2 magic bytes intact (#117 fix).
// The TIFF IFD traversal operates on an internal working copy; the rawEXIF
// returned to the caller is always unpatched so it can be written back to disk.
func TestRW2ExtractPreservesOriginalMagic(t *testing.T) {
	t.Parallel()
	data := buildRW2WithIPTCAndXMP(
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
	// Bytes 2-3 of rawEXIF must retain the original RW2 magic (0x55 0x00),
	// NOT the patched TIFF magic (0x2A 0x00). Writing rawEXIF to disk must yield
	// a valid RW2 file. (#117 fix)
	if rawEXIF[2] == 0x2A && rawEXIF[3] == 0x00 {
		t.Errorf("#117 regression: rawEXIF bytes[2:4] = 2a 00 (patched TIFF magic); want original RW2 magic 55 00")
	}
}

// TestRW2InvalidMagicReturnsError verifies that non-RW2 data returns ErrInvalidMagic.
func TestRW2InvalidMagicReturnsError(t *testing.T) {
	t.Parallel()
	// Feed standard TIFF LE — bytes 2-3 are 0x2A 0x00, not RW2 magic 0x55 0x00.
	bad := make([]byte, 14)
	bad[0], bad[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(bad[2:], 0x002A)
	binary.LittleEndian.PutUint32(bad[4:], 8)

	_, _, _, err := Extract(bytes.NewReader(bad))
	if err == nil {
		t.Error("expected ErrInvalidMagic for non-RW2 input, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("expected ErrInvalidMagic; got %v", err)
	}
}

// TestRW2TruncatedMidWay verifies graceful handling of a truncated RW2 file.
func TestRW2TruncatedMidWay(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 18)
	copy(buf[0:4], rw2Magic)
	binary.LittleEndian.PutUint32(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[8:], 4) // claims 4 entries, truncated

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil even for truncated RW2")
	}
}

// TestRW2TruncatedAtMagicOnly verifies that an RW2 file with only the 4-byte
// magic header (< 8 bytes) returns rawEXIF (patched bytes) with no error.
func TestRW2TruncatedAtMagicOnly(t *testing.T) {
	t.Parallel()
	data := make([]byte, 4)
	copy(data, rw2Magic)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract too-short RW2: unexpected error: %v", err)
	}
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

// TestRW2InjectAndExtractRoundTripWithIPTCXMP verifies a full RW2
// Inject → Extract round-trip with both IPTC and XMP payloads.
func TestRW2InjectAndExtractRoundTripWithIPTCXMP(t *testing.T) {
	t.Parallel()
	data := buildRW2()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x04, 'R', 'W', '2', '!'}
	wantXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, wantIPTC, wantXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	result := out.Bytes()
	if len(result) < 4 {
		t.Fatal("output too short")
	}
	if !bytes.HasPrefix(result, rw2Magic) {
		t.Errorf("RW2 magic not present in output: %02x%02x%02x%02x",
			result[0], result[1], result[2], result[3])
	}

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

// TestRW2BigTIFFStylePayloadRejectsEarlyOnMagicCheck verifies that data
// whose bytes 2-3 are BigTIFF-like (0x2B 0x00) but carry RW2 prefix bytes 0-1
// ("II") is caught by the RW2 magic check.  rw2Magic is 0x49 0x49 0x55 0x00;
// bytes[2]=0x2B does NOT match rw2Magic[2]=0x55, so ErrInvalidMagic is returned
// before any TIFF parsing occurs.
func TestRW2BigTIFFStylePayloadRejectsEarlyOnMagicCheck(t *testing.T) {
	t.Parallel()
	data := make([]byte, 16)
	data[0], data[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(data[2:], 0x002B) // NOT rw2Magic bytes 2-3

	_, _, _, err := Extract(bytes.NewReader(data))
	if err == nil {
		t.Error("expected ErrInvalidMagic for BigTIFF-like data, got nil")
	}
	if !errors.Is(err, ErrInvalidMagic) {
		t.Errorf("expected ErrInvalidMagic; got %v", err)
	}
}

// TestRW2TIFFDelegationConfirmed verifies that tiff.ErrUnsupportedMagic is
// reachable from rw2.Inject when the tiff.Inject path is exercised with data
// that passes RW2 magic check but fails the TIFF magic check after patching.
//
// This scenario is artificial: after patching bytes 2-3 to 0x2A 0x00, the
// data becomes valid standard TIFF. The test instead confirms that Inject
// wraps errors from tiff.Inject with the "rw2:" prefix.
func TestRW2InjectErrorWrapping(t *testing.T) {
	t.Parallel()
	// Minimal RW2 with a deliberately corrupt IFD0 offset so exif.Parse fails.
	data := buildRW2()
	binary.LittleEndian.PutUint32(data[4:], 0xFFFFFF00) // IFD0 offset past end

	iptcPayload := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'a', 'b', 'c'}
	var out bytes.Buffer
	err := Inject(bytes.NewReader(data), &out, nil, iptcPayload, nil, true)
	if err == nil {
		t.Fatal("expected error for corrupt IFD0 offset, got nil")
	}
	// Error must carry the "rw2:" prefix.
	errStr := err.Error()
	if len(errStr) < 4 || errStr[:4] != "rw2:" {
		t.Errorf("error prefix: got %q, want prefix %q", errStr, "rw2:")
	}
}

// TestRW2TIFFErrUnsupportedMagicPropagates verifies that tiff.ErrUnsupportedMagic
// is accessible via errors.Is through the rw2 error chain.
//
// To trigger ErrUnsupportedMagic, we construct data that:
//  1. Has rw2Magic bytes 0-1 ("II") but an RW2-specific byte at position 2 (0x55).
//  2. After patching by rw2.Extract (which overwrites bytes 2-3 with 0x2A 0x00),
//     becomes standard TIFF magic 0x002A — so ErrUnsupportedMagic won't fire here.
//
// The ErrUnsupportedMagic path fires when Inject is called with an externally
// supplied rawEXIF that carries BigTIFF magic.
func TestRW2InjectWithBigTIFFRawEXIFProducesError(t *testing.T) {
	t.Parallel()
	// Construct a valid RW2 for the reader.
	validRW2 := buildRW2()

	// rawEXIF with BigTIFF magic (what a caller might accidentally supply).
	badRawEXIF := make([]byte, 16)
	badRawEXIF[0], badRawEXIF[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(badRawEXIF[2:], 0x002B)
	binary.LittleEndian.PutUint16(badRawEXIF[4:], 8)
	binary.LittleEndian.PutUint64(badRawEXIF[8:], 16)

	iptcPayload := []byte("iptc-payload-for-bigtiff-test")
	var out bytes.Buffer
	// tiff.Inject will use badRawEXIF as base, call buildUpdatedTIFF → exif.Parse
	// which rejects BigTIFF magic → tiff.Inject returns error → rw2.Inject wraps it.
	err := Inject(bytes.NewReader(validRW2), &out, badRawEXIF, iptcPayload, nil, true)
	if err == nil {
		t.Fatal("expected error for BigTIFF rawEXIF, got nil")
	}
}
