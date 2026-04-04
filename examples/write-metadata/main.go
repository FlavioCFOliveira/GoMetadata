// Command write-metadata applies metadata fields to an image file and writes
// the result to an output path.
//
// Usage:
//
//	write-metadata [flags] <input> <output>
//
// Flags:
//
//	-caption    "..."   caption / image description
//	-copyright  "..."   copyright notice
//	-creator    "..."   author / creator name
//	-lat        0.0     GPS latitude in decimal degrees (WGS-84; negative = South)
//	-lon        0.0     GPS longitude in decimal degrees (WGS-84; negative = West)
//
// When <output> is the same path as <input> the file is updated in place using
// an atomic rename. When they differ the input is copied to the output path
// first, then the metadata is applied to the copy.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
)

func main() {
	caption := flag.String("caption", "", "caption / image description")
	copyright := flag.String("copyright", "", "copyright notice")
	creator := flag.String("creator", "", "author / creator name")
	lat := flag.Float64("lat", 0, "GPS latitude in decimal degrees (WGS-84)")
	lon := flag.Float64("lon", 0, "GPS longitude in decimal degrees (WGS-84)")
	setGPS := false

	// Detect whether -lat or -lon were explicitly provided by checking the
	// default value before Parse; flag.Visit reports only flags that were set.
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "lat" || f.Name == "lon" {
			setGPS = true
		}
	})

	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: write-metadata [flags] <input> <output>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	inputPath := flag.Arg(0)
	outputPath := flag.Arg(1)

	// When writing to a different output path, copy the input to the output
	// first so WriteFile operates on the target location.
	if inputPath != outputPath {
		if err := copyFile(inputPath, outputPath); err != nil {
			log.Fatalf("write-metadata: copy: %v", err)
		}
	}

	// Read metadata from the output path (either the original or its copy).
	m, err := gometadata.ReadFile(outputPath)
	if err != nil {
		log.Fatalf("write-metadata: read: %v", err)
	}

	// Apply only the fields that were explicitly set.
	if *caption != "" {
		m.SetCaption(*caption)
	}
	if *copyright != "" {
		m.SetCopyright(*copyright)
	}
	if *creator != "" {
		m.SetCreator(*creator)
	}
	if setGPS {
		m.SetGPS(*lat, *lon)
	}

	if err := gometadata.WriteFile(outputPath, m); err != nil {
		log.Fatalf("write-metadata: write: %v", err)
	}

	fmt.Printf("wrote metadata to %s\n", outputPath)
}

// copyFile copies the file at src to dst, creating or truncating dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close destination: %w", err)
	}
	return nil
}
