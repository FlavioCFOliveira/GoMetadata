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

// Inject writes a modified ARW stream to w.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if err := tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP); err != nil {
		return fmt.Errorf("arw: %w", err)
	}
	return nil
}
