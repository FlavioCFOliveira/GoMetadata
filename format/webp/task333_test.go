package webp

// task333_test.go — regression tests for task #333: an 'EXIF' chunk that
// starts with the "Exif\0\0" JPEG identifier is read as the TIFF stream it
// carries, a chunk with the non-standard FourCC "XMP\0" is read as XMP, and a
// write emits exactly one standard 'EXIF' chunk (no identifier) and one 'XMP '
// chunk (RFC 9649 §2.7.1.5: at most one chunk of each type; Google WebP
// Container Specification, "Metadata").
//
// Fixture shape: testdata/corpus/webp/metadata-extractor/Issue 473 (Java).webp
// (VP8X with only the Exif flag, VP8, EXIF with the identifier, XMP\0).

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

type rawChunk struct {
	id   string
	data []byte
}

// webpOf builds a RIFF/WEBP stream from the given chunks, in order.
func webpOf(chunks ...rawChunk) []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")
	for _, c := range chunks {
		var id [4]byte
		copy(id[:], c.id)
		writeRIFFChunk(&body, id, c.data)
	}
	out := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(body.Len())) //nolint:gosec // G115: test sizes are small
	return append(out, body.Bytes()...)
}

// chunksOf returns the (FourCC, payload) list of a WebP stream.
func chunksOf(t *testing.T, data []byte) []rawChunk {
	t.Helper()
	var out []rawChunk
	for pos := 12; pos+8 <= len(data); {
		n := int(binary.LittleEndian.Uint32(data[pos+4:]))
		if pos+8+n > len(data) {
			t.Fatalf("chunk at %d overruns the stream", pos)
		}
		out = append(out, rawChunk{string(data[pos : pos+4]), data[pos+8 : pos+8+n]})
		pos += 8 + n + n%2
	}
	return out
}

func countFourCC(cs []rawChunk, id string) int {
	n := 0
	for _, c := range cs {
		if c.id == id {
			n++
		}
	}
	return n
}

var (
	vp8Payload = []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00} //nolint:gochecknoglobals // immutable test fixture
	tiffStream = []byte("MM\x00*\x00\x00\x00\x08\x00\x00\x00\x00\x00\x00")          //nolint:gochecknoglobals // immutable test fixture
	xmpPacket  = []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"><a/></x:xmpmeta>")   //nolint:gochecknoglobals // immutable test fixture
)

func vp8xPayloadFlags(flags byte) []byte {
	return []byte{flags, 0, 0, 0, 0, 0, 0, 0, 0, 0}
}

func extractAll(t *testing.T, data []byte) (exif, xmp []byte) {
	t.Helper()
	e, _, x, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return e, x
}

// TestExtract_ExifPreambleAndXMPNul (WEBP-EXIF-01, WEBP-XMP-01).
func TestExtract_ExifPreambleAndXMPNul(t *testing.T) {
	t.Parallel()
	withPreamble := append([]byte("Exif\x00\x00"), tiffStream...)
	cases := []struct {
		name     string
		chunks   []rawChunk
		wantEXIF []byte
		wantXMP  []byte
	}{
		{"WEBP-01 EXIF with identifier", []rawChunk{{"VP8 ", vp8Payload}, {"EXIF", withPreamble}}, tiffStream, nil},
		{"WEBP-01 EXIF without identifier", []rawChunk{{"VP8 ", vp8Payload}, {"EXIF", tiffStream}}, tiffStream, nil},
		{"WEBP-01 EXIF with only the identifier", []rawChunk{{"VP8 ", vp8Payload}, {"EXIF", []byte("Exif\x00\x00")}}, []byte{}, nil},
		{"WEBP-01 identifier-like prefix is not stripped", []rawChunk{{"VP8 ", vp8Payload}, {"EXIF", []byte("Exif\x00MM\x00*")}}, []byte("Exif\x00MM\x00*"), nil},
		{"WEBP-02 XMP NUL FourCC", []rawChunk{{"VP8 ", vp8Payload}, {"XMP\x00", xmpPacket}}, nil, xmpPacket},
		{"WEBP-02 XMP space FourCC", []rawChunk{{"VP8 ", vp8Payload}, {"XMP ", xmpPacket}}, nil, xmpPacket},
		{"WEBP-02 both XMP FourCCs, standard first", []rawChunk{{"XMP ", xmpPacket}, {"XMP\x00", []byte("<other/>")}}, nil, xmpPacket},
		{"WEBP-02 both XMP FourCCs, standard last", []rawChunk{{"XMP\x00", []byte("<other/>")}, {"XMP ", xmpPacket}}, nil, xmpPacket},
		{"WEBP-02 Issue 473 layout", []rawChunk{{"VP8X", vp8xPayloadFlags(0x08)}, {"VP8 ", vp8Payload}, {"EXIF", withPreamble}, {"XMP\x00", xmpPacket}}, tiffStream, xmpPacket},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotEXIF, gotXMP := extractAll(t, webpOf(tc.chunks...))
			if !bytes.Equal(gotEXIF, tc.wantEXIF) || (gotEXIF == nil) != (tc.wantEXIF == nil) {
				t.Errorf("EXIF = %q, want %q", gotEXIF, tc.wantEXIF)
			}
			if !bytes.Equal(gotXMP, tc.wantXMP) || (gotXMP == nil) != (tc.wantXMP == nil) {
				t.Errorf("XMP = %q, want %q", gotXMP, tc.wantXMP)
			}
		})
	}
}

// TestInject_OneStandardChunkEach (WEBP-META-01): whatever FourCC variant the
// source used, the output holds exactly one 'EXIF' chunk without the
// identifier and exactly one 'XMP ' chunk, with the VP8X flags set to match.
func TestInject_OneStandardChunkEach(t *testing.T) {
	t.Parallel()
	t.Run("WEBP-03", func(t *testing.T) {
		t.Parallel()
		src := webpOf(
			rawChunk{"VP8X", vp8xPayloadFlags(0x08)},
			rawChunk{"VP8 ", vp8Payload},
			rawChunk{"EXIF", append([]byte("Exif\x00\x00"), tiffStream...)},
			rawChunk{"XMP\x00", xmpPacket},
		)
		rawEXIF, _ := extractAll(t, src)
		newXMP := []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"><b/></x:xmpmeta>")
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(src), &out, rawEXIF, nil, newXMP, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		cs := chunksOf(t, out.Bytes())
		for id, want := range map[string]int{"EXIF": 1, "XMP ": 1, "XMP\x00": 0, "VP8X": 1, "VP8 ": 1} {
			if got := countFourCC(cs, id); got != want {
				t.Errorf("%d %q chunks, want %d", got, id, want)
			}
		}
		gotEXIF, gotXMP := extractAll(t, out.Bytes())
		if !bytes.Equal(gotEXIF, tiffStream) {
			t.Errorf("EXIF after write = %q, want the original TIFF stream %q", gotEXIF, tiffStream)
		}
		if !bytes.Equal(gotXMP, newXMP) {
			t.Errorf("XMP after write = %q, want %q", gotXMP, newXMP)
		}
		for _, c := range cs {
			if c.id == "VP8X" && c.data[0]&(vp8xFlagEXIF|vp8xFlagXMP) != vp8xFlagEXIF|vp8xFlagXMP {
				t.Errorf("VP8X flags = %#x, want the EXIF and XMP bits set", c.data[0])
			}
			if c.id == "EXIF" && bytes.HasPrefix(c.data, []byte("Exif\x00\x00")) {
				t.Error("the EXIF chunk still starts with the Exif identifier")
			}
		}
		// With no XMP payload the "XMP\0" chunk goes too (nil removes XMP, as for 'XMP ').
		out.Reset()
		if err := Inject(bytes.NewReader(src), &out, rawEXIF, nil, nil, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		if cs := chunksOf(t, out.Bytes()); countFourCC(cs, "XMP\x00")+countFourCC(cs, "XMP ") != 0 {
			t.Error("an XMP chunk survived a write with no XMP payload")
		}
	})
}

// TestCorpus_Issue473 runs the reproduction of task #333 on the corpus file.
func TestCorpus_Issue473(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "testdata", "corpus", "webp", "metadata-extractor", "Issue 473 (Java).webp")
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	rawEXIF, rawXMP := extractAll(t, src)
	if !bytes.HasPrefix(rawEXIF, []byte("MM\x00*")) || !bytes.HasPrefix(rawXMP, []byte("<?xpacket")) {
		t.Fatalf("EXIF %q / XMP %q not read", rawEXIF[:min(4, len(rawEXIF))], rawXMP[:min(9, len(rawXMP))])
	}
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, rawEXIF, nil, rawXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	// The XMP is unchanged, so its "XMP\0" chunk is copied as it is (task #413).
	cs := chunksOf(t, out.Bytes())
	if countFourCC(cs, "EXIF") != 1 || countFourCC(cs, "XMP ") != 0 || countFourCC(cs, "XMP\x00") != 1 {
		t.Errorf("chunks after the write: %v", func() []string {
			ids := make([]string, 0, len(cs))
			for _, c := range cs {
				ids = append(ids, c.id)
			}
			return ids
		}())
	}
	gotEXIF, gotXMP := extractAll(t, out.Bytes())
	if !bytes.Equal(gotEXIF, rawEXIF) || !bytes.Equal(gotXMP, rawXMP) {
		t.Error("EXIF or XMP content changed by the round trip")
	}
}
