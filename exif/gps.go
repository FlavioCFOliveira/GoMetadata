package exif

// GPS IFD tag IDs (EXIF §4.6.6, GPS Attribute Information).
const (
	TagGPSVersionID    TagID = 0x0000
	TagGPSLatitudeRef  TagID = 0x0001
	TagGPSLatitude     TagID = 0x0002
	TagGPSLongitudeRef TagID = 0x0003
	TagGPSLongitude    TagID = 0x0004
	TagGPSAltitudeRef  TagID = 0x0005
	TagGPSAltitude     TagID = 0x0006
	TagGPSTimeStamp    TagID = 0x0007
	TagGPSDateStamp    TagID = 0x001D
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
