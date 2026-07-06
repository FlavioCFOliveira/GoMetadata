package cr2

import (
	"bytes"
	"io"
	"testing"
)

func FuzzCR2Extract(f *testing.F) {
	// Seed: minimal LE TIFF (same base as CR2 without the "CR" signature).
	f.Add(minimalTIFF())

	// Seed: minimal CR2 with "CR" signature at bytes 8-9.
	cr2 := minimalTIFF()
	cr2[8], cr2[9] = 'C', 'R'
	f.Add(cr2)

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated header.
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}

// FuzzCR2Inject feeds arbitrary bytes as the source CR2 container and asserts
// that Inject never panics — it must return an error or write valid output,
// never crash. CR2.Inject is a thin error-wrapping pass-through straight to
// tiff.Inject — CR2 is standard-magic TIFF with a "CR" marker at bytes 8-9
// that is opaque to the TIFF layer — so this target exercises the same
// exif.Parse/relocateTIFF machinery FuzzTIFFInject exercises, through the
// cr2 package's public entry point.
//
// Mirrors FuzzTIFFInject exactly: data is passed as BOTH the reader (r) and
// as rawEXIF, since a non-nil rawEXIF becomes the "base" buffer that
// relocateTIFF parses (r is seeked but never read in that branch). Feeding
// the fuzzer-controlled bytes into that path — rather than a fixed, unrelated
// payload — lets the fuzzer explore IFD cycle-detection, field-width
// arithmetic, and encode round-tripping.
//
// preserveUnknownSegments is fixed at true, matching the tiff/webp/heif/png/
// jpeg Inject fuzzers (task #258).
func FuzzCR2Inject(f *testing.F) {
	// Seed 1: minimal LE TIFF with 0-entry IFD0 (no "CR" marker).
	f.Add(minimalTIFF())

	// Seed 2: minimal CR2 with the "CR" signature at bytes 8-9.
	{
		cr2 := minimalTIFF()
		cr2[8], cr2[9] = 'C', 'R'
		f.Add(cr2)
	}

	// Seed 3: empty input — exercises the ErrFileTooShort path.
	f.Add([]byte{})

	// Seed 4: truncated header.
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	// Seed 5: CR2/TIFF carrying IPTC and XMP in IFD0 (with "CR" marker) —
	// exercises IFD entry traversal with existing out-of-line tag values.
	f.Add(buildCR2WithIPTCAndXMP(
		[]byte("fuzz-cr2-iptc-seed"),
		[]byte("<xmpmeta cr2='1'/>"),
	))

	// Fixed metadata payloads for rawIPTC/rawXMP: short and constant so that
	// Inject reaches relocateTIFF on every iteration regardless of what the
	// fuzzer-controlled rawEXIF/base buffer contains.
	rawIPTC := []byte("fuzz-cr2-iptc-data")
	rawXMP := []byte("<xmpmeta/>")

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug in the write path.
		err := Inject(bytes.NewReader(data), io.Discard, data, rawIPTC, rawXMP, true)
		_ = err
	})
}
