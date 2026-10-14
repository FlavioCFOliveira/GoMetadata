package gometadata

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// Task #295: a JPEG whose XMP needs extended XMP must be written
// deterministically — the GUID is the MD5 digest of the extended
// serialisation (Adobe XMP Specification Part 3 §1.1.3.1), not a random value.

// checkDeterministicExtendedWrite reads src, sets a caption, writes it twice
// from two independent reads and checks that both outputs are identical and
// that every XMP property of src survives with its value.
func checkDeterministicExtendedWrite(t *testing.T, src []byte) {
	t.Helper()
	write := func() ([]byte, *Metadata) {
		m, err := Read(bytes.NewReader(src))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		m.SetCaption("Task 295 caption")
		var out bytes.Buffer
		if err := Write(bytes.NewReader(src), &out, m); err != nil {
			t.Fatalf("Write: %v", err)
		}
		return out.Bytes(), m
	}
	first, m := write()
	second, _ := write()
	if !bytes.Equal(first, second) {
		t.Fatal("two writes of the same input differ")
	}
	if !bytes.Contains(first, []byte("http://ns.adobe.com/xmp/extension/\x00")) {
		t.Fatal("precondition: the written XMP did not need extended XMP")
	}

	got, err := Read(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("Read written file: %v", err)
	}
	if got.XMP == nil {
		t.Fatal("written file has no XMP")
	}
	for ns, props := range m.XMP.Properties {
		for local, v := range props {
			if ns == xmp.NSdc && local == "description" {
				continue
			}
			if g := got.XMP.Get(ns, local); g != v {
				t.Errorf("%s %s: got %d bytes, want %d bytes", ns, local, len(g), len(v))
			}
		}
	}
	if c := got.XMP.Caption(); c != "Task 295 caption" {
		t.Errorf("caption = %q", c)
	}
}

// TestTask295_ExtendedXMPWriteDeterministic runs the check on a JPEG built in
// memory whose XMP needs extended XMP, so it runs without the corpus.
func TestTask295_ExtendedXMPWriteDeterministic(t *testing.T) {
	t.Parallel()
	base := buildMinimalJPEG(minimalTIFFPayload())
	m, err := Read(bytes.NewReader(base))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.XMP = new(xmp.XMP)
	m.XMP.Set("http://ns.google.com/photos/1.0/depthmap/", "Data", strings.Repeat("QUJD", 40000))
	m.XMP.Set("http://ns.google.com/photos/1.0/depthmap/", "Mime", "image/png")
	var src bytes.Buffer
	if err := Write(bytes.NewReader(base), &src, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	checkDeterministicExtendedWrite(t, src.Bytes())
}

// TestTask295_CorpusExtendedXMPWriteDeterministic runs the check on the
// corpus JPEG files whose XMP, with a caption added, needs extended XMP
// (Google depth-map and photo-sphere files).
func TestTask295_CorpusExtendedXMPWriteDeterministic(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{
		"jpeg/exiv2/exiv2-bug922.jpg",
		"jpeg/exiv2/exiv2-bug922_2.jpg",
		"jpeg/exiv2/exiv2-bug922_3.jpg",
		"jpeg/metadata-extractor/Android Depth Map.jpg",
		"jpeg/metadata-extractor/Android Depth Map_2.jpg",
		"jpeg/metadata-extractor/Google Cardboard.jpg",
		"jpeg/metadata-extractor/Google Cardboard_2.jpg",
	} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			src := readCorpusBytes(t, rel)
			checkDeterministicExtendedWrite(t, src)
		})
	}
}

// readCorpusBytes returns the content of a corpus file, skipping the test
// when the corpus is absent (docs/TESTING.md §2.1).
func readCorpusBytes(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(requireCorpusFile(t, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}
