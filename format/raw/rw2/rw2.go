// Package rw2 implements metadata extraction for Panasonic RW2 files.
// RW2 is a TIFF variant with the byte order marker "IIU\x00" and uses
// non-standard IFD entry counts and offset encoding.
package rw2

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

// rw2Magic is the Panasonic RW2 byte order marker (bytes 0-3).
var rw2Magic = []byte{0x49, 0x49, 0x55, 0x00} //nolint:gochecknoglobals // package-level constant bytes

// Extract reads metadata from an RW2 file.
//
// The returned rawEXIF is the ORIGINAL file bytes with the RW2 magic
// preserved (#117), so that RawEXIF() round-trips and writing rawEXIF back
// to disk produces a valid RW2 file. The IFD0 scan reads the original bytes
// directly: it never reads bytes [0:4], so no magic patching is needed.
//
// Note: RW2 uses non-standard IFD encoding; some entries may not decode correctly.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("rw2: seek: %w", err)
	}
	data, err := readInput(r)
	if err != nil {
		return nil, nil, nil, err
	}
	if !bytes.HasPrefix(data, rw2Magic) {
		return nil, nil, nil, ErrInvalidMagic
	}

	rawEXIF = data // original bytes, magic preserved

	if len(data) < 8 {
		return rawEXIF, nil, nil, nil
	}

	// RW2 is always little-endian ("II"); the IFD0 offset at bytes [4:8] is a
	// standard TIFF offset (ExifTool Panasonic.pm).
	order := binary.LittleEndian
	ifd0Off := order.Uint32(data[4:])
	// Best-effort extraction; RW2 IFD encoding may differ from standard TIFF.
	rawIPTC, rawXMP = extractTIFFTags(data, ifd0Off, order)
	return rawEXIF, rawIPTC, rawXMP, nil
}

// Inject writes a modified RW2 stream to w by delegating to the TIFF writer.
// RW2 magic bytes are patched to standard TIFF LE before injection and
// restored in the output so the file remains a valid RW2.
// Note: RW2 uses non-standard IFD encoding; some entries may not survive the
// round-trip, but EXIF/IPTC/XMP metadata is correctly updated.
//
// WARNING — image-data corruption risk: RW2 is TIFF-based. Writing metadata
// into an RW2 file via this function re-encodes the IFD block without relocating
// the Panasonic GUID header, RawDataOffset, or SubIFD image-data pointers.
// Those offsets become invalid after re-encoding, corrupting the image. Do NOT
// call Inject directly from a user-facing write path; use gometadata.Write
// instead, which routes RW2 through the dedicated writeTIFFRW2 path
// (tiff.InjectWithEXIFRW2) that handles GUID preservation and offset rebasing.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rw2: seek: %w", err)
	}
	data, err := readInput(r)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(data, rw2Magic) {
		return ErrInvalidMagic
	}

	// Patch bytes 2-3 to standard TIFF LE magic (TIFF 6.0 §2: 0x002A) so
	// tiff.Inject accepts the stream. data is exclusively owned (returned by
	// readInput), so in-place mutation is safe.
	data[2] = 0x2A
	data[3] = 0x00

	// mw restores the RW2 magic ("IIU\x00") on the first write; tiff.Inject
	// writes its output with a single Write call, so no output buffering is
	// needed.
	mw := &iobuf.MagicWriter{W: w, Magic: [2]byte{rw2Magic[2], rw2Magic[3]}}
	injectErr := tiff.Inject(bytes.NewReader(data), mw, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments)
	if mw.Err != nil {
		return fmt.Errorf("rw2: write: %w", mw.Err)
	}
	if errors.Is(injectErr, iobuf.ErrShortFirstWrite) {
		return ErrOutputTooShort
	}
	if injectErr != nil {
		return fmt.Errorf("rw2: inject: %w", injectErr)
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
			return nil, fmt.Errorf("rw2: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
		}
		return nil, fmt.Errorf("rw2: read: %w", err)
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
