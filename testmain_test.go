package gometadata

// testmain_test.go — TestMain for the gometadata package.
//
// TestMain generates the synthetic JPEG fixtures in testdata/fixtures/ that
// the Example tests in example_test.go require. This ensures that the core
// API Examples run in CI without requiring the optional testdata/corpus download.
//
// DESIGN POLICY (see docs/TESTING.md):
//   - Fixtures are generated deterministically from the library's own API plus
//     direct TIFF binary construction for metadata that lacks a public Set* method.
//   - Critical example outputs are expected values hard-coded in example_test.go.
//   - The corpus (testdata/corpus/**) remains optional — corpus tests skip when
//     absent but must pass when present.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// fixturesDir is the path to the generated fixtures directory.
const fixturesDir = "testdata/fixtures"

func TestMain(m *testing.M) {
	// Generate fixtures (idempotent — existing files are not overwritten).
	if err := os.MkdirAll(fixturesDir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "testmain: mkdir %s: %v\n", fixturesDir, err)
		os.Exit(1)
	}
	if err := generateFixtures(); err != nil {
		fmt.Fprintf(os.Stderr, "testmain: generateFixtures: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// generateFixtures creates all synthetic JPEG fixtures needed by the Example
// tests. Files that already exist (e.g. BigTIFF_LE.tif committed to the repo)
// are left unchanged.
func generateFixtures() error {
	// BigTIFF fixtures are committed directly to testdata/fixtures/ — no generation.

	// Synthetic JPEG fixtures generated from the library API.
	type fixture struct {
		name string
		fn   func() ([]byte, error)
	}
	fixtures := []fixture{
		{"exif-samples-11-tests.jpg", buildFixture11Tests},
		{"canon_hdr_YES.jpg", buildFixtureCanonHDR},
		{"jolla.jpg", buildFixtureJolla},
		{"67-0_length_string.jpg", buildFixtureAltitude},
		{"IPTC-PhotometadataRef-Std2021.1.jpg", buildFixtureIPTCRef},
	}
	for _, f := range fixtures {
		path := fixturesDir + "/" + f.name
		if _, err := os.Stat(path); err == nil {
			// File already exists (committed or previously generated) — skip.
			continue
		}
		data, err := f.fn()
		if err != nil {
			return fmt.Errorf("generate %s: %w", f.name, err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// buildFixture11Tests builds a synthetic JPEG that carries exactly the EXIF
// values expected by the example tests that use the exif-samples/11-tests.jpg
// corpus file:
//   - Make: "Canon"
//   - Model: "Canon DIGITAL IXUS 40"
//   - ExposureTime: 1/500 s
//   - FNumber: 2.8
//   - FocalLength: 5.8 mm
//   - DateTimeOriginal: 2007-09-03T16:03:45Z (no GPS)
//   - PixelXDimension: 2272, PixelYDimension: 1704
//   - ColorSpace: 1 (sRGB)
//
// ColorSpace is not yet settable via the public API so the TIFF payload is
// built directly from binary.
func buildFixture11Tests() ([]byte, error) {
	tiff := buildTIFFWith11TestsTags()
	jpeg := buildMinimalJPEG(tiff)

	// Verify the values round-trip correctly before committing the fixture.
	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		return nil, fmt.Errorf("11-tests fixture round-trip Read: %w", err)
	}
	if m.Make() != "Canon" {
		return nil, fmt.Errorf("11-tests fixture Make = %q, want Canon", m.Make())
	}
	if m.CameraModel() != "Canon DIGITAL IXUS 40" {
		return nil, fmt.Errorf("11-tests fixture Model = %q", m.CameraModel())
	}
	if cs, ok := m.ColorSpace(); !ok || cs != 1 {
		return nil, fmt.Errorf("11-tests fixture ColorSpace = %v, %v; want 1, true", cs, ok)
	}
	return jpeg, nil
}

// buildTIFFWith11TestsTags builds a LE TIFF payload for the 11-tests fixture.
// It encodes all required IFD0 and ExifIFD tags for the Example assertions.
//
// IFD0: Make, Model, ExifIFDPointer
// ExifIFD: ExposureTime, FNumber, FocalLength, DateTimeOriginal,
//
//	PixelXDimension, PixelYDimension, ColorSpace
func buildTIFFWith11TestsTags() []byte {
	order := binary.LittleEndian

	// ---- ASCII value area (all null-terminated) ----
	makeStr := []byte("Canon\x00")
	modelStr := []byte("Canon DIGITAL IXUS 40\x00")
	// DateTimeOriginal: "YYYY:MM:DD HH:MM:SS\x00" (20 bytes, EXIF §4.6.5).
	dtStr := []byte("2007:09:03 16:03:45\x00")

	// ---- Size planning ----
	const (
		headerSz = 8

		// IFD0: Make, Model, ExifIFDPointer — 3 entries.
		nIFD0  = 3
		ifd0Sz = 2 + nIFD0*12 + 4

		// ExifIFD: ExposureTime, FNumber, FocalLength, DateTimeOriginal,
		//          PixelXDimension, PixelYDimension, ColorSpace — 7 entries.
		nExifIFD  = 7
		exifIFDSz = 2 + nExifIFD*12 + 4
	)

	ifd0Off := uint32(headerSz)
	// OOL values placed after IFD0 (before ExifIFD for simplicity).
	makeOff := ifd0Off + uint32(ifd0Sz)
	modelOff := makeOff + uint32(len(makeStr))     //nolint:gosec // G115: fixture strings are always short
	exifIFDOff := modelOff + uint32(len(modelStr)) //nolint:gosec // G115: fixture strings are always short

	// ExifIFD OOL values placed after ExifIFD.
	// ExposureTime RATIONAL = 8 bytes; FNumber RATIONAL = 8 bytes;
	// FocalLength RATIONAL = 8 bytes; DateTimeOriginal ASCII = 20 bytes.
	expTimeOff := exifIFDOff + uint32(exifIFDSz)
	fNumberOff := expTimeOff + 8
	focalLenOff := fNumberOff + 8
	dtOff := focalLenOff + 8
	totalSize := int(dtOff) + len(dtStr)
	buf := make([]byte, totalSize)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	putEntry := func(base int, i int, tag, typ uint16, count, val uint32) {
		p := base + 2 + i*12
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
	}

	// IFD0.
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	putEntry(int(ifd0Off), 0, 0x010F, 2 /*ASCII*/, uint32(len(makeStr)), makeOff)   //nolint:gosec // G115: fixture strings are always short
	putEntry(int(ifd0Off), 1, 0x0110, 2 /*ASCII*/, uint32(len(modelStr)), modelOff) //nolint:gosec // G115: fixture strings are always short
	putEntry(int(ifd0Off), 2, 0x8769, 4 /*LONG*/, 1, exifIFDOff)                    // ExifIFDPointer
	order.PutUint32(buf[int(ifd0Off)+2+nIFD0*12:], 0)                               // next IFD = 0

	// OOL string values.
	copy(buf[makeOff:], makeStr)
	copy(buf[modelOff:], modelStr)

	// ExifIFD.
	order.PutUint16(buf[exifIFDOff:], uint16(nExifIFD))
	putEntry(int(exifIFDOff), 0, 0x829A, 5 /*RATIONAL*/, 1, expTimeOff)          // ExposureTime 1/500
	putEntry(int(exifIFDOff), 1, 0x829D, 5 /*RATIONAL*/, 1, fNumberOff)          // FNumber 28/10
	putEntry(int(exifIFDOff), 2, 0x920A, 5 /*RATIONAL*/, 1, focalLenOff)         // FocalLength 58/10
	putEntry(int(exifIFDOff), 3, 0x9003, 2 /*ASCII*/, uint32(len(dtStr)), dtOff) //nolint:gosec // G115: dtStr is a short constant
	putEntry(int(exifIFDOff), 4, 0xA001, 3 /*SHORT*/, 1, 1)                      // ColorSpace = 1 (sRGB)
	putEntry(int(exifIFDOff), 5, 0xA002, 4 /*LONG*/, 1, 2272)                    // PixelXDimension
	putEntry(int(exifIFDOff), 6, 0xA003, 4 /*LONG*/, 1, 1704)                    // PixelYDimension
	order.PutUint32(buf[int(exifIFDOff)+2+nExifIFD*12:], 0)

	// OOL rational values (numerator / denominator).
	order.PutUint32(buf[expTimeOff:], 1)
	order.PutUint32(buf[expTimeOff+4:], 500) // ExposureTime = 1/500
	order.PutUint32(buf[fNumberOff:], 28)
	order.PutUint32(buf[fNumberOff+4:], 10) // FNumber = 28/10 = 2.8
	order.PutUint32(buf[focalLenOff:], 58)
	order.PutUint32(buf[focalLenOff+4:], 10) // FocalLength = 58/10 = 5.8
	copy(buf[dtOff:], dtStr)

	return buf
}

// buildFixtureCanonHDR builds a synthetic JPEG with Orientation=6 (90° CW),
// matching what example_test.go expects from canon_hdr_YES.jpg.
func buildFixtureCanonHDR() ([]byte, error) {
	m := NewMetadata(format.FormatJPEG)
	m.SetOrientation(6) // 90° clockwise

	minimal := buildMinimalJPEG(nil)
	var out bytes.Buffer
	if err := Write(bytes.NewReader(minimal), &out, m); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// buildFixtureJolla builds a synthetic JPEG with WhiteBalance=1 (manual),
// matching what example_test.go expects from jolla.jpg.
// WhiteBalance is stored in ExifIFD tag 0xA403.
func buildFixtureJolla() ([]byte, error) {
	// Build a TIFF payload with ExifIFD containing WhiteBalance=1.
	// Layout: IFD0 (ExifIFDPointer) → ExifIFD (WhiteBalance=1)
	order := binary.LittleEndian
	const (
		headerSz  = 8
		nIFD0     = 1 // ExifIFDPointer only
		ifd0Sz    = 2 + nIFD0*12 + 4
		nExifIFD  = 1 // WhiteBalance only
		exifIFDSz = 2 + nExifIFD*12 + 4
	)
	ifd0Off := uint32(headerSz)
	exifIFDOff := ifd0Off + uint32(ifd0Sz)
	totalSize := int(exifIFDOff) + exifIFDSz
	buf := make([]byte, totalSize)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	writeEntry := func(base int, i int, tag, typ uint16, count, val uint32) {
		p := base + 2 + i*12
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
	}

	// IFD0: ExifIFDPointer (0x8769, LONG, count=1, value=exifIFDOff).
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	writeEntry(int(ifd0Off), 0, 0x8769, 4 /*TypeLong*/, 1, exifIFDOff)
	order.PutUint32(buf[int(ifd0Off)+2+nIFD0*12:], 0) // next IFD = 0

	// ExifIFD: WhiteBalance (0xA403, SHORT, count=1, value=1).
	order.PutUint16(buf[exifIFDOff:], uint16(nExifIFD))
	writeEntry(int(exifIFDOff), 0, 0xA403, 3 /*TypeShort*/, 1, 1 /*manual*/)
	order.PutUint32(buf[int(exifIFDOff)+2+nExifIFD*12:], 0) // next = 0

	jpeg := buildMinimalJPEG(buf)
	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		return nil, fmt.Errorf("jolla fixture Read: %w", err)
	}
	if wb, ok := m.WhiteBalance(); !ok || wb != 1 {
		return nil, fmt.Errorf("jolla fixture WhiteBalance = %v, %v; want 1, true", wb, ok)
	}
	return jpeg, nil
}

// buildFixtureAltitude builds a synthetic JPEG carrying GPS altitude = 340 m
// above sea level (matching example_test.go ExampleMetadata_Altitude).
// GPS IFD tags used:
//   - 0x0005 GPSAltitudeRef: BYTE 1, value 0 (above sea level)
//   - 0x0006 GPSAltitude:    RATIONAL 1, 340/1
func buildFixtureAltitude() ([]byte, error) {
	// Rational altitude value: 340/1 in LE.
	// TIFF layout: IFD0 (GPSIFDPointer) → GPSIFD (GPSAltitudeRef + GPSAltitude)
	order := binary.LittleEndian
	const (
		headerSz = 8
		nIFD0    = 1 // GPSIFDPointer only
		ifd0Sz   = 2 + nIFD0*12 + 4
		nGPSIFD  = 2 // GPSAltitudeRef + GPSAltitude
		gpsIFDSz = 2 + nGPSIFD*12 + 4
		// GPSAltitude rational is 8 bytes OOL.
		altRationalSz = 8
	)
	ifd0Off := uint32(headerSz)
	gpsIFDOff := ifd0Off + uint32(ifd0Sz)
	altOff := gpsIFDOff + uint32(gpsIFDSz)
	totalSize := int(altOff) + altRationalSz
	buf := make([]byte, totalSize)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	writeEntry := func(base int, i int, tag, typ uint16, count, val uint32) {
		p := base + 2 + i*12
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
	}

	// IFD0: GPSIFDPointer (0x8825, LONG, count=1, value=gpsIFDOff).
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	writeEntry(int(ifd0Off), 0, 0x8825, 4 /*TypeLong*/, 1, gpsIFDOff)
	order.PutUint32(buf[int(ifd0Off)+2+nIFD0*12:], 0)

	// GPS IFD:
	//   0x0005 GPSAltitudeRef: BYTE 1, value 0 (inline in val-or-off).
	//   0x0006 GPSAltitude:    RATIONAL 1, OOL at altOff.
	order.PutUint16(buf[gpsIFDOff:], uint16(nGPSIFD))
	writeEntry(int(gpsIFDOff), 0, 0x0005, 1 /*TypeByte*/, 1, 0 /*above sea*/)
	writeEntry(int(gpsIFDOff), 1, 0x0006, 5 /*TypeRational*/, 1, altOff)
	order.PutUint32(buf[int(gpsIFDOff)+2+nGPSIFD*12:], 0)

	// Rational value: 340/1.
	order.PutUint32(buf[altOff:], 340)
	order.PutUint32(buf[altOff+4:], 1)

	jpeg := buildMinimalJPEG(buf)
	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		return nil, fmt.Errorf("altitude fixture Read: %w", err)
	}
	if alt, ok := m.Altitude(); !ok || alt != 340.0 {
		return nil, fmt.Errorf("altitude fixture Altitude() = %v, %v; want 340.0, true", alt, ok)
	}
	return jpeg, nil
}

// buildFixtureIPTCRef builds a synthetic JPEG carrying IPTC + XMP data that
// matches the values expected by the example tests that read
// IPTC-PhotometadataRef-Std2021.1.jpg:
//   - 3 keywords (via IPTC)
//   - Caption: "The description aka caption (ref2021.1)" (via IPTC)
//   - Copyright: "Copyright (Notice) 2021.1 IPTC - www.iptc.org  (ref2021.1)" (via XMP)
//   - XMP present, IPTC present
func buildFixtureIPTCRef() ([]byte, error) {
	// Start from a bare JPEG.
	minimal := buildMinimalJPEG(nil)
	m, err := Read(bytes.NewReader(minimal))
	if err != nil {
		return nil, fmt.Errorf("iptcref fixture Read base: %w", err)
	}

	// Force-create IPTC and XMP so the data is embedded.
	m.IPTC = new(iptc.IPTC)
	m.IPTC.SetCaption("The description aka caption (ref2021.1)")
	m.IPTC.SetKeywords([]string{"keyword1", "keyword2", "keyword3"})

	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	m.XMP.SetCopyright("Copyright (Notice) 2021.1 IPTC - www.iptc.org  (ref2021.1)")
	// Also set 3 keywords in XMP so Keywords() returns 3 regardless of priority.
	m.XMP.SetKeywords([]string{"keyword1", "keyword2", "keyword3"})

	var out bytes.Buffer
	if err := Write(bytes.NewReader(minimal), &out, m); err != nil {
		return nil, fmt.Errorf("iptcref fixture Write: %w", err)
	}
	return out.Bytes(), nil
}
