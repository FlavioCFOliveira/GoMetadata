package gometadata

// xmp_tree_task283_test.go — end-to-end regression test through the public
// Read/Write API for task #283: the attributes past the 32nd on one element
// were dropped on read.

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// topLevelProperties lists the top-level XMP properties of packet as
// "namespace local" strings, using encoding/xml as an independent parser.
func topLevelProperties(t *testing.T, packet []byte) map[string]bool {
	t.Helper()
	const rdfNS = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	props := map[string]bool{}
	dec := xml.NewDecoder(bytes.NewReader(packet))
	depth, descDepth := 0, -1
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch e := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case e.Name.Space == rdfNS && e.Name.Local == "Description" && descDepth < 0:
				descDepth = depth
				for _, a := range e.Attr {
					if a.Name.Space != "" && a.Name.Space != rdfNS && a.Name.Space != "xmlns" {
						props[a.Name.Space+" "+a.Name.Local] = true
					}
				}
			case descDepth > 0 && depth == descDepth+1:
				props[e.Name.Space+" "+e.Name.Local] = true
			}
		case xml.EndElement:
			if depth == descDepth {
				descDepth = -1
			}
			depth--
		}
	}
	return props
}

// TestTask283Canon7DReadsEveryProperty reads the Lightroom XMP of the corpus
// image "Canon EOS 7D.jpg", whose rdf:Description carries 131 attributes,
// and requires every top-level property found by encoding/xml; then a
// caption and copyright edit must keep every other property.
func TestTask283Canon7DReadsEveryProperty(t *testing.T) {
	t.Parallel()
	path := filepath.Join("testdata", "corpus", "jpeg", "metadata-extractor", "Canon EOS 7D.jpg")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("corpus file %s is absent; run 'make testdata'", path)
	}
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := topLevelProperties(t, m.RawXMP())
	if len(want) < 131 {
		t.Fatalf("independent parse found %d properties, want >= 131 (wrong file?)", len(want))
	}
	have := map[string]bool{}
	for ns, props := range m.XMP.Properties {
		for k := range props {
			top := k
			if i := strings.IndexAny(k, ".["); i > 0 {
				top = k[:i]
			}
			have[ns+" "+top] = true
		}
	}
	for p := range want {
		if !have[p] {
			t.Errorf("property %s not read", p)
		}
	}

	m.SetCaption("caption")
	m.SetCopyright("copyright")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	expected := expectedPacket(t, m.RawXMP(), func(x *xmp.XMP) {
		x.SetCaption("caption")
		x.SetCopyright("copyright")
	})
	if got := writtenPacket(t, out.Bytes()); !bytes.Equal(got, expected) {
		t.Fatalf("written XMP data model differs:\n--- want\n%s\n--- got\n%s", expected, got)
	}
}
