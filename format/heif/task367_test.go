package heif

import (
	"bytes"
	"testing"
)

// Task #367.
//
// Inject used to append the new 'mdat' box after every byte of the source. When
// the top-level boxes of the source end in bytes that do not parse as a box,
// the appended box header completed a bogus file-level 'meta' header that
// reached into the new box and hid the real, moov-nested one: Extract read
// nothing. Inject now writes the new box where the last real box ends and keeps
// the trailing bytes, byte for byte, after it (ISO/IEC 14496-12 §4.2).
//
// Reference for the reader side: libavif src/read.c avifParse (a top-level
// header that does not parse is a parse failure, BMFF_PARSE_FAILED, after every
// earlier box was handled) and heic-rs src/boxes.rs BoxIter (the iterator yields
// the earlier boxes, then an error). Extract is more tolerant by design: it
// stops at the first bytes that are not a box and keeps what it found.

// task367Trailers returns tails that do not parse as a box on their own, the
// first of which turns into a 'meta' box when a box is appended after it.
func task367Trailers() map[string][]byte {
	return map[string][]byte{
		// size larger than what follows + type 'meta': a header, not a box.
		"bogus meta header": {0x00, 0x00, 0x00, 0x20, 'm', 'e', 't', 'a'},
		"short tail":        {0xAA, 0xBB, 0xCC},
		"size below header": {0x00, 0x00, 0x00, 0x04, 'f', 'r', 'e', 'e'},
		"large size":        {0x00, 0x00, 0x00, 0x01, 'm', 'e', 't', 'a', 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
	}
}

// TestTask367TrailingBytesKept: the metadata written into a file that ends in
// non-box bytes reads back, and the trailing bytes are the last bytes of the
// output, unchanged. HEIF-TRAILING-BYTES-01.
func TestTask367TrailingBytesKept(t *testing.T) {
	t.Parallel()
	for name, tail := range task367Trailers() {
		for _, inMoov := range []bool{false, true} {
			t.Run("HEIF-TRAILING-BYTES-01/"+name, func(t *testing.T) {
				t.Parallel()
				src := buildTask334File(task334Layout{withItems: true, inMoov: inMoov, offsetSize: 4, lengthSize: 4})
				if closeOpenEndedBox(append([]byte(nil), src...)) != len(src) {
					t.Fatal("fixture source is not a clean box sequence")
				}
				src = append(src, tail...)
				newEXIF := conformanceMinimalTIFF()
				newXMP := []byte("<new/>")
				out := injectBytes(t, src, newEXIF, newXMP)

				if !bytes.HasSuffix(out, tail) {
					t.Fatalf("output does not end with the trailing bytes %x: tail %x", tail, out[len(out)-len(tail):])
				}
				gotEXIF, _, gotXMP, err := Extract(bytes.NewReader(out))
				if err != nil {
					t.Fatalf("Extract: %v", err)
				}
				if !bytes.Equal(gotEXIF, newEXIF) {
					t.Errorf("EXIF read back %x, want %x", gotEXIF, newEXIF)
				}
				if !bytes.Equal(gotXMP, newXMP) {
					t.Errorf("XMP read back %q, want %q", gotXMP, newXMP)
				}
				// The new item payloads are inside a box: the box sequence of the
				// output ends where the trailing bytes start.
				if end := closeOpenEndedBox(append([]byte(nil), out...)); end != len(out)-len(tail) {
					t.Errorf("top-level boxes end at %d, want %d", end, len(out)-len(tail))
				}
			})
		}
	}
}

// TestTask367NoTrailingBytesUnchanged: a clean source keeps the layout of
// before the fix (the new box is the last bytes of the file).
func TestTask367NoTrailingBytesUnchanged(t *testing.T) {
	t.Parallel()
	t.Run("HEIF-TRAILING-BYTES-01/clean source", func(t *testing.T) {
		t.Parallel()
		src := buildTask334File(task334Layout{withItems: true, offsetSize: 4, lengthSize: 4})
		out := injectBytes(t, src, conformanceMinimalTIFF(), []byte("<new/>"))
		if end := closeOpenEndedBox(append([]byte(nil), out...)); end != len(out) {
			t.Fatalf("top-level boxes end at %d of %d", end, len(out))
		}
		if !bytes.HasSuffix(out, []byte("<new/>")) {
			t.Error("the new payloads are not at the end of the file")
		}
	})
}
