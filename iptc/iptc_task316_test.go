package iptc

// iptc_task316_test.go — task #316: IIM 4.2 marks Contact (2:118) and
// Writer/Editor (2:122) repeatable (IPTC-NAA Information Interchange Model
// 4.2, §6 Application Record: "Repeatable" for both). Verified: Parse and
// Encode keep every occurrence in order; these tests pin that behaviour.

import (
	"bytes"
	"testing"
)

func TestContactAndWriterEditorRepeatable(t *testing.T) {
	t.Parallel()
	for _, n := range []uint8{DS2Contact, DS2CaptionWriter} {
		info, ok := LookupDataset(2, n)
		if !ok || !info.Repeatable {
			t.Errorf("LookupDataset(2, %d) = %+v, %v; IIM 4.2 §6 marks it repeatable", n, info, ok)
		}
	}

	in := &IPTC{}
	values := []struct {
		ds uint8
		v  string
	}{
		{DS2Contact, "Contact A"}, {DS2Contact, "Contact B"},
		{DS2CaptionWriter, "Writer A"}, {DS2CaptionWriter, "Writer B"},
	}
	for _, x := range values {
		in.Records[2] = append(in.Records[2], Dataset{Record: 2, DataSet: x.ds, Value: []byte(x.v)})
	}
	raw, err := Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var got []string
	for _, d := range out.Records[2] {
		if d.DataSet == DS2Contact || d.DataSet == DS2CaptionWriter {
			got = append(got, string(d.Value))
		}
	}
	want := []string{"Contact A", "Contact B", "Writer A", "Writer B"}
	if len(got) != len(want) {
		t.Fatalf("round trip kept %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("occurrence %d = %q, want %q", i, got[i], want[i])
		}
	}
	// Re-encoding the parsed IPTC is stable.
	again, err := Encode(out)
	if err != nil || !bytes.Equal(again, raw) {
		t.Errorf("re-encode differs: %v", err)
	}
}
