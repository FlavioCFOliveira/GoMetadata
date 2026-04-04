// Package riff provides a minimal RIFF chunk reader used by the webp container package.
package riff

import (
	"encoding/binary"
	"io"
)

// Chunk represents a RIFF chunk header.
type Chunk struct {
	// FourCC is the four-character chunk identifier.
	FourCC [4]byte
	// Size is the data size in bytes (excluding the 8-byte header).
	// Chunks with odd Size have a 1-byte padding byte that is not counted.
	Size uint32
	// Offset is the position of the first data byte within the stream.
	Offset int64
}

// FourCCString returns the chunk identifier as a string.
func (c *Chunk) FourCCString() string {
	return string(c.FourCC[:])
}

// ReadChunk reads the next RIFF chunk header from r.
func ReadChunk(r io.ReadSeeker) (Chunk, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Chunk{}, err
	}
	var c Chunk
	copy(c.FourCC[:], hdr[:4])
	c.Size = binary.LittleEndian.Uint32(hdr[4:])
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return Chunk{}, err
	}
	c.Offset = pos
	return c, nil
}

// SkipChunk advances r past the data (and any padding byte) of c.
func SkipChunk(r io.ReadSeeker, c Chunk) error {
	skip := int64(c.Size)
	if c.Size%2 != 0 {
		skip++ // padding byte
	}
	_, err := r.Seek(c.Offset+skip, io.SeekStart)
	return err
}
