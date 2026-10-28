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

// ErrCarriedDataTooLarge is returned by EncodeRebased when the data that the
// private IFDs of IFD0 address outside their own structures, together with the
// data of the MakerNote, is larger than the Exif stream it was read from (the
// data of a valid stream are disjoint ranges of it), or when IFD0 points to
// more private IFDs than any real file has. Writing the stream anyway would
// leave pointers that no longer address their data, or let a small stream
// grow without bound (CWE-400).
var ErrCarriedDataTooLarge = errors.New("exif: the data referenced by the metadata is larger than the Exif block it was read from")

// ErrNoMakerNote is returned by the MakerNote editing methods (DeleteMakerNoteTag,
// SetMakerNoteTag, MakerNoteTagIDs) when the EXIF has no MakerNote, or has one
// that the library did not parse into tags (a manufacturer layout it does not
// read, or the SkipMakerNote option).
var ErrNoMakerNote = errors.New("exif: the image has no MakerNote with tags to edit")

// ErrMakerNoteTagNotFound is returned by the MakerNote editing methods when the
// MakerNote has no tag of the given name or ID. A tag cannot be added to a
// MakerNote: that would move every value behind the tag table.
var ErrMakerNoteTagNotFound = errors.New("exif: the MakerNote has no such tag")

// ErrMakerNoteNotEditable is returned by the MakerNote editing methods when the
// edit cannot be made without risking the file. The error wraps this value with
// the reason, which is one of: the manufacturer layout of the MakerNote is not
// known (so the base of its offsets is not), the position of the MakerNote in
// its file is not known, the MakerNote refers to data outside its own bytes (an
// edited MakerNote would lose it), the edit could not be verified, or the value
// is larger than the limit. The EXIF is left unchanged.
var ErrMakerNoteNotEditable = errors.New("exif: this MakerNote cannot be edited safely")

// ErrMakerNoteValueType is returned by SetMakerNoteTag when the value does not fit
// the type of the tag it replaces (for example a string for a numeric tag, or an
// integer outside the range of a SHORT).
var ErrMakerNoteValueType = errors.New("exif: the value does not fit the type of the MakerNote tag")

// ErrMakerNoteSizeChange is returned when a MakerNote value would have to change
// size and the caller asked for a value of the same size (the FixedSize option of
// SetMakerNoteTag).
var ErrMakerNoteSizeChange = errors.New("exif: the new MakerNote value has a different size from the one it replaces")

// ErrMakerNoteAmbiguousTag is returned by Metadata.SetMakerNoteTag when the name
// it was given belongs to several tags of the MakerNote ("CameraSerialNumber"
// is both tag 0x001D and tag 0x00A0 of a Nikon MakerNote): the value belongs to
// one of them. Name the tag by its hexadecimal ID, for example "0x001D".
var ErrMakerNoteAmbiguousTag = errors.New("exif: the name belongs to several MakerNote tags; name the tag by its ID")
