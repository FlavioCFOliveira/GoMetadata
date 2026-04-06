package orf

import "errors"

// ErrInvalidMagic is returned when the input does not begin with the Olympus ORF magic bytes.
var ErrInvalidMagic = errors.New("orf: invalid magic bytes")

// ErrOutputTooShort is returned when the reconstructed ORF output is shorter than expected.
var ErrOutputTooShort = errors.New("orf: output too short")
