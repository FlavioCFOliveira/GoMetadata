package iptc

// iptc_task206_test.go — regression coverage for sprint 44 task #206:
// Records[0] (the internal UTF-8-flag pseudo-dataset) is now backed by
// fixed-size storage (utf8Slot/utf8Val) embedded directly in *IPTC instead of
// a freshly heap-allocated []byte{1} + append-grown slice, eliminating 2
// allocations per flag-set with no change to the observable shape of
// Records[0].

import (
	"bytes"
	"testing"
)

// TestUTF8FlagRecords0ShapeUnchanged verifies that after Parse sets the UTF-8
// flag, Records[0] has exactly the same observable shape it always has: one
// Dataset with Record=0, DataSet=0, Value=[]byte{1}.
func TestUTF8FlagRecords0ShapeUnchanged(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{1, DS1CodedCharacterSet, []byte{0x1B, 0x25, 0x47}},
		{2, DS2Caption, []byte("café")},
	})
	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := len(i.Records[0]); got != 1 {
		t.Fatalf("len(Records[0]) = %d, want 1", got)
	}
	ds := i.Records[0][0]
	if ds.Record != 0 {
		t.Errorf("Records[0][0].Record = %d, want 0", ds.Record)
	}
	if ds.DataSet != 0 {
		t.Errorf("Records[0][0].DataSet = %d, want 0", ds.DataSet)
	}
	if !bytes.Equal(ds.Value, []byte{1}) {
		t.Errorf("Records[0][0].Value = %v, want [1]", ds.Value)
	}
	if !i.isUTF8() {
		t.Error("isUTF8() = false after 1:90 declaration, want true")
	}
}

// TestUTF8FlagSetUTF8IfNeededShapeUnchanged verifies the same Records[0]
// shape for the write-path entry point (setUTF8IfNeeded, reached via
// AddKeyword/AddCreator/setRecord2/etc. whenever a caller-supplied value
// contains non-ASCII bytes and no 1:90 declaration exists yet).
func TestUTF8FlagSetUTF8IfNeededShapeUnchanged(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.AddKeyword("café") // non-ASCII, no prior UTF-8 declaration

	if got := len(i.Records[0]); got != 1 {
		t.Fatalf("len(Records[0]) = %d, want 1", got)
	}
	ds := i.Records[0][0]
	if ds.Record != 0 || ds.DataSet != 0 {
		t.Errorf("Records[0][0] = {Record:%d DataSet:%d}, want {0 0}", ds.Record, ds.DataSet)
	}
	if !bytes.Equal(ds.Value, []byte{1}) {
		t.Errorf("Records[0][0].Value = %v, want [1]", ds.Value)
	}
	if !i.isUTF8() {
		t.Error("isUTF8() = false after AddKeyword with non-ASCII content, want true")
	}
}

// TestUTF8FlagExternalAppendDoesNotCorrupt verifies that appending to the
// exported Records[0] slice from outside the package does not corrupt the
// internal UTF-8 flag: the cap-clamped ([:1:1]) slice returned by
// setUTF8Flag forces any external append to allocate a brand-new backing
// array rather than growing into memory owned by i's own utf8Slot/utf8Val
// arrays, so the original flag Dataset (copied by value into the new array)
// still reports Value[0] == 1 and isUTF8() still returns true afterward.
func TestUTF8FlagExternalAppendDoesNotCorrupt(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{1, DS1CodedCharacterSet, []byte{0x1B, 0x25, 0x47}},
		{2, DS2Caption, []byte("caption")},
	})
	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !i.isUTF8() {
		t.Fatal("isUTF8() = false before external append, want true (precondition)")
	}

	// Simulate an external caller appending to the exported Records[0] slice.
	// cap(Records[0]) == 1 (set by setUTF8Flag's [:1:1] clamp), so this append
	// must reallocate into a fresh backing array rather than writing into
	// i.utf8Slot.
	before := i.Records[0][0].Value
	i.Records[0] = append(i.Records[0], Dataset{Record: 9, DataSet: 9, Value: []byte{9}})

	if got := len(i.Records[0]); got != 2 {
		t.Fatalf("len(Records[0]) after external append = %d, want 2", got)
	}
	if !i.isUTF8() {
		t.Error("isUTF8() = false after external append, want true (flag must survive)")
	}
	if !bytes.Equal(i.Records[0][0].Value, []byte{1}) {
		t.Errorf("Records[0][0].Value after external append = %v, want [1] (original flag entry must be unchanged)", i.Records[0][0].Value)
	}
	// The original flag Dataset's Value slice header was copied by value into
	// the new backing array; it must still point at the same underlying byte
	// as before the append (i.utf8Val), not a copy — this is what makes the
	// "no corruption" property hold without needing a defensive deep copy.
	if &before[0] != &i.Records[0][0].Value[0] {
		t.Error("Records[0][0].Value no longer aliases the original flag byte after external append")
	}

	// A second, independent *IPTC must not be affected by the first one's
	// external append: utf8Slot/utf8Val are per-instance, not shared state.
	i2, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse (second instance): %v", err)
	}
	if got := len(i2.Records[0]); got != 1 {
		t.Errorf("second *IPTC: len(Records[0]) = %d, want 1 (must be unaffected by the first instance's external append)", got)
	}
	if !i2.isUTF8() {
		t.Error("second *IPTC: isUTF8() = false, want true")
	}
}
