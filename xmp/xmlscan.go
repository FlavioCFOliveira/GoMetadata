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
	"hash/maphash"
	"slices"
	"strings"
)

// maxXMLDepth is the maximum element nesting depth accepted by the scanner.
// A deeper document is rejected with ErrXMLNestingDepth. The limit bounds
// the recursion of the RDF parser (parserdf.go) and of the serialiser.
const maxXMLDepth = 100

// maxXMLElements and maxXMLAttrs bound the element and attribute tree that
// scanXML builds. The tree costs about 140 bytes per element and 90 per
// attribute, so a 15 MiB packet of "<a/>" used to make Parse allocate 3 GiB
// (security audit F-05). A real packet is orders of magnitude below both: the
// largest legitimate XMP, a Lightroom history or a face-region list, holds
// tens of thousands of elements, and ISO 16684-1 sets no limit. A larger
// document is rejected with ErrXMLTooComplex. Reference: the Adobe XMP Toolkit
// bounds the same tree through the 16 MiB packet cap the caller applies, and
// libxml2 rejects documents past its own node limits the same way.
const (
	maxXMLElements = 1 << 19
	maxXMLAttrs    = 1 << 20
)

// growDoubling makes room for one more element of s by doubling its capacity,
// never beyond limit elements. append alone grows a large slice by only 25%,
// which copies about five times the final size on the way to a table of
// maxXMLElements entries.
func growDoubling[T any](s []T, limit int) []T {
	if len(s) < cap(s) {
		return s
	}
	return slices.Grow(s, max(min(len(s), limit-len(s)), 16))
}

// nsIndexThreshold is the number of in-scope namespace bindings above which
// resolve switches from a backward linear scan to a hash index (security audit
// F-04: N declarations on one element and N attributes made resolution
// O(N^2)). A real packet declares a few dozen prefixes, so ordinary documents
// never build the index and keep the allocation-free linear scan.
const nsIndexThreshold = 32

// maxPooledNSBuckets bounds the capacity of the binding index kept with a
// pooled xmlDoc.
const maxPooledNSBuckets = 1 << 12

// nsInitialBuckets is the number of buckets the namespace binding index starts
// with; it doubles whenever the scope holds more than two bindings per bucket,
// so a bucket chain stays short however many declarations a packet makes.
const nsInitialBuckets = 128

// nsSeed seeds the hash of the binding index, so that an attacker cannot
// choose prefixes that all land in one bucket.
var nsSeed = maphash.MakeSeed() //nolint:gochecknoglobals // immutable per-process hash seed

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
	text     string

	// cdataFirst and cdataLast delimit the chain of the CDATA sections that
	// follow later markup (xmlDoc.cdata); noNode when there are none. They
	// are joined once by xmlDoc.text, never concatenated one by one (security
	// audit F-10: repeated string concatenation was quadratic).
	cdataFirst, cdataLast int32

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
	// prev is the index of the previous binding in the same hash bucket, and
	// hash the seeded hash of the prefix; both are set only while the binding
	// index of the document is active (xmlDoc.nsIndexed).
	prev int32
	hash uint32
}

// cdataPart is one CDATA section of an element, linked to the next one.
type cdataPart struct {
	content []byte // transient view of the input, or its EOL-normalised copy
	next    int32  // next part of the same element; noNode terminates
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
// The slices grow on demand: the attributes of one element and the namespace
// declarations in scope are not capped individually (task #283), but the whole
// document is bounded by maxXMLElements and maxXMLAttrs (security audit F-05).
// elems[0] is a virtual document node whose children are the top-level
// elements.
type xmlDoc struct {
	src   []byte // the document being scanned (transient, see transientAliasString)
	elems []xelem
	attrs []xattr
	scope []nsBinding
	open  []openElem
	cdata []cdataPart

	// nsHead and nsIndexed implement the binding index: nsHead[h&(len-1)] is
	// the innermost in-scope binding of the bucket of hash h, or noNode. The
	// index is built when the scope first exceeds nsIndexThreshold.
	nsHead    []int32
	nsMask    uint32 // len(nsHead) - 1; the length is a power of two
	nsIndexed bool

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
	if el.cdataFirst != noNode {
		el.text = d.joinCDATA(el.text, el.cdataFirst)
	}
	return el.text
}

// joinCDATA returns head followed by the CDATA sections of the chain that
// starts at part first, built with a single allocation (none when head is
// empty and the chain has one part).
func (d *xmlDoc) joinCDATA(head string, first int32) string {
	if head == "" && d.cdata[first].next == noNode {
		return transientAliasString(d.cdata[first].content)
	}
	n := len(head)
	for p := first; p != noNode; p = d.cdata[p].next {
		n += len(d.cdata[p].content)
	}
	var sb strings.Builder
	sb.Grow(n)
	sb.WriteString(head)
	for p := first; p != noNode; p = d.cdata[p].next {
		sb.Write(d.cdata[p].content)
	}
	return sb.String()
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
	clear(d.cdata)
	d.src = nil
	d.inFirstRun = false
	d.elems = d.elems[:0]
	d.attrs = d.attrs[:0]
	d.scope = d.scope[:0]
	d.open = d.open[:0]
	d.cdata = d.cdata[:0]
	d.nsIndexed = false
	if cap(d.nsHead) > maxPooledNSBuckets {
		d.nsHead = nil // do not pin the table of one huge document in the pool
	}
	d.hasNodeID = false
	d.elems = append(d.elems, xelem{firstChild: noNode, lastChild: noNode, next: noNode, textPos: -1, cdataFirst: noNode, cdataLast: noNode})
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
	if d.nsIndexed {
		h := nsHash(prefix)
		for i := d.nsHead[h&d.nsMask]; i != noNode; i = d.scope[i].prev {
			if b := &d.scope[i]; b.hash == h && bytes.Equal(b.prefix, prefix) {
				return b.uri, true
			}
		}
		return "", false
	}
	for _, b := range slices.Backward(d.scope) {
		// Length and first byte reject most bindings before bytes.Equal.
		if q := b.prefix; len(q) == len(prefix) && q[0] == prefix[0] && bytes.Equal(q, prefix) {
			return b.uri, true
		}
	}
	return "", false
}

// nsHash returns the seeded hash of prefix, the key of the binding index.
func nsHash(prefix []byte) uint32 {
	return uint32(maphash.Bytes(nsSeed, prefix)) //nolint:gosec // G115: the low 32 bits of a hash are all the index uses
}

// pushBinding adds the declaration xmlns:prefix="uri" to the scope and, once
// the scope is large, to the binding index (see nsIndexThreshold).
func (d *xmlDoc) pushBinding(prefix []byte, uri string) {
	d.scope = append(d.scope, nsBinding{prefix: prefix, uri: uri})
	switch {
	case d.nsIndexed:
		if len(d.scope) > 2*len(d.nsHead) {
			d.rebuildNSIndex(2 * len(d.nsHead))
			return
		}
		d.indexBinding(int32(len(d.scope) - 1)) //nolint:gosec // G115: bounded by the 16 MiB document cap
	case len(d.scope) > nsIndexThreshold:
		d.rebuildNSIndex(nsInitialBuckets)
	}
}

// rebuildNSIndex (re)builds the binding index with n buckets (a power of two)
// over the whole scope, oldest binding first so that each chain runs from the
// innermost binding to the outermost.
func (d *xmlDoc) rebuildNSIndex(n int) {
	d.nsHead = slices.Grow(d.nsHead[:0], n)[:n]
	d.nsMask = uint32(n - 1) //nolint:gosec // G115: n is a power of two far below 2^32
	for i := range d.nsHead {
		d.nsHead[i] = noNode
	}
	d.nsIndexed = true
	for i := range d.scope {
		d.indexBinding(int32(i))
	}
}

// indexBinding links binding i at the head of its bucket.
func (d *xmlDoc) indexBinding(i int32) {
	b := &d.scope[i]
	b.hash = nsHash(b.prefix)
	k := b.hash & d.nsMask
	b.prev = d.nsHead[k]
	d.nsHead[k] = i
}

// truncateScope drops the bindings above n (the scope of a closed element),
// innermost first, so that every bucket head returns to the binding that was
// in force before.
func (d *xmlDoc) truncateScope(n int32) {
	if d.nsIndexed {
		for i := len(d.scope) - 1; i >= int(n); i-- {
			b := &d.scope[i]
			d.nsHead[b.hash&d.nsMask] = b.prev
		}
	}
	clear(d.scope[n:])
	d.scope = d.scope[:n]
}

// elemLargeDoc is the size from which elemHint counts the start tags of a
// document instead of estimating them from its length.
const elemLargeDoc = 64 << 10

// elemHint returns the number of elements to reserve for the document b.
//
// A small document gets the estimate of one element per 48 bytes. For a large
// one the estimate can be far off in either direction: a document that
// carries a big text value (extended XMP holds binary data as base64, for
// example) has few elements per byte, and reserving one slot per 48 bytes
// would allocate many times the table that the parse needs (task #394, sprint
// 50: 4.4 MiB for a 1.7 MiB document of 1,200 elements). The start tags are
// then counted: "<" opens every start tag, end tag, comment and processing
// instruction, and "</" every end tag, so their difference bounds the number
// of start tags from above, within the comments and processing instructions
// of the document. Both counts are vectorised and read the document once
// each, a small fraction of the scan that follows. The count comes from the
// document itself, so a hostile document cannot make it larger than it is;
// the caller still bounds it by maxXMLElements. The result never exceeds the
// estimate of one element per 48 bytes: a document whose "<" bytes sit in
// CDATA sections would otherwise make the count, not the document, set the
// size of the table. A document with more elements than that estimate grows
// the table on demand, as before.
func elemHint(b []byte) int {
	est := len(b) / 48
	if len(b) < elemLargeDoc {
		return est
	}
	return min(est, bytes.Count(b, ltBytes)-bytes.Count(b, ltSlashBytes)+1)
}

var (
	ltBytes      = []byte("<")  //nolint:gochecknoglobals // immutable sentinel; avoids a per-call []byte literal
	ltSlashBytes = []byte("</") //nolint:gochecknoglobals // immutable sentinel; avoids a per-call []byte literal
)

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
// A nesting depth above maxXMLDepth is rejected with ErrXMLNestingDepth, and a
// document with more than maxXMLElements elements or maxXMLAttrs attributes
// with ErrXMLTooComplex.
//
// The scanner stays lenient about everything that does not change the tree:
// comments, processing instructions and DOCTYPE declarations are skipped
// without interpretation, so no entity is ever expanded from a DTD
// (anti-XXE); unterminated comments end the scan.
func scanXML(b []byte, d *xmlDoc) error { //nolint:cyclop,gocyclo // one branch per XML markup kind (XML 1.0 §2.4–§2.8)
	d.src = b
	// Size the element table once for a large document, instead of doubling
	// (and re-copying) it up to the cap; a pooled scratch value that already
	// has the capacity makes this a no-op. About one element per 48 bytes.
	d.elems = slices.Grow(d.elems, min(elemHint(b), maxXMLElements))
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
				d.cdata = append(d.cdata, cdataPart{content: normaliseEOL(content), next: noNode})
				idx := int32(len(d.cdata) - 1) //nolint:gosec // G115: bounded by the 16 MiB document cap
				if e.cdataFirst == noNode {
					e.cdataFirst = idx
				} else {
					d.cdata[e.cdataLast].next = idx
				}
				e.cdataLast = idx
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
	d.truncateScope(top.scopeLen)
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
	if len(d.elems) > maxXMLElements {
		return 0, fmt.Errorf("%w: more than %d elements", ErrXMLTooComplex, maxXMLElements)
	}
	scopeLen := int32(len(d.scope)) //nolint:gosec // G115: bounded by the 16 MiB document cap
	attrLo := int32(len(d.attrs))   //nolint:gosec // G115: bounded by the 16 MiB document cap

	prefix, local, pos := scanName(b, pos)
	if len(prefix) == 0 && len(local) == 0 {
		// XML 1.0 §3.1 (production 40): a start tag begins with a Name. (A
		// name such as "q:" is a Name too; the RDF parser drops it later.)
		return 0, fmt.Errorf("%w: start tag without an element name", ErrXMLNotWellFormed)
	}
	pos, err := d.scanAttrs(b, pos)
	if err != nil {
		return 0, err
	}

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
	d.elems = growDoubling(d.elems, maxXMLElements+1)
	d.elems = append(d.elems, xelem{
		prefix: prefix, local: local, ns: ns, term: rdfTermKind(ns, local),
		attrLo: attrLo, attrHi: attrHi,
		firstChild: noNode, lastChild: noNode, next: noNode,
		textPos: -1, cdataFirst: noNode, cdataLast: noNode,
	})
	p := &d.elems[parent]
	if p.lastChild == noNode {
		p.firstChild = idx
	} else {
		d.elems[p.lastChild].next = idx
	}
	p.lastChild = idx

	if selfClose {
		d.truncateScope(scopeLen)
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
// unresolved. The attribute count of the document is bounded by maxXMLAttrs
// (security audit F-05); a namespace declaration is not an attribute and is
// bound only by the document size (task #283), which resolve survives in
// near-linear time through the binding index (F-04).
func (d *xmlDoc) scanAttrs(b []byte, pos int) (int, error) { //nolint:cyclop,gocyclo // attribute list grammar (XML 1.0 §3.1) and the three attribute kinds
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
			d.pushBinding(attrLocal, canonicalNS(val))
		case len(attrPrefix) == 0 && string(attrLocal) == "xmlns":
			// xmlns="uri" — a default namespace; XMP never relies on it.
		default:
			if string(attrLocal) == "nodeID" {
				d.hasNodeID = true
			}
			if len(d.attrs) >= maxXMLAttrs {
				return pos, fmt.Errorf("%w: more than %d attributes", ErrXMLTooComplex, maxXMLAttrs)
			}
			d.attrs = growDoubling(d.attrs, maxXMLAttrs)
			d.attrs = append(d.attrs, xattr{prefix: attrPrefix, local: attrLocal, val: val})
		}
	}
	return pos, nil
}
