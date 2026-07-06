package tiff

import "errors"

// maxFileSize is the upper bound on the total number of bytes this package will
// read from an io.Reader in a single Extract or Inject call. Reads via
// io.ReadAll are wrapped with io.LimitReader(r, maxFileSize+1); if the reader
// delivers more bytes than this limit the operation is aborted with
// ErrFileTooLarge before any allocation proportional to file size is retained.
//
// Real-world TIFF/DNG/NEF/ARW/CR2/ORF/RW2 files are well under 100 MiB;
// 256 MiB gives ample headroom for future sensor improvements while bounding
// worst-case heap allocation to a predictable, safe value.
//
// Declared as a var (not a const) so that tests can lower it temporarily to
// verify the OOM-guard path without allocating 256 MiB of memory.
//
// #140 fix: cap uncapped io.ReadAll calls to prevent OOM on oversized or
// infinite streaming readers.
var maxFileSize int64 = 256 << 20 //nolint:gochecknoglobals // test-overridable cap; never mutated in production paths

// ErrFileTooLarge is returned when the input exceeds maxFileSize. This prevents
// a streaming or adversarially large reader from causing unbounded heap
// allocation. Callers can detect this specific condition with errors.Is.
var ErrFileTooLarge = errors.New("tiff: input exceeds maximum file size (256 MiB)")

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

// ErrTooManyImageBlocks is returned when a single StripOffsets/StripByteCounts
// or TileOffsets/TileByteCounts entry declares more elements than
// maxImageBlocksPerOffsetEntry, when a 0x014A SubIFDs entry declares more
// pointers than maxSubIFDsPerEntry, or when the cumulative number of image
// blocks and SubIFD entries enumerated across a single relocate call exceeds
// maxAggregateImageBlocks.
//
// GM-W1 (CWE-770 uncontrolled resource consumption / CWE-405 asymmetric
// resource consumption): each element in a StripOffsets/StripByteCounts,
// TileOffsets/TileByteCounts, or 0x014A array costs only 2-4 bytes of file
// content but drives one heap-allocated *imageBlock (or *subIFDInfo) in the
// write-path relocator. Without a bound, a crafted file well under this
// package's 256 MiB file-size cap can declare an implausibly large element
// count and drive multi-gigabyte allocation before any real image data is
// ever copied. See relocate.go (maxImageBlocksPerOffsetEntry,
// maxSubIFDsPerEntry, maxAggregateImageBlocks, imageBlockBudget) for the cap
// constants and the full rationale.
var ErrTooManyImageBlocks = errors.New("tiff: too many image blocks or SubIFD entries declared for a single write")
