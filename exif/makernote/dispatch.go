// Package makernote dispatches MakerNote parsing to the appropriate
// manufacturer-specific sub-package based on the IFD0 Make tag value.
package makernote

import (
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/canon"
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/dji"
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/fujifilm"
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/leica"
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/nikon"
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/olympus"
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/panasonic"
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/pentax"
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/samsung"
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/sigma"
	"github.com/FlavioCFOliveira/GoMetadata/exif/makernote/sony"
)

// Parser is implemented by each manufacturer-specific package.
type Parser interface {
	// Parse decodes a raw MakerNote payload into a map of tag ID → raw value bytes.
	Parse(b []byte) (map[uint16][]byte, error)
}

// Dispatch selects the correct Parser for the given make string.
// Returns nil when the make is unknown or unsupported.
func Dispatch(make string) Parser {
	switch make {
	case "Canon":
		return canon.Parser{}
	case "NIKON CORPORATION", "Nikon":
		return nikon.Parser{}
	case "SONY":
		return sony.Parser{}
	case "FUJIFILM":
		return fujifilm.Parser{}
	case "OLYMPUS IMAGING CORP.", "OLYMPUS CORPORATION", "Olympus":
		return olympus.Parser{}
	case "PENTAX Corporation", "Ricoh", "RICOH":
		return pentax.Parser{}
	case "Panasonic":
		return panasonic.Parser{}
	case "LEICA CAMERA AG", "Leica Camera AG", "LEICA", "Leica":
		return leica.Parser{}
	case "DJI":
		return dji.Parser{}
	case "SAMSUNG":
		return samsung.Parser{}
	case "SIGMA":
		return sigma.Parser{}
	}
	return nil
}
