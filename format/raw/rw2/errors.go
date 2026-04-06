package rw2

import "errors"

// ErrInvalidMagic is returned when the input does not begin with the Panasonic RW2 magic bytes.
var ErrInvalidMagic = errors.New("rw2: invalid magic bytes")

// ErrOutputTooShort is returned when the reconstructed RW2 output is shorter than expected.
var ErrOutputTooShort = errors.New("rw2: output too short")
