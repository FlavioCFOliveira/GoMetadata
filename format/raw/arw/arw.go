// Package arw implements metadata extraction for Sony ARW files.
// ARW is a TIFF-based format; metadata is extracted via the standard TIFF path.
package arw

import (
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
)

// Extract reads metadata from an ARW file.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	return tiff.Extract(r)
}

// Inject writes a modified ARW stream to w.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	return tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
}
