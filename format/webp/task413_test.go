package webp

// task413_test.go — regression tests for task #413: a write that does not
// change the XMP copies the source's non-standard "XMP\0" chunk as it is,
// FourCC included; a write that changes it writes the standard "XMP " chunk
// (Google WebP Container Specification, "Metadata"; RFC 9649 §2.7.1.5).
//
// The VP8X XMP flag (RFC 9649 §2.7.1.5) is defined for the "XMP " chunk. A
// source that has a VP8X chunk keeps its flag when its "XMP\0" chunk is
// copied: the output states what the source stated. A source without VP8X
// gets a new one with the flag set for the XMP chunk the output holds.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func injectXMP(t *testing.T, src, exif, xmp []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, exif, nil, xmp, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return out.Bytes()
}

func vp8xFlagsOf(t *testing.T, cs []rawChunk) byte {
	t.Helper()
	for _, c := range cs {
		if c.id == "VP8X" {
			return c.data[0]
		}
	}
	t.Fatal("no VP8X chunk")
	return 0
}

// TestInject_XMPNulChunk (WEBP-XMP-03): the cases of #413.
func TestInject_XMPNulChunk(t *testing.T) {
	t.Parallel()
	oddXMP := []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"><odd/></x:xmpmeta>") // odd length: pad byte
	if len(oddXMP)%2 == 0 {
		oddXMP = append(oddXMP, ' ')
	}
	changed := []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"><b/></x:xmpmeta>")
	for _, xmp := range [][]byte{xmpPacket, oddXMP} {
		for _, srcFlags := range []byte{0x08, 0x0c} {
			src := webpOf(
				rawChunk{"VP8X", vp8xPayloadFlags(srcFlags)},
				rawChunk{"VP8 ", vp8Payload},
				rawChunk{"EXIF", tiffStream},
				rawChunk{"XMP\x00", xmp},
			)
			rawEXIF, rawXMP := extractAll(t, src)

			// Unchanged XMP: the chunk is copied as it is, with the source's flag.
			cs := chunksOf(t, injectXMP(t, src, rawEXIF, rawXMP))
			if countFourCC(cs, "XMP\x00") != 1 || countFourCC(cs, "XMP ") != 0 {
				t.Errorf("flags %#x len %d: unchanged XMP: want one 'XMP\\0' chunk and no 'XMP '", srcFlags, len(xmp))
			}
			for _, c := range cs {
				if c.id == "XMP\x00" && !bytes.Equal(c.data, xmp) {
					t.Errorf("unchanged XMP: the 'XMP\\0' payload differs from the source")
				}
			}
			if got := vp8xFlagsOf(t, cs); got&vp8xFlagXMP != srcFlags&vp8xFlagXMP || got&vp8xFlagEXIF == 0 {
				t.Errorf("unchanged XMP: VP8X flags %#x, source %#x", got, srcFlags)
			}

			// Changed XMP: the standard chunk, the flag set.
			cs = chunksOf(t, injectXMP(t, src, rawEXIF, changed))
			if countFourCC(cs, "XMP ") != 1 || countFourCC(cs, "XMP\x00") != 0 {
				t.Errorf("changed XMP: want one 'XMP ' chunk and no 'XMP\\0'")
			}
			if vp8xFlagsOf(t, cs)&vp8xFlagXMP == 0 {
				t.Errorf("changed XMP: VP8X XMP flag not set")
			}

			// Removed XMP: no XMP chunk, the flag cleared.
			cs = chunksOf(t, injectXMP(t, src, rawEXIF, nil))
			if countFourCC(cs, "XMP ")+countFourCC(cs, "XMP\x00") != 0 || vp8xFlagsOf(t, cs)&vp8xFlagXMP != 0 {
				t.Errorf("removed XMP: an XMP chunk or the flag survived")
			}
		}
	}

	t.Run("no VP8X in the source", func(t *testing.T) {
		t.Parallel()
		src := webpOf(rawChunk{"VP8 ", vp8Payload}, rawChunk{"XMP\x00", xmpPacket})
		cs := chunksOf(t, injectXMP(t, src, nil, xmpPacket))
		if countFourCC(cs, "XMP\x00") != 1 || vp8xFlagsOf(t, cs)&vp8xFlagXMP == 0 {
			t.Error("want the 'XMP\\0' chunk copied and a new VP8X with the XMP flag")
		}
	})
	t.Run("standard chunk present", func(t *testing.T) {
		t.Parallel()
		// The reader takes the 'XMP ' chunk: the output holds one 'XMP ' chunk.
		src := webpOf(rawChunk{"VP8 ", vp8Payload}, rawChunk{"XMP\x00", []byte("<other/>")}, rawChunk{"XMP ", xmpPacket})
		cs := chunksOf(t, injectXMP(t, src, nil, xmpPacket))
		if countFourCC(cs, "XMP ") != 1 || countFourCC(cs, "XMP\x00") != 0 {
			t.Error("want one 'XMP ' chunk and no 'XMP\\0'")
		}
	})
	t.Run("payload equals a later XMP\\0 chunk only", func(t *testing.T) {
		t.Parallel()
		// The reader keeps the first 'XMP\0' chunk; a payload equal to a later
		// one is a change.
		src := webpOf(rawChunk{"VP8 ", vp8Payload}, rawChunk{"XMP\x00", []byte("<one/>")}, rawChunk{"XMP\x00", []byte("<two/>")})
		cs := chunksOf(t, injectXMP(t, src, nil, []byte("<two/>")))
		if countFourCC(cs, "XMP ") != 1 || countFourCC(cs, "XMP\x00") != 0 {
			t.Error("want one 'XMP ' chunk and no 'XMP\\0'")
		}
	})
}

// TestCorpus_Issue473_Unchanged: the reproduction of task #413 on the corpus
// file, written with an EXIF change only: the 'XMP\0' chunk of the source
// (header, payload and pad) is in the output byte for byte.
func TestCorpus_Issue473_Unchanged(t *testing.T) {
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
	out := injectXMP(t, src, append(bytes.Clone(rawEXIF), 0, 0), rawXMP)
	var want rawChunk
	for _, c := range chunksOf(t, src) {
		if c.id == "XMP\x00" {
			want = c
		}
	}
	got := countFourCC(chunksOf(t, out), "XMP\x00")
	if got != 1 || want.data == nil {
		t.Fatalf("%d 'XMP\\0' chunks in the output, source holds %d bytes of them", got, len(want.data))
	}
	chunk := append(binary.LittleEndian.AppendUint32([]byte("XMP\x00"), uint32(len(want.data))), want.data...) //nolint:gosec // G115: test sizes are small
	if len(want.data)%2 == 1 {
		chunk = append(chunk, 0)
	}
	if !bytes.HasSuffix(out, chunk) {
		t.Error("the 'XMP\\0' chunk of the source is not in the output byte for byte")
	}
}
