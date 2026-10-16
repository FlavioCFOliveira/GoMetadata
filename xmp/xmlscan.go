package xmp

// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/ExpatAdapter.cpp:SetQualName, Copyright 2005 Adobe, SPDX-License-Identifier: BSD-3-Clause
// Adapted from Adobe XMP-Toolkit-SDK source/XML_Node.cpp:XML_Node::IsWhitespaceNode, Copyright 2004 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on this file. Only the
// compatibility mapping of an unqualified about/ID attribute of
// rdf:Description and the white-space test of hasText are adapted; the
// scanner itself is original.

import (
	"bytes"
	"fmt"
	"slices"
)

// maxXMLDepth is the maximum element nesting depth accepted by the scanner.
// A deeper document is rejected with ErrXMLNestingDepth. The limit bounds
// the recursion of the RDF parser (parserdf.go) and of the serialiser.
const maxXMLDepth = 100

// xelem is one element of the transient element tree built by scanXML.
//
// Every []byte and string field is a TRANSIENT view of the input buffer (or
// a heap string produced by entity decoding) and is valid only during the
// synchronous Parse call that built it; see transientAliasString's contract.
// Nothing in an xelem is ever stored in an *XMP without being interned first.
type xelem struct {
	prefix, local []byte  // qualified name split at the first ':'
	ns            string  // namespace URI resolved for prefix ("" when unbound)
	term          rdfTerm // RDF syntax term of the name (termOther outside rdf:)

	// The character data of the element is decoded on demand by
	// xmlDoc.text: most elements (rdf:RDF, rdf:Description, containers)
	// carry only whitespace that the RDF parser never reads. textPos is the
	// offset of the first run (after the start tag; -1 for an empty-element
	// tag), extra any CDATA found after later markup, and text the cache.
	textPos  int32
	textDone bool
	extra    string
	text     string

	attrLo, attrHi int32 // attributes are xmlDoc.attrs[attrLo:attrHi]

	firstChild, lastChild, next int32 // child element links; noNode terminates
}

// xattr is one non-namespace-declaration attribute of an xelem.
type xattr struct {
	prefix, local []byte
	ns            string // namespace URI resolved for prefix ("" for an unprefixed attribute)
	val           string
	term          rdfTerm // RDF syntax term of the name (termOther outside rdf:)
}

// nsBinding is one in-scope namespace declaration (xmlns:prefix="uri").
type nsBinding struct {
	prefix []byte
	uri    string
}

// openElem records an element that is open on the scanner stack together
// with the length of the namespace scope before its own declarations, so the
// scope can be restored when the element closes (XML Namespaces §6.1).
type openElem struct {
	elem     int32
	scopeLen int32
}

// xmlDoc is the transient element tree of one XMP document. It plays the
// role of the XML_Node tree that the Adobe XMP Toolkit builds with Expat
// before its RDF parser runs, which gives the RDF parser the look-ahead it
// needs to tell the property element forms apart (RDF/XML Syntax §7.2.14).
//
// All slices grow on demand, so neither the number of attributes on one
// element nor the number of namespace declarations in scope is capped
// (task #283). elems[0] is a virtual document node whose children are the
// top-level elements.
type xmlDoc struct {
	src   []byte // the document being scanned (transient, see transientAliasString)
	elems []xelem
	attrs []xattr
	scope []nsBinding
	open  []openElem

	// hasNodeID records that some attribute is named nodeID, so the RDF
	// parser looks for rdf:nodeID references only in documents that can
	// have them.
	hasNodeID bool

	// inFirstRun is true between a start tag and the next markup other than
	// a CDATA section: a CDATA section met in that span belongs to the
	// element's first character-data run, which xmlDoc.text decodes.
	inFirstRun bool
}

// text returns the character data of element e: its first run, entity-decoded
// and verbatim, followed by any CDATA section found after later markup. It
// is computed once, on first use.
//
// The text is not trimmed: XMP Part 1 §7.5 and XML 1.0 §2.10
// pass all white space of an element's character data to the application, and
// the Adobe XMP Toolkit's LiteralPropertyElement stores it unchanged
// (RDF_Parser::LiteralPropertyElement, XMPCore/source/ParseRDF.cpp). Line
// ends are normalised to U+000A as XML 1.0 §2.11 requires (see
// normaliseEOL).
func (d *xmlDoc) text(e int32) string {
	el := &d.elems[e]
	if el.textDone {
		return el.text
	}
	el.textDone = true
	if el.textPos >= 0 {
		_, el.text = collectTextContent(d.src, int(el.textPos))
	}
	if el.extra != "" {
		if el.text == "" {
			el.text = el.extra
		} else {
			el.text += el.extra
		}
	}
	return el.text
}

// hasText reports whether the character data of element e contains anything
// but white space. It is the Go counterpart of the Toolkit's
// XML_Node::IsWhitespaceNode test: the white space between the child elements
// of a node is formatting, not content, and the RDF parser must ignore it
// (RDF/XML Syntax §7.2.14: resourcePropertyElt ::= ws* nodeElement ws*),
// while the same white space is part of the value of a literal property.
func (d *xmlDoc) hasText(e int32) bool {
	for _, c := range []byte(d.text(e)) {
		if !isASCIISpace(c) {
			return true
		}
	}
	return false
}

// reset prepares d for a new document. It clears the previous content first
// so that no transient view of an earlier input buffer stays reachable from a
// pooled xmlDoc (finding XMPBUF-RETAIN-01).
func (d *xmlDoc) reset() {
	clear(d.elems)
	clear(d.attrs)
	clear(d.scope)
	d.src = nil
	d.inFirstRun = false
	d.elems = d.elems[:0]
	d.attrs = d.attrs[:0]
	d.scope = d.scope[:0]
	d.open = d.open[:0]
	d.hasNodeID = false
	d.elems = append(d.elems, xelem{firstChild: noNode, lastChild: noNode, next: noNode, textPos: -1})
	// XML Namespaces §3: the 'xml' prefix is bound to XMLNamespaceURI by
	// definition and needs no declaration, so xml:lang always resolves.
	d.scope = append(d.scope, nsBinding{prefix: xmlPrefixBytes, uri: XMLNamespaceURI})
}

// canonicalNS returns the package constant for the namespaces that the RDF
// parser compares against, so that those comparisons find identical string
// data and finish without comparing bytes; any other URI is returned as is.
func canonicalNS(uri string) string {
	switch uri {
	case NSrdf:
		return NSrdf
	case XMLNamespaceURI:
		return XMLNamespaceURI
	case NSx:
		return NSx
	}
	return uri
}

// resolve returns the URI bound to prefix by the innermost in-scope
// declaration. An empty prefix has no namespace (XMP does not use default
// namespaces, so an unprefixed name is in none) and resolves to ("", true);
// a prefix that no in-scope declaration binds resolves to ("", false), the
// violation of Namespaces in XML 1.0 §5, Namespace Constraint "Prefix
// Declared" (the 'xml' prefix is always bound, see reset).
func (d *xmlDoc) resolve(prefix []byte) (uri string, bound bool) {
	if len(prefix) == 0 {
		return "", true
	}
	for _, b := range slices.Backward(d.scope) {
		// Length and first byte reject most bindings before bytes.Equal.
		if q := b.prefix; len(q) == len(prefix) && q[0] == prefix[0] && bytes.Equal(q, prefix) {
			return b.uri, true
		}
	}
	return "", false
}

// scanXML builds the element tree of b into d.
//
// The scanner enforces the well-formedness constraints on which the element
// tree depends (XML 1.0 §3, "Well-Formedness Constraint: Element Type
// Match"; XMP Part 1 §7.1, p. 9: an XMP packet is a well-formed XML
// document): every end tag must carry the name of the innermost open start
// tag, no end tag may appear without an open element, no start tag may be
// malformed, and no element may be left open at the end of the input. A
// violation makes scanXML return an error that wraps ErrXMLNotWellFormed and
// names the element, so that a document is never read as a tree that its
// author did not write (the Adobe XMP Toolkit stops at the first violation
// too: Expat reports it and ExpatAdapter::ParseBuffer throws kXMPErr_BadXML).
// A nesting depth above maxXMLDepth is rejected with ErrXMLNestingDepth.
//
// The scanner stays lenient about everything that does not change the tree:
// comments, processing instructions and DOCTYPE declarations are skipped
// without interpretation, so no entity is ever expanded from a DTD
// (anti-XXE); unterminated comments end the scan.
func scanXML(b []byte, d *xmlDoc) error { //nolint:cyclop,gocyclo // one branch per XML markup kind (XML 1.0 §2.4–§2.8)
	d.src = b
	pos := 0
	for pos < len(b) {
		i := bytes.IndexByte(b[pos:], '<')
		if i < 0 {
			break
		}
		pos += i + 1 // b[pos] is the byte after '<'
		if pos >= len(b) {
			break
		}

		// XML 1.0 §2.7: a CDATA section found outside the text run that
		// follows a start tag (for example after a comment) still belongs to
		// the character data of the innermost open element.
		if isCDATA(b, pos) {
			content, after := parseCDATA(b, pos)
			pos = after
			if !d.inFirstRun && len(content) > 0 && len(d.open) > 0 {
				e := &d.elems[d.open[len(d.open)-1].elem]
				content = normaliseEOL(content)
				if e.extra == "" {
					e.extra = transientAliasString(content)
				} else {
					e.extra += string(content)
				}
			}
			continue
		}

		d.inFirstRun = false
		if newPos, skipped := skipSpecialTag(b, pos); skipped {
			pos = newPos
			continue
		}

		if b[pos] == '/' {
			end := bytes.IndexByte(b[pos:], '>')
			if end < 0 {
				break // truncated end tag; the element it closes stays open
			}
			// XML 1.0 §3.1 (production 42): ETag ::= '</' Name S? '>'.
			name := b[pos+1 : pos+end]
			for len(name) > 0 && isASCIISpace(name[len(name)-1]) {
				name = name[:len(name)-1]
			}
			pos += end + 1
			if err := d.closeElem(name); err != nil {
				return err
			}
			continue
		}

		var err error
		if pos, err = d.startElem(b, pos); err != nil {
			return err
		}
	}
	if n := len(d.open); n > 0 {
		// XML 1.0 §3: every start tag must be matched by an end tag.
		return fmt.Errorf("%w: element %q is not closed", ErrXMLNotWellFormed, qualifiedName(&d.elems[d.open[n-1].elem]))
	}
	return nil
}

// errUnboundPrefix reports a name whose prefix no namespace declaration in
// scope binds: Namespaces in XML 1.0 §5, Namespace Constraint "Prefix
// Declared" (ISO 16684-1:2019 §7.1 and XMP Part 1 §7.1 require the packet to
// be well-formed XML with namespaces). kind is "element" or "attribute". It
// allocates, and runs only on an ill-formed document.
func errUnboundPrefix(kind string, prefix, local []byte) error {
	return fmt.Errorf("%w: namespace prefix %q of %s %q is not declared", ErrXMLNotWellFormed, prefix, kind, string(prefix)+":"+string(local))
}

// qualifiedName returns the qualified name "prefix:local" of e as written in
// the source. It allocates, and is meant for error messages only.
func qualifiedName(e *xelem) string {
	if len(e.prefix) == 0 {
		return string(e.local)
	}
	return string(e.prefix) + ":" + string(e.local)
}

// nameMatches reports whether name, the name of an end tag, is the qualified
// name of e. XML 1.0 §3 (Element Type Match) compares the Names themselves,
// prefix included, so that the comparison needs neither the namespace nor
// any allocation.
func (e *xelem) nameMatches(name []byte) bool {
	if len(e.prefix) == 0 {
		return bytes.Equal(name, e.local)
	}
	n := len(e.prefix)
	return len(name) == n+1+len(e.local) && name[n] == ':' &&
		bytes.Equal(name[:n], e.prefix) && bytes.Equal(name[n+1:], e.local)
}

// closeElem closes the innermost open element on the end tag named name and
// restores the namespace scope that was in effect before it. It fails when no
// element is open or when name is not the name of the innermost open element
// (XML 1.0 §3, Well-Formedness Constraint: Element Type Match).
func (d *xmlDoc) closeElem(name []byte) error {
	n := len(d.open)
	if n == 0 {
		return fmt.Errorf("%w: end tag %q has no matching start tag", ErrXMLNotWellFormed, name)
	}
	top := d.open[n-1]
	if e := &d.elems[top.elem]; !e.nameMatches(name) {
		return fmt.Errorf("%w: end tag %q does not match start tag %q", ErrXMLNotWellFormed, name, qualifiedName(e))
	}
	clear(d.scope[top.scopeLen:])
	d.scope = d.scope[:top.scopeLen]
	d.open = d.open[:n-1]
	return nil
}

// startElem parses the start tag (or empty-element tag) that begins at
// b[pos], the byte after '<'. It records the element, its attributes and its
// namespace declarations, and the first run of character data that follows
// the tag. It returns the position of the next markup.
func (d *xmlDoc) startElem(b []byte, pos int) (int, error) { //nolint:cyclop,gocyclo // start tag, attributes, namespace resolution, and the empty-element form
	if len(d.open)+1 > maxXMLDepth {
		return 0, ErrXMLNestingDepth
	}
	scopeLen := int32(len(d.scope)) //nolint:gosec // G115: bounded by the 16 MiB document cap
	attrLo := int32(len(d.attrs))   //nolint:gosec // G115: bounded by the 16 MiB document cap

	prefix, local, pos := scanName(b, pos)
	if len(prefix) == 0 && len(local) == 0 {
		// XML 1.0 §3.1 (production 40): a start tag begins with a Name. (A
		// name such as "q:" is a Name too; the RDF parser drops it later.)
		return 0, fmt.Errorf("%w: start tag without an element name", ErrXMLNotWellFormed)
	}
	pos = d.scanAttrs(b, pos)

	// XML Namespaces §6.1: the scope of a declaration includes the element
	// that carries it, so names are resolved only after every attribute of
	// the start tag has been read — a declaration may follow its first use.
	attrHi := int32(len(d.attrs)) //nolint:gosec // G115: bounded by the 16 MiB document cap
	for i := attrLo; i < attrHi; i++ {
		a := &d.attrs[i]
		var bound bool
		if a.ns, bound = d.resolve(a.prefix); !bound {
			return 0, errUnboundPrefix("attribute", a.prefix, a.local)
		}
		a.term = rdfTermKind(a.ns, a.local)
	}

	selfClose := false
	if pos < len(b) && b[pos] == '/' {
		selfClose = true
		pos++
	}
	// XML 1.0 §3.1 (productions 40 and 44): a start tag ends with '>' and an
	// empty-element tag with '/>'. Anything else here (a '/' inside a name, a
	// '<' inside the tag) is not well-formed. A tag cut off by the end of the
	// input is left to the open-element check of scanXML.
	if pos < len(b) {
		if b[pos] != '>' {
			return 0, fmt.Errorf("%w: malformed start tag of element %q", ErrXMLNotWellFormed, qualifiedName(&xelem{prefix: prefix, local: local}))
		}
		pos++
	}

	ns, bound := d.resolve(prefix)
	if !bound {
		return 0, errUnboundPrefix("element", prefix, local)
	}
	if ns == NSrdf && string(local) == "Description" {
		// Compatibility with pre-standard XMP (ExpatAdapter.cpp:SetQualName):
		// an unqualified "about" or "ID" attribute of rdf:Description means
		// rdf:about or rdf:ID. Early Adobe writers emitted that form.
		for i := attrLo; i < attrHi; i++ {
			if a := &d.attrs[i]; a.ns == "" && (string(a.local) == "about" || string(a.local) == "ID") {
				a.ns = NSrdf
				a.term = rdfTermKind(a.ns, a.local)
			}
		}
	}

	idx := int32(len(d.elems)) //nolint:gosec // G115: bounded by the 16 MiB document cap
	parent := int32(0)
	if n := len(d.open); n > 0 {
		parent = d.open[n-1].elem
	}
	d.elems = append(d.elems, xelem{
		prefix: prefix, local: local, ns: ns, term: rdfTermKind(ns, local),
		attrLo: attrLo, attrHi: attrHi,
		firstChild: noNode, lastChild: noNode, next: noNode,
		textPos: -1,
	})
	p := &d.elems[parent]
	if p.lastChild == noNode {
		p.firstChild = idx
	} else {
		d.elems[p.lastChild].next = idx
	}
	p.lastChild = idx

	if selfClose {
		clear(d.scope[scopeLen:])
		d.scope = d.scope[:scopeLen]
		return pos, nil
	}
	d.open = append(d.open, openElem{elem: idx, scopeLen: scopeLen})
	d.elems[idx].textPos = int32(pos) //nolint:gosec // G115: bounded by the 16 MiB document cap
	d.inFirstRun = true
	return pos, nil
}

// scanAttrs parses the attributes of a start tag beginning at b[pos] up to
// the closing '>' or '/'. Namespace declarations are pushed onto the scope;
// every other attribute is appended to d.attrs with its namespace still
// unresolved. Neither list is capped (task #283).
func (d *xmlDoc) scanAttrs(b []byte, pos int) int { //nolint:cyclop,gocyclo // attribute list grammar (XML 1.0 §3.1) and the three attribute kinds
	// XML 1.0 §3.1: the attribute list ends at '>' or '/'. A '<' inside a tag
	// is illegal (XML 1.0 §2.4) and also ends the list, which guarantees
	// progress on malformed input.
	for pos < len(b) && b[pos] != '>' && b[pos] != '/' && b[pos] != '<' {
		for pos < len(b) && isASCIISpace(b[pos]) {
			pos++
		}
		if pos >= len(b) || b[pos] == '>' || b[pos] == '/' || b[pos] == '<' {
			break
		}
		attrPrefix, attrLocal, val, newPos, ok := parseSingleAttr(b, pos)
		pos = newPos
		if !ok {
			continue
		}
		switch {
		case string(attrPrefix) == "xmlns":
			// xmlns:prefix="uri" — XML Namespaces §3.
			d.scope = append(d.scope, nsBinding{prefix: attrLocal, uri: canonicalNS(val)})
		case len(attrPrefix) == 0 && string(attrLocal) == "xmlns":
			// xmlns="uri" — a default namespace; XMP never relies on it.
		default:
			if string(attrLocal) == "nodeID" {
				d.hasNodeID = true
			}
			d.attrs = append(d.attrs, xattr{prefix: attrPrefix, local: attrLocal, val: val})
		}
	}
	return pos
}
