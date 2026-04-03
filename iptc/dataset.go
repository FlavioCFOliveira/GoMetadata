package iptc

// Well-known Record 1 (Envelope Record) dataset numbers (IIM §1.5).
const (
	// DS1CodedCharacterSet is dataset 1:90, which declares the character
	// encoding used in Record 2 text fields. The value ESC % G signals UTF-8
	// per IIM §1.5.1. This dataset belongs to Record 1, not Record 2.
	DS1CodedCharacterSet uint8 = 90
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
	DS2Byline          uint8 = 80  // By-line (author)
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
