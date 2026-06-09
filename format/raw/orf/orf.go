// Package orf implements metadata extraction for Olympus ORF files.
// ORF uses a TIFF-like structure with an Olympus-specific byte order marker:
//   - "IIRO" (0x49 0x49 0x52 0x4F): Olympus DSLRs (E-series, OM-D line).
//   - "IIRS" (0x49 0x49 0x52 0x53): older Olympus compacts (C-series, SP-series).
//
// Both variants are structurally identical; only bytes [2:4] differ.
package orf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
)

// orfMagic is the IIRO variant of the Olympus ORF magic (the most common variant,
// used by all Olympus DSLRs and OM-D series cameras).
//
// For detection purposes (including the IIRS compact variant), use isORFMagic.
var orfMagic = []byte{0x49, 0x49, 0x52, 0x4F} //nolint:gochecknoglobals // package-level constant bytes; never mutated

// isORFMagic reports whether data begins with a valid Olympus ORF magic.
// Accepts both IIRO (byte[3]=0x4F) and IIRS (byte[3]=0x53).
//
// ExifTool Olympus.pm: ORFMagic = "IIRO" | "IIRS".
func isORFMagic(data []byte) bool {
	return len(data) >= 4 &&
		data[0] == 0x49 && data[1] == 0x49 && data[2] == 0x52 &&
		(data[3] == 0x4F || data[3] == 0x53)
}

// Extract reads metadata from an ORF file.
// The TIFF parser operates on a working copy of the data with bytes [2:4]
// patched to standard TIFF LE magic (0x2A 0x00) so that the shared IFD
// traversal code can be reused. The returned rawEXIF contains the ORIGINAL
// unmodified bytes so that RawEXIF() round-trips correctly and writing
// rawEXIF back to disk produces a valid ORF file.
// Both IIRO and IIRS magic variants are accepted.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("orf: seek: %w", err)
	}
	// #140 fix: cap the full-file read to maxFileSize+1 bytes so that an
	// oversized or infinite streaming reader cannot trigger unbounded heap
	// allocation. ErrFileTooLarge is returned when the limit is exceeded,
	// before any parsing takes place.
	data, err := io.ReadAll(io.LimitReader(r, maxFileSize+1))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("orf: read: %w", err)
	}
	if int64(len(data)) > maxFileSize {
		return nil, nil, nil, fmt.Errorf("orf: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}
	if !isORFMagic(data) {
		return nil, nil, nil, ErrInvalidMagic
	}

	// #117 fix: rawEXIF must carry the ORIGINAL bytes so that callers can
	// round-trip the file correctly (writing rawEXIF to disk produces a valid
	// ORF, not a standard TIFF). Operate on a separate working copy for TIFF
	// parsing so the original bytes are never mutated.
	//
	// ORF magic: bytes [0:2]="II", bytes [2:4]="RO" (IIRO) or "RS" (IIRS).
	// TIFF parser requires bytes [2:4]=0x2A 0x00 (TIFF 6.0 §2, magic = 42).
	// ExifTool Olympus.pm: patch bytes[2:4] to 0x2A 0x00 for IFD traversal.
	tiffData := make([]byte, len(data))
	copy(tiffData, data)
	tiffData[2] = 0x2A
	tiffData[3] = 0x00

	rawEXIF = data // original bytes, magic preserved

	if len(tiffData) < 8 {
		return rawEXIF, nil, nil, nil
	}

	order := binary.LittleEndian
	ifd0Off := order.Uint32(tiffData[4:])
	rawIPTC, rawXMP = extractTIFFTags(tiffData, ifd0Off, order)
	return rawEXIF, rawIPTC, rawXMP, nil
}

// Inject writes a modified ORF stream to w by delegating to the TIFF writer.
// ORF magic bytes (IIRO or IIRS) are patched to standard TIFF LE before
// injection and restored in the output so the file remains a valid ORF.
//
// WARNING — image-data corruption risk: ORF is TIFF-based. Writing metadata
// into an ORF file re-encodes the IFD block (via tiff.Inject → exif.Encode)
// without relocating StripOffsets, TileOffsets, or SubIFD image-data pointers.
// Those offsets become invalid after re-encoding, corrupting the image. Do NOT
// call Inject directly on files from a user-facing write path; use
// gometadata.Write instead, which routes ORF through tiff.InjectWithEXIFORF
// (the copy-and-relocate path that correctly rebases all image-data offsets).
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("orf: seek: %w", err)
	}
	// #140 fix: cap the full-file read to maxFileSize+1 bytes so that an
	// oversized or infinite streaming reader cannot trigger unbounded heap
	// allocation. ErrFileTooLarge is returned when the limit is exceeded,
	// before any parsing takes place.
	data, err := io.ReadAll(io.LimitReader(r, maxFileSize+1))
	if err != nil {
		return fmt.Errorf("orf: read: %w", err)
	}
	if int64(len(data)) > maxFileSize {
		return fmt.Errorf("orf: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}
	if !isORFMagic(data) {
		return ErrInvalidMagic
	}

	// Save the original magic variant (IIRO or IIRS) for restoration in the output.
	origMagic2 := data[2] // 0x52 ('R') — same in both variants
	origMagic3 := data[3] // 0x4F ('O') for IIRO or 0x53 ('S') for IIRS

	// Patch bytes 2-3 to standard TIFF LE magic so tiff.Inject works.
	// data is exclusively owned (returned by io.ReadAll), so in-place mutation
	// is safe and avoids a full-file copy (RAW files can be tens of MB).
	data[2] = 0x2A
	data[3] = 0x00

	// Buffer the TIFF output so we can restore the ORF magic bytes.
	var buf bytes.Buffer
	if injectErr := tiff.Inject(bytes.NewReader(data), &buf, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments); injectErr != nil {
		return fmt.Errorf("orf: inject: %w", injectErr)
	}

	out := buf.Bytes()
	if len(out) < 4 {
		return ErrOutputTooShort
	}
	// Restore the original ORF magic variant in the output.
	out[2] = origMagic2
	out[3] = origMagic3

	_, err = w.Write(out)
	if err != nil {
		return fmt.Errorf("orf: write: %w", err)
	}
	return nil
}

func extractTIFFTags(data []byte, ifd0Off uint32, order binary.ByteOrder) (rawIPTC, rawXMP []byte) { //nolint:gocyclo // IPTC trimming branch is inherent to TypeLong-vs-TypeUndefined handling; extracting a helper would reduce clarity
	if int(ifd0Off)+2 > len(data) {
		return nil, nil
	}
	count := int(order.Uint16(data[ifd0Off:]))
	pos := int(ifd0Off) + 2
	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
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
			// ROBUST-16 (iptc.md §5): strip TypeLong structural padding only;
			// TypeByte/Undefined payloads are returned as-is. See
			// format/tiff.extractTagValues for the full rationale. Task #153.
			if len(v) > 0 {
				if typ == 4 { // TypeLong: trim structural alignment padding
					rawIPTC = trimIPTCLongPadding(v)
				} else {
					rawIPTC = v // TypeByte / TypeUndefined: no trim (ROBUST-16)
				}
				if len(rawIPTC) == 0 {
					rawIPTC = nil
				}
			}
		case 0x02BC:
			rawXMP = v
		}
	}
	return rawIPTC, rawXMP
}

// trimIPTCLongPadding trims trailing 0x00 alignment bytes from a TypeLong IPTC
// payload. TIFF 6.0 §2: TypeLong values are padded to the next 4-byte boundary;
// those padding bytes are not IPTC data. ROBUST-16: only called for TypeLong;
// TypeByte/Undefined payloads are never trimmed.
func trimIPTCLongPadding(v []byte) []byte {
	end := len(v)
	for end > 0 && v[end-1] == 0x00 {
		end--
	}
	return v[:end]
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
