package heif

import "errors"

// ErrMaxNestingDepth is returned when findBox exceeds the maximum recursive nesting depth.
var ErrMaxNestingDepth = errors.New("heif: findBox: exceeded maximum nesting depth")
