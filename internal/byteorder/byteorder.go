// Package byteorder provides zero-allocation big-endian and little-endian
// integer reads from byte slices. These are used in all hot-path parsers
// instead of encoding/binary.Read, which uses reflection and allocates.
package byteorder

import "encoding/binary"

// Uint16LE reads a little-endian uint16 from b[off:off+2].
// Panics if b is too short.
func Uint16LE(b []byte, off int) uint16 {
	return binary.LittleEndian.Uint16(b[off:])
}

// Uint16BE reads a big-endian uint16 from b[off:off+2].
func Uint16BE(b []byte, off int) uint16 {
	return binary.BigEndian.Uint16(b[off:])
}

// Uint32LE reads a little-endian uint32 from b[off:off+4].
func Uint32LE(b []byte, off int) uint32 {
	return binary.LittleEndian.Uint32(b[off:])
}

// Uint32BE reads a big-endian uint32 from b[off:off+4].
func Uint32BE(b []byte, off int) uint32 {
	return binary.BigEndian.Uint32(b[off:])
}

// Uint16 reads a uint16 using the given byte order.
func Uint16(b []byte, off int, order binary.ByteOrder) uint16 {
	return order.Uint16(b[off:])
}

// Uint32 reads a uint32 using the given byte order.
func Uint32(b []byte, off int, order binary.ByteOrder) uint32 {
	return order.Uint32(b[off:])
}
