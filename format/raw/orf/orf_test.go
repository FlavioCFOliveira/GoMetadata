package orf

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildORF creates a minimal ORF file: standard TIFF bytes with the ORF magic
// bytes ("IIRO") replacing the standard TIFF marker at bytes 2-3.
func buildORF() []byte {
	buf := make([]byte, 14)
	copy(buf[0:4], orfMagic) // "IIRO"
	binary.LittleEndian.PutUint32(buf[4:], 8)
	// IFD0: 0 entries, next IFD = 0
	return buf
}

func TestExtractHasORFMagic(t *testing.T) {
	t.Parallel()
	data := buildORF()
	if !bytes.HasPrefix(data, orfMagic) {
		t.Fatal("test data does not start with ORF magic")
	}
}

func TestExtractReturnsRawEXIF(t *testing.T) {
	t.Parallel()
	data := buildORF()
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil, want non-nil patched TIFF payload")
	}
	// The returned rawEXIF should have standard TIFF magic (patched), not ORF magic.
	if len(rawEXIF) >= 4 && rawEXIF[2] == orfMagic[2] && rawEXIF[3] == orfMagic[3] {
		t.Error("rawEXIF still has ORF magic bytes; expected standard TIFF magic 0x2A 0x00")
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil (no IPTC tag in minimal ORF)", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil (no XMP tag in minimal ORF)", rawXMP)
	}
}

func TestExtractInvalidMagicReturnsError(t *testing.T) {
	t.Parallel()
	data := buildORF()
	data[0] = 'M' // corrupt magic
	_, _, _, err := Extract(bytes.NewReader(data))
	if err == nil {
		t.Error("Extract with invalid magic: expected error, got nil")
	}
}

func TestInjectOutputHasORFMagic(t *testing.T) {
	t.Parallel()
	data := buildORF()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	result := out.Bytes()
	if len(result) < 4 {
		t.Fatal("Inject output too short")
	}
	// Output must restore ORF magic (bytes 2-3 = "RO").
	if result[2] != orfMagic[2] || result[3] != orfMagic[3] {
		t.Errorf("ORF magic not restored: bytes[2:4] = %#02x %#02x, want %#02x %#02x",
			result[2], result[3], orfMagic[2], orfMagic[3])
	}
}

func TestInjectRoundTrip(t *testing.T) {
	t.Parallel()
	data := buildORF()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil after round-trip")
	}
}

func TestInjectInvalidMagicReturnsError(t *testing.T) {
	t.Parallel()
	data := buildORF()
	data[0] = 'M'
	var out bytes.Buffer
	err := Inject(bytes.NewReader(data), &out, nil, nil, nil)
	if err == nil {
		t.Error("Inject with invalid magic: expected error, got nil")
	}
}

// buildORFWithTag builds a minimal ORF containing a single IFD0 entry with
// the given tag. If the value fits in 4 bytes it is stored inline in the
// offset field; otherwise it is stored out-of-line immediately after the IFD.
//
// Structure (little-endian):
//
//	[0..3]  orfMagic "IIRO"
//	[4..7]  IFD0 offset = 8
//	[8..9]  entry count = 1
//	[10..21] IFD entry (12 bytes)
//	[22..25] next IFD = 0
//	[26..]  out-of-line value data (when len(value) > 4)
func buildORFWithTag(tag uint16, typ uint16, value []byte) []byte {
	const ifd0Off = 8
	// IFD = 2-byte count + 12-byte entry + 4-byte next-IFD pointer = 18 bytes
	// Out-of-line data starts at offset 8 + 18 = 26
	const dataOff = 26

	buf := make([]byte, dataOff+len(value))
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)

	// IFD: count = 1
	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1)

	// IFD entry
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], tag)
	binary.LittleEndian.PutUint16(buf[e+2:], typ)
	binary.LittleEndian.PutUint32(buf[e+4:], uint32(len(value))) //nolint:gosec // G115: test helper, intentional type cast
	if len(value) <= 4 {
		copy(buf[e+8:], value)
	} else {
		binary.LittleEndian.PutUint32(buf[e+8:], dataOff)
		copy(buf[dataOff:], value)
	}

	// next IFD = 0
	binary.LittleEndian.PutUint32(buf[e+12:], 0)
	return buf
}

// --- Test L: Extract with IPTC tag 0x83BB ---

// TestExtractTIFFTagsIPTC verifies that an ORF containing an IFD0 entry with
// tag 0x83BB (IPTC) causes Extract to return a non-nil rawIPTC.
func TestExtractTIFFTagsIPTC(t *testing.T) {
	t.Parallel()
	iptcData := []byte{0x1C, 0x02, 0x05, 0x00, 0x03, 'A', 'B', 'C'}
	// Type 7 = UNDEFINED (1 byte per unit); len(iptcData) = 8 → out-of-line
	data := buildORFWithTag(0x83BB, 7, iptcData)

	_, rawIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawIPTC == nil {
		t.Fatal("rawIPTC is nil; expected IPTC data from tag 0x83BB")
	}
	if !bytes.Equal(rawIPTC, iptcData) {
		t.Errorf("rawIPTC = %x, want %x", rawIPTC, iptcData)
	}
}

// --- Test L (inline variant): IPTC tag with inline value (total <= 4 bytes) ---

func TestExtractTIFFTagsIPTCInline(t *testing.T) {
	t.Parallel()
	iptcData := []byte{0x1C, 0x02} // 2 bytes → inline
	data := buildORFWithTag(0x83BB, 7, iptcData)

	_, rawIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawIPTC == nil {
		t.Fatal("rawIPTC is nil; expected inline IPTC data")
	}
	if !bytes.Equal(rawIPTC, iptcData) {
		t.Errorf("rawIPTC = %x, want %x", rawIPTC, iptcData)
	}
}

// --- Test M: Extract with XMP tag 0x02BC ---

// TestExtractTIFFTagsXMP verifies that an ORF containing an IFD0 entry with
// tag 0x02BC (XMP) causes Extract to return a non-nil rawXMP.
func TestExtractTIFFTagsXMP(t *testing.T) {
	t.Parallel()
	xmpData := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta/>`)
	data := buildORFWithTag(0x02BC, 1, xmpData) // type 1 = BYTE

	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil; expected XMP data from tag 0x02BC")
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, xmpData)
	}
}

// --- Test N: typeSize covers all type branches ---

// TestTypeSizeAllBranches exercises typeSize for every defined TIFF type code
// and the unknown-type fallback by building ORF entries with each type and
// triggering extractTIFFTags via Extract.
func TestTypeSizeAllBranches(t *testing.T) {
	t.Parallel()
	// typeSize return values:
	//  1→1 (BYTE), 2→1 (ASCII), 3→2 (SHORT), 4→4 (LONG), 5→8 (RATIONAL),
	//  6→1 (SBYTE), 7→1 (UNDEFINED), 8→2 (SSHORT), 9→4 (SLONG),
	//  10→8 (SRATIONAL), 11→4 (FLOAT), 12→8 (DOUBLE), 0→0 (unknown)
	tests := []struct {
		typ  uint16
		want uint32
	}{
		{1, 1}, {2, 1}, {3, 2}, {4, 4}, {5, 8},
		{6, 1}, {7, 1}, {8, 2}, {9, 4}, {10, 8},
		{11, 4}, {12, 8}, {0, 0}, {255, 0},
	}

	for _, tc := range tests {
		got := typeSize(tc.typ)
		if got != tc.want {
			t.Errorf("typeSize(%d) = %d, want %d", tc.typ, got, tc.want)
		}
	}
}

// --- Test O: extractTIFFTags with out-of-bounds IFD offset ---

// TestExtractOutOfBoundsIFDOffset verifies that an ORF whose IFD0 offset
// points past the end of the data slice does not panic and returns no error.
func TestExtractOutOfBoundsIFDOffset(t *testing.T) {
	t.Parallel()
	// Start from a valid minimal ORF, then corrupt the IFD0 offset to point
	// far beyond the data.
	data := buildORF()
	// IFD0 offset is stored at bytes 4-7 (LE uint32). Point it to 0xFFFFFF00.
	binary.LittleEndian.PutUint32(data[4:], 0xFFFFFF00)

	// Must not panic; may return nil or non-nil rawEXIF, but no crash.
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract with out-of-bounds IFD offset: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF should still be non-nil (the patched TIFF bytes)")
	}
	// IPTC and XMP must be nil since no valid IFD entries could be parsed.
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil", rawXMP)
	}
}

// --- Test P: Inject with IPTC and XMP payloads ---

// TestInjectIPTCXMP verifies that calling Inject with IPTC and XMP payloads
// does not panic and produces a valid ORF output (correct magic bytes).
// Round-trip correctness of IPTC/XMP depends on the TIFF delegate; this test
// checks for absence of panic and correct ORF magic restoration.
func TestInjectIPTCXMP(t *testing.T) {
	t.Parallel()
	data := buildORF()
	iptcPayload := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'N', 'e', 'w'}
	xmpPayload := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)

	var out bytes.Buffer
	err := Inject(bytes.NewReader(data), &out, nil, iptcPayload, xmpPayload)
	if err != nil {
		t.Fatalf("Inject with IPTC+XMP: %v", err)
	}
	result := out.Bytes()
	if len(result) < 4 {
		t.Fatal("output too short")
	}
	if result[0] != orfMagic[0] || result[1] != orfMagic[1] ||
		result[2] != orfMagic[2] || result[3] != orfMagic[3] {
		t.Errorf("ORF magic not present in output: %02x %02x %02x %02x",
			result[0], result[1], result[2], result[3])
	}
}

// --- Additional: out-of-bounds out-of-line value offset in IFD entry ---

// TestExtractTIFFTagsOOBValueOffset verifies that an IFD entry whose out-of-line
// value offset is beyond the data slice is silently skipped (no panic).
func TestExtractTIFFTagsOOBValueOffset(t *testing.T) {
	t.Parallel()
	// Build an ORF where the entry claims 100 bytes of out-of-line data but
	// the offset points past the end of the buffer.
	const ifd0Off = 8
	const dataOff = 26

	// Buffer is only 26 bytes (no room for out-of-line data).
	buf := make([]byte, dataOff)
	copy(buf[0:4], orfMagic)
	binary.LittleEndian.PutUint32(buf[4:], ifd0Off)

	binary.LittleEndian.PutUint16(buf[ifd0Off:], 1)
	e := ifd0Off + 2
	binary.LittleEndian.PutUint16(buf[e:], 0x83BB) // IPTC tag
	binary.LittleEndian.PutUint16(buf[e+2:], 7)    // UNDEFINED type
	binary.LittleEndian.PutUint32(buf[e+4:], 100)  // count = 100 bytes
	binary.LittleEndian.PutUint32(buf[e+8:], 5000) // offset WAY past end

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	// rawIPTC must be nil because the OOB offset entry was skipped.
	_ = rawEXIF
	_ = rawXMP
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil for OOB value offset", rawIPTC)
	}
}

// TestExtractTooShortReturnsMagicOnly verifies that when an ORF file is valid
// (has correct magic) but too short to contain a full TIFF header (< 8 bytes),
// Extract returns the patched bytes as rawEXIF with no IPTC/XMP.
func TestExtractTooShortReturnsMagicOnly(t *testing.T) {
	t.Parallel()
	// Build a 6-byte ORF: 4-byte magic + 2 bytes — valid magic but too short for IFD.
	data := make([]byte, 6)
	copy(data[:4], orfMagic)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract too-short ORF: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF should be non-nil even for short ORF")
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil for short ORF", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil for short ORF", rawXMP)
	}
}
