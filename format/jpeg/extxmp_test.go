package jpeg

// extxmp_test.go — regression battery for extended XMP in JPEG (tasks #278,
// #295, #315; Adobe XMP Specification Part 3 §1.1.3.1).

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

// TestExtendedSerialization proves that the packet wrapper, its padding and
// the white space around the document are removed (Part 3 §1.1.3.1: the
// extended portion is serialised without a packet wrapper).
func TestExtendedSerialization(t *testing.T) {
	t.Parallel()
	const doc = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF/></x:xmpmeta>`
	tests := []struct{ name, in, want string }{
		{"wrapped and padded", `<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` + "\n" + doc + "\n" + strings.Repeat(" ", 2048) + "\n" + `<?xpacket end="w"?>`, doc},
		{"leading white space", "\n " + `<?xpacket begin='' id='x'?>` + doc + `<?xpacket end='r'?>`, doc},
		{"no wrapper", "  " + doc + "\n", doc},
		{"wrapper not first", doc + `<?xpacket begin="" id=""?>`, doc + `<?xpacket begin="" id=""?>`},
		{"unterminated wrapper", `<?xpacket begin=` + doc, `<?xpacket begin=` + doc},
	}
	for _, tc := range tests {
		if got := string(extendedSerialization([]byte(tc.in))); got != tc.want {
			t.Errorf("%s: extendedSerialization = %q, want %q", tc.name, got, tc.want)
		}
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

// largeXMPPacket returns an XMP packet larger than one APP1 segment, with a
// nested structure.
func largeXMPPacket(t *testing.T) []byte {
	t.Helper()
	x, err := xmppkg.Parse([]byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:GDepth="http://ns.google.com/photos/1.0/depthmap/"` +
		` xmlns:Container="http://ns.google.com/photos/1.0/container/" xmlns:Item="http://ns.google.com/photos/1.0/container/item/">` +
		`<GDepth:Data>` + strings.Repeat("QUJD", 40000) + `</GDepth:Data>` +
		`<Container:Directory><rdf:Seq><rdf:li rdf:parseType="Resource"><Container:Item Item:Semantic="Primary"/></rdf:li></rdf:Seq></Container:Directory>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta>`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	x.SetCaption("caption")
	raw, err := xmppkg.Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(raw) <= maxXMPPayload {
		t.Fatalf("precondition: packet of %d bytes fits one segment", len(raw))
	}
	return raw
}

// TestTask295_ExtendedXMPWriteDeterministic proves that writing the same
// oversized XMP twice gives identical bytes, that the GUID is the uppercase
// MD5 digest of the extended serialisation, and that the written file reads
// back with the nested structure and without HasExtendedXMP. The wrapper and
// the namespace rules are checked by TestConformanceExtendedXMPWrite; the
// partitioning by TestTask332_*.
func TestTask295_ExtendedXMPWriteDeterministic(t *testing.T) {
	t.Parallel()
	raw := largeXMPPacket(t)
	src := buildJPEG(nil, nil, nil)
	var out1, out2 bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out1, nil, nil, raw, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if err := Inject(bytes.NewReader(src), &out2, nil, nil, raw, true); err != nil {
		t.Fatalf("second Inject: %v", err)
	}
	if !bytes.Equal(out1.Bytes(), out2.Bytes()) {
		t.Fatal("two writes of the same XMP differ")
	}

	_, ext := extSegments(t, out1.Bytes())
	if len(ext) != 1 {
		t.Fatalf("extended XMP carries %d GUIDs, want 1", len(ext))
	}
	for guid, body := range ext {
		if sum := md5.Sum(body); guid != strings.ToUpper(hex.EncodeToString(sum[:])) { //nolint:gosec // G401: spec-mandated digest
			t.Errorf("GUID %s is not the uppercase MD5 of the extended serialisation", guid)
		}
	}

	_, _, got, err := Extract(bytes.NewReader(out1.Bytes()))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{"Item:Semantic", "Primary", "QUJDQUJD", "caption"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("reassembled XMP lacks %q", want)
		}
	}
	if bytes.Contains(got, []byte("HasExtendedXMP")) {
		t.Error("reassembled XMP still carries HasExtendedXMP")
	}
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

// TestConformanceExtendedXMPWrite checks the extended XMP rules of
// docs/conformance/xmp.md §3.1 on a written file (Adobe XMP Specification
// Part 3 §1.1.3.1).
func TestConformanceExtendedXMPWrite(t *testing.T) {
	t.Parallel()
	raw := largeXMPPacket(t)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(buildJPEG(nil, nil, nil)), &out, nil, nil, raw, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	main, ext := extSegments(t, out.Bytes())
	if len(ext) != 1 {
		t.Fatalf("extended XMP carries %d GUIDs, want 1", len(ext))
	}
	var guid string
	var body []byte
	for g, b := range ext {
		guid, body = g, b
	}

	t.Run("JPEG-06", func(t *testing.T) {
		t.Parallel()
		sum := md5.Sum(body) //nolint:gosec // G401: spec-mandated digest
		if want := strings.ToUpper(hex.EncodeToString(sum[:])); guid != want {
			t.Errorf("GUID = %s, want the MD5 digest of the extended XMP %s", guid, want)
		}
		if g, ok := extractGUIDFromMain(main); !ok || g != guid {
			t.Errorf("xmpNote:HasExtendedXMP = %q, %v; want %s", g, ok, guid)
		}
	})
	t.Run("JPEG-09", func(t *testing.T) {
		t.Parallel()
		if bytes.Contains(body, []byte("<?xpacket")) || !bytes.HasPrefix(body, []byte("<x:xmpmeta")) {
			t.Errorf("extended XMP is not serialized without a packet wrapper: %.80q", body)
		}
		if strings.ToUpper(guid) != guid {
			t.Errorf("GUID %s is not written with capital A-F", guid)
		}
	})
	t.Run("JPEG-10", func(t *testing.T) {
		t.Parallel()
		if !bytes.Contains(main, []byte(`xmlns:xmpNote="http://ns.adobe.com/xmp/note/"`)) {
			t.Errorf("standard packet does not bind xmpNote to http://ns.adobe.com/xmp/note/:\n%s", main)
		}
		_, _, got, err := Extract(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		x, err := xmppkg.Parse(got)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if v := x.Get(xmppkg.NSxmpNote, "HasExtendedXMP"); v != "" {
			t.Errorf("recomposed XMP keeps xmpNote:HasExtendedXMP = %q", v)
		}
		if x.Caption() != "caption" {
			t.Errorf("recomposed XMP lost the extended properties")
		}
	})
}

// TestTask332_FallbackExtendedWrite: an oversized packet that cannot be parsed
// is not partitioned; it is stored whole as extended XMP behind a stub
// standard packet, and the extended serialisation is the packet without its
// wrapper (writeExtendedXMP).
func TestTask332_FallbackExtendedWrite(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-13", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="">` + strings.Repeat("junk ", 20000) + `</rdf:Wrong></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)
		if _, err := xmppkg.Parse(raw); err == nil {
			t.Fatal("precondition: the packet parses")
		}
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(buildJPEG(nil, nil, nil)), &out, nil, nil, raw, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		main, ext := extSegments(t, out.Bytes())
		if len(ext) != 1 || !bytes.Contains(main, []byte("HasExtendedXMP")) {
			t.Fatalf("fallback layout: %d extended GUIDs, main %q", len(ext), main)
		}
		for _, body := range ext {
			if !bytes.Equal(body, extendedSerialization(raw)) {
				t.Error("extended serialisation is not the written packet without its wrapper")
			}
		}
	})
}
