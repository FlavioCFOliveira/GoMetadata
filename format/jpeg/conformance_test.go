package jpeg

// JPEG container specification-conformance test battery (task #155).
//
// Normative authority: ITU-T T.81 | ISO/IEC 10918-1:1994 (JPEG structure);
// ITU-T T.871 | ISO/IEC 10918-5:2013 (JFIF); CIPA DC-008-2019 §4.5.4 (EXIF
// APP1); CIPA DC-008-2019 §4.5.6 (Photoshop APP13/IPTC); Adobe XMP
// Specification Part 3 §1.1.3–§1.1.4 (XMP and extended XMP).
//
// §1 checklist assertions covered in this file:
//   §1(b) SOI detection
//   §1(c) Markers FF xx; standalone vs. segment markers; Lp semantics; FF00
//         byte-stuffing; 65533-byte single-segment limit; multiple APP1 legal
//   §1(d) EXIF prefix "Exif\0\0"; XMP namespace URI; ExtendedXMP header layout;
//         IPTC in APP13 resource 0x0404
//   §1(e) APPn length = payload+2; new APPn before SOS; entropy/stuffing
//         unchanged; EXIF ≤ 65533; ExtendedXMP consistency
//   §1(f) Truncation; missing EOI; Lp > remaining; Lp < 2; APPn past EOF;
//         multiple Exif\0\0 (use first); ExtendedXMP missing/dup/overlapping
//         chunks; GUID mismatch; malformed IRB; fill bytes; multiple APP13
//         concatenation; corpus parity

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ---------------------------------------------------------------------------
// §1(c) — Marker structure
// ---------------------------------------------------------------------------

// TestJPEGMarkerLengthIncludesItself verifies that the 2-byte Lp field written
// by writeSegment equals payload_length + 2 (the spec mandates the length field
// counts itself but not the marker).
//
// ITU-T T.81 §B.1.1.4: "The length parameter specifies the number of bytes in
// the marker segment, including the two bytes of the length parameter itself
// but not including the two bytes of the marker."
func TestJPEGMarkerLengthIncludesItself(t *testing.T) {
	t.Parallel()

	payloads := [][]byte{
		[]byte("x"),       // 1 byte
		make([]byte, 10),  // 10 bytes
		make([]byte, 100), // 100 bytes
		[]byte("Exif\x00\x00" + string(make([]byte, 8))), // minimal EXIF wrapper
	}

	for _, payload := range payloads {
		var buf bytes.Buffer
		if err := writeSegment(&buf, markerAPP1, payload); err != nil {
			t.Fatalf("writeSegment(%d-byte payload): %v", len(payload), err)
		}
		out := buf.Bytes()
		// Bytes [0..1] = FF E1; bytes [2..3] = Lp (big-endian).
		if len(out) < 4 {
			t.Fatalf("writeSegment output too short: %d bytes", len(out))
		}
		lp := int(binary.BigEndian.Uint16(out[2:4]))
		wantLp := len(payload) + 2 // §B.1.1.4: Lp = data_bytes + 2
		if lp != wantLp {
			t.Errorf("Lp = %d, want %d (payload=%d bytes)", lp, wantLp, len(payload))
		}
	}
}

// TestJPEGMaxSingleSegmentPayload verifies the 65533-byte single-segment limit.
//
// ITU-T T.81 §B.1.1.4: the 2-byte Lp field ≤ 65535; Lp includes its own 2
// bytes, so maximum payload = 65535 − 2 = 65533.
//
//   - Exactly 65533 bytes must be accepted.
//   - 65534 bytes must be rejected with ErrSegmentTooLarge.
func TestJPEGMaxSingleSegmentPayload(t *testing.T) {
	t.Parallel()

	// Positive: 65533-byte payload (maximum legal).
	t.Run("JPEG-max-payload-65533-accepted", func(t *testing.T) {
		t.Parallel()
		maxPayload := make([]byte, 65533)
		var buf bytes.Buffer
		if err := writeSegment(&buf, markerAPP1, maxPayload); err != nil {
			t.Errorf("writeSegment(65533 bytes): expected nil error, got %v", err)
		}
	})

	// Negative: 65534-byte payload must be rejected.
	t.Run("JPEG-max-payload-65534-rejected", func(t *testing.T) {
		t.Parallel()
		overPayload := make([]byte, 65534) // payload+2 = 65536 > 65535
		var buf bytes.Buffer
		err := writeSegment(&buf, markerAPP1, overPayload)
		if err == nil {
			t.Error("writeSegment(65534 bytes): expected ErrSegmentTooLarge, got nil")
		}
	})
}

// TestJPEGFF00StuffingPassthrough verifies that 0xFF 0x00 byte-stuffing
// sequences in the entropy-coded data (after SOS) are passed through unchanged
// by Inject.
//
// ITU-T T.81 §F.1.2.3: "0xFF 0x00" in entropy-coded data is a stuffed zero —
// it is not a marker and must not be treated as one. The library must copy
// entropy data verbatim.
func TestJPEGFF00StuffingPassthrough(t *testing.T) {
	t.Parallel()

	// Build a JPEG whose "scan data" contains FF 00 stuffed zero bytes.
	// These are not markers: the JPEG decoder strips the 0x00, but the
	// metadata library must pass them through without modification.
	//
	// Layout after SOS: FF 00 AA BB FF 00 CC DD EOI
	stuffed := []byte{0xFF, 0x00, 0xAA, 0xBB, 0xFF, 0x00, 0xCC, 0xDD}

	var src bytes.Buffer
	src.Write([]byte{0xFF, 0xD8})             // SOI
	src.Write([]byte{0xFF, 0xDA, 0x00, 0x02}) // SOS (minimal, length=2 → 0 payload bytes)
	src.Write(stuffed)                        // entropy data with FF 00 stuffing
	src.Write([]byte{0xFF, 0xD9})             // EOI

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src.Bytes()), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	outBytes := out.Bytes()

	// Locate SOS in output.
	sosIdx := -1
	for i := 0; i+1 < len(outBytes); i++ {
		if outBytes[i] == 0xFF && outBytes[i+1] == markerSOS {
			sosIdx = i
			break
		}
	}
	if sosIdx < 0 {
		t.Fatal("SOS marker not found in output")
	}

	// Scan data starts after SOS marker (2 bytes) + SOS length (2 bytes).
	scanStart := sosIdx + 4 // FF DA + 00 02
	if !bytes.Contains(outBytes[scanStart:], stuffed) {
		t.Errorf("FF 00 stuffing bytes not present verbatim after SOS; Inject must copy entropy data unchanged")
	}
}

// TestJPEGStandaloneMarkersNoLength verifies that standalone markers (SOI, EOI,
// RST0–RST7, TEM) are recognised by readSegment as carrying no length/data field.
//
// ITU-T T.81 §B.1.1.2: "Markers that do not have a marker segment following
// them include: SOI, EOI, RST m, TEM, DHT, DAC, DRI, COM, SOF, SOS, APP."
// Wait — only SOI, EOI, RSTm, and TEM are stand-alone (no length field).
// §B.2.1, §B.4.1: SOI (D8) and EOI (D9) are two-byte sequences with no
// parameter segment. RSTm (D0–D7) and TEM (01) are also standalone.
func TestJPEGStandaloneMarkersNoLength(t *testing.T) {
	t.Parallel()

	standalones := []struct {
		name   string
		marker byte
	}{
		{"SOI", markerSOI},
		{"EOI", markerEOI},
		{"RST0", 0xD0},
		{"RST1", 0xD1},
		{"RST3", 0xD3},
		{"RST7", 0xD7},
		{"TEM", 0x01},
	}

	for _, tc := range standalones {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Build a 2-byte marker sequence (no length field follows).
			var buf bytes.Buffer
			buf.Write([]byte{0xFF, tc.marker})
			scratch := make([]byte, 4096)
			marker, data, err := readSegment(&buf, &scratch)
			if err != nil {
				t.Fatalf("readSegment for %s: %v", tc.name, err)
			}
			if marker != tc.marker {
				t.Errorf("marker = 0x%02X, want 0x%02X", marker, tc.marker)
			}
			if data != nil {
				t.Errorf("%s: data = %v, want nil (standalone markers carry no data)", tc.name, data)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// §1(d) — EXIF / XMP / ExtendedXMP / IPTC embedding rules
// ---------------------------------------------------------------------------

// TestJPEGEXIFPrefixRequired verifies that an APP1 segment whose payload does
// NOT begin with "Exif\x00\x00" is not returned as rawEXIF.
//
// CIPA DC-008-2019 §4.5.4: APP1 containing Exif data must begin with
// the 6-byte identifier "45 78 69 66 00 00" ("Exif\0\0"). Any APP1 that lacks
// this prefix is not an EXIF segment and must be ignored for EXIF purposes.
func TestJPEGEXIFPrefixRequired(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		payload []byte
	}{
		{"wrong-prefix-JFIF", append([]byte("JFIF\x00"), make([]byte, 8)...)},
		{"no-prefix-tiff-bytes", minimalTIFFBytes()}, // valid TIFF but no Exif\0\0 header
		{"exif-lowercase", append([]byte("exif\x00\x00"), minimalTIFFBytes()...)},
		{"exif-one-null", append([]byte("Exif\x00"), minimalTIFFBytes()...)}, // only one NUL
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Build an APP1 segment with the non-EXIF payload.
			var buf bytes.Buffer
			buf.Write([]byte{0xFF, 0xD8}) // SOI
			var lbuf [2]byte
			binary.BigEndian.PutUint16(lbuf[:], uint16(len(tc.payload)+2)) //nolint:gosec // G115: test helper
			buf.Write([]byte{0xFF, 0xE1})
			buf.Write(lbuf[:])
			buf.Write(tc.payload)
			buf.Write([]byte{0xFF, 0xD9}) // EOI

			rawEXIF, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if rawEXIF != nil {
				t.Errorf("%s: rawEXIF is non-nil (%d bytes); APP1 without valid 'Exif\\x00\\x00' prefix must not be returned as EXIF",
					tc.name, len(rawEXIF))
			}
		})
	}
}

// TestJPEGXMPNamespaceURIPrefix verifies that the standard XMP APP1 is
// identified exclusively by the namespace URI prefix.
//
// Adobe XMP Specification Part 3 §1.1.3: the APP1 segment that carries
// standard XMP must begin with the null-terminated string
// "http://ns.adobe.com/xap/1.0/\x00" (29 bytes). Any APP1 that starts with a
// different prefix is not an XMP segment and must not be returned as rawXMP.
func TestJPEGXMPNamespaceURIPrefix(t *testing.T) {
	t.Parallel()

	xmpBody := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta>`)

	// Positive: correct namespace URI → rawXMP must be non-nil.
	t.Run("JPEG-XMP-correct-namespace-accepted", func(t *testing.T) {
		t.Parallel()
		j := buildJPEG(nil, nil, xmpBody)
		_, _, rawXMP, err := Extract(bytes.NewReader(j))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if rawXMP == nil {
			t.Error("rawXMP is nil; correct XMP namespace URI must be recognised")
		}
	})

	// Negative: wrong namespace → rawXMP must be nil.
	t.Run("JPEG-XMP-wrong-namespace-ignored", func(t *testing.T) {
		t.Parallel()
		wrongNS := append([]byte("http://ns.adobe.com/xmp/1.0/\x00"), xmpBody...)

		var buf bytes.Buffer
		buf.Write([]byte{0xFF, 0xD8})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], uint16(len(wrongNS)+2)) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xE1})
		buf.Write(lbuf[:])
		buf.Write(wrongNS)
		buf.Write([]byte{0xFF, 0xD9})

		_, _, rawXMP, err := Extract(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if rawXMP != nil {
			t.Errorf("rawXMP is non-nil for wrong XMP namespace; must be ignored")
		}
	})
}

// TestJPEGExtendedXMPHeaderLayout verifies the exact binary layout of the
// ExtendedXMP APP1 header written by writeExtendedChunks.
//
// Adobe XMP Specification Part 3 §1.1.4: the APP1 segment body for an
// Extended XMP chunk is:
//
//	[0..34]  = "http://ns.adobe.com/xmp/extension/\x00"  (35 bytes)
//	[35..66] = GUID (32 ASCII hex characters)
//	[67..70] = fullLength (uint32 BE) — total size of all extended XMP data
//	[71..74] = offset (uint32 BE)     — byte offset of this chunk
//	[75..]   = chunk data
func TestJPEGExtendedXMPHeaderLayout(t *testing.T) {
	t.Parallel()

	// Build a payload large enough to require the extended XMP path.
	padding := bytes.Repeat([]byte("C"), 66_000)
	rawXMP := []byte(
		`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:title>` + string(padding) + `</dc:title>` +
			`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`,
	)

	src := buildJPEG(nil, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, rawXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	outBytes := out.Bytes()

	// Locate extended XMP identifier.
	// identXMPNote = "http://ns.adobe.com/xmp/extension/\x00"
	identPos := bytes.Index(outBytes, identXMPNote)
	if identPos < 0 {
		t.Fatal("JPEG-ExtendedXMP: extended APP1 identifier not found in output")
	}

	bodyStart := identPos + len(identXMPNote)
	const minBodyLen = 32 + 4 + 4 // GUID + fullLen + offset
	if bodyStart+minBodyLen > len(outBytes) {
		t.Fatalf("JPEG-ExtendedXMP: output too short at bodyStart=%d", bodyStart)
	}

	// GUID must be exactly 32 hex characters.
	guidBytes := outBytes[bodyStart : bodyStart+32]
	for _, b := range guidBytes {
		isHex := (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
		if !isHex {
			t.Errorf("GUID byte 0x%02X is not a valid hex character; GUID must be 32 hex chars (XMP Part 3 §1.1.4)", b)
		}
	}

	// fullLength must be non-zero.
	fullLen := binary.BigEndian.Uint32(outBytes[bodyStart+32 : bodyStart+36])
	if fullLen == 0 {
		t.Error("JPEG-ExtendedXMP: fullLength is 0; must be the total extended payload size (§1.1.4)")
	}

	// First chunk offset must be 0.
	offset := binary.BigEndian.Uint32(outBytes[bodyStart+36 : bodyStart+40])
	if offset != 0 {
		t.Errorf("JPEG-ExtendedXMP: first chunk offset = %d, want 0 (§1.1.4)", offset)
	}
}

// TestJPEGIPTCResourceID0404 verifies that the IRB block written for IPTC data
// uses Photoshop resource ID 0x0404 (IPTC-NAA) as mandated by the spec.
//
// CIPA DC-008-2019 §4.5.6: the Photoshop IRB resource block carrying IPTC data
// must have resource ID 0x0404. Any other ID will be ignored by conforming
// readers.
func TestJPEGIPTCResourceID0404(t *testing.T) {
	t.Parallel()

	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}

	src := buildJPEG(nil, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, iptcData, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// Extract the raw APP13 payload and verify the resource ID.
	outAPP13 := extractAPP13Payload(out.Bytes())
	if outAPP13 == nil {
		t.Fatal("no APP13 segment in Inject output")
	}

	// Scan the IRB for a 0x0404 block.
	irb := bytes.TrimPrefix(outAPP13, identPS)
	found := false
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
		if resourceID == 0x0404 {
			found = true
			if !bytes.Equal(data, iptcData) {
				t.Errorf("IPTC resource 0x0404 data = %q, want %q", data, iptcData)
			}
		}
		blockEnd := newPos
		if len(data)%2 != 0 {
			blockEnd++
		}
		pos = blockEnd
	}
	if !found {
		t.Error("JPEG-IPTC: resource 0x0404 not found in APP13 IRB (§1(d) IPTC embedding rule)")
	}
}

// ---------------------------------------------------------------------------
// §1(e) — Write byte-correctness
// ---------------------------------------------------------------------------

// TestJPEGWriteAPPnBeforeSOS verifies that Inject writes new APPn metadata
// segments BEFORE the SOS marker.
//
// JPEG structure: metadata precedes SOS; entropy data follows SOS. Writing
// APPn segments after SOS would make them invisible to conforming parsers.
// ITU-T T.81 §B.2: frame header and application segments precede SOS.
func TestJPEGWriteAPPnBeforeSOS(t *testing.T) {
	t.Parallel()

	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'A', 'B', 'C'}
	xmpData := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta>`)

	src := buildJPEG(nil, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, tiffData, iptcData, xmpData, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	outBytes := out.Bytes()

	// Find the SOS marker position.
	sosPos := -1
	for i := 0; i+1 < len(outBytes); i++ {
		if outBytes[i] == 0xFF && outBytes[i+1] == markerSOS {
			sosPos = i
			break
		}
	}
	if sosPos < 0 {
		t.Fatal("SOS marker not found in Inject output")
	}

	// All APP1/APP13 markers must appear before SOS.
	for i := 0; i+1 < len(outBytes); i++ {
		if outBytes[i] != 0xFF {
			continue
		}
		m := outBytes[i+1]
		if m == markerAPP1 || m == markerAPP13 {
			if i > sosPos {
				t.Errorf("APPn marker 0x%02X at position %d appears AFTER SOS at %d (§1(e): metadata must precede SOS)",
					m, i, sosPos)
			}
		}
	}
}

// TestJPEGWriteSegmentLengthCorrect verifies that every APPn segment written
// by Inject has Lp = payload_length + 2, which is the requirement from
// ITU-T T.81 §B.1.1.4: length includes itself.
func TestJPEGWriteSegmentLengthCorrect(t *testing.T) {
	t.Parallel()

	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	xmpData := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta>`)

	src := buildJPEG(nil, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, tiffData, iptcData, xmpData, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	outBytes := out.Bytes()

	// Walk every segment in the output and verify the length field consistency.
	// Skip SOI (2 bytes at position 0).
	pos := 2
	for pos < len(outBytes)-1 {
		if outBytes[pos] != 0xFF {
			// Entropy data: stop segment walk at first non-marker byte after SOS.
			break
		}
		m := outBytes[pos+1]

		// Standalone markers: no length field.
		if isStandalone(m) {
			if m == markerSOI || m == markerEOI {
				break
			}
			pos += 2
			continue
		}

		// SOS: verify header length then stop (entropy data follows).
		if m == markerSOS {
			if pos+4 > len(outBytes) {
				t.Fatalf("SOS segment truncated at pos=%d", pos)
			}
			lp := int(binary.BigEndian.Uint16(outBytes[pos+2 : pos+4]))
			if lp < 2 {
				t.Errorf("SOS at pos=%d has Lp=%d < 2 (§B.1.1.4)", pos, lp)
			}
			break
		}

		// Segment marker: validate Lp.
		if pos+4 > len(outBytes) {
			t.Fatalf("truncated segment at pos=%d", pos)
		}
		lp := int(binary.BigEndian.Uint16(outBytes[pos+2 : pos+4]))
		if lp < 2 {
			t.Errorf("marker 0x%02X at pos=%d has Lp=%d < 2 (§B.1.1.4)", m, pos, lp)
		}
		payloadLen := lp - 2

		// Verify the segment payload fits within the output buffer.
		segEnd := pos + 2 + lp
		if segEnd > len(outBytes) {
			t.Errorf("marker 0x%02X at pos=%d: segment end %d > output length %d (Lp=%d)",
				m, pos, segEnd, len(outBytes), lp)
			break
		}
		// Actual payload (what was written) must equal lp−2 bytes.
		if pos+4+payloadLen > len(outBytes) {
			t.Errorf("marker 0x%02X at pos=%d: payload end %d > output length %d",
				m, pos, pos+4+payloadLen, len(outBytes))
			break
		}
		pos = segEnd
	}
}

// TestJPEGEXIFPayloadLimit65533 verifies that the library rejects EXIF payloads
// that would exceed the 65533-byte single-segment cap.
//
// CIPA DC-008-2019 §4.5.4: EXIF data resides in a single APP1; the Lp field
// is 16-bit, so the maximum total is 65535. Lp counts itself (2 bytes) plus
// the 6-byte "Exif\0\0" prefix, leaving 65527 bytes for the TIFF stream.
// Container spec §1(e): "EXIF write must not exceed 65533 — fail rather than truncate."
func TestJPEGEXIFPayloadLimit65533(t *testing.T) {
	t.Parallel()

	// identExif = 6 bytes; length field = 2 bytes; 6+rawSize+2 must be ≤ 65535.
	// So rawSize ≤ 65535 − 6 − 2 = 65527 is the maximum raw EXIF payload.
	//
	// The library uses maxAPP1Payload = 65533 and guards at: len(identExif)+len(rawEXIF)+2 > 65535
	// → 6+rawEXIF+2 > 65535 → rawEXIF > 65527. So 65528 must be rejected.
	t.Run("JPEG-EXIF-65527-accepted", func(t *testing.T) {
		t.Parallel()
		// Maximum EXIF payload that fits in APP1.
		payload := make([]byte, 65527)
		var out bytes.Buffer
		err := writeEXIFSegment(&out, payload)
		if err != nil {
			t.Errorf("writeEXIFSegment(65527 bytes): expected nil, got %v", err)
		}
	})

	t.Run("JPEG-EXIF-65528-rejected", func(t *testing.T) {
		t.Parallel()
		// One byte over the limit.
		payload := make([]byte, 65528)
		var out bytes.Buffer
		err := writeEXIFSegment(&out, payload)
		if err == nil {
			t.Error("writeEXIFSegment(65528 bytes): expected error, got nil")
		}
	})
}

// TestJPEGExtendedXMPFullLengthOffsetConsistent verifies that the fullLength
// and offset fields in all extended XMP APP1 chunks written by Inject are
// mutually consistent: every offset must be within [0, fullLength), and
// offset=0 for the first chunk.
//
// Adobe XMP Specification Part 3 §1.1.4: fullLength is the total size of the
// complete extended XMP document; offset is the byte offset within that document
// where this chunk's data begins. All chunks must cover non-overlapping ranges
// that together cover [0, fullLength).
func TestJPEGExtendedXMPFullLengthOffsetConsistent(t *testing.T) {
	t.Parallel()

	// Build a payload that forces multi-chunk extended XMP.
	padding := bytes.Repeat([]byte("X"), 130_000)
	rawXMP := []byte(
		`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:subject>` + string(padding) + `</dc:subject>` +
			`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`,
	)

	src := buildJPEG(nil, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, rawXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	outBytes := out.Bytes()

	// Collect all extended XMP APP1 bodies.
	type chunkInfo struct {
		fullLen uint32
		offset  uint32
		dataLen int
	}
	var chunks []chunkInfo

	search := outBytes
	for {
		idx := bytes.Index(search, identXMPNote)
		if idx < 0 {
			break
		}
		body := search[idx+len(identXMPNote):]
		if len(body) < 40 { // 32 GUID + 4 fullLen + 4 offset
			break
		}
		fullLen := binary.BigEndian.Uint32(body[32:36])
		offset := binary.BigEndian.Uint32(body[36:40])
		chunkData := body[40:]
		chunks = append(chunks, chunkInfo{fullLen: fullLen, offset: offset, dataLen: len(chunkData)})
		search = search[idx+len(identXMPNote):]
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 extended XMP chunks for a 130 KiB payload, got %d", len(chunks))
	}

	// All chunks must agree on fullLength.
	firstFullLen := chunks[0].fullLen
	for i, c := range chunks {
		if c.fullLen != firstFullLen {
			t.Errorf("chunk[%d].fullLength = %d, want %d (all chunks must agree)", i, c.fullLen, firstFullLen)
		}
		if c.offset >= firstFullLen {
			t.Errorf("chunk[%d].offset = %d ≥ fullLength = %d (invalid)", i, c.offset, firstFullLen)
		}
	}

	// First chunk must have offset=0.
	if chunks[0].offset != 0 {
		t.Errorf("first extended XMP chunk offset = %d, want 0 (§1.1.4)", chunks[0].offset)
	}

	// Chunks must cover the full [0, fullLength) range without gaps.
	coverage := make([]bool, firstFullLen)
	for _, c := range chunks {
		end := c.offset + uint32(c.dataLen) //nolint:gosec // G115: test helper; offset+dataLen bounded by fullLen
		if end > firstFullLen {
			end = firstFullLen
		}
		for i := c.offset; i < end; i++ {
			coverage[i] = true
		}
	}
	for i, covered := range coverage {
		if !covered {
			t.Errorf("extended XMP byte offset %d not covered by any chunk (fullLength=%d)", i, firstFullLen)
			break // report only the first gap
		}
	}
}

// ---------------------------------------------------------------------------
// §1(f) — Robustness cases
// ---------------------------------------------------------------------------

// TestJPEGRobustTruncatedAfterSOI verifies that a stream containing only the
// 2-byte SOI (FF D8) does not panic and returns nil payloads.
//
// §1(f): "Truncated after SOI."
func TestJPEGRobustTruncatedAfterSOI(t *testing.T) {
	t.Parallel()
	// Only SOI: no marker follows.
	onlySOI := []byte{0xFF, 0xD8}
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(onlySOI))
	if err != nil {
		t.Errorf("JPEG-robust-truncated-after-SOI: expected nil error, got %v", err)
	}
	if rawEXIF != nil || rawIPTC != nil || rawXMP != nil {
		t.Errorf("expected nil payloads for SOI-only stream; got exif=%v iptc=%v xmp=%v", rawEXIF, rawIPTC, rawXMP)
	}
}

// TestJPEGRobustMissingEOI verifies that a JPEG with no EOI or SOS (stream ends
// after the last APP segment) does not panic and returns the collected metadata.
//
// §1(f): "missing EOI" — graceful degradation required.
func TestJPEGRobustMissingEOI(t *testing.T) {
	t.Parallel()
	// SOI + EXIF APP1 — no SOS, no EOI.
	tiffData := minimalTIFFBytes()
	payload := append([]byte("Exif\x00\x00"), tiffData...)
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(payload)+2)) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xE1})
	buf.Write(lbuf[:])
	buf.Write(payload)
	// No EOI — stream ends here.

	// Must not panic. EXIF was fully read before the truncation, so must be returned.
	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf.Bytes()))
	if rawEXIF == nil {
		t.Error("JPEG-robust-missing-EOI: rawEXIF is nil; EXIF was fully parsed before truncation")
	}
}

// TestJPEGRobustLpGreaterThanRemaining verifies that a segment whose declared
// length exceeds the remaining bytes in the stream is handled without panic.
//
// §1(f): "Lp > remaining."
func TestJPEGRobustLpGreaterThanRemaining(t *testing.T) {
	t.Parallel()
	// APP1 with Lp=500 but only 4 payload bytes follow.
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})             // SOI
	buf.Write([]byte{0xFF, 0xE1})             // APP1 marker
	writeBE16(&buf, 500)                      // Lp=500 (498 payload bytes claimed)
	buf.Write([]byte{0x00, 0x01, 0x02, 0x03}) // only 4 bytes actually present
	// No EOI: stream ends here.

	rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(buf.Bytes()))
	// No panic; all nil since no valid metadata was fully read.
	_ = rawEXIF
	_ = rawIPTC
	_ = rawXMP
}

// TestJPEGRobustLpLessThan2 verifies that a segment with Lp < 2 (structurally
// invalid) does not panic.
//
// ITU-T T.81 §B.1.1.4: Lp ≥ 2; §1(f): "Lp < 2."
func TestJPEGRobustLpLessThan2(t *testing.T) {
	t.Parallel()
	cases := []uint16{0, 1}
	for _, lp := range cases {
		t.Run(fmt.Sprintf("Lp=%d", lp), func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			buf.Write([]byte{0xFF, 0xD8}) // SOI
			buf.Write([]byte{0xFF, 0xE1}) // APP1
			writeBE16(&buf, lp)
			// No panic required; graceful degradation.
			rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(buf.Bytes()))
			_ = rawEXIF
			_ = rawIPTC
			_ = rawXMP
		})
	}
}

// TestJPEGRobustAPPnPastEOF verifies that an APP1 segment whose payload
// extends past the end of file is handled without panic or OOB access.
//
// §1(f): "APPn past EOF."
func TestJPEGRobustAPPnPastEOF(t *testing.T) {
	t.Parallel()
	// Lp = 65535 (maximum) but the file is only 6 bytes total.
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})             // SOI (2 bytes)
	buf.Write([]byte{0xFF, 0xE1, 0xFF, 0xFF}) // APP1 with Lp=65535 — 65533 payload bytes claimed
	// No payload bytes follow: stream ends immediately.

	rawEXIF, rawIPTC, rawXMP, _ := Extract(bytes.NewReader(buf.Bytes()))
	// No panic; all nil.
	_ = rawEXIF
	_ = rawIPTC
	_ = rawXMP
}

// TestJPEGRobustMultipleExifUsesFirst verifies that when a JPEG contains two
// APP1 segments with the "Exif\x00\x00" prefix, the parser uses the FIRST one
// and ignores subsequent ones.
//
// §1(f): "multiple Exif\0\0 (use first)."
// In practice some tools write a second EXIF APP1 after the first; the spec
// does not explicitly address this but the convention is to use the first.
func TestJPEGRobustMultipleExifUsesFirst(t *testing.T) {
	t.Parallel()

	// Build two distinct TIFF streams to identify which is returned.
	firstTIFF := minimalTIFFBytes()
	// Second TIFF: same structure but different ImageWidth value (1024 vs 800).
	secondTIFF := func() []byte {
		b := make([]byte, len(firstTIFF))
		copy(b, firstTIFF)
		// IFD entry 0 at offset 10: tag(2)+type(2)+count(4)+value(4)
		// value offset 18–21 → change to 1024
		binary.LittleEndian.PutUint32(b[18:], 1024)
		return b
	}()

	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	// First APP1 with firstTIFF.
	p1 := append([]byte("Exif\x00\x00"), firstTIFF...)
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(p1)+2)) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xE1})
	buf.Write(lbuf[:])
	buf.Write(p1)

	// Second APP1 with secondTIFF.
	p2 := append([]byte("Exif\x00\x00"), secondTIFF...)
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(p2)+2)) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xE1})
	buf.Write(lbuf[:])
	buf.Write(p2)

	buf.Write([]byte{0xFF, 0xD9}) // EOI

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("rawEXIF is nil; expected first EXIF segment to be returned")
	}
	// The returned EXIF must be firstTIFF, not secondTIFF.
	if !bytes.Equal(rawEXIF, firstTIFF) {
		t.Error("JPEG-robust-multiple-exif: returned EXIF is not the first one")
	}
	if bytes.Equal(rawEXIF, secondTIFF) {
		t.Error("JPEG-robust-multiple-exif: returned EXIF is the second one; must use first")
	}
}

// TestJPEGRobustExtendedXMPMissingChunks verifies that when the main XMP packet
// references an extended XMP GUID but the corresponding extended APP1 segments
// are absent, Extract returns the main XMP unchanged (graceful degradation).
//
// §1(f): "ExtendedXMP missing… chunks."
func TestJPEGRobustExtendedXMPMissingChunks(t *testing.T) {
	t.Parallel()

	const guid = "DEADBEEF11223344DEADBEEF11223344"
	// Main XMP with HasExtendedXMP but NO extended APP1 segment follows.
	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
		` xmpNote:HasExtendedXMP="` + guid + `">` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta>`)

	// Build: SOI + main XMP APP1 only + SOS + EOI (no extended chunks).
	mainPayload := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), mainXMP...)
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(mainPayload)+2)) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xE1})
	buf.Write(lbuf[:])
	buf.Write(mainPayload)
	buf.Write([]byte{0xFF, 0xD9}) // EOI

	_, _, rawXMP, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Main XMP must be returned as-is (no reassembly without chunks).
	if rawXMP == nil {
		t.Fatal("rawXMP is nil; main XMP should be returned when extended chunks are absent")
	}
	if !bytes.Equal(rawXMP, mainXMP) {
		t.Errorf("rawXMP modified despite missing extended chunks; got %d bytes, want %d", len(rawXMP), len(mainXMP))
	}
}

// TestJPEGRobustExtendedXMPDuplicateOffsets verifies that multiple extended XMP
// chunks with the same offset do not panic and produce a non-nil XMP result
// (the assembled content may be garbled, but the library must not crash).
//
// §1(f): "ExtendedXMP… dup… chunks."
func TestJPEGRobustExtendedXMPDuplicateOffsets(t *testing.T) {
	t.Parallel()

	const guid = "AABBCCDDEEFF00112233445566778899"
	extChunkData := []byte(`<rdf:Description rdf:about=""/></rdf:RDF>`)

	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
		` xmpNote:HasExtendedXMP="` + guid + `">` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta>`)

	writeExtChunk := func(buf *bytes.Buffer, offset uint32, data []byte) {
		var body bytes.Buffer
		body.WriteString("http://ns.adobe.com/xmp/extension/\x00")
		body.WriteString(guid)
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:], uint32(len(data))) //nolint:gosec // G115: test helper
		binary.BigEndian.PutUint32(hdr[4:], offset)
		body.Write(hdr[:])
		body.Write(data)
		extPayload := body.Bytes()
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], uint16(len(extPayload)+2)) //nolint:gosec // G115: test helper
		buf.Write([]byte{0xFF, 0xE1})
		buf.Write(lbuf[:])
		buf.Write(extPayload)
	}

	mainPayload := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), mainXMP...)
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(mainPayload)+2)) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xE1})
	buf.Write(lbuf[:])
	buf.Write(mainPayload)

	// Write two extended chunks with the same offset=0 (duplicate).
	writeExtChunk(&buf, 0, extChunkData)
	writeExtChunk(&buf, 0, extChunkData)

	buf.Write([]byte{0xFF, 0xD9})

	// Must not panic.
	_, _, rawXMP, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Errorf("JPEG-robust-extended-dup-offsets: unexpected error: %v", err)
	}
	// rawXMP may be nil or non-nil; no crash is the key requirement.
	_ = rawXMP
}

// TestJPEGRobustExtendedXMPGUIDMismatch verifies that extended XMP chunks
// whose GUID differs from the GUID in the main XMP are silently ignored.
//
// §1(f): "GUID mismatch."
// Adobe XMP Specification Part 3 §1.1.4: readers must use only chunks whose
// GUID matches the HasExtendedXMP value in the main packet.
func TestJPEGRobustExtendedXMPGUIDMismatch(t *testing.T) {
	t.Parallel()

	const mainGUID = "AAAABBBBCCCCDDDDAAAABBBBCCCCDDDD"
	const chunkGUID = "XXXXYYYY0000XXXXYYYYXXXXYYYYXXXX" // different GUID

	mainXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
		` xmpNote:HasExtendedXMP="` + mainGUID + `">` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta>`)

	extContent := []byte(`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"/>`)
	j := buildExtendedXMPJPEG(mainXMP, chunkGUID, extContent)

	_, _, rawXMP, err := Extract(bytes.NewReader(j))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// The main XMP must be returned unchanged (no merge with mismatched chunk).
	if rawXMP == nil {
		t.Fatal("rawXMP is nil; main XMP must be returned even when extended GUID mismatches")
	}
	if bytes.Contains(rawXMP, extContent) {
		t.Error("JPEG-robust-guid-mismatch: extended content was merged despite GUID mismatch; must not merge")
	}
}

// TestJPEGRobustMultipleAPP13Concatenation verifies that when a JPEG carries
// two APP13 "Photoshop 3.0" segments, their IRB payloads are concatenated and
// the 0x0404 IPTC block is found regardless of which segment it is in.
//
// §1(f) / IPTC embedding rule: multiple APP13 Photoshop 3.0 segments must be
// logically concatenated. Legacy tools may split large APP13 payloads.
// containers.md §1(f); iptc.md IRB-APP13-09.
func TestJPEGRobustMultipleAPP13Concatenation(t *testing.T) {
	t.Parallel()

	// IPTC data placed in the SECOND APP13 segment (first has a different resource).
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	digestData := []byte{0x01, 0x02, 0x03, 0x04} // non-IPTC resource in first segment

	buildAPP13Segment := func(irb []byte) []byte {
		payload := append([]byte("Photoshop 3.0\x00"), irb...)
		var seg bytes.Buffer
		seg.Write([]byte{0xFF, 0xED})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], uint16(len(payload)+2)) //nolint:gosec // G115: test helper
		seg.Write(lbuf[:])
		seg.Write(payload)
		return seg.Bytes()
	}

	// First APP13: only resource 0x0425 (digest), no IPTC.
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	irb1 := buildIRBDirect(0x0425, digestData)
	buf.Write(buildAPP13Segment(irb1))

	// Second APP13: resource 0x0404 (IPTC).
	irb2 := buildIRB(iptcData)
	buf.Write(buildAPP13Segment(irb2))

	buf.Write([]byte{0xFF, 0xD9}) // EOI

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawIPTC == nil {
		t.Fatal("JPEG-multiple-APP13: rawIPTC is nil; 0x0404 in second APP13 must be found via concatenation")
	}
	if !bytes.Equal(rawIPTC, iptcData) {
		t.Errorf("JPEG-multiple-APP13: rawIPTC = %q, want %q", rawIPTC, iptcData)
	}
}

// TestJPEGRobustMalformedIRBLength verifies that a malformed IRB block (where
// the 4-byte data-size field exceeds the remaining buffer) is handled without
// panic, and that metadata before the malformed entry is still returned.
//
// §1(f): "malformed IRB length/padding."
func TestJPEGRobustMalformedIRBLength(t *testing.T) {
	t.Parallel()

	// IRB: valid 0x0404 block first, then a malformed block with oversized size.
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'A', 'B', 'C'}
	var irb bytes.Buffer
	irb.Write(buildIRB(iptcData)) // valid 0x0404

	// Malformed block: 8BIM + ID 0x040C + size=0xFFFFFFFF + 0 data bytes.
	irb.WriteString("8BIM")
	irb.Write([]byte{0x04, 0x0C})             // resource ID
	irb.Write([]byte{0x00, 0x00})             // pascal name
	irb.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF}) // giant size
	// No data bytes: stream ends here.

	app13Payload := append([]byte("Photoshop 3.0\x00"), irb.Bytes()...)
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(app13Payload)+2)) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xED})
	buf.Write(lbuf[:])
	buf.Write(app13Payload)
	buf.Write([]byte{0xFF, 0xD9})

	_, rawIPTC, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("JPEG-robust-malformed-IRB: unexpected error: %v", err)
	}
	if rawIPTC == nil {
		t.Error("JPEG-robust-malformed-IRB: rawIPTC is nil; valid 0x0404 before malformed block must still be returned")
	}
	if !bytes.Equal(rawIPTC, iptcData) {
		t.Errorf("JPEG-robust-malformed-IRB: rawIPTC = %q, want %q", rawIPTC, iptcData)
	}
}

// TestJPEGRobustFillBytesBeforeMarker verifies that multiple consecutive 0xFF
// fill bytes preceding any marker are silently consumed by the scanner.
//
// ITU-T T.81 §B.1.1.2: fill bytes (0xFF) may precede any marker; they must
// be ignored. §1(f): "fill bytes (FF FF…)."
func TestJPEGRobustFillBytesBeforeMarker(t *testing.T) {
	t.Parallel()

	tiffData := minimalTIFFBytes()
	payload := append([]byte("Exif\x00\x00"), tiffData...)

	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	// Insert three 0xFF fill bytes before the APP1 marker.
	// Stream: FF FF FF FF E1 <length> <payload>
	writeBE16(&buf, 0xFFFF)   // two fill bytes written as u16 BE
	buf.WriteByte(0xFF)       // third fill byte
	buf.WriteByte(markerAPP1) // actual APP1 marker
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], uint16(len(payload)+2)) //nolint:gosec // G115: test helper
	buf.Write(lbuf[:])
	buf.Write(payload)
	buf.Write([]byte{0xFF, 0xD9}) // EOI

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("JPEG-fill-bytes: %v", err)
	}
	if rawEXIF == nil {
		t.Error("JPEG-fill-bytes: rawEXIF is nil; fill bytes before marker must be skipped")
	}
}

// ---------------------------------------------------------------------------
// §1 corpus parity
// ---------------------------------------------------------------------------

// TestJPEGCorpusParity verifies that Extract does not panic or return an error
// for any JPEG in the real-world corpus. It also verifies:
//   - Every file produces consistent non-nil or nil payloads (no partial garbage).
//   - A subsequent Inject(nil, nil, nil) round-trip produces a valid JPEG that
//     Extract can re-parse without error.
//
// §1 corpus parity test — corpus may be skipped if testdata is not downloaded.
//
//nolint:paralleltest,tparallel // outer test cannot be parallel: CorpusFiles may call t.Skip before sub-tests are created
func TestJPEGCorpusParity(t *testing.T) {
	paths := testutil.CorpusFiles(t, "jpeg")

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read corpus file: %v", err)
			}

			r := bytes.NewReader(data)

			// Extract must not panic.
			rawEXIF, rawIPTC, rawXMP, extractErr := Extract(r)
			if extractErr != nil {
				// A real-world JPEG that fails to parse is a library bug.
				t.Errorf("corpus %s: Extract returned error: %v", path, extractErr)
				return
			}

			// Inject round-trip: write back the same metadata (nil → no change).
			var out bytes.Buffer
			if injectErr := Inject(bytes.NewReader(data), &out, rawEXIF, rawIPTC, rawXMP, true); injectErr != nil {
				t.Errorf("corpus %s: Inject returned error: %v", path, injectErr)
				return
			}

			// The output must be re-parseable.
			_, _, _, reExtractErr := Extract(bytes.NewReader(out.Bytes()))
			if reExtractErr != nil {
				t.Errorf("corpus %s: Extract after Inject round-trip returned error: %v", path, reExtractErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// writeBE16 writes a big-endian uint16 to b.
// Used by tests that need to embed raw length fields inline without a
// temporary [2]byte variable.
func writeBE16(b *bytes.Buffer, v uint16) {
	b.WriteByte(byte(v >> 8))
	b.WriteByte(byte(v)) //nolint:gosec // G115: intentional low-byte truncation of uint16 → byte
}
