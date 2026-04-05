package exif

// TagID is a TIFF/EXIF tag number (EXIF §4.6, TIFF §8).
type TagID uint16

// Standard IFD0 / TIFF baseline tags (TIFF §8 and EXIF §4.6.4 Table 3).
const (
	TagImageWidth        TagID = 0x0100
	TagImageLength       TagID = 0x0101
	TagBitsPerSample     TagID = 0x0102
	TagCompression       TagID = 0x0103
	TagPhotometricInterp TagID = 0x0106
	TagImageDescription  TagID = 0x010E
	TagMake              TagID = 0x010F
	TagModel             TagID = 0x0110
	TagOrientation       TagID = 0x0112
	TagXResolution       TagID = 0x011A
	TagYResolution       TagID = 0x011B
	TagResolutionUnit    TagID = 0x0128
	TagSoftware          TagID = 0x0131
	TagDateTime          TagID = 0x0132
	TagArtist            TagID = 0x013B
	TagWhitePoint        TagID = 0x013E
	TagCopyright         TagID = 0x8298

	// TIFF 6.0 baseline tags not in EXIF §4.6.4 but present in real files (TIFF 6.0 §8).
	TagNewSubfileType              TagID = 0x00FE
	TagSubfileType                 TagID = 0x00FF
	TagThresholding                TagID = 0x0107
	TagFillOrder                   TagID = 0x010A
	TagDocumentName                TagID = 0x010D
	TagStripOffsets                TagID = 0x0111
	TagSamplesPerPixel             TagID = 0x0115
	TagRowsPerStrip                TagID = 0x0116
	TagStripByteCounts             TagID = 0x0117
	TagMinSampleValue              TagID = 0x0118
	TagMaxSampleValue              TagID = 0x0119
	TagPlanarConfiguration         TagID = 0x011C
	TagPageName                    TagID = 0x011D
	TagXPosition                   TagID = 0x011E
	TagYPosition                   TagID = 0x011F
	TagGrayResponseUnit            TagID = 0x0122
	TagGrayResponseCurve           TagID = 0x0123
	TagPageNumber                  TagID = 0x0129
	TagTransferFunction            TagID = 0x012D
	TagHostComputer                TagID = 0x013C
	TagPredictor                   TagID = 0x013D
	TagPrimaryChromaticities       TagID = 0x013F
	TagColorMap                    TagID = 0x0140
	TagHalftoneHints               TagID = 0x0141
	TagTileWidth                   TagID = 0x0142
	TagTileLength                  TagID = 0x0143
	TagTileOffsets                 TagID = 0x0144
	TagTileByteCounts              TagID = 0x0145
	TagSubIFDs                     TagID = 0x014A // TIFF-EP / DNG extension
	TagExtraSamples                TagID = 0x0152
	TagSampleFormat                TagID = 0x0153
	TagJPEGInterchangeFormat       TagID = 0x0201 // Thumbnail offset in IFD1
	TagJPEGInterchangeFormatLength TagID = 0x0202
	TagYCbCrCoefficients           TagID = 0x0211
	TagYCbCrSubSampling            TagID = 0x0212
	TagYCbCrPositioning            TagID = 0x0213
	TagReferenceBlackWhite         TagID = 0x0214

	// Pointer tags (EXIF §4.6.3).
	TagExifIFDPointer    TagID = 0x8769
	TagGPSIFDPointer     TagID = 0x8825
	TagInteropIFDPointer TagID = 0xA005

	// IPTC and XMP embedded in TIFF (not EXIF-defined, TIFF extension).
	TagIPTC TagID = 0x83BB
	TagXMP  TagID = 0x02BC

	// EXIF IFD tags (EXIF §4.6.5 Table 4).
	TagMakerNote TagID = 0x927C

	// Interoperability IFD tags (EXIF §4.6.7, Annex A).
	TagInteroperabilityIndex   TagID = 0x0001
	TagInteroperabilityVersion TagID = 0x0002
)

// tagInfo holds registry metadata for a tag ID.
type tagInfo struct {
	Name  string
	Type  DataType
	Count uint32 // 0 means variable
}

// Standard EXIF IFD tags (EXIF §4.6.5 Table 4).
const (
	TagExposureTime              TagID = 0x829A // Rational (seconds)
	TagFNumber                   TagID = 0x829D // Rational (f/N)
	TagExposureProgram           TagID = 0x8822 // Short
	TagISOSpeedRatings           TagID = 0x8827 // Short (ISO 12232)
	TagSensitivityType           TagID = 0x8830 // Short (EXIF 2.3+)
	TagRecommendedExposureIndex  TagID = 0x8832 // Long  (EXIF 2.3+)
	TagDateTimeOriginal          TagID = 0x9003 // ASCII 20 ("YYYY:MM:DD HH:MM:SS\0")
	TagDateTimeDigitized         TagID = 0x9004 // ASCII 20
	TagOffsetTime                TagID = 0x9010 // ASCII (EXIF 2.31+)
	TagOffsetTimeOriginal        TagID = 0x9011 // ASCII (EXIF 2.31+)
	TagOffsetTimeDigitized       TagID = 0x9012 // ASCII (EXIF 2.31+)
	TagShutterSpeedValue         TagID = 0x9201 // SRational (APEX)
	TagApertureValue             TagID = 0x9202 // Rational  (APEX)
	TagBrightnessValue           TagID = 0x9203 // SRational (APEX)
	TagExposureBiasValue         TagID = 0x9204 // SRational (APEX)
	TagMaxApertureValue          TagID = 0x9205 // Rational  (APEX)
	TagSubjectDistance           TagID = 0x9206 // Rational  (metres)
	TagMeteringMode              TagID = 0x9207 // Short
	TagLightSource               TagID = 0x9208 // Short
	TagFlash                     TagID = 0x9209 // Short
	TagFocalLength               TagID = 0x920A // Rational  (mm)
	TagSubSecTime                TagID = 0x9290 // ASCII
	TagSubSecTimeOriginal        TagID = 0x9291 // ASCII
	TagSubSecTimeDigitized       TagID = 0x9292 // ASCII
	TagFlashpixVersion           TagID = 0xA000 // Undefined 4
	TagColorSpace                TagID = 0xA001 // Short
	TagPixelXDimension           TagID = 0xA002 // Short or Long
	TagPixelYDimension           TagID = 0xA003 // Short or Long
	TagRelatedSoundFile          TagID = 0xA004 // ASCII 13
	TagFlashEnergy               TagID = 0xA20B // Rational
	TagFocalPlaneXResolution     TagID = 0xA20E // Rational
	TagFocalPlaneYResolution     TagID = 0xA20F // Rational
	TagFocalPlaneResolutionUnit  TagID = 0xA210 // Short
	TagExposureIndex             TagID = 0xA215 // Rational
	TagSensingMethod             TagID = 0xA217 // Short
	TagFileSource                TagID = 0xA300 // Undefined 1
	TagSceneType                 TagID = 0xA301 // Undefined 1
	TagCustomRendered            TagID = 0xA401 // Short
	TagExposureMode              TagID = 0xA402 // Short
	TagWhiteBalance              TagID = 0xA403 // Short
	TagDigitalZoomRatio          TagID = 0xA404 // Rational
	TagFocalLengthIn35mmFilm     TagID = 0xA405 // Short
	TagSceneCaptureType          TagID = 0xA406 // Short
	TagGainControl               TagID = 0xA407 // Rational
	TagContrast                  TagID = 0xA408 // Short
	TagSaturation                TagID = 0xA409 // Short
	TagSharpness                 TagID = 0xA40A // Short
	TagSubjectDistanceRange      TagID = 0xA40C // Short
	TagImageUniqueID             TagID = 0xA420 // ASCII 33
	TagCameraOwnerName           TagID = 0xA430 // ASCII (EXIF 2.3+)
	TagBodySerialNumber          TagID = 0xA431 // ASCII (EXIF 2.3+)
	TagLensSpecification         TagID = 0xA432 // Rational 4
	TagLensMake                  TagID = 0xA433 // ASCII (EXIF 2.3+)
	TagLensModel                 TagID = 0xA434 // ASCII (EXIF 2.3+)
	TagLensSerialNumber          TagID = 0xA435 // ASCII (EXIF 2.3+)
	TagStandardOutputSensitivity TagID = 0x8801 // Long 1 (EXIF 2.3+ §4.6.5)
	TagDeviceSettingDescription  TagID = 0xA40B // Undefined (EXIF 2.x §4.6.5)
)

// tagRegistry maps TagID to tagInfo for all known tags.
// Populated at init time; treated as read-only thereafter.
var tagRegistry map[TagID]tagInfo

func init() {
	tagRegistry = make(map[TagID]tagInfo, 256)

	// IFD0 / TIFF baseline tags (TIFF 6.0 §8).
	for tag, info := range map[TagID]tagInfo{
		TagImageWidth:        {"ImageWidth", TypeShort, 1},
		TagImageLength:       {"ImageLength", TypeShort, 1},
		TagBitsPerSample:     {"BitsPerSample", TypeShort, 0},
		TagCompression:       {"Compression", TypeShort, 1},
		TagPhotometricInterp: {"PhotometricInterpretation", TypeShort, 1},
		TagImageDescription:  {"ImageDescription", TypeASCII, 0},
		TagMake:              {"Make", TypeASCII, 0},
		TagModel:             {"Model", TypeASCII, 0},
		TagOrientation:       {"Orientation", TypeShort, 1},
		TagXResolution:       {"XResolution", TypeRational, 1},
		TagYResolution:       {"YResolution", TypeRational, 1},
		TagResolutionUnit:    {"ResolutionUnit", TypeShort, 1},
		TagSoftware:          {"Software", TypeASCII, 0},
		TagDateTime:          {"DateTime", TypeASCII, 20},
		TagArtist:            {"Artist", TypeASCII, 0},
		TagWhitePoint:        {"WhitePoint", TypeRational, 2},
		TagCopyright:         {"Copyright", TypeASCII, 0},
		TagExifIFDPointer:    {"ExifIFDPointer", TypeLong, 1},
		TagGPSIFDPointer:     {"GPSIFDPointer", TypeLong, 1},
		TagInteropIFDPointer: {"InteroperabilityIFDPointer", TypeLong, 1},
		TagIPTC:              {"IPTC-NAA", TypeLong, 0},
		TagXMP:               {"XMP", TypeByte, 0},
		// TIFF 6.0 baseline tags (TIFF 6.0 §8, present in real-world TIFF/RAW files).
		TagNewSubfileType:              {"NewSubfileType", TypeLong, 1},
		TagSubfileType:                 {"SubfileType", TypeShort, 1},
		TagThresholding:                {"Thresholding", TypeShort, 1},
		TagFillOrder:                   {"FillOrder", TypeShort, 1},
		TagDocumentName:                {"DocumentName", TypeASCII, 0},
		TagStripOffsets:                {"StripOffsets", TypeLong, 0},
		TagSamplesPerPixel:             {"SamplesPerPixel", TypeShort, 1},
		TagRowsPerStrip:                {"RowsPerStrip", TypeLong, 1},
		TagStripByteCounts:             {"StripByteCounts", TypeLong, 0},
		TagMinSampleValue:              {"MinSampleValue", TypeShort, 0},
		TagMaxSampleValue:              {"MaxSampleValue", TypeShort, 0},
		TagPlanarConfiguration:         {"PlanarConfiguration", TypeShort, 1},
		TagPageName:                    {"PageName", TypeASCII, 0},
		TagXPosition:                   {"XPosition", TypeRational, 1},
		TagYPosition:                   {"YPosition", TypeRational, 1},
		TagGrayResponseUnit:            {"GrayResponseUnit", TypeShort, 1},
		TagGrayResponseCurve:           {"GrayResponseCurve", TypeShort, 0},
		TagPageNumber:                  {"PageNumber", TypeShort, 2},
		TagTransferFunction:            {"TransferFunction", TypeShort, 0},
		TagHostComputer:                {"HostComputer", TypeASCII, 0},
		TagPredictor:                   {"Predictor", TypeShort, 1},
		TagPrimaryChromaticities:       {"PrimaryChromaticities", TypeRational, 6},
		TagColorMap:                    {"ColorMap", TypeShort, 0},
		TagHalftoneHints:               {"HalftoneHints", TypeShort, 2},
		TagTileWidth:                   {"TileWidth", TypeLong, 1},
		TagTileLength:                  {"TileLength", TypeLong, 1},
		TagTileOffsets:                 {"TileOffsets", TypeLong, 0},
		TagTileByteCounts:              {"TileByteCounts", TypeLong, 0},
		TagSubIFDs:                     {"SubIFDs", TypeLong, 0},
		TagExtraSamples:                {"ExtraSamples", TypeShort, 0},
		TagSampleFormat:                {"SampleFormat", TypeShort, 0},
		TagJPEGInterchangeFormat:       {"JPEGInterchangeFormat", TypeLong, 1},
		TagJPEGInterchangeFormatLength: {"JPEGInterchangeFormatLength", TypeLong, 1},
		TagYCbCrCoefficients:           {"YCbCrCoefficients", TypeRational, 3},
		TagYCbCrSubSampling:            {"YCbCrSubSampling", TypeShort, 2},
		TagYCbCrPositioning:            {"YCbCrPositioning", TypeShort, 1},
		TagReferenceBlackWhite:         {"ReferenceBlackWhite", TypeRational, 6},
	} {
		tagRegistry[tag] = info
	}

	// InteropIFD tags (EXIF §4.6.7, Annex A).
	for tag, info := range map[TagID]tagInfo{
		TagInteroperabilityIndex:   {"InteroperabilityIndex", TypeASCII, 0},
		TagInteroperabilityVersion: {"InteroperabilityVersion", TypeUndefined, 4},
	} {
		tagRegistry[tag] = info
	}

	// EXIF IFD tags (EXIF §4.6.5 Table 4).
	for tag, info := range map[TagID]tagInfo{
		TagExposureTime:             {"ExposureTime", TypeRational, 1},
		TagFNumber:                  {"FNumber", TypeRational, 1},
		TagExposureProgram:          {"ExposureProgram", TypeShort, 1},
		TagISOSpeedRatings:          {"ISOSpeedRatings", TypeShort, 0},
		TagSensitivityType:          {"SensitivityType", TypeShort, 1},
		TagRecommendedExposureIndex: {"RecommendedExposureIndex", TypeLong, 1},
		TagDateTimeOriginal:         {"DateTimeOriginal", TypeASCII, 20},
		TagDateTimeDigitized:        {"DateTimeDigitized", TypeASCII, 20},
		TagOffsetTime:               {"OffsetTime", TypeASCII, 0},
		TagOffsetTimeOriginal:       {"OffsetTimeOriginal", TypeASCII, 0},
		TagOffsetTimeDigitized:      {"OffsetTimeDigitized", TypeASCII, 0},
		TagShutterSpeedValue:        {"ShutterSpeedValue", TypeSRational, 1},
		TagApertureValue:            {"ApertureValue", TypeRational, 1},
		TagBrightnessValue:          {"BrightnessValue", TypeSRational, 1},
		TagExposureBiasValue:        {"ExposureBiasValue", TypeSRational, 1},
		TagMaxApertureValue:         {"MaxApertureValue", TypeRational, 1},
		TagSubjectDistance:          {"SubjectDistance", TypeRational, 1},
		TagMeteringMode:             {"MeteringMode", TypeShort, 1},
		TagLightSource:              {"LightSource", TypeShort, 1},
		TagFlash:                    {"Flash", TypeShort, 1},
		TagFocalLength:              {"FocalLength", TypeRational, 1},
		TagSubSecTime:               {"SubSecTime", TypeASCII, 0},
		TagSubSecTimeOriginal:       {"SubSecTimeOriginal", TypeASCII, 0},
		TagSubSecTimeDigitized:      {"SubSecTimeDigitized", TypeASCII, 0},
		TagFlashpixVersion:          {"FlashpixVersion", TypeUndefined, 4},
		TagColorSpace:               {"ColorSpace", TypeShort, 1},
		TagPixelXDimension:          {"PixelXDimension", TypeLong, 1},
		TagPixelYDimension:          {"PixelYDimension", TypeLong, 1},
		TagRelatedSoundFile:         {"RelatedSoundFile", TypeASCII, 13},
		TagFocalPlaneXResolution:    {"FocalPlaneXResolution", TypeRational, 1},
		TagFocalPlaneYResolution:    {"FocalPlaneYResolution", TypeRational, 1},
		TagFocalPlaneResolutionUnit: {"FocalPlaneResolutionUnit", TypeShort, 1},
		TagExposureIndex:            {"ExposureIndex", TypeRational, 1},
		TagSensingMethod:            {"SensingMethod", TypeShort, 1},
		TagCustomRendered:           {"CustomRendered", TypeShort, 1},
		TagExposureMode:             {"ExposureMode", TypeShort, 1},
		TagWhiteBalance:             {"WhiteBalance", TypeShort, 1},
		TagDigitalZoomRatio:         {"DigitalZoomRatio", TypeRational, 1},
		TagFocalLengthIn35mmFilm:    {"FocalLengthIn35mmFilm", TypeShort, 1},
		TagSceneCaptureType:         {"SceneCaptureType", TypeShort, 1},
		TagGainControl:              {"GainControl", TypeRational, 1},
		TagContrast:                 {"Contrast", TypeShort, 1},
		TagSaturation:               {"Saturation", TypeShort, 1},
		TagSharpness:                {"Sharpness", TypeShort, 1},
		TagSubjectDistanceRange:     {"SubjectDistanceRange", TypeShort, 1},
		TagImageUniqueID:            {"ImageUniqueID", TypeASCII, 33},
		TagCameraOwnerName:          {"CameraOwnerName", TypeASCII, 0},
		TagBodySerialNumber:         {"BodySerialNumber", TypeASCII, 0},
		TagLensSpecification:        {"LensSpecification", TypeRational, 4},
		TagLensMake:                 {"LensMake", TypeASCII, 0},
		TagLensModel:                {"LensModel", TypeASCII, 0},
		TagLensSerialNumber:         {"LensSerialNumber", TypeASCII, 0},
		TagMakerNote:                {"MakerNote", TypeUndefined, 0},
	} {
		tagRegistry[tag] = info
	}

	// GPS IFD tags (EXIF §4.6.6 Table 15, all 32 entries).
	for tag, info := range map[TagID]tagInfo{
		TagGPSVersionID:         {"GPSVersionID", TypeByte, 4},
		TagGPSLatitudeRef:       {"GPSLatitudeRef", TypeASCII, 2},
		TagGPSLatitude:          {"GPSLatitude", TypeRational, 3},
		TagGPSLongitudeRef:      {"GPSLongitudeRef", TypeASCII, 2},
		TagGPSLongitude:         {"GPSLongitude", TypeRational, 3},
		TagGPSAltitudeRef:       {"GPSAltitudeRef", TypeByte, 1},
		TagGPSAltitude:          {"GPSAltitude", TypeRational, 1},
		TagGPSTimeStamp:         {"GPSTimeStamp", TypeRational, 3},
		TagGPSSatellites:        {"GPSSatellites", TypeASCII, 0},
		TagGPSStatus:            {"GPSStatus", TypeASCII, 2},
		TagGPSMeasureMode:       {"GPSMeasureMode", TypeASCII, 2},
		TagGPSDOP:               {"GPSDOP", TypeRational, 1},
		TagGPSSpeedRef:          {"GPSSpeedRef", TypeASCII, 2},
		TagGPSSpeed:             {"GPSSpeed", TypeRational, 1},
		TagGPSTrackRef:          {"GPSTrackRef", TypeASCII, 2},
		TagGPSTrack:             {"GPSTrack", TypeRational, 1},
		TagGPSImgDirectionRef:   {"GPSImgDirectionRef", TypeASCII, 2},
		TagGPSImgDirection:      {"GPSImgDirection", TypeRational, 1},
		TagGPSMapDatum:          {"GPSMapDatum", TypeASCII, 0},
		TagGPSDestLatitudeRef:   {"GPSDestLatitudeRef", TypeASCII, 2},
		TagGPSDestLatitude:      {"GPSDestLatitude", TypeRational, 3},
		TagGPSDestLongitudeRef:  {"GPSDestLongitudeRef", TypeASCII, 2},
		TagGPSDestLongitude:     {"GPSDestLongitude", TypeRational, 3},
		TagGPSDestBearingRef:    {"GPSDestBearingRef", TypeASCII, 2},
		TagGPSDestBearing:       {"GPSDestBearing", TypeRational, 1},
		TagGPSDestDistanceRef:   {"GPSDestDistanceRef", TypeASCII, 2},
		TagGPSDestDistance:      {"GPSDestDistance", TypeRational, 1},
		TagGPSProcessingMethod:  {"GPSProcessingMethod", TypeUndefined, 0},
		TagGPSAreaInformation:   {"GPSAreaInformation", TypeUndefined, 0},
		TagGPSDateStamp:         {"GPSDateStamp", TypeASCII, 11},
		TagGPSDifferential:      {"GPSDifferential", TypeShort, 1},
		TagGPSHPositioningError: {"GPSHPositioningError", TypeRational, 1},
	} {
		tagRegistry[tag] = info
	}

	// Additional EXIF 2.3+ tags not in the main EXIF IFD block above
	// (CIPA DC-008-2023 §4.6.5).
	for tag, info := range map[TagID]tagInfo{
		TagStandardOutputSensitivity: {"StandardOutputSensitivity", TypeLong, 1},
		TagDeviceSettingDescription:  {"DeviceSettingDescription", TypeUndefined, 0},
	} {
		tagRegistry[tag] = info
	}
}
