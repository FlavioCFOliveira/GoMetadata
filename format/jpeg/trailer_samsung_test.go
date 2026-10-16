package jpeg

// trailer_samsung_test.go — task #353: a Samsung "Sound & Shot" trailer keeps
// the absolute offsets of its audio file valid when the trailer moves, and the
// other trailers that follow an AFCP trailer (CanonVRD and the like) stay
// readable. Layouts: see internal/trailer.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

var samsungAudio = []byte("RIFF-fake-audio-file-bytes") //nolint:gochecknoglobals // immutable test fixture

// samsungOf builds a Samsung trailer that starts at absolute offset start: the
// audio file, a SEFH/SEFT directory block, and the QDIO block with "BS". The
// absolute offsets of the audio file are written for that position.
func samsungOf(start int64, audio []byte) []byte {
	le := binary.LittleEndian
	var out []byte
	out = append(out, audio...)
	dir := []byte("SEFH")
	dir = le.AppendUint32(dir, 101)
	dir = le.AppendUint32(dir, 0)
	out = append(out, dir...)
	out = le.AppendUint32(out, uint32(len(dir))) //nolint:gosec // G115: test sizes are small
	out = append(out, "SEFT"...)
	out = append(out, "QDIO"...)
	out = le.AppendUint32(out, 101)
	out = le.AppendUint32(out, 1)
	out = le.AppendUint32(out, uint32(start))                   //nolint:gosec // G115: test sizes are small
	out = le.AppendUint32(out, uint32(start+int64(len(audio)))) //nolint:gosec // G115: test sizes are small
	out = le.AppendUint32(out, 20)
	out = append(out, "QDIO"...)
	return append(out, "BS"...)
}

// samsungAudioOffsets returns the absolute audio offsets that the QDIO block of
// data names, found by its "QDIOBS" end (the last one).
func samsungAudioOffsets(t *testing.T, data []byte) (start, end int64) {
	t.Helper()
	q := bytes.LastIndex(data, []byte("QDIOBS"))
	if q < 24 {
		t.Fatal("no QDIO block in the output")
	}
	le := binary.LittleEndian
	return int64(le.Uint32(data[q-12:])), int64(le.Uint32(data[q-8:]))
}

func TestTrailers_SamsungQDIO(t *testing.T) {
	t.Parallel()
	exif := []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00")
	probe := jpegWithTail(nil)
	vivo := []byte("streamdata-and-a-trailer-after-the-samsung-one")
	for _, tc := range []struct {
		name string
		tail func(base int64) []byte
	}{
		{"alone", func(base int64) []byte { return samsungOf(base, samsungAudio) }},
		{"followed by another trailer", func(base int64) []byte {
			return append(samsungOf(base, samsungAudio), vivo...)
		}},
		{"after an AFCP trailer", func(base int64) []byte {
			afcp := afcpOf(binary.BigEndian, "AXS!", base, [][2]string{{"IPTC", string(oldIIM)}})
			return append(afcp, samsungOf(base+int64(len(afcp)), samsungAudio)...)
		}},
		{"after unknown bytes", func(base int64) []byte {
			gap := []byte("CANON OPTIONAL DATA..")
			return append(gap, samsungOf(base+int64(len(gap)), samsungAudio)...)
		}},
	} {
		t.Run("TRAILER-SAMSUNG-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := jpegWithTail(tc.tail(int64(len(probe))))
			s0, e0 := samsungAudioOffsets(t, src)
			if !bytes.Equal(src[s0:e0], samsungAudio) {
				t.Fatal("fixture: the source offsets do not name the audio file")
			}
			// An EXIF payload and a new IIM move the trailer.
			out := injectTail(t, src, exif, newIIM)
			s, e := samsungAudioOffsets(t, out)
			if s == s0 {
				t.Fatal("the trailer did not move: the test proves nothing")
			}
			if e-s != int64(len(samsungAudio)) || !bytes.Equal(out[s:e], samsungAudio) {
				t.Errorf("QDIO offsets %d-%d name %q, want the audio file", s, e, out[s:e])
			}
			// Only the 8 offset bytes change: the QDIO block, with the
			// directory before it (offsets relative to it), is the source's.
			qs, qo := bytes.LastIndex(src, []byte("QDIOBS")), bytes.LastIndex(out, []byte("QDIOBS"))
			blockSrc, blockOut := bytes.Clone(src[qs-24-20:qs+6]), bytes.Clone(out[qo-24-20:qo+6])
			clear(blockSrc[20+12 : 20+20])
			clear(blockOut[20+12 : 20+20])
			if !bytes.Equal(blockSrc, blockOut) {
				t.Error("the Samsung trailer was altered beyond its audio offsets")
			}
		})
	}
}

// TestTrailers_SamsungUnchanged: a write that does not move the trailer
// leaves the file byte for byte, and a QDIO block that is not understood (its
// offsets lie outside the file, or it has no directory) is never touched.
func TestTrailers_SamsungUnchanged(t *testing.T) {
	t.Parallel()
	t.Run("TRAILER-SAMSUNG-02", func(t *testing.T) {
		t.Parallel()
		probe := jpegWithTail(nil)
		src := jpegWithTail(samsungOf(int64(len(probe)), samsungAudio))
		if out := injectTail(t, src, nil, nil); !bytes.Equal(out, src) {
			t.Error("a write that changes nothing altered the file")
		}
		exif := []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00")

		badOffsets := samsungOf(1<<30, samsungAudio) // the offsets name bytes beyond the file
		src = jpegWithTail(badOffsets)
		if out := injectTail(t, src, exif, nil); !bytes.HasSuffix(out, badOffsets) {
			t.Error("a QDIO block with out-of-range offsets was rewritten")
		}

		noDir := samsungOf(int64(len(probe)), samsungAudio)
		seft := bytes.Index(noDir, []byte("SEFT"))
		copy(noDir[seft:], "XXXX") // no directory block
		src = jpegWithTail(noDir)
		if out := injectTail(t, src, exif, nil); !bytes.HasSuffix(out, noDir) {
			t.Error("a QDIO block without a directory was rewritten")
		}
	})
}

// TestTrailers_CorpusSamsung runs the reproduction of task #353 on the corpus
// file ExifTool.jpg, whose trailer chain is AFCP, FotoStation (2), CanonVRD,
// PhotoMechanic, MIE, Samsung, Vivo. After a write that moves the chain, the
// Samsung audio offsets name the audio file again, every trailer after the
// AFCP and FotoStation trailers is byte for byte the source's, and the chain
// stays in the same order.
func TestTrailers_CorpusSamsung(t *testing.T) {
	t.Parallel()
	t.Run("TRAILER-SAMSUNG-03", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join("..", "..", "testdata", "corpus", "jpeg", "exiftool", "ExifTool.jpg")
		src, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
		}
		if err != nil {
			t.Fatal(err)
		}
		s0, e0 := samsungAudioOffsets(t, src)
		audio := bytes.Clone(src[s0:e0])
		if string(audio) != "<dummy wav file>" {
			t.Fatalf("the source offsets name %q, not the audio file", audio)
		}
		out := injectTail(t, src, []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00"), newIIM)
		s, e := samsungAudioOffsets(t, out)
		if s == s0 {
			t.Fatal("the chain did not move: the test proves nothing")
		}
		if !bytes.Equal(out[s:e], audio) {
			t.Errorf("QDIO offsets %d-%d name %q, want %q", s, e, out[s:e], audio)
		}
		// Everything from the CanonVRD trailer to the end of the file is
		// carried unchanged, except the 8 offset bytes of the QDIO block.
		vrd := []byte("CANON OPTIONAL DATA\x00")
		iSrc, iOut := bytes.Index(src, vrd), bytes.Index(out, vrd)
		if iSrc < 0 || iOut < 0 {
			t.Fatal("no CanonVRD trailer")
		}
		tailSrc, tailOut := bytes.Clone(src[iSrc:]), bytes.Clone(out[iOut:])
		if len(tailSrc) != len(tailOut) {
			t.Fatalf("the chain after AFCP and FotoStation changed size: %d -> %d", len(tailSrc), len(tailOut))
		}
		q := bytes.LastIndex(tailSrc, []byte("QDIOBS"))
		for _, b := range [][]byte{tailSrc, tailOut} {
			clear(b[q-12 : q-4])
		}
		if !bytes.Equal(tailSrc, tailOut) {
			t.Error("a trailer after the AFCP and FotoStation trailers was altered")
		}
	})
}

// qdioAudio returns the bytes that the last well-formed Samsung QDIO block of
// data (the layout internal/trailer documents, with its directory block and
// offsets inside the region after the image) names, or false when there is no
// such block. FuzzTrailers uses it to check that a write keeps them.
func qdioAudio(data []byte, base int64) ([]byte, bool) {
	le := binary.LittleEndian
	q := bytes.LastIndex(data, []byte("QDIOBS"))
	if q < 32 || int64(q) < base {
		return nil, false
	}
	d := q - 24 // the QDIO data
	if string(data[d:d+4]) != "QDIO" || le.Uint32(data[q-4:]) != 20 || string(data[d-4:d]) != "SEFT" {
		return nil, false
	}
	n := int(le.Uint32(data[d-8:]))
	if n < 12 || n > d-8 || string(data[d-8-n:d-8-n+4]) != "SEFH" {
		return nil, false
	}
	s, e := int64(le.Uint32(data[d+12:])), int64(le.Uint32(data[d+16:]))
	if s < base || e < s || e > int64(d) {
		return nil, false
	}
	return data[s:e], true
}
