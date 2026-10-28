package exif

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// footerLookalikeTIFF is a TIFF stream whose Canon MakerNote ends with a value
// whose last 8 bytes look like the Canon footer ("II*\0" and 4 more bytes): the
// minimised input that FuzzMakerNoteEdit found (seed 761e70b8858a387f).
const footerLookalikeTIFF = "II*\x00\b\x00\x00\x0000\x0f\x01\x02\x000\x00\x00\x00&\x00\x00\x00i\x87000000,\x00\x00\x000000CAnon\x0000|\x92\a\x00J\x00\x00\x00D\x00\x00\x000000000000\x03\x0000000000000001\x02\x00\x0f\x00\x00\x00~\x00\x00\x000000000000000000000000000000000000000000II*\x000000"

// TestTask418_FooterLookalikeInsideValueIsNotPatched: EncodeRebased patches the
// TIFF offset of the Canon footer only when the last 8 bytes of the note are a
// genuine footer. A value that ends with the bytes of one is kept byte for byte.
func TestTask418_FooterLookalikeInsideValueIsNotPatched(t *testing.T) {
	t.Parallel()
	src := []byte(footerLookalikeTIFF)
	e, err := Parse(src)
	if err != nil || e == nil || e.MakerNoteIFD == nil {
		t.Fatalf("Parse: %v (MakerNote IFD present: %v)", err, e != nil && e.MakerNoteIFD != nil)
	}
	want := bytes.Clone(e.MakerNote)
	out, err := EncodeRebased(e, src)
	if err != nil {
		t.Fatalf("EncodeRebased: %v", err)
	}
	e2, err := Parse(out)
	if err != nil || e2 == nil || e2.MakerNoteIFD == nil {
		t.Fatalf("Parse of the output: %v", err)
	}
	if !bytes.Equal(e2.MakerNote[len(e2.MakerNote)-8:], want[len(want)-8:]) {
		t.Errorf("the tail of the note changed: % x, want % x", e2.MakerNote[len(e2.MakerNote)-8:], want[len(want)-8:])
	}
	for _, o := range e.MakerNoteIFD.Entries {
		g := e2.MakerNoteIFD.Get(o.Tag)
		if g == nil || !bytes.Equal(g.Value, o.Value) {
			t.Errorf("entry %#x differs after EncodeRebased: %x, want %x", uint16(o.Tag), g, o.Value)
		}
	}
}

// TestTask418_GenuineFooterStillPatched: a note whose last 8 bytes are a real
// footer after its last value still has the footer set to its new position.
func TestTask418_GenuineFooterStillPatched(t *testing.T) {
	t.Parallel()
	src, _ := testutil.MakerNoteTIFF(canonSpec(binary.LittleEndian, 0))
	e, err := Parse(src)
	if err != nil || e == nil {
		t.Fatalf("Parse: %v", err)
	}
	mn := e.MakerNote
	lay := MakerNoteLayout{ByteOrder: binary.LittleEndian, TIFFRelative: true, layout: mnCanon}
	if !genuineFooter(mn, lay, int64(e.MakerNoteOffset)) {
		t.Fatal("a Canon note with its footer after the last value is not seen as having one")
	}
	out, err := EncodeRebased(e, src)
	if err != nil {
		t.Fatalf("EncodeRebased: %v", err)
	}
	e2, err := Parse(out)
	if err != nil || e2 == nil {
		t.Fatalf("Parse of the output: %v", err)
	}
	if got, want := binary.LittleEndian.Uint32(e2.MakerNote[len(e2.MakerNote)-4:]), e2.MakerNoteOffset; got != want {
		t.Errorf("footer offset = %d, want %d", got, want)
	}
}
