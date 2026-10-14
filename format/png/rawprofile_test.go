package png

// rawprofile_test.go — tests for task #302: IPTC and EXIF stored in
// "Raw profile type ..." text chunks (ImageMagick / ExifTool convention).

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// rawProfileText formats profile bytes the way ImageMagick does: a line break,
// the profile name, the decimal length right-aligned in 8 columns, and the hex
// digits wrapped at 72 per line.
func rawProfileHexText(name string, profile []byte) []byte {
	h := hex.EncodeToString(profile)
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s\n%8d\n", name, len(profile))
	for len(h) > 72 {
		b.WriteString(h[:72] + "\n")
		h = h[72:]
	}
	b.WriteString(h + "\n")
	return []byte(b.String())
}

func deflate(b []byte) []byte {
	var out bytes.Buffer
	zw := zlib.NewWriter(&out)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return out.Bytes()
}

// textChunk builds the payload of a tEXt, zTXt or iTXt chunk for keyword/text.
func textChunk(chunkType, keyword string, text []byte) []byte {
	var b bytes.Buffer
	b.WriteString(keyword)
	b.WriteByte(0)
	switch chunkType {
	case "tEXt":
		b.Write(text)
	case "zTXt":
		b.WriteByte(0)
		b.Write(deflate(text))
	case "iTXt": // uncompressed
		b.Write([]byte{0, 0, 0, 0})
		b.Write(text)
	case "iTXtZ": // compressed
		b.Write([]byte{1, 0, 0, 0})
		b.Write(deflate(text))
	}
	return b.Bytes()
}

// pngWithChunks wraps the given (type, payload) chunks between IHDR and IEND.
func pngWithChunks(chunks ...[2]any) []byte {
	var buf bytes.Buffer
	buf.Write(pngSig[:])
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 1)
	binary.BigEndian.PutUint32(ihdr[4:], 1)
	ihdr[8], ihdr[9] = 8, 2
	writeChunkTo(&buf, "IHDR", ihdr)
	for _, c := range chunks {
		t, ok := c[0].(string)
		if !ok {
			panic("pngWithChunks: chunk type must be a string")
		}
		if t == "iTXtZ" {
			t = "iTXt"
		}
		writeChunkTo(&buf, t, c[1].([]byte)) //nolint:forcetypeassert // test helper
	}
	writeChunkTo(&buf, "IEND", nil)
	return buf.Bytes()
}

// sampleIIM returns an IIM stream with a caption, a keyword, and a copyright
// notice (IPTC IIM 4.2 §6: 2:120, 2:25, 2:116).
func sampleIIM(t *testing.T) []byte {
	t.Helper()
	i := &iptc.IPTC{}
	i.SetCaption("Raw profile caption")
	i.SetKeywords([]string{"alpha", "beta"})
	i.SetCopyright("(c) 2026 Raw Profile")
	out, err := iptc.Encode(i)
	if err != nil {
		t.Fatalf("iptc.Encode: %v", err)
	}
	return out
}

// irbOf wraps iim in a Photoshop IRB (resource 0x0404) with an unrelated
// 0x040C resource first, as Photoshop writes them.
func irbOf(iim []byte) []byte {
	var b []byte
	b = append(b, "8BIM\x04\x0C\x00\x00\x00\x00\x00\x04thmb"...)
	b = append(b, "8BIM\x04\x04\x00\x00"...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(iim))) //nolint:gosec // G115: test sizes are small
	b = append(b, iim...)
	if len(iim)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

// tinyEXIF returns a TIFF stream with Make and Model.
func tinyEXIF(t *testing.T) []byte {
	t.Helper()
	e := &exif.EXIF{IFD0: &exif.IFD{}}
	e.SetMake("RawProfileMake")
	e.SetCameraModel("RawProfileModel")
	out, err := exif.Encode(e)
	if err != nil {
		t.Fatalf("exif.Encode: %v", err)
	}
	return out
}

func extractBytes(t *testing.T, data []byte) (rawEXIF, rawIPTC, rawXMP []byte) {
	t.Helper()
	e, i, x, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return e, i, x
}

// TestExtract_RawProfileIPTC covers every chunk type and payload shape that
// carries IPTC in a "Raw profile type iptc" chunk.
func TestExtract_RawProfileIPTC(t *testing.T) {
	t.Parallel()
	iim := sampleIIM(t)
	irbData := irbOf(iim)
	cases := []struct {
		name      string
		chunkType string
		keyword   string
		profile   []byte
	}{
		{"zTXt-irb", "zTXt", "Raw profile type iptc", irbData},
		{"tEXt-irb", "tEXt", "Raw profile type iptc", irbData},
		{"iTXt-irb", "iTXt", "Raw profile type iptc", irbData},
		{"iTXt-compressed-irb", "iTXtZ", "Raw profile type iptc", irbData},
		{"zTXt-bare-iim", "zTXt", "Raw profile type iptc", iim},
		{"zTXt-photoshop-prefix", "zTXt", "Raw profile type iptc", append([]byte("Photoshop 3.0\x00"), irbData...)},
		{"keyword-case-insensitive", "zTXt", "Raw profile type IPTC", irbData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ct := tc.chunkType
			data := pngWithChunks([2]any{ct, textChunk(ct, tc.keyword, rawProfileHexText("IPTC profile", tc.profile))})
			_, got, _ := extractBytes(t, data)
			if !bytes.Equal(got, iim) {
				t.Errorf("rawIPTC = %x, want %x", got, iim)
			}
		})
	}
}

// TestExtract_RawProfileEXIF covers the "exif" and "APP1" keywords, with and
// without the "Exif\0\0" identifier, and the XMP packet carried by APP1.
func TestExtract_RawProfileEXIF(t *testing.T) {
	t.Parallel()
	tiff := tinyEXIF(t)
	withIdent := append([]byte("Exif\x00\x00"), tiff...)
	xmpPacket := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	cases := []struct {
		name, keyword string
		profile       []byte
		wantEXIF      []byte
		wantXMP       []byte
	}{
		{"exif-ident", "Raw profile type exif", withIdent, tiff, nil},
		{"exif-bare-tiff", "Raw profile type exif", tiff, tiff, nil},
		{"APP1-exif", "Raw profile type APP1", withIdent, tiff, nil},
		{"APP1-xmp", "Raw profile type APP1", append([]byte("http://ns.adobe.com/xap/1.0/\x00"), xmpPacket...), nil, xmpPacket},
	}
	for _, tc := range cases {
		for _, ct := range []string{"zTXt", "tEXt", "iTXt"} {
			t.Run(tc.name+"/"+ct, func(t *testing.T) {
				t.Parallel()
				data := pngWithChunks([2]any{ct, textChunk(ct, tc.keyword, rawProfileHexText("exif", tc.profile))})
				gotEXIF, gotIPTC, gotXMP := extractBytes(t, data)
				if !bytes.Equal(gotEXIF, tc.wantEXIF) {
					t.Errorf("rawEXIF = %x, want %x", gotEXIF, tc.wantEXIF)
				}
				if !bytes.Equal(gotXMP, tc.wantXMP) {
					t.Errorf("rawXMP = %q, want %q", gotXMP, tc.wantXMP)
				}
				if gotIPTC != nil {
					t.Errorf("rawIPTC = %x, want nil", gotIPTC)
				}
			})
		}
	}
}

// TestExtract_RawProfileNativeChunksWin: the eXIf chunk and the XMP iTXt take
// precedence over raw profiles, whatever the chunk order.
func TestExtract_RawProfileNativeChunksWin(t *testing.T) {
	t.Parallel()
	tiff := tinyEXIF(t)
	otherTIFF := append([]byte(nil), tiff...)
	otherTIFF = append(otherTIFF, 0, 0) // distinguishable from the raw profile copy
	xmpNative := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><native/></x:xmpmeta>`)
	xmpRaw := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><raw/></x:xmpmeta>`)
	rawEXIF := [2]any{"zTXt", textChunk("zTXt", "Raw profile type exif", rawProfileHexText("exif", append([]byte("Exif\x00\x00"), tiff...)))}
	rawXMP := [2]any{"zTXt", textChunk("zTXt", "Raw profile type APP1", rawProfileHexText("APP1", append([]byte("http://ns.adobe.com/xap/1.0/\x00"), xmpRaw...)))}
	nativeEXIF := [2]any{"eXIf", otherTIFF}
	nativeXMP := [2]any{"iTXt", buildXMPChunk(xmpNative)}
	for name, chunks := range map[string][][2]any{
		"raw-first":    {rawEXIF, rawXMP, nativeEXIF, nativeXMP},
		"native-first": {nativeEXIF, nativeXMP, rawEXIF, rawXMP},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			gotEXIF, _, gotXMP := extractBytes(t, pngWithChunks(chunks...))
			if !bytes.Equal(gotEXIF, otherTIFF) {
				t.Errorf("rawEXIF is not the eXIf chunk")
			}
			if !bytes.Equal(gotXMP, xmpNative) {
				t.Errorf("rawXMP = %q, want the iTXt packet", gotXMP)
			}
		})
	}
}

// TestExtract_RawProfileMalformed: malformed hex, a length mismatch, or any
// other damage yields no data and never panics.
func TestExtract_RawProfileMalformed(t *testing.T) {
	t.Parallel()
	iim := sampleIIM(t)
	good := rawProfileHexText("IPTC profile", irbOf(iim))
	hexStart := bytes.LastIndexByte(good[:len(good)-1], '\n') + 1
	mutate := func(f func([]byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	texts := map[string][]byte{
		"non-hex-char":       mutate(func(b []byte) []byte { b[hexStart] = 'g'; return b }),
		"odd-digit-count":    mutate(func(b []byte) []byte { return append(b[:len(b)-2], '\n') }),
		"declared-too-big":   bytes.Replace(good, []byte(fmt.Sprintf("%8d", len(irbOf(iim)))), []byte(fmt.Sprintf("%8d", len(irbOf(iim))+1)), 1),
		"declared-too-small": bytes.Replace(good, []byte(fmt.Sprintf("%8d", len(irbOf(iim)))), []byte(fmt.Sprintf("%8d", len(irbOf(iim))-1)), 1),
		"huge-declared":      []byte("\nIPTC profile\n4294967295\nabcd\n"),
		"too-many-digits":    []byte("\nIPTC profile\n123456789012345\nabcd\n"),
		"no-length":          []byte("\nIPTC profile\nabcd\n"),
		"no-name-line":       []byte("abcd"),
		"empty":              {},
		"truncated-hex":      good[:len(good)/2],
		"not-iim-not-irb":    rawProfileHexText("IPTC profile", []byte("hello world, not a profile")),
	}
	for name, text := range texts {
		for _, ct := range []string{"tEXt", "zTXt", "iTXt"} {
			t.Run(name+"/"+ct, func(t *testing.T) {
				t.Parallel()
				data := pngWithChunks([2]any{ct, textChunk(ct, "Raw profile type iptc", text)})
				if _, got, _ := extractBytes(t, data); got != nil {
					t.Errorf("rawIPTC = %x, want nil", got)
				}
			})
		}
	}

	t.Run("bad-zlib", func(t *testing.T) {
		t.Parallel()
		payload := append([]byte("Raw profile type iptc\x00\x00"), "not a zlib stream"...)
		if _, got, _ := extractBytes(t, pngWithChunks([2]any{"zTXt", payload})); got != nil {
			t.Errorf("rawIPTC = %x, want nil", got)
		}
	})
	t.Run("bad-compression-method", func(t *testing.T) {
		t.Parallel()
		payload := append([]byte("Raw profile type iptc\x00\x07"), deflate(good)...)
		if _, got, _ := extractBytes(t, pngWithChunks([2]any{"zTXt", payload})); got != nil {
			t.Errorf("rawIPTC = %x, want nil", got)
		}
	})
	t.Run("keyword-without-nul", func(t *testing.T) {
		t.Parallel()
		if _, got, _ := extractBytes(t, pngWithChunks([2]any{"tEXt", []byte("Raw profile type iptc")})); got != nil {
			t.Errorf("rawIPTC = %x, want nil", got)
		}
	})
	t.Run("unknown-profile-kind", func(t *testing.T) {
		t.Parallel()
		data := pngWithChunks([2]any{"tEXt", textChunk("tEXt", "Raw profile type icc", good)})
		e, i, x := extractBytes(t, data)
		if e != nil || i != nil || x != nil {
			t.Errorf("unexpected data for an unread profile kind: %x %x %q", e, i, x)
		}
	})
}

// TestDecodeRawProfile_ImageMagickLayout pins the decoded bytes for the exact
// layout ImageMagick writes (72 hex digits per line, 8-column length).
func TestDecodeRawProfile_ImageMagickLayout(t *testing.T) {
	t.Parallel()
	profile := make([]byte, 100)
	for i := range profile {
		profile[i] = byte(i * 7)
	}
	got := decodeRawProfile(rawProfileHexText("exif", profile))
	if !bytes.Equal(got, profile) {
		t.Errorf("decoded profile differs: %x", got)
	}
	// CRLF line breaks (ExifTool on Windows) and upper-case digits.
	crlf := strings.ReplaceAll(strings.ToUpper(string(rawProfileHexText("exif", profile))), "\n", "\r\n")
	crlf = strings.Replace(crlf, "EXIF", "exif", 1)
	if got := decodeRawProfile([]byte(crlf)); !bytes.Equal(got, profile) {
		t.Errorf("CRLF/upper-case decode differs")
	}
}

// TestSkipPlainTextChunks: ordinary text chunks and XMP are unaffected.
func TestExtract_RawProfileDoesNotDisturbXMPText(t *testing.T) {
	t.Parallel()
	xmp := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	data := pngWithChunks(
		[2]any{"tEXt", textChunk("tEXt", "Comment", []byte("hello"))},
		[2]any{"iTXt", buildXMPChunk(xmp)},
	)
	e, i, x := extractBytes(t, data)
	if e != nil || i != nil || !bytes.Equal(x, xmp) {
		t.Errorf("got %x %x %q", e, i, x)
	}
}

// TestExtract_RawProfileFixtures (task #302): one committed fixture per
// keyword; the decoded values equal what ExifTool 13.55 reports for the same
// file ("exiftool -a -G1 -IPTC:all -Make -Model").
//
//   - png_raw_profile_iptc.png: written entirely by ExifTool ("exiftool
//     -IPTC:Caption-Abstract=... file.png" stores a zTXt "Raw profile type
//     iptc" chunk holding a Photoshop IRB);
//   - png_raw_profile_exif.png (zTXt "Raw profile type exif") and
//     png_raw_profile_app1.png (tEXt "Raw profile type APP1"): ImageMagick
//     layout around an EXIF block written by ExifTool ("exiftool -Make=...
//     -Model=... -o x.exif").
func TestExtract_RawProfileFixtures(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("..", "..", "testdata", "fixtures")

	t.Run("iptc", func(t *testing.T) {
		t.Parallel()
		data, err := os.ReadFile(filepath.Join(dir, "png_raw_profile_iptc.png"))
		if err != nil {
			t.Fatal(err)
		}
		_, rawIPTC, _ := extractBytes(t, data)
		p, err := iptc.Parse(rawIPTC)
		if err != nil {
			t.Fatalf("iptc.Parse: %v", err)
		}
		// [IPTC] Keywords: alpha, beta; By-line: Fixture Author; Copyright
		// Notice: (c) 2026 Fixture; Caption-Abstract: ExifTool raw profile
		// caption; Application Record Version: 4.
		if got := p.Keywords(); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
			t.Errorf("Keywords = %q", got)
		}
		if got := p.Creator(); got != "Fixture Author" {
			t.Errorf("By-line = %q", got)
		}
		if got := p.Copyright(); got != "(c) 2026 Fixture" {
			t.Errorf("Copyright Notice = %q", got)
		}
		if got := p.Caption(); got != "ExifTool raw profile caption" {
			t.Errorf("Caption-Abstract = %q", got)
		}
		if !bytes.Contains(rawIPTC, []byte{0x1C, 0x02, 0x00, 0x00, 0x02, 0x00, 0x04}) {
			t.Error("Application Record Version 2:00 = 4 missing")
		}
	})
	for _, name := range []string{"png_raw_profile_exif.png", "png_raw_profile_app1.png"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			rawEXIF, rawIPTC, rawXMP := extractBytes(t, data)
			if rawIPTC != nil || rawXMP != nil {
				t.Errorf("unexpected IPTC/XMP: %x %q", rawIPTC, rawXMP)
			}
			e, err := exif.Parse(rawEXIF)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			// [IFD0] Make: RawProfileMake; Camera Model Name: RawProfileModel.
			var camMake string
			if me := e.IFD0.Get(exif.TagMake); me != nil {
				camMake = strings.TrimRight(string(me.Value), "\x00")
			}
			if camMake != "RawProfileMake" || e.CameraModel() != "RawProfileModel" {
				t.Errorf("Make/Model = %q / %q", camMake, e.CameraModel())
			}
		})
	}
}

// FuzzDecodeRawProfile feeds arbitrary text to the raw profile decoder: it
// must never panic, and any non-nil result must have the length it declares.
func FuzzDecodeRawProfile(f *testing.F) {
	f.Add([]byte("\nIPTC profile\n       4\n1c020000\n"))
	f.Add([]byte("\nexif\n       2\nab\ncd\n"))
	f.Add([]byte("\nx\n99999999999\nab"))
	f.Add([]byte("\n\n\n"))
	f.Add([]byte("\nIPTC profile\n       4\n1c02zz00\n")) // malformed hex
	f.Add([]byte("\nIPTC profile\n       5\n1c020000\n")) // length mismatch
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, text []byte) {
		out := decodeRawProfile(text)
		if out == nil {
			return
		}
		// Re-derive the declared length: the decoder only accepts equality.
		i := skipSpace(text, 0)
		nl := bytes.IndexByte(text[i:], '\n')
		i = skipSpace(text, i+nl+1)
		var declared int
		for ; i < len(text) && text[i] >= '0' && text[i] <= '9'; i++ {
			declared = declared*10 + int(text[i]-'0')
		}
		if declared != len(out) {
			t.Fatalf("decoded %d bytes, declared %d", len(out), declared)
		}
	})
}

// FuzzPNGExtractRawProfile mutates whole PNGs seeded with raw profile chunks.
func FuzzPNGExtractRawProfile(f *testing.F) {
	iim := []byte{0x1C, 0x02, 0x78, 0x00, 0x02, 'h', 'i'}
	for _, ct := range []string{"tEXt", "zTXt", "iTXt"} {
		f.Add(pngWithChunks([2]any{ct, textChunk(ct, "Raw profile type iptc", rawProfileHexText("IPTC profile", irbOf(iim)))}))
		f.Add(pngWithChunks([2]any{ct, textChunk(ct, "Raw profile type exif", rawProfileHexText("exif", append([]byte("Exif\x00\x00"), 'I', 'I', '*', 0)))}))
		// Malformed hex digit and declared-length mismatch (task #302).
		f.Add(pngWithChunks([2]any{ct, textChunk(ct, "Raw profile type iptc", []byte("\nIPTC profile\n       4\n1c02zz00\n"))}))
		f.Add(pngWithChunks([2]any{ct, textChunk(ct, "Raw profile type APP1", []byte("\nAPP1\n       9\n1c020000\n"))}))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}

// BenchmarkPNGExtractRawProfile measures Extract on a PNG whose only metadata
// is a zTXt raw profile with IPTC.
func BenchmarkPNGExtractRawProfile(b *testing.B) {
	iim := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	data := pngWithChunks([2]any{"zTXt", textChunk("zTXt", "Raw profile type iptc", rawProfileHexText("IPTC profile", irbOf(iim)))})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, _, _, err := Extract(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
