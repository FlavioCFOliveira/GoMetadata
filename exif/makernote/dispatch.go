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

// parsers maps Make tag strings to the corresponding Parser implementation.
// Multiple Make string variants that share the same parser are each listed as
// separate keys so that Dispatch remains a single map lookup (CC = 1).
//
//nolint:gochecknoglobals // dispatch table: package-level read-only map populated at init and never mutated
var parsers = map[string]Parser{
	"Canon":                 canon.Parser{},
	"NIKON CORPORATION":     nikon.Parser{},
	"Nikon":                 nikon.Parser{},
	"SONY":                  sony.Parser{},
	"FUJIFILM":              fujifilm.Parser{},
	"OLYMPUS IMAGING CORP.": olympus.Parser{},
	"OLYMPUS CORPORATION":   olympus.Parser{},
	"Olympus":               olympus.Parser{},
	"PENTAX Corporation":    pentax.Parser{},
	"Ricoh":                 pentax.Parser{},
	"RICOH":                 pentax.Parser{},
	"Panasonic":             panasonic.Parser{},
	"LEICA CAMERA AG":       leica.Parser{},
	"Leica Camera AG":       leica.Parser{},
	"LEICA":                 leica.Parser{},
	"Leica":                 leica.Parser{},
	"DJI":                   dji.Parser{},
	"SAMSUNG":               samsung.Parser{},
	"SIGMA":                 sigma.Parser{},
}

// Dispatch selects the correct Parser for the given make string.
// Returns nil when the make is unknown or unsupported.
func Dispatch(cameraMake string) Parser {
	return parsers[cameraMake]
}
