package testutil

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "overwrite golden files with current output") //nolint:gochecknoglobals // test flag registered at package init

// CheckGolden compares got (marshalled to JSON) against the golden file at
// testdata/golden/<name>.json. When -update is set, it writes the current
// output instead of comparing.
func CheckGolden(t *testing.T, name string, got any) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".json")
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir golden: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Fatalf("golden file %s missing; run with -update to create it", path)
	}
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(want) != string(data) {
		t.Errorf("golden mismatch for %s\ngot:\n%s\nwant:\n%s", name, data, want)
	}
}
