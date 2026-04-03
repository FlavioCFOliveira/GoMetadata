// Package exif implements an EXIF/TIFF parser and writer.
//
// Compliance: CIPA DC-008-2023 / JEITA CP-3451 (EXIF 3.0) and TIFF 6.0.
// Spec citations in comments reference the CIPA document as "EXIF §<section>"
// and the TIFF 6.0 spec as "TIFF §<section>".
package exif

import (
	"encoding/binary"
	"fmt"
	"time"
)

// EXIF holds the parsed contents of an EXIF block.
// IFD0, ExifIFD, GPSIFD, and InteropIFD are the standard IFD subtrees.
// MakerNote is populated only when a recognised manufacturer is detected.
type EXIF struct {
	ByteOrder    binary.ByteOrder
	IFD0         *IFD
	ExifIFD      *IFD
	GPSIFD       *IFD
	InteropIFD   *IFD
	MakerNote    []byte // raw MakerNote bytes
	MakerNoteIFD *IFD   // parsed MakerNote IFD; nil when parsing is unsupported for this make
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
			// MakerNote (EXIF §4.6.5, tag 0x927C) — raw bytes; parse if make is known.
			if mn := sub.Get(TagMakerNote); mn != nil {
				e.MakerNote = mn.Value
				// Parse the MakerNote IFD when the make is recognised.
				if makeEntry := ifd0.Get(TagMake); makeEntry != nil {
					e.MakerNoteIFD = parseMakerNoteIFD(mn.Value, makeEntry.String(), order)
				}
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

// DateTimeOriginal returns the original capture date/time from ExifIFD tag 0x9003
// (DateTimeOriginal, EXIF §4.6.5). The timezone offset from tag 0x9011
// (OffsetTimeOriginal, EXIF 2.31+) is applied when present; otherwise UTC is assumed.
func (e *EXIF) DateTimeOriginal() (time.Time, bool) {
	if e == nil || e.ExifIFD == nil {
		return time.Time{}, false
	}
	entry := e.ExifIFD.Get(TagDateTimeOriginal)
	if entry == nil {
		return time.Time{}, false
	}
	s := entry.String()
	if s == "" {
		return time.Time{}, false
	}

	// Try to read OffsetTimeOriginal for timezone (EXIF 2.31+, tag 0x9011).
	loc := time.UTC
	if off := e.ExifIFD.Get(TagOffsetTimeOriginal); off != nil {
		if tzStr := off.String(); tzStr != "" {
			if tz, err := parseExifTZ(tzStr); err == nil {
				loc = tz
			}
		}
	}

	// EXIF datetime format: "YYYY:MM:DD HH:MM:SS" (EXIF §4.6.5).
	t, err := time.ParseInLocation("2006:01:02 15:04:05", s, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// parseExifTZ parses an EXIF offset string such as "+02:00" or "-05:00" into
// a *time.Location. Returns an error if the format is not recognised.
func parseExifTZ(s string) (*time.Location, error) {
	t, err := time.Parse("-07:00", s)
	if err != nil {
		return nil, err
	}
	_, offset := t.Zone()
	return time.FixedZone(s, offset), nil
}

// ExposureTime returns the exposure time as a rational [numerator, denominator]
// from ExifIFD tag 0x829A (EXIF §4.6.5). ok is false when not present.
func (e *EXIF) ExposureTime() (num, den uint32, ok bool) {
	if e == nil || e.ExifIFD == nil {
		return 0, 0, false
	}
	entry := e.ExifIFD.Get(TagExposureTime)
	if entry == nil {
		return 0, 0, false
	}
	r := entry.Rational(0)
	return r[0], r[1], r[1] != 0
}

// FNumber returns the F-number (aperture) as a float64 from ExifIFD tag 0x829D
// (EXIF §4.6.5). ok is false when not present or denominator is zero.
func (e *EXIF) FNumber() (float64, bool) {
	if e == nil || e.ExifIFD == nil {
		return 0, false
	}
	entry := e.ExifIFD.Get(TagFNumber)
	if entry == nil {
		return 0, false
	}
	r := entry.Rational(0)
	if r[1] == 0 {
		return 0, false
	}
	return float64(r[0]) / float64(r[1]), true
}

// ISO returns the ISO speed rating from ExifIFD tag 0x8827 (EXIF §4.6.5).
// ok is false when not present.
func (e *EXIF) ISO() (uint, bool) {
	if e == nil || e.ExifIFD == nil {
		return 0, false
	}
	entry := e.ExifIFD.Get(TagISOSpeedRatings)
	if entry == nil {
		return 0, false
	}
	return uint(entry.Uint16()), true
}

// FocalLength returns the focal length in millimetres from ExifIFD tag 0x920A
// (EXIF §4.6.5). ok is false when not present or denominator is zero.
func (e *EXIF) FocalLength() (float64, bool) {
	if e == nil || e.ExifIFD == nil {
		return 0, false
	}
	entry := e.ExifIFD.Get(TagFocalLength)
	if entry == nil {
		return 0, false
	}
	r := entry.Rational(0)
	if r[1] == 0 {
		return 0, false
	}
	return float64(r[0]) / float64(r[1]), true
}

// LensModel returns the lens model string from ExifIFD tag 0xA434
// (LensModel, EXIF §4.6.5). Returns an empty string when not present.
func (e *EXIF) LensModel() string {
	if e == nil || e.ExifIFD == nil {
		return ""
	}
	entry := e.ExifIFD.Get(TagLensModel)
	if entry == nil {
		return ""
	}
	return entry.String()
}

// Orientation returns the image orientation from IFD0 tag 0x0112
// (EXIF §4.6.4 Table 3). ok is false when not present.
func (e *EXIF) Orientation() (uint16, bool) {
	if e == nil {
		return 0, false
	}
	entry := e.IFD0.Get(TagOrientation)
	if entry == nil {
		return 0, false
	}
	return entry.Uint16(), true
}

// ImageSize returns the pixel dimensions of the full-resolution image from
// ExifIFD tags 0xA002/0xA003 (PixelXDimension / PixelYDimension, EXIF §4.6.5).
// ok is false when not present.
func (e *EXIF) ImageSize() (width, height uint32, ok bool) {
	if e == nil || e.ExifIFD == nil {
		return 0, 0, false
	}
	xEntry := e.ExifIFD.Get(TagPixelXDimension)
	yEntry := e.ExifIFD.Get(TagPixelYDimension)
	if xEntry == nil || yEntry == nil {
		return 0, 0, false
	}
	// PixelXDimension may be SHORT or LONG (EXIF §4.6.5).
	var w, h uint32
	switch xEntry.Type {
	case TypeShort:
		w = uint32(xEntry.Uint16())
	default:
		w = xEntry.Uint32()
	}
	switch yEntry.Type {
	case TypeShort:
		h = uint32(yEntry.Uint16())
	default:
		h = yEntry.Uint32()
	}
	return w, h, w > 0 && h > 0
}

// Creator returns the artist / creator string from IFD0 tag 0x013B
// (Artist, EXIF §4.6.4 Table 3).
func (e *EXIF) Creator() string {
	if e == nil {
		return ""
	}
	entry := e.IFD0.Get(TagArtist)
	if entry == nil {
		return ""
	}
	return entry.String()
}
