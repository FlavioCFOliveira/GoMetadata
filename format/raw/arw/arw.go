// Package arw implements metadata extraction for Sony ARW files.
// ARW is a TIFF-based format; metadata is extracted via the standard TIFF path.
package arw

import (
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
)

// Extract reads metadata from an ARW file.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	rawEXIF, rawIPTC, rawXMP, err = tiff.Extract(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("arw: %w", err)
	}
	return rawEXIF, rawIPTC, rawXMP, nil
}

// Inject writes a modified ARW stream to w by delegating to the TIFF writer.
//
// WARNING — image-data corruption risk: ARW is TIFF-based. Writing metadata
// into an ARW file via this function re-encodes the IFD block without relocating
// Sony MakerNote TIFF-absolute offsets or the SR2Private (0xC634) block. Those
// offsets become invalid after re-encoding, corrupting the image. Do NOT call
// Inject directly from a user-facing write path; use gometadata.Write instead,
// which routes ARW through the dedicated writeTIFFARW path
// (tiff.InjectWithEXIFARW) that handles all Sony-specific relocation.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	if err := tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments); err != nil {
		return fmt.Errorf("arw: %w", err)
	}
	return nil
}
