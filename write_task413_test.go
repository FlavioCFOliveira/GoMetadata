package gometadata

// write_task413_test.go — task #413: a Write that modifies EXIF only keeps the
// source's non-standard "XMP\0" WebP chunk byte for byte; a Write that
// modifies the XMP writes the standard "XMP " chunk (Google WebP Container
// Specification, "Metadata"; RFC 9649 §2.7.1.5).

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteWebPXMPNulChunk(t *testing.T) {
	t.Parallel()
	path := filepath.Join("testdata", "corpus", "webp", "metadata-extractor", "Issue 473 (Java).webp")
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	// The chunk as the source holds it: FourCC, size, data (the size is even).
	i := bytes.Index(src, []byte("XMP\x00"))
	if i < 0 {
		t.Fatal("the fixture has no XMP\\0 chunk")
	}
	size := int(src[i+4]) | int(src[i+5])<<8 | int(src[i+6])<<16 | int(src[i+7])<<24
	chunk := src[i : i+8+size+size&1]

	write := func(set func(*Metadata)) []byte {
		m, err := Read(bytes.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		set(m)
		var out bytes.Buffer
		if err := Write(bytes.NewReader(src), &out, m); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}

	if out := write(func(m *Metadata) { m.SetISO(1234) }); !bytes.HasSuffix(out, chunk) {
		t.Error("EXIF-only write: the XMP\\0 chunk is not in the output byte for byte")
	}
	out := write(func(m *Metadata) { m.SetCaption("x") })
	if bytes.Contains(out, []byte("XMP\x00")) || !bytes.Contains(out, []byte("XMP ")) {
		t.Error("XMP write: want the standard 'XMP ' chunk and no 'XMP\\0'")
	}
}
