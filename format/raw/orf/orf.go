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
	"errors"
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
	"github.com/FlavioCFOliveira/GoMetadata/internal/tiffscan"
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

// Extract reads metadata from an ORF file. Both IIRO and IIRS magic variants
// are accepted.
//
// The returned rawEXIF is the ORIGINAL file bytes with the ORF magic
// preserved (#117), so that RawEXIF() round-trips and writing rawEXIF back
// to disk produces a valid ORF file. The IFD0 scan reads the original bytes
// directly: it never reads bytes [0:4], so no magic patching is needed.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("orf: seek: %w", err)
	}
	data, err := readInput(r)
	if err != nil {
		return nil, nil, nil, err
	}
	if !isORFMagic(data) {
		return nil, nil, nil, ErrInvalidMagic
	}

	rawEXIF = data // original bytes, magic preserved

	if len(data) < 8 {
		return rawEXIF, nil, nil, nil
	}

	// ORF is always little-endian ("II"); the IFD0 offset at bytes [4:8] is a
	// standard TIFF offset (ExifTool Olympus.pm).
	order := binary.LittleEndian
	ifd0Off := order.Uint32(data[4:])
	rawIPTC, rawXMP = extractTIFFTags(data, ifd0Off, order)
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
	data, err := readInput(r)
	if err != nil {
		return err
	}
	if !isORFMagic(data) {
		return ErrInvalidMagic
	}

	// Save the original magic variant (IIRO or IIRS) for the output.
	mw := &iobuf.MagicWriter{W: w, Magic: [2]byte{data[2], data[3]}}

	// Patch bytes 2-3 to standard TIFF LE magic (TIFF 6.0 §2: 0x002A) so
	// tiff.Inject accepts the stream. data is exclusively owned (returned by
	// readInput), so in-place mutation is safe.
	data[2] = 0x2A
	data[3] = 0x00

	// mw restores the ORF magic on the first write; tiff.Inject writes its
	// output with a single Write call, so no output buffering is needed.
	injectErr := tiff.Inject(bytes.NewReader(data), mw, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments)
	if mw.Err != nil {
		return fmt.Errorf("orf: write: %w", mw.Err)
	}
	if errors.Is(injectErr, iobuf.ErrShortFirstWrite) {
		return ErrOutputTooShort
	}
	if injectErr != nil {
		return fmt.Errorf("orf: inject: %w", injectErr)
	}
	if !mw.Started() {
		return ErrOutputTooShort
	}
	return nil
}

// readInput reads the whole of r from its current position, capped at
// maxFileSize bytes (#140): ErrFileTooLarge is returned before any parsing
// takes place.
func readInput(r io.ReadSeeker) ([]byte, error) {
	data, err := iobuf.ReadAll(r, maxFileSize)
	if err != nil {
		if errors.Is(err, iobuf.ErrTooLarge) {
			return nil, fmt.Errorf("orf: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
		}
		return nil, fmt.Errorf("orf: read: %w", err)
	}
	return data, nil
}

// extractTIFFTags scans IFD0 for the IPTC (0x83BB) and XMP (0x02BC) tags and
// returns their raw byte values, using the shared tiffscan scanner.
func extractTIFFTags(data []byte, ifd0Off uint32, order binary.ByteOrder) (rawIPTC, rawXMP []byte) {
	return tiffscan.ExtractTagValues(data, ifd0Off, order, false)
}

// typeSize returns the byte size of a single value for the given classic TIFF
// type code, or 0 for an unrecognised type (shared tiffscan table).
func typeSize(t uint16) uint32 {
	return tiffscan.TypeSize(t, false)
}
