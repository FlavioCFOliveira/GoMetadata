package gometadata

import (
	"bytes"
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
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, _ = Read(bytes.NewReader(data))
	}
}

// BenchmarkRead_JPEG_WithXMP exercises the full Read path for a JPEG that
// carries both IPTC (APP13) and XMP (APP1) segments in addition to EXIF.
// This variant covers the multi-segment dispatch and the XMP packet scanner.
func BenchmarkRead_JPEG_WithXMP(b *testing.B) {
	data := buildJPEGWithIPTCAndXMP("A benchmark IPTC caption", "A benchmark XMP caption")
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, _ = Read(bytes.NewReader(data))
	}
}

// BenchmarkRead_PNG measures the full top-level Read path for a PNG:
// format detection → PNG chunk scan → no metadata found (pass-through).
// The minimal PNG (IHDR + IEND) exercises the chunk iterator without any
// metadata payload so that the baseline cost of format detection and chunk
// traversal is isolated.
func BenchmarkRead_PNG(b *testing.B) {
	data := buildMinimalPNG()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()

	for range b.N {
		_, _ = Read(bytes.NewReader(data))
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
