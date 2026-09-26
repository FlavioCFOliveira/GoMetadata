package exif_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// corpusEXIFs returns the parsed EXIF of every corpus and fixture file that
// carries EXIF, keyed by path. It skips the test when no corpus is present.
func corpusEXIFs(t *testing.T) map[string]*exif.EXIF {
	t.Helper()
	roots := []string{"testdata", "../testdata/corpus", "../testdata/fixtures", "../format/tiff/testdata"}
	out := make(map[string]*exif.EXIF)
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, rerr := os.ReadFile(path) //nolint:gosec // G304: fixed test corpus paths
			if rerr != nil {
				return fmt.Errorf("read %s: %w", path, rerr)
			}
			m, rerr := gometadata.Read(bytes.NewReader(data))
			if rerr != nil || m.EXIF == nil {
				return nil //nolint:nilerr // files without parseable EXIF are not part of this property
			}
			out[path] = m.EXIF
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", root, walkErr)
		}
	}
	if len(out) == 0 {
		t.Skip("no corpus files with EXIF present; run 'make testdata' to download images")
	}
	return out
}

// TestEncodedSizeMatchesEncode proves EncodedSize(e) == len(Encode(e)) and
// that EncodeInto produces the same bytes as Encode, for every corpus file.
func TestEncodedSizeMatchesEncode(t *testing.T) {
	t.Parallel()
	for path, e := range corpusEXIFs(t) {
		want, encErr := exif.Encode(e)
		n, sizeErr := exif.EncodedSize(e)
		if (encErr == nil) != (sizeErr == nil) {
			t.Errorf("%s: Encode err=%v, EncodedSize err=%v", path, encErr, sizeErr)
			continue
		}
		if encErr != nil {
			continue
		}
		if n != len(want) {
			t.Errorf("%s: EncodedSize=%d, len(Encode)=%d", path, n, len(want))
			continue
		}

		// Pre-sized destination: written in place, identical bytes.
		dst := make([]byte, 3, n+16)
		got, err := exif.EncodeInto(dst, e)
		if err != nil {
			t.Fatalf("%s: EncodeInto: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: EncodeInto (pre-sized) differs from Encode", path)
		}
		if n > 0 && &got[0] != &dst[:1][0] {
			t.Errorf("%s: EncodeInto did not reuse a sufficiently large dst", path)
		}

		// Undersized destination: a new buffer, dst untouched.
		small := []byte{0xAA}
		got, err = exif.EncodeInto(small[:0], e)
		if err != nil {
			t.Fatalf("%s: EncodeInto (small): %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: EncodeInto (small) differs from Encode", path)
		}
		if small[0] != 0xAA {
			t.Errorf("%s: EncodeInto wrote into an undersized dst", path)
		}
	}
}

// TestEncodedSizeNil verifies EncodedSize and EncodeInto report ErrNilEXIF
// exactly like Encode.
func TestEncodedSizeNil(t *testing.T) {
	t.Parallel()
	if _, err := exif.EncodedSize(nil); err != exif.ErrNilEXIF { //nolint:errorlint // sentinel returned unwrapped
		t.Errorf("EncodedSize(nil) err = %v, want ErrNilEXIF", err)
	}
	if _, err := exif.EncodeInto(nil, nil); err != exif.ErrNilEXIF { //nolint:errorlint // sentinel returned unwrapped
		t.Errorf("EncodeInto(nil, nil) err = %v, want ErrNilEXIF", err)
	}
}
