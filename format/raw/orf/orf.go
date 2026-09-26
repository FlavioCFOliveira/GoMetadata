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
// #293: rawEXIF is now the METADATA PREFIX of the file (see
// tiff.ExtractWithMagic / format/tiff/extent.go's #289 scanner), not the
// whole file — a corpus-wide measurement found ORF's metadata occupies
// ~11.5% of a real file (including the MakerNote, tag 0x927C), so reading
// only that prefix is a substantial saving over the pre-#293 whole-file
// read. RawEXIF() docs and CHANGELOG updated accordingly (matching #289's
// TIFF/CR2/NEF/ARW/DNG contract). ORF is structurally classic TIFF; only
// bytes[2:4] (the magic: "RO"/0x4F52 or "RS"/0x5352) differ from TIFF 6.0
// §2's 0x002A, so tiff.ExtractWithMagic is used instead of tiff.Extract to
// accept it — mirroring exif.AcceptRAWMagic's existing role on the write
// path (relocate_orf.go).
//
// Two behaviours predating #293 are preserved exactly, since several
// conformance tests (containers.md §8(f)) pin them:
//   - An oversized input is rejected with ErrFileTooLarge BEFORE its magic
//     bytes are ever inspected (matching the pre-#293 readInput-first
//     order) — checked here, since tiff.ExtractWithMagic's own size cap
//     alone would otherwise let an "invalid magic" error from a huge,
//     all-zero input mask the more actionable ErrFileTooLarge.
//   - A file too short to carry an IFD0 offset field (bytes[4:8], < 8
//     bytes total) is tolerated: Extract returns whatever bytes are
//     present, verbatim, as rawEXIF, with a nil error and nil IPTC/XMP —
//     round-trip fidelity for a near-empty file — rather than
//     tiff.ExtractWithMagic's own stricter ErrFileTooShort.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("orf: seek: %w", err)
	}

	// Handles the size cap and the too-short special case; see their own
	// doc comments above. handled reports whether Extract should return
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
	if !isORFMagic(peek[:]) {
		return nil, nil, nil, ErrInvalidMagic
	}
	acceptMagic := binary.LittleEndian.Uint16(peek[2:4])
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("orf: seek: %w", err)
	}

	rawEXIF, rawIPTC, rawXMP, err = tiff.ExtractWithMagic(r, acceptMagic)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("orf: %w", err)
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
		return nil, false, fmt.Errorf("orf: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}
	if _, serr := r.Seek(0, io.SeekStart); serr != nil {
		return nil, false, fmt.Errorf("orf: seek: %w", serr)
	}
	if end >= 8 {
		return nil, false, nil
	}
	data, rerr := readInput(r)
	if rerr != nil {
		return nil, false, rerr
	}
	if !isORFMagic(data) {
		return nil, false, ErrInvalidMagic
	}
	return data, true, nil
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
