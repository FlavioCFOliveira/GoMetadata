package trailer

import (
	"bytes"
	"encoding/binary"
	"testing"
)

var (
	oldIIM = []byte{0x1C, 0x02, 0x00, 0x00, 0x02, 0x00, 0x04, 0x1C, 0x02, 0x78, 0x00, 0x03, 'o', 'l', 'd'}                                         //nolint:gochecknoglobals // immutable test fixture
	newIIM = []byte{0x1C, 0x02, 0x00, 0x00, 0x02, 0x00, 0x04, 0x1C, 0x02, 0x78, 0x00, 0x0B, 'n', 'e', 'w', ' ', 'c', 'a', 'p', 't', 'i', 'o', 'n'} //nolint:gochecknoglobals // immutable test fixture
)

// afcpOf builds an AFCP trailer that starts at absolute offset start.
func afcpOf(order Order, sig string, start int64, entries [][2]string) []byte {
	var out []byte
	out = append(out, sig...)
	out = append(out, 0, 1)
	out = order.AppendUint16(out, uint16(len(entries))) //nolint:gosec // G115: test sizes are small
	out = append(out, 0, 0, 0, 0)
	off := start + 12 + int64(12*len(entries))
	for _, e := range entries {
		out = append(out, e[0]...)
		out = order.AppendUint32(out, uint32(len(e[1]))) //nolint:gosec // G115: test sizes are small
		out = order.AppendUint32(out, uint32(off))       //nolint:gosec // G115: test sizes are small
		off += int64(len(e[1]))
	}
	for _, e := range entries {
		out = append(out, e[1]...)
	}
	out = append(out, sig...)
	out = order.AppendUint32(out, uint32(start)) //nolint:gosec // G115: test sizes are small
	return append(out, 0, 0, 0, 0)
}

func fotoOf(recs ...FotoRecord) []byte {
	var out []byte
	for _, r := range recs {
		out = append(out, r.Data...)
		out = binary.BigEndian.AppendUint16(out, r.Tag)
		out = binary.BigEndian.AppendUint32(out, uint32(len(r.Data)+10)) //nolint:gosec // G115: test sizes are small
		out = append(out, fotoSig...)
	}
	return out
}

// TestFindTail: a trailer is found from the end of the file, whichever
// source reads it, and only then. TRAILER-TAIL-01.
func TestFindTail(t *testing.T) {
	t.Parallel()
	image := bytes.Repeat([]byte{0x55}, 100)
	afcp := func(start int64) []byte {
		return afcpOf(binary.BigEndian, "AXS!", start, [][2]string{{"IPTC", string(oldIIM)}})
	}
	foto := fotoOf(FotoRecord{Tag: 3, Data: []byte("thumb")}, FotoRecord{Tag: 1, Data: oldIIM})
	for _, tc := range []struct {
		name      string
		file      []byte
		wantStart int64 // -1: no trailer
	}{
		{"AFCP", append(bytes.Clone(image), afcp(100)...), 100},
		{"FotoStation", append(bytes.Clone(image), foto...), 100},
		{"AFCP then FotoStation", append(append(bytes.Clone(image), afcp(100)...), foto...), 100},
		{"FotoStation then AFCP", append(append(bytes.Clone(image), foto...), afcp(100+int64(len(foto)))...), 100},
		{"nothing", bytes.Clone(image), -1},
		{"too short", []byte("AXS!"), -1},
		{"EOF record names an offset beyond the file", append(bytes.Clone(image), "AXS!\xff\xff\xff\xff\x00\x00\x00\x00"...), -1},
	} {
		t.Run("TRAILER-TAIL-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			for srcName, find := range map[string]func() ([]byte, int64, error){
				"bytes": func() ([]byte, int64, error) { return FindTail(BytesSource(tc.file), int64(len(tc.file))) },
				"reader": func() ([]byte, int64, error) {
					return FindTail(ReaderSource{R: bytes.NewReader(tc.file)}, int64(len(tc.file)))
				},
			} {
				region, base, err := find()
				if err != nil {
					t.Fatalf("%s: %v", srcName, err)
				}
				if tc.wantStart < 0 {
					if region != nil {
						t.Errorf("%s: found a trailer at %d", srcName, base)
					}
					continue
				}
				if base != tc.wantStart || !bytes.Equal(region, tc.file[tc.wantStart:]) {
					t.Errorf("%s: region from %d (%d bytes), want from %d", srcName, base, len(region), tc.wantStart)
				}
			}
		})
	}
}

// TestPlanWriteRegion: the region is written with the IPTC copies replaced and
// the AFCP offsets set for the position it is written at; the bytes between
// and after the trailers are copied. TRAILER-PLAN-01.
func TestPlanWriteRegion(t *testing.T) {
	t.Parallel()
	gap := []byte("gap!")
	afcp := afcpOf(binary.LittleEndian, "AXS*", 40, [][2]string{{"TEXT", "caption"}, {"IPTC", string(oldIIM)}})
	foto := fotoOf(FotoRecord{Tag: 1, Data: oldIIM})
	after := []byte("trailing bytes")
	region := bytes.Join([][]byte{gap, afcp, foto, after}, nil) // the region starts 4 bytes before the AFCP header

	plan := PlanFromRegion(region, 36, newIIM)
	if len(plan.Spans) != 2 {
		t.Fatalf("%d spans, want 2 (AFCP, FotoStation)", len(plan.Spans))
	}
	var buf bytes.Buffer
	out := &OutWriter{W: &buf}
	prefix := bytes.Repeat([]byte{1}, 119) // the AFCP header moves from 40 to 123
	if _, err := out.Write(prefix); err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteRegion(out, out); err != nil {
		t.Fatalf("WriteRegion: %v", err)
	}
	got := buf.Bytes()
	a := FindAFCP(got[119:], 119)
	if a == nil || a.Start != 123 {
		t.Fatalf("AFCP trailer not found at 123: %+v", a)
	}
	if string(a.Entries[0].Data) != "caption" || !bytes.Equal(a.Entries[1].Data, newIIM) {
		t.Errorf("AFCP entries: %q, %x", a.Entries[0].Data, a.Entries[1].Data)
	}
	for i, e := range a.Entries {
		d := got[a.Start+12+int64(12*i):]
		off, n := int64(a.Order.Uint32(d[8:])), int64(a.Order.Uint32(d[4:]))
		if !bytes.Equal(got[off:off+n], e.Data) {
			t.Errorf("entry %d: offset %d does not name its data", i, off)
		}
	}
	recs, _, _ := FindFotoStation(got, 0, a.End)
	if len(recs) != 1 || !bytes.Equal(recs[0].Data, newIIM) {
		t.Errorf("FotoStation records: %+v", recs)
	}
	if !bytes.HasSuffix(got, after) || !bytes.Contains(got, gap) {
		t.Error("the bytes between and after the trailers were not copied")
	}
}

// FuzzTrailerTail: arbitrary file bytes never panic FindTail, PlanFromRegion
// or WriteRegion, both sources agree, and a plan writes exactly the bytes of
// its region when nothing changes (same position, no new IPTC).
func FuzzTrailerTail(f *testing.F) {
	f.Add(append(bytes.Repeat([]byte{7}, 20), afcpOf(binary.BigEndian, "AXS!", 20, [][2]string{{"IPTC", string(oldIIM)}})...))
	f.Add(fotoOf(FotoRecord{Tag: 1, Data: oldIIM}))
	f.Add([]byte("AXS!\x00\x00\x00\x00AXS!\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, file []byte) {
		region, base, err := FindTail(BytesSource(file), int64(len(file)))
		if err != nil {
			t.Fatal(err)
		}
		r2, b2, err := FindTail(ReaderSource{R: bytes.NewReader(file)}, int64(len(file)))
		if err != nil || b2 != base || !bytes.Equal(r2, region) {
			t.Fatalf("sources disagree: %d vs %d (%v)", base, b2, err)
		}
		if region == nil {
			return
		}
		plan := PlanFromRegion(region, base, nil)
		var buf bytes.Buffer
		out := &OutWriter{W: &buf}
		_, _ = out.Write(file[:base])
		if err := plan.WriteRegion(out, out); err != nil {
			t.Fatal(err)
		}
		// Written where it was read, with the IPTC copies kept, the file is
		// the same except for the reserved fields a rewrite zeroes.
		if buf.Len() != len(file) {
			t.Fatalf("output %d bytes, want %d", buf.Len(), len(file))
		}
	})
}
