package xmp_test

import (
	"fmt"

	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// minimalXMP is a well-formed XMP packet with two dc: properties:
// dc:description = "Grand Canyon" and dc:rights = "2024 Jane Smith".
// The <?xpacket?> wrapper is required by XMP §7.3.
var minimalXMP = []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?>
<x:xmpmeta xmlns:x='adobe:ns:meta/'>
  <rdf:RDF xmlns:rdf='http://www.w3.org/1999/02/22-rdf-syntax-ns#'>
    <rdf:Description rdf:about=''
      xmlns:dc='http://purl.org/dc/elements/1.1/'
      dc:description='Grand Canyon'
      dc:rights='2024 Jane Smith'/>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end='w'?>`)

// ExampleParse demonstrates parsing a raw XMP packet and reading the
// caption (dc:description) and copyright (dc:rights) properties.
func ExampleParse() {
	x, err := xmp.Parse(minimalXMP)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("Caption:  ", x.Caption())
	fmt.Println("Copyright:", x.Copyright())
	// Output:
	// Caption:   Grand Canyon
	// Copyright: 2024 Jane Smith
}

// ExampleXMP_Get demonstrates low-level property access using the Get method
// with explicit namespace URI constants. This is useful for properties that
// do not have a dedicated convenience method.
func ExampleXMP_Get() {
	x, err := xmp.Parse(minimalXMP)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// xmp.NSdc is the Dublin Core namespace URI ("http://purl.org/dc/elements/1.1/").
	v := x.Get(xmp.NSdc, "rights")
	fmt.Println(v)
	// Output: 2024 Jane Smith
}

// ExampleXMP_Set demonstrates setting a property with Set and verifying the
// value survives an Encode → Parse round-trip.
func ExampleXMP_Set() {
	// The zero value of XMP is usable: Set initialises the Properties map on
	// first write, so no explicit construction is required.
	x := &xmp.XMP{}
	x.Set(xmp.NSdc, "creator", "Jane Smith")

	// Encode produces a standards-compliant <?xpacket?> wrapped UTF-8 packet.
	encoded, err := xmp.Encode(x)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	// Parse the encoded bytes to confirm the round-trip.
	x2, err := xmp.Parse(encoded)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println(x2.Creator())
	// Output: Jane Smith
}
