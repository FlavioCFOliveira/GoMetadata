// Package bmff provides a minimal ISO Base Media File Format (ISO 14496-12)
// box reader used by the heif and cr3 container packages.
package bmff

import (
	"encoding/binary"
	"io"
)

// Box represents a parsed ISOBMFF box header.
type Box struct {
	// Size is the total box size in bytes including the header.
	// A Size of 0 means the box extends to EOF.
	Size uint64
	// Type is the four-character box type code.
	Type [4]byte
	// Offset is the position of the first data byte (after the header)
	// within the stream.
	Offset int64
	// DataSize is Size minus the header length.
	DataSize uint64
}

// TypeString returns the box type as a string.
func (b *Box) TypeString() string {
	return string(b.Type[:])
}

// ReadBox reads the next box header from r at the current position.
func ReadBox(r io.ReadSeeker) (Box, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Box{}, err
	}
	size := uint64(binary.BigEndian.Uint32(hdr[:4]))
	var box Box
	copy(box.Type[:], hdr[4:8])

	headerLen := uint64(8)
	if size == 1 {
		// Extended size: next 8 bytes hold the actual 64-bit size.
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return Box{}, err
		}
		size = binary.BigEndian.Uint64(ext[:])
		headerLen = 16
	}
	box.Size = size
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return Box{}, err
	}
	box.Offset = pos
	if size == 0 {
		box.DataSize = 0 // extends to EOF
	} else {
		box.DataSize = size - headerLen
	}
	return box, nil
}

// SkipBox advances r past the data portion of box.
func SkipBox(r io.ReadSeeker, box Box) error {
	if box.Size == 0 {
		_, err := r.Seek(0, io.SeekEnd)
		return err
	}
	_, err := r.Seek(box.Offset+int64(box.DataSize), io.SeekStart)
	return err
}
