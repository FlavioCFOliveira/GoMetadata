package tiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"
)

// testXMPPacket is a minimal XMP packet that forces the relocate path.
const testXMPPacket = `<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end='w'?>`

// TestInjectWithEXIFRawDoesNotMutateCallerInput verifies that
// InjectWithEXIFORF and InjectWithEXIFRW2 never modify the caller's
// originalBytes, while still restoring the container magic in the output.
func TestInjectWithEXIFRawDoesNotMutateCallerInput(t *testing.T) {
	t.Parallel()

	orf := buildMinimalTIFF(binary.LittleEndian, nil, nil)
	orf[2], orf[3] = 'R', 'O' // IIRO

	type inject func([]byte, io.Writer) error
	cases := []struct {
		name   string
		input  func(t *testing.T) []byte
		inject inject
	}{
		{"ORF_fixture", func(*testing.T) []byte { return orf }, injectORF},
		{"RW2_fixture", func(*testing.T) []byte { return buildRW2WithIFD1() }, injectRW2},
		{"ORF_corpus", corpusFile("../../testdata/corpus/raw/metadata-extractor/Olympus TG-4.orf"), injectORF},
		{"RW2_corpus", corpusFile("../../testdata/corpus/raw/metadata-extractor/Panasonic DMC-GF7.rw2"), injectRW2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := tc.input(t)
			orig := bytes.Clone(in)
			var out bytes.Buffer
			if err := tc.inject(in, &out); err != nil {
				t.Fatalf("inject: %v", err)
			}
			if !bytes.Equal(in, orig) {
				t.Fatal("caller input was modified")
			}
			if out.Len() < 4 || !bytes.Equal(out.Bytes()[:4], orig[:4]) {
				t.Fatalf("output magic = % X, want % X", out.Bytes()[:min(4, out.Len())], orig[:4])
			}
		})
	}
}

func injectORF(in []byte, w io.Writer) error {
	return InjectWithEXIFORF(in, nil, nil, []byte(testXMPPacket), w)
}

func injectRW2(in []byte, w io.Writer) error {
	return InjectWithEXIFRW2(in, nil, nil, []byte(testXMPPacket), w)
}

// corpusFile returns an input loader that skips the test when the corpus
// file is absent.
func corpusFile(path string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("corpus file %s not present: %v", path, err)
		}
		return data
	}
}

// endlessSeeker is an io.ReadSeeker whose Seek succeeds only for
// Seek(0, io.SeekStart), so the sized read cannot learn the input size.
type endlessSeeker struct{ r *bytes.Reader }

func (e *endlessSeeker) Read(p []byte) (int, error) { return e.r.Read(p) } //nolint:wrapcheck // transparent test reader

func (e *endlessSeeker) Seek(off int64, whence int) (int64, error) {
	if off == 0 && whence == io.SeekStart {
		return e.r.Seek(0, io.SeekStart) //nolint:wrapcheck // transparent test reader
	}
	return 0, errors.New("seek not supported")
}

// TestExtractNonSeekableFallback verifies Extract falls back to the bounded
// streaming read when the input size cannot be obtained by seeking, and
// still enforces maxFileSize on that path.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractNonSeekableFallback(t *testing.T) {
	data := buildMinimalTIFF(binary.LittleEndian, nil, []byte(testXMPPacket))

	rawEXIF, _, rawXMP, err := Extract(&endlessSeeker{bytes.NewReader(data)})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !bytes.Equal(rawEXIF, data) || !bytes.Equal(rawXMP, []byte(testXMPPacket)) {
		t.Fatal("fallback path returned different payloads")
	}

	setMaxFileSizeForTest(t, int64(len(data)-1))
	if _, _, _, err := Extract(&endlessSeeker{bytes.NewReader(data)}); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Extract over limit: err = %v, want ErrFileTooLarge", err)
	}
}
