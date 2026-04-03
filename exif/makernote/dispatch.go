// Package makernote dispatches MakerNote parsing to the appropriate
// manufacturer-specific sub-package based on the IFD0 Make tag value.
package makernote

// Parser is implemented by each manufacturer-specific package.
type Parser interface {
	// Parse decodes a raw MakerNote payload into a map of tag ID → raw value bytes.
	Parse(b []byte) (map[uint16][]byte, error)
}

// Dispatch selects the correct Parser for the given make string.
// Returns nil when the make is unknown or unsupported.
func Dispatch(make string) Parser {
	panic("not implemented")
}
