package arw

// arw_delegation_test.go — task #48: verify that ARW correctly delegates to
// the TIFF path and returns EXIF/IPTC/XMP through that delegation.
//
// Spec: Sony ARW is a TIFF-based format (standard TIFF LE header 0x002A).
// All metadata extraction is handled by format/tiff; this package wraps it.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
)

// buildARWWithIPTCAndXMP constructs a minimal ARW/TIFF byte stream carrying
// an IPTC tag (0x83BB) and an XMP tag (0x02BC) in IFD0.
// The data is valid standard TIFF LE — ARW uses exactly this format.
func buildARWWithIPTCAndXMP(iptcData, xmpData []byte) []byte {
	order := binary.LittleEndian

	// Layout: header(8) + ifd(2+2×12+4) + iptcData + xmpData
	const hdr = 8
	const ifdSize = 2 + 2*12 + 4 // count(2) + 2 entries(24) + next(4) = 30
	iptcOff := hdr + ifdSize
	xmpOff := iptcOff + len(iptcData)
	totalSize := xmpOff + len(xmpData)

	buf := make([]byte, totalSize)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], hdr)

	// IFD0: 2 entries
	order.PutUint16(buf[hdr:], 2)
	// Entry 0: IPTC (0x83BB, UNDEFINED, out-of-line)
	e0 := hdr + 2
	order.PutUint16(buf[e0:], 0x83BB)
	order.PutUint16(buf[e0+2:], 7)
	order.PutUint32(buf[e0+4:], uint32(len(iptcData))) //nolint:gosec // G115: test-helper, bounded by buf
	order.PutUint32(buf[e0+8:], uint32(iptcOff))
	// Entry 1: XMP (0x02BC, BYTE, out-of-line)
	e1 := e0 + 12
	order.PutUint16(buf[e1:], 0x02BC)
	order.PutUint16(buf[e1+2:], 1)
	order.PutUint32(buf[e1+4:], uint32(len(xmpData))) //nolint:gosec // G115: test-helper, bounded by buf
	order.PutUint32(buf[e1+8:], uint32(xmpOff))       //nolint:gosec // G115: test-helper, bounded by buf
	// next-IFD = 0
	order.PutUint32(buf[e1+12:], 0)

	copy(buf[iptcOff:], iptcData)
	copy(buf[xmpOff:], xmpData)
	return buf
}

// TestARWDelegationIPTCAndXMP verifies that ARW.Extract returns IPTC and XMP
// metadata correctly through the TIFF delegation path.
//
// This is the primary delegation test required by task #48 F: "each of the 6
// RAW formats returns the expected metadata through the tiff delegation path".
func TestARWDelegationIPTCAndXMP(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'S', 'o', 'n', 'y', '!'}
	wantXMP := []byte(`<?xpacket begin="" uid="W5M0"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	data := buildARWWithIPTCAndXMP(wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
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

// TestARWDelegationErrorPrefix verifies that errors from tiff.Extract are
// wrapped with the "arw:" prefix so callers can identify the origin.
func TestARWDelegationErrorPrefix(t *testing.T) {
	t.Parallel()
	// Feed invalid byte-order to trigger tiff.Extract error.
	bad := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, _, _, err := Extract(bytes.NewReader(bad))
	if err == nil {
		t.Fatal("expected error for non-TIFF input, got nil")
	}
	errStr := err.Error()
	if len(errStr) < 4 || errStr[:4] != "arw:" {
		t.Errorf("error prefix: got %q, want prefix %q", errStr, "arw:")
	}
}

// TestARWTruncatedMidWay verifies that a truncated ARW/TIFF file (valid header,
// claims IFD entries but buffer truncated before them) does not panic.
// Must return an error or partial result, but never a panic.
func TestARWTruncatedMidWay(t *testing.T) {
	t.Parallel()
	// Header valid; IFD entry count = 2 but only 6 bytes follow (< 1 full entry).
	buf := make([]byte, 18)                        // 8 (hdr) + 2 (count) + 8 (partial entry bytes)
	binary.LittleEndian.PutUint16(buf[0:], 0x4949) // 'II'
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8) // IFD0 at 8
	binary.LittleEndian.PutUint16(buf[8:], 2) // claims 2 entries

	// Must not panic.
	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	// rawEXIF must be non-nil (the whole TIFF byte slice is returned as rawEXIF).
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil even for truncated ARW")
	}
}

// TestARWBigTIFFReturnsError verifies that a BigTIFF input (magic 0x002B)
// is rejected gracefully by ARW.Extract (via tiff.Extract's magic check).
func TestARWBigTIFFReturnsError(t *testing.T) {
	t.Parallel()
	bigTIFF := make([]byte, 16)
	bigTIFF[0], bigTIFF[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(bigTIFF[2:], 0x002B) // BigTIFF magic
	binary.LittleEndian.PutUint16(bigTIFF[4:], 8)
	binary.LittleEndian.PutUint16(bigTIFF[6:], 0)
	binary.LittleEndian.PutUint64(bigTIFF[8:], 16)

	_, _, _, err := Extract(bytes.NewReader(bigTIFF))
	if err == nil {
		t.Error("Extract BigTIFF: expected error, got nil")
	}
	// The wrapped error must include ErrUnsupportedMagic from the tiff package.
	if !errors.Is(err, tiff.ErrUnsupportedMagic) {
		t.Errorf("expected errors.Is(err, tiff.ErrUnsupportedMagic); got %v", err)
	}
}
