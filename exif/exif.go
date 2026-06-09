// Package exif implements an EXIF/TIFF parser and writer.
//
// Compliance: CIPA DC-008-2023 / JEITA CP-3451 (EXIF 3.0) and TIFF 6.0.
// Spec citations in comments reference the CIPA document as "EXIF §<section>"
// and the TIFF 6.0 spec as "TIFF §<section>".
package exif

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/internal/metaerr"
)

// EXIF holds the parsed contents of an EXIF block.
// IFD0, ExifIFD, GPSIFD, and InteropIFD are the standard IFD subtrees.
// MakerNote is populated only when a recognised manufacturer is detected.
//
// MakerNoteOffset is the absolute TIFF-stream byte offset at which the raw
// MakerNote value begins in the original parsed buffer.  It is zero when
// MakerNote is nil or when the ExifIFD was not parsed from a byte stream (e.g.
// a freshly constructed EXIF with no MakerNote).  For classic TIFF files this
// field holds the full offset (which fits in uint32).  For BigTIFF files where
// the MakerNote resides above 4 GiB, MakerNoteOffset is truncated — use
// MakerNoteOffset64 instead, which always holds the full 64-bit value.
//
// MakerNoteOffset64 is the 64-bit counterpart: it is set for both classic TIFF
// and BigTIFF and is never truncated.  New callers should prefer MakerNoteOffset64.
// Existing callers that target classic TIFF (NEF, ARW, ORF — all of which have
// 32-bit file offsets) may safely continue using MakerNoteOffset.
//
// These fields are informational: callers can use them to detect whether Encode
// has moved the MakerNote to a different position.  Manufacturers that store
// MakerNote-internal offsets relative to the parent TIFF start (e.g. certain
// Nikon bodies) will produce stale internal offsets if the MakerNote moves;
// full offset rebasing is not yet implemented.  EXIF §4.6.5, tag 0x927C.
// BigTIFF spec §2 (Aware Systems / libtiff); fix for audit finding #142.
//
// BigTIFF is true when the EXIF was parsed from a BigTIFF source (magic 0x002B,
// 64-bit IFD offsets). When BigTIFF is true, Encode returns
// ErrBigTIFFEncodeNotSupported without emitting any bytes: downgrading a BigTIFF
// source to classic TIFF (32-bit offsets) would silently truncate every 64-bit
// offset and corrupt any file whose structures reside above 4 GiB.
// BigTIFF spec §2; audit finding #107.
type EXIF struct {
	ByteOrder         binary.ByteOrder
	IFD0              *IFD
	ExifIFD           *IFD
	GPSIFD            *IFD
	InteropIFD        *IFD
	MakerNote         []byte // raw MakerNote bytes
	MakerNoteIFD      *IFD   // parsed MakerNote IFD; nil when parsing is unsupported for this make
	MakerNoteOffset   uint32 // absolute TIFF-stream offset of the raw MakerNote value; 0 when absent; truncated for BigTIFF >4GiB
	MakerNoteOffset64 uint64 // 64-bit counterpart of MakerNoteOffset; never truncated; preferred for new callers
	// BigTIFF is set to true by Parse when the source magic is 0x002B (BigTIFF).
	// Encode returns ErrBigTIFFEncodeNotSupported for BigTIFF sources to prevent
	// silent 0x002A downgrade which would truncate all 64-bit offsets to 32 bits.
	// BigTIFF spec §2 (Aware Systems / libtiff); audit finding #107.
	BigTIFF bool

	// Warnings is a slice of diagnostic messages produced during parsing.
	// A non-nil Warnings slice does not indicate a parse failure; the EXIF
	// data is still usable. Each entry describes a recoverable anomaly such
	// as a truncated IFD entry count (#130), duplicate tag (#126), an OOL
	// value offset that aliases the IFD directory (#132), or an unreadable
	// next-IFD pointer (#131).
	//
	// The caller (gometadata.Read) converts each warning into a
	// *ParseSegmentError appended to Metadata.ParseWarnings so warnings are
	// visible at the top-level API without aborting parsing.
	Warnings []string
}

// ParseOption configures a Parse call.
type ParseOption func(*parseConfig)

type parseConfig struct {
	skipMakerNote bool
}

// SkipMakerNote skips parsing the manufacturer-specific MakerNote IFD.
// The raw MakerNote bytes (EXIF.MakerNote) are still retained for round-trip
// writes; only the decoded MakerNoteIFD is omitted. Use this when you do not
// need manufacturer extension tags and want to minimise parse cost on camera files.
func SkipMakerNote() ParseOption { return func(c *parseConfig) { c.skipMakerNote = true } }

// parseByteOrder reads the two-byte byte-order marker at b[0:2] and returns
// the corresponding binary.ByteOrder. Returns a CorruptMetadataError for any
// marker other than "II" (little-endian) or "MM" (big-endian).
// Caller must ensure len(b) >= 2.
//
// TIFF §2: "The byte order field contains the value 49 49h (II) for
// little-endian (Intel) byte order, or 4D 4Dh (MM) for big-endian (Motorola)".
func parseByteOrder(b []byte) (binary.ByteOrder, error) {
	switch {
	case b[0] == 'I' && b[1] == 'I':
		return binary.LittleEndian, nil
	case b[0] == 'M' && b[1] == 'M':
		return binary.BigEndian, nil
	default:
		return nil, &metaerr.CorruptMetadataError{
			Format: "EXIF",
			Reason: fmt.Sprintf("invalid byte order marker %q", b[:2]),
		}
	}
}

// parseExifSubIFDs traverses the ExifIFD sub-tree rooted at the
// TagExifIFDPointer entry in ifd0.  It returns the ExifIFD, the raw MakerNote
// bytes (always retained for round-trip writes), the MakerNote's TIFF-relative
// byte offset as both uint32 and uint64 (zero if absent), the parsed
// MakerNoteIFD (nil when cfg.skipMakerNote is set or the make is unrecognised),
// the InteropIFD, and any diagnostic warnings accumulated during traversal.
// All fields are nil/empty when the corresponding pointer tag is absent or the
// sub-IFD cannot be traversed — errors are silently discarded to match the
// original Parse behaviour (corrupt sub-IFDs must not abort the whole parse).
//
// EXIF §4.6.3: ExifIFD pointer is tag 0x8769; InteropIFD pointer is tag 0xA005.
// EXIF §4.6.5: MakerNote is tag 0x927C.
func parseExifSubIFDs(b []byte, ifd0 *IFD, order binary.ByteOrder, cfg *parseConfig) (exifIFD *IFD, makerNote []byte, makerNoteOffset uint32, makerNoteOffset64 uint64, makerNoteIFD *IFD, interopIFD *IFD, warnings []string) {
	ptr := ifd0.Get(TagExifIFDPointer)
	if ptr == nil || len(ptr.Value) < 4 {
		return nil, nil, 0, 0, nil, nil, nil
	}
	sub, subWarnings, err := traverse(b, order.Uint32(ptr.Value), order)
	warnings = append(warnings, subWarnings...)
	if err != nil {
		return nil, nil, 0, 0, nil, nil, warnings
	}
	exifIFD = sub

	// MakerNote (EXIF §4.6.5, tag 0x927C) — raw bytes always retained;
	// IFD parsing is skipped when SkipMakerNote() is requested.
	if mn := sub.Get(TagMakerNote); mn != nil {
		makerNote = mn.Value
		// rawOffset (uint64) is non-zero when the MakerNote value is out-of-line
		// (total size > 4 bytes, always true for real MakerNote payloads).
		// It records the TIFF-stream offset so EXIF.MakerNoteOffset /
		// MakerNoteOffset64 can expose the original position for movement-
		// detection by callers.  Classic TIFF rawOffset fits in uint32.
		makerNoteOffset64 = mn.rawOffset
		makerNoteOffset = uint32(mn.rawOffset) //nolint:gosec // G115: classic TIFF offsets always fit uint32
		if !cfg.skipMakerNote {
			if makeEntry := ifd0.Get(TagMake); makeEntry != nil {
				makerNoteIFD = parseMakerNoteIFD(mn.Value, makeEntry.String(), order)
			}
		}
	}

	// Interoperability IFD pointer (EXIF §4.6.3, tag 0xA005).
	if iptr := sub.Get(TagInteropIFDPointer); iptr != nil && len(iptr.Value) >= 4 {
		if isub, isubWarnings, ierr := traverse(b, order.Uint32(iptr.Value), order); ierr == nil {
			interopIFD = isub
			warnings = append(warnings, isubWarnings...)
		}
	}
	return exifIFD, makerNote, makerNoteOffset, makerNoteOffset64, makerNoteIFD, interopIFD, warnings
}

// parseGPSSubIFD traverses the GPS IFD rooted at the TagGPSIFDPointer entry
// in ifd0.  Returns the GPS IFD (nil when absent or unreadable) and any
// diagnostic warnings accumulated during traversal.
//
// EXIF §4.6.3: GPS IFD pointer is tag 0x8825.
func parseGPSSubIFD(b []byte, ifd0 *IFD, order binary.ByteOrder) (*IFD, []string) {
	ptr := ifd0.Get(TagGPSIFDPointer)
	if ptr == nil || len(ptr.Value) < 4 {
		return nil, nil
	}
	sub, warnings, err := traverse(b, order.Uint32(ptr.Value), order)
	if err != nil {
		return nil, warnings
	}
	return sub, warnings
}

// parseExifSubIFDsBigTIFF traverses the ExifIFD sub-tree for BigTIFF files.
// It is functionally equivalent to parseExifSubIFDs but uses the BigTIFF IFD
// traversal path and readBigTIFFSubIFDOffset to read 64-bit pointer values.
//
// BigTIFF spec §2: sub-IFD pointer entries use TypeShort (16-bit), TypeLong
// (32-bit), TypeLong8 (64-bit), or TypeIFD8 (64-bit) for their offset value.
// EXIF §4.6.3: ExifIFD pointer is tag 0x8769; InteropIFD pointer is tag 0xA005.
// EXIF §4.6.5: MakerNote is tag 0x927C.
// Audit finding #142: MakerNoteOffset64 is populated from the full uint64 rawOffset
// so that MakerNote positions above 4 GiB are reported without truncation.
func parseExifSubIFDsBigTIFF(b []byte, ifd0 *IFD, order binary.ByteOrder, cfg *parseConfig) (exifIFD *IFD, makerNote []byte, makerNoteOffset uint32, makerNoteOffset64 uint64, makerNoteIFD *IFD, interopIFD *IFD, warnings []string) { //nolint:gocyclo,cyclop // BigTIFF ExifIFD traversal mirrors parseExifSubIFDs structure; complexity is inherent in the sub-IFD dispatch chain
	ptr := ifd0.Get(TagExifIFDPointer)
	off, ok := readBigTIFFSubIFDOffset(ptr)
	if !ok || off == 0 {
		return nil, nil, 0, 0, nil, nil, nil
	}
	sub, subWarnings, err := traverseBigTIFF(b, off, order)
	warnings = append(warnings, subWarnings...)
	if err != nil {
		return nil, nil, 0, 0, nil, nil, warnings
	}
	exifIFD = sub

	// MakerNote: BigTIFF-embedded MakerNotes are classic-TIFF-internal blobs;
	// retain raw bytes for round-trip writes but do not attempt IFD parsing
	// (vendor offsets inside the MakerNote are TIFF-absolute in classic TIFF
	// and may be meaningless in a BigTIFF context). BigTIFF spec §2 note:
	// MakerNote parsing out-of-scope for BigTIFF; do not crash, just skip IFD parse.
	//
	// #142: mn.rawOffset is now uint64 (no longer truncated in parseIFDEntryBigTIFF).
	// Populate MakerNoteOffset64 from the full value; MakerNoteOffset receives the
	// lower 32 bits for backward compatibility with existing callers that target
	// classic TIFF RAW formats (NEF, ARW, ORF — all ≤ 4 GiB).
	if mn := sub.Get(TagMakerNote); mn != nil {
		makerNote = mn.Value
		makerNoteOffset64 = mn.rawOffset
		makerNoteOffset = uint32(mn.rawOffset & 0xFFFF_FFFF) // backward compat: lower 32 bits only; callers needing full offset use MakerNoteOffset64
		if !cfg.skipMakerNote {
			if makeEntry := ifd0.Get(TagMake); makeEntry != nil {
				// parseMakerNoteIFD uses the classic-TIFF traversal internally; for
				// BigTIFF containers the MakerNote blob is still a classic-TIFF
				// fragment so this is correct IF the MakerNote itself is a plain IFD.
				// On failure (e.g. Nikon encrypted notes) the result is nil — safe.
				makerNoteIFD = parseMakerNoteIFD(mn.Value, makeEntry.String(), order)
			}
		}
	}

	// Interoperability IFD pointer (EXIF §4.6.3, tag 0xA005).
	if iptr := sub.Get(TagInteropIFDPointer); iptr != nil {
		if ioff, iok := readBigTIFFSubIFDOffset(iptr); iok && ioff != 0 {
			if isub, isubWarnings, ierr := traverseBigTIFF(b, ioff, order); ierr == nil {
				interopIFD = isub
				warnings = append(warnings, isubWarnings...)
			}
		}
	}
	return exifIFD, makerNote, makerNoteOffset, makerNoteOffset64, makerNoteIFD, interopIFD, warnings
}

// parseGPSSubIFDBigTIFF traverses the GPS IFD for BigTIFF files.
// It is functionally equivalent to parseGPSSubIFD but reads the pointer offset
// with readBigTIFFSubIFDOffset and traverses with traverseBigTIFF.
// Returns the GPS IFD (nil when absent or unreadable) and any warnings.
//
// EXIF §4.6.3: GPS IFD pointer is tag 0x8825.
func parseGPSSubIFDBigTIFF(b []byte, ifd0 *IFD, order binary.ByteOrder) (*IFD, []string) {
	ptr := ifd0.Get(TagGPSIFDPointer)
	off, ok := readBigTIFFSubIFDOffset(ptr)
	if !ok || off == 0 {
		return nil, nil
	}
	sub, warnings, err := traverseBigTIFF(b, off, order)
	if err != nil {
		return nil, warnings
	}
	return sub, warnings
}

// bigTIFFMinHeader is the minimum valid BigTIFF header size.
// BigTIFF spec §2: 16 bytes = 2 (order) + 2 (magic) + 2 (offset-bytesize) +
// 2 (constant 0) + 8 (IFD0 offset).
const bigTIFFMinHeader = 16

// Parse parses a raw EXIF block starting at the TIFF header ("II" or "MM").
// b must be the complete EXIF payload (after the "Exif\x00\x00" prefix is
// stripped by the container layer). opts are optional; existing callers that
// pass no options continue to work without change.
//
// Both classic TIFF (magic 0x002A, 32-bit IFD offsets) and BigTIFF
// (magic 0x002B, 64-bit IFD offsets, BigTIFF spec §2 Aware Systems / libtiff)
// are supported.  The classic path is unchanged in performance and allocation
// profile.  The BigTIFF path uses separate traversal functions
// (traverseBigTIFF, parseSingleIFDBigTIFF, parseIFDEntryBigTIFF) that share
// the same IFD/IFDEntry types with the classic path.
func Parse(b []byte, opts ...ParseOption) (*EXIF, error) {
	var cfg parseConfig
	for _, o := range opts {
		o(&cfg)
	}
	if len(b) < 8 {
		return nil, &metaerr.TruncatedFileError{At: "EXIF header"}
	}

	// Determine byte order from the TIFF header (TIFF §2 / BigTIFF spec §2).
	order, err := parseByteOrder(b)
	if err != nil {
		return nil, err
	}

	magic := order.Uint16(b[2:])
	switch magic {
	case 0x002A:
		// Classic TIFF path: 8-byte header, 32-bit IFD offsets (TIFF §2).
		// This path is byte-for-byte identical to the pre-BigTIFF implementation;
		// no new branches or overhead are added on this fast path.
		ifd0Off := order.Uint32(b[4:])
		e := &EXIF{ByteOrder: order}

		ifd0, ifd0Warnings, ferr := traverse(b, ifd0Off, order)
		if ferr != nil {
			return nil, ferr
		}
		e.IFD0 = ifd0
		e.Warnings = append(e.Warnings, ifd0Warnings...)

		var exifWarnings, gpsWarnings []string
		e.ExifIFD, e.MakerNote, e.MakerNoteOffset, e.MakerNoteOffset64, e.MakerNoteIFD, e.InteropIFD, exifWarnings = parseExifSubIFDs(b, ifd0, order, &cfg)
		e.Warnings = append(e.Warnings, exifWarnings...)

		e.GPSIFD, gpsWarnings = parseGPSSubIFD(b, ifd0, order)
		e.Warnings = append(e.Warnings, gpsWarnings...)
		return e, nil

	case 0x002B:
		// BigTIFF path: 16-byte header, 64-bit IFD offsets (BigTIFF spec §2).
		// Validate the header length and offset-bytesize before proceeding.
		if len(b) < bigTIFFMinHeader {
			return nil, &metaerr.TruncatedFileError{At: "BigTIFF header"}
		}
		// BigTIFF spec §2: bytes [4:6] = offset-bytesize MUST equal 8.
		offsetBytesize := order.Uint16(b[4:])
		if offsetBytesize != 8 {
			return nil, &metaerr.CorruptMetadataError{
				Format: "EXIF",
				Reason: fmt.Sprintf("BigTIFF offset-bytesize = %d, must be 8", offsetBytesize),
			}
		}
		// bytes [6:8] = constant 0 (reserved); advisory per spec, not enforced.
		// bytes [8:16] = IFD0 offset (uint64).
		ifd0Off := order.Uint64(b[8:])

		// BigTIFF spec §2; audit finding #107: mark the provenance so Encode can
		// reject re-encoding this EXIF as classic TIFF (which would truncate all
		// 64-bit offsets to 32 bits and silently corrupt the output).
		e := &EXIF{ByteOrder: order, BigTIFF: true}
		ifd0, ifd0Warnings, ferr := traverseBigTIFF(b, ifd0Off, order)
		if ferr != nil {
			return nil, ferr
		}
		e.IFD0 = ifd0
		e.Warnings = append(e.Warnings, ifd0Warnings...)

		var exifWarnings, gpsWarnings []string
		e.ExifIFD, e.MakerNote, e.MakerNoteOffset, e.MakerNoteOffset64, e.MakerNoteIFD, e.InteropIFD, exifWarnings = parseExifSubIFDsBigTIFF(b, ifd0, order, &cfg)
		e.Warnings = append(e.Warnings, exifWarnings...)

		e.GPSIFD, gpsWarnings = parseGPSSubIFDBigTIFF(b, ifd0, order)
		e.Warnings = append(e.Warnings, gpsWarnings...)
		return e, nil

	default:
		return nil, &metaerr.CorruptMetadataError{
			Format: "EXIF",
			Reason: fmt.Sprintf("invalid TIFF magic 0x%04X (expected 0x002A classic TIFF or 0x002B BigTIFF)", magic),
		}
	}
}

// Encode serialises e back to a raw EXIF byte stream (TIFF header + IFDs).
//
// Round-trip fidelity guarantee:
//   - Known-type inline entries (total value size ≤ 4 bytes) are perfectly preserved.
//   - Known-type out-of-line entries (total value size > 4 bytes) are re-serialised
//     into a fresh value area; their data is preserved exactly.
//   - Unknown-type entries (TIFF type codes not defined in TIFF 6.0) are stored
//     during parsing as their raw 4-byte IFD field. On re-encode that 4-byte field
//     is written back verbatim as an inline value. If the original field was an
//     offset into a private data blob, that blob is NOT copied — the offset in the
//     new stream will be stale and the pointed-to data will be silently lost.
//
// This is an inherent constraint: without a known type size it is impossible to
// locate or copy the out-of-line data. Callers that embed private data under
// unknown type codes must re-inject that data into the stream after calling Encode.
// Task #84 pins this behaviour; any change to it is a conscious, tested decision.
func Encode(e *EXIF) ([]byte, error) {
	return serialise(e)
}

// ParseIFDAt parses the IFD starting at offset within b using the given byte
// order, and returns the parsed IFD, the next-IFD offset (0 if absent or end
// of chain), and whether parsing succeeded.
//
// It is intended for callers (e.g. the TIFF copy-and-relocate layer) that need
// to parse a single IFD at an arbitrary offset within a raw TIFF buffer,
// without going through the full Parse → EXIF struct path.
//
// The returned IFD is a self-contained structure; it does not share backing
// arrays with b for its Entries slice, though IFDEntry.Value fields may alias b
// for out-of-line values (zero-copy parse). Callers that need the IFD to
// outlive b should copy the Value fields they need.
//
// TIFF 6.0 §2: IFD layout — count(2) + entries(count×12) + nextIFD(4).
func ParseIFDAt(b []byte, offset uint32, order binary.ByteOrder) (*IFD, uint32, bool) {
	ifd, next, ok, _ := parseSingleIFD(b, offset, order)
	return ifd, next, ok
}

// CameraModel returns the value of IFD0 tag 0x0110 (Model, EXIF §4.6.4 Table 3).
func (e *EXIF) CameraModel() string {
	if e == nil {
		return ""
	}
	entry := e.IFD0.Get(TagModel)
	if entry == nil {
		return ""
	}
	return entry.String()
}

// GPS returns decimal-degree coordinates from the GPS IFD.
func (e *EXIF) GPS() (lat, lon float64, ok bool) {
	if e == nil || e.GPSIFD == nil {
		return 0, 0, false
	}
	return parseGPS(e.GPSIFD)
}

// Copyright returns the value of IFD0 tag 0x8298 (Copyright, EXIF §4.6.4 Table 3).
func (e *EXIF) Copyright() string {
	if e == nil {
		return ""
	}
	entry := e.IFD0.Get(TagCopyright)
	if entry == nil {
		return ""
	}
	return entry.String()
}

// Caption returns the value of IFD0 tag 0x010E (ImageDescription, EXIF §4.6.4 Table 3).
func (e *EXIF) Caption() string {
	if e == nil {
		return ""
	}
	entry := e.IFD0.Get(TagImageDescription)
	if entry == nil {
		return ""
	}
	return entry.String()
}

// DateTimeOriginal returns the original capture date/time from ExifIFD tag 0x9003
// (DateTimeOriginal, EXIF §4.6.5). The timezone offset from tag 0x9011
// (OffsetTimeOriginal, EXIF 2.31+) is applied when present; otherwise UTC is assumed.
func (e *EXIF) DateTimeOriginal() (time.Time, bool) {
	if e == nil || e.ExifIFD == nil {
		return time.Time{}, false
	}
	entry := e.ExifIFD.Get(TagDateTimeOriginal)
	if entry == nil {
		return time.Time{}, false
	}
	s := entry.String()
	if s == "" {
		return time.Time{}, false
	}

	// Try to read OffsetTimeOriginal for timezone (EXIF 2.31+, tag 0x9011).
	loc := time.UTC
	if off := e.ExifIFD.Get(TagOffsetTimeOriginal); off != nil {
		if tzStr := off.String(); tzStr != "" {
			if tz, err := parseExifTZ(tzStr); err == nil {
				loc = tz
			}
		}
	}

	// EXIF datetime format: "YYYY:MM:DD HH:MM:SS" (EXIF §4.6.5).
	t, err := time.ParseInLocation("2006:01:02 15:04:05", s, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// parseExifTZ parses an EXIF offset string such as "+02:00" or "-05:00" into
// a *time.Location. Returns an error if the format is not recognised.
func parseExifTZ(s string) (*time.Location, error) {
	t, err := time.Parse("-07:00", s)
	if err != nil {
		return nil, fmt.Errorf("exif: parse date/time: %w", err)
	}
	_, offset := t.Zone()
	return time.FixedZone(s, offset), nil
}

// ExposureTime returns the exposure time as a rational [numerator, denominator]
// from ExifIFD tag 0x829A (EXIF §4.6.5). ok is false when not present.
func (e *EXIF) ExposureTime() (num, den uint32, ok bool) {
	if e == nil || e.ExifIFD == nil {
		return 0, 0, false
	}
	entry := e.ExifIFD.Get(TagExposureTime)
	if entry == nil {
		return 0, 0, false
	}
	r := entry.Rational(0)
	return r[0], r[1], r[1] != 0
}

// FNumber returns the F-number (aperture) as a float64 from ExifIFD tag 0x829D
// (EXIF §4.6.5). ok is false when not present or denominator is zero.
func (e *EXIF) FNumber() (float64, bool) {
	if e == nil || e.ExifIFD == nil {
		return 0, false
	}
	entry := e.ExifIFD.Get(TagFNumber)
	if entry == nil {
		return 0, false
	}
	r := entry.Rational(0)
	if r[1] == 0 {
		return 0, false
	}
	return float64(r[0]) / float64(r[1]), true
}

// ISO returns the ISO speed rating from ExifIFD tag 0x8827 (EXIF §4.6.5).
// ok is false when not present.
func (e *EXIF) ISO() (uint, bool) {
	if e == nil || e.ExifIFD == nil {
		return 0, false
	}
	entry := e.ExifIFD.Get(TagISOSpeedRatings)
	if entry == nil {
		return 0, false
	}
	return uint(entry.Uint16()), true
}

// FocalLength returns the focal length in millimetres from ExifIFD tag 0x920A
// (EXIF §4.6.5). ok is false when not present or denominator is zero.
func (e *EXIF) FocalLength() (float64, bool) {
	if e == nil || e.ExifIFD == nil {
		return 0, false
	}
	entry := e.ExifIFD.Get(TagFocalLength)
	if entry == nil {
		return 0, false
	}
	r := entry.Rational(0)
	if r[1] == 0 {
		return 0, false
	}
	return float64(r[0]) / float64(r[1]), true
}

// LensModel returns the lens model string from ExifIFD tag 0xA434
// (LensModel, EXIF §4.6.5). Returns an empty string when not present.
func (e *EXIF) LensModel() string {
	if e == nil || e.ExifIFD == nil {
		return ""
	}
	entry := e.ExifIFD.Get(TagLensModel)
	if entry == nil {
		return ""
	}
	return entry.String()
}

// Orientation returns the image orientation from IFD0 tag 0x0112
// (EXIF §4.6.4 Table 3). ok is false when not present.
func (e *EXIF) Orientation() (uint16, bool) {
	if e == nil {
		return 0, false
	}
	entry := e.IFD0.Get(TagOrientation)
	if entry == nil {
		return 0, false
	}
	return entry.Uint16(), true
}

// ImageSize returns the pixel dimensions of the full-resolution image from
// ExifIFD tags 0xA002/0xA003 (PixelXDimension / PixelYDimension, EXIF §4.6.5).
// ok is false when not present.
func (e *EXIF) ImageSize() (width, height uint32, ok bool) {
	if e == nil || e.ExifIFD == nil {
		return 0, 0, false
	}
	xEntry := e.ExifIFD.Get(TagPixelXDimension)
	yEntry := e.ExifIFD.Get(TagPixelYDimension)
	if xEntry == nil || yEntry == nil {
		return 0, 0, false
	}
	// PixelXDimension may be SHORT or LONG (EXIF §4.6.5).
	var w, h uint32
	switch xEntry.Type {
	case TypeShort:
		w = uint32(xEntry.Uint16())
	default:
		w = xEntry.Uint32()
	}
	switch yEntry.Type {
	case TypeShort:
		h = uint32(yEntry.Uint16())
	default:
		h = yEntry.Uint32()
	}
	return w, h, w > 0 && h > 0
}

// UserComment returns the decoded value of ExifIFD tag 0x9286 (UserComment,
// EXIF 2.32 CIPA DC-008-2023 §4.6.5). The first 8 bytes of the stored value
// identify the character encoding; the remainder is decoded accordingly:
//
//   - "ASCII\x00\x00\x00" prefix → treated as UTF-8 (Windows writes ASCII prefix
//     with UTF-8 content; both are accepted).
//   - "UNICODE\x00"  prefix → UTF-16, byte order from the EXIF stream.
//   - "JIS\x00..."   prefix → best-effort raw pass-through (no JIS→UTF-8 table).
//   - all-zero prefix (Undefined) → raw bytes interpreted as UTF-8.
//
// Trailing NUL bytes are always stripped. Returns an empty string when the tag
// is absent or the payload is shorter than the mandatory 8-byte charset prefix.
func (e *EXIF) UserComment() string {
	if e == nil || e.ExifIFD == nil {
		return ""
	}
	entry := e.ExifIFD.Get(TagUserComment)
	if entry == nil {
		return ""
	}
	// Determine byte order for UNICODE prefix: follow the EXIF stream order.
	bigEndian := e.ByteOrder == binary.BigEndian
	return decodeUserComment(entry.Value, bigEndian)
}

// xpTagString decodes a Windows XP* tag value from the ExifIFD.
// XP* tags store null-terminated UTF-16 LE as TypeByte.
// Returns an empty string when the tag is absent or IFD is nil.
func (e *EXIF) xpTagString(tag TagID) string {
	if e == nil || e.ExifIFD == nil {
		return ""
	}
	entry := e.ExifIFD.Get(tag)
	if entry == nil {
		return ""
	}
	// Windows XP* tags: TypeByte, null-terminated UTF-16 LE.
	// Microsoft EXIF Extension: each character occupies two bytes, low byte first.
	return decodeUTF16LE(entry.Value)
}

// XPTitle returns the decoded value of ExifIFD tag 0x9C9B (XPTitle).
// Windows Photo Gallery writes titles here as null-terminated UTF-16 LE in a
// TypeByte field. Not part of the EXIF standard; Microsoft EXIF Extension.
func (e *EXIF) XPTitle() string { return e.xpTagString(TagXPTitle) }

// XPComment returns the decoded value of ExifIFD tag 0x9C9C (XPComment).
// Windows-specific; see XPTitle for encoding details.
func (e *EXIF) XPComment() string { return e.xpTagString(TagXPComment) }

// XPAuthor returns the decoded value of ExifIFD tag 0x9C9D (XPAuthor).
// Windows-specific; see XPTitle for encoding details.
func (e *EXIF) XPAuthor() string { return e.xpTagString(TagXPAuthor) }

// XPKeywords returns the decoded value of ExifIFD tag 0x9C9E (XPKeywords).
// Windows-specific; see XPTitle for encoding details.
func (e *EXIF) XPKeywords() string { return e.xpTagString(TagXPKeywords) }

// XPSubject returns the decoded value of ExifIFD tag 0x9C9F (XPSubject).
// Windows-specific; see XPTitle for encoding details.
func (e *EXIF) XPSubject() string { return e.xpTagString(TagXPSubject) }

// Creator returns the artist / creator string from IFD0 tag 0x013B
// (Artist, EXIF §4.6.4 Table 3).
func (e *EXIF) Creator() string {
	if e == nil {
		return ""
	}
	entry := e.IFD0.Get(TagArtist)
	if entry == nil {
		return ""
	}
	return entry.String()
}

// ---------------------------------------------------------------------------
// Write setters
// ---------------------------------------------------------------------------

// ifd0ByteOrder returns the byte order in use by IFD0, defaulting to
// binary.LittleEndian for an empty or newly created IFD.
func (e *EXIF) ifd0ByteOrder() binary.ByteOrder {
	if len(e.IFD0.Entries) > 0 {
		return e.IFD0.Entries[0].byteOrder
	}
	return binary.LittleEndian
}

// SetCameraModel sets IFD0 tag 0x0110 (Model, EXIF §4.6.4 Table 3).
func (e *EXIF) SetCameraModel(s string) {
	if e == nil || e.IFD0 == nil {
		return
	}
	v := asciiValue(s)
	e.IFD0.set(TagModel, TypeASCII, uint32(len(v)), v) //nolint:gosec // G115: string length bounded by input
}

// SetCaption sets IFD0 tag 0x010E (ImageDescription, EXIF §4.6.4 Table 3).
func (e *EXIF) SetCaption(s string) {
	if e == nil || e.IFD0 == nil {
		return
	}
	v := asciiValue(s)
	e.IFD0.set(TagImageDescription, TypeASCII, uint32(len(v)), v) //nolint:gosec // G115: string length bounded by input
}

// SetCopyright sets IFD0 tag 0x8298 (Copyright, EXIF §4.6.4 Table 3).
func (e *EXIF) SetCopyright(s string) {
	if e == nil || e.IFD0 == nil {
		return
	}
	v := asciiValue(s)
	e.IFD0.set(TagCopyright, TypeASCII, uint32(len(v)), v) //nolint:gosec // G115: string length bounded by input
}

// SetCreator sets IFD0 tag 0x013B (Artist, EXIF §4.6.4 Table 3).
func (e *EXIF) SetCreator(s string) {
	if e == nil || e.IFD0 == nil {
		return
	}
	v := asciiValue(s)
	e.IFD0.set(TagArtist, TypeASCII, uint32(len(v)), v) //nolint:gosec // G115: string length bounded by input
}

// SetOrientation sets IFD0 tag 0x0112 (Orientation, EXIF §4.6.4 Table 3).
// Valid values are 1–8 per EXIF spec; the method does not validate the range.
func (e *EXIF) SetOrientation(v uint16) {
	if e == nil || e.IFD0 == nil {
		return
	}
	// Encode using the IFD's own byte order so the inline value bytes are
	// written correctly for both LE and BE TIFF streams.
	order := e.ifd0ByteOrder()
	var b [2]byte
	order.PutUint16(b[:], v)
	e.IFD0.set(TagOrientation, TypeShort, 1, b[:])
}

// ensureExifIFD creates ExifIFD if nil and ensures IFD0 carries a placeholder
// TagExifIFDPointer entry so that Encode() will wire the real offset.
// It is called by all setters that target the ExifIFD.
func (e *EXIF) ensureExifIFD() {
	if e.ExifIFD != nil {
		return
	}
	e.ExifIFD = &IFD{}
	if e.IFD0 != nil && e.IFD0.Get(TagExifIFDPointer) == nil {
		// Value 0 is a placeholder; encode() (write.go) overwrites it with the
		// correct absolute offset once the ExifIFD is serialised.
		var placeholder [4]byte
		e.IFD0.set(TagExifIFDPointer, TypeLong, 1, placeholder[:])
	}
}

// SetMake sets IFD0 tag 0x010F (Make, EXIF §4.6.4 Table 3).
func (e *EXIF) SetMake(s string) {
	if e == nil || e.IFD0 == nil {
		return
	}
	v := asciiValue(s)
	e.IFD0.set(TagMake, TypeASCII, uint32(len(v)), v) //nolint:gosec // G115: string length bounded by input
}

// SetDateTimeOriginal sets ExifIFD tag 0x9003 (DateTimeOriginal, EXIF §4.6.5)
// from t, using the EXIF datetime format "YYYY:MM:DD HH:MM:SS\x00" (20 bytes).
func (e *EXIF) SetDateTimeOriginal(t time.Time) {
	if e == nil || e.IFD0 == nil {
		return
	}
	e.ensureExifIFD()
	// EXIF §4.6.5: DateTimeOriginal is a 20-byte ASCII field including the NUL.
	formatted := t.Format("2006:01:02 15:04:05") + "\x00"
	v := []byte(formatted)
	e.ExifIFD.set(TagDateTimeOriginal, TypeASCII, uint32(len(v)), v) //nolint:gosec // G115: string length bounded by input
}

// SetExposureTime sets ExifIFD tag 0x829A (ExposureTime, EXIF §4.6.5).
// num and den are the numerator and denominator of the rational exposure value.
func (e *EXIF) SetExposureTime(num, den uint32) {
	if e == nil || e.IFD0 == nil {
		return
	}
	e.ensureExifIFD()
	order := e.ifd0ByteOrder()
	b := make([]byte, 8)
	order.PutUint32(b[0:], num)
	order.PutUint32(b[4:], den)
	e.ExifIFD.set(TagExposureTime, TypeRational, 1, b)
}

// SetFNumber sets ExifIFD tag 0x829D (FNumber, EXIF §4.6.5).
// f is encoded as a rational with denominator 100 to preserve two decimal places.
func (e *EXIF) SetFNumber(f float64) {
	if e == nil || e.IFD0 == nil {
		return
	}
	e.ensureExifIFD()
	order := e.ifd0ByteOrder()
	const denom = uint32(100)
	num := uint32(math.Round(f * float64(denom)))
	b := make([]byte, 8)
	order.PutUint32(b[0:], num)
	order.PutUint32(b[4:], denom)
	e.ExifIFD.set(TagFNumber, TypeRational, 1, b)
}

// SetISO sets ExifIFD tag 0x8827 (ISOSpeedRatings, EXIF §4.6.5).
func (e *EXIF) SetISO(iso uint) {
	if e == nil || e.IFD0 == nil {
		return
	}
	e.ensureExifIFD()
	order := e.ifd0ByteOrder()
	var b [2]byte
	if iso > 65535 {
		iso = 65535
	}
	order.PutUint16(b[:], uint16(iso))
	e.ExifIFD.set(TagISOSpeedRatings, TypeShort, 1, b[:])
}

// SetFocalLength sets ExifIFD tag 0x920A (FocalLength, EXIF §4.6.5).
// mm is encoded as a rational with denominator 100.
func (e *EXIF) SetFocalLength(mm float64) {
	if e == nil || e.IFD0 == nil {
		return
	}
	e.ensureExifIFD()
	order := e.ifd0ByteOrder()
	const denom = uint32(100)
	num := uint32(math.Round(mm * float64(denom)))
	b := make([]byte, 8)
	order.PutUint32(b[0:], num)
	order.PutUint32(b[4:], denom)
	e.ExifIFD.set(TagFocalLength, TypeRational, 1, b)
}

// SetLensModel sets ExifIFD tag 0xA434 (LensModel, EXIF §4.6.5).
func (e *EXIF) SetLensModel(s string) {
	if e == nil || e.IFD0 == nil {
		return
	}
	e.ensureExifIFD()
	v := asciiValue(s)
	e.ExifIFD.set(TagLensModel, TypeASCII, uint32(len(v)), v) //nolint:gosec // G115: string length bounded by input
}

// SetImageSize sets ExifIFD tags 0xA002 and 0xA003 (PixelXDimension /
// PixelYDimension, EXIF §4.6.5) to the given pixel dimensions.
func (e *EXIF) SetImageSize(width, height uint32) {
	if e == nil || e.IFD0 == nil {
		return
	}
	e.ensureExifIFD()
	order := e.ifd0ByteOrder()
	var bw, bh [4]byte
	order.PutUint32(bw[:], width)
	order.PutUint32(bh[:], height)
	e.ExifIFD.set(TagPixelXDimension, TypeLong, 1, bw[:])
	e.ExifIFD.set(TagPixelYDimension, TypeLong, 1, bh[:])
}

// validWGS84Coords reports whether lat and lon are finite, non-NaN values
// within the WGS-84 bounds accepted by the EXIF GPS IFD.
//
// CIPA DC-008-2019 §4.6.6: GPSLatitude valid range [0,90]; GPSLongitude [0,180].
// The signed ranges accepted here are [-90,90] for latitude and [-180,180] for
// longitude; SetGPS converts to absolute values and stores the hemisphere in
// the Ref tag.
func validWGS84Coords(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) &&
		!math.IsInf(lat, 0) && !math.IsInf(lon, 0) &&
		lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// decimalToDMSBytes converts a non-negative decimal-degree coordinate to the
// three RATIONAL pairs [degrees/1, minutes/1, seconds*1e6/1e6] encoded per the
// EXIF GPS spec (EXIF §4.6.6). Each rational is 8 bytes (two uint32s), so the
// returned array is 24 bytes total.
//
// Extracting this from a closure inside SetGPS eliminates a closure allocation
// per SetGPS call; order is passed as a parameter instead of captured.
func decimalToDMSBytes(coord float64, order binary.ByteOrder) [24]byte {
	coord = math.Abs(coord)

	deg := math.Floor(coord)
	rem := (coord - deg) * 60
	mins := math.Floor(rem)
	sec := (rem - mins) * 60

	// Scale seconds to integer numerator with denominator 1,000,000.
	const secDenom = uint32(1_000_000)
	secNum := uint32(math.Round(sec * float64(secDenom)))

	var b [24]byte // 3 rationals × 8 bytes; stack-allocated, no heap escape
	order.PutUint32(b[0:], uint32(deg))
	order.PutUint32(b[4:], 1)
	order.PutUint32(b[8:], uint32(mins))
	order.PutUint32(b[12:], 1)
	order.PutUint32(b[16:], secNum)
	order.PutUint32(b[20:], secDenom)
	return b
}

// SetGPS sets the GPS IFD from decimal-degree WGS-84 coordinates.
// It creates GPSIFD if nil and sets the four mandatory tags:
//
//   - GPSLatitudeRef  (0x0001): "N\x00" or "S\x00"
//   - GPSLatitude     (0x0002): three RATIONAL values (degrees, minutes, seconds)
//   - GPSLongitudeRef (0x0003): "E\x00" or "W\x00"
//   - GPSLongitude    (0x0004): three RATIONAL values
//
// DMS encoding per EXIF §4.6.6: degrees denominator = 1, minutes denominator = 1,
// seconds denominator = 1,000,000 (preserves ~0.28 mm spatial precision).
//
// A placeholder TagGPSIFDPointer entry is also inserted into IFD0 so that
// Encode() detects the GPS IFD and wires the offset correctly.
//
// WGS-84 range validation: lat must be in [-90, 90] and lon in [-180, 180].
// NaN, ±Inf, and out-of-range values are rejected silently — the GPS IFD is
// left unset (or unchanged if already present) and no error is returned. This
// matches the void signature contract of all Set* methods.
func (e *EXIF) SetGPS(lat, lon float64) {
	if e == nil || e.IFD0 == nil {
		return
	}

	// WGS-84 range validation. NaN/Inf values must be rejected before reaching
	// decimalToDMSBytes: uint32(math.Floor(NaN)) yields 0 on arm64 (silently
	// encoding the Gulf of Guinea) and uint32(+Inf) yields 4294967295.
	// Both produce well-formed but semantically invalid GPS IFDs.
	if !validWGS84Coords(lat, lon) {
		return
	}

	// Determine byte order from IFD0 — GPS IFD entries must match the stream.
	order := e.ifd0ByteOrder()

	latRef := "N\x00"
	if lat < 0 {
		latRef = "S\x00"
	}
	lonRef := "E\x00"
	if lon < 0 {
		lonRef = "W\x00"
	}

	if e.GPSIFD == nil {
		e.GPSIFD = &IFD{}
	}
	gps := e.GPSIFD

	// decimalToDMSBytes returns a [24]byte array (stack-allocated); take a
	// heap copy for the IFDEntry.Value slice so the entry outlives this call.
	latDMS := decimalToDMSBytes(lat, order)
	lonDMS := decimalToDMSBytes(lon, order)

	// Write GPSVersionID (0x0000, BYTE, count=4) = {2,3,0,0} only when not already
	// present. EXIF §4.6.6 Table 15: GPSVersionID indicates the GPS IFD version;
	// {2,3,0,0} corresponds to EXIF 2.3/3.0 GPS attribute information revision.
	if gps.Get(TagGPSVersionID) == nil {
		gps.set(TagGPSVersionID, TypeByte, 4, []byte{2, 3, 0, 0})
	}
	gps.set(TagGPSLatitudeRef, TypeASCII, 2, []byte(latRef))
	gps.set(TagGPSLatitude, TypeRational, 3, latDMS[:])
	gps.set(TagGPSLongitudeRef, TypeASCII, 2, []byte(lonRef))
	gps.set(TagGPSLongitude, TypeRational, 3, lonDMS[:])

	// Ensure IFD0 carries a TagGPSIFDPointer entry so encode() will serialise
	// the GPS IFD and patch the real offset.  Value 0 is a placeholder;
	// encode() (write.go) overwrites it with the correct absolute offset.
	if e.IFD0.Get(TagGPSIFDPointer) == nil {
		var placeholder [4]byte
		e.IFD0.set(TagGPSIFDPointer, TypeLong, 1, placeholder[:])
	}
}
