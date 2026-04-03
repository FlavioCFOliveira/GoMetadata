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

	// Pointer tags (EXIF §4.6.3).
	TagExifIFDPointer    TagID = 0x8769
	TagGPSIFDPointer     TagID = 0x8825
	TagInteropIFDPointer TagID = 0xA005

	// IPTC and XMP embedded in TIFF (not EXIF-defined, TIFF extension).
	TagIPTC TagID = 0x83BB
	TagXMP  TagID = 0x02BC

	// EXIF IFD tags (EXIF §4.6.5 Table 4).
	TagMakerNote TagID = 0x927C
)

// tagInfo holds registry metadata for a tag ID.
type tagInfo struct {
	Name  string
	Type  DataType
	Count uint32 // 0 means variable
}

// tagRegistry maps TagID to tagInfo for all known tags.
// Populated at init time; treated as read-only thereafter.
var tagRegistry map[TagID]tagInfo

func init() {
	tagRegistry = make(map[TagID]tagInfo, 128)
	// Entries added here; implementations will populate fully.
}
