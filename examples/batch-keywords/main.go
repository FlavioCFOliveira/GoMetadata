// Command batch-keywords appends a keyword to every image file in a directory
// tree. Files that cannot be read or written are skipped with a warning;
// processing continues for the remaining files.
//
// Usage:
//
//	batch-keywords [-dry-run] <keyword> <dir>
//
// Flags:
//
//	-dry-run    print what would be changed without writing any files
//
// Supported extensions: .jpg, .jpeg, .png, .tiff, .tif
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
)

// supportedExtensions is the set of file suffixes (lower-case) that this
// tool will process. Format detection inside the library uses magic bytes,
// but the extension pre-filter avoids unnecessary I/O on non-image files.
var supportedExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".tiff": true,
	".tif":  true,
}

func main() {
	dryRun := flag.Bool("dry-run", false, "print changes without writing")
	flag.Parse()

	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: batch-keywords [-dry-run] <keyword> <dir>")
		os.Exit(1)
	}

	keyword := flag.Arg(0)
	root := flag.Arg(1)

	if keyword == "" {
		log.Fatal("batch-keywords: keyword must not be empty")
	}

	var (
		processed int
		skipped   int
		changed   int
	)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Log the error and continue — do not abort the entire walk.
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
			fmt.Fprintf(os.Stderr, "skip (read error): %s: %v\n", path, err)
			skipped++
			return nil
		}

		// Check whether the keyword is already present to avoid duplicates.
		existing := m.Keywords()
		for _, kw := range existing {
			if kw == keyword {
				fmt.Printf("already has keyword: %s\n", path)
				return nil
			}
		}

		updated := append(existing, keyword)

		if *dryRun {
			fmt.Printf("would add %q to: %s\n", keyword, path)
			changed++
			return nil
		}

		m.SetKeywords(updated)
		if err := gometadata.WriteFile(path, m); err != nil {
			fmt.Fprintf(os.Stderr, "skip (write error): %s: %v\n", path, err)
			skipped++
			return nil
		}

		fmt.Printf("added %q to: %s\n", keyword, path)
		changed++
		return nil
	})

	if err != nil {
		log.Fatalf("batch-keywords: walk: %v", err)
	}

	action := "updated"
	if *dryRun {
		action = "would update"
	}
	fmt.Printf("\nprocessed: %d  %s: %d  skipped: %d\n", processed, action, changed, skipped)
}
