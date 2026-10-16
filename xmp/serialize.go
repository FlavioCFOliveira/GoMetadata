package xmp

// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/XMPMeta-Serialize.cpp:SerializeCanonicalRDFProperty, Copyright 2003-2009 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on this file.

import (
	"bytes"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// propStatus is the outcome of comparing a retained property with its
// Properties entries.
type propStatus uint8

const (
	// propUnchanged: every key of the property is present with its parsed
	// value and no key was added — write the tree node as parsed.
	propUnchanged propStatus = iota
	// propDeleted: every key of the property was removed — omit it.
	propDeleted
	// propMerged: keys were changed, added or removed — write the tree node
	// with the changes applied.
	propMerged
)

// encScratch is the pooled working memory of one Encode call.
type encScratch struct {
	nodes  nodeList
	keyBuf []byte
	items  []int32

	// Per-schema working lists of writeSchema.
	props  []int32
	keys   []string
	ords   []int // ordinal of the retained property of each key, or -1
	keyed  []string
	extra  []string
	counts []int
	status []propStatus
	byOrd  []string // keys grouped by ordinal (see owned)
	start  []int
	seen   map[string]struct{}
}

var encScratchPool = sync.Pool{New: func() any { return &encScratch{seen: make(map[string]struct{})} }} //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure

// prefixer assigns the namespace prefixes of one serialised packet.
// Well-known namespaces use their canonical prefix (prefixMap). Any other
// namespace reuses the prefix of the source document when it is a legal
// prefix that is still free, and otherwise gets a generated prefix nsN.
// NS-03 / XML Namespaces §3: two URIs are never bound to the same prefix.
type prefixer struct {
	used     map[string]struct{}
	assigned map[string]string
	src      map[string]string
	counter  int
}

// prefix returns the prefix for uri, assigning one on first use.
func (pf *prefixer) prefix(uri string) string {
	if p, ok := prefixMap[uri]; ok {
		return p
	}
	if p, ok := pf.assigned[uri]; ok {
		return p
	}
	if pf.assigned == nil {
		pf.assigned = make(map[string]string)
	}
	if sp, ok := pf.src[uri]; ok && validPrefix(sp) {
		if _, taken := pf.used[sp]; !taken {
			pf.used[sp] = struct{}{}
			pf.assigned[uri] = sp
			return sp
		}
	}
	p := uniquePrefixFor(uri, pf.used, &pf.counter)
	pf.assigned[uri] = p
	return p
}

// validPrefix reports whether p can be reused as a namespace prefix: a
// non-empty NCName (XML Namespaces §3) that does not begin with "xml", which
// is reserved (XML Namespaces §3, "Reserved Prefixes and Namespace Names"),
// and is not one of the prefixes the packet wrapper binds.
func validPrefix(p string) bool { //nolint:cyclop,gocyclo // NCName character classes (XML Namespaces §3)
	// "x" and "rdf" are bound by the packet structure itself.
	if p == "" || p == "x" || p == "rdf" || len(p) >= 3 && strings.EqualFold(p[:3], "xml") {
		return false
	}
	for i := range len(p) {
		c := p[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c >= 0x80:
		case i > 0 && (c >= '0' && c <= '9' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// encoder holds the state of one serialise call for a parsed XMP value.
type encoder struct {
	x   *XMP
	buf *bytes.Buffer
	pf  prefixer
	sc  *encScratch
}

// schemaIndex returns the retained schema node of ns, or noNode.
func (x *XMP) schemaIndex(ns string) int32 {
	if len(x.nodes) == 0 {
		return noNode
	}
	for s := x.nodes[0].firstChild; s != noNode; s = x.nodes[s].next {
		if x.nodes[s].ns == ns && x.nodes[s].flags&nodeBlank == 0 {
			return s
		}
	}
	return noNode
}

// propNames finds a retained property of one schema by name: linearly for
// a schema with few retained properties, through index otherwise.
type propNames struct {
	t     nodeList
	props []int32
	index map[string]int
}

// find returns the ordinal of the retained property named name, or -1.
func (pn *propNames) find(name string) int {
	if pn.index != nil {
		if o, ok := pn.index[name]; ok {
			return o
		}
		return -1
	}
	for o, c := range pn.props {
		if pn.t[c].name == name {
			return o
		}
	}
	return -1
}

// bucketOf returns the ordinal of the retained property that owns the flat
// key k, or -1. A key belongs to property P when it is "P", "P[N]…" or
// "P.…"; names are matched exactly, so a property whose own name contains
// '.' is still found.
func (pn *propNames) bucketOf(k string) int {
	if o := pn.find(k); o >= 0 {
		return o
	}
	if i := strings.IndexByte(k, '['); i > 0 {
		if o := pn.find(k[:i]); o >= 0 {
			return o
		}
	}
	for i := range len(k) {
		if k[i] == '.' {
			if o := pn.find(k[:i]); o >= 0 {
				return o
			}
		}
	}
	return -1
}

// appendKey appends the flat key of a field: "P.F" when item < 0, else
// "P[item].F".
func appendKey(b []byte, p string, item int, f string) []byte {
	b = append(b, p...)
	if item >= 0 {
		b = append(b, '[')
		b = appendIndex(b, item)
		b = append(b, ']')
	}
	b = append(b, '.')
	return append(b, f...)
}

// itemFlatEquals reports whether the flat form of the simple item it
// ("lang|value", or the value for no lang or x-default) equals s.
func (l nodeList) itemFlatEquals(it int32, s string) bool {
	if lang := l.itemLang(it); lang != "" && lang != "x-default" {
		return len(s) == len(lang)+1+len(l[it].value) && strings.HasPrefix(s, lang) &&
			s[len(lang)] == '|' && strings.HasSuffix(s, l[it].value)
	}
	return s == l[it].value
}

// joinedEquals reports whether v equals the flat value that Parse built for
// the simple items of array a (see joinItems), without building it.
func (l nodeList) joinedEquals(a int32, v string) bool {
	first := true
	for it := l[a].firstChild; it != noNode; it = l[it].next {
		if !l[it].isSimple() || l[it].value == "" {
			continue
		}
		if !first {
			if v == "" || v[0] != '\x1e' {
				return false
			}
			v = v[1:]
		}
		first = false
		seg := v
		if i := strings.IndexByte(v, '\x1e'); i >= 0 {
			seg = v[:i]
		}
		if !l.itemFlatEquals(it, seg) {
			return false
		}
		v = v[len(seg):]
	}
	return !first && v == ""
}

// ownerMatch counts the owner nodes of the retained property i and how many
// of them still have their parsed value in props.
func (e *encoder) ownerMatch(i int32, props map[string]string) (owners, matched int) { //nolint:cyclop,gocyclo // one case per owner form of the key conventions
	t := e.x.nodes
	n := &t[i]
	if n.flags&nodeOwner != 0 {
		owners++
		v, ok := props[n.name]
		if ok && (n.isSimple() && v == n.value || n.isArray() && t.joinedEquals(i, v)) {
			matched++
		}
	}
	check := func(item int, s int32) {
		for f := t[s].firstChild; f != noNode; f = t[f].next {
			if t[f].flags&nodeOwner == 0 {
				continue
			}
			owners++
			e.sc.keyBuf = appendKey(e.sc.keyBuf[:0], n.name, item, t[f].name)
			if v, ok := props[string(e.sc.keyBuf)]; ok && v == t[f].value {
				matched++
			}
		}
	}
	switch {
	case n.isStruct():
		check(-1, i)
	case n.isArray():
		idx := 0
		for it := n.firstChild; it != noNode; it = t[it].next {
			if t[it].isStruct() {
				check(idx, it)
			}
			idx++
		}
	}
	return owners, matched
}

// status compares the retained property i with its flatCount Properties
// entries.
func (e *encoder) status(i int32, props map[string]string, flatCount int) propStatus {
	owners, matched := e.ownerMatch(i, props)
	switch {
	case flatCount == owners && matched == owners:
		return propUnchanged
	case flatCount == 0:
		return propDeleted
	default:
		return propMerged
	}
}

// fieldKey is a flat key of a retained property split into its item index
// (-1 for "P.F") and field name.
type fieldKey struct {
	item  int
	field string
	key   string
}

// splitKeys sorts the keys of property p into the top-level key and field
// keys. ok is false when a key has a form this package cannot map onto the
// property's node (for example "P[x].F" with a non-numeric index).
func splitKeys(p string, keys []string) (hasTop bool, fields []fieldKey, ok bool) { //nolint:cyclop,gocyclo // one case per flat key form
	for _, k := range keys {
		switch {
		case k == p:
			hasTop = true
		case len(k) > len(p)+1 && k[len(p)] == '.':
			fields = append(fields, fieldKey{item: -1, field: k[len(p)+1:], key: k})
		case len(k) > len(p) && k[len(p)] == '[':
			rest := k[len(p)+1:]
			end := strings.IndexByte(rest, ']')
			if end <= 0 || end+1 >= len(rest) || rest[end+1] != '.' {
				return false, nil, false
			}
			idx, err := strconv.Atoi(rest[:end])
			if err != nil || idx < 0 {
				return false, nil, false
			}
			fields = append(fields, fieldKey{item: idx, field: rest[end+2:], key: k})
		default:
			return false, nil, false
		}
	}
	return hasTop, fields, true
}

// merge applies the Properties entries keys of the retained property i to a
// copy of its subtree and returns the copy's index in e.sc.nodes. ok is
// false when the entries do not fit the shape of the node (for example a
// "P.F" key on an array); the caller then writes the property from its
// entries alone.
func (e *encoder) merge(i int32, props map[string]string, keys []string) (int32, bool) { //nolint:cyclop,gocyclo,funlen // one branch per node form; the steps are the minimum to apply an edit while keeping unmodified children
	sc := e.sc.nodes[:0]
	r := copySubtree(&sc, e.x.nodes, i, nil)
	defer func() { e.sc.nodes = sc }()
	p := sc[r].name
	hasTop, fields, ok := splitKeys(p, keys)
	if !ok {
		return noNode, false
	}
	switch {
	case sc[r].isSimple():
		if len(fields) > 0 || !hasTop {
			return noNode, false
		}
		v := props[p]
		if strings.IndexByte(v, '\x1e') >= 0 {
			return noNode, false
		}
		sc[r].value = v
		sc[r].flags |= nodeOwner
		return r, true
	case sc[r].isStruct():
		if hasTop {
			return noNode, false
		}
		for _, f := range fields {
			if f.item >= 0 {
				return noNode, false
			}
		}
		e.mergeFields(&sc, r, r, -1, fields, props)
		return r, true
	}

	// Array.
	for _, f := range fields {
		if f.item < 0 {
			return noNode, false
		}
	}
	items := e.sc.items[:0]
	for it := sc[r].firstChild; it != noNode; it = sc[it].next {
		items = append(items, it)
	}
	orig := len(items)
	for _, f := range fields {
		if f.item < orig && !sc[items[f.item]].isStruct() {
			return noNode, false
		}
	}
	// Sort the field keys by item index (stable, so each item keeps its
	// fields in key order) and walk items and keys together.
	slices.SortStableFunc(fields, func(a, b fieldKey) int { return a.item - b.item })
	k := 0
	for idx := range orig {
		lo := k
		for k < len(fields) && fields[k].item == idx {
			k++
		}
		it := items[idx]
		owned := false
		for f := sc[it].firstChild; f != noNode; f = sc[f].next {
			owned = owned || sc[f].flags&nodeOwner != 0
		}
		e.mergeFields(&sc, r, it, idx, fields[lo:k], props)
		if owned && sc[it].firstChild == noNode {
			items[idx] = noNode // every field was deleted: drop the item
		}
	}
	// New struct items, in ascending index order. Like the writer for keys
	// without a tree node, the indices only order the new items; gaps are
	// closed.
	for k < len(fields) {
		idx := fields[k].item
		it := sc.add("", "", "", nodeItem|nodeStruct)
		for ; k < len(fields) && fields[k].item == idx; k++ {
			fi := sc.add(fieldNS(sc, r, fields[k].field), fields[k].field, props[fields[k].key], nodeOwner)
			sc.appendChild(it, fi)
		}
		items = append(items, it)
	}

	// Rebuild the child list: simple items from the top-level value (when it
	// changed), then the other items in their original order. A struct item
	// whose fields were all deleted is dropped.
	topOwner := sc[r].flags&nodeOwner != 0
	v, has := props[p]
	rebuild := hasTop && (!topOwner || !sc.joinedEquals(r, v)) || topOwner && !hasTop
	sc[r].firstChild, sc[r].lastChild = noNode, noNode
	if rebuild {
		if has {
			e.rebuildSimpleItems(&sc, r, items, v)
		}
	}
	for _, it := range items {
		if it == noNode || sc[it].isSimple() && rebuild {
			continue
		}
		sc.appendChild(r, it)
	}
	e.sc.items = items[:0]
	if hasTop {
		sc[r].flags |= nodeOwner
	}
	return r, true
}

// mergeFields updates the owner fields of struct s (a struct property when
// item < 0, else array item number item) from props: a changed key replaces
// the value, a missing key deletes the field, and a key without a field adds
// a simple field in the property's namespace.
func (e *encoder) mergeFields(sc *nodeList, prop, s int32, item int, fields []fieldKey, props map[string]string) { //nolint:cyclop,gocyclo // update, delete and add passes over one struct
	l := *sc
	if !l[s].isStruct() {
		return
	}
	p := l[prop].name
	prev := noNode
	for f := l[s].firstChild; f != noNode; {
		next := l[f].next
		if l[f].flags&nodeOwner == 0 {
			prev, f = f, next
			continue
		}
		e.sc.keyBuf = appendKey(e.sc.keyBuf[:0], p, item, l[f].name)
		if v, ok := props[string(e.sc.keyBuf)]; ok {
			l[f].value = v
			prev, f = f, next
			continue
		}
		// The key was deleted: unlink the field.
		if prev == noNode {
			l[s].firstChild = next
		} else {
			l[prev].next = next
		}
		if l[s].lastChild == f {
			l[s].lastChild = prev
		}
		f = next
	}
	// Names of the owner fields, to recognise keys that add a field.
	var owned map[string]struct{}
	for f := l[s].firstChild; f != noNode; f = l[f].next {
		if l[f].flags&nodeOwner != 0 {
			if owned == nil {
				owned = make(map[string]struct{})
			}
			owned[l[f].name] = struct{}{}
		}
	}
	for _, fk := range fields {
		if fk.item != item {
			continue
		}
		if _, exists := owned[fk.field]; !exists {
			fi := sc.add(fieldNS(l, prop, fk.field), fk.field, props[fk.key], nodeOwner)
			l = *sc
			l.appendChild(s, fi)
		}
	}
}

// fieldNS returns the namespace for a new field named name of property prop:
// the namespace of an existing field with that name (in the struct or in any
// struct item), so that a key such as "History[2].action" added next to
// parsed stEvt:action fields gets stEvt again, or else the namespace of the
// property, which is the convention of the flat keys (see XMP.Properties).
func fieldNS(l nodeList, prop int32, name string) string {
	find := func(s int32) string {
		for f := l[s].firstChild; f != noNode; f = l[f].next {
			if l[f].name == name && l[f].flags&nodeItem == 0 {
				return l[f].ns
			}
		}
		return ""
	}
	if l[prop].isStruct() {
		if ns := find(prop); ns != "" {
			return ns
		}
		return l[prop].ns
	}
	for it := l[prop].firstChild; it != noNode; it = l[it].next {
		if ns := find(it); ns != "" && l[it].isStruct() {
			return ns
		}
	}
	return l[prop].ns
}

// rebuildSimpleItems appends to array r the simple items encoded by the flat
// value v, reusing an original item (and its qualifiers) when its flat form is
// unchanged at the same position. For rdf:Alt, "lang|value" sets xml:lang and
// a bare value is x-default, as for a property without a tree node.
func (e *encoder) rebuildSimpleItems(sc *nodeList, r int32, items []int32, v string) {
	alt := (*sc)[r].flags&nodeAlternate != 0
	old := 0
	for {
		seg, rest, more := strings.Cut(v, "\x1e")
		for old < len(items) && (items[old] == noNode || !(*sc)[items[old]].isSimple() || (*sc)[items[old]].value == "") {
			old++
		}
		if old < len(items) && sc.itemFlatEquals(items[old], seg) {
			sc.appendChild(r, items[old])
		} else {
			it := e.newSimpleItem(sc, seg, alt)
			sc.appendChild(r, it)
		}
		old++
		if !more {
			return
		}
		v = rest
	}
}

// newSimpleItem adds a simple array item for the flat segment seg.
func (*encoder) newSimpleItem(sc *nodeList, seg string, alt bool) int32 {
	if !alt {
		return sc.add("", "", seg, nodeItem)
	}
	lang, val, hasLang := strings.Cut(seg, "|")
	if !hasLang {
		lang, val = "x-default", seg
	}
	it := sc.add("", "", val, nodeItem)
	q := sc.add(XMLNamespaceURI, "lang", lang, 0)
	sc.appendQual(it, q)
	return it
}

// collectNS appends to dst every namespace used below node i (fields and
// qualifiers), except the RDF and XML namespaces and those already in dst.
// seen indexes dst once it grows past dupLinearLimit, so that a tree with
// many distinct namespaces is collected in linear time; the caller passes
// the same map for every call that appends to the same dst.
func collectNS(l nodeList, i int32, dst []string, seen map[string]struct{}) []string { //nolint:cyclop,gocyclo // the two dedupe modes and the qualifier and child walks
	add := func(ns string) {
		if ns == "" || ns == NSrdf || ns == XMLNamespaceURI {
			return
		}
		if len(dst) < dupLinearLimit {
			if slices.Contains(dst, ns) {
				return
			}
		} else {
			if len(seen) == 0 {
				for _, d := range dst {
					seen[d] = struct{}{}
				}
			}
			if _, ok := seen[ns]; ok {
				return
			}
			seen[ns] = struct{}{}
		}
		dst = append(dst, ns)
	}
	var walk func(int32)
	walk = func(n int32) {
		for q := l[n].firstQual; q != noNode; q = l[q].next {
			add(l[q].ns)
			walk(q)
		}
		for c := l[n].firstChild; c != noNode; c = l[c].next {
			add(l[c].ns)
			walk(c)
		}
	}
	walk(i)
	return dst
}

// isAttrQual reports whether q is written as an attribute of its node's
// element rather than with the qualified-value form (IsRDFAttrQualifier).
// Of the Toolkit's attribute qualifiers only xml:lang can come from Parse;
// rdf:ID and rdf:nodeID are ignored on input. rdf:resource is handled by
// the caller.
func (l nodeList) isAttrQual(q int32) bool { return l[q].isLangQual() }

// isResourceQual reports whether q is an rdf:resource qualifier, which Parse
// creates only for an ill-formed element with two rdf:resource attributes.
func (l nodeList) isResourceQual(q int32) bool { return l[q].ns == NSrdf && l[q].name == "resource" }

// writeElemName writes the element name of node i.
func (e *encoder) writeElemName(l nodeList, i int32, asValue bool) {
	switch {
	case asValue:
		e.buf.WriteString("rdf:value")
	case l[i].flags&nodeItem != 0:
		e.buf.WriteString("rdf:li")
	default:
		e.buf.WriteString(e.pf.prefix(l[i].ns))
		e.buf.WriteByte(':')
		writeXMLName(e.buf, l[i].name)
	}
}

// writeNodeID writes the rdf:nodeID attribute of qualifier q, if any.
func (e *encoder) writeNodeID(l nodeList, q int32) {
	if q == noNode {
		return
	}
	e.buf.WriteString(" rdf:nodeID=\"")
	writeXMLAttrEscaped(e.buf, l[q].value)
	e.buf.WriteByte('"')
}

// writeBlankBlocks writes the retained blank-node blocks (see nodeBlank)
// after the schema descriptions: an rdf:Description or rdf:Bag/Seq/Alt
// element that carries the node ID and declares the namespaces it uses.
func (e *encoder) writeBlankBlocks() {
	t := e.x.nodes
	if len(t) == 0 {
		return
	}
	for b := t[0].firstChild; b != noNode; b = t[b].next {
		if t[b].flags&nodeBlank == 0 {
			continue
		}
		// collectNS from the block node itself covers its children too.
		used := collectNS(t, b, nil, map[string]struct{}{})
		elem := "Description"
		if t[b].isArray() {
			elem = t[b].containerName()
		}
		e.buf.WriteString("  <rdf:")
		e.buf.WriteString(elem)
		e.buf.WriteString(" rdf:nodeID=\"")
		writeXMLAttrEscaped(e.buf, t[b].name)
		e.buf.WriteByte('"')
		for _, u := range used {
			e.buf.WriteString(" xmlns:")
			e.buf.WriteString(e.pf.prefix(u))
			e.buf.WriteString("=\"")
			writeXMLAttrEscaped(e.buf, u)
			e.buf.WriteByte('"')
		}
		e.buf.WriteString(">\n")
		for c := t[b].firstChild; c != noNode; c = t[c].next {
			e.emitNode(t, c, 3, false)
		}
		e.buf.WriteString("  </rdf:")
		e.buf.WriteString(elem)
		e.buf.WriteString(">\n")
	}
}

// writeIndent writes n spaces.
func (e *encoder) writeIndent(n int) {
	for range n {
		e.buf.WriteByte(' ')
	}
}

// emitNode writes node i of l, a property, field, array item or qualifier,
// at the given indent. With asValue the node is written as the rdf:value of
// its own qualified form. Port of SerializeCanonicalRDFProperty in its
// verbose form (rdf:parseType="Resource" instead of a nested
// rdf:Description), which is the form this package already wrote for
// structs. The forms are (ISO 16684-1:2019 §7.5–§7.9):
//
//	<ns:Simple>value</ns:Simple>
//	<ns:URI rdf:resource="value"/>
//	<ns:Struct rdf:parseType="Resource"> fields </ns:Struct>
//	<ns:Array> <rdf:Bag|Seq|Alt> items as rdf:li </rdf:Bag> </ns:Array>
//	<ns:Qualified rdf:parseType="Resource">
//	  <rdf:value> value </rdf:value> qualifiers as fields
//	</ns:Qualified>
//
// xml:lang is written as an attribute of the element and does not by itself
// force the qualified form. Recursion depth is bounded by maxXMLDepth for a
// parsed tree; trees built by Encode are at most three levels deep.
func (e *encoder) emitNode(l nodeList, i int32, indent int, asValue bool) { //nolint:cyclop,gocyclo,funlen // transcription of the Toolkit's per-form serialisation
	n := &l[i]
	e.writeIndent(indent)
	e.buf.WriteByte('<')
	e.writeElemName(l, i, asValue)

	general := false
	nodeID := noNode
	for q := n.firstQual; q != noNode; q = l[q].next {
		switch {
		case l.isResourceQual(q):
		case l[q].isNodeIDQual():
			nodeID = q
		case l.isAttrQual(q):
			if !asValue {
				e.buf.WriteString(" xml:lang=\"")
				writeXMLAttrEscaped(e.buf, l[q].value)
				e.buf.WriteByte('"')
			}
		default:
			general = true
		}
	}

	switch {
	case general && !asValue:
		e.buf.WriteString(" rdf:parseType=\"Resource\">\n")
		e.emitNode(l, i, indent+1, true)
		for q := n.firstQual; q != noNode; q = l[q].next {
			if !l.isAttrQual(q) && !l.isResourceQual(q) && !l[q].isNodeIDQual() {
				e.emitNode(l, q, indent+1, false)
			}
		}
		e.writeIndent(indent)
	case n.isArray():
		ctype := n.containerName()
		e.buf.WriteString(">\n")
		e.writeIndent(indent + 1)
		e.buf.WriteString("<rdf:")
		e.buf.WriteString(ctype)
		e.writeNodeID(l, nodeID)
		if n.firstChild == noNode {
			e.buf.WriteString("/>\n")
		} else {
			e.buf.WriteString(">\n")
			for c := n.firstChild; c != noNode; c = l[c].next {
				e.emitNode(l, c, indent+2, false)
			}
			e.writeIndent(indent + 1)
			e.buf.WriteString("</rdf:")
			e.buf.WriteString(ctype)
			e.buf.WriteString(">\n")
		}
		e.writeIndent(indent)
	case n.isStruct() && nodeID != noNode:
		// A struct that references a blank node keeps the explicit
		// rdf:Description form, the only one that can carry rdf:nodeID
		// (RDF/XML Syntax §7.2.11).
		e.buf.WriteString(">\n")
		e.writeIndent(indent + 1)
		e.buf.WriteString("<rdf:Description")
		e.writeNodeID(l, nodeID)
		if n.firstChild == noNode {
			e.buf.WriteString("/>\n")
		} else {
			e.buf.WriteString(">\n")
			for c := n.firstChild; c != noNode; c = l[c].next {
				e.emitNode(l, c, indent+2, false)
			}
			e.writeIndent(indent + 1)
			e.buf.WriteString("</rdf:Description>\n")
		}
		e.writeIndent(indent)
	case n.isStruct():
		if n.firstChild == noNode {
			e.buf.WriteString(" rdf:parseType=\"Resource\"/>\n")
			return
		}
		e.buf.WriteString(" rdf:parseType=\"Resource\">\n")
		for c := n.firstChild; c != noNode; c = l[c].next {
			e.emitNode(l, c, indent+1, false)
		}
		e.writeIndent(indent)
	case n.flags&nodeURI != 0:
		e.buf.WriteString(" rdf:resource=\"")
		writeXMLAttrEscaped(e.buf, n.value)
		e.buf.WriteString("\"/>\n")
		return
	default:
		if !asValue {
			e.writeNodeID(l, nodeID)
		}
		e.buf.WriteByte('>')
		writeXMLEscaped(e.buf, n.value)
	}
	e.buf.WriteString("</")
	e.writeElemName(l, i, asValue)
	e.buf.WriteString(">\n")
}
