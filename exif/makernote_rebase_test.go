package exif

// makernote_rebase_test.go — tasks #317 and #318: EncodeRebased keeps a
// MakerNote valid at the position the encoded stream gives it, and carries
// the data its IFD addresses outside its declared bytes. Rule IDs: see
// docs/conformance/exif-tiff.md (R-20 to R-25).

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// mnCase is one synthetic MakerNote layout.
type mnCase struct {
	name string
	spec testutil.MNSpec
	// tiffRelative is what MakerNoteLayout must report for the layout table
	// (before the data-driven check).
	tiffRelative bool
	// moves is true when the case must move the MakerNote in Encode, so that
	// Encode alone (no repair) leaves a TIFF-relative note broken.
	moves bool
}

// The values of the synthetic MakerNotes: a 15-byte string, a RATIONAL and
// three LONGs.
const (
	mnSerialStr = "SERIAL-0123456\x00"
	mnRatStr    = "\x01\x00\x00\x00\x03\x00\x00\x00"
	mnTripleStr = "\x0a\x00\x00\x00\x14\x00\x00\x00\x1e\x00\x00\x00"
)

func mnSerial() []byte { return []byte(mnSerialStr) }
func mnRat() []byte    { return []byte(mnRatStr) }
func mnTriple() []byte { return []byte(mnTripleStr) }

// mnEntries returns the entries every layout carries: an inline SHORT, two
// out-of-line values inside the note.
func mnEntries() []testutil.MNEntry {
	return []testutil.MNEntry{
		{Tag: 0x0001, Type: 3, Count: 1, Value: []byte{7, 0}},
		{Tag: 0x0002, Type: 2, Count: uint32(len(mnSerialStr)), Value: mnSerial()},
		{Tag: 0x0003, Type: 5, Count: 1, Value: mnRat()},
	}
}

func mnCases() []mnCase {
	le := binary.LittleEndian
	return []mnCase{
		{name: "Canon plain IFD with footer", tiffRelative: true, moves: true,
			spec: testutil.MNSpec{Make: "Canon", Order: le, TIFFRelative: true, Entries: mnEntries(), Footer: true, PadBefore: 6}},
		{name: "Sony DSC", tiffRelative: true, moves: true,
			spec: testutil.MNSpec{Make: "SONY", Order: le, Prefix: []byte("SONY DSC \x00\x00\x00"), TIFFRelative: true, Entries: mnEntries(), PadBefore: 6}},
		{name: "Panasonic", tiffRelative: true, moves: true,
			spec: testutil.MNSpec{Make: "Panasonic", Order: le, Prefix: []byte("Panasonic\x00\x00\x00"), TIFFRelative: true, Entries: mnEntries(), PadBefore: 6}},
		{name: "Olympus OLYMP", tiffRelative: true, moves: true,
			spec: testutil.MNSpec{Make: "OLYMPUS OPTICAL CO.,LTD", Order: le, Prefix: []byte("OLYMP\x00\x01\x00"), TIFFRelative: true, Entries: mnEntries(), PadBefore: 6}},
		{name: "Nikon Type 2 headerless", tiffRelative: true, moves: true,
			spec: testutil.MNSpec{Make: "NIKON", Order: le, TIFFRelative: true, Entries: mnEntries(), PadBefore: 6}},
		{name: "Olympus OLYMP value outside the note", tiffRelative: true, moves: true,
			spec: testutil.MNSpec{Make: "OLYMPUS OPTICAL CO.,LTD", Order: le, Prefix: []byte("OLYMP\x00\x01\x00"), TIFFRelative: true, PadBefore: 6,
				Entries: append(mnEntries(), testutil.MNEntry{Tag: 0x0004, Type: 4, Count: 3, Value: mnTriple(), Outside: true})}},
		{name: "Olympus OLYMPUS blob-relative", tiffRelative: false, moves: true,
			spec: testutil.MNSpec{Make: "OLYMPUS CORPORATION", Order: le, Prefix: []byte("OLYMPUS\x00II\x03\x00"), Entries: mnEntries(), PadBefore: 6}},
	}
}

// decodeMN is testutil.DecodeMN with a test failure for a stream it cannot read.
func decodeMN(t *testing.T, out []byte, mnAbs, ifdAt int, order binary.ByteOrder, tiff bool, base int) map[uint16][]byte {
	t.Helper()
	res := testutil.DecodeMN(out, mnAbs, ifdAt, order, tiff, base)
	if res == nil {
		t.Fatalf("MakerNote IFD at %d+%d is outside the stream of %d bytes", mnAbs, ifdAt, len(out))
	}
	return res
}

// makerNotePosition returns the offset of the MakerNote value in out.
func makerNotePosition(t *testing.T, out []byte) int {
	t.Helper()
	pos, ok := findMakerNoteAbs(out, binary.LittleEndian)
	if !ok {
		t.Fatal("MakerNote not found in the encoded stream")
	}
	return pos
}

// TestMakerNoteRebase_R20_ValuesFollowTheMove (task #317, R-20): after
// EncodeRebased every out-of-line value of the MakerNote IFD reads the same
// as in the source, for each layout; Encode alone leaves a TIFF-relative note
// pointing at the old position.
func TestMakerNoteRebase_R20_ValuesFollowTheMove(t *testing.T) {
	t.Parallel()
	for _, tc := range mnCases() {
		t.Run("R-20/"+tc.name, func(t *testing.T) {
			t.Parallel()
			tiff, lay := testutil.MakerNoteTIFF(tc.spec)
			e, err := Parse(tiff)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			l, ok := e.MakerNoteLayout()
			if !ok {
				t.Fatal("MakerNoteLayout: layout not detected")
			}
			if l.TIFFRelative != tc.tiffRelative {
				t.Errorf("MakerNoteLayout.TIFFRelative = %v, want %v", l.TIFFRelative, tc.tiffRelative)
			}
			want := decodeMN(t, tiff, lay.MNOffset, l.IFDOffset, l.ByteOrder, tc.spec.TIFFRelative, tc.spec.Base)

			out, err := EncodeRebased(e, tiff)
			if err != nil {
				t.Fatalf("EncodeRebased: %v", err)
			}
			mnAbs := makerNotePosition(t, out)
			if tc.moves && mnAbs == lay.MNOffset {
				t.Fatalf("the MakerNote did not move (%d): the case proves nothing", mnAbs)
			}
			got := decodeMN(t, out, mnAbs, l.IFDOffset, l.ByteOrder, tc.spec.TIFFRelative, tc.spec.Base)
			for tag, w := range want {
				if !bytes.Equal(got[tag], w) {
					t.Errorf("tag %#04x = %x, want %x", tag, got[tag], w)
				}
			}
			if len(got) != len(want) {
				t.Errorf("%d entries read back, want %d", len(got), len(want))
			}

			// The same stream through Encode: a TIFF-relative note is broken.
			plain, err := Encode(e)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if tc.tiffRelative && tc.moves {
				broken := decodeMN(t, plain, makerNotePosition(t, plain), l.IFDOffset, l.ByteOrder, true, 0)
				same := true
				for tag, w := range want {
					same = same && bytes.Equal(broken[tag], w)
				}
				if same {
					t.Error("Encode alone kept the TIFF-relative values valid: the case does not detect a missing rebase")
				}
			}
		})
	}
}

// TestMakerNoteRebase_R21_CarryDataOutsideTheNote (task #318, R-21): a value
// that lies after the declared end of the note (Olympus C-720UZ) is carried:
// it reads the same after EncodeRebased, and Encode alone loses it.
func TestMakerNoteRebase_R21_CarryDataOutsideTheNote(t *testing.T) {
	t.Parallel()
	spec := testutil.MNSpec{Make: "OLYMPUS OPTICAL CO.,LTD", Order: binary.LittleEndian, Prefix: []byte("OLYMP\x00\x01\x00"), TIFFRelative: true, PadBefore: 6,
		Entries: append(mnEntries(), testutil.MNEntry{Tag: 0x0004, Type: 4, Count: 3, Value: mnTriple(), Outside: true})}
	tiff, lay := testutil.MakerNoteTIFF(spec)
	e, err := Parse(tiff)
	if err != nil {
		t.Fatal(err)
	}
	if lay.Values[3] < lay.MNOffset+lay.MNLen {
		t.Fatalf("fixture: the value is at %d, inside the note (%d..%d)", lay.Values[3], lay.MNOffset, lay.MNOffset+lay.MNLen)
	}
	out, err := EncodeRebased(e, tiff)
	if err != nil {
		t.Fatal(err)
	}
	e1, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	got := e1.MakerNoteIFD.Get(0x0004)
	if got == nil || !bytes.Equal(got.Value, mnTriple()) {
		t.Errorf("carried value = %v, want %x", got, mnTriple())
	}
	if len(e1.MakerNote) <= len(e.MakerNote) {
		t.Errorf("the MakerNote did not grow (%d -> %d bytes): nothing was carried", len(e.MakerNote), len(e1.MakerNote))
	}
	// Without src the value cannot be read: the pointer is left, never invented.
	noSrc, err := EncodeRebased(e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e2, err := Parse(noSrc); err != nil {
		t.Fatal(err)
	} else if v := e2.MakerNoteIFD.Get(0x0004); v != nil && bytes.Equal(v.Value, mnTriple()) {
		t.Error("the outside value was read back without a source stream: the test fixture is not outside the note")
	}
}

// nikon3Spec is a Nikon Type 3 note (offsets from the embedded TIFF header at
// offset 10) with a PreviewIFD that runs past the declared end and whose
// preview image lies outside the note, as the Coolpix P7000 does.
func nikon3Spec(preview []byte) testutil.MNSpec {
	le := binary.LittleEndian
	prefix := append([]byte("Nikon\x00\x02\x10\x00\x00II*\x00"), 8, 0, 0, 0)
	return testutil.MNSpec{Make: "NIKON", Order: le, Prefix: prefix, Base: 10, PadBefore: 6,
		Entries: []testutil.MNEntry{
			{Tag: 0x0001, Type: 3, Count: 1, Value: []byte{7, 0}},
			{Tag: 0x0002, Type: 2, Count: uint32(len(mnSerialStr)), Value: mnSerial()},
			{Tag: 0x0011, Sub: &testutil.MNSub{CrossesEnd: true, Entries: []testutil.MNEntry{
				{Tag: 0x0103, Type: 3, Count: 1, Value: []byte{6, 0}},
				{Tag: 0x011A, Type: 5, Count: 1, Value: mnRat()},
				{Tag: 0x0201, Type: 4, Count: 1, PtrOutside: preview},
				{Tag: 0x0202, Type: 4, Count: 1, Value: binary.LittleEndian.AppendUint32(nil, uint32(len(preview)))}, //nolint:gosec // G115: small test value
				{Tag: 0x0213, Type: 3, Count: 1, Value: []byte{2, 0}},
			}}},
		}}
}

// previewOf reads the preview image and X resolution of the Nikon PreviewIFD
// of the note in out.
func previewOf(t *testing.T, out []byte) (xres, preview []byte) {
	t.Helper()
	le := binary.LittleEndian
	mn := makerNotePosition(t, out)
	base := mn + 10
	sub := base + int(le.Uint32(out[mn+len("Nikon\x00\x02\x10\x00\x00II*\x00\x08\x00\x00\x00")+2+12*2+8:]))
	if sub < 0 || sub+2 > len(out) {
		return nil, nil
	}
	n := int(le.Uint16(out[sub:]))
	var off, length int
	for i := range n {
		ep := sub + 2 + 12*i
		if ep+12 > len(out) {
			return xres, nil
		}
		switch le.Uint16(out[ep:]) {
		case 0x011A:
			if p := base + int(le.Uint32(out[ep+8:])); p >= 0 && p+8 <= len(out) {
				xres = out[p : p+8]
			}
		case 0x0201:
			off = int(le.Uint32(out[ep+8:]))
		case 0x0202:
			length = int(le.Uint32(out[ep+8:]))
		}
	}
	if off == 0 || base+off+length > len(out) {
		return xres, nil
	}
	return xres, out[base+off : base+off+length]
}

// TestMakerNoteRebase_R22_NikonPreviewIFD (task #318, R-22): the Nikon
// PreviewIFD that crosses the declared end of the note and the preview image
// it addresses outside it are carried, and the offsets inside the carried
// structures follow.
func TestMakerNoteRebase_R22_NikonPreviewIFD(t *testing.T) {
	t.Parallel()
	preview := append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte("preview-"), 40)...)
	preview = append(preview, 0xFF, 0xD9)
	tiff, lay := testutil.MakerNoteTIFF(nikon3Spec(preview))
	e, err := Parse(tiff)
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := e.MakerNoteLayout(); !ok || l.TIFFRelative || l.Base != 10 {
		t.Fatalf("MakerNoteLayout = %+v, %v; want blob-relative with Base 10", l, ok)
	}
	if len(e.MakerNote) != lay.MNLen {
		t.Fatalf("fixture: declared %d, parsed %d", lay.MNLen, len(e.MakerNote))
	}
	out, err := EncodeRebased(e, tiff)
	if err != nil {
		t.Fatal(err)
	}
	xres, got := previewOf(t, out)
	if !bytes.Equal(xres, mnRat()) {
		t.Errorf("PreviewIFD XResolution = %x, want %x (value stored after the declared end)", xres, mnRat())
	}
	if !bytes.Equal(got, preview) {
		t.Errorf("preview image = %d bytes, want %d identical bytes", len(got), len(preview))
	}
	// Encode alone drops both.
	plain, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, p := previewOf(t, plain); bytes.Equal(p, preview) {
		t.Error("Encode alone kept the preview: the fixture does not detect missing carrying")
	}
}

// TestMakerNoteRebase_R23_CanonFooter (task #317, R-23): Canon's footer holds
// the TIFF offset of the note and follows it; a note that was moved without
// its pointers (footer and pointers disagree with the position) is repaired
// from the footer, as ExifTool's "Adjusted MakerNotes base by".
func TestMakerNoteRebase_R23_CanonFooter(t *testing.T) {
	t.Parallel()
	for _, add := range []int{0, 62, -30} {
		spec := testutil.MNSpec{Make: "Canon", Order: binary.LittleEndian, TIFFRelative: true, Entries: mnEntries(), Footer: true, FooterAdd: add, PadBefore: 6}
		tiff, lay := testutil.MakerNoteTIFF(spec)
		e, err := Parse(tiff)
		if err != nil {
			t.Fatal(err)
		}
		out, err := EncodeRebased(e, tiff)
		if err != nil {
			t.Fatal(err)
		}
		mn := makerNotePosition(t, out)
		foot := out[mn+lay.MNLen-8 : mn+lay.MNLen]
		if got := int(binary.LittleEndian.Uint32(foot[4:])); got != mn || string(foot[:4]) != "II*\x00" {
			t.Errorf("R-23 add %d: footer = % x, want a TIFF mark and the offset %d", add, foot, mn)
		}
		// The pointers follow the position: every value reads as in the source
		// as ExifTool reads it (the pointers were written for the footer offset).
		got := decodeMN(t, out, mn, 0, binary.LittleEndian, true, 0)
		want := map[uint16][]byte{1: {7, 0}, 2: mnSerial(), 3: mnRat()}
		for tag, w := range want {
			if !bytes.Equal(got[tag], w) {
				t.Errorf("R-23 add %d: tag %d = %x, want %x", add, tag, got[tag], w)
			}
		}
	}
}

// TestMakerNoteRebase_R24_TheDataDecidesTheBase (task #317, R-24): a note
// whose offsets count from a different origin than its layout table says is
// repaired for the origin the data shows: Sanyo notes of the ExifTool corpus
// hold TIFF offsets, Samsung NX notes offsets from the note.
func TestMakerNoteRebase_R24_TheDataDecidesTheBase(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	t.Run("Sanyo with TIFF offsets (table: from the note)", func(t *testing.T) {
		t.Parallel()
		spec := testutil.MNSpec{Make: "SANYO Electric Co.,Ltd.", Order: le, Prefix: []byte("SANYO\x00\x01\x00"), TIFFRelative: true, Entries: mnEntries(), PadBefore: 6}
		tiff, lay := testutil.MakerNoteTIFF(spec)
		e, err := Parse(tiff)
		if err != nil {
			t.Fatal(err)
		}
		if l, _ := e.MakerNoteLayout(); l.TIFFRelative {
			t.Fatal("fixture: the table must say blob-relative for Sanyo")
		}
		out, err := EncodeRebased(e, tiff)
		if err != nil {
			t.Fatal(err)
		}
		mn := makerNotePosition(t, out)
		if mn == lay.MNOffset {
			t.Fatal("the note did not move")
		}
		got := decodeMN(t, out, mn, 8, le, true, 0)
		if !bytes.Equal(got[2], mnSerial()) || !bytes.Equal(got[3], mnRat()) {
			t.Errorf("values after the move: %x, %x", got[2], got[3])
		}
	})
	t.Run("Samsung with offsets from the note (table: from the TIFF header)", func(t *testing.T) {
		t.Parallel()
		spec := testutil.MNSpec{Make: "SAMSUNG", Order: le, Entries: mnEntries(), PadBefore: 600}
		tiff, lay := testutil.MakerNoteTIFF(spec)
		e, err := Parse(tiff)
		if err != nil {
			t.Fatal(err)
		}
		if l, _ := e.MakerNoteLayout(); !l.TIFFRelative {
			t.Fatal("fixture: the table must say TIFF-relative for Samsung")
		}
		out, err := EncodeRebased(e, tiff)
		if err != nil {
			t.Fatal(err)
		}
		mn := makerNotePosition(t, out)
		if mn == lay.MNOffset {
			t.Fatal("the note did not move")
		}
		if !bytes.Equal(out[mn:mn+lay.MNLen], tiff[lay.MNOffset:lay.MNOffset+lay.MNLen]) {
			t.Error("a note whose offsets count from itself must move byte for byte")
		}
	})
}

// TestMakerNoteRebase_R25_InconsistentPointers (task #317, R-25): a TIFF-relative
// note with a value that straddles its end has wrong pointers (ExifTool:
// "Possibly incorrect maker notes offsets"); nothing is carried, and every
// pointer follows the note by the same distance, which keeps it as it was.
func TestMakerNoteRebase_R25_InconsistentPointers(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	spec := testutil.MNSpec{Make: "Canon", Order: le, TIFFRelative: true, PadBefore: 6, Entries: []testutil.MNEntry{
		{Tag: 0x0001, Type: 3, Count: 1, Value: []byte{7, 0}},
		{Tag: 0x0002, Type: 2, Count: uint32(len(mnSerialStr)), Value: mnSerial()},
		{Tag: 0x0003, Type: 5, Count: 1, Value: mnRat()},
	}}
	tiff, lay := testutil.MakerNoteTIFF(spec)
	e, err := Parse(tiff)
	if err != nil {
		t.Fatal(err)
	}
	// Shorten the declared length so that the last value straddles the end.
	e.MakerNote = e.MakerNote[:len(e.MakerNote)-4]
	if ent := e.ExifIFD.Get(TagMakerNote); ent != nil {
		ent.Value = e.MakerNote
		ent.Count = uint32(len(e.MakerNote)) //nolint:gosec // G115: tiny test note
	}
	out, err := EncodeRebased(e, tiff)
	if err != nil {
		t.Fatal(err)
	}
	mn := makerNotePosition(t, out)
	delta := mn - lay.MNOffset
	// Every pointer moved by the distance the note moved, and nothing was
	// appended to the note.
	for i := range 3 {
		ep := 2 + 12*i
		oldPtr := int(le.Uint32(tiff[lay.MNOffset+ep+8:]))
		newPtr := int(le.Uint32(out[mn+ep+8:]))
		if total := map[int]int{0: 2, 1: len(mnSerial()), 2: 8}[i]; total > 4 && newPtr != oldPtr+delta {
			t.Errorf("entry %d: pointer %d, want %d (old %d + %d)", i, newPtr, oldPtr+delta, oldPtr, delta)
		}
	}
	if got, want := len(e.MakerNote), len(out[mn:]); got > want {
		t.Errorf("the note is %d bytes in the stream of %d bytes", got, want)
	}
}

// TestMakerNoteRebase_R26_NoSourceNoMove: the result is Encode's when e has no
// MakerNote or the MakerNote needs no repair; the receiver is never mutated.
func TestMakerNoteRebase_R26_NoSourceNoMove(t *testing.T) {
	t.Parallel()
	plain := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}}
	plain.SetCaption("no makernote")
	a, err := Encode(plain)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeRebased(plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("EncodeRebased differs from Encode for an EXIF without MakerNote")
	}
	// The receiver is not changed by the repair.
	spec := testutil.MNSpec{Make: "Canon", Order: binary.LittleEndian, TIFFRelative: true, Entries: mnEntries(), Footer: true, PadBefore: 6}
	tiff, _ := testutil.MakerNoteTIFF(spec)
	e, err := Parse(tiff)
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(e.MakerNote)
	srcBefore := bytes.Clone(tiff)
	if _, err := EncodeRebased(e, tiff); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, e.MakerNote) || !bytes.Equal(srcBefore, tiff) {
		t.Error("EncodeRebased modified the MakerNote of the receiver or the source stream")
	}
}
