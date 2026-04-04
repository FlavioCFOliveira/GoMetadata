// Package nef implements metadata extraction for Nikon NEF files.
// NEF is a TIFF-based format; metadata is extracted via the standard TIFF path.
package nef

import (
	"io"

	"github.com/FlavioCFOliveira/img-metadata/format/tiff"
)

// Extract reads metadata from a NEF file.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	return tiff.Extract(r)
}

// Inject writes a modified NEF stream to w.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	return tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
}
