package xmp

import (
	"strings"
	"unsafe"
)

// Budget for the composed keys of the flat Properties view (task #277).
//
// A struct field is exposed in Properties under the key "P.F" and a field of
// a struct array item under "P[N].F". Each key repeats the property name P,
// so their total size grows with items × len(P) — quadratically in the
// document size for an adversarial document that pairs a long property name
// with many items. The tree that Parse builds is linear in the document size
// and holds every node regardless of this budget; the budget only limits how
// many composed keys are mirrored into Properties. Once it is spent, further
// composed keys are not added to Properties (whole entries are skipped, a
// name is never truncated), and the affected properties stay in the retained
// tree, so Encode still writes them back unchanged.
//
// Real documents use a small fraction of the budget: a key is typically a few
// dozen bytes, while the RDF of the field it names (start tag, value, end
// tag, and the rdf:li wrapper of an array item) is about as long or longer.
// The slack covers tiny documents whose markup is minimal.
const (
	composedKeyFactor = 4
	composedKeySlack  = 4 << 10
)

// finish fills x.Properties from the parse tree and keeps, in x.nodes, every
// top-level property that the flat view cannot reproduce exactly.
//
// Every string stored in x — Properties keys and values, and the retained
// nodes — is copied into x.arena at this point and only at this point; the
// parse tree itself holds transient views of the input (see
// transientAliasString).
func (p *rdfParser) finish(docLen int) {
	budget := composedKeyFactor*docLen + composedKeySlack
	if need := p.arenaNeed(budget); need > 0 {
		p.x.arena = make([]byte, 0, need)
	}
	p.x.about = p.x.intern(p.about)
	p.x.toolkit = p.x.intern(p.toolkit)
	if len(p.s.prefixed) > 0 {
		p.x.prefixes = make(map[string]string, len(p.s.prefixed))
		for ns, pfx := range p.s.prefixed {
			p.x.prefixes[p.internNS(ns)] = p.x.intern(pfx)
		}
	}
	retained := 1 // the root
	for s := p.t[0].firstChild; s != noNode; s = p.t[s].next {
		if p.t[s].flags&nodeBlank != 0 {
			retained += p.t.subtreeSize(s)
			continue
		}
		ns := p.t[s].ns
		table := arrayProperties[ns]
		var m map[string]string
		keep := false
		for prop := p.t[s].firstChild; prop != noNode; prop = p.t[prop].next {
			m = p.project(s, prop, m, &budget)
			if !p.representable(ns, table, prop) {
				p.t[prop].flags |= nodeRetain
				retained += p.t.subtreeSize(prop)
				keep = true
			}
		}
		if keep {
			retained++ // the schema node
		}
	}
	if retained > 1 {
		p.retain(retained)
	}
}

// arenaNeed returns an upper estimate of the bytes that finish copies into
// XMP.arena, so the arena is allocated once instead of being regrown: every
// superseded backing array of a regrown arena stays alive as long as a string
// points into it, which can double the retained memory. The tree builder
// accumulates the names and values of every node, a copy of every array item
// for its joined value, the composed keys (bounded here by budget), and the
// first few distinct namespaces; the strings recorded outside the tree are
// added here.
func (p *rdfParser) arenaNeed(budget int) int {
	n := p.est + min(p.composed, budget) + len(p.about) + len(p.toolkit)
	for ns, pfx := range p.s.prefixed {
		n += len(ns) + len(pfx)
	}
	return n
}

// project mirrors the top-level property prop of schema s into m, creating m
// (and its entry in x.Properties) on the first key. Keys and values follow
// the documented Properties conventions (see XMP.Properties). A node whose
// key is stored is marked nodeOwner; a key that is already present is left
// unchanged (first one wins), and its node is not an owner.
func (p *rdfParser) project(s, prop int32, m map[string]string, budget *int) map[string]string { //nolint:cyclop,gocyclo // one case per node form of the documented key conventions
	n := &p.t[prop]
	switch {
	case n.isSimple():
		if p.put(s, &m, n.name, false) {
			p.t[prop].flags |= nodeOwner
			p.internName(prop)
			p.internValue(prop)
			m[p.t[prop].name] = p.t[prop].value
		}
	case n.isStruct():
		for f := n.firstChild; f != noNode; f = p.t[f].next {
			if p.t[f].isSimple() {
				p.projectField(s, &m, prop, -1, f, budget)
			}
		}
	default: // array
		if joined, ok := p.joinItems(s, &m, prop); ok {
			p.t[prop].flags |= nodeOwner
			p.internName(prop)
			m[p.t[prop].name] = joined
		}
		idx := 0
		for it := n.firstChild; it != noNode; it = p.t[it].next {
			if p.t[it].isStruct() {
				for f := p.t[it].firstChild; f != noNode; f = p.t[f].next {
					if p.t[f].isSimple() {
						p.projectField(s, &m, prop, idx, f, budget)
					}
				}
			}
			idx++
		}
	}
	return m
}

// put reports whether key is still free in *m, creating the map of schema s
// on first use. It does not store anything; the caller stores the interned
// key and value.
//
// The names of the top-level properties of one schema are unique (the parser
// rejects duplicates), and a composed key always contains '.', so a
// top-level name without '.' and '[' cannot be taken and is not looked up.
func (p *rdfParser) put(s int32, m *map[string]string, key string, composed bool) bool {
	if *m == nil {
		hint := p.t.childCount(s)
		*m = make(map[string]string, hint)
		p.x.Properties[p.schemaNS(s)] = *m
		return true
	}
	if !composed && legacyName(key) {
		return true
	}
	_, dup := (*m)[key]
	return !dup
}

// projectField stores the simple field f of prop under "P.F" (item < 0) or
// "P[item].F", subject to the composed-key budget.
func (p *rdfParser) projectField(s int32, m *map[string]string, prop int32, item int, f int32, budget *int) {
	kb := p.s.keyBuf[:0]
	kb = append(kb, p.t[prop].name...)
	if item >= 0 {
		kb = append(kb, '[')
		kb = appendIndex(kb, item)
		kb = append(kb, ']')
	}
	kb = append(kb, '.')
	kb = append(kb, p.t[f].name...)
	p.s.keyBuf = kb
	if len(kb) > *budget {
		return
	}
	if !p.put(s, m, transientAliasString(kb), true) {
		return
	}
	*budget -= len(kb)
	key := p.x.intern(transientAliasString(kb))
	p.internValue(f)
	(*m)[key] = p.t[f].value
	p.t[f].flags |= nodeOwner
}

// appendIndex appends the decimal form of i (i >= 0) without allocating.
func appendIndex(b []byte, i int) []byte {
	var d [20]byte
	n := len(d)
	for {
		n--
		d[n] = byte('0' + i%10)
		i /= 10
		if i == 0 {
			break
		}
	}
	return append(b, d[n:]...)
}

// itemText returns the flat form of a simple array item: "lang|value" for an
// item with an xml:lang other than x-default, else the value. The previous
// parser used the same convention for every array kind (P1-H).
func (l nodeList) itemLang(it int32) string {
	q := l[it].firstQual
	if q != noNode && l[q].isLangQual() {
		return l[q].value
	}
	return ""
}

// joinItems builds the flat value of an array of simple items: the non-empty
// items joined with U+001E, each prefixed with "lang|" when it carries an
// xml:lang other than x-default. It returns false when the array has no
// non-empty simple item or when the key is already taken. A single plain
// item is returned without copying it twice; otherwise the joined value is
// written straight into XMP.arena.
func (p *rdfParser) joinItems(s int32, m *map[string]string, prop int32) (string, bool) { //nolint:cyclop,gocyclo // two passes (size, then copy) over the same item filter
	total, count, single := 0, 0, noNode
	for it := p.t[prop].firstChild; it != noNode; it = p.t[it].next {
		n := &p.t[it]
		if !n.isSimple() || n.value == "" {
			continue
		}
		if count > 0 {
			total++ // separator
		}
		if lang := p.t.itemLang(it); lang != "" && lang != "x-default" {
			total += len(lang) + 1
		}
		total += len(n.value)
		count++
		single = it
	}
	if count == 0 || !p.put(s, m, p.t[prop].name, false) {
		return "", false
	}
	if count == 1 {
		if lang := p.t.itemLang(single); lang == "" || lang == "x-default" {
			p.internValue(single)
			return p.t[single].value, true
		}
	}
	start := len(p.x.arena)
	p.x.arena = growArena(p.x.arena, total)
	first := true
	for it := p.t[prop].firstChild; it != noNode; it = p.t[it].next {
		n := &p.t[it]
		if !n.isSimple() || n.value == "" {
			continue
		}
		if !first {
			p.x.arena = append(p.x.arena, '\x1e')
		}
		first = false
		if lang := p.t.itemLang(it); lang != "" && lang != "x-default" {
			p.x.arena = append(p.x.arena, lang...)
			p.x.arena = append(p.x.arena, '|')
		}
		p.x.arena = append(p.x.arena, n.value...)
	}
	return unsafe.String(unsafe.SliceData(p.x.arena[start:]), total), true //nolint:gosec // G103: the region [start:start+total) was just appended to XMP.arena, which is append-only (see XMP.arena)
}

// schemaNS interns the namespace of schema s once and stores the interned
// string back into the schema node, so that retain reuses it.
func (p *rdfParser) schemaNS(s int32) string {
	n := &p.t[s]
	if n.flags&nodeNameInterned == 0 {
		n.ns = p.x.intern(n.ns)
		n.flags |= nodeNameInterned // a schema has no name; the bit marks ns
	}
	return n.ns
}

// growArena makes room for n more bytes in a, following append's growth so
// that earlier arena strings stay valid (see XMP.arena, invariant 2).
func growArena(a []byte, n int) []byte {
	if cap(a)-len(a) >= n {
		return a
	}
	return append(a, make([]byte, n)...)[:len(a)]
}

// internName and internValue copy a node's name or value into XMP.arena once.
func (p *rdfParser) internName(i int32) {
	n := &p.t[i]
	if n.flags&nodeNameInterned == 0 {
		n.name = p.x.intern(n.name)
		n.flags |= nodeNameInterned
	}
}

func (p *rdfParser) internValue(i int32) {
	n := &p.t[i]
	if n.flags&nodeValueInterned == 0 {
		n.value = p.x.intern(n.value)
		n.flags |= nodeValueInterned
	}
}

// legacyName reports whether name can be a component of a flat key: the
// writer for keys without a tree node splits keys at '.' and '['.
func legacyName(name string) bool {
	return name != "" && strings.IndexByte(name, '.') < 0 && strings.IndexByte(name, '[') < 0
}

// representable reports whether the flat Properties entries of the top-level
// property prop in namespace ns are enough for Encode to write the property
// back with the same XMP data model (ISO 16684-1:2019 §6) through the writer
// for keys without a tree node — except that a simple value of a schema
// array property is converted to that array (see the simple case below). Such a property is not retained in
// XMP.nodes, which keeps Parse as cheap as before for the common case. A
// property that fails any rule — qualifiers, URI values, nested structures,
// empty array items, a container that differs from the one the writer would
// choose, names that the flat key syntax cannot carry, or keys that were not
// stored — is retained and written from the tree.
func (p *rdfParser) representable(ns string, table map[string]string, prop int32) bool { //nolint:cyclop,gocyclo // one rule per data-model feature that the flat view cannot carry
	t := p.t
	n := &t[prop]
	if n.firstQual != noNode || !legacyName(n.name) {
		return false
	}
	switch {
	case n.isSimple():
		// A simple value of a property that the schema defines as an array
		// (e.g. dc:rights="…" as a plain attribute) is not kept as parsed:
		// the writer for keys converts it to a one-item array of the schema's
		// container, as the previous parser did. The exception is an rdf:Alt
		// value containing '|', which that writer would read as "lang|value".
		ct, isColl := table[n.name]
		return n.flags&nodeOwner != 0 && n.flags&nodeURI == 0 &&
			strings.IndexByte(n.value, '\x1e') < 0 &&
			(!isColl || ct != "Alt" || strings.IndexByte(n.value, '|') < 0)
	case n.isStruct():
		return n.firstChild != noNode && t.fieldsRepresentable(ns, prop)
	}
	ctype := n.containerName()
	first := n.firstChild
	if first == noNode {
		return false
	}
	if t[first].isStruct() {
		for it := first; it != noNode; it = t[it].next {
			if !t[it].isStruct() || t[it].firstQual != noNode || t[it].firstChild == noNode ||
				!t.fieldsRepresentable(ns, it) {
				return false
			}
		}
		return ctype == tableContainer(table, n.name)
	}
	if n.flags&nodeOwner == 0 {
		return false
	}
	count := 0
	for it := first; it != noNode; it = t[it].next {
		item := &t[it]
		if !item.isSimple() || item.flags&nodeURI != 0 || item.value == "" ||
			strings.IndexByte(item.value, '\x1e') >= 0 {
			return false
		}
		lang := ""
		if q := item.firstQual; q != noNode {
			if !t[q].isLangQual() || t[q].next != noNode {
				return false
			}
			lang = t[q].value
		}
		if ctype == "Alt" {
			// The writer emits a bare item as x-default and splits "lang|value"
			// at the first '|'.
			if lang == "" || strings.IndexByte(lang, '|') >= 0 ||
				(lang == "x-default" && strings.IndexByte(item.value, '|') >= 0) {
				return false
			}
		} else if lang != "" {
			return false
		}
		count++
	}
	if ct, ok := table[n.name]; ok {
		return ctype == ct
	}
	return ctype == "Bag" && count >= 2
}

// tableContainer returns the container of name in a schema's
// arrayProperties table, or "Bag" (collectionType's default).
func tableContainer(table map[string]string, name string) string {
	if ct, ok := table[name]; ok {
		return ct
	}
	return "Bag"
}

// fieldsRepresentable reports whether every field of the struct s is a simple,
// unqualified text field in namespace ns whose "P.F" or "P[N].F" key was
// stored.
func (l nodeList) fieldsRepresentable(ns string, s int32) bool {
	for f := l[s].firstChild; f != noNode; f = l[f].next {
		n := &l[f]
		if !n.isSimple() || n.flags&(nodeOwner|nodeURI) != nodeOwner || n.firstQual != noNode ||
			n.ns != ns || !legacyName(n.name) || strings.IndexByte(n.value, '\x1e') >= 0 {
			return false
		}
	}
	return true
}

// retain copies the schemas and top-level properties marked nodeRetain into
// x.nodes, interning every string into XMP.arena. size is the exact number
// of nodes to copy.
func (p *rdfParser) retain(size int) {
	out := make(nodeList, 0, size)
	out.add("", "", "", 0)
	for s := p.t[0].firstChild; s != noNode; s = p.t[s].next {
		if p.t[s].flags&nodeBlank != 0 {
			c := copySubtree(&out, p.t, s, p)
			out.appendChild(0, c)
			continue
		}
		si := noNode
		for prop := p.t[s].firstChild; prop != noNode; prop = p.t[prop].next {
			if p.t[prop].flags&nodeRetain == 0 {
				continue
			}
			if si == noNode {
				si = out.add(p.internNS(p.t[s].ns), "", "", 0)
				out.appendChild(0, si)
			}
			c := copySubtree(&out, p.t, prop, p)
			out.appendChild(si, c)
		}
	}
	p.x.nodes = out
}

// fixNode interns the strings of a node copied into XMP.nodes and clears the
// parse-time flags.
func (p *rdfParser) fixNode(n *node) {
	n.ns = p.internNS(n.ns)
	if n.flags&nodeNameInterned == 0 {
		n.name = p.x.intern(n.name)
	}
	switch {
	case n.flags&nodeValueInterned != 0:
	case n.ns == NSrdf && n.name == "type":
		// A type URI is shared by every node typed with it (addTypeQualifier).
		n.value = p.internNS(n.value)
	default:
		n.value = p.x.intern(n.value)
	}
	n.flags &^= nodeNameInterned | nodeValueInterned | nodeHasValueElem | nodeIndexedChildren | nodeIndexedQuals
}
