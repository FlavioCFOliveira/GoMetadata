package xmp

// task329_test.go — regression battery for task #329: registered aliases
// were not folded into their base properties, so reads missed alias-only
// values and setters left stale alias values behind (Adobe XMP Specification
// Part 2 §1.1.1; Toolkit MoveExplicitAliases).

import (
	"strings"
	"testing"
)

// aliasDoc builds a packet whose single rdf:Description holds body, with the
// namespaces that the alias table uses bound to their conventional prefixes.
func aliasDoc(body string) []byte {
	return xmpDoc(`<rdf:Description rdf:about="" xmlns:dc="` + NSdc + `" xmlns:xmp="` + NSxmp + `" xmlns:xmpRights="` + NSxmpRights +
		`" xmlns:pdf="` + NSpdf + `" xmlns:photoshop="` + NSphotoshop + `" xmlns:tiff="` + NStiff + `" xmlns:exif="` + NSexif +
		`" xmlns:png="http://ns.adobe.com/png/1.0/">` + body + `</rdf:Description>`)
}

// prefixOf returns the prefix that aliasDoc binds to ns.
func prefixOf(ns string) string {
	switch ns {
	case NSdc:
		return "dc"
	case NSxmp:
		return "xmp"
	case NSxmpRights:
		return "xmpRights"
	case NSpdf:
		return "pdf"
	case NSphotoshop:
		return "photoshop"
	case NStiff:
		return "tiff"
	case NSexif:
		return "exif"
	}
	return "png"
}

// TestTask329EveryAliasReadsThroughItsBase checks all 35 registered aliases,
// in element form: the value is found at the base property, the alias is
// gone, and no alias schema is left empty (checklist RDF-10).
func TestTask329EveryAliasReadsThroughItsBase(t *testing.T) {
	t.Parallel()
	if len(standardAliases) != 35 {
		t.Fatalf("alias table has %d rows, want 35", len(standardAliases))
	}
	for _, row := range standardAliases {
		t.Run("RDF-10/"+prefixOf(row.aliasNS)+":"+row.aliasLocal, func(t *testing.T) {
			t.Parallel()
			pfx := prefixOf(row.aliasNS)
			x := mustParse(t, aliasDoc(`<`+pfx+`:`+row.aliasLocal+`>alias value</`+pfx+`:`+row.aliasLocal+`>`))
			if got := x.Get(row.base.NS, row.base.Local); got != "alias value" {
				t.Errorf("base %s = %q, want %q", row.base.Local, got, "alias value")
			}
			if got := x.Get(row.aliasNS, row.aliasLocal); got != "" {
				t.Errorf("alias still present with value %q", got)
			}
			if m, ok := x.Properties[row.aliasNS]; ok && row.aliasNS != row.base.NS {
				t.Errorf("alias schema left in Properties: %v", m)
			}
			// The written packet holds the base form only.
			out := string(mustEncode(t, x))
			if strings.Contains(out, pfx+":"+row.aliasLocal+">") && row.aliasNS != row.base.NS {
				t.Errorf("Encode wrote the alias:\n%s", out)
			}
			again := mustParse(t, []byte(out))
			if got := again.Get(row.base.NS, row.base.Local); got != "alias value" {
				t.Errorf("after round trip base = %q", got)
			}
		})
	}
}

// TestTask329AliasAsAttribute covers the attribute form of an alias.
func TestTask329AliasAsAttribute(t *testing.T) {
	t.Parallel()
	x := mustParse(t, xmpDoc(`<rdf:Description rdf:about="" xmlns:photoshop="`+NSphotoshop+`" photoshop:Caption="from attribute"/>`))
	if got := x.Caption(); got != "from attribute" {
		t.Errorf("Caption = %q, want %q (alias-only packet)", got, "from attribute")
	}
}

// TestTask329SettersLeaveNoStaleAlias is the acceptance scenario: after
// SetCaption and SetCopyright no alias holds the old value.
func TestTask329SettersLeaveNoStaleAlias(t *testing.T) {
	t.Parallel()
	x := mustParse(t, aliasDoc(
		`<photoshop:Caption>old</photoshop:Caption>`+
			`<tiff:ImageDescription><rdf:Alt><rdf:li xml:lang="x-default">old</rdf:li></rdf:Alt></tiff:ImageDescription>`+
			`<tiff:Copyright><rdf:Alt><rdf:li xml:lang="x-default">old</rdf:li></rdf:Alt></tiff:Copyright>`))
	x.SetCaption("new")
	x.SetCopyright("newr")
	out := string(mustEncode(t, x))
	if strings.Contains(out, "old") || strings.Contains(out, "photoshop:") || strings.Contains(out, "tiff:") {
		t.Errorf("stale alias in output:\n%s", out)
	}
	again := mustParse(t, []byte(out))
	if again.Caption() != "new" || again.Copyright() != "newr" {
		t.Errorf("Caption, Copyright = %q, %q", again.Caption(), again.Copyright())
	}
}

// TestTask329ConflictRules documents and pins the rules of
// MoveExplicitAliases without strict aliasing (XMPMeta-Parse.cpp line 354).
func TestTask329ConflictRules(t *testing.T) {
	t.Parallel()
	alt := func(items ...string) string { // lang, value pairs
		var b strings.Builder
		b.WriteString(`<rdf:Alt>`)
		for len(items) >= 2 {
			b.WriteString(`<rdf:li xml:lang="` + items[0] + `">` + items[1] + `</rdf:li>`)
			items = items[2:]
		}
		b.WriteString(`</rdf:Alt>`)
		return b.String()
	}
	tests := []struct {
		name, body string
		ns, local  string
		want       string // flat value of the base property
		gone       []string
	}{
		{
			name: "simple alias, base absent: transplanted",
			body: `<xmp:Format>image/png</xmp:Format>`,
			ns:   NSdc, local: "format", want: "image/png", gone: []string{"xmp:Format"},
		},
		{
			name: "simple alias, base present and equal: alias deleted",
			body: `<xmp:Format>image/png</xmp:Format><dc:format>image/png</dc:format>`,
			ns:   NSdc, local: "format", want: "image/png", gone: []string{"xmp:Format"},
		},
		{
			name: "simple alias, base present and different: base wins, alias value is lost",
			body: `<xmp:Format>image/png</xmp:Format><dc:format>image/jpeg</dc:format>`,
			ns:   NSdc, local: "format", want: "image/jpeg", gone: []string{"xmp:Format", "image/png"},
		},
		{
			name: "alias after base in document order",
			body: `<dc:format>image/jpeg</dc:format><xmp:Format>image/png</xmp:Format>`,
			ns:   NSdc, local: "format", want: "image/jpeg", gone: []string{"xmp:Format"},
		},
		{
			name: "array item alias, base array absent: Seq created",
			body: `<photoshop:Author>Ann</photoshop:Author>`,
			ns:   NSdc, local: "creator", want: "Ann", gone: []string{"photoshop:Author"},
		},
		{
			name: "array item alias, base array present: first item wins",
			body: `<photoshop:Author>Ann</photoshop:Author><dc:creator><rdf:Seq><rdf:li>Bob</rdf:li></rdf:Seq></dc:creator>`,
			ns:   NSdc, local: "creator", want: "Bob", gone: []string{"photoshop:Author", "Ann"},
		},
		{
			name: "array item alias, base array empty: alias becomes the item",
			body: `<photoshop:Author>Ann</photoshop:Author><dc:creator><rdf:Seq/></dc:creator>`,
			ns:   NSdc, local: "creator", want: "Ann", gone: []string{"photoshop:Author"},
		},
		{
			name: "text alias, base Alt without x-default: alias becomes x-default, first",
			body: `<photoshop:Caption>Alias</photoshop:Caption><dc:description>` + alt("fr", "Bonjour") + `</dc:description>`,
			ns:   NSdc, local: "description", want: "Alias\x1efr|Bonjour", gone: []string{"photoshop:Caption"},
		},
		{
			name: "text alias, base Alt with x-default: alias deleted",
			body: `<photoshop:Caption>Alias</photoshop:Caption><dc:description>` + alt("x-default", "Base", "fr", "Bonjour") + `</dc:description>`,
			ns:   NSdc, local: "description", want: "Base\x1efr|Bonjour", gone: []string{"photoshop:Caption", "Alias"},
		},
		{
			name: "text alias, base written as a simple value: counts as the x-default item",
			body: `<photoshop:Caption>Alias</photoshop:Caption><dc:description>Base</dc:description>`,
			ns:   NSdc, local: "description", want: "Base", gone: []string{"photoshop:Caption", "Alias"},
		},
		{
			name: "text alias with xml:lang x-default is accepted",
			body: `<photoshop:Caption xml:lang="x-default">Alias</photoshop:Caption>`,
			ns:   NSdc, local: "description", want: "Alias", gone: []string{"photoshop:Caption"},
		},
		{
			name: "language alternative alias, base absent: transplanted whole",
			body: `<tiff:ImageDescription>` + alt("x-default", "Alias", "fr", "Bonjour") + `</tiff:ImageDescription>`,
			ns:   NSdc, local: "description", want: "Alias\x1efr|Bonjour", gone: []string{"tiff:ImageDescription"},
		},
		{
			name: "language alternative alias, base Alt: languages merged, base x-default wins",
			body: `<tiff:ImageDescription>` + alt("x-default", "Alias", "fr", "Bonjour", "de", "Hallo") + `</tiff:ImageDescription>` +
				`<dc:description>` + alt("x-default", "Base", "de", "Guten Tag") + `</dc:description>`,
			ns: NSdc, local: "description", want: "Base\x1ede|Guten Tag\x1efr|Bonjour", gone: []string{"tiff:ImageDescription", "Alias", "Hallo"},
		},
		{
			name: "ModifyDate from two aliases: first one in document order is transplanted",
			body: `<tiff:DateTime>2020-01-01T00:00:00Z</tiff:DateTime><png:ModificationTime>2021-01-01T00:00:00Z</png:ModificationTime>`,
			ns:   NSxmp, local: "ModifyDate", want: "2020-01-01T00:00:00Z", gone: []string{"tiff:DateTime", "png:ModificationTime"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := mustParse(t, aliasDoc(tc.body))
			if got := x.Get(tc.ns, tc.local); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.local, got, tc.want)
			}
			out := string(mustEncode(t, x))
			for _, g := range tc.gone {
				if strings.Contains(out, g) {
					t.Errorf("output still holds %q:\n%s", g, out)
				}
			}
			again := mustParse(t, []byte(out))
			if got := again.Get(tc.ns, tc.local); got != tc.want {
				t.Errorf("after round trip %s = %q, want %q", tc.local, got, tc.want)
			}
		})
	}
}

// TestTask329UnfoldableAliasesAreKept pins the cases left as written
// instead of being folded (see moveExplicitAliases).
func TestTask329UnfoldableAliasesAreKept(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, body, keep string }{
		{"text alias in another language", `<photoshop:Caption xml:lang="fr">Bonjour</photoshop:Caption>`, "photoshop:Caption"},
		{"array item alias that is a bag", `<photoshop:Author><rdf:Bag><rdf:li>Ann</rdf:li></rdf:Bag></photoshop:Author>`, "photoshop:Author"},
		{"base is a struct", `<xmp:Format>x</xmp:Format><dc:format rdf:parseType="Resource"><dc:a>1</dc:a></dc:format>`, "xmp:Format"},
	}
	for _, tc := range tests {
		x := mustParse(t, aliasDoc(tc.body))
		if out := string(mustEncode(t, x)); !strings.Contains(out, tc.keep) {
			t.Errorf("%s: alias %s was dropped:\n%s", tc.name, tc.keep, out)
		}
	}
}

// TestTask329ExtendedPacket applies the folding to ParseExtended.
func TestTask329ExtendedPacket(t *testing.T) {
	t.Parallel()
	std := extStandard(NSxmpNote, "t")
	ext := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:pdf="` + NSpdf + `"><pdf:Subject>Alias subject</pdf:Subject></rdf:Description></rdf:RDF></x:xmpmeta>`
	x, err := ParseExtended(std, []byte(ext))
	if err != nil {
		t.Fatalf("ParseExtended: %v", err)
	}
	if got := x.Get(NSdc, "description"); got != "Alias subject" {
		t.Errorf("dc:description = %q, want the folded pdf:Subject", got)
	}
}
