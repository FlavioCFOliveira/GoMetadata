package exif

// task286_acceptrawmagic_test.go — regression battery for task #286
// (Sprint 44, Batch E): exif.AcceptRAWMagic lets format/tiff and
// format/raw/{orf,rw2} parse non-standard-magic RAW containers directly off
// their original bytes, without changing Parse's default (no-option)
// behaviour for every other caller.

import (
	"encoding/binary"
	"testing"
)

// TestParseRejectsORFRW2MagicByDefault proves that exif.Parse — called
// without any options, exactly as every general-purpose caller (JPEG APP1,
// PNG eXIf, standard TIFF/BigTIFF, ...) calls it — still rejects Olympus ORF
// (IIRO/IIRS) and Panasonic RW2 (IIU\x00) magic. AcceptRAWMagic must never
// change this default: it is opt-in only, exercised solely by format/tiff and
// format/raw/{orf,rw2}'s own internal call sites (see read.go's
// nonStandardRAWMagic and format/tiff's relocate_orf.go/relocate_rw2.go).
func TestParseRejectsORFRW2MagicByDefault(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		magic uint16 // little-endian value written to bytes[2:4]
	}{
		{"ORF IIRO", 0x4F52},
		{"ORF IIRS", 0x5352},
		{`RW2 "IIU\x00"`, 0x0055},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := minimalTIFF(binary.LittleEndian, nil)
			binary.LittleEndian.PutUint16(data[2:], tc.magic)
			if _, err := Parse(data); err == nil {
				t.Fatalf("Parse(%s magic, no options): expected an error, got nil", tc.name)
			}
		})
	}
}

// TestAcceptRAWMagicParsesOptedInMagic proves that AcceptRAWMagic(magic)
// makes Parse treat exactly that magic value like classic TIFF (0x002A), and
// that it does not loosen the check for any OTHER non-standard magic value —
// the option must match exactly, not merely be present.
func TestAcceptRAWMagicParsesOptedInMagic(t *testing.T) {
	t.Parallel()
	data := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 640},
	})
	const orfMagic = 0x4F52 // IIRO
	binary.LittleEndian.PutUint16(data[2:], orfMagic)

	if _, err := Parse(data); err == nil {
		t.Fatal("Parse without AcceptRAWMagic: expected an error for ORF magic, got nil")
	}

	if _, err := Parse(data, AcceptRAWMagic(0x5352)); err == nil {
		t.Fatal("Parse with AcceptRAWMagic(IIRS) on IIRO data: expected an error, got nil")
	}

	e, err := Parse(data, AcceptRAWMagic(orfMagic))
	if err != nil {
		t.Fatalf("Parse with AcceptRAWMagic(0x%04X): %v", orfMagic, err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}
	entry := e.IFD0.Get(TagImageWidth)
	if entry == nil {
		t.Fatal("TagImageWidth not found")
	}
	if entry.Uint32() != 640 {
		t.Errorf("got %d, want 640", entry.Uint32())
	}
}

// TestAcceptRAWMagicZeroValueDoesNotAcceptZeroMagic proves the
// cfg.extraMagic != 0 guard in Parse's dispatch condition: a caller that
// never calls AcceptRAWMagic (so parseConfig.extraMagic is its zero value)
// must not have a corrupt file whose magic field happens to be 0x0000
// silently accepted as classic TIFF.
func TestAcceptRAWMagicZeroValueDoesNotAcceptZeroMagic(t *testing.T) {
	t.Parallel()
	data := minimalTIFF(binary.LittleEndian, nil)
	binary.LittleEndian.PutUint16(data[2:], 0x0000)
	if _, err := Parse(data); err == nil {
		t.Fatal("Parse: expected an error for magic 0x0000 with no options, got nil")
	}
}
