package gometadata

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// corpusRejectsFile pins the files under testdata that the library rejects, and
// the stage at which it does so. They are malformed inputs (truncated or
// corrupt files from the exiv2 and metadata-extractor suites, fuzz seeds) and
// non-image files; rejecting them is the specified behaviour.
const corpusRejectsFile = "testdata/corpus_write_rejects.txt"

// corpusUpdateEnv, when set, rewrites corpusRejectsFile from the current results.
const corpusUpdateEnv = "GOMETADATA_UPDATE_CORPUS_REJECTS"

// Stages of the corpus pipeline, in order.
const (
	stageRead    = "read"    // Read of the file
	stageWrite   = "write"   // Write of the file as read
	stageCaption = "caption" // Write after SetCaption
)

// corpusPipeline runs Read, an unmodified Write, then SetCaption and Write on
// data, and returns the first stage that failed ("" when none did) with its
// error. A panic is reported as a failure of the stage it happened in.
func corpusPipeline(data []byte) (stage string, err error) {
	stage = stageRead
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	m, err := Read(bytes.NewReader(data))
	if err != nil {
		return stage, err
	}
	stage = stageWrite
	if werr := Write(bytes.NewReader(data), &bytes.Buffer{}, m); werr != nil {
		return stage, werr
	}
	stage = stageCaption
	m.SetCaption("corpus write test")
	if werr := Write(bytes.NewReader(data), &bytes.Buffer{}, m); werr != nil {
		return stage, werr
	}
	return "", nil
}

// TestCorpusWriteAll runs Read, an unmodified Write, then SetCaption and Write
// on every file under testdata (security audit F-07a, task #379: a valid TIFF
// whose image blocks overlap, such as a Sony ARW, must be written). A file must
// pass every stage, except the pinned malformed inputs of corpusRejectsFile,
// which must fail at the pinned stage or later in the pipeline order.
func TestCorpusWriteAll(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat(filepath.Join("testdata", "corpus")); os.IsNotExist(err) {
		t.Skip("corpus absent (run testdata/download.sh): testdata/corpus")
	}
	var files []string
	err := filepath.WalkDir("testdata", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.ToSlash(path) == corpusRejectsFile {
			return err
		}
		files = append(files, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk testdata: %v", err)
	}
	sort.Strings(files)

	type result struct {
		stage string
		err   error
	}
	results := make([]result, len(files))
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.GOMAXPROCS(0) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				data, rerr := os.ReadFile(files[i])
				if rerr != nil {
					results[i] = result{stageRead, rerr}
					continue
				}
				s, e := corpusPipeline(data)
				results[i] = result{s, e}
			}
		}()
	}
	for i := range files {
		next <- i
	}
	close(next)
	wg.Wait()

	if os.Getenv(corpusUpdateEnv) != "" {
		var sb strings.Builder
		for i, r := range results {
			if r.stage != "" {
				fmt.Fprintf(&sb, "%s\t%s\n", r.stage, files[i])
			}
		}
		if err := os.WriteFile(corpusRejectsFile, []byte(sb.String()), 0o600); err != nil {
			t.Fatalf("write %s: %v", corpusRejectsFile, err)
		}
	}
	pinned := readCorpusRejects(t)

	var passed, rejected, fixed int
	for i, r := range results {
		want, isPinned := pinned[files[i]]
		switch {
		case r.stage == "":
			passed++
			if isPinned {
				fixed++
				t.Logf("%s: now passes every stage; remove it from %s", files[i], corpusRejectsFile)
			}
		case !isPinned:
			t.Errorf("%s: %s failed: %v", files[i], r.stage, r.err)
		case r.stage != want:
			t.Errorf("%s: failed at %s, pinned as failing at %s: %v", files[i], r.stage, want, r.err)
		default:
			rejected++
		}
	}
	t.Logf("corpus: %d files, %d pass every stage, %d pinned rejects, %d pinned rejects now passing", len(files), passed, rejected, fixed)
}

// readCorpusRejects returns the pinned rejects: file path to stage.
func readCorpusRejects(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(corpusRejectsFile)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with %s=1)", corpusRejectsFile, err, corpusUpdateEnv)
	}
	pinned := make(map[string]string)
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		stage, path, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("%s: malformed line %q", corpusRejectsFile, line)
		}
		pinned[path] = stage
	}
	return pinned
}
