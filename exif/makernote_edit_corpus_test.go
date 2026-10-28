package exif

// makernote_edit_corpus_test.go — tasks #398 and #399 over the real files of
// the corpus: every MakerNote that the editor accepts survives a delete and a
// size-changing edit followed by EncodeRebased, with every other tag
// unchanged.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// diffTop compares the entries of a MakerNote before and after an edit of tag
// (op: "delete", "grow" or "same"; raw is the value written) and returns the
// first difference from what the edit must have done.
func diffTop(before, after []topEntry, tag TagID, op string, raw []byte) string {
	var want []topEntry
	for _, te := range before {
		switch {
		case te.tag != tag:
			want = append(want, te)
		case op != "delete":
			te.val = raw
			te.cnt = uint32(len(raw)) / max(typeSize(te.typ), 1) //nolint:gosec // G115: test sizes are small
			want = append(want, te)
		}
	}
	if len(want) != len(after) {
		return "the number of tags changed"
	}
	for i := range want {
		a, b := want[i], after[i]
		if a.tag != b.tag || a.typ != b.typ || a.cnt != b.cnt || !bytes.Equal(a.val, b.val) {
			return "tag differs: " + hexTag(a.tag)
		}
	}
	return ""
}

func hexTag(t TagID) string {
	const digits = "0123456789abcdef"
	return string([]byte{'0', 'x', digits[t>>12&15], digits[t>>8&15], digits[t>>4&15], digits[t&15]})
}

// TestMakerNoteEdit_R399_Corpus: see the file comment. The census of the notes
// that are refused, by reason, is logged.
func TestMakerNoteEdit_R399_Corpus(t *testing.T) {
	t.Parallel()
	files := corpusFiles(t, ".jpg", ".jpeg", ".tif", ".tiff", ".nef", ".arw", ".cr2", ".dng", ".orf", ".rw2")
	if len(files) == 0 {
		t.Skip("corpus absent (see testdata/download.sh)")
	}
	type tally struct{ editable, refused int }
	census := map[string]*tally{}
	reasons := map[string]int{}
	growRefused := map[string]int{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil || len(data) > 32<<20 {
			continue
		}
		e, err := corpusEXIF(data)
		if err != nil || e == nil || e.MakerNoteIFD == nil || len(e.MakerNoteIFD.Entries) < 2 {
			continue
		}
		off := 0
		if i := bytes.Index(data[:min(len(data), 1<<16)], []byte("Exif\x00\x00")); i >= 0 {
			off = i + 6
		}
		src := data[off:]
		name := "?"
		if l, ok := e.MakerNoteLayout(); ok {
			name = mnLayouts[l.layout].name
		}
		if census[name] == nil {
			census[name] = &tally{}
		}
		if _, err := newMakerNoteEdit(e); err != nil {
			census[name].refused++
			reasons[name+": "+err.Error()]++
			continue
		}
		census[name].editable++
		rel, _ := filepath.Rel(filepath.Join("..", "testdata", "corpus"), f)

		// The victim: a text or opaque out-of-line tag, else the first. The values
		// are those the editor reads, which follow the offset base the data show
		// (the IFD that Parse builds for a few layouts reads against the base of
		// the layout table instead).
		m0, err := newMakerNoteEdit(e)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := m0.topEntries(m0.orig)
		victim := before[0]
		for _, te := range before {
			if len(te.val) > 4 && (te.typ == TypeASCII || te.typ == TypeUndefined || te.typ == TypeByte) {
				victim = te
				break
			}
		}
		tag := victim.tag

		for _, op := range []string{"delete", "grow", "same"} {
			e2, err := corpusEXIF(data)
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			switch op {
			case "delete":
				err = e2.DeleteMakerNoteTag(tag)
			case "grow":
				var v any
				v, raw = editValue(victim.typ, victim.val, 16)
				err = e2.SetMakerNoteTag(tag, v)
			case "same":
				var v any
				v, raw = editValue(victim.typ, victim.val, 0)
				err = e2.SetMakerNoteTag(tag, v, FixedSize())
			}
			if err != nil {
				switch {
				case errors.Is(err, ErrMakerNoteValueType):
					continue // a type the test cannot encode
				case op != "same" && errors.Is(err, ErrMakerNoteNotEditable):
					// The offset base of the note is ambiguous: its layout table and its
					// data disagree, and the edit would make the writer pick the other
					// base. A replacement of the same size never does.
					growRefused[name]++
					continue
				}
				t.Errorf("%s %s tag %s: %v", rel, op, hexTag(tag), err)
				continue
			}
			out, err := EncodeRebased(e2, src)
			if err != nil {
				t.Errorf("%s %s: EncodeRebased: %v", rel, op, err)
				continue
			}
			e3, err := Parse(out)
			if err != nil {
				t.Errorf("%s %s: the encoded stream does not parse: %v", rel, op, err)
				continue
			}
			// The edited note must be one the editor accepts again, and its
			// entries, as the editor reads them, must be those of the source.
			m3, err := newMakerNoteEdit(e3)
			if err != nil {
				t.Errorf("%s %s: the encoded MakerNote is not editable: %v", rel, op, err)
				continue
			}
			got, _ := m3.topEntries(m3.buf)
			if d := diffTop(before, got, tag, op, raw); d != "" {
				t.Errorf("%s %s: %s", rel, op, d)
			}
		}
	}
	names := make([]string, 0, len(census))
	for n := range census {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Logf("%-14s editable %3d, refused %3d", n, census[n].editable, census[n].refused)
	}
	for n, c := range growRefused {
		t.Logf("%-14s an edit that changes the tags or sizes is refused for %d files (ambiguous offset base)", n, c)
	}
	keys := make([]string, 0, len(reasons))
	for r := range reasons {
		keys = append(keys, r)
	}
	sort.Strings(keys)
	for _, r := range keys {
		t.Logf("refused x%d  %s", reasons[r], r)
	}
}

// editValue returns a value for an entry of type typ whose current bytes are
// cur, with extra more elements, and the bytes SetMakerNoteTag must store for
// it: text as a string (NUL-free, then NUL-terminated), anything else as raw
// bytes with the last byte changed.
func editValue(typ DataType, cur []byte, extra int) (value any, want []byte) {
	if typ == TypeASCII || typ == TypeUTF8 {
		b := bytes.Clone(cur[:max(len(cur)-1, 0)])
		for i, c := range b {
			if c == 0 {
				b[i] = 'x'
			}
		}
		b = append(b, bytes.Repeat([]byte{'y'}, extra)...)
		return string(b), append(b, 0)
	}
	raw := append(bytes.Clone(cur), bytes.Repeat([]byte{0x5A}, extra*int(typeSize(typ)))...)
	if len(raw) > 0 {
		raw[len(raw)-1] ^= 0x01
	}
	return raw, raw
}
