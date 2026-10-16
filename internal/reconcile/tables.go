package reconcile

// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FormatSupport/ReconcileTIFF.cpp:sPrimaryIFDMappings,sExifIFDMappings,sGPSInfoIFDMappings, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause
// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FormatSupport/IPTC_Support.cpp:kKnownDataSets, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// The rows are transcribed from the Toolkit tables in their original order,
// which is the order of tables 3, 4, 5 and 12 of Exif 2.2 for the TIFF tables
// and ascending DataSet number for the IIM table. The changes are mechanical:
// the C enumerations are replaced by Go constants, the sentinel rows are
// dropped (Go arrays carry their length), kAnyCount is written as 0, and the
// count 3*256 of TransferFunction is written as 768. See THIRD_PARTY_NOTICES.md
// for the licence note on these files.

// Namespace URIs of the XMP properties named by the tables (Adobe XMP
// Specification Part 2, and CIPA DC-010-2012 for exifEX). They repeat the
// xmp package constants so that this package depends on no other package of
// the module; TestNamespaces_MatchXMPPackage proves the two agree.
const (
	nsTIFF      = "http://ns.adobe.com/tiff/1.0/"
	nsEXIF      = "http://ns.adobe.com/exif/1.0/"
	nsExifEX    = "http://cipa.jp/exif/1.0/"
	nsAux       = "http://ns.adobe.com/exif/1.0/aux/"
	nsDC        = "http://purl.org/dc/elements/1.1/"
	nsPhotoshop = "http://ns.adobe.com/photoshop/1.0/"
	nsIPTCCore  = "http://iptc.org/std/Iptc4xmpCore/1.0/xmlns/"
)

// primaryIFDMappings lists the IFD0 tags that the XMP Toolkit maps to XMP
// (ReconcileTIFF.cpp:sPrimaryIFDMappings). A row with an empty Name has a
// special mapping that the table cannot express (for example ImageDescription,
// which maps to the x-default item of dc:description).
var primaryIFDMappings = [...]TIFFMapping{ //nolint:gochecknoglobals // read-only table transcribed from the XMP Toolkit; statically initialised, never mutated
	{Tag: 256, Type: TypeShortOrLong, Count: 1, Export: ExportNever, NS: nsTIFF, Name: "ImageWidth"},          // ImageWidth
	{Tag: 257, Type: TypeShortOrLong, Count: 1, Export: ExportNever, NS: nsTIFF, Name: "ImageLength"},         // ImageLength
	{Tag: 258, Type: TypeShort, Count: 3, Export: ExportNever, NS: nsTIFF, Name: "BitsPerSample"},             // BitsPerSample
	{Tag: 259, Type: TypeShort, Count: 1, Export: ExportNever, NS: nsTIFF, Name: "Compression"},               // Compression
	{Tag: 262, Type: TypeShort, Count: 1, Export: ExportNever, NS: nsTIFF, Name: "PhotometricInterpretation"}, // PhotometricInterpretation
	{Tag: 274, Type: TypeShort, Count: 1, Export: ExportNoDelete, NS: nsTIFF, Name: "Orientation"},            // Orientation
	{Tag: 277, Type: TypeShort, Count: 1, Export: ExportNever, NS: nsTIFF, Name: "SamplesPerPixel"},           // SamplesPerPixel
	{Tag: 284, Type: TypeShort, Count: 1, Export: ExportNever, NS: nsTIFF, Name: "PlanarConfiguration"},       // PlanarConfiguration
	{Tag: 529, Type: TypeRational, Count: 3, Export: ExportNever, NS: nsTIFF, Name: "YCbCrCoefficients"},      // YCbCrCoefficients
	{Tag: 530, Type: TypeShort, Count: 2, Export: ExportNever, NS: nsTIFF, Name: "YCbCrSubSampling"},          // YCbCrSubSampling
	{Tag: 282, Type: TypeRational, Count: 1, Export: ExportNoDelete, NS: nsTIFF, Name: "XResolution"},         // XResolution
	{Tag: 283, Type: TypeRational, Count: 1, Export: ExportNoDelete, NS: nsTIFF, Name: "YResolution"},         // YResolution
	{Tag: 296, Type: TypeShort, Count: 1, Export: ExportNoDelete, NS: nsTIFF, Name: "ResolutionUnit"},         // ResolutionUnit
	{Tag: 301, Type: TypeShort, Count: 768, Export: ExportNever, NS: nsTIFF, Name: "TransferFunction"},        // TransferFunction
	{Tag: 318, Type: TypeRational, Count: 2, Export: ExportNever, NS: nsTIFF, Name: "WhitePoint"},             // WhitePoint
	{Tag: 319, Type: TypeRational, Count: 6, Export: ExportNever, NS: nsTIFF, Name: "PrimaryChromaticities"},  // PrimaryChromaticities
	{Tag: 531, Type: TypeShort, Count: 1, Export: ExportNever, NS: nsTIFF, Name: "YCbCrPositioning"},          // YCbCrPositioning
	{Tag: 532, Type: TypeRational, Count: 6, Export: ExportNever, NS: nsTIFF, Name: "ReferenceBlackWhite"},    // ReferenceBlackWhite
	{Tag: 306, Type: TypeASCII, Count: 20, Export: ExportAlways, NS: "", Name: ""},                            // DateTime
	{Tag: 270, Type: TypeASCII, Count: 0, Export: ExportAlways, NS: "", Name: ""},                             // ImageDescription
	{Tag: 271, Type: TypeASCII, Count: 0, Export: ExportInjectOnly, NS: nsTIFF, Name: "Make"},                 // Make
	{Tag: 272, Type: TypeASCII, Count: 0, Export: ExportInjectOnly, NS: nsTIFF, Name: "Model"},                // Model
	{Tag: 305, Type: TypeASCII, Count: 0, Export: ExportAlways, NS: nsTIFF, Name: "Software"},                 // Software
	{Tag: 315, Type: TypeASCII, Count: 0, Export: ExportAlways, NS: "", Name: ""},                             // Artist
	{Tag: 33432, Type: TypeASCII, Count: 0, Export: ExportAlways, NS: "", Name: ""},                           // Copyright
}

// exifIFDMappings lists the Exif IFD tags that the XMP Toolkit maps to XMP
// (ReconcileTIFF.cpp:sExifIFDMappings).
var exifIFDMappings = [...]TIFFMapping{ //nolint:gochecknoglobals // read-only table transcribed from the XMP Toolkit; statically initialised, never mutated
	{Tag: 36864, Type: TypeUndefined, Count: 4, Export: ExportInjectOnly, NS: "", Name: ""},                          // ExifVersion
	{Tag: 40960, Type: TypeUndefined, Count: 4, Export: ExportNever, NS: "", Name: ""},                               // FlashpixVersion
	{Tag: 40961, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "ColorSpace"},                // ColorSpace
	{Tag: 42240, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsExifEX, Name: "Gamma"},                // Gamma
	{Tag: 37121, Type: TypeUndefined, Count: 4, Export: ExportInjectOnly, NS: "", Name: ""},                          // ComponentsConfiguration
	{Tag: 37122, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "CompressedBitsPerPixel"}, // CompressedBitsPerPixel
	{Tag: 40962, Type: TypeShortOrLong, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "PixelXDimension"},     // PixelXDimension
	{Tag: 40963, Type: TypeShortOrLong, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "PixelYDimension"},     // PixelYDimension
	{Tag: 37510, Type: TypeUndefined, Count: 0, Export: ExportAlways, NS: "", Name: ""},                              // UserComment
	{Tag: 40964, Type: TypeASCII, Count: 0, Export: ExportAlways, NS: nsEXIF, Name: "RelatedSoundFile"},              // RelatedSoundFile
	{Tag: 36867, Type: TypeASCII, Count: 20, Export: ExportAlways, NS: "", Name: ""},                                 // DateTimeOriginal
	{Tag: 36868, Type: TypeASCII, Count: 20, Export: ExportAlways, NS: "", Name: ""},                                 // DateTimeDigitized
	{Tag: 36880, Type: TypeASCII, Count: 7, Export: ExportAlways, NS: "", Name: ""},                                  // OffsetTime
	{Tag: 36881, Type: TypeASCII, Count: 7, Export: ExportAlways, NS: "", Name: ""},                                  // OffsetTimeOriginal
	{Tag: 36882, Type: TypeASCII, Count: 7, Export: ExportAlways, NS: "", Name: ""},                                  // OffsetTimeDigitized
	{Tag: 42016, Type: TypeASCII, Count: 33, Export: ExportInjectOnly, NS: nsEXIF, Name: "ImageUniqueID"},            // ImageUniqueID
	{Tag: 42032, Type: TypeASCII, Count: 0, Export: ExportInjectOnly, NS: nsExifEX, Name: "CameraOwnerName"},         // CameraOwnerName
	{Tag: 42033, Type: TypeASCII, Count: 0, Export: ExportInjectOnly, NS: nsExifEX, Name: "BodySerialNumber"},        // BodySerialNumber
	{Tag: 42034, Type: TypeRational, Count: 4, Export: ExportInjectOnly, NS: nsExifEX, Name: "LensSpecification"},    // LensSpecification
	{Tag: 42035, Type: TypeASCII, Count: 0, Export: ExportInjectOnly, NS: nsExifEX, Name: "LensMake"},                // LensMake
	{Tag: 42036, Type: TypeASCII, Count: 0, Export: ExportInjectOnly, NS: nsExifEX, Name: "LensModel"},               // LensModel
	{Tag: 42037, Type: TypeASCII, Count: 0, Export: ExportInjectOnly, NS: nsExifEX, Name: "LensSerialNumber"},        // LensSerialNumber
	{Tag: 33434, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "ExposureTime"},           // ExposureTime
	{Tag: 33437, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "FNumber"},                // FNumber
	{Tag: 34850, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "ExposureProgram"},           // ExposureProgram
	{Tag: 34852, Type: TypeASCII, Count: 0, Export: ExportInjectOnly, NS: nsEXIF, Name: "SpectralSensitivity"},       // SpectralSensitivity
	{Tag: 34855, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: "", Name: ""},                              // PhotographicSensitivity
	{Tag: 34856, Type: TypeUndefined, Count: 0, Export: ExportNever, NS: "", Name: ""},                               // OECF
	{Tag: 34864, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: "", Name: ""},                              // SensitivityType
	{Tag: 34865, Type: TypeLong, Count: 1, Export: ExportInjectOnly, NS: "", Name: ""},                               // StandardOutputSensitivity
	{Tag: 34866, Type: TypeLong, Count: 1, Export: ExportInjectOnly, NS: "", Name: ""},                               // RecommendedExposureIndex
	{Tag: 34867, Type: TypeLong, Count: 1, Export: ExportInjectOnly, NS: "", Name: ""},                               // ISOSpeed
	{Tag: 34868, Type: TypeLong, Count: 1, Export: ExportInjectOnly, NS: "", Name: ""},                               // ISOSpeedLatitudeyyy
	{Tag: 34869, Type: TypeLong, Count: 1, Export: ExportInjectOnly, NS: "", Name: ""},                               // ISOSpeedLatitudezzz
	{Tag: 37377, Type: TypeSRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "ShutterSpeedValue"},     // ShutterSpeedValue
	{Tag: 37378, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "ApertureValue"},          // ApertureValue
	{Tag: 37379, Type: TypeSRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "BrightnessValue"},       // BrightnessValue
	{Tag: 37380, Type: TypeSRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "ExposureBiasValue"},     // ExposureBiasValue
	{Tag: 37381, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "MaxApertureValue"},       // MaxApertureValue
	{Tag: 37382, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "SubjectDistance"},        // SubjectDistance
	{Tag: 37383, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "MeteringMode"},              // MeteringMode
	{Tag: 37384, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "LightSource"},               // LightSource
	{Tag: 37385, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: "", Name: ""},                              // Flash
	{Tag: 37386, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "FocalLength"},            // FocalLength
	{Tag: 37396, Type: TypeShort, Count: 0, Export: ExportNever, NS: nsEXIF, Name: "SubjectArea"},                    // SubjectArea
	{Tag: 41483, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "FlashEnergy"},            // FlashEnergy
	{Tag: 41484, Type: TypeUndefined, Count: 0, Export: ExportInjectOnly, NS: "", Name: ""},                          // SpatialFrequencyResponse
	{Tag: 41486, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "FocalPlaneXResolution"},  // FocalPlaneXResolution
	{Tag: 41487, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "FocalPlaneYResolution"},  // FocalPlaneYResolution
	{Tag: 41488, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "FocalPlaneResolutionUnit"},  // FocalPlaneResolutionUnit
	{Tag: 41492, Type: TypeShort, Count: 2, Export: ExportNever, NS: nsEXIF, Name: "SubjectLocation"},                // SubjectLocation
	{Tag: 41493, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "ExposureIndex"},          // ExposureIndex
	{Tag: 41495, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "SensingMethod"},             // SensingMethod
	{Tag: 41728, Type: TypeUndefined, Count: 1, Export: ExportInjectOnly, NS: "", Name: ""},                          // FileSource
	{Tag: 41729, Type: TypeUndefined, Count: 1, Export: ExportInjectOnly, NS: "", Name: ""},                          // SceneType
	{Tag: 41730, Type: TypeUndefined, Count: 0, Export: ExportInjectOnly, NS: "", Name: ""},                          // CFAPattern
	{Tag: 41985, Type: TypeShort, Count: 1, Export: ExportNever, NS: nsEXIF, Name: "CustomRendered"},                 // CustomRendered
	{Tag: 41986, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "ExposureMode"},              // ExposureMode
	{Tag: 41987, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "WhiteBalance"},              // WhiteBalance
	{Tag: 41988, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "DigitalZoomRatio"},       // DigitalZoomRatio
	{Tag: 41989, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "FocalLengthIn35mmFilm"},     // FocalLengthIn35mmFilm
	{Tag: 41990, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "SceneCaptureType"},          // SceneCaptureType
	{Tag: 41991, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "GainControl"},               // GainControl
	{Tag: 41992, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "Contrast"},                  // Contrast
	{Tag: 41993, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "Saturation"},                // Saturation
	{Tag: 41994, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "Sharpness"},                 // Sharpness
	{Tag: 41995, Type: TypeUndefined, Count: 0, Export: ExportInjectOnly, NS: "", Name: ""},                          // DeviceSettingDescription
	{Tag: 41996, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "SubjectDistanceRange"},      // SubjectDistanceRange
}

// gpsIFDMappings lists the GPS Info IFD tags that the XMP Toolkit maps to XMP
// (ReconcileTIFF.cpp:sGPSInfoIFDMappings).
var gpsIFDMappings = [...]TIFFMapping{ //nolint:gochecknoglobals // read-only table transcribed from the XMP Toolkit; statically initialised, never mutated
	{Tag: 0, Type: TypeByte, Count: 4, Export: ExportInjectOnly, NS: "", Name: ""},                                // GPSVersionID
	{Tag: 2, Type: TypeRational, Count: 3, Export: ExportAlways, NS: "", Name: ""},                                // GPSLatitude
	{Tag: 4, Type: TypeRational, Count: 3, Export: ExportAlways, NS: "", Name: ""},                                // GPSLongitude
	{Tag: 5, Type: TypeByte, Count: 1, Export: ExportAlways, NS: nsEXIF, Name: "GPSAltitudeRef"},                  // GPSAltitudeRef
	{Tag: 6, Type: TypeRational, Count: 1, Export: ExportAlways, NS: nsEXIF, Name: "GPSAltitude"},                 // GPSAltitude
	{Tag: 7, Type: TypeRational, Count: 3, Export: ExportAlways, NS: "", Name: ""},                                // GPSTimeStamp
	{Tag: 8, Type: TypeASCII, Count: 0, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSSatellites"},              // GPSSatellites
	{Tag: 9, Type: TypeASCII, Count: 2, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSStatus"},                  // GPSStatus
	{Tag: 10, Type: TypeASCII, Count: 2, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSMeasureMode"},            // GPSMeasureMode
	{Tag: 11, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSDOP"},                 // GPSDOP
	{Tag: 12, Type: TypeASCII, Count: 2, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSSpeedRef"},               // GPSSpeedRef
	{Tag: 13, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSSpeed"},               // GPSSpeed
	{Tag: 14, Type: TypeASCII, Count: 2, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSTrackRef"},               // GPSTrackRef
	{Tag: 15, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSTrack"},               // GPSTrack
	{Tag: 16, Type: TypeASCII, Count: 2, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSImgDirectionRef"},        // GPSImgDirectionRef
	{Tag: 17, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSImgDirection"},        // GPSImgDirection
	{Tag: 18, Type: TypeASCII, Count: 0, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSMapDatum"},               // GPSMapDatum
	{Tag: 20, Type: TypeRational, Count: 3, Export: ExportInjectOnly, NS: "", Name: ""},                           // GPSDestLatitude
	{Tag: 22, Type: TypeRational, Count: 3, Export: ExportInjectOnly, NS: "", Name: ""},                           // GPSDestLongitude
	{Tag: 23, Type: TypeASCII, Count: 2, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSDestBearingRef"},         // GPSDestBearingRef
	{Tag: 24, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSDestBearing"},         // GPSDestBearing
	{Tag: 25, Type: TypeASCII, Count: 2, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSDestDistanceRef"},        // GPSDestDistanceRef
	{Tag: 26, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSDestDistance"},        // GPSDestDistance
	{Tag: 27, Type: TypeUndefined, Count: 0, Export: ExportInjectOnly, NS: "", Name: ""},                          // GPSProcessingMethod
	{Tag: 28, Type: TypeUndefined, Count: 0, Export: ExportInjectOnly, NS: "", Name: ""},                          // GPSAreaInformation
	{Tag: 30, Type: TypeShort, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "GPSDifferential"},           // GPSDifferential
	{Tag: 31, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsExifEX, Name: "GPSHPositioningError"}, // GPSHPositioningError
}

// iimDataSets lists the IIM record 2 DataSets known to the XMP Toolkit, with
// their XMP mapping and their maximum length in bytes
// (IPTC_Support.cpp:kKnownDataSets). MaxLen is the IIM 4.2 octet limit of
// the DataSet.
var iimDataSets = [...]IIMMapping{ //nolint:gochecknoglobals // read-only table transcribed from the XMP Toolkit; statically initialised, never mutated
	{DataSet: 3, Form: IIMUnmappedText, MaxLen: 67, NS: "", Name: ""},                           // ObjectType
	{DataSet: 4, Form: IIMSpecial, MaxLen: 68, NS: nsIPTCCore, Name: "IntellectualGenre"},       // IntellectualGenre
	{DataSet: 5, Form: IIMLangAlt, MaxLen: 64, NS: nsDC, Name: "title"},                         // Title
	{DataSet: 7, Form: IIMUnmappedText, MaxLen: 64, NS: "", Name: ""},                           // EditStatus
	{DataSet: 8, Form: IIMUnmappedText, MaxLen: 2, NS: "", Name: ""},                            // EditorialUpdate
	{DataSet: 10, Form: IIMSimple, MaxLen: 1, NS: nsPhotoshop, Name: "Urgency"},                 // Urgency
	{DataSet: 12, Form: IIMSpecial, MaxLen: 236, NS: nsIPTCCore, Name: "SubjectCode"},           // SubjectCode
	{DataSet: 15, Form: IIMSimple, MaxLen: 3, NS: nsPhotoshop, Name: "Category"},                // Category
	{DataSet: 20, Form: IIMArray, MaxLen: 32, NS: nsPhotoshop, Name: "SupplementalCategories"},  // SuppCategory
	{DataSet: 22, Form: IIMUnmappedText, MaxLen: 32, NS: "", Name: ""},                          // FixtureIdentifier
	{DataSet: 25, Form: IIMArray, MaxLen: 64, NS: nsDC, Name: "subject"},                        // Keyword
	{DataSet: 26, Form: IIMUnmappedText, MaxLen: 3, NS: "", Name: ""},                           // ContentLocCode
	{DataSet: 27, Form: IIMUnmappedText, MaxLen: 64, NS: "", Name: ""},                          // ContentLocName
	{DataSet: 30, Form: IIMUnmappedText, MaxLen: 8, NS: "", Name: ""},                           // ReleaseDate
	{DataSet: 35, Form: IIMUnmappedText, MaxLen: 11, NS: "", Name: ""},                          // ReleaseTime
	{DataSet: 37, Form: IIMUnmappedText, MaxLen: 8, NS: "", Name: ""},                           // ExpDate
	{DataSet: 38, Form: IIMUnmappedText, MaxLen: 11, NS: "", Name: ""},                          // ExpTime
	{DataSet: 40, Form: IIMSimple, MaxLen: 256, NS: nsPhotoshop, Name: "Instructions"},          // Instructions
	{DataSet: 42, Form: IIMUnmappedText, MaxLen: 2, NS: "", Name: ""},                           // ActionAdvised
	{DataSet: 45, Form: IIMUnmappedText, MaxLen: 10, NS: "", Name: ""},                          // RefService
	{DataSet: 47, Form: IIMUnmappedText, MaxLen: 8, NS: "", Name: ""},                           // RefDate
	{DataSet: 50, Form: IIMUnmappedText, MaxLen: 8, NS: "", Name: ""},                           // RefNumber
	{DataSet: 55, Form: IIMSpecial, MaxLen: 8, NS: nsPhotoshop, Name: "DateCreated"},            // DateCreated
	{DataSet: 60, Form: IIMUnmappedText, MaxLen: 11, NS: "", Name: ""},                          // TimeCreated
	{DataSet: 62, Form: IIM3Way, MaxLen: 8, NS: "", Name: ""},                                   // DigitalCreateDate
	{DataSet: 63, Form: IIMUnmappedText, MaxLen: 11, NS: "", Name: ""},                          // DigitalCreateTime
	{DataSet: 65, Form: IIMUnmappedText, MaxLen: 32, NS: "", Name: ""},                          // OriginProgram
	{DataSet: 70, Form: IIMUnmappedText, MaxLen: 10, NS: "", Name: ""},                          // ProgramVersion
	{DataSet: 75, Form: IIMUnmappedText, MaxLen: 1, NS: "", Name: ""},                           // ObjectCycle
	{DataSet: 80, Form: IIM3Way, MaxLen: 32, NS: "", Name: ""},                                  // Creator
	{DataSet: 85, Form: IIMSimple, MaxLen: 32, NS: nsPhotoshop, Name: "AuthorsPosition"},        // CreatorJobtitle
	{DataSet: 90, Form: IIMSimple, MaxLen: 32, NS: nsPhotoshop, Name: "City"},                   // City
	{DataSet: 92, Form: IIMSimple, MaxLen: 32, NS: nsIPTCCore, Name: "Location"},                // Location
	{DataSet: 95, Form: IIMSimple, MaxLen: 32, NS: nsPhotoshop, Name: "State"},                  // State
	{DataSet: 100, Form: IIMSimple, MaxLen: 3, NS: nsIPTCCore, Name: "CountryCode"},             // CountryCode
	{DataSet: 101, Form: IIMSimple, MaxLen: 64, NS: nsPhotoshop, Name: "Country"},               // Country
	{DataSet: 103, Form: IIMSimple, MaxLen: 32, NS: nsPhotoshop, Name: "TransmissionReference"}, // JobID
	{DataSet: 105, Form: IIMSimple, MaxLen: 256, NS: nsPhotoshop, Name: "Headline"},             // Headline
	{DataSet: 110, Form: IIMSimple, MaxLen: 32, NS: nsPhotoshop, Name: "Credit"},                // Provider
	{DataSet: 115, Form: IIMSimple, MaxLen: 32, NS: nsPhotoshop, Name: "Source"},                // Source
	{DataSet: 116, Form: IIM3Way, MaxLen: 128, NS: "", Name: ""},                                // CopyrightNotice
	{DataSet: 118, Form: IIMUnmappedText, MaxLen: 128, NS: "", Name: ""},                        // Contact
	{DataSet: 120, Form: IIM3Way, MaxLen: 2000, NS: "", Name: ""},                               // Description
	{DataSet: 122, Form: IIMSimple, MaxLen: 32, NS: nsPhotoshop, Name: "CaptionWriter"},         // DescriptionWriter
	{DataSet: 125, Form: IIMUnmappedBin, MaxLen: 7360, NS: "", Name: ""},                        // RasterizedCaption
	{DataSet: 130, Form: IIMUnmappedText, MaxLen: 2, NS: "", Name: ""},                          // ImageType
	{DataSet: 131, Form: IIMUnmappedText, MaxLen: 1, NS: "", Name: ""},                          // ImageOrientation
	{DataSet: 135, Form: IIMUnmappedText, MaxLen: 3, NS: "", Name: ""},                          // LanguageID
	{DataSet: 150, Form: IIMUnmappedText, MaxLen: 2, NS: "", Name: ""},                          // AudioType
	{DataSet: 151, Form: IIMUnmappedText, MaxLen: 6, NS: "", Name: ""},                          // AudioSampleRate
	{DataSet: 152, Form: IIMUnmappedText, MaxLen: 2, NS: "", Name: ""},                          // AudioSampleRes
	{DataSet: 153, Form: IIMUnmappedText, MaxLen: 6, NS: "", Name: ""},                          // AudioDuration
	{DataSet: 154, Form: IIMUnmappedText, MaxLen: 64, NS: "", Name: ""},                         // AudioOutcue
	{DataSet: 200, Form: IIMUnmappedBin, MaxLen: 2, NS: "", Name: ""},                           // PreviewFormat
	{DataSet: 201, Form: IIMUnmappedBin, MaxLen: 2, NS: "", Name: ""},                           // PreviewFormatVers
	{DataSet: 202, Form: IIMUnmappedBin, MaxLen: 256000, NS: "", Name: ""},                      // PreviewData
}
