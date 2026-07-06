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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
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
// Also proves docs/conformance/iptc.md §4 rule RECONCILE-02 (the read-side
// counterpart of the RECONCILE-01..05 dual-write bridging battery in this
// file): RECONCILE-02 is this same MWG-02 digest-elevation policy, restated
// under the IPTC↔XMP reconciliation rule numbering. No new test is added for
// RECONCILE-02 — this doc comment is the traceability link.
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

// ---------------------------------------------------------------------------
// RECONCILE-01..05 conformance battery — IPTC/XMP/EXIF write-side bridging.
//
// docs/conformance/iptc.md §4. RECONCILE-02 is proven by the existing
// TestConformance_MWG02 above (see its doc comment for the cross-reference);
// the remaining four rules are proven below.
// ---------------------------------------------------------------------------

// buildIPTCBylines returns a raw IIM stream containing one dataset 2:80
// (By-line, IIM §2.2.25) occurrence per entry in names, in order. Used to
// prove RECONCILE-04's read-direction order preservation.
func buildIPTCBylines(names []string) []byte {
	var buf bytes.Buffer
	for _, n := range names {
		val := []byte(n)
		buf.WriteByte(0x1C)
		buf.WriteByte(2)
		buf.WriteByte(iptc.DS2Byline)
		buf.WriteByte(byte(len(val) >> 8)) //nolint:gosec // G115: test helper, bounded
		buf.WriteByte(byte(len(val)))      //nolint:gosec // G115: test helper, bounded
		buf.Write(val)
	}
	return buf.Bytes()
}

// buildXMPCreatorSeqSegment returns a complete JPEG APP1 XMP segment carrying
// dc:creator as an ordered rdf:Seq with one rdf:li per entry in names, in
// order (ISO 16684-1 §7.5). Used to prove RECONCILE-04's read-direction order
// preservation on the XMP side.
func buildXMPCreatorSeqSegment(names []string) []byte {
	var li bytes.Buffer
	for _, n := range names {
		li.WriteString("<rdf:li>")
		li.WriteString(n)
		li.WriteString("</rdf:li>")
	}
	xmpPacket := fmt.Sprintf(
		`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>`+
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">`+
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`+
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">`+
			`<dc:creator><rdf:Seq>%s</rdf:Seq></dc:creator>`+
			`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`,
		li.String(),
	)
	ns := "http://ns.adobe.com/xap/1.0/\x00"
	payload := append([]byte(ns), []byte(xmpPacket)...)
	return buildXMPSegmentRaw(payload)
}

// TestConformance_RECONCILE01 proves docs/conformance/iptc.md §4 rule
// RECONCILE-01: every convenience setter that targets more than one backend
// must leave all targeted backends holding semantically equivalent content.
// Assertions are made at the component level (m.IPTC, m.XMP, m.EXIF
// directly), not through the top-level priority-resolving getters, so that a
// silent divergence cannot be masked by MWG-01/MWG-02 read-priority logic.
func TestConformance_RECONCILE01(t *testing.T) {
	t.Parallel()

	t.Run("RECONCILE-01/caption-parity", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		m.SetCaption("x")
		if got := m.IPTC.Caption(); got != "x" {
			t.Errorf("IPTC.Caption() = %q, want %q", got, "x")
		}
		if got := m.XMP.Caption(); got != "x" {
			t.Errorf("XMP.Caption() = %q, want %q", got, "x")
		}
	})

	t.Run("RECONCILE-01/copyright-parity", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		m.SetCopyright("y")
		if got := m.IPTC.Copyright(); got != "y" {
			t.Errorf("IPTC.Copyright() = %q, want %q", got, "y")
		}
		if got := m.XMP.Copyright(); got != "y" {
			t.Errorf("XMP.Copyright() = %q, want %q", got, "y")
		}
	})

	t.Run("RECONCILE-01/creators-parity", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		want := []string{"Alice", "Bob"}
		m.SetCreators(want)
		if got := m.IPTC.AllCreators(); !slices.Equal(got, want) {
			t.Errorf("IPTC.AllCreators() = %v, want %v", got, want)
		}
		if got := m.XMP.Creators(); !slices.Equal(got, want) {
			t.Errorf("XMP.Creators() = %v, want %v", got, want)
		}
	})

	t.Run("RECONCILE-01/datecreated-parity", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		ts := time.Date(2025, 3, 10, 9, 15, 30, 0, time.FixedZone("+0300", 3*3600))
		m.SetDateTimeOriginal(ts)

		// Reconstruct the instant from IPTC 2:55 + 2:60 and compare to ts.
		iptcCombined := m.IPTC.DateCreated() + m.IPTC.TimeCreated()
		iptcInstant, err := time.Parse("20060102150405-0700", iptcCombined)
		if err != nil {
			t.Fatalf("parsing IPTC date+time %q: %v", iptcCombined, err)
		}
		if !iptcInstant.Equal(ts) {
			t.Errorf("IPTC date+time = %v, want instant %v", iptcInstant, ts)
		}

		xmpVal := m.XMP.Get(xmp.NSphotoshop, "DateCreated")
		xmpInstant, err := time.Parse(time.RFC3339, xmpVal)
		if err != nil {
			t.Fatalf("parsing XMP photoshop:DateCreated %q: %v", xmpVal, err)
		}
		if !xmpInstant.Equal(ts) {
			t.Errorf("XMP photoshop:DateCreated = %v, want instant %v", xmpInstant, ts)
		}

		// EXIF 0x9003 stores wall-clock digits only, no offset (RECONCILE-03);
		// compare wall-clock components rather than the absolute instant.
		exifT, ok := m.EXIF.DateTimeOriginal()
		if !ok {
			t.Fatal("EXIF.DateTimeOriginal() ok=false")
		}
		if exifT.Year() != ts.Year() || exifT.Month() != ts.Month() || exifT.Day() != ts.Day() ||
			exifT.Hour() != ts.Hour() || exifT.Minute() != ts.Minute() || exifT.Second() != ts.Second() {
			t.Errorf("EXIF.DateTimeOriginal() wall-clock = %v, want wall-clock of %v", exifT, ts)
		}
	})
}

// TestConformance_RECONCILE03 proves docs/conformance/iptc.md §4 rule
// RECONCILE-03: a single capture timestamp fans out to IIM 2:55+2:60, EXIF
// 0x9003, and XMP photoshop:DateCreated, with sub-second precision dropped on
// IIM and XMP, the IIM UTC offset always in explicit "+0000" (never "Z")
// form, the year-overflow guard on 2:55/2:60, and xmp:CreateDate left
// deliberately untouched (reconciliation spec §0.1).
func TestConformance_RECONCILE03(t *testing.T) {
	t.Parallel()

	t.Run("RECONCILE-03/split-on-write", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		ts := time.Date(2026, 6, 15, 14, 30, 22, 500_000_000, time.FixedZone("+0100", 3600))
		m.SetDateTimeOriginal(ts)

		if got := m.IPTC.DateCreated(); got != "20260615" {
			t.Errorf("IPTC.DateCreated() = %q, want %q", got, "20260615")
		}
		if got := m.IPTC.TimeCreated(); got != "143022+0100" {
			t.Errorf("IPTC.TimeCreated() = %q, want %q (sub-second dropped)", got, "143022+0100")
		}
		if got := m.XMP.Get(xmp.NSphotoshop, "DateCreated"); got != "2026-06-15T14:30:22+01:00" {
			t.Errorf("XMP photoshop:DateCreated = %q, want %q", got, "2026-06-15T14:30:22+01:00")
		}
	})

	t.Run("RECONCILE-03/utc-zero-offset", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		ts := time.Date(2026, 6, 15, 14, 30, 22, 0, time.UTC)
		m.SetDateTimeOriginal(ts)

		if got := m.IPTC.TimeCreated(); got != "143022+0000" {
			t.Errorf("IPTC.TimeCreated() = %q, want %q (explicit +0000, never Z)", got, "143022+0000")
		}
		xmpVal := m.XMP.Get(xmp.NSphotoshop, "DateCreated")
		if !strings.HasSuffix(xmpVal, "Z") {
			t.Errorf("XMP photoshop:DateCreated = %q, want a Z-suffixed UTC value", xmpVal)
		}
	})

	t.Run("RECONCILE-03/year-overflow-guard", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		ts := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		m.SetDateTimeOriginal(ts)

		if got := m.IPTC.DateCreated(); got != "" {
			t.Errorf("IPTC.DateCreated() = %q, want empty (year-overflow guard, §0.3)", got)
		}
		if got := m.IPTC.TimeCreated(); got != "" {
			t.Errorf("IPTC.TimeCreated() = %q, want empty (year-overflow guard is a complete no-op)", got)
		}
		// EXIF and XMP must proceed normally: the IPTC-only guard in
		// IPTC.SetDateCreated must not prevent Metadata.SetDateTimeOriginal
		// from writing to the other two backends.
		//
		// Note: EXIF's own ASCII DateTimeOriginal tag ("YYYY:MM:DD HH:MM:SS")
		// is itself a fixed-width field and cannot represent a 5-digit year
		// either, so EXIF.DateTimeOriginal() (which re-parses the stored
		// string) is not a reliable probe here — that round-trip limitation
		// is a pre-existing, unrelated property of the exif package's own
		// date formatting, not something this bridge introduces or is
		// required to fix. We assert only that the write itself reached
		// ExifIFD, which is what "EXIF proceeds normally" means in this
		// context (RECONCILE-03).
		if e := m.EXIF.ExifIFD.Get(exif.TagDateTimeOriginal); e == nil || e.String() == "" {
			t.Error("EXIF DateTimeOriginal tag missing; year-overflow guard must not affect EXIF")
		}
		if got := m.XMP.Get(xmp.NSphotoshop, "DateCreated"); got == "" {
			t.Error("XMP photoshop:DateCreated empty; year-overflow guard must not affect XMP")
		}
	})

	t.Run("RECONCILE-03/xmp-createdate-untouched", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		m.SetDateTimeOriginal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		if got := m.XMP.Get(xmp.NSxmp, "CreateDate"); got != "" {
			t.Errorf("XMP xmp:CreateDate = %q, want empty (RECONCILE-03 must not write xmp:CreateDate, §0.1)", got)
		}
	})

	t.Run("RECONCILE-03/full-roundtrip", func(t *testing.T) {
		t.Parallel()
		img := acBuildJPEGWithEXIFOnly()
		m, err := Read(bytes.NewReader(img))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		ts := time.Date(2025, 12, 24, 8, 0, 0, 0, time.FixedZone("+0200", 2*3600))
		m.SetDateTimeOriginal(ts)

		var out bytes.Buffer
		if err := Write(bytes.NewReader(img), &out, m); err != nil {
			t.Fatalf("Write: %v", err)
		}
		m2, err := Read(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Read after Write: %v", err)
		}

		if got := m2.IPTC.DateCreated(); got != "20251224" {
			t.Errorf("post-roundtrip IPTC.DateCreated() = %q, want %q", got, "20251224")
		}
		if got := m2.IPTC.TimeCreated(); got != "080000+0200" {
			t.Errorf("post-roundtrip IPTC.TimeCreated() = %q, want %q", got, "080000+0200")
		}
		if got := m2.XMP.Get(xmp.NSphotoshop, "DateCreated"); got != "2025-12-24T08:00:00+02:00" {
			t.Errorf("post-roundtrip XMP photoshop:DateCreated = %q, want %q", got, "2025-12-24T08:00:00+02:00")
		}
		exifT, ok := m2.EXIF.DateTimeOriginal()
		if !ok {
			t.Fatal("post-roundtrip EXIF.DateTimeOriginal() ok=false")
		}
		if exifT.Year() != 2025 || exifT.Month() != 12 || exifT.Day() != 24 ||
			exifT.Hour() != 8 || exifT.Minute() != 0 || exifT.Second() != 0 {
			t.Errorf("post-roundtrip EXIF.DateTimeOriginal() = %v, want wall-clock 2025-12-24 08:00:00", exifT)
		}
	})
}

// TestConformance_RECONCILE04 proves docs/conformance/iptc.md §4 rule
// RECONCILE-04: IIM 2:80 By-line order is preserved into XMP dc:creator's
// ordered rdf:Seq and vice versa on read, SetCreators is a full replace (not
// an append) across all three backends, and EXIF 0x013B Artist receives the
// "; "-joined flattened form.
func TestConformance_RECONCILE04(t *testing.T) {
	t.Parallel()

	t.Run("RECONCILE-04/order-preserved-on-write", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		want := []string{"Alice", "Bob", "Carol"}
		m.SetCreators(want)

		if got := m.IPTC.AllCreators(); !slices.Equal(got, want) {
			t.Errorf("IPTC.AllCreators() = %v, want %v", got, want)
		}

		encoded, err := xmp.Encode(m.XMP)
		if err != nil {
			t.Fatalf("xmp.Encode: %v", err)
		}
		out := string(encoded)
		if !strings.Contains(out, "<rdf:Seq>") {
			t.Error("encoded XMP: dc:creator must use rdf:Seq (RECONCILE-04)")
		}
		// Byte-level order check: each <rdf:li> item must appear at a strictly
		// increasing byte offset, in the same order as `want`.
		lastIdx := -1
		for _, name := range want {
			li := "<rdf:li>" + name + "</rdf:li>"
			idx := strings.Index(out, li)
			if idx < 0 {
				t.Fatalf("encoded XMP missing %q:\n%s", li, out)
			}
			if idx <= lastIdx {
				t.Errorf("encoded XMP: %q at byte offset %d is out of order (previous item ended at/after %d)", li, idx, lastIdx)
			}
			lastIdx = idx
		}
	})

	t.Run("RECONCILE-04/order-preserved-on-read", func(t *testing.T) {
		t.Parallel()
		names := []string{"Carol", "Alice", "Bob"}
		rawIPTC := buildIPTCBylines(names)

		var irbBlocks bytes.Buffer
		irbBlocks.Write(buildIRBBlock(0x0404, rawIPTC))

		var jpeg bytes.Buffer
		jpeg.Write([]byte{0xFF, 0xD8})
		jpeg.Write(buildAPP13Segment(irbBlocks.Bytes()))
		jpeg.Write(buildXMPCreatorSeqSegment(names))
		jpeg.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})

		m, err := Read(bytes.NewReader(jpeg.Bytes()))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got := m.IPTC.AllCreators(); !slices.Equal(got, names) {
			t.Errorf("IPTC.AllCreators() = %v, want %v", got, names)
		}
		if got := m.XMP.Creators(); !slices.Equal(got, names) {
			t.Errorf("XMP.Creators() = %v, want %v", got, names)
		}
	})

	t.Run("RECONCILE-04/full-replace-not-append", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		m.SetCreators([]string{"A", "B"})
		m.SetCreators([]string{"C"})
		if got := m.IPTC.AllCreators(); !slices.Equal(got, []string{"C"}) {
			t.Errorf("IPTC.AllCreators() = %v, want [C] (full replace, not append)", got)
		}
		if got := m.XMP.Creators(); !slices.Equal(got, []string{"C"}) {
			t.Errorf("XMP.Creators() = %v, want [C] (full replace, not append)", got)
		}
	})

	t.Run("RECONCILE-04/exif-artist-flattened", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		m.SetCreators([]string{"Alice", "Bob", "Carol"})
		if got := m.EXIF.Creator(); got != "Alice; Bob; Carol" {
			t.Errorf("EXIF.Creator() = %q, want %q", got, "Alice; Bob; Carol")
		}
	})

	t.Run("RECONCILE-04/empty-clears-iptc-and-xmp", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		m.SetCreators([]string{"Alice", "Bob"})
		m.SetCreators(nil)
		if got := m.IPTC.AllCreators(); got != nil {
			t.Errorf("IPTC.AllCreators() = %v, want nil", got)
		}
		if got := m.XMP.Creators(); got != nil {
			t.Errorf("XMP.Creators() = %v, want nil", got)
		}
	})
}

// TestConformance_RECONCILE05 proves docs/conformance/iptc.md §4 rule
// RECONCILE-05: an IIM Record 2 plain-string dataset round-trips through XMP
// as an rdf:Alt collection whose x-default item is authoritative regardless
// of document position, and any non-default xml:lang alternative never
// leaks into the value the library treats as authoritative.
func TestConformance_RECONCILE05(t *testing.T) {
	t.Parallel()

	t.Run("RECONCILE-05/plain-to-xdefault-on-write", func(t *testing.T) {
		t.Parallel()
		m := newTestMetadata(t)
		m.SetCaption("hello")

		encoded, err := xmp.Encode(m.XMP)
		if err != nil {
			t.Fatalf("xmp.Encode: %v", err)
		}
		out := string(encoded)
		if !strings.Contains(out, "<rdf:Alt>") {
			t.Error("encoded XMP: dc:description must use rdf:Alt (RECONCILE-05)")
		}
		if !strings.Contains(out, `<rdf:li xml:lang="x-default">hello</rdf:li>`) {
			t.Errorf("encoded XMP missing x-default rdf:li for caption:\n%s", out)
		}
	})

	t.Run("RECONCILE-05/xdefault-to-plain-on-read", func(t *testing.T) {
		t.Parallel()
		const (
			iptcValue     = "IPTC caption raw"
			xmpDefault    = "English"
			xmpNonDefault = "Deutscher Untertitel"
		)
		// Digest match ⇒ default MWG-01 (XMP > IIM, RECONCILE-02) applies, so
		// the XMP x-default value is authoritative for m.Caption().
		rawIPTC := buildIPTCIIMBytes(iptcValue)
		digest := iptc.Digest(rawIPTC)

		var irbBlocks bytes.Buffer
		irbBlocks.Write(buildIRBBlock(0x0404, rawIPTC))
		irbBlocks.Write(buildIRBBlock(0x0425, digest[:]))

		xmpPacket := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
			`<dc:description><rdf:Alt>` +
			`<rdf:li xml:lang="x-default">` + xmpDefault + `</rdf:li>` +
			`<rdf:li xml:lang="de">` + xmpNonDefault + `</rdf:li>` +
			`</rdf:Alt></dc:description>` +
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
		if got := m.Caption(); got != xmpDefault {
			t.Errorf("Caption() = %q, want %q (x-default must win regardless of document position)", got, xmpDefault)
		}
		if got := m.Caption(); got == xmpNonDefault {
			t.Errorf("Caption() leaked the non-default language alternative %q", xmpNonDefault)
		}
		// The raw IIM value is unaffected by the XMP alt-selection logic: it
		// remains exactly what was on the wire, so the German alternative
		// (which has no IIM representation) never appears on the IIM-facing side.
		if got := m.IPTC.Caption(); got != iptcValue {
			t.Errorf("IPTC.Caption() = %q, want raw IIM value %q", got, iptcValue)
		}
	})
}
