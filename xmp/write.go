package xmp

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// xmpPadding is the pre-computed 2 KB whitespace padding block for XMP in-place
// editing (XMP §7.3). Initialised once at package load; never mutated.
var xmpPadding = func() [2048]byte { //nolint:gochecknoglobals // package-level constant bytes
	var b [2048]byte
	for i := range b {
		if (i+1)%100 == 0 {
			b[i] = '\n'
		} else {
			b[i] = ' '
		}
	}
	return b
}()

// encBufPool recycles bytes.Buffer instances across encode calls.
// Pre-grown buffers avoid the repeated backing-array reallocations that
// occur when building an XMP packet from scratch.
var encBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}                 //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
var nsListPool = sync.Pool{New: func() any { s := make([]string, 0, 8); return &s }}     //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
var localListPool = sync.Pool{New: func() any { s := make([]string, 0, 16); return &s }} //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure

// serialise encodes x to a padded XMP packet.
// The packet uses UTF-8 encoding and a read/write <?xpacket?> wrapper
// with 2 KB of whitespace padding per XMP §7.3 (in-place editing support).
//
// Each namespace is written in its own rdf:Description, in URI order. A
// namespace's retained tree properties (XMP.nodes) come first, in document
// order, each written unchanged, merged with its edited Properties entries,
// or omitted when all its entries were deleted. The remaining Properties
// entries follow, written from their keys alone (classifyProps). serialise
// never modifies x: merges are applied to a pooled scratch copy.
func serialise(x *XMP) ([]byte, error) {
	return serialiseMode(x, serialiseDefault)
}

// serialiseOpts selects the form of a serialised packet.
type serialiseOpts uint8

const (
	// serialiseDefault is the form Encode writes: <?xpacket?> wrapper, 2 KB
	// of padding, one formatting line per element.
	serialiseDefault serialiseOpts = 0
	// serialiseCompact removes the formatting white space between elements
	// (see compactMarkup) and the padding. XMP Specification Part 3 §1.1.3.2
	// (p. 15): the StandardXMP of a JPEG should be as small as possible,
	// "eliminating all formatting whitespace" and padding.
	serialiseCompact serialiseOpts = 1 << iota
	// serialiseNoWrapper omits the <?xpacket?> processing instructions:
	// Part 3 §1.1.3.1 (p. 14) requires the ExtendedXMP of a JPEG to be
	// "serialized without a packet wrapper".
	serialiseNoWrapper
)

// serialiseMode is serialise with the form chosen by mode; see serialiseOpts.
// A compact or unwrapped packet carries no padding.
func serialiseMode(x *XMP, mode serialiseOpts) ([]byte, error) {
	buf, err := serialiseBuf(x, mode)
	if err != nil {
		return nil, err
	}
	// Copy the result before returning the buffer to the pool so callers own
	// their slice independently of the pooled backing array.
	result := bytes.Clone(buf.Bytes())
	encBufPool.Put(buf)
	return result, nil
}

// serialiseBuf is serialiseMode without the final copy: it returns the pooled
// buffer that holds the packet, which the caller must hand back to encBufPool
// once it has read it. It lets a caller that only needs the size of a packet
// (PackageForJPEG) skip the copy.
func serialiseBuf(x *XMP, mode serialiseOpts) (*bytes.Buffer, error) {
	compact := mode&serialiseCompact != 0
	wrapper := mode&serialiseNoWrapper == 0
	buf := encBufPool.Get().(*bytes.Buffer) //nolint:forcetypeassert,revive // encBufPool.New always stores *bytes.Buffer; pool invariant
	buf.Reset()

	// Estimate output size: fixed wrapper (~250 B) + 2 KB padding + ~100 B per property.
	nProps := len(x.nodes)
	for _, props := range x.Properties {
		nProps += len(props)
	}
	buf.Grow(256 + 2048 + nProps*100)

	writePacketHead(buf, x, wrapper)

	// Sort namespace URIs for deterministic output (ISO 16684-1 §7.4).
	nsListPtr := nsListPool.Get().(*[]string) //nolint:forcetypeassert,revive // nsListPool.New always stores *[]string; pool invariant
	nsList := x.namespaces((*nsListPtr)[:0])
	// Write-back: if append grew the slice, update the pointer so the pool
	// gets the larger backing array next time.
	*nsListPtr = nsList
	defer nsListPool.Put(nsListPtr)

	// NS-03 / XMP Part 1 §6 / XML Namespaces: the writer MUST NOT bind two
	// distinct URIs to the same prefix.  Well-known URIs have fixed canonical
	// prefixes (prefixMap).  Unknown URIs reuse the prefix of the source
	// document when it is free, and otherwise fall back to generated prefixes
	// ns0, ns1, … using a per-call counter so sibling unknown namespaces always
	// get distinct prefixes regardless of insertion order.
	usedPrefixes := make(map[string]struct{}, len(nsList)+3)
	// Pre-populate with all well-known prefixes that will be used in this packet,
	// so generated ns0/ns1/… never accidentally collide with a canonical prefix.
	for _, ns := range nsList {
		if p, ok := prefixMap[ns]; ok {
			usedPrefixes[p] = struct{}{}
		}
	}
	// The same holds for well-known namespaces used only below the top level
	// of retained properties (fields, qualifiers, blank nodes): their
	// canonical prefix must be reserved before an unknown namespace may
	// reuse a source prefix.
	x.reserveNestedPrefixes(usedPrefixes)
	enc := encoder{x: x, buf: buf, pf: prefixer{used: usedPrefixes, src: x.prefixes}}
	for _, ns := range nsList {
		enc.pf.prefix(ns)
	}

	if len(x.nodes) > 0 {
		sc := encScratchPool.Get().(*encScratch) //nolint:forcetypeassert,revive // encScratchPool.New always stores *encScratch; pool invariant
		enc.sc = sc
		defer releaseEncScratch(sc)
	}

	for _, ns := range nsList {
		if s := x.schemaIndex(ns); s != noNode {
			enc.writeSchema(ns, s)
			continue
		}
		props := x.Properties[ns]
		prefix := enc.pf.prefix(ns)
		enc.startDescription(ns, prefix, nil)

		// Sort property names for deterministic output.
		localListPtr := localListPool.Get().(*[]string) //nolint:forcetypeassert,revive // localListPool.New always stores *[]string; pool invariant
		localList := (*localListPtr)[:0]
		for local := range props {
			localList = append(localList, local)
		}
		slices.Sort(localList)
		*localListPtr = localList

		writeKeyedProperties(buf, x, prefix, ns, props, localList, "")

		// #151: localListPool.Put MUST follow the last use of localList.
		// Putting the pointer back to the pool earlier would allow another
		// goroutine to receive and overwrite the backing slice while this
		// call is still iterating it.
		localListPool.Put(localListPtr)
		buf.WriteString("  </rdf:Description>\n")
	}

	enc.writeBlankBlocks()
	buf.WriteString(" </rdf:RDF>\n</x:xmpmeta>\n")

	writePacketTail(buf, wrapper, compact)
	if compact {
		out := buf.Bytes()
		buf.Truncate(len(compactMarkup(out)))
	}
	return buf, nil
}

// writePacketHead writes the start of a packet: the <?xpacket?> processing
// instruction when wrapper is true, the x:xmpmeta element and the rdf:RDF
// element.
func writePacketHead(buf *bytes.Buffer, x *XMP, wrapper bool) {
	// Opening packet wrapper with UTF-8 BOM marker (XMP §7.1).
	if wrapper {
		buf.WriteString("<?xpacket begin=\"\xef\xbb\xbf\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n")
	}
	buf.WriteString("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"")
	if x.toolkit != "" {
		// XMP Part 1 §7.3.3: x:xmptk names the toolkit that wrote the packet;
		// a parsed value is kept so that a round trip does not lose it.
		buf.WriteString(" x:xmptk=\"")
		writeXMLEscaped(buf, x.toolkit)
		buf.WriteByte('"')
	}
	buf.WriteString(">\n")
	buf.WriteString(" <rdf:RDF xmlns:rdf=\"http://www.w3.org/1999/02/22-rdf-syntax-ns#\">\n")
}

// writePacketTail writes the end of a packet that writePacketHead started,
// after the rdf:RDF element: the padding (unless compact) and the closing
// processing instruction when wrapper is true.
func writePacketTail(buf *bytes.Buffer, wrapper, compact bool) {
	if !wrapper {
		return
	}
	if !compact {
		// 2 KB padding of spaces / newlines for in-place editing (XMP §7.3).
		// Uses the pre-computed package-level array to avoid a per-call allocation.
		buf.Write(xmpPadding[:])
		buf.WriteString("\n")
	}
	buf.WriteString("<?xpacket end=\"w\"?>")
}

// reserveNestedPrefixes adds to used the canonical prefix of every
// well-known namespace that occurs in the retained tree.
func (x *XMP) reserveNestedPrefixes(used map[string]struct{}) {
	for i := range x.nodes {
		if p, ok := prefixMap[x.nodes[i].ns]; ok {
			used[p] = struct{}{}
		}
	}
}

// namespaces appends to dst, sorted, every namespace that has Properties
// entries or retained tree properties.
func (x *XMP) namespaces(dst []string) []string {
	for ns, props := range x.Properties {
		if len(props) > 0 {
			dst = append(dst, ns)
		}
	}
	if len(x.nodes) > 0 {
		for s := x.nodes[0].firstChild; s != noNode; s = x.nodes[s].next {
			if x.nodes[s].flags&nodeBlank != 0 {
				continue
			}
			if ns := x.nodes[s].ns; len(x.Properties[ns]) == 0 && x.nodes[s].firstChild != noNode {
				dst = append(dst, ns)
			}
		}
	}
	slices.Sort(dst)
	return dst
}

// owned returns, in key order, the keys of sc.keys that belong to the
// retained property with ordinal o. The first call groups every bucketed key
// by ordinal in one pass (a counting sort over sc.counts), so a schema with
// many modified properties is handled in linear time.
func (sc *encScratch) owned(o int) []string {
	if len(sc.byOrd) == 0 {
		sc.start = slices.Grow(sc.start[:0], len(sc.counts)+1)[:len(sc.counts)+1]
		sc.start[0] = 0
		for i, n := range sc.counts {
			sc.start[i+1] = sc.start[i] + n
		}
		total := sc.start[len(sc.counts)]
		sc.byOrd = slices.Grow(sc.byOrd[:0], total)[:total]
		next := slices.Clone(sc.start[:len(sc.counts)])
		for i, k := range sc.keys {
			if ord := sc.ords[i]; ord >= 0 {
				sc.byOrd[next[ord]] = k
				next[ord]++
			}
		}
	}
	return sc.byOrd[sc.start[o]:sc.start[o+1]]
}

// releaseEncScratch returns sc to its pool, clearing every reference to
// caller-owned strings first. It is a function rather than an encoder method
// so that the encoder, and the prefix map it holds, stay on the stack.
func releaseEncScratch(sc *encScratch) {
	clear(sc.nodes)
	sc.nodes = sc.nodes[:0]
	sc.keyBuf = sc.keyBuf[:0]
	sc.items = sc.items[:0]
	clear(sc.keys)
	sc.keys = sc.keys[:0]
	clear(sc.keyed)
	sc.keyed = sc.keyed[:0]
	clear(sc.extra)
	sc.extra = sc.extra[:0]
	clear(sc.byOrd)
	sc.byOrd = sc.byOrd[:0]
	clearMap(sc.seen)
	sc.props = sc.props[:0]
	encScratchPool.Put(sc)
}

// startDescription writes the rdf:Description start tag of namespace ns,
// bound to prefix, declaring also every namespace in extra.
func (e *encoder) startDescription(ns, prefix string, extra []string) {
	buf := e.buf
	buf.WriteString("  <rdf:Description rdf:about=\"")
	writeXMLEscaped(buf, e.x.about)
	buf.WriteString("\" xmlns:")
	buf.WriteString(prefix)
	buf.WriteString("=\"")
	// #170 / XML 1.0 §3.1 / ISO 16684-1 §7: the namespace URI is written as
	// an XML attribute value and MUST be XML-escaped.  An unescaped URI can
	// contain '"', '<', or '&' (e.g., a URI decoded from &quot; while parsing
	// untrusted XMP), which would break the attribute boundary and inject
	// arbitrary XML.  writeXMLEscaped handles all five XML entities plus C0
	// control chars, making the value safe for attribute context.
	writeXMLEscaped(buf, ns)
	buf.WriteByte('"')
	for _, u := range extra {
		if u == ns {
			continue
		}
		buf.WriteString(" xmlns:")
		buf.WriteString(e.pf.prefix(u))
		buf.WriteString("=\"")
		writeXMLEscaped(buf, u)
		buf.WriteByte('"')
	}
	buf.WriteString(">\n")
}

// writeSchema writes the rdf:Description of namespace ns, whose retained
// schema node is s: first the retained properties, then the Properties
// entries that belong to no retained property.
func (e *encoder) writeSchema(ns string, s int32) { //nolint:cyclop,gocyclo // classification, status, and emission of the two property sources of one namespace
	x := e.x
	t := x.nodes
	props := x.Properties[ns]
	sc := e.sc

	// The retained properties of the schema, in document order.
	sc.props = sc.props[:0]
	for c := t[s].firstChild; c != noNode; c = t[c].next {
		sc.props = append(sc.props, c)
	}
	names := propNames{t: t, props: sc.props}
	if len(sc.props) > dupLinearLimit {
		names.index = make(map[string]int, len(sc.props))
		for o, c := range sc.props {
			if _, dup := names.index[t[c].name]; !dup {
				names.index[t[c].name] = o
			}
		}
	}

	// Sort the keys and bucket them by retained property.
	sc.keys = sc.keys[:0]
	for k := range props {
		sc.keys = append(sc.keys, k)
	}
	slices.Sort(sc.keys)
	sc.counts = slices.Grow(sc.counts[:0], len(sc.props))[:len(sc.props)]
	clear(sc.counts)
	sc.keyed = sc.keyed[:0]
	sc.ords = sc.ords[:0]
	clear(sc.byOrd)
	sc.byOrd = sc.byOrd[:0]
	for _, k := range sc.keys {
		o := names.bucketOf(k)
		sc.ords = append(sc.ords, o)
		if o >= 0 {
			sc.counts[o]++
		} else {
			sc.keyed = append(sc.keyed, k)
		}
	}

	sc.status = slices.Grow(sc.status[:0], len(sc.props))[:len(sc.props)]
	write := len(sc.keyed) > 0
	sc.extra = sc.extra[:0]
	clearMap(sc.seen)
	for o, c := range sc.props {
		sc.status[o] = e.status(c, props, sc.counts[o])
		if sc.status[o] != propDeleted {
			write = true
			sc.extra = collectNS(t, c, sc.extra, sc.seen)
		}
	}
	if !write {
		return
	}
	for _, u := range sc.extra {
		e.pf.prefix(u)
	}
	prefix := e.pf.prefix(ns)
	e.startDescription(ns, prefix, sc.extra)

	for o, c := range sc.props {
		switch sc.status[o] {
		case propDeleted:
		case propUnchanged:
			e.emitNode(t, c, 3, false)
		default:
			own := sc.owned(o)
			if r, ok := e.merge(c, props, own); ok {
				e.emitNode(sc.nodes, r, 3, false)
			} else {
				ctype := ""
				if t[c].isArray() {
					ctype = t[c].containerName()
				}
				writeKeyedProperties(e.buf, x, prefix, ns, props, own, ctype)
			}
		}
	}
	writeKeyedProperties(e.buf, x, prefix, ns, props, sc.keyed, "")
	e.buf.WriteString("  </rdf:Description>\n")
}

// writeKeyedProperties writes the Properties entries localList (sorted) of
// namespace ns from their keys alone: plain keys as simple or array
// properties, "parent.field" keys as structs and "parent[N].field" keys as
// arrays of structs. ctype, when not empty, is the container to use for
// arrays instead of the spec-sourced default.
func writeKeyedProperties(buf *bytes.Buffer, x *XMP, prefix, ns string, props map[string]string, localList []string, ctype string) {
	// #14: Classify properties into three groups before serialising:
	//   1. Top-level properties (no dot, or dot not preceded by "[N]") → simple / multi-valued.
	//   2. Struct properties (key "parent.field") → must be emitted under one
	//      <prefix:parent rdf:parseType="Resource"> wrapper per parent key.
	//   3. Struct-in-list-item properties (key "parent[N].field") → must be emitted
	//      as a sequence/bag of rdf:Description elements.
	topProps, structParents, listStructParents := classifyProps(localList)

	for _, local := range topProps {
		val := props[local]
		// #RECONCILE-05 / task #273: an array-typed property MUST always use
		// its rdf:Alt/Seq/Bag collection container per ISO 16684-1 §7.5,
		// even when it holds exactly one item — the presence of the internal
		// U+001E multi-item separator is not, by itself, a reliable signal of
		// array-ness. effectiveContainerType carries that knowledge from the
		// spec-sourced table; a parsed property whose container differs from
		// the table is retained in the tree and never reaches this function
		// unmodified (task #303).
		//
		// Task #274 defense-in-depth assessment: deferring to
		// effectiveContainerType ENTIRELY (ignoring \x1e) was rejected —
		// isColl==false covers BOTH a spec-known Simple property AND a new
		// custom property, and a caller joining values via Set() on a custom
		// property relies on the \x1e branch below to get a collection.
		// Numeric character references cannot smuggle U+001E into a parsed
		// value (isForbiddenXMLCharRef in rdf.go).
		ct, isColl := x.effectiveContainerType(ns, local)
		if ctype != "" {
			ct, isColl = ctype, true
		}
		if strings.IndexByte(val, '\x1e') < 0 && !isColl {
			writeSimpleProperty(buf, prefix, local, val)
		} else {
			if ct == "" {
				// The value carries the \x1e separator but (ns, local) has no
				// spec-table entry — e.g. a caller joined values manually via
				// Set() for an unrecognised property. Fall back to the
				// historical default container.
				ct = collectionType(ns, local)
			}
			writeMultiValuedProperty(buf, prefix, local, val, ct)
		}
	}
	for _, parent := range structParents {
		writeStructProperty(buf, prefix, parent, props, localList)
	}
	for _, parent := range listStructParents {
		writeStructInListProperty(buf, x, prefix, ns, parent, props, localList, ctype)
	}
}

// writeSimpleProperty writes a single-valued XMP property element to buf.
// Produces: <prefix:local>val</prefix:local>\n with XML escaping applied to val.
//
// #171 / XML 1.0 §2.3: local is emitted as an XML element tag name and must be
// a legal NCName.  writeXMLName strips any byte that would inject XML structure.
func writeSimpleProperty(buf *bytes.Buffer, prefix, local, val string) {
	buf.WriteString("   <")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	writeXMLName(buf, local)
	buf.WriteByte('>')
	writeXMLEscaped(buf, val)
	buf.WriteString("</")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	writeXMLName(buf, local)
	buf.WriteString(">\n")
}

// writeMultiValuedProperty writes a multi-valued XMP property element to buf.
// val is a '\x1e'-delimited list of values (or, for a single-item array-typed
// property, a value with no separator at all). ctype is the RDF collection
// kind ("Alt", "Seq", or "Bag") to use, resolved by the caller via
// effectiveContainerType per ISO 16684-1 §7.5 (task #273: this used to be
// resolved internally via collectionType(ns, local), which only ever
// consulted the spec-sourced table — the caller now resolves it so that a
// property's own parse-time-observed container takes priority; see
// effectiveContainerType in namespace.go). For Alt collections, items may
// carry an xml:lang prefix encoded as "lang|value".
//
// #171 / XML 1.0 §2.3: local is emitted as an XML element tag name; writeXMLName
// strips illegal NCName bytes to prevent XML injection.
func writeMultiValuedProperty(buf *bytes.Buffer, prefix, local, val, ctype string) {
	buf.WriteString("   <")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	writeXMLName(buf, local)
	buf.WriteString(">\n    <rdf:")
	buf.WriteString(ctype)
	buf.WriteString(">\n")
	// Zero-alloc iteration: uses strings.IndexByte instead of strings.Split to
	// avoid a []string heap allocation on every call (mirrors the pattern in
	// Keywords() in xmp.go).
	start := 0
	for {
		end := strings.IndexByte(val[start:], '\x1e')
		var v string
		if end < 0 {
			v = val[start:]
		} else {
			v = val[start : start+end]
		}
		if ctype == "Alt" {
			// Preserve xml:lang if stored as "lang|value" (P1-H).
			lang, altVal, hasLang := strings.Cut(v, "|")
			if hasLang {
				buf.WriteString("     <rdf:li xml:lang=\"")
				writeXMLEscaped(buf, lang)
				buf.WriteString("\">")
				writeXMLEscaped(buf, altVal)
			} else {
				buf.WriteString("     <rdf:li xml:lang=\"x-default\">")
				writeXMLEscaped(buf, v)
			}
		} else {
			buf.WriteString("     <rdf:li>")
			writeXMLEscaped(buf, v)
		}
		buf.WriteString("</rdf:li>\n")
		if end < 0 {
			break
		}
		start += end + 1
	}
	buf.WriteString("    </rdf:")
	buf.WriteString(ctype)
	buf.WriteString(">\n   </")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	writeXMLName(buf, local)
	buf.WriteString(">\n")
}

// classifyProps partitions a sorted list of property local names into three groups:
//   - topProps: plain top-level properties (no dot, or multi-valued with dot in values).
//   - structParents: sorted unique parent names of "parent.field" struct properties.
//   - listStructParents: sorted unique parent names of "parent[N].field" struct-in-list properties.
//
// #14: This classification drives the serialiser's dispatch between writeSimpleProperty,
// writeStructProperty, and writeStructInListProperty.
func classifyProps(localList []string) (topProps, structParents, listStructParents []string) {
	structParentSet := make(map[string]struct{})
	listStructParentSet := make(map[string]struct{})
	for _, local := range localList {
		parent, _, isListStruct, isStruct := parseStructKey(local)
		switch {
		case isListStruct:
			listStructParentSet[parent] = struct{}{}
		case isStruct:
			structParentSet[parent] = struct{}{}
		default:
			topProps = append(topProps, local)
		}
	}
	for p := range structParentSet {
		structParents = append(structParents, p)
	}
	for p := range listStructParentSet {
		listStructParents = append(listStructParents, p)
	}
	slices.Sort(structParents)
	slices.Sort(listStructParents)
	return topProps, structParents, listStructParents
}

// parseStructKey inspects a property local name and classifies it:
//   - "parent[N].field" → parent="parent", field="field", isListStruct=true, isStruct=false
//   - "parent.field"    → parent="parent", field="field", isListStruct=false, isStruct=true
//   - "plain"           → parent="", field="plain", isListStruct=false, isStruct=false
//
// #14 / #13: The bracket index format "parent[N].field" is produced by
// the projection of the parse tree (project.go), which also produces "parent.field".
func parseStructKey(local string) (parent, field string, isListStruct, isStruct bool) {
	bracketIdx := strings.IndexByte(local, '[')
	dotIdx := strings.IndexByte(local, '.')
	if bracketIdx >= 0 && dotIdx > bracketIdx {
		// "parent[N].field" pattern.
		return local[:bracketIdx], local[dotIdx+1:], true, false
	}
	if dotIdx >= 0 {
		// "parent.field" pattern.
		return local[:dotIdx], local[dotIdx+1:], false, true
	}
	return "", local, false, false
}

// writeStructProperty emits a struct property as:
//
//	<prefix:parent rdf:parseType="Resource">
//	  <prefix:field1>val1</prefix:field1>
//	  ...
//	</prefix:parent>
//
// #14 — XMP Part 1 §C.2.6: struct values must use rdf:parseType="Resource" or
// an explicit rdf:Description child. The parseType shorthand is more compact and
// is the canonical form emitted by Adobe tools. Element names must be valid XML
// names (no dots), so "parent.field" keys must be split and nested.
// #171 / XML 1.0 §2.3: parent and field are emitted as XML element tag names;
// writeXMLName strips illegal NCName bytes to prevent XML injection.
func writeStructProperty(buf *bytes.Buffer, prefix, parent string, props map[string]string, localList []string) {
	buf.WriteString("   <")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	writeXMLName(buf, parent)
	buf.WriteString(" rdf:parseType=\"Resource\">\n")
	for _, local := range localList {
		p, field, isListStruct, isStruct := parseStructKey(local)
		if !isStruct || isListStruct || p != parent {
			continue
		}
		buf.WriteString("    <")
		buf.WriteString(prefix)
		buf.WriteByte(':')
		writeXMLName(buf, field)
		buf.WriteByte('>')
		writeXMLEscaped(buf, props[local])
		buf.WriteString("</")
		buf.WriteString(prefix)
		buf.WriteByte(':')
		writeXMLName(buf, field)
		buf.WriteString(">\n")
	}
	buf.WriteString("   </")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	writeXMLName(buf, parent)
	buf.WriteString(">\n")
}

// writeStructInListProperty emits a sequence/bag of struct items as:
//
//	<prefix:parent>
//	  <rdf:Seq>
//	    <rdf:li rdf:parseType="Resource">
//	      <prefix:field1>val1</prefix:field1>
//	    </rdf:li>
//	    ...
//	  </rdf:Seq>
//	</prefix:parent>
//
// #14 / #13 — XMP Part 1 §C.2.5 and §C.2.6: an ordered sequence of structs is
// an rdf:Seq whose items are rdf:li elements each containing a struct value
// (rdf:parseType="Resource" shorthand). The collection type (Seq/Bag) is
// resolved via x.effectiveContainerType, which prefers the container actually
// observed in the source document (task #273) and falls back to the
// spec-sourced collectionType table (xmpMM:History → Seq, etc.) for
// properties with no parse-time record.
//
// Items are sorted by index to ensure deterministic, correct ordering.
func writeStructInListProperty(buf *bytes.Buffer, x *XMP, prefix, ns, parent string, props map[string]string, localList []string, ctypeHint string) {
	// Collect all unique indices present for this parent.
	indices := collectStructInListIndices(parent, localList)
	if len(indices) == 0 {
		return
	}
	slices.Sort(indices)

	ctype, isColl := x.effectiveContainerType(ns, parent)
	if ctypeHint != "" {
		ctype, isColl = ctypeHint, true
	}
	if !isColl {
		// A struct-in-list property is always wrapped in a collection — there
		// is no "simple" rendering for an array of structs — so fall back to
		// the historical default (collectionType's unconditional "Bag"
		// default for anything with no spec-table entry).
		ctype = collectionType(ns, parent)
	}
	buf.WriteString("   <")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	// #171 / XML 1.0 §2.3: parent and field are XML element tag names; writeXMLName
	// strips illegal NCName bytes to prevent XML injection.
	writeXMLName(buf, parent)
	buf.WriteString(">\n    <rdf:")
	buf.WriteString(ctype)
	buf.WriteString(">\n")

	for _, idx := range indices {
		buf.WriteString("     <rdf:li rdf:parseType=\"Resource\">\n")
		// Emit all fields for this item index, in sorted order (localList is already sorted).
		idxStr := strconv.Itoa(idx)
		prefix2 := parent + "[" + idxStr + "]."
		for _, local := range localList {
			if !strings.HasPrefix(local, prefix2) {
				continue
			}
			field := local[len(prefix2):]
			buf.WriteString("      <")
			buf.WriteString(prefix)
			buf.WriteByte(':')
			writeXMLName(buf, field)
			buf.WriteByte('>')
			writeXMLEscaped(buf, props[local])
			buf.WriteString("</")
			buf.WriteString(prefix)
			buf.WriteByte(':')
			writeXMLName(buf, field)
			buf.WriteString(">\n")
		}
		buf.WriteString("     </rdf:li>\n")
	}

	buf.WriteString("    </rdf:")
	buf.WriteString(ctype)
	buf.WriteString(">\n   </")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	writeXMLName(buf, parent)
	buf.WriteString(">\n")
}

// collectStructInListIndices returns the sorted unique integer indices present
// in the "parent[N].field" keys of localList for the given parent name.
func collectStructInListIndices(parent string, localList []string) []int {
	seen := make(map[int]struct{})
	for _, local := range localList {
		p, _, isListStruct, _ := parseStructKey(local)
		if !isListStruct || p != parent {
			continue
		}
		// Extract the index from "parent[N].field".
		bracketIdx := strings.IndexByte(local, '[')
		if bracketIdx < 0 {
			continue
		}
		// Find the ']' character after '['.
		closeBracket := strings.IndexByte(local[bracketIdx:], ']')
		if closeBracket < 0 {
			continue
		}
		idxStr := local[bracketIdx+1 : bracketIdx+closeBracket]
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			continue
		}
		seen[idx] = struct{}{}
	}
	result := make([]int, 0, len(seen))
	for idx := range seen {
		result = append(result, idx)
	}
	return result
}

// writeXMLName writes a local name or field name to buf, stripping any byte
// that would make the resulting token an illegal XML NCName or that could
// inject XML structure.  This is the defence-in-depth guard for finding #171.
//
// XML 1.0 §2.3 / XML Namespaces §3 (NCName): a local name must not contain
// '<', '>', '&', '"', '\”, '/', '=', whitespace, or ':' (colon is the
// namespace separator and is already absent from stored local names; we strip
// it defensively).  Characters that pass isNameTerminator in the parser are
// already excluded at parse time (#171 fix in rdf.go); this writer-side check
// closes the second injection vector: a caller who calls Set() with a crafted
// key bypassing the parser.
//
// Design: rather than replacing illegal bytes with a placeholder (which could
// silently round-trip incorrect data), we strip them entirely.  An empty result
// is possible only for a completely illegal name; in that case the element tag
// would be "<prefix:>" which is itself invalid — callers using the public API
// (Set/Get) cannot reach this state without intentionally supplying an illegal
// key.  The stripping is purely defensive against adversarial direct struct
// manipulation.
func writeXMLName(buf *bytes.Buffer, name string) { //nolint:cyclop,gocyclo // illegal-byte check for XML NCName requires branching on each forbidden byte class; refactoring would obscure the security-critical logic
	// Fast path: no illegal bytes — write the string as-is.
	// illegal bytes: C0 controls, space, tab, LF, CR, '<', '>', '&', '"', '\'', '/', '=', ':'
	legal := true
	for i := range len(name) {
		c := name[i]
		if c <= 0x1F || c == '<' || c == '>' || c == '&' || c == '"' || c == '\'' ||
			c == '/' || c == '=' || c == ':' {
			legal = false
			break
		}
	}
	if legal {
		buf.WriteString(name)
		return
	}
	// Slow path: strip illegal bytes.
	for i := range len(name) {
		c := name[i]
		if c <= 0x1F || c == '<' || c == '>' || c == '&' || c == '"' || c == '\'' ||
			c == '/' || c == '=' || c == ':' {
			continue
		}
		buf.WriteByte(c)
	}
}

// writeXMLEscaped writes s to buf with XML character escaping, operating
// directly on the string to avoid the []byte(s) conversion that
// encoding/xml.EscapeText requires.  Handles the five predefined XML
// entities, the CR character, and the XML 1.0 §2.2 forbidden C0 control
// characters (ROB-10 / VT-08 / XMP conformance).
//
// XML 1.0 §2.2 forbidden code points that MUST NOT appear in any serialised
// XML document: U+0000–U+0008, U+000B, U+000C, U+000E–U+001F, U+FFFE, U+FFFF.
// These are replaced with U+FFFD (REPLACEMENT CHARACTER, UTF-8: EF BF BD) per
// Unicode §5.22 "Best Practice for U+FFFD Substitution".  The three-byte UTF-8
// sequence is written directly so that no additional rune-decode round-trip is
// needed on the hot serialisation path.
//
// Note: the forbidden-character check operates on individual bytes of the UTF-8
// encoding.  The multi-byte sequences for U+FFFE (EF BF BE) and U+FFFF (EF BF
// BF) share the leading byte 0xEF with many legitimate CJK characters, so they
// are detected by a three-byte lookahead rather than a single-byte switch.
func writeXMLEscaped(buf *bytes.Buffer, s string) { //nolint:cyclop,gocyclo // XML 1.0 §2.2 C0 range + multi-byte U+FFFE/FFFF detection requires this branching; refactoring would obscure the spec logic
	last := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		var esc string
		switch {
		// ── XML 1.0 §2.2 forbidden C0 control characters ───────────────
		// U+0000–U+0008 (NUL through BS): forbidden in XML 1.0.
		// ROB-10 / VT-08: replace with U+FFFD rather than emitting invalid XML.
		case c <= 0x08:
			buf.WriteString(s[last:i])
			buf.WriteString("\xef\xbf\xbd") // U+FFFD UTF-8
			last = i + 1
			continue
		// U+0009 (TAB): legal XML whitespace — fall through to default.
		// U+000A (LF): legal XML whitespace — fall through to default.
		// U+000B (VT): forbidden.
		// U+000C (FF): forbidden.
		case c == 0x0B || c == 0x0C:
			buf.WriteString(s[last:i])
			buf.WriteString("\xef\xbf\xbd") // U+FFFD UTF-8
			last = i + 1
			continue
		// U+000D (CR): legal but must be escaped as &#xD; per XML normalisation.
		case c == '\r':
			esc = "&#xD;"
		// U+000E–U+001F: forbidden (except TAB/LF/CR already handled above).
		case c >= 0x0E && c <= 0x1F:
			buf.WriteString(s[last:i])
			buf.WriteString("\xef\xbf\xbd") // U+FFFD UTF-8
			last = i + 1
			continue
		// ── Standard XML entity escapes ─────────────────────────────────
		case c == '&':
			esc = "&amp;"
		case c == '<':
			esc = "&lt;"
		case c == '>':
			esc = "&gt;"
		case c == '"':
			esc = "&#34;"
		case c == '\'':
			esc = "&#39;"
		// ── U+FFFE (EF BF BE) and U+FFFF (EF BF BF): forbidden ─────────
		// These are three-byte UTF-8 sequences sharing 0xEF as the leading byte.
		// Detect by three-byte lookahead; only fire for the exact forbidden pairs.
		// All other 0xEF-led sequences (e.g. CJK, Specials) are legal and pass through.
		case c == 0xEF && i+2 < len(s) && s[i+1] == 0xBF && (s[i+2] == 0xBE || s[i+2] == 0xBF):
			// Consume the entire 3-byte sequence and replace with U+FFFD.
			buf.WriteString(s[last:i])
			buf.WriteString("\xef\xbf\xbd") // U+FFFD UTF-8
			i += 2                          // skip the two continuation bytes (loop i++ handles the third)
			last = i + 1
			continue
		default:
			continue
		}
		buf.WriteString(s[last:i])
		buf.WriteString(esc)
		last = i + 1
	}
	buf.WriteString(s[last:])
}

// compactMarkup removes, in place, the formatting white space that serialise
// writes between elements: a line feed and its indentation that follow a '>'
// and precede a '<'. It returns the shortened slice.
//
// Text values never contain a raw '<' or '>' (writeXMLEscaped writes them as
// entities), so every '>' ends a tag and the white space after it that runs
// into a '<' is either formatting or a value that consists of white space
// only. A value always sits between the start tag of its element and the end
// tag, so the one pattern that is kept is a start tag followed by white space
// and an end tag: it covers a white-space value, and costs a few bytes for
// an empty container.
func compactMarkup(b []byte) []byte { //nolint:gocyclo // a small state machine over the tag boundaries; splitting it would obscure the one rule it implements
	const (
		tagOther = iota // end tag, empty-element tag, processing instruction
		tagStart        // start tag
	)
	w, kind := 0, tagOther
	for r := 0; r < len(b); {
		c := b[r]
		b[w] = c
		w++
		r++
		switch c {
		case '<':
			kind = tagStart
			if r < len(b) && (b[r] == '/' || b[r] == '?' || b[r] == '!') {
				kind = tagOther
			}
		case '>':
			if kind == tagStart && w >= 2 && b[w-2] == '/' {
				kind = tagOther // an empty-element tag
			}
			if r < len(b) && b[r] == '\n' {
				j := r + 1
				for j < len(b) && b[j] == ' ' {
					j++
				}
				if j < len(b) && b[j] == '<' && (kind != tagStart || j+1 >= len(b) || b[j+1] != '/') {
					r = j
				}
			}
		}
	}
	return b[:w]
}
