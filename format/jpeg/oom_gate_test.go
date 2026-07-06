package jpeg

// oom_gate_test.go — regression gate for #262: format/jpeg was the one
// container package that lacked the project-wide aggregate #140-style
// maxFileSize cap enforced by every sibling format (webp, tiff, heif, dng,
// cr2, cr3, nef, arw, orf, rw2). This is a defense-in-depth hardening, not a
// vulnerability fix: every JPEG APPn segment is already bounded to 65535
// bytes by the 16-bit length field (ISO/IEC 10918-1 §B.1.1.4), so the
// pre-existing streaming design carried no per-segment amplification risk.
//
// These tests verify that Extract, ExtractFull, and Inject reject inputs
// whose cumulative bytes exceed maxFileSize with ErrFileTooLarge, that a
// flood of many (individually small) Photoshop APP13 segments whose
// aggregate size exceeds the cap is independently rejected, and that a
// normal-sized JPEG round-trips byte-for-byte identically regardless of
// whether maxFileSize has been (harmlessly) lowered.
//
// The tests lower maxFileSize to a small value for the size-cap paths and
// restore it via t.Cleanup so the production default (256 MiB) is never
// changed across the test suite. No 256 MiB allocation is ever performed.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
)

// capBytesOOM is the small cap used by the plain oversize-input tests.
const capBytesOOM = 64

// setMaxFileSizeForTest temporarily replaces the package-level maxFileSize
// with cap and registers a t.Cleanup to restore the original value. It must
// not be called from parallel sub-tests that share a package-level variable.
func setMaxFileSizeForTest(t *testing.T, cap int64) {
	t.Helper()
	orig := maxFileSize
	maxFileSize = cap
	t.Cleanup(func() { maxFileSize = orig })
}

// buildOversizeJPEG builds a structurally well-formed JPEG (valid SOI, one
// APP1/EXIF segment, SOS, EOI) whose single APP1 payload is fillerLen bytes —
// large enough to exceed a small test maxFileSize on its own, so the
// countingReader size cap is exercised without first tripping any unrelated
// marker-validation error. Every marker in the stream is well-formed.
func buildOversizeJPEG(fillerLen int) []byte {
	filler := bytes.Repeat([]byte{0xAB}, fillerLen)
	return buildJPEG(filler, nil, nil)
}

// buildJPEGWithAPP13Flood builds a JPEG carrying count consecutive Photoshop
// APP13 segments, each wrapping a payloadLen-byte filler body (deliberately
// NOT a well-formed 8BIM resource — the aggregate-size cap in
// scanMetadataSegmentsWithWire / extractOriginalIRB triggers purely on raw
// segment byte length, before any 8BIM/resource parsing takes place). Every
// individual segment is far under the 65535-byte APP segment limit; only the
// SUM of all segments' stripped IRB payloads is intended to exceed the test
// cap (IRB-APP13-09: multiple Photoshop APP13 payloads are concatenated).
func buildJPEGWithAPP13Flood(payloadLen, count int) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	payload := bytes.Repeat([]byte{0xCD}, payloadLen)
	for range count {
		var irb bytes.Buffer
		irb.WriteString("Photoshop 3.0\x00")
		irb.Write(payload)

		length := uint16(irb.Len() + 2) //nolint:gosec // G115: test helper, bounded by construction
		buf.Write([]byte{0xFF, 0xED})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], length)
		buf.Write(lbuf[:])
		buf.Write(irb.Bytes())
	}

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // minimal SOS + EOI
	return buf.Bytes()
}

// TestExtractFileTooLarge verifies that Extract returns ErrFileTooLarge when
// the cumulative bytes read from a well-formed JPEG exceed maxFileSize.
//
// Gate for #262 (format/jpeg lacked the project-wide aggregate size cap).
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractFileTooLarge(t *testing.T) {
	setMaxFileSizeForTest(t, capBytesOOM)

	data := buildOversizeJPEG(capBytesOOM*4 + 1)
	_, _, _, err := Extract(bytes.NewReader(data))
	if err == nil {
		t.Fatal("Extract: expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Extract: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestExtractFullFileTooLarge verifies that ExtractFull returns
// ErrFileTooLarge for the same oversized input as TestExtractFileTooLarge.
//
// Gate for #262.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractFullFileTooLarge(t *testing.T) {
	setMaxFileSizeForTest(t, capBytesOOM)

	data := buildOversizeJPEG(capBytesOOM*4 + 1)
	_, _, _, _, _, _, err := ExtractFull(bytes.NewReader(data))
	if err == nil {
		t.Fatal("ExtractFull: expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("ExtractFull: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestInjectFileTooLarge verifies that Inject returns ErrFileTooLarge when
// the source reader exceeds maxFileSize, and that the passthrough io.Copy of
// compressed image data (writeSOS) is likewise bounded rather than copying an
// oversized body unbounded.
//
// Gate for #262.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectFileTooLarge(t *testing.T) {
	setMaxFileSizeForTest(t, capBytesOOM)

	data := buildOversizeJPEG(capBytesOOM*4 + 1)
	err := Inject(bytes.NewReader(data), io.Discard, nil, nil, nil, true)
	if err == nil {
		t.Fatal("Inject: expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Inject: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestExtractAPP13FloodExceedsAggregateCap verifies that a flood of many
// Photoshop APP13 segments — each individually far under the 65535-byte
// per-segment limit — is rejected once their aggregate accumulated size
// exceeds maxFileSize, independent of the byte-for-byte countingReader cap.
//
// Gate for #262 ("Also independently bound the aggregate app13Payloads
// accumulation so a flood of APP13 segments cannot accumulate more than the
// cap").
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractAPP13FloodExceedsAggregateCap(t *testing.T) {
	// Two 1000-byte APP13 payloads (2000 bytes aggregate) against a
	// 1500-byte cap: individually each segment is tiny relative to the
	// 65535-byte APP segment limit, but the pair's combined stripped-IRB
	// size (2000 bytes) exceeds the lowered cap.
	setMaxFileSizeForTest(t, 1500)

	data := buildJPEGWithAPP13Flood(1000, 2)
	_, _, _, err := Extract(bytes.NewReader(data))
	if err == nil {
		t.Fatal("Extract: expected error for APP13 flood exceeding aggregate cap, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Extract: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestInjectAPP13FloodExceedsAggregateCap mirrors
// TestExtractAPP13FloodExceedsAggregateCap for Inject's IRB pre-scan
// (extractOriginalIRB), which independently accumulates and bounds the same
// aggregate APP13 total.
//
// Gate for #262.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectAPP13FloodExceedsAggregateCap(t *testing.T) {
	setMaxFileSizeForTest(t, 1500)

	data := buildJPEGWithAPP13Flood(1000, 2)
	err := Inject(bytes.NewReader(data), io.Discard, nil, nil, nil, true)
	if err == nil {
		t.Fatal("Inject: expected error for APP13 flood exceeding aggregate cap, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Inject: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestExtractPositiveControlByteIdentical verifies that a normal small JPEG
// yields identical Extract results whether maxFileSize is left at its
// production default or temporarily lowered to a value still far above the
// input's size — proving the countingReader wrapper introduces no behavioral
// change on the in-spec fast path.
//
// Positive control for #262.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractPositiveControlByteIdentical(t *testing.T) {
	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	src := buildJPEG(tiffData, iptcData, nil)

	rawEXIFDefault, rawIPTCDefault, rawXMPDefault, err := Extract(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Extract (default cap): unexpected error: %v", err)
	}

	// len(src) is a few dozen bytes; 4096 is still three orders of magnitude
	// larger, so this in no way approximates the size-cap paths above.
	setMaxFileSizeForTest(t, 4096)

	rawEXIFLowered, rawIPTCLowered, rawXMPLowered, err := Extract(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Extract (lowered cap): unexpected error: %v", err)
	}

	if !bytes.Equal(rawEXIFDefault, rawEXIFLowered) {
		t.Errorf("rawEXIF differs between default and lowered maxFileSize:\ndefault: %x\nlowered: %x", rawEXIFDefault, rawEXIFLowered)
	}
	if !bytes.Equal(rawIPTCDefault, rawIPTCLowered) {
		t.Errorf("rawIPTC differs between default and lowered maxFileSize:\ndefault: %x\nlowered: %x", rawIPTCDefault, rawIPTCLowered)
	}
	if !bytes.Equal(rawXMPDefault, rawXMPLowered) {
		t.Errorf("rawXMP differs between default and lowered maxFileSize:\ndefault: %x\nlowered: %x", rawXMPDefault, rawXMPLowered)
	}
	if rawEXIFDefault == nil {
		t.Error("rawEXIF is nil; positive control expected non-nil EXIF payload")
	}
}

// TestInjectPositiveControlByteIdentical verifies that Inject produces
// byte-for-byte identical output for a normal small JPEG whether maxFileSize
// is left at its production default or temporarily lowered to a value still
// far above the input's size.
//
// Positive control for #262.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectPositiveControlByteIdentical(t *testing.T) {
	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	src := buildJPEG(tiffData, iptcData, nil)
	newIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'N', 'e', 'w'}

	var outDefault bytes.Buffer
	if err := Inject(bytes.NewReader(src), &outDefault, tiffData, newIPTC, nil, true); err != nil {
		t.Fatalf("Inject (default cap): unexpected error: %v", err)
	}

	setMaxFileSizeForTest(t, 4096)

	var outLowered bytes.Buffer
	if err := Inject(bytes.NewReader(src), &outLowered, tiffData, newIPTC, nil, true); err != nil {
		t.Fatalf("Inject (lowered cap): unexpected error: %v", err)
	}

	if !bytes.Equal(outDefault.Bytes(), outLowered.Bytes()) {
		t.Fatalf("Inject output differs between default and lowered maxFileSize:\ndefault: %x\nlowered: %x",
			outDefault.Bytes(), outLowered.Bytes())
	}
	if outDefault.Len() == 0 {
		t.Error("Inject output is empty; positive control expected a non-empty JPEG")
	}
}

// buildJPEGWithLargeBody builds a JPEG consisting only of SOI, an empty-data
// SOS segment, and bodyLen bytes of trailing "compressed image data" (never
// interpreted, since writeSOS/io.Copy treats everything after SOS as an
// opaque byte stream through to EOF). No EOI is required: io.Copy copies
// until the underlying reader is exhausted regardless of marker content.
func buildJPEGWithLargeBody(bodyLen int) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})             // SOI
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02}) // SOS, zero-length scan header
	buf.Write(bytes.Repeat([]byte{0x42}, bodyLen))
	return buf.Bytes()
}

// TestInjectBoundsPassthroughBodyCopy verifies that writeSOS's io.Copy(w, r)
// of the compressed image data is interrupted once the cumulative bytes
// copied exceed maxFileSize, rather than copying an oversized body to
// completion. This specifically exercises countingReader.WriteTo's
// remainingFitsBudget guard: *bytes.Reader implements io.WriterTo, whose
// WriteTo call is otherwise a single, uninterruptible copy of everything
// remaining.
//
// Gate for #262 ("The JPEG passthrough io.Copy(w, r) ... must remain bounded
// too — a >256 MiB body must not be copied unbounded").
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectBoundsPassthroughBodyCopy(t *testing.T) {
	const cap0 = 1024
	const bodyLen = 65536 // far larger than cap0
	setMaxFileSizeForTest(t, cap0)

	data := buildJPEGWithLargeBody(bodyLen)
	var out bytes.Buffer
	err := Inject(bytes.NewReader(data), &out, nil, nil, nil, true)
	if err == nil {
		t.Fatal("Inject: expected error for oversized passthrough body, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Inject: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
	// The copy must have been interrupted well before reaching bodyLen bytes;
	// otherwise the passthrough is effectively unbounded.
	if out.Len() >= bodyLen {
		t.Errorf("Inject: passthrough body copy was not bounded: wrote %d bytes (body was %d)", out.Len(), bodyLen)
	}
}

// seekOnlyReader wraps a *bytes.Reader but deliberately does NOT expose the
// underlying WriteTo method, so it satisfies io.ReadSeeker without
// satisfying io.WriterTo. Used to exercise countingReader.WriteTo's fallback
// branch (io.Copy's generic algorithm via countingReader.Read), which is
// otherwise never reached in this test suite because bytes.NewReader — used
// by virtually every other test — always implements io.WriterTo.
type seekOnlyReader struct {
	r *bytes.Reader
}

// Read must return the underlying error (typically io.EOF) unwrapped, for
// the same reason documented on countingReader.Read: io.ReadFull compares it
// to io.EOF with == internally.
func (s *seekOnlyReader) Read(p []byte) (int, error) {
	return s.r.Read(p) //nolint:wrapcheck // io.Reader contract: preserve io.EOF identity, see comment above
}

func (s *seekOnlyReader) Seek(offset int64, whence int) (int64, error) {
	pos, err := s.r.Seek(offset, whence)
	if err != nil {
		return pos, fmt.Errorf("seekOnlyReader: seek: %w", err)
	}
	return pos, nil
}

// TestInjectWriteToFallbackPath verifies that Inject produces correct output
// when the source reader does not implement io.WriterTo, exercising
// countingReader.WriteTo's generic io.Copy fallback branch, and that the
// fallback still enforces the aggregate size cap on an oversized body.
//
// Gate for #262 (countingReader.WriteTo fallback branch coverage).
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectWriteToFallbackPath(t *testing.T) {
	tiffData := minimalTIFFBytes()
	src := buildJPEG(tiffData, nil, nil)

	t.Run("normal_size_correct_output", func(t *testing.T) {
		var outDirect, outFallback bytes.Buffer
		if err := Inject(bytes.NewReader(src), &outDirect, tiffData, nil, nil, true); err != nil {
			t.Fatalf("Inject (direct *bytes.Reader): unexpected error: %v", err)
		}
		if err := Inject(&seekOnlyReader{r: bytes.NewReader(src)}, &outFallback, tiffData, nil, nil, true); err != nil {
			t.Fatalf("Inject (seekOnlyReader fallback): unexpected error: %v", err)
		}
		if !bytes.Equal(outDirect.Bytes(), outFallback.Bytes()) {
			t.Fatalf("Inject output differs between WriteTo fast path and fallback:\nfast path: %x\nfallback:  %x",
				outDirect.Bytes(), outFallback.Bytes())
		}
	})

	t.Run("oversized_body_rejected", func(t *testing.T) {
		setMaxFileSizeForTest(t, 1024)
		data := buildJPEGWithLargeBody(65536)
		var out bytes.Buffer
		err := Inject(&seekOnlyReader{r: bytes.NewReader(data)}, &out, nil, nil, nil, true)
		if err == nil {
			t.Fatal("Inject: expected error for oversized passthrough body via fallback path, got nil")
		}
		if !errors.Is(err, ErrFileTooLarge) {
			t.Errorf("Inject: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
		}
		if out.Len() >= 65536 {
			t.Errorf("Inject: fallback passthrough body copy was not bounded: wrote %d bytes", out.Len())
		}
	})
}
