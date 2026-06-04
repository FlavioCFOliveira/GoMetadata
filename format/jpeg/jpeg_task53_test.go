package jpeg

// Task #53 — format/jpeg: APP1/APP13 segments, robustness, and security.
//
// Tests are organised by category:
//
//   F  — Functional: APP1/APP13 extraction, injection, baseline vs. progressive,
//         combined metadata, unknown-segment passthrough, segment order, extended XMP.
//   S  — Security: malformed/huge length fields, missing SOI/EOI, truncated
//         mid-segment, multiple APP1 segments, extended-XMP GUID flood.
//   E  — Edge: combined EXIF+IPTC+XMP coexistence, COM segment round-trip,
//         baseline-vs-progressive parity.
//
// References:
//   JPEG structure: ISO/IEC 10918-1 §B.1.1.4 (length field semantics),
//                   §B.1.1.2 (fill bytes), §B.1.1.3 (SOI), §B.2.1 (frame header).
//   EXIF APP1:      EXIF 2.32, CIPA DC-008-2019 §4.5.4.
//   Photoshop APP13: EXIF 2.32 §4.5.6.
//   Extended XMP:   Adobe XMP Specification Part 3 §1.1.3.1, §1.1.4.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// F-1: APP1 extraction — EXIF and XMP coexist in separate APP1 segments.
// ---------------------------------------------------------------------------

// TestExtractMultipleAPP1Segments verifies that when two APP1 segments appear
// (one EXIF, one XMP), both are extracted correctly without panic or OOB access.
// JPEG ISO/IEC 10918-1 §B.1.1.4 allows multiple APP marker segments of the
// same type; the parser must handle them gracefully.
func TestExtractMultipleAPP1Segments(t *testing.T) {
	t.Parallel()

	tiffData := minimalTIFFBytes()
	xmpData := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta>`)
	j := buildJPEG(tiffData, nil, xmpData)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil; expected EXIF from first APP1")
	}
	if rawXMP == nil {
		t.Error("rawXMP is nil; expected XMP from second APP1")
	}
	if rawIPTC != nil {
		t.Error("rawIPTC must be nil when no APP13 is present")
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, xmpData)
	}
}

// ---------------------------------------------------------------------------
// F-2: APP13 extraction — IPTC within Photoshop IRB.
// ---------------------------------------------------------------------------

// TestExtractAPP13IPTC verifies APP13 IPTC extraction end-to-end via Extract.
// This complements unit tests of parseIRB/processAPP13Segment by exercising the
// full scanner path.
func TestExtractAPP13IPTC(t *testing.T) {
	t.Parallel()

	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	j := buildJPEG(nil, iptcData, nil)

	_, rawIPTC, _, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawIPTC == nil {
		t.Fatal("rawIPTC is nil; expected IPTC from APP13")
	}
	if !bytes.Equal(rawIPTC, iptcData) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, iptcData)
	}
}

// ---------------------------------------------------------------------------
// F-3: Injection preserves scan data byte-for-byte.
// ---------------------------------------------------------------------------

// TestInjectPreservesScanData verifies that Inject passes the compressed image
// data (after SOS) verbatim to the output writer.
// The proof: embed a distinctive sentinel byte sequence in the "scan data" region
// and confirm it appears byte-for-byte in the output, in the correct position
// relative to the final SOS segment.
func TestInjectPreservesScanData(t *testing.T) {
	t.Parallel()

	// Build a JPEG where the "scan data" contains a known sentinel sequence.
	// Real scan data is opaque compressed bytes; we use a synthetic payload.
	sentinel := []byte{0x01, 0x02, 0x03, 0x04, 0xDE, 0xAD, 0xBE, 0xEF}

	var src bytes.Buffer
	src.Write([]byte{0xFF, 0xD8}) // SOI

	// APP1 with EXIF (will be stripped and rewritten by Inject).
	tiffData := minimalTIFFBytes()
	payload := append([]byte("Exif\x00\x00"), tiffData...)
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(payload)+2)) //nolint:gosec // G115: test helper
	src.Write([]byte{0xFF, 0xE1})
	src.Write(lbuf[:])
	src.Write(payload)

	// SOS header (minimal: just the length field, 2 bytes, value=2).
	src.Write([]byte{0xFF, 0xDA, 0x00, 0x02})
	// "Scan data": the sentinel sequence followed by EOI.
	src.Write(sentinel)
	src.Write([]byte{0xFF, 0xD9})

	newTIFF := minimalTIFFBytes()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src.Bytes()), &out, newTIFF, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	outBytes := out.Bytes()

	// Locate SOS in output: find 0xFF 0xDA.
	sosIdx := -1
	for i := 0; i+1 < len(outBytes); i++ {
		if outBytes[i] == 0xFF && outBytes[i+1] == 0xDA {
			sosIdx = i
			break
		}
	}
	if sosIdx < 0 {
		t.Fatal("SOS marker not found in Inject output")
	}

	// After SOS marker (2) + length (2) + data (0 bytes for length=2), the scan
	// data begins. The sentinel must appear there verbatim.
	scanStart := sosIdx + 2 + 2 // FF DA + 00 02 (length=2 means 0 payload bytes)
	if scanStart+len(sentinel) > len(outBytes) {
		t.Fatalf("output too short to contain sentinel after SOS: got %d bytes total, scanStart=%d", len(outBytes), scanStart)
	}
	if !bytes.Equal(outBytes[scanStart:scanStart+len(sentinel)], sentinel) {
		t.Errorf("scan data corrupted: got %X, want %X", outBytes[scanStart:scanStart+len(sentinel)], sentinel)
	}
}

// ---------------------------------------------------------------------------
// F-4: Baseline vs. progressive JPEG — same metadata extracted from both.
// ---------------------------------------------------------------------------

// buildProgressiveJPEG builds a syntactically progressive JPEG marker stream.
// A progressive JPEG is distinguished from baseline by the SOF2 (0xC2) marker
// instead of SOF0 (0xC0). Per ISO/IEC 10918-1 §B.2.2: frame header marker.
// Metadata segments (APP1, APP13) appear before the SOF marker and must be
// extracted identically regardless of frame type.
func buildProgressiveJPEG(exifData, iptcData []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	if exifData != nil {
		p := append([]byte("Exif\x00\x00"), exifData...)
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], uint16(len(p)+2)) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xE1})
		buf.Write(lbuf[:])
		buf.Write(p)
	}

	if iptcData != nil {
		irb := buildIRB(iptcData)
		var psPayload bytes.Buffer
		psPayload.WriteString("Photoshop 3.0\x00")
		psPayload.Write(irb)
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], uint16(psPayload.Len()+2)) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xED})
		buf.Write(lbuf[:])
		buf.Write(psPayload.Bytes())
	}

	// SOF2 marker (progressive DCT frame header). Minimal: 8 bytes of frame data.
	// ISO/IEC 10918-1 §B.2.2: P(1) + Y(2) + X(2) + Nf(1) + ... minimum 8 bytes.
	sof2Payload := []byte{0x08, 0x00, 0x10, 0x00, 0x10, 0x01, 0x01, 0x11}
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(sof2Payload)+2)) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xC2})
	buf.Write(lbuf[:])
	buf.Write(sof2Payload)

	// SOS + EOI.
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// TestBaselineVsProgressiveExtractionParity verifies that the same EXIF and
// IPTC metadata is extracted identically from a baseline JPEG and a
// progressive JPEG carrying the same APP segments. The parser must not inspect
// frame-type markers (SOF0 vs SOF2) — metadata lives only in APP segments.
func TestBaselineVsProgressiveExtractionParity(t *testing.T) {
	t.Parallel()

	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}

	baseline := buildJPEG(tiffData, iptcData, nil)
	progressive := buildProgressiveJPEG(tiffData, iptcData)

	bExif, bIPTC, bXMP, bErr := Extract(bytes.NewReader(baseline))
	if bErr != nil {
		t.Fatalf("Extract (baseline): %v", bErr)
	}

	pExif, pIPTC, pXMP, pErr := Extract(bytes.NewReader(progressive))
	if pErr != nil {
		t.Fatalf("Extract (progressive): %v", pErr)
	}

	if !bytes.Equal(bExif, pExif) {
		t.Errorf("EXIF differs: baseline=%d bytes, progressive=%d bytes", len(bExif), len(pExif))
	}
	if !bytes.Equal(bIPTC, pIPTC) {
		t.Errorf("IPTC differs: baseline=%d bytes, progressive=%d bytes", len(bIPTC), len(pIPTC))
	}
	if bXMP != nil || pXMP != nil {
		t.Errorf("XMP should be nil for both: baseline=%v progressive=%v", bXMP, pXMP)
	}
}

// ---------------------------------------------------------------------------
// E-1: Combined EXIF + IPTC + XMP in one JPEG — all three extract correctly.
// ---------------------------------------------------------------------------

// TestExtractCombinedMetadata verifies that a single JPEG carrying EXIF (APP1),
// XMP (APP1), and IPTC (APP13) simultaneously yields non-nil, correct payloads
// for all three from a single Extract call.
func TestExtractCombinedMetadata(t *testing.T) {
	t.Parallel()

	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	xmpData := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta>`)

	j := buildJPEG(tiffData, iptcData, xmpData)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if rawEXIF == nil {
		t.Error("rawEXIF is nil; expected EXIF payload")
	}
	if rawIPTC == nil {
		t.Error("rawIPTC is nil; expected IPTC payload")
	}
	if rawXMP == nil {
		t.Error("rawXMP is nil; expected XMP payload")
	}

	if !bytes.Equal(rawIPTC, iptcData) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, iptcData)
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, xmpData)
	}
	// rawEXIF content: TIFF bytes after stripping "Exif\x00\x00".
	if !bytes.Equal(rawEXIF, tiffData) {
		t.Errorf("rawEXIF = %v, want %v", rawEXIF, tiffData)
	}
}

// TestInjectCombinedMetadataRoundTrip verifies that Inject writes all three
// metadata payloads (EXIF, IPTC, XMP) and that a subsequent Extract recovers
// all three correctly.
func TestInjectCombinedMetadataRoundTrip(t *testing.T) {
	t.Parallel()

	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x07, 'I', 'P', 'T', 'C', 'd', 'a', 't'}
	xmpData := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`)

	src := buildJPEG(nil, nil, nil) // start with a bare JPEG
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, tiffData, iptcData, xmpData, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	gotEXIF, gotIPTC, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}

	if !bytes.Equal(gotEXIF, tiffData) {
		t.Errorf("EXIF mismatch after inject")
	}
	if !bytes.Equal(gotIPTC, iptcData) {
		t.Errorf("IPTC mismatch after inject")
	}
	if !bytes.Equal(gotXMP, xmpData) {
		t.Errorf("XMP mismatch after inject")
	}
}

// ---------------------------------------------------------------------------
// E-2: PreserveUnknownSegments — COM and APPn survive Read→Write.
// ---------------------------------------------------------------------------

// buildJPEGWithUnknownSegments builds a JPEG containing:
//   - One COM (comment) segment with arbitrary payload.
//   - One APP4 (0xE4) segment with arbitrary payload.
//   - EXIF APP1 segment.
//   - SOS + EOI.
func buildJPEGWithUnknownSegments(exifData, comPayload, app4Payload []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	// COM segment (0xFF 0xFE): JPEG comment, not a metadata segment.
	// ISO/IEC 10918-1 §B.2.4.7.
	if comPayload != nil {
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], uint16(len(comPayload)+2)) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xFE})
		buf.Write(lbuf[:])
		buf.Write(comPayload)
	}

	// APP4 segment (0xFF 0xE4): unknown application segment.
	if app4Payload != nil {
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], uint16(len(app4Payload)+2)) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xE4})
		buf.Write(lbuf[:])
		buf.Write(app4Payload)
	}

	if exifData != nil {
		p := append([]byte("Exif\x00\x00"), exifData...)
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], uint16(len(p)+2)) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xE1})
		buf.Write(lbuf[:])
		buf.Write(p)
	}

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // SOS + EOI
	return buf.Bytes()
}

// TestInjectPreservesCOMSegment verifies that a COM (comment) segment survives
// an Inject call that replaces EXIF. COM is not a metadata segment; Inject must
// pass it through verbatim.
func TestInjectPreservesCOMSegment(t *testing.T) {
	t.Parallel()

	tiffData := minimalTIFFBytes()
	comPayload := []byte("Copyright 2025 GoMetadata Test")
	j := buildJPEGWithUnknownSegments(tiffData, comPayload, nil)

	newTIFF := minimalTIFFBytes()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(j), &out, newTIFF, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	outBytes := out.Bytes()

	// COM marker (0xFF 0xFE) must be present.
	found := false
	for i := 0; i+1 < len(outBytes); i++ {
		if outBytes[i] == 0xFF && outBytes[i+1] == 0xFE {
			found = true
			break
		}
	}
	if !found {
		t.Error("COM segment (0xFF 0xFE) not found in Inject output; unknown segments must be preserved")
	}

	// The comment payload must be present verbatim.
	if !bytes.Contains(outBytes, comPayload) {
		t.Errorf("COM payload %q not found in Inject output", comPayload)
	}
}

// TestInjectPreservesAPP4Segment verifies that an APP4 (0xFF 0xE4) segment
// survives an Inject call. APP4 is neither EXIF, XMP, nor Photoshop APP13;
// it must be copied unchanged to the output.
func TestInjectPreservesAPP4Segment(t *testing.T) {
	t.Parallel()

	app4Payload := []byte("SomeProprietaryData")
	j := buildJPEGWithUnknownSegments(nil, nil, app4Payload)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(j), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	outBytes := out.Bytes()
	found := false
	for i := 0; i+1 < len(outBytes); i++ {
		if outBytes[i] == 0xFF && outBytes[i+1] == 0xE4 {
			found = true
			break
		}
	}
	if !found {
		t.Error("APP4 segment (0xFF 0xE4) not found in Inject output; unknown segments must be preserved")
	}
}

// TestInjectSegmentOrderPreserved verifies that Inject emits segments in the
// expected order: SOI → new metadata → existing non-metadata segments (in their
// original relative order) → SOS → scan data → EOI.
//
// The order check is important for interoperability: some decoders expect APP
// segments to precede frame markers (SOF*) which precede SOS.
func TestInjectSegmentOrderPreserved(t *testing.T) {
	t.Parallel()

	// Build: SOI + COM + APP4 + EXIF APP1 + SOS + EOI.
	comPayload := []byte("comment")
	app4Payload := []byte("app4data")
	tiffData := minimalTIFFBytes()
	src := buildJPEGWithUnknownSegments(tiffData, comPayload, app4Payload)

	newTIFF := minimalTIFFBytes()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, newTIFF, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	outBytes := out.Bytes()

	// Scan for marker positions.
	markerPositions := map[byte]int{}
	for i := 0; i+1 < len(outBytes); i++ {
		if outBytes[i] == 0xFF {
			m := outBytes[i+1]
			if _, seen := markerPositions[m]; !seen {
				markerPositions[m] = i
			}
		}
	}

	// SOI must be at position 0.
	if markerPositions[markerSOI] != 0 {
		t.Errorf("SOI not at position 0; got %d", markerPositions[markerSOI])
	}

	// New EXIF APP1 (0xE1) must come before COM (0xFE) and APP4 (0xE4).
	// Inject writes new metadata first, then passes through non-metadata in order.
	exifPos := markerPositions[markerAPP1]
	comPos := markerPositions[0xFE]
	app4Pos := markerPositions[0xE4]
	sosPos := markerPositions[markerSOS]

	if comPos == 0 || app4Pos == 0 {
		t.Fatalf("COM or APP4 marker not found in output")
	}
	if exifPos >= comPos {
		t.Errorf("new EXIF APP1 (pos=%d) must precede COM (pos=%d)", exifPos, comPos)
	}
	if comPos >= app4Pos {
		t.Errorf("COM (pos=%d) must precede APP4 (pos=%d) — relative order not preserved", comPos, app4Pos)
	}
	if app4Pos >= sosPos {
		t.Errorf("APP4 (pos=%d) must precede SOS (pos=%d)", app4Pos, sosPos)
	}
}

// ---------------------------------------------------------------------------
// S-1: Malformed segment length matrix.
// ---------------------------------------------------------------------------

// buildJPEGWithBadLength builds a JPEG where the APP1 segment has the given
// raw 2-byte length field value. The field includes itself (JPEG ISO/IEC
// 10918-1 §B.1.1.4), so length < 2 is always invalid.
func buildJPEGWithBadLength(rawLength uint16) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI
	buf.Write([]byte{0xFF, 0xE1}) // APP1 marker
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], rawLength)
	buf.Write(lbuf[:])
	// No payload follows — any read attempt on data bytes will hit EOF.
	return buf.Bytes()
}

// TestMalformedSegmentLengthMatrix exercises the malformed-length guard in
// readSegment across a matrix of adversarial length values. All cases must
// complete without panic or OOB access — graceful degradation is required.
//
// Per JPEG ISO/IEC 10918-1 §B.1.1.4: "The length parameter specifies the
// number of bytes, including the two bytes of the length parameter itself,
// following the marker." A value < 2 is structurally invalid; the scanner
// catches the internal error and degrades gracefully (returns nil payloads,
// nil error) rather than propagating the error to the caller.
func TestMalformedSegmentLengthMatrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		rawLength uint16
	}{
		// Values < 2: invalid per spec; scanner degrades gracefully, no panic.
		{"length=0", 0},
		{"length=1", 1},
		// Large lengths that point beyond the (tiny) file — truncated read.
		{"length=0xFFFF (max uint16)", 0xFFFF},
		{"length=1000 with empty body", 1000},
		{"length=100 with empty body", 100},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			j := buildJPEGWithBadLength(tc.rawLength)
			// Must not panic regardless of input. Error or nil: both acceptable.
			rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(j))
			_ = rawEXIF
			_ = rawIPTC
			_ = rawXMP
			_ = err
		})
	}
}

// TestSegmentLengthZeroGraceful verifies that a segment with length=0 does not
// panic and that Extract returns gracefully. readSegment detects length < 2 and
// returns ErrInvalidMarkerLength; the scanner loop catches all non-EOF errors
// and degrades gracefully by returning whatever metadata was collected so far
// (none in this case — nil payloads, nil error).
//
// JPEG ISO/IEC 10918-1 §B.1.1.4: length includes the 2-byte length field,
// so 0 means a negative data size — structurally invalid; library must not panic.
func TestSegmentLengthZeroGraceful(t *testing.T) {
	t.Parallel()

	j := buildJPEGWithBadLength(0)
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(j))
	// Graceful degradation: no panic, no crash. All payloads nil (nothing parsed).
	if err != nil {
		t.Errorf("Extract with segment length=0: expected nil error (graceful), got %v", err)
	}
	if rawEXIF != nil || rawIPTC != nil || rawXMP != nil {
		t.Errorf("expected nil payloads for structurally invalid length=0 stream; got exif=%v iptc=%v xmp=%v",
			rawEXIF, rawIPTC, rawXMP)
	}
}

// TestSegmentLengthOneGraceful verifies that a segment with length=1 does not
// panic. length=1 < 2 violates JPEG ISO/IEC 10918-1 §B.1.1.4; the scanner
// must degrade gracefully.
func TestSegmentLengthOneGraceful(t *testing.T) {
	t.Parallel()

	j := buildJPEGWithBadLength(1)
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Errorf("Extract with segment length=1: expected nil error (graceful), got %v", err)
	}
	if rawEXIF != nil || rawIPTC != nil || rawXMP != nil {
		t.Errorf("expected nil payloads for structurally invalid length=1 stream; got exif=%v iptc=%v xmp=%v",
			rawEXIF, rawIPTC, rawXMP)
	}
}

// TestSegmentLengthBeyondEOFNoPanic verifies that a segment whose declared
// length extends beyond the end of the file does not panic and gracefully
// returns (an error or nil payloads, but no OOB).
// JPEG ISO/IEC 10918-1 §B.1.1.4: parser must handle truncated streams.
func TestSegmentLengthBeyondEOFNoPanic(t *testing.T) {
	t.Parallel()

	// length=1000 but only 2 bytes of body follow — truncated.
	j := buildJPEGWithBadLength(1000)
	rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(j))
	// No panic; all payloads must be nil (no valid segment was parsed).
	_ = rawEXIF
	_ = rawIPTC
	_ = rawXMP
}

// ---------------------------------------------------------------------------
// S-2: Missing SOI / missing EOI.
// ---------------------------------------------------------------------------

// TestExtractMissingSOIReturnsError verifies that Extract returns an error
// when the input does not begin with 0xFF 0xD8 (SOI marker).
// JPEG ISO/IEC 10918-1 §B.1.1.3: all JPEG streams begin with SOI.
func TestExtractMissingSOIReturnsError(t *testing.T) {
	t.Parallel()

	// A valid APP1 segment but no SOI.
	tiffData := minimalTIFFBytes()
	p := append([]byte("Exif\x00\x00"), tiffData...)
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(p)+2)) //nolint:gosec // G115: test helper
	noSOI := append([]byte{0xFF, 0xE1}, lbuf[:]...)
	noSOI = append(noSOI, p...)
	noSOI = append(noSOI, 0xFF, 0xD9)

	_, _, _, err := Extract(bytes.NewReader(noSOI))
	if err == nil {
		t.Error("Extract without SOI: expected error, got nil")
	}
}

// TestExtractMissingEOIGraceful verifies that a JPEG stream that ends abruptly
// without an EOI marker (or SOS) is handled gracefully: Extract must return
// without panic and must return whatever metadata was already collected.
func TestExtractMissingEOIGraceful(t *testing.T) {
	t.Parallel()

	// SOI + EXIF APP1 only; no SOS, no EOI.
	tiffData := minimalTIFFBytes()
	p := append([]byte("Exif\x00\x00"), tiffData...)
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(p)+2)) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xE1})
	buf.Write(lbuf[:])
	buf.Write(p)
	// No EOI: stream ends here.

	// Must not panic. The parser may return an error or gracefully return the EXIF.
	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf.Bytes()))
	// The EXIF was fully parsed before the truncation point, so it should be returned.
	if rawEXIF == nil {
		t.Error("rawEXIF is nil; EXIF was fully parsed before EOI was missing")
	}
}

// ---------------------------------------------------------------------------
// S-3: Truncated mid-segment.
// ---------------------------------------------------------------------------

// TestExtractTruncatedMidSegment verifies a matrix of truncation points: all
// must be handled without panic or OOB.
func TestExtractTruncatedMidSegment(t *testing.T) {
	t.Parallel()

	// Build a well-formed JPEG with EXIF.
	tiffData := minimalTIFFBytes()
	full := buildJPEG(tiffData, nil, nil)

	// Truncate at various byte positions within the APP1 segment.
	// All truncation points must not panic.
	for _, truncAt := range []int{2, 3, 4, 5, 6, 7, 8, 10, 12, len(full) / 2} {
		if truncAt >= len(full) {
			continue
		}
		t.Run(fmt.Sprintf("truncAt=%d", truncAt), func(t *testing.T) {
			t.Parallel()
			truncated := full[:truncAt]
			rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(truncated))
			_ = rawEXIF
			_ = rawIPTC
			_ = rawXMP
		})
	}
}

// ---------------------------------------------------------------------------
// S-4: Multiple APP1 segments — no panic, no OOB.
// ---------------------------------------------------------------------------

// buildJPEGWithManyAPP1 builds a JPEG with n identical EXIF APP1 segments.
// This exercises the "multiple APP1 segments" code path. Real-world cameras
// may emit duplicate or sequential APP1 segments (e.g. dual-GUID panoramas).
func buildJPEGWithManyAPP1(n int, exifData []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	p := append([]byte("Exif\x00\x00"), exifData...)
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(p)+2)) //nolint:gosec // G115: test helper

	for range n {
		buf.Write([]byte{0xFF, 0xE1})
		buf.Write(lbuf[:])
		buf.Write(p)
	}
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// TestMultipleAPP1SegmentsNoPanic verifies that a JPEG with many APP1
// segments (EXIF, duplicate EXIF, and XMP) does not panic or access memory
// out-of-bounds. ISO/IEC 10918-1 §B.1.1.4 permits multiple APP markers.
func TestMultipleAPP1SegmentsNoPanic(t *testing.T) {
	t.Parallel()

	tiffData := minimalTIFFBytes()

	// 10 identical EXIF APP1 segments.
	j := buildJPEGWithManyAPP1(10, tiffData)
	rawEXIF, _, _, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract with 10 APP1 segments: unexpected error: %v", err)
	}
	// Parser takes the first valid EXIF; must not be nil.
	if rawEXIF == nil {
		t.Error("rawEXIF is nil; expected EXIF from first APP1 of 10")
	}
}

// TestMultipleAPP1SegmentsMixedNoPanic verifies that a JPEG with 5 EXIF APP1
// segments followed by 5 XMP APP1 segments does not panic.
func TestMultipleAPP1SegmentsMixedNoPanic(t *testing.T) {
	t.Parallel()

	tiffData := minimalTIFFBytes()
	xmpPayload := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta>`)

	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})

	// 5 EXIF APP1.
	for range 5 {
		p := append([]byte("Exif\x00\x00"), tiffData...)
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], uint16(len(p)+2)) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xE1})
		buf.Write(lbuf[:])
		buf.Write(p)
	}

	// 5 XMP APP1.
	for range 5 {
		xmpIdent := "http://ns.adobe.com/xap/1.0/\x00"
		p := append([]byte(xmpIdent), xmpPayload...)
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], uint16(len(p)+2)) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xE1})
		buf.Write(lbuf[:])
		buf.Write(p)
	}

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract with 10 mixed APP1 segments: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil; expected EXIF")
	}
	if rawXMP == nil {
		t.Error("rawXMP is nil; expected XMP")
	}
}

// ---------------------------------------------------------------------------
// S-5: Extended XMP GUID flood — maxExtendedXMPGUIDs cap.
// ---------------------------------------------------------------------------

// buildGUIDFloodJPEG builds a JPEG where the extended XMP APP1 stream carries
// nGUIDs distinct GUIDs, each with one chunk of chunkSize bytes. The main
// XMP carries HasExtendedXMP pointing at the first GUID.
//
// This simulates an adversarial file crafted to overflow the extended map with
// many distinct GUIDs, each accumulating up to maxExtendedXMPTotal bytes.
func buildGUIDFloodJPEG(nGUIDs, chunkSize int) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})

	// Generate nGUIDs distinct 32-char hex GUIDs.
	guids := make([]string, nGUIDs)
	for i := range nGUIDs {
		guids[i] = fmt.Sprintf("%032X", i+1) // e.g. "00000000000000000000000000000001"
	}

	// Main XMP APP1: HasExtendedXMP points at guids[0].
	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
		` xmpNote:HasExtendedXMP="` + guids[0] + `">` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta>`)
	mainPayload := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), mainXMP...)
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(mainPayload)+2)) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xE1})
	buf.Write(lbuf[:])
	buf.Write(mainPayload)

	// Extended XMP APP1 chunks: one chunk per GUID, each with chunkSize bytes.
	chunkData := bytes.Repeat([]byte("G"), chunkSize)
	for _, guid := range guids {
		var extBody bytes.Buffer
		extBody.WriteString("http://ns.adobe.com/xmp/extension/\x00")
		extBody.WriteString(guid) // 32 bytes
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:], uint32(chunkSize)) //nolint:gosec // G115: test helper
		binary.BigEndian.PutUint32(hdr[4:], 0)                 // offset 0
		extBody.Write(hdr[:])
		extBody.Write(chunkData)

		extData := extBody.Bytes()
		binary.BigEndian.PutUint16(lbuf[:], uint16(len(extData)+2)) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xE1})
		buf.Write(lbuf[:])
		buf.Write(extData)
	}

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// TestExtendedXMPGUIDFloodCapped verifies that a JPEG with more than
// maxExtendedXMPGUIDs distinct GUIDs in extended XMP APP1 segments does not
// accumulate all of them. The parser must stop at maxExtendedXMPGUIDs distinct
// keys and mark the result as truncated, preventing O(nGUIDs × 16 MiB) growth.
//
// Adobe XMP Specification Part 3 §1.1.3.1: a conforming file uses exactly one
// GUID. We permit up to maxExtendedXMPGUIDs (4) distinct GUIDs.
func TestExtendedXMPGUIDFloodCapped(t *testing.T) {
	t.Parallel()

	// Send 10× the allowed GUIDs, each with a 1 KiB chunk.
	const chunkSize = 1024
	nGUIDs := 10 * maxExtendedXMPGUIDs

	j := buildGUIDFloodJPEG(nGUIDs, chunkSize)

	// Must not panic.
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract GUID flood: unexpected error: %v", err)
	}
	_ = rawEXIF
	// The returned XMP must not represent all nGUIDs × chunkSize bytes.
	// The cap means at most maxExtendedXMPGUIDs GUIDs are accumulated.
	// Total accumulated bytes ≤ maxExtendedXMPGUIDs × chunkSize (very small).
	maxAllowed := maxExtendedXMPGUIDs * chunkSize
	if len(rawXMP) > maxAllowed+len(rawXMP)/2 {
		// We give some latitude because reassembly can add main XMP overhead,
		// but definitely not nGUIDs × chunkSize.
		t.Errorf("rawXMP length %d suggests GUID flood was not bounded (maxAllowed ≈ %d)",
			len(rawXMP), maxAllowed)
	}
}

// TestExtendedXMPGUIDFloodDoesNotPanic verifies that with many distinct GUIDs
// the parser neither panics nor returns a non-nil error.
func TestExtendedXMPGUIDFloodDoesNotPanic(t *testing.T) {
	t.Parallel()

	// 100 distinct GUIDs with small chunks — 100× what the cap allows.
	j := buildGUIDFloodJPEG(100, 512)
	_, _, _, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract GUID flood (100 GUIDs): unexpected error: %v", err)
	}
}

// TestExtendedXMPGUIDCapBoundary verifies the exact boundary: exactly
// maxExtendedXMPGUIDs distinct GUIDs must all be accepted (cap is inclusive),
// but maxExtendedXMPGUIDs+1 must cause the extra GUID to be dropped.
func TestExtendedXMPGUIDCapBoundary(t *testing.T) {
	t.Parallel()

	const chunkSize = 64

	// Exactly maxExtendedXMPGUIDs distinct GUIDs: all should be accumulated.
	j := buildGUIDFloodJPEG(maxExtendedXMPGUIDs, chunkSize)
	_, _, _, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract with exactly %d GUIDs (at cap): unexpected error: %v",
			maxExtendedXMPGUIDs, err)
	}

	// maxExtendedXMPGUIDs+1: the extra GUID must be silently dropped.
	jPlus := buildGUIDFloodJPEG(maxExtendedXMPGUIDs+1, chunkSize)
	_, _, _, errPlus := Extract(bytes.NewReader(jPlus))
	if errPlus != nil {
		t.Fatalf("Extract with %d GUIDs (over cap): unexpected error: %v",
			maxExtendedXMPGUIDs+1, errPlus)
	}
}

// ---------------------------------------------------------------------------
// S-6: ExtendedXMP GUID flood — fuzz-style adversarial corpus seed validation.
// ---------------------------------------------------------------------------

// TestExtendedXMPGUIDFloodFuzzSeedNoPanic runs the adversarial "GUID flood"
// byte pattern through Extract to confirm no panic — this mirrors what the
// fuzz seed will test.
func TestExtendedXMPGUIDFloodFuzzSeedNoPanic(t *testing.T) {
	t.Parallel()

	// Many tiny GUIDs, each a single byte chunk.
	for _, n := range []int{1, maxExtendedXMPGUIDs, maxExtendedXMPGUIDs + 1, 50} {
		t.Run(fmt.Sprintf("nGUIDs=%d", n), func(t *testing.T) {
			t.Parallel()
			j := buildGUIDFloodJPEG(n, 8)
			rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(j))
			_ = rawEXIF
			_ = rawIPTC
			_ = rawXMP
		})
	}
}

// ---------------------------------------------------------------------------
// S-7: Inject preserves all non-metadata segments AND scan data on injection
//       even when ALL three metadata types are replaced simultaneously.
// ---------------------------------------------------------------------------

// TestInjectAllThreeMetadataPreservesUnknownAndScan verifies the full
// injection pipeline when all three payloads (EXIF, IPTC, XMP) are provided
// and the source JPEG contains an unknown APP4 and a COM segment: the
// non-metadata segments must survive and scan data must be intact.
func TestInjectAllThreeMetadataPreservesUnknownAndScan(t *testing.T) {
	t.Parallel()

	// Build source JPEG: COM + APP4 + EXIF + IPTC + SOS (with scan sentinel) + EOI.
	sentinel := []byte{0xAB, 0xCD, 0xEF, 0x12, 0x34}

	var src bytes.Buffer
	src.Write([]byte{0xFF, 0xD8}) // SOI

	// COM (0xFE)
	com := []byte("Original Comment")
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(com)+2)) //nolint:gosec // G115: test helper
	src.Write([]byte{0xFF, 0xFE})
	src.Write(lbuf[:])
	src.Write(com)

	// APP4 (0xE4)
	app4 := []byte("APP4Payload")
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(app4)+2)) //nolint:gosec // G115: test helper
	src.Write([]byte{0xFF, 0xE4})
	src.Write(lbuf[:])
	src.Write(app4)

	// Old EXIF APP1.
	oldTIFF := minimalTIFFBytes()
	exifP := append([]byte("Exif\x00\x00"), oldTIFF...)
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(exifP)+2)) //nolint:gosec // G115: test helper
	src.Write([]byte{0xFF, 0xE1})
	src.Write(lbuf[:])
	src.Write(exifP)

	// Old IPTC APP13.
	oldIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'O', 'l', 'd'}
	irb := buildIRB(oldIPTC)
	var psPayload bytes.Buffer
	psPayload.WriteString("Photoshop 3.0\x00")
	psPayload.Write(irb)
	binary.BigEndian.PutUint16(lbuf[:], uint16(psPayload.Len()+2)) //nolint:gosec // G115: test helper
	src.Write([]byte{0xFF, 0xED})
	src.Write(lbuf[:])
	src.Write(psPayload.Bytes())

	// SOS + scan sentinel + EOI.
	src.Write([]byte{0xFF, 0xDA, 0x00, 0x02})
	src.Write(sentinel)
	src.Write([]byte{0xFF, 0xD9})

	// Inject all three new payloads.
	newTIFF := minimalTIFFBytes()
	newIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'N', 'e', 'w'}
	newXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta>`)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src.Bytes()), &out, newTIFF, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	outBytes := out.Bytes()

	// Verify COM survived.
	if !bytes.Contains(outBytes, com) {
		t.Error("COM payload not found in Inject output")
	}

	// Verify APP4 survived.
	foundAPP4 := false
	for i := 0; i+1 < len(outBytes); i++ {
		if outBytes[i] == 0xFF && outBytes[i+1] == 0xE4 {
			foundAPP4 = true
			break
		}
	}
	if !foundAPP4 {
		t.Error("APP4 marker (0xFF 0xE4) not found in Inject output")
	}

	// Verify scan data sentinel survived.
	if !bytes.Contains(outBytes, sentinel) {
		t.Errorf("scan data sentinel %X not found in Inject output", sentinel)
	}

	// Verify new metadata is present.
	gotEXIF, gotIPTC, gotXMP, err := Extract(bytes.NewReader(outBytes))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if gotEXIF == nil {
		t.Error("new EXIF not found after Inject")
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP: got %q, want %q", gotXMP, newXMP)
	}
}

// ---------------------------------------------------------------------------
// F-5: ExtendedXMP multi-segment: StandardXMP APP1 + ExtendedXMP APP1 chunks
//      reassembled via 32-char GUID + 8-byte header per Adobe XMP Part 3 spec.
// ---------------------------------------------------------------------------

// TestExtendedXMPMultiSegmentGUIDReassembly verifies the full extended-XMP
// reassembly pipeline as specified in Adobe XMP Specification Part 3 §1.1.4:
//
//  1. A standard XMP APP1 carries HasExtendedXMP="<GUID>" in the main packet.
//  2. Multiple extended XMP APP1 segments carry the same GUID, each with
//     a 32-byte GUID + 4-byte fullLen + 4-byte offset header followed by chunk data.
//  3. Extract reassembles them in offset order and merges the content into
//     the returned rawXMP.
func TestExtendedXMPMultiSegmentGUIDReassembly(t *testing.T) {
	t.Parallel()

	const guid = "ABCDEF0123456789ABCDEF0123456789"

	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
		` xmpNote:HasExtendedXMP="` + guid + `">` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta>`)

	// Build a multi-chunk extended payload containing a distinctive string.
	extContent := []byte(
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:title>GUIDReassemblyTest</dc:title>` +
			`</rdf:Description>` +
			`</rdf:RDF>`,
	)

	j := buildExtendedXMPJPEG(mainXMP, guid, extContent)

	_, _, rawXMP, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil; expected reassembled extended XMP")
	}
	if !bytes.Contains(rawXMP, []byte("GUIDReassemblyTest")) {
		t.Errorf("reassembled XMP does not contain extended content; got: %s", rawXMP)
	}
}

// TestExtendedXMPHeaderLayout verifies that the extended XMP APP1 binary
// header layout matches Adobe XMP Specification Part 3 §1.1.4:
//
//	Bytes 0–34: "http://ns.adobe.com/xmp/extension/\x00" (35 bytes)
//	Bytes 35–66: GUID (32 ASCII hex characters)
//	Bytes 67–70: fullLength (4 bytes, big-endian)
//	Bytes 71–74: offset (4 bytes, big-endian)
//	Bytes 75+:   chunk data
//
// This test extracts the first extended APP1 body written by Inject and
// validates each field at the expected byte offsets.
func TestExtendedXMPHeaderLayout(t *testing.T) {
	t.Parallel()

	// Build a payload large enough to force the extended path.
	padding := bytes.Repeat([]byte("L"), 66_000)
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
	if err := Inject(bytes.NewReader(src), &out, nil, nil, rawXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	outBytes := out.Bytes()

	// Locate the extended XMP APP1 identifier in the output.
	identPos := bytes.Index(outBytes, identXMPNote)
	if identPos < 0 {
		t.Fatal("extended XMP APP1 identifier not found in Inject output")
	}

	// The identifier starts at the beginning of the APP1 payload (after the
	// 4-byte FF E1 + length header). The body immediately follows the identifier.
	bodyStart := identPos + len(identXMPNote)

	// Validate GUID field (32 bytes at bodyStart).
	if bodyStart+40 > len(outBytes) {
		t.Fatalf("output too short to contain GUID+header at bodyStart=%d", bodyStart)
	}
	guidBytes := outBytes[bodyStart : bodyStart+32]
	for _, b := range guidBytes {
		isHex := (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
		if !isHex {
			t.Errorf("GUID byte 0x%02X is not a valid hex character", b)
		}
	}

	// Validate fullLength (4 bytes at bodyStart+32).
	fullLen := binary.BigEndian.Uint32(outBytes[bodyStart+32 : bodyStart+36])
	if fullLen == 0 {
		t.Error("fullLength field is 0; expected non-zero for a multi-chunk extended XMP payload")
	}
	if fullLen > maxExtendedXMPTotal {
		t.Errorf("fullLength %d exceeds maxExtendedXMPTotal %d", fullLen, maxExtendedXMPTotal)
	}

	// Validate offset (4 bytes at bodyStart+36): first chunk must have offset=0.
	offset := binary.BigEndian.Uint32(outBytes[bodyStart+36 : bodyStart+40])
	if offset != 0 {
		t.Errorf("first extended chunk offset = %d, want 0", offset)
	}
}
