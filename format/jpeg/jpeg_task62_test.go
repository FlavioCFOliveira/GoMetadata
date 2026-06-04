package jpeg

// Task #62 — regression test: JPEG write must preserve non-IPTC 8BIM resources.
//
// Root cause (task #62): writeIPTCSegment rebuilt the Photoshop IRB with only a
// bare 0x0404 block, destroying every other 8BIM resource in the original APP13
// (e.g. IPTC digest 0x0425, Photoshop thumbnail 0x040C, ICC clipping path 0x040F).
//
// Correction: Inject pre-scans the source JPEG to capture the full original IRB,
// then spliceIPTCIntoIRB replaces only the 0x0404 block while copying all other
// 8BIM blocks verbatim.

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildMultiBlockAPP13 builds a complete APP13 segment payload (including the
// "Photoshop 3.0\x00" header) containing three 8BIM blocks:
//
//   - 0x0404 (IPTC-NAA)         with iptcData
//   - 0x0425 (IPTC digest)       with digestData
//   - 0x040C (Photoshop thumbnail) with thumbData
//
// The blocks are written in that order. Even-padding is applied per EXIF §4.5.6.
func buildMultiBlockAPP13(iptcData, digestData, thumbData []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("Photoshop 3.0\x00")
	for _, block := range []struct {
		id   uint16
		data []byte
	}{
		{0x0404, iptcData},
		{0x0425, digestData},
		{0x040C, thumbData},
	} {
		buf.WriteString("8BIM")
		buf.WriteByte(byte(block.id >> 8))
		buf.WriteByte(byte(block.id)) //nolint:gosec // G115: test helper
		buf.WriteByte(0x00)           // pascal name: length 0
		buf.WriteByte(0x00)           // pascal name: even-padding byte
		var sz [4]byte
		binary.BigEndian.PutUint32(sz[:], uint32(len(block.data))) //nolint:gosec // G115: test helper
		buf.Write(sz[:])
		buf.Write(block.data)
		if len(block.data)%2 != 0 {
			buf.WriteByte(0x00) // even-padding per EXIF §4.5.6
		}
	}
	return buf.Bytes()
}

// buildJPEGWithRawAPP13 builds a minimal JPEG stream embedding a pre-built
// APP13 payload (the caller provides the entire segment body including the
// "Photoshop 3.0\x00" header).
func buildJPEGWithRawAPP13(app13Payload []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	length := uint16(len(app13Payload) + 2) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xED})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], length)
	buf.Write(lbuf[:])
	buf.Write(app13Payload)

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // SOS+EOI
	return buf.Bytes()
}

// parseAPP13ResourceIDs scans a raw APP13 payload (including the "Photoshop
// 3.0\x00" header) and returns the ordered list of 8BIM resource IDs found.
func parseAPP13ResourceIDs(app13Payload []byte) []uint16 {
	if !bytes.HasPrefix(app13Payload, identPS) {
		return nil
	}
	irb := app13Payload[len(identPS):]
	var ids []uint16
	pos := 0
	for pos < len(irb) {
		resourceID, data, newPos, ok := parseIRBEntry(irb, pos)
		if !ok {
			if newPos == pos {
				pos++
				continue
			}
			break
		}
		ids = append(ids, resourceID)
		blockEnd := newPos
		if len(data)%2 != 0 {
			blockEnd++
		}
		pos = blockEnd
	}
	return ids
}

// parseAPP13ResourceData scans a raw APP13 payload and returns the data bytes
// for the given resource ID, or nil if not found.
func parseAPP13ResourceData(app13Payload []byte, targetID uint16) []byte {
	if !bytes.HasPrefix(app13Payload, identPS) {
		return nil
	}
	irb := app13Payload[len(identPS):]
	pos := 0
	for pos < len(irb) {
		resourceID, data, newPos, ok := parseIRBEntry(irb, pos)
		if !ok {
			if newPos == pos {
				pos++
				continue
			}
			break
		}
		blockEnd := newPos
		if len(data)%2 != 0 {
			blockEnd++
		}
		if resourceID == targetID {
			return data
		}
		pos = blockEnd
	}
	return nil
}

// extractAPP13Payload scans the output JPEG bytes and returns the APP13 segment
// payload (everything after the FF ED length field), or nil if absent.
func extractAPP13Payload(jpegBytes []byte) []byte {
	r := bytes.NewReader(jpegBytes)
	scratch := make([]byte, 4096)
	scratchPtr := &scratch

	// Skip SOI.
	soi := make([]byte, 2)
	if _, err := r.Read(soi); err != nil {
		return nil
	}

	for {
		marker, data, err := readSegment(r, scratchPtr)
		if err != nil {
			return nil
		}
		if marker == markerAPP13 {
			out := make([]byte, len(data))
			copy(out, data)
			return out
		}
		if marker == markerSOS || marker == markerEOI {
			return nil
		}
	}
}

// TestInjectPreservesNonIPTC8BIM is the mandatory regression gate for task #62.
//
// It crafts an APP13 with three 8BIM blocks (0x0404 IPTC, 0x0425 digest,
// 0x040C thumbnail), calls Inject with the raw IPTC IIM bytes unchanged, then
// parses the output APP13 and asserts that all three resource IDs are present
// with byte-identical data.
//
// REGRESSION GATE: this test MUST fail before the fix and pass after.
func TestInjectPreservesNonIPTC8BIM(t *testing.T) {
	t.Parallel()

	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	digestData := []byte{0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
		0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89} // 16-byte digest
	thumbData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01} // JPEG thumbnail stub (even length)

	// Build source JPEG with multi-block APP13.
	app13Payload := buildMultiBlockAPP13(iptcData, digestData, thumbData)
	srcJPEG := buildJPEGWithRawAPP13(app13Payload)

	// Inject with unchanged rawIPTC (same IIM bytes).
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(srcJPEG), &out, nil, iptcData, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// Extract the APP13 payload from the output JPEG.
	outAPP13 := extractAPP13Payload(out.Bytes())
	if outAPP13 == nil {
		t.Fatal("output JPEG contains no APP13 segment")
	}

	// All three resource IDs must be present.
	ids := parseAPP13ResourceIDs(outAPP13)
	wantIDs := []uint16{0x0404, 0x0425, 0x040C}
	for _, want := range wantIDs {
		found := false
		for _, got := range ids {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("resource ID 0x%04X missing from output APP13; present IDs: %#v", want, ids)
		}
	}

	// Data for each resource must be byte-identical.
	checkData := func(id uint16, wantData []byte) {
		t.Helper()
		got := parseAPP13ResourceData(outAPP13, id)
		if !bytes.Equal(got, wantData) {
			t.Errorf("resource 0x%04X data mismatch: got %q, want %q", id, got, wantData)
		}
	}
	checkData(0x0404, iptcData)
	checkData(0x0425, digestData)
	checkData(0x040C, thumbData)
}

// TestInjectPreservesNonIPTC8BIMOrderPreserved verifies that sibling 8BIM
// resources retain their original order after splice (not just presence).
func TestInjectPreservesNonIPTC8BIMOrderPreserved(t *testing.T) {
	t.Parallel()

	iptcData := []byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 'f', 'o', 'o'}
	digestData := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	thumbData := []byte{0x01, 0x02, 0x03, 0x04}

	app13Payload := buildMultiBlockAPP13(iptcData, digestData, thumbData)
	srcJPEG := buildJPEGWithRawAPP13(app13Payload)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(srcJPEG), &out, nil, iptcData, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	outAPP13 := extractAPP13Payload(out.Bytes())
	if outAPP13 == nil {
		t.Fatal("output JPEG contains no APP13 segment")
	}

	ids := parseAPP13ResourceIDs(outAPP13)
	// Expected order: 0x0404, then 0x0425, then 0x040C (splice preserves order).
	wantOrder := []uint16{0x0404, 0x0425, 0x040C}
	if len(ids) != len(wantOrder) {
		t.Fatalf("resource count: got %d (%#v), want %d (%#v)", len(ids), ids, len(wantOrder), wantOrder)
	}
	for i, want := range wantOrder {
		if ids[i] != want {
			t.Errorf("resource[%d]: got 0x%04X, want 0x%04X", i, ids[i], want)
		}
	}
}

// TestSpliceIPTCIntoIRBNoOriginal verifies that spliceIPTCIntoIRB with a nil
// origIRB falls back to buildIRB behaviour (bare 0x0404-only IRB).
func TestSpliceIPTCIntoIRBNoOriginal(t *testing.T) {
	t.Parallel()

	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x04, 'f', 'o', 'o', 'b'}
	// writeIPTCSegment is called with nil origIRB → must produce a valid 0x0404 IRB.
	result := spliceIPTCIntoIRB(nil, iptcData)
	// The result should be parseable and contain exactly the 0x0404 block.
	got := parseIRB(result)
	if !bytes.Equal(got, iptcData) {
		t.Errorf("spliceIPTCIntoIRB(nil, data): got %q, want %q", got, iptcData)
	}
}

// TestSpliceIPTCIntoIRBReplacesExisting verifies that when origIRB already
// contains a 0x0404 block, it is replaced with the new data, not duplicated.
func TestSpliceIPTCIntoIRBReplacesExisting(t *testing.T) {
	t.Parallel()

	oldIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'o', 'l', 'd'}
	newIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'n', 'e', 'w'}

	// Build an IRB with only 0x0404.
	origIRB := buildIRB(oldIPTC)
	result := spliceIPTCIntoIRB(origIRB, newIPTC)

	got := parseIRB(result)
	if !bytes.Equal(got, newIPTC) {
		t.Errorf("spliceIPTCIntoIRB: got %q, want %q", got, newIPTC)
	}

	// Must not contain the old data.
	if bytes.Contains(result, oldIPTC) {
		t.Error("spliceIPTCIntoIRB: old IPTC data still present after replacement")
	}

	// Must contain exactly one 0x0404 block.
	count := 0
	pos := 0
	for pos < len(result) {
		resourceID, data, newPos, ok := parseIRBEntry(result, pos)
		if !ok {
			if newPos == pos {
				pos++
				continue
			}
			break
		}
		blockEnd := newPos
		if len(data)%2 != 0 {
			blockEnd++
		}
		if resourceID == 0x0404 {
			count++
		}
		pos = blockEnd
	}
	if count != 1 {
		t.Errorf("spliceIPTCIntoIRB: found %d 0x0404 blocks, want exactly 1", count)
	}
}

// TestInjectNoAPP13SourceStillWritesIPTC verifies that when the source JPEG has
// no APP13 segment, Inject still correctly writes a new APP13 with the IPTC data.
func TestInjectNoAPP13SourceStillWritesIPTC(t *testing.T) {
	t.Parallel()

	// A JPEG with no APP13 at all.
	srcJPEG := buildJPEG(nil, nil, nil)
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(srcJPEG), &out, nil, iptcData, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	outAPP13 := extractAPP13Payload(out.Bytes())
	if outAPP13 == nil {
		t.Fatal("output JPEG contains no APP13 segment after Inject with IPTC data")
	}

	got := parseAPP13ResourceData(outAPP13, 0x0404)
	if !bytes.Equal(got, iptcData) {
		t.Errorf("0x0404 data: got %q, want %q", got, iptcData)
	}
}
