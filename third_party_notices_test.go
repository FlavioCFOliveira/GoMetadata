package gometadata_test

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

// adaptedHeader matches the per-file attribution header documented in
// docs/THIRD_PARTY.md:
//
//	// Adapted from <project> <file>:<symbol>, Copyright ..., SPDX-License-Identifier: <id>
//
// Group 1 is the project name, group 2 the SPDX identifier. The prefix is
// assembled at run time so this file does not match itself.
var adaptedHeader = regexp.MustCompile(`(?m)^// ` + `Adapted from (\S+) .*SPDX-License-Identifier: (\S+)\s*$`)

// adaptedMarker flags a line that claims adapted content, whether or not it
// is well formed, so a malformed header cannot escape the check.
var adaptedMarker = regexp.MustCompile(`(?m)^// ` + `Adapted from `)

// TestThirdPartyNotices_EveryAdaptedFileIsRegistered fails when a .go file
// carries an adaptation header without a matching entry in
// THIRD_PARTY_NOTICES.md or without the licence text in LICENSES/.
func TestThirdPartyNotices_EveryAdaptedFileIsRegistered(t *testing.T) {
	t.Parallel()
	// os.Root confines every file operation to the repository root.
	root, err := os.OpenRoot(".")
	if err != nil {
		t.Fatalf("open repository root: %v", err)
	}
	defer func() { _ = root.Close() }()
	repo := root.FS()
	notices, err := fs.ReadFile(repo, "THIRD_PARTY_NOTICES.md")
	if err != nil {
		t.Fatalf("THIRD_PARTY_NOTICES.md is required: %v", err)
	}
	register := string(notices)
	// LICENSE must remain the MIT licence of the project.
	lic, err := fs.ReadFile(repo, "LICENSE")
	if err != nil || !strings.HasPrefix(string(lic), "MIT License") {
		t.Fatalf("LICENSE must start with %q (read error: %v)", "MIT License", err)
	}

	checked := 0
	walkErr := fs.WalkDir(repo, ".", func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", file, err)
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "testdata", "LICENSES":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(file, ".go") {
			return nil
		}
		src, err := fs.ReadFile(repo, file)
		if err != nil {
			return fmt.Errorf("read %s: %w", file, err)
		}
		if !adaptedMarker.Match(src) {
			return nil
		}
		checked++
		slash := path.Clean(file)
		if !strings.Contains(register, "`"+slash+"`") {
			t.Errorf("%s has an adaptation header but is not listed in THIRD_PARTY_NOTICES.md", slash)
		}
		headers := adaptedHeader.FindAllSubmatch(src, -1)
		if len(headers) != len(adaptedMarker.FindAll(src, -1)) {
			t.Errorf("%s has a malformed adaptation header (see docs/THIRD_PARTY.md)", slash)
		}
		for _, h := range headers {
			project, spdx := string(h[1]), string(h[2])
			if !strings.Contains(register, "## "+project) && !strings.Contains(strings.ToLower(register), "## "+strings.ToLower(project)) {
				t.Errorf("%s: project %q has no entry in THIRD_PARTY_NOTICES.md", slash, project)
			}
			if _, statErr := fs.Stat(repo, path.Join("LICENSES", spdx+".txt")); statErr != nil {
				t.Errorf("%s: licence %q has no file LICENSES/%s.txt", slash, spdx, spdx)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if checked == 0 {
		t.Fatal("no adapted files found; the walk is not reaching the source tree")
	}
}
