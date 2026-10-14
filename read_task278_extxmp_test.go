package gometadata

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: Adobe XMP Specification Part 3 §1.1.3.1 mandates MD5 for the extended XMP GUID
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// buildJPEGWithOverlappingExtendedXMP returns a JPEG whose standard and
// extended XMP both carry tiff:Model (simple) and Container:Directory (a
// struct inside a struct array item), with different values. The standard
// packet also carries tiff:Make, which the extended XMP does not.
func buildJPEGWithOverlappingExtendedXMP() []byte {
	const identXMP = "http://ns.adobe.com/xap/1.0/\x00"
	const identExt = "http://ns.adobe.com/xmp/extension/\x00"
	const decl = ` xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
		` xmlns:Container="http://ns.google.com/photos/1.0/container/"` +
		` xmlns:Item="http://ns.google.com/photos/1.0/container/item/"`
	ext := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` + decl + ` tiff:Model="ExtendedModel">` +
		`<Container:Directory><rdf:Seq><rdf:li rdf:parseType="Resource">` +
		`<Container:Item Item:Semantic="ExtendedGainMap" Item:Length="52666"/>` +
		`</rdf:li></rdf:Seq></Container:Directory>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta>`
	sum := md5.Sum([]byte(ext)) //nolint:gosec // G401: spec-mandated digest
	guid := strings.ToUpper(hex.EncodeToString(sum[:]))
	main := `<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/"` + decl +
		` xmpNote:HasExtendedXMP="` + guid + `" tiff:Model="StandardModel" tiff:Make="StandardMake">` +
		`<Container:Directory><rdf:Seq><rdf:li rdf:parseType="Resource">` +
		`<Container:Item Item:Semantic="StandardPrimary" Item:Padding="9"/>` +
		`</rdf:li></rdf:Seq></Container:Directory>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`

	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	writeAPP1(&buf, append([]byte(identXMP), main...))
	body := append([]byte(identExt), guid...)
	body = binary.BigEndian.AppendUint32(body, uint32(len(ext))) //nolint:gosec // G115: test fixture of a few hundred bytes
	body = binary.BigEndian.AppendUint32(body, 0)
	writeAPP1(&buf, append(body, ext...))
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// assertExtendedWins checks the overlap policy on a parsed XMP: the extended
// value of tiff:Model and Container:Directory, the standard-only tiff:Make.
func assertExtendedWins(t *testing.T, step string, m *Metadata) {
	t.Helper()
	if m.XMP == nil {
		t.Fatalf("%s: no XMP", step)
	}
	if got := m.XMP.Get(xmp.NStiff, "Model"); got != "ExtendedModel" {
		t.Errorf("%s: tiff:Model = %q, want the extended value", step, got)
	}
	if got := m.XMP.Get(xmp.NStiff, "Make"); got != "StandardMake" {
		t.Errorf("%s: tiff:Make = %q, want the standard-only value", step, got)
	}
	raw := m.RawXMP()
	for _, want := range []string{"ExtendedGainMap", "52666"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("%s: XMP lacks the extended nested value %q", step, want)
		}
	}
	for _, gone := range []string{"StandardPrimary", "Padding", "StandardModel", "HasExtendedXMP"} {
		if bytes.Contains(raw, []byte(gone)) {
			t.Errorf("%s: XMP keeps %q", step, gone)
		}
	}
}

// TestTask278_ExtendedValueWinsAndIsWrittenBack pins the overlap policy of
// the Adobe XMP Toolkit (XMPUtils::MergeFromJPEG): a simple and a nested
// top-level property present in both the standard and the extended XMP of a
// JPEG take the extended value as a whole on Read, and a later Write writes
// that value back unchanged.
func TestTask278_ExtendedValueWinsAndIsWrittenBack(t *testing.T) {
	t.Parallel()
	src := buildJPEGWithOverlappingExtendedXMP()
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	assertExtendedWins(t, "read", m)
	before, err := xmp.Encode(m.XMP)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read written file: %v", err)
	}
	assertExtendedWins(t, "after write", m2)
	after, err := xmp.Encode(m2.XMP)
	if err != nil {
		t.Fatalf("Encode after write: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("Write changed the XMP:\n%s\n---\n%s", before, after)
	}
}
