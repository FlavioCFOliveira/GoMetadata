package exif

// GPS IFD tag IDs (EXIF §4.6.6 Table 15, GPS Attribute Information).
const (
	TagGPSVersionID          TagID = 0x0000 // Byte 4        — GPS version
	TagGPSLatitudeRef        TagID = 0x0001 // ASCII 2       — "N" or "S"
	TagGPSLatitude           TagID = 0x0002 // Rational 3    — degrees/minutes/seconds
	TagGPSLongitudeRef       TagID = 0x0003 // ASCII 2       — "E" or "W"
	TagGPSLongitude          TagID = 0x0004 // Rational 3
	TagGPSAltitudeRef        TagID = 0x0005 // Byte 1        — 0=above, 1=below sea
	TagGPSAltitude           TagID = 0x0006 // Rational 1    — metres
	TagGPSTimeStamp          TagID = 0x0007 // Rational 3    — UTC HH/MM/SS
	TagGPSSatellites         TagID = 0x0008 // ASCII 0       — satellites used
	TagGPSStatus             TagID = 0x0009 // ASCII 2       — "A"=measurement, "V"=interoperability
	TagGPSMeasureMode        TagID = 0x000A // ASCII 2       — "2" or "3" dimensional
	TagGPSDOP                TagID = 0x000B // Rational 1    — dilution of precision
	TagGPSSpeedRef           TagID = 0x000C // ASCII 2       — "K","M","N"
	TagGPSSpeed              TagID = 0x000D // Rational 1    — speed of GPS receiver
	TagGPSTrackRef           TagID = 0x000E // ASCII 2       — "T"=true, "M"=magnetic
	TagGPSTrack              TagID = 0x000F // Rational 1    — direction of movement
	TagGPSImgDirectionRef    TagID = 0x0010 // ASCII 2       — "T" or "M"
	TagGPSImgDirection       TagID = 0x0011 // Rational 1    — direction of image
	TagGPSMapDatum           TagID = 0x0012 // ASCII 0       — geodetic survey data
	TagGPSDestLatitudeRef    TagID = 0x0013 // ASCII 2       — "N" or "S"
	TagGPSDestLatitude       TagID = 0x0014 // Rational 3
	TagGPSDestLongitudeRef   TagID = 0x0015 // ASCII 2       — "E" or "W"
	TagGPSDestLongitude      TagID = 0x0016 // Rational 3
	TagGPSDestBearingRef     TagID = 0x0017 // ASCII 2       — "T" or "M"
	TagGPSDestBearing        TagID = 0x0018 // Rational 1
	TagGPSDestDistanceRef    TagID = 0x0019 // ASCII 2       — "K","M","N"
	TagGPSDestDistance       TagID = 0x001A // Rational 1
	TagGPSProcessingMethod   TagID = 0x001B // Undefined 0   — processing method name
	TagGPSAreaInformation    TagID = 0x001C // Undefined 0   — area name
	TagGPSDateStamp          TagID = 0x001D // ASCII 11      — UTC date (YYYY:MM:DD\0)
	TagGPSDifferential       TagID = 0x001E // Short 1       — 0=no correction, 1=differential
	TagGPSHPositioningError  TagID = 0x001F // Rational 1    — horizontal positioning error (EXIF 2.31+)
)

// parseGPS extracts decimal-degree coordinates from a GPS IFD.
// DMS (degrees/minutes/seconds) values are converted per the EXIF spec
// (EXIF §4.6.6, GPS tags 0x0002/0x0004).
func parseGPS(ifd *IFD) (lat, lon float64, ok bool) {
	latEntry := ifd.Get(TagGPSLatitude)
	latRefEntry := ifd.Get(TagGPSLatitudeRef)
	lonEntry := ifd.Get(TagGPSLongitude)
	lonRefEntry := ifd.Get(TagGPSLongitudeRef)

	if latEntry == nil || latRefEntry == nil || lonEntry == nil || lonRefEntry == nil {
		return 0, 0, false
	}

	// Each coordinate is 3 RATIONAL values: degrees, minutes, seconds (EXIF §4.6.6).
	if latEntry.Count != 3 || lonEntry.Count != 3 {
		return 0, 0, false
	}

	var latDMS [3][2]uint32
	for i := 0; i < 3; i++ {
		latDMS[i] = latEntry.Rational(i)
	}

	var lonDMS [3][2]uint32
	for i := 0; i < 3; i++ {
		lonDMS[i] = lonEntry.Rational(i)
	}

	latRef := latRefEntry.String()
	lonRef := lonRefEntry.String()

	lat = dmsToDecimal(latDMS, latRef)
	lon = dmsToDecimal(lonDMS, lonRef)

	// Validate WGS-84 coordinate ranges (EXIF §4.6.6).
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, false
	}

	return lat, lon, true
}

// dmsToDecimal converts a RATIONAL triplet [degrees, minutes, seconds] to
// a signed decimal-degree value. ref must be "N", "S", "E", or "W".
func dmsToDecimal(dms [3][2]uint32, ref string) float64 {
	// Guard against division by zero in any denominator.
	if dms[0][1] == 0 || dms[1][1] == 0 || dms[2][1] == 0 {
		return 0
	}

	deg := float64(dms[0][0]) / float64(dms[0][1])
	min := float64(dms[1][0]) / float64(dms[1][1])
	sec := float64(dms[2][0]) / float64(dms[2][1])

	decimal := deg + min/60 + sec/3600

	if ref == "S" || ref == "W" {
		decimal = -decimal
	}

	return decimal
}
