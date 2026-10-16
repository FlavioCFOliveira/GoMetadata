package gometadata

// write_task343_test.go — regression tests for task #343: a segment the
// caller opted out of loading (WithoutXMP, WithoutIPTC, WithoutEXIF) is
// carried unchanged by Write, also after a setter ran (CLAUDE.md §5: write
// operations preserve all existing metadata not explicitly modified).

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

const task343Packet = `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
	`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:Label="Red"/>` +
	`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`

// task343JPEG returns a JPEG that carries EXIF (Make), IPTC (keywords) and
// XMP (xmp:Label) segments.
func task343JPEG(t *testing.T) []byte {
	t.Helper()
	e := &exif.EXIF{ByteOrder: binary.LittleEndian, IFD0: &exif.IFD{}}
	e.SetMake("TestMake")
	rawEXIF, err := exif.Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	i := &iptc.IPTC{}
	i.SetKeywords([]string{"alpha", "beta"})
	rawIPTC, err := iptc.Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	seg := func(marker byte, payload []byte) []byte {
		b := []byte{0xFF, marker}
		b = binary.BigEndian.AppendUint16(b, uint16(len(payload)+2)) //nolint:gosec // G115: small test payload
		return append(b, payload...)
	}
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	buf.Write(seg(0xE1, append([]byte("Exif\x00\x00"), rawEXIF...)))
	buf.Write(seg(0xE1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), task343Packet...)))
	irbData := append([]byte("Photoshop 3.0\x00"), "8BIM\x04\x04\x00\x00"...)
	irbData = binary.BigEndian.AppendUint32(irbData, uint32(len(rawIPTC))) //nolint:gosec // G115: small test payload
	irbData = append(irbData, rawIPTC...)
	if len(rawIPTC)%2 != 0 {
		irbData = append(irbData, 0)
	}
	buf.Write(seg(0xED, irbData))
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

func task343Write(t *testing.T, src []byte, m *Metadata) *Metadata {
	t.Helper()
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

// TestTask343WithoutXMPCarriesPacket: a setter after a WithoutXMP read does not
// replace the XMP packet of the source; EXIF and IPTC still take the value.
func TestTask343WithoutXMPCarriesPacket(t *testing.T) {
	t.Parallel()
	src := task343JPEG(t)
	m, err := Read(bytes.NewReader(src), WithoutXMP())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.SetCaption("new caption")
	if m.XMP != nil {
		t.Fatalf("SetCaption created an XMP over the packet that was not loaded: %v", m.XMP.Properties)
	}
	back := task343Write(t, src, m)
	if !bytes.Equal(back.RawXMP(), []byte(task343Packet)) {
		t.Errorf("XMP packet changed:\n got %q\nwant %q", back.RawXMP(), task343Packet)
	}
	if got := back.XMP.Get(xmp.NSxmp, "Label"); got != "Red" {
		t.Errorf("xmp:Label = %q, want Red", got)
	}
	if got := back.EXIF.Caption(); got != "new caption" {
		t.Errorf("EXIF caption = %q, want the value written", got)
	}
	if got := back.IPTC.Caption(); got != "new caption" {
		t.Errorf("IPTC caption = %q, want the value written", got)
	}
}

// TestTask343ExplicitXMPReplacesPacket: assigning m.XMP replaces the packet
// that was not loaded, on purpose.
func TestTask343ExplicitXMPReplacesPacket(t *testing.T) {
	t.Parallel()
	src := task343JPEG(t)
	m, err := Read(bytes.NewReader(src), WithoutXMP())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.XMP = &xmp.XMP{Properties: map[string]map[string]string{xmp.NSxmp: {"Label": "Blue"}}}
	back := task343Write(t, src, m)
	if got := back.XMP.Get(xmp.NSxmp, "Label"); got != "Blue" {
		t.Errorf("xmp:Label = %q, want Blue", got)
	}
}

// TestTask343WithoutXMPNoPacket: a file without XMP read with WithoutXMP has
// nothing to lose, so the setter still creates the XMP.
func TestTask343WithoutXMPNoPacket(t *testing.T) {
	t.Parallel()
	src := []byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9} // SOI SOS EOI: no metadata
	m, err := Read(bytes.NewReader(src), WithoutXMP())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.SetCaption("c")
	if m.XMP == nil {
		t.Error("SetCaption did not create an XMP for a file that had none")
	}
}

// TestTask343WithoutIPTCCarriesIPTC and TestTask343WithoutEXIFCarriesEXIF: the
// same guard for the other two segments.
func TestTask343WithoutIPTCCarriesIPTC(t *testing.T) {
	t.Parallel()
	src := task343JPEG(t)
	m, err := Read(bytes.NewReader(src), WithoutIPTC())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.SetCaption("new caption")
	if m.IPTC != nil {
		t.Fatalf("SetCaption created an IPTC over the segment that was not loaded")
	}
	back := task343Write(t, src, m)
	if got := back.IPTC.Keywords(); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("IPTC keywords = %v, want [alpha beta] kept", got)
	}
	if got := back.XMP.Caption(); got != "new caption" {
		t.Errorf("XMP caption = %q, want the value written", got)
	}
}

func TestTask343WithoutEXIFCarriesEXIF(t *testing.T) {
	t.Parallel()
	src := task343JPEG(t)
	m, err := Read(bytes.NewReader(src), WithoutEXIF())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.SetCaption("new caption")
	if m.EXIF != nil {
		t.Fatalf("SetCaption created an EXIF over the segment that was not loaded")
	}
	back := task343Write(t, src, m)
	if got := back.Make(); got != "TestMake" {
		t.Errorf("Make = %q, want TestMake kept", got)
	}
	if got := back.IPTC.Caption(); got != "new caption" {
		t.Errorf("IPTC caption = %q, want the value written", got)
	}
}
