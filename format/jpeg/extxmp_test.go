package jpeg

// extxmp_test.go — regression battery for extended XMP in JPEG (tasks #278,
// #315; Adobe XMP Specification Part 3 §1.1.3.1).

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: the spec mandates MD5 for the extended XMP GUID
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	xmppkg "github.com/FlavioCFOliveira/GoMetadata/xmp"
)

const testGUID = "04B9E48040A30A6308713BD1E4223B41"

// TestExtractGUIDFromMain covers both RDF serialisations of
// xmpNote:HasExtendedXMP and the inputs that must not yield a GUID (#278).
func TestExtractGUIDFromMain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, main, want string
		ok               bool
	}{
		{"attribute double quote", ` xmpNote:HasExtendedXMP="` + testGUID + `"/>`, testGUID, true},
		{"attribute single quote", "\n  xmpNote:HasExtendedXMP='" + testGUID + "'>", testGUID, true},
		{"attribute white space around Eq", "\txmpNote:HasExtendedXMP \r\n= \"" + testGUID + `"`, testGUID, true},
		{"attribute other prefix", ` n0:HasExtendedXMP="` + testGUID + `"`, testGUID, true},
		{"attribute lowercase hex", ` xmpNote:HasExtendedXMP="` + strings.ToLower(testGUID) + `"`, strings.ToLower(testGUID), true},
		{"element", `<xmpNote:HasExtendedXMP>` + testGUID + `</xmpNote:HasExtendedXMP>`, testGUID, true},
		{"element with attribute", `<xmpNote:HasExtendedXMP rdf:datatype="x">` + testGUID + `</xmpNote:HasExtendedXMP>`, testGUID, true},
		{"element after invalid occurrence", `<dc:note>HasExtendedXMP</dc:note><xmpNote:HasExtendedXMP>` + testGUID + `</xmpNote:HasExtendedXMP>`, testGUID, true},
		{"element with white space", `<xmpNote:HasExtendedXMP> ` + testGUID + ` </xmpNote:HasExtendedXMP>`, "", false},
		{"element empty", `<xmpNote:HasExtendedXMP/>`, "", false},
		{"end tag only", `</xmpNote:HasExtendedXMP>` + testGUID + `<`, "", false},
		{"no prefix", ` HasExtendedXMP="` + testGUID + `"`, "", false},
		{"empty prefix", ` :HasExtendedXMP="` + testGUID + `"`, "", false},
		{"longer name", ` xmpNote:HasExtendedXMPx="` + testGUID + `"`, "", false},
		{"longer element name", `<xmpNote:HasExtendedXMPx>` + testGUID + `</xmpNote:HasExtendedXMPx>`, "", false},
		{"31 digits", ` xmpNote:HasExtendedXMP="` + testGUID[:31] + `"`, "", false},
		{"33 digits", ` xmpNote:HasExtendedXMP="` + testGUID + `0"`, "", false},
		{"not hex", ` xmpNote:HasExtendedXMP="` + strings.Repeat("G", 32) + `"`, "", false},
		{"mismatched quotes", ` xmpNote:HasExtendedXMP="` + testGUID + `'`, "", false},
		{"truncated", ` xmpNote:HasExtendedXMP="` + testGUID, "", false},
		{"at start of input", `xmpNote:HasExtendedXMP="` + testGUID + `"`, "", false},
		{"absent", `<rdf:Description/>`, "", false},
	}
	for _, tc := range tests {
		got, ok := extractGUIDFromMain([]byte(tc.main))
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: extractGUIDFromMain = %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// TestTask278_SpliceFallbackExtendedWins covers the byte-splice fallback for
// an extended XMP that is a bare rdf:Description fragment: its properties are
// spliced before those of the standard packet, so a property both carry
// takes the extended value, the policy of xmp.ParseExtended and of
// XMPUtils::MergeFromJPEG in the Adobe XMP Toolkit.
func TestTask278_SpliceFallbackExtendedWins(t *testing.T) {
	t.Parallel()
	main := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
		` xmpNote:HasExtendedXMP="` + testGUID + `" tiff:Model="StandardModel" tiff:Make="StandardMake"/>` +
		`</rdf:RDF></x:xmpmeta>`)
	frag := []byte(`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="ExtendedModel"/></rdf:RDF>`)
	_, _, rawXMP, err := Extract(bytes.NewReader(buildExtendedXMPJPEG(main, testGUID, frag)))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	x, err := xmppkg.Parse(rawXMP)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := x.Get(xmppkg.NStiff, "Model"); got != "ExtendedModel" {
		t.Errorf("tiff:Model = %q, want the extended value", got)
	}
	if got := x.Get(xmppkg.NStiff, "Make"); got != "StandardMake" {
		t.Errorf("tiff:Make = %q, want the standard-only value", got)
	}
}

// extSegments returns the standard XMP packet and the extended XMP chunks,
// keyed by GUID and reassembled in offset order, of the JPEG data.
func extSegments(t *testing.T, data []byte) (main []byte, ext map[string][]byte) {
	t.Helper()
	ext = map[string][]byte{}
	parts := map[string]map[uint32][]byte{}
	for i := 2; i+4 <= len(data) && data[i] == 0xFF; {
		m := data[i+1]
		if m == markerSOS || m == markerEOI {
			break
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		seg := data[i+4 : i+2+n]
		switch {
		case m == markerAPP1 && bytes.HasPrefix(seg, identXMP):
			main = seg[len(identXMP):]
		case m == markerAPP1 && bytes.HasPrefix(seg, identXMPNote):
			body := seg[len(identXMPNote):]
			g := string(body[:32])
			if parts[g] == nil {
				parts[g] = map[uint32][]byte{}
			}
			parts[g][binary.BigEndian.Uint32(body[36:40])] = body[40:]
		}
		i += 2 + n
	}
	for g, p := range parts {
		var b []byte
		for off := uint32(0); ; {
			c, ok := p[off]
			if !ok {
				break
			}
			b = append(b, c...)
			off += uint32(len(c)) //nolint:gosec // G115: test helper
		}
		ext[g] = b
	}
	return main, ext
}

// TestTask278_BothFormsAndNestedMerge reads JPEG files whose standard packet
// writes HasExtendedXMP as a property element (the form of ExifTool's
// ExtendedXMP.jpg) or as a property attribute, in either xmpNote namespace
// URI, and whose extended XMP nests a struct inside a struct array item: the
// extended properties must be merged with the nested data intact, and a
// pass-through write must keep the extended XMP (#278, #315).
func TestTask278_BothFormsAndNestedMerge(t *testing.T) {
	t.Parallel()
	ext := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:pdf="http://ns.adobe.com/pdf/1.3/"` +
		` xmlns:Container="http://ns.google.com/photos/1.0/container/" xmlns:Item="http://ns.google.com/photos/1.0/container/item/">` +
		`<pdf:Producer>Extended producer</pdf:Producer>` +
		`<Container:Directory><rdf:Seq><rdf:li rdf:parseType="Resource"><Container:Item Item:Semantic="GainMap"/></rdf:li></rdf:Seq></Container:Directory>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta>`)
	sum := md5.Sum(ext) //nolint:gosec // G401: spec-mandated digest
	guid := strings.ToUpper(hex.EncodeToString(sum[:]))
	forms := map[string]string{
		"element":   `>` + "\n" + `  <xmpNote:HasExtendedXMP>` + guid + `</xmpNote:HasExtendedXMP>` + "\n" + ` </rdf:Description>`,
		"attribute": "\n" + `  xmpNote:HasExtendedXMP='` + guid + `'/>`,
	}
	for form, prop := range forms {
		for _, uri := range []string{xmppkg.NSxmpNote, "http://ns.adobe.com/xap/1.0/se/Note/"} {
			checkExtendedMerge(t, form, uri, prop, guid, ext)
		}
	}
}

// checkExtendedMerge runs TestTask278_BothFormsAndNestedMerge for one form
// of HasExtendedXMP (prop closes the rdf:Description) in namespace uri.
func checkExtendedMerge(t *testing.T, form, uri, prop, guid string, ext []byte) {
	t.Helper()
	ns := form + " " + uri
	main := []byte(`<?xpacket begin='` + "\xef\xbb\xbf" + `' id='W5M0MpCehiHzreSzNTczkc9d'?>` + "\n" +
		`<x:xmpmeta xmlns:x='adobe:ns:meta/'><rdf:RDF xmlns:rdf='http://www.w3.org/1999/02/22-rdf-syntax-ns#'>` + "\n" +
		` <rdf:Description rdf:about='' xmlns:xmpNote='` + uri + `'` + prop +
		`</rdf:RDF></x:xmpmeta>` + "\n" + `<?xpacket end='w'?>`)
	data := buildExtendedXMPJPEG(main, guid, ext)

	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s: Extract: %v", ns, err)
	}
	x, err := xmppkg.Parse(rawXMP)
	if err != nil {
		t.Fatalf("%s: Parse: %v", ns, err)
	}
	if got := x.Get(xmppkg.NSpdf, "Producer"); got != "Extended producer" {
		t.Errorf("%s: pdf:Producer = %q, want the extended value", ns, got)
	}
	if !bytes.Contains(rawXMP, []byte("GainMap")) || !bytes.Contains(rawXMP, []byte("Item:Semantic")) {
		t.Errorf("%s: nested extended structure lost:\n%s", ns, rawXMP)
	}
	if got := x.Get(uri, "HasExtendedXMP"); got != "" {
		t.Errorf("%s: HasExtendedXMP = %q, want it removed", ns, got)
	}

	// A pass-through write keeps the extended XMP (the element form used
	// to lose it: the GUID was not found, so no chunk was written).
	_, _, _, wire, err := ExtractWithWire(bytes.NewReader(data))
	if err != nil || wire == nil {
		t.Fatalf("%s: ExtractWithWire: wire=%v err=%v", ns, wire != nil, err)
	}
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, wire, true); err != nil {
		t.Fatalf("%s: Inject: %v", ns, err)
	}
	if _, gotExt := extSegments(t, out.Bytes()); !bytes.Equal(gotExt[guid], ext) {
		t.Errorf("%s: pass-through write changed or dropped the extended XMP", ns)
	}
}
