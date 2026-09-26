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
// #293: rawEXIF is now the METADATA PREFIX of the file (see
// tiff.ExtractWithMagic / format/tiff/extent.go's #289 scanner), not the
// whole file — a corpus-wide measurement found RW2's metadata occupies
// ~3.3% of a real file (including tag 0x002E), so reading only that prefix
// is a substantial saving over the pre-#293 whole-file read. RawEXIF() docs
// and CHANGELOG updated accordingly (matching #289's TIFF/CR2/NEF/ARW/DNG
// contract). RW2 is structurally classic TIFF; only bytes[2:4] (the magic,
// "U\x00"/0x0055) differ from TIFF 6.0 §2's 0x002A, so tiff.ExtractWithMagic
// is used instead of tiff.Extract to accept it — mirroring
// exif.AcceptRAWMagic's existing role on the write path (relocate_rw2.go).
//
// Note: RW2 uses non-standard IFD encoding; some entries may not decode
// correctly.
//
// Two behaviours predating #293 are preserved exactly, mirroring orf.Extract
// (see its own doc comment for the full rationale, shared verbatim here):
//   - An oversized input is rejected with ErrFileTooLarge before its magic
//     bytes are ever inspected.
//   - A file too short to carry an IFD0 offset field (< 8 bytes total) is
//     tolerated: Extract returns whatever bytes are present, verbatim, as
//     rawEXIF, with a nil error and nil IPTC/XMP.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("rw2: seek: %w", err)
	}

	// Handles the size cap and the too-short special case; see Extract's
	// own doc comment above. handled reports whether Extract should return
	// immediately with data as rawEXIF (nil IPTC/XMP, nil error).
	data, handled, err := extractSizeCappedShortCircuit(r)
	if err != nil {
		return nil, nil, nil, err
	}
	if handled {
		return data, nil, nil, nil
	}

	var peek [4]byte
	if _, err = io.ReadFull(r, peek[:]); err != nil {
		return nil, nil, nil, ErrInvalidMagic
	}
	if !bytes.Equal(peek[:], rw2Magic) {
		return nil, nil, nil, ErrInvalidMagic
	}
	acceptMagic := binary.LittleEndian.Uint16(peek[2:4])
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("rw2: seek: %w", err)
	}

	rawEXIF, rawIPTC, rawXMP, err = tiff.ExtractWithMagic(r, acceptMagic)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("rw2: %w", err)
	}
	return rawEXIF, rawIPTC, rawXMP, nil
}

// extractSizeCappedShortCircuit implements the two pre-#293 behaviours
// Extract preserves verbatim (see Extract's own doc comment for the full
// rationale): rejecting an oversized input before any magic inspection, and
// tolerating a file too short to carry an IFD0 offset field. r's position
// is at 0 on entry and is restored to 0 before returning in every case
// where the caller (Extract) still needs to read from it (handled == false,
// err == nil).
//
// Split out of Extract to keep its cyclomatic/nesting complexity within the
// project's linter thresholds.
//
// Returns:
//   - err != nil: Extract must return this error immediately.
//   - handled: Extract must return data as rawEXIF (nil IPTC/XMP, nil error).
//   - otherwise: r is positioned at 0; Extract continues its normal path.
func extractSizeCappedShortCircuit(r io.ReadSeeker) (data []byte, handled bool, err error) {
	end, szErr := r.Seek(0, io.SeekEnd)
	if szErr != nil {
		// r does not support Seek(SeekEnd) — fall through to
		// tiff.ExtractWithMagic, whose own seekFileSize call will hit the
		// same condition and take its non-seekable-reader fallback path
		// (extractWholeFile), enforcing maxFileSize via readInput there
		// instead. r's position is already wherever szErr's failed Seek left
		// it; that is unchanged from before this call, per io.Seeker's
		// contract for a failed Seek.
		return nil, false, nil //nolint:nilerr // non-seekable-to-end reader: disable the size-cap/short-circuit fast path, not fatal
	}
	if end > maxFileSize {
		return nil, false, fmt.Errorf("rw2: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}
	if _, serr := r.Seek(0, io.SeekStart); serr != nil {
		return nil, false, fmt.Errorf("rw2: seek: %w", serr)
	}
	if end >= 8 {
		return nil, false, nil
	}
	data, rerr := readInput(r)
	if rerr != nil {
		return nil, false, rerr
	}
	if !bytes.HasPrefix(data, rw2Magic) {
		return nil, false, ErrInvalidMagic
	}
	return data, true, nil
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
