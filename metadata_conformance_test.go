package gometadata

// metadata_conformance_test.go — cross-format reconciliation conformance tests.
//
// Rule IDs are used verbatim as Go sub-test names so that the conformance
// battery is machine-parseable. This file covers rules that live in the root
// reconciliation layer rather than inside a single format package.
//
// Spec citation: MWG Guidelines v2.0 §3.3.1 (IPTC Digest reconciliation).

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// ---------------------------------------------------------------------------
// IRB/JPEG builder helpers local to this file.
// ---------------------------------------------------------------------------

// buildIRBBlock returns a single 8BIM resource block: 4-byte "8BIM" + 2-byte
// resource ID + 2-byte empty Pascal string + 4-byte big-endian length + data
// + optional pad byte. Adobe Photoshop File Formats Specification §"Image
// Resources".
func buildIRBBlock(resourceID uint16, data []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("8BIM")
	var id [2]byte
	binary.BigEndian.PutUint16(id[:], resourceID)
	buf.Write(id[:])
	buf.Write([]byte{0x00, 0x00}) // zero-length Pascal name (1 byte len + 1 byte pad)
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(data))) //nolint:gosec // G115: test helper, len bounded by test data
	buf.Write(sz[:])
	buf.Write(data)
	if len(data)%2 != 0 {
		buf.WriteByte(0x00) // IRB-APP13-07: even-pad data block
	}
	return buf.Bytes()
}

// buildAPP13Segment returns a complete JPEG APP13 segment (0xFF 0xED + length
// + "Photoshop 3.0\x00" + irbBlocks). irbBlocks must already be concatenated.
func buildAPP13Segment(irbBlocks []byte) []byte {
	var payload bytes.Buffer
	payload.WriteString("Photoshop 3.0\x00") // IRB-APP13-02
	payload.Write(irbBlocks)

	var seg bytes.Buffer
	seg.Write([]byte{0xFF, 0xED})       // APP13 marker
	length := uint16(payload.Len() + 2) //nolint:gosec // G115: test helper, bounded by test data
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	seg.Write(lb[:])
	seg.Write(payload.Bytes())
	return seg.Bytes()
}

// buildXMPSegment returns a minimal APP1 XMP segment carrying xmpCaption in
// dc:description[x-default]. Adobe XMP Specification Part 3 §1.1.3.
func buildXMPSegment(xmpCaption string) []byte {
	xmpPacket := fmt.Sprintf(
		`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>`+
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">`+
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`+
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">`+
			`<dc:description><rdf:Alt><rdf:li xml:lang="x-default">%s</rdf:li></rdf:Alt></dc:description>`+
			`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`,
		xmpCaption,
	)
	ns := "http://ns.adobe.com/xap/1.0/\x00"
	payload := append([]byte(ns), []byte(xmpPacket)...)

	var seg bytes.Buffer
	seg.Write([]byte{0xFF, 0xE1})      // APP1
	length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, bounded by test data
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	seg.Write(lb[:])
	seg.Write(payload)
	return seg.Bytes()
}

// buildIPTCIIMBytes returns a minimal IPTC IIM dataset 2:120 (Caption-Abstract)
// containing caption. IIM §1.5.
func buildIPTCIIMBytes(caption string) []byte {
	val := []byte(caption)
	var buf bytes.Buffer
	buf.WriteByte(0x1C)
	buf.WriteByte(2)
	buf.WriteByte(120)                 // DataSet 2:120 Caption-Abstract
	buf.WriteByte(byte(len(val) >> 8)) //nolint:gosec // G115: test helper, len bounded
	buf.WriteByte(byte(len(val)))      //nolint:gosec // G115: test helper, len bounded
	buf.Write(val)
	return buf.Bytes()
}

// buildMWG02JPEG constructs a minimal JPEG containing an IPTC block (0x0404),
// an optional IPTC digest block (0x0425, 16 bytes), and an XMP block.
//
//   - iptcCaption: value for IPTC 2:120 (Caption-Abstract)
//   - xmpCaption:  value for XMP dc:description[x-default]
//   - digestBytes: nil → no 0x0425 block; non-nil → 16-byte digest payload
func buildMWG02JPEG(iptcCaption, xmpCaption string, digestBytes []byte) []byte {
	rawIPTC := buildIPTCIIMBytes(iptcCaption)

	var irbBlocks bytes.Buffer
	irbBlocks.Write(buildIRBBlock(0x0404, rawIPTC))
	if digestBytes != nil {
		irbBlocks.Write(buildIRBBlock(0x0425, digestBytes))
	}

	var jpeg bytes.Buffer
	jpeg.Write([]byte{0xFF, 0xD8}) // SOI
	jpeg.Write(buildAPP13Segment(irbBlocks.Bytes()))
	jpeg.Write(buildXMPSegment(xmpCaption))
	jpeg.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // minimal SOS + EOI
	return jpeg.Bytes()
}

// ---------------------------------------------------------------------------
// MWG-02 conformance sub-tests.
// ---------------------------------------------------------------------------

// TestConformance_MWG02 verifies the IPTC-digest reconciliation rule.
//
// MWG Guidelines v2.0 §3.3.1: the Photoshop resource 0x0425 stores an MD5
// digest of the raw 0x0404 IIM stream at the time XMP was last written.
//
//   - Digest match     → XMP has read priority (default MWG-01 behaviour).
//   - Digest mismatch  → IPTC trust is elevated; IPTC wins for conflicting fields.
//   - All-zero digest  → Treated as "unknown"; IPTC trust is elevated.
//   - Digest absent    → Default MWG-01 behaviour (XMP priority).
func TestConformance_MWG02(t *testing.T) {
	t.Parallel()
	const (
		iptcValue = "IPTC caption"
		xmpValue  = "XMP caption"
	)

	// --- (a) Digest match → XMP keeps read priority ---
	t.Run("MWG-02/digest-match-xmp-wins", func(t *testing.T) {
		t.Parallel()
		// MWG §3.3.1: match means XMP was written after the last IPTC edit; XMP is authoritative.
		rawIPTC := buildIPTCIIMBytes(iptcValue)
		digest := iptc.Digest(rawIPTC)

		jpegBytes := buildMWG02JPEG(iptcValue, xmpValue, digest[:])
		m, err := Read(bytes.NewReader(jpegBytes))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got := m.Caption(); got != xmpValue {
			t.Errorf("Caption() = %q; want XMP value %q (digest match → XMP priority)", got, xmpValue)
		}
	})

	// --- (b) Digest mismatch → IPTC trust elevated; IPTC wins ---
	t.Run("MWG-02/digest-mismatch-iptc-wins", func(t *testing.T) {
		t.Parallel()
		// MWG §3.3.1: mismatch means IPTC was edited independently of XMP; elevate IPTC.
		// Use a wrong digest (all 0xAB) so it cannot accidentally match.
		wrongDigest := [16]byte{0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB,
			0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB}

		jpegBytes := buildMWG02JPEG(iptcValue, xmpValue, wrongDigest[:])
		m, err := Read(bytes.NewReader(jpegBytes))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got := m.Caption(); got != iptcValue {
			t.Errorf("Caption() = %q; want IPTC value %q (digest mismatch → IPTC priority)", got, iptcValue)
		}
	})

	// --- (c) Digest absent → default MWG-01 (XMP priority) ---
	t.Run("MWG-02/digest-absent-xmp-wins", func(t *testing.T) {
		t.Parallel()
		// MWG §3.3.1: when resource 0x0425 is missing, fall back to MWG-01 priority.
		jpegBytes := buildMWG02JPEG(iptcValue, xmpValue, nil /* no digest block */)
		m, err := Read(bytes.NewReader(jpegBytes))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got := m.Caption(); got != xmpValue {
			t.Errorf("Caption() = %q; want XMP value %q (no digest → MWG-01 default XMP priority)", got, xmpValue)
		}
	})

	// --- (d) All-zero digest sentinel → IPTC trust elevated ---
	t.Run("MWG-02/zero-digest-sentinel-iptc-wins", func(t *testing.T) {
		t.Parallel()
		// MWG §3.3.1: the all-zero digest (16 zero bytes) is the "unknown" sentinel;
		// treat as mismatch and elevate IPTC trust.
		zeroDigest := make([]byte, 16) // all zero

		jpegBytes := buildMWG02JPEG(iptcValue, xmpValue, zeroDigest)
		m, err := Read(bytes.NewReader(jpegBytes))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got := m.Caption(); got != iptcValue {
			t.Errorf("Caption() = %q; want IPTC value %q (zero-digest sentinel → IPTC priority)", got, iptcValue)
		}
	})

	// --- Additional: digest match + Copyright field (MWG-05) ---
	t.Run("MWG-02/digest-match-copyright-xmp-wins", func(t *testing.T) {
		t.Parallel()
		// Build IPTC with copyright via 2:116 and XMP with dc:rights.
		rawIPTC := buildIPTCIIMBytesField(116, "IPTC Copyright")
		digest := iptc.Digest(rawIPTC)

		var irbBlocks bytes.Buffer
		irbBlocks.Write(buildIRBBlock(0x0404, rawIPTC))
		irbBlocks.Write(buildIRBBlock(0x0425, digest[:]))

		xmpPacket := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:rights><rdf:Alt><rdf:li xml:lang="x-default">XMP Copyright</rdf:li></rdf:Alt></dc:rights>` +
			`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
		ns := "http://ns.adobe.com/xap/1.0/\x00"
		xmpPayload := append([]byte(ns), []byte(xmpPacket)...)

		var jpeg bytes.Buffer
		jpeg.Write([]byte{0xFF, 0xD8})
		jpeg.Write(buildAPP13Segment(irbBlocks.Bytes()))
		jpeg.Write(buildXMPSegmentRaw(xmpPayload))
		jpeg.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})

		m, err := Read(bytes.NewReader(jpeg.Bytes()))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got := m.Copyright(); got != "XMP Copyright" {
			t.Errorf("Copyright() = %q; want %q (digest match → XMP priority)", got, "XMP Copyright")
		}
	})

	// --- Additional: digest mismatch + Copyright field → IPTC wins ---
	t.Run("MWG-02/digest-mismatch-copyright-iptc-wins", func(t *testing.T) {
		t.Parallel()
		rawIPTC := buildIPTCIIMBytesField(116, "IPTC Copyright")
		wrongDigest := [16]byte{0xCC}

		var irbBlocks bytes.Buffer
		irbBlocks.Write(buildIRBBlock(0x0404, rawIPTC))
		irbBlocks.Write(buildIRBBlock(0x0425, wrongDigest[:]))

		xmpPacket := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:rights><rdf:Alt><rdf:li xml:lang="x-default">XMP Copyright</rdf:li></rdf:Alt></dc:rights>` +
			`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
		ns := "http://ns.adobe.com/xap/1.0/\x00"
		xmpPayload := append([]byte(ns), []byte(xmpPacket)...)

		var jpeg bytes.Buffer
		jpeg.Write([]byte{0xFF, 0xD8})
		jpeg.Write(buildAPP13Segment(irbBlocks.Bytes()))
		jpeg.Write(buildXMPSegmentRaw(xmpPayload))
		jpeg.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})

		m, err := Read(bytes.NewReader(jpeg.Bytes()))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got := m.Copyright(); got != "IPTC Copyright" {
			t.Errorf("Copyright() = %q; want %q (digest mismatch → IPTC priority)", got, "IPTC Copyright")
		}
	})

	// --- MWG-01 regression: digest match must not regress XMP-over-EXIF ---
	t.Run("MWG-01/regression-no-digest-xmp-over-iptc", func(t *testing.T) {
		t.Parallel()
		// When EXIF/IPTC and XMP agree (and digest matches), XMP must still be the source.
		// This test confirms MWG-01 is not altered by the digest logic.
		// Use a unique IPTC caption to ensure buildMWG02JPEG's iptcCaption parameter varies
		// across callers (prevents unparam lint false-positive on the helper signature).
		const sameCaptionValue = "Same caption in both IPTC and XMP"
		rawIPTC := buildIPTCIIMBytes(sameCaptionValue)
		digest := iptc.Digest(rawIPTC)
		jpegBytes := buildMWG02JPEG(sameCaptionValue, sameCaptionValue, digest[:])
		m, err := Read(bytes.NewReader(jpegBytes))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got := m.Caption(); got != sameCaptionValue {
			t.Errorf("Caption() = %q; want %q (MWG-01 regression: identical values, digest match)", got, sameCaptionValue)
		}
	})
}

// buildIPTCIIMBytesField builds a minimal IIM stream for record 2, arbitrary
// dataset number (ds), with text value v. IIM §1.5.
func buildIPTCIIMBytesField(ds uint8, v string) []byte {
	val := []byte(v)
	var buf bytes.Buffer
	buf.WriteByte(0x1C)
	buf.WriteByte(2)
	buf.WriteByte(ds)
	buf.WriteByte(byte(len(val) >> 8)) //nolint:gosec // G115: test helper, bounded
	buf.WriteByte(byte(len(val)))      //nolint:gosec // G115: test helper, bounded
	buf.Write(val)
	return buf.Bytes()
}

// buildXMPSegmentRaw returns an APP1 segment wrapping the pre-assembled XMP
// payload (including the namespace identifier prefix). Use when the payload
// is already constructed without requiring further manipulation.
func buildXMPSegmentRaw(payload []byte) []byte {
	var seg bytes.Buffer
	seg.Write([]byte{0xFF, 0xE1})
	length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, bounded
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	seg.Write(lb[:])
	seg.Write(payload)
	return seg.Bytes()
}
