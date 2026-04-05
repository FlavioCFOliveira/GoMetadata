package xmp

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"unsafe"
)

// nsEntry maps an XML namespace prefix to its URI.
// Stored in a stack-allocated array to avoid heap allocation for the common case.
// prefix is a zero-copy slice into the original parse buffer; uri is a string
// because it is used as a Properties map key and must outlive the parse buffer.
type nsEntry struct {
	prefix []byte // slice into parse buffer — no allocation on store
	uri    string
}

// xmpAttr represents a single parsed XML attribute (excluding xmlns declarations).
type xmpAttr struct {
	ns  string // resolved namespace URI
	loc string // local name
	val string // attribute value (entities already unescaped)
}

// liPool recycles the []string slice used to accumulate rdf:li values within a
// single collection (rdf:Alt, rdf:Seq, rdf:Bag). The pool eliminates the heap
// allocation on the hot-path after the first parse call.
var liPool = sync.Pool{New: func() any { //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
	s := make([]string, 0, 8)
	return &s
}}

// builderPool recycles strings.Builder instances used by unescapeXML.
// They are only taken from the pool when the input actually contains '&'.
var builderPool = sync.Pool{New: func() any { return &strings.Builder{} }} //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure

// rdfParser holds all mutable state for a single parseRDF invocation.
// Bundling state into a struct allows the three dispatch methods (onStartElement,
// onEndElement, onCharData) to be extracted cleanly, reducing cyclomatic
// complexity of parseRDF from ~97 to ≤ 10.
type rdfParser struct {
	x *XMP

	// Depth tracking (absolute element nesting level, 1-based).
	depth int
	// Depth at which the current top-level rdf:Description was opened.
	descDepth int
	// Depth at which the current property element was opened.
	propDepth int
	// Namespace and local name of the current property element.
	propNS    string
	propLocal string
	// True when inside an rdf:Alt / rdf:Seq / rdf:Bag.
	inColl bool
	// True when inside a struct value node (nested rdf:Description inside a property).
	inStruct bool
	// Depth of the struct's rdf:Description element.
	structDepth int
	// Field element currently being parsed inside a struct.
	structFieldNS    string
	structFieldLocal string
	structFieldDepth int
	// xml:lang of the current rdf:li element (P1-H, used for rdf:Alt).
	liLang string

	// Stack-allocated namespace table. The scanner pushes entries as it
	// encounters xmlns:prefix="uri" declarations; resolveNS scans backward so
	// inner declarations shadow outer ones correctly.
	nsTable [32]nsEntry
	nsCount int

	// Stack-allocated attribute buffer. 16 attributes per element is sufficient
	// for all real XMP properties; excess attributes are silently dropped.
	attrBuf [16]xmpAttr

	// Pooled list accumulator for rdf:li values.
	liVals *[]string
}

// closeStructField resets the struct field tracking fields when the parser
// leaves a struct field element at the current depth.
func (p *rdfParser) closeStructField() {
	p.structFieldNS, p.structFieldLocal = "", ""
	p.structFieldDepth = 0
}

// closeStruct resets the struct tracking fields when the parser leaves a
// struct value node at the current depth.
func (p *rdfParser) closeStruct() {
	p.inStruct = false
	p.structDepth = 0
}

// closeProp finalises a property element, flushing any accumulated rdf:li
// values for collection properties before resetting all property tracking fields.
func (p *rdfParser) closeProp() {
	if p.inColl && len(*p.liVals) > 0 {
		storeProperty(p.x, p.propNS, p.propLocal, strings.Join(*p.liVals, "\x1e"))
		*p.liVals = (*p.liVals)[:0]
	}
	p.inColl = false
	p.inStruct = false
	p.propNS, p.propLocal = "", ""
	p.propDepth = 0
}

// onEndElement handles the closing-tag dispatch logic.
// It is called both from the explicit end-tag path and from self-closing tags,
// eliminating the duplication that previously existed between those two branches.
func (p *rdfParser) onEndElement() {
	switch {
	case p.inStruct && p.structFieldDepth > 0 && p.depth == p.structFieldDepth:
		// Closing a struct field element.
		p.closeStructField()

	case p.inStruct && p.depth == p.structDepth:
		// Closing the struct value node.
		p.closeStruct()

	case p.depth == p.propDepth && p.propDepth > 0:
		// Closing the current property element.
		p.closeProp()

	case p.depth == p.descDepth:
		p.descDepth = 0
	}
	p.depth--
}

// onStartStructField handles an element that opens a new struct field inside
// the current struct value node.
// Compliance: XMP Part 1 §C.2.6 (struct field elements).
func (p *rdfParser) onStartStructField(ns string, tagLocal []byte) {
	p.structFieldNS = ns
	p.structFieldLocal = string(tagLocal)
	p.structFieldDepth = p.depth
}

// onStartProperty handles a direct child element of a top-level rdf:Description
// that begins a new property.
// Compliance: XMP Part 1 §C.2.4.
func (p *rdfParser) onStartProperty(ns string, tagLocal []byte) {
	p.propNS = ns
	p.propLocal = string(tagLocal) // string() here: stored as map key
	p.propDepth = p.depth
	// rdf:resource shorthand and rdf:parseType="Resource" already handled in onStartElement.
}

// onStartCollection handles rdf:Alt, rdf:Seq, and rdf:Bag container elements
// immediately inside a property element.
// Compliance: XMP Part 1 §C.2.5.
func (p *rdfParser) onStartCollection(ns string, tagLocal []byte) {
	tl := string(tagLocal) // zero-alloc compare (compiler optimisation)
	if ns == NSrdf && (tl == "Alt" || tl == "Seq" || tl == "Bag") {
		p.inColl = true
		*p.liVals = (*p.liVals)[:0]
	}
}

// onStartListItem handles rdf:li elements inside a collection, capturing any
// xml:lang attribute for rdf:Alt items.
// Compliance: XMP Part 1 §C.2.5 and P1-H.
func (p *rdfParser) onStartListItem(attrs []xmpAttr) {
	p.liLang = ""
	for _, a := range attrs {
		if a.loc == "lang" {
			p.liLang = a.val
			break
		}
	}
}

// applyAttrShorthands scans attrs for rdf:parseType="Resource" and
// rdf:resource shorthand attributes and applies their effects immediately.
// Must be called before the element-kind dispatch so that parseType and
// resource are visible in the switch cases below.
// Compliance: XMP Part 1 §C.2.5 / §C.2.6.
func (p *rdfParser) applyAttrShorthands(attrs []xmpAttr) {
	for _, a := range attrs {
		if a.ns == NSrdf && a.loc == "parseType" && a.val == "Resource" {
			p.inStruct = true
			p.structDepth = p.depth
		}
		if a.ns == NSrdf && a.loc == "resource" && p.propDepth > 0 && p.depth == p.propDepth {
			storeProperty(p.x, p.propNS, p.propLocal, a.val)
		}
	}
}

// atStructField reports whether the current element is a struct field:
// we are inside a struct, at exactly one level below the struct node,
// and no field is currently open.
func (p *rdfParser) atStructField() bool {
	return p.inStruct && p.depth == p.structDepth+1 && p.structFieldDepth == 0
}

// atProperty reports whether the current element is a property element:
// a direct child of the current top-level rdf:Description, and no property
// is already open.
func (p *rdfParser) atProperty() bool {
	return p.descDepth > 0 && p.depth == p.descDepth+1 && p.propDepth == 0
}

// atCollection reports whether the current element is a collection container
// (rdf:Alt/Seq/Bag): directly inside the current property and not a struct.
func (p *rdfParser) atCollection() bool {
	return p.propDepth > 0 && p.depth == p.propDepth+1 && !p.inStruct
}

// atListItem reports whether the current element is an rdf:li inside the
// current collection.
func (p *rdfParser) atListItem() bool {
	return p.inColl && p.depth == p.propDepth+2
}

// onStartElement handles the start-element dispatch for a single element.
// ns is the resolved namespace URI for the element; tagLocal is the zero-copy
// local name slice; attrs is the resolved attribute slice for the element.
//
// Compliance: ISO 16684-1:2019 §7, Adobe XMP Specification Part 1 §C.
func (p *rdfParser) onStartElement(ns string, tagLocal []byte, attrs []xmpAttr) {
	// First pass over attrs: handle rdf:parseType="Resource" and
	// rdf:resource shorthand (XMP Part 1 §C.2.5 / §C.2.6).
	p.applyAttrShorthands(attrs)

	switch {
	// ── Struct field element inside a struct node (P1-G) ──────────────
	case p.atStructField():
		p.onStartStructField(ns, tagLocal)

	// ── rdf:Description: top-level block or struct value node ─────────
	case ns == NSrdf && string(tagLocal) == "Description":
		p.onStartDescription(attrs)

	// ── Property element: direct child of top-level rdf:Description ───
	case p.atProperty():
		p.onStartProperty(ns, tagLocal)

	// ── Collection container: rdf:Alt / Seq / Bag ────────────────────
	case p.atCollection():
		p.onStartCollection(ns, tagLocal)

	// ── rdf:li inside a collection ────────────────────────────────────
	case p.atListItem():
		// Capture xml:lang attribute for rdf:Alt items (P1-H).
		p.onStartListItem(attrs)
	}
}

// onStartStructValueNode handles a nested rdf:Description that introduces a
// struct value node inside a property element.
// Compliance: XMP Part 1 §C.2.6.
func (p *rdfParser) onStartStructValueNode(attrs []xmpAttr) {
	p.inStruct = true
	p.structDepth = p.depth
	for _, a := range attrs {
		if a.ns == "" || a.ns == NSrdf || a.ns == NSx {
			continue
		}
		storeProperty(p.x, p.propNS, p.propLocal+"."+a.loc, a.val)
	}
}

// onStartTopLevelDesc handles a top-level rdf:Description element, registering
// shorthand (inline) properties from its attributes.
// Compliance: XMP Part 1 §C.2.4.
func (p *rdfParser) onStartTopLevelDesc(attrs []xmpAttr) {
	p.descDepth = p.depth
	for _, a := range attrs {
		if a.ns == "" || a.ns == NSrdf || a.ns == NSx {
			continue
		}
		storeProperty(p.x, a.ns, a.loc, a.val)
	}
}

// onStartDescription handles rdf:Description elements, which can be either
// a top-level property block or a struct value node nested inside a property.
// Extracted from onStartElement to reduce its cyclomatic complexity.
//
// Compliance: XMP Part 1 §C.2.4 (shorthand properties) and §C.2.6 (struct value nodes).
func (p *rdfParser) onStartDescription(attrs []xmpAttr) {
	if p.propDepth > 0 && p.depth == p.propDepth+1 && !p.inStruct {
		// Struct value node: nested rdf:Description inside a property element
		// (XMP Part 1 §C.2.6). Store inline attrs as "propLocal.fieldLocal"
		// keys in the parent property's namespace.
		p.onStartStructValueNode(attrs)
	} else if p.descDepth == 0 {
		// Top-level rdf:Description — begin a new property block.
		// Shorthand properties are inline attributes (XMP Part 1 §C.2.4).
		p.onStartTopLevelDesc(attrs)
	}
}

// onCharDataStructField stores the text content of a struct field element.
// Compliance: XMP Part 1 §C.2.6 (P1-G).
func (p *rdfParser) onCharDataStructField(s string) {
	fieldNS := p.structFieldNS
	if fieldNS == "" {
		fieldNS = p.propNS
	}
	key := p.propLocal + "." + p.structFieldLocal
	if p.x.Properties[fieldNS] == nil {
		p.x.Properties[fieldNS] = make(map[string]string)
	}
	if p.x.Properties[fieldNS][key] == "" {
		p.x.Properties[fieldNS][key] = s
	}
}

// onCharDataListItem appends text content inside an rdf:li element to the
// current collection accumulator, preserving xml:lang prefix for rdf:Alt items.
// Compliance: XMP Part 1 §C.2.5 (P1-H).
func (p *rdfParser) onCharDataListItem(s string) {
	if p.liLang != "" && p.liLang != "x-default" {
		*p.liVals = append(*p.liVals, p.liLang+"|"+s)
	} else {
		*p.liVals = append(*p.liVals, s)
	}
}

// onCharDataSimple stores the text content of a simple (scalar) property element.
// Compliance: XMP Part 1 §C.2.3.
func (p *rdfParser) onCharDataSimple(s string) {
	if p.x.Properties[p.propNS] == nil {
		p.x.Properties[p.propNS] = make(map[string]string)
	}
	// Only store if not already set (e.g. by rdf:resource attribute).
	if p.x.Properties[p.propNS][p.propLocal] == "" {
		p.x.Properties[p.propNS][p.propLocal] = s
	}
}

// onCharData handles text content between tags.
// s is the already-unescaped text content of the current element.
//
// Compliance: ISO 16684-1:2019 §7, XMP Part 1 §C.2.3 and §C.2.6.
func (p *rdfParser) onCharData(s string) {
	switch {
	case p.inStruct && p.structFieldDepth > 0 && p.depth == p.structFieldDepth:
		// Text content of a struct field element (P1-G).
		// Store as "propLocal.fieldLocal" in the parent property namespace.
		// If the field is in a different namespace, use that namespace.
		p.onCharDataStructField(s)

	case p.inColl && p.depth == p.propDepth+2:
		// Inside rdf:li (propDepth+1 = collection, propDepth+2 = li).
		// For rdf:Alt items, preserve non-default xml:lang as "lang|value" (P1-H).
		p.onCharDataListItem(s)

	case p.propDepth > 0 && !p.inStruct && p.depth == p.propDepth:
		// Direct text content of a simple property (XMP Part 1 §C.2.3).
		p.onCharDataSimple(s)
	}
}

// skipComment advances pos past an XML comment construct <!-- ... -->.
// b[pos] must be '!' at entry. Returns the updated position.
func skipComment(b []byte, pos int) int {
	end := bytes.Index(b[pos:], []byte("-->"))
	if end < 0 {
		return len(b) // unterminated — skip to end
	}
	return pos + end + 3
}

// skipPI advances pos past an XML processing instruction <? ... ?>.
// b[pos] must be '?' at entry. Returns the updated position.
func skipPI(b []byte, pos int) int {
	end := bytes.Index(b[pos:], []byte("?>"))
	if end < 0 {
		return len(b)
	}
	return pos + end + 2
}

// skipBang advances pos past an XML <! ... > construct (CDATA, DOCTYPE, etc.)
// b[pos] must be '!' at entry. Returns the updated position.
func skipBang(b []byte, pos int) int {
	end := bytes.IndexByte(b[pos:], '>')
	if end < 0 {
		return len(b)
	}
	return pos + end + 1
}

// isComment reports whether b[pos:] begins an XML comment (<!--).
func isComment(b []byte, pos int) bool {
	return pos+2 < len(b) && b[pos] == '!' && b[pos+1] == '-' && b[pos+2] == '-'
}

// skipSpecialTag advances pos past a comment (<!-- ... -->), processing
// instruction (<? ... ?>), or CDATA/DOCTYPE (<! ... >) construct.
// Returns the updated position and true if a special tag was consumed;
// returns the original position and false otherwise.
func skipSpecialTag(b []byte, pos int) (newPos int, skipped bool) {
	if pos >= len(b) {
		return pos, false
	}
	switch {
	case isComment(b, pos):
		return skipComment(b, pos), true
	case b[pos] == '?':
		return skipPI(b, pos), true
	case b[pos] == '!':
		return skipBang(b, pos), true
	}
	return pos, false
}

// parseStartTag parses a start (or self-closing) tag beginning at b[pos] (the
// byte immediately after '<'). It updates p.depth, dispatches onStartElement,
// and handles both the self-closing case and the text content that follows.
// Returns the updated position, or -1 to signal a fatal parse error.
func parseStartTag(b []byte, pos int, p *rdfParser) (newPos int, err error) {
	p.depth++
	if p.depth > 100 {
		return 0, errors.New("xmp: XML nesting depth exceeded 100 levels")
	}

	// Parse the tag name: [prefix:]local.
	tagPrefix, tagLocal, newPos2 := scanName(b, pos)
	pos = newPos2

	// Resolve the element's namespace URI.
	ns := resolveNS(p.nsTable[:p.nsCount], tagPrefix)

	// Parse attributes. xmlns declarations are registered into nsTable;
	// regular attributes land in attrBuf.
	var nAttrs int
	p.nsCount, nAttrs, pos = scanAttrs(b, pos, &p.nsTable, p.nsCount, &p.attrBuf)
	attrs := p.attrBuf[:nAttrs]

	// Detect self-closing '/>' — consume '/' and '>'.
	selfClose := false
	if pos < len(b) && b[pos] == '/' {
		selfClose = true
		pos++
	}
	if pos < len(b) && b[pos] == '>' {
		pos++
	}

	p.onStartElement(ns, tagLocal, attrs)

	if selfClose {
		// Self-closing element: immediately apply EndElement logic.
		p.onEndElement()
		return pos, nil
	}

	// ── Text content between this tag and the next '<' ────────────────
	textEnd := bytes.IndexByte(b[pos:], '<')
	var text []byte
	if textEnd >= 0 {
		text = trimSpace(b[pos : pos+textEnd])
		// Do NOT advance pos here; the outer loop will find this '<'.
	} else {
		text = trimSpace(b[pos:])
		pos = len(b)
	}

	if len(text) > 0 {
		p.onCharData(unescapeXML(text))
	}

	return pos, nil
}

// parseRDF walks the RDF graph rooted at the x:xmpmeta element and populates
// the Properties map in x. It handles rdf:Alt, rdf:Seq, and rdf:Bag
// collections by joining their rdf:li values with U+001E (record separator).
// rdf:Alt items preserve xml:lang as a "lang|value" prefix (P1-H).
// Struct properties (nested rdf:Description) are stored as "parent.child" keys
// within the parent property's namespace (P1-G).
//
// This implementation is a hand-rolled byte scanner that avoids all encoding/xml
// allocations on the hot path. Namespace declarations are tracked in a
// stack-allocated [32]nsEntry table; attributes in a stack-allocated [16]xmpAttr
// buffer. Only entity-escaped values and multi-valued collection joins allocate.
//
// Compliance: ISO 16684-1:2019 §7, Adobe XMP Specification Part 1 §C.
func parseRDF(b []byte, x *XMP) error {
	p := rdfParser{x: x}

	// Pooled list accumulator for rdf:li values.
	p.liVals = liPool.Get().(*[]string) //nolint:forcetypeassert,revive // liPool.New always stores *[]string; pool invariant
	*p.liVals = (*p.liVals)[:0]
	defer liPool.Put(p.liVals)

	pos := 0

	for pos < len(b) {
		// Find the next '<'.
		i := bytes.IndexByte(b[pos:], '<')
		if i < 0 {
			break
		}
		pos += i + 1 // b[pos] is now the byte immediately after '<'

		if pos >= len(b) {
			break
		}

		// ── Comment, PI, CDATA, DOCTYPE ─────────────────────────────────────
		if newPos, skipped := skipSpecialTag(b, pos); skipped {
			if newPos >= len(b) {
				break
			}
			pos = newPos
			continue
		}

		// ── End tag: </prefix:local> or </local> ─────────────────────────────
		if b[pos] == '/' {
			pos++ // skip '/'
			end := bytes.IndexByte(b[pos:], '>')
			if end < 0 {
				break
			}
			pos += end + 1
			p.onEndElement()
			continue
		}

		// ── Start tag or self-closing tag ────────────────────────────────────
		var err error
		pos, err = parseStartTag(b, pos, &p)
		if err != nil {
			return err
		}
	}

	return nil
}

// scanName parses an XML name starting at b[pos] and returns zero-copy
// byte slices for the prefix and local name, plus the position after the name.
//
// Stops at whitespace, '>', '/', or '='. For names of the form "prefix:local",
// prefix is the part before ':' and local is the part after. For unqualified
// names, prefix is nil and local is the whole name. Callers must convert to
// string (string(local)) only when storing; comparisons should use
// string(local) == "literal" which the compiler optimises to a zero-alloc
// byte comparison.
func scanName(b []byte, pos int) (prefix, local []byte, end int) {
	start := pos
	colon := -1
	for pos < len(b) {
		c := b[pos]
		// Stop at XML attribute/tag terminators.
		if isNameTerminator(c) {
			break
		}
		if c == ':' && colon < 0 {
			colon = pos
		}
		pos++
	}
	if colon >= 0 {
		prefix = b[start:colon]
		local = b[colon+1 : pos]
	} else {
		local = b[start:pos]
	}
	return prefix, local, pos
}

// isNameTerminator reports whether c is a byte that terminates an XML name
// token in the context of attribute/tag parsing.
func isNameTerminator(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
		c == '>' || c == '/' || c == '='
}

// advancePastEquals skips optional whitespace at b[pos], then expects '=' and
// advances past it. Returns the updated position and true on success; returns
// the position after the whitespace and false if '=' is not present (malformed).
func advancePastEquals(b []byte, pos int) (newPos int, ok bool) {
	for pos < len(b) && isASCIISpace(b[pos]) {
		pos++
	}
	if pos >= len(b) || b[pos] != '=' {
		return pos, false
	}
	return pos + 1, true // skip '='
}

// parseSingleAttr parses one XML attribute starting at b[pos].
// Returns the attribute prefix, local name, value, the updated position,
// and whether the attribute was well-formed.
func parseSingleAttr(b []byte, pos int) (attrPrefix, attrLocal []byte, val string, newPos int, ok bool) {
	attrPrefix, attrLocal, pos = scanName(b, pos)

	// Require '=' (with optional surrounding whitespace).
	pos, ok = advancePastEquals(b, pos)
	if !ok {
		return nil, nil, "", pos, false
	}

	val, pos, ok = parseAttributeValue(b, pos)
	return attrPrefix, attrLocal, val, pos, ok
}

// scanAttrs parses XML attributes starting at b[pos] until '>' or '/' is
// reached. xmlns:prefix="uri" declarations are added to nsTable; all other
// attributes are appended to out (up to cap(out)).
//
// Returns the updated namespace count, the number of non-namespace attributes,
// and the position after the last attribute (pointing at '>' or '/').
func scanAttrs(b []byte, pos int, nsTable *[32]nsEntry, nsCount int, out *[16]xmpAttr) (newNsCount, nAttrs, newPos int) {
	nAttrs = 0
	for pos < len(b) && b[pos] != '>' && b[pos] != '/' {
		// Skip whitespace between attributes.
		for pos < len(b) && isASCIISpace(b[pos]) {
			pos++
		}
		if pos >= len(b) || b[pos] == '>' || b[pos] == '/' {
			break
		}

		attrPrefix, attrLocal, val, newPos2, ok := parseSingleAttr(b, pos)
		pos = newPos2
		if !ok {
			continue
		}

		nsCount, nAttrs = classifyAndStoreAttr(attrPrefix, attrLocal, val, nsTable, nsCount, out, nAttrs)
	}
	return nsCount, nAttrs, pos
}

// skipUnquotedAttr advances pos past an unquoted attribute token, stopping at
// whitespace, '>', or '/'. Returns the updated position.
func skipUnquotedAttr(b []byte, pos int) int {
	for pos < len(b) && b[pos] != ' ' && b[pos] != '\t' && b[pos] != '>' && b[pos] != '/' {
		pos++
	}
	return pos
}

// readQuotedValue reads the attribute value enclosed by quote (either '"' or
// "'") starting at b[pos] (the byte after the opening quote character).
// Returns the unescaped value string and the position after the closing quote.
func readQuotedValue(b []byte, pos int, quote byte) (val string, newPos int) {
	valStart := pos
	for pos < len(b) && b[pos] != quote {
		pos++
	}
	val = unescapeXML(b[valStart:pos])
	if pos < len(b) {
		pos++ // skip closing quote
	}
	return val, pos
}

// parseAttributeValue skips optional whitespace, reads a quoted attribute
// value (single or double quotes), unescapes entities, and returns the decoded
// string plus the updated position. ok is false if the input is malformed.
func parseAttributeValue(b []byte, pos int) (val string, newPos int, ok bool) {
	// Skip optional whitespace before the quote.
	for pos < len(b) && isASCIISpace(b[pos]) {
		pos++
	}
	if pos >= len(b) {
		return "", pos, false
	}

	quote := b[pos]
	if quote != '"' && quote != '\'' {
		// Malformed: unquoted attribute value — skip to next whitespace.
		return "", skipUnquotedAttr(b, pos), false
	}
	pos++ // skip opening quote

	val, pos = readQuotedValue(b, pos, quote)
	return val, pos, true
}

// classifyAndStoreAttr classifies a parsed attribute as either an xmlns
// declaration or a regular attribute, updating the namespace table or the
// attribute output buffer accordingly. Returns updated nsCount and nAttrs.
func classifyAndStoreAttr(attrPrefix, attrLocal []byte, val string, nsTable *[32]nsEntry, nsCount int, out *[16]xmpAttr, nAttrs int) (int, int) {
	// string(attrPrefix) == "xmlns" is a zero-alloc comparison (Go compiler).
	switch {
	case string(attrPrefix) == "xmlns":
		// xmlns:prefix="uri" — register namespace binding.
		// attrLocal is a zero-copy slice; no string conversion needed here.
		if nsCount < len(nsTable) {
			nsTable[nsCount] = nsEntry{prefix: attrLocal, uri: val}
			nsCount++
		}
	case string(attrLocal) == "xmlns" && len(attrPrefix) == 0:
		// xmlns="uri" — default namespace declaration; ignore (XMP never uses it).
	default:
		// Regular attribute: resolve its namespace and store.
		if nAttrs < len(out) {
			resolvedNS := resolveNS(nsTable[:nsCount], attrPrefix)
			out[nAttrs] = xmpAttr{ns: resolvedNS, loc: string(attrLocal), val: val}
			nAttrs++
		}
	}
	return nsCount, nAttrs
}

// resolveNS looks up the URI for the given prefix in the namespace table.
// The table is scanned backward so inner (later) declarations shadow outer ones.
// Returns "" if the prefix is not found. prefix is a []byte slice (zero-copy
// from the parse buffer); comparison uses bytes.Equal to avoid string allocation.
func resolveNS(table []nsEntry, prefix []byte) string {
	for i := len(table) - 1; i >= 0; i-- {
		if bytes.Equal(table[i].prefix, prefix) {
			return table[i].uri
		}
	}
	return ""
}

// unescapeXML converts b to a string, replacing the five predefined XML
// entities and numeric character references. When b contains no '&', it
// returns string(b) directly (one allocation, no builder overhead).
func unescapeXML(b []byte) string {
	if bytes.IndexByte(b, '&') < 0 {
		// Fast path: no XML entities — create a string that borrows from b's
		// backing array. unsafe.String is GC-safe: the string header retains a
		// pointer into b's array, preventing it from being collected while the
		// string is live.
		if len(b) == 0 {
			return ""
		}
		return unsafe.String(unsafe.SliceData(b), len(b)) //nolint:gosec // G103: unsafe.String is safe here; b is kept alive by the caller via the parent slice
	}

	bld := builderPool.Get().(*strings.Builder) //nolint:forcetypeassert,revive // builderPool.New always stores *strings.Builder; pool invariant
	bld.Reset()
	bld.Grow(len(b))

	for i := 0; i < len(b); {
		if b[i] != '&' {
			bld.WriteByte(b[i])
			i++
			continue
		}
		// Find the closing ';'.
		semi := bytes.IndexByte(b[i:], ';')
		if semi < 0 {
			// No closing ';' — emit literally.
			bld.Write(b[i:])
			break
		}
		ref := b[i+1 : i+semi] // the content between '&' and ';'
		i += semi + 1
		decodeEntity(ref, bld)
	}

	s := bld.String()
	bld.Reset()
	builderPool.Put(bld)
	return s
}

// decodeCharRef decodes a numeric XML character reference (&#N; or &#xHH;).
// ref is the content after '#' (e.g., []byte("65") or []byte("x41")).
// Returns true and writes the rune if the reference is valid.
func decodeCharRef(ref []byte, bld *strings.Builder) bool {
	if len(ref) == 0 {
		return false
	}
	var r rune
	var ok bool
	if ref[0] == 'x' || ref[0] == 'X' {
		r, ok = parseHex(ref[1:])
	} else {
		r, ok = parseDec(ref)
	}
	if ok {
		bld.WriteRune(r)
	}
	return ok
}

// decodeEntity writes the character(s) for the XML entity reference ref into
// bld. ref is the content between '&' and ';' (e.g., []byte("amp") or
// []byte("#65")). Handles the five predefined entities, decimal and hex numeric
// character references, and unknown entities (emitted literally as &ref;).
func decodeEntity(ref []byte, bld *strings.Builder) {
	switch {
	case bytes.Equal(ref, []byte("amp")):
		bld.WriteByte('&')
	case bytes.Equal(ref, []byte("lt")):
		bld.WriteByte('<')
	case bytes.Equal(ref, []byte("gt")):
		bld.WriteByte('>')
	case bytes.Equal(ref, []byte("quot")):
		bld.WriteByte('"')
	case bytes.Equal(ref, []byte("apos")):
		bld.WriteByte('\'')
	case len(ref) > 1 && ref[0] == '#':
		// Numeric character reference: &#N; or &#xHH;
		decodeCharRef(ref[1:], bld)
	default:
		// Unknown entity — emit the original reference.
		bld.WriteByte('&')
		bld.Write(ref)
		bld.WriteByte(';')
	}
}

// parseHex parses a hexadecimal rune reference (without the leading "x").
func parseHex(b []byte) (rune, bool) {
	var v rune
	for _, c := range b {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			v |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= rune(c-'A') + 10
		default:
			return 0, false
		}
	}
	return v, true
}

// parseDec parses a decimal rune reference.
func parseDec(b []byte) (rune, bool) {
	var v rune
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + rune(c-'0')
	}
	return v, true
}

// trimSpace returns a sub-slice of b with leading and trailing ASCII whitespace
// removed. It operates on []byte to avoid an intermediate string allocation.
func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isASCIISpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isASCIISpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

// isASCIISpace reports whether b is an ASCII whitespace character.
func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// storeProperty writes val to x.Properties[ns][local], initialising inner maps
// as needed. It does not overwrite an existing value (first writer wins), so
// that rdf:resource attributes set before element text content take precedence.
func storeProperty(x *XMP, ns, local, val string) {
	if x.Properties[ns] == nil {
		x.Properties[ns] = make(map[string]string)
	}
	x.Properties[ns][local] = val
}
