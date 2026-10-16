package jpeg

import (
	"bytes"
	"strings"
	"testing"
)

// TestTask315_XMPNoteNamespaceURI proves that the standard packet written for
// extended XMP binds xmpNote to http://ns.adobe.com/xmp/note/ (Adobe XMP
// Specification Part 3 §1.1.3.1; XMP Toolkit kXMP_NS_XMP_Note), never to the
// URI that earlier releases wrote, and that files using either URI read back.
func TestTask315_XMPNoteNamespaceURI(t *testing.T) {
	t.Parallel()
	rawXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:format>` + strings.Repeat("x", 70000) + `</dc:format>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(buildJPEG(nil, nil, nil)), &out, nil, nil, rawXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`xmlns:xmpNote="http://ns.adobe.com/xmp/note/"`)) {
		t.Error("written standard packet does not bind xmpNote to http://ns.adobe.com/xmp/note/")
	}
	if bytes.Contains(out.Bytes(), []byte("http://ns.adobe.com/xap/1.0/se/Note/")) {
		t.Error("written standard packet uses the legacy xmpNote URI")
	}

	const guid = "00112233445566778899AABBCCDDEEFF"
	ext := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:format>image/jpeg</dc:format>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta>`)
	for _, uri := range []string{"http://ns.adobe.com/xmp/note/", "http://ns.adobe.com/xap/1.0/se/Note/"} {
		main := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:xmpNote="` + uri + `" xmpNote:HasExtendedXMP="` + guid + `"/>` +
			`</rdf:RDF></x:xmpmeta>`)
		_, _, got, err := Extract(bytes.NewReader(buildExtendedXMPJPEG(main, guid, ext)))
		if err != nil {
			t.Fatalf("%s: Extract: %v", uri, err)
		}
		if !bytes.Contains(got, []byte("image/jpeg")) {
			t.Errorf("%s: extended XMP not read:\n%s", uri, got)
		}
	}
}
