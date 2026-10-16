package gometadata

// write_task316_repeatable_test.go — task #316: Contact (2:118) and
// Writer/Editor (2:122), repeatable per IIM 4.2 §6, keep every value through
// Write.

import (
	"bytes"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// TestWrite_ContactAndWriterEditorRepeatable (task #316): two values each of
// 2:118 and 2:122 survive a JPEG write ("exiftool -IPTC:Contact
// -IPTC:Writer-Editor" on the output lists both of each).
func TestWrite_ContactAndWriterEditorRepeatable(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("testdata/fixtures/jolla.jpg")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.IPTC == nil {
		m.IPTC = &iptc.IPTC{}
	}
	m.IPTC.Records[2] = append(m.IPTC.Records[2],
		iptc.Dataset{Record: 2, DataSet: iptc.DS2Contact, Value: []byte("Contact A")},
		iptc.Dataset{Record: 2, DataSet: iptc.DS2Contact, Value: []byte("Contact B")},
		iptc.Dataset{Record: 2, DataSet: iptc.DS2CaptionWriter, Value: []byte("Writer A")},
		iptc.Dataset{Record: 2, DataSet: iptc.DS2CaptionWriter, Value: []byte("Writer B")},
	)
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	back, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read output: %v", err)
	}
	counts := map[uint8][]string{}
	for _, d := range back.IPTC.Records[2] {
		counts[d.DataSet] = append(counts[d.DataSet], string(d.Value))
	}
	if c := counts[iptc.DS2Contact]; len(c) != 2 || c[0] != "Contact A" || c[1] != "Contact B" {
		t.Errorf("2:118 = %q, want [Contact A Contact B]", c)
	}
	if w := counts[iptc.DS2CaptionWriter]; len(w) != 2 || w[0] != "Writer A" || w[1] != "Writer B" {
		t.Errorf("2:122 = %q, want [Writer A Writer B]", w)
	}
}
