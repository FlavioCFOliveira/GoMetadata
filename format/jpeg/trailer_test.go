package jpeg

// trailer_test.go — regression tests for task #322: Inject rewrites the IPTC
// copies in AFCP and FotoStation trailers and the absolute offsets of the AFCP
// trailer (layouts: see trailer.go).

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// jpegWithTail builds SOI, SOS, entropy data, EOI and tail.
func jpegWithTail(tail []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0x01, 0x02, 0xFF, 0x00, 0x03, 0xFF, 0xD9})
	b.Write(tail)
	return b.Bytes()
}

// afcpOf builds an AFCP trailer that starts at absolute offset start.
func afcpOf(order trailerOrder, sig string, start int64, entries [][2]string) []byte {
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

// fotoOf builds FotoStation records.
func fotoOf(recs ...fotoRecord) []byte {
	var out []byte
	for _, r := range recs {
		out = append(out, r.data...)
		out = binary.BigEndian.AppendUint16(out, r.tag)
		out = binary.BigEndian.AppendUint32(out, uint32(len(r.data)+10)) //nolint:gosec // G115: test sizes are small
		out = append(out, fotoSig...)
	}
	return out
}

var (
	oldIIM = []byte{0x1C, 0x02, 0x00, 0x00, 0x02, 0x00, 0x04, 0x1C, 0x02, 0x78, 0x00, 0x03, 'o', 'l', 'd'}                                         //nolint:gochecknoglobals // immutable test fixture
	newIIM = []byte{0x1C, 0x02, 0x00, 0x00, 0x02, 0x00, 0x04, 0x1C, 0x02, 0x78, 0x00, 0x0B, 'n', 'e', 'w', ' ', 'c', 'a', 'p', 't', 'i', 'o', 'n'} //nolint:gochecknoglobals // immutable test fixture
)

// parseOut returns the AFCP trailer and FotoStation records of a JPEG.
func parseOut(t *testing.T, data []byte) (*afcpTrailer, []fotoRecord) {
	t.Helper()
	eoi := bytes.Index(data, []byte{0xFF, 0xD9})
	if eoi < 0 {
		t.Fatal("no EOI")
	}
	base := int64(eoi + 2)
	region := data[base:]
	afcp := findAFCP(region, base)
	from := base
	if afcp != nil {
		from = afcp.end
	}
	recs, _, _ := findFotoStation(region, base, from)
	return afcp, recs
}

func injectTail(t *testing.T, src []byte, rawEXIF, rawIPTC []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, rawEXIF, rawIPTC, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return out.Bytes()
}

func entryData(t *testing.T, a *afcpTrailer, tag string) []byte {
	t.Helper()
	for _, e := range a.entries {
		if string(e.tag[:]) == tag {
			return e.data
		}
	}
	t.Fatalf("no %s entry", tag)
	return nil
}

// checkAFCPConsistent verifies that every offset of the AFCP trailer of data
// is an absolute offset of the data it names, and that the EOF record points
// at the header (AFCP specification).
func checkAFCPConsistent(t *testing.T, data []byte, want [][2]string) {
	t.Helper()
	a, _ := parseOut(t, data)
	if a == nil {
		t.Fatal("no consistent AFCP trailer in the output")
	}
	if len(a.entries) != len(want) {
		t.Fatalf("%d entries, want %d", len(a.entries), len(want))
	}
	hdr := data[a.start : a.start+12]
	count := int(a.order.Uint16(hdr[6:]))
	for i := range count {
		d := data[a.start+12+int64(12*i):]
		n := int64(a.order.Uint32(d[4:]))
		off := int64(a.order.Uint32(d[8:]))
		if string(d[:4]) != want[i][0] || string(data[off:off+n]) != want[i][1] { //nolint:gosec // G602: d holds the 12-byte directory entry
			t.Errorf("entry %d: tag %q offset %d size %d does not name %q", i, d[:4], off, n, want[i][1]) //nolint:gosec // G602: see above
		}
	}
	if int64(a.order.Uint32(data[a.end-8:])) != a.start {
		t.Error("the EOF record does not point at the header")
	}
}

func TestTrailers_AFCPRewrite(t *testing.T) {
	t.Parallel()
	t.Run("IIM-COPY-01", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name  string
			order trailerOrder
			sig   string
		}{
			{"Motorola", binary.BigEndian, "AXS!"},
			{"Intel", binary.LittleEndian, "AXS*"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				entries := [][2]string{{"TEXT", "a caption"}, {"IPTC", string(oldIIM)}, {"Nail", "thumbnail-bytes"}}
				probe := jpegWithTail(nil)
				src := jpegWithTail(afcpOf(tc.order, tc.sig, int64(len(probe)), entries))
				// A different EXIF payload moves the trailer: offsets must follow, and the IPTC copy is the new IIM.
				out := injectTail(t, src, []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00"), newIIM)
				want := [][2]string{{"TEXT", "a caption"}, {"IPTC", string(newIIM)}, {"Nail", "thumbnail-bytes"}}
				checkAFCPConsistent(t, out, want)
				a, _ := parseOut(t, out)
				if a.start == int64(len(probe)) {
					t.Fatal("the trailer did not move: the test proves nothing")
				}
				if a.order != tc.order || string(a.sig[:]) != tc.sig {
					t.Errorf("byte order or signature changed: %q", a.sig)
				}
				if !bytes.HasSuffix(out, make([]byte, 4)) || a.end != int64(len(out)) {
					t.Error("the trailer does not end the file")
				}

				// No IPTC to write: the IPTC entry stays, the offsets still follow.
				out = injectTail(t, src, []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00"), nil)
				checkAFCPConsistent(t, out, [][2]string{{"TEXT", "a caption"}, {"IPTC", string(oldIIM)}, {"Nail", "thumbnail-bytes"}})
			})
		}
	})
}

// TestTrailers_UnchangedIsByteIdentical: a write that changes neither the
// position of the trailer nor the IPTC copies keeps the file byte for byte;
// a write that only moves the trailer rewrites just its offsets.
func TestTrailers_UnchangedIsByteIdentical(t *testing.T) {
	t.Parallel()
	t.Run("IIM-COPY-02", func(t *testing.T) {
		t.Parallel()
		probe := jpegWithTail(nil)
		tail := afcpOf(binary.BigEndian, "AXS!", int64(len(probe)), [][2]string{{"IPTC", string(oldIIM)}, {"TEXT", "t"}})
		// Reserved fields that a rewrite would zero: they prove the trailer was copied.
		tail[8], tail[len(tail)-1] = 0xAB, 0xCD
		src := jpegWithTail(tail)
		if out := injectTail(t, src, nil, nil); !bytes.Equal(out, src) {
			t.Error("a write that changes nothing altered the file")
		}
		// A JPEG without a trailer is unaffected, and ends with EOI.
		plain := jpegWithTail(nil)
		if out := injectTail(t, plain, nil, nil); !bytes.Equal(out, plain) {
			t.Error("plain JPEG changed")
		}
	})
}

// TestTrailers_FollowedByOtherData: bytes before the AFCP header and after its
// EOF record are copied through, and a FotoStation trailer after it is
// rewritten (the layout of ExifTool.jpg).
func TestTrailers_FollowedByOtherData(t *testing.T) {
	t.Parallel()
	t.Run("IIM-COPY-01", func(t *testing.T) {
		t.Parallel()
		probe := jpegWithTail(nil)
		gap := []byte("\x00\x00")
		start := int64(len(probe) + len(gap))
		afcp := afcpOf(binary.BigEndian, "AXS!", start, [][2]string{{"IPTC", string(oldIIM)}, {"%SCC", "\x02\x24\x02\x24"}})
		foto := fotoOf(fotoRecord{1, oldIIM}, fotoRecord{4, []byte("preview")})
		after := []byte("CANON OPTIONAL DATA tail bytes")
		src := jpegWithTail(bytes.Join([][]byte{gap, afcp, foto, after}, nil))
		out := injectTail(t, src, []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00"), newIIM)

		a, recs := parseOut(t, out)
		if a == nil {
			t.Fatal("no AFCP trailer in the output")
		}
		if got := entryData(t, a, "IPTC"); !bytes.Equal(got, newIIM) {
			t.Errorf("AFCP IPTC = %x, want the new IIM", got)
		}
		if got := entryData(t, a, "%SCC"); string(got) != "\x02\x24\x02\x24" {
			t.Errorf("another AFCP entry changed: %x", got)
		}
		if len(recs) != 2 || recs[0].tag != 1 || !bytes.Equal(recs[0].data, newIIM) || recs[1].tag != 4 || string(recs[1].data) != "preview" {
			t.Errorf("FotoStation records after the write: %+v", recs)
		}
		if !bytes.HasSuffix(out, after) {
			t.Error("the data after the trailers was not copied through")
		}
		if !bytes.Contains(out, gap) {
			t.Error("the bytes between EOI and the AFCP header were lost")
		}
		checkAFCPConsistent(t, out, [][2]string{{"IPTC", string(newIIM)}, {"%SCC", "\x02\x24\x02\x24"}})
	})
}

// TestTrailers_FotoStationOnly: a FotoStation trailer right after EOI.
func TestTrailers_FotoStationOnly(t *testing.T) {
	t.Parallel()
	src := jpegWithTail(fotoOf(fotoRecord{3, []byte("thumb")}, fotoRecord{1, oldIIM}))
	out := injectTail(t, src, nil, newIIM)
	_, recs := parseOut(t, out)
	if len(recs) != 2 || string(recs[0].data) != "thumb" || !bytes.Equal(recs[1].data, newIIM) {
		t.Errorf("records: %+v", recs)
	}
}

// TestTrailers_Malformed: trailers that are not consistent are copied
// through unchanged and never cause an error.
func TestTrailers_Malformed(t *testing.T) {
	t.Parallel()
	probe := jpegWithTail(nil)
	good := afcpOf(binary.BigEndian, "AXS!", int64(len(probe)), [][2]string{{"IPTC", string(oldIIM)}})
	wrongStart := bytes.Clone(good)
	binary.BigEndian.PutUint32(wrongStart[len(wrongStart)-8:], 7) // the EOF record points elsewhere
	badOffset := bytes.Clone(good)
	binary.BigEndian.PutUint32(badOffset[12+8:], 1<<30) // an entry beyond the file
	for name, tail := range map[string][]byte{
		"EOF record points elsewhere": wrongStart,
		"entry offset out of range":   badOffset,
		"truncated":                   good[:len(good)-5],
		"signature only":              []byte("AXS!"),
		"garbage":                     []byte("not a trailer at all"),
	} {
		src := jpegWithTail(tail)
		out := injectTail(t, src, nil, newIIM)
		if !bytes.HasSuffix(out, tail) {
			t.Errorf("%s: the trailer was modified", name)
		}
	}
}

// TestTrailers_Corpus runs the reproduction of task #322 on the corpus files.
func TestTrailers_Corpus(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"AFCP.jpg", "ExifTool.jpg"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("..", "..", "testdata", "corpus", "jpeg", "exiftool", name)
			src, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
			}
			if err != nil {
				t.Fatal(err)
			}
			inA, _ := parseOut(t, src)
			if inA == nil {
				t.Fatal("the corpus file has no AFCP trailer that this package recognises")
			}
			out := injectTail(t, src, nil, newIIM)
			a, recs := parseOut(t, out)
			if a == nil {
				t.Fatal("no AFCP trailer in the output")
			}
			if got := entryData(t, a, "IPTC"); !bytes.Equal(got, newIIM) {
				t.Errorf("AFCP IPTC copy = %x, want the new IIM", got)
			}
			for i, e := range inA.entries {
				if string(e.tag[:]) != "IPTC" && !bytes.Equal(e.data, a.entries[i].data) {
					t.Errorf("entry %q changed", e.tag)
				}
			}
			checkAFCPConsistent(t, out, func() [][2]string {
				var w [][2]string
				for _, e := range a.entries {
					w = append(w, [2]string{string(e.tag[:]), string(e.data)})
				}
				return w
			}())
			for _, r := range recs {
				if r.tag == 1 && !bytes.Equal(r.data, newIIM) {
					t.Errorf("FotoStation IPTC copy = %x", r.data)
				}
			}
		})
	}
}

// FuzzTrailers: arbitrary bytes after the EOI of a JPEG never panic Inject,
// and whatever AFCP trailer the output holds is consistent.
func FuzzTrailers(f *testing.F) {
	probe := jpegWithTail(nil)
	f.Add(afcpOf(binary.BigEndian, "AXS!", int64(len(probe)), [][2]string{{"IPTC", string(oldIIM)}, {"TEXT", "x"}}))
	f.Add(afcpOf(binary.LittleEndian, "AXS*", int64(len(probe)), [][2]string{{"IPTC", string(oldIIM)}}))
	f.Add(fotoOf(fotoRecord{1, oldIIM}))
	f.Add([]byte("AXS!\x00\x01\x00\xff\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, tail []byte) {
		src := jpegWithTail(tail)
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(src), &out, []byte("II*\x00"), newIIM, nil, true); err != nil {
			return
		}
		data := out.Bytes()
		eoi := bytes.Index(data, []byte{0xFF, 0xD9})
		if eoi < 0 {
			t.Fatal("output lost its EOI")
		}
		base := int64(eoi + 2)
		if a := findAFCP(data[base:], base); a != nil {
			if int64(a.order.Uint32(data[a.end-8:])) != a.start {
				t.Fatal("inconsistent AFCP trailer in the output")
			}
		}
	})
}
