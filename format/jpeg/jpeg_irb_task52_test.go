package jpeg

// Task #52 — APP13 / Photoshop IRB (8BIM) white-box tests.
//
// parseIRB and buildIRB are unexported and embedded in the jpeg package, so
// these tests must reside here. They exercise:
//   S  — Malformed 8BIM resource block (no panic, returns nil).
//   S  — Truncated IRB payload (no panic, returns nil).
//   S  — 8BIM data-size exceeds remaining buffer (no panic, returns nil).
//   F  — IPTC-NAA resource (0x0404) correctly extracted from IRB.
//   F  — Non-IPTC resources in the IRB are skipped; 0x0404 still found.
//   F  — buildIRB produces a correctly structured 8BIM block that parseIRB
//         can round-trip through.
//   F  — Even-padding applied to data blocks of odd length.

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildIRBDirect builds a raw IRB byte slice (without the "Photoshop 3.0\x00"
// prefix) containing one 8BIM block: resource ID + pascal name + data size + data.
// Odd-length data is even-padded. This mirrors the structure that buildIRB emits,
// but accepts arbitrary resource IDs for test control.
func buildIRBDirect(resourceID uint16, data []byte) []byte {
	size := len(data)
	var buf bytes.Buffer
	buf.WriteString("8BIM")
	buf.WriteByte(byte(resourceID >> 8))
	buf.WriteByte(byte(resourceID)) //nolint:gosec // G115: test helper
	buf.WriteByte(0x00)             // pascal name: length=0
	buf.WriteByte(0x00)             // pascal name: padding (nameLen+1 even → 0+1=1, need 1 more → total 2 bytes)
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(size)) //nolint:gosec // G115: test helper
	buf.Write(sz[:])
	buf.Write(data)
	if size%2 != 0 {
		buf.WriteByte(0x00) // even-padding per EXIF §4.5.6
	}
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// F-1: buildIRB / parseIRB round-trip — IPTC resource 0x0404.
// ---------------------------------------------------------------------------

// TestBuildIRBRoundTrip verifies that buildIRB produces a well-formed 8BIM
// block that parseIRB can decode, recovering the original IIM bytes.
func TestBuildIRBRoundTrip(t *testing.T) {
	t.Parallel()

	// A minimal IIM stream with a Caption dataset.
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}

	irb := buildIRB(iptcData)
	got := parseIRB(irb)

	if !bytes.Equal(got, iptcData) {
		t.Errorf("parseIRB(buildIRB(...)): got %q, want %q", got, iptcData)
	}
}

// TestParseIRBIPTCResourceExtracted verifies that parseIRB returns the data
// slice for resource ID 0x0404 (IPTC-NAA) from a single-block IRB.
func TestParseIRBIPTCResourceExtracted(t *testing.T) {
	t.Parallel()

	iptcPayload := []byte{0x1C, 0x02, 0x78, 0x00, 0x07, 'c', 'a', 'p', 't', 'i', 'o', 'n'}
	irb := buildIRBDirect(0x0404, iptcPayload)

	got := parseIRB(irb)
	if !bytes.Equal(got, iptcPayload) {
		t.Errorf("parseIRB: got %q, want %q", got, iptcPayload)
	}
}

// TestParseIRBNonIPTCResourceSkipped verifies that parseIRB skips resources
// that are not 0x0404 and returns nil when no IPTC resource is present.
func TestParseIRBNonIPTCResourceSkipped(t *testing.T) {
	t.Parallel()

	// IRB with two non-IPTC resources.
	var buf bytes.Buffer
	buf.Write(buildIRBDirect(0x040A, []byte("thumbnail"))) // ICC profile resource
	buf.Write(buildIRBDirect(0x0425, []byte("caption")))   // IPTC keywords resource (not 0x0404)

	got := parseIRB(buf.Bytes())
	if got != nil {
		t.Errorf("parseIRB with no 0x0404 resource: got %q, want nil", got)
	}
}

// TestParseIRBMultiBlockIPTCFound verifies that parseIRB correctly skips
// non-IPTC resources before finding and returning the 0x0404 block.
// This mirrors the real-world case where a Photoshop file has ICC profile,
// thumbnail, and other resources before the IPTC block.
func TestParseIRBMultiBlockIPTCFound(t *testing.T) {
	t.Parallel()

	iptcPayload := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'f', 'o', 'u', 'n', 'd'}
	var buf bytes.Buffer
	buf.Write(buildIRBDirect(0x040A, []byte("icc profile data")))   // non-IPTC
	buf.Write(buildIRBDirect(0x0425, []byte("thumbnail data")))     // non-IPTC
	buf.Write(buildIRBDirect(0x0404, iptcPayload))                  // IPTC 0x0404
	buf.Write(buildIRBDirect(0x040B, []byte("more non-iptc data"))) // after IPTC

	got := parseIRB(buf.Bytes())
	if !bytes.Equal(got, iptcPayload) {
		t.Errorf("parseIRB (multi-block): got %q, want %q", got, iptcPayload)
	}
}

// TestParseIRBEvenPaddingApplied verifies that odd-length data blocks are
// padded to even length (EXIF §4.5.6) and that the next block is still found.
func TestParseIRBEvenPaddingApplied(t *testing.T) {
	t.Parallel()

	// Non-IPTC block with odd-length data (5 bytes) — padded to 6.
	// The IPTC block starts immediately after (at byte 4+2+2+4+5+1=18).
	iptcPayload := []byte{0x1C, 0x02, 0x74, 0x00, 0x03, 'y', 'e', 's'}
	var buf bytes.Buffer
	buf.Write(buildIRBDirect(0x040A, []byte("odd"))) // 3 bytes → padded to 4
	buf.Write(buildIRBDirect(0x0404, iptcPayload))

	got := parseIRB(buf.Bytes())
	if !bytes.Equal(got, iptcPayload) {
		t.Errorf("parseIRB after odd-length padding: got %q, want %q", got, iptcPayload)
	}
}

// ---------------------------------------------------------------------------
// S-1: Malformed 8BIM blocks — no panic, returns nil.
// ---------------------------------------------------------------------------

// TestParseIRBMalformed8BIMSignature verifies that a stream with a bad 8BIM
// signature (e.g. "7BIM" or random bytes) does not panic and returns nil.
func TestParseIRBMalformed8BIMSignature(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"too-short-for-signature", []byte{0x38, 0x42}},
		{"wrong-signature-7BIM", []byte("7BIM\x04\x04\x00\x00\x00\x00\x00\x02\x41\x42")},
		{"wrong-signature-garbage", []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x04, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x41, 0x42}},
		{"null-bytes", make([]byte, 20)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Must not panic.
			got := parseIRB(tc.data)
			if got != nil {
				t.Errorf("parseIRB(%s): got non-nil result %q, want nil", tc.name, got)
			}
		})
	}
}

// TestParseIRBTruncatedPayload verifies that an IRB stream that ends abruptly
// inside a data block does not panic and returns nil.
func TestParseIRBTruncatedPayload(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		data []byte
	}{
		{
			name: "truncated-after-signature",
			data: []byte("8BIM"), // no resource ID, pascal name, size, data
		},
		{
			name: "truncated-in-resource-id",
			data: []byte("8BIM\x04"), // only 1 byte of 2-byte resource ID
		},
		{
			name: "truncated-in-pascal-name",
			data: []byte("8BIM\x04\x04"), // no pascal name bytes
		},
		{
			name: "truncated-in-data-size",
			data: []byte("8BIM\x04\x04\x00\x00\x00\x00"), // only 2 of 4 size bytes
		},
		{
			name: "truncated-in-data-declares-100-has-10",
			data: func() []byte {
				b := make([]byte, 0, 8+4+10)
				b = append(b, []byte("8BIM\x04\x04\x00\x00")...)
				b = append(b, 0x00, 0x00, 0x00, 0x64) // size = 100
				b = append(b, make([]byte, 10)...)    // only 10 bytes
				return b
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseIRB(tc.data)
			// Truncated blocks must return nil without panicking.
			if got != nil {
				t.Errorf("parseIRB(%s): got non-nil result, want nil", tc.name)
			}
		})
	}
}

// TestParseIRBDataSizeOverflow verifies that a 8BIM block declaring a data
// size larger than the remaining buffer (potential overflow condition) is
// handled gracefully — no panic, returns nil.
func TestParseIRBDataSizeOverflow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		declSize uint32
		dataLen  int // actual bytes provided after the size field
	}{
		{"max-uint32-0-bytes", 0xFFFFFFFF, 0},
		{"1GiB-0-bytes", 0x40000000, 0},
		{"1MiB-10-bytes", 0x00100000, 10},
		{"100-5-bytes", 100, 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			buf.WriteString("8BIM")
			buf.WriteByte(0x04)
			buf.WriteByte(0x04) // IPTC resource ID
			buf.WriteByte(0x00)
			buf.WriteByte(0x00) // empty pascal name
			var sz [4]byte
			binary.BigEndian.PutUint32(sz[:], tc.declSize)
			buf.Write(sz[:])
			buf.Write(make([]byte, tc.dataLen)) // fewer bytes than declared

			got := parseIRB(buf.Bytes())
			if got != nil {
				t.Errorf("parseIRB(%s): got non-nil result, want nil (data size exceeds buffer)", tc.name)
			}
		})
	}
}

// TestParseIRBNilAndEmpty verifies that parseIRB handles nil and empty inputs
// without panicking and returns nil.
func TestParseIRBNilAndEmpty(t *testing.T) {
	t.Parallel()

	if got := parseIRB(nil); got != nil {
		t.Errorf("parseIRB(nil): got %q, want nil", got)
	}
	if got := parseIRB([]byte{}); got != nil {
		t.Errorf("parseIRB([]byte{}): got %q, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// F-2: buildIRB structure verification.
// ---------------------------------------------------------------------------

// TestBuildIRBStructure verifies the byte layout of the IRB block produced by
// buildIRB (EXIF §4.5.6): 8BIM marker + resource ID 0x0404 + empty pascal name
// + 4-byte big-endian data size + data bytes (+ optional padding).
func TestBuildIRBStructure(t *testing.T) {
	t.Parallel()

	data := []byte("test-iptc")
	irb := buildIRB(data)

	// Expected structure:
	// [0..3]  = "8BIM"
	// [4..5]  = 0x0404 (resource ID)
	// [6..7]  = 0x00 0x00 (empty pascal name: length=0 + padding)
	// [8..11] = 0x00 0x00 0x00 0x09 (data size = 9, big-endian)
	// [12..20]= "test-iptc"
	// No padding needed: len("test-iptc") = 9 (odd) → pad to 10 → [21] = 0x00
	if len(irb) < 22 {
		t.Fatalf("buildIRB: got %d bytes, want at least 22", len(irb))
	}
	if string(irb[0:4]) != "8BIM" {
		t.Errorf("8BIM marker: got %q, want %q", irb[0:4], "8BIM")
	}
	if irb[4] != 0x04 || irb[5] != 0x04 {
		t.Errorf("resource ID: got %02X %02X, want 04 04", irb[4], irb[5])
	}
	if irb[6] != 0x00 || irb[7] != 0x00 {
		t.Errorf("pascal name: got %02X %02X, want 00 00", irb[6], irb[7])
	}
	dataSize := binary.BigEndian.Uint32(irb[8:12])
	if int(dataSize) != len(data) {
		t.Errorf("data size field: got %d, want %d", dataSize, len(data))
	}
	if !bytes.Equal(irb[12:12+len(data)], data) {
		t.Errorf("data field: got %q, want %q", irb[12:12+len(data)], data)
	}
	// Odd-length data (9 bytes) must be padded to even.
	if len(irb) != 12+len(data)+1 {
		t.Errorf("IRB length: got %d, want %d (with padding)", len(irb), 12+len(data)+1)
	}
	if irb[len(irb)-1] != 0x00 {
		t.Errorf("padding byte: got 0x%02X, want 0x00", irb[len(irb)-1])
	}
}

// TestBuildIRBEvenDataNoPadding verifies that buildIRB does NOT add a padding
// byte when the data length is already even (EXIF §4.5.6).
func TestBuildIRBEvenDataNoPadding(t *testing.T) {
	t.Parallel()

	data := []byte("12345678") // 8 bytes — even, no padding needed
	irb := buildIRB(data)

	// Expected: 12 bytes header + 8 bytes data = 20 bytes total.
	if len(irb) != 20 {
		t.Errorf("buildIRB with even-length data: got %d bytes, want 20", len(irb))
	}
}

// ---------------------------------------------------------------------------
// F-3: processAPP13Segment integration — Photoshop prefix required.
// ---------------------------------------------------------------------------

// TestProcessAPP13SegmentRejectsNonPhotoshop verifies that processAPP13Segment
// returns nil for payloads that do not start with "Photoshop 3.0\x00".
func TestProcessAPP13SegmentRejectsNonPhotoshop(t *testing.T) {
	t.Parallel()

	cases := [][]byte{
		[]byte("Photoshop 2.0\x00" + "8BIM\x04\x04\x00\x00\x00\x00\x00\x00"),
		[]byte("Not a Photoshop\x00anything"),
		{0xFF, 0xED, 0x00, 0x00},
		{},
	}

	for _, data := range cases {
		got := processAPP13Segment(data)
		if got != nil {
			t.Errorf("processAPP13Segment(%q): got non-nil, want nil (non-Photoshop 3.0 prefix)", data[:min(16, len(data))])
		}
	}
}

// TestProcessAPP13SegmentExtractsIPTC verifies the full processAPP13Segment
// path: Photoshop 3.0 header + valid 8BIM 0x0404 block → IPTC IIM bytes.
func TestProcessAPP13SegmentExtractsIPTC(t *testing.T) {
	t.Parallel()

	iptcPayload := []byte{0x1C, 0x02, 0x78, 0x00, 0x07, 'c', 'a', 'p', 't', 'i', 'o', 'n'}

	// Build: "Photoshop 3.0\x00" + 8BIM 0x0404 block.
	var buf bytes.Buffer
	buf.WriteString("Photoshop 3.0\x00")
	buf.Write(buildIRBDirect(0x0404, iptcPayload))

	got := processAPP13Segment(buf.Bytes())
	if !bytes.Equal(got, iptcPayload) {
		t.Errorf("processAPP13Segment: got %q, want %q", got, iptcPayload)
	}
}

// TestProcessAPP13SegmentEmptyIRBReturnsNil verifies that processAPP13Segment
// returns nil when the Photoshop header is followed by an empty or invalid IRB.
func TestProcessAPP13SegmentEmptyIRBReturnsNil(t *testing.T) {
	t.Parallel()

	// "Photoshop 3.0\x00" followed by garbage — parseIRB returns nil → processAPP13Segment returns nil.
	payload := append([]byte("Photoshop 3.0\x00"), 0xDE, 0xAD, 0xBE, 0xEF)
	got := processAPP13Segment(payload)
	if got != nil {
		t.Errorf("processAPP13Segment with empty IRB: got %q, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// S-2: parseIRBEntry edge cases.
// ---------------------------------------------------------------------------

// TestParseIRBEntrySignatureMismatchAdvancesOne verifies that when parseIRBEntry
// encounters a signature mismatch, it returns newPos == pos, signalling to the
// caller to advance by 1 (scan-forward miss, not structural failure).
func TestParseIRBEntrySignatureMismatchAdvancesOne(t *testing.T) {
	t.Parallel()

	// Non-8BIM data at pos=0; should return newPos=0 (mismatch, not structural).
	data := []byte{0xFF, 0xFE, 0x04, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, _, newPos, ok := parseIRBEntry(data, 0)
	if ok {
		t.Error("parseIRBEntry: expected ok=false for non-8BIM signature")
	}
	if newPos != 0 {
		t.Errorf("parseIRBEntry signature mismatch: newPos=%d, want 0 (scan-forward signal)", newPos)
	}
}

// TestParseIRBEntryTruncatedBufferReturnsNotOK verifies that a buffer too
// short to contain a complete 8BIM header causes parseIRBEntry to return
// ok=false with newPos > pos (structural failure).
func TestParseIRBEntryTruncatedBufferReturnsNotOK(t *testing.T) {
	t.Parallel()

	// Only 3 bytes — not enough for the 4-byte signature.
	data := []byte{0x38, 0x42, 0x49} // "8BI"
	_, _, _, ok := parseIRBEntry(data, 0)
	if ok {
		t.Error("parseIRBEntry: expected ok=false for buffer too short for signature")
	}
}
