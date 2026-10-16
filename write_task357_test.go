package gometadata

// write_task357_test.go — task #357: a JPEG whose extended XMP could not be
// merged keeps its xmp/extension/ segments, byte for byte, when a setter makes
// Write re-encode the standard packet, and the packet links to them
// (xmpNote:HasExtendedXMP, Adobe XMP Specification Part 3 §1.1.3.1, pp. 14-15).
// Policy (original: the Adobe Toolkit drops such segments and ExifTool raises
// an error): keep verbatim, never drop silently; a packet that needs extended
// XMP of its own next to the kept segments is refused.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/jpeg"
)

const t357GUID = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// t357Seg builds an APP1 segment with payload.
func t357Seg(payload []byte) []byte {
	b := []byte{0xFF, 0xE1}
	b = binary.BigEndian.AppendUint16(b, uint16(len(payload)+2)) //nolint:gosec // G115: small test payload
	return append(b, payload...)
}

// t357ExtSeg builds one extended XMP segment: signature, GUID, the declared
// full length, the offset of the chunk and the chunk.
func t357ExtSeg(fullLen, offset uint32, chunk []byte) []byte {
	p := []byte("http://ns.adobe.com/xmp/extension/\x00" + t357GUID)
	p = binary.BigEndian.AppendUint32(p, fullLen)
	p = binary.BigEndian.AppendUint32(p, offset)
	return t357Seg(append(p, chunk...))
}

// t357Main is a standard XMP packet that links to the extended XMP t357GUID.
func t357Main() string {
	return `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/" xmpNote:HasExtendedXMP="` + t357GUID + `"/></rdf:RDF></x:xmpmeta>`
}

// t357JPEG assembles SOI, the standard packet, a COM segment, the extended
// segments, and an empty scan.
func t357JPEG(main string, ext ...[]byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8})
	buf.Write(t357Seg(append([]byte("http://ns.adobe.com/xap/1.0/\x00"), main...)))
	buf.Write([]byte{0xFF, 0xFE, 0x00, 0x06, 'a', 'b', 'c', 'd'}) // a comment between the packet and the extension
	for _, e := range ext {
		buf.Write(e)
	}
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// t357Fixtures returns the unmergeable cases: the ill-formed extended document
// in one chunk (the fixture of #350) and in two, and chunks with a gap.
func t357Fixtures() map[string][]byte {
	doc := []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"><rdf:RDF><rdf:Description>") // ill-formed
	half := len(doc) / 2
	return map[string][]byte{
		"ill-formed document, one chunk": t357JPEG(t357Main(),
			t357ExtSeg(uint32(len(doc)), 0, doc)), //nolint:gosec // G115: small test payload
		"ill-formed document, two chunks": t357JPEG(t357Main(),
			t357ExtSeg(uint32(len(doc)), 0, doc[:half]),             //nolint:gosec // G115: small test payload
			t357ExtSeg(uint32(len(doc)), uint32(half), doc[half:])), //nolint:gosec // G115: small test payload
		"gap between the chunks": t357JPEG(t357Main(),
			t357ExtSeg(100, 0, doc[:10]),
			t357ExtSeg(100, 40, doc[10:20])),
	}
}

// keptGUID returns the GUID of the extended XMP that m keeps, or "".
func keptGUID(m *Metadata) string {
	if m.keptExtGUID == nil {
		return ""
	}
	return *m.keptExtGUID
}

// t357ExtSegments returns the extended XMP segments of a JPEG, whole (marker,
// length and payload), in file order.
func t357ExtSegments(t *testing.T, data []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for pos := 2; pos+4 <= len(data) && data[pos] == 0xFF && data[pos+1] != 0xDA; {
		n := int(binary.BigEndian.Uint16(data[pos+2:]))
		seg := data[pos : pos+2+n]
		if data[pos+1] == 0xE1 && bytes.HasPrefix(seg[4:], []byte("http://ns.adobe.com/xmp/extension/\x00")) {
			out = append(out, seg)
		}
		pos += 2 + n
	}
	return out
}

// t357StandardPacket returns the standard XMP packet of a JPEG.
func t357StandardPacket(t *testing.T, data []byte) string {
	t.Helper()
	const sig = "http://ns.adobe.com/xap/1.0/\x00"
	i := bytes.Index(data, []byte(sig))
	if i < 0 {
		t.Fatal("no standard XMP packet")
	}
	n := int(binary.BigEndian.Uint16(data[i-2:]))
	return string(data[i+len(sig) : i-2+n])
}

// TestTask357SetterWriteKeepsUnmergedExtendedXMP is the #357 acceptance test:
// Read, SetCaption and Write keep every source extended segment byte for byte
// and in order, write the caption, link the standard packet to the segments,
// and give a file that reads the same way again (so a second write keeps them
// too). XMP-EXT-KEEP-01.
func TestTask357SetterWriteKeepsUnmergedExtendedXMP(t *testing.T) {
	t.Parallel()
	for name, src := range t357Fixtures() {
		t.Run("XMP-EXT-KEEP-01/"+name, func(t *testing.T) {
			t.Parallel()
			want := t357ExtSegments(t, src)
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got := keptGUID(m); got != t357GUID {
				t.Fatalf("keptExtGUID = %q, want %q", got, t357GUID)
			}
			// Two rounds: the second starts from the output of the first.
			cur := src
			for round, caption := range []string{"first caption", "second caption"} {
				m.SetCaption(caption)
				var out bytes.Buffer
				if err := Write(bytes.NewReader(cur), &out, m); err != nil {
					t.Fatalf("round %d: Write: %v", round, err)
				}
				got := t357ExtSegments(t, out.Bytes())
				if len(got) != len(want) {
					t.Fatalf("round %d: %d extended segments in the output, want %d", round, len(got), len(want))
				}
				for i := range want {
					if !bytes.Equal(got[i], want[i]) {
						t.Errorf("round %d: extended segment %d changed", round, i)
					}
				}
				if p := t357StandardPacket(t, out.Bytes()); !strings.Contains(p, "HasExtendedXMP") || !strings.Contains(p, t357GUID) {
					t.Errorf("round %d: the standard packet does not link to the extended XMP: %s", round, p)
				}
				m2, err := Read(bytes.NewReader(out.Bytes()))
				if err != nil {
					t.Fatalf("round %d: Read(output): %v", round, err)
				}
				if got := m2.Caption(); got != caption {
					t.Errorf("round %d: caption = %q, want %q", round, got, caption)
				}
				if got := keptGUID(m2); got != t357GUID {
					t.Errorf("round %d: the output does not read as having kept extended XMP (%q)", round, got)
				}
				cur, m = out.Bytes(), m2
			}
		})
	}
}

// TestTask357NoDanglingLink: metadata read from a file with unmerged extended
// XMP and written into a file that has none must not link to extended XMP that
// is not there. XMP-EXT-KEEP-02.
func TestTask357NoDanglingLink(t *testing.T) {
	t.Parallel()
	src := t357Fixtures()["ill-formed document, one chunk"]
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	m.SetCaption("c")
	plain := []byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}
	var out bytes.Buffer
	if err := Write(bytes.NewReader(plain), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if bytes.Contains(out.Bytes(), []byte("HasExtendedXMP")) || len(t357ExtSegments(t, out.Bytes())) != 0 {
		t.Error("the output links to, or carries, extended XMP that the target did not have")
	}
	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil || m2.Caption() != "c" {
		t.Errorf("Read(output) = %v, %v", m2, err)
	}
}

// TestTask357MergedExtendedXMPIsNotKept: extended XMP that Read merged is part
// of the model, so a setter write re-encodes it and drops the segments, as
// before. XMP-EXT-KEEP-03.
func TestTask357MergedExtendedXMPIsNotKept(t *testing.T) {
	t.Parallel()
	doc := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:Label="Red"/></rdf:RDF></x:xmpmeta>`)
	src := t357JPEG(t357Main(), t357ExtSeg(uint32(len(doc)), 0, doc)) //nolint:gosec // G115: small test payload
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := keptGUID(m); got != "" {
		t.Fatalf("keptExtGUID = %q for extended XMP that merges", got)
	}
	m.SetCaption("c")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatal(err)
	}
	if n := len(t357ExtSegments(t, out.Bytes())); n != 0 {
		t.Errorf("%d extended segments in the output, want none: the merged properties are in the standard packet", n)
	}
	if m2, err := Read(bytes.NewReader(out.Bytes())); err != nil || m2.Caption() != "c" || !strings.Contains(string(m2.rawXMP), "Red") {
		t.Errorf("the merged property was lost: %v", err)
	}
}

// TestTask357OversizedPacketIsRefused: a packet that needs extended XMP of its
// own cannot be written next to the kept segments (one ExtendedXMP per file,
// Part 3 §1.1.3.1, p. 14); Write reports it and writes nothing. XMP-EXT-KEEP-04.
func TestTask357OversizedPacketIsRefused(t *testing.T) {
	t.Parallel()
	src := t357Fixtures()["ill-formed document, one chunk"]
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	m.SetCaption(strings.Repeat("x", 70000))
	var out bytes.Buffer
	err = Write(bytes.NewReader(src), &out, m)
	if !errors.Is(err, jpeg.ErrExtendedXMPConflict) {
		t.Fatalf("Write error = %v, want jpeg.ErrExtendedXMPConflict", err)
	}
	if out.Len() != 0 {
		t.Errorf("a refused write produced %d bytes", out.Len())
	}
}
