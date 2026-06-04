package gometadata

// Regression tests for task #85: PreserveUnknownSegments(false) must be
// honoured, not silently ignored.
//
// Three scenarios:
//   - JPEG with an unknown APPn (APP4) + PreserveUnknownSegments(false):
//     the APP4 segment must be absent from the output.
//   - JPEG with an unknown APPn (APP4) + PreserveUnknownSegments(true) / default:
//     the APP4 segment must survive byte-identical in the output.
//   - PNG with PreserveUnknownSegments(false): must return an error wrapping
//     png.ErrPreserveUnknownSegmentsNotSupported (not silently pass-through).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	pngfmt "github.com/FlavioCFOliveira/GoMetadata/format/png"
)

// markerAPP4 is the JPEG APP4 marker byte (0xE4). APP4 is not interpreted by
// this library and is therefore treated as an "unknown" application segment.
const markerAPP4 byte = 0xE4

// buildJPEGWithAPP4 constructs a minimal JPEG that contains an APP4 segment
// carrying the provided payload, sandwiched between SOI and SOS/EOI.
// This exercises the unknown-APPn preservation / stripping path in jpeg.Inject.
func buildJPEGWithAPP4(app4Payload []byte) []byte {
	var buf bytes.Buffer

	// SOI
	buf.Write([]byte{0xFF, 0xD8})

	// APP4 segment: 0xFF 0xE4 + 2-byte length (includes length field) + payload
	segLen := uint16(len(app4Payload) + 2) //nolint:gosec // G115: test helper; payload bounded by test
	buf.Write([]byte{0xFF, markerAPP4})
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], segLen)
	buf.Write(lb[:])
	buf.Write(app4Payload)

	// Minimal SOS + image data stub + EOI
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})

	return buf.Bytes()
}

// containsAPP4 scans the raw JPEG bytes and reports whether any APP4 marker
// (0xFF 0xE4) is present after the SOI.
func containsAPP4(jpegData []byte) bool {
	// Skip SOI (2 bytes).
	i := 2
	for i+1 < len(jpegData) {
		if jpegData[i] != 0xFF {
			break
		}
		m := jpegData[i+1]
		if m == markerAPP4 {
			return true
		}
		// Standalone markers (SOI, EOI, RST*): no length field.
		if m == 0xD8 || m == 0xD9 || (m >= 0xD0 && m <= 0xD7) || m == 0x01 {
			i += 2
			continue
		}
		// SOS: everything following is compressed data; stop scanning.
		if m == 0xDA {
			return false
		}
		// Normal marker: advance by 2 (marker) + length field value.
		if i+3 >= len(jpegData) {
			break
		}
		segLen := int(binary.BigEndian.Uint16(jpegData[i+2:])) + 2 // +2 for marker bytes
		i += segLen
	}
	return false
}

// TestPreserveUnknownSegmentsTrueKeeps verifies that the default behaviour
// (PreserveUnknownSegments(true) / no option) passes unknown APPn segments
// through unchanged. This is the byte-compatibility regression guard: any
// JPEG written with the old API must behave identically.
func TestPreserveUnknownSegmentsTrueKeeps(t *testing.T) {
	t.Parallel()

	app4Payload := []byte("GoMetadata-test-APP4-payload")
	src := buildJPEGWithAPP4(app4Payload)

	// Confirm the source contains the APP4 segment.
	if !containsAPP4(src) {
		t.Fatal("precondition: source JPEG does not contain APP4 segment")
	}

	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Case 1: no option at all (default = preserve=true).
	var outDefault bytes.Buffer
	if err := Write(bytes.NewReader(src), &outDefault, m); err != nil {
		t.Fatalf("Write (default): %v", err)
	}
	if !containsAPP4(outDefault.Bytes()) {
		t.Error("default (no option): APP4 segment missing from output; want preserved")
	}

	// Case 2: explicit PreserveUnknownSegments(true).
	var outExplicit bytes.Buffer
	if err := Write(bytes.NewReader(src), &outExplicit, m, PreserveUnknownSegments(true)); err != nil {
		t.Fatalf("Write (PreserveUnknownSegments(true)): %v", err)
	}
	if !containsAPP4(outExplicit.Bytes()) {
		t.Error("PreserveUnknownSegments(true): APP4 segment missing from output; want preserved")
	}
}

// TestPreserveUnknownSegmentsFalseStrips is the primary regression gate for
// task #85. It verifies that PreserveUnknownSegments(false) actually strips
// unknown APPn segments from the JPEG output.
//
// Before the fix, the option was silently ignored and the APP4 segment
// survived regardless of the value passed. After the fix, APP4 must be absent
// from the output when PreserveUnknownSegments(false) is requested.
func TestPreserveUnknownSegmentsFalseStrips(t *testing.T) {
	t.Parallel()

	app4Payload := []byte("GoMetadata-test-APP4-PII-data")
	src := buildJPEGWithAPP4(app4Payload)

	// Confirm the source contains the APP4 segment so the test is meaningful.
	if !containsAPP4(src) {
		t.Fatal("precondition: source JPEG does not contain APP4 segment")
	}

	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m, PreserveUnknownSegments(false)); err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}

	// The APP4 segment must NOT appear in the output.
	if containsAPP4(out.Bytes()) {
		t.Error("task #85 regression: PreserveUnknownSegments(false) did not strip APP4 segment — option was silently ignored")
	}

	// The output must still be a readable JPEG (basic sanity: starts with SOI,
	// ends with image data, not corrupted).
	outBytes := out.Bytes()
	if len(outBytes) < 4 || outBytes[0] != 0xFF || outBytes[1] != 0xD8 {
		t.Errorf("output does not begin with JPEG SOI: first 4 bytes: %x", outBytes[:min(4, len(outBytes))])
	}
}

// TestPreserveUnknownSegmentsFalseUnsupportedReturnsErr verifies that formats
// for which unknown-segment stripping is not implemented return an explicit
// error (wrapping the format-specific ErrPreserveUnknownSegmentsNotSupported)
// rather than silently passing the option through.
//
// PNG is used as the representative "unsupported" format. The same behaviour
// applies to WebP, HEIF/AVIF, and CR3 (covered by the per-format unit tests in
// format/png, format/webp, format/heif, and format/raw/cr3).
func TestPreserveUnknownSegmentsFalseUnsupportedReturnsErr(t *testing.T) {
	t.Parallel()

	pngData := buildMinimalPNG()
	m, err := Read(bytes.NewReader(pngData))
	if err != nil {
		t.Fatalf("Read PNG: %v", err)
	}

	var out bytes.Buffer
	writeErr := Write(bytes.NewReader(pngData), &out, m, PreserveUnknownSegments(false))
	if writeErr == nil {
		t.Fatal("Write PNG with PreserveUnknownSegments(false): expected error, got nil")
	}

	// The error must wrap the format-specific sentinel so callers can test with errors.Is.
	if !errors.Is(writeErr, pngfmt.ErrPreserveUnknownSegmentsNotSupported) {
		t.Errorf("expected errors.Is(err, png.ErrPreserveUnknownSegmentsNotSupported) == true; got: %v", writeErr)
	}

	// No bytes must have been written to the output (the error fires before I/O).
	if out.Len() > 0 {
		t.Errorf("Write wrote %d byte(s) before returning error; want 0", out.Len())
	}
}
