package exif

// Adapted from metadata-extractor Source/com/drew/metadata/exif/ExifTiffHandler.java:ExifTiffHandler.processMakernote, Copyright 2002-2019 Drew Noakes and contributors, SPDX-License-Identifier: Apache-2.0

// TagContext names the IFD whose tag numbering a tag ID belongs to. A tag ID
// has no meaning without its IFD: 0x0001 is GPSLatitudeRef in the GPS IFD,
// InteropIndex in the Interoperability IFD and a vendor tag in a MakerNote.
// The zero value is not a valid context.
//
// Use TagNameIn to resolve a name and EXIF.MakerNoteContext to find the
// context of the MakerNote of a parsed file.
type TagContext uint8

// Contexts of the standard IFDs (CIPA DC-008 §4.6.2 to §4.6.7; TIFF 6.0 §2).
const (
	// ContextIFD0 is the primary image IFD (TIFF baseline tags).
	ContextIFD0 TagContext = iota + 1
	// ContextExif is the Exif sub-IFD (Exif IFD pointer 0x8769).
	ContextExif
	// ContextGPS is the GPS sub-IFD (GPS IFD pointer 0x8825).
	ContextGPS
	// ContextInterop is the Interoperability sub-IFD (pointer 0xA005).
	ContextInterop
	// ContextIFD1 is the thumbnail IFD that follows IFD0.
	ContextIFD1
)

// Contexts of the MakerNote IFDs, one per manufacturer tag table. A table is
// selected by the layout of the MakerNote, as metadata-extractor selects its
// directory class (ExifTiffHandler.processMakernote). Several layouts can
// share a table: for example every Olympus layout, and the Minolta and Epson
// layouts that use the Olympus tags.
const (
	// ContextMakerNoteCanon is the Canon MakerNote.
	ContextMakerNoteCanon TagContext = iota + ContextIFD1 + 1
	// ContextMakerNoteCasio is the Casio Type 1 MakerNote (plain IFD, QV series).
	ContextMakerNoteCasio
	// ContextMakerNoteCasioType2 is the Casio Type 2 MakerNote ("QVC\0" and
	// the "AOC\0" layout of some Pentax and Asahi cameras).
	ContextMakerNoteCasioType2
	// ContextMakerNoteFujifilm is the Fujifilm MakerNote.
	ContextMakerNoteFujifilm
	// ContextMakerNoteKyocera is the Kyocera MakerNote.
	ContextMakerNoteKyocera
	// ContextMakerNoteLeica is the Leica MakerNote of the cameras whose Make
	// is "Leica Camera AG".
	ContextMakerNoteLeica
	// ContextMakerNoteNikonType1 is the Nikon Type 1 MakerNote ("Nikon\0" and
	// version 1, the Coolpix 100 to 900 series).
	ContextMakerNoteNikonType1
	// ContextMakerNoteNikon is the Nikon Type 2 and Type 3 MakerNote (every
	// other Nikon layout).
	ContextMakerNoteNikon
	// ContextMakerNoteOlympus is the Olympus MakerNote, including the OM
	// System, Epson, Agfa and Minolta layouts that use its tags.
	ContextMakerNoteOlympus
	// ContextMakerNotePanasonic is the Panasonic MakerNote, also used by the
	// Leica cameras whose Make is "LEICA".
	ContextMakerNotePanasonic
	// ContextMakerNotePentax is the Pentax and Asahi MakerNote.
	ContextMakerNotePentax
	// ContextMakerNoteRicoh is the Ricoh MakerNote.
	ContextMakerNoteRicoh
	// ContextMakerNoteSanyo is the Sanyo MakerNote.
	ContextMakerNoteSanyo
	// ContextMakerNoteSigma is the Sigma and Foveon MakerNote.
	ContextMakerNoteSigma
	// ContextMakerNoteSony is the Sony MakerNote.
	ContextMakerNoteSony
	// ContextMakerNoteSonyEricsson is the Sony Ericsson ("SEMC MS") MakerNote.
	ContextMakerNoteSonyEricsson

	// contextEnd is one past the last context.
	contextEnd
)

// makerNoteTables maps each MakerNote context to its generated table, which
// is sorted by tag ID (makernote_tags_gen.go). Read-only after package
// initialisation.
//
//nolint:gochecknoglobals // read-only table, indexed by context
var makerNoteTables = [...][]makerNoteTag{
	0: makerNoteTagsCanon[:], // ContextMakerNoteCanon
	ContextMakerNoteCasio - ContextMakerNoteCanon:        makerNoteTagsCasioType1[:],
	ContextMakerNoteCasioType2 - ContextMakerNoteCanon:   makerNoteTagsCasioType2[:],
	ContextMakerNoteFujifilm - ContextMakerNoteCanon:     makerNoteTagsFujifilm[:],
	ContextMakerNoteKyocera - ContextMakerNoteCanon:      makerNoteTagsKyocera[:],
	ContextMakerNoteLeica - ContextMakerNoteCanon:        makerNoteTagsLeica[:],
	ContextMakerNoteNikonType1 - ContextMakerNoteCanon:   makerNoteTagsNikonType1[:],
	ContextMakerNoteNikon - ContextMakerNoteCanon:        makerNoteTagsNikonType2[:],
	ContextMakerNoteOlympus - ContextMakerNoteCanon:      makerNoteTagsOlympus[:],
	ContextMakerNotePanasonic - ContextMakerNoteCanon:    makerNoteTagsPanasonic[:],
	ContextMakerNotePentax - ContextMakerNoteCanon:       makerNoteTagsPentax[:],
	ContextMakerNoteRicoh - ContextMakerNoteCanon:        makerNoteTagsRicoh[:],
	ContextMakerNoteSanyo - ContextMakerNoteCanon:        makerNoteTagsSanyo[:],
	ContextMakerNoteSigma - ContextMakerNoteCanon:        makerNoteTagsSigma[:],
	ContextMakerNoteSony - ContextMakerNoteCanon:         makerNoteTagsSonyType1[:],
	ContextMakerNoteSonyEricsson - ContextMakerNoteCanon: makerNoteTagsSonyType6[:],
}

// GPS tag IDs occupy 0x0000 to 0x001F (CIPA DC-008 §4.6.6, Table 15), a range
// that no TIFF or Exif tag uses (TIFF tags start at 0x00FE).
const gpsMaxTag TagID = 0x001F

// Interoperability tag IDs (CIPA DC-008 §4.6.7 and Annex A; DCF 2.0 §4.7.2):
// 0x0001 InteroperabilityIndex, 0x0002 InteroperabilityVersion and the
// related-image tags 0x1000 to 0x1002.
const (
	interopRelatedFirst TagID = 0x1000
	interopRelatedLast  TagID = 0x1002
)

// TagNameIn returns the name of tag in the IFD that ctx names, or an empty
// string when the context does not define the tag or ctx is not a valid
// context. Unlike TagName, which looks a tag ID up in one table for all IFDs,
// TagNameIn never returns a GPS name for an Interoperability tag or the other
// way round, and it names MakerNote tags.
//
//	exif.TagNameIn(exif.ContextGPS, 1)     // "GPSLatitudeRef"
//	exif.TagNameIn(exif.ContextInterop, 1) // "InteropIndex"
//	exif.TagNameIn(exif.ContextMakerNoteCanon, 0x0095) // "LensModel"
//
// ContextIFD0, ContextIFD1 and ContextExif share one table of TIFF, TIFF/EP,
// Exif and DNG tags, without the IDs that belong to the GPS and
// Interoperability IFDs. MakerNote names come from the tables of
// metadata-extractor; they are CamelCase like the names of TagName. It does
// not allocate.
func TagNameIn(ctx TagContext, tag TagID) string {
	switch ctx { //nolint:exhaustive // the MakerNote contexts are range-checked below
	case ContextIFD0, ContextIFD1, ContextExif:
		if tag <= gpsMaxTag || (tag >= interopRelatedFirst && tag <= interopRelatedLast) {
			return ""
		}
		return tagRegistry[tag].Name
	case ContextGPS:
		if tag > gpsMaxTag {
			return ""
		}
		return tagRegistry[tag].Name
	case ContextInterop:
		return interopTagName(tag)
	}
	if ctx >= ContextMakerNoteCanon && ctx < contextEnd {
		return lookupMakerNoteTag(makerNoteTables[ctx-ContextMakerNoteCanon], tag)
	}
	return ""
}

// interopTagName names the tags of the Interoperability IFD (CIPA DC-008
// §4.6.7; the names are those of the tag list of the specification).
func interopTagName(tag TagID) string {
	switch tag {
	case TagInteroperabilityIndex:
		return "InteropIndex"
	case TagInteroperabilityVersion:
		return "InteropVersion"
	case interopRelatedFirst, interopRelatedFirst + 1, interopRelatedLast:
		return tagRegistry[tag].Name
	}
	return ""
}

// lookupMakerNoteTag binary-searches the table, which is sorted by ID.
func lookupMakerNoteTag(table []makerNoteTag, tag TagID) string {
	lo, hi := 0, len(table)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		switch id := TagID(table[mid].id); {
		case id < tag:
			lo = mid + 1
		case id > tag:
			hi = mid
		default:
			return table[mid].name
		}
	}
	return ""
}

// MakerNoteContext returns the TagContext of the MakerNote of e, which selects
// the manufacturer tag table for TagNameIn. The layout is detected as Parse
// detects it (signature first, then the Make of IFD0). ok is false when e has
// no MakerNote or when no tag table exists for its layout: Apple, DJI, Samsung
// and Leica Type 5 (the tag reference of those tables is ExifTool, which this
// library does not copy), and the binary Kodak and Reconyx layouts. It does not
// parse the MakerNote and does not allocate.
func (e *EXIF) MakerNoteContext() (ctx TagContext, ok bool) {
	if e == nil || len(e.MakerNote) == 0 {
		return 0, false
	}
	var mk []byte
	if e.IFD0 != nil {
		if me := e.IFD0.Get(TagMake); me != nil {
			mk = trimMake(me.Value)
		}
	}
	return makerNoteContextOf(detectLayout(e.MakerNote, mk), mk)
}

// makerNoteContextOf maps a detected layout to its tag-table context. The
// mapping follows the directory class that metadata-extractor selects for the
// layout (ExifTiffHandler.processMakernote); mk is the trimmed Make.
//
//nolint:cyclop,gocyclo // one flat layout table; each case is a single mapping
func makerNoteContextOf(l mnLayout, mk []byte) (TagContext, bool) {
	switch l { //nolint:exhaustive // the layouts without a table (see MakerNoteContext) fall to the default
	case mnCanon:
		return ContextMakerNoteCanon, true
	case mnCasio:
		return ContextMakerNoteCasio, true
	case mnCasio2, mnPentaxAOC:
		// "AOC\0" is read with the Casio Type 2 tags (processMakernote: "NON-Standard
		// TIFF IFD Data using Casio Type 2 Tags", Pentax ist D).
		return ContextMakerNoteCasioType2, true
	case mnFujifilm:
		return ContextMakerNoteFujifilm, true
	case mnKyocera:
		return ContextMakerNoteKyocera, true
	case mnLeicaTIFF, mnLeicaMake:
		// processMakernote: Make "Leica Camera AG" selects the Leica directory,
		// Make "LEICA" the Panasonic one.
		switch string(mk) {
		case "Leica Camera AG":
			return ContextMakerNoteLeica, true
		case "LEICA":
			return ContextMakerNotePanasonic, true
		}
	case mnNikon1:
		return ContextMakerNoteNikonType1, true
	case mnNikon2, mnNikon3:
		return ContextMakerNoteNikon, true
	case mnOlympusOld, mnOlympus, mnOMSystem, mnOlympusMake, mnMinolta:
		return ContextMakerNoteOlympus, true
	case mnPanasonic:
		return ContextMakerNotePanasonic, true
	case mnPentax, mnPentaxPlain:
		return ContextMakerNotePentax, true
	case mnRicoh:
		return ContextMakerNoteRicoh, true
	case mnSanyo:
		return ContextMakerNoteSanyo, true
	case mnSigma:
		return ContextMakerNoteSigma, true
	case mnSonyCam, mnSony:
		return ContextMakerNoteSony, true
	case mnSonyEricsson:
		return ContextMakerNoteSonyEricsson, true
	}
	return 0, false
}
