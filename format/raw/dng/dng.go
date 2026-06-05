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
// DNG is TIFF-based (Adobe DNG Specification 1.7). The tiff.Inject path uses a
// copy-and-relocate serializer that handles the canonical DNG layout: IFD0 holds
// a thumbnail with its own strip/tile blocks; one or more SubIFDs (tag 0x014A)
// carry the full-resolution image data with their own strip/tile blocks.
//
// The relocator:
//   - Enumerates image blocks from IFD0 + IFD1 chain (strips, tiles, JPEG).
//   - Recursively follows SubIFDs (0x014A) and enumerates their image blocks.
//   - Re-encodes the IFD structure, appends all SubIFD raw bytes and image blocks
//     at corrected absolute offsets, and patches all offset tags accordingly.
//
// This covers single-SubIFD, multi-SubIFD, and tiled-SubIFD DNG structures.
// Validation against a real DNG corpus is recommended before relying on this
// in production — the synthetic test fixture covers the canonical structure but
// real cameras may have manufacturer-specific extensions.
//
// See format/tiff/relocate.go for implementation details (epic #33, task #94).
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	if err := tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments); err != nil {
		return fmt.Errorf("dng: %w", err)
	}
	return nil
}
