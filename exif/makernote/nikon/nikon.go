// Package nikon parses Nikon MakerNote IFDs.
package nikon

// Parser implements makernote.Parser for Nikon cameras.
type Parser struct{}

// Parse decodes a Nikon MakerNote payload.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	panic("not implemented")
}
