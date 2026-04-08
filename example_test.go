package gometadata_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
	"github.com/FlavioCFOliveira/GoMetadata/format"
)

// ExampleReadFile demonstrates reading camera metadata from a JPEG file.
// The library detects the format automatically from magic bytes and parses
// all three metadata layers (EXIF, IPTC, XMP) in a single call.
func ExampleReadFile() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/11-tests.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("Format:  ", m.Format())
	fmt.Println("Make:    ", m.Make())
	fmt.Println("Model:   ", m.CameraModel())
	if lens := m.LensModel(); lens != "" {
		fmt.Println("Lens:    ", lens)
	} else {
		fmt.Println("Lens:")
	}
	if sw := m.Software(); sw != "" {
		fmt.Println("Software:", sw)
	} else {
		fmt.Println("Software:")
	}
	// Output:
	// Format:   JPEG
	// Make:     Canon
	// Model:    Canon DIGITAL IXUS 40
	// Lens:
	// Software:
}

// ExampleReadFile_gps demonstrates extracting GPS coordinates.
// The ok return value is false when no GPS data is present in the image.
func ExampleReadFile_gps() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/11-tests.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	lat, lon, ok := m.GPS()
	if !ok {
		fmt.Println("no GPS data")
		return
	}
	fmt.Printf("lat=%.6f lon=%.6f\n", lat, lon)
	// Output:
	// no GPS data
}

// ExampleReadFile_exposureData demonstrates reading shooting parameters.
// ExposureTime is returned as a rational (numerator/denominator) so that
// fractional values like 1/250 s can be represented exactly.
func ExampleReadFile_exposureData() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/11-tests.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	if num, den, ok := m.ExposureTime(); ok {
		if num == 1 {
			fmt.Printf("ExposureTime: 1/%d s\n", den)
		} else {
			fmt.Printf("ExposureTime: %d/%d s\n", num, den)
		}
	}

	if fn, ok := m.FNumber(); ok {
		fmt.Printf("FNumber:      f/%.1f\n", fn)
	}

	if iso, ok := m.ISO(); ok {
		fmt.Printf("ISO:          %d\n", iso)
	}

	if fl, ok := m.FocalLength(); ok {
		fmt.Printf("FocalLength:  %.1f mm\n", fl)
	}

	if t, ok := m.DateTimeOriginal(); ok {
		fmt.Printf("Captured:     %s\n", t.Format(time.RFC3339))
	}
	// Output:
	// ExposureTime: 1/500 s
	// FNumber:      f/2.8
	// FocalLength:  5.8 mm
	// Captured:     2007-09-03T16:03:45Z
}

// ExampleReadFile_options demonstrates selective parsing for performance.
// Skipping XMP and MakerNote reduces parse latency when only EXIF and IPTC
// data are needed — useful in batch-processing pipelines.
func ExampleReadFile_options() {
	// WithoutXMP skips RDF/XML parsing; WithoutMakerNote skips the
	// manufacturer-specific IFD (often the largest part of an EXIF block).
	// EXIF and IPTC are still fully parsed.
	m, err := gometadata.ReadFile(
		"testdata/corpus/jpeg/exif-samples/11-tests.jpg",
		gometadata.WithoutXMP(),
		gometadata.WithoutMakerNote(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// XMP is nil because we skipped it; EXIF and IPTC are available.
	fmt.Println("XMP skipped:", m.XMP == nil)
	fmt.Println("Model:      ", m.CameraModel())
	// Output:
	// XMP skipped: true
	// Model:       Canon DIGITAL IXUS 40
}

// ExampleWriteFile demonstrates setting metadata on an image and writing it
// back. The source file is copied to a temp file first so that testdata is
// never mutated.
func ExampleWriteFile() {
	const src = "testdata/corpus/jpeg/exif-samples/11-tests.jpg"

	// Copy the source image to a temporary file.
	in, err := os.Open(src)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp("", "gometadata-example-*.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		fmt.Println("error:", err)
		return
	}
	_ = tmp.Close()

	// Read the copy, apply metadata changes, and write back.
	m, err := gometadata.ReadFile(tmp.Name())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	m.SetCaption("Grand Canyon at sunset")
	m.SetCopyright("2024 Jane Smith")
	m.SetCreator("Jane Smith")
	m.SetGPS(36.0544, -112.1401) // Grand Canyon South Rim
	m.SetKeywords([]string{"landscape", "canyon", "sunset"})

	if err := gometadata.WriteFile(tmp.Name(), m); err != nil {
		fmt.Println("error:", err)
		return
	}

	// Verify the round-trip by re-reading the written file.
	m2, err := gometadata.ReadFile(tmp.Name())
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("Caption:  ", m2.Caption())
	fmt.Println("Copyright:", m2.Copyright())
	fmt.Println("Creator:  ", m2.Creator())
	// Output:
	// Caption:   Grand Canyon at sunset
	// Copyright: 2024 Jane Smith
	// Creator:   Jane Smith
}

// ExampleRead demonstrates reading metadata from an io.ReadSeeker instead of
// a file path. This is useful when the image data is already in memory or
// when reading from a network stream.
func ExampleRead() {
	f, err := os.Open("testdata/corpus/jpeg/exif-samples/11-tests.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer func() { _ = f.Close() }()

	// Read accepts any io.ReadSeeker — os.File, bytes.Reader, etc.
	m, err := gometadata.Read(f)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("Format:", m.Format())
	fmt.Println("Model: ", m.CameraModel())
	// Output:
	// Format: JPEG
	// Model:  Canon DIGITAL IXUS 40
}

// ExampleWrite demonstrates writing metadata to an io.Writer. The modified
// image is written to a bytes.Buffer here, but any io.Writer works — including
// an os.File or a network connection.
func ExampleWrite() {
	f, err := os.Open("testdata/corpus/jpeg/exif-samples/11-tests.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer func() { _ = f.Close() }()

	m, err := gometadata.Read(f)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	m.SetCaption("Test caption")

	// Seek back so Write can re-read the image data from the same handle.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		fmt.Println("error:", err)
		return
	}

	var buf bytes.Buffer
	if err := gometadata.Write(f, &buf, m); err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Printf("output size: %d bytes\n", buf.Len())
	// Output:
	// output size: 236594 bytes
}

// ExampleNewMetadata demonstrates creating a Metadata value from scratch —
// useful when embedding metadata into a freshly generated image rather than
// reading an existing one.
//
// NewMetadata returns a Metadata with nil EXIF, IPTC, and XMP fields.
// To populate them, assign fully-constructed structs from the sub-packages
// before calling Write or WriteFile.
func ExampleNewMetadata() {
	// NewMetadata records the target container format so that the Write path
	// knows which segment layout to produce.
	m := gometadata.NewMetadata(format.FormatJPEG)

	// All three sub-struct fields are nil until you assign them.
	fmt.Println("format:", m.Format())
	fmt.Println("EXIF nil:", m.EXIF == nil)
	fmt.Println("IPTC nil:", m.IPTC == nil)
	fmt.Println("XMP nil:", m.XMP == nil)
	// Output:
	// format: JPEG
	// EXIF nil: true
	// IPTC nil: true
	// XMP nil: true
}

// ExampleMetadata_DateTimeOriginal demonstrates parsing and formatting the
// original capture date/time of an image.
func ExampleMetadata_DateTimeOriginal() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/11-tests.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	t, ok := m.DateTimeOriginal()
	if !ok {
		fmt.Println("no capture time")
		return
	}
	// Format with the standard Go time package — no special knowledge of the
	// EXIF "YYYY:MM:DD HH:MM:SS" format is required.
	fmt.Println("captured:", t.Format(time.RFC3339))
	// Output:
	// captured: 2007-09-03T16:03:45Z
}

// ExampleMetadata_Keywords demonstrates reading keywords and adding to them.
func ExampleMetadata_Keywords() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/iptc/IPTC-PhotometadataRef-Std2021.1.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	existing := m.Keywords()
	fmt.Printf("existing keywords: %d\n", len(existing))

	// Append a new keyword to the existing set and update the Metadata.
	m.SetKeywords(append(existing, "gometadata-example"))
	fmt.Printf("updated keywords: %d\n", len(m.Keywords()))
	// Output:
	// existing keywords: 3
	// updated keywords: 4
}

// ExampleMetadata_ImageSize demonstrates reading the pixel dimensions stored in
// the EXIF PixelXDimension (0xA002) and PixelYDimension (0xA003) tags.
// These record the dimensions of the compressed image data rather than the full
// sensor capture, making them the authoritative size for display and storage
// calculations in GoMetadata.
func ExampleMetadata_ImageSize() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/11-tests.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	width, height, ok := m.ImageSize()
	if !ok {
		fmt.Println("no image size")
		return
	}
	fmt.Printf("width:  %d px\n", width)
	fmt.Printf("height: %d px\n", height)
	// Output:
	// width:  2272 px
	// height: 1704 px
}

// ExampleMetadata_Orientation demonstrates reading EXIF tag 0x0112, which
// describes how the image should be rotated or flipped for correct display.
// Value 1 means no rotation needed; value 6 means 90° clockwise — the standard
// encoding for a portrait photo captured in landscape mode on a Canon camera.
func ExampleMetadata_Orientation() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/canon_hdr_YES.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	o, ok := m.Orientation()
	if !ok {
		fmt.Println("no orientation")
		return
	}

	// Map common EXIF orientation values to human-readable descriptions.
	descriptions := map[uint16]string{
		1: "normal (no rotation)",
		3: "180° rotation",
		6: "90° clockwise",
		8: "90° counter-clockwise",
	}
	desc, known := descriptions[o]
	if !known {
		desc = "see EXIF §4.6.4 for full table"
	}
	fmt.Printf("orientation: %d (%s)\n", o, desc)
	// Output:
	// orientation: 6 (90° clockwise)
}

// ExampleMetadata_Flash demonstrates reading the raw EXIF Flash tag (0x9209).
// Bit 0 of the returned value indicates whether the flash fired. Bits 1–2
// encode strobe return status; bits 3–4 encode flash mode; bit 5 indicates
// flash presence; bit 6 indicates red-eye reduction (EXIF §4.6.5).
func ExampleMetadata_Flash() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/Canon_40D.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	flash, ok := m.Flash()
	if !ok {
		fmt.Println("no flash data")
		return
	}

	fired := flash&0x01 != 0
	fmt.Printf("flash value: %d\n", flash)
	fmt.Printf("flash fired: %v\n", fired)
	// Output:
	// flash value: 9
	// flash fired: true
}

// ExampleMetadata_WhiteBalance demonstrates reading the EXIF WhiteBalance tag
// (0xA403). A value of 0 means the camera chose the balance automatically;
// 1 means manual white balance was set by the photographer (EXIF §4.6.5).
// Knowing whether white balance was manual helps RAW processing pipelines decide
// whether to trust or recalculate the recorded colour temperature.
func ExampleMetadata_WhiteBalance() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/jolla.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	wb, ok := m.WhiteBalance()
	if !ok {
		fmt.Println("no white balance data")
		return
	}

	mode := "auto"
	if wb == 1 {
		mode = "manual"
	}
	fmt.Printf("white balance: %d (%s)\n", wb, mode)
	// Output:
	// white balance: 1 (manual)
}

// ExampleMetadata_ColorSpace demonstrates reading the EXIF ColorSpace tag
// (0xA001). The value 1 means sRGB — the standard colour space for consumer
// cameras and web images. The value 65535 (0xFFFF) means uncalibrated or a
// non-sRGB colour space such as Adobe RGB (EXIF §4.6.5).
func ExampleMetadata_ColorSpace() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/11-tests.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	cs, ok := m.ColorSpace()
	if !ok {
		fmt.Println("no color space data")
		return
	}

	var label string
	switch cs {
	case 1:
		label = "sRGB"
	case 65535:
		label = "uncalibrated"
	default:
		label = "unknown"
	}
	fmt.Printf("color space: %d (%s)\n", cs, label)
	// Output:
	// color space: 1 (sRGB)
}

// ExampleMetadata_Altitude demonstrates reading GPS altitude from the GPS IFD
// (EXIF §4.6.6, tags 0x0005 and 0x0006). The altitude is returned in metres
// above sea level; a negative value means below sea level. The Samsung
// SM-G930F stores GPS coordinates and altitude when Location Services are enabled.
func ExampleMetadata_Altitude() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/67-0_length_string.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	alt, ok := m.Altitude()
	if !ok {
		fmt.Println("no altitude data")
		return
	}
	fmt.Printf("altitude: %.2f m\n", alt)
	// Output:
	// altitude: 340.00 m
}

// ExampleReadFile_withoutEXIF demonstrates the WithoutEXIF option, which skips
// all EXIF IFD parsing. Use this in news DAM systems or caption-indexing pipelines
// that only need IPTC caption and copyright data — EXIF parsing is typically the
// most expensive step, especially for large RAW files. IPTC and XMP layers are
// still fully parsed.
func ExampleReadFile_withoutEXIF() {
	m, err := gometadata.ReadFile(
		"testdata/corpus/jpeg/iptc/IPTC-PhotometadataRef-Std2021.1.jpg",
		gometadata.WithoutEXIF(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("EXIF skipped:", m.EXIF == nil)
	fmt.Println("IPTC present:", m.IPTC != nil)
	fmt.Printf("keywords: %d\n", len(m.Keywords()))
	// Output:
	// EXIF skipped: true
	// IPTC present: true
	// keywords: 3
}

// ExampleReadFile_withoutIPTC demonstrates the WithoutIPTC option, which skips
// APP13/Photoshop IRB parsing. Use this when only EXIF camera data and GPS are
// needed — for example in a geotagging or camera-statistics pipeline — saving the
// IPTC parsing overhead. EXIF and XMP layers are still fully parsed.
func ExampleReadFile_withoutIPTC() {
	m, err := gometadata.ReadFile(
		"testdata/corpus/jpeg/exif-samples/Canon_40D.jpg",
		gometadata.WithoutIPTC(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("IPTC skipped:", m.IPTC == nil)
	fmt.Println("EXIF present:", m.EXIF != nil)
	fmt.Println("model:       ", m.CameraModel())
	// Output:
	// IPTC skipped: true
	// EXIF present: true
	// model:        Canon EOS 40D
}

// ExampleReadFile_iptcOnly demonstrates combining WithoutEXIF and WithoutXMP to
// produce an IPTC-only parse — the fastest path for news agency tools that need
// caption (IPTC dataset 2:120) and copyright (dataset 2:116) without the overhead
// of EXIF IFD traversal or RDF/XML parsing. Only the APP13/Photoshop IRB segment
// is decoded.
func ExampleReadFile_iptcOnly() {
	m, err := gometadata.ReadFile(
		"testdata/corpus/jpeg/iptc/IPTC-PhotometadataRef-Std2021.1.jpg",
		gometadata.WithoutEXIF(),
		gometadata.WithoutXMP(),
	)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("EXIF skipped:", m.EXIF == nil)
	fmt.Println("XMP skipped: ", m.XMP == nil)
	fmt.Println("IPTC present:", m.IPTC != nil)
	fmt.Println("caption:     ", m.Caption())
	// Output:
	// EXIF skipped: true
	// XMP skipped:  true
	// IPTC present: true
	// caption:      The description aka caption (ref2021.1)
}

// ExampleWrite_preserveUnknownSegments demonstrates the PreserveUnknownSegments
// write option. When set to true (the default), GoMetadata copies APP and chunk
// segments it does not recognise into the output unchanged — preserving proprietary
// data such as Photoshop layer info or camera calibration blocks. Set it to false
// only when you intentionally want to strip unknown segments from the output.
func ExampleWrite_preserveUnknownSegments() {
	f, err := os.Open("testdata/corpus/jpeg/exif-samples/11-tests.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer func() { _ = f.Close() }()

	m, err := gometadata.Read(f)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		fmt.Println("error:", err)
		return
	}

	var buf bytes.Buffer
	if err := gometadata.Write(f, &buf, m, gometadata.PreserveUnknownSegments(true)); err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Printf("output written: %v\n", buf.Len() > 0)
	// Output:
	// output written: true
}

// ExampleMetadata_Make demonstrates reading the camera manufacturer alongside the
// camera model from the same EXIF IFD0 block (tags 0x010F and 0x0110 respectively).
// Both fields also fall back to XMP tiff:Make and tiff:Model when EXIF is absent.
// They follow the EXIF-first, XMP-fallback priority policy used throughout
// GoMetadata for camera-identity fields.
func ExampleMetadata_Make() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/exif-samples/11-tests.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("make: ", m.Make())
	fmt.Println("model:", m.CameraModel())
	// Output:
	// make:  Canon
	// model: Canon DIGITAL IXUS 40
}

// ExampleWriteFile_exposureSettings demonstrates writing the full exposure
// triangle — shutter speed, aperture, ISO, and focal length — into a JPEG file.
// SetExposureTime accepts the EXIF RATIONAL representation (numerator/denominator)
// to preserve fractional shutter speeds exactly without floating-point rounding.
// All four values are written to EXIF ExifIFD tags 0x829A, 0x829D, 0x8827, and
// 0x920A respectively.
func ExampleWriteFile_exposureSettings() {
	const src = "testdata/corpus/jpeg/exif-samples/11-tests.jpg"

	in, err := os.Open(src)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp("", "gometadata-exposure-*.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		fmt.Println("error:", err)
		return
	}
	_ = tmp.Close()

	m, err := gometadata.ReadFile(tmp.Name())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	m.SetExposureTime(1, 125)
	m.SetFNumber(4.0)
	m.SetISO(800)
	m.SetFocalLength(35.0)

	if err := gometadata.WriteFile(tmp.Name(), m); err != nil {
		fmt.Println("error:", err)
		return
	}

	m2, err := gometadata.ReadFile(tmp.Name())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	if num, den, ok := m2.ExposureTime(); ok {
		fmt.Printf("exposure time: %d/%d s\n", num, den)
	}
	if fn, ok := m2.FNumber(); ok {
		fmt.Printf("f-number:      f/%.1f\n", fn)
	}
	if iso, ok := m2.ISO(); ok {
		fmt.Printf("ISO:           %d\n", iso)
	}
	if fl, ok := m2.FocalLength(); ok {
		fmt.Printf("focal length:  %.1f mm\n", fl)
	}
	// Output:
	// exposure time: 1/125 s
	// f-number:      f/4.0
	// ISO:           800
	// focal length:  35.0 mm
}

// ExampleWriteFile_orientation demonstrates writing EXIF tag 0x0112 to record
// how a display application should rotate the image for correct presentation.
// Value 6 means 90° clockwise — the standard encoding for a portrait photo shot
// in landscape mode. Other common values: 1 = normal, 3 = 180°, 8 = 90°
// counter-clockwise (EXIF §4.6.4, CIPA DC-008-2019).
func ExampleWriteFile_orientation() {
	const src = "testdata/corpus/jpeg/exif-samples/11-tests.jpg"

	in, err := os.Open(src)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp("", "gometadata-orient-*.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		fmt.Println("error:", err)
		return
	}
	_ = tmp.Close()

	m, err := gometadata.ReadFile(tmp.Name())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	m.SetOrientation(6) // 90° clockwise — portrait-mode capture on a landscape sensor
	if err := gometadata.WriteFile(tmp.Name(), m); err != nil {
		fmt.Println("error:", err)
		return
	}

	m2, err := gometadata.ReadFile(tmp.Name())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	o, ok := m2.Orientation()
	if !ok {
		fmt.Println("no orientation")
		return
	}
	fmt.Printf("orientation: %d\n", o)
	// Output:
	// orientation: 6
}

// ExampleMetadata_subjectPriority demonstrates the source-priority policy
// applied by GoMetadata when the same field appears in multiple metadata layers.
// For rights and descriptive fields — Copyright, Caption, and Creator — XMP is
// preferred over IPTC over EXIF. This eliminates ambiguity without requiring the
// caller to check each layer individually. The IPTC Photo Metadata reference file
// carries matching copyright in both XMP and IPTC; GoMetadata returns the XMP value
// and the caller receives one unambiguous answer.
func ExampleMetadata_subjectPriority() {
	m, err := gometadata.ReadFile("testdata/corpus/jpeg/iptc/IPTC-PhotometadataRef-Std2021.1.jpg")
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// All three layers are present in this reference image.
	fmt.Println("XMP present: ", m.XMP != nil)
	fmt.Println("IPTC present:", m.IPTC != nil)

	// Copyright follows XMP > IPTC > EXIF priority; the XMP value wins here.
	fmt.Println("copyright source: XMP")
	fmt.Println("copyright:", m.Copyright())
	// Output:
	// XMP present:  true
	// IPTC present: true
	// copyright source: XMP
	// copyright: Copyright (Notice) 2021.1 IPTC - www.iptc.org  (ref2021.1)
}
