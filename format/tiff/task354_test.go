package tiff

// task354_test.go — task #354: an AFCP (or FotoStation) trailer at the end of
// a TIFF keeps an IPTC copy and absolute offsets. A TIFF write moves the image
// data, so the trailer is written after the last block with its IPTC copy
// replaced by the IIM written to tag 0x83BB and its offsets set for the new
// position. Layouts: see internal/trailer.

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/trailer"
)

var (
	t354OldIIM = []byte{0x1C, 0x02, 0x00, 0x00, 0x02, 0x00, 0x04, 0x1C, 0x02, 0x78, 0x00, 0x03, 'o', 'l', 'd'}                                         //nolint:gochecknoglobals // immutable test fixture
	t354NewIIM = []byte{0x1C, 0x02, 0x00, 0x00, 0x02, 0x00, 0x04, 0x1C, 0x02, 0x78, 0x00, 0x0B, 'n', 'e', 'w', ' ', 'c', 'a', 'p', 't', 'i', 'o', 'n'} //nolint:gochecknoglobals // immutable test fixture
)

// t354AFCP builds an AFCP trailer that starts at absolute offset start.
func t354AFCP(order trailer.Order, sig string, start int64, entries [][2]string) []byte {
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

// t354Foto builds FotoStation records (tag, data).
func t354Foto(recs ...trailer.FotoRecord) []byte {
	var out []byte
	for _, r := range recs {
		out = append(out, r.Data...)
		out = binary.BigEndian.AppendUint16(out, r.Tag)
		out = binary.BigEndian.AppendUint32(out, uint32(len(r.Data)+10)) //nolint:gosec // G115: test sizes are small
		out = append(out, trailer.FotoSig()...)
	}
	return out
}

// t354Source assembles a little-endian TIFF whose IFD0 has an IPTC tag holding
// t354OldIIM and a strip, returns it, and the offset where the strip ends.
func t354Source(t *testing.T) []byte {
	t.Helper()
	ifd0 := &tNode{strip: []byte("PIXELS-0123456789"), entries: []tEntry{
		{tag: uint16(exif.TagIPTC), typ: uint16(exif.TypeUndefined), count: uint32(len(t354OldIIM)), val: t354OldIIM}, //nolint:gosec // G115: test sizes are small
	}}
	return (&tAsm{order: binary.LittleEndian}).bytes(t, ifd0)
}

// t354Write writes newIPTC into src through the TIFF write path that
// gometadata.Write uses, with the source held whole (wholeFile) or only its
// metadata prefix (streamed from a reader).
func t354Write(t *testing.T, src []byte, wholeFile bool, newIPTC []byte) []byte {
	t.Helper()
	prefix := src
	if !wholeFile {
		prefix = src[:40] // IFD0 and its values; the strip is streamed from the reader
	}
	e, err := exif.Parse(src)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	var out bytes.Buffer
	if err := InjectWithEXIFStream(bytes.NewReader(src), prefix, wholeFile, e, newIPTC, nil, &out); err != nil {
		t.Fatalf("InjectWithEXIFStream: %v", err)
	}
	return out.Bytes()
}

// t354Tail returns the AFCP trailer and FotoStation records at the end of data.
func t354Tail(t *testing.T, data []byte) (*trailer.AFCP, []trailer.FotoRecord) {
	t.Helper()
	region, base, err := trailer.FindTail(trailer.BytesSource(data), int64(len(data)))
	if err != nil || region == nil {
		t.Fatalf("no trailer at the end of the output (err %v)", err)
	}
	afcp := trailer.FindAFCP(region, base)
	from := base
	if afcp != nil {
		from = afcp.End
	}
	recs, _, _ := trailer.FindFotoStation(region, base, from)
	if recs == nil && afcp != nil {
		recs, _, _ = trailer.FindFotoStation(region, base, base) // records before the AFCP trailer
	}
	return afcp, recs
}

// t354CheckAFCP verifies the AFCP trailer of data: every offset is an
// absolute offset of the data it names, the EOF record points at the header,
// and the trailer ends the file or is followed only by what follows it in the
// source.
func t354CheckAFCP(t *testing.T, data []byte, want [][2]string) *trailer.AFCP {
	t.Helper()
	a, _ := t354Tail(t, data)
	if a == nil {
		t.Fatal("no consistent AFCP trailer in the output")
	}
	if len(a.Entries) != len(want) {
		t.Fatalf("%d entries, want %d", len(a.Entries), len(want))
	}
	for i := range a.Entries {
		d := data[a.Start+12+int64(12*i):]
		n, off := int64(a.Order.Uint32(d[4:])), int64(a.Order.Uint32(d[8:]))
		if string(d[:4]) != want[i][0] || string(data[off:off+n]) != want[i][1] { //nolint:gosec // G602: d holds the 12-byte directory entry
			t.Errorf("entry %d: tag %q offset %d size %d does not name %q", i, d[:4], off, n, want[i][1]) //nolint:gosec // G602: see above
		}
	}
	if int64(a.Order.Uint32(data[a.End-8:])) != a.Start {
		t.Error("the EOF record does not point at the header")
	}
	return a
}

// TestTask354TIFFAFCPTrailerRewritten is the #354 acceptance test: a TIFF
// with an AFCP trailer, written through the whole-file and the streaming
// path, keeps the trailer valid and its IPTC copy equal to the IIM written to
// IFD0. TIFF-AFCP-01.
func TestTask354TIFFAFCPTrailerRewritten(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		order trailer.Order
		sig   string
	}{
		{"Motorola", binary.BigEndian, "AXS!"},
		{"Intel", binary.LittleEndian, "AXS*"},
	} {
		for _, whole := range []bool{true, false} {
			name := tc.name + "/streamed"
			if whole {
				name = tc.name + "/whole file"
			}
			t.Run("TIFF-AFCP-01/"+name, func(t *testing.T) {
				t.Parallel()
				plain := t354Source(t)
				entries := [][2]string{{"TEXT", "a caption"}, {"IPTC", string(t354OldIIM)}, {"Nail", "thumbnail-bytes"}}
				src := append(bytes.Clone(plain), t354AFCP(tc.order, tc.sig, int64(len(plain)), entries)...)

				out := t354Write(t, src, whole, t354NewIIM)

				want := [][2]string{{"TEXT", "a caption"}, {"IPTC", string(t354NewIIM)}, {"Nail", "thumbnail-bytes"}}
				a := t354CheckAFCP(t, out, want)
				if a.Order != tc.order || string(a.Sig[:]) != tc.sig {
					t.Errorf("byte order or signature changed: %q", a.Sig)
				}
				if a.End != int64(len(out)) {
					t.Error("the trailer does not end the file")
				}
				// The same write of the source without the trailer is the
				// output without it: the TIFF data is unaffected.
				base := t354Write(t, plain, whole, t354NewIIM)
				if !bytes.Equal(out[:a.Start], base) {
					t.Error("the TIFF data differs from a write of the file without the trailer")
				}
				// The AFCP IPTC copy is the IIM of IFD0.
				e, err := exif.Parse(out[:a.Start])
				if err != nil {
					t.Fatalf("exif.Parse(output): %v", err)
				}
				if ent := e.IFD0.Get(exif.TagIPTC); ent == nil || !bytes.HasPrefix(ent.Value, t354NewIIM) {
					t.Errorf("IFD0 IPTC tag = %v, want the new IIM", ent)
				}
			})
		}
	}
}

// TestTask354TIFFFotoStationTrailer: FotoStation records before and after an
// AFCP trailer have their IPTC copy replaced, and the AFCP offsets follow.
// TIFF-AFCP-02.
func TestTask354TIFFFotoStationTrailer(t *testing.T) {
	t.Parallel()
	plain := t354Source(t)
	foto := t354Foto(trailer.FotoRecord{Tag: 3, Data: []byte("thumb")}, trailer.FotoRecord{Tag: 1, Data: t354OldIIM})
	for _, tc := range []struct {
		name string
		tail func(base int64) []byte
		afcp bool
	}{
		{"FotoStation only", func(int64) []byte { return foto }, false},
		{"AFCP then FotoStation", func(base int64) []byte {
			return append(t354AFCP(binary.BigEndian, "AXS!", base, [][2]string{{"IPTC", string(t354OldIIM)}}), foto...)
		}, true},
		{"FotoStation then AFCP", func(base int64) []byte {
			return append(bytes.Clone(foto), t354AFCP(binary.BigEndian, "AXS!", base+int64(len(foto)), [][2]string{{"IPTC", string(t354OldIIM)}})...)
		}, true},
	} {
		t.Run("TIFF-AFCP-02/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := append(bytes.Clone(plain), tc.tail(int64(len(plain)))...)
			out := t354Write(t, src, true, t354NewIIM)
			a, recs := t354Tail(t, out)
			if len(recs) != 2 || recs[0].Tag != 3 || string(recs[0].Data) != "thumb" || recs[1].Tag != 1 || !bytes.Equal(recs[1].Data, t354NewIIM) {
				t.Errorf("FotoStation records after the write: %+v", recs)
			}
			if tc.afcp {
				if a == nil {
					t.Fatal("AFCP trailer lost")
				}
				t354CheckAFCP(t, out, [][2]string{{"IPTC", string(t354NewIIM)}})
			}
		})
	}
}

// TestTask354TIFFTrailerEdgeCases: no IPTC to write keeps the IPTC copy but
// still moves the offsets; image data that merely looks like a trailer is
// image data; a file without a trailer is written as before.
// TIFF-AFCP-03.
func TestTask354TIFFTrailerEdgeCases(t *testing.T) {
	t.Parallel()
	plain := t354Source(t)
	entries := [][2]string{{"IPTC", string(t354OldIIM)}, {"TEXT", "t"}}

	t.Run("TIFF-AFCP-03/nil IPTC keeps the copy", func(t *testing.T) {
		t.Parallel()
		src := append(bytes.Clone(plain), t354AFCP(binary.BigEndian, "AXS!", int64(len(plain)), entries)...)
		out := t354Write(t, src, true, nil)
		t354CheckAFCP(t, out, [][2]string{{"IPTC", string(t354OldIIM)}, {"TEXT", "t"}})
	})

	t.Run("TIFF-AFCP-03/look-alike inside image data", func(t *testing.T) {
		t.Parallel()
		// The strip itself ends with a complete, consistent AFCP trailer.
		stripEnd := int64(len(plain))
		lookalike := t354AFCP(binary.BigEndian, "AXS!", stripEnd, entries)
		ifd0 := &tNode{strip: append([]byte("PIXELS-0123456789"), lookalike...), entries: []tEntry{
			{tag: uint16(exif.TagIPTC), typ: uint16(exif.TypeUndefined), count: uint32(len(t354OldIIM)), val: t354OldIIM}, //nolint:gosec // G115: test sizes are small
		}}
		src := (&tAsm{order: binary.LittleEndian}).bytes(t, ifd0)
		out := t354Write(t, src, true, t354NewIIM)
		if !bytes.Contains(out, lookalike) {
			t.Error("image data that looks like a trailer was rewritten")
		}
	})

	t.Run("TIFF-AFCP-03/no trailer", func(t *testing.T) {
		t.Parallel()
		out := t354Write(t, plain, true, t354NewIIM)
		if region, _, _ := trailer.FindTail(trailer.BytesSource(out), int64(len(out))); region != nil {
			t.Error("a trailer appeared in a file that had none")
		}
	})

	t.Run("TIFF-AFCP-03/malformed trailer is not invented", func(t *testing.T) {
		t.Parallel()
		good := t354AFCP(binary.BigEndian, "AXS!", int64(len(plain)), entries)
		binary.BigEndian.PutUint32(good[len(good)-8:], 3) // the EOF record points elsewhere
		src := append(bytes.Clone(plain), good...)
		out := t354Write(t, src, true, t354NewIIM)
		base := t354Write(t, plain, true, t354NewIIM)
		if !bytes.Equal(out, base) {
			t.Error("an inconsistent trailer changed the output")
		}
	})
}
