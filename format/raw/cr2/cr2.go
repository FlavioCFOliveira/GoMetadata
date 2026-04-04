// Package cr2 implements metadata extraction for Canon CR2 files.
// CR2 is a TIFF-based format with a Canon-specific IFD structure.
// The EXIF payload is located via the standard SubIFD pointer (tag 0x8769).
package cr2

import (
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
)

// Extract reads metadata from a CR2 file. Delegates TIFF parsing to
// format/tiff with Canon-specific IFD pointer awareness.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	// CR2 is standard TIFF with a Canon marker at bytes 8–9; the metadata
	// structure is otherwise identical to TIFF.
	rawEXIF, rawIPTC, rawXMP, err = tiff.Extract(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cr2: %w", err)
	}
	return rawEXIF, rawIPTC, rawXMP, nil
}

// Inject writes a modified CR2 stream to w.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if err := tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP); err != nil {
		return fmt.Errorf("cr2: %w", err)
	}
	return nil
}
