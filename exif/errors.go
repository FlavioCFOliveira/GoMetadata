package exif

import "errors"

// ErrNilEXIF is returned when attempting to encode a nil EXIF value.
var ErrNilEXIF = errors.New("exif: cannot encode nil EXIF")

// ErrBigTIFFEncodeNotSupported is returned by Encode when the EXIF was parsed
// from a BigTIFF source (magic 0x002B).  The encoder only supports classic TIFF
// (magic 0x002A, 32-bit offsets); silently downgrading a BigTIFF source to
// classic TIFF would truncate every 64-bit offset to 32 bits and silently
// corrupt any file whose structures reside above 4 GiB.
//
// To write metadata into a BigTIFF container, a native BigTIFF encoder is
// required (not yet implemented).  Read and all inspection operations continue
// to work correctly on BigTIFF sources; only write is blocked.
//
// BigTIFF spec §2 (Aware Systems / libtiff); audit finding #107.
var ErrBigTIFFEncodeNotSupported = errors.New("exif: encoding a BigTIFF source as classic TIFF is not supported; BigTIFF write is not yet implemented")
