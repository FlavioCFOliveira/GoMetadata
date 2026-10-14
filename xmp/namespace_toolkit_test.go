package xmp

// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/XMPMeta.cpp:XMPMeta::Initialize,RegisterStandardAliases, Copyright 2003 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md. The expected tables below are an independent
// transcription of the reference (commit 7093513bd3caaad29da01db0f275d88a39d6bcc2).

import (
	"strings"
	"testing"
)

// toolkitNamespaces lists the 55 URI/prefix pairs of XMPMeta::Initialize.
var toolkitNamespaces = [][2]string{ //nolint:gochecknoglobals // read-only reference table
	{"http://www.w3.org/XML/1998/namespace", "xml"},
	{"http://www.w3.org/1999/02/22-rdf-syntax-ns#", "rdf"},
	{"http://purl.org/dc/elements/1.1/", "dc"},
	{"http://ns.adobe.com/xap/1.0/", "xmp"},
	{"http://ns.adobe.com/pdf/1.3/", "pdf"},
	{"http://ns.adobe.com/photoshop/1.0/", "photoshop"},
	{"http://ns.adobe.com/album/1.0/", "album"},
	{"http://ns.adobe.com/exif/1.0/", "exif"},
	{"http://ns.adobe.com/exif/1.0/aux/", "aux"},
	{"http://cipa.jp/exif/1.0/", "exifEX"},
	{"http://ns.adobe.com/tiff/1.0/", "tiff"},
	{"http://ns.adobe.com/png/1.0/", "png"},
	{"http://ns.adobe.com/jpeg/1.0/", "jpeg"},
	{"http://ns.adobe.com/jp2k/1.0/", "jp2k"},
	{"http://ns.adobe.com/camera-raw-settings/1.0/", "crs"},
	{"http://ns.adobe.com/asf/1.0/", "asf"},
	{"http://ns.adobe.com/xmp/wav/1.0/", "wav"},
	{"http://ns.adobe.com/StockPhoto/1.0/", "bmsp"},
	{"http://ns.adobe.com/creatorAtom/1.0/", "creatorAtom"},
	{"http://ns.adobe.com/xap/1.0/rights/", "xmpRights"},
	{"http://ns.adobe.com/xap/1.0/mm/", "xmpMM"},
	{"http://ns.adobe.com/xap/1.0/bj/", "xmpBJ"},
	{"http://ns.adobe.com/xmp/note/", "xmpNote"},
	{"http://ns.adobe.com/xmp/1.0/DynamicMedia/", "xmpDM"},
	{"http://ns.adobe.com/xmp/1.0/Script/", "xmpScript"},
	{"http://ns.adobe.com/bwf/bext/1.0/", "bext"},
	{"http://ns.adobe.com/aes/cart/", "AEScart"},
	{"http://ns.adobe.com/riff/info/", "riffinfo"},
	{"http://ns.adobe.com/xap/1.0/t/", "xmpT"},
	{"http://ns.adobe.com/xap/1.0/t/pg/", "xmpTPg"},
	{"http://ns.adobe.com/xap/1.0/g/", "xmpG"},
	{"http://ns.adobe.com/xap/1.0/g/img/", "xmpGImg"},
	{"http://ns.adobe.com/xap/1.0/sType/Font#", "stFnt"},
	{"http://ns.adobe.com/xap/1.0/sType/Dimensions#", "stDim"},
	{"http://ns.adobe.com/xap/1.0/sType/ResourceEvent#", "stEvt"},
	{"http://ns.adobe.com/xap/1.0/sType/ResourceRef#", "stRef"},
	{"http://ns.adobe.com/xap/1.0/sType/Version#", "stVer"},
	{"http://ns.adobe.com/xap/1.0/sType/Job#", "stJob"},
	{"http://ns.adobe.com/xap/1.0/sType/ManifestItem#", "stMfs"},
	{"http://ns.adobe.com/xmp/Identifier/qual/1.0/", "xmpidq"},
	{"http://iptc.org/std/Iptc4xmpCore/1.0/xmlns/", "Iptc4xmpCore"},
	{"http://iptc.org/std/Iptc4xmpExt/2008-02-29/", "Iptc4xmpExt"},
	{"http://ns.adobe.com/DICOM/", "DICOM"},
	{"http://ns.useplus.org/ldf/xmp/1.0/", "plus"},
	{"http://www.aiim.org/pdfa/ns/schema#", "pdfaSchema"},
	{"http://www.aiim.org/pdfa/ns/property#", "pdfaProperty"},
	{"http://www.aiim.org/pdfa/ns/type#", "pdfaType"},
	{"http://www.aiim.org/pdfa/ns/field#", "pdfaField"},
	{"http://www.aiim.org/pdfa/ns/id/", "pdfaid"},
	{"http://www.aiim.org/pdfa/ns/extension/", "pdfaExtension"},
	{"http://ns.adobe.com/pdfx/1.3/", "pdfx"},
	{"http://www.npes.org/pdfx/ns/id/", "pdfxid"},
	{"adobe:ns:meta/", "x"},
	{"http://ns.adobe.com/iX/1.0/", "iX"},
	{"http://ns.adobe.com/ixml/1.0/", "iXML"},
}

// toolkitAliases lists the 35 aliases of RegisterStandardAliases as
// {alias namespace prefix, alias local, base prefix, base local, form}.
var toolkitAliases = []struct { //nolint:gochecknoglobals // read-only reference table
	aliasPrefix, aliasLocal, basePrefix, baseLocal string
	form                                           AliasForm
}{
	{"xmp", "Author", "dc", "creator", AliasOrderedArrayFirst},
	{"xmp", "Authors", "dc", "creator", AliasSimple},
	{"xmp", "Description", "dc", "description", AliasSimple},
	{"xmp", "Format", "dc", "format", AliasSimple},
	{"xmp", "Keywords", "dc", "subject", AliasSimple},
	{"xmp", "Locale", "dc", "language", AliasSimple},
	{"xmp", "Title", "dc", "title", AliasSimple},
	{"xmpRights", "Copyright", "dc", "rights", AliasSimple},
	{"pdf", "Author", "dc", "creator", AliasOrderedArrayFirst},
	{"pdf", "BaseURL", "xmp", "BaseURL", AliasSimple},
	{"pdf", "CreationDate", "xmp", "CreateDate", AliasSimple},
	{"pdf", "Creator", "xmp", "CreatorTool", AliasSimple},
	{"pdf", "ModDate", "xmp", "ModifyDate", AliasSimple},
	{"pdf", "Subject", "dc", "description", AliasAltTextDefault},
	{"pdf", "Title", "dc", "title", AliasAltTextDefault},
	{"photoshop", "Author", "dc", "creator", AliasOrderedArrayFirst},
	{"photoshop", "Caption", "dc", "description", AliasAltTextDefault},
	{"photoshop", "Copyright", "dc", "rights", AliasAltTextDefault},
	{"photoshop", "Keywords", "dc", "subject", AliasSimple},
	{"photoshop", "Marked", "xmpRights", "Marked", AliasSimple},
	{"photoshop", "Title", "dc", "title", AliasAltTextDefault},
	{"photoshop", "WebStatement", "xmpRights", "WebStatement", AliasSimple},
	{"tiff", "Artist", "dc", "creator", AliasOrderedArrayFirst},
	{"tiff", "Copyright", "dc", "rights", AliasSimple},
	{"tiff", "DateTime", "xmp", "ModifyDate", AliasSimple},
	{"exif", "DateTimeDigitized", "xmp", "CreateDate", AliasSimple},
	{"tiff", "ImageDescription", "dc", "description", AliasAltTextDefault},
	{"tiff", "Software", "xmp", "CreatorTool", AliasSimple},
	{"png", "Author", "dc", "creator", AliasOrderedArrayFirst},
	{"png", "Copyright", "dc", "rights", AliasAltTextDefault},
	{"png", "CreationTime", "xmp", "CreateDate", AliasSimple},
	{"png", "Description", "dc", "description", AliasAltTextDefault},
	{"png", "ModificationTime", "xmp", "ModifyDate", AliasSimple},
	{"png", "Software", "xmp", "CreatorTool", AliasSimple},
	{"png", "Title", "dc", "title", AliasAltTextDefault},
}

func TestStandardNamespaces_AllRegisteredWithToolkitPrefix(t *testing.T) {
	t.Parallel()
	if len(toolkitNamespaces) != 55 || len(standardNamespaces) != 55 {
		t.Fatalf("namespace tables hold %d/%d rows, want 55", len(toolkitNamespaces), len(standardNamespaces))
	}
	for _, ns := range toolkitNamespaces {
		if got, ok := prefixMap[ns[0]]; !ok || got != ns[1] {
			t.Errorf("prefixMap[%q] = %q, %v; want %q", ns[0], got, ok, ns[1])
		}
	}
}

// TestStandardNamespaces_PrefixesUnique proves no prefix is bound to two URIs
// among the namespaces this package knows, which would break NS-03.
func TestStandardNamespaces_PrefixesUnique(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for uri, p := range prefixMap {
		if prev, dup := seen[p]; dup {
			t.Errorf("prefix %q bound to both %q and %q", p, prev, uri)
		}
		seen[p] = uri
	}
}

// TestStandardNamespaces_WriteEmitsToolkitPrefix writes one property in every
// Toolkit namespace and checks the serialised prefix, then parses it back.
func TestStandardNamespaces_WriteEmitsToolkitPrefix(t *testing.T) {
	t.Parallel()
	for _, ns := range toolkitNamespaces {
		uri, prefix := ns[0], ns[1]
		x := &XMP{}
		x.putProp(uri, "Probe", "v")
		out, err := Encode(x)
		if err != nil {
			t.Fatalf("%s: Encode: %v", prefix, err)
		}
		s := string(out)
		if !strings.Contains(s, "xmlns:"+prefix+"=\""+uri+"\"") || !strings.Contains(s, "<"+prefix+":Probe>v</"+prefix+":Probe>") {
			t.Errorf("%s: output lacks the Toolkit prefix binding:\n%s", prefix, s)
			continue
		}
		y, err := Parse(out)
		if err != nil {
			t.Fatalf("%s: Parse: %v", prefix, err)
		}
		if got := y.Get(uri, "Probe"); got != "v" {
			t.Errorf("%s: round trip Get = %q, want %q", prefix, got, "v")
		}
	}
}

func TestResolveAlias_StandardAliases(t *testing.T) {
	t.Parallel()
	if len(toolkitAliases) != 35 || len(standardAliases) != 35 || len(aliasMap) != 35 {
		t.Fatalf("alias tables hold %d/%d/%d rows, want 35", len(toolkitAliases), len(standardAliases), len(aliasMap))
	}
	uriOf := func(prefix string) string {
		for _, ns := range toolkitNamespaces {
			if ns[1] == prefix {
				return ns[0]
			}
		}
		t.Fatalf("unknown prefix %q in test table", prefix)
		return ""
	}
	for _, a := range toolkitAliases {
		got, ok := ResolveAlias(uriOf(a.aliasPrefix), a.aliasLocal)
		want := Alias{NS: uriOf(a.basePrefix), Local: a.baseLocal, Form: a.form}
		if !ok || got != want {
			t.Errorf("ResolveAlias(%s:%s) = %+v, %v; want %+v", a.aliasPrefix, a.aliasLocal, got, ok, want)
		}
	}
	if _, ok := ResolveAlias(NSdc, "creator"); ok {
		t.Error("dc:creator is a base property, not an alias")
	}
}

func TestNamespaceLookup_ZeroAllocs(t *testing.T) { //nolint:paralleltest // testing.AllocsPerRun panics under t.Parallel
	var (
		p  string
		a  Alias
		ok bool
	)
	n := testing.AllocsPerRun(1000, func() {
		p, ok = prefixMap["http://ns.adobe.com/xmp/1.0/DynamicMedia/"]
		a, ok = ResolveAlias(NSphotoshop, "Caption")
	})
	if n != 0 {
		t.Errorf("namespace/alias lookup allocs/op = %v, want 0", n)
	}
	_, _, _ = p, a, ok
}

func BenchmarkNamespaceLookup(b *testing.B) {
	b.Run("prefix", func(b *testing.B) {
		uris := [...]string{NSdc, "http://ns.adobe.com/xmp/1.0/DynamicMedia/", "http://www.aiim.org/pdfa/ns/id/", "urn:unknown"}
		b.ReportAllocs()
		var sink string
		for i := 0; b.Loop(); i++ {
			sink = prefixMap[uris[i%len(uris)]]
		}
		_ = sink
	})
	b.Run("alias", func(b *testing.B) {
		b.ReportAllocs()
		var sink Alias
		for b.Loop() {
			sink, _ = ResolveAlias(NSphotoshop, "Caption")
		}
		_ = sink
	})
}
