package xmp

import (
	"bytes"
	"slices"
	"strings"
	"sync"
	"unicode"
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

// maxUnescapedXMLBytes caps the decoded output of a single XMP attribute or
// text node to prevent unbounded allocation from crafted numeric character refs.
const maxUnescapedXMLBytes = 1 << 20 // 1 MiB

// commentClose and piEnd are package-level byte slices for XML structural
// terminators. Declared here to avoid heap-allocating []byte literals inside
// skipComment and skipPI on every call (each []byte("...") literal in a
// function body escapes to the heap).
var commentClose = []byte("-->")   //nolint:gochecknoglobals // immutable sentinel; avoids per-call []byte literal heap allocation
var piEnd = []byte("?>")           //nolint:gochecknoglobals // immutable sentinel; avoids per-call []byte literal heap allocation
var cdataEnd = []byte("]]>")       //nolint:gochecknoglobals // immutable sentinel; XML 1.0 §2.7 CDATA section close delimiter
var cdataOpen = []byte("![CDATA[") //nolint:gochecknoglobals // immutable sentinel; the bytes after '<' that identify a CDATA section

// nsDepthEntry records how many namespace entries were in the table when an
// element was opened, so onEndElement can restore nsCount on element close
// (namespace scope pop). The table is parallel to element depth 1–100.
// ISO 16684-1 §7.4: namespace bindings are scoped to the element that declares them.
type nsDepthEntry struct {
	nsCountBefore int // nsCount value before this element's xmlns attrs were parsed
}

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
	// #13: also true when inside a struct-in-list-item (rdf:li > rdf:Description).
	inStruct bool
	// Depth of the struct's rdf:Description element.
	structDepth int
	// Field element currently being parsed inside a struct.
	structFieldNS    string
	structFieldLocal string
	structFieldDepth int
	// xml:lang of the current rdf:li element (P1-H, used for rdf:Alt).
	liLang string

	// #13: struct-in-list-item tracking.
	// inStructInList is true when the current struct was opened inside an rdf:li.
	// liItemIndex is the 0-based index of the current rdf:li within the collection,
	// used to generate "propLocal[N].field" keys.
	// liItemDepth is the depth at which the enclosing rdf:li was opened.
	inStructInList bool
	liItemIndex    int
	liItemDepth    int

	// Stack-allocated namespace table. The scanner pushes entries as it
	// encounters xmlns:prefix="uri" declarations; resolveNS scans backward so
	// inner declarations shadow outer ones correctly.
	//
	// #15: Increased from 32 to 64 to handle legitimate documents with many
	// namespace declarations (e.g. multi-tool XMP with xmpMM:History).
	nsTable [64]nsEntry
	nsCount int

	// #15: Per-depth namespace scope stack. nsDepth[d] records the nsCount value
	// before element at depth d added its xmlns declarations. onEndElement restores
	// nsCount from this stack, implementing proper XML namespace scoping.
	// Size matches the maximum nesting depth (100).
	nsDepth [101]nsDepthEntry

	// Stack-allocated attribute buffer.
	// #15: Increased from 16 to 32 to handle elements with many inline attributes
	// (e.g. rdf:Description with many shorthand properties). Excess attributes are
	// silently dropped only beyond 32, which is sufficient for all real XMP.
	attrBuf [32]xmpAttr

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
// #13: also clears inStructInList when closing a struct-in-list-item.
func (p *rdfParser) closeStruct() {
	p.inStruct = false
	p.structDepth = 0
	if p.inStructInList {
		p.inStructInList = false
		// liItemIndex is intentionally preserved: the next rdf:li in the same
		// collection will increment it.
	}
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
	p.inStructInList = false
	p.liItemIndex = 0
	p.liItemDepth = 0
	p.propNS, p.propLocal = "", ""
	p.propDepth = 0
}

// advanceListItemIndex advances liItemIndex when the current closing element
// is the rdf:li that contained a struct-in-list-item, so the next sibling
// rdf:li gets the next index.
// #13: XMP Part 1 §C.2.5 — struct-valued list items are indexed from 0.
func (p *rdfParser) advanceListItemIndex() {
	if p.inColl && p.liItemDepth > 0 && p.depth == p.liItemDepth {
		p.liItemIndex++
		p.liItemDepth = 0
	}
}

// popNSScope restores nsCount to the value it had before the current element's
// xmlns attributes were registered, implementing XML namespace scoping.
// #15: ISO 16684-1 §7.4 / XML Namespaces §3: bindings scope to the declaring element.
func (p *rdfParser) popNSScope() {
	if p.depth > 0 && p.depth <= 100 {
		p.nsCount = p.nsDepth[p.depth].nsCountBefore
	}
}

// onEndElement handles the closing-tag dispatch logic.
// It is called both from the explicit end-tag path and from self-closing tags,
// eliminating the duplication that previously existed between those two branches.
//
// Security note: malformed input may contain more close tags than open tags.
// Guard against depth underflow: if depth is already 0 there is no open element
// to close, so silently ignore the spurious end tag.  This preserves the
// parser's lenient design (only ErrEmptyInput and ErrXMLNestingDepth are fatal).
func (p *rdfParser) onEndElement() {
	// #36: depth underflow guard — unmatched close tags must not drive depth
	// negative.  parseStartTag indexes nsDepth[p.depth] after p.depth++; if
	// depth were -1 before the increment the index would be -1 → panic.
	if p.depth <= 0 {
		return
	}
	switch {
	case p.inStruct && p.structFieldDepth > 0 && p.depth == p.structFieldDepth:
		p.closeStructField()
	case p.inStruct && p.depth == p.structDepth:
		p.closeStruct()
	case p.depth == p.propDepth && p.propDepth > 0:
		p.closeProp()
	case p.depth == p.descDepth:
		p.descDepth = 0
	}
	p.advanceListItemIndex() // #13: advance rdf:li index after struct-in-list close
	p.popNSScope()           // #15: restore namespace scope on element close
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
// Compliance: XMP Part 1 §C.2.4, XMP Part 1 §C.2.5.
//
// #173 / XMP Part 1 §C.2.5: rdf:resource on a property element is the
// "simple value shorthand" form — <ns:Prop rdf:resource="…"/> — equivalent to
// an rdf:Description with rdf:resource.  applyAttrShorthands cannot handle this
// because it runs before onStartProperty sets propDepth; by the time the depth
// guard (p.propDepth > 0 && p.depth == p.propDepth) would pass, the property
// element has already been dispatched.  We scan attrs here, after establishing
// the property context, so the resource is captured correctly.
func (p *rdfParser) onStartProperty(ns string, tagLocal []byte, attrs []xmpAttr) {
	p.propNS = ns
	p.propLocal = string(tagLocal) // string() here: stored as map key
	p.propDepth = p.depth
	// XMP Part 1 §C.2.5: rdf:resource on the property element itself is a
	// simple-value shorthand; store it immediately now that propDepth is set.
	for _, a := range attrs {
		if a.ns == NSrdf && a.loc == "resource" {
			storeProperty(p.x, p.propNS, p.propLocal, a.val)
			break
		}
	}
}

// onStartCollection handles rdf:Alt, rdf:Seq, and rdf:Bag container elements
// immediately inside a property element.
// Compliance: XMP Part 1 §C.2.5.
//
// Task #273: also records the observed container kind for (p.propNS,
// p.propLocal) via recordContainerType (through startCollection below), so
// Encode can reproduce the source document's container structure exactly on
// round trip — see the containerTypes field doc in xmp.go for the full
// rationale. This happens before any rdf:li children are parsed, so a
// collection that turns out to hold exactly one item (or zero) is still
// recorded correctly.
//
// Allocation note: tl is used EXCLUSIVELY inside the tl == "…" comparisons
// below — never passed to a function call or stored — which lets the Go
// compiler apply its "compare a []byte-derived string against a literal
// without allocating" optimisation. startCollection is always called with
// one of the three string *literals* ("Alt"/"Seq"/"Bag" — references to
// static read-only data, never freshly allocated) rather than with tl
// itself; passing tl straight through to specTableMatches/
// recordContainerType instead would defeat that optimisation (the compiler
// must then materialise tl as a real heap string on every collection open,
// even when the record ends up being skipped) — verified via
// testing.AllocsPerRun bisection during development of this fix: 4 extra
// allocations per parse of a representative multi-collection document.
func (p *rdfParser) onStartCollection(ns string, tagLocal []byte) {
	tl := string(tagLocal) // zero-alloc compare (compiler optimisation)
	if ns != NSrdf {
		return
	}
	switch tl {
	case "Alt":
		p.startCollection("Alt")
	case "Seq":
		p.startCollection("Seq")
	case "Bag":
		p.startCollection("Bag")
	}
}

// startCollection marks the parser as inside a collection and records its
// container kind (task #273). ctype MUST always be called with one of the
// three string literals from onStartCollection above — see the allocation
// note there. Recording is skipped when specTableMatches reports that the
// spec-sourced table (namespace.go) already agrees with the observed
// container: Encode would reach the identical answer via its fallback path
// regardless, so recording it is redundant. This keeps parsing standard,
// spec-compliant XMP (the overwhelming majority of real-world files)
// allocation-free for this step; only a property whose container the table
// has never heard of, or gets wrong for this specific document, pays the
// recording cost — precisely the cases where it changes the outcome.
func (p *rdfParser) startCollection(ctype string) {
	p.inColl = true
	*p.liVals = (*p.liVals)[:0]
	if !specTableMatches(p.propNS, p.propLocal, ctype) {
		recordContainerType(p.x, p.propNS, p.propLocal, ctype)
	}
}

// onStartListItem handles rdf:li elements inside a collection, capturing any
// xml:lang attribute for rdf:Alt items.
// Compliance: XMP Part 1 §C.2.5 and P1-H.
//
// #13: Records the current depth as liItemDepth so that onEndElement can detect
// when the rdf:li closes and advance liItemIndex for the next sibling item.
//
// When applyAttrShorthands has already set inStruct=true via rdf:parseType="Resource"
// on this rdf:li element, the rdf:li itself is the struct node. Mark inStructInList so
// that onCharDataStructField generates "propLocal[N].field" keys instead of
// "propLocal.field". This handles the <rdf:li rdf:parseType="Resource"> shorthand form
// (XMP Part 1 §C.2.5 / §C.2.6).
func (p *rdfParser) onStartListItem(attrs []xmpAttr) {
	p.liLang = ""
	p.liItemDepth = p.depth
	for _, a := range attrs {
		// XML Namespaces §6.1: only xml:lang — i.e. a 'lang' attribute whose
		// namespace URI is the canonical XML namespace — carries language
		// information.  A bare unqualified lang="" attribute (a.ns=="") is NOT
		// xml:lang; accepting it would corrupt values by prepending a bogus lang
		// tag (finding #180).  The 'xml' prefix is permanently pre-bound to
		// "http://www.w3.org/XML/1998/namespace" by the XML specification and
		// does not require an explicit xmlns:xml declaration.
		if a.loc == "lang" && a.ns == "http://www.w3.org/XML/1998/namespace" {
			p.liLang = a.val
			break
		}
	}
	// If rdf:parseType="Resource" was already applied by applyAttrShorthands,
	// the rdf:li itself is the struct node — mark it as a struct-in-list-item.
	if p.inStruct && p.structDepth == p.depth {
		p.inStructInList = true
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
		p.onStartProperty(ns, tagLocal, attrs)

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

// onStartStructInListItem handles a nested rdf:Description inside an rdf:li,
// which is a struct-valued list item (e.g. xmpMM:History items).
//
// #13 — Struct-in-list-item: XMP Part 1 §C.2.5 / §C.2.6 allows rdf:li to
// contain a full rdf:Description as its value, producing a sequence/bag of
// structs. The field key format is "propLocal[N].fieldLocal" where N is the
// 0-based index of the rdf:li within the collection.
//
// Example: xmpMM:History is an rdf:Seq of stEvt structs. After parsing:
//   - Properties[NSxmpMM]["History[0].action"] = "saved"
//   - Properties[NSxmpMM]["History[0].instanceID"] = "xmp.iid:..."
//   - Properties[NSxmpMM]["History[1].action"] = "derived"
//
// This representation is consistent with the "parent.field" convention used
// for non-list structs (onStartStructValueNode), extended with a 0-based index.
func (p *rdfParser) onStartStructInListItem(attrs []xmpAttr) {
	p.inStruct = true
	p.inStructInList = true
	p.structDepth = p.depth
	// Process inline attributes as struct fields (shorthand form).
	for _, a := range attrs {
		if a.ns == "" || a.ns == NSrdf || a.ns == NSx {
			continue
		}
		// Use the property's namespace (not the attribute's) for the key,
		// consistent with onStartStructValueNode / onCharDataStructField.
		key := buildStructInListKey(p.propLocal, p.liItemIndex, a.loc)
		storeProperty(p.x, p.propNS, key, a.val)
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
// a top-level property block, a struct value node nested inside a property,
// or a struct-valued rdf:li inside a collection.
// Extracted from onStartElement to reduce its cyclomatic complexity.
//
// Compliance: XMP Part 1 §C.2.4 (shorthand properties) and §C.2.6 (struct value nodes).
// #13: Extends §C.2.5 with struct-in-list-item handling.
func (p *rdfParser) onStartDescription(attrs []xmpAttr) {
	switch {
	case p.inColl && p.depth == p.propDepth+3 && !p.inStruct:
		// Struct-in-list-item: rdf:Description inside rdf:li inside a collection.
		// Depth layout: prop=propDepth, coll=propDepth+1, li=propDepth+2,
		// rdf:Description=propDepth+3.
		// #13 — XMP Part 1 §C.2.5 / §C.2.6: an rdf:li may contain a nested
		// rdf:Description which forms a struct value for that list item.
		p.onStartStructInListItem(attrs)

	case p.propDepth > 0 && p.depth == p.propDepth+1 && !p.inStruct:
		// Struct value node: nested rdf:Description inside a property element
		// (XMP Part 1 §C.2.6). Store inline attrs as "propLocal.fieldLocal"
		// keys in the parent property's namespace.
		p.onStartStructValueNode(attrs)

	case p.descDepth == 0:
		// Top-level rdf:Description — begin a new property block.
		// Shorthand properties are inline attributes (XMP Part 1 §C.2.4).
		p.onStartTopLevelDesc(attrs)
	}
}

// onCharDataStructField stores the text content of a struct field element.
// Compliance: XMP Part 1 §C.2.6 (P1-G).
//
// #13: When inside a struct-in-list-item, the key uses the "propLocal[N].field"
// format; otherwise it uses the plain "propLocal.field" format (pre-existing).
// Both cases store into the parent property's namespace.
func (p *rdfParser) onCharDataStructField(s string) {
	// Always use the parent property's namespace for struct fields.
	// (structFieldNS is the element namespace of the field tag, which may differ
	// from the property namespace, but XMP storage is keyed by property namespace.)
	var key string
	if p.inStructInList {
		key = buildStructInListKey(p.propLocal, p.liItemIndex, p.structFieldLocal)
	} else {
		key = p.propLocal + "." + p.structFieldLocal
	}
	if p.x.Properties[p.propNS] == nil {
		p.x.Properties[p.propNS] = make(map[string]string)
	}
	if p.x.Properties[p.propNS][key] == "" {
		p.x.Properties[p.propNS][key] = s
	}
}

// onCharDataListItem appends text content inside an rdf:li element to the
// current collection accumulator, preserving xml:lang prefix for rdf:Alt items.
// Compliance: XMP Part 1 §C.2.5 (P1-H).
func (p *rdfParser) onCharDataListItem(s string) {
	if p.liLang != "" && p.liLang != "x-default" {
		// Build "lang|value" with a single allocation rather than two.
		// The two-step `p.liLang+"|"+s` first allocates for the intermediate
		// `p.liLang+"|"` string, then allocates again for the full concatenation.
		// strings.Builder with Grow produces exactly one allocation (the final string).
		bld := builderPool.Get().(*strings.Builder) //nolint:forcetypeassert,revive // builderPool.New always stores *strings.Builder; pool invariant
		bld.Reset()
		bld.Grow(len(p.liLang) + 1 + len(s))
		bld.WriteString(p.liLang)
		bld.WriteByte('|')
		bld.WriteString(s)
		result := bld.String()
		bld.Reset()
		builderPool.Put(bld)
		*p.liVals = append(*p.liVals, result)
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
	// Use package-level commentClose to avoid heap-allocating a []byte literal
	// on every call (the literal form []byte("-->") escapes to heap).
	end := bytes.Index(b[pos:], commentClose)
	if end < 0 {
		return len(b) // unterminated — skip to end
	}
	return pos + end + 3
}

// skipPI advances pos past an XML processing instruction <? ... ?>.
// b[pos] must be '?' at entry. Returns the updated position.
func skipPI(b []byte, pos int) int {
	// Use package-level piEnd to avoid heap-allocating a []byte literal
	// on every call (the literal form []byte("?>") escapes to heap).
	end := bytes.Index(b[pos:], piEnd)
	if end < 0 {
		return len(b)
	}
	return pos + end + 2
}

// skipBang advances pos past an XML <! ... > construct (DOCTYPE, etc.)
// b[pos] must be '!' at entry AND the construct must NOT be a CDATA section
// (callers check isCDATA first).  Returns the updated position.
//
// Anti-XXE: DOCTYPE and other <!…> constructs are silently skipped; entities
// within them are never expanded.
func skipBang(b []byte, pos int) int {
	end := bytes.IndexByte(b[pos:], '>')
	if end < 0 {
		return len(b)
	}
	return pos + end + 1
}

// isCDATA reports whether b[pos:] begins an XML CDATA section (![CDATA[).
// b[pos] must already be '!' at entry (i.e., the '<' has been consumed and
// b[pos] is '!').  XML 1.0 §2.7: CDSect ::= CDStart CData CDEnd;
// CDStart ::= '<![CDATA['.
func isCDATA(b []byte, pos int) bool {
	return bytes.HasPrefix(b[pos:], cdataOpen)
}

// parseCDATA extracts the character data content of a CDATA section.
// b[pos] must be '!' at entry (i.e., the '<' has been consumed and b[pos:]
// begins '![CDATA[...').
//
// Returns the raw content bytes (between '![CDATA[' and ']]>') and the updated
// position past the ']]>' close delimiter.  Content bytes are a zero-copy slice
// into b; the caller must not write to them.
//
// XML 1.0 §2.7: CDATA section content is literal character data; entity
// references within it are NOT expanded (the '&' character is literal).
// This is enforced here by returning the raw bytes without calling unescapeXML.
//
// If the CDATA section is unterminated (no ']]>' found), returns nil, len(b).
func parseCDATA(b []byte, pos int) (content []byte, newPos int) {
	// Skip '![CDATA[' — 8 bytes: '!', '[', 'C', 'D', 'A', 'T', 'A', '['.
	// len(cdataOpen) == 8.
	advance := len(cdataOpen)
	if pos+advance > len(b) {
		return nil, len(b)
	}
	pos += advance // b[pos] is now the first byte of the CDATA content

	end := bytes.Index(b[pos:], cdataEnd)
	if end < 0 {
		return nil, len(b) // unterminated — skip to end
	}
	content = b[pos : pos+end]    // zero-copy slice of the raw CDATA content
	return content, pos + end + 3 // +3 for "]]>"
}

// isComment reports whether b[pos:] begins an XML comment (<!--).
func isComment(b []byte, pos int) bool {
	return pos+2 < len(b) && b[pos] == '!' && b[pos+1] == '-' && b[pos+2] == '-'
}

// skipSpecialTag advances pos past a comment (<!-- ... -->), processing
// instruction (<? ... ?>), or non-CDATA bang (<! ... >) construct.
// CDATA sections are NOT handled here — they are handled by the caller
// via parseCDATA so that their content can be delivered as character data.
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
	case b[pos] == '!' && !isCDATA(b, pos):
		// Non-CDATA bang construct (DOCTYPE, etc.) — skip safely.
		// Anti-XXE: entities inside DOCTYPE are never expanded (skipBang
		// discards the entire construct without interpretation).
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
		return 0, ErrXMLNestingDepth
	}

	// #15: Record nsCount *before* this element's xmlns attrs are scanned,
	// so onEndElement can restore it (namespace scope pop).
	// ISO 16684-1 §7.4 / XML Namespaces §3: bindings scope to the declaring element.
	p.nsDepth[p.depth] = nsDepthEntry{nsCountBefore: p.nsCount}

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
	// We collect text in segments separated by CDATA sections.
	// Most elements have no CDATA — the fast path finds text before '<' directly.
	pos, text := collectTextContent(b, pos)

	if len(text) > 0 {
		p.onCharData(text)
	}

	return pos, nil
}

// collectTextContent gathers the text content of an element starting at b[pos],
// handling interleaved CDATA sections per XML 1.0 §2.7.
//
// The function concatenates:
//   - Raw text segments (entity-unescaped via unescapeXML)
//   - CDATA section content (delivered as-is — no entity expansion inside CDATA)
//
// Returns the updated position (pointing at the '<' of the next tag, or len(b))
// and the combined text string.  Returns an empty string when there is no text.
//
// Performance note: the common case (no CDATA) is handled without any allocation
// by the fast inner path.  The CDATA branch uses a pooled strings.Builder and
// allocates only when actually needed.
func collectTextContent(b []byte, pos int) (newPos int, text string) { //nolint:cyclop // CDATA fast/slow paths plus tag-boundary detection require this branching; refactoring would obscure the spec logic
	// Fast path: no CDATA in range — find text before '<'.
	textEnd := bytes.IndexByte(b[pos:], '<')
	if textEnd < 0 {
		// No '<' at all — all remaining bytes are text.
		s := unescapeXML(trimSpace(b[pos:]))
		return len(b), s
	}

	// There is a '<' ahead.  Peek at what follows it.
	ahead := pos + textEnd + 1 // position after '<'
	if ahead >= len(b) || !isCDATA(b, ahead) {
		// No CDATA involved — fast path: return text before '<', leave pos at '<'.
		s := unescapeXML(trimSpace(b[pos : pos+textEnd]))
		return pos + textEnd, s // do NOT advance past '<'; outer loop handles it
	}

	// Slow path: CDATA section(s) interleaved with text.
	// Use a pooled builder to concatenate segments without repeated allocs.
	bld := builderPool.Get().(*strings.Builder) //nolint:forcetypeassert,revive // builderPool.New always stores *strings.Builder; pool invariant
	bld.Reset()
	defer func() {
		bld.Reset()
		builderPool.Put(bld)
	}()

	for pos < len(b) {
		// Find the next '<'.
		nextTag := bytes.IndexByte(b[pos:], '<')
		if nextTag < 0 {
			// No more tags — append remaining text.
			bld.WriteString(unescapeXML(trimSpace(b[pos:])))
			pos = len(b)
			break
		}

		// Append text segment before '<'.
		seg := trimSpace(b[pos : pos+nextTag])
		if len(seg) > 0 {
			bld.WriteString(unescapeXML(seg))
		}
		pos = pos + nextTag + 1 // advance past '<'

		if pos >= len(b) {
			break
		}

		if isCDATA(b, pos) {
			// XML 1.0 §2.7: CDATA content is literal text — no entity expansion.
			// The content is appended as-is (raw bytes converted to string).
			content, afterCDATA := parseCDATA(b, pos)
			if len(content) > 0 {
				bld.Write(content)
			}
			pos = afterCDATA
			// Continue scanning for more text or CDATA after this section.
			continue
		}

		// Not a CDATA section — we've reached a real tag boundary.
		// Back up so the outer loop processes this '<'.
		pos-- // restore the '<'
		break
	}

	return pos, bld.String()
}

// XMLNamespaceURI is the canonical URI for the XML namespace, permanently
// pre-bound to the "xml" prefix per XML Namespaces §3 ("The 'xml' prefix is
// by definition bound to the namespace name http://www.w3.org/XML/1998/namespace").
// Using a package-level constant avoids re-allocating the string on every parseRDF call.
const XMLNamespaceURI = "http://www.w3.org/XML/1998/namespace"

// xmlPrefixBytes holds the 3-byte "xml" prefix as a package-level []byte.
// Pre-allocated to avoid a heap allocation inside parseRDF on every call.
var xmlPrefixBytes = []byte("xml") //nolint:gochecknoglobals // immutable sentinel; avoids per-call allocation

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
func parseRDF(b []byte, x *XMP) error { //nolint:cyclop,gocyclo // main XML dispatch loop has inherent branching (CDATA, PI, comment, end-tag, start-tag); each branch is a single XML construct
	p := rdfParser{x: x}

	// XML Namespaces §3: the 'xml' prefix is permanently pre-bound to
	// "http://www.w3.org/XML/1998/namespace" and must not be overridden.
	// Pre-populate it as entry 0 so resolveNS returns the correct URI for
	// attributes like xml:lang and xml:space without requiring an explicit
	// xmlns:xml declaration in the document.
	// This also enables finding #180's fix: onStartListItem now accepts only
	// a.ns==XMLNamespaceURI (not the empty string), and the pre-population
	// ensures real xml:lang resolves to that URI rather than "".
	p.nsTable[0] = nsEntry{prefix: xmlPrefixBytes, uri: XMLNamespaceURI}
	p.nsCount = 1

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

		// ── CDATA section (XML 1.0 §2.7) ───────────────────────────────────
		// CDATA is checked before the generic skipSpecialTag so that its
		// character-data content is delivered to onCharData rather than discarded.
		if isCDATA(b, pos) {
			content, afterCDATA := parseCDATA(b, pos)
			pos = afterCDATA
			if len(content) > 0 {
				// XML 1.0 §2.7: no entity expansion inside CDATA; string(content)
				// copies the raw CDATA bytes into a new string value.
				p.onCharData(string(content))
			}
			if pos >= len(b) {
				break
			}
			continue
		}

		// ── Comment, PI, DOCTYPE ─────────────────────────────────────────────
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
//
// XML 1.0 §2.3 (NameStartChar, NameChar): '<' is not a legal XML name character.
// Including it as a name terminator prevents a crafted document from smuggling
// '<' into a stored local name, which would allow XML injection when that name
// is later emitted unescaped in Encode (#171).
func isNameTerminator(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
		c == '>' || c == '/' || c == '=' || c == '<'
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
//
// #15: nsTable is [64]nsEntry and out is [32]xmpAttr (up from 32 and 16)
// to handle legitimate complex documents without silent data loss.
func scanAttrs(b []byte, pos int, nsTable *[64]nsEntry, nsCount int, out *[32]xmpAttr) (newNsCount, nAttrs, newPos int) { //nolint:cyclop,gocyclo // attribute scanning loop inherently branches on >/<//=, whitespace, and the classifyAndStoreAttr dispatch; refactoring would obscure the XML grammar
	nAttrs = 0
	// XML 1.0 §3.1: the attribute list ends at '>' (tag end) or '/' (self-close).
	// Also stop at '<': a '<' inside a tag is illegal (XML 1.0 §2.4) and would
	// cause parseSingleAttr to return without advancing pos, producing an infinite
	// loop.  Stopping here is safe — the malformed tag will be silently dropped by
	// the parser's lenient design (#171 defence-in-depth: isNameTerminator also
	// rejects '<' in name scanning, so this guard handles the outer loop boundary).
	for pos < len(b) && b[pos] != '>' && b[pos] != '/' && b[pos] != '<' {
		// Skip whitespace between attributes.
		for pos < len(b) && isASCIISpace(b[pos]) {
			pos++
		}
		if pos >= len(b) || b[pos] == '>' || b[pos] == '/' || b[pos] == '<' {
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
//
// #15: nsTable is [64]nsEntry and out is [32]xmpAttr (up from 32 and 16).
func classifyAndStoreAttr(attrPrefix, attrLocal []byte, val string, nsTable *[64]nsEntry, nsCount int, out *[32]xmpAttr, nAttrs int) (int, int) {
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
	for _, e := range slices.Backward(table) {
		if bytes.Equal(e.prefix, prefix) {
			return e.uri
		}
	}
	return ""
}

// unescapeXML converts b to a string, replacing the five predefined XML
// entities and numeric character references. When b contains no '&', it
// returns string(b) directly (one allocation, no builder overhead).
//
// Fix #72: The fast path previously returned unsafe.String(unsafe.SliceData(b), len(b)),
// a string whose backing memory IS the caller's parse buffer. Any mutation of that
// buffer after Parse returned (sync.Pool reuse, mmap, shared-buffer architectures)
// silently corrupted all entity-free property values with no error signal.
// The fix is to return string(b) — a standard Go heap copy — which eliminates the
// aliasing entirely. The allocation cost is one string copy per entity-free text
// segment, identical to what the slow (entity) path already paid. Benchmark
// evidence: BenchmarkRDFParse confirms the per-call alloc count and ns/op are
// within the expected range after this change (see bench_test.go).
func unescapeXML(b []byte) string {
	if bytes.IndexByte(b, '&') < 0 {
		// Fast path: no XML entities present — copy into a new string so that
		// the returned value is independent of b's backing array. This is the
		// standard string(b) conversion: one heap allocation, one copy.
		//
		// We deliberately do NOT use unsafe.String here. unsafe.String would
		// return a string that shares b's backing array; if the caller mutates
		// or reuses b after Parse returns (sync.Pool, mmap, streaming read),
		// every stored property value would be silently corrupted. The previous
		// comment "b is kept alive by the caller via the parent slice" only
		// addressed GC liveness — it did not address the mutation hazard.
		//
		// Task #72 / data-corruption fix: replace unsafe alias with safe copy.
		if len(b) == 0 {
			return ""
		}
		return string(b)
	}

	bld := builderPool.Get().(*strings.Builder) //nolint:forcetypeassert,revive // builderPool.New always stores *strings.Builder; pool invariant
	bld.Reset()
	bld.Grow(len(b))

	for i := 0; i < len(b); {
		if b[i] != '&' {
			bld.WriteByte(b[i])
			i++
			if bld.Len() > maxUnescapedXMLBytes {
				bld.Reset()
				builderPool.Put(bld)
				return ""
			}
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
		if bld.Len() > maxUnescapedXMLBytes {
			bld.Reset()
			builderPool.Put(bld)
			return ""
		}
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
//
// Named-entity lookup is delegated to decodeNamedEntity, which switches on
// length and compares individual bytes directly — zero-alloc. The previous
// bytes.Equal(ref, []byte("amp")) form allocated a []byte literal on the heap
// on every call.
func decodeEntity(ref []byte, bld *strings.Builder) {
	if len(ref) > 1 && ref[0] == '#' {
		// Numeric character reference: &#N; or &#xHH;
		decodeCharRef(ref[1:], bld)
		return
	}
	if decodeNamedEntity(ref, bld) {
		return
	}
	// Unknown entity — emit the original reference.
	bld.WriteByte('&')
	bld.Write(ref)
	bld.WriteByte(';')
}

// decodeNamedEntity writes the single character for one of the five predefined
// XML named entities (&amp; &lt; &gt; &quot; &apos;) to bld.
// Returns true if ref matched a predefined entity, false otherwise.
//
// The Go compiler optimises switch string(ref) { case "amp": ... } to a zero-alloc
// byte comparison when the switch operand is a []byte-to-string conversion and the
// cases are string literals. This avoids both the []byte literal heap allocation of
// bytes.Equal(ref, []byte("amp")) and the cyclomatic complexity of a manual
// length+byte-index dispatch.
func decodeNamedEntity(ref []byte, bld *strings.Builder) bool {
	switch string(ref) {
	case "amp":
		bld.WriteByte('&')
	case "lt":
		bld.WriteByte('<')
	case "gt":
		bld.WriteByte('>')
	case "quot":
		bld.WriteByte('"')
	case "apos":
		bld.WriteByte('\'')
	default:
		return false
	}
	return true
}

// parseHex parses a hexadecimal rune reference (without the leading "x").
//
// XML 1.0 §4.1: a numeric character reference is only valid when the code
// point is a legal XML character and a Unicode scalar value (≤ U+10FFFF,
// not a surrogate U+D800–U+DFFF). References outside this range are rejected
// by returning (0, false), which causes decodeCharRef to return false and
// decodeEntity to emit the original &ref; literal — a safe, spec-conformant
// fallback. This mirrors the guard in decodeUTF32 (encoding.go).
//
//nolint:gocyclo // switch on hex digit range is inherently > 10 complexity; refactoring would reduce readability without correctness benefit
func parseHex(b []byte) (rune, bool) {
	// Reject inputs that are obviously too long to represent a valid Unicode
	// scalar value (U+10FFFF = 6 hex digits). More than 7 digits can only
	// produce a value > 0x10FFFF or overflow int32; reject early.
	if len(b) > 7 {
		return 0, false
	}
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
	// XML 1.0 §4.1 / Unicode §3.9: reject surrogates and values above U+10FFFF.
	if v > unicode.MaxRune || (v >= 0xD800 && v <= 0xDFFF) {
		return 0, false
	}
	return v, true
}

// parseDec parses a decimal rune reference.
//
// XML 1.0 §4.1: a numeric character reference is only valid when the code
// point is a legal XML character and a Unicode scalar value (≤ U+10FFFF,
// not a surrogate U+D800–U+DFFF). References outside this range are rejected
// by returning (0, false), which causes decodeCharRef to return false and
// decodeEntity to emit the original &ref; literal — a safe, spec-conformant
// fallback. This mirrors the guard in decodeUTF32 (encoding.go).
func parseDec(b []byte) (rune, bool) {
	// Reject inputs that are obviously too long to represent a valid Unicode
	// scalar value (U+10FFFF = 1114111 = 7 decimal digits). More than 7 digits
	// can only produce a value > 0x10FFFF or overflow int32; reject early.
	if len(b) > 7 {
		return 0, false
	}
	var v rune
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + rune(c-'0')
	}
	// XML 1.0 §4.1 / Unicode §3.9: reject surrogates and values above U+10FFFF.
	if v > unicode.MaxRune || (v >= 0xD800 && v <= 0xDFFF) {
		return 0, false
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

// recordContainerType records the RDF collection kind (ctype: "Alt", "Seq",
// or "Bag") observed for property (ns, local) at parse time.
//
// Task #273 / ISO 16684-1 §7.5: round-trip fidelity requires reproducing the
// source document's container structure for ANY property in ANY namespace,
// not just the ones enumerated in namespace.go's spec-sourced table. See the
// XMP.containerTypes field doc (xmp.go) for the full design rationale, and
// startCollection's doc for why ctype must always be a string literal.
//
// Allocates x.containerTypes lazily, mirroring storeProperty's lazy
// allocation of x.Properties below: a document with no collections needing
// an override at all never allocates this map, so the zero/low-alloc parse
// fast path for standard, spec-compliant XMP is unaffected.
func recordContainerType(x *XMP, ns, local, ctype string) {
	if x.containerTypes == nil {
		x.containerTypes = make(map[string]map[string]string)
	}
	if x.containerTypes[ns] == nil {
		x.containerTypes[ns] = make(map[string]string)
	}
	x.containerTypes[ns][local] = ctype
}

// storeProperty writes val to x.Properties[ns][local], initialising inner maps
// as needed.
//
// Policy: first writer wins.
//
// Rationale (#12): ISO 16684-1 §7.4 permits multiple rdf:Description blocks in the
// same namespace (common when different tools contribute metadata). If two blocks
// declare the same property, the first declaration encountered during a forward
// document parse takes precedence. This is consistent with onCharDataSimple (line
// below) and with the XMP spec's intent that a property has one canonical value.
// "First wins" is also the safest choice for round-trip fidelity: the first value
// is the one the originating tool explicitly set; later duplicates are often
// artefacts of copy-on-write tool workflows.
func storeProperty(x *XMP, ns, local, val string) {
	if x.Properties[ns] == nil {
		x.Properties[ns] = make(map[string]string)
	}
	// #12: guard against overwrite — first-wins policy.
	if x.Properties[ns][local] != "" {
		return
	}
	x.Properties[ns][local] = val
}

// buildStructInListKey builds the storage key for a struct field within a
// list item, using the format "propLocal[index].fieldLocal".
//
// #13 — XMP Part 1 §C.2.5 / §C.2.6: rdf:li may contain a nested rdf:Description
// forming a struct value for that list item. We extend the "parent.field" dotted-key
// convention with a 0-based bracket index so array entries are distinguishable.
//
// Allocation budget: one strings.Builder call (one alloc for the final string).
// The builder is stack-allocated; no pool needed for this low-frequency path.
func buildStructInListKey(propLocal string, idx int, fieldLocal string) string {
	// Fast path for small indices: avoid strconv.Itoa overhead.
	// Maximum realistic xmpMM:History depth is a few hundred entries.
	var buf [32]byte
	n := 0
	// Write propLocal (does not allocate when assigned from a field that is already
	// a string literal — the builder WriteString copies).
	bld := strings.Builder{}
	bld.Grow(len(propLocal) + 12 + len(fieldLocal))
	bld.WriteString(propLocal)
	bld.WriteByte('[')
	// Render idx as decimal digits into buf.
	if idx == 0 {
		buf[0] = '0'
		n = 1
	} else {
		i := idx
		for i > 0 {
			buf[n] = byte('0' + i%10)
			n++
			i /= 10
		}
		// Reverse the digits.
		for lo, hi := 0, n-1; lo < hi; lo, hi = lo+1, hi-1 {
			buf[lo], buf[hi] = buf[hi], buf[lo]
		}
	}
	bld.Write(buf[:n])
	bld.WriteByte(']')
	bld.WriteByte('.')
	bld.WriteString(fieldLocal)
	return bld.String()
}
