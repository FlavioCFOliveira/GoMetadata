package exif

// makernote_edit_test.go — tasks #398 and #399: delete, same-size and
// size-changing edits of MakerNote values, for every synthetic layout of
// makernote_rebase_test.go, and the stream they encode to.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

const longSerial = "SERIAL-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// editedValues reads back the three entries of mnEntries from the MakerNote IFD
// that Parse builds for the encoded stream out, and from the independent
// decoder. A missing entry is absent from the map.
func editedValues(t *testing.T, out []byte, tc mnCase) map[uint16][]byte {
	t.Helper()
	e, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(encoded): %v", err)
	}
	l, ok := e.MakerNoteLayout()
	if !ok {
		t.Fatal("encoded MakerNote: layout not detected")
	}
	mnAbs := makerNotePosition(t, out)
	dec := decodeMN(t, out, mnAbs, l.IFDOffset, l.ByteOrder, l.TIFFRelative, l.Base)
	if e.MakerNoteIFD == nil {
		t.Fatal("encoded MakerNote: not parsed by Parse")
	}
	// Parse and the independent decoder must agree on every entry.
	for tag, want := range dec {
		got := e.MakerNoteIFD.Get(TagID(tag))
		if got == nil || !bytes.Equal(got.Value, want) {
			t.Errorf("%s: tag %#04x: Parse reads %v, the decoder %x", tc.name, tag, got, want)
		}
	}
	for _, ent := range e.MakerNoteIFD.Entries {
		if _, ok := dec[uint16(ent.Tag)]; !ok {
			t.Errorf("%s: tag %#04x: Parse reads an entry the decoder does not", tc.name, uint16(ent.Tag))
		}
	}
	return dec
}

// TestMakerNoteEdit_R398_DeleteSameSizeAndResize: for every layout, an edit of
// the MakerNote followed by EncodeRebased reads back as the edit says, and the
// untouched entries keep their values.
func TestMakerNoteEdit_R398_DeleteSameSizeAndResize(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	for _, tc := range mnCases() {
		if strings.Contains(tc.name, "outside the note") {
			continue // refused, see TestMakerNoteEdit_R399_NotEditable
		}
		type edit struct {
			name string
			do   func(e *EXIF) error
			want map[uint16][]byte // entries that differ from the source; nil value = deleted
		}
		rat16 := append(mnRat(), mnRat()...)
		edits := []edit{
			{"delete out-of-line", func(e *EXIF) error { return e.DeleteMakerNoteTag(0x0002) }, map[uint16][]byte{2: nil}},
			{"delete inline", func(e *EXIF) error { return e.DeleteMakerNoteTag(0x0001) }, map[uint16][]byte{1: nil}},
			{"delete last in the table", func(e *EXIF) error { return e.DeleteMakerNoteTag(0x0003) }, map[uint16][]byte{3: nil}},
			{"same size text", func(e *EXIF) error { return e.SetMakerNoteTag(0x0002, "SERIAL-9876543", FixedSize()) }, map[uint16][]byte{2: []byte("SERIAL-9876543\x00")}},
			{"same size raw", func(e *EXIF) error { return e.SetMakerNoteTag(0x0003, []byte{9, 0, 0, 0, 8, 0, 0, 0}, FixedSize()) }, map[uint16][]byte{3: {9, 0, 0, 0, 8, 0, 0, 0}}},
			{"same size inline", func(e *EXIF) error { return e.SetMakerNoteTag(0x0001, uint16(0x1234), FixedSize()) }, map[uint16][]byte{1: {0x34, 0x12}}},
			{"grow text", func(e *EXIF) error { return e.SetMakerNoteTag(0x0002, longSerial) }, map[uint16][]byte{2: []byte(longSerial + "\x00")}},
			{"shrink text in place", func(e *EXIF) error { return e.SetMakerNoteTag(0x0002, "SHORT-12") }, map[uint16][]byte{2: []byte("SHORT-12\x00")}},
			{"shrink text to inline", func(e *EXIF) error { return e.SetMakerNoteTag(0x0002, "ab") }, map[uint16][]byte{2: []byte("ab\x00")}},
			{"grow raw", func(e *EXIF) error { return e.SetMakerNoteTag(0x0003, rat16) }, map[uint16][]byte{3: rat16}},
			{"grow inline to out-of-line", func(e *EXIF) error { return e.SetMakerNoteTag(0x0001, []uint16{1, 2, 3}) }, map[uint16][]byte{1: {1, 0, 2, 0, 3, 0}}},
		}
		for _, ed := range edits {
			t.Run(tc.name+"/"+ed.name, func(t *testing.T) {
				t.Parallel()
				tiff, lay := testutil.MakerNoteTIFF(tc.spec)
				orig := bytes.Clone(tiff)
				e, err := Parse(tiff)
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				before := bytes.Clone(e.MakerNote)
				if err := ed.do(e); err != nil {
					t.Fatalf("edit: %v", err)
				}
				if !bytes.Equal(tiff, orig) {
					t.Fatal("the edit wrote into the buffer given to Parse")
				}
				if bytes.Equal(before, e.MakerNote) {
					t.Fatal("the MakerNote is unchanged")
				}
				if got := e.ExifIFD.Get(TagMakerNote); got == nil || !bytes.Equal(got.Value, e.MakerNote) || int(got.Count) != len(e.MakerNote) {
					t.Fatal("the MakerNote entry of the Exif IFD and EXIF.MakerNote differ")
				}
				// The parsed IFD of e follows the edit.
				for tag, w := range ed.want {
					got := e.MakerNoteIFD.Get(TagID(tag))
					if (w == nil) != (got == nil) || (w != nil && !bytes.Equal(got.Value, w)) {
						t.Errorf("EXIF.MakerNoteIFD tag %#04x = %v, want %x", tag, got, w)
					}
				}

				out, err := EncodeRebased(e, tiff)
				if err != nil {
					t.Fatalf("EncodeRebased: %v", err)
				}
				got := editedValues(t, out, tc)
				src := testutil.DecodeMN(orig, lay.MNOffset, len(tc.spec.Prefix), tc.spec.Order, tc.spec.TIFFRelative, tc.spec.Base)
				for tag, v := range src {
					w, edited := ed.want[tag]
					if !edited {
						w = v
					}
					if !bytes.Equal(got[tag], w) {
						t.Errorf("tag %#04x = %x, want %x", tag, got[tag], w)
					}
					if edited && w == nil {
						if _, present := got[tag]; present {
							t.Errorf("deleted tag %#04x is still in the MakerNote", tag)
						}
					}
				}
				// A deleted value does not stay in the file.
				if ed.name == "delete out-of-line" && bytes.Contains(out, []byte("SERIAL-0123456")) {
					t.Error("the deleted value is still in the encoded stream")
				}
				// The edit is stable: edited, encoded, parsed and edited again.
				e2, err := Parse(out)
				if err != nil {
					t.Fatal(err)
				}
				if err := e2.SetMakerNoteTag(0x0001, uint16(77)); err != nil && !errors.Is(err, ErrMakerNoteTagNotFound) {
					t.Fatalf("second edit: %v", err)
				}
				_ = le
			})
		}
	}
}

// TestMakerNoteEdit_R398_Errors: the sentinel errors, and e unchanged on every
// error.
func TestMakerNoteEdit_R398_Errors(t *testing.T) {
	t.Parallel()
	tc := mnCases()[0] // Canon plain IFD with footer
	tests := []struct {
		name string
		do   func(e *EXIF) error
		want error
	}{
		{"delete missing tag", func(e *EXIF) error { return e.DeleteMakerNoteTag(0x7777) }, ErrMakerNoteTagNotFound},
		{"set missing tag", func(e *EXIF) error { return e.SetMakerNoteTag(0x7777, "x") }, ErrMakerNoteTagNotFound},
		{"fixed size, larger", func(e *EXIF) error { return e.SetMakerNoteTag(0x0002, longSerial, FixedSize()) }, ErrMakerNoteSizeChange},
		{"fixed size, smaller", func(e *EXIF) error { return e.SetMakerNoteTag(0x0002, "ab", FixedSize()) }, ErrMakerNoteSizeChange},
		{"string for a number", func(e *EXIF) error { return e.SetMakerNoteTag(0x0001, "abc") }, ErrMakerNoteValueType},
		{"number for a string", func(e *EXIF) error { return e.SetMakerNoteTag(0x0002, 5) }, ErrMakerNoteValueType},
		{"out of range", func(e *EXIF) error { return e.SetMakerNoteTag(0x0001, 70000) }, ErrMakerNoteValueType},
		{"negative for unsigned", func(e *EXIF) error { return e.SetMakerNoteTag(0x0001, -1) }, ErrMakerNoteValueType},
		{"NUL in text", func(e *EXIF) error { return e.SetMakerNoteTag(0x0002, "a\x00b") }, ErrMakerNoteValueType},
		{"raw not a multiple", func(e *EXIF) error { return e.SetMakerNoteTag(0x0003, []byte{1, 2, 3}) }, ErrMakerNoteValueType},
		{"raw empty", func(e *EXIF) error { return e.SetMakerNoteTag(0x0003, []byte{}) }, ErrMakerNoteValueType},
		{"raw for text", func(e *EXIF) error { return e.SetMakerNoteTag(0x0002, []byte("abc\x00")) }, ErrMakerNoteValueType},
		{"unsupported type", func(e *EXIF) error { return e.SetMakerNoteTag(0x0001, 1.5) }, ErrMakerNoteValueType},
		{"nil EXIF", func(*EXIF) error { var e *EXIF; return e.DeleteMakerNoteTag(1) }, ErrNilEXIF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tiff, _ := testutil.MakerNoteTIFF(tc.spec)
			e, err := Parse(tiff)
			if err != nil {
				t.Fatal(err)
			}
			before := bytes.Clone(e.MakerNote)
			entries := len(e.MakerNoteIFD.Entries)
			if err := tt.do(e); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if !bytes.Equal(before, e.MakerNote) || len(e.MakerNoteIFD.Entries) != entries {
				t.Error("the EXIF changed although the edit failed")
			}
		})
	}
	t.Run("no MakerNote", func(t *testing.T) {
		t.Parallel()
		e := New(true)
		if err := e.DeleteMakerNoteTag(1); !errors.Is(err, ErrNoMakerNote) {
			t.Errorf("error = %v, want ErrNoMakerNote", err)
		}
		if _, err := e.MakerNoteTagIDs("x"); !errors.Is(err, ErrNoMakerNote) {
			t.Errorf("MakerNoteTagIDs error = %v, want ErrNoMakerNote", err)
		}
	})
	t.Run("last entry", func(t *testing.T) {
		t.Parallel()
		spec := tc.spec
		spec.Entries = mnEntries()[:1]
		tiff, _ := testutil.MakerNoteTIFF(spec)
		e, err := Parse(tiff)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.DeleteMakerNoteTag(0x0001); !errors.Is(err, ErrMakerNoteNotEditable) {
			t.Errorf("error = %v, want ErrMakerNoteNotEditable", err)
		}
	})
}

// TestMakerNoteEdit_R399_NotEditable: a note that refers to data outside its
// own bytes, one whose layout is unknown, and one whose position is unknown are
// refused, and left as they were.
func TestMakerNoteEdit_R399_NotEditable(t *testing.T) {
	t.Parallel()
	cases := mnCases()
	var outside mnCase
	for _, tc := range cases {
		if strings.Contains(tc.name, "outside the note") {
			outside = tc
		}
	}
	t.Run("value outside the note", func(t *testing.T) {
		t.Parallel()
		tiff, _ := testutil.MakerNoteTIFF(outside.spec)
		e, err := Parse(tiff)
		if err != nil {
			t.Fatal(err)
		}
		for _, do := range []func() error{
			func() error { return e.DeleteMakerNoteTag(0x0001) },
			func() error { return e.SetMakerNoteTag(0x0001, uint16(3)) },
		} {
			if err := do(); !errors.Is(err, ErrMakerNoteNotEditable) {
				t.Errorf("error = %v, want ErrMakerNoteNotEditable", err)
			}
		}
	})
	t.Run("layout not known", func(t *testing.T) {
		t.Parallel()
		spec := testutil.MNSpec{Make: "Unknown Maker", Order: binary.LittleEndian, TIFFRelative: true, Entries: mnEntries()}
		tiff, _ := testutil.MakerNoteTIFF(spec)
		e, err := Parse(tiff)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.DeleteMakerNoteTag(0x0001); !errors.Is(err, ErrNoMakerNote) && !errors.Is(err, ErrMakerNoteNotEditable) {
			t.Errorf("error = %v, want ErrNoMakerNote or ErrMakerNoteNotEditable", err)
		}
	})
	t.Run("source position unknown", func(t *testing.T) {
		t.Parallel()
		tiff, _ := testutil.MakerNoteTIFF(cases[0].spec)
		e, err := Parse(tiff)
		if err != nil {
			t.Fatal(err)
		}
		e.MakerNoteOffset, e.MakerNoteOffset64 = 0, 0
		if err := e.DeleteMakerNoteTag(0x0002); !errors.Is(err, ErrMakerNoteNotEditable) {
			t.Errorf("error = %v, want ErrMakerNoteNotEditable", err)
		}
	})
	t.Run("BigTIFF", func(t *testing.T) {
		t.Parallel()
		tiff, _ := testutil.MakerNoteTIFF(cases[0].spec)
		e, err := Parse(tiff)
		if err != nil {
			t.Fatal(err)
		}
		e.BigTIFF = true
		if err := e.DeleteMakerNoteTag(0x0002); !errors.Is(err, ErrMakerNoteNotEditable) {
			t.Errorf("error = %v, want ErrMakerNoteNotEditable", err)
		}
	})
}

// TestMakerNoteEdit_R399_ShiftedCanonBase: a Canon note whose base is shifted
// (task #407) is edited with the corrected base: every untouched value and
// the edited one read as ExifTool reads them after EncodeRebased.
func TestMakerNoteEdit_R399_ShiftedCanonBase(t *testing.T) {
	t.Parallel()
	for _, shift := range []int{0, 164, -4} {
		tiff, _ := testutil.MakerNoteTIFF(canonSpec(binary.LittleEndian, shift))
		e, err := Parse(tiff)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.SetMakerNoteTag(0x0002, longSerial); err != nil {
			t.Fatalf("shift %d: SetMakerNoteTag: %v", shift, err)
		}
		if err := e.DeleteMakerNoteTag(0x0003); err != nil {
			t.Fatalf("shift %d: DeleteMakerNoteTag: %v", shift, err)
		}
		out, err := EncodeRebased(e, tiff)
		if err != nil {
			t.Fatal(err)
		}
		e2, err := Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		if got := e2.MakerNoteIFD.Get(0x0002); got == nil || string(got.Value) != longSerial+"\x00" {
			t.Errorf("shift %d: tag 2 = %v", shift, got)
		}
		if e2.MakerNoteIFD.Get(0x0003) != nil {
			t.Errorf("shift %d: the deleted tag 3 is still there", shift)
		}
		if got := e2.MakerNoteIFD.Get(0x0001); got == nil || got.Count != 1 {
			t.Errorf("shift %d: tag 1 = %v", shift, got)
		}
	}
}

// TestMakerNoteEdit_R399_Idempotent: the same edit twice gives the same note.
func TestMakerNoteEdit_R399_Idempotent(t *testing.T) {
	t.Parallel()
	tiff, _ := testutil.MakerNoteTIFF(mnCases()[0].spec)
	e, err := Parse(tiff)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetMakerNoteTag(0x0002, longSerial); err != nil {
		t.Fatal(err)
	}
	once := bytes.Clone(e.MakerNote)
	if err := e.SetMakerNoteTag(0x0002, longSerial); err != nil {
		t.Fatal(err)
	}
	// The second edit has the size of the first: it writes in place.
	if !bytes.Equal(once, e.MakerNote) {
		t.Error("the second identical edit changed the MakerNote")
	}
}

// TestMakerNoteEdit_R398_TagIDs: names and hexadecimal IDs resolve to the tags
// the MakerNote has.
func TestMakerNoteEdit_R398_TagIDs(t *testing.T) {
	t.Parallel()
	spec := testutil.MNSpec{Make: "Canon", Order: binary.LittleEndian, TIFFRelative: true, PadBefore: 6, Footer: true, Entries: []testutil.MNEntry{
		{Tag: 0x000C, Type: 4, Count: 1, Value: []byte{1, 0, 0, 0}},
		{Tag: 0x0095, Type: 2, Count: 12, Value: []byte("EF50mm f/1.8\x00")[:12]},
	}}
	tiff, _ := testutil.MakerNoteTIFF(spec)
	e, err := Parse(tiff)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		in   string
		want []TagID
		err  error
	}{
		{"CameraSerialNumber", []TagID{0x000C}, nil},
		{"LensModel", []TagID{0x0095}, nil},
		{"0x0095", []TagID{0x0095}, nil},
		{"0X95", []TagID{0x0095}, nil},
		{"SerialInfoArray", nil, ErrMakerNoteTagNotFound}, // in the table, not in the note
		{"NoSuchName", nil, ErrMakerNoteTagNotFound},
		{"0xZZ", nil, ErrMakerNoteTagNotFound},
		{"0x7777", nil, ErrMakerNoteTagNotFound},
		{"", nil, ErrMakerNoteTagNotFound},
	} {
		got, err := e.MakerNoteTagIDs(tc.in)
		if !errors.Is(err, tc.err) || len(got) != len(tc.want) || (len(got) > 0 && got[0] != tc.want[0]) {
			t.Errorf("MakerNoteTagIDs(%q) = %v, %v; want %v, %v", tc.in, got, err, tc.want, tc.err)
		}
	}
}

// TestMakerNoteEdit_R399_SharedBytes: a value whose bytes another entry shares
// is not written in place (that would change the other entry): it is replaced
// by a copy at the end of the note, and refused under FixedSize.
func TestMakerNoteEdit_R399_SharedBytes(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	build := func() ([]byte, *EXIF) {
		tiff, lay := testutil.MakerNoteTIFF(canonSpec(le, 0))
		// Entry 3 (the RATIONAL, 8 bytes) is made to point 4 bytes into the
		// value of entry 2 (the 16-byte text): the two share 4 bytes.
		ifd := lay.MNOffset // the Canon IFD starts the note
		val2 := int(le.Uint32(tiff[ifd+2+12*1+8:]))
		le.PutUint32(tiff[ifd+2+12*2+8:], uint32(val2+4)) //nolint:gosec // G115: test sizes are small
		e, err := Parse(tiff)
		if err != nil {
			t.Fatal(err)
		}
		return tiff, e
	}
	_, e := build()
	before := bytes.Clone(e.MakerNote)
	if err := e.SetMakerNoteTag(0x0002, "SERIAL-9876543", FixedSize()); !errors.Is(err, ErrMakerNoteNotEditable) {
		t.Fatalf("FixedSize on shared bytes: error = %v, want ErrMakerNoteNotEditable", err)
	}
	if !bytes.Equal(before, e.MakerNote) {
		t.Fatal("a refused edit changed the note")
	}
	m, err := newMakerNoteEdit(e)
	if err != nil {
		t.Fatal(err)
	}
	was, _ := m.topEntries(m.orig)
	if err := e.SetMakerNoteTag(0x0002, "SERIAL-9876543"); err != nil {
		t.Fatalf("SetMakerNoteTag: %v", err)
	}
	m2, err := newMakerNoteEdit(e)
	if err != nil {
		t.Fatal(err)
	}
	now, _ := m2.topEntries(m2.orig)
	for i := range was {
		switch was[i].tag {
		case 0x0002:
			if string(now[i].val) != "SERIAL-9876543\x00" {
				t.Errorf("tag 2 = %q", now[i].val)
			}
		default:
			if !bytes.Equal(was[i].val, now[i].val) {
				t.Errorf("tag %#04x changed although it only shared bytes with the edited one", uint16(was[i].tag))
			}
		}
	}
}

// TestMakerNoteEdit_R398_DeleteSeveralAtomically: DeleteMakerNoteTags removes all
// the tags or none.
func TestMakerNoteEdit_R398_DeleteSeveralAtomically(t *testing.T) {
	t.Parallel()
	tiff, _ := testutil.MakerNoteTIFF(mnCases()[0].spec)
	e, err := Parse(tiff)
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(e.MakerNote)
	if err := e.DeleteMakerNoteTags([]TagID{0x0001, 0x7777}); !errors.Is(err, ErrMakerNoteTagNotFound) {
		t.Fatalf("error = %v, want ErrMakerNoteTagNotFound", err)
	}
	if !bytes.Equal(before, e.MakerNote) || e.MakerNoteIFD.Get(0x0001) == nil {
		t.Fatal("a refused delete changed the note")
	}
	if err := e.DeleteMakerNoteTags([]TagID{0x0001, 0x0003}); err != nil {
		t.Fatal(err)
	}
	if e.MakerNoteIFD.Get(0x0001) != nil || e.MakerNoteIFD.Get(0x0003) != nil || e.MakerNoteIFD.Get(0x0002) == nil {
		t.Error("the parsed MakerNote IFD does not follow the delete")
	}
}

// TestMakerNoteEdit_R398_OtherBytesStay: the entries of the
// MakerNote other than the edited one stay byte for byte, whichever the layout.
func TestMakerNoteEdit_R398_OtherBytesStay(t *testing.T) {
	t.Parallel()
	for _, tc := range mnCases() {
		if strings.Contains(tc.name, "outside the note") {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tiff, _ := testutil.MakerNoteTIFF(tc.spec)
			e, err := Parse(tiff)
			if err != nil {
				t.Fatal(err)
			}
			before := bytes.Clone(e.MakerNote)
			if err := e.SetMakerNoteTag(0x0001, uint16(9), FixedSize()); err != nil {
				t.Fatal(err)
			}
			// A same-size edit of an inline value changes the 2 value bytes of one
			// entry and nothing else.
			diff := 0
			for i := range before {
				if before[i] != e.MakerNote[i] {
					diff++
				}
			}
			if len(before) != len(e.MakerNote) || diff != 1 { // 7 -> 9: one byte
				t.Errorf("%d bytes differ (length %d -> %d), want 1", diff, len(before), len(e.MakerNote))
			}
		})
	}
}
