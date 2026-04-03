// Package sony parses Sony MakerNote IFDs.
package sony

// Parser implements makernote.Parser for Sony cameras.
type Parser struct{}

// Parse decodes a Sony MakerNote payload.
// Not yet implemented; returns nil without error.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	return nil, nil
}
