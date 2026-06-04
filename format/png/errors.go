package png

import "errors"

// ErrInvalidSignature is returned when the input does not begin with the PNG magic bytes.
var ErrInvalidSignature = errors.New("png: invalid signature")

// ErrUnsupportedCompression is returned when a compressed chunk uses an unknown compression method.
var ErrUnsupportedCompression = errors.New("png: unsupported compression method")

// ErrChunkTooLarge is returned when a chunk's declared data length exceeds
// maxPNGChunkSize. This prevents a crafted 4-byte length field from triggering
// a multi-gigabyte allocation before any I/O takes place.
var ErrChunkTooLarge = errors.New("png: chunk size exceeds limit")

// ErrChunkCRCMismatch is returned when the CRC-32/IEEE of a metadata chunk's
// type and data fields does not match the stored 4-byte CRC trailer.
//
// Verification policy: CRC is checked only for the PNG chunk types that this
// library actually interprets — eXIf, iTXt, tEXt, zTXt, and IHDR. Pixel-data
// chunks (IDAT) and other pass-through chunks are forwarded without CRC
// computation; computing CRC over IDAT would add significant latency with no
// correctness benefit for a metadata-only library.
//
// A mismatch on a verified chunk indicates either file corruption or deliberate
// tampering; silently accepting corrupt chunks would propagate bad EXIF/XMP
// data to the caller without any signal. Callers that must tolerate corrupt
// metadata for recovery purposes can detect this error with
// errors.Is(err, ErrChunkCRCMismatch) and choose to proceed.
var ErrChunkCRCMismatch = errors.New("png: chunk CRC mismatch")

// ErrCorruptXMP is returned when the rawXMP bytes passed to Inject are not a
// valid XMP packet. This includes the case where a caller accidentally passes an
// internal JPEG extended-XMP wire-frame to the PNG injector; the wire-frame is a
// JPEG-only internal encoding that cannot be stored as a PNG XMP chunk.
var ErrCorruptXMP = errors.New("png: corrupt or invalid XMP data")
