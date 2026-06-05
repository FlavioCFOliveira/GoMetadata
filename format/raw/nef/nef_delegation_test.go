package nef

// nef_delegation_test.go — task #48: verify NEF metadata delegation through
// the TIFF path and security/edge-case hardening.
//
// Spec: Nikon NEF (Electronic Format) is a TIFF-based format.
// Nikon cameras use both little-endian (D-SLR) and big-endian (early Nikon D1)
// byte orders — both paths must be covered.

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildNEFWithIPTCAndXMP constructs a minimal NEF/TIFF stream (LE or BE) with
// IPTC (0x83BB) and XMP (0x02BC) in IFD0.
func buildNEFWithIPTCAndXMP(order binary.ByteOrder, iptcData, xmpData []byte) []byte {
	const hdrLen = 8
	const ifdSize = 2 + 2*12 + 4
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
	order.PutUint32(buf[e1+12:], 0)                   // next-IFD = 0

	copy(buf[iptcOff:], iptcData)
	copy(buf[xmpOff:], xmpData)
	return buf
}

// TestNEFDelegationIPTCAndXMPLE verifies metadata extraction through the TIFF
// delegation path for little-endian NEF files.
//
// Task #48 F: "each of the 6 RAW formats returns the expected metadata through
// the tiff delegation path."
func TestNEFDelegationIPTCAndXMPLE(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x06, 'N', 'i', 'k', 'o', 'n', '!'}
	wantXMP := []byte(`<?xpacket begin="" uid="nef"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	data := buildNEFWithIPTCAndXMP(binary.LittleEndian, wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract LE NEF: %v", err)
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

// TestNEFDelegationIPTCAndXMPBE verifies metadata extraction for big-endian
// NEF files.
//
// The Nikon D1 (1999) and some early professional Nikon bodies used big-endian
// byte order ("MM").  TIFF 6.0 §2: big-endian byte order is fully supported.
func TestNEFDelegationIPTCAndXMPBE(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x04, 'N', 'E', 'F', '!'}
	wantXMP := []byte("<xmpmeta nef='be'/>")

	data := buildNEFWithIPTCAndXMP(binary.BigEndian, wantIPTC, wantXMP)

	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract BE NEF: %v", err)
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestNEFDelegationErrorPrefix verifies error wrapping with "nef:" prefix.
func TestNEFDelegationErrorPrefix(t *testing.T) {
	t.Parallel()
	bad := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, _, _, err := Extract(bytes.NewReader(bad))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errStr := err.Error()
	if len(errStr) < 4 || errStr[:4] != "nef:" {
		t.Errorf("error prefix: got %q, want prefix %q", errStr, "nef:")
	}
}

// TestNEFTruncatedMidWay verifies graceful handling of a truncated NEF file.
func TestNEFTruncatedMidWay(t *testing.T) {
	t.Parallel()
	// Valid LE header; IFD0 claims 4 entries but buffer is only 18 bytes.
	buf := make([]byte, 18)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8)
	binary.LittleEndian.PutUint16(buf[8:], 4) // claims 4 entries, only 8 bytes follow

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil even for truncated NEF")
	}
}

// TestNEFTruncatedBigEndianMidWay verifies graceful handling of a truncated
// big-endian NEF file.
func TestNEFTruncatedBigEndianMidWay(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 18)
	buf[0], buf[1] = 'M', 'M'
	binary.BigEndian.PutUint16(buf[2:], 0x002A)
	binary.BigEndian.PutUint32(buf[4:], 8)
	binary.BigEndian.PutUint16(buf[8:], 4) // claims 4 entries, truncated

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil even for truncated BE NEF")
	}
}

// TestNEFBigTIFFSucceeds verifies that a BigTIFF input (magic 0x002B) is now
// accepted by NEF.Extract (task #54: BigTIFF read support added to tiff.Extract).
func TestNEFBigTIFFSucceeds(t *testing.T) {
	t.Parallel()
	bigTIFF := make([]byte, 16)
	bigTIFF[0], bigTIFF[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(bigTIFF[2:], 0x002B)
	binary.LittleEndian.PutUint16(bigTIFF[4:], 8)
	binary.LittleEndian.PutUint16(bigTIFF[6:], 0)
	binary.LittleEndian.PutUint64(bigTIFF[8:], 16)

	rawEXIF, _, _, err := Extract(bytes.NewReader(bigTIFF))
	if err != nil {
		t.Errorf("Extract BigTIFF: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("Extract BigTIFF: rawEXIF is nil")
	}
}
