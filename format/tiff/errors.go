package tiff

import "errors"

// ErrFileTooShort is returned when the TIFF input is too short to contain a valid header.
var ErrFileTooShort = errors.New("tiff: file too short")

// ErrInvalidByteOrder is returned when the byte order marker is neither "II" nor "MM".
var ErrInvalidByteOrder = errors.New("tiff: invalid byte order marker")

// ErrUnsupportedMagic is returned when the TIFF magic number is neither 0x002A
// (classic TIFF) nor 0x002B (BigTIFF). Any other value indicates a corrupted or
// unsupported file.
var ErrUnsupportedMagic = errors.New("tiff: unsupported magic number")

// ErrCR2OutputTooShort is returned when the assembled CR2 output is too short
// to receive the CR2 marker insertion (minimum 8 bytes required).
var ErrCR2OutputTooShort = errors.New("tiff: CR2 output too short to insert marker")

// ErrCR2IFD0OutOfBounds is returned when the IFD0 offset computed after CR2
// marker insertion points beyond the end of the assembled output.
var ErrCR2IFD0OutOfBounds = errors.New("tiff: CR2 IFD0 offset out of bounds after marker insertion")
