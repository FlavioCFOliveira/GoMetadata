package iptc

// Adapted from bep/imagemeta metadecoder_iptc_fields.json (cross-check of the dataset list and of the repeatable flags), Copyright (c) 2022 Bjørn Erik Pedersen, SPDX-License-Identifier: MIT
//
// See THIRD_PARTY_NOTICES.md. Names, lengths and repeatability of the standard
// datasets come from the IPTC-NAA Information Interchange Model 4.2 (July 2014);
// bep/imagemeta is used only to cross-check the dataset list (its table is
// itself generated from ExifTool documentation, so none of its names or notes
// are copied).

// DatasetFormat classifies how the value of a dataset is encoded
// (IIM 4.2 §1.6 and the dataset descriptions of chapters 5, 6, 11-13).
type DatasetFormat uint8

// Dataset value formats.
const (
	// FormatUnspecified marks a dataset whose value format is not defined by
	// IIM 4.2 (record 3 is defined in a separate publication; vendor datasets
	// have no standard).
	FormatUnspecified DatasetFormat = iota
	// FormatText is a character string (graphic characters, numeric
	// characters or alphabetic characters; IIM 4.2 §6).
	FormatText
	// FormatUint is a big-endian unsigned binary number.
	FormatUint
	// FormatBytes is opaque binary data.
	FormatBytes
)

// DatasetInfo describes one IIM dataset: its identity, its value format and
// the constraints of IIM 4.2.
type DatasetInfo struct {
	// Name is the dataset name without spaces or punctuation.
	Name string
	// MinLen and MaxLen bound the value length in octets. MaxLen is 0 when
	// IIM 4.2 defines no upper bound (or no definition at all); MinLen is 0
	// when the dataset may be empty or has no defined minimum.
	MinLen, MaxLen int
	// Record and Number are the record and dataset numbers ("2:120").
	Record, Number uint8
	// Format is the value format.
	Format DatasetFormat
	// Repeatable reports whether the dataset may occur more than once in a
	// record.
	Repeatable bool
	// Standard is true for datasets defined by IIM 4.2 or by the IPTC-NAA
	// Digital Newsphoto Parameter Record, and false for widely used
	// non-standard datasets.
	Standard bool
}

// datasetTable lists every known dataset ordered by (Record, Number); it is
// searched by binary search in LookupDataset, which keeps the lookup
// allocation-free and avoids a map initialisation at start-up.
//
// IIM 4.2 sections: record 1 chapter 5, record 2 chapter 6, record 7 chapter
// 11, record 8 chapter 12, record 9 chapter 13. Record 3 (chapter 7) is
// defined in a separate publication, so its value formats and lengths are
// unspecified here.
var datasetTable = [...]DatasetInfo{ //nolint:gochecknoglobals // read-only spec-sourced table
	{Record: 1, Number: 0, Name: "EnvelopeRecordVersion", Format: FormatUint, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 1, Number: 5, Name: "Destination", Format: FormatText, MaxLen: 1024, Repeatable: true, Standard: true},
	{Record: 1, Number: 20, Name: "FileFormat", Format: FormatUint, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 1, Number: 22, Name: "FileFormatVersion", Format: FormatUint, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 1, Number: 30, Name: "ServiceIdentifier", Format: FormatText, MaxLen: 10, Standard: true},
	{Record: 1, Number: 40, Name: "EnvelopeNumber", Format: FormatText, MinLen: 8, MaxLen: 8, Standard: true},
	{Record: 1, Number: 50, Name: "ProductID", Format: FormatText, MaxLen: 32, Repeatable: true, Standard: true},
	{Record: 1, Number: 60, Name: "EnvelopePriority", Format: FormatText, MinLen: 1, MaxLen: 1, Standard: true},
	{Record: 1, Number: 70, Name: "DateSent", Format: FormatText, MinLen: 8, MaxLen: 8, Standard: true},
	{Record: 1, Number: 80, Name: "TimeSent", Format: FormatText, MinLen: 11, MaxLen: 11, Standard: true},
	{Record: 1, Number: 90, Name: "CodedCharacterSet", Format: FormatText, MaxLen: 32, Standard: true},
	{Record: 1, Number: 100, Name: "UniqueObjectName", Format: FormatText, MinLen: 14, MaxLen: 80, Standard: true},
	{Record: 1, Number: 120, Name: "ARMIdentifier", Format: FormatUint, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 1, Number: 122, Name: "ARMVersion", Format: FormatUint, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 2, Number: 0, Name: "ApplicationRecordVersion", Format: FormatUint, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 2, Number: 3, Name: "ObjectTypeReference", Format: FormatText, MinLen: 3, MaxLen: 67, Standard: true},
	{Record: 2, Number: 4, Name: "ObjectAttributeReference", Format: FormatText, MinLen: 4, MaxLen: 68, Repeatable: true, Standard: true},
	{Record: 2, Number: 5, Name: "ObjectName", Format: FormatText, MaxLen: 64, Standard: true},
	{Record: 2, Number: 7, Name: "EditStatus", Format: FormatText, MaxLen: 64, Standard: true},
	{Record: 2, Number: 8, Name: "EditorialUpdate", Format: FormatText, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 2, Number: 10, Name: "Urgency", Format: FormatText, MinLen: 1, MaxLen: 1, Standard: true},
	{Record: 2, Number: 12, Name: "SubjectReference", Format: FormatText, MinLen: 13, MaxLen: 236, Repeatable: true, Standard: true},
	{Record: 2, Number: 15, Name: "Category", Format: FormatText, MaxLen: 3, Standard: true},
	{Record: 2, Number: 20, Name: "SupplementalCategory", Format: FormatText, MaxLen: 32, Repeatable: true, Standard: true},
	{Record: 2, Number: 22, Name: "FixtureIdentifier", Format: FormatText, MaxLen: 32, Standard: true},
	{Record: 2, Number: 25, Name: "Keywords", Format: FormatText, MaxLen: 64, Repeatable: true, Standard: true},
	{Record: 2, Number: 26, Name: "ContentLocationCode", Format: FormatText, MinLen: 3, MaxLen: 3, Repeatable: true, Standard: true},
	{Record: 2, Number: 27, Name: "ContentLocationName", Format: FormatText, MaxLen: 64, Repeatable: true, Standard: true},
	{Record: 2, Number: 30, Name: "ReleaseDate", Format: FormatText, MinLen: 8, MaxLen: 8, Standard: true},
	{Record: 2, Number: 35, Name: "ReleaseTime", Format: FormatText, MinLen: 11, MaxLen: 11, Standard: true},
	{Record: 2, Number: 37, Name: "ExpirationDate", Format: FormatText, MinLen: 8, MaxLen: 8, Standard: true},
	{Record: 2, Number: 38, Name: "ExpirationTime", Format: FormatText, MinLen: 11, MaxLen: 11, Standard: true},
	{Record: 2, Number: 40, Name: "SpecialInstructions", Format: FormatText, MaxLen: 256, Standard: true},
	{Record: 2, Number: 42, Name: "ActionAdvised", Format: FormatText, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 2, Number: 45, Name: "ReferenceService", Format: FormatText, MaxLen: 10, Repeatable: true, Standard: true},
	{Record: 2, Number: 47, Name: "ReferenceDate", Format: FormatText, MinLen: 8, MaxLen: 8, Repeatable: true, Standard: true},
	{Record: 2, Number: 50, Name: "ReferenceNumber", Format: FormatText, MinLen: 8, MaxLen: 8, Repeatable: true, Standard: true},
	{Record: 2, Number: 55, Name: "DateCreated", Format: FormatText, MinLen: 8, MaxLen: 8, Standard: true},
	{Record: 2, Number: 60, Name: "TimeCreated", Format: FormatText, MinLen: 11, MaxLen: 11, Standard: true},
	{Record: 2, Number: 62, Name: "DigitalCreationDate", Format: FormatText, MinLen: 8, MaxLen: 8, Standard: true},
	{Record: 2, Number: 63, Name: "DigitalCreationTime", Format: FormatText, MinLen: 11, MaxLen: 11, Standard: true},
	{Record: 2, Number: 65, Name: "OriginatingProgram", Format: FormatText, MaxLen: 32, Standard: true},
	{Record: 2, Number: 70, Name: "ProgramVersion", Format: FormatText, MaxLen: 10, Standard: true},
	{Record: 2, Number: 75, Name: "ObjectCycle", Format: FormatText, MinLen: 1, MaxLen: 1, Standard: true},
	{Record: 2, Number: 80, Name: "Byline", Format: FormatText, MaxLen: 32, Repeatable: true, Standard: true},
	{Record: 2, Number: 85, Name: "BylineTitle", Format: FormatText, MaxLen: 32, Repeatable: true, Standard: true},
	{Record: 2, Number: 90, Name: "City", Format: FormatText, MaxLen: 32, Standard: true},
	{Record: 2, Number: 92, Name: "SubLocation", Format: FormatText, MaxLen: 32, Standard: true},
	{Record: 2, Number: 95, Name: "ProvinceState", Format: FormatText, MaxLen: 32, Standard: true},
	{Record: 2, Number: 100, Name: "CountryPrimaryLocationCode", Format: FormatText, MinLen: 3, MaxLen: 3, Standard: true},
	{Record: 2, Number: 101, Name: "CountryPrimaryLocationName", Format: FormatText, MaxLen: 64, Standard: true},
	{Record: 2, Number: 103, Name: "OriginalTransmissionReference", Format: FormatText, MaxLen: 32, Standard: true},
	{Record: 2, Number: 105, Name: "Headline", Format: FormatText, MaxLen: 256, Standard: true},
	{Record: 2, Number: 110, Name: "Credit", Format: FormatText, MaxLen: 32, Standard: true},
	{Record: 2, Number: 115, Name: "Source", Format: FormatText, MaxLen: 32, Standard: true},
	{Record: 2, Number: 116, Name: "CopyrightNotice", Format: FormatText, MaxLen: 128, Standard: true},
	{Record: 2, Number: 118, Name: "Contact", Format: FormatText, MaxLen: 128, Repeatable: true, Standard: true},
	{Record: 2, Number: 120, Name: "CaptionAbstract", Format: FormatText, MaxLen: 2000, Standard: true},
	{Record: 2, Number: 121, Name: "LocalCaption"},
	{Record: 2, Number: 122, Name: "WriterEditor", Format: FormatText, MaxLen: 32, Repeatable: true, Standard: true},
	{Record: 2, Number: 125, Name: "RasterizedCaption", Format: FormatBytes, MinLen: 7360, MaxLen: 7360, Standard: true},
	{Record: 2, Number: 130, Name: "ImageType", Format: FormatText, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 2, Number: 131, Name: "ImageOrientation", Format: FormatText, MinLen: 1, MaxLen: 1, Standard: true},
	{Record: 2, Number: 135, Name: "LanguageIdentifier", Format: FormatText, MinLen: 2, MaxLen: 3, Standard: true},
	{Record: 2, Number: 150, Name: "AudioType", Format: FormatText, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 2, Number: 151, Name: "AudioSamplingRate", Format: FormatText, MinLen: 6, MaxLen: 6, Standard: true},
	{Record: 2, Number: 152, Name: "AudioSamplingResolution", Format: FormatText, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 2, Number: 153, Name: "AudioDuration", Format: FormatText, MinLen: 6, MaxLen: 6, Standard: true},
	{Record: 2, Number: 154, Name: "AudioOutcue", Format: FormatText, MaxLen: 64, Standard: true},
	{Record: 2, Number: 184, Name: "JobID"},
	{Record: 2, Number: 185, Name: "MasterDocumentID"},
	{Record: 2, Number: 186, Name: "ShortDocumentID"},
	{Record: 2, Number: 187, Name: "UniqueDocumentID"},
	{Record: 2, Number: 188, Name: "OwnerID"},
	{Record: 2, Number: 200, Name: "ObjectDataPreviewFileFormat", Format: FormatUint, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 2, Number: 201, Name: "ObjectDataPreviewFileFormatVersion", Format: FormatUint, MinLen: 2, MaxLen: 2, Standard: true},
	{Record: 2, Number: 202, Name: "ObjectDataPreviewData", Format: FormatBytes, MaxLen: 256000, Standard: true},
	{Record: 2, Number: 221, Name: "EditorPreferences"},
	{Record: 2, Number: 225, Name: "ClassifyState"},
	{Record: 2, Number: 228, Name: "SimilarityIndex"},
	{Record: 2, Number: 230, Name: "DocumentNotes"},
	{Record: 2, Number: 231, Name: "DocumentHistory"},
	{Record: 2, Number: 232, Name: "CameraInfo"},
	{Record: 2, Number: 255, Name: "CatalogSets", Repeatable: true},
	{Record: 3, Number: 0, Name: "NewsPhotoVersion", Standard: true},
	{Record: 3, Number: 10, Name: "PictureNumber", Standard: true},
	{Record: 3, Number: 20, Name: "PixelsPerLine", Standard: true},
	{Record: 3, Number: 30, Name: "NumberOfLines", Standard: true},
	{Record: 3, Number: 40, Name: "PixelSizeInScanningDirection", Standard: true},
	{Record: 3, Number: 50, Name: "PixelSizePerpendicular", Standard: true},
	{Record: 3, Number: 55, Name: "SupplementalType", Standard: true},
	{Record: 3, Number: 60, Name: "ColourRepresentation", Standard: true},
	{Record: 3, Number: 64, Name: "InterchangeColourSpace", Standard: true},
	{Record: 3, Number: 65, Name: "ColourSequence", Standard: true},
	{Record: 3, Number: 66, Name: "ICCInputColourProfile", Standard: true},
	{Record: 3, Number: 70, Name: "ColourCalibrationMatrix", Standard: true},
	{Record: 3, Number: 80, Name: "LookupTable", Standard: true},
	{Record: 3, Number: 84, Name: "NumberOfIndexEntries", Standard: true},
	{Record: 3, Number: 85, Name: "ColourPalette", Standard: true},
	{Record: 3, Number: 86, Name: "NumberOfBitsPerSample", Standard: true},
	{Record: 3, Number: 90, Name: "SamplingStructure", Standard: true},
	{Record: 3, Number: 100, Name: "ScanningDirection", Standard: true},
	{Record: 3, Number: 102, Name: "ImageRotation", Standard: true},
	{Record: 3, Number: 110, Name: "DataCompressionMethod", Standard: true},
	{Record: 3, Number: 120, Name: "QuantisationMethod", Standard: true},
	{Record: 3, Number: 125, Name: "EndPoints", Standard: true},
	{Record: 3, Number: 130, Name: "ExcursionTolerance", Standard: true},
	{Record: 3, Number: 135, Name: "BitsPerComponent", Standard: true},
	{Record: 3, Number: 140, Name: "MaximumDensityRange", Standard: true},
	{Record: 3, Number: 145, Name: "GammaCompensatedValue", Standard: true},
	{Record: 7, Number: 10, Name: "SizeMode", Format: FormatUint, MinLen: 1, MaxLen: 1, Standard: true},
	{Record: 7, Number: 20, Name: "MaxSubfileSize", Format: FormatUint, MaxLen: 4, Standard: true},
	{Record: 7, Number: 90, Name: "ObjectDataSizeAnnounced", Format: FormatUint, MaxLen: 4, Standard: true},
	{Record: 7, Number: 95, Name: "MaximumObjectDataSize", Format: FormatUint, MaxLen: 4, Standard: true},
	{Record: 8, Number: 10, Name: "Subfile", Format: FormatBytes, Repeatable: true, Standard: true},
	{Record: 9, Number: 10, Name: "ConfirmedObjectDataSize", Format: FormatUint, MaxLen: 4, Standard: true},
}

// LookupDataset returns the description of dataset record:number (for
// example 2, 120 for Caption/Abstract). The boolean is false when the dataset
// is unknown. The lookup does not allocate.
func LookupDataset(record, number uint8) (DatasetInfo, bool) {
	key := uint16(record)<<8 | uint16(number)
	lo, hi := 0, len(datasetTable)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		e := &datasetTable[mid]
		if uint16(e.Record)<<8|uint16(e.Number) < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(datasetTable) {
		e := &datasetTable[lo]
		if e.Record == record && e.Number == number {
			return *e, true
		}
	}
	return DatasetInfo{}, false
}
