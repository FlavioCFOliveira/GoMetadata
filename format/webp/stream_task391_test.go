package webp

// stream_task391_test.go — task #391: the streaming Inject must write the bytes
// the whole-file implementation wrote (oracleInject, oracle_test.go), for every
// input: real files, truncations of them and byte flips in their headers.

import (
	"bytes"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// payloadCombos are the (rawEXIF, rawXMP) pairs every input is written with.
func payloadCombos() []struct {
	name      string
	exif, xmp []byte
} {
	return []struct {
		name      string
		exif, xmp []byte
	}{
		{"none", nil, nil},
		{"exif", []byte("MM\x00*\x00\x00\x00\x08\x00\x00\x00\x00\x00\x00"), nil},
		{"xmp", nil, []byte("<x:xmpmeta xmlns:x='adobe:ns:meta/'/>")},
		{"both-odd", []byte("odd"), []byte("odd-xmp")},
		{"empty", []byte{}, []byte{}},
	}
}

// assertSameAsOracle writes data with both implementations and compares them.
func assertSameAsOracle(t *testing.T, name string, data []byte) {
	t.Helper()
	for _, pc := range payloadCombos() {
		want, wantErr := oracleInject(data, pc.exif, pc.xmp)
		var got bytes.Buffer
		err := Inject(bytes.NewReader(data), &got, pc.exif, nil, pc.xmp, true)
		if (err != nil) != (wantErr != nil) {
			t.Fatalf("%s/%s: err = %v, oracle err = %v", name, pc.name, err, wantErr)
		}
		if err == nil && !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("%s/%s: output differs from the whole-file implementation (%d vs %d bytes)", name, pc.name, got.Len(), len(want))
		}
	}
}

// seedFiles returns the WebP files of the package fixtures and, when present,
// of the corpus.
func seedFiles(t *testing.T) map[string][]byte {
	t.Helper()
	seeds := map[string][]byte{}
	for _, pattern := range []string{"testdata/*.webp", "../../testdata/corpus/webp/*/*.webp"} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if data, err := os.ReadFile(m); err == nil {
				seeds[m] = data
			}
		}
	}
	seeds["synthetic-vp8x"] = webpOf(rawChunk{"VP8X", vp8xPayloadFlags(0x10)}, rawChunk{"VP8 ", vp8Payload}, rawChunk{"EXIF", []byte("old")}, rawChunk{"XMP ", []byte("old")}, rawChunk{"ICCP", []byte("icc")})
	seeds["synthetic-simple"] = webpOf(rawChunk{"VP8 ", vp8Payload}, rawChunk{"ALPH", []byte{1, 2, 3}})
	seeds["synthetic-xmpnul"] = webpOf(rawChunk{"VP8L", []byte{0x2f, 0, 0, 0, 0, 0}}, rawChunk{"XMP\x00", []byte("x")}, rawChunk{"JUNK", []byte("odd")})
	return seeds
}

// TestInjectMatchesWholeFileImplementation compares the two implementations
// on every seed file and on its truncations and corruptions.
func TestInjectMatchesWholeFileImplementation(t *testing.T) {
	t.Parallel()
	for name, data := range seedFiles(t) {
		assertSameAsOracle(t, name, data)
		head := min(len(data), 96)
		for cut := range head { // every truncation inside the headers
			assertSameAsOracle(t, name+"/cut", data[:cut])
		}
		for _, frac := range []int{2, 3, 5, 7} { // and some in the chunk data
			assertSameAsOracle(t, name+"/cutfrac", data[:len(data)/frac])
			assertSameAsOracle(t, name+"/cutfrac-1", data[:len(data)-len(data)/frac])
		}
		rng := rand.New(rand.NewPCG(uint64(len(data)), 391)) //nolint:gosec // G115,G404: deterministic seed for reproducible corruptions, not a secret
		for range 150 {                                      // byte flips in the headers: sizes, FourCCs, VP8X
			mut := bytes.Clone(data[:min(len(data), 4096)])
			for range 1 + rng.IntN(3) {
				mut[rng.IntN(min(len(mut), 128))] = byte(rng.IntN(256)) //nolint:gosec // G115: IntN(256) fits a byte
			}
			assertSameAsOracle(t, name+"/flip", mut)
		}
	}
}

// FuzzInjectMatchesWholeFile: for any input and payloads the streaming Inject
// and the whole-file implementation agree (task #391).
func FuzzInjectMatchesWholeFile(f *testing.F) {
	for _, data := range [][]byte{
		webpOf(rawChunk{"VP8X", vp8xPayloadFlags(0x10)}, rawChunk{"VP8 ", vp8Payload}, rawChunk{"EXIF", []byte("old")}),
		webpOf(rawChunk{"VP8 ", vp8Payload}),
		webpOf(rawChunk{"VP8L", []byte{0x2f, 0, 0, 0, 0}}, rawChunk{"XMP\x00", []byte("x")}),
	} {
		f.Add(data, []byte("exif"), []byte("xmp"), uint8(3))
	}
	f.Fuzz(func(t *testing.T, data, exif, xmp []byte, which uint8) {
		var e, x []byte
		if which&1 != 0 {
			e = exif
		}
		if which&2 != 0 {
			x = xmp
		}
		if len(x) >= 8 && x[0] == 0 { // the wire-frame guard is tested elsewhere
			return
		}
		want, wantErr := oracleInject(data, e, x)
		var got bytes.Buffer
		err := Inject(bytes.NewReader(data), &got, e, nil, x, true)
		if (err != nil) != (wantErr != nil) {
			t.Fatalf("err = %v, oracle err = %v", err, wantErr)
		}
		if err == nil && !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("output differs from the whole-file implementation")
		}
	})
}
