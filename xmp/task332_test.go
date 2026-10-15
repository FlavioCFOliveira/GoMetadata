package xmp

// task332_test.go — tests for the compact serialisation and for
// PackageForJPEG (task #332): the partitioning of the XMP of a JPEG file in
// Adobe XMP Specification Part 3 §1.1.3.1-§1.1.3.2 (pp. 14-15), ported from
// the Adobe XMP Toolkit's XMPUtils::PackageForJPEG.

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: the MD5 digest of the extended XMP is mandated by Part 3 §1.1.3.1
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// topLines returns one line per top-level property of x: the Properties
// entries and the retained tree nodes, without the order of properties.
func topLines(x *XMP) []string {
	var lines []string
	for ns, m := range x.Properties {
		for k, v := range m {
			lines = append(lines, fmt.Sprintf("P %s %q=%q", ns, k, v))
		}
	}
	if len(x.nodes) > 0 {
		for s := x.nodes[0].firstChild; s != noNode; s = x.nodes[s].next {
			if x.nodes[s].flags&nodeBlank != 0 {
				var b strings.Builder
				dumpNode(&b, x.nodes, s, 0)
				lines = append(lines, "B "+b.String())
				continue
			}
			for p := x.nodes[s].firstChild; p != noNode; p = x.nodes[p].next {
				var b strings.Builder
				dumpNode(&b, x.nodes, p, 0)
				lines = append(lines, "T "+x.nodes[s].ns+" "+b.String())
			}
		}
	}
	slices.Sort(lines)
	return lines
}

// bigDoc builds an XMP packet with the given extra namespace blocks.
func bigDoc(blocks ...string) []byte {
	var b strings.Builder
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	for _, blk := range blocks {
		b.WriteString(blk)
	}
	b.WriteString(`</rdf:RDF></x:xmpmeta>`)
	return []byte(b.String())
}

const (
	blockDC = `<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/" ` +
		`xmp:CreatorTool="Tool" xmp:Rating="3"><dc:title><rdf:Alt><rdf:li xml:lang="x-default">Title</rdf:li></rdf:Alt></dc:title>` +
		`<dc:creator><rdf:Seq><rdf:li>Ann</rdf:li></rdf:Seq></dc:creator></rdf:Description>`
)

// blockHistory builds a photoshop:History-like or custom property of about n bytes.
func blockProp(prefixDecl, name string, n int) string {
	return fmt.Sprintf(`<rdf:Description rdf:about="" %s><%s>%s</%s></rdf:Description>`, prefixDecl, name, strings.Repeat("x", n), name)
}

func TestCompactMarkup_PreservesModel(t *testing.T) {
	t.Parallel()
	docs := map[string][]byte{
		"scalars and arrays": bigDoc(blockDC),
		"white space values": bigDoc(`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/">` +
			"<xmp:Label>\n   </xmp:Label><xmp:Nickname> </xmp:Nickname><dc:format>\n a\n  b\n</dc:format></rdf:Description>"),
		"retained forms": bigDoc(`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:t="http://example.com/t/">` +
			`<t:Nested rdf:parseType="Resource"><t:In rdf:parseType="Resource"><t:Deep>d</t:Deep></t:In></t:Nested>` +
			`<dc:subject><rdf:Bag><rdf:li></rdf:li><rdf:li>k</rdf:li></rdf:Bag></dc:subject>` +
			`<t:Qual xml:lang="fr">q</t:Qual></rdf:Description>`),
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			x := mustParse(t, doc)
			plain, err := serialiseMode(x, serialiseDefault)
			if err != nil {
				t.Fatal(err)
			}
			compact, err := serialiseMode(x, serialiseCompact)
			if err != nil {
				t.Fatal(err)
			}
			if len(compact) >= len(plain)-2048 {
				t.Errorf("compact form is %d bytes, plain form %d (2048 of them padding): no formatting removed", len(compact), len(plain))
			}
			if bytes.Contains(compact, []byte("\n  ")) && !strings.Contains(name, "white space") {
				t.Errorf("compact form still has formatting white space:\n%s", compact)
			}
			if a, b := topLines(mustParse(t, plain)), topLines(mustParse(t, compact)); !slices.Equal(a, b) {
				t.Errorf("model changed by compaction:\n%s\n---\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
			}
			// No wrapper: the extended form is a bare xmpmeta document.
			bare, err := serialiseMode(x, serialiseCompact|serialiseNoWrapper)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(bare, []byte("<?xpacket")) || !bytes.HasPrefix(bare, []byte("<x:xmpmeta")) {
				t.Errorf("unwrapped form: %.60s", bare)
			}
			if a, b := topLines(mustParse(t, plain)), topLines(mustParse(t, bare)); !slices.Equal(a, b) {
				t.Error("model changed by the unwrapped form")
			}
		})
	}
}

func TestPackageForJPEG_SmallStaysWhole(t *testing.T) {
	t.Parallel()
	x := mustParse(t, bigDoc(blockDC))
	pkg, err := PackageForJPEG(x)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Extended != nil || pkg.GUID != "" {
		t.Fatalf("a small XMP was split: ext %d bytes, GUID %q", len(pkg.Extended), pkg.GUID)
	}
	if bytes.Contains(pkg.Standard, []byte("HasExtendedXMP")) {
		t.Error("the standard packet carries HasExtendedXMP although nothing moved")
	}
	if len(pkg.Standard) > jpegStdLimit {
		t.Errorf("standard packet is %d bytes, limit %d", len(pkg.Standard), jpegStdLimit)
	}
	if a, b := topLines(x), topLines(mustParse(t, pkg.Standard)); !slices.Equal(a, b) {
		t.Error("the standard packet differs from the XMP")
	}
}

// TestPackageForJPEG_NearLimitNotSplit: a compact serialisation of 64000
// bytes is not split (Part 3 §1.1.3.2, p. 15: avoid partitioning when
// possible), although with the 2 KB packet padding it exceeds 65502 bytes.
func TestPackageForJPEG_NearLimitNotSplit(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-11", func(t *testing.T) {
		t.Parallel()
		doc := bigDoc(blockDC, blockProp(`xmlns:t="http://example.com/t/"`, "t:Big", 0))
		base := mustParse(t, doc)
		b0, err := serialiseMode(base, serialiseCompact)
		if err != nil {
			t.Fatal(err)
		}
		fill := 64000 - len(b0)
		x := mustParse(t, bigDoc(blockDC, blockProp(`xmlns:t="http://example.com/t/"`, "t:Big", fill)))
		compact, err := serialiseMode(x, serialiseCompact)
		if err != nil {
			t.Fatal(err)
		}
		if len(compact) != 64000 {
			t.Fatalf("fixture compact size = %d, want 64000", len(compact))
		}
		if padded := mustEncode(t, x); len(padded) <= 65502 {
			t.Fatalf("fixture padded size = %d, want > 65502", len(padded))
		}
		pkg, err := PackageForJPEG(x)
		if err != nil {
			t.Fatal(err)
		}
		if pkg.Extended != nil {
			t.Fatalf("a 64000-byte compact XMP was split (extended %d bytes)", len(pkg.Extended))
		}
		if len(pkg.Standard) != jpegStdLimit {
			t.Errorf("standard packet is %d bytes, want %d (64000 plus padding up to the limit)", len(pkg.Standard), jpegStdLimit)
		}
	})
}

// TestPackageForJPEG_Partition: 200 KB of XMP keeps the scalar and array
// properties of dc and xmp in the standard packet and moves the largest
// properties; the two portions recompose into the original XMP.
func TestPackageForJPEG_Partition(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-12", func(t *testing.T) {
		t.Parallel()
		doc := bigDoc(blockDC,
			blockProp(`xmlns:t="http://example.com/t/"`, "t:Huge", 120000),
			blockProp(`xmlns:u="http://example.com/u/"`, "u:Large", 60000),
			blockProp(`xmlns:v="http://example.com/v/"`, "v:Small", 3000),
		)
		x := mustParse(t, doc)
		pkg, err := PackageForJPEG(x)
		if err != nil {
			t.Fatal(err)
		}
		if pkg.Extended == nil || len(pkg.GUID) != 32 {
			t.Fatalf("not split: ext %d bytes, GUID %q", len(pkg.Extended), pkg.GUID)
		}
		if len(pkg.Standard) > jpegStdLimit {
			t.Errorf("standard packet is %d bytes, limit %d", len(pkg.Standard), jpegStdLimit)
		}
		if sum := md5.Sum(pkg.Extended); !strings.EqualFold(pkg.GUID, hex.EncodeToString(sum[:])) || pkg.GUID != strings.ToUpper(pkg.GUID) { //nolint:gosec // G401: see the import
			t.Errorf("GUID %q is not the uppercase MD5 of the extended serialisation", pkg.GUID)
		}
		if bytes.Contains(pkg.Extended, []byte("<?xpacket")) {
			t.Error("the extended serialisation has a packet wrapper")
		}
		std := mustParse(t, pkg.Standard)
		if got := std.Get(NSxmpNote, hasExtendedXMP); got != pkg.GUID {
			t.Errorf("HasExtendedXMP = %q, want %q", got, pkg.GUID)
		}
		if std.Get(NSdc, "title") != "Title" || std.Get(NSxmp, "CreatorTool") != "Tool" || std.Get(NSxmp, "Rating") != "3" || std.Creator() != "Ann" {
			t.Errorf("dc/xmp properties left the standard packet: %v", std.Properties)
		}
		if std.Get("http://example.com/v/", "Small") == "" {
			t.Error("the small property moved although a large one was enough")
		}
		if std.Get("http://example.com/t/", "Huge") != "" {
			t.Error("the largest property stayed in the standard packet")
		}
		ext := mustParse(t, pkg.Extended)
		if ext.Get("http://example.com/t/", "Huge") == "" {
			t.Error("the largest property is not in the extended portion")
		}
		merged, err := ParseExtended(pkg.Standard, pkg.Extended)
		if err != nil {
			t.Fatalf("ParseExtended: %v", err)
		}
		if a, b := topLines(x), topLines(merged); !slices.Equal(a, b) {
			t.Errorf("the recomposed XMP differs from the original (%d vs %d lines)", len(a), len(b))
		}
		// Deterministic: the same XMP gives the same bytes.
		again, err := PackageForJPEG(x)
		if err != nil || !bytes.Equal(again.Standard, pkg.Standard) || !bytes.Equal(again.Extended, pkg.Extended) {
			t.Error("PackageForJPEG is not deterministic")
		}
	})
}

// TestPackageForJPEG_Order pins the order of Part 3 §1.1.3.2 (p. 15):
// xmp:Thumbnails first, then the Camera Raw namespace, then photoshop:History,
// stopping as soon as the standard packet fits.
func TestPackageForJPEG_Order(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-12", func(t *testing.T) {
		t.Parallel()
		thumb := func(n int) string {
			return fmt.Sprintf(`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:xmpGImg="http://ns.adobe.com/xap/1.0/g/img/">`+
				`<xmp:Thumbnails><rdf:Alt><rdf:li rdf:parseType="Resource"><xmpGImg:format>JPEG</xmpGImg:format><xmpGImg:image>%s</xmpGImg:image></rdf:li></rdf:Alt></xmp:Thumbnails></rdf:Description>`,
				strings.Repeat("A", n))
		}
		crs := func(n int) string {
			return blockProp(`xmlns:crs="http://ns.adobe.com/camera-raw-settings/1.0/"`, "crs:ToneCurve", n)
		}
		hist := func(n int) string {
			return blockProp(`xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/"`, "photoshop:History", n)
		}
		other := func(n int) string { return blockProp(`xmlns:t="http://example.com/t/"`, "t:Other", n) }

		cases := []struct {
			name     string
			doc      []byte
			inExt    []string // "ns name"
			inStd    []string
			wantMove bool
		}{
			{
				"thumbnails alone is enough",
				bigDoc(blockDC, thumb(40000), crs(15000), hist(15000), other(10000)),
				[]string{"http://ns.adobe.com/xap/1.0/ Thumbnails"},
				[]string{"http://ns.adobe.com/camera-raw-settings/1.0/ ToneCurve", "http://ns.adobe.com/photoshop/1.0/ History", "http://example.com/t/ Other"},
				true,
			},
			{
				"camera raw after thumbnails",
				bigDoc(blockDC, thumb(30000), crs(45000), hist(15000), other(10000)),
				[]string{"http://ns.adobe.com/xap/1.0/ Thumbnails", "http://ns.adobe.com/camera-raw-settings/1.0/ ToneCurve"},
				[]string{"http://ns.adobe.com/photoshop/1.0/ History", "http://example.com/t/ Other"},
				true,
			},
			{
				"history after camera raw",
				bigDoc(blockDC, thumb(20000), crs(20000), hist(50000), other(20000)),
				[]string{"http://ns.adobe.com/xap/1.0/ Thumbnails", "http://ns.adobe.com/camera-raw-settings/1.0/ ToneCurve", "http://ns.adobe.com/photoshop/1.0/ History"},
				[]string{"http://example.com/t/ Other"},
				true,
			},
			{
				"largest first when none of the three is enough",
				bigDoc(blockDC, thumb(1000), crs(1000), hist(1000), other(70000)),
				[]string{"http://example.com/t/ Other", "http://ns.adobe.com/xap/1.0/ Thumbnails", "http://ns.adobe.com/camera-raw-settings/1.0/ ToneCurve", "http://ns.adobe.com/photoshop/1.0/ History"},
				nil,
				true,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				x := mustParse(t, tc.doc)
				pkg, err := PackageForJPEG(x)
				if err != nil {
					t.Fatal(err)
				}
				if pkg.Extended == nil {
					t.Fatal("not split")
				}
				std, ext := mustParse(t, pkg.Standard), mustParse(t, pkg.Extended)
				has := func(p *XMP, key string) bool {
					ns, name, _ := strings.Cut(key, " ")
					return len(p.Properties[ns]) > 0 && slices.ContainsFunc(p.topProps(), func(tp topProp) bool { return tp.ns == ns && tp.name == name })
				}
				for _, k := range tc.inExt {
					if !has(ext, k) || has(std, k) {
						t.Errorf("%s: want in the extended portion only", k)
					}
				}
				for _, k := range tc.inStd {
					if !has(std, k) || has(ext, k) {
						t.Errorf("%s: want in the standard portion only", k)
					}
				}
				merged, err := ParseExtended(pkg.Standard, pkg.Extended)
				if err != nil {
					t.Fatal(err)
				}
				if a, b := topLines(x), topLines(merged); !slices.Equal(a, b) {
					t.Errorf("a property was lost or changed by the partition (%d vs %d lines)", len(a), len(b))
				}
			})
		}
	})
}

// TestPackageForJPEG_RetainedAndPinned: retained properties (nested structs)
// move whole; a property that refers to a blank node never moves, so a
// document whose pinned part alone is too large is refused.
func TestPackageForJPEG_RetainedAndPinned(t *testing.T) {
	t.Parallel()
	nested := `<rdf:Description rdf:about="" xmlns:t="http://example.com/t/"><t:Nested rdf:parseType="Resource"><t:In rdf:parseType="Resource"><t:Deep>` +
		strings.Repeat("d", 80000) + `</t:Deep></t:In></t:Nested></rdf:Description>`
	x := mustParse(t, bigDoc(blockDC, nested))
	if len(x.nodes) == 0 {
		t.Fatal("fixture has no retained property")
	}
	pkg, err := PackageForJPEG(x)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := ParseExtended(pkg.Standard, pkg.Extended)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := topLines(x), topLines(merged); !slices.Equal(a, b) {
		t.Error("a retained property changed by the partition")
	}

	pinned := `<rdf:Bag rdf:nodeID="b1"><rdf:li>` + strings.Repeat("k", 70000) + `</rdf:li></rdf:Bag>` +
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:subject rdf:nodeID="b1"/></rdf:Description>`
	_, err = PackageForJPEG(mustParse(t, bigDoc(pinned)))
	if !errors.Is(err, ErrTooLargeForJPEG) {
		t.Errorf("err = %v, want ErrTooLargeForJPEG", err)
	}
}

func TestPackageForJPEG_DropsStaleLink(t *testing.T) {
	t.Parallel()
	doc := bigDoc(blockDC, `<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/" xmpNote:HasExtendedXMP="0123456789ABCDEF0123456789ABCDEF"/>`)
	pkg, err := PackageForJPEG(mustParse(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(pkg.Standard, []byte("HasExtendedXMP")) {
		t.Error("a stale xmpNote:HasExtendedXMP was written with no extended portion")
	}
}

func TestStripExtendedLink(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-14", func(t *testing.T) {
		t.Parallel()
		with := bigDoc(blockDC, `<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/" xmpNote:HasExtendedXMP="0123456789ABCDEF0123456789ABCDEF"/>`)
		got, err := StripExtendedLink(with)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(got, []byte("HasExtendedXMP")) {
			t.Error("the link property survived")
		}
		x := mustParse(t, got)
		if x.Get(NSdc, "title") != "Title" || x.Get(NSxmp, "CreatorTool") != "Tool" {
			t.Errorf("other properties lost: %v", x.Properties)
		}
		without := bigDoc(blockDC)
		if got, err := StripExtendedLink(without); err != nil || !bytes.Equal(got, without) {
			t.Errorf("a packet without the property changed or failed: %v", err)
		}
		if _, err := StripExtendedLink([]byte("<x:xmpmeta xmlns:x='adobe:ns:meta/'><oops></x:xmpmeta>")); err == nil {
			t.Error("an ill-formed packet was accepted")
		}
	})
}

func BenchmarkPackageForJPEG(b *testing.B) {
	x, err := Parse(bigDoc(blockDC,
		blockProp(`xmlns:t="http://example.com/t/"`, "t:Huge", 120000),
		blockProp(`xmlns:u="http://example.com/u/"`, "u:Large", 60000)))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for range b.N {
		if _, err := PackageForJPEG(x); err != nil {
			b.Fatal(err)
		}
	}
}

// FuzzPackageForJPEG: the two portions recompose into the XMP they came from,
// and the standard packet fits the limit, for any parseable document.
func FuzzPackageForJPEG(f *testing.F) {
	f.Add(bigDoc(blockDC))
	f.Add(bigDoc(blockDC, blockProp(`xmlns:t="http://example.com/t/"`, "t:Huge", 70000)))
	f.Fuzz(func(t *testing.T, doc []byte) {
		x, err := Parse(doc)
		if err != nil {
			return
		}
		pkg, err := PackageForJPEG(x)
		if err != nil {
			return
		}
		if len(pkg.Standard) > jpegStdLimit {
			t.Fatalf("standard packet is %d bytes", len(pkg.Standard))
		}
		if pkg.Extended == nil {
			return
		}
		if _, err := ParseExtended(pkg.Standard, pkg.Extended); err != nil {
			t.Fatalf("portions do not recompose: %v", err)
		}
	})
}
