package exif_test

import (
	"fmt"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// minimalEXIF returns 34 bytes that form a valid little-endian EXIF block
// containing a single IFD0 entry: TagModel (0x0110) = "TestCam".
//
// Layout (TIFF §2, CIPA DC-008-2023 §4.5.4):
//
//	offset  0: 49 49        — "II" little-endian byte order marker
//	offset  2: 2A 00        — TIFF magic = 42
//	offset  4: 08 00 00 00  — IFD0 starts at offset 8
//	offset  8: 01 00        — IFD0 has 1 entry
//	offset 10: 10 01        — tag = 0x0110 (TagModel, 272 decimal)
//	offset 12: 02 00        — type = ASCII (2)
//	offset 14: 08 00 00 00  — count = 8 (7 chars + NUL)
//	offset 18: 1A 00 00 00  — value offset = 26
//	offset 22: 00 00 00 00  — next-IFD offset = 0 (end of chain)
//	offset 26: 54 65 73 74 43 61 6D 00  — "TestCam\x00"
func minimalEXIF() []byte {
	return []byte{
		// TIFF header (8 bytes)
		0x49, 0x49, // "II" — little-endian
		0x2A, 0x00, // TIFF magic 42
		0x08, 0x00, 0x00, 0x00, // IFD0 offset = 8

		// IFD0 entry count (2 bytes)
		0x01, 0x00, // 1 entry

		// IFD entry for TagModel (12 bytes)
		0x10, 0x01, // tag = 0x0110 (272)
		0x02, 0x00, // type = ASCII (2)
		0x08, 0x00, 0x00, 0x00, // count = 8 ("TestCam\x00")
		0x1A, 0x00, 0x00, 0x00, // value offset = 26

		// Next-IFD pointer (4 bytes)
		0x00, 0x00, 0x00, 0x00, // 0 = no next IFD

		// Value area: "TestCam\x00" at offset 26
		0x54, 0x65, 0x73, 0x74, 0x43, 0x61, 0x6D, 0x00,
	}
}

// ExampleParse demonstrates parsing a raw EXIF byte block, iterating the
// IFD0 entries, and reading the camera model string.
func ExampleParse() {
	e, err := exif.Parse(minimalEXIF())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println(e.CameraModel())
	// Output: TestCam
}

// ExampleParse_skipMakerNote demonstrates using the SkipMakerNote option.
// When no MakerNote is present the option has no observable effect, but on
// camera files it avoids the cost of manufacturer-specific IFD parsing.
func ExampleParse_skipMakerNote() {
	e, err := exif.Parse(minimalEXIF(), exif.SkipMakerNote())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// MakerNoteIFD is nil — either because SkipMakerNote was passed or
	// because the file contains no MakerNote tag (as is the case here).
	fmt.Println("MakerNoteIFD nil:", e.MakerNoteIFD == nil)
	fmt.Println("Model:           ", e.CameraModel())
	// Output:
	// MakerNoteIFD nil: true
	// Model:            TestCam
}

// ExampleIFD_Get demonstrates low-level IFD entry access.
// IFD.Get performs a binary search over the sorted entry list and returns
// a pointer to the matching IFDEntry, or nil when the tag is absent.
func ExampleIFD_Get() {
	e, err := exif.Parse(minimalEXIF())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	entry := e.IFD0.Get(exif.TagModel)
	if entry == nil {
		fmt.Println("tag not found")
		return
	}

	// String() strips the trailing NUL terminator required by the TIFF spec
	// (TIFF §2: ASCII fields are NUL-terminated).
	fmt.Println(entry.String())
	// Output: TestCam
}

// ExampleEncode demonstrates modifying a tag and re-encoding the EXIF block.
// The typical workflow is: Parse → mutate with setters → Encode → embed in container.
func ExampleEncode() {
	e, err := exif.Parse(minimalEXIF())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// Overwrite the camera model using the high-level setter.
	e.SetCameraModel("NewCam")

	// Re-encode to a fresh byte slice.
	b, err := exif.Encode(e)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// Parse the re-encoded bytes to confirm the mutation round-tripped.
	e2, err := exif.Parse(b)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println(e2.CameraModel())
	// Output: NewCam
}
