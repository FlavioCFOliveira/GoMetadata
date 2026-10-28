package exif

// MakerNote tag names. makernote_tags_gen.go holds one table per manufacturer
// (tag ID to name); tag_context.go selects the table and exposes it through
// TagNameIn. To regenerate the tables, check out metadata-extractor at the
// commit recorded in THIRD_PARTY_NOTICES.md and run, from the repository root:
//
//	METADATA_EXTRACTOR=/path/to/metadata-extractor go generate ./exif

//go:generate go run ../internal/tools/mngen -src $METADATA_EXTRACTOR -out makernote_tags_gen.go

// makerNoteTag is one entry of a generated MakerNote tag-name table.
type makerNoteTag struct {
	id   uint16
	name string
}
