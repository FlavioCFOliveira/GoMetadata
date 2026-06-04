// Package nef implements metadata extraction for Nikon NEF files.
// NEF is a TIFF-based format; metadata is extracted via the standard TIFF path.
package nef

import (
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
)

// Extract reads metadata from a NEF file.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	rawEXIF, rawIPTC, rawXMP, err = tiff.Extract(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("nef: %w", err)
	}
	return rawEXIF, rawIPTC, rawXMP, nil
}

// Inject writes a modified NEF stream to w by delegating to the TIFF writer.
//
// WARNING — image-data corruption risk: NEF is TIFF-based. Writing metadata
// into a NEF file re-encodes the IFD block (via tiff.Inject → exif.Encode)
// without relocating StripOffsets, TileOffsets, or SubIFD image-data pointers.
// Those offsets become invalid after re-encoding, corrupting the image. Do NOT
// call Inject directly on files from a user-facing write path; use
// gometadata.Write instead, which gates NEF behind ErrWriteNotSupported until
// full structural relocation is implemented (roadmap Option A, epic #33).
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	if err := tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments); err != nil {
		return fmt.Errorf("nef: %w", err)
	}
	return nil
}
