// Command copyright-stamp applies a standardised copyright notice, creator
// name, caption, and keyword set to every image file in a directory tree. It
// demonstrates how to write EXIF, IPTC, and XMP rights metadata simultaneously
// using GoMetadata's unified setter API.
//
// This is a practical Go example for stock photographers, photo agencies, and
// media asset management (DAM) systems that need to embed copyright information
// into JPEG, TIFF, PNG, WebP, HEIF/HEIC, and RAW image files using Go. The
// SetCopyright, SetCreator, and SetCaption methods write to all non-nil metadata
// components (EXIF tag 0x8298, IPTC dataset 2:116, XMP dc:rights) in a single
// call, keeping EXIF, IPTC, and XMP in sync automatically.
//
// Usage:
//
//	copyright-stamp [flags] <dir>
//
// Flags:
//
//	-copyright  copyright notice text, e.g. "© 2025 Jane Smith. All rights reserved."
//	-creator    creator / author name
//	-caption    image description / caption (optional)
//	-keywords   comma-separated keyword list (optional)
//	-dry-run    print what would be changed without writing any files
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
)

func main() {
	copyright := flag.String("copyright", "", "copyright notice text")
	creator := flag.String("creator", "", "creator / author name")
	caption := flag.String("caption", "", "image description / caption (optional)")
	keywords := flag.String("keywords", "", "comma-separated keyword list (optional)")
	dryRun := flag.Bool("dry-run", false, "print what would be changed without writing any files")
	flag.Parse()

	if *copyright == "" {
		log.Fatal("copyright-stamp: -copyright is required")
	}
	if *creator == "" {
		log.Fatal("copyright-stamp: -creator is required")
	}
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: copyright-stamp [flags] <dir>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	root := flag.Arg(0)

	// supportedExtensions is the set of file suffixes (lower-case) that this
	// tool will process. Format detection inside the library uses magic bytes,
	// but the extension pre-filter avoids unnecessary I/O on non-image files.
	supportedExtensions := map[string]bool{
		".jpg":  true,
		".jpeg": true,
		".tif":  true,
		".tiff": true,
		".png":  true,
		".webp": true,
		".heic": true,
		".heif": true,
		".avif": true,
		".cr2":  true,
		".cr3":  true,
		".nef":  true,
		".arw":  true,
		".dng":  true,
		".orf":  true,
		".rw2":  true,
	}

	// Parse keywords once up front.
	var kws []string
	if *keywords != "" {
		kws = strings.Split(*keywords, ",")
		for i := range kws {
			kws[i] = strings.TrimSpace(kws[i])
		}
	}

	var (
		processed int
		changed   int
		skipped   int
	)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			fmt.Fprintf(os.Stderr, "walk error: %s: %v\n", path, walkErr)
			skipped++
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !supportedExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		processed++

		m, err := gometadata.ReadFile(path)
		if err != nil {
			var corrupt *gometadata.CorruptMetadataError
			var truncated *gometadata.TruncatedFileError
			switch {
			case errors.As(err, &corrupt):
				fmt.Fprintf(os.Stderr, "skip (corrupt metadata): %s: %v\n", path, err)
			case errors.As(err, &truncated):
				fmt.Fprintf(os.Stderr, "skip (truncated file): %s: %v\n", path, err)
			default:
				fmt.Fprintf(os.Stderr, "skip (read error): %s: %v\n", path, err)
			}
			skipped++
			return nil
		}

		m.SetCopyright(*copyright)
		m.SetCreator(*creator)
		if *caption != "" {
			m.SetCaption(*caption)
		}
		if len(kws) > 0 {
			m.SetKeywords(kws)
		}

		if *dryRun {
			fmt.Printf("would stamp: %s\n", path)
			changed++
			return nil
		}

		if err := gometadata.WriteFile(path, m); err != nil {
			fmt.Fprintf(os.Stderr, "skip (write error): %s: %v\n", path, err)
			skipped++
			return nil
		}

		fmt.Printf("stamped: %s\n", path)
		changed++
		return nil
	})

	if err != nil {
		log.Fatalf("copyright-stamp: walk: %v", err)
	}

	fmt.Printf("\nprocessed: %d  stamped: %d  skipped: %d\n", processed, changed, skipped)
}
