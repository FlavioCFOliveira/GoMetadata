package gometadata

// read_task313_irb_test.go — task #313: duplicate IPTC resources read the
// same through the JPEG (APP13) and TIFF (tag 0x8649) paths: the first copy
// wins (XMP Toolkit PSIR_FileWriter.cpp:ParseMemoryResources policy).

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/irb"
	"github.com/FlavioCFOliveira/GoMetadata/internal/tiffscan"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// captionIIM returns an IIM stream holding caption (2:120).
func captionIIM(t *testing.T, caption string) []byte {
	t.Helper()
	i := &iptc.IPTC{}
	i.SetCaption(caption)
	out, err := iptc.Encode(i)
	if err != nil {
		t.Fatalf("iptc.Encode: %v", err)
	}
	return out
}

// tiffWithPSIR encodes a TIFF whose IFD0 holds the IRB psir in tag 0x8649
// and, when native is non-nil, native in tag 0x83BB (LONG, zero padded).
func tiffWithPSIR(t *testing.T, native, psir []byte) []byte {
	t.Helper()
	entries := []exif.IFDEntry{}
	if native != nil {
		padded := append(bytes.Clone(native), make([]byte, (4-len(native)%4)%4)...)
		entries = append(entries, exif.IFDEntry{Tag: exif.TagIPTC, Type: exif.TypeLong, Count: uint32(len(padded) / 4), Value: padded}) //nolint:gosec // G115: test sizes are small
	}
	entries = append(entries, exif.IFDEntry{Tag: exif.TagID(tiffscan.PSIRTag), Type: exif.TypeUndefined, Count: uint32(len(psir)), Value: psir}) //nolint:gosec // G115: test sizes are small
	out, err := exif.Encode(&exif.EXIF{ByteOrder: binary.LittleEndian, IFD0: &exif.IFD{Entries: entries}})
	if err != nil {
		t.Fatalf("exif.Encode: %v", err)
	}
	return out
}

// TestRead_DuplicateIPTCResourcesFirstWins (task #313).
func TestRead_DuplicateIPTCResourcesFirstWins(t *testing.T) {
	t.Parallel()
	dup := append(irb.AppendBlock(nil, irb.ResourceIPTC, captionIIM(t, "first copy")),
		irb.AppendBlock(nil, irb.ResourceIPTC, captionIIM(t, "second copy"))...)
	for name, data := range map[string][]byte{
		"JPEG APP13":      jpegWithAPP13(t, dup),
		"TIFF tag 0x8649": tiffWithPSIR(t, nil, dup),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if m.IPTC == nil || m.IPTC.Caption() != "first copy" {
				t.Errorf("IPTC caption = %v, want the first copy", m.IPTC)
			}
		})
	}
}
