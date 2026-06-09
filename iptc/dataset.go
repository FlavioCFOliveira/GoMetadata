package iptc

// Well-known Record 1 (Envelope Record) dataset numbers (IIM §1.5).
const (
	// DS1EnvelopeRecordVersion is dataset 1:00 — the mandatory Envelope Record
	// Version field. IIM §1.6.1 requires it to be the first dataset of Record 1
	// when Record 1 is present; its value is a big-endian uint16 = 4.
	DS1EnvelopeRecordVersion uint8 = 0

	// DS1CodedCharacterSet is dataset 1:90, which declares the character
	// encoding used in Record 2 text fields. The value ESC % G signals UTF-8
	// per IIM §1.5.1. This dataset belongs to Record 1, not Record 2.
	DS1CodedCharacterSet uint8 = 90
)

// Well-known Record 2 mandatory dataset numbers (IIM §2.2.1).
const (
	// DS2ApplicationRecordVersion is dataset 2:00 — the mandatory Application
	// Record Version field. IIM §2.2.1 requires it to be the first dataset of
	// Record 2; its value is a big-endian uint16 = 4. Encode emits this as the
	// very first Record-2 dataset (IIM-REC-02, task #153).
	DS2ApplicationRecordVersion uint8 = 0
)

// Well-known Record 2 (Application Record) dataset numbers (IIM §2.2).
// Record 2 contains the primary image metadata used by most applications.
const (
	DS2ObjectTypeRef   uint8 = 3   // Object Type Reference
	DS2ObjectAttrRef   uint8 = 4   // Object Attribute Reference
	DS2ObjectName      uint8 = 5   // Object Name (title)
	DS2EditStatus      uint8 = 7   // Edit Status
	DS2Urgency         uint8 = 10  // Urgency
	DS2SubjectRef      uint8 = 12  // Subject Reference
	DS2Category        uint8 = 15  // Category
	DS2SupplCategory   uint8 = 20  // Supplemental Category
	DS2Keywords        uint8 = 25  // Keywords (repeatable)
	DS2SpecialInstr    uint8 = 40  // Special Instructions
	DS2DateCreated     uint8 = 55  // Date Created (CCYYMMDD)
	DS2TimeCreated     uint8 = 60  // Time Created (HHMMSS±HHMM)
	DS2DigCreationDate uint8 = 62  // Digital Creation Date (CCYYMMDD)
	DS2DigCreationTime uint8 = 63  // Digital Creation Time (HHMMSS±HHMM)
	DS2OriginProgram   uint8 = 65  // Originating Program
	DS2ProgramVersion  uint8 = 70  // Program Version
	DS2Byline          uint8 = 80  // By-line (author, repeatable per IIM §2.2.25)
	DS2BylineTitle     uint8 = 85  // By-line Title
	DS2City            uint8 = 90  // City
	DS2SubLocation     uint8 = 92  // Sub-location
	DS2ProvinceState   uint8 = 95  // Province/State
	DS2CountryCode     uint8 = 100 // Country/Primary Location Code (ISO 3166)
	DS2CountryName     uint8 = 101 // Country/Primary Location Name
	DS2OrigTransRef    uint8 = 103 // Original Transmission Reference
	DS2Headline        uint8 = 105 // Headline
	DS2Credit          uint8 = 110 // Credit
	DS2Source          uint8 = 115 // Source
	DS2CopyrightNotice uint8 = 116 // Copyright Notice
	DS2Contact         uint8 = 118 // Contact
	DS2Caption         uint8 = 120 // Caption/Abstract
	DS2CaptionWriter   uint8 = 122 // Caption Writer/Editor
	DS2ImageType       uint8 = 130 // Image Type
	DS2ImageOrient     uint8 = 131 // Image Orientation
	DS2LangID          uint8 = 135 // Language Identifier
)

// datasetMaxLen maps a Record 2 dataset number to its maximum byte length as
// defined by IPTC IIM version 4.2, §2.2 (Application Record). A value of 0
// means no specific limit is defined by the spec for that dataset, so no
// truncation is applied by this library.
//
// These limits are enforced at write time (setRecord2, AddKeyword, AddCreator)
// via truncateToLimit. The policy is UTF-8-safe truncation: the value is
// trimmed to at most maxLen bytes but never cut in the middle of a multi-byte
// UTF-8 rune (IIM §2.2; see truncateToLimit).
//
// IIM §2.2 field-length table:
//
//	2:05  Object Name              64 bytes
//	2:07  Edit Status              64 bytes
//	2:10  Urgency                   1 byte
//	2:12  Subject Reference       236 bytes
//	2:15  Category                  3 bytes
//	2:20  Supplemental Category   32 bytes/occurrence
//	2:25  Keywords                 64 bytes/occurrence
//	2:40  Special Instructions    256 bytes
//	2:55  Date Created              8 bytes
//	2:60  Time Created             11 bytes
//	2:62  Digital Creation Date    8 bytes
//	2:63  Digital Creation Time   11 bytes
//	2:65  Originating Program      32 bytes
//	2:70  Program Version          10 bytes
//	2:80  By-line                  32 bytes/occurrence
//	2:85  By-line Title            32 bytes/occurrence
//	2:90  City                     32 bytes
//	2:92  Sub-location             32 bytes
//	2:95  Province/State           32 bytes
//	2:100 Country Code              3 bytes
//	2:101 Country Name             64 bytes
//	2:103 Original Transmission Ref 32 bytes
//	2:105 Headline                 256 bytes
//	2:110 Credit                   32 bytes
//	2:115 Source                   32 bytes
//	2:116 Copyright Notice        128 bytes
//	2:118 Contact                 128 bytes
//	2:120 Caption/Abstract       2000 bytes
//	2:122 Caption Writer/Editor    32 bytes
//	2:130 Image Type                2 bytes
//	2:131 Image Orientation         1 byte
//	2:135 Language Identifier       3 bytes
var datasetMaxLen = [256]int{ //nolint:gochecknoglobals // read-only table; global avoids per-call allocation
	DS2ObjectName:      64,
	DS2EditStatus:      64,
	DS2Urgency:         1,
	DS2SubjectRef:      236,
	DS2Category:        3,
	DS2SupplCategory:   32,
	DS2Keywords:        64,
	DS2SpecialInstr:    256,
	DS2DateCreated:     8,
	DS2TimeCreated:     11,
	DS2DigCreationDate: 8,
	DS2DigCreationTime: 11,
	DS2OriginProgram:   32,
	DS2ProgramVersion:  10,
	DS2Byline:          32,
	DS2BylineTitle:     32,
	DS2City:            32,
	DS2SubLocation:     32,
	DS2ProvinceState:   32,
	DS2CountryCode:     3,
	DS2CountryName:     64,
	DS2OrigTransRef:    32,
	DS2Headline:        256,
	DS2Credit:          32,
	DS2Source:          32,
	DS2CopyrightNotice: 128,
	DS2Contact:         128,
	DS2Caption:         2000,
	DS2CaptionWriter:   32,
	DS2ImageType:       2,
	DS2ImageOrient:     1,
	DS2LangID:          3,
}
