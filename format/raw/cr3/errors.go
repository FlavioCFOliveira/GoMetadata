package cr3

import "errors"

// ErrNoMoovBox is returned when the CR3 container does not contain a moov box.
var ErrNoMoovBox = errors.New("cr3: no moov box found")
