package reconcile

// Field identifies one field that the root package keeps consistent across
// EXIF, IPTC IIM and XMP.
type Field uint8

// Reconciled fields. Each one is set by one convenience setter of the root
// package (Metadata.SetCaption and the others) and read by the getter of the
// same name.
const (
	// Description is the caption of the image (MWG §5.2).
	Description Field = iota
	// Copyright is the copyright notice (MWG §5.6).
	Copyright
	// Creator is the list of creators (MWG §5.7).
	Creator
	// Keywords is the list of keywords (MWG §5.1).
	Keywords
	// DateTimeOriginal is the date and time the photograph was taken
	// (MWG §5.3).
	DateTimeOriginal
	// Make is the manufacturer of the camera.
	Make
	// Model is the model of the camera.
	Model
	// LensModel is the model of the lens.
	LensModel
	// GPSLatitude is the latitude of the camera (MWG §5.8.1).
	GPSLatitude
	// GPSLongitude is the longitude of the camera (MWG §5.8.1).
	GPSLongitude
	// ExposureTime is the exposure time in seconds.
	ExposureTime
	// FNumber is the F number.
	FNumber
	// ISO is the photographic sensitivity.
	ISO
	// FocalLength is the focal length of the lens in millimetres.
	FocalLength
	// Orientation is the orientation of the primary image (MWG §5.4).
	Orientation
	// PixelXDimension is the width of the primary image in pixels.
	PixelXDimension
	// PixelYDimension is the height of the primary image in pixels.
	PixelYDimension

	// FieldCount is the number of reconciled fields.
	FieldCount
)

// XMPWrite tells how a setter writes one XMP property of a field.
type XMPWrite uint8

// XMP write modes.
const (
	// WriteAlways creates or replaces the property with the value the caller
	// passed to the setter. It is used for the properties that the MWG maps
	// to an EXIF or IIM form (MWG §4.2.3.1 and §4.2.3.2, "Writing": a
	// Changer SHOULD update both forms of a value), and for the camera
	// properties that the setters have always written to XMP.
	WriteAlways XMPWrite = iota + 1
	// WriteIfPresent replaces the property only when the XMP packet already
	// has it, with the value of the EXIF tag the setter has just written,
	// formatted as the Toolkit imports it (ReconcileTIFF.cpp,
	// ImportSingleTIFF_*). MWG §4.2.3.1, "Writing": EXIF device properties
	// SHOULD NOT be duplicated in the exif: and tiff: namespaces when the
	// file format supports EXIF, but when both forms exist a Changer SHOULD
	// update both, so an existing copy is updated and none is created.
	WriteIfPresent
)

// XMPProperty is one XMP property of a field.
type XMPProperty struct {
	NS    string
	Name  string
	Write XMPWrite
}

// ReadOrder tells how a getter chooses among the forms of a field.
type ReadOrder uint8

// Read orders.
const (
	// ReadEXIF reads the EXIF form only.
	ReadEXIF ReadOrder = iota + 1
	// ReadEXIFThenXMP reads the EXIF form, then the XMP properties in table
	// order; the first non-empty value wins.
	ReadEXIFThenXMP
	// ReadEXIFThenXMPThenIIM reads EXIF, then XMP, then IIM, and lets an IIM
	// value win when the IPTC digest shows that the IIM changed behind the
	// back of the XMP and the IIM value differs from the value predicted
	// from the XMP (MWG v2.0 §4.2.3.1, §4.2.3.2 and §4.2.3.3; the policy
	// stated in ReconcileTIFF.cpp at PhotoDataUtils::Import3WayItems).
	ReadEXIFThenXMPThenIIM
	// ReadXMPThenIIM reads XMP, then IIM, with the same digest rule
	// (PhotoDataUtils::Import2WayIPTC).
	ReadXMPThenIIM
	// ReadDate applies ReadEXIFThenXMPThenIIM to dates: an edited IIM date
	// (digest rule) first, then the EXIF form with its sub-second and offset
	// tags, then the first XMP property (the XMP copy of the EXIF tag), then
	// the second XMP property, then the IIM date and time (MWG §5.3).
	ReadDate
)

// Mapping describes where a field lives in each metadata form and how it is
// written and read.
type Mapping struct {
	// MWG is the name of the field in MWG v2.0 §5, or "" when the MWG does
	// not define the field.
	MWG string
	// XMP lists the XMP properties of the field, in read order. Unused
	// entries are zero.
	XMP [2]XMPProperty
	// Tag is the EXIF tag in IFD; it is 0 when IFD is IFDNone.
	Tag uint16
	// IFD is the directory of the EXIF tag.
	IFD IFD
	// IIMMaxLen is the maximum length in bytes of the IIM DataSet, from the
	// Toolkit IIM table (TestFields_AgreeWithToolkitTables proves the copy),
	// or 0 when the field has no IIM form.
	IIMMaxLen uint16
	// DataSet is the IIM record 2 DataSet, or 0 when the field has no IIM
	// form.
	DataSet uint8
	// Read is the read order of the getter.
	Read ReadOrder
}

// fields is indexed by Field. The EXIF tags and IIM DataSets are the ones of
// the MWG v2.0 §5 property tables; the XMP properties are the ones of the
// Toolkit mapping tables (tables.go), except where a comment says otherwise.
// TestFields_AgreeWithToolkitTables proves every EXIF/XMP pair against the
// Toolkit rows.
var fields = [FieldCount]Mapping{ //nolint:gochecknoglobals // read-only table; statically initialised, never mutated
	// MWG §5.2: EXIF ImageDescription (0x010E), IIM 2:120, XMP
	// dc:description["x-default"]. Toolkit: 0x010E has a special mapping,
	// 2:120 is a 3-way DataSet.
	Description: {
		MWG: "Description", IFD: IFDPrimary, Tag: 0x010E, DataSet: 120, IIMMaxLen: 2000,
		XMP:  [2]XMPProperty{{NS: nsDC, Name: "description", Write: WriteAlways}},
		Read: ReadEXIFThenXMPThenIIM,
	},
	// MWG §5.6: EXIF Copyright (0x8298), IIM 2:116, XMP dc:rights.
	Copyright: {
		MWG: "Copyright", IFD: IFDPrimary, Tag: 0x8298, DataSet: 116, IIMMaxLen: 128,
		XMP:  [2]XMPProperty{{NS: nsDC, Name: "rights", Write: WriteAlways}},
		Read: ReadEXIFThenXMPThenIIM,
	},
	// MWG §5.7: EXIF Artist (0x013B, a "; "-separated list), IIM 2:80
	// (repeatable), XMP dc:creator (ordered array).
	Creator: {
		MWG: "Creator", IFD: IFDPrimary, Tag: 0x013B, DataSet: 80, IIMMaxLen: 32,
		XMP:  [2]XMPProperty{{NS: nsDC, Name: "creator", Write: WriteAlways}},
		Read: ReadEXIFThenXMPThenIIM,
	},
	// MWG §5.1: IIM 2:25 (repeatable) and XMP dc:subject; no EXIF form.
	Keywords: {
		MWG: "Keywords", DataSet: 25, IIMMaxLen: 64,
		XMP:  [2]XMPProperty{{NS: nsDC, Name: "subject", Write: WriteAlways}},
		Read: ReadXMPThenIIM,
	},
	// MWG §5.3: EXIF DateTimeOriginal (0x9003) with SubSecTimeOriginal
	// (0x9291), IIM 2:55 with 2:60, XMP photoshop:DateCreated. The Toolkit
	// maps 0x9003 to exif:DateTimeOriginal and 2:55 to photoshop:DateCreated
	// ("a proposed tweak to MWG", ReconcileIPTC.cpp:ImportIPTC_Date); both
	// XMP properties are written.
	DateTimeOriginal: {
		MWG: "DateTimeOriginal", IFD: IFDExif, Tag: 0x9003, DataSet: 55, IIMMaxLen: 8,
		XMP: [2]XMPProperty{
			{NS: nsEXIF, Name: "DateTimeOriginal", Write: WriteAlways},
			{NS: nsPhotoshop, Name: "DateCreated", Write: WriteAlways},
		},
		Read: ReadDate,
	},
	Make: {
		IFD: IFDPrimary, Tag: 0x010F,
		XMP:  [2]XMPProperty{{NS: nsTIFF, Name: "Make", Write: WriteIfPresent}},
		Read: ReadEXIFThenXMP,
	},
	Model: {
		IFD: IFDPrimary, Tag: 0x0110,
		XMP:  [2]XMPProperty{{NS: nsTIFF, Name: "Model", Write: WriteAlways}},
		Read: ReadEXIFThenXMP,
	},
	// aux:Lens is the Adobe auxiliary property that the setter has always
	// written; exifEX:LensModel is the Toolkit mapping of 0xA434
	// (CIPA DC-010-2012).
	LensModel: {
		IFD: IFDExif, Tag: 0xA434,
		XMP: [2]XMPProperty{
			{NS: nsAux, Name: "Lens", Write: WriteAlways},
			{NS: nsExifEX, Name: "LensModel", Write: WriteIfPresent},
		},
		Read: ReadEXIFThenXMP,
	},
	// MWG §5.8.1: GPS tags 1 to 6. The Toolkit maps 0x0002 and 0x0004
	// specially to exif:GPSLatitude and exif:GPSLongitude ("DDD,MM.mmR").
	GPSLatitude: {
		IFD: IFDGPS, Tag: 0x0002,
		XMP:  [2]XMPProperty{{NS: nsEXIF, Name: "GPSLatitude", Write: WriteAlways}},
		Read: ReadEXIFThenXMP,
	},
	GPSLongitude: {
		IFD: IFDGPS, Tag: 0x0004,
		XMP:  [2]XMPProperty{{NS: nsEXIF, Name: "GPSLongitude", Write: WriteAlways}},
		Read: ReadEXIFThenXMP,
	},
	ExposureTime: {
		IFD: IFDExif, Tag: 0x829A,
		XMP:  [2]XMPProperty{{NS: nsEXIF, Name: "ExposureTime", Write: WriteIfPresent}},
		Read: ReadEXIF,
	},
	FNumber: {
		IFD: IFDExif, Tag: 0x829D,
		XMP:  [2]XMPProperty{{NS: nsEXIF, Name: "FNumber", Write: WriteIfPresent}},
		Read: ReadEXIF,
	},
	// The Toolkit maps 0x8827 (PhotographicSensitivity, formerly
	// ISOSpeedRatings) specially to the ordered array exif:ISOSpeedRatings
	// and to exifEX:PhotographicSensitivity
	// (ReconcileTIFF.cpp:ImportTIFF_PhotographicSensitivity).
	ISO: {
		IFD: IFDExif, Tag: 0x8827,
		XMP: [2]XMPProperty{
			{NS: nsEXIF, Name: "ISOSpeedRatings", Write: WriteIfPresent},
			{NS: nsExifEX, Name: "PhotographicSensitivity", Write: WriteIfPresent},
		},
		Read: ReadEXIF,
	},
	FocalLength: {
		IFD: IFDExif, Tag: 0x920A,
		XMP:  [2]XMPProperty{{NS: nsEXIF, Name: "FocalLength", Write: WriteIfPresent}},
		Read: ReadEXIF,
	},
	// MWG §5.4: the orientation is represented in EXIF only.
	Orientation: {
		MWG: "Orientation", IFD: IFDPrimary, Tag: 0x0112,
		XMP:  [2]XMPProperty{{NS: nsTIFF, Name: "Orientation", Write: WriteIfPresent}},
		Read: ReadEXIF,
	},
	PixelXDimension: {
		IFD: IFDExif, Tag: 0xA002,
		XMP:  [2]XMPProperty{{NS: nsEXIF, Name: "PixelXDimension", Write: WriteIfPresent}},
		Read: ReadEXIF,
	},
	PixelYDimension: {
		IFD: IFDExif, Tag: 0xA003,
		XMP:  [2]XMPProperty{{NS: nsEXIF, Name: "PixelYDimension", Write: WriteIfPresent}},
		Read: ReadEXIF,
	},
}

// Lookup returns the mapping of f. It panics when f is not a valid Field,
// which is a programming error inside this module.
func Lookup(f Field) *Mapping { return &fields[f] }

// IIMMaxLen returns the maximum length in bytes of the IIM form of f (the
// Toolkit IIM table length of its DataSet), or 0 when f has no IIM form.
func IIMMaxLen(f Field) int { return int(fields[f].IIMMaxLen) }
