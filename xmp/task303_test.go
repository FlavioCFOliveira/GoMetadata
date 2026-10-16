package xmp

// task303_test.go — regression battery for task #303: Parse then Encode must
// keep nested structures (structs, arrays of structs, structs in structs),
// qualifiers, URI values, empty arrays, rdf:about, x:xmptk, the prefixes of
// unknown namespaces, and rdf:nodeID references; an edit through Properties
// must change only what it names.

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// nestedXMP exercises every RDF form of ISO 16684-1:2019 §7 that the flat
// Properties view cannot carry on its own.
const nestedXMP = `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="Test Toolkit 1.0">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about="uuid:faf5bdd5-ba3d-11da-ad31-d33d75182f1b"
    xmlns:dc="http://purl.org/dc/elements/1.1/"
    xmlns:xmp="http://ns.adobe.com/xap/1.0/"
    xmlns:exif="http://ns.adobe.com/exif/1.0/"
    xmlns:mwg-rs="http://www.metadataworkinggroup.com/schemas/regions/"
    xmlns:mwg-kw="http://www.metadataworkinggroup.com/schemas/keywords/"
    xmlns:stDim="http://ns.adobe.com/xap/1.0/sType/Dimensions#"
    xmlns:stArea="http://ns.adobe.com/xmp/sType/Area#"
    xmlns:Iptc4xmpExt="http://iptc.org/std/Iptc4xmpExt/2008-02-29/"
    xmlns:xmpMM="http://ns.adobe.com/xap/1.0/mm/"
    xmlns:stEvt="http://ns.adobe.com/xap/1.0/sType/ResourceEvent#"
    xmlns:cc="http://creativecommons.org/ns#"
    xmlns:myns="http://example.com/my/1.0/"
    xmp:Rating="3">
   <dc:title>
    <rdf:Alt>
     <rdf:li xml:lang="x-default">Title</rdf:li>
     <rdf:li xml:lang="de">Titel</rdf:li>
    </rdf:Alt>
   </dc:title>
   <dc:subject>
    <rdf:Bag>
     <rdf:li>one</rdf:li>
     <rdf:li>two</rdf:li>
    </rdf:Bag>
   </dc:subject>
   <dc:contributor>
    <rdf:Bag/>
   </dc:contributor>
   <exif:Flash exif:Fired="False" exif:Return="0" exif:Mode="2" exif:Function="False" exif:RedEyeMode="False"/>
   <mwg-rs:Regions rdf:parseType="Resource">
    <mwg-rs:AppliedToDimensions stDim:w="4000" stDim:h="3000" stDim:unit="pixel"/>
    <mwg-rs:RegionList>
     <rdf:Bag>
      <rdf:li>
       <rdf:Description mwg-rs:Name="Anna" mwg-rs:Type="Face">
        <mwg-rs:Area stArea:x="0.5" stArea:y="0.4" stArea:w="0.1" stArea:h="0.2" stArea:unit="normalized"/>
       </rdf:Description>
      </rdf:li>
     </rdf:Bag>
    </mwg-rs:RegionList>
   </mwg-rs:Regions>
   <mwg-kw:Keywords rdf:parseType="Resource">
    <mwg-kw:Hierarchy>
     <rdf:Bag>
      <rdf:li rdf:parseType="Resource">
       <mwg-kw:Keyword>Animals</mwg-kw:Keyword>
       <mwg-kw:Children>
        <rdf:Bag>
         <rdf:li rdf:parseType="Resource">
          <mwg-kw:Keyword>Cats</mwg-kw:Keyword>
         </rdf:li>
        </rdf:Bag>
       </mwg-kw:Children>
      </rdf:li>
     </rdf:Bag>
    </mwg-kw:Hierarchy>
   </mwg-kw:Keywords>
   <Iptc4xmpExt:ArtworkOrObject>
    <rdf:Bag>
     <rdf:li rdf:parseType="Resource">
      <Iptc4xmpExt:AOTitle>
       <rdf:Alt>
        <rdf:li xml:lang="x-default">Mona Lisa</rdf:li>
        <rdf:li xml:lang="fr">La Joconde</rdf:li>
       </rdf:Alt>
      </Iptc4xmpExt:AOTitle>
      <Iptc4xmpExt:AOSource>Louvre</Iptc4xmpExt:AOSource>
     </rdf:li>
    </rdf:Bag>
   </Iptc4xmpExt:ArtworkOrObject>
   <xmpMM:History>
    <rdf:Seq>
     <rdf:li stEvt:action="created" stEvt:when="2024-01-01T10:00:00Z"/>
     <rdf:li rdf:parseType="Resource">
      <stEvt:action>saved</stEvt:action>
      <stEvt:when>2024-01-02T10:00:00Z</stEvt:when>
     </rdf:li>
    </rdf:Seq>
   </xmpMM:History>
   <cc:license rdf:resource="https://creativecommons.org/licenses/by/4.0/"/>
   <myns:Note xml:lang="en">Hello</myns:Note>
   <myns:Qualified rdf:parseType="Resource">
    <rdf:value>main</rdf:value>
    <myns:Source>camera</myns:Source>
   </myns:Qualified>
   <myns:Numbered>
    <rdf:Seq>
     <rdf:_1>first</rdf:_1>
     <rdf:_2>second</rdf:_2>
    </rdf:Seq>
   </myns:Numbered>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

// modelDump renders the whole data model of x — the Properties view and the
// retained tree — as sorted text, so two values can be compared regardless
// of schema order.
func modelDump(x *XMP) string {
	var lines []string
	for ns, m := range x.Properties {
		for k, v := range m {
			lines = append(lines, fmt.Sprintf("P %s %q=%q", ns, k, v))
		}
	}
	if len(x.nodes) > 0 {
		for s := x.nodes[0].firstChild; s != noNode; s = x.nodes[s].next {
			var b strings.Builder
			dumpNode(&b, x.nodes, s, 0)
			lines = append(lines, "T "+b.String())
		}
	}
	lines = append(lines, "about="+x.about, "toolkit="+x.toolkit)
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// dumpNode renders node i of l and its subtree.
func dumpNode(b *strings.Builder, l nodeList, i int32, depth int) {
	n := &l[i]
	const keep = nodeStruct | nodeArray | nodeOrdered | nodeAlternate | nodeURI | nodeItem | nodeBlank
	fmt.Fprintf(b, "%s{%s|%s|%q|%x", strings.Repeat(" ", depth), n.ns, n.name, n.value, n.flags&keep)
	for q := n.firstQual; q != noNode; q = l[q].next {
		b.WriteString(" Q")
		dumpNode(b, l, q, depth+1)
	}
	for c := n.firstChild; c != noNode; c = l[c].next {
		b.WriteString(" C")
		dumpNode(b, l, c, depth+1)
	}
	b.WriteString("}")
}

// TestTask303NestedRoundTrip verifies that Parse → Encode → Parse keeps the
// complete data model and that Encode reaches a fixpoint.
func TestTask303NestedRoundTrip(t *testing.T) {
	t.Parallel()
	x := mustParse(t, []byte(nestedXMP))
	enc := mustEncode(t, x)
	x2 := mustParse(t, enc)
	if a, b := modelDump(x), modelDump(x2); a != b {
		t.Fatalf("data model changed on round trip:\n--- parsed\n%s\n--- re-parsed\n%s\n--- packet\n%s", a, b, enc)
	}
	if enc2 := mustEncode(t, x2); !bytes.Equal(enc, enc2) {
		t.Fatalf("Encode is not a fixpoint:\n%s\n---\n%s", enc, enc2)
	}
	for _, want := range []string{
		`x:xmptk="Test Toolkit 1.0"`,
		`rdf:about="uuid:faf5bdd5-ba3d-11da-ad31-d33d75182f1b"`,
		`xmlns:myns="http://example.com/my/1.0/"`,
		`<rdf:Bag/>`,
		`<cc:license rdf:resource="https://creativecommons.org/licenses/by/4.0/"/>`,
		`<myns:Note xml:lang="en">Hello</myns:Note>`,
		`<rdf:value>main</rdf:value>`,
		`<stArea:x>0.5</stArea:x>`,
		`<mwg-kw:Keyword>Cats</mwg-kw:Keyword>`,
		`<rdf:li xml:lang="fr">La Joconde</rdf:li>`,
		`<stEvt:action>created</stEvt:action>`,
		`<rdf:li>second</rdf:li>`,
	} {
		if !bytes.Contains(enc, []byte(want)) {
			t.Errorf("encoded packet lacks %s", want)
		}
	}
}

// TestTask303PropertiesView verifies the documented flat keys for the forms
// that have one; a qualified value (rdf:value) is "P" = value with no key for
// its qualifiers.
func TestTask303PropertiesView(t *testing.T) {
	t.Parallel()
	x := mustParse(t, []byte(nestedXMP))
	for _, c := range []struct{ ns, key, want string }{
		{NSxmp, "Rating", "3"},
		{NSdc, "title", "Title\x1ede|Titel"},
		{NSdc, "subject", "one\x1etwo"},
		{NSexif, "Flash.Fired", "False"},
		{NSexif, "Flash.Mode", "2"},
		{NSiptcExt, "ArtworkOrObject[0].AOSource", "Louvre"},
		{NSxmpMM, "History[0].action", "created"},
		{NSxmpMM, "History[1].when", "2024-01-02T10:00:00Z"},
		{NScc, "license", "https://creativecommons.org/licenses/by/4.0/"},
		{"http://example.com/my/1.0/", "Note", "Hello"},
		{"http://example.com/my/1.0/", "Qualified", "main"},
		{"http://example.com/my/1.0/", "Qualified.value", ""},
		{"http://example.com/my/1.0/", "Qualified.Source", ""},
		{"http://example.com/my/1.0/", "Numbered", "first\x1esecond"},
	} {
		if got := x.Get(c.ns, c.key); got != c.want {
			t.Errorf("Get(%s, %q) = %q, want %q", c.ns, c.key, got, c.want)
		}
	}
}

// TestTask303SetCaptionKeepsEverythingElse is the reported case: one
// unrelated edit must not drop any nested property.
func TestTask303SetCaptionKeepsEverythingElse(t *testing.T) {
	t.Parallel()
	x := mustParse(t, []byte(nestedXMP))
	before := modelDump(x)
	x.SetCaption("A caption")
	x2 := mustParse(t, mustEncode(t, x))
	if got := x2.Caption(); got != "A caption" {
		t.Fatalf("Caption() = %q", got)
	}
	delete(x2.Properties[NSdc], "description")
	if after := modelDump(x2); after != before {
		t.Fatalf("SetCaption changed other properties:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

// TestTask303EncodeDoesNotMutate verifies that Encode leaves its argument
// unchanged, also when it merges edits, and is safe for concurrent use.
func TestTask303EncodeDoesNotMutate(t *testing.T) {
	t.Parallel()
	x := mustParse(t, []byte(nestedXMP))
	x.Properties[NSexif]["Flash.Mode"] = "3"
	delete(x.Properties[NSxmpMM], "History[1].when")
	before := modelDump(x)
	first := mustEncode(t, x)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if got, err := Encode(x); err != nil || !bytes.Equal(got, first) {
				t.Errorf("concurrent Encode differs (err %v)", err)
			}
		})
	}
	wg.Wait()
	if after := modelDump(x); after != before {
		t.Fatalf("Encode modified its argument:\n%s\n---\n%s", before, after)
	}
}

// TestTask303EditsThroughProperties verifies how Encode applies edits of the
// Properties view to retained nested properties.
func TestTask303EditsThroughProperties(t *testing.T) {
	t.Parallel()
	const my = "http://example.com/my/1.0/"
	cases := []struct {
		name  string
		edit  func(x *XMP)
		check func(t *testing.T, x2 *XMP, enc []byte)
	}{
		{
			name: "change struct field",
			edit: func(x *XMP) { x.Properties[NSexif]["Flash.Mode"] = "3" },
			check: func(t *testing.T, x2 *XMP, _ []byte) {
				t.Helper()
				if got := x2.Get(NSexif, "Flash.Mode"); got != "3" {
					t.Errorf("Flash.Mode = %q", got)
				}
				if got := x2.Get(NSexif, "Flash.Fired"); got != "False" {
					t.Errorf("Flash.Fired = %q", got)
				}
			},
		},
		{
			name: "delete array item field",
			edit: func(x *XMP) { delete(x.Properties[NSxmpMM], "History[1].when") },
			check: func(t *testing.T, x2 *XMP, _ []byte) {
				t.Helper()
				if got := x2.Get(NSxmpMM, "History[1].when"); got != "" {
					t.Errorf("History[1].when = %q, want deleted", got)
				}
				if got := x2.Get(NSxmpMM, "History[1].action"); got != "saved" {
					t.Errorf("History[1].action = %q", got)
				}
			},
		},
		{
			name: "add array item",
			edit: func(x *XMP) { x.Properties[NSxmpMM]["History[2].action"] = "derived" },
			check: func(t *testing.T, x2 *XMP, enc []byte) {
				t.Helper()
				if got := x2.Get(NSxmpMM, "History[2].action"); got != "derived" {
					t.Errorf("History[2].action = %q", got)
				}
				// The new field reuses the namespace of its siblings.
				if !bytes.Contains(enc, []byte("<stEvt:action>derived</stEvt:action>")) {
					t.Errorf("new item not written as stEvt:action:\n%s", enc)
				}
			},
		},
		{
			name: "replace keywords keeps nothing stale",
			edit: func(x *XMP) { x.SetKeywords([]string{"two", "three"}) },
			check: func(t *testing.T, x2 *XMP, _ []byte) {
				t.Helper()
				if got := x2.Keywords(); !slices.Equal(got, []string{"two", "three"}) {
					t.Errorf("Keywords() = %q", got)
				}
			},
		},
		{
			name: "change lang alt default",
			edit: func(x *XMP) { x.Properties[NSdc]["title"] = "New\x1ede|Titel" },
			check: func(t *testing.T, x2 *XMP, _ []byte) {
				t.Helper()
				if got := x2.Get(NSdc, "title"); got != "New\x1ede|Titel" {
					t.Errorf("dc:title = %q", got)
				}
			},
		},
		{
			name: "change qualified value keeps qualifier",
			edit: func(x *XMP) { x.Properties[my]["Qualified"] = "other" },
			check: func(t *testing.T, x2 *XMP, enc []byte) {
				t.Helper()
				if got := x2.Get(my, "Qualified"); got != "other" {
					t.Errorf("Qualified = %q", got)
				}
				if !bytes.Contains(enc, []byte("<myns:Source>camera</myns:Source>")) {
					t.Errorf("qualifier lost:\n%s", enc)
				}
			},
		},
		{
			name: "delete whole retained property",
			edit: func(x *XMP) {
				for k := range x.Properties[NSexif] {
					delete(x.Properties[NSexif], k)
				}
			},
			check: func(t *testing.T, _ *XMP, enc []byte) {
				t.Helper()
				if bytes.Contains(enc, []byte("exif:Flash")) {
					t.Errorf("deleted exif:Flash still written:\n%s", enc)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := mustParse(t, []byte(nestedXMP))
			c.edit(x)
			enc := mustEncode(t, x)
			x2 := mustParse(t, enc)
			c.check(t, x2, enc)
			// Every nested property that was not edited survives.
			for _, want := range []string{"<stArea:x>0.5</stArea:x>", "<mwg-kw:Keyword>Cats</mwg-kw:Keyword>", "La Joconde"} {
				if !bytes.Contains(enc, []byte(want)) {
					t.Errorf("unrelated nested value %s lost", want)
				}
			}
		})
	}
}

// TestTask303PlainDocumentKeepsNoTree verifies the fast path: a document
// whose properties the flat view reproduces exactly retains no tree, so
// Parse costs no more than before for the common case.
func TestTask303PlainDocumentKeepsNoTree(t *testing.T) {
	t.Parallel()
	x := mustParse(t, []byte(representativeXMP))
	if len(x.nodes) != 0 {
		var b strings.Builder
		dumpNode(&b, x.nodes, 0, 0)
		t.Fatalf("representative packet retained %d nodes: %s", len(x.nodes), b.String())
	}
}

// TestTask303NodeIDPreserved verifies that rdf:nodeID references (written
// by f-spot, among others) are kept, so RDF-aware readers resolve them in
// the written packet as in the source.
func TestTask303NodeIDPreserved(t *testing.T) {
	t.Parallel()
	doc := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF
 xmlns:dc="http://purl.org/dc/elements/1.1/"
 xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Bag rdf:nodeID="anon2"><rdf:li>ExifTool</rdf:li><rdf:li>XMP</rdf:li></rdf:Bag>
<rdf:Description rdf:about="" dc:title="nodeID test"><dc:subject rdf:nodeID="anon2"/></rdf:Description>
</rdf:RDF></x:xmpmeta>`
	x := mustParse(t, []byte(doc))
	enc := mustEncode(t, x)
	for _, want := range []string{`<dc:subject rdf:nodeID="anon2">`, `<rdf:Bag rdf:nodeID="anon2">`, `<rdf:li>ExifTool</rdf:li>`} {
		if !bytes.Contains(enc, []byte(want)) {
			t.Errorf("packet lacks %s:\n%s", want, enc)
		}
	}
	if a, b := modelDump(x), modelDump(mustParse(t, enc)); a != b {
		t.Fatalf("model changed:\n%s\n---\n%s", a, b)
	}
}

// TestTask303PrefixOwnership verifies that a prefix rebound to a second
// unknown URI stays with the first URI, as NS-03 requires one URI per prefix.
func TestTask303PrefixOwnership(t *testing.T) {
	t.Parallel()
	doc := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:xxxx="http://testtag.com/fish/1.0/"><xxxx:Test>trout</xxxx:Test></rdf:Description>
<rdf:Description rdf:about="" xmlns:xxxx="http://testtag.com/feline/1.0/"><xxxx:Test>tabby</xxxx:Test></rdf:Description>
</rdf:RDF></x:xmpmeta>`
	enc := mustEncode(t, mustParse(t, []byte(doc)))
	if !bytes.Contains(enc, []byte(`xmlns:xxxx="http://testtag.com/fish/1.0/"`)) {
		t.Errorf("first URI lost its prefix:\n%s", enc)
	}
	if bytes.Count(enc, []byte("xmlns:xxxx=")) != 1 {
		t.Errorf("prefix xxxx bound twice:\n%s", enc)
	}
}

// TestTask303PrefixReservedForNestedNamespace verifies that a document that
// binds a well-known prefix ("dc") to an unknown URI cannot make the writer
// bind "dc" twice when the real dc namespace occurs only inside a struct.
func TestTask303PrefixReservedForNestedNamespace(t *testing.T) {
	t.Parallel()
	doc := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:dc="http://example.com/fake-dc/" xmlns:purl="http://purl.org/dc/elements/1.1/" xmlns:s="http://example.com/s/">
<dc:Thing>fake</dc:Thing>
<s:Holder rdf:parseType="Resource"><purl:format>image/jpeg</purl:format></s:Holder>
</rdf:Description></rdf:RDF></x:xmpmeta>`
	x := mustParse(t, []byte(doc))
	enc := mustEncode(t, x)
	if bytes.Contains(enc, []byte(`xmlns:dc="http://example.com/fake-dc/"`)) {
		t.Fatalf("unknown URI took the canonical dc prefix:\n%s", enc)
	}
	if a, b := modelDump(x), modelDump(mustParse(t, enc)); a != b {
		t.Fatalf("round trip changed the model:\n%s\n---\n%s\n%s", a, b, enc)
	}
}

// TestTask303DuplicateAttributesKeepFirst covers the fuzz finding
// 02ab6634eeb7616c: a repeated attribute (ill-formed, XML 1.0 §3.1) used to
// become two xml:lang qualifiers, written back as a repeated attribute. The
// first occurrence wins, as for repeated properties.
func TestTask303DuplicateAttributesKeepFirst(t *testing.T) {
	t.Parallel()
	doc := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:q="http://example.com/q/">
<dc:title><rdf:Alt><rdf:li xml:lang="en" xml:lang="de">T</rdf:li></rdf:Alt></dc:title>
<q:P q:a="1" q:a="2">v</q:P>
</rdf:Description></rdf:RDF></x:xmpmeta>`
	x := mustParse(t, []byte(doc))
	if got := x.Get(NSdc, "title"); got != "en|T" {
		t.Errorf("dc:title = %q, want %q", got, "en|T")
	}
	enc := mustEncode(t, x)
	if bytes.Count(enc, []byte("xml:lang=")) != 1 || bytes.Contains(enc, []byte(">2<")) {
		t.Fatalf("repeated attribute written back:\n%s", enc)
	}
	if again := mustEncode(t, mustParse(t, enc)); !bytes.Equal(enc, again) {
		t.Fatalf("Encode is not a fixpoint:\n%s\n---\n%s", enc, again)
	}
}

// TestTask303InvalidNamesDropped covers the fuzz finding 0df20a72153b2b60:
// an element or attribute whose local name is empty or contains a byte that
// no XML name may contain (here an empty name and a second ':') was kept and
// then written as a different or ill-formed name. Such names are dropped on
// read, so Encode reproduces what Parse kept.
func TestTask303InvalidNamesDropped(t *testing.T) {
	t.Parallel()
	doc := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:q="http://example.com/q/" q:a:b="1" q:ok="2"><q:>x</q:><q:c:d>y</q:c:d><q:good>z</q:good></rdf:Description>
</rdf:RDF></x:xmpmeta>`
	x := mustParse(t, []byte(doc))
	m := x.Properties["http://example.com/q/"]
	if len(m) != 2 || m["ok"] != "2" || m["good"] != "z" {
		t.Fatalf("Properties = %q, want only ok and good", m)
	}
	enc := mustEncode(t, x)
	if again := mustEncode(t, mustParse(t, enc)); !bytes.Equal(enc, again) {
		t.Fatalf("Encode is not a fixpoint:\n%s\n---\n%s", enc, again)
	}
}

// TestTask303TextWithManyAttributes verifies the leniency for a text element
// that carries non-RDF attributes (invalid RDF, read by ExifTool as
// qualifiers) whatever the number of attributes: the text is kept and the
// attributes become qualifiers, as with up to three attributes.
func TestTask303TextWithManyAttributes(t *testing.T) {
	t.Parallel()
	doc := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:q="http://example.com/q/">
<q:Few q:a="1">few</q:Few><q:Many q:a="1" q:b="2" q:c="3" q:d="4">many</q:Many>
</rdf:Description></rdf:RDF></x:xmpmeta>`
	x := mustParse(t, []byte(doc))
	for _, c := range []struct{ key, want string }{{"Few", "few"}, {"Many", "many"}} {
		if got := x.Get("http://example.com/q/", c.key); got != c.want {
			t.Errorf("%s = %q, want %q", c.key, got, c.want)
		}
	}
	enc := mustEncode(t, x)
	if !bytes.Contains(enc, []byte("<q:d>4</q:d>")) {
		t.Fatalf("qualifier lost:\n%s", enc)
	}
	if a, b := modelDump(x), modelDump(mustParse(t, enc)); a != b {
		t.Fatalf("round trip changed the model:\n%s\n---\n%s", a, b)
	}
}

// TestTask303QualifiedValueWideStruct verifies the rdf:value fix-up of a
// struct with many fields: every field becomes a qualifier, in linear time
// (the fix-up used to search the qualifier list once per field).
func TestTask303QualifiedValueWideStruct(t *testing.T) {
	t.Parallel()
	const fields = 50000
	var b strings.Builder
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about="" xmlns:q="http://example.com/q/"><q:P rdf:parseType="Resource"><rdf:value>v</rdf:value>`)
	for i := range fields {
		fmt.Fprintf(&b, "<q:f%d>%d</q:f%d>", i, i, i)
	}
	b.WriteString(`</q:P></rdf:Description></rdf:RDF></x:xmpmeta>`)
	x := mustParse(t, []byte(b.String()))
	if got := x.Get("http://example.com/q/", "P"); got != "v" {
		t.Fatalf("P = %q, want v", got)
	}
	enc := mustEncode(t, x)
	if got := bytes.Count(enc, []byte("<q:f")); got != fields {
		t.Fatalf("written packet has %d qualifiers, want %d", got, fields)
	}
}

// TestTask303ManyRetainedEdits edits every field of 40 retained struct
// properties whose fields live in 40 different namespaces, which exercises
// the per-property key grouping and the indexed namespace collection of
// Encode.
func TestTask303ManyRetainedEdits(t *testing.T) {
	t.Parallel()
	const n = 40
	var b strings.Builder
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:s="http://example.com/s/"`)
	for i := range n {
		fmt.Fprintf(&b, ` xmlns:f%d="http://example.com/f%d/"`, i, i)
	}
	b.WriteString(`>`)
	for i := range n {
		fmt.Fprintf(&b, `<s:P%d rdf:parseType="Resource"><f%d:a>old%d</f%d:a><f%d:b>keep%d</f%d:b></s:P%d>`, i, i, i, i, i, i, i, i)
	}
	b.WriteString(`</rdf:Description></rdf:RDF></x:xmpmeta>`)
	x := mustParse(t, []byte(b.String()))
	m := x.Properties["http://example.com/s/"]
	for i := range n {
		m[fmt.Sprintf("P%d.a", i)] = fmt.Sprintf("new%d", i)
	}
	enc := mustEncode(t, x)
	x2 := mustParse(t, enc)
	for i := range n {
		if got := x2.Get("http://example.com/s/", fmt.Sprintf("P%d.a", i)); got != fmt.Sprintf("new%d", i) {
			t.Fatalf("P%d.a = %q", i, got)
		}
		if got := x2.Get("http://example.com/s/", fmt.Sprintf("P%d.b", i)); got != fmt.Sprintf("keep%d", i) {
			t.Fatalf("P%d.b = %q", i, got)
		}
		if !bytes.Contains(enc, []byte(fmt.Sprintf(`<f%d:a>new%d</f%d:a>`, i, i, i))) {
			t.Fatalf("field P%d.a not written in its own namespace", i)
		}
	}
}

// TestTask303SimpleValueOfArrayPropertyConverted pins the conversion of a
// simple value of a property that the schema defines as an array (here
// dc:rights, a Lang Alt, written as a plain attribute): Encode writes it as a
// one-item rdf:Alt with x-default, as the previous parser did, and no tree
// node is kept for it. A value containing '|' cannot go through the
// "lang|value" convention and is written back as parsed.
func TestTask303SimpleValueOfArrayPropertyConverted(t *testing.T) {
	t.Parallel()
	doc := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" dc:rights="(c) 2024 Test"/>
</rdf:RDF></x:xmpmeta>`
	x := mustParse(t, []byte(doc))
	if len(x.nodes) != 0 {
		t.Fatalf("simple dc:rights retained %d tree nodes, want none", len(x.nodes))
	}
	enc := mustEncode(t, x)
	if !bytes.Contains(enc, []byte(`<rdf:li xml:lang="x-default">(c) 2024 Test</rdf:li>`)) {
		t.Fatalf("dc:rights not converted to rdf:Alt:\n%s", enc)
	}
	if got := mustParse(t, enc).Copyright(); got != "(c) 2024 Test" {
		t.Fatalf("Copyright() after round trip = %q", got)
	}

	pipe := strings.Replace(doc, "(c) 2024 Test", "a|b", 1)
	enc = mustEncode(t, mustParse(t, []byte(pipe)))
	if !bytes.Contains(enc, []byte("<dc:rights>a|b</dc:rights>")) {
		t.Fatalf("dc:rights with '|' not kept as parsed:\n%s", enc)
	}
}

// TestTask303TypedNodesShareTypeURI covers the fuzz finding
// c114f1e1801a4aa2: the rdf:type qualifier of a typed node holds the
// node's namespace URI plus its local name, so many items typed with a
// long namespace declared once used to copy that URI into the arena once
// per item. Each distinct type URI is now stored once.
func TestTask303TypedNodesShareTypeURI(t *testing.T) {
	t.Parallel()
	ns := "http://example.com/" + strings.Repeat("n", 2000) + "/"
	var b strings.Builder
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about="" xmlns:t="` + ns + `"><t:P><rdf:Seq>`)
	for range 200 {
		b.WriteString(`<rdf:li><t:Type t:f="v"/></rdf:li>`)
	}
	b.WriteString(`</rdf:Seq></t:P></rdf:Description></rdf:RDF></x:xmpmeta>`)
	doc := []byte(b.String())
	x := mustParse(t, doc)
	if len(x.arena) > 2*len(doc) {
		t.Fatalf("arena %d bytes for a %d-byte document", len(x.arena), len(doc))
	}
	enc := mustEncode(t, x)
	if got := bytes.Count(enc, []byte(`<rdf:type rdf:resource="`+ns+`Type"/>`)); got != 200 {
		t.Fatalf("written packet has %d rdf:type qualifiers, want 200", got)
	}
	if a, b := modelDump(x), modelDump(mustParse(t, enc)); a != b {
		t.Fatal("round trip changed the model")
	}
}

// TestTask303CorpusSidecars round-trips every XMP sidecar of the corpus and
// requires the data model to survive unchanged.
func TestTask303CorpusSidecars(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("..", "testdata", "corpus", "xmp")
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".xmp") {
			files = append(files, p)
		}
		return nil
	})
	if len(files) == 0 {
		t.Skipf("corpus directory %s is absent or empty; run 'make testdata'", dir)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		x, err := Parse(data)
		if err != nil {
			continue // documents that Parse rejects are covered elsewhere
		}
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if a, b := modelDump(x), modelDump(x2); a != b {
			t.Errorf("%s: data model changed on round trip:\n--- parsed\n%s\n--- re-parsed\n%s", f, a, b)
		}
		if !maps.EqualFunc(x.Properties, x2.Properties, maps.Equal) {
			t.Errorf("%s: Properties changed on round trip", f)
		}
	}
}
