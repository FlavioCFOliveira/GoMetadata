package cr3

import "errors"

// ErrNoMoovBox is returned when the CR3 container does not contain a moov box.
var ErrNoMoovBox = errors.New("cr3: no moov box found")

// ErrWriteNotSupported is returned by Inject when any metadata payload is
// non-nil. CR3 metadata writes are blocked because the Canon UUID box (moov)
// and the mdat boxes that follow it store absolute ISOBMFF chunk offsets in
// stco/co64 tables inside trak/stbl. Replacing CMT1 with a re-encoded EXIF of
// a different size shifts mdat by delta bytes, but the stco/co64 tables cannot
// be patched without a full ISOBMFF offset-relocation pass. Until that pass is
// implemented (deferred — see roadmap epic #33 follow-up), any write that
// changes moov size would silently corrupt every chunk-offset table reference
// into mdat, making the file's image and preview data unreadable by conformant
// decoders. Reads are unaffected.
//
// Callers can test for this condition with errors.Is:
//
//	err := gometadata.WriteFile("photo.cr3", m)
//	if errors.Is(err, cr3.ErrWriteNotSupported) {
//	    // CR3 write not yet supported; use a read-only workflow
//	}
var ErrWriteNotSupported = errors.New("cr3: metadata write not supported: stco/co64 offset relocation is required but not yet implemented")
