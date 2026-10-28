package gometadata

// makernote_edit.go — edit the MakerNote of a read image without corrupting it
// (tasks #398 and #399). The method and its limits are described in
// exif/makernote_edit.go; this file is the one-call surface over it: the caller
// names a tag, the library finds the tag, edits the MakerNote, and marks the
// EXIF segment as modified so that Write encodes it and carries every other
// segment and every image byte over unchanged (task #387).

import (
	"fmt"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Errors of the MakerNote editing methods of Metadata. They are the errors of
// package exif, re-exported so that a caller needs one import.
var (
	// ErrNoMakerNote means the image has no MakerNote with tags to edit: it has
	// none, the camera's layout is not one the library reads, or the image was
	// read with WithoutMakerNote or WithoutEXIF.
	ErrNoMakerNote = exif.ErrNoMakerNote
	// ErrMakerNoteTagNotFound means the MakerNote has no tag of the name or ID
	// given. A tag cannot be added to a MakerNote.
	ErrMakerNoteTagNotFound = exif.ErrMakerNoteTagNotFound
	// ErrMakerNoteNotEditable means the MakerNote cannot be edited without risk
	// to the file; the error text gives the reason. The Metadata is unchanged.
	ErrMakerNoteNotEditable = exif.ErrMakerNoteNotEditable
	// ErrMakerNoteValueType means the value does not fit the type of the tag.
	ErrMakerNoteValueType = exif.ErrMakerNoteValueType
	// ErrMakerNoteAmbiguousTag means the name given belongs to several tags of
	// the MakerNote; name the tag by its ID ("0x001D").
	ErrMakerNoteAmbiguousTag = exif.ErrMakerNoteAmbiguousTag
)

// DeleteMakerNoteTag removes a tag from the MakerNote (the camera-specific
// block of the Exif data) together with its value, so that the value is gone
// from the file: for example the camera or lens serial number.
//
// tag is the name of the tag as ExifTool and the Exif libraries spell it
// ("CameraSerialNumber", "LensModel"; exif.TagNameIn lists the names of a
// manufacturer) or its hexadecimal ID ("0x000C"). A name that belongs to several
// tags of the MakerNote removes all of them. Every other tag keeps its value and
// its position, and Write copies the rest of the image unchanged.
//
//	m, _ := gometadata.ReadFile("photo.jpg")
//	if err := m.DeleteMakerNoteTag("CameraSerialNumber"); err != nil { ... }
//	gometadata.WriteFile("photo.jpg", m)
//
// Two effects of the camera formats are not the library's to avoid. A reader
// such as ExifTool may report a minor warning ("Possibly incorrect maker notes
// offsets") when the removed value was the first one behind the tag table,
// because a gap is left where it was; every tag is still read correctly. And a
// value that another part of the MakerNote is computed from changes what the
// reader finds there: Nikon encrypts parts of its MakerNote with a key made
// from the serial number and the shutter count, so a reader cannot decode
// those parts once the serial number is deleted or replaced; their bytes are
// untouched.
//
// It returns ErrNoMakerNote when the image has no MakerNote the library reads,
// ErrMakerNoteTagNotFound when the MakerNote has no such tag, and
// ErrMakerNoteNotEditable when the MakerNote cannot be edited safely (its layout
// is unknown, it refers to data outside its own bytes, or the edit could not be
// verified). The Metadata is unchanged on every error. It is safe for concurrent
// use with the other methods of m.
func (m *Metadata) DeleteMakerNoteTag(tag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids, err := m.makerNoteIDs(tag)
	if err != nil {
		return err
	}
	if err := m.EXIF.DeleteMakerNoteTags(ids); err != nil {
		return err //nolint:wrapcheck // the error is one of the re-exported exif errors of this API; wrapping would only add a prefix
	}
	m.touch(SegmentEXIF)
	return nil
}

// SetMakerNoteTag replaces the value of a tag of the MakerNote. The type of the
// tag stays as the camera wrote it, and value must fit it:
//
//   - a string for a text tag ("SerialNumber");
//   - an integer, []uint16 or []uint32 for a numeric tag, within the range of its type;
//   - []byte for any tag: the raw bytes of the value in the byte order of the
//     MakerNote, as many as a whole number of its elements.
//
// tag is a name or a hexadecimal ID, as for DeleteMakerNoteTag; a name that
// belongs to several tags of the MakerNote returns ErrMakerNoteAmbiguousTag. A
// tag that the MakerNote does not have cannot be added (ErrMakerNoteTagNotFound).
//
// A value of the same byte size as the old one replaces it where it is. A value
// of another size is stored where the old one was when it fits there, and at the
// end of the MakerNote otherwise, with the entry pointing to it; nothing else in
// the MakerNote moves, and Write copies every other segment and the image data
// unchanged. This holds for every image format that can carry a MakerNote the
// library edits: JPEG, PNG, WebP, HEIF, AVIF, TIFF, DNG, CR2, NEF, ARW and ORF.
//
// The MakerNote of some images cannot be edited without risk, and the call then
// returns ErrMakerNoteNotEditable with the reason, leaving m unchanged: a camera
// layout whose offset base is not known, a MakerNote that refers to data outside
// its own bytes (for example the Nikon Coolpix P7000 and the Olympus C-720UZ),
// offsets that disagree with the length of the MakerNote, a BigTIFF file, and
// the Canon CR3 format, which keeps its MakerNote in a box of its own. Canon
// MakerNotes whose offsets were shifted by other software are edited with the
// base their footer names.
//
// It returns the errors of DeleteMakerNoteTag and ErrMakerNoteValueType when
// value does not fit the tag. The Metadata is unchanged on every error. It is
// safe for concurrent use with the other methods of m.
func (m *Metadata) SetMakerNoteTag(tag string, value any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids, err := m.makerNoteIDs(tag)
	if err != nil {
		return err
	}
	if len(ids) > 1 {
		return fmt.Errorf("%w: %q is tags %s", ErrMakerNoteAmbiguousTag, tag, hexIDs(ids))
	}
	if err := m.EXIF.SetMakerNoteTag(ids[0], value); err != nil {
		return err //nolint:wrapcheck // see DeleteMakerNoteTag
	}
	m.touch(SegmentEXIF)
	return nil
}

// makerNoteIDs resolves tag to the IDs of the MakerNote tags it names. The
// caller holds m.mu.
func (m *Metadata) makerNoteIDs(tag string) ([]exif.TagID, error) {
	if m.EXIF == nil {
		return nil, ErrNoMakerNote
	}
	return m.EXIF.MakerNoteTagIDs(tag) //nolint:wrapcheck // see DeleteMakerNoteTag
}

// hexIDs formats tag IDs as "0x001D, 0x00A0".
func hexIDs(ids []exif.TagID) string {
	const digits = "0123456789ABCDEF"
	b := make([]byte, 0, 8*len(ids))
	for i, id := range ids {
		if i > 0 {
			b = append(b, ',', ' ')
		}
		b = append(b, '0', 'x', digits[id>>12&15], digits[id>>8&15], digits[id>>4&15], digits[id&15])
	}
	return string(b)
}
