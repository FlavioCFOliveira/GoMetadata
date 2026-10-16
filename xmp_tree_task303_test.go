package gometadata

// xmp_tree_task303_test.go — end-to-end regression test through the public
// Read/Write API for task #303: nested XMP structures were dropped by an
// unrelated edit.

import (
	"bytes"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// nestedPacket carries the nested forms of the #303 report: MWG regions, an
// MWG keyword hierarchy, the exif:Flash struct, IPTC Extension artwork with
// language alternatives, and a qualified value in a custom namespace.
const nestedPacket = `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="Test Toolkit">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about=""
    xmlns:exif="http://ns.adobe.com/exif/1.0/"
    xmlns:mwg-rs="http://www.metadataworkinggroup.com/schemas/regions/"
    xmlns:mwg-kw="http://www.metadataworkinggroup.com/schemas/keywords/"
    xmlns:stDim="http://ns.adobe.com/xap/1.0/sType/Dimensions#"
    xmlns:stArea="http://ns.adobe.com/xmp/sType/Area#"
    xmlns:Iptc4xmpExt="http://iptc.org/std/Iptc4xmpExt/2008-02-29/"
    xmlns:myXMPns="http://example.com/myXMPns/">
   <exif:Flash exif:Fired="False" exif:Return="0" exif:Mode="2" exif:Function="False" exif:RedEyeMode="False"/>
   <mwg-rs:Regions rdf:parseType="Resource">
    <mwg-rs:AppliedToDimensions stDim:w="4000" stDim:h="3000" stDim:unit="pixel"/>
    <mwg-rs:RegionList>
     <rdf:Bag>
      <rdf:li rdf:parseType="Resource">
       <mwg-rs:Name>Anna</mwg-rs:Name>
       <mwg-rs:Type>Face</mwg-rs:Type>
       <mwg-rs:Area stArea:x="0.5" stArea:y="0.4" stArea:w="0.1" stArea:h="0.2" stArea:unit="normalized"/>
      </rdf:li>
     </rdf:Bag>
    </mwg-rs:RegionList>
   </mwg-rs:Regions>
   <mwg-kw:Keywords rdf:parseType="Resource">
    <mwg-kw:Hierarchy>
     <rdf:Bag>
      <rdf:li rdf:parseType="Resource">
       <mwg-kw:Keyword>Animals</mwg-kw:Keyword>
       <mwg-kw:Children><rdf:Bag><rdf:li rdf:parseType="Resource"><mwg-kw:Keyword>Cats</mwg-kw:Keyword></rdf:li></rdf:Bag></mwg-kw:Children>
      </rdf:li>
     </rdf:Bag>
    </mwg-kw:Hierarchy>
   </mwg-kw:Keywords>
   <Iptc4xmpExt:ArtworkOrObject>
    <rdf:Bag>
     <rdf:li rdf:parseType="Resource">
      <Iptc4xmpExt:AOTitle><rdf:Alt><rdf:li xml:lang="x-default">Mona Lisa</rdf:li><rdf:li xml:lang="fr">La Joconde</rdf:li></rdf:Alt></Iptc4xmpExt:AOTitle>
     </rdf:li>
    </rdf:Bag>
   </Iptc4xmpExt:ArtworkOrObject>
   <myXMPns:BTestTag rdf:parseType="Resource">
    <rdf:value>main</rdf:value>
    <myXMPns:Field1>qualifier</myXMPns:Field1>
   </myXMPns:BTestTag>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

// expectedPacket returns the packet that Encode writes for raw after edit:
// writing the same data model twice yields the same bytes, so comparing two
// encodings compares two data models.
func expectedPacket(t *testing.T, raw []byte, edit func(*xmp.XMP)) []byte {
	t.Helper()
	x, err := xmp.Parse(raw)
	if err != nil {
		t.Fatalf("xmp.Parse: %v", err)
	}
	edit(x)
	b, err := xmp.Encode(x)
	if err != nil {
		t.Fatalf("xmp.Encode: %v", err)
	}
	return b
}

// writtenPacket reads the XMP of a written file and re-encodes it.
func writtenPacket(t *testing.T, file []byte) []byte {
	t.Helper()
	m, err := Read(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("Read(written): %v", err)
	}
	return expectedPacket(t, m.RawXMP(), func(*xmp.XMP) {})
}

// TestTask303JPEGSetCaptionKeepsNestedXMP is the reported case: SetCaption
// and Write on a JPEG must keep every nested XMP property.
func TestTask303JPEGSetCaptionKeepsNestedXMP(t *testing.T) {
	t.Parallel()
	src := fuzzBuildJPEGWithXMP([]byte(nestedPacket))
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.SetCaption("caption")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := expectedPacket(t, []byte(nestedPacket), func(x *xmp.XMP) { x.SetCaption("caption") })
	got := writtenPacket(t, out.Bytes())
	if !bytes.Equal(got, want) {
		t.Fatalf("written XMP data model differs:\n--- want\n%s\n--- got\n%s", want, got)
	}
	for _, v := range []string{"stArea:x", "Cats", "La Joconde", "exif:RedEyeMode", "<myXMPns:Field1>qualifier</myXMPns:Field1>", "stDim:w"} {
		if !bytes.Contains(got, []byte(v)) {
			t.Errorf("written XMP lacks %s", v)
		}
	}
}
