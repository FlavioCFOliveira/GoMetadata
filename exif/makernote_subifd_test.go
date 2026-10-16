package exif

// makernote_subifd_test.go — task #370, rule R-36: RebaseMakerNoteSubIFDs.

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// olympSubIFDStream builds an "OLYMP\0" TIFF whose tag 0x2020 value is an IFD
// of one entry with a text addressed from the TIFF header (see
// format/tiff.olympSubIFDFile), and the position of the pointer field.
func olympSubIFDStream(prefix string, tiffRel bool) (stream []byte, ptrField int) {
	le := binary.LittleEndian
	build := func(ptr uint32) ([]byte, testutil.MNLayout) {
		v := le.AppendUint16(nil, 1)
		v = le.AppendUint16(v, 0x0003)
		v = le.AppendUint16(v, 2)
		v = le.AppendUint32(v, 6)
		v = le.AppendUint32(v, ptr)
		v = le.AppendUint32(v, 0)
		v = append(v, "SENTRY"...)
		return testutil.MakerNoteTIFF(testutil.MNSpec{
			Make: "OLYMPUS OPTICAL CO.,LTD", Order: le, Prefix: []byte(prefix), TIFFRelative: tiffRel, PadBefore: 6,
			Entries: []testutil.MNEntry{{Tag: 0x2020, Type: 7, Count: uint32(len(v)), Value: v}}, //nolint:gosec // G115: tiny test sizes
		})
	}
	_, lay := build(0)
	stream, _ = build(uint32(lay.Values[0] + 18)) //nolint:gosec // G115: tiny test offsets
	return stream, lay.Values[0] + 2 + 8
}

// TestRebaseMakerNoteSubIFDs_R36 covers what the function sets and what it
// leaves: the pointer of a sub-IFD value of an "OLYMP\0" note follows the note;
// other layouts, other streams, and a note that is not where it is said to be are
// left alone.
func TestRebaseMakerNoteSubIFDs_R36(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	t.Run("R-36/OLYMP pointer follows the note", func(t *testing.T) {
		t.Parallel()
		src, field := olympSubIFDStream("OLYMP\x00\x01\x00", true)
		e, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		const moved = 100
		out := append(make([]byte, moved), src...) // the stream is shifted: the note is at mnOff + moved
		mnAt := int(e.MakerNoteOffset) + moved
		copy(out[moved:], src)
		if !e.RebaseMakerNoteSubIFDs(out, mnAt) {
			t.Fatal("RebaseMakerNoteSubIFDs reported no pointer set")
		}
		want := le.Uint32(src[field:]) + moved
		if got := le.Uint32(out[moved+field:]); got != want {
			t.Errorf("pointer = %d, want %d", got, want)
		}
		if e.MakerNote[0] != 'O' || !bytes.Equal(e.MakerNote, src[e.MakerNoteOffset:int(e.MakerNoteOffset)+len(e.MakerNote)]) {
			t.Error("e was modified")
		}
	})
	t.Run("R-36/left alone", func(t *testing.T) {
		t.Parallel()
		olymp, _ := olympSubIFDStream("OLYMP\x00\x01\x00", true)
		eOlymp, err := Parse(olymp)
		if err != nil {
			t.Fatal(err)
		}
		other, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
			Make: "Canon", Order: le, TIFFRelative: true, PadBefore: 6,
			Entries: []testutil.MNEntry{{Tag: 0x0004, Type: 4, Count: 3, Value: []byte{1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0}}},
		})
		eOther, err := Parse(other)
		if err != nil {
			t.Fatal(err)
		}
		var nilEXIF *EXIF
		for _, tc := range []struct {
			name   string
			e      *EXIF
			stream []byte
			mnAt   int
		}{
			{"nil EXIF", nilEXIF, olymp, 10},
			{"another maker", eOther, other, int(eOther.MakerNoteOffset)},
			{"note beyond the stream", eOlymp, olymp, len(olymp) - 4},
			{"negative position", eOlymp, olymp, -1},
		} {
			before := bytes.Clone(tc.stream)
			if tc.e.RebaseMakerNoteSubIFDs(tc.stream, tc.mnAt) {
				t.Errorf("%s: reported a pointer set", tc.name)
			}
			if !bytes.Equal(before, tc.stream) {
				t.Errorf("%s: the stream was modified", tc.name)
			}
		}
	})
}
