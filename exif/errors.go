package exif

import "errors"

// ErrNilEXIF is returned when attempting to encode a nil EXIF value.
var ErrNilEXIF = errors.New("exif: cannot encode nil EXIF")
