package webp

import "errors"

// ErrNotWebP is returned when the input does not begin with a valid RIFF/WEBP header.
var ErrNotWebP = errors.New("webp: not a WebP file")

// ErrFileTooShort is returned when the WebP input is too short to contain a valid RIFF header.
var ErrFileTooShort = errors.New("webp: file too short")
