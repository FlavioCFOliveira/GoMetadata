package iptc

import "errors"

// ErrDatasetValueTooLarge is returned by Encode when a Dataset.Value is too
// long to be represented by the IIM 4.2 §1.6.2 extended-length encoding,
// whose length field is a 4-byte big-endian unsigned integer (max 0xFFFFFFFF
// bytes, i.e. 4 GiB - 1). Encode never emits a corrupt, silently-wrapped
// length field: constructing a Dataset with an oversized Value directly
// (bypassing Parse's aggregate/per-dataset size caps) and passing it to
// Encode returns this error instead.
var ErrDatasetValueTooLarge = errors.New("iptc: dataset value exceeds the maximum representable extended-length size (4 GiB - 1)")
