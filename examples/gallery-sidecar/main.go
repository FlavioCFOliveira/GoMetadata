// Command gallery-sidecar reads metadata from one or more image files and
// prints a JSON representation suitable for use as a sidecar file, search
// index entry, or API response payload. It covers EXIF camera data, GPS
// coordinates, capture time, and descriptive fields (caption, copyright,
// creator, keywords).
//
// This example shows how to extract structured image metadata in Go for web
// applications, static site generators (Hugo, Jekyll, Next.js), photo gallery
// software, and content management systems. It demonstrates selective parsing
// with WithoutMakerNote for bulk-processing performance, optional fields via
// Go pointer types (nil serialises as JSON null), and serialising GoMetadata
// results to JSON without a reflection-heavy ORM layer.
//
// Usage:
//
//	gallery-sidecar [-pretty] <image> [<image>...]
//
// Flags:
//
//	-pretty   pretty-print the JSON output (default: compact one-liner per file)
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
)

// imageRecord holds the metadata extracted from a single image file, ready
// for JSON serialisation. Pointer fields are nil when the value is absent,
// which serialises as JSON null (or is omitted entirely with omitempty).
type imageRecord struct {
	File         string   `json:"file"`
	Format       string   `json:"format"`
	Make         *string  `json:"make,omitempty"`
	Model        *string  `json:"model,omitempty"`
	Lens         *string  `json:"lens,omitempty"`
	Width        *uint32  `json:"width,omitempty"`
	Height       *uint32  `json:"height,omitempty"`
	CapturedAt   *string  `json:"captured_at,omitempty"` // RFC3339
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	Altitude     *float64 `json:"altitude,omitempty"`
	ExposureTime *string  `json:"exposure_time,omitempty"` // "1/250" or "N/D"
	FNumber      *float64 `json:"f_number,omitempty"`
	ISO          *uint    `json:"iso,omitempty"`
	FocalLength  *float64 `json:"focal_length_mm,omitempty"`
	Caption      *string  `json:"caption,omitempty"`
	Copyright    *string  `json:"copyright,omitempty"`
	Creator      *string  `json:"creator,omitempty"`
	Keywords     []string `json:"keywords,omitempty"`
}

func main() {
	pretty := flag.Bool("pretty", false, "pretty-print the JSON output")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: gallery-sidecar [-pretty] <image> [<image>...]")
		os.Exit(1)
	}

	records := make([]imageRecord, 0, flag.NArg())

	for _, path := range flag.Args() {
		// WithoutMakerNote skips the costliest part of EXIF parsing, which is
		// important when processing large batches of images.
		m, err := gometadata.ReadFile(path, gometadata.WithoutMakerNote())
		if err != nil {
			var unsupported *gometadata.UnsupportedFormatError
			if errors.As(err, &unsupported) {
				fmt.Fprintf(os.Stderr, "gallery-sidecar: skip (unsupported format): %s\n", path)
				continue
			}
			log.Fatalf("gallery-sidecar: %s: %v", path, err)
		}

		rec := imageRecord{
			File:   path,
			Format: m.Format().String(),
		}

		rec.Make = strPtr(m.Make())
		rec.Model = strPtr(m.CameraModel())
		rec.Lens = strPtr(m.LensModel())

		if w, h, ok := m.ImageSize(); ok {
			rec.Width = &w
			rec.Height = &h
		}

		if t, ok := m.DateTimeOriginal(); ok {
			s := t.Format(time.RFC3339)
			rec.CapturedAt = &s
		}

		if lat, lon, ok := m.GPS(); ok {
			rec.Latitude = &lat
			rec.Longitude = &lon
		}

		if alt, ok := m.Altitude(); ok {
			rec.Altitude = &alt
		}

		if num, den, ok := m.ExposureTime(); ok {
			var s string
			if num == 1 {
				s = fmt.Sprintf("1/%d", den)
			} else {
				s = fmt.Sprintf("%d/%d", num, den)
			}
			rec.ExposureTime = &s
		}

		if fn, ok := m.FNumber(); ok {
			rec.FNumber = &fn
		}

		if iso, ok := m.ISO(); ok {
			rec.ISO = &iso
		}

		if fl, ok := m.FocalLength(); ok {
			rec.FocalLength = &fl
		}

		rec.Caption = strPtr(m.Caption())
		rec.Copyright = strPtr(m.Copyright())
		rec.Creator = strPtr(m.Creator())

		if kws := m.Keywords(); len(kws) > 0 {
			rec.Keywords = kws
		}

		records = append(records, rec)
	}

	var out []byte
	var marshalErr error
	if *pretty {
		out, marshalErr = json.MarshalIndent(records, "", "  ")
	} else {
		out, marshalErr = json.Marshal(records)
	}
	if marshalErr != nil {
		log.Fatalf("gallery-sidecar: marshal: %v", marshalErr)
	}

	fmt.Printf("%s\n", out)
}

// strPtr returns a pointer to s, or nil if s is the empty string.
// This allows optional string fields to serialise as JSON null / omitempty.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
