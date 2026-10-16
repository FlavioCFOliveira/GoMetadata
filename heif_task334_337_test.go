package gometadata

// heif_task334_337_test.go — task #335: a HEIF file whose iloc box declares
// offset_size or length_size 0 (ISO/IEC 14496-12 §8.11.3.3: sizes are taken
// from {0, 4, 8}) receives the metadata a caller writes; Write no longer
// returns nil with the file unchanged. The package-level matrix lives in
// format/heif/task334_335_test.go.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/heif"
)

// hbox assembles an ISOBMFF box.
func hbox(typ string, body []byte) []byte {
	return append(binary.BigEndian.AppendUint32(nil, uint32(8+len(body))), append([]byte(typ), body...)...) //nolint:gosec // G115: small test box
}

// hfile assembles a HEIF file with one image item "IMAGE" located by an iloc
// box with the given offset_size and length_size. With offset_size 0 the item
// is located by its 4-byte base_offset, otherwise by a 4-byte extent_offset.
func hfile(offsetSize, lengthSize byte) []byte {
	const image = "IMAGE"
	build := func(mdatData uint32) []byte {
		iloc := []byte{0, 0, 0, 0, offsetSize<<4 | lengthSize, 0x40 /* base_offset_size 4 */, 0, 1, 0, 1, 0, 0}
		if offsetSize == 0 {
			iloc = append(iloc, binary.BigEndian.AppendUint32(nil, mdatData)...) // base_offset
		} else {
			iloc = append(iloc, 0, 0, 0, 0) // base_offset 0
		}
		iloc = append(iloc, 0, 1) // extent_count
		if offsetSize != 0 {
			iloc = append(iloc, binary.BigEndian.AppendUint32(nil, mdatData)...) // extent_offset
		}
		if lengthSize != 0 {
			iloc = append(iloc, binary.BigEndian.AppendUint32(nil, uint32(len(image)))...) // extent_length
		}
		infe := hbox("infe", append([]byte{2, 0, 0, 0, 0, 1, 0, 0}, append([]byte("hvc1"), 0)...))
		iinf := hbox("iinf", append([]byte{0, 0, 0, 0, 0, 1}, infe...))
		meta := hbox("meta", append(append(append([]byte{0, 0, 0, 0}, hbox("pitm", []byte{0, 0, 0, 0, 0, 1})...), iinf...), hbox("iloc", iloc)...))
		ftyp := hbox("ftyp", []byte("heic\x00\x00\x00\x00mif1"))
		return append(append(ftyp, meta...), hbox("mdat", []byte(image))...)
	}
	first := build(0)
	return build(uint32(len(first) - len(image))) //nolint:gosec // G115: small test file
}

// TestHEIF_Task335_ZeroWidthIlocWrite: Read-modify-Write of a file with a
// zero-width iloc field stores the caption, keeps the image item addressable,
// and re-reads through the public API.
func TestHEIF_Task335_ZeroWidthIlocWrite(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                 string
		offsetSize, lengthSz byte
	}{
		{"offset_size 0", 0, 4},
		{"length_size 0", 4, 0},
	} {
		t.Run("HEIF-ILOC-WIDTH0-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := hfile(tc.offsetSize, tc.lengthSz)
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("Read source: %v", err)
			}
			m.SetCaption("zero-width iloc")
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src), &out, m); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if bytes.Equal(out.Bytes(), src) {
				t.Fatal("Write returned nil but left the file unchanged: the caption was dropped")
			}
			after, err := Read(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("Read output: %v", err)
			}
			if after.Caption() != "zero-width iloc" {
				t.Errorf("caption after the round trip = %q", after.Caption())
			}
			// The image item is still addressed: base_offset + extent_offset
			// of its entry (widths are 4 and 4 after the rewrite) points at
			// the image bytes.
			i := bytes.Index(out.Bytes(), []byte("iloc")) + 4
			body := out.Bytes()[i:]
			if body[4] != 0x44 {
				t.Fatalf("output iloc sizes = %#x, want 0x44 (widened)", body[4])
			}
			// item: id(2) dref(2) base(4) count(2) offset(4) length(4), after
			// version/flags(4) sizes(2) item_count(2).
			base := binary.BigEndian.Uint32(body[12:])
			off := binary.BigEndian.Uint32(body[18:])
			if got := out.Bytes()[base+off : base+off+5]; string(got) != "IMAGE" {
				t.Errorf("image item resolves to %q, want IMAGE", got)
			}
		})
	}
}

// TestHEIF_Task335_WriteRefusesInsteadOfDropping: a file that cannot hold the
// requested metadata makes Write return an error and write nothing, never nil
// with the change discarded.
func TestHEIF_Task335_WriteRefusesInsteadOfDropping(t *testing.T) {
	t.Parallel()
	ftyp := hbox("ftyp", []byte("heic\x00\x00\x00\x00mif1"))
	for _, tc := range []struct {
		name string
		file []byte
		want error
	}{
		{"no meta box", ftyp, heif.ErrNoMetadataContainer},
		{"meta without iloc", append(append([]byte{}, ftyp...), hbox("meta", []byte{0, 0, 0, 0})...), heif.ErrUnreadableItemLocations},
	} {
		t.Run("HEIF-WRITE-NODROP-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(tc.file))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			m.SetCaption("must not be dropped silently")
			var out bytes.Buffer
			err = Write(bytes.NewReader(tc.file), &out, m)
			if !errors.Is(err, tc.want) {
				t.Errorf("Write error = %v, want %v", err, tc.want)
			}
			if out.Len() != 0 {
				t.Errorf("Write produced %d bytes before refusing", out.Len())
			}
		})
	}
}
