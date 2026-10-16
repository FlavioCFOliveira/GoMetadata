package jpeg

// externaloffsets_test.go — task #362: an Olympus MakerNote offset to the
// preview image after the end of the JPEG follows the preview when a write
// moves the MakerNote or the data.

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// olympusEXIF builds the EXIF stream of an Olympus E-series image: MakerNote
// "OLYMPUS\0II\3\0" whose CameraSettings sub-IFD (tag 0x2020) holds
// PreviewImageStart (0x0101, counted from the start of the MakerNote) and
// PreviewImageLength (0x0102). pad moves the MakerNote.
func olympusEXIF(start, length uint32, pad int) []byte {
	le := binary.LittleEndian
	u32 := func(v uint32) []byte { return le.AppendUint32(nil, v) }
	tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
		Make: "OLYMPUS IMAGING CORP.", Order: le, Prefix: []byte("OLYMPUS\x00II\x03\x00"), PadBefore: pad,
		Entries: []testutil.MNEntry{
			{Tag: 0x0200, Type: 4, Count: 1, Value: u32(7)},
			{Tag: 0x2020, Sub: &testutil.MNSub{Entries: []testutil.MNEntry{
				{Tag: 0x0100, Type: 4, Count: 1, Value: u32(1)},
				{Tag: 0x0101, Type: 4, Count: 1, Value: u32(start)},
				{Tag: 0x0102, Type: 4, Count: 1, Value: u32(length)},
			}}},
		},
	})
	return tiff
}

// previewJPEG is the preview image: a JPEG stream of its own.
var previewJPEG = append(append([]byte{0xFF, 0xD8, 0xFF, 0xDB, 0x00, 0x03, 0x00}, bytes.Repeat([]byte("preview-data-"), 20)...), 0xFF, 0xD9) //nolint:gochecknoglobals // immutable test fixture

// previewLen is the length of the preview image.
var previewLen = uint32(len(previewJPEG)) //nolint:gochecknoglobals // immutable test fixture

// olympusJPEG builds SOI, APP1 (EXIF), SOS, entropy data, EOI, then between and
// the tail mkTail makes (which holds the preview); the MakerNote offset is set
// to address the preview. mkTail receives the position of the tail in the file.
func olympusJPEG(t *testing.T, between []byte, mkTail func(tailAt int64) []byte) []byte {
	t.Helper()
	build := func(previewAt int64) []byte {
		// The MakerNote of the source sits at pad 6; its value counts from it.
		probe := olympusEXIF(0, previewLen, 6)
		mnFile := int64(12) + mnOffsetOf(t, probe)
		exifStream := olympusEXIF(uint32(previewAt-mnFile), previewLen, 6) //nolint:gosec // G115: small test offsets
		var b bytes.Buffer
		b.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
		b.Write(binary.BigEndian.AppendUint16(nil, uint16(2+6+len(exifStream)))) //nolint:gosec // G115: small test sizes
		b.WriteString("Exif\x00\x00")
		b.Write(exifStream)
		b.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0x01, 0x02, 0xFF, 0x00, 0x03, 0xFF, 0xD9})
		b.Write(between)
		b.Write(mkTail(int64(b.Len())))
		return b.Bytes()
	}
	// The length of the file does not depend on the offset: build once to find
	// where the preview lands, and again with the offset that addresses it.
	i := bytes.Index(build(0), previewJPEG)
	if i < 0 {
		t.Fatal("fixture: no preview")
	}
	file := build(int64(i))
	if j := bytes.Index(file, previewJPEG); j != i {
		t.Fatalf("fixture: the preview moved from %d to %d", i, j)
	}
	return file
}

// mnOffsetOf returns the offset of the MakerNote in the TIFF stream.
func mnOffsetOf(t *testing.T, tiff []byte) int64 {
	t.Helper()
	e, err := exif.Parse(tiff)
	if err != nil || e.MakerNoteOffset == 0 {
		t.Fatalf("fixture: Parse = %v, MakerNoteOffset %d", err, e.MakerNoteOffset)
	}
	return int64(e.MakerNoteOffset)
}

// previewAfter returns the preview image that the MakerNote offset of the EXIF
// of file addresses, or nil.
func previewAfter(t *testing.T, file []byte) []byte {
	t.Helper()
	raw, _, _, err := Extract(bytes.NewReader(file))
	if err != nil || raw == nil {
		t.Fatalf("Extract: %v", err)
	}
	offs := exif.ExternalOffsets(raw)
	if len(offs) != 1 {
		t.Fatalf("ExternalOffsets = %v, want one offset", offs)
	}
	i := bytes.Index(file, []byte("Exif\x00\x00"))
	at := int64(i) + 6 + int64(offs[0].Base) + int64(offs[0].Value)
	end := at + int64(offs[0].Length)
	if at < 0 || end > int64(len(file)) {
		return nil
	}
	return file[at:end]
}

// TestExternalOffsets_O1_PreviewFollowsTheWrite: the offset addresses the
// preview after a write that grows or shrinks what precedes the image data, and
// after one that does not.
func TestExternalOffsets_O1_PreviewFollowsTheWrite(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		between []byte
		mkTail  func(at int64) []byte
		newEXIF func(src []byte) []byte
		iptc    []byte
	}{
		{"MakerNote moves forward", nil,
			func(int64) []byte { return previewJPEG },
			func(src []byte) []byte {
				return olympusEXIF(binary.LittleEndian.Uint32(src), previewLen, 40)
			}, nil},
		{"MakerNote moves back", nil,
			func(int64) []byte { return previewJPEG },
			func(src []byte) []byte {
				return olympusEXIF(binary.LittleEndian.Uint32(src), previewLen, 0)
			}, nil},
		{"IPTC added before the image data", nil,
			func(int64) []byte { return previewJPEG },
			func(src []byte) []byte {
				return olympusEXIF(binary.LittleEndian.Uint32(src), previewLen, 6)
			}, newIIM},
		{"bytes between the image and the preview", []byte("unknown-trailer-bytes"),
			func(int64) []byte { return previewJPEG },
			func(src []byte) []byte {
				return olympusEXIF(binary.LittleEndian.Uint32(src), previewLen, 40)
			}, newIIM},
	} {
		t.Run("R-27/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := olympusJPEG(t, tc.between, tc.mkTail)
			if got := previewAfter(t, src); !bytes.Equal(got, previewJPEG) {
				t.Fatalf("fixture: the source offset addresses %q, not the preview", got)
			}
			rawSrc, _, _, _ := Extract(bytes.NewReader(src))
			offs := exif.ExternalOffsets(rawSrc)
			stream := tc.newEXIF(binary.LittleEndian.AppendUint32(nil, offs[0].Value))
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, stream, tc.iptc, nil, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			if len(out.Bytes()) == len(src) && bytes.Equal(out.Bytes(), src) {
				t.Fatal("the write changed nothing: the test proves nothing")
			}
			if got := previewAfter(t, out.Bytes()); !bytes.Equal(got, previewJPEG) {
				t.Errorf("after the write the offset addresses %q, want the preview", got)
			}
			if !bytes.HasSuffix(out.Bytes(), previewJPEG) {
				t.Error("the preview is not at the end of the output")
			}
		})
	}
}

// TestExternalOffsets_O2_UnchangedIsByteIdentical: a write that moves nothing
// leaves the offset, and the file, as they were.
func TestExternalOffsets_O2_UnchangedIsByteIdentical(t *testing.T) {
	t.Parallel()
	t.Run("R-27", func(t *testing.T) {
		t.Parallel()
		src := olympusJPEG(t, nil, func(int64) []byte { return previewJPEG })
		raw, _, _, err := Extract(bytes.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(src), &out, raw, nil, nil, true); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), src) {
			t.Error("an identical rewrite changed the file")
		}
	})
}

// TestExternalOffsets_O3_TrailerBeforeThePreview: the preview follows an AFCP
// trailer whose IPTC copy a write changes (the trailer grows), so the offset
// counts the growth. The preview inside the trailer is left alone.
func TestExternalOffsets_O3_TrailerBeforeThePreview(t *testing.T) {
	t.Parallel()
	t.Run("R-27", func(t *testing.T) {
		t.Parallel()
		afcp := func(at int64) []byte {
			return afcpOf(binary.BigEndian, "AXS!", at, [][2]string{{"IPTC", string(oldIIM)}})
		}
		// Trailer first, preview after it.
		src := olympusJPEG(t, nil, func(at int64) []byte { return append(afcp(at), previewJPEG...) })
		rawSrc, _, _, _ := Extract(bytes.NewReader(src))
		offs := exif.ExternalOffsets(rawSrc)
		stream := olympusEXIF(offs[0].Value, offs[0].Length, 40)
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(src), &out, stream, newIIM, nil, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		if got := previewAfter(t, out.Bytes()); !bytes.Equal(got, previewJPEG) {
			t.Errorf("after the write the offset addresses %q, want the preview", got)
		}
	})
}

// TestExternalOffsets_O4_NotRewritten: an offset that does not address a JPEG
// stream after the first scan header, or whose EXIF is not a copy of the
// source's, is left as it is.
func TestExternalOffsets_O4_NotRewritten(t *testing.T) {
	t.Parallel()
	src := olympusJPEG(t, nil, func(int64) []byte { return previewJPEG })
	rawSrc, _, _, _ := Extract(bytes.NewReader(src))
	offs := exif.ExternalOffsets(rawSrc)
	for _, tc := range []struct {
		name  string
		start uint32 // new value of the offset
		pad   int
	}{
		{"value changed by the caller", offs[0].Value + 2, 40},
		{"addresses bytes that are not a JPEG", offs[0].Value + 3, 40},
	} {
		t.Run("R-27/"+tc.name, func(t *testing.T) {
			t.Parallel()
			stream := olympusEXIF(tc.start, offs[0].Length, tc.pad)
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, stream, nil, nil, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			raw, _, _, _ := Extract(bytes.NewReader(out.Bytes()))
			got := exif.ExternalOffsets(raw)
			if len(got) != 1 || got[0].Value != tc.start {
				t.Errorf("offset = %v, want it left at %d", got, tc.start)
			}
		})
	}
	t.Run("R-27/no EXIF in the source", func(t *testing.T) {
		t.Parallel()
		plain := jpegWithTail(previewJPEG)
		stream := olympusEXIF(0x1000, previewLen, 6)
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(plain), &out, stream, nil, nil, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		raw, _, _, _ := Extract(bytes.NewReader(out.Bytes()))
		if got := exif.ExternalOffsets(raw); len(got) != 1 || got[0].Value != 0x1000 {
			t.Errorf("offset = %v, want it left at 4096", got)
		}
	})
}

// TestExternalOffsets_O5_Corpus: the five Olympus bodies of the corpus whose
// MakerNote offset addresses the preview after the image: after a write that
// replaces the EXIF stream with a larger copy the offset still addresses the
// preview, byte for byte.
func TestExternalOffsets_O5_Corpus(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("..", "..", "testdata", "corpus", "jpeg", "metadata-extractor")
	for _, name := range []string{"Olympus E-M1.jpg", "Olympus E-M5.jpg", "Olympus TG-850.jpg", "Olympus SH-1.jpg", "Olympus SP-100EE.jpg"} {
		t.Run("R-27/"+name, func(t *testing.T) {
			t.Parallel()
			src, err := os.ReadFile(filepath.Join(dir, name))
			if os.IsNotExist(err) {
				t.Skipf("corpus file absent (run testdata/download.sh): %s", name)
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, _, _, err := Extract(bytes.NewReader(src))
			if err != nil || raw == nil {
				t.Fatalf("Extract: %v", err)
			}
			before := exif.ExternalOffsets(raw)
			if len(before) != 1 {
				t.Fatalf("ExternalOffsets = %v, want one", before)
			}
			want := previewBytes(t, src, before[0])
			if !bytes.HasPrefix(want, []byte{0xFF, 0xD8}) {
				t.Fatal("the source offset does not address a JPEG preview")
			}
			// A larger EXIF stream (the same bytes and 64 more at the end of the
			// stream, after everything) and an IPTC block: both move the data.
			bigger := append(bytes.Clone(raw), make([]byte, 64)...)
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, bigger, newIIM, nil, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			raw2, _, _, err := Extract(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			after := exif.ExternalOffsets(raw2)
			if len(after) != 1 {
				t.Fatalf("ExternalOffsets after the write = %v, want one", after)
			}
			if got := previewBytes(t, out.Bytes(), after[0]); !bytes.Equal(got, want) {
				t.Errorf("the offset addresses %d bytes that differ from the preview (%d bytes)", len(got), len(want))
			}
		})
	}
}

// previewBytes returns the bytes that the MakerNote offset o of the EXIF
// stream of file addresses.
func previewBytes(t *testing.T, file []byte, o exif.ExternalOffset) []byte {
	t.Helper()
	i := bytes.Index(file, []byte("Exif\x00\x00"))
	if i < 0 {
		t.Fatal("no EXIF")
	}
	at := int64(i) + 6 + int64(o.Base) + int64(o.Value)
	if at < 0 || at+int64(o.Length) > int64(len(file)) {
		return nil
	}
	return bytes.Clone(file[at : at+int64(o.Length)])
}

// FuzzInjectExternalOffsets: arbitrary JPEG bytes and arbitrary preview offsets
// never panic Inject, whose EXIF stream is an Olympus MakerNote with a preview
// offset (task #362); the output of a successful Inject still has its EXIF.
func FuzzInjectExternalOffsets(f *testing.F) {
	probe := olympusJPEG0Fuzz()
	f.Add(probe, uint32(0), previewLen)
	f.Add(probe, uint32(1<<20), previewLen)
	f.Add(jpegWithTail(previewJPEG), uint32(10), previewLen)
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xD9}, uint32(0xFFFFFFFF), uint32(0xFFFFFFFF))
	f.Fuzz(func(t *testing.T, data []byte, start, length uint32) {
		stream := olympusEXIF(start, length, 6)
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(data), &out, stream, newIIM, nil, true); err != nil {
			return
		}
		raw, _, _, err := Extract(bytes.NewReader(out.Bytes()))
		if err != nil || raw == nil {
			t.Fatalf("the output of a successful Inject has no EXIF: %v", err)
		}
		if bytes.Equal(raw, stream) {
			return
		}
		// Only the 4 bytes of an offset may differ from the stream passed in.
		if len(raw) != len(stream) {
			t.Fatalf("EXIF length %d, want %d", len(raw), len(stream))
		}
		diff := 0
		for i := range raw {
			if raw[i] != stream[i] {
				diff++
			}
		}
		if diff > 4 {
			t.Fatalf("%d bytes of the EXIF stream changed, want at most the 4 of one offset", diff)
		}
	})
}

// olympusJPEG0Fuzz builds a seed JPEG (an Olympus EXIF segment and a preview
// after the image) without a *testing.T.
func olympusJPEG0Fuzz() []byte {
	exifStream := olympusEXIF(0, previewLen, 6)
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	b.Write(binary.BigEndian.AppendUint16(nil, uint16(2+6+len(exifStream)))) //nolint:gosec // G115: small test sizes
	b.WriteString("Exif\x00\x00")
	b.Write(exifStream)
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0x01, 0x02, 0xFF, 0x00, 0x03, 0xFF, 0xD9})
	b.Write(previewJPEG)
	return b.Bytes()
}

// BenchmarkInjectOlympusPreview measures Inject of an EXIF with an Olympus
// preview offset: the measuring pass and the rewrite.
func BenchmarkInjectOlympusPreview(b *testing.B) {
	file := olympusJPEG0Fuzz()
	raw, _, _, err := Extract(bytes.NewReader(file))
	if err != nil {
		b.Fatal(err)
	}
	b.Run("Olympus", func(b *testing.B) {
		b.SetBytes(int64(len(file)))
		b.ReportAllocs()
		for range b.N {
			if err := Inject(bytes.NewReader(file), io.Discard, raw, newIIM, nil, true); err != nil {
				b.Fatal(err)
			}
		}
	})
}
