package exif

// makernote_base_412_test.go — task #412: Parse reads the value offsets of a
// MakerNote with the base the data decide (resolveLayout), the base the writers
// and the MakerNote editor use, and reads a Kyocera note with the base of its own
// entries.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// corpusTIFF reads the corpus file f and returns its TIFF stream (the Exif APP1
// payload of a JPEG, the file itself otherwise); nil when it holds no EXIF.
func corpusTIFF(f string) []byte {
	data, err := os.ReadFile(f)
	if err != nil || len(data) > 32<<20 {
		return nil
	}
	off := 0
	if i := bytes.Index(data[:min(len(data), 1<<16)], []byte("Exif\x00\x00")); i >= 0 {
		off = i + 6
	}
	return data[off:]
}

// legacyMakerNoteIFD reads the MakerNote of e from tiff with the base of the
// layout table, as Parse did before task #412.
func legacyMakerNoteIFD(e *EXIF, tiff []byte) *IFD {
	_, mk := makerNoteOf(e)
	l := detectLayout(e.MakerNote, mk)
	parse := mnLayouts[l].parse
	if parse == nil {
		return nil
	}
	if l == mnCanon {
		if c, ok := canonRebasedCopy(e.MakerNote, e.ByteOrder, uint64(e.MakerNoteOffset)); ok {
			return parse(blobView(c), e.ByteOrder)
		}
	}
	return parse(makerNoteView(mnLayouts[l].base, e.MakerNote, tiff, uint64(e.MakerNoteOffset)), e.ByteOrder)
}

// changedByTask412 names the layouts whose notes the corpus reads differently
// since the data decide the base; every other layout reads as before.
var changedByTask412 = map[string]bool{ //nolint:gochecknoglobals // test table
	"sanyo": true, "ricoh": true, "pentax": true, "pentax-aoc": true, "samsung": true, "kyocera": true,
}

// TestMakerNoteBase_R412_CorpusRegression: over every corpus file with a
// MakerNote, the IFD of a layout the task does not concern is the IFD read
// without it, entry by entry; for the other layouts every inline value (which
// does not depend on the base) is unchanged and no tag is lost.
func TestMakerNoteBase_R412_CorpusRegression(t *testing.T) {
	t.Parallel()
	files := corpusFiles(t, ".jpg", ".jpeg", ".tif", ".tiff", ".cr2", ".dng", ".nef", ".arw", ".orf", ".rw2", ".srw")
	if len(files) == 0 {
		t.Skip("corpus absent (see testdata/download.sh)")
	}
	seen, changed := 0, 0
	for _, f := range files {
		tiff := corpusTIFF(f)
		e, err := Parse(tiff)
		if err != nil || e == nil || e.IFD0 == nil || len(e.MakerNote) == 0 || e.MakerNoteOffset == 0 {
			continue
		}
		_, mk := makerNoteOf(e)
		name := mnLayouts[detectLayout(e.MakerNote, mk)].name
		legacy := legacyMakerNoteIFD(e, tiff)
		seen++
		if sameIFD(legacy, e.MakerNoteIFD) {
			continue
		}
		changed++
		if !changedByTask412[name] {
			t.Errorf("%s: layout %q reads differently from the layout table's base", f, name)
			continue
		}
		if e.MakerNoteIFD == nil {
			t.Errorf("%s: the MakerNote IFD is lost", f)
			continue
		}
		for _, old := range legacy.Entries {
			if int64(typeSize(old.Type))*int64(old.Count) > 4 {
				continue
			}
			if got := e.MakerNoteIFD.Get(old.Tag); got == nil || got.Type != old.Type || got.Count != old.Count || !bytes.Equal(got.Value, old.Value) {
				t.Errorf("%s: inline tag %#04x changed or lost", f, uint16(old.Tag))
			}
		}
	}
	if seen == 0 {
		t.Fatal("no corpus file with a MakerNote")
	}
	t.Logf("%d MakerNotes, %d read differently", seen, changed)
}

// TestMakerNoteBase_R412_ExifToolValues pins the out-of-line values of the three
// corpus files of the task, captured once from ExifTool 13.55 ("exiftool -j -b -n
// -a -u -H -G1 -MakerNotes:all"; the binary values by their SHA-256), without
// running it.
func TestMakerNoteBase_R412_ExifToolValues(t *testing.T) {
	t.Parallel()
	type want struct {
		tag    TagID
		count  uint32
		shorts string // ExifTool value of a SHORT or BYTE list
		sha    string // SHA-256 of the value bytes
		prefix string // hex of the first bytes of the value
	}
	tests := []struct {
		file string
		want []want
	}{
		{"jpeg/exiftool/Pentax.jpg", []want{
			{tag: 0x0200, count: 4, shorts: "0 0 0 0"},
			{tag: 0x0201, count: 4, shorts: "13568 8192 8192 9088"},
			{tag: 0x0209, count: 16, shorts: "156 157 156 154 157 168 158 151 158 179 163 159 163 174 160 176"},
			{tag: 0x020a, count: 16, shorts: "0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"},
			{tag: 0x020d, count: 4, shorts: "13600 8192 8192 8765"},
			{tag: 0x020e, count: 4, shorts: "16128 8192 8192 6635"},
			{tag: 0x020f, count: 4, shorts: "14560 8192 8192 7782"},
			{tag: 0x0210, count: 4, shorts: "8192 8192 8192 20971"},
			{tag: 0x0211, count: 4, shorts: "17376 8192 8192 8847"},
			{tag: 0x0212, count: 4, shorts: "14528 8192 8192 10076"},
			{tag: 0x0213, count: 4, shorts: "13088 8192 8192 12206"},
			{tag: 0x0214, count: 4, shorts: "13632 8192 8192 8601"},
		}},
		{"jpeg/metadata-extractor/Samsung NX3000_2.jpg", []want{
			{tag: 0x0060, count: 6000, sha: "a82416a397d1d04a24ff27f63a8310d6afff7372cd6fbfde215d771a217a7537"},
			{tag: 0x0080, count: 20000, sha: "66fe2fd182510742827b9fee031561a647e0fc368e0365af9b73da73c6757e36"},
			{tag: 0x00a0, count: 7000, sha: "1c91f597f506398301d6bdc673cd17fb95f850af58e54b0f4515501f2054f227"},
			{tag: 0x00c0, count: 6144, sha: "7cfcc82f0c3d5b9e6796db48543a9c55863d90273405ee37f281e40509b03127"},
		}},
		{"jpeg/metadata-extractor/Kyocera FC-S3.jpg", []want{
			{tag: 0x0001, count: 55160, sha: "56e1b65e08e81843c5ae31d8d81664741fdeaa493bc43024d16d741fe4469425"},
			// ExifTool -v3: the PrintIM block at byte 55208 of the MakerNote.
			{tag: 0x0e00, count: 40, prefix: "5072696e74494d003031303000000400010016000000"},
		}},
	}
	for _, tc := range tests {
		t.Run(filepath.Base(tc.file), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("..", "testdata", "corpus", tc.file)
			if _, err := os.Stat(path); err != nil {
				t.Skip("corpus file absent (see testdata/download.sh)")
			}
			e, err := Parse(corpusTIFF(path))
			if err != nil || e.MakerNoteIFD == nil {
				t.Fatalf("Parse: %v, MakerNoteIFD %v", err, e.MakerNoteIFD)
			}
			for _, w := range tc.want {
				got := e.MakerNoteIFD.Get(w.tag)
				if got == nil {
					t.Errorf("tag %#04x missing", uint16(w.tag))
					continue
				}
				if got.Count != w.count {
					t.Errorf("tag %#04x count = %d, want %d", uint16(w.tag), got.Count, w.count)
				}
				switch {
				case w.shorts != "":
					if s := listOf(got); s != w.shorts {
						t.Errorf("tag %#04x = %q, want %q", uint16(w.tag), s, w.shorts)
					}
				case w.sha != "":
					if sum := sha256.Sum256(got.Value); hex.EncodeToString(sum[:]) != w.sha {
						t.Errorf("tag %#04x: value differs from ExifTool's", uint16(w.tag))
					}
				case !bytes.HasPrefix(got.Value, mustHex(t, w.prefix)):
					t.Errorf("tag %#04x = % x..., want prefix %s", uint16(w.tag), got.Value[:min(len(got.Value), 24)], w.prefix)
				}
			}
		})
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// listOf formats the values of a BYTE, UNDEFINED or SHORT entry as ExifTool -n
// does: space-separated decimals. The Pentax MakerNote of the corpus is
// big-endian.
func listOf(e *IFDEntry) string {
	var out []byte
	switch e.Type {
	case TypeShort:
		for i := 0; i+2 <= len(e.Value); i += 2 {
			out = appendInt(out, int(binary.BigEndian.Uint16(e.Value[i:])))
		}
	default:
		for _, b := range e.Value {
			out = appendInt(out, int(b))
		}
	}
	return string(bytes.TrimSuffix(out, []byte(" ")))
}

func appendInt(b []byte, n int) []byte {
	return append(append(b, []byte(itoa(n))...), ' ')
}

// TestMakerNoteBase_R412_DataDecidesBase: a note whose offsets count from the
// TIFF header although its layout table says the MakerNote (Pentax AOC), and one
// whose offsets count from the MakerNote although the table says the TIFF header
// (Samsung), are read at the values; Parse and the resolver of the writers agree.
func TestMakerNoteBase_R412_DataDecidesBase(t *testing.T) {
	t.Parallel()
	le, be := binary.LittleEndian, binary.BigEndian
	tests := []struct {
		name string
		spec testutil.MNSpec
	}{
		{"Pentax AOC counts from TIFF", testutil.MNSpec{Make: "PENTAX", Order: be, Prefix: []byte("AOC\x00\x00\x00"), TIFFRelative: true, Entries: mnEntries(), PadBefore: 6}},
		{"Pentax AOC counts from MakerNote", testutil.MNSpec{Make: "PENTAX", Order: be, Prefix: []byte("AOC\x00\x00\x00"), Entries: mnEntries(), PadBefore: 6}},
		{"Samsung counts from MakerNote", testutil.MNSpec{Make: "SAMSUNG", Order: le, Entries: mnEntries(), PadBefore: 6}},
		{"Samsung counts from TIFF", testutil.MNSpec{Make: "SAMSUNG", Order: le, TIFFRelative: true, Entries: mnEntries(), PadBefore: 6}},
		{"Sanyo counts from TIFF", testutil.MNSpec{Make: "SANYO", Order: le, Prefix: []byte("SANYO\x00\x01\x00"), TIFFRelative: true, Entries: mnEntries(), PadBefore: 6}},
		{"Ricoh counts from TIFF", testutil.MNSpec{Make: "RICOH", Order: be, Prefix: []byte("Ricoh\x00\x00\x00"), TIFFRelative: true, Entries: mnEntries(), PadBefore: 6}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tiff, _ := testutil.MakerNoteTIFF(tc.spec)
			e, err := Parse(tiff)
			if err != nil || e.MakerNoteIFD == nil {
				t.Fatalf("Parse: %v, MakerNoteIFD %v", err, e.MakerNoteIFD)
			}
			if got := e.MakerNoteIFD.Get(0x0002); got == nil || string(got.Value) != mnSerialStr {
				t.Errorf("tag 0x0002 = %v, want %q", got, mnSerialStr)
			}
			if got := e.MakerNoteIFD.Get(0x0003); got == nil || string(got.Value) != mnRatStr {
				t.Errorf("tag 0x0003 = %v, want %x", got, mnRatStr)
			}
			// The editor and the writers resolve the base the same way.
			mn, mk := makerNoteOf(e)
			lay, _, ok := resolveMakerNote(e, mn, mk)
			if !ok || lay.TIFFRelative != tc.spec.TIFFRelative {
				t.Errorf("resolveMakerNote: TIFFRelative = %v (ok %v), want %v", lay.TIFFRelative, ok, tc.spec.TIFFRelative)
			}
		})
	}
}

// TestMakerNoteBase_R412_ShiftedOffsetsKeepTable: offsets that are off by a
// constant, so that a value starts inside the entry table under the base the
// data prefer (the Sanyo S1 of the corpus), are not read from that base: the
// bytes there are not the values. The note reads as the layout table says.
func TestMakerNoteBase_R412_ShiftedOffsetsKeepTable(t *testing.T) {
	t.Parallel()
	spec := testutil.MNSpec{Make: "SANYO", Order: binary.LittleEndian, Prefix: []byte("SANYO\x00\x01\x00"), TIFFRelative: true, Entries: mnEntries(), FooterAdd: -20, PadBefore: 6}
	tiff, _ := testutil.MakerNoteTIFF(spec)
	e, err := Parse(tiff)
	if err != nil {
		t.Fatal(err)
	}
	if want := legacyMakerNoteIFD(e, tiff); !sameIFD(want, e.MakerNoteIFD) {
		t.Errorf("a note whose offsets start inside its table is not read as the layout table says")
	}
	if got := e.MakerNoteIFD.Get(0x0002); got != nil && string(got.Value) != mnSerialStr {
		t.Errorf("tag 0x0002 = %q: bytes that are not the value were returned", got.Value)
	}
}

// kyoceraNote builds a Kyocera MakerNote of the camera format: "KYOCERA" padded
// to 22 bytes, an IFD of three entries whose offsets count from the entry (an
// inline SHORT, a 15-byte string and a RATIONAL), and the two values behind the
// table.
func kyoceraNote(o binary.ByteOrder) []byte {
	const ifd = kyoceraIFDOffset
	note := make([]byte, ifd+2+3*12+4, ifd+2+3*12+4+len(mnSerialStr)+len(mnRatStr))
	copy(note, "KYOCERA")
	o.PutUint16(note[ifd:], 3)
	put := func(i int, tag, typ uint16, cnt uint32) int {
		ep := ifd + 2 + 12*i
		o.PutUint16(note[ep:], tag)
		o.PutUint16(note[ep+2:], typ)
		o.PutUint32(note[ep+4:], cnt)
		return ep
	}
	ep := put(0, 1, 3, 1)
	o.PutUint16(note[ep+8:], 7)
	ep = put(1, 2, 2, uint32(len(mnSerialStr)))
	serialAt := len(note)
	o.PutUint32(note[ep+8:], uint32(serialAt-ep)) //nolint:gosec // G115: small test sizes
	note = append(note, mnSerialStr...)
	ep = ifd + 2 + 12*2
	put(2, 3, 5, 1)
	ratAt := len(note)
	o.PutUint32(note[ep+8:], uint32(ratAt-ep)) //nolint:gosec // G115: small test sizes
	note = append(note, mnRatStr...)
	return note
}

// TestMakerNoteBase_R412_KyoceraEntryRelative: the offset of a Kyocera entry
// counts from the entry (ExifTool MakerNotes.pm, MakerNoteKyocera, EntryBased).
func TestMakerNoteBase_R412_KyoceraEntryRelative(t *testing.T) {
	t.Parallel()
	for _, o := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(o.String(), func(t *testing.T) {
			t.Parallel()
			note := kyoceraNote(o)
			orig := bytes.Clone(note)
			ifd := makerNoteDispatch([]byte("KYOCERA"), o, note)
			if ifd == nil {
				t.Fatal("no IFD")
			}
			if got := ifd.Get(2); got == nil || string(got.Value) != mnSerialStr {
				t.Errorf("tag 2 = %v, want %q", got, mnSerialStr)
			}
			if got := ifd.Get(3); got == nil || string(got.Value) != mnRatStr {
				t.Errorf("tag 3 = %v, want %x", got, mnRatStr)
			}
			if got := ifd.Get(1); got == nil || got.Count != 1 {
				t.Errorf("tag 1 = %v", got)
			}
			if !bytes.Equal(note, orig) {
				t.Error("the read modified the MakerNote bytes")
			}
		})
	}
	if _, ok := kyoceraRebasedCopy([]byte("KYOCERA"), binary.LittleEndian); ok {
		t.Error("a note too short to hold an IFD was copied")
	}
	// An offset that overflows 32 bits reads no value, and does not panic.
	note := kyoceraNote(binary.LittleEndian)
	ep := kyoceraIFDOffset + 2 + 12
	binary.LittleEndian.PutUint32(note[ep+8:], 0xFFFF_FFF0)
	if ifd := makerNoteDispatch([]byte("KYOCERA"), binary.LittleEndian, note); ifd != nil && ifd.Get(2) != nil {
		t.Error("an offset beyond 32 bits returned a value")
	}
}

// TestMakerNoteBase_R412_KyoceraNotEditable: the MakerNote editor declines a
// Kyocera note, whose offsets it would not keep, and a writer carries it as it is.
func TestMakerNoteBase_R412_KyoceraNotEditable(t *testing.T) {
	t.Parallel()
	note := kyoceraNote(binary.LittleEndian)
	e := &EXIF{
		ByteOrder: binary.LittleEndian,
		IFD0:      &IFD{Entries: []IFDEntry{{Tag: TagMake, Type: TypeASCII, Count: 8, Value: []byte("KYOCERA\x00")}}},
		ExifIFD:   &IFD{Entries: []IFDEntry{{Tag: TagMakerNote, Type: TypeUndefined, Count: uint32(len(note)), Value: note}}}, //nolint:gosec // G115: a 94-byte test note
		MakerNote: note, MakerNoteOffset: 200, MakerNoteOffset64: 200,
	}
	if err := e.DeleteMakerNoteTag(2); !errors.Is(err, ErrMakerNoteNotEditable) {
		t.Errorf("DeleteMakerNoteTag = %v, want ErrMakerNoteNotEditable", err)
	}
	if p := planMakerNote(e, nil, new(int)); p != nil {
		t.Errorf("planMakerNote = %+v, want no repair for a note of entry-relative offsets", p)
	}
}

// BenchmarkMakerNoteDispatchAt_R412 measures the dispatch of a MakerNote read
// from a TIFF stream (what Parse does) for layouts whose base is the one the
// table says, the one the data choose, and the entry-relative one.
func BenchmarkMakerNoteDispatchAt_R412(b *testing.B) {
	le, be := binary.LittleEndian, binary.BigEndian
	kNote := kyoceraNote(le)
	kTIFF := append(make([]byte, 64), kNote...)
	cases := []struct {
		name string
		spec testutil.MNSpec
	}{
		{"Canon", testutil.MNSpec{Make: "Canon", Order: le, TIFFRelative: true, Entries: mnEntries(), Footer: true, PadBefore: 6}},
		{"Nikon3Style/Fujifilm", testutil.MNSpec{Make: "FUJIFILM", Order: le, Prefix: append([]byte("FUJIFILM"), 12, 0, 0, 0), Entries: mnEntries(), PadBefore: 6}},
		{"PentaxTableBlob", testutil.MNSpec{Make: "PENTAX", Order: be, Prefix: []byte("AOC\x00\x00\x00"), Entries: mnEntries(), PadBefore: 6}},
		{"PentaxDataTIFF", testutil.MNSpec{Make: "PENTAX", Order: be, Prefix: []byte("AOC\x00\x00\x00"), TIFFRelative: true, Entries: mnEntries(), PadBefore: 6}},
		{"SamsungDataBlob", testutil.MNSpec{Make: "SAMSUNG", Order: le, Entries: mnEntries(), PadBefore: 6}},
	}
	for i := range cases {
		c := &cases[i]
		tiff, lay := testutil.MakerNoteTIFF(c.spec)
		mn := tiff[lay.MNOffset : lay.MNOffset+lay.MNLen]
		mk := []byte(c.spec.Make)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = makerNoteDispatchAt(mk, c.spec.Order, mn, tiff, uint64(lay.MNOffset)) //nolint:gosec // G115: small test sizes
			}
		})
	}
	b.Run("Kyocera", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			_ = makerNoteDispatchAt([]byte("KYOCERA"), le, kNote, kTIFF, 64)
		}
	})
}

// TestMakerNoteBase_R408_TieKeepsOutOfTheTable: a large note holds every value
// under both bases (a Samsung SRW note carries the preview image); the base under
// which a value starts inside the entry table is the wrong one, so the table of
// layouts does not decide that tie.
func TestMakerNoteBase_R408_TieKeepsOutOfTheTable(t *testing.T) {
	t.Parallel()
	entries := make([]testutil.MNEntry, 0, 8)
	for i := range 8 {
		entries = append(entries, testutil.MNEntry{Tag: uint16(1 + i), Type: 2, Count: uint32(len(mnSerialStr)), Value: mnSerial()})
	}
	spec := testutil.MNSpec{Make: "S", Order: binary.LittleEndian, Entries: entries}
	tiff, lay := testutil.MakerNoteTIFF(spec)
	e, err := Parse(tiff)
	if err != nil {
		t.Fatal(err)
	}
	mn, _ := makerNoteOf(e)
	if !valuesAllInside(mn, MakerNoteLayout{ByteOrder: binary.LittleEndian}, int64(lay.MNOffset)) {
		t.Fatal("fixture: the values must lie inside the note when read from the MakerNote")
	}
	// The table of layouts says TIFF for Samsung; the Make selects that layout.
	e.IFD0.Entries[0].Value = []byte("SAMSUNG\x00")
	l, _, ok := resolveMakerNote(e, mn, []byte("SAMSUNG"))
	if !ok || l.TIFFRelative {
		t.Errorf("resolveMakerNote: TIFFRelative = %v (ok %v), want false: the TIFF reading puts a value in the entry table", l.TIFFRelative, ok)
	}
}

// TestEncode_R408_PaddingEntryDropped: an all-zero entry that opens IFD0 (the
// Samsung NX3000 pads IFD0 with one) is not written: ExifTool reads none of an
// IFD whose first entry has type 0. A tag-0 entry that is not padding is kept.
func TestEncode_R408_PaddingEntryDropped(t *testing.T) {
	t.Parallel()
	pad := IFDEntry{Value: make([]byte, 4)}
	mk := IFDEntry{Tag: TagMake, Type: TypeASCII, Count: 4, Value: []byte("ABC\x00")}
	real0 := IFDEntry{Tag: 0, Type: TypeByte, Count: 1, Value: []byte{9}}
	tests := []struct {
		name    string
		entries []IFDEntry
		want    int
	}{
		{"padding dropped", []IFDEntry{pad, mk}, 1},
		{"no padding", []IFDEntry{mk}, 1},
		{"tag 0 with a type is data", []IFDEntry{real0, mk}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{Entries: tc.entries}}
			out, err := Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Parse(out)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.IFD0.Entries) != tc.want || got.IFD0.Entries[0].Type == 0 {
				t.Errorf("IFD0 has %d entries, first type %d; want %d, none of type 0", len(got.IFD0.Entries), got.IFD0.Entries[0].Type, tc.want)
			}
			if len(e.IFD0.Entries) != len(tc.entries) {
				t.Error("Encode modified the entries of its receiver")
			}
		})
	}
}
