// Package canon parses Canon MakerNote IFDs.
package canon

// Parser implements makernote.Parser for Canon cameras.
type Parser struct{}

// Parse decodes a Canon MakerNote payload.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	panic("not implemented")
}
