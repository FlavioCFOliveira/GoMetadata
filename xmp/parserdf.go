package xmp

// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/ParseRDF.cpp:RDF_Parser, Copyright 2004 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on this file.
//
// This file ports the recursive-descent RDF parser of the Adobe XMP Toolkit
// (RDF/XML Syntax Specification, W3C 2004, §7.2 grammar, restricted to the
// XMP subset of ISO 16684-1:2019 §7). The Toolkit reports recoverable errors
// through a callback and skips the offending construct; this port skips the
// same constructs silently, which keeps Parse lenient: only ErrEmptyInput,
// ErrDocumentTooLarge and ErrXMLNestingDepth are fatal.

import (
	"slices"
	"sync"
)

// rdfTerm classifies an RDF syntax name (RDF/XML Syntax §7.2.2–§7.2.4).
type rdfTerm uint8

// RDF term kinds, in the order of the Toolkit's RDFTermKind enumeration.
const (
	termOther rdfTerm = iota
	termRDF
	termID
	termAbout
	termParseType
	termResource
	termNodeID
	termDatatype // last coreSyntaxTerm
	termDescription
	termLi // last syntaxTerm
	termAboutEach
	termAboutEachPrefix
	termBagID // last oldTerm
)

// rdfTermKind returns the RDF term kind of the name (ns, local).
// Port of GetRDFTermKind; names are compared by namespace URI, not prefix.
func rdfTermKind(ns string, local []byte) rdfTerm { //nolint:cyclop,gocyclo // one case per RDF syntax term (RDF/XML Syntax §7.2.2–§7.2.4)
	if ns != NSrdf {
		return termOther
	}
	switch string(local) {
	case "li":
		return termLi
	case "parseType":
		return termParseType
	case "Description":
		return termDescription
	case "about":
		return termAbout
	case "resource":
		return termResource
	case "RDF":
		return termRDF
	case "ID":
		return termID
	case "nodeID":
		return termNodeID
	case "datatype":
		return termDatatype
	case "aboutEach":
		return termAboutEach
	case "aboutEachPrefix":
		return termAboutEachPrefix
	case "bagID":
		return termBagID
	}
	return termOther
}

// isCoreSyntaxTerm reports RDF/XML Syntax §7.2.2 coreSyntaxTerms.
func isCoreSyntaxTerm(t rdfTerm) bool { return t >= termRDF && t <= termDatatype }

// isOldTerm reports RDF/XML Syntax §7.2.4 oldTerms.
func isOldTerm(t rdfTerm) bool { return t >= termAboutEach && t <= termBagID }

// isPropertyElementName reports RDF/XML Syntax §7.2.6 propertyElementURIs:
// anyURI - (coreSyntaxTerms | rdf:Description | oldTerms).
func isPropertyElementName(t rdfTerm) bool {
	return t != termDescription && !isOldTerm(t) && !isCoreSyntaxTerm(t)
}

// isNumberedItemName reports an "rdf:_n" container membership name, which the
// Toolkit tolerates in place of rdf:li (IsNumberedArrayItemName).
func isNumberedItemName(local []byte) bool {
	if len(local) < 2 || local[0] != '_' {
		return false
	}
	for _, c := range local[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// nsIX is the namespace of the obsolete iX:changes "punchcard" property,
// which the Toolkit strips on parse.
const nsIX = "http://ns.adobe.com/iX/1.0/"

// dupKey identifies a named child of a node for the duplicate check of
// AddChildNode ("Duplicate property or field node").
type dupKey struct {
	parent   int32
	ns, name string
}

// rdfScratch is the pooled working memory of one Parse call: the element
// tree, the full parse tree, and two lookup maps. Nothing in it is reachable
// from the returned *XMP.
type rdfScratch struct {
	doc      xmlDoc
	nodes    nodeList
	dup      map[dupKey]struct{}
	schemas  map[string]int32
	nsIntern map[string]string
	refs     map[string]struct{} // rdf:nodeID values referenced below the top level
	owners   map[string]string   // prefix → first URI recorded with it
	prefixed map[string]string   // URI → prefix to keep (see notePrefix)
	types    map[string]string   // distinct typed-node type URIs (see addTypeQualifier)
	qset     map[dupKey]struct{} // fixupQualifiedNode's qualifier set, when large
	qlist    []dupKey            // fixupQualifiedNode's qualifier set, when small
	keyBuf   []byte
}

// maxPooledScratch bounds the element/node count of a scratch value that is
// returned to the pool, so one very large document does not pin a large
// buffer for the lifetime of the process.
const maxPooledScratch = 1 << 14

var rdfScratchPool = sync.Pool{New: func() any { //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
	s := &rdfScratch{
		dup:      make(map[dupKey]struct{}),
		schemas:  make(map[string]int32),
		nsIntern: make(map[string]string),
		refs:     make(map[string]struct{}),
		owners:   make(map[string]string),
		prefixed: make(map[string]string),
		types:    make(map[string]string),
		qset:     make(map[dupKey]struct{}),
	}
	s.doc.reset()
	return s
}}

// release clears every transient view of the input held by s and returns s
// to the pool, unless it grew beyond maxPooledScratch. A pooled scratch value
// is therefore always reset and ready for the next document.
func (s *rdfScratch) release() {
	if cap(s.doc.elems) > maxPooledScratch || cap(s.doc.attrs) > maxPooledScratch ||
		cap(s.nodes) > maxPooledScratch || len(s.dup) > maxPooledScratch {
		return
	}
	s.doc.reset()
	clear(s.nodes)
	s.nodes = s.nodes[:0]
	clearMap(s.dup)
	clearMap(s.schemas)
	clearMap(s.nsIntern)
	clearMap(s.refs)
	clearMap(s.owners)
	clearMap(s.prefixed)
	clearMap(s.types)
	clearMap(s.qset)
	clear(s.qlist)
	s.qlist = s.qlist[:0]
	s.keyBuf = s.keyBuf[:0]
	rdfScratchPool.Put(s)
}

// clearMap empties m; most scratch maps stay empty for typical documents.
func clearMap[K comparable, V any](m map[K]V) {
	if len(m) > 0 {
		clear(m)
	}
}

// rdfParser builds the XMP data model tree from an element tree. It is the
// Go counterpart of the Toolkit's RDF_Parser.
type rdfParser struct {
	x *XMP
	d *xmlDoc
	s *rdfScratch
	t nodeList // t[0] is the tree root; its children are the schema nodes

	// about and toolkit are transient until finish interns them.
	about, toolkit string
	aboutSet       bool

	// lastNS and lastSchema cache the most recent schemaFor result:
	// consecutive top-level properties usually share a namespace.
	lastNS     string
	lastSchema int32
	nschemas   int

	// est and composed accumulate, while the tree is built, an upper
	// estimate of the bytes that finish copies into XMP.arena (see
	// arenaNeed); topLen is the name length of the current top-level
	// property, and seenNS the first distinct namespaces counted.
	est, composed int
	topLen        int
	seenNS        [8]string
	nseen         int
}

// parseRDF parses the XMP document body b into x: it builds the element
// tree, runs the RDF parser over it, and then fills x.Properties and the
// retained tree (see finish in project.go).
//
// Compliance: ISO 16684-1:2019 §7 (RDF serialisation of XMP); Adobe XMP
// Specification Part 1 §7.
func parseRDF(b []byte, x *XMP) error {
	s := rdfScratchPool.Get().(*rdfScratch) //nolint:forcetypeassert,revive // rdfScratchPool.New always stores *rdfScratch; pool invariant
	defer s.release()
	if err := scanXML(b, &s.doc); err != nil {
		return err
	}
	p := rdfParser{x: x, d: &s.doc, s: s, t: s.nodes[:0]}
	p.t.add("", "", "", 0) // root
	p.walk(0)
	p.finish(len(b))
	s.nodes = p.t
	return nil
}

// walk finds the rdf:RDF elements below element e and parses their node
// elements. Like the previous parser it also accepts a top-level
// rdf:Description outside rdf:RDF, and it records the x:xmptk attribute of
// x:xmpmeta (XMP Part 1 §7.3.3).
func (p *rdfParser) walk(e int32) { //nolint:cyclop,gocyclo // dispatch over the wrapper elements around rdf:RDF
	for c := p.d.elems[e].firstChild; c != noNode; c = p.d.elems[c].next {
		el := &p.d.elems[c]
		switch el.term {
		case termRDF:
			// RDF/XML Syntax §7.2.9–§7.2.10: rdf:RDF holds a nodeElementList.
			if p.d.hasNodeID {
				for n := el.firstChild; n != noNode; n = p.d.elems[n].next {
					p.collectRefs(n)
				}
			}
			for n := el.firstChild; n != noNode; n = p.d.elems[n].next {
				if id, ok := p.blankID(n); ok {
					p.blankBlock(n, id)
				} else {
					p.nodeElement(0, n, true)
				}
			}
		case termDescription:
			p.nodeElement(0, c, true)
		default:
			if el.ns == NSx && (string(el.local) == "xmpmeta" || string(el.local) == "xapmeta") {
				p.noteToolkit(el)
			}
			p.walk(c)
		}
	}
}

// collectRefs records the rdf:nodeID values used by the descendants of the
// top-level node element e (property elements and nested node elements).
func (p *rdfParser) collectRefs(e int32) {
	for c := p.d.elems[e].firstChild; c != noNode; c = p.d.elems[c].next {
		el := &p.d.elems[c]
		for i := el.attrLo; i < el.attrHi; i++ {
			if a := &p.d.attrs[i]; a.term == termNodeID {
				p.s.refs[a.val] = struct{}{}
			}
		}
		p.collectRefs(c)
	}
}

// blankID reports whether the top-level node element e is the definition of
// a referenced blank node: it carries an rdf:nodeID that some property
// references, and no rdf:about. An unreferenced rdf:nodeID is ignored, as by
// the Toolkit, and the element is parsed as a normal top-level node.
func (p *rdfParser) blankID(e int32) (string, bool) {
	if len(p.s.refs) == 0 {
		return "", false
	}
	el := &p.d.elems[e]
	id, found := "", false
	for i := el.attrLo; i < el.attrHi; i++ {
		a := &p.d.attrs[i]
		switch a.term { //nolint:exhaustive // only rdf:about and rdf:nodeID matter here
		case termAbout:
			return "", false
		case termNodeID:
			id, found = a.val, true
		}
	}
	if !found {
		return "", false
	}
	_, ref := p.s.refs[id]
	return id, ref
}

// blankBlock keeps the referenced blank node element e (see nodeBlank) as a
// root child named id: an rdf:Description becomes a struct-like block whose
// children are its properties, an rdf:Bag/Seq/Alt an array-like block of
// items. Any other element is ignored.
func (p *rdfParser) blankBlock(e int32, id string) {
	el := &p.d.elems[e]
	if el.ns != NSrdf {
		return
	}
	var flags nodeFlags
	switch string(el.local) {
	case "Description":
		flags = nodeStruct
	case "Bag":
		flags = nodeArray
	case "Seq":
		flags = nodeArray | nodeOrdered
	case "Alt":
		flags = nodeArray | nodeOrdered | nodeAlternate
	default:
		return
	}
	b := p.t.add("", id, "", flags|nodeBlank)
	p.t.appendChild(0, b)
	p.est += len(id)
	p.topLen = 0
	p.nodeElementAttrs(b, e, false)
	p.propertyElementList(b, e, false)
}

// noteToolkit records the first x:xmptk attribute — or x:xaptk, its name in
// the pre-standard x:xapmeta wrapper — so that Encode can write it back as
// x:xmptk.
func (p *rdfParser) noteToolkit(el *xelem) {
	if p.toolkit != "" {
		return
	}
	for i := el.attrLo; i < el.attrHi; i++ {
		a := &p.d.attrs[i]
		if a.ns == NSx && (string(a.local) == "xmptk" || string(a.local) == "xaptk") {
			p.toolkit = a.val
			return
		}
	}
}

// nodeElement parses a node element (RDF/XML Syntax §7.2.11).
// Port of RDF_Parser::NodeElement.
func (p *rdfParser) nodeElement(parent, e int32, top bool) {
	el := &p.d.elems[e]
	term := el.term
	if term != termDescription && term != termOther {
		return // "Node element must be rdf:Description or typedNode"
	}
	if top && term == termOther {
		return // "Top level typedNode not allowed"
	}
	p.nodeElementAttrs(parent, e, top)
	p.propertyElementList(parent, e, top)
}

// nodeElementAttrs turns the property attributes of a node element into
// properties or fields. rdf:ID and rdf:nodeID are ignored; rdf:about on a
// top-level node names the tree. Port of RDF_Parser::NodeElementAttrs.
func (p *rdfParser) nodeElementAttrs(parent, e int32, top bool) { //nolint:cyclop,gocyclo // one branch per RDF/XML grammar case, transcribed from the Toolkit
	el := &p.d.elems[e]
	exclusive := false
	for i := el.attrLo; i < el.attrHi; i++ {
		a := &p.d.attrs[i]
		switch a.term {
		case termID, termNodeID, termAbout:
			if exclusive {
				continue // "Mutally exclusive about, ID, nodeID attributes"
			}
			exclusive = true
			switch {
			case top && a.term == termAbout && !p.aboutSet && a.val != "":
				p.aboutSet = true
				p.about = a.val
			case !top && a.term == termNodeID && p.t[parent].flags&nodeBlank == 0:
				// Kept as a reference to a blank node (see nodeBlank).
				p.addQualifier(parent, NSrdf, "nodeID", nil, a.val)
			}
		case termOther:
			// Attributes in the x: namespace, and rdf: attributes on a top-level
			// node, are syntax rather than properties; the previous parser
			// skipped them too.
			if a.ns == NSx || (top && a.ns == NSrdf) {
				continue
			}
			p.addChildNode(parent, a.ns, a.local, a.prefix, a.val, top)
		default:
			// "Invalid nodeElement attribute"
		}
	}
}

// propertyElementList parses every child element of e as a property element
// (RDF/XML Syntax §7.2.13).
func (p *rdfParser) propertyElementList(parent, e int32, top bool) {
	for c := p.d.elems[e].firstChild; c != noNode; c = p.d.elems[c].next {
		p.propertyElement(parent, c, top)
	}
}

// isXMLLang reports whether a is the xml:lang attribute.
func isXMLLang(a *xattr) bool { return a.ns == XMLNamespaceURI && string(a.local) == "lang" }

// isRDFAttr reports whether a is the attribute rdf:<local>.
func isRDFAttr(a *xattr, local string) bool { return a.ns == NSrdf && string(a.local) == local }

// propertyElement dispatches a property element to the parser of its form
// (RDF/XML Syntax §7.2.14). The forms are told apart by their attributes and,
// for the resource and literal forms, by their content.
// Port of RDF_Parser::PropertyElement.
func (p *rdfParser) propertyElement(parent, c int32, top bool) { //nolint:cyclop,gocyclo // one branch per RDF/XML grammar case, transcribed from the Toolkit
	el := &p.d.elems[c]
	if !isPropertyElementName(el.term) {
		return // "Invalid property element name"
	}
	// Leniency kept from the previous parser (finding #180): an attribute
	// without a namespace is not RDF ("XML namespace required for all
	// elements and attributes") and is ignored instead of turning the
	// element into an invalid emptyPropertyElt that drops its content.
	nattrs, rdfAttrs := 0, false
	for i := el.attrLo; i < el.attrHi; i++ {
		if a := &p.d.attrs[i]; a.ns != "" {
			nattrs++
			rdfAttrs = rdfAttrs || a.ns == NSrdf
		}
	}
	if nattrs > 3 {
		// Only an emptyPropertyElt can have more than 3 attributes; the
		// leniency for a text element with non-RDF attributes (see below)
		// applies whatever their number.
		if !rdfAttrs && el.firstChild == noNode && p.d.text(c) != "" {
			p.literalPropertyElement(parent, c, top)
		} else {
			p.emptyPropertyElement(parent, c, top)
		}
		return
	}
	for i := el.attrLo; i < el.attrHi; i++ {
		a := &p.d.attrs[i]
		if a.ns == "" || isXMLLang(a) || a.term == termID {
			continue
		}
		switch {
		case a.term == termDatatype:
			p.literalPropertyElement(parent, c, top)
		case a.ns != NSrdf && el.firstChild == noNode && p.d.text(c) != "":
			// Leniency: RDF allows property attributes only on an empty
			// element, so the Toolkit drops a text element that carries one.
			// ExifTool reads such attributes as qualifiers of the text value,
			// and so does this parser, which keeps the value (finding #180's
			// ex:lang case) and writes the qualifiers back in the standard
			// rdf:value form.
			p.literalPropertyElement(parent, c, top)
		case a.term != termParseType:
			p.emptyPropertyElement(parent, c, top)
		case a.val == "Resource":
			p.parseTypeResourcePropertyElement(parent, c, top)
		case a.val == "Literal" && el.firstChild == noNode:
			// The Toolkit rejects rdf:parseType="Literal"; the previous parser
			// read its text, and so does this one when it holds no markup.
			p.literalPropertyElement(parent, c, top)
		default:
			// "ParseTypeLiteral/Collection/Other property element not allowed"
		}
		return
	}
	// Only rdf:ID and xml:lang: the content decides between the empty,
	// literal and resource forms.
	switch {
	case el.firstChild == noNode && p.d.text(c) == "":
		p.emptyPropertyElement(parent, c, top)
	case el.firstChild == noNode:
		p.literalPropertyElement(parent, c, top)
	default:
		p.resourcePropertyElement(parent, c, top)
	}
}

// resourcePropertyElement parses a property whose value is a node element:
// an array (rdf:Bag/Seq/Alt), a struct (rdf:Description), a typed node, or a
// qualified value written with rdf:value (RDF/XML Syntax §7.2.15;
// ISO 16684-1:2019 §7.6–§7.9). Port of RDF_Parser::ResourcePropertyElement.
func (p *rdfParser) resourcePropertyElement(parent, c int32, top bool) { //nolint:cyclop,gocyclo // one branch per RDF/XML grammar case, transcribed from the Toolkit
	el := &p.d.elems[c]
	if top && el.ns == nsIX && string(el.local) == "changes" {
		return // Strip old "punchcard" chaff.
	}
	nc := p.addChildNode(parent, el.ns, el.local, el.prefix, "", top)
	if nc == noNode {
		return
	}
	for i := el.attrLo; i < el.attrHi; i++ {
		a := &p.d.attrs[i]
		if isXMLLang(a) {
			p.addQualifier(nc, a.ns, transientAliasString(a.local), a.prefix, a.val)
		}
		// rdf:ID is ignored; any other attribute is invalid here.
	}
	if p.d.text(c) != "" {
		return // "Children of resource property element must be XML elements"
	}
	child := &p.d.elems[el.firstChild]
	isRDF := child.ns == NSrdf
	switch {
	case isRDF && string(child.local) == "Bag":
		p.t[nc].flags |= nodeArray
	case isRDF && string(child.local) == "Seq":
		p.t[nc].flags |= nodeArray | nodeOrdered
	case isRDF && string(child.local) == "Alt":
		p.t[nc].flags |= nodeArray | nodeOrdered | nodeAlternate
	default:
		if !isRDF || string(child.local) != "Description" {
			// Typed node (RDF/XML Syntax §7.2.11): add an rdf:type qualifier
			// whose URI value is the element's expanded name.
			if child.ns == "" || !validLocalName(child.local) {
				return // "All XML elements must be in a namespace"
			}
			p.addTypeQualifier(nc, child.ns, child.local)
		}
		p.t[nc].flags |= nodeStruct
	}
	p.nodeElement(nc, el.firstChild, false)
	if p.t[nc].flags&nodeHasValueElem != 0 {
		p.fixupQualifiedNode(nc)
	}
}

// literalPropertyElement parses a property whose value is text
// (RDF/XML Syntax §7.2.16). Port of RDF_Parser::LiteralPropertyElement.
func (p *rdfParser) literalPropertyElement(parent, c int32, top bool) {
	el := &p.d.elems[c]
	nc := p.addChildNode(parent, el.ns, el.local, el.prefix, p.d.text(c), top)
	if nc == noNode {
		return
	}
	for i := el.attrLo; i < el.attrHi; i++ {
		a := &p.d.attrs[i]
		if a.ns == "" || a.ns == NSrdf {
			continue // rdf:ID, rdf:datatype and rdf:parseType are ignored
		}
		// xml:lang, and (leniency, see propertyElement) any other attribute
		// outside the RDF namespace, is a qualifier.
		p.addQualifier(nc, a.ns, transientAliasString(a.local), a.prefix, a.val)
	}
}

// parseTypeResourcePropertyElement parses a struct written with
// rdf:parseType="Resource" (RDF/XML Syntax §7.2.18; ISO 16684-1:2019 §7.7).
// Port of RDF_Parser::ParseTypeResourcePropertyElement.
func (p *rdfParser) parseTypeResourcePropertyElement(parent, c int32, top bool) {
	el := &p.d.elems[c]
	nc := p.addChildNode(parent, el.ns, el.local, el.prefix, "", top)
	if nc == noNode {
		return
	}
	p.t[nc].flags |= nodeStruct
	for i := el.attrLo; i < el.attrHi; i++ {
		a := &p.d.attrs[i]
		if isXMLLang(a) {
			p.addQualifier(nc, a.ns, transientAliasString(a.local), a.prefix, a.val)
		}
		// rdf:parseType and rdf:ID are ignored; any other attribute is invalid.
	}
	p.propertyElementList(nc, c, false)
	if p.t[nc].flags&nodeHasValueElem != 0 {
		p.fixupQualifiedNode(nc)
	}
}

// emptyPropertyElement parses an element without content
// (RDF/XML Syntax §7.2.21). The XMP mapping rules are:
//  1. an rdf:value attribute makes a simple text value; every other
//     attribute is a qualifier;
//  2. an rdf:resource attribute makes a simple URI value; every other
//     attribute is a qualifier;
//  3. with no attribute other than xml:lang, rdf:ID or rdf:nodeID the value
//     is a simple empty text;
//  4. otherwise the element is a struct whose fields are the attributes
//     other than xml:lang, rdf:ID and rdf:nodeID.
//
// Port of RDF_Parser::EmptyPropertyElement.
func (p *rdfParser) emptyPropertyElement(parent, c int32, top bool) { //nolint:cyclop,gocyclo // one branch per RDF attribute kind, transcribed from the Toolkit
	el := &p.d.elems[c]
	if el.firstChild != noNode || p.d.text(c) != "" {
		return // "Nested content not allowed with rdf:resource or property attributes"
	}
	var hasPropertyAttrs, hasResource, hasNodeID, hasValue bool
	valueAttr := noNode
	for i := el.attrLo; i < el.attrHi; i++ {
		a := &p.d.attrs[i]
		if a.ns == "" {
			continue // ignored, see propertyElement
		}
		switch a.term {
		case termID:
		case termResource:
			if hasNodeID || hasValue {
				return
			}
			hasResource = true
			valueAttr = i
		case termNodeID:
			if hasResource {
				return
			}
			hasNodeID = true
		case termOther:
			switch {
			case isRDFAttr(a, "value"):
				if hasResource {
					return
				}
				hasValue = true
				valueAttr = i
			case !isXMLLang(a):
				hasPropertyAttrs = true
			}
		default:
			return // "Unrecognized attribute of empty property element"
		}
	}

	nc := p.addChildNode(parent, el.ns, el.local, el.prefix, "", top)
	if nc == noNode {
		return
	}
	isStruct := false
	switch {
	case hasValue || hasResource:
		p.t[nc].value = p.d.attrs[valueAttr].val
		p.est += len(p.t[nc].value)
		if !hasValue {
			p.t[nc].flags |= nodeURI
		}
	case hasPropertyAttrs:
		p.t[nc].flags |= nodeStruct
		isStruct = true
	}
	for i := el.attrLo; i < el.attrHi; i++ {
		if i == valueAttr {
			continue
		}
		a := &p.d.attrs[i]
		switch a.term {
		case termNodeID:
			// Kept as a reference to a blank node (see nodeBlank).
			p.addQualifier(nc, NSrdf, "nodeID", nil, a.val)
		case termResource:
			p.addQualifier(nc, a.ns, transientAliasString(a.local), a.prefix, a.val)
		case termOther:
			if !isStruct || isXMLLang(a) {
				p.addQualifier(nc, a.ns, transientAliasString(a.local), a.prefix, a.val)
			} else {
				p.addChildNode(nc, a.ns, a.local, a.prefix, a.val, false)
			}
		default:
			// rdf:ID is ignored.
		}
	}
}

// schemaFor returns the schema node of ns, creating it on first use.
func (p *rdfParser) schemaFor(ns string, prefix []byte) int32 {
	if ns == p.lastNS && p.lastSchema > 0 {
		return p.lastSchema
	}
	s := p.findSchema(ns)
	if s == noNode {
		s = p.newSchema(ns, prefix)
	}
	p.lastNS, p.lastSchema = ns, s
	return s
}

// newSchema adds the schema node of ns. Past dupLinearLimit schemas, every
// schema is indexed in rdfScratch.schemas (see findSchema).
func (p *rdfParser) newSchema(ns string, prefix []byte) int32 {
	s := p.t.add(ns, "", "", 0)
	p.t.appendChild(0, s)
	p.nschemas++
	switch {
	case p.nschemas == dupLinearLimit+1:
		for c := p.t[0].firstChild; c != noNode; c = p.t[c].next {
			if p.t[c].flags&nodeBlank == 0 {
				p.s.schemas[p.t[c].ns] = c
			}
		}
	case p.nschemas > dupLinearLimit:
		p.s.schemas[ns] = s
	}
	p.notePrefix(ns, prefix)
	p.countNS(ns)
	return s
}

// findSchema returns the schema node of ns, or noNode. A document has few
// namespaces, so the root's children are scanned linearly until there are
// more than dupLinearLimit schemas, after which rdfScratch.schemas indexes
// them.
func (p *rdfParser) findSchema(ns string) int32 {
	if p.nschemas > dupLinearLimit {
		if s, ok := p.s.schemas[ns]; ok {
			return s
		}
		return noNode
	}
	for c := p.t[0].firstChild; c != noNode; c = p.t[c].next {
		if p.t[c].ns == ns && p.t[c].flags&nodeBlank == 0 {
			return c
		}
	}
	return noNode
}

// dupLinearLimit is the number of named children of one node that the
// duplicate check scans linearly; a node with more children is indexed in
// rdfScratch.dup, so the check stays O(1) for adversarial documents with
// thousands of properties or fields while small nodes avoid hashing.
const dupLinearLimit = 16

// isDuplicate reports whether parent already has a named child (ns, name),
// and records the new child when parent is indexed.
func (p *rdfParser) isDuplicate(parent int32, ns, name string) bool {
	return p.isDupIn(parent, parent, p.t[parent].firstChild, nodeIndexedChildren, ns, name)
}

// isDuplicateQual reports whether parent already has the qualifier
// (ns, name). The lenient scanner accepts an element with a repeated
// attribute (ill-formed per XML 1.0 §3.1, "Unique Att Spec"); the first one
// wins, so a written packet never repeats an attribute either.
func (p *rdfParser) isDuplicateQual(parent int32, ns, name string) bool {
	return p.isDupIn(parent, -parent-1, p.t[parent].firstQual, nodeIndexedQuals, ns, name) // qualifier lists use negative keys
}

// isDupIn implements isDuplicate and isDuplicateQual for the list of node
// owner starting at first, indexed in rdfScratch.dup under key (and marked
// with flag on owner) once it grows past dupLinearLimit.
func (p *rdfParser) isDupIn(owner, key, first int32, flag nodeFlags, ns, name string) bool {
	k := dupKey{parent: key, ns: ns, name: name}
	if p.t[owner].flags&flag != 0 {
		if _, dup := p.s.dup[k]; dup {
			return true
		}
		p.s.dup[k] = struct{}{}
		return false
	}
	n := 0
	for c := first; c != noNode; c = p.t[c].next {
		if p.t[c].name == name && p.t[c].ns == ns {
			return true
		}
		n++
	}
	if n >= dupLinearLimit {
		p.t[owner].flags |= flag
		for c := first; c != noNode; c = p.t[c].next {
			p.s.dup[dupKey{parent: key, ns: p.t[c].ns, name: p.t[c].name}] = struct{}{}
		}
		p.s.dup[k] = struct{}{}
	}
	return false
}

// addChildNode adds a property, field or array item named (ns, local) under
// parent and returns its index, or noNode when the RDF is invalid at this
// point: an element without a namespace, rdf:li outside an array, a named
// child of an array, a duplicate named child, or a misplaced rdf:value.
// For a top-level property, parent is the tree root and the node is added to
// the schema of ns. Port of RDF_Parser::AddChildNode.
func (p *rdfParser) addChildNode(parent int32, ns string, local, prefix []byte, value string, top bool) int32 { //nolint:cyclop,gocyclo // one branch per RDF/XML grammar case, transcribed from the Toolkit
	if ns == "" {
		return noNode // "XML namespace required for all elements and attributes"
	}
	if !validLocalName(local) {
		return noNode // not an XML NCName; the writer could not reproduce it
	}
	if top {
		parent = p.schemaFor(ns, prefix)
	}
	isArrayParent := p.t[parent].isArray()
	isItem := ns == NSrdf && string(local) == "li"
	isValue := ns == NSrdf && string(local) == "value"
	switch {
	case isItem:
		if !isArrayParent {
			return noNode // "Misplaced rdf:li element"
		}
	case isArrayParent:
		// Tolerate rdf:_n; do not verify the order.
		if ns != NSrdf || !isNumberedItemName(local) {
			return noNode // "Array items cannot have arbitrary child names"
		}
		isItem = true
	}

	name := transientAliasString(local)
	if !isItem && !isValue && p.isDuplicate(parent, ns, name) {
		return noNode // "Duplicate property or field node"
	}
	if isValue {
		if top || !p.t[parent].isStruct() {
			return noNode // "Misplaced rdf:value element"
		}
		p.t[parent].flags |= nodeHasValueElem
	}

	var idx int32
	if isItem {
		idx = p.t.add("", "", value, nodeItem)
		p.est += 2*len(value) + 8 // the item, and its copy in the joined value
	} else {
		idx = p.t.add(ns, name, value, 0)
		p.noteNamed(parent, ns, name, value, prefix, top)
	}
	if isValue && p.t[parent].firstChild != noNode {
		p.t.prependChild(parent, idx)
	} else {
		p.t.appendChild(parent, idx)
	}
	return idx
}

// noteNamed accounts a new named node (property or field) in the arena
// estimate and records the prefix of a field in a foreign namespace; a
// top-level property is noted with its schema.
func (p *rdfParser) noteNamed(parent int32, ns, name, value string, prefix []byte, top bool) {
	p.est += len(name) + len(value)
	if top {
		p.topLen = len(name)
		return
	}
	p.composed += p.topLen + len(name) + 24 // "P[N].F"
	if ns != p.t[parent].ns {
		p.notePrefix(ns, prefix)
		p.countNS(ns)
	}
}

// addQualifier adds the qualifier (ns, name) = value to parent and returns
// its index, or noNode when ns is empty. xml:lang is kept first and rdf:type
// right after it. Port of RDF_Parser::AddQualifierNode.
func (p *rdfParser) addQualifier(parent int32, ns, name string, prefix []byte, value string) int32 {
	if ns == "" {
		return noNode // "XML namespace required for all elements and attributes"
	}
	if !validLocalName(transientAliasBytes(name)) || p.isDuplicateQual(parent, ns, name) {
		return noNode
	}
	q := p.t.add(ns, name, value, 0)
	p.est += len(name) + len(value) + 8 // a short lang prefix in a joined value
	switch {
	case ns == XMLNamespaceURI && name == "lang":
		p.t.prependQual(parent, q)
	case ns == NSrdf && name == "type":
		at := p.t[parent].firstQual
		if at != noNode && !p.t[at].isLangQual() {
			at = noNode
		}
		p.t.insertQualAfter(parent, at, q)
	default:
		p.t.appendQual(parent, q)
		p.notePrefix(ns, prefix)
		p.countNS(ns)
	}
	return q
}

// validLocalName reports whether local can be the local part of an element
// or attribute name that the writer reproduces unchanged: non-empty, and
// free of the bytes that XML 1.0 §2.3 excludes from an NCName and that
// writeXMLName (write.go) strips — C0 controls, '<', '>', '&', '"', the
// single quote, '/', '=' and a second ':'. The lenient scanner can deliver such a name
// from ill-formed input; dropping it keeps Parse and Encode in agreement.
func validLocalName(local []byte) bool {
	if len(local) == 0 {
		return false
	}
	for _, c := range local {
		if invalidNameByte[c] {
			return false
		}
	}
	return true
}

// invalidNameByte marks the bytes rejected by validLocalName.
var invalidNameByte = func() (t [256]bool) { //nolint:gochecknoglobals // read-only table built once at package initialisation
	for c := range 0x20 {
		t[c] = true
	}
	for _, c := range []byte{'<', '>', '&', '"', '\'', '/', '=', ':'} {
		t[c] = true
	}
	return t
}()

// countNS adds ns to the arena estimate once, for the first len(seenNS)
// distinct namespaces; any further one is still interned correctly, at the
// cost of an arena regrowth.
func (p *rdfParser) countNS(ns string) {
	if slices.Contains(p.seenNS[:p.nseen], ns) {
		return
	}
	if p.nseen < len(p.seenNS) {
		p.seenNS[p.nseen] = ns
		p.nseen++
		p.est += len(ns)
	}
}

// addTypeQualifier adds the rdf:type qualifier of a typed node, whose URI
// value is the node element's namespace followed by its local name. Each
// distinct type URI is copied into XMP.arena once, however many nodes use
// it: a document that declares a long namespace once and types many items
// with it must not make the arena grow with items × URI length (the class
// of XMPARENA-NS-REINTERN-01).
func (p *rdfParser) addTypeQualifier(nc int32, ns string, local []byte) {
	typeName := ns + string(local)
	v, seen := p.s.types[typeName]
	if !seen {
		v = typeName
		p.s.types[v] = v
	}
	q := p.addQualifier(nc, NSrdf, "type", nil, v)
	if q == noNode {
		return
	}
	p.t[q].flags |= nodeURI
	if seen {
		p.est -= len(v) // counted by addQualifier, but copied once (fixNode)
	}
}

// fixupQualifiedNode turns a struct that holds an rdf:value field into the
// qualified value it encodes (ISO 16684-1:2019 §7.9.2.5): the rdf:value field
// supplies the form, value and children, and every other field becomes a
// qualifier. Port of RDF_Parser::FixupQualifiedNode, with the same result
// order — xml:lang first, then the parent's other qualifiers, the rdf:value
// node's qualifiers, and the former fields — and the same precedence: a
// qualifier of the rdf:value node replaces one of the parent with the same
// name, and a field that repeats an existing qualifier is dropped. The
// duplicate checks use a set instead of the Toolkit's linear searches, so a
// struct with many fields stays linear.
func (p *rdfParser) fixupQualifiedNode(parent int32) { //nolint:cyclop,gocyclo // the four qualifier sources and their precedence, transcribed from the Toolkit
	t := p.t
	vn := t[parent].firstChild // addChildNode put rdf:value first
	pq, vq := t[parent].firstQual, t[vn].firstQual
	t[parent].firstQual, t[parent].lastQual = noNode, noNode
	t[parent].flags &^= nodeIndexedQuals // the parent's qualifier list is rebuilt below
	set := qualSet{s: p.s}
	defer set.reset()

	// xml:lang: the rdf:value node's wins.
	lang := noNode
	for q := vq; q != noNode && lang == noNode; q = t[q].next {
		if t[q].isLangQual() {
			lang = q
		}
	}
	for q := pq; q != noNode && lang == noNode; q = t[q].next {
		if t[q].isLangQual() {
			lang = q
		}
	}
	if lang != noNode {
		set.add(t, lang)
		t.appendQual(parent, lang)
	}
	// The rdf:value node's other qualifiers replace the parent's.
	for q := vq; q != noNode; q = t[q].next {
		if !t[q].isLangQual() {
			set.add(t, q)
		}
	}
	for q := pq; q != noNode; {
		nq := t[q].next
		if !t[q].isLangQual() && !set.has(t, q) {
			set.add(t, q)
			t.appendQual(parent, q)
		}
		q = nq
	}
	for q := vq; q != noNode; {
		nq := t[q].next
		if !t[q].isLangQual() {
			t.appendQual(parent, q)
		}
		q = nq
	}
	// The other fields become qualifiers unless they repeat one.
	for c := t[vn].next; c != noNode; {
		nc := t[c].next
		if !set.has(t, c) {
			set.add(t, c)
			if t[c].isLangQual() {
				t.prependQual(parent, c)
			} else {
				t.appendQual(parent, c)
			}
		}
		c = nc
	}

	keep := t[parent].flags &^ (nodeStruct | nodeHasValueElem | nodeArrayMask | nodeURI)
	t[parent].flags = keep | t[vn].flags&(nodeStruct|nodeArrayMask|nodeURI)
	t[parent].value = t[vn].value
	t[parent].firstChild, t[parent].lastChild = t[vn].firstChild, t[vn].lastChild
}

// qualSet is the set of qualifier names of one fixupQualifiedNode call: a
// short list scanned linearly, spilling to a map past dupLinearLimit names.
type qualSet struct {
	s *rdfScratch
}

func (q qualSet) has(t nodeList, i int32) bool {
	k := dupKey{ns: t[i].ns, name: t[i].name}
	if len(q.s.qlist) <= dupLinearLimit {
		return slices.Contains(q.s.qlist, k)
	}
	_, ok := q.s.qset[k]
	return ok
}

func (q qualSet) add(t nodeList, i int32) {
	k := dupKey{ns: t[i].ns, name: t[i].name}
	q.s.qlist = append(q.s.qlist, k)
	switch {
	case len(q.s.qlist) == dupLinearLimit+1:
		for _, e := range q.s.qlist {
			q.s.qset[e] = struct{}{}
		}
	case len(q.s.qlist) > dupLinearLimit:
		q.s.qset[k] = struct{}{}
	}
}

func (q qualSet) reset() {
	clear(q.s.qlist)
	q.s.qlist = q.s.qlist[:0]
	clearMap(q.s.qset)
}

// notePrefix records the prefix that the document binds to ns, for a
// namespace without a well-known prefix, so that Encode can reuse it instead
// of generating nsN (task #303: tools such as ExifTool name groups after
// the prefix of an unknown namespace).
func (p *rdfParser) notePrefix(ns string, prefix []byte) {
	if len(prefix) == 0 {
		return
	}
	if _, known := prefixMap[ns]; known {
		return
	}
	if _, seen := p.s.prefixed[ns]; seen {
		return
	}
	// A prefix that the document rebinds to another unknown URI stays with
	// the URI that used it first; the later URI gets a generated prefix.
	if owner, taken := p.s.owners[string(prefix)]; taken && owner != ns {
		return
	}
	pfx := transientAliasString(prefix)
	p.s.owners[pfx] = ns
	p.s.prefixed[ns] = pfx
}

// internNS interns a namespace URI once per Parse call, so that a URI used
// by many nodes is copied into XMP.arena only once (XMPARENA-NS-REINTERN-01).
func (p *rdfParser) internNS(ns string) string {
	if ns == "" {
		return ""
	}
	// A schema namespace is interned once, in the schema node (schemaNS).
	if s := p.findSchema(ns); s != noNode {
		return p.schemaNS(s)
	}
	if v, ok := p.s.nsIntern[ns]; ok {
		return v
	}
	v := p.x.intern(ns)
	p.s.nsIntern[v] = v
	return v
}
