// Command stream-transcode reads an image from stdin, applies metadata
// supplied via flags, and writes the modified image to stdout. No temporary
// files are created — the library operates entirely on io.ReadSeeker and
// io.Writer interfaces.
//
// This example demonstrates how to embed or update EXIF, IPTC, and XMP
// metadata in Go without touching the filesystem — suitable for HTTP request
// handlers, cloud function pipelines (AWS Lambda, Google Cloud Functions), and
// streaming image processors. The Read and Write functions accept any
// io.ReadSeeker and io.Writer, making integration with net/http, bytes.Buffer,
// or object-store streams straightforward.
//
// Usage:
//
//	stream-transcode [flags] < input.jpg > output.jpg
//
// Flags:
//
//	-caption    image description
//	-copyright  copyright notice
//	-creator    author / creator name
//	-lat        GPS latitude  (decimal degrees, positive = North)
//	-lon        GPS longitude (decimal degrees, positive = East)
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
)

func main() {
	caption := flag.String("caption", "", "image description")
	copyright := flag.String("copyright", "", "copyright notice")
	creator := flag.String("creator", "", "author / creator name")
	lat := flag.Float64("lat", 0, "GPS latitude in decimal degrees (positive = North)")
	lon := flag.Float64("lon", 0, "GPS longitude in decimal degrees (positive = East)")
	setGPS := flag.Bool("gps", false, "set GPS coordinates using -lat and -lon values")
	flag.Parse()

	// os.Stdin is an *os.File, which implements io.ReadSeeker when stdin is a
	// regular file or pipe. For truly non-seekable streams, buffer into a
	// bytes.Buffer and wrap with bytes.NewReader first.
	m, err := gometadata.Read(os.Stdin)
	if err != nil {
		var unsupported *gometadata.UnsupportedFormatError
		if errors.As(err, &unsupported) {
			fmt.Fprintln(os.Stderr, "stream-transcode: unsupported format")
			os.Exit(1)
		}
		log.Fatal(err)
	}

	if *caption != "" {
		m.SetCaption(*caption)
	}
	if *copyright != "" {
		m.SetCopyright(*copyright)
	}
	if *creator != "" {
		m.SetCreator(*creator)
	}
	// Only set GPS when the caller explicitly requested it via -gps, or when
	// -lat or -lon were set to a non-zero value.
	if *setGPS || *lat != 0 || *lon != 0 {
		m.SetGPS(*lat, *lon)
	}

	// Seek stdin back to the beginning so the Write call can re-read the
	// original image bytes. Read consumed the stream to detect format and parse
	// metadata; Write needs the full original stream to copy image data through.
	if _, err := os.Stdin.Seek(0, io.SeekStart); err != nil {
		log.Fatalf("stream-transcode: seek stdin: %v", err)
	}

	// PreserveUnknownSegments(true) ensures that any APP or other segments not
	// understood by the library (e.g., custom ICC profiles, Photoshop data) are
	// passed through byte-for-byte rather than dropped.
	if err := gometadata.Write(os.Stdin, os.Stdout, m, gometadata.PreserveUnknownSegments(true)); err != nil {
		log.Fatalf("stream-transcode: write: %v", err)
	}
}
