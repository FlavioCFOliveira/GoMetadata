package tiff

import "errors"

// ErrFileTooShort is returned when the TIFF input is too short to contain a valid header.
var ErrFileTooShort = errors.New("tiff: file too short")

// ErrInvalidByteOrder is returned when the byte order marker is neither "II" nor "MM".
var ErrInvalidByteOrder = errors.New("tiff: invalid byte order marker")
