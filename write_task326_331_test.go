package gometadata

// write_task326_331_test.go — end-to-end regression tests for the XMP bugs
// of sprint 46 (#326 to #331) through Read, the setters and Write.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// buildJPEGWithXMPPacket returns a minimal JPEG whose APP1 segment carries
// packet as its XMP packet.
func buildJPEGWithXMPPacket(packet []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	payload := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), packet...)
	buf.Write([]byte{0xFF, 0xE1})
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], uint16(len(payload)+2)) //nolint:gosec // G115: small test packet
	buf.Write(lb[:])
	buf.Write(payload)
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

const task327Head = `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
	`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/">`
const task327Tail = `</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`

// TestTask327IllFormedPacketIsCarriedUnchanged requires that Write keeps an
// unparseable XMP packet byte for byte when a setter runs, still updates
// EXIF and IPTC, and that Read reports the packet (XML 1.0 §3 Element Type
// Match; checklist ROB-02).
func TestTask327IllFormedPacketIsCarriedUnchanged(t *testing.T) {
	t.Parallel()
	packet := []byte(task327Head + `<xmp:Label>kept</xmp:Label><dc:format>image/jpeg</dc:title>` + task327Tail)
	src := buildJPEGWithXMPPacket(packet)

	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.XMP != nil {
		t.Fatalf("m.XMP = %v, want nil for an ill-formed packet", m.XMP)
	}
	var warned bool
	for _, w := range m.ParseWarnings {
		if w.Segment == "XMP" {
			warned = errors.Is(w.Err, xmp.ErrXMLNotWellFormed) && strings.Contains(w.Err.Error(), "dc:title")
		}
	}
	if !warned {
		t.Errorf("ParseWarnings = %v, want an XMP warning that names dc:title", m.ParseWarnings)
	}

	m.SetCaption("new caption")
	if m.XMP != nil {
		t.Fatalf("SetCaption created an XMP over an unparseable packet: %v", m.XMP.Properties)
	}
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	back, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read of the output: %v", err)
	}
	if !bytes.Equal(back.RawXMP(), packet) {
		t.Errorf("XMP packet changed:\n got %q\nwant %q", back.RawXMP(), packet)
	}
	if got := back.Caption(); got != "new caption" {
		t.Errorf("Caption of the output = %q, want the value written to EXIF and IPTC", got)
	}
}

// TestTask327ExplicitXMPReplacesPacket checks the escape hatch: a caller that
// assigns m.XMP replaces the unparseable packet on purpose.
func TestTask327ExplicitXMPReplacesPacket(t *testing.T) {
	t.Parallel()
	src := buildJPEGWithXMPPacket([]byte(task327Head + `<dc:format>x</dc:title>` + task327Tail))
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.XMP = &xmp.XMP{Properties: map[string]map[string]string{}}
	m.XMP.SetCaption("replacement")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	back, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil || back.XMP == nil || back.XMP.Caption() != "replacement" {
		t.Fatalf("Read of the output: %v, %v", back, err)
	}
}

// TestTask326Utf16PacketWithoutBOMSurvivesWrite is the reproduction of #326
// with a synthetic file: a UTF-16BE packet without a BOM keeps every
// property after SetCaption and Write.
func TestTask326Utf16PacketWithoutBOMSurvivesWrite(t *testing.T) {
	t.Parallel()
	doc := task327Head + `<xmp:Label>kept</xmp:Label><dc:format>image/jpeg</dc:format>` + task327Tail
	var packet []byte
	for _, u := range utf16.Encode([]rune(doc)) {
		packet = binary.BigEndian.AppendUint16(packet, u)
	}
	src := buildJPEGWithXMPPacket(packet)
	m, err := Read(bytes.NewReader(src))
	if err != nil || m.XMP == nil {
		t.Fatalf("Read: %v, XMP %v", err, m.XMP)
	}
	m.SetCaption("x")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	back, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil || back.XMP == nil {
		t.Fatalf("Read of the output: %v", err)
	}
	if got := back.XMP.Get(xmp.NSxmp, "Label"); got != "kept" {
		t.Errorf("xmp:Label = %q, want kept", got)
	}
	if got := back.XMP.Get(xmp.NSdc, "format"); got != "image/jpeg" {
		t.Errorf("dc:format = %q, want image/jpeg", got)
	}
	if got := back.XMP.Caption(); got != "x" {
		t.Errorf("Caption = %q, want x", got)
	}
}

// TestTask328And330WriteKeepsValuesAndLanguages writes through the public
// API: element text keeps its white space, and SetCaption keeps the other
// languages of dc:description.
func TestTask328And330WriteKeepsValuesAndLanguages(t *testing.T) {
	t.Parallel()
	packet := []byte(task327Head + `<xmp:Label>  two  spaces  </xmp:Label>` +
		`<dc:description><rdf:Alt><rdf:li xml:lang="x-default">Hello</rdf:li><rdf:li xml:lang="fr">Bonjour</rdf:li></rdf:Alt></dc:description>` + task327Tail)
	src := buildJPEGWithXMPPacket(packet)
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.SetCaption("Hi")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	back, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil || back.XMP == nil {
		t.Fatalf("Read of the output: %v", err)
	}
	if got := back.XMP.Get(xmp.NSxmp, "Label"); got != "  two  spaces  " {
		t.Errorf("xmp:Label = %q, want the white space kept", got)
	}
	if got := back.XMP.Get(xmp.NSdc, "description"); got != "Hi\x1efr|Bonjour" {
		t.Errorf("dc:description = %q, want %q", got, "Hi\x1efr|Bonjour")
	}
}

// TestTask329WriteLeavesNoStaleAlias writes through the public API: after
// SetCaption and SetCopyright no alias property keeps the old value.
func TestTask329WriteLeavesNoStaleAlias(t *testing.T) {
	t.Parallel()
	packet := []byte(strings.Replace(task327Head, `xmlns:xmp=`, `xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" xmlns:xmp=`, 1) +
		`<photoshop:Caption>old</photoshop:Caption>` +
		`<tiff:Copyright><rdf:Alt><rdf:li xml:lang="x-default">old</rdf:li></rdf:Alt></tiff:Copyright>` + task327Tail)
	src := buildJPEGWithXMPPacket(packet)
	m, err := Read(bytes.NewReader(src))
	if err != nil || m.XMP == nil {
		t.Fatalf("Read: %v", err)
	}
	if got := m.XMP.Caption(); got != "old" {
		t.Fatalf("alias-only Caption = %q, want old", got)
	}
	m.SetCaption("new")
	m.SetCopyright("newr")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	back, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil || back.XMP == nil {
		t.Fatalf("Read of the output: %v", err)
	}
	if raw := string(back.RawXMP()); strings.Contains(raw, "old") || strings.Contains(raw, "photoshop:Caption") || strings.Contains(raw, "tiff:Copyright") {
		t.Errorf("stale alias in the written packet:\n%s", raw)
	}
}

// corpusXMPProperties returns the number of flat XMP properties of the
// corpus image at rel and its metadata, or skips when the corpus is absent.
func corpusXMPProperties(t *testing.T, rel string) (string, *Metadata, int) {
	t.Helper()
	path := requireCorpusFile(t, rel)
	m, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	n := 0
	if m.XMP != nil {
		for _, props := range m.XMP.Properties {
			n += len(props)
		}
	}
	return path, m, n
}

// writeAndReread sets a caption, writes the image of path to memory and
// reads it back.
func writeAndReread(t *testing.T, path string, m *Metadata) *Metadata {
	t.Helper()
	src, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m.SetCaption("x")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	back, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read of the output: %v", err)
	}
	return back
}

// TestTask326CorpusUTF16WithoutBOM covers ExifTool's XMP.jpg, whose packet
// starts 00 3C 00 3F (UTF-16BE, no BOM).
func TestTask326CorpusUTF16WithoutBOM(t *testing.T) {
	t.Parallel()
	path, m, before := corpusXMPProperties(t, "jpeg/exiftool/XMP.jpg")
	if before < 20 {
		t.Fatalf("XMP.jpg: %d properties read, want the 25 tags of the packet (UTF-16BE without BOM)", before)
	}
	back := writeAndReread(t, path, m)
	after := 0
	for _, props := range back.XMP.Properties {
		after += len(props)
	}
	if after < before-1 { // dc:description may be added or replaced by SetCaption
		t.Errorf("XMP.jpg: %d properties after Write, want about %d", after, before)
	}
}

// TestTask327CorpusExiv2Bug1026 covers the corpus file whose packet holds
// the ill-formed element <exif:TIFF/EPStandardID>.
func TestTask327CorpusExiv2Bug1026(t *testing.T) {
	t.Parallel()
	path, m, _ := corpusXMPProperties(t, "jpeg/exiv2/exiv2-bug1026.jpg")
	if m.XMP != nil || len(m.RawXMP()) == 0 {
		t.Fatalf("XMP = %v, raw %d bytes; want an unparseable packet kept raw", m.XMP, len(m.RawXMP()))
	}
	want := m.RawXMP()
	back := writeAndReread(t, path, m)
	if !bytes.Equal(back.RawXMP(), want) {
		t.Errorf("the unparseable packet changed by Write (%d bytes before, %d after)", len(want), len(back.RawXMP()))
	}
}

// TestTask328CorpusWhitespaceKept covers photoshop:Headline "Enduring Freedom\n".
func TestTask328CorpusWhitespaceKept(t *testing.T) {
	t.Parallel()
	path, m, _ := corpusXMPProperties(t, "jpeg/exif-samples/long_description_2.jpg")
	const want = "Enduring Freedom\n"
	if got := m.XMP.Get(xmp.NSphotoshop, "Headline"); got != want {
		t.Fatalf("Headline = %q, want %q", got, want)
	}
	back := writeAndReread(t, path, m)
	if got := back.XMP.Get(xmp.NSphotoshop, "Headline"); got != want {
		t.Errorf("Headline after Write = %q, want %q", got, want)
	}
}
