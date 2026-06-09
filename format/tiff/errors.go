package tiff

import "errors"

// ErrFileTooShort is returned when the TIFF input is too short to contain a valid header.
var ErrFileTooShort = errors.New("tiff: file too short")

// ErrInvalidByteOrder is returned when the byte order marker is neither "II" nor "MM".
var ErrInvalidByteOrder = errors.New("tiff: invalid byte order marker")

// ErrUnsupportedMagic is returned when the TIFF magic number is neither 0x002A
// (classic TIFF) nor 0x002B (BigTIFF). Any other value indicates a corrupted or
// unsupported file.
var ErrUnsupportedMagic = errors.New("tiff: unsupported magic number")

// ErrCR2OutputTooShort is returned when the assembled CR2 output is too short
// to receive the CR2 marker insertion (minimum 8 bytes required).
var ErrCR2OutputTooShort = errors.New("tiff: CR2 output too short to insert marker")

// ErrCR2IFD0OutOfBounds is returned when the IFD0 offset computed after CR2
// marker insertion points beyond the end of the assembled output.
var ErrCR2IFD0OutOfBounds = errors.New("tiff: CR2 IFD0 offset out of bounds after marker insertion")

// ErrOffsetOverflow is returned when the computed imageStart offset (ifdEnd +
// SubIFD sizes + format-specific blocks) overflows uint32. This can only occur
// for synthetic TIFF skeletons whose combined IFD+SubIFD region exceeds 4 GiB;
// real-world files are orders of magnitude smaller.
//
// TIFF 6.0 §2: all offsets are 32-bit unsigned integers, so values > 4 GiB are
// not representable and the file would be unreadable.
var ErrOffsetOverflow = errors.New("tiff: imageStart offset overflows uint32 (file would exceed 4 GiB limit)")

// ErrImageBlockOverflow is returned when at least one image-data block would be
// placed at offset math.MaxUint32 (the sentinel set by assignNewOffsets on
// uint32 overflow). Writing 0xFFFFFFFF as a StripOffset would produce an
// unreadable file, so the write is aborted instead.
//
// TIFF 6.0 §2: StripOffsets values are uint32 absolute file offsets; the value
// 0xFFFFFFFF is used as an overflow sentinel internally and must never appear in
// a written output file.
var ErrImageBlockOverflow = errors.New("tiff: image block offset overflows uint32 (cumulative image data exceeds 4 GiB)")
