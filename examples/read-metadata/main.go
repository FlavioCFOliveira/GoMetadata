// Command read-metadata prints all available metadata from an image file.
//
// Usage:
//
//	read-metadata <image-file>
//
// The format is detected automatically from magic bytes; no file extension
// is needed. Supported containers: JPEG, TIFF, PNG, HEIF/HEIC, WebP, and
// RAW variants (CR2, CR3, NEF, ARW, DNG, ORF, RW2).
package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: read-metadata <image-file>")
		os.Exit(1)
	}

	path := os.Args[1]

	m, err := gometadata.ReadFile(path)
	if err != nil {
		log.Fatalf("read-metadata: %v", err)
	}

	// --- Container format ---
	fmt.Printf("Format:   %s\n", m.Format())

	// --- Camera ---
	printIfNonEmpty("Make:    ", m.Make())
	printIfNonEmpty("Model:   ", m.CameraModel())
	printIfNonEmpty("Lens:    ", m.LensModel())
	printIfNonEmpty("Software:", m.Software())

	// --- GPS ---
	if lat, lon, ok := m.GPS(); ok {
		fmt.Printf("GPS:      %.6f, %.6f\n", lat, lon)
	}

	// --- Date/time ---
	if t, ok := m.DateTimeOriginal(); ok {
		fmt.Printf("Captured: %s\n", t.Format(time.RFC3339))
	}

	// --- Exposure ---
	if num, den, ok := m.ExposureTime(); ok {
		if num == 1 {
			fmt.Printf("Exposure: 1/%d s\n", den)
		} else {
			fmt.Printf("Exposure: %d/%d s\n", num, den)
		}
	}
	if fn, ok := m.FNumber(); ok {
		fmt.Printf("F-number: f/%.1f\n", fn)
	}
	if iso, ok := m.ISO(); ok {
		fmt.Printf("ISO:      %d\n", iso)
	}
	if fl, ok := m.FocalLength(); ok {
		fmt.Printf("Focal:    %.1f mm\n", fl)
	}

	// --- Descriptive ---
	printIfNonEmpty("Caption:  ", m.Caption())
	printIfNonEmpty("Copyright:", m.Copyright())
	printIfNonEmpty("Creator:  ", m.Creator())

	if kw := m.Keywords(); len(kw) > 0 {
		fmt.Printf("Keywords: %s\n", strings.Join(kw, ", "))
	}
}

// printIfNonEmpty prints label + value only when value is not empty.
func printIfNonEmpty(label, value string) {
	if value != "" {
		fmt.Printf("%s %s\n", label, value)
	}
}
