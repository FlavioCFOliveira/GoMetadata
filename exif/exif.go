// Package exif implements an EXIF/TIFF parser and writer.
//
// Compliance: CIPA DC-008-2023 / JEITA CP-3451 (EXIF 3.0) and TIFF 6.0.
// Spec citations in comments reference the CIPA document as "EXIF §<section>"
// and the TIFF 6.0 spec as "TIFF §<section>".
package exif

import (
	"encoding/binary"
	"fmt"
)

// EXIF holds the parsed contents of an EXIF block.
// IFD0, ExifIFD, GPSIFD, and InteropIFD are the standard IFD subtrees.
// MakerNote is populated only when a recognised manufacturer is detected.
type EXIF struct {
	ByteOrder  binary.ByteOrder
	IFD0       *IFD
	ExifIFD    *IFD
	GPSIFD     *IFD
	InteropIFD *IFD
	MakerNote  []byte // raw, unparsed unless exif/makernote is used
}

// Parse parses a raw EXIF block starting at the TIFF header ("II" or "MM").
// b must be the complete EXIF payload (after the "Exif\x00\x00" prefix is
// stripped by the container layer).
func Parse(b []byte) (*EXIF, error) {
	if len(b) < 8 {
		return nil, fmt.Errorf("exif: data too short (%d bytes)", len(b))
	}

	// Determine byte order from the TIFF header (TIFF §2).
	var order binary.ByteOrder
	switch {
	case b[0] == 'I' && b[1] == 'I':
		order = binary.LittleEndian
	case b[0] == 'M' && b[1] == 'M':
		order = binary.BigEndian
	default:
		return nil, fmt.Errorf("exif: invalid byte order marker %q", b[:2])
	}

	// TIFF magic number 0x002A (TIFF §2).
	magic := order.Uint16(b[2:])
	if magic != 0x002A {
		return nil, fmt.Errorf("exif: invalid TIFF magic 0x%04X", magic)
	}

	// Offset to IFD0 (TIFF §2).
	ifd0Off := order.Uint32(b[4:])

	e := &EXIF{ByteOrder: order}

	ifd0, err := traverse(b, ifd0Off, order)
	if err != nil {
		return nil, err
	}
	e.IFD0 = ifd0

	// ExifIFD sub-IFD pointer (EXIF §4.6.3, tag 0x8769).
	if ptr := ifd0.Get(TagExifIFDPointer); ptr != nil && len(ptr.Value) >= 4 {
		off := order.Uint32(ptr.Value)
		if sub, subErr := traverse(b, off, order); subErr == nil {
			e.ExifIFD = sub
			// MakerNote (EXIF §4.6.5, tag 0x927C) — raw bytes only.
			if mn := sub.Get(TagMakerNote); mn != nil {
				e.MakerNote = mn.Value
			}
			// Interoperability IFD pointer (EXIF §4.6.3, tag 0xA005).
			if iptr := sub.Get(TagInteropIFDPointer); iptr != nil && len(iptr.Value) >= 4 {
				ioff := order.Uint32(iptr.Value)
				if isub, ierr := traverse(b, ioff, order); ierr == nil {
					e.InteropIFD = isub
				}
			}
		}
	}

	// GPS IFD pointer (EXIF §4.6.3, tag 0x8825).
	if ptr := ifd0.Get(TagGPSIFDPointer); ptr != nil && len(ptr.Value) >= 4 {
		off := order.Uint32(ptr.Value)
		if sub, subErr := traverse(b, off, order); subErr == nil {
			e.GPSIFD = sub
		}
	}

	return e, nil
}

// Encode serialises e back to a raw EXIF byte stream (TIFF header + IFDs).
func Encode(e *EXIF) ([]byte, error) {
	return encode(e)
}

// CameraModel returns the value of IFD0 tag 0x0110 (Model, EXIF §4.6.4 Table 3).
func (e *EXIF) CameraModel() string {
	if e == nil {
		return ""
	}
	entry := e.IFD0.Get(TagModel)
	if entry == nil {
		return ""
	}
	return entry.String()
}

// GPS returns decimal-degree coordinates from the GPS IFD.
func (e *EXIF) GPS() (lat, lon float64, ok bool) {
	if e == nil || e.GPSIFD == nil {
		return 0, 0, false
	}
	return parseGPS(e.GPSIFD)
}

// Copyright returns the value of IFD0 tag 0x8298 (Copyright, EXIF §4.6.4 Table 3).
func (e *EXIF) Copyright() string {
	if e == nil {
		return ""
	}
	entry := e.IFD0.Get(TagCopyright)
	if entry == nil {
		return ""
	}
	return entry.String()
}

// Caption returns the value of IFD0 tag 0x010E (ImageDescription, EXIF §4.6.4 Table 3).
func (e *EXIF) Caption() string {
	if e == nil {
		return ""
	}
	entry := e.IFD0.Get(TagImageDescription)
	if entry == nil {
		return ""
	}
	return entry.String()
}
