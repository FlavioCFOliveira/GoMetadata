package gometadata

// write_task323_333_corpus_test.go — end-to-end checks of tasks #323, #325
// and #333 through the public API on corpus files (each skips when its
// corpus file is absent: run testdata/download.sh).

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: MD5 is the MWG IPTC digest
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/jpeg"
)

func corpusBytes(t *testing.T, parts ...string) []byte {
	t.Helper()
	path := filepath.Join(append([]string{"testdata", "corpus"}, parts...)...)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestCorpus_IPTCDigest (task #323): the digest is created for a file that has
// none, and a stale digest is refreshed (MWG Guidelines v2.0 §4.2.3.2, p. 29).
func TestCorpus_IPTCDigest(t *testing.T) {
	t.Parallel()
	t.Run("created", func(t *testing.T) {
		t.Parallel()
		src := corpusBytes(t, "jpeg", "metadata-extractor", "Google Cardboard.jpg")
		if _, _, d, _, _, _, err := jpeg.ExtractFull(bytes.NewReader(src)); err != nil || d != nil {
			t.Fatalf("precondition: digest %x, err %v", d, err)
		}
		out := writeCaption(t, src, "created")
		_, iim, d, _, _, _, err := jpeg.ExtractFull(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		want := md5.Sum(iim) //nolint:gosec // G401: MWG digest
		if iim == nil || !bytes.Equal(d, want[:]) {
			t.Errorf("digest = %x, want MD5 of the IIM %x", d, want)
		}
	})
	t.Run("stale is refreshed", func(t *testing.T) {
		t.Parallel()
		src := corpusBytes(t, "jpeg", "exiftool", "ExifTool.jpg")
		_, oldIIM, oldDigest, _, _, _, err := jpeg.ExtractFull(bytes.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		if cur := md5.Sum(oldIIM); oldDigest == nil || bytes.Equal(oldDigest, cur[:]) { //nolint:gosec // G401: MWG digest
			t.Fatalf("precondition: the digest %x must be stale", oldDigest)
		}
		out := writeCaption(t, src, "stale")
		_, iim, d, _, _, _, err := jpeg.ExtractFull(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if cur := md5.Sum(iim); !bytes.Equal(d, cur[:]) { //nolint:gosec // G401: MWG digest
			t.Errorf("digest = %x, want MD5 of the written IIM %x (the stale %x must be refreshed)", d, cur, oldDigest)
		}
	})
}

// pngChunks returns the chunk types of a PNG and the keywords of its text chunks.
func pngChunks(t *testing.T, data []byte) (types []string, keywords []string) {
	t.Helper()
	for pos := 8; pos+12 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[pos:]))
		ct := string(data[pos+4 : pos+8])
		types = append(types, ct)
		if ct == "tEXt" || ct == "zTXt" || ct == "iTXt" {
			body := data[pos+8 : pos+8+n]
			if i := bytes.IndexByte(body, 0); i >= 0 {
				keywords = append(keywords, string(body[:i]))
			}
		}
		pos += 12 + n
	}
	return types, keywords
}

// TestCorpus_PNGRawProfiles (task #325): after SetCaption no raw profile of
// EXIF, APP1 or XMP is left beside the standard chunks, and the EXIF it held
// is still there.
func TestCorpus_PNGRawProfiles(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"exiv2/ReaganSmallPng.png", "exiv2/ReaganLargePng.png", "exiv2/imagemagick.png", "exiv2/exiv2-bug922.png",
		"metadata-extractor/Issue 316 (dotnet).png", "metadata-extractor/Issue 204 (dotnet).png",
		"metadata-extractor/1ebd73c1d3fbc89782f29507364128fc.png", "metadata-extractor/0839d93f8e77e21acd0ac40a80b14b7b.png",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			src := corpusBytes(t, "png", filepath.FromSlash(name))
			before, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			out := writeCaption(t, src, "one current copy")
			types, kws := pngChunks(t, out)
			for _, kw := range kws {
				if k := strings.ToLower(kw); k == "raw profile type exif" || k == "raw profile type app1" || k == "raw profile type xmp" {
					t.Errorf("stale text chunk %q left after the write", kw)
				}
			}
			n := map[string]int{}
			for _, ct := range types {
				n[ct]++
			}
			if n["eXIf"] > 1 {
				t.Errorf("%d eXIf chunks", n["eXIf"])
			}
			after, err := Read(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("Read output: %v", err)
			}
			if after.Caption() != "one current copy" {
				t.Errorf("caption = %q", after.Caption())
			}
			if before.CameraModel() != after.CameraModel() {
				t.Errorf("camera model %q became %q", before.CameraModel(), after.CameraModel())
			}
		})
	}
}

// TestCorpus_WebPExifPreambleXMPNul (task #333): the EXIF behind the
// "Exif\0\0" identifier is read and written back once, in a standard chunk,
// and the "XMP\0" chunk is replaced by one "XMP " chunk.
func TestCorpus_WebPExifPreambleXMPNul(t *testing.T) {
	t.Parallel()
	src := corpusBytes(t, "webp", "metadata-extractor", "Issue 473 (Java).webp")
	before, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if before.CameraModel() != "iPhone X" {
		t.Fatalf("EXIF not read: camera model %q", before.CameraModel())
	}
	out := writeCaption(t, src, "webp caption")
	n := map[string]int{}
	for pos := 12; pos+8 <= len(out); {
		sz := int(binary.LittleEndian.Uint32(out[pos+4:]))
		n[string(out[pos:pos+4])]++
		pos += 8 + sz + sz%2
	}
	if n["EXIF"] != 1 || n["XMP "] != 1 || n["XMP\x00"] != 0 {
		t.Errorf("chunks after the write: %v", n)
	}
	after, err := Read(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Read output: %v", err)
	}
	if after.CameraModel() != "iPhone X" || after.Caption() != "webp caption" {
		t.Errorf("camera model %q, caption %q", after.CameraModel(), after.Caption())
	}
}
