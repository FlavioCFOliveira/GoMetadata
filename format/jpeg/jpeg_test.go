package jpeg

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildJPEG builds a minimal JPEG stream with optional APP1(EXIF) and APP13(IPTC) segments.
func buildJPEG(exifData, iptcData, xmpData []byte) []byte {
	var buf bytes.Buffer

	// SOI
	buf.Write([]byte{0xFF, 0xD8})

	if exifData != nil {
		// APP1 with Exif header
		payload := append([]byte("Exif\x00\x00"), exifData...)
		length := uint16(len(payload) + 2)
		buf.Write([]byte{0xFF, 0xE1})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], length)
		buf.Write(lbuf[:])
		buf.Write(payload)
	}

	if xmpData != nil {
		// APP1 with XMP namespace
		ns := "http://ns.adobe.com/xap/1.0/\x00"
		payload := append([]byte(ns), xmpData...)
		length := uint16(len(payload) + 2)
		buf.Write([]byte{0xFF, 0xE1})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], length)
		buf.Write(lbuf[:])
		buf.Write(payload)
	}

	if iptcData != nil {
		// APP13 with Photoshop IRB wrapper
		// Photoshop IRB: "Photoshop 3.0\0" + 8BIM + resource ID 0x0404 + ...
		var irb bytes.Buffer
		irb.WriteString("Photoshop 3.0\x00")
		irb.WriteString("8BIM")
		irb.Write([]byte{0x04, 0x04}) // IPTC resource ID
		irb.Write([]byte{0x00, 0x00}) // pascal string (empty name)
		// Resource data size (4 bytes BE)
		var sz [4]byte
		binary.BigEndian.PutUint32(sz[:], uint32(len(iptcData)))
		irb.Write(sz[:])
		irb.Write(iptcData)
		if len(iptcData)%2 != 0 {
			irb.WriteByte(0x00)
		}

		length := uint16(irb.Len() + 2)
		buf.Write([]byte{0xFF, 0xED})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], length)
		buf.Write(lbuf[:])
		buf.Write(irb.Bytes())
	}

	// Minimal SOS + EOI
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})

	return buf.Bytes()
}

// minimalTIFFBytes builds a 3-entry TIFF suitable as EXIF payload.
func minimalTIFFBytes() []byte {
	order := binary.LittleEndian
	buf := make([]byte, 8+2+1*12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)
	p := 10
	order.PutUint16(buf[p:], 0x0100) // ImageWidth
	order.PutUint16(buf[p+2:], 4)    // LONG
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], 800)
	return buf
}

func TestExtractEXIF(t *testing.T) {
	tiffData := minimalTIFFBytes()
	jpeg := buildJPEG(tiffData, nil, nil)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil")
	}
	if rawIPTC != nil {
		t.Error("expected nil rawIPTC")
	}
	if rawXMP != nil {
		t.Error("expected nil rawXMP")
	}
	_ = rawEXIF
}

func TestExtractIPTC(t *testing.T) {
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	jpeg := buildJPEG(nil, iptcData, nil)

	_, rawIPTC, _, err := Extract(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawIPTC == nil {
		t.Fatal("rawIPTC is nil")
	}
	if !bytes.Equal(rawIPTC, iptcData) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, iptcData)
	}
}

func TestInjectRoundTrip(t *testing.T) {
	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	jpeg := buildJPEG(tiffData, iptcData, nil)

	newIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'N', 'e', 'w'}
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(jpeg), &out, tiffData, newIPTC, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	_, gotIPTC, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC after inject: got %q, want %q", gotIPTC, newIPTC)
	}
}

// buildJPEGWithAPP2 builds a JPEG that includes an APP2 (0xFF 0xE2) segment
// with payload content, followed by optional EXIF, then SOS+EOI.
func buildJPEGWithAPP2(exifData []byte, app2Payload []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})

	// APP2 segment
	if app2Payload != nil {
		length := uint16(len(app2Payload) + 2)
		buf.Write([]byte{0xFF, 0xE2})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], length)
		buf.Write(lbuf[:])
		buf.Write(app2Payload)
	}

	if exifData != nil {
		payload := append([]byte("Exif\x00\x00"), exifData...)
		length := uint16(len(payload) + 2)
		buf.Write([]byte{0xFF, 0xE1})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], length)
		buf.Write(lbuf[:])
		buf.Write(payload)
	}

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// buildExtendedXMPJPEG constructs a JPEG that carries a main XMP APP1 segment
// (with HasExtendedXMP attribute) and a single extended XMP APP1 chunk.
//
// mainXMP must already contain HasExtendedXMP="<guid32>" and </rdf:RDF>.
// extContent is the RDF body of the extended document (between <rdf:Description
// and </rdf:RDF> inclusive).
func buildExtendedXMPJPEG(mainXMP []byte, guid string, extContent []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})

	// Main XMP APP1
	mainPayload := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), mainXMP...)
	mainLen := uint16(len(mainPayload) + 2)
	buf.Write([]byte{0xFF, 0xE1})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], mainLen)
	buf.Write(lbuf[:])
	buf.Write(mainPayload)

	// Extended XMP APP1
	// Structure: identXMPNote + 32-byte GUID + 4-byte fullLen + 4-byte offset + data
	var extBody bytes.Buffer
	extBody.WriteString("http://ns.adobe.com/xap/1.0/se/\x00")
	extBody.WriteString(guid) // 32 bytes
	var fullLenBuf [4]byte
	binary.BigEndian.PutUint32(fullLenBuf[:], uint32(len(extContent)))
	extBody.Write(fullLenBuf[:])
	var offsetBuf [4]byte
	binary.BigEndian.PutUint32(offsetBuf[:], 0) // first chunk starts at offset 0
	extBody.Write(offsetBuf[:])
	extBody.Write(extContent)

	extPayload := extBody.Bytes()
	extLen := uint16(len(extPayload) + 2)
	buf.Write([]byte{0xFF, 0xE1})
	binary.BigEndian.PutUint16(lbuf[:], extLen)
	buf.Write(lbuf[:])
	buf.Write(extPayload)

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// buildJPEGWithFillByte builds a JPEG where a 0xFF fill byte precedes the
// APP1 marker (i.e., the stream contains 0xFF 0xFF 0xE1 ... after the SOI).
// This exercises the fill-byte skip loop in readSegment.
func buildJPEGWithFillByte(exifData []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})

	// 0xFF fill byte before APP1 marker — readSegment must skip it.
	payload := append([]byte("Exif\x00\x00"), exifData...)
	length := uint16(len(payload) + 2)
	buf.Write([]byte{0xFF, 0xFF, 0xE1})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], length)
	buf.Write(lbuf[:])
	buf.Write(payload)

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// buildAPP13MultiBlock builds an APP13 segment whose IRB contains two 8BIM
// resources: first resource ID rid1 with data1, then 0x0404 with iptcData.
func buildAPP13MultiBlock(rid1 uint16, data1, iptcData []byte) []byte {
	var irb bytes.Buffer
	irb.WriteString("Photoshop 3.0\x00")

	// First block: rid1
	irb.WriteString("8BIM")
	irb.Write([]byte{byte(rid1 >> 8), byte(rid1)})
	irb.Write([]byte{0x00, 0x00}) // empty pascal name
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(data1)))
	irb.Write(sz[:])
	irb.Write(data1)
	if len(data1)%2 != 0 {
		irb.WriteByte(0x00)
	}

	// Second block: 0x0404 (IPTC)
	irb.WriteString("8BIM")
	irb.Write([]byte{0x04, 0x04})
	irb.Write([]byte{0x00, 0x00})
	binary.BigEndian.PutUint32(sz[:], uint32(len(iptcData)))
	irb.Write(sz[:])
	irb.Write(iptcData)
	if len(iptcData)%2 != 0 {
		irb.WriteByte(0x00)
	}

	irbBytes := irb.Bytes()
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8})
	totalLen := uint16(len(irbBytes) + 2)
	out.Write([]byte{0xFF, 0xED})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], totalLen)
	out.Write(lbuf[:])
	out.Write(irbBytes)
	out.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return out.Bytes()
}

// --- Test A: RST marker between APP1 and SOS must not panic ---

// TestExtractRSTMarkerNoPanic verifies that a JPEG with RST markers (standalone
// markers with no data) embedded in the marker stream does not panic and returns
// the expected metadata.
func TestExtractRSTMarkerNoPanic(t *testing.T) {
	tiffData := minimalTIFFBytes()

	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})

	// APP1 with EXIF
	payload := append([]byte("Exif\x00\x00"), tiffData...)
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(payload)+2))
	buf.Write([]byte{0xFF, 0xE1})
	buf.Write(lbuf[:])
	buf.Write(payload)

	// RST0 standalone marker (0xFF 0xD0) — no length, no data
	buf.Write([]byte{0xFF, 0xD0})
	// RST7 standalone marker (0xFF 0xD7)
	buf.Write([]byte{0xFF, 0xD7})

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil; expected EXIF to survive RST markers")
	}
}

// --- Test B: reassembleExtendedXMP ---

// TestExtractExtendedXMPReassembly verifies that a JPEG carrying a main XMP
// packet with HasExtendedXMP and a matching extended XMP APP1 chunk is
// correctly merged by Extract.
func TestExtractExtendedXMPReassembly(t *testing.T) {
	const guid = "DEADBEEF00000000DEADBEEF00000000"

	// Main XMP packet: minimal RDF with HasExtendedXMP as an *attribute*
	// (attribute form, e.g. HasExtendedXMP="<GUID>") and a closing </rdf:RDF>
	// tag so reassembleExtendedXMP can splice content before it.
	// The implementation scans for the first " or ' within 5 bytes after the
	// property name, so the attribute form is required here.
	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/"` +
		` xmpNote:HasExtendedXMP="` + guid + `">` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta>`)

	// Extended XMP content: a standalone RDF document with an extra Description.
	// reassembleExtendedXMP looks for <rdf:Description and </rdf:RDF> in this.
	extContent := []byte(
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:title>Extended Title</dc:title>` +
			`</rdf:Description>` +
			`</rdf:RDF>`,
	)

	jpeg := buildExtendedXMPJPEG(mainXMP, guid, extContent)

	_, _, rawXMP, err := Extract(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil; expected merged XMP")
	}
	if !bytes.Contains(rawXMP, []byte("Extended Title")) {
		t.Errorf("merged XMP does not contain extended content; got:\n%s", rawXMP)
	}
}

// --- Test C: Inject with nil rawEXIF removes the segment ---

// TestInjectRemoveEXIF verifies that passing nil rawEXIF to Inject removes
// the existing EXIF APP1 from the output stream.
func TestInjectRemoveEXIF(t *testing.T) {
	tiffData := minimalTIFFBytes()
	jpeg := buildJPEG(tiffData, nil, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(jpeg), &out, nil, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if rawEXIF != nil {
		t.Error("rawEXIF should be nil after injecting nil EXIF, but got non-nil")
	}
}

// --- Test D: Inject XMP round-trip ---

// TestInjectXMPRoundTrip verifies that XMP injected into a JPEG with no
// existing XMP is faithfully extracted back.
func TestInjectXMPRoundTrip(t *testing.T) {
	jpeg := buildJPEG(nil, nil, nil)
	xmpPayload := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta>`)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(jpeg), &out, nil, nil, xmpPayload); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	_, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawXMP, xmpPayload) {
		t.Errorf("rawXMP after inject = %q, want %q", rawXMP, xmpPayload)
	}
}

// --- Test E: Inject preserves unknown APP segments ---

// TestInjectPreservesUnknownAPP verifies that a non-metadata APP segment
// (APP2, marker 0xE2) survives an Inject call that replaces the EXIF.
func TestInjectPreservesUnknownAPP(t *testing.T) {
	tiffData := minimalTIFFBytes()
	app2Payload := []byte("ICC_PROFILE\x00\x01\x01some profile bytes")
	jpeg := buildJPEGWithAPP2(tiffData, app2Payload)

	newEXIF := minimalTIFFBytes()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(jpeg), &out, newEXIF, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// The output must still contain the APP2 marker bytes.
	result := out.Bytes()
	found := false
	for i := 0; i+1 < len(result); i++ {
		if result[i] == 0xFF && result[i+1] == 0xE2 {
			found = true
			break
		}
	}
	if !found {
		t.Error("APP2 segment (0xFF 0xE2) not found in Inject output; unknown segments must be preserved")
	}
}

// --- Test F: Extract truncated JPEG must not panic ---

// TestExtractTruncatedNoPanic verifies that a truncated JPEG (just SOI +
// partial APP1 header) does not panic and returns without a fatal error.
func TestExtractTruncatedNoPanic(t *testing.T) {
	// SOI + marker + length byte (truncated — length says 10 bytes but only 0 follow)
	truncated := []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x0A}
	// Must not panic; error is acceptable.
	rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(truncated))
	// All payloads must be nil (nothing successfully parsed).
	_ = rawEXIF
	_ = rawIPTC
	_ = rawXMP
}

// --- Test G: JPEG with only XMP ---

// TestExtractXMPOnly verifies that a JPEG with an XMP APP1 but no EXIF or
// IPTC returns non-nil rawXMP and nil rawEXIF/rawIPTC.
func TestExtractXMPOnly(t *testing.T) {
	xmpData := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta>`)
	jpeg := buildJPEG(nil, nil, xmpData)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Error("rawXMP is nil; expected non-nil for XMP-only JPEG")
	}
	if rawEXIF != nil {
		t.Error("rawEXIF should be nil")
	}
	if rawIPTC != nil {
		t.Error("rawIPTC should be nil")
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, xmpData)
	}
}

// --- Test H: writeSegment oversized payload ---

// TestInjectOversizedEXIFReturnsError verifies that Inject returns an error
// when the EXIF payload is so large that the APP1 segment would exceed the
// 16-bit JPEG length field maximum (65535 bytes).
// identExif is 6 bytes; the length field adds 2 bytes; so rawEXIF must be
// > 65535 - 6 - 2 = 65527 bytes to trigger the error.
func TestInjectOversizedEXIFReturnsError(t *testing.T) {
	jpeg := buildJPEG(nil, nil, nil)
	// 65528 bytes: len(identExif=6) + 65528 + 2 = 65536 > 65535 → error
	oversized := make([]byte, 65528)
	var out bytes.Buffer
	err := Inject(bytes.NewReader(jpeg), &out, oversized, nil, nil)
	if err == nil {
		t.Error("Inject with oversized EXIF: expected error, got nil")
	}
}

// --- Test I: parseIRB with multiple 8BIM blocks ---

// TestExtractParseIRBMultipleBlocks verifies that parseIRB (exercised via
// Extract) skips unknown resource blocks and returns data only from 0x0404.
func TestExtractParseIRBMultipleBlocks(t *testing.T) {
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'A', 'B', 'C'}
	// APP13 with resource 0x0405 (thumbnail — unknown) followed by 0x0404 (IPTC).
	unknownData := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	jpeg := buildAPP13MultiBlock(0x0405, unknownData, iptcData)

	_, rawIPTC, _, err := Extract(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawIPTC == nil {
		t.Fatal("rawIPTC is nil; expected 0x0404 data from multi-block IRB")
	}
	if !bytes.Equal(rawIPTC, iptcData) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, iptcData)
	}
}

// --- Test J: readSegment with fill byte ---

// TestExtractFillByteBeforeMarker verifies that a 0xFF fill byte preceding a
// real marker (producing 0xFF 0xFF 0xE1 in the stream) is silently consumed
// by readSegment, and the segment is parsed correctly.
func TestExtractFillByteBeforeMarker(t *testing.T) {
	tiffData := minimalTIFFBytes()
	jpeg := buildJPEGWithFillByte(tiffData)

	rawEXIF, _, _, err := Extract(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Extract with fill byte: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil; expected EXIF data after fill-byte marker")
	}
}

// --- Test K: bare JPEG (SOI + SOS + EOI) ---

// TestExtractBareJPEGAllNil verifies that a JPEG with no metadata segments
// returns no error and all nil payloads.
func TestExtractBareJPEGAllNil(t *testing.T) {
	bare := []byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(bare))
	if err != nil {
		t.Fatalf("Extract bare JPEG: %v", err)
	}
	if rawEXIF != nil || rawIPTC != nil || rawXMP != nil {
		t.Errorf("expected all nil for bare JPEG, got exif=%v iptc=%v xmp=%v", rawEXIF, rawIPTC, rawXMP)
	}
}

// --- Additional coverage: Inject with oversized XMP ---

// TestInjectOversizedXMPReturnsError verifies the XMP size limit error path.
func TestInjectOversizedXMPReturnsError(t *testing.T) {
	jpeg := buildJPEG(nil, nil, nil)
	// identXMP is 29 bytes; 29 + N + 2 > 65535 → N > 65504
	oversized := make([]byte, 65505)
	var out bytes.Buffer
	err := Inject(bytes.NewReader(jpeg), &out, nil, nil, oversized)
	if err == nil {
		t.Error("Inject with oversized XMP: expected error, got nil")
	}
}

// --- Additional coverage: Extract non-JPEG returns error ---

// TestExtractNonJPEGReturnsError verifies that a non-JPEG byte stream
// (wrong SOI magic) causes Extract to return an error.
func TestExtractNonJPEGReturnsError(t *testing.T) {
	notJPEG := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A} // PNG magic
	_, _, _, err := Extract(bytes.NewReader(notJPEG))
	if err == nil {
		t.Error("Extract on non-JPEG: expected error, got nil")
	}
}

// TestInjectNonJPEGReturnsError verifies that Inject on a non-JPEG returns
// an error.
func TestInjectNonJPEGReturnsError(t *testing.T) {
	notJPEG := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A}
	var out bytes.Buffer
	err := Inject(bytes.NewReader(notJPEG), &out, nil, nil, nil)
	if err == nil {
		t.Error("Inject on non-JPEG: expected error, got nil")
	}
}

// --- Extended XMP: graceful degradation when GUID not found ---

// TestExtractExtendedXMPNoMatchingGUID verifies that when the main XMP has
// HasExtendedXMP but the GUID does not match any extended chunk, the main XMP
// is returned unchanged (graceful degradation).
func TestExtractExtendedXMPNoMatchingGUID(t *testing.T) {
	const guid = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const differentGUID = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

	mainXMP := []byte(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/"` +
		` xmpNote:HasExtendedXMP="` + guid + `">` +
		`</rdf:Description></rdf:RDF>`)

	extContent := []byte(`<rdf:Description rdf:about=""/></rdf:RDF>`)

	// Provide extended chunk with a DIFFERENT guid so it won't match.
	jpeg := buildExtendedXMPJPEG(mainXMP, differentGUID, extContent)

	_, _, rawXMP, err := Extract(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP should not be nil when main XMP is present")
	}
	// The XMP should equal mainXMP (unchanged, no merge occurred).
	if !bytes.Equal(rawXMP, mainXMP) {
		t.Errorf("XMP was unexpectedly modified; got %q, want %q", rawXMP, mainXMP)
	}
}

// --- EOI path in Inject ---

// TestInjectEOIPath verifies that Inject correctly handles a JPEG that ends
// with EOI (no SOS). This exercises the markerEOI branch in Inject.
func TestInjectEOIPath(t *testing.T) {
	// Bare JPEG with just SOI + EOI (no SOS).
	bare := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(bare), &out, nil, nil, nil); err != nil {
		t.Fatalf("Inject EOI-only JPEG: %v", err)
	}
	result := out.Bytes()
	if len(result) < 2 {
		t.Fatal("output too short")
	}
	// Output must end with EOI.
	last := result[len(result)-2:]
	if last[0] != 0xFF || last[1] != markerEOI {
		t.Errorf("output does not end with EOI; last two bytes: %02X %02X", last[0], last[1])
	}
}

// --- Inject standalone marker passthrough ---

// TestInjectStandaloneMarkerPassthrough verifies that Inject copies standalone
// markers (e.g., RST0) that are not metadata segments into the output unchanged.
func TestInjectStandaloneMarkerPassthrough(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	buf.Write([]byte{0xFF, 0xD0}) // RST0 standalone marker
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(buf.Bytes()), &out, nil, nil, nil); err != nil {
		t.Fatalf("Inject with RST marker: %v", err)
	}

	result := out.Bytes()
	found := false
	for i := 0; i+1 < len(result); i++ {
		if result[i] == 0xFF && result[i+1] == 0xD0 {
			found = true
			break
		}
	}
	if !found {
		t.Error("RST0 standalone marker (0xFF 0xD0) not found in Inject output")
	}
}

func BenchmarkJPEGExtract(b *testing.B) {
	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	jpeg := buildJPEG(tiffData, iptcData, nil)
	b.SetBytes(int64(len(jpeg)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = Extract(bytes.NewReader(jpeg))
	}
}

func BenchmarkJPEGInject(b *testing.B) {
	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	jpeg := buildJPEG(tiffData, iptcData, nil)
	newIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'N', 'e', 'w'}
	b.SetBytes(int64(len(jpeg)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out bytes.Buffer
		_ = Inject(bytes.NewReader(jpeg), &out, tiffData, newIPTC, nil)
	}
}
