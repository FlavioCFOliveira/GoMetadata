// Package dng implements metadata extraction for Adobe DNG files.
// DNG is a TIFF-based format defined by Adobe DNG Specification 1.7.
// It extends TIFF with Adobe-specific tags and is also a valid TIFF file.
package dng

import (
	"io"

	"github.com/FlavioCFOliveira/img-metadata/format/tiff"
)

// Extract reads metadata from a DNG file.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	return tiff.Extract(r)
}

// Inject writes a modified DNG stream to w.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	return tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
}
