package jpeg

// task323_350_351_test.go — regression tests for sprint 46 tasks #323 (IPTC
// digest on write), #350 (an unmergeable extended XMP leaves no link) and
// #351 (65502-byte limit of the standard XMP packet).

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: MD5 is the MWG IPTC digest algorithm, not used for security
	"strconv"
	"strings"
	"testing"

	irbpkg "github.com/FlavioCFOliveira/GoMetadata/internal/irb"
	xmppkg "github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// TestInject_IPTCDigestPolicy (task #323; MWG Guidelines v2.0 §4.2.3.2, p. 29:
// "If IPTC-IIM and XMP are both present, whether changed or not, a Changer
// MUST create or update the checksum value"): with XMP written, an absent
// digest is created, a matching one refreshed and a stale one refreshed;
// without XMP the digest is left as it is.
func TestInject_IPTCDigestPolicy(t *testing.T) {
	t.Parallel()
	oldIIM := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'o', 'l', 'd'}
	newIIM := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'n', 'e', 'w'}
	sum := func(b []byte) []byte { s := md5.Sum(b); return s[:] } //nolint:gosec // G401: MWG digest
	stale := bytes.Repeat([]byte{0x5A}, 16)
	xmpPacket := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	withIRB := func(digest []byte) []byte {
		var b []byte
		if digest != nil {
			b = irbpkg.AppendBlock(b, irbpkg.ResourceIPTCDigest, digest)
		}
		return irbpkg.AppendBlock(b, irbpkg.ResourceIPTC, oldIIM)
	}
	cases := []struct {
		name       string
		digest     []byte // digest of the source; nil: none
		iim        []byte // IIM written
		xmp        []byte // XMP written; nil: none
		wantDigest []byte // nil: no digest in the output
	}{
		{"RECONCILE-06 absent digest is created", nil, newIIM, xmpPacket, sum(newIIM)},
		{"RECONCILE-06 matching digest is refreshed", sum(oldIIM), newIIM, xmpPacket, sum(newIIM)},
		{"RECONCILE-06 stale digest is refreshed", stale, newIIM, xmpPacket, sum(newIIM)},
		{"RECONCILE-06 stale digest of an unchanged IIM is refreshed", stale, oldIIM, xmpPacket, sum(oldIIM)},
		{"RECONCILE-06 current digest of an unchanged IIM stays", sum(oldIIM), oldIIM, xmpPacket, sum(oldIIM)},
		{"RECONCILE-06 no XMP: no digest created", nil, newIIM, nil, nil},
		{"RECONCILE-06 no XMP: stale digest kept as it is", stale, newIIM, nil, stale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := buildJPEGWithRawAPP13(append(append([]byte{}, identPS...), withIRB(tc.digest)...))
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, nil, tc.iim, tc.xmp, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			_, iim, digest, _, _, _, err := ExtractFull(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("ExtractFull: %v", err)
			}
			if !bytes.Equal(iim, tc.iim) {
				t.Errorf("IIM = %x, want %x", iim, tc.iim)
			}
			if !bytes.Equal(digest, tc.wantDigest) {
				t.Errorf("digest = %x, want %x", digest, tc.wantDigest)
			}
		})
	}
}

// ext350 is an extended XMP document body that is not well-formed: the
// rdf:Description is never closed.
const ext350 = `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about="" xmlns:p="http://example.com/p/" p:Moved="m"></rdf:RDF></x:xmpmeta>`

// TestTask350_UnmergedExtendedLinkRemoved (JPEG-14): an extended chunk whose
// GUID matches the standard packet but that cannot be merged (ill-formed, or
// no XMP document at all) leaves a standard packet without
// xmpNote:HasExtendedXMP, the signal ErrExtendedXMPTruncated, and the wire
// frame that an unmodified write needs (Part 3 §1.1.3.1, p. 15; Toolkit
// JPEG_Handler.cpp:ProcessXMP deletes the property before it merges).
func TestTask350_UnmergedExtendedLinkRemoved(t *testing.T) {
	t.Parallel()
	const guid = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	main := []byte(task332Head + `<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/" xmpNote:HasExtendedXMP="` + guid + `"/></rdf:RDF></x:xmpmeta>`)
	cases := map[string]string{
		"ill-formed extended document": ext350,
		"text that is not XML":         "this is not an XMP document",
		"unbound prefix": `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about=""><zz:Moved>m</zz:Moved></rdf:Description></rdf:RDF></x:xmpmeta>`,
	}
	for name, ext := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := buildExtendedXMPJPEG(main, guid, []byte(ext))
			_, _, _, raw, wire, truncated, err := ExtractFull(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("ExtractFull: %v", err)
			}
			if bytes.Contains(raw, []byte("HasExtendedXMP")) {
				t.Errorf("the packet still carries xmpNote:HasExtendedXMP:\n%s", raw)
			}
			if !truncated {
				t.Error("no warning signal although the extended XMP was not merged")
			}
			x, err := xmppkg.Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if x.Get(xmppkg.NSdc, "title") != "Partition title" {
				t.Error("the standard properties were lost")
			}
			if wire == nil {
				t.Error("no wire frame: an unmodified write would drop the extended XMP")
			}
			// WithoutXMP keeps the standard packet verbatim, link included, as before.
			_, _, _, rawLazy, _, _, err := ExtractFullSelective(bytes.NewReader(data), true, false)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(rawLazy, main) {
				t.Error("WithoutXMP altered the standard packet")
			}
		})
	}
}

// packetOfSize returns an XMP packet of exactly n bytes with no padding: its
// one property carries a value of the length that makes up the size.
func packetOfSize(t *testing.T, n int) []byte {
	t.Helper()
	const head = `<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:p="http://example.com/p/"><p:V>`
	const tail = `</p:V></rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	fill := n - len(head) - len(tail)
	if fill < 0 {
		t.Fatalf("size %d is below the %d bytes of the packet frame", n, len(head)+len(tail))
	}
	p := []byte(head + strings.Repeat("v", fill) + tail)
	if len(p) != n {
		t.Fatalf("packet is %d bytes, want %d", len(p), n)
	}
	return p
}

// TestTask351_StandardPacketLimit (JPEG-02): XMP Part 3 §1.1.3.1, p. 14 limits
// the XMP packet of a single APP1 segment to 65502 bytes. An unpadded packet
// of 65502 bytes is written as one standard packet, one of 65503 bytes is
// partitioned (the property moves to extended XMP), and a file written up to
// the 65504 bytes the segment can hold still reads.
func TestTask351_StandardPacketLimit(t *testing.T) {
	t.Parallel()
	if maxXMPPayload != 65502 {
		t.Fatalf("maxXMPPayload = %d, want 65502 (XMP Part 3 §1.1.3.1, p. 14)", maxXMPPayload)
	}
	extNote := []byte("http://ns.adobe.com/xmp/extension/\x00")
	for _, tc := range []struct {
		size        int
		wantPartial bool
	}{
		{65502, false},
		{65503, true},
		{65504, true},
	} {
		t.Run("JPEG-02/"+strconv.Itoa(tc.size), func(t *testing.T) {
			t.Parallel()
			packet := packetOfSize(t, tc.size)
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(buildJPEG(nil, nil, nil)), &out, nil, nil, packet, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			if got := bytes.Contains(out.Bytes(), extNote); got != tc.wantPartial {
				t.Errorf("extended XMP present = %v, want %v", got, tc.wantPartial)
			}
			if !tc.wantPartial {
				if sizes := app1XMPSegments(out.Bytes()); len(sizes) != 1 || sizes[0] != tc.size {
					t.Errorf("standard XMP segments %v, want one of %d bytes", sizes, tc.size)
				}
			}
			// The reader takes the packet back whole or recomposed.
			_, _, raw, err := Extract(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			x, err := xmppkg.Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := len(x.Get("http://example.com/p/", "V")); got == 0 {
				t.Error("the property was lost")
			}
		})
	}
	// Read side: a file that holds a 65504-byte standard packet (the segment
	// maximum, which earlier releases wrote) is read, not rejected.
	data := buildJPEG(nil, nil, packetOfSize(t, 65504))
	_, _, raw, err := Extract(bytes.NewReader(data))
	if err != nil || len(raw) != 65504 {
		t.Errorf("Extract of a 65504-byte packet = %d bytes, %v; want 65504, nil", len(raw), err)
	}
}
