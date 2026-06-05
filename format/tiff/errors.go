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
