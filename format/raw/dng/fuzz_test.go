package dng

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func FuzzDNGExtract(f *testing.F) {
	// Seed: minimal LE TIFF.
	minLE := make([]byte, 14)
	minLE[0], minLE[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(minLE[2:], 0x002A)
	binary.LittleEndian.PutUint32(minLE[4:], 8)
	f.Add(minLE)

	// Seed: minimal BE TIFF.
	minBE := make([]byte, 14)
	minBE[0], minBE[1] = 'M', 'M'
	binary.BigEndian.PutUint16(minBE[2:], 0x002A)
	binary.BigEndian.PutUint32(minBE[4:], 8)
	f.Add(minBE)

	// Seed: empty input.
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}

// FuzzDNGInject feeds arbitrary bytes as the source DNG container and
// asserts that Inject never panics — it must return an error or write valid
// output, never crash. DNG.Inject is a thin error-wrapping pass-through
// straight to tiff.Inject — DNG is standard-magic TIFF (Adobe DNG
// Specification 1.7) with no DNG-specific byte patching at this layer — so
// this target exercises the same exif.Parse/relocateTIFF machinery
// FuzzTIFFInject exercises, through the dng package's public entry point.
// This is also the copy-and-relocate SubIFD path (tag 0x014A) documented on
// Inject, which recursively follows SubIFDs and re-encodes offsets.
//
// Mirrors FuzzTIFFInject exactly: data is passed as BOTH the reader (r) and
// as rawEXIF, since a non-nil rawEXIF becomes the "base" buffer that
// relocateTIFF parses (r is seeked but never read in that branch). Feeding
// the fuzzer-controlled bytes into that path — rather than a fixed, unrelated
// payload — lets the fuzzer explore IFD cycle-detection (including cyclic
// SubIFD chains), field-width arithmetic, and encode round-tripping.
//
// preserveUnknownSegments is fixed at true, matching the tiff/webp/heif/png/
// jpeg Inject fuzzers (task #258).
func FuzzDNGInject(f *testing.F) {
	// Seed 1: minimal LE TIFF with 0-entry IFD0.
	minLE := make([]byte, 14)
	minLE[0], minLE[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(minLE[2:], 0x002A)
	binary.LittleEndian.PutUint32(minLE[4:], 8)
	f.Add(minLE)

	// Seed 2: minimal BE TIFF with 0-entry IFD0.
	minBE := make([]byte, 14)
	minBE[0], minBE[1] = 'M', 'M'
	binary.BigEndian.PutUint16(minBE[2:], 0x002A)
	binary.BigEndian.PutUint32(minBE[4:], 8)
	f.Add(minBE)

	// Seed 3: empty input — exercises the ErrFileTooShort path.
	f.Add([]byte{})

	// Seed 4: DNG/TIFF carrying the DNGVersion marker plus IPTC and XMP in
	// IFD0 (little-endian) — exercises IFD entry traversal with a mix of
	// inline and out-of-line tag values.
	f.Add(buildDNGWithIPTCAndXMP(
		binary.LittleEndian,
		[]byte("fuzz-dng-iptc-seed"),
		[]byte("<xmpmeta dng='1'/>"),
	))

	// Fixed metadata payloads for rawIPTC/rawXMP: short and constant so that
	// Inject reaches relocateTIFF on every iteration regardless of what the
	// fuzzer-controlled rawEXIF/base buffer contains.
	rawIPTC := []byte("fuzz-dng-iptc-data")
	rawXMP := []byte("<xmpmeta/>")

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug in the write path.
		err := Inject(bytes.NewReader(data), io.Discard, data, rawIPTC, rawXMP, true)
		_ = err
	})
}
