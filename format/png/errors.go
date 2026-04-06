package png

import "errors"

// ErrInvalidSignature is returned when the input does not begin with the PNG magic bytes.
var ErrInvalidSignature = errors.New("png: invalid signature")

// ErrUnsupportedCompression is returned when a compressed chunk uses an unknown compression method.
var ErrUnsupportedCompression = errors.New("png: unsupported compression method")
