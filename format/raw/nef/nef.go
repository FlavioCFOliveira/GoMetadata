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

// Inject writes a modified NEF stream to w.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if err := tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP); err != nil {
		return fmt.Errorf("nef: %w", err)
	}
	return nil
}
