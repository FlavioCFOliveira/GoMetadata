package arw

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func FuzzARWExtract(f *testing.F) {
	// Seed: minimal LE TIFF.
	minLE := make([]byte, 14)
	minLE[0], minLE[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(minLE[2:], 0x002A)
	binary.LittleEndian.PutUint32(minLE[4:], 8)
	f.Add(minLE)

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated header.
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}

// FuzzARWInject feeds arbitrary bytes as the source ARW container and asserts
// that Inject never panics — it must return an error or write valid output,
// never crash. ARW.Inject is a thin error-wrapping pass-through straight to
// tiff.Inject — ARW uses standard TIFF magic with no Sony-specific byte
// patching at this layer (unlike orf/rw2) — so this target must exercise the
// same exif.Parse/relocateTIFF machinery FuzzTIFFInject exercises, through
// the arw package's public entry point.
//
// Mirrors FuzzTIFFInject exactly: data is passed as BOTH the reader (r) and
// as rawEXIF. Inside tiff.Inject, a non-nil rawEXIF becomes the "base" TIFF
// buffer that relocateTIFF parses and rebuilds (see tiff.go: "base = rawEXIF"
// when rawEXIF != nil) — r is seeked but never read in that branch. Passing a
// fixed, unrelated rawEXIF here would starve the fuzzer of any influence over
// the interesting code path; feeding it the mutated bytes instead lets the
// fuzzer explore IFD cycle-detection, field-width arithmetic, and encode
// round-tripping exactly as FuzzTIFFInject does.
//
// preserveUnknownSegments is fixed at true, matching the tiff/webp/heif/png/
// jpeg Inject fuzzers (task #258).
func FuzzARWInject(f *testing.F) {
	// Seed 1: minimal LE TIFF with 0-entry IFD0 — Inject with fixed IPTC/XMP
	// must call relocateTIFF and succeed (empty IFD0, no cycle, valid header).
	f.Add(minimalTIFF())

	// Seed 2: empty input — exercises the ErrFileTooShort path.
	f.Add([]byte{})

	// Seed 3: truncated header — 4 bytes (valid byte-order mark, no magic/offset).
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	// Seed 4: ARW/TIFF carrying IPTC and XMP in IFD0 — exercises IFD entry
	// traversal with existing out-of-line tag values present.
	f.Add(buildARWWithIPTCAndXMP(
		[]byte("fuzz-arw-iptc-seed"),
		[]byte("<xmpmeta arw='1'/>"),
	))

	// Fixed metadata payloads for rawIPTC/rawXMP: short and constant so that
	// Inject reaches relocateTIFF on every iteration regardless of what the
	// fuzzer-controlled rawEXIF/base buffer contains.
	rawIPTC := []byte("fuzz-arw-iptc-data")
	rawXMP := []byte("<xmpmeta/>")

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug in the write path.
		err := Inject(bytes.NewReader(data), io.Discard, data, rawIPTC, rawXMP, true)
		_ = err
	})
}
