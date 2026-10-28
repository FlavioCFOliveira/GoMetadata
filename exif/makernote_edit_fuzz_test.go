package exif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// FuzzMakerNoteEdit (tasks #398, #399): an edit of the MakerNote of any stream
// never panics and never writes into the stream it was parsed from; a refused
// edit leaves the EXIF as it was; an accepted one encodes, and the entries a
// reader finds in the encoded stream are those the edit promised.
func FuzzMakerNoteEdit(f *testing.F) {
	cases := mnCases()
	for i := range cases {
		tiff, _ := testutil.MakerNoteTIFF(cases[i].spec)
		f.Add(tiff, uint8(0), uint8(0), uint8(3))
		f.Add(tiff, uint8(1), uint8(1), uint8(40))
		f.Add(tiff, uint8(2), uint8(2), uint8(1))
	}
	shifted, _ := testutil.MakerNoteTIFF(canonSpec(binary.LittleEndian, 164))
	f.Add(shifted, uint8(1), uint8(1), uint8(30))
	// Real MakerNotes, when the corpus is there: Canon with a shifted base, Nikon 3,
	// Sony without a next-IFD offset, Panasonic, Olympus, Fujifilm.
	for _, rel := range []string{
		"jpeg/metadata-extractor/Canon EOS REBEL T3i.jpg", "jpeg/metadata-extractor/Nikon D300.jpg",
		"jpeg/metadata-extractor/Sony DSC-H9.jpg", "jpeg/metadata-extractor/Panasonic DMC-FZ18.jpg",
		"jpeg/exiftool/Olympus.jpg", "jpeg/exiftool/FujiFilm.jpg",
	} {
		if data, err := os.ReadFile(filepath.Join("..", "testdata", "corpus", filepath.FromSlash(rel))); err == nil {
			if i := bytes.Index(data[:min(len(data), 1<<16)], []byte("Exif\x00\x00")); i >= 0 {
				f.Add(data[i+6:], uint8(1), uint8(1), uint8(30))
				f.Add(data[i+6:], uint8(3), uint8(0), uint8(0))
			}
		}
	}
	f.Fuzz(func(t *testing.T, tiff []byte, sel, op, size uint8) {
		src := bytes.Clone(tiff)
		e, err := Parse(tiff)
		if err != nil || e == nil || e.MakerNoteIFD == nil {
			return
		}
		m, err := newMakerNoteEdit(e)
		if err != nil {
			if !errors.Is(err, ErrMakerNoteNotEditable) && !errors.Is(err, ErrNoMakerNote) {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		want, ok := m.topEntries(m.orig)
		if !ok || len(want) == 0 {
			return
		}
		victim := want[int(sel)%len(want)]
		before := bytes.Clone(e.MakerNote)
		nEntries := len(e.MakerNoteIFD.Entries)

		var editErr error
		var promised []topEntry
		switch op % 3 {
		case 0:
			editErr = e.DeleteMakerNoteTag(victim.tag)
			for _, te := range want {
				if te.tag != victim.tag {
					promised = append(promised, te)
				}
			}
		default:
			var value any
			var raw []byte
			if victim.typ == TypeASCII || victim.typ == TypeUTF8 {
				s := string(bytes.Repeat([]byte{'x'}, int(size)))
				value, raw = s, append([]byte(s), 0)
			} else {
				es := int(typeSize(victim.typ))
				if es == 0 {
					return
				}
				raw = bytes.Repeat([]byte{0x5A}, es*(int(size)%64+1))
				value = raw
			}
			var opts []MakerNoteEditOption
			if op%3 == 2 {
				opts = append(opts, FixedSize())
			}
			editErr = e.SetMakerNoteTag(victim.tag, value, opts...)
			for _, te := range want {
				if te.tag == victim.tag {
					te.val = raw
					te.cnt = uint32(len(raw) / max(int(typeSize(te.typ)), 1)) //nolint:gosec // G115: bounded by the fuzz sizes
				}
				promised = append(promised, te)
			}
		}
		if !bytes.Equal(tiff, src) {
			t.Fatal("the edit wrote into the stream given to Parse")
		}
		if editErr != nil {
			if !bytes.Equal(before, e.MakerNote) || len(e.MakerNoteIFD.Entries) != nEntries {
				t.Fatalf("the EXIF changed although the edit failed (%v)", editErr)
			}
			return
		}
		out, err := EncodeRebased(e, tiff)
		if err != nil {
			t.Fatalf("EncodeRebased after an accepted edit: %v", err)
		}
		e2, err := Parse(out)
		if err != nil {
			t.Fatalf("the encoded stream does not parse: %v", err)
		}
		m2, err := newMakerNoteEdit(e2)
		if err != nil {
			t.Fatalf("the encoded MakerNote is not editable: %v", err)
		}
		got, _ := m2.topEntries(m2.orig)
		if len(got) != len(promised) {
			t.Fatalf("%d entries in the output, the edit promised %d", len(got), len(promised))
		}
		for i := range got {
			if got[i].tag != promised[i].tag || got[i].typ != promised[i].typ || got[i].cnt != promised[i].cnt || !bytes.Equal(got[i].val, promised[i].val) {
				t.Fatalf("entry %d (tag %#04x) differs from the promise", i, uint16(promised[i].tag))
			}
		}
	})
}
