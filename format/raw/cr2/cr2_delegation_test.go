package cr2

// cr2_delegation_test.go — task #48: verify CR2 metadata delegation through
// the TIFF path and security/edge-case hardening.
//
// Spec: Canon CR2 is a TIFF-based format. The "CR" marker at bytes 8-9 is
// Canon-specific (CR2 spec §3.1); all other parsing uses standard TIFF IFD
// traversal (TIFF 6.0 §2).

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildCR2WithIPTCAndXMP constructs a minimal CR2 file carrying IPTC (0x83BB)
// and XMP (0x02BC) in IFD0. Bytes 8-9 carry the "CR" Canon signature.
func buildCR2WithIPTCAndXMP(iptcData, xmpData []byte) []byte {
	order := binary.LittleEndian

	// Layout: header(10+2pad) + ifd(2+2×12+4) + iptcData + xmpData
	// Header is 8 bytes; CR2 extends it with "CR" at bytes 8-9 (and version/etc).
	// We write 12 bytes of header area (to include the "CR" marker),
	// then IFD0 at offset 8 (standard TIFF).
	const hdrLen = 8
	const ifdSize = 2 + 2*12 + 4
	iptcOff := hdrLen + ifdSize
	xmpOff := iptcOff + len(iptcData)
	totalSize := xmpOff + len(xmpData)

	// Ensure buffer is at least 10 bytes to place the "CR" marker.
	if totalSize < 10 {
		totalSize = 10
	}
	buf := make([]byte, totalSize)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], hdrLen) // IFD0 at offset 8
	buf[8], buf[9] = 'C', 'R'        // Canon CR2 signature (CR2 spec §3.1)

	// IFD0: 2 entries
	order.PutUint16(buf[hdrLen:], 2)
	e0 := hdrLen + 2
	order.PutUint16(buf[e0:], 0x83BB)
	order.PutUint16(buf[e0+2:], 7)
	order.PutUint32(buf[e0+4:], uint32(len(iptcData))) //nolint:gosec // G115: test-helper offset bounded by buf allocation
	order.PutUint32(buf[e0+8:], uint32(iptcOff))
	e1 := e0 + 12
	order.PutUint16(buf[e1:], 0x02BC)
	order.PutUint16(buf[e1+2:], 1)
	order.PutUint32(buf[e1+4:], uint32(len(xmpData))) //nolint:gosec // G115: test-helper offset bounded by buf allocation
	order.PutUint32(buf[e1+8:], uint32(xmpOff))       //nolint:gosec // G115: test-helper offset bounded by buf allocation
	order.PutUint32(buf[e1+12:], 0)                   // next-IFD = 0

	copy(buf[iptcOff:], iptcData)
	copy(buf[xmpOff:], xmpData)
	return buf
}

// TestCR2DelegationIPTCAndXMP verifies that CR2.Extract returns IPTC and XMP
// metadata correctly through the TIFF delegation path.
//
// Task #48 F: "each of the 6 RAW formats returns the expected metadata through
// the tiff delegation path."
func TestCR2DelegationIPTCAndXMP(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x06, 'C', 'a', 'n', 'o', 'n', '!'}
	wantXMP := []byte(`<?xpacket begin="" uid="W5M0"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	data := buildCR2WithIPTCAndXMP(wantIPTC, wantXMP)

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

// TestCR2DelegationErrorPrefix verifies that errors from tiff.Extract are
// wrapped with the "cr2:" prefix.
func TestCR2DelegationErrorPrefix(t *testing.T) {
	t.Parallel()
	bad := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, _, _, err := Extract(bytes.NewReader(bad))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errStr := err.Error()
	if len(errStr) < 4 || errStr[:4] != "cr2:" {
		t.Errorf("error prefix: got %q, want prefix %q", errStr, "cr2:")
	}
}

// TestCR2TruncatedMidWay verifies graceful handling of a truncated CR2 file.
func TestCR2TruncatedMidWay(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 18)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8)
	buf[8], buf[9] = 'C', 'R'
	binary.LittleEndian.PutUint16(buf[8:], 3) // IFD0 at offset 8: count = 3
	// Buffer truncated — fewer than 3×12 entry bytes follow.

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil even for truncated CR2")
	}
}

// TestCR2BigTIFFSucceeds verifies that a BigTIFF input (magic 0x002B) is now
// accepted by CR2.Extract (task #54: BigTIFF read support added to tiff.Extract).
func TestCR2BigTIFFSucceeds(t *testing.T) {
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
