// Package orf implements metadata extraction for Olympus ORF files.
// ORF uses a TIFF-like structure with an Olympus-specific byte order marker
// "IIRO" instead of the standard "II\x2A\x00".
package orf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
)

// orfMagic is the Olympus ORF byte order / magic marker (bytes 0-3).
// Olympus replaces the standard TIFF magic (0x2A 0x00) with "RO" (0x52 0x4F).
var orfMagic = []byte{0x49, 0x49, 0x52, 0x4F} //nolint:gochecknoglobals // package-level constant bytes

// Extract reads metadata from an ORF file.
// The ORF magic bytes are patched to standard TIFF before extraction so
// that the TIFF IFD traversal code can be reused.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("orf: seek: %w", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("orf: read: %w", err)
	}
	if !bytes.HasPrefix(data, orfMagic) {
		return nil, nil, nil, fmt.Errorf("orf: invalid magic bytes")
	}

	// Patch bytes 2-3 to standard TIFF LE magic so the TIFF parser works.
	patched := make([]byte, len(data))
	copy(patched, data)
	patched[2] = 0x2A
	patched[3] = 0x00

	if len(patched) < 8 {
		return patched, nil, nil, nil
	}

	order := binary.LittleEndian
	rawEXIF = patched

	ifd0Off := order.Uint32(patched[4:])
	rawIPTC, rawXMP = extractTIFFTags(patched, ifd0Off, order)
	return rawEXIF, rawIPTC, rawXMP, nil
}

// Inject writes a modified ORF stream to w by delegating to the TIFF writer.
// ORF magic bytes are patched to standard TIFF LE before injection and
// restored in the output so the file remains a valid ORF.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("orf: seek: %w", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("orf: read: %w", err)
	}
	if !bytes.HasPrefix(data, orfMagic) {
		return fmt.Errorf("orf: invalid magic bytes")
	}

	// Patch bytes 2-3 to standard TIFF LE magic so tiff.Inject works.
	patched := make([]byte, len(data))
	copy(patched, data)
	patched[2] = 0x2A
	patched[3] = 0x00

	// Buffer the TIFF output so we can restore the ORF magic bytes.
	var buf bytes.Buffer
	if err := tiff.Inject(bytes.NewReader(patched), &buf, rawEXIF, rawIPTC, rawXMP); err != nil {
		return fmt.Errorf("orf: inject: %w", err)
	}

	out := buf.Bytes()
	if len(out) < 4 {
		return fmt.Errorf("orf: output too short")
	}
	// Restore ORF magic ("IIRO") in the output.
	out[2] = orfMagic[2]
	out[3] = orfMagic[3]

	_, err = w.Write(out)
	return err
}

func extractTIFFTags(data []byte, ifd0Off uint32, order binary.ByteOrder) (rawIPTC, rawXMP []byte) {
	if int(ifd0Off)+2 > len(data) {
		return nil, nil
	}
	count := int(order.Uint16(data[ifd0Off:]))
	pos := int(ifd0Off) + 2
	for i := 0; i < count; i++ {
		e := pos + i*12
		if e+12 > len(data) {
			break
		}
		tag := order.Uint16(data[e:])
		typ := order.Uint16(data[e+2:])
		cnt := order.Uint32(data[e+4:])
		sz := typeSize(typ)
		if sz == 0 {
			continue
		}
		total := uint64(sz) * uint64(cnt)
		var v []byte
		if total <= 4 {
			v = data[e+8 : e+8+int(total)]
		} else {
			off := order.Uint32(data[e+8:])
			// Guard against integer overflow: check before computing end.
			if uint64(off) > uint64(len(data)) || total > uint64(len(data))-uint64(off) {
				continue
			}
			v = data[uint64(off) : uint64(off)+total]
		}
		switch tag {
		case 0x83BB:
			rawIPTC = v
		case 0x02BC:
			rawXMP = v
		}
	}
	return rawIPTC, rawXMP
}

func typeSize(t uint16) uint32 {
	switch t {
	case 1, 2, 6, 7:
		return 1
	case 3, 8:
		return 2
	case 4, 9, 11:
		return 4
	case 5, 10, 12:
		return 8
	}
	return 0
}
