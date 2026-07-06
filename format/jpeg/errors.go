package jpeg

import "errors"

// ErrNotJPEG is returned when the input does not begin with the JPEG SOI marker.
var ErrNotJPEG = errors.New("jpeg: not a JPEG file")

// ErrEXIFPayloadTooLarge is returned when the EXIF payload exceeds the APP1 segment limit.
var ErrEXIFPayloadTooLarge = errors.New("jpeg: EXIF payload exceeds APP1 segment limit")

// ErrIPTCPayloadTooLarge is returned when the IPTC IRB payload exceeds the APP13 segment limit.
var ErrIPTCPayloadTooLarge = errors.New("jpeg: IPTC IRB payload exceeds APP13 segment limit")

// ErrXMPStubTooLarge is returned when the generated extended XMP main stub exceeds the APP1 limit.
var ErrXMPStubTooLarge = errors.New("jpeg: extended XMP main stub exceeds APP1 limit")

// ErrInvalidMarkerPrefix is returned when a segment header does not begin with 0xFF.
var ErrInvalidMarkerPrefix = errors.New("jpeg: invalid marker prefix")

// ErrInvalidMarkerLength is returned when a marker length field is less than 2.
var ErrInvalidMarkerLength = errors.New("jpeg: marker has invalid length")

// ErrSegmentTooLarge is returned when a segment payload would exceed the 65535-byte APP segment limit.
var ErrSegmentTooLarge = errors.New("jpeg: segment payload exceeds APP segment limit")

// ErrExtendedXMPTruncated is returned (or signalled via the truncated flag) when
// the total accumulated size of extended XMP chunks for a single GUID exceeds
// maxExtendedXMPTotal. The assembled payload is truncated at the cap.
var ErrExtendedXMPTruncated = errors.New("jpeg: extended XMP payload exceeds size limit and was truncated")

// ErrIRBDataSizeTooLarge is returned by parseIRBEntry when a 4-byte data-size
// field in a Photoshop IRB entry names a value that cannot be safely represented
// as a Go int on the current platform, or that would exceed the containing buffer.
var ErrIRBDataSizeTooLarge = errors.New("jpeg: IRB entry data size exceeds buffer")

// maxFileSize is the upper bound on the total number of bytes this package will
// read from an io.Reader in a single Extract or Inject call. Every per-APP-segment
// payload is already bounded to 65535 bytes by the JPEG 16-bit length field
// (ISO/IEC 10918-1 §B.1.1.4), so this package carries no per-segment amplification
// risk; this cap exists purely for defense-in-depth uniformity with the sibling
// container packages (webp, tiff, heif, dng, cr2, cr3, nef, arw, orf, rw2), which
// all enforce the identical #140-style aggregate cap.
//
// Unlike those packages, format/jpeg does not buffer the whole file via
// io.ReadAll: it streams the marker sequence incrementally so that Extract and
// Inject never hold more than one scratch buffer's worth of the file in memory
// at a time. The cap is instead enforced by countingReader (limits.go), which
// wraps the caller's io.ReadSeeker and rejects reads once the cumulative byte
// count since the last Seek exceeds maxFileSize.
//
// Real-world JPEG files are well under 100 MiB; 256 MiB gives ample headroom
// while bounding worst-case total input to a predictable, safe value.
//
// Declared as a var (not a const) so that tests can lower it temporarily to
// verify the size-cap path without allocating 256 MiB of memory.
//
// #262 fix (defense-in-depth): add the project-wide aggregate input-size cap to
// the one container package (format/jpeg) that lacked it, so the codebase's
// security posture is uniform across every supported format.
var maxFileSize int64 = 256 << 20 //nolint:gochecknoglobals // test-overridable cap; never mutated in production paths

// ErrFileTooLarge is returned when the cumulative bytes read from the input
// (or the aggregate size of all Photoshop APP13 payloads collected during a
// single scan) exceeds maxFileSize. Callers can detect this specific condition
// with errors.Is.
var ErrFileTooLarge = errors.New("jpeg: input exceeds maximum file size (256 MiB)")
