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
// into an ARW file re-encodes the IFD block (via tiff.Inject → exif.Encode)
// without relocating StripOffsets, TileOffsets, or SubIFD image-data pointers.
// Those offsets become invalid after re-encoding, corrupting the image. Do NOT
// call Inject directly on files from a user-facing write path; use
// gometadata.Write instead, which gates ARW behind ErrWriteNotSupported until
// full structural relocation is implemented (roadmap Option A, epic #33).
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if err := tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP); err != nil {
		return fmt.Errorf("arw: %w", err)
	}
	return nil
}
