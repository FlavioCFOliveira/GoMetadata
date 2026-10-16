package tiff

// task360_test.go — task #360: a TIFF-family write carries the bytes after
// the data it moves (unknown trailers, trailers that follow an AFCP
// trailer), and does not carry the stale structures it regenerates.
// TIFF-TAIL-01 to TIFF-TAIL-04 (docs/conformance/containers.md).

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/trailer"
)

// unknownTrailer is data that no IFD addresses and no trailer parser knows.
var unknownTrailer = []byte("PRIVATE-TRAILER\x00\x01\x02\x03 tail of some other writer") //nolint:gochecknoglobals // immutable test fixture

// TestTask360UnknownTrailerIsCarried: TIFF-TAIL-01. A trailer that nothing
// addresses, appended after the image data, is in the output byte for byte
// after the written data, through the whole-file and the streaming path.
func TestTask360UnknownTrailerIsCarried(t *testing.T) {
	t.Parallel()
	plain := t354Source(t)
	for _, whole := range []bool{true, false} {
		name := "whole file"
		if !whole {
			name = "streamed"
		}
		t.Run("TIFF-TAIL-01/"+name, func(t *testing.T) {
			t.Parallel()
			src := append(bytes.Clone(plain), unknownTrailer...)
			out := t354Write(t, src, whole, t354NewIIM)
			base := t354Write(t, plain, whole, t354NewIIM)
			if !bytes.Equal(out, append(bytes.Clone(base), unknownTrailer...)) {
				t.Errorf("output is %d bytes, want the %d written bytes followed by the %d trailer bytes", len(out), len(base), len(unknownTrailer))
			}
		})
	}
}

// TestTask360AFCPFollowedByAnotherTrailer: TIFF-TAIL-02. An AFCP trailer that
// another trailer follows is still found and rewritten (its IPTC copy is the new
// IIM, its offsets are those of its new position), and the bytes after it are
// carried unchanged.
func TestTask360AFCPFollowedByAnotherTrailer(t *testing.T) {
	t.Parallel()
	plain := t354Source(t)
	entries := [][2]string{{"IPTC", string(t354OldIIM)}, {"TEXT", "t"}}
	for _, whole := range []bool{true, false} {
		name := "whole file"
		if !whole {
			name = "streamed"
		}
		t.Run("TIFF-TAIL-02/"+name, func(t *testing.T) {
			t.Parallel()
			afcp := t354AFCP(binary.BigEndian, "AXS!", int64(len(plain)), entries)
			src := append(append(bytes.Clone(plain), afcp...), unknownTrailer...)
			out := t354Write(t, src, whole, t354NewIIM)
			base := t354Write(t, plain, whole, t354NewIIM)
			if !bytes.HasSuffix(out, unknownTrailer) {
				t.Fatal("the trailer after the AFCP trailer was not carried")
			}
			region := out[len(base):]
			a := trailer.FindAFCP(region, int64(len(base)))
			if a == nil {
				t.Fatal("the AFCP trailer was lost")
			}
			got := map[string]string{}
			for _, e := range a.Entries {
				got[string(e.Tag[:])] = string(e.Data)
			}
			if got["IPTC"] != string(t354NewIIM) || got["TEXT"] != "t" {
				t.Errorf("AFCP entries = %v, want the new IIM and TEXT unchanged", got)
			}
			// Its offsets are those of the new position: each names its data.
			for i := range a.Entries {
				d := out[a.Start+12+int64(12*i):]
				n, off := int64(a.Order.Uint32(d[4:])), int64(a.Order.Uint32(d[8:]))
				if !bytes.Equal(out[off:off+n], a.Entries[i].Data) {
					t.Errorf("entry %d offset %d does not name its data", i, off)
				}
			}
			if !bytes.Equal(out[a.End:], unknownTrailer) {
				t.Errorf("%d bytes after the AFCP trailer, want the %d of the other trailer", len(out)-int(a.End), len(unknownTrailer))
			}
		})
	}
}

// TestTask360StaleStructuresAreNotCarried: TIFF-TAIL-03. When the IFD of the
// source follows the image data (libtiff writes it there), the bytes of that
// IFD and of its values are what the write regenerates: only what follows
// them is carried.
func TestTask360StaleStructuresAreNotCarried(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	strip := []byte("PIXELS-0123456789")
	// header | strip | IFD0 (strip, IPTC) | IPTC value | trailer
	ifd0 := 8 + len(strip)
	if ifd0%2 == 1 {
		ifd0++
	}
	buf := make([]byte, ifd0+2+3*12+4)
	copy(buf, "II*\x00")
	le.PutUint32(buf[4:], uint32(ifd0)) //nolint:gosec // G115: tiny
	copy(buf[8:], strip)
	le.PutUint16(buf[ifd0:], 3)
	ent := func(i int, tag, typ uint16, count, val uint32) {
		p := ifd0 + 2 + 12*i
		le.PutUint16(buf[p:], tag)
		le.PutUint16(buf[p+2:], typ)
		le.PutUint32(buf[p+4:], count)
		le.PutUint32(buf[p+8:], val)
	}
	ent(0, uint16(exif.TagStripOffsets), 4, 1, 8)
	ent(1, uint16(exif.TagStripByteCounts), 4, 1, uint32(len(strip)))                                   //nolint:gosec // G115: tiny
	ent(2, uint16(exif.TagIPTC), uint16(exif.TypeUndefined), uint32(len(t354OldIIM)), uint32(len(buf))) //nolint:gosec // G115: tiny
	src := bytes.Join([][]byte{buf, t354OldIIM, unknownTrailer}, nil)
	for _, whole := range []bool{true, false} {
		prefix := src
		if !whole {
			prefix = src[:ifd0+2+3*12+4+len(t354OldIIM)] // the structures; the strip and the trailer stream
		}
		e, err := exif.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := InjectWithEXIFStream(bytes.NewReader(src), prefix, whole, e, t354NewIIM, nil, &out); err != nil {
			t.Fatalf("InjectWithEXIFStream: %v", err)
		}
		got := out.Bytes()
		if !bytes.HasSuffix(got, unknownTrailer) {
			t.Errorf("whole=%v: the trailer after the source IFD was not carried", whole)
		}
		if bytes.Contains(got, t354OldIIM) {
			t.Errorf("whole=%v: the stale IIM of the source IFD was carried", whole)
		}
		if !bytes.Contains(got, strip) || !bytes.Contains(got, t354NewIIM) {
			t.Errorf("whole=%v: the strip or the new IIM is missing", whole)
		}
	}
}

// TestTask360NoTailIsNoChange: TIFF-TAIL-04. A file that ends with its image
// data (or with its structures) is written exactly as before: nothing is
// appended.
func TestTask360NoTailIsNoChange(t *testing.T) {
	t.Parallel()
	plain := t354Source(t)
	out := t354Write(t, plain, true, t354NewIIM)
	// The same bytes without trailing material, through the streaming path.
	if streamed := t354Write(t, plain, false, t354NewIIM); !bytes.Equal(out, streamed) {
		t.Error("the whole-file and the streamed writes differ")
	}
	if !bytes.HasSuffix(out, []byte("PIXELS-0123456789")) {
		t.Error("the output does not end with the image data of the source")
	}
}

// TestTask360TrailerAfterRealTIFFs: TIFF-TAIL-01 on the committed fixtures, a
// classic TIFF and both BigTIFF byte orders: a trailer appended after the file
// is the last thing in the output, whatever the container, and the output
// without the trailer is the output of the file without it.
func TestTask360TrailerAfterRealTIFFs(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"testdata/cramps.tif", "testdata/big_cramps_le.tif", "testdata/big_cramps_be.tif"} {
		t.Run("TIFF-TAIL-01/"+name, func(t *testing.T) {
			t.Parallel()
			plain, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			e, err := exif.Parse(plain)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			var base bytes.Buffer
			if err := InjectWithEXIFStream(bytes.NewReader(plain), plain, true, e, t354NewIIM, nil, &base); err != nil {
				t.Fatalf("write without trailer: %v", err)
			}
			src := append(bytes.Clone(plain), unknownTrailer...)
			e2, err := exif.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := InjectWithEXIFStream(bytes.NewReader(src), src, true, e2, t354NewIIM, nil, &out); err != nil {
				t.Fatalf("write with trailer: %v", err)
			}
			if !bytes.Equal(out.Bytes(), append(bytes.Clone(base.Bytes()), unknownTrailer...)) {
				t.Errorf("output is %d bytes, want the %d written bytes and the %d trailer bytes", out.Len(), base.Len(), len(unknownTrailer))
			}
		})
	}
}
