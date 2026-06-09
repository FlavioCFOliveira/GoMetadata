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

// ErrCorruptXMP is returned when the rawXMP bytes passed to any TIFF Inject
// entry point are an internal JPEG extended-XMP wire-frame payload rather than
// a valid XMP packet. The wire-frame encoding (magic 0x00 'X' 'M' 'P' 'E' 'X'
// 'T' 0x00) is specific to JPEG containers and cannot be stored in TIFF tag
// 0x02BC. Callers that forward rawXMP from jpeg.ExtractWithWire to a TIFF
// injector must use the reassembled rawXMP (not rawXMPWire).
//
// Task #118 regression: the JPEG wire-frame guard was added to PNG, WebP, and
// HEIF inject paths; TIFF inject paths were not covered. This error is the
// TIFF equivalent. Mirrors format/png.ErrCorruptXMP, format/webp.ErrCorruptXMP,
// and format/heif.ErrCorruptXMP.
var ErrCorruptXMP = errors.New("tiff: corrupt or invalid XMP data")

// ErrSubIFDCountMismatch is returned by patchSubIFDPointers when the number of
// subIFDInfo values passed does not match the 0x014A entry count in the
// re-encoded TIFF stream. The function patches as many slots as possible
// (min(declared, actual)) and returns this error so the caller can detect the
// discrepancy rather than silently writing a partially-patched SubIFD array.
//
// Task #116 regression: the mismatch previously passed silently; now it surfaces
// as an explicit error so callers can log or propagate it.
var ErrSubIFDCountMismatch = errors.New("tiff: 0x014A SubIFDs count mismatch between declared entry and subIFD slice")
