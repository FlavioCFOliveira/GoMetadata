package xmp

// Adapted from Adobe XMP-Toolkit-SDK public/include/XMP_Const.h:kXMP_NS_*, Copyright 2003 Adobe, SPDX-License-Identifier: BSD-3-Clause
// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/XMPMeta.cpp:XMPMeta::Initialize,RegisterStandardAliases, Copyright 2003 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on these files.

// standardNamespace binds a namespace URI to the prefix under which the Adobe
// XMP Toolkit registers it.
type standardNamespace struct {
	uri, prefix string
}

// standardNamespaces lists the 55 namespaces that the XMP Toolkit registers at
// start-up (XMPMeta::Initialize), in registration order. The serialiser binds
// each of them to its Toolkit prefix instead of a generated nsN prefix
// (ISO 16684-1:2019 §7.4). The Toolkit also defines the deprecated constants
// kXMP_NS_XMP_T, kXMP_NS_XMP_T_PG and kXMP_NS_XMP_G_IMG, which repeat the URIs
// of xmpT, xmpTPg and xmpGImg and add no namespace.
var standardNamespaces = [...]standardNamespace{ //nolint:gochecknoglobals // read-only spec-sourced table; merged into prefixMap once at init
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

// AliasForm tells how an alias maps onto its base property. The values mirror
// the array-form option the XMP Toolkit passes to RegisterAlias.
type AliasForm uint8

// Alias forms.
const (
	// AliasSimple means the alias denotes the base property itself.
	AliasSimple AliasForm = iota
	// AliasOrderedArrayFirst means the alias denotes the first item of the
	// base property, which is an ordered array (rdf:Seq).
	AliasOrderedArrayFirst
	// AliasAltTextDefault means the alias denotes the default ("x-default")
	// item of the base property, which is a language alternative (rdf:Alt).
	AliasAltTextDefault
)

// Alias identifies the base property of an XMP alias.
type Alias struct {
	// NS is the namespace URI of the base property.
	NS string
	// Local is the local name of the base property.
	Local string
	// Form tells how the alias maps onto the base property.
	Form AliasForm
}

// aliasKey is the lookup key of the alias table; a two-string struct key
// hashes without allocating.
type aliasKey struct {
	ns, local string
}

// aliasRow is one row of standardAliases.
type aliasRow struct {
	aliasNS, aliasLocal string
	base                Alias
}

// standardAliases lists the 35 aliases that the XMP Toolkit registers in
// RegisterStandardAliases (Adobe XMP Toolkit SDK; the alias mechanism is described in the Adobe XMP
// Specification Part 1, section "Aliases").
var standardAliases = [...]aliasRow{ //nolint:gochecknoglobals // read-only spec-sourced table; indexed into aliasMap once at init
	{"http://ns.adobe.com/xap/1.0/", "Author", Alias{"http://purl.org/dc/elements/1.1/", "creator", AliasOrderedArrayFirst}},
	{"http://ns.adobe.com/xap/1.0/", "Authors", Alias{"http://purl.org/dc/elements/1.1/", "creator", AliasSimple}},
	{"http://ns.adobe.com/xap/1.0/", "Description", Alias{"http://purl.org/dc/elements/1.1/", "description", AliasSimple}},
	{"http://ns.adobe.com/xap/1.0/", "Format", Alias{"http://purl.org/dc/elements/1.1/", "format", AliasSimple}},
	{"http://ns.adobe.com/xap/1.0/", "Keywords", Alias{"http://purl.org/dc/elements/1.1/", "subject", AliasSimple}},
	{"http://ns.adobe.com/xap/1.0/", "Locale", Alias{"http://purl.org/dc/elements/1.1/", "language", AliasSimple}},
	{"http://ns.adobe.com/xap/1.0/", "Title", Alias{"http://purl.org/dc/elements/1.1/", "title", AliasSimple}},
	{"http://ns.adobe.com/xap/1.0/rights/", "Copyright", Alias{"http://purl.org/dc/elements/1.1/", "rights", AliasSimple}},
	{"http://ns.adobe.com/pdf/1.3/", "Author", Alias{"http://purl.org/dc/elements/1.1/", "creator", AliasOrderedArrayFirst}},
	{"http://ns.adobe.com/pdf/1.3/", "BaseURL", Alias{"http://ns.adobe.com/xap/1.0/", "BaseURL", AliasSimple}},
	{"http://ns.adobe.com/pdf/1.3/", "CreationDate", Alias{"http://ns.adobe.com/xap/1.0/", "CreateDate", AliasSimple}},
	{"http://ns.adobe.com/pdf/1.3/", "Creator", Alias{"http://ns.adobe.com/xap/1.0/", "CreatorTool", AliasSimple}},
	{"http://ns.adobe.com/pdf/1.3/", "ModDate", Alias{"http://ns.adobe.com/xap/1.0/", "ModifyDate", AliasSimple}},
	{"http://ns.adobe.com/pdf/1.3/", "Subject", Alias{"http://purl.org/dc/elements/1.1/", "description", AliasAltTextDefault}},
	{"http://ns.adobe.com/pdf/1.3/", "Title", Alias{"http://purl.org/dc/elements/1.1/", "title", AliasAltTextDefault}},
	{"http://ns.adobe.com/photoshop/1.0/", "Author", Alias{"http://purl.org/dc/elements/1.1/", "creator", AliasOrderedArrayFirst}},
	{"http://ns.adobe.com/photoshop/1.0/", "Caption", Alias{"http://purl.org/dc/elements/1.1/", "description", AliasAltTextDefault}},
	{"http://ns.adobe.com/photoshop/1.0/", "Copyright", Alias{"http://purl.org/dc/elements/1.1/", "rights", AliasAltTextDefault}},
	{"http://ns.adobe.com/photoshop/1.0/", "Keywords", Alias{"http://purl.org/dc/elements/1.1/", "subject", AliasSimple}},
	{"http://ns.adobe.com/photoshop/1.0/", "Marked", Alias{"http://ns.adobe.com/xap/1.0/rights/", "Marked", AliasSimple}},
	{"http://ns.adobe.com/photoshop/1.0/", "Title", Alias{"http://purl.org/dc/elements/1.1/", "title", AliasAltTextDefault}},
	{"http://ns.adobe.com/photoshop/1.0/", "WebStatement", Alias{"http://ns.adobe.com/xap/1.0/rights/", "WebStatement", AliasSimple}},
	{"http://ns.adobe.com/tiff/1.0/", "Artist", Alias{"http://purl.org/dc/elements/1.1/", "creator", AliasOrderedArrayFirst}},
	{"http://ns.adobe.com/tiff/1.0/", "Copyright", Alias{"http://purl.org/dc/elements/1.1/", "rights", AliasSimple}},
	{"http://ns.adobe.com/tiff/1.0/", "DateTime", Alias{"http://ns.adobe.com/xap/1.0/", "ModifyDate", AliasSimple}},
	{"http://ns.adobe.com/exif/1.0/", "DateTimeDigitized", Alias{"http://ns.adobe.com/xap/1.0/", "CreateDate", AliasSimple}},
	{"http://ns.adobe.com/tiff/1.0/", "ImageDescription", Alias{"http://purl.org/dc/elements/1.1/", "description", AliasAltTextDefault}},
	{"http://ns.adobe.com/tiff/1.0/", "Software", Alias{"http://ns.adobe.com/xap/1.0/", "CreatorTool", AliasSimple}},
	{"http://ns.adobe.com/png/1.0/", "Author", Alias{"http://purl.org/dc/elements/1.1/", "creator", AliasOrderedArrayFirst}},
	{"http://ns.adobe.com/png/1.0/", "Copyright", Alias{"http://purl.org/dc/elements/1.1/", "rights", AliasAltTextDefault}},
	{"http://ns.adobe.com/png/1.0/", "CreationTime", Alias{"http://ns.adobe.com/xap/1.0/", "CreateDate", AliasSimple}},
	{"http://ns.adobe.com/png/1.0/", "Description", Alias{"http://purl.org/dc/elements/1.1/", "description", AliasAltTextDefault}},
	{"http://ns.adobe.com/png/1.0/", "ModificationTime", Alias{"http://ns.adobe.com/xap/1.0/", "ModifyDate", AliasSimple}},
	{"http://ns.adobe.com/png/1.0/", "Software", Alias{"http://ns.adobe.com/xap/1.0/", "CreatorTool", AliasSimple}},
	{"http://ns.adobe.com/png/1.0/", "Title", Alias{"http://purl.org/dc/elements/1.1/", "title", AliasAltTextDefault}},
}

// aliasMap indexes standardAliases; it is read-only after package initialisation.
var aliasMap = buildAliasMap() //nolint:gochecknoglobals // read-only lookup table built once at package initialisation

func buildAliasMap() map[aliasKey]Alias {
	m := make(map[aliasKey]Alias, len(standardAliases))
	for i := range standardAliases {
		r := &standardAliases[i]
		m[aliasKey{r.aliasNS, r.aliasLocal}] = r.base
	}
	return m
}

// withStandardNamespaces adds every Toolkit namespace that m does not bind yet
// and returns m. The hand-written bindings of prefixMap keep priority; a URI
// that is already bound is never rebound. It runs once, while prefixMap is
// initialised.
func withStandardNamespaces(m map[string]string) map[string]string {
	for i := range standardNamespaces {
		s := &standardNamespaces[i]
		if _, ok := m[s.uri]; !ok {
			m[s.uri] = s.prefix
		}
	}
	return m
}

// ResolveAlias returns the base property of the XMP alias (ns, local), for
// example (NSphotoshop, "Caption") resolves to the default item of
// dc:description. The boolean is false when (ns, local) is not one of the
// standard aliases of the XMP Toolkit. The lookup does not allocate.
func ResolveAlias(ns, local string) (Alias, bool) {
	a, ok := aliasMap[aliasKey{ns, local}]
	return a, ok
}
