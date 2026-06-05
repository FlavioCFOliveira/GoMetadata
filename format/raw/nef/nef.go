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
// into a NEF file via this function re-encodes the IFD block without relocating
// StripOffsets, TileOffsets, SubIFD image-data pointers, or Nikon MakerNote
// references. Those offsets become invalid after re-encoding, corrupting the
// image. Do NOT call Inject directly from a user-facing write path; use
// gometadata.Write instead, which routes NEF through the dedicated writeTIFFNEF
// path (tiff.InjectWithEXIFNEF) that handles all Nikon-specific relocation.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	if err := tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments); err != nil {
		return fmt.Errorf("nef: %w", err)
	}
	return nil
}
