package iptc_test

import (
	"fmt"

	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// minimalIPTC returns a synthetic IIM byte stream with two datasets:
//
//	2:120 (Caption/Abstract) = "Sunset"
//	2:25  (Keywords)         = "landscape"
//
// Each dataset is encoded as:
//
//	0x1C          — tag marker (IIM §1.6)
//	record (1 B)  — record number
//	dataset (1 B) — dataset number
//	size_high (1 B) + size_low (1 B) — big-endian data length
//	data bytes
func minimalIPTC() []byte {
	return []byte{
		// 2:120 Caption/Abstract = "Sunset" (6 bytes)
		0x1C, 0x02, 0x78, 0x00, 0x06,
		'S', 'u', 'n', 's', 'e', 't',

		// 2:25 Keywords = "landscape" (9 bytes)
		0x1C, 0x02, 0x19, 0x00, 0x09,
		'l', 'a', 'n', 'd', 's', 'c', 'a', 'p', 'e',
	}
}

// ExampleParse demonstrates parsing a raw IIM byte stream and reading the
// caption and keywords from it.
func ExampleParse() {
	i, err := iptc.Parse(minimalIPTC())
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("Caption:", i.Caption())
	fmt.Println("Keywords:", i.Keywords())
	// Output:
	// Caption: Sunset
	// Keywords: [landscape]
}

// ExampleEncode demonstrates building an IPTC struct from scratch, setting
// a caption and a keyword, encoding to bytes, and round-tripping back through
// Parse to verify the output.
func ExampleEncode() {
	// The zero value of IPTC is valid: Records is a [10][]Dataset array whose
	// nil slices are populated by the setter methods (IIM §2.2).
	i := &iptc.IPTC{}
	i.SetCaption("Test")
	i.AddKeyword("go")

	b, err := iptc.Encode(i)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// Parse the encoded bytes to confirm the round-trip.
	i2, err := iptc.Parse(b)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("Caption:", i2.Caption())
	kw := i2.Keywords()
	if len(kw) > 0 {
		fmt.Println("Keyword:", kw[0])
	}
	// Output:
	// Caption: Test
	// Keyword: go
}
