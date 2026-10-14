package gometadata

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Task #296: the entry values of Metadata.EXIF alias the raw EXIF bytes that
// Read retains and Write uses as its relocation base. The contract is that
// IFDEntry.Value is read-only and is changed by assigning a new slice; every
// Value is capped at its own length so that growing it with append copies
// instead of writing into the retained bytes that follow it.

// forEachEXIFEntry calls fn for every entry of every IFD reachable from e.
func forEachEXIFEntry(e *exif.EXIF, fn func(where string, ent *exif.IFDEntry)) {
	walk := func(where string, ifd *exif.IFD) {
		for n := 0; ifd != nil; ifd, n = ifd.Next, n+1 {
			for i := range ifd.Entries {
				fn(where, &ifd.Entries[i])
			}
		}
	}
	walk("IFD0", e.IFD0)
	walk("ExifIFD", e.ExifIFD)
	walk("GPSIFD", e.GPSIFD)
	walk("InteropIFD", e.InteropIFD)
	walk("MakerNoteIFD", e.MakerNoteIFD)
}

// TestTask296_EntryValuesCappedAtLength proves that no parsed value leaves
// spare capacity over the retained raw EXIF bytes, for classic TIFF, BigTIFF
// and a camera JPEG with a MakerNote.
func TestTask296_EntryValuesCappedAtLength(t *testing.T) {
	t.Parallel()
	inputs := map[string][]byte{
		"classic TIFF": buildRichTIFF("CapMake", "CapModel", 1, 100),
	}
	for _, name := range []string{"BigTIFF_LE.tif", "BigTIFF_BE.tif", "Canon_40D.jpg"} {
		data, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		inputs[name] = data
	}
	for name, data := range inputs {
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: Read: %v", name, err)
		}
		if m.EXIF == nil {
			t.Fatalf("%s: no EXIF parsed", name)
		}
		n := 0
		forEachEXIFEntry(m.EXIF, func(where string, ent *exif.IFDEntry) {
			n++
			if cap(ent.Value) != len(ent.Value) {
				t.Errorf("%s: %s tag 0x%04X: cap(Value) = %d, len = %d", name, where, uint16(ent.Tag), cap(ent.Value), len(ent.Value))
			}
		})
		if n == 0 {
			t.Fatalf("%s: no entries parsed", name)
		}
		if mn := m.EXIF.MakerNote; cap(mn) != len(mn) {
			t.Errorf("%s: cap(MakerNote) = %d, len = %d", name, cap(mn), len(mn))
		}
	}
}

// TestTask296_AppendToValueKeepsRawEXIF grows the Make value with append —
// the documented way to derive a new value from an old one — and proves that
// neither the retained raw EXIF bytes nor the Model value stored right after
// Make in the file change, and that Write produces the edited Make with every
// other value intact.
func TestTask296_AppendToValueKeepsRawEXIF(t *testing.T) {
	t.Parallel()
	src := buildRichTIFF("CapMake", "CapModel", 1, 100)
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	rawBefore, _, _ := m.RawSegments()
	rawBefore = bytes.Clone(rawBefore)

	ent := m.EXIF.IFD0.Get(exif.TagMake)
	if ent == nil {
		t.Fatal("Make entry missing")
	}
	// Drop the NUL terminator, append a suffix and a new terminator.
	ent.Value = append(ent.Value[:len(ent.Value)-1], "Longer\x00"...)
	ent.Count = uint32(len(ent.Value)) //nolint:gosec // G115: a few bytes

	if rawAfter, _, _ := m.RawSegments(); !bytes.Equal(rawAfter, rawBefore) {
		t.Fatal("append to an entry value modified the retained raw EXIF bytes")
	}
	if got := m.CameraModel(); got != "CapModel" {
		t.Fatalf("CameraModel after append to Make = %q, want %q", got, "CapModel")
	}

	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read written file: %v", err)
	}
	if got := m2.Make(); got != "CapMakeLonger" {
		t.Errorf("Make = %q, want %q", got, "CapMakeLonger")
	}
	if got := m2.CameraModel(); got != "CapModel" {
		t.Errorf("Model = %q, want %q", got, "CapModel")
	}
	if got := m2.EXIF.IFD0.Get(exif.TagOrientation); got == nil || got.Uint16() != 1 {
		t.Errorf("Orientation lost or changed: %+v", got)
	}
}

// TestTask296_AssignedValueKeepsRawEXIF replaces a value by assignment, the
// other documented way to change it, and proves that the retained raw EXIF
// bytes are unchanged, so a second Write from the same Metadata is identical
// to the first.
func TestTask296_AssignedValueKeepsRawEXIF(t *testing.T) {
	t.Parallel()
	src := buildRichTIFF("CapMake", "CapModel", 1, 100)
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	rawBefore, _, _ := m.RawSegments()
	rawBefore = bytes.Clone(rawBefore)

	ent := m.EXIF.IFD0.Get(exif.TagModel)
	ent.Value = []byte("Other\x00")
	ent.Count = uint32(len(ent.Value)) //nolint:gosec // G115: a few bytes

	if rawAfter, _, _ := m.RawSegments(); !bytes.Equal(rawAfter, rawBefore) {
		t.Fatal("assigning an entry value modified the retained raw EXIF bytes")
	}
	var first, second bytes.Buffer
	if err := Write(bytes.NewReader(src), &first, m); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	if err := Write(bytes.NewReader(src), &second, m); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Error("two Writes of the same Metadata differ")
	}
	m2, err := Read(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatalf("Read written file: %v", err)
	}
	if got := m2.CameraModel(); got != "Other" {
		t.Errorf("Model = %q, want %q", got, "Other")
	}
	if got := m2.Make(); got != "CapMake" {
		t.Errorf("Make = %q, want %q", got, "CapMake")
	}
}
