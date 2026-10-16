package jpeg

// task332_test.go — regression tests for task #332: the XMP of a JPEG is
// partitioned as Adobe XMP Specification Part 3 §1.1.3.2 (p. 15) describes,
// and the link property xmpNote:HasExtendedXMP is removed on read when no
// extended chunk matches (§1.1.3.1, p. 15; Toolkit JPEG_Handler.cpp
// ProcessXMP).

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: the spec mandates MD5 for the extended XMP GUID
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	xmppkg "github.com/FlavioCFOliveira/GoMetadata/xmp"
)

const task332Head = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:CreatorTool="Tool" xmp:Rating="4">` +
	`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">Partition title</rdf:li></rdf:Alt></dc:title>` +
	`<dc:creator><rdf:Seq><rdf:li>Ann</rdf:li></rdf:Seq></dc:creator></rdf:Description>`

func task332Prop(ns, name string, n int) string {
	return fmt.Sprintf(`<rdf:Description rdf:about="" xmlns:p=%q><p:%s>%s</p:%s></rdf:Description>`, ns, name, strings.Repeat("x", n), name)
}

func task332XMP(t *testing.T, props ...string) *xmppkg.XMP {
	t.Helper()
	x, err := xmppkg.Parse([]byte(task332Head + strings.Join(props, "") + `</rdf:RDF></x:xmpmeta>`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return x
}

// app1XMPSegments returns the payload sizes of the standard XMP APP1 segments
// (after the identifier) of the JPEG data.
func app1XMPSegments(data []byte) (sizes []int) {
	for i := 2; i+4 <= len(data) && data[i] == 0xFF; {
		m := data[i+1]
		if m == markerSOS || m == markerEOI {
			break
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if m == markerAPP1 && bytes.HasPrefix(data[i+4:], identXMP) {
			sizes = append(sizes, n-2-len(identXMP))
		}
		i += 2 + n
	}
	return sizes
}

// TestTask332_CompactNotSplit (JPEG-XMP-PART-01): an XMP whose compact
// serialisation is 64000 bytes (65500+ with the packet padding) is written as
// one standard packet, without padding, and no extended segment.
func TestTask332_CompactNotSplit(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-11", func(t *testing.T) {
		t.Parallel()
		// Grow one property until the compact form is 64000 bytes.
		var raw []byte
		for n := 63000; ; {
			x := task332XMP(t, task332Prop("http://example.com/p/", "Big", n))
			padded, err := xmppkg.Encode(x)
			if err != nil {
				t.Fatal(err)
			}
			head, tail := trimPacketPadding(padded)
			size := len(head) + len(tail)
			if size == 64000 {
				raw = padded
				break
			}
			n += 64000 - size
			if n < 0 || n > 70000 {
				t.Fatalf("cannot build the fixture (n=%d)", n)
			}
		}
		if len(raw) <= maxXMPPayload {
			t.Fatalf("fixture is %d bytes padded, want more than %d", len(raw), maxXMPPayload)
		}
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(buildJPEG(nil, nil, nil)), &out, nil, nil, raw, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		main, ext := extSegments(t, out.Bytes())
		if len(ext) != 0 {
			t.Fatalf("a 64000-byte compact XMP was split into %d extended GUIDs", len(ext))
		}
		if sizes := app1XMPSegments(out.Bytes()); len(sizes) != 1 || sizes[0] != 64000 {
			t.Errorf("standard XMP segments %v, want one of 64000 bytes", sizes)
		}
		if bytes.Contains(main, []byte("HasExtendedXMP")) || bytes.Contains(main, []byte(strings.Repeat(" ", 200))) {
			t.Error("the standard packet has a link or padding")
		}
		_, _, got, err := Extract(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		gx, err := xmppkg.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		if gx.Get("http://example.com/p/", "Big") != strings.Repeat("x", len(gx.Get("http://example.com/p/", "Big"))) || gx.Get("http://example.com/p/", "Big") == "" {
			t.Error("the property changed")
		}
	})
}

// TestTask332_PartitionKeepsStandardScalars (JPEG-XMP-PART-02): a 200 KB XMP
// keeps the dc and xmp properties in the standard packet, moves the largest
// properties to the extended XMP, and reads back whole.
func TestTask332_PartitionKeepsStandardScalars(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-12", func(t *testing.T) {
		t.Parallel()
		x := task332XMP(t,
			task332Prop("http://example.com/big/", "Huge", 140000),
			task332Prop("http://example.com/mid/", "Mid", 50000),
			task332Prop("http://example.com/small/", "Small", 1000),
		)
		raw, err := xmppkg.Encode(x)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(buildJPEG(nil, nil, nil)), &out, nil, nil, raw, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		main, ext := extSegments(t, out.Bytes())
		if len(ext) != 1 {
			t.Fatalf("%d extended GUIDs, want 1", len(ext))
		}
		std, err := xmppkg.Parse(main)
		if err != nil {
			t.Fatalf("Parse standard: %v", err)
		}
		for guid, body := range ext {
			if sum := md5.Sum(body); guid != strings.ToUpper(hex.EncodeToString(sum[:])) { //nolint:gosec // G401: spec-mandated digest
				t.Errorf("GUID %s is not the MD5 of the extended serialisation", guid)
			}
			if std.Get(xmppkg.NSxmpNote, "HasExtendedXMP") != guid {
				t.Errorf("HasExtendedXMP = %q, want %s", std.Get(xmppkg.NSxmpNote, "HasExtendedXMP"), guid)
			}
			if bytes.Contains(body, []byte("<?xpacket")) {
				t.Error("the extended XMP has a packet wrapper")
			}
		}
		if len(main) > 65000 {
			t.Errorf("standard packet is %d bytes, want at most 65000", len(main))
		}
		if std.Get(xmppkg.NSdc, "title") != "Partition title" || std.Creator() != "Ann" ||
			std.Get(xmppkg.NSxmp, "CreatorTool") != "Tool" || std.Get(xmppkg.NSxmp, "Rating") != "4" {
			t.Errorf("dc/xmp properties are not in the standard packet: %v", std.Properties)
		}
		if std.Get("http://example.com/small/", "Small") == "" || std.Get("http://example.com/big/", "Huge") != "" {
			t.Error("the partition moved the wrong properties")
		}

		_, _, got, err := Extract(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		gx, err := xmppkg.Parse(got)
		if err != nil {
			t.Fatalf("Parse reassembled: %v", err)
		}
		if got := gx.Get("http://example.com/big/", "Huge"); len(got) != 140000 {
			t.Errorf("the large property has %d bytes after the round trip, want 140000", len(got))
		}
		if gx.Get("http://example.com/mid/", "Mid") == "" || gx.Get(xmppkg.NSdc, "title") != "Partition title" {
			t.Error("properties lost in the round trip")
		}
		if gx.Get(xmppkg.NSxmpNote, "HasExtendedXMP") != "" {
			t.Error("the reassembled XMP still has HasExtendedXMP")
		}
	})
}

// TestTask332_PaddedPacketThatFitsUnpadded (JPEG-XMP-PART-03): a packet
// larger than one segment only because of its padding is written whole, with
// the padding removed (§1.1.3.2, p. 15).
func TestTask332_PaddedPacketThatFitsUnpadded(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-11", func(t *testing.T) {
		t.Parallel()
		x := task332XMP(t, task332Prop("http://example.com/p/", "Big", 63500))
		raw, err := xmppkg.Encode(x)
		if err != nil {
			t.Fatal(err)
		}
		if head, tail := trimPacketPadding(raw); len(raw) <= maxXMPPayload || len(head)+len(tail) > maxXMPPayload {
			t.Fatalf("fixture: %d bytes padded, %d unpadded", len(raw), len(head)+len(tail))
		}
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(buildJPEG(nil, nil, nil)), &out, nil, nil, raw, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		if _, ext := extSegments(t, out.Bytes()); len(ext) != 0 {
			t.Fatal("split although the unpadded packet fits one segment")
		}
		sizes := app1XMPSegments(out.Bytes())
		if len(sizes) != 1 || sizes[0] > maxXMPPayload || !bytes.HasSuffix(extractMain(t, out.Bytes()), []byte(`<?xpacket end="w"?>`)) {
			t.Errorf("standard XMP segments %v", sizes)
		}
	})
}

func extractMain(t *testing.T, data []byte) []byte {
	t.Helper()
	main, _ := extSegments(t, data)
	return main
}

// TestTask332_TrimPacketPadding pins the padding split.
func TestTask332_TrimPacketPadding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, head, tail string }{
		{"<a/>  \n  <?xpacket end=\"w\"?>", "<a/>", `<?xpacket end="w"?>`},
		{"<a/><?xpacket end='r'?>", "<a/>", `<?xpacket end='r'?>`},
		{"<a/>  ", "<a/>  ", ""},
		{"", "", ""},
	} {
		head, tail := trimPacketPadding([]byte(tc.in))
		if string(head) != tc.head || string(tail) != tc.tail {
			t.Errorf("trimPacketPadding(%q) = %q, %q; want %q, %q", tc.in, head, tail, tc.head, tc.tail)
		}
	}
}

// TestTask332_UnmatchedLinkRemoved (JPEG-XMP-PART-04): a standard packet
// whose HasExtendedXMP GUID matches no extended chunk, or matches chunks that
// do not recompose, is read without the property; a write of what was read
// does not bring it back.
func TestTask332_UnmatchedLinkRemoved(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-14", func(t *testing.T) {
		t.Parallel()
		const guid = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		main := []byte(task332Head + `<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/" xmpNote:HasExtendedXMP="` + guid + `"/></rdf:RDF></x:xmpmeta>`)
		ext := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:p="http://example.com/p/" p:Moved="m"/></rdf:RDF></x:xmpmeta>`)
		cases := map[string][]byte{
			"GUID mismatch": buildExtendedXMPJPEG(main, "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", ext),
			// A chunk that declares more bytes than it carries does not recompose.
			"chunks do not recompose": func() []byte {
				d := buildExtendedXMPJPEG(main, guid, ext)
				i := bytes.LastIndex(d, []byte(guid))
				binary.BigEndian.PutUint32(d[i+32:], uint32(len(ext))+50) //nolint:gosec // G115: test sizes are small
				return d
			}(),
		}
		for name, data := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				_, _, raw, err := Extract(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("Extract: %v", err)
				}
				x, err := xmppkg.Parse(raw)
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				if x.Get(xmppkg.NSxmpNote, "HasExtendedXMP") != "" {
					t.Error("HasExtendedXMP was kept although no extended chunk was recomposed")
				}
				if x.Get(xmppkg.NSdc, "title") != "Partition title" {
					t.Error("the standard properties were lost")
				}
				var out bytes.Buffer
				if err := Inject(bytes.NewReader(buildJPEG(nil, nil, nil)), &out, nil, nil, raw, true); err != nil {
					t.Fatalf("Inject: %v", err)
				}
				_, _, again, err := Extract(bytes.NewReader(out.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(again, []byte("HasExtendedXMP")) {
					t.Error("the link came back on write")
				}
			})
		}
		// WithoutXMP semantics (#238): the raw standard packet is not parsed.
		_, _, _, raw, _, _, err := ExtractFullSelective(bytes.NewReader(cases["GUID mismatch"]), true, false)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte("HasExtendedXMP")) {
			t.Error("an unwanted XMP packet was re-serialised")
		}
	})
}
