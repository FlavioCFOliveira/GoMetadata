package tiff

// legacy_api_compat_test.go — backward-compatibility regression guard.
//
// A pre-v1.4.0 apidiff run against tag v1.3.0 found that the six
// InjectWithEXIF* entry points had gained three new parameters (an
// io.ReadSeeker and a wholeFile bool) as part of sprint 44's streaming
// rewrite (#291) — an incompatible API change that is forbidden in a MINOR
// release (semver / Go module compatibility rules).
//
// The fix restores the original v1.3.0 signatures as thin wrappers over the
// new streaming implementations, which keep their #291 behaviour under new
// names (InjectWithEXIF*Stream). This file proves both halves of that fix:
//
//   - TestLegacyInjectSignaturesPinned: compile-time assignments that pin the
//     exact v1.3.0 signature for all six legacy names, so a future signature
//     change fails to compile instead of silently breaking every caller
//     again.
//   - TestLegacyInjectMatchesStream: table-driven test proving each legacy
//     function's output is byte-identical to calling its *Stream sibling as
//     InjectWithEXIF*Stream(bytes.NewReader(originalBytes), originalBytes,
//     true, ...) — i.e. the wrapper introduces no behavioural difference
//     whatsoever, for both the pass-through fast path and the full
//     copy-and-relocate path.
import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// The following compile-time assignments pin the exact v1.3.0 signature of
// every InjectWithEXIF* legacy entry point. If any of these six functions is
// ever given a different signature, the package fails to compile — turning
// the exact class of break an apidiff run caught in sprint 44's streaming
// rewrite (#291) into a permanent, unskippable regression gate.
var (
	_ func(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error = InjectWithEXIF
	_ func(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error = InjectWithEXIFCR2
	_ func(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error = InjectWithEXIFNEF
	_ func(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error = InjectWithEXIFARW
	_ func(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error = InjectWithEXIFORF
	_ func(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error = InjectWithEXIFRW2
)

// TestLegacyInjectSignaturesPinned documents, and gives a named home to, the
// compile-time signature pin declared above: if this file compiles, all six
// legacy InjectWithEXIF* functions still have their exact v1.3.0 signature.
func TestLegacyInjectSignaturesPinned(t *testing.T) {
	t.Parallel()
	// No runtime assertion is needed: the package-level var block above is
	// the actual gate. This test exists so `go test -run` and CI reports
	// name the guarantee explicitly.
}

// mutatedEXIFForCompatTest parses original independently and applies the
// identical mutation (SetCopyright); every call performs its own exif.Parse,
// so no two callers of this helper ever share a mutable *exif.EXIF — every
// relocate function mutates its EXIF argument in place (ThumbnailData clear,
// Entries slice rewrite for strip/tile placeholders; see relocate.go), so
// reusing one instance across a legacy-vs-Stream comparison would make the
// second call observe the first call's mutated state instead of the
// original input.
//
// acceptMagic is forwarded to exif.AcceptRAWMagic (0 for standard classic
// TIFF; the ORF/RW2 non-standard bytes[2:4] magic otherwise — see
// exif.AcceptRAWMagic's own doc comment).
func mutatedEXIFForCompatTest(t *testing.T, original []byte, acceptMagic uint16) *exif.EXIF {
	t.Helper()
	var opts []exif.ParseOption
	if acceptMagic != 0 {
		opts = append(opts, exif.AcceptRAWMagic(acceptMagic))
	}
	e, err := exif.Parse(original, opts...)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	e.SetCopyright("(c) 2026 legacy-api-compat")
	return e
}

// legacyCompatCase describes one InjectWithEXIF*/InjectWithEXIF*Stream pair
// under one input/argument combination.
type legacyCompatCase struct {
	name        string
	input       func() []byte
	mutate      bool // false: modifiedEXIF is nil (pass-through or internal-parse path); true: independently parsed + mutated per call
	acceptMagic uint16
	rawIPTC     []byte
	rawXMP      []byte
	legacyFn    func(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error
	streamFn    func(r io.ReadSeeker, prefix []byte, wholeFile bool, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error
}

// TestLegacyInjectMatchesStream proves that every legacy InjectWithEXIF*
// function produces byte-identical output to calling its InjectWithEXIF*
// Stream sibling as
// InjectWithEXIF*Stream(bytes.NewReader(originalBytes), originalBytes, true,
// modifiedEXIF, rawIPTC, rawXMP, w) — exactly what the wrapper does — for
// both the pass-through fast path (no metadata changes) and the full
// copy-and-relocate path (mutated EXIF + upserted IPTC/XMP).
func TestLegacyInjectMatchesStream(t *testing.T) {
	t.Parallel()

	tiffInput := func() []byte { return buildMinimalTIFF(binary.LittleEndian, nil, nil) }
	orfInput := func() []byte {
		b := buildMinimalTIFF(binary.LittleEndian, nil, nil)
		b[2], b[3] = 'R', 'O' // IIRO Olympus ORF magic
		return b
	}
	rw2Input := buildRW2WithIFD1

	const (
		orfMagic = 0x4F52 // "RO" at bytes[2:4], little-endian uint16 (see exif.AcceptRAWMagic)
		rw2Magic = 0x0055 // "U\x00" at bytes[2:4], little-endian uint16
	)

	cases := []legacyCompatCase{
		{"InjectWithEXIF/passthrough", tiffInput, false, 0, nil, nil, InjectWithEXIF, InjectWithEXIFStream},
		{"InjectWithEXIF/relocate", tiffInput, true, 0, []byte("IPTC-PAYLOAD"), []byte(testXMPPacket), InjectWithEXIF, InjectWithEXIFStream},

		{"InjectWithEXIFCR2/passthrough", tiffInput, false, 0, nil, nil, InjectWithEXIFCR2, InjectWithEXIFCR2Stream},
		{"InjectWithEXIFCR2/relocate", tiffInput, true, 0, []byte("IPTC-PAYLOAD"), []byte(testXMPPacket), InjectWithEXIFCR2, InjectWithEXIFCR2Stream},

		{"InjectWithEXIFNEF/passthrough", tiffInput, false, 0, nil, nil, InjectWithEXIFNEF, InjectWithEXIFNEFStream},
		{"InjectWithEXIFNEF/relocate", tiffInput, true, 0, []byte("IPTC-PAYLOAD"), []byte(testXMPPacket), InjectWithEXIFNEF, InjectWithEXIFNEFStream},

		{"InjectWithEXIFARW/passthrough", tiffInput, false, 0, nil, nil, InjectWithEXIFARW, InjectWithEXIFARWStream},
		{"InjectWithEXIFARW/relocate", tiffInput, true, 0, []byte("IPTC-PAYLOAD"), []byte(testXMPPacket), InjectWithEXIFARW, InjectWithEXIFARWStream},

		{"InjectWithEXIFORF/passthrough", orfInput, false, 0, nil, nil, InjectWithEXIFORF, InjectWithEXIFORFStream},
		{"InjectWithEXIFORF/relocate", orfInput, true, orfMagic, []byte("IPTC-PAYLOAD"), []byte(testXMPPacket), InjectWithEXIFORF, InjectWithEXIFORFStream},

		{"InjectWithEXIFRW2/passthrough", rw2Input, false, 0, nil, nil, InjectWithEXIFRW2, InjectWithEXIFRW2Stream},
		{"InjectWithEXIFRW2/relocate", rw2Input, true, rw2Magic, []byte("IPTC-PAYLOAD"), []byte(testXMPPacket), InjectWithEXIFRW2, InjectWithEXIFRW2Stream},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := tc.input()

			var legacyEXIF, streamEXIF *exif.EXIF
			if tc.mutate {
				// Independent parses: each function call gets its OWN *exif.EXIF,
				// so the in-place mutations relocateTIFFFromParsed performs on one
				// call can never leak into the other.
				legacyEXIF = mutatedEXIFForCompatTest(t, original, tc.acceptMagic)
				streamEXIF = mutatedEXIFForCompatTest(t, original, tc.acceptMagic)
			}

			var legacyOut bytes.Buffer
			if err := tc.legacyFn(original, legacyEXIF, tc.rawIPTC, tc.rawXMP, &legacyOut); err != nil {
				t.Fatalf("legacy function: %v", err)
			}

			var streamOut bytes.Buffer
			if err := tc.streamFn(bytes.NewReader(original), original, true, streamEXIF, tc.rawIPTC, tc.rawXMP, &streamOut); err != nil {
				t.Fatalf("stream function: %v", err)
			}

			if !bytes.Equal(legacyOut.Bytes(), streamOut.Bytes()) {
				t.Fatalf("legacy output (%d bytes) differs from stream output (%d bytes)",
					legacyOut.Len(), streamOut.Len())
			}
			if legacyOut.Len() == 0 {
				t.Fatal("both outputs are empty")
			}
		})
	}
}

// BenchmarkInjectWithEXIF and BenchmarkInjectWithEXIFStream measure the
// legacy wrapper's overhead over calling its streaming implementation
// directly: the wrapper adds exactly one bytes.NewReader construction (a
// small value that does not escape to the heap here, since it is passed by
// value through io.ReadSeeker without being retained beyond the call) around
// an otherwise identical call to InjectWithEXIFStream. modifiedEXIF is nil in
// both benchmarks so relocateTIFFFromParsed performs its own exif.Parse on
// every iteration — an identical, independent cost on both sides — isolating
// the wrapper's own overhead rather than measuring parse cost twice.
//
// Fixture: testdata/cramps.tif (see realfile_test.go for provenance).
func BenchmarkInjectWithEXIF(b *testing.B) {
	original, err := os.ReadFile(crampsFixture)
	if err != nil {
		b.Skipf("testdata/cramps.tif not present: %v", err)
	}
	rawIPTC := []byte("benchmark-iptc-payload")
	rawXMP := []byte(testXMPPacket)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var out bytes.Buffer
		if err := InjectWithEXIF(original, nil, rawIPTC, rawXMP, &out); err != nil {
			b.Fatalf("InjectWithEXIF: %v", err)
		}
	}
}

func BenchmarkInjectWithEXIFStream(b *testing.B) {
	original, err := os.ReadFile(crampsFixture)
	if err != nil {
		b.Skipf("testdata/cramps.tif not present: %v", err)
	}
	rawIPTC := []byte("benchmark-iptc-payload")
	rawXMP := []byte(testXMPPacket)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var out bytes.Buffer
		if err := InjectWithEXIFStream(bytes.NewReader(original), original, true, nil, rawIPTC, rawXMP, &out); err != nil {
			b.Fatalf("InjectWithEXIFStream: %v", err)
		}
	}
}
