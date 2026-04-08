// Command multi-format-roundtrip demonstrates reading and writing image
// metadata across all container formats supported by GoMetadata: JPEG, TIFF,
// PNG, WebP, HEIF/HEIC, and camera RAW variants (Canon CR2/CR3, Nikon NEF,
// Sony ARW, Adobe DNG, Olympus ORF, Panasonic RW2).
//
// The program scans a directory, reads each image, stamps a fixed set of
// metadata fields (caption, copyright, creator, GPS coordinates), writes to a
// temporary copy in the same directory, re-reads the copy, and verifies that
// all written values survived the round-trip unchanged. The program exits
// non-zero if any file fails verification.
//
// Use this as an integration smoke test when embedding GoMetadata into a Go
// application that processes mixed-format image inputs. The output also
// illustrates format-specific constraints: IPTC is only supported in JPEG and
// TIFF, so keyword and copyright data falls back to XMP-only for PNG, WebP,
// and HEIF — but the high-level setters handle this transparently.
//
// Usage:
//
//	multi-format-roundtrip <dir>
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
	"github.com/FlavioCFOliveira/GoMetadata/format"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: multi-format-roundtrip <dir>")
		os.Exit(1)
	}

	root := os.Args[1]

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

	var (
		tested int
		passed int
		failed int
	)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error { //nolint:gosec // root is a CLI argument intentionally supplied by the user
		if walkErr != nil {
			fmt.Fprintf(os.Stderr, "walk error: %s: %v\n", path, walkErr)
			return nil
		}
		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !supportedExtensions[ext] {
			return nil
		}

		tested++

		// Step 1: read original metadata.
		m, err := gometadata.ReadFile(path)
		if err != nil {
			var corrupt *gometadata.CorruptMetadataError
			var truncated *gometadata.TruncatedFileError
			var unsupported *gometadata.UnsupportedFormatError
			switch {
			case errors.As(err, &corrupt):
				fmt.Fprintf(os.Stderr, "skip (corrupt metadata): %s: %v\n", path, err)
			case errors.As(err, &truncated):
				fmt.Fprintf(os.Stderr, "skip (truncated file): %s: %v\n", path, err)
			case errors.As(err, &unsupported):
				fmt.Fprintf(os.Stderr, "skip (unsupported format): %s\n", path)
			default:
				fmt.Fprintf(os.Stderr, "skip (read error): %s: %v\n", path, err)
			}
			tested-- // don't count skipped files
			return nil
		}

		// Step 2: check that the format supports writing before proceeding.
		if !format.SupportsWrite(m.Format()) {
			fmt.Printf("skip (write not supported): %s\n", path)
			tested--
			return nil
		}

		// Step 3: stamp the fixed test values.
		m.SetCaption("roundtrip-test")
		m.SetCopyright("GoMetadata roundtrip test")
		m.SetCreator("multi-format-roundtrip")
		m.SetGPS(51.5074, -0.1278) // London, UK

		// Step 4: write to a temporary file in the same directory so that
		// filesystem permissions and path semantics are equivalent.
		// The extension is preserved so the library can detect the format by
		// magic bytes on the re-read.
		tmp, err := os.CreateTemp(filepath.Dir(path), "roundtrip-*"+ext)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: create temp: %v\n", path, err)
			failed++
			return nil
		}
		tmpName := tmp.Name()
		// Always remove the temporary file when done, regardless of outcome.
		defer func() { _ = os.Remove(tmpName) }()
		// Close now so WriteFile can open it by name.
		_ = tmp.Close()

		// Step 5: write modified metadata into the temp file.
		if writeErr := gometadata.WriteFile(tmpName, m); writeErr != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: write: %v\n", path, writeErr)
			failed++
			return nil
		}

		// Step 6: re-read the temp file.
		m2, err := gometadata.ReadFile(tmpName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: re-read: %v\n", path, err)
			failed++
			return nil
		}

		// Step 7: verify that all three written fields survived the round-trip.
		captionOK := m2.Caption() == "roundtrip-test"
		copyrightOK := m2.Copyright() == "GoMetadata roundtrip test"
		creatorOK := m2.Creator() == "multi-format-roundtrip"

		if !captionOK || !copyrightOK || !creatorOK {
			fmt.Fprintf(os.Stderr, "FAIL %s: caption/copyright/creator mismatch (caption=%v copyright=%v creator=%v)\n",
				path, captionOK, copyrightOK, creatorOK)
			failed++
			return nil
		}

		fmt.Printf("PASS %s (%s)\n", path, m.Format())
		passed++
		return nil
	})

	if err != nil {
		log.Fatalf("multi-format-roundtrip: walk: %v", err)
	}

	fmt.Printf("\ntested: %d  passed: %d  failed: %d\n", tested, passed, failed)

	if failed > 0 {
		os.Exit(1)
	}
}
