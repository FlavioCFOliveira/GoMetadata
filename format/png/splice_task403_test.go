package png

// splice_task403_test.go — tasks #400 and #403: Inject copies the source, puts a
// new eXIf or XMP chunk in the place of the old one of its kind (after IHDR
// when there is none), copies what it cannot follow, and adds nothing else.
// Rule IDs are the sub-test names; docs/conformance/containers.md lists them.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// pngChunk returns a chunk with a correct CRC.
func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	writeChunkTo(&b, typ, data)
	return b.Bytes()
}

var (
	spIHDR = pngChunk("IHDR", []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0})                                 //nolint:gochecknoglobals // immutable test fixture
	spIDAT = pngChunk("IDAT", []byte{0x78, 0x9c, 0x63, 0x60, 0x00, 0x00, 0x00, 0x02, 0x00, 0x01})            //nolint:gochecknoglobals // immutable test fixture
	spIEND = pngChunk("IEND", nil)                                                                           //nolint:gochecknoglobals // immutable test fixture
	spTEXt = pngChunk("tEXt", []byte("Comment\x00hello"))                                                    //nolint:gochecknoglobals // immutable test fixture
	spGAMA = pngChunk("gAMA", []byte{0, 1, 0x86, 0xA0})                                                      //nolint:gochecknoglobals // immutable test fixture
	spOldE = pngChunk("eXIf", []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'O', 'L'})               //nolint:gochecknoglobals // immutable test fixture
	spOldX = pngChunk("iTXt", buildXMPChunk([]byte("<?xpacket begin='' id='x'?><old/><?xpacket end='r'?>"))) //nolint:gochecknoglobals // immutable test fixture
	spOtrX = pngChunk("iTXt", append([]byte("Other.keyword\x00\x00\x00\x00\x00"), "text"...))                //nolint:gochecknoglobals // immutable test fixture
	spNewE = []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'N', 'W'}                                 //nolint:gochecknoglobals // immutable test fixture
	spNewX = []byte("<?xpacket begin='' id='x'?><new/><?xpacket end='r'?>")                                  //nolint:gochecknoglobals // immutable test fixture
)

// spPNG joins the signature and the chunks.
func spPNG(chunks ...[]byte) []byte {
	b := append([]byte(nil), pngSig[:]...)
	for _, c := range chunks {
		b = append(b, c...)
	}
	return b
}

// spTypes returns the chunk types of b in order, "eXIf" and "XMP" for the two
// kinds the tests look at; it stops at the first chunk that cannot be read.
func spTypes(t *testing.T, b []byte) []string {
	t.Helper()
	var out []string
	pos := 8
	for pos+12 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[pos:]))
		typ := string(b[pos+4 : pos+8])
		if pos+12+n > len(b) {
			break
		}
		if typ == "iTXt" && isXMPChunk(b[pos+8:pos+8+n]) {
			typ = "XMP"
		}
		out = append(out, typ)
		pos += 12 + n
	}
	return out
}

// spStripPNG returns b without its eXIf and text chunks (tEXt, zTXt, iTXt): the
// chunks Inject may drop or replace. It stops at the first chunk that cannot be
// read and keeps the rest. It is written from the PNG chunk layout alone
// (W3C PNG 3rd ed. §5.3), independent of Inject.
func spStripPNG(b []byte) []byte {
	if len(b) < 8 {
		return b
	}
	out := append([]byte(nil), b[:8]...)
	pos := 8
	for pos+12 <= len(b) {
		n := uint64(binary.BigEndian.Uint32(b[pos:]))
		if uint64(pos)+12+n > uint64(len(b)) {
			break
		}
		end := pos + 12 + int(n) //nolint:gosec // G115: n <= len(b) was checked above
		switch string(b[pos+4 : pos+8]) {
		case "eXIf", "tEXt", "zTXt", "iTXt":
		default:
			out = append(out, b[pos:end]...)
		}
		pos = end
	}
	return append(out, b[pos:]...)
}

func spInjectPNG(t *testing.T, src, exifData, xmp []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, exifData, nil, xmp, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return out.Bytes()
}

func eq(a ...string) []string { return a }

// TestInjectPlacement covers PNG-PLACE: a new chunk takes the place of the old
// chunk of its kind; a kind the file has none of goes right after IHDR.
func TestInjectPlacement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		src       []byte
		exif, xmp []byte
		want      []string
		keep      Keep
	}{
		{"PNG-PLACE-01 none old: EXIF then XMP after IHDR", spPNG(spIHDR, spGAMA, spIDAT, spIEND), spNewE, spNewX,
			eq("IHDR", "eXIf", "XMP", "gAMA", "IDAT", "IEND"), 0},
		{"PNG-PLACE-02 old eXIf is replaced where it is", spPNG(spIHDR, spGAMA, spOldE, spIDAT, spIEND), spNewE, nil,
			eq("IHDR", "gAMA", "eXIf", "IDAT", "IEND"), 0},
		{"PNG-PLACE-03 old XMP after IDAT is replaced where it is", spPNG(spIHDR, spIDAT, spOldX, spIEND), nil, spNewX,
			eq("IHDR", "IDAT", "XMP", "IEND"), 0},
		{"PNG-PLACE-04 a kind with no old chunk goes after IHDR, the other stays", spPNG(spIHDR, spGAMA, spOldE, spIDAT, spIEND), spNewE, spNewX,
			eq("IHDR", "XMP", "gAMA", "eXIf", "IDAT", "IEND"), 0},
		{"PNG-PLACE-05 second old eXIf is dropped", spPNG(spIHDR, spOldE, spGAMA, spOldE, spIDAT, spIEND), spNewE, nil,
			eq("IHDR", "eXIf", "gAMA", "IDAT", "IEND"), 0},
		{"PNG-PLACE-06 second old XMP is dropped", spPNG(spIHDR, spGAMA, spOldX, spOldX, spIDAT, spIEND), nil, spNewX,
			eq("IHDR", "gAMA", "XMP", "IDAT", "IEND"), 0},
		{"PNG-PLACE-07 an iTXt that is not XMP is copied and does not place the XMP", spPNG(spIHDR, spOtrX, spIDAT, spIEND), nil, spNewX,
			eq("IHDR", "XMP", "iTXt", "IDAT", "IEND"), 0},
		{"PNG-PLACE-08 nothing to write: old eXIf and XMP are dropped by Inject, as before", spPNG(spIHDR, spOldE, spOldX, spTEXt, spIDAT, spIEND), nil, nil,
			eq("IHDR", "tEXt", "IDAT", "IEND"), 0},
		{"PNG-PLACE-08k nothing to write, eXIf and XMP kept: every chunk stays, byte for byte", spPNG(spIHDR, spOldE, spOldX, spTEXt, spIDAT, spIEND), nil, nil,
			eq("IHDR", "eXIf", "XMP", "tEXt", "IDAT", "IEND"), KeepEXIF | KeepXMP},
		{"PNG-PLACE-09 both old: each in its own place", spPNG(spIHDR, spOldX, spGAMA, spOldE, spIDAT, spIEND), spNewE, spNewX,
			eq("IHDR", "XMP", "gAMA", "eXIf", "IDAT", "IEND"), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out []byte
			if tc.keep != 0 {
				var buf bytes.Buffer
				if err := InjectKeeping(bytes.NewReader(tc.src), &buf, tc.exif, nil, tc.xmp, true, tc.keep); err != nil {
					t.Fatalf("InjectKeeping: %v", err)
				}
				out = buf.Bytes()
				if !bytes.Equal(out, tc.src) {
					t.Error("keeping every metadata chunk and writing none must give the source")
				}
			} else {
				out = spInjectPNG(t, tc.src, tc.exif, tc.xmp)
			}
			got := spTypes(t, out)
			if len(got) != len(tc.want) {
				t.Fatalf("chunks = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("chunks = %v, want %v", got, tc.want)
				}
			}
			if !bytes.Equal(spStripPNG(out), spStripPNG(tc.src)) {
				t.Error("the output without eXIf and text chunks differs from the source without them")
			}
			rawE, _, rawX, err := Extract(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if tc.exif != nil && !bytes.Equal(rawE, tc.exif) || tc.xmp != nil && !bytes.Equal(rawX, tc.xmp) {
				t.Error("the new metadata is not what Extract reads back")
			}
		})
	}
}

// TestInjectVerbatim covers PNG-IDENT: bytes the write does not replace are
// the source's: the CRC of a chunk that is wrong, the bytes after IEND, a
// chunk the library does not know.
func TestInjectVerbatim(t *testing.T) {
	t.Parallel()
	badCRC := pngChunk("pHYs", []byte{0, 0, 0x0b, 0x13, 0, 0, 0x0b, 0x13, 1})
	badCRC[len(badCRC)-1] ^= 0xFF
	src := spPNG(spIHDR, badCRC, pngChunk("zzZz", []byte("private")), spOldE, spIDAT, spIEND, []byte("data after IEND"))
	out := spInjectPNG(t, src, spNewE, spNewX)
	if !bytes.Contains(out, badCRC) {
		t.Error("the chunk with the wrong CRC was not copied byte for byte")
	}
	if !bytes.HasSuffix(out, append(append([]byte(nil), spIEND...), "data after IEND"...)) {
		t.Error("the bytes after IEND were not copied")
	}
	if !bytes.Equal(spStripPNG(out), spStripPNG(src)) {
		t.Error("the output without eXIf and text chunks differs from the source without them")
	}
	if got := spInjectPNG(t, spPNG(spIHDR, badCRC, spIDAT, spIEND, []byte("x")), nil, nil); !bytes.Equal(got, spPNG(spIHDR, badCRC, spIDAT, spIEND, []byte("x"))) {
		t.Error("a write of nothing changed a file without metadata")
	}
}

// TestInjectDamaged covers PNG-DAMAGE (task #403): after IHDR a chunk that
// cannot be followed is copied with the rest of the file, the metadata is
// still added after IHDR, and no IEND is invented.
func TestInjectDamaged(t *testing.T) {
	t.Parallel()
	over := append(binary.BigEndian.AppendUint32(nil, 5000), "iCCPpartial"...) // 5000 declared, 7 present
	huge := append(binary.BigEndian.AppendUint32(nil, 0xFFFFFFFF), "IDATxx"...)
	tests := []struct {
		name string
		tail []byte // after the intact chunks
	}{
		{"PNG-DAMAGE-01 declared length runs past the end", over},
		{"PNG-DAMAGE-02 length above 2^31-1", huge},
		{"PNG-DAMAGE-03 header cut short", []byte{0, 0, 0, 13, 'I'}},
		{"PNG-DAMAGE-04 one byte of a header", []byte{0}},
		{"PNG-DAMAGE-05 chunk cut before its CRC", append(binary.BigEndian.AppendUint32(nil, 4), "tEXtabcd"...)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := spPNG(spIHDR, spGAMA, spOldE, spIDAT)
			src = append(src, tc.tail...)
			out := spInjectPNG(t, src, spNewE, spNewX)
			if !bytes.HasSuffix(out, append(append([]byte(nil), spIDAT...), tc.tail...)) {
				t.Errorf("the damaged tail was not copied as it is: %x", out)
			}
			if got := spTypes(t, out); len(got) < 4 || got[0] != "IHDR" || got[1] != "XMP" || got[2] != "gAMA" || got[3] != "eXIf" {
				t.Errorf("chunks = %v, want IHDR XMP gAMA eXIf (replaced in place) ...", got)
			}
			for _, c := range spTypes(t, out) {
				if c == "IEND" {
					t.Error("an IEND chunk was added to a file that was cut short")
				}
			}
		})
	}
	t.Run("PNG-DAMAGE-06 the same damage before IHDR is refused", func(t *testing.T) {
		t.Parallel()
		for _, src := range [][]byte{
			spPNG(over),
			spPNG(huge),
			spPNG([]byte{0, 0, 0}),
			spPNG(),
		} {
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, spNewE, nil, nil, true); err == nil {
				t.Errorf("source %x: Inject wrote the metadata of a file with no image header", src[8:])
			}
		}
	})
	t.Run("PNG-DAMAGE-07 no IHDR at the clean end of the file", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		err := Inject(bytes.NewReader(spPNG(spGAMA, spIDAT, spIEND[:0])), &out, spNewE, nil, nil, true)
		if !errors.Is(err, ErrNoImageHeader) {
			t.Errorf("error = %v, want ErrNoImageHeader", err)
		}
		if err := Inject(bytes.NewReader(spPNG(spIEND)), io.Discard, spNewE, nil, nil, true); !errors.Is(err, ErrNoImageHeader) {
			t.Errorf("IEND before IHDR: error = %v, want ErrNoImageHeader", err)
		}
		if err := Inject(bytes.NewReader(spPNG(spGAMA)), io.Discard, nil, nil, nil, true); err != nil {
			t.Errorf("a write of nothing to a file with no IHDR failed: %v", err)
		}
	})
}

// TestInjectIEND covers PNG-IEND: a file that ends at a chunk boundary without
// IEND receives one (W3C PNG 3rd ed. §5.6: IEND must be last), which is why
// testdata/corpus/png/metadata-extractor/m6-*.png, which ends with a corrupted
// IEND ("IFND"), grows by 12 bytes in a write.
func TestInjectIEND(t *testing.T) {
	t.Parallel()
	src := spPNG(spIHDR, spIDAT, pngChunk("IFND", nil))
	out := spInjectPNG(t, src, nil, spNewX)
	if want := len(src) + len(spIEND) + len(pngChunk("iTXt", buildXMPChunk(spNewX))); len(out) != want {
		t.Errorf("output is %d bytes, want %d: the source, the XMP chunk and an IEND", len(out), want)
	}
	if !bytes.HasSuffix(out, spIEND) {
		t.Error("the output does not end with IEND")
	}
	if got := spInjectPNG(t, src, nil, nil); !bytes.Equal(got, append(append([]byte(nil), src...), spIEND...)) {
		t.Error("a write of nothing must add the IEND chunk and nothing else")
	}
	if !bytes.Equal(spInjectPNG(t, spPNG(spIHDR, spIDAT, spIEND), nil, nil), spPNG(spIHDR, spIDAT, spIEND)) {
		t.Error("a file with IEND changed in a write of nothing")
	}
}

// TestScanOldMetadata covers the pre-scan: it reads headers and the keyword of
// iTXt chunks only, restores the position, and stops where the chunk stream
// cannot be followed.
func TestScanOldMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		src            []byte
		wantE, wantX   bool
		exifOld, xmpOl bool
	}{
		{"both", spPNG(spIHDR, spOldE, spOldX, spIEND), true, true, true, true},
		{"none", spPNG(spIHDR, spGAMA, spOtrX, spIEND), true, true, false, false},
		{"only the kinds asked for", spPNG(spIHDR, spOldE, spOldX, spIEND), false, true, false, true},
		{"after IEND is not looked at", spPNG(spIHDR, spIEND, spOldE), true, true, false, false},
		{"short iTXt", spPNG(spIHDR, pngChunk("iTXt", []byte("XML:com")), spIEND), true, true, false, false},
		{"damaged tail stops the scan", append(spPNG(spIHDR), 0, 0, 0xFF, 0xFF, 'e', 'X', 'I', 'f'), true, true, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := bytes.NewReader(tc.src)
			if _, err := r.Seek(8, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			var hdr [8]byte
			e, x, err := scanOldMetadata(r, &hdr, 8, int64(len(tc.src)), tc.wantE, tc.wantX)
			if err != nil {
				t.Fatal(err)
			}
			if e != tc.exifOld || x != tc.xmpOl {
				t.Errorf("scanOldMetadata = (%v, %v), want (%v, %v)", e, x, tc.exifOld, tc.xmpOl)
			}
			if pos, _ := r.Seek(0, io.SeekCurrent); pos != 8 {
				t.Errorf("reader left at %d, want 8", pos)
			}
		})
	}
}

// FuzzPNGSplice feeds arbitrary bytes as the source and checks, for each write
// that succeeds, that the source is copied: without its eXIf and text chunks
// the output is the source without them, or that and the IEND chunk Inject adds
// to a file that ends without one; the new chunks are in the output.
func FuzzPNGSplice(f *testing.F) {
	f.Add(spPNG(spIHDR, spGAMA, spOldE, spOldX, spIDAT, spIEND, []byte("tail")))
	f.Add(spPNG(spIHDR, spIDAT, pngChunk("IFND", nil)))
	f.Add(append(spPNG(spIHDR, spGAMA), 0, 0, 0x13, 0x88, 'i', 'C', 'C', 'P', 1))
	f.Add(spPNG(spOtrX))
	f.Fuzz(func(t *testing.T, src []byte) {
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(src), &out, spNewE, nil, spNewX, true); err != nil {
			return
		}
		got, want := spStripPNG(out.Bytes()), spStripPNG(src)
		if !bytes.Equal(got, want) && !bytes.Equal(got, append(append([]byte(nil), want...), spIEND...)) {
			t.Fatalf("the output without eXIf and text chunks is not the source without them:\n out %x\n src %x", got, want)
		}
		if !bytes.Contains(out.Bytes(), pngChunk("eXIf", spNewE)) || !bytes.Contains(out.Bytes(), pngChunk("iTXt", buildXMPChunk(spNewX))) {
			t.Fatal("a new chunk is not in the output")
		}
	})
}

// TestInjectKeeping covers PNG-KEEP (task #405): the chunks of the kept
// segments stay as the source has them, whatever is written to the others.
func TestInjectKeeping(t *testing.T) {
	t.Parallel()
	badE := pngChunk("eXIf", []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'B', 'D'})
	badE[len(badE)-1] ^= 0xFF // a CRC that is wrong stays wrong
	rawExif := pngChunk("zTXt", []byte("Raw profile type exif\x00\x00x"))
	tests := []struct {
		name      string
		src       []byte
		exif, xmp []byte
		keep      Keep
		want      []string
		kept      [][]byte // chunks the output must contain verbatim
	}{
		{"PNG-KEEP-01 eXIf kept, XMP replaced", spPNG(spIHDR, spOldE, spOldX, spIDAT, spIEND), nil, spNewX, KeepEXIF,
			eq("IHDR", "eXIf", "XMP", "IDAT", "IEND"), [][]byte{spOldE}},
		{"PNG-KEEP-02 XMP kept, eXIf replaced", spPNG(spIHDR, spOldE, spOldX, spIDAT, spIEND), spNewE, nil, KeepXMP,
			eq("IHDR", "eXIf", "XMP", "IDAT", "IEND"), [][]byte{spOldX}},
		{"PNG-KEEP-03 eXIf kept with a wrong CRC and a duplicate", spPNG(spIHDR, badE, spGAMA, spOldE, spIDAT, spIEND), nil, spNewX, KeepEXIF,
			eq("IHDR", "XMP", "eXIf", "gAMA", "eXIf", "IDAT", "IEND"), [][]byte{badE, spOldE}},
		{"PNG-KEEP-04 an XMP kept is not replaced by a new one that is ignored", spPNG(spIHDR, spOldX, spIDAT, spIEND), spNewE, spNewX, KeepXMP,
			eq("IHDR", "eXIf", "XMP", "IDAT", "IEND"), [][]byte{spOldX}},
		{"PNG-KEEP-05 EXIF raw profile kept, no eXIf written", spPNG(spIHDR, rawExif, spIDAT, spIEND), nil, spNewX, KeepEXIF,
			eq("IHDR", "XMP", "zTXt", "IDAT", "IEND"), [][]byte{rawExif}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := InjectKeeping(bytes.NewReader(tc.src), &out, tc.exif, nil, tc.xmp, true, tc.keep); err != nil {
				t.Fatalf("InjectKeeping: %v", err)
			}
			got := spTypes(t, out.Bytes())
			if len(got) != len(tc.want) {
				t.Fatalf("chunks = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("chunks = %v, want %v", got, tc.want)
				}
			}
			for _, c := range tc.kept {
				if !bytes.Contains(out.Bytes(), c) {
					t.Errorf("a kept chunk is not in the output byte for byte")
				}
			}
		})
	}
}

// FuzzPNGInjectKeeping: a write that keeps every metadata segment and writes
// none is the source (task #405), plus the IEND chunk that a source ending at a
// chunk boundary without one receives; it never panics and writes no metadata.
func FuzzPNGInjectKeeping(f *testing.F) {
	f.Add(spPNG(spIHDR, spGAMA, spOldE, spOldX, spTEXt, spIDAT, spIEND, []byte("tail")))
	f.Add(spPNG(spIHDR, spIDAT, pngChunk("IFND", nil)))
	f.Add(append(spPNG(spIHDR, spGAMA), 0, 0, 0x13, 0x88, 'i', 'C', 'C', 'P', 1))
	f.Add(spPNG(spOtrX))
	f.Fuzz(func(t *testing.T, src []byte) {
		var out bytes.Buffer
		if err := InjectKeeping(bytes.NewReader(src), &out, spNewE, nil, spNewX, true, KeepEXIF|KeepIPTC|KeepXMP); err != nil {
			return
		}
		if !bytes.Equal(out.Bytes(), src) && !bytes.Equal(out.Bytes(), append(append([]byte(nil), src...), spIEND...)) {
			t.Fatalf("keeping every segment did not give the source:\n out %x\n src %x", out.Bytes(), src)
		}
	})
}
