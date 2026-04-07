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
