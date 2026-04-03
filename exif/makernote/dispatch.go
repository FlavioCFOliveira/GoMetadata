// Package makernote dispatches MakerNote parsing to the appropriate
// manufacturer-specific sub-package based on the IFD0 Make tag value.
package makernote

import (
	"github.com/flaviocfo/img-metadata/exif/makernote/canon"
	"github.com/flaviocfo/img-metadata/exif/makernote/nikon"
	"github.com/flaviocfo/img-metadata/exif/makernote/sony"
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
	}
	return nil
}
