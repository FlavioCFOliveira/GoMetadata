package gometadata

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

// BenchmarkRead_JPEG measures the full top-level Read path for a JPEG:
// format detection → EXIF APP1 segment extraction → EXIF parse.
// Input is a minimal but structurally valid JPEG with one IFD0 entry so
// that the EXIF parser exercises its real hot path.
// No filesystem I/O is timed; the bytes.Reader wraps a pre-built in-memory
// slice that is constructed once outside the measured loop.
func BenchmarkRead_JPEG(b *testing.B) {
	data := buildMinimalJPEG(minimalTIFFPayload())
	// #236: the reader is constructed once outside the loop and rewound via
	// Seek per iteration so that b.N artificial bytes.Reader allocations do
	// not inflate the allocs/op reported for the library's own Read path.
	r := bytes.NewReader(data)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, _ = r.Seek(0, io.SeekStart)
		_, _ = Read(r)
	}
}

// BenchmarkRead_JPEG_WithXMP exercises the full Read path for a JPEG that
// carries both IPTC (APP13) and XMP (APP1) segments in addition to EXIF.
// This variant covers the multi-segment dispatch and the XMP packet scanner.
func BenchmarkRead_JPEG_WithXMP(b *testing.B) {
	data := buildJPEGWithIPTCAndXMP("A benchmark IPTC caption", "A benchmark XMP caption")
	// #236: reader hoisted outside the loop; see BenchmarkRead_JPEG.
	r := bytes.NewReader(data)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, _ = r.Seek(0, io.SeekStart)
		_, _ = Read(r)
	}
}

// buildJPEGWithExtendedXMP builds a JPEG carrying a main XMP APP1 (with an
// attribute-form xmpNote:HasExtendedXMP GUID) and a single extended XMP APP1
// chunk holding a complete, independently parseable XMP document. Both
// packets are well-formed enough that reassembleExtendedXMPByParse's parse +
// merge + re-encode path runs (rather than the byte-splice fallback), which
// is the reassembly cost task #238 lets WithoutXMP skip.
func buildJPEGWithExtendedXMP(guid string) []byte {
	const identXMP = "http://ns.adobe.com/xap/1.0/\x00"
	const identXMPNote = "http://ns.adobe.com/xmp/extension/\x00"

	mainXMP := `<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
		` xmpNote:HasExtendedXMP="` + guid + `"/>` +
		`</rdf:RDF></x:xmpmeta>` +
		`<?xpacket end="w"?>`

	extXMP := `<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:dc="http://purl.org/dc/elements/1.1/"` +
		` dc:title="Extended benchmark title"` +
		` dc:description="Extended benchmark description"/>` +
		`</rdf:RDF></x:xmpmeta>` +
		`<?xpacket end="w"?>`

	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	mainPayload := append([]byte(identXMP), mainXMP...)
	writeAPP1(&buf, mainPayload)

	extBody := make([]byte, 0, len(identXMPNote)+32+4+4+len(extXMP))
	extBody = append(extBody, identXMPNote...)
	extBody = append(extBody, guid...)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(extXMP))) //nolint:gosec // G115: test helper, intentional type cast
	extBody = append(extBody, lenBuf[:]...)
	binary.BigEndian.PutUint32(lenBuf[:], 0) // single chunk, offset 0
	extBody = append(extBody, lenBuf[:]...)
	extBody = append(extBody, extXMP...)
	writeAPP1(&buf, extBody)

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // minimal SOS + EOI
	return buf.Bytes()
}

// writeAPP1 appends one length-prefixed APP1 (0xFFE1) segment to buf.
func writeAPP1(buf *bytes.Buffer, payload []byte) {
	length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, intentional type cast
	buf.Write([]byte{0xFF, 0xE1})
	var lbuf [2]byte
	binary.BigEndian.PutUint16(lbuf[:], length)
	buf.Write(lbuf[:])
	buf.Write(payload)
}

// BenchmarkRead_JPEG_ExtendedXMP measures the default Read path for a JPEG
// carrying multi-segment extended XMP (Adobe XMP Specification Part 3
// §1.1.4), which requires reassembling the main and extended packets.
// See BenchmarkRead_JPEG_ExtendedXMP_WithoutXMP (task #238) for the
// WithoutXMP() variant that skips this reassembly.
func BenchmarkRead_JPEG_ExtendedXMP(b *testing.B) {
	data := buildJPEGWithExtendedXMP("04B9E48040A30A6308713BD1E4223B41")
	r := bytes.NewReader(data)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, _ = r.Seek(0, io.SeekStart)
		_, _ = Read(r)
	}
}

// BenchmarkRead_JPEG_ExtendedXMP_WithoutXMP is the WithoutXMP() counterpart
// of BenchmarkRead_JPEG_ExtendedXMP (task #238): a caller that opts out of
// XMP must not pay for the extended-XMP reassembly (packet parse, property
// merge, re-encode) that only feeds m.XMP and RawXMP().
func BenchmarkRead_JPEG_ExtendedXMP_WithoutXMP(b *testing.B) {
	data := buildJPEGWithExtendedXMP("04B9E48040A30A6308713BD1E4223B41")
	r := bytes.NewReader(data)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, _ = r.Seek(0, io.SeekStart)
		_, _ = Read(r, WithoutXMP())
	}
}

// BenchmarkRawSegments proves the #239 zero-copy accessor performs 0
// allocs/op, unlike RawEXIF/RawIPTC/RawXMP which each clone their segment.
func BenchmarkRawSegments(b *testing.B) {
	data := buildJPEGWithIPTCAndXMP("A benchmark IPTC caption", "A benchmark XMP caption")
	m, err := Read(bytes.NewReader(data))
	if err != nil {
		b.Fatalf("Read: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, _, _ = m.RawSegments()
	}
}

// BenchmarkRead_PNG measures the full top-level Read path for a PNG:
// format detection → PNG chunk scan → no metadata found (pass-through).
// The minimal PNG (IHDR + IEND) exercises the chunk iterator without any
// metadata payload so that the baseline cost of format detection and chunk
// traversal is isolated.
func BenchmarkRead_PNG(b *testing.B) {
	data := buildMinimalPNG()
	// #236: reader hoisted outside the loop; see BenchmarkRead_JPEG.
	r := bytes.NewReader(data)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, _ = r.Seek(0, io.SeekStart)
		_, _ = Read(r)
	}
}

// BenchmarkMWGAccessors measures the cost of repeatedly calling the four
// MWG-02 digest-conditioned accessors (Copyright, Caption, Keywords, Creator)
// on a Metadata whose IPTC digest resource (Photoshop 0x0425) is present and
// mismatches the current IPTC block. Before task #208 every one of these
// four calls independently recomputed an MD5 digest of the entire raw IPTC
// IIM stream (via iptcTrustElevated → iptc.DigestMatch); after #208 the
// digest-elevation decision is computed once, by Read, and cached, so all
// four calls (times b.N) perform at most one MD5 in total for the whole
// benchmark run.
func BenchmarkMWGAccessors(b *testing.B) {
	wrongDigest := make([]byte, 16)
	for idx := range wrongDigest {
		wrongDigest[idx] = 0xAB
	}
	jpegBytes := buildMWG02JPEG("IPTC caption", "XMP caption", wrongDigest)
	m, err := Read(bytes.NewReader(jpegBytes))
	if err != nil {
		b.Fatalf("Read: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = m.Copyright()
		_ = m.Caption()
		_ = m.Keywords()
		_ = m.Creator()
	}
}
