// Package dng implements metadata extraction for Adobe DNG files.
// DNG is a TIFF-based format defined by Adobe DNG Specification 1.7.
// It extends TIFF with Adobe-specific tags and is also a valid TIFF file.
package dng

import (
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
)

// Extract reads metadata from a DNG file.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	rawEXIF, rawIPTC, rawXMP, err = tiff.Extract(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dng: %w", err)
	}
	return rawEXIF, rawIPTC, rawXMP, nil
}

// Inject writes a modified DNG stream to w by delegating to the TIFF writer.
//
// WARNING — image-data corruption risk: DNG is TIFF-based. Writing metadata
// into a DNG file re-encodes the IFD block (via tiff.Inject → exif.Encode)
// without relocating StripOffsets, TileOffsets, or SubIFD image-data pointers.
// Those offsets become invalid after re-encoding, corrupting the image. Do NOT
// call Inject directly on files from a user-facing write path; use
// gometadata.Write instead, which gates DNG behind ErrWriteNotSupported until
// full structural relocation is implemented (roadmap Option A, epic #33).
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	if err := tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments); err != nil {
		return fmt.Errorf("dng: %w", err)
	}
	return nil
}
