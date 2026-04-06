package fujifilm

import "errors"

// ErrMakerNoteTooShort is returned when the MakerNote payload is shorter than required.
var ErrMakerNoteTooShort = errors.New("fujifilm: makernote too short")

// ErrInvalidMagic is returned when the MakerNote does not begin with the expected "FUJIFILM" prefix.
var ErrInvalidMagic = errors.New("fujifilm: invalid magic")
