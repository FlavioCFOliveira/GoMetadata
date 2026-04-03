// Package testutil provides shared helpers for tests across all packages.
package testutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// CorpusFiles returns the paths of all files under testdata/corpus/<subdir>
// relative to the caller's package directory, walking subdirectories recursively.
// The test is skipped if the directory does not exist or contains no files.
func CorpusFiles(t *testing.T, subdir string) []string {
	t.Helper()
	dir := filepath.Join("testdata", "corpus", subdir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("corpus directory %s does not exist; run 'make testdata' to download images", dir)
	}
	var paths []string
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk corpus dir: %v", err)
	}
	if len(paths) == 0 {
		t.Skipf("corpus directory %s is empty; run 'make testdata' to download images", dir)
	}
	return paths
}
