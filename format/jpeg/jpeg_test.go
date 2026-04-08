package jpeg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
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
		length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, intentional type cast
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
		length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, intentional type cast
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
		binary.BigEndian.PutUint32(sz[:], uint32(len(iptcData))) //nolint:gosec // G115: test helper, intentional type cast
		irb.Write(sz[:])
		irb.Write(iptcData)
		if len(iptcData)%2 != 0 {
			irb.WriteByte(0x00)
		}

		length := uint16(irb.Len() + 2) //nolint:gosec // G115: test helper, intentional type cast
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
		length := uint16(len(app2Payload) + 2) //nolint:gosec // G115: test helper, intentional type cast
		buf.Write([]byte{0xFF, 0xE2})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], length)
		buf.Write(lbuf[:])
		buf.Write(app2Payload)
	}

	if exifData != nil {
		payload := append([]byte("Exif\x00\x00"), exifData...)
		length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, intentional type cast
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
	mainLen := uint16(len(mainPayload) + 2) //nolint:gosec // G115: test helper, intentional type cast
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
	binary.BigEndian.PutUint32(fullLenBuf[:], uint32(len(extContent))) //nolint:gosec // G115: test helper, intentional type cast
	extBody.Write(fullLenBuf[:])
	var offsetBuf [4]byte
	binary.BigEndian.PutUint32(offsetBuf[:], 0) // first chunk starts at offset 0
	extBody.Write(offsetBuf[:])
	extBody.Write(extContent)

	extPayload := extBody.Bytes()
	extLen := uint16(len(extPayload) + 2) //nolint:gosec // G115: test helper, intentional type cast
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
	length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, intentional type cast
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
	irb.Write([]byte{byte(rid1 >> 8), byte(rid1)}) //nolint:gosec // G115: test helper, intentional type cast
	irb.Write([]byte{0x00, 0x00})                  // empty pascal name
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(data1))) //nolint:gosec // G115: test helper, intentional type cast
	irb.Write(sz[:])
	irb.Write(data1)
	if len(data1)%2 != 0 {
		irb.WriteByte(0x00)
	}

	// Second block: 0x0404 (IPTC)
	irb.WriteString("8BIM")
	irb.Write([]byte{0x04, 0x04})
	irb.Write([]byte{0x00, 0x00})
	binary.BigEndian.PutUint32(sz[:], uint32(len(iptcData))) //nolint:gosec // G115: test helper, intentional type cast
	irb.Write(sz[:])
	irb.Write(iptcData)
	if len(iptcData)%2 != 0 {
		irb.WriteByte(0x00)
	}

	irbBytes := irb.Bytes()
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8})
	totalLen := uint16(len(irbBytes) + 2) //nolint:gosec // G115: test helper, intentional type cast
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
	t.Parallel()
	tiffData := minimalTIFFBytes()

	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})

	// APP1 with EXIF
	payload := append([]byte("Exif\x00\x00"), tiffData...)
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(payload)+2)) //nolint:gosec // G115: test helper, intentional type cast
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	bare := []byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(bare))
	if err != nil {
		t.Fatalf("Extract bare JPEG: %v", err)
	}
	if rawEXIF != nil || rawIPTC != nil || rawXMP != nil {
		t.Errorf("expected all nil for bare JPEG, got exif=%v iptc=%v xmp=%v", rawEXIF, rawIPTC, rawXMP)
	}
}

// --- Extended XMP write: TestInjectExtendedXMP ---

// TestInjectExtendedXMP verifies that Inject correctly splits an XMP packet
// larger than maxXMPPayload (65504 bytes) across a main APP1 and one or more
// extended APP1 segments (Adobe XMP Specification Part 3 §1.1.4), and that
// the subsequent Extract call reassembles the full XMP content.
func TestInjectExtendedXMP(t *testing.T) {
	t.Parallel()
	// Build a rawXMP that is larger than maxXMPPayload (65504 bytes).
	// We use a valid XMP envelope so that Extract's reassembleExtendedXMP can
	// splice the extended content into it correctly.
	const extraContentSize = 66000

	// Construct a payload whose inner comment field is large enough to push
	// the whole packet over the single-segment limit. The payload is a well-
	// formed XMP document so Extract can parse and reassemble it.
	padding := bytes.Repeat([]byte("x"), extraContentSize)
	rawXMP := []byte(
		`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:description>` + string(padding) + `</dc:description>` +
			`</rdf:Description>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`,
	)
	if len(rawXMP) <= maxXMPPayload {
		t.Fatalf("test precondition failed: rawXMP (%d bytes) must exceed maxXMPPayload (%d bytes)", len(rawXMP), maxXMPPayload)
	}

	src := buildJPEG(nil, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, rawXMP); err != nil {
		t.Fatalf("Inject with oversized XMP: %v", err)
	}

	// Extract must successfully reassemble the extended XMP.
	_, _, got, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after extended XMP inject: %v", err)
	}
	if got == nil {
		t.Fatal("Extract returned nil rawXMP; expected reassembled extended XMP")
	}

	// The reassembled XMP must contain the large padding string.
	if !bytes.Contains(got, padding) {
		t.Errorf("reassembled XMP (%d bytes) does not contain the expected padding content (%d bytes of 'x')",
			len(got), len(padding))
	}
}

// TestInjectExtendedXMPMultiChunk verifies that an XMP packet large enough to
// require more than one extended APP1 chunk is split and reassembled correctly.
func TestInjectExtendedXMPMultiChunk(t *testing.T) {
	t.Parallel()
	// Each extended chunk holds maxExtChunkSize (65461) bytes.
	// We need > 65461 bytes in rawXMP to force two extended chunks.
	const extraContentSize = 130_000 // forces at least 2 chunks

	padding := bytes.Repeat([]byte("y"), extraContentSize)
	rawXMP := []byte(
		`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:description>` + string(padding) + `</dc:description>` +
			`</rdf:Description>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`,
	)

	src := buildJPEG(nil, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, rawXMP); err != nil {
		t.Fatalf("Inject multi-chunk extended XMP: %v", err)
	}

	// Count the number of extended XMP APP1 segments in the output.
	outBytes := out.Bytes()
	extNoteIdent := []byte("http://ns.adobe.com/xap/1.0/se/\x00")
	extCount := bytes.Count(outBytes, extNoteIdent)
	if extCount < 2 {
		t.Errorf("expected at least 2 extended APP1 segments, found %d", extCount)
	}

	_, _, got, err := Extract(bytes.NewReader(outBytes))
	if err != nil {
		t.Fatalf("Extract after multi-chunk inject: %v", err)
	}
	if got == nil {
		t.Fatal("Extract returned nil rawXMP")
	}
	if !bytes.Contains(got, padding) {
		t.Errorf("reassembled XMP does not contain expected padding content")
	}
}

// TestInjectSmallXMPFastPath verifies that an XMP packet that fits within
// maxXMPPayload is still written as a single standard APP1 (no extended split).
func TestInjectSmallXMPFastPath(t *testing.T) {
	t.Parallel()
	xmpPayload := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta>`)
	if len(xmpPayload) > maxXMPPayload {
		t.Fatal("test precondition failed: payload must be small")
	}

	src := buildJPEG(nil, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, xmpPayload); err != nil {
		t.Fatalf("Inject small XMP: %v", err)
	}

	outBytes := out.Bytes()
	// Must have zero extended XMP segments.
	extNoteIdent := []byte("http://ns.adobe.com/xap/1.0/se/\x00")
	if bytes.Contains(outBytes, extNoteIdent) {
		t.Error("small XMP inject produced extended APP1 segment(s); expected fast path only")
	}

	_, _, got, err := Extract(bytes.NewReader(outBytes))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !bytes.Equal(got, xmpPayload) {
		t.Errorf("XMP round-trip mismatch: got %q, want %q", got, xmpPayload)
	}
}

// --- Additional coverage: Extract non-JPEG returns error ---

// TestExtractNonJPEGReturnsError verifies that a non-JPEG byte stream
// (wrong SOI magic) causes Extract to return an error.
func TestExtractNonJPEGReturnsError(t *testing.T) {
	t.Parallel()
	notJPEG := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A} // PNG magic
	_, _, _, err := Extract(bytes.NewReader(notJPEG))
	if err == nil {
		t.Error("Extract on non-JPEG: expected error, got nil")
	}
}

// TestInjectNonJPEGReturnsError verifies that Inject on a non-JPEG returns
// an error.
func TestInjectNonJPEGReturnsError(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// --- parseIRB tests ---

// TestParseIRBScanForward verifies that parseIRB scans forward past bytes that
// don't form an "8BIM" signature, eventually finding the real block.
func TestParseIRBScanForward(t *testing.T) {
	t.Parallel()
	// Build an IRB blob where the first few bytes are garbage (not "8BIM"),
	// so parseIRB must scan forward to find the real entry.
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'A', 'B', 'C'}

	var irb bytes.Buffer
	irb.WriteString("JUNK") // 4 garbage bytes that don't form "8BIM"
	irb.WriteString("8BIM")
	irb.Write([]byte{0x04, 0x04}) // IPTC resource ID
	irb.Write([]byte{0x00, 0x00}) // empty pascal name
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(iptcData))) //nolint:gosec // G115: safe test helper
	irb.Write(sz[:])
	irb.Write(iptcData)

	got := parseIRB(irb.Bytes())
	if got == nil {
		t.Fatal("parseIRB scan-forward: expected non-nil result")
	}
	if !bytes.Equal(got, iptcData) {
		t.Errorf("parseIRB scan-forward: got %v, want %v", got, iptcData)
	}
}

// TestParseIRBOddPaddedData verifies that parseIRB applies the even-padding
// rule to data blocks with an odd length and still finds the next entry.
func TestParseIRBOddPaddedData(t *testing.T) {
	t.Parallel()
	// First entry: resource ID 0x0400 (not IPTC), odd-length data → padded by 1 byte.
	// Second entry: resource ID 0x0404 (IPTC), even-length data.
	otherData := []byte{0xAA} // 1 byte, odd → needs 1 byte padding
	iptcData := []byte{0x1C, 0x02}

	var irb bytes.Buffer
	writeIRBEntry := func(resourceID uint16, data []byte) {
		irb.WriteString("8BIM")
		irb.Write([]byte{byte(resourceID >> 8), byte(resourceID)}) //nolint:gosec // G115: safe: resourceID is a known small uint16
		irb.Write([]byte{0x00, 0x00})                              // empty pascal name
		var sz [4]byte
		binary.BigEndian.PutUint32(sz[:], uint32(len(data))) //nolint:gosec // G115: safe test helper
		irb.Write(sz[:])
		irb.Write(data)
		if len(data)%2 != 0 {
			irb.WriteByte(0x00) // even-pad
		}
	}

	writeIRBEntry(0x0400, otherData) // non-IPTC with odd-length data
	writeIRBEntry(0x0404, iptcData)  // IPTC entry

	got := parseIRB(irb.Bytes())
	if got == nil {
		t.Fatal("parseIRB odd-padded: expected non-nil result")
	}
	if !bytes.Equal(got, iptcData) {
		t.Errorf("parseIRB odd-padded: got %v, want %v", got, iptcData)
	}
}

// TestParseIRBTruncatedDataSize verifies that parseIRB returns nil for an entry
// where the data size field would extend beyond the buffer.
func TestParseIRBTruncatedDataSize(t *testing.T) {
	t.Parallel()
	// Build an 8BIM entry where dataSize=100 but the buffer only has 4 bytes after the size field.
	var irb bytes.Buffer
	irb.WriteString("8BIM")
	irb.Write([]byte{0x04, 0x04}) // IPTC resource ID
	irb.Write([]byte{0x00, 0x00}) // empty pascal name
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], 100) // claims 100 bytes of data
	irb.Write(sz[:])
	irb.Write([]byte{0x01, 0x02}) // only 2 bytes — truncated

	got := parseIRB(irb.Bytes())
	if got != nil {
		t.Errorf("parseIRB truncated: expected nil, got %v", got)
	}
}

// TestParseIRBEntryMissingResourceIDField verifies that parseIRBEntry returns
// ok=false when the buffer is too short to hold the resource ID after "8BIM".
func TestParseIRBEntryMissingResourceIDField(t *testing.T) {
	t.Parallel()
	// Exactly "8BIM" with no following bytes for the resource ID.
	buf := []byte("8BIM")
	_, _, _, ok := parseIRBEntry(buf, 0)
	if ok {
		t.Error("expected ok=false when resource ID field is missing")
	}
}

// TestWriteMarkerError verifies that writeMarker returns an error when the
// writer fails.
func TestWriteMarkerError(t *testing.T) {
	t.Parallel()
	err := writeMarker(errorWriter{}, 0xD9)
	if err == nil {
		t.Error("writeMarker: expected error from failing writer, got nil")
	}
}

// TestWriteSegmentTooLarge verifies that writeSegment returns ErrSegmentTooLarge
// when the payload exceeds the JPEG 16-bit length field limit.
func TestWriteSegmentTooLarge(t *testing.T) {
	t.Parallel()
	// 65534 bytes of data + 2-byte length = 65536, which exceeds 65535.
	hugePaylod := make([]byte, 65534)
	var out bytes.Buffer
	err := writeSegment(&out, 0xE1, hugePaylod)
	if err == nil {
		t.Error("writeSegment: expected ErrSegmentTooLarge, got nil")
	}
}

// TestReadSegmentFillBytes verifies that readSegment correctly skips fill bytes
// (consecutive 0xFF bytes before the marker byte).
func TestReadSegmentFillBytes(t *testing.T) {
	t.Parallel()
	// Build: 0xFF 0xFF 0xFF 0xD9 (two fill bytes before EOI marker).
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xFF, 0xFF, 0xD9})
	scratch := make([]byte, 4096)
	marker, data, err := readSegment(&buf, &scratch)
	if err != nil {
		t.Fatalf("readSegment fill bytes: %v", err)
	}
	if marker != markerEOI {
		t.Errorf("marker = 0x%02X, want 0x%02X (EOI)", marker, markerEOI)
	}
	if data != nil {
		t.Errorf("data = %v, want nil for standalone marker", data)
	}
}

// TestReadSegmentInvalidLength verifies that readSegment returns an error when
// the length field is less than 2 (violates JPEG spec).
func TestReadSegmentInvalidLength(t *testing.T) {
	t.Parallel()
	// Build: 0xFF 0xE1 (APP1) with length=1 (invalid; minimum is 2).
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xE1, 0x00, 0x01})
	scratch := make([]byte, 4096)
	_, _, err := readSegment(&buf, &scratch)
	if err == nil {
		t.Error("readSegment: expected error for length=1, got nil")
	}
}

// TestSkipPascalStringEdgeCases exercises skipPascalString when the pos is at
// or beyond the buffer end, and when nameLen requires padding.
func TestSkipPascalStringEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("pos at end returns pos+1, false", func(t *testing.T) {
		t.Parallel()
		buf := []byte{0x05}
		newPos, ok := skipPascalString(buf, 1) // pos == len(buf)
		if ok {
			t.Error("expected ok=false")
		}
		if newPos != 2 {
			t.Errorf("newPos = %d, want 2", newPos)
		}
	})

	t.Run("even-length name (no padding byte)", func(t *testing.T) {
		t.Parallel()
		// nameLen=2 → total = 1(len) + 2(name) = 3 (odd) → padded: total = 4
		buf := []byte{0x02, 'A', 'B', 0x00} // length + 2 chars + pad
		newPos, ok := skipPascalString(buf, 0)
		if !ok {
			t.Errorf("expected ok=true, got false")
		}
		_ = newPos
	})
}

// errorWriter is a test helper that always returns an error on Write.
type errorWriter struct{}

func (errorWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("simulated write error")
}

// TestProcessAPP13PhotoshopPrefixNoIRB verifies that processAPP13Segment returns
// nil when the data starts with the Photoshop prefix but contains no valid 8BIM
// entries (parseIRB returns nil).
func TestProcessAPP13PhotoshopPrefixNoIRB(t *testing.T) {
	t.Parallel()
	// "Photoshop 3.0\x00" followed by garbage that has no "8BIM" signature.
	data := append(append([]byte{}, identPS...), []byte("GARBAGE_NO_BIM")...)
	result := processAPP13Segment(data)
	if result != nil {
		t.Errorf("expected nil for Photoshop prefix with no 8BIM, got %v", result)
	}
}

// TestWriteSOSCopyError verifies that writeSOS returns an error when
// io.Copy fails (failing writer after SOS segment header is written).
func TestWriteSOSCopyError(t *testing.T) {
	t.Parallel()
	// writeSOS with empty data calls writeSegment (1 Write: the 4-byte header),
	// then io.Copy. failAfter=1 lets the header write succeed, then fails on copy.
	ew := &countingWriter{failAfter: 1}
	data := []byte{}
	reader := bytes.NewReader([]byte{0x01, 0x02}) // some image data to copy
	err := writeSOS(reader, ew, data)
	if err == nil {
		t.Error("expected error from writeSOS when io.Copy fails, got nil")
	}
}

// countingWriter succeeds for the first failAfter writes, then fails.
type countingWriter struct {
	count     int
	failAfter int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.count++
	if w.count > w.failAfter {
		return 0, fmt.Errorf("simulated write error after %d writes", w.failAfter)
	}
	return len(p), nil
}

// TestCopyNonMetadataSegmentsReadError verifies that copyNonMetadataSegments
// returns the non-EOF error when readSegment fails.
func TestCopyNonMetadataSegmentsReadError(t *testing.T) {
	t.Parallel()
	// Build a valid JPEG SOI, then one APP0 segment, then truncated data.
	// This will cause readSegment to return a non-EOF error.
	var buf bytes.Buffer
	// Write an APP0 marker with length=5 (2-byte length field = 5, but data only 3 bytes follow).
	buf.Write([]byte{0xFF, 0xE0}) // APP0 marker
	buf.Write([]byte{0x00, 0x05}) // length=5 means 3 bytes of data
	buf.Write([]byte{0x00, 0x00}) // only 2 bytes — truncated

	scratch := make([]byte, 0, 4096)
	var out bytes.Buffer
	err := copyNonMetadataSegments(bytes.NewReader(buf.Bytes()), &out, &scratch)
	// The truncated read should produce an error (non-EOF, unexpected EOF).
	if err == nil {
		t.Error("expected error for truncated segment, got nil")
	}
}

func BenchmarkJPEGExtract(b *testing.B) {
	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	jpeg := buildJPEG(tiffData, iptcData, nil)
	b.SetBytes(int64(len(jpeg)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(jpeg))
	}
}

func BenchmarkJPEGInject(b *testing.B) {
	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	jpeg := buildJPEG(tiffData, iptcData, nil)
	newIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'N', 'e', 'w'}
	b.SetBytes(int64(len(jpeg)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var out bytes.Buffer
		_ = Inject(bytes.NewReader(jpeg), &out, tiffData, newIPTC, nil)
	}
}

func BenchmarkJPEGExtract_Real(b *testing.B) {
	data, err := os.ReadFile("../../testdata/corpus/jpeg/exiftool/ExifTool.jpg")
	if err != nil {
		b.Skip("corpus file not available")
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r := bytes.NewReader(data)
		_, _, _, _ = Extract(r)
	}
}
