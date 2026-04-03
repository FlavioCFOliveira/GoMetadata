package exif

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// IFD represents a TIFF Image File Directory (TIFF §2).
type IFD struct {
	Entries []IFDEntry
	Next    *IFD // linked IFDs (e.g. IFD1 for thumbnail)
}

// IFDEntry represents a single TIFF directory entry (TIFF §2, 12 bytes each).
type IFDEntry struct {
	Tag   TagID
	Type  DataType
	Count uint32
	// Value holds the decoded value. For types whose total size fits in 4 bytes
	// the raw bytes are stored inline; otherwise this is a []byte slice into
	// the original buffer (zero-copy).
	Value     []byte
	byteOrder binary.ByteOrder
}

// traverse walks the IFD chain starting at offset within b, using the given
// byte order. It returns the root IFD.
//
// The next-IFD pointer chain is followed iteratively (not recursively) to
// prevent stack overflows on cyclic or deeply nested inputs (fuzz safety).
func traverse(b []byte, offset uint32, order binary.ByteOrder) (*IFD, error) {
	if int(offset)+2 > len(b) {
		return nil, fmt.Errorf("exif: IFD offset %d out of bounds (buf len %d)", offset, len(b))
	}

	// visited tracks offsets we have already started parsing to detect cycles.
	visited := make(map[uint32]bool)

	var root, current *IFD
	cur := offset

	for cur != 0 {
		if visited[cur] {
			break // cycle detected — stop following the chain
		}
		visited[cur] = true

		if int(cur)+2 > len(b) {
			break
		}

		count := order.Uint16(b[cur:])
		pos := int(cur) + 2

		if pos+int(count)*12 > len(b) {
			// Truncated entry list — stop but return what we have.
			break
		}

		ifd := &IFD{Entries: make([]IFDEntry, 0, count)}

		for i := 0; i < int(count); i++ {
			e := pos + i*12
			tag := TagID(order.Uint16(b[e:]))
			typ := DataType(order.Uint16(b[e+2:]))
			cnt := order.Uint32(b[e+4:])

			sz := typeSize(typ)
			totalSize := uint64(sz) * uint64(cnt)

			var value []byte
			if sz == 0 || totalSize > 4 {
				if sz == 0 {
					// Unknown type: store the raw 4-byte offset/value field.
					value = b[e+8 : e+12]
				} else {
					valOff := order.Uint32(b[e+8:])
					end := uint64(valOff) + totalSize
					if end > uint64(len(b)) {
						// Out-of-bounds offset: skip this entry gracefully.
						continue
					}
					value = b[valOff:end]
				}
			} else {
				// Value is inline, left-justified in the 4-byte field (TIFF §2).
				value = b[e+8 : e+8+int(totalSize)]
			}

			ifd.Entries = append(ifd.Entries, IFDEntry{
				Tag:       tag,
				Type:      typ,
				Count:     cnt,
				Value:     value,
				byteOrder: order,
			})
		}

		// Link into the chain.
		if root == nil {
			root = ifd
		} else {
			current.Next = ifd
		}
		current = ifd

		// Read the next-IFD pointer (4 bytes after the last entry, TIFF §2).
		nextPtrPos := pos + int(count)*12
		if nextPtrPos+4 > len(b) {
			break
		}
		cur = order.Uint32(b[nextPtrPos:])
	}

	if root == nil {
		return nil, fmt.Errorf("exif: IFD at offset %d could not be parsed (buf len %d)", offset, len(b))
	}
	return root, nil
}

// Get returns the first entry matching tag, or nil if not found.
func (ifd *IFD) Get(tag TagID) *IFDEntry {
	if ifd == nil {
		return nil
	}
	for i := range ifd.Entries {
		if ifd.Entries[i].Tag == tag {
			return &ifd.Entries[i]
		}
	}
	return nil
}

// String decodes the entry value as a NUL-terminated ASCII string (TypeASCII, TIFF §2).
func (e *IFDEntry) String() string {
	if e.Type != TypeASCII || len(e.Value) == 0 {
		return ""
	}
	// Strip trailing NUL bytes.
	v := e.Value
	for len(v) > 0 && v[len(v)-1] == 0 {
		v = v[:len(v)-1]
	}
	return string(v)
}

// Uint16 decodes the first SHORT value.
func (e *IFDEntry) Uint16() uint16 {
	if (e.Type != TypeShort && e.Type != TypeSShort) || len(e.Value) < 2 {
		return 0
	}
	return e.byteOrder.Uint16(e.Value)
}

// Uint32 decodes the first LONG value.
func (e *IFDEntry) Uint32() uint32 {
	if (e.Type != TypeLong && e.Type != TypeSLong) || len(e.Value) < 4 {
		return 0
	}
	return e.byteOrder.Uint32(e.Value)
}

// Rational decodes the i-th RATIONAL value as [numerator, denominator].
// Returns [0, 0] on out-of-range access.
func (e *IFDEntry) Rational(i int) [2]uint32 {
	if e.Type != TypeRational && e.Type != TypeSRational {
		return [2]uint32{}
	}
	off := i * 8
	if off+8 > len(e.Value) {
		return [2]uint32{}
	}
	return [2]uint32{
		e.byteOrder.Uint32(e.Value[off:]),
		e.byteOrder.Uint32(e.Value[off+4:]),
	}
}

// --- helpers used by encode ---

// filterEntries returns a copy of ifd.Entries with the given tags removed.
func filterEntries(ifd *IFD, exclude ...TagID) []IFDEntry {
	if ifd == nil {
		return nil
	}
	excl := make(map[TagID]bool, len(exclude))
	for _, t := range exclude {
		excl[t] = true
	}
	result := make([]IFDEntry, 0, len(ifd.Entries))
	for _, entry := range ifd.Entries {
		if !excl[entry.Tag] {
			result = append(result, entry)
		}
	}
	return result
}

// sortEntries sorts entries by tag ID in ascending order (TIFF §7 requirement).
func sortEntries(entries []IFDEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Tag < entries[j].Tag
	})
}

// ifdTotalSize returns the total bytes occupied by the serialised IFD block:
// 2 (entry count) + len(entries)*12 (entry list) + 4 (next-IFD pointer) + value area.
func ifdTotalSize(entries []IFDEntry) uint32 {
	sz := uint32(2 + len(entries)*12 + 4)
	for _, e := range entries {
		ts := typeSize(e.Type)
		if ts == 0 {
			continue
		}
		total := uint64(ts) * uint64(e.Count)
		if total > 4 {
			sz += uint32(total)
		}
	}
	return sz
}

// writeIFD appends the serialised IFD block to out and returns the extended slice.
// startOff is the absolute file offset at which the IFD block begins (used to
// compute value-area offsets).
func writeIFD(out []byte, entries []IFDEntry, order binary.ByteOrder, startOff uint32) []byte {
	n := len(entries)
	// value area begins right after: 2 (count) + n*12 (entries) + 4 (next-IFD).
	valueOff := startOff + uint32(2+n*12+4)

	countB := make([]byte, 2)
	order.PutUint16(countB, uint16(n))
	out = append(out, countB...)

	entryBuf := make([]byte, n*12)
	var valueArea []byte
	curOff := valueOff

	for i, e := range entries {
		p := i * 12
		order.PutUint16(entryBuf[p:], uint16(e.Tag))
		order.PutUint16(entryBuf[p+2:], uint16(e.Type))
		order.PutUint32(entryBuf[p+4:], e.Count)

		ts := typeSize(e.Type)
		total := uint64(ts) * uint64(e.Count)

		if ts == 0 || total <= 4 {
			// Inline value: copy into the 4-byte field (TIFF §2).
			copy(entryBuf[p+8:p+12], e.Value)
		} else {
			order.PutUint32(entryBuf[p+8:], curOff)
			valueArea = append(valueArea, e.Value...)
			curOff += uint32(len(e.Value))
		}
	}

	out = append(out, entryBuf...)
	out = append(out, 0, 0, 0, 0) // next-IFD pointer = 0
	out = append(out, valueArea...)
	return out
}
