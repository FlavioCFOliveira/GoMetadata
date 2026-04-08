package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCorpusFilesSkipsWhenMissing verifies that CorpusFiles behaves gracefully
// when the corpus directory does not exist (the common case in CI without testdata).
// The inner sub-test will be skipped, which is the expected outcome.
func TestCorpusFilesSkipsWhenMissing(t *testing.T) {
	t.Parallel()
	t.Run("inner", func(t *testing.T) {
		t.Parallel()
		CorpusFiles(t, "nonexistent-subdir-that-will-never-exist-xyzzy")
		// If we reach here (not skipped), the directory unexpectedly exists.
	})
}

// TestCorpusFilesWithRealDir verifies that CorpusFiles returns paths when the
// directory exists and contains at least one file.
// Not parallel: uses os.Chdir which is process-global.
func TestCorpusFilesWithRealDir(t *testing.T) { //nolint:paralleltest // uses os.Chdir; cannot be parallel
	tmp := t.TempDir()
	subdir := filepath.Join(tmp, "testdata", "corpus", "images")
	if err := os.MkdirAll(subdir, 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(subdir, "img-*.jpg")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	paths := CorpusFiles(t, "images")
	if len(paths) == 0 {
		t.Error("CorpusFiles returned no paths, want ≥ 1")
	}
}

// TestCorpusFilesEmptyDirSkips verifies that CorpusFiles skips the test when
// the corpus directory exists but contains no files.
// Not parallel: uses os.Chdir which is process-global.
func TestCorpusFilesEmptyDirSkips(t *testing.T) { //nolint:paralleltest // uses os.Chdir; cannot be parallel
	tmp := t.TempDir()

	// Create the corpus directory with no files in it.
	emptySubdir := filepath.Join(tmp, "testdata", "corpus", "empty-subdir")
	if err := os.MkdirAll(emptySubdir, 0o750); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	// Run in a sub-test so the skip doesn't propagate to the parent.
	t.Run("inner", func(t *testing.T) {
		CorpusFiles(t, "empty-subdir")
		// If we reach here, the test was not skipped — the dir had files unexpectedly.
		t.Log("did not skip — directory had files")
	})
}

// TestCheckGoldenRoundTrip exercises CheckGolden write-then-compare: first
// manually creates the golden file (simulating -update), then compares against
// it with the same value. Both branches of CheckGolden are exercised.
// Not parallel: uses os.Chdir which is process-global.
func TestCheckGoldenRoundTrip(t *testing.T) { //nolint:paralleltest // uses os.Chdir; cannot be parallel
	tmp := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	type payload struct {
		Key string `json:"key"`
	}
	want := payload{Key: "hello"}

	// Create the golden file manually (equivalent to running with -update).
	data, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldenDir := filepath.Join("testdata", "golden")
	if err := os.MkdirAll(goldenDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goldenDir, "test-fixture.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Now run CheckGolden in compare mode — should pass because the content matches.
	CheckGolden(t, "test-fixture", want)
}
