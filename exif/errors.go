package exif

import "errors"

// ErrNilEXIF is returned when attempting to encode a nil EXIF value.
var ErrNilEXIF = errors.New("exif: cannot encode nil EXIF")

// ErrBigTIFFEncodeNotSupported was returned by Encode when the EXIF was parsed
// from a BigTIFF source (magic 0x002B).  Before task #264, the encoder only
// supported classic TIFF (magic 0x002A, 32-bit offsets); silently downgrading
// a BigTIFF source to classic TIFF would have truncated every 64-bit offset to
// 32 bits and silently corrupted any file whose structures reside above 4 GiB.
//
// As of task #264, Encode natively supports BigTIFF: when EXIF.BigTIFF is
// true, Encode emits a conformant 16-byte BigTIFF header, 20-byte IFD entries,
// and 64-bit offsets throughout (BigTIFF spec §2, Aware Systems / libtiff).
// This sentinel is preserved for API compatibility; it is no longer returned
// by Encode under normal conditions.
//
// Deprecated: BigTIFF write is now supported. This error is retained for API
// compatibility and may be removed in a future major release.
//
// BigTIFF spec §2 (Aware Systems / libtiff); audit finding #107; task #264.
var ErrBigTIFFEncodeNotSupported = errors.New("exif: encoding a BigTIFF source as classic TIFF is not supported; BigTIFF write is not yet implemented")

// ErrBigTIFFPointerOverflow is returned by Encode when a BigTIFF-sourced EXIF
// (EXIF.BigTIFF == true) would need to encode a sub-IFD pointer
// (ExifIFDPointer 0x8769, GPSIFDPointer 0x8825, InteropIFDPointer 0xA005) or a
// thumbnail pointer (JPEGInterchangeFormat 0x0201 / JPEGInterchangeFormatLength
// 0x0202) at an absolute stream offset that does not fit in 32 bits.
//
// These five tags are fixed by the EXIF specification to type LONG (a 4-byte
// value) regardless of container (EXIF §4.6.3, §4.5.5); BigTIFF write does not
// promote them to the 64-bit IFD8/LONG8 types because doing so would deviate
// from the convention used by every known BigTIFF writer (libtiff / tiffcp)
// and by this package's own BigTIFF reader (see readBigTIFFSubIFDOffset,
// exif/ifd.go, which accepts TypeLong as the primary encoding). When the true
// target offset does not fit in 32 bits, Encode returns this error instead of
// silently truncating the pointer — a truncated pointer would corrupt the
// file by making the sub-IFD or thumbnail unreachable (or reachable at the
// wrong location).
//
// BigTIFF spec §2 (Aware Systems / libtiff); EXIF §4.6.3, §4.5.5; task #264.
var ErrBigTIFFPointerOverflow = errors.New("exif: BigTIFF sub-IFD or thumbnail pointer offset exceeds 32 bits; these tags are fixed EXIF LONG (4-byte) fields regardless of container")

// ErrBigTIFFEncodeSizeExceeded is returned by Encode when the total encoded
// size of a BigTIFF EXIF payload would exceed maxBigTIFFEncodeSize
// (exif/write.go).
//
// Classic-TIFF Encode bounds its pre-allocation implicitly: its offset fields
// are 32 bits wide, so ifdTotalSize saturates at math.MaxUint32 and no larger
// buffer can ever be requested. BigTIFF's 64-bit offset fields have no such
// natural ceiling, so an EXIF struct with a manually constructed, pathological
// IFDEntry.Count could otherwise direct Encode to attempt an unbounded
// make([]byte, 0, N) allocation — a memory-exhaustion DoS (CWE-400).
// maxBigTIFFEncodeSize is the explicit, documented, test-overridable sanity
// ceiling that replaces the (inapplicable) MaxUint32 saturation used by the
// classic path.
//
// BigTIFF spec §2 (Aware Systems / libtiff); task #264.
var ErrBigTIFFEncodeSizeExceeded = errors.New("exif: encoded BigTIFF size exceeds the sanity ceiling")
