package iptc

import "testing"

// Adapted from bep/imagemeta metadecoder_iptc_fields.json:record,id,repeatable, Copyright (c) 2022 Bjørn Erik Pedersen, SPDX-License-Identifier: MIT
//
// See THIRD_PARTY_NOTICES.md. refDataset lists the 116 datasets of the
// reference table (commit fbefe9cdc214) with their repeatable flag.
type refDataset struct {
	record, number uint8
	repeatable     bool
}

var refDatasets = []refDataset{ //nolint:gochecknoglobals // read-only reference table
	{1, 0, false},
	{1, 5, true},
	{1, 20, false},
	{1, 22, false},
	{1, 30, false},
	{1, 40, false},
	{1, 50, true},
	{1, 60, false},
	{1, 70, false},
	{1, 80, false},
	{1, 90, false},
	{1, 100, false},
	{1, 120, false},
	{1, 122, false},
	{2, 0, false},
	{2, 3, false},
	{2, 4, true},
	{2, 5, false},
	{2, 7, false},
	{2, 8, false},
	{2, 10, false},
	{2, 12, true},
	{2, 15, false},
	{2, 20, true},
	{2, 22, false},
	{2, 25, true},
	{2, 26, true},
	{2, 27, true},
	{2, 30, false},
	{2, 35, false},
	{2, 37, false},
	{2, 38, false},
	{2, 40, false},
	{2, 42, false},
	{2, 45, true},
	{2, 47, true},
	{2, 50, true},
	{2, 55, false},
	{2, 60, false},
	{2, 62, false},
	{2, 63, false},
	{2, 65, false},
	{2, 70, false},
	{2, 75, false},
	{2, 80, true},
	{2, 85, true},
	{2, 90, false},
	{2, 92, false},
	{2, 95, false},
	{2, 100, false},
	{2, 101, false},
	{2, 103, false},
	{2, 105, false},
	{2, 110, false},
	{2, 115, false},
	{2, 116, false},
	{2, 118, true},
	{2, 120, false},
	{2, 121, false},
	{2, 122, true},
	{2, 125, false},
	{2, 130, false},
	{2, 131, false},
	{2, 135, false},
	{2, 150, false},
	{2, 151, false},
	{2, 152, false},
	{2, 153, false},
	{2, 154, false},
	{2, 184, false},
	{2, 185, false},
	{2, 186, false},
	{2, 187, false},
	{2, 188, false},
	{2, 200, false},
	{2, 201, false},
	{2, 202, false},
	{2, 221, false},
	{2, 225, false},
	{2, 228, false},
	{2, 230, false},
	{2, 231, false},
	{2, 232, false},
	{2, 255, true},
	{3, 0, false},
	{3, 10, false},
	{3, 20, false},
	{3, 30, false},
	{3, 40, false},
	{3, 50, false},
	{3, 55, false},
	{3, 60, false},
	{3, 64, false},
	{3, 65, false},
	{3, 66, false},
	{3, 70, false},
	{3, 80, false},
	{3, 84, false},
	{3, 85, false},
	{3, 86, false},
	{3, 90, false},
	{3, 100, false},
	{3, 102, false},
	{3, 110, false},
	{3, 120, false},
	{3, 125, false},
	{3, 130, false},
	{3, 135, false},
	{3, 140, false},
	{3, 145, false},
	{7, 10, false},
	{7, 20, false},
	{7, 90, false},
	{7, 95, false},
	{8, 10, true},
	{9, 10, false},
}

func TestLookupDataset_ReferenceTableRegistered(t *testing.T) {
	t.Parallel()
	for _, r := range refDatasets {
		info, ok := LookupDataset(r.record, r.number)
		if !ok {
			t.Errorf("dataset %d:%d of the reference table is not registered", r.record, r.number)
			continue
		}
		if info.Record != r.record || info.Number != r.number || info.Name == "" {
			t.Errorf("dataset %d:%d: malformed entry %+v", r.record, r.number, info)
		}
		if info.Repeatable != r.repeatable {
			t.Errorf("dataset %d:%d (%s): Repeatable = %v, reference says %v", r.record, r.number, info.Name, info.Repeatable, r.repeatable)
		}
	}
	if len(datasetTable) != len(refDatasets) {
		t.Errorf("len(datasetTable) = %d, want %d", len(datasetTable), len(refDatasets))
	}
}

func TestDatasetTable_SortedAndUnique(t *testing.T) {
	t.Parallel()
	for i := 1; i < len(datasetTable); i++ {
		p, c := datasetTable[i-1], datasetTable[i]
		if uint16(p.Record)<<8|uint16(p.Number) >= uint16(c.Record)<<8|uint16(c.Number) {
			t.Fatalf("datasetTable not strictly ordered at %d:%d -> %d:%d", p.Record, p.Number, c.Record, c.Number)
		}
	}
	for _, e := range datasetTable {
		if e.MaxLen != 0 && e.MinLen > e.MaxLen {
			t.Errorf("%d:%d (%s): MinLen %d > MaxLen %d", e.Record, e.Number, e.Name, e.MinLen, e.MaxLen)
		}
		switch e.Record {
		case 1, 2, 3, 7, 8, 9:
		default:
			t.Errorf("%d:%d (%s): record outside 1, 2, 3, 7, 8, 9", e.Record, e.Number, e.Name)
		}
	}
}

// TestDatasetTable_AgreesWithRecord2Limits proves the new table and the
// write-time truncation table datasetMaxLen (IIM 4.2 §2.2) cannot drift.
func TestDatasetTable_AgreesWithRecord2Limits(t *testing.T) {
	t.Parallel()
	for num, limit := range datasetMaxLen {
		if limit == 0 {
			continue
		}
		info, ok := LookupDataset(2, uint8(num))
		if !ok {
			t.Errorf("2:%d has a write limit of %d but no table entry", num, limit)
			continue
		}
		if info.MaxLen != limit {
			t.Errorf("2:%d (%s): MaxLen = %d, datasetMaxLen = %d", num, info.Name, info.MaxLen, limit)
		}
	}
}

func TestLookupDataset_Spec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rec, num   uint8
		name       string
		min, max   int
		repeatable bool
		standard   bool
	}{
		{1, 90, "CodedCharacterSet", 0, 32, false, true},
		{2, 25, "Keywords", 0, 64, true, true},
		{2, 120, "CaptionAbstract", 0, 2000, false, true},
		{2, 12, "SubjectReference", 13, 236, true, true},
		{2, 118, "Contact", 0, 128, true, true},
		{7, 10, "SizeMode", 1, 1, false, true},
		{8, 10, "Subfile", 0, 0, true, true},
		{9, 10, "ConfirmedObjectDataSize", 0, 4, false, true},
		{2, 255, "CatalogSets", 0, 0, true, false},
	}
	for _, tc := range cases {
		info, ok := LookupDataset(tc.rec, tc.num)
		if !ok || info.Name != tc.name || info.MinLen != tc.min || info.MaxLen != tc.max ||
			info.Repeatable != tc.repeatable || info.Standard != tc.standard {
			t.Errorf("LookupDataset(%d, %d) = %+v, %v", tc.rec, tc.num, info, ok)
		}
	}
	for _, miss := range [][2]uint8{{0, 0}, {2, 1}, {4, 10}, {5, 10}, {6, 10}, {10, 10}, {255, 255}} {
		if _, ok := LookupDataset(miss[0], miss[1]); ok {
			t.Errorf("LookupDataset(%d, %d) unexpectedly found", miss[0], miss[1])
		}
	}
}

func TestLookupDataset_ZeroAllocs(t *testing.T) { //nolint:paralleltest // testing.AllocsPerRun panics under t.Parallel
	var sink DatasetInfo
	if n := testing.AllocsPerRun(1000, func() { sink, _ = LookupDataset(2, 120) }); n != 0 {
		t.Errorf("LookupDataset allocs/op = %v, want 0", n)
	}
	_ = sink
}

func BenchmarkLookupDataset(b *testing.B) {
	keys := [...][2]uint8{{2, 120}, {2, 25}, {1, 90}, {7, 95}, {3, 145}, {2, 1}}
	b.ReportAllocs()
	var sink DatasetInfo
	for i := 0; b.Loop(); i++ {
		k := keys[i%len(keys)]
		sink, _ = LookupDataset(k[0], k[1])
	}
	_ = sink
}
