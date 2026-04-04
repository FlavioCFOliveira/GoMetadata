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
	fmt.Println("Lens:    ", m.LensModel())
	fmt.Println("Software:", m.Software())
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
	updated := append(existing, "gometadata-example")
	m.SetKeywords(updated)
	fmt.Printf("updated keywords: %d\n", len(m.Keywords()))
}
