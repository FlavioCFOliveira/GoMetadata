package tiff

// relocate_olympus_external_test.go — task #370, rule R-34 (docs/conformance/
// exif-tiff.md): the Olympus preview offset after a TIFF or ORF write, for data
// inside the MakerNote, inside an image block, between the IFDs and the image
// data, and after the last block.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// olympusPreview is the data the offset addresses: a JPEG-like stream.
var olympusPreview = append(append([]byte{0xFF, 0xD8, 0xFF, 0xDB, 0x00, 0x03, 0x00}, bytes.Repeat([]byte("preview-"), 12)...), 0xFF, 0xD9) //nolint:gochecknoglobals // immutable test fixture

// olympusStrip is the image data of IFD0.
var olympusStrip = bytes.Repeat([]byte("STRIP"), 20) //nolint:gochecknoglobals // immutable test fixture

// olympusIFDValue returns the entries of an E-1 CameraSettings value.
func olympusIFDValue(start, length uint32) []byte {
	le := binary.LittleEndian
	v := le.AppendUint16(nil, 3)
	for _, e := range [...]struct {
		tag uint16
		val uint32
	}{{0x0100, 1}, {0x0101, start}, {0x0102, length}} {
		v = le.AppendUint16(v, e.tag)
		v = le.AppendUint16(v, 4)
		v = le.AppendUint32(v, 1)
		v = le.AppendUint32(v, e.val)
	}
	return le.AppendUint32(v, 0)
}

// olympusSource builds a classic little-endian TIFF with an Olympus MakerNote
// whose preview offset (layout "olympus": CameraSettings IFD counted from the
// MakerNote; "olymp": the E-1 value counted from the TIFF header) addresses the
// preview as place says:
//
//	"mn"    inside the MakerNote
//	"tail"  after everything (no image blocks)
//	"gap"   between the IFDs and the strip of IFD0
//	"block" inside the strip of IFD0
//
// It returns the file and the position and length of the strip of IFD0 (position
// 0: no strip).
func olympusSource(tb testing.TB, layout, place string) (file []byte, stripAt, stripLen int) {
	tb.Helper()
	le := binary.LittleEndian
	n := uint32(len(olympusPreview)) //nolint:gosec // G115: tiny test sizes
	data := testutil.MNEntry{Tag: 0x0300, Type: 7, Count: n, Value: olympusPreview}
	spec := testutil.MNSpec{Order: le, PadBefore: 6}
	switch layout {
	case "olympus":
		spec.Make, spec.Prefix = "OLYMPUS IMAGING CORP.", []byte("OLYMPUS\x00II\x03\x00")
		spec.Entries = []testutil.MNEntry{data, {Tag: 0x2020, Sub: &testutil.MNSub{Entries: []testutil.MNEntry{
			{Tag: 0x0100, Type: 4, Count: 1, Value: le.AppendUint32(nil, 1)},
			{Tag: 0x0101, Type: 4, Count: 1, Value: le.AppendUint32(nil, 0)},
			{Tag: 0x0102, Type: 4, Count: 1, Value: le.AppendUint32(nil, n)},
		}}}}
	case "olymp":
		spec.Make, spec.Prefix, spec.TIFFRelative = "OLYMPUS OPTICAL CO.,LTD", []byte("OLYMP\x00\x01\x00"), true
		spec.Entries = []testutil.MNEntry{data, {Tag: 0x2020, Type: 7, Count: 42, Value: olympusIFDValue(0, n)}}
	default:
		tb.Fatalf("layout %q", layout)
	}
	tiff, lay := testutil.MakerNoteTIFF(spec)
	if len(tiff)%2 == 1 {
		tiff = append(tiff, 0)
	}
	var target int
	switch place {
	case "mn":
		target = lay.Values[0]
		file = tiff
	case "tail":
		target = len(tiff)
		file = append(tiff, olympusPreview...)
	case "gap":
		target = len(tiff)
		stripAt, stripLen = target+len(olympusPreview)+len(olympusPreview)%2, len(olympusStrip)
		file = append(append(tiff, olympusPreview...), make([]byte, len(olympusPreview)%2)...)
		file = append(file, olympusStrip...)
	case "block":
		stripAt = len(tiff)
		file = append(append(append(tiff, olympusStrip...), olympusPreview...), olympusStrip...)
		target = stripAt + len(olympusStrip)
		stripLen = len(olympusStrip)*2 + len(olympusPreview) // the strip holds the preview
	default:
		tb.Fatalf("place %q", place)
	}
	offs := exif.ExternalOffsets(file)
	if len(offs) != 1 {
		tb.Fatalf("fixture: ExternalOffsets = %+v, want one", offs)
	}
	le.PutUint32(file[offs[0].Field:], uint32(target-offs[0].Base)) //nolint:gosec // G115: tiny test offsets
	if got := exif.ExternalOffsets(file); len(got) != 1 || got[0].Target() != int64(target) {
		tb.Fatalf("fixture: the offset addresses %v, want %d", got, target)
	}
	return file, stripAt, stripLen
}

// addStrip parses file and adds StripOffsets and StripByteCounts to IFD0 (kept
// sorted: Make, StripOffsets, StripByteCounts, the Exif IFD pointer).
func addStrip(t *testing.T, file []byte, stripAt, stripLen int, opts ...exif.ParseOption) *exif.EXIF {
	t.Helper()
	e, err := exif.Parse(file, opts...)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if stripAt == 0 {
		return e
	}
	le := binary.LittleEndian
	long := func(tag exif.TagID, v int) exif.IFDEntry {
		return exif.IFDEntry{Tag: tag, Type: exif.TypeLong, Count: 1, Value: le.AppendUint32(nil, uint32(v))} //nolint:gosec // G115: tiny test offsets
	}
	ents := e.IFD0.Entries
	e.IFD0.Entries = append(append(append([]exif.IFDEntry{}, ents[:1]...), long(exif.TagStripOffsets, stripAt), long(exif.TagStripByteCounts, stripLen)), ents[1:]...)
	return e
}

// TestRelocateOlympusExternal_R34 (task #370): whatever is moved by a write that
// grows IFD0 (a new XMP tag), the Olympus preview offset of the output addresses
// the preview, byte for byte, for a TIFF and for an ORF, and the strip of IFD0
// is intact.
func TestRelocateOlympusExternal_R34(t *testing.T) {
	t.Parallel()
	for _, container := range []string{"TIFF", "ORF"} {
		for _, layout := range []string{"olympus", "olymp"} {
			for _, place := range []string{"mn", "tail", "gap", "block"} {
				t.Run(fmt.Sprintf("R-34/%s/%s/%s", container, layout, place), func(t *testing.T) {
					t.Parallel()
					file, stripAt, stripLen := olympusSource(t, layout, place)
					var opts []exif.ParseOption
					in := bytes.Clone(file)
					if container == "ORF" {
						in[2], in[3] = 'R', 'O' // "IIRO"
						opts = append(opts, exif.AcceptRAWMagic(binary.LittleEndian.Uint16(in[2:4])))
					}
					e := addStrip(t, in, stripAt, stripLen, opts...)
					before := exif.ExternalOffsets(in, opts...)
					if len(before) != 1 {
						t.Fatalf("fixture: ExternalOffsets = %+v", before)
					}
					xmp := bytes.Repeat([]byte("x"), 301)
					var out bytes.Buffer
					var err error
					if container == "ORF" {
						err = InjectWithEXIFORF(in, e, nil, xmp, &out)
					} else {
						err = InjectWithEXIF(in, e, nil, xmp, &out)
					}
					if err != nil {
						t.Fatalf("Inject: %v", err)
					}
					res := out.Bytes()
					after := exif.ExternalOffsets(res, opts...)
					if len(after) != 1 {
						t.Fatalf("ExternalOffsets of the output = %+v, want one", after)
					}
					tg, n := after[0].Target(), int64(after[0].Length)
					if tg < 0 || tg+n > int64(len(res)) {
						t.Fatalf("the offset addresses [%d,+%d), outside the %d-byte output", tg, n, len(res))
					}
					if !bytes.Equal(res[tg:tg+n], olympusPreview) {
						t.Errorf("the offset of the output addresses %d bytes that are not the preview", n)
					}
					if tg == before[0].Target() {
						t.Error("the preview did not move: the test proves nothing")
					}
					if stripAt != 0 {
						o2, err := exif.Parse(res, opts...)
						if err != nil {
							t.Fatalf("Parse of the output: %v", err)
						}
						so := o2.IFD0.Get(exif.TagStripOffsets)
						if so == nil || !bytes.Equal(res[so.Uint32():int(so.Uint32())+len(olympusStrip)], olympusStrip) {
							t.Error("the strip of IFD0 is not at its StripOffsets")
						}
					}
				})
			}
		}
	}
}

// TestRelocateOlympusExternal_R34_LeftAsItIs: an offset whose data cannot be
// placed (past the end of the file, partly inside the MakerNote, not a JPEG
// stream between the IFDs and the image) leaves the write working and the value
// as it was.
func TestRelocateOlympusExternal_R34_LeftAsItIs(t *testing.T) {
	t.Parallel()
	file, _, _ := olympusSource(t, "olympus", "tail")
	offs := exif.ExternalOffsets(file)
	o := offs[0]
	for _, tc := range []struct {
		name  string
		start uint32
	}{
		{"past the end of the file", uint32(len(file) + 1000 - o.Base)},                   //nolint:gosec // G115: tiny test values
		{"straddles the end of the MakerNote", uint32(o.MNStart + o.MNLen - 20 - o.Base)}, //nolint:gosec // G115: tiny test values
		{"inside the IFDs", 10},
	} {
		t.Run("R-34/"+tc.name, func(t *testing.T) {
			t.Parallel()
			in := bytes.Clone(file)
			binary.LittleEndian.PutUint32(in[o.Field:], tc.start)
			e, err := exif.Parse(in)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := InjectWithEXIF(in, e, nil, bytes.Repeat([]byte("x"), 301), &out); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			got := exif.ExternalOffsets(out.Bytes())
			if len(got) != 1 || got[0].Value != tc.start {
				t.Errorf("offset = %+v, want it left at %d", got, tc.start)
			}
		})
	}
}

// olympSubIFDFile builds a classic little-endian TIFF with an "OLYMP\0"
// MakerNote (offsets counted from the TIFF header) whose tag 0x2020 holds, as an
// UNDEFINED value, an IFD of one entry: a text of six bytes stored after the IFD
// inside the same value, addressed by a TIFF-relative offset (the E-1 form of
// the sub-directories, which the compacts of its generation use too).
func olympSubIFDFile(t *testing.T) (file []byte, text string) {
	t.Helper()
	le := binary.LittleEndian
	text = "SENTRY"
	build := func(ptr uint32) ([]byte, testutil.MNLayout) {
		v := le.AppendUint16(nil, 1)
		v = le.AppendUint16(v, 0x0003)            // CameraSettings entry of the sub-IFD
		v = le.AppendUint16(v, 2)                 // ASCII
		v = le.AppendUint32(v, uint32(len(text))) //nolint:gosec // G115: tiny test values
		v = le.AppendUint32(v, ptr)
		v = le.AppendUint32(v, 0)
		v = append(v, text...)
		return testutil.MakerNoteTIFF(testutil.MNSpec{
			Make: "OLYMPUS OPTICAL CO.,LTD", Order: le, Prefix: []byte("OLYMP\x00\x01\x00"), TIFFRelative: true, PadBefore: 6,
			Entries: []testutil.MNEntry{{Tag: 0x2020, Type: 7, Count: uint32(len(v)), Value: v}}, //nolint:gosec // G115: tiny test sizes
		})
	}
	_, lay := build(0)
	file, _ = build(uint32(lay.Values[0] + 18)) //nolint:gosec // G115: tiny test offsets
	return file, text
}

// subIFDText reads, from the classic little-endian TIFF stream out, the text
// that the sub-IFD of tag 0x2020 of the "OLYMP\0" MakerNote addresses, found
// without the parser of the library under test for the sub-IFD itself.
func subIFDText(t *testing.T, out []byte, opts ...exif.ParseOption) string {
	t.Helper()
	e, err := exif.Parse(out, opts...)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	mn := e.MakerNoteIFD.Get(0x2020)
	if mn == nil || len(mn.Value) < 18 {
		t.Fatalf("no 0x2020 value in the MakerNote: %v", mn)
	}
	le := binary.LittleEndian
	n, ptr := le.Uint32(mn.Value[2+4:]), le.Uint32(mn.Value[2+8:])
	if int(ptr)+int(n) > len(out) {
		return ""
	}
	return string(out[ptr : ptr+n])
}

// TestRelocateOlympusSubIFD_R36 (task #370): the pointers inside the sub-IFDs of
// an "OLYMP\0" MakerNote follow the MakerNote in a TIFF and in an ORF write;
// the compacts of the corpus (SP350, SP500UZ, C8080) read the same values after
// a write.
func TestRelocateOlympusSubIFD_R36(t *testing.T) {
	t.Parallel()
	for _, container := range []string{"TIFF", "ORF"} {
		t.Run("R-36/"+container, func(t *testing.T) {
			t.Parallel()
			file, text := olympSubIFDFile(t)
			var opts []exif.ParseOption
			if container == "ORF" {
				file[2], file[3] = 'R', 'O'
				opts = append(opts, exif.AcceptRAWMagic(binary.LittleEndian.Uint16(file[2:4])))
			}
			if got := subIFDText(t, file, opts...); got != text {
				t.Fatalf("fixture: the source pointer addresses %q", got)
			}
			e, err := exif.Parse(file, opts...)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if container == "ORF" {
				err = InjectWithEXIFORF(file, e, nil, bytes.Repeat([]byte("x"), 301), &out)
			} else {
				err = InjectWithEXIF(file, e, nil, bytes.Repeat([]byte("x"), 301), &out)
			}
			if err != nil {
				t.Fatalf("Inject: %v", err)
			}
			if got := subIFDText(t, out.Bytes(), opts...); got != text {
				t.Errorf("after the write the pointer inside the sub-IFD addresses %q, want %q", got, text)
			}
		})
	}
}
