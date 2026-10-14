// Package reconcile holds the declarative mapping between EXIF tags, IPTC IIM
// DataSets and XMP properties, and the pure policy functions that keep the
// three metadata forms of an image consistent.
//
// The mapping tables are transcribed from the Adobe XMP Toolkit SDK
// (ReconcileTIFF.cpp and IPTC_Support.cpp, see tables.go). On top of them,
// Fields describes the reconciled fields that the convenience API of the root
// package reads and writes: where each field lives in EXIF, IIM and XMP, how
// each XMP property is written, and in which order the forms are read. The
// policy functions implement the rules of the Metadata Working Group
// Guidelines for Handling Image Metadata, version 2.0 (November 2010), cited
// as "MWG §x.y" below.
//
// The package has no state and allocates only where a function returns a new
// string or slice; every table is statically initialised and read-only.
package reconcile

// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FormatSupport/ReconcileTIFF.cpp:TIFF_MappingToXMP,kExport_Never,kExport_Always,kExport_NoDelete,kExport_InjectOnly, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause
// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FormatSupport/IPTC_Support.hpp:DataSetCharacteristics,kIPTC_MapSimple,kIPTC_MapLangAlt,kIPTC_MapArray,kIPTC_MapSpecial,kIPTC_Map3Way,kIPTC_UnmappedText,kIPTC_UnmappedBin, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// The row types and their enumerations are transcribed from the Toolkit; the
// lookups are original. See THIRD_PARTY_NOTICES.md for the licence note on
// these files.

// IFD identifies the TIFF image file directory that holds a mapped tag.
type IFD uint8

// TIFF directories of the mapped tags (CIPA DC-008-2023 §4.6.2).
const (
	// IFDNone marks a field without an EXIF form.
	IFDNone IFD = iota
	// IFDPrimary is IFD0, the directory of the primary image.
	IFDPrimary
	// IFDExif is the Exif IFD, pointed to by tag 0x8769 of IFD0.
	IFDExif
	// IFDGPS is the GPS Info IFD, pointed to by tag 0x8825 of IFD0.
	IFDGPS
)

// TIFFType is a TIFF field type as the Toolkit tables use it.
type TIFFType uint16

// TIFF field types (TIFF 6.0 §2). TypeShortOrLong is the Toolkit pseudo-type
// kTIFF_ShortOrLongType: the tag may be SHORT or LONG.
const (
	TypeShortOrLong TIFFType = 0
	TypeByte        TIFFType = 1
	TypeASCII       TIFFType = 2
	TypeShort       TIFFType = 3
	TypeLong        TIFFType = 4
	TypeRational    TIFFType = 5
	TypeUndefined   TIFFType = 7
	TypeSRational   TIFFType = 10
)

// Export is the Toolkit export mode of a TIFF mapping: what an XMP-to-EXIF
// export does with the native tag (ReconcileTIFF.cpp, kExport_*).
type Export uint8

// Toolkit export modes.
const (
	// ExportNever never writes the tag from XMP.
	ExportNever Export = iota
	// ExportAlways adds, modifies or deletes the tag.
	ExportAlways
	// ExportNoDelete adds or modifies the tag, and keeps it when the XMP is
	// absent.
	ExportNoDelete
	// ExportInjectOnly adds the tag when it is absent and never modifies or
	// deletes an existing value.
	ExportInjectOnly
)

// TIFFMapping is one row of a Toolkit TIFF mapping table: a tag, its expected
// type and count (0 means any count), its export mode, and the XMP property
// it maps to. NS and Name are empty when the tag has a special mapping.
type TIFFMapping struct {
	NS     string
	Name   string
	Count  uint32
	Tag    uint16
	Type   TIFFType
	Export Export
}

// TIFFMappings returns the Toolkit mapping table of ifd, in Toolkit order, or
// nil for IFDNone. The returned slice aliases a read-only table and must not
// be modified.
func TIFFMappings(ifd IFD) []TIFFMapping {
	switch ifd {
	case IFDPrimary:
		return primaryIFDMappings[:]
	case IFDExif:
		return exifIFDMappings[:]
	case IFDGPS:
		return gpsIFDMappings[:]
	default:
		return nil
	}
}

// TIFF returns the Toolkit mapping of tag in ifd. The boolean is false when
// the Toolkit does not map the tag. The lookup does not allocate.
func TIFF(ifd IFD, tag uint16) (TIFFMapping, bool) {
	for _, m := range TIFFMappings(ifd) {
		if m.Tag == tag {
			return m, true
		}
	}
	return TIFFMapping{}, false
}

// IIMForm is the Toolkit mapping form of an IIM DataSet
// (IPTC_Support.hpp, kIPTC_Map*).
type IIMForm uint8

// Toolkit IIM mapping forms.
const (
	// IIMSimple maps the DataSet to a simple XMP property.
	IIMSimple IIMForm = iota
	// IIMLangAlt maps the DataSet to the x-default item of a language
	// alternative.
	IIMLangAlt
	// IIMArray maps every occurrence of the DataSet to an item of an array.
	IIMArray
	// IIMSpecial needs DataSet-specific code (dates, genre, subject code).
	IIMSpecial
	// IIM3Way is mapped together with an EXIF tag and an XMP property.
	IIM3Way
	// IIMUnmappedText is a text DataSet without an XMP mapping.
	IIMUnmappedText
	// IIMUnmappedBin is a binary DataSet without an XMP mapping.
	IIMUnmappedBin
)

// IIMMapping is one row of the Toolkit IIM table: a record 2 DataSet number,
// its mapping form, its maximum length in bytes, and the XMP property it maps
// to (empty for 3-way, special and unmapped DataSets).
type IIMMapping struct {
	NS      string
	Name    string
	MaxLen  uint32
	DataSet uint8
	Form    IIMForm
}

// IIMMappings returns the Toolkit IIM table in ascending DataSet order. The
// returned slice aliases a read-only table and must not be modified.
func IIMMappings() []IIMMapping { return iimDataSets[:] }

// IIM returns the Toolkit mapping of record 2 DataSet ds. The boolean is false
// when the Toolkit does not know the DataSet. The table is sorted by DataSet
// number, so the lookup is a binary search; it does not allocate.
func IIM(ds uint8) (IIMMapping, bool) {
	lo, hi := 0, len(iimDataSets)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if iimDataSets[mid].DataSet < ds {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(iimDataSets) && iimDataSets[lo].DataSet == ds {
		return iimDataSets[lo], true
	}
	return IIMMapping{}, false
}
