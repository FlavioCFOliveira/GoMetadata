package dng

// dng_delegation_test.go — task #48: verify DNG metadata delegation through
// the TIFF path and security/edge-case hardening.
//
// Spec: Adobe DNG (Digital Negative) is a TIFF-based format (Adobe DNG
// Specification 1.7). It uses standard TIFF LE or BE byte order and IFD
// layout, with DNG-specific private tags (e.g. DNGVersion 0xC612).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
)

// buildDNGWithIPTCAndXMP constructs a minimal DNG/TIFF stream with IPTC and
// XMP in IFD0, plus the DNG marker tag 0xC612 (DNGVersion) so that the format
// detector recognises it as DNG.
func buildDNGWithIPTCAndXMP(order binary.ByteOrder, iptcData, xmpData []byte) []byte {
	// Layout: header(8) + ifd(2+3×12+4) + iptcData + xmpData
	// 3 entries: DNGVersion (inline), IPTC (out-of-line), XMP (out-of-line)
	const hdrLen = 8
	const ifdSize = 2 + 3*12 + 4
	iptcOff := hdrLen + ifdSize
	xmpOff := iptcOff + len(iptcData)
	totalSize := xmpOff + len(xmpData)

	buf := make([]byte, totalSize)
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], hdrLen)

	// IFD0: 3 entries
	order.PutUint16(buf[hdrLen:], 3)

	// Entry 0: DNGVersion (0xC612, BYTE, count=4, inline)
	// Adobe DNG Spec §6: DNGVersion identifies the file as DNG.
	e0 := hdrLen + 2
	order.PutUint16(buf[e0:], 0xC612) // DNGVersion tag
	order.PutUint16(buf[e0+2:], 1)    // BYTE
	order.PutUint32(buf[e0+4:], 4)    // count = 4 (4 bytes inline)
	buf[e0+8] = 1                     // DNG version 1.6.0.0
	buf[e0+9] = 6
	buf[e0+10] = 0
	buf[e0+11] = 0

	// Entry 1: IPTC (0x83BB, UNDEFINED, out-of-line)
	e1 := e0 + 12
	order.PutUint16(buf[e1:], 0x83BB)
	order.PutUint16(buf[e1+2:], 7)
	order.PutUint32(buf[e1+4:], uint32(len(iptcData))) //nolint:gosec // G115: test-helper, bounded by buf
	order.PutUint32(buf[e1+8:], uint32(iptcOff))

	// Entry 2: XMP (0x02BC, BYTE, out-of-line)
	e2 := e1 + 12
	order.PutUint16(buf[e2:], 0x02BC)
	order.PutUint16(buf[e2+2:], 1)
	order.PutUint32(buf[e2+4:], uint32(len(xmpData))) //nolint:gosec // G115: test-helper, bounded by buf
	order.PutUint32(buf[e2+8:], uint32(xmpOff))       //nolint:gosec // G115: test-helper, bounded by buf

	// next-IFD = 0
	order.PutUint32(buf[e2+12:], 0)

	copy(buf[iptcOff:], iptcData)
	copy(buf[xmpOff:], xmpData)
	return buf
}

// TestDNGDelegationIPTCAndXMPLE verifies that DNG.Extract returns IPTC and
// XMP metadata correctly through the TIFF delegation path (little-endian).
//
// Task #48 F: "each of the 6 RAW formats returns the expected metadata through
// the tiff delegation path."
func TestDNGDelegationIPTCAndXMPLE(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'A', 'd', 'o', 'b', 'e'}
	wantXMP := []byte(`<?xpacket begin="" uid="dng"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	data := buildDNGWithIPTCAndXMP(binary.LittleEndian, wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract LE DNG: %v", err)
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

// TestDNGDelegationIPTCAndXMPBE verifies extraction from a big-endian DNG.
// TIFF 6.0 §2: big-endian ("MM") byte order is fully supported.
func TestDNGDelegationIPTCAndXMPBE(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x04, 'D', 'N', 'G', '!'}
	wantXMP := []byte("<xmpmeta be='dng'/>")

	data := buildDNGWithIPTCAndXMP(binary.BigEndian, wantIPTC, wantXMP)

	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract BE DNG: %v", err)
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestDNGDelegationErrorPrefix verifies error wrapping with "dng:" prefix.
func TestDNGDelegationErrorPrefix(t *testing.T) {
	t.Parallel()
	bad := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, _, _, err := Extract(bytes.NewReader(bad))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errStr := err.Error()
	if len(errStr) < 4 || errStr[:4] != "dng:" {
		t.Errorf("error prefix: got %q, want prefix %q", errStr, "dng:")
	}
}

// TestDNGTruncatedMidWay verifies graceful handling of a truncated DNG file.
func TestDNGTruncatedMidWay(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 18)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[8:], 5) // claims 5 entries, truncated

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil even for truncated DNG")
	}
}

// TestDNGBigTIFFReturnsError verifies that a BigTIFF input is rejected.
func TestDNGBigTIFFReturnsError(t *testing.T) {
	t.Parallel()
	bigTIFF := make([]byte, 16)
	bigTIFF[0], bigTIFF[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(bigTIFF[2:], 0x002B)
	binary.LittleEndian.PutUint16(bigTIFF[4:], 8)
	binary.LittleEndian.PutUint16(bigTIFF[6:], 0)
	binary.LittleEndian.PutUint64(bigTIFF[8:], 16)

	_, _, _, err := Extract(bytes.NewReader(bigTIFF))
	if err == nil {
		t.Error("Extract BigTIFF: expected error, got nil")
	}
	if !errors.Is(err, tiff.ErrUnsupportedMagic) {
		t.Errorf("expected errors.Is(err, tiff.ErrUnsupportedMagic); got %v", err)
	}
}
