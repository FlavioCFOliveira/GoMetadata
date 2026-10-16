package heif

import "errors"

// itemLoc is the resolved location of an iloc item, for test assertions:
// offset is the absolute position of the first extent (base_offset +
// extent_offset) and length is the sum of every extent length.
type itemLoc struct {
	offset uint64
	length uint64
}

// parseIloc returns the location of every item of the iloc box found in
// metaContent, keyed by item ID. It is a test helper built on parseIlocFull.
func parseIloc(metaContent []byte) map[uint16]itemLoc {
	result := make(map[uint16]itemLoc)
	info, ok := parseIlocFull(metaContent)
	if !ok {
		return result
	}
	for _, item := range info.items {
		var loc itemLoc
		for i, ext := range item.extents {
			if i == 0 {
				loc.offset = item.baseOffset + ext.offset
			}
			loc.length += ext.length
		}
		result[item.id] = loc
	}
	return result
}

// errNoMetaBox is returned by findMetaContent for a file without a usable
// metadata container.
var errNoMetaBox = errors.New("no usable meta box")

// findMetaContent returns the content of the metadata container that Extract
// and Inject select in data (the whole file), without the box header and the
// FullBox version/flags, or errNoMetaBox when the file has none (task #349).
func findMetaContent(data []byte) ([]byte, error) {
	if c := metaContentInWindow(data, true); c != nil {
		return c, nil
	}
	return nil, errNoMetaBox
}
