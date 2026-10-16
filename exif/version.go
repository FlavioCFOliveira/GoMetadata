package exif

// version.go — the ExifVersion rule of the setters: a setter that writes a tag
// or a tag type which a given version of the standard introduced raises the
// ExifVersion of the file to that version, and never lowers a higher one.
//
// Exif 3.1 (CIPA DC-008-2026) §4.2, p. 14: the version is recorded as four
// bytes A1 A2 B1 B2; "when the changes are such that data readers can properly
// recognize conventional information and skip the data that was recorded using
// newly added functions, the lower part of the version (B1, B2) is changed";
// "when there is a possibility that a data reader implementing the old
// standard may operate incorrectly due to the new specifications, the upper
// part of the version (A1, A2) is changed". A file whose ExifVersion is lower
// than the version that defines a tag it holds therefore claims a standard
// that does not contain the tag.
//
// Source of the table: the revision history of Exif 3.1, pp. 3-5 (the same
// history is in Exif 3.0), and the tag tables of the same document, Table 9
// (Exif IFD tags), pp. 44-47, and Table 18, pp. 94-96 (tag numbers and names),
// checked against Exif 2.3 (CIPA DC-008-2012), whose Revision History, pp. 3-4,
// has the 2.3 entry and whose Exif IFD tag table lists 0x8830-0x8835 and
// 0xA430-0xA435 and none of the tags of the later versions:
//
//	0230  Exif 2.3 (April 2010): "Added and revised tags (Sensitivity-related
//	      tags, GPS information, camera and lens information, items pertaining
//	      to sound files, and light-source color)". The sensitivity tags
//	      (0x8830-0x8835) and the camera and lens tags (0xA430-0xA435) are in
//	      Table 18 of Exif 2.3 and carry the same names and numbers there.
//	0231  Exif 2.31 (July 2016): the three time offset tags (OffsetTime,
//	      OffsetTimeOriginal, OffsetTimeDigitized: 0x9010-0x9012) and the tags
//	      "relating to shooting situation" (Temperature, Humidity, Pressure,
//	      WaterDepth, Acceleration, CameraElevationAngle: 0x9400-0x9405).
//	0232  Exif 2.32 (May 2019): "Added 3 tags relating to a composite image"
//	      (CompositeImage, SourceImageNumberOfCompositeImage,
//	      SourceExposureTimesOfCompositeImage: 0xA460-0xA462).
//	0300  Exif 3.0 (May 2023): the type UTF-8 (129) and the tags Title
//	      (ImageTitle), Photographer, ImageEditor, CameraFirmware,
//	      RAWDevelopingSoftware, ImageEditingSoftware and
//	      MetadataEditingSoftware (0xA436-0xA43C). Exif 3.1 (January 2026) adds
//	      LearningOptOutIn (0x9287), DevelopmentType, DevelopmentTypeDescription,
//	      DistortionCorrection, ChromaticAberrationCorrection, ShadingCorrection
//	      and NoiseReduction (0xA40D-0xA412) and keeps the version "0300"
//	      (§4.6.6.1.1, p. 47: the ExifVersion of this standard is "0300").
//
// Tags that are older, or whose version of introduction the local texts do not
// state (Exif 2.2 and 2.21 are not available locally: for example Gamma,
// 0xA500), are not gated: an unlisted tag never raises the version, so the
// table can only err towards leaving a version as the file had it. The GPS tags
// added by Exif 2.3 ("GPS information") are not gated either, because the
// revision history does not name them.
//
// All the tag numbers above are tags of the Exif IFD (Table 18).

// Exif versions that gate tags, as the four ASCII digits that ExifVersion holds.
const (
	exifVersion230 = "0230"
	exifVersion231 = "0231"
	exifVersion232 = "0232"
)

// minExifVersion returns the lowest ExifVersion that defines tag of the Exif
// IFD, or "" when the tag is not gated (see the table at the top of the file).
//
//nolint:cyclop,gocyclo // one flat table of tag ranges, one case per version
func minExifVersion(tag TagID) string {
	switch {
	case tag >= 0x8830 && tag <= 0x8835, tag >= 0xA430 && tag <= 0xA435:
		return exifVersion230
	case tag >= 0x9010 && tag <= 0x9012, tag >= 0x9400 && tag <= 0x9405:
		return exifVersion231
	case tag >= 0xA460 && tag <= 0xA462:
		return exifVersion232
	case tag == 0x9287, tag >= 0xA40D && tag <= 0xA412, tag >= 0xA436 && tag <= 0xA43C:
		return exifVersionDefault
	}
	return ""
}

// requireExifTag makes the ExifVersion at least the version that defines tag
// of the Exif IFD (minExifVersion); it does nothing for a tag that is not
// gated. Every setter that writes an Exif IFD tag calls it.
func (e *EXIF) requireExifTag(tag TagID) {
	if lowest := minExifVersion(tag); lowest != "" {
		e.requireExifVersion(lowest)
	}
}

// requireExifVersion makes the ExifVersion at least lowest (four ASCII digits),
// creating the Exif IFD when it is absent (Exif 3.1 §4.2, p. 14, and
// §4.6.6.1.1, p. 47). An ExifVersion that is already lowest or higher is left as
// it is: a version is never lowered. A lower one, or one that is not four ASCII
// digits and so cannot be compared, is replaced by lowest.
func (e *EXIF) requireExifVersion(lowest string) {
	e.ensureExifIFD()
	if v := e.ExifIFD.Get(TagExifVersion); v != nil && !versionBefore(v.Value, lowest) {
		return
	}
	e.ExifIFD.set(TagExifVersion, TypeUndefined, 4, []byte(lowest), e.ifd0BigEndian())
}

// versionBefore reports whether v is a four-digit ExifVersion lower than lowest,
// or is not four ASCII digits at all.
func versionBefore(v []byte, lowest string) bool {
	if len(v) != 4 {
		return true
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return true
		}
	}
	return string(v) < lowest
}
