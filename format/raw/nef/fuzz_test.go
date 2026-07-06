package nef

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func FuzzNEFExtract(f *testing.F) {
	// Seed: minimal LE TIFF.
	f.Add(minimalTIFF())

	// Seed: minimal BE TIFF (Nikon D1 used big-endian).
	minBE := make([]byte, 14)
	minBE[0], minBE[1] = 'M', 'M'
	minBE[2], minBE[3] = 0x00, 0x2A
	minBE[4], minBE[5], minBE[6], minBE[7] = 0x00, 0x00, 0x00, 0x08
	f.Add(minBE)

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated header.
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}

// FuzzNEFInject feeds arbitrary bytes as the source NEF container and
// asserts that Inject never panics — it must return an error or write valid
// output, never crash. NEF.Inject is a thin error-wrapping pass-through
// straight to tiff.Inject — NEF is standard-magic TIFF (both LE for modern
// Nikon D-SLRs and BE for the early Nikon D1) with no NEF-specific byte
// patching at this layer — so this target exercises the same
// exif.Parse/relocateTIFF machinery FuzzTIFFInject exercises, through the
// nef package's public entry point.
//
// Mirrors FuzzTIFFInject exactly: data is passed as BOTH the reader (r) and
// as rawEXIF, since a non-nil rawEXIF becomes the "base" buffer that
// relocateTIFF parses (r is seeked but never read in that branch). Feeding
// the fuzzer-controlled bytes into that path — rather than a fixed, unrelated
// payload — lets the fuzzer explore both byte orders' IFD cycle-detection,
// field-width arithmetic, and encode round-tripping.
//
// preserveUnknownSegments is fixed at true, matching the tiff/webp/heif/png/
// jpeg Inject fuzzers (task #258).
func FuzzNEFInject(f *testing.F) {
	// Seed 1: minimal LE TIFF with 0-entry IFD0.
	f.Add(minimalTIFF())

	// Seed 2: minimal BE TIFF with 0-entry IFD0 (Nikon D1 byte order).
	{
		minBE := make([]byte, 14)
		minBE[0], minBE[1] = 'M', 'M'
		minBE[2], minBE[3] = 0x00, 0x2A
		minBE[4], minBE[5], minBE[6], minBE[7] = 0x00, 0x00, 0x00, 0x08
		f.Add(minBE)
	}

	// Seed 3: empty input — exercises the ErrFileTooShort path.
	f.Add([]byte{})

	// Seed 4: truncated header.
	f.Add([]byte{'I', 'I', 0x2A, 0x00})

	// Seed 5: NEF/TIFF carrying IPTC and XMP in IFD0 (little-endian) —
	// exercises IFD entry traversal with existing out-of-line tag values.
	f.Add(buildNEFWithIPTCAndXMP(
		binary.LittleEndian,
		[]byte("fuzz-nef-iptc-seed"),
		[]byte("<xmpmeta nef='1'/>"),
	))

	// Seed 6: same, big-endian — exercises the BE code path with IPTC/XMP.
	f.Add(buildNEFWithIPTCAndXMP(
		binary.BigEndian,
		[]byte("fuzz-nef-iptc-be"),
		[]byte("<xmpmeta nef='be'/>"),
	))

	// Fixed metadata payloads for rawIPTC/rawXMP: short and constant so that
	// Inject reaches relocateTIFF on every iteration regardless of what the
	// fuzzer-controlled rawEXIF/base buffer contains.
	rawIPTC := []byte("fuzz-nef-iptc-data")
	rawXMP := []byte("<xmpmeta/>")

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug in the write path.
		err := Inject(bytes.NewReader(data), io.Discard, data, rawIPTC, rawXMP, true)
		_ = err
	})
}
