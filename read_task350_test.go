package gometadata

// read_task350_test.go — regression test for task #350: an extended XMP chunk
// that matches the GUID of the standard packet but cannot be merged leaves a
// packet without xmpNote:HasExtendedXMP and a ParseWarning (Adobe XMP
// Specification Part 3 §1.1.3.1, p. 15; Toolkit JPEG_Handler.cpp:ProcessXMP).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/jpeg"
)

func TestTask350UnmergedExtendedXMPWarns(t *testing.T) {
	t.Parallel()
	const guid = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	main := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/" xmpNote:HasExtendedXMP="` + guid + `"/></rdf:RDF></x:xmpmeta>`
	ext := []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"><rdf:RDF><rdf:Description>") // ill-formed
	seg := func(payload []byte) []byte {
		b := []byte{0xFF, 0xE1}
		b = binary.BigEndian.AppendUint16(b, uint16(len(payload)+2)) //nolint:gosec // G115: small test payload
		return append(b, payload...)
	}
	extPayload := []byte("http://ns.adobe.com/xmp/extension/\x00" + guid)
	extPayload = binary.BigEndian.AppendUint32(extPayload, uint32(len(ext))) //nolint:gosec // G115: small test payload
	extPayload = binary.BigEndian.AppendUint32(extPayload, 0)
	extPayload = append(extPayload, ext...)
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	buf.Write(seg(append([]byte("http://ns.adobe.com/xap/1.0/\x00"), main...)))
	buf.Write(seg(extPayload))
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})

	m, err := Read(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if bytes.Contains(m.rawXMP, []byte("HasExtendedXMP")) {
		t.Error("the packet still links to the extended XMP that was not merged")
	}
	var found bool
	for _, w := range m.ParseWarnings {
		if errors.Is(w.Err, jpeg.ErrExtendedXMPTruncated) {
			found = true
		}
	}
	if !found {
		t.Errorf("no ParseWarning for the unmerged extended XMP: %v", m.ParseWarnings)
	}
}
