package xmp

// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/XMPMeta-Parse.cpp:MoveExplicitAliases,TransplantNamedAlias,TransplantArrayItemAlias, Copyright 2003 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on this file.

// aliasesByNS groups the rows of standardAliases by the namespace of the
// alias, so that the parser looks at the properties of a schema only when
// that schema can contain an alias at all. It is read-only after package
// initialisation.
var aliasesByNS = buildAliasesByNS() //nolint:gochecknoglobals // read-only lookup table built once at package initialisation

func buildAliasesByNS() map[string][]aliasRow {
	m := make(map[string][]aliasRow, 8)
	for i := range standardAliases {
		r := standardAliases[i]
		m[r.aliasNS] = append(m[r.aliasNS], r)
	}
	return m
}

// moveExplicitAliases folds the properties that are registered XMP aliases
// into their base properties, as the Adobe XMP Toolkit does at the end of
// XMPMeta::ParseFromBuffer (MoveExplicitAliases, XMPMeta-Parse.cpp, line 354).
// After it, a document holds one value per aliased property, so a reader that
// asks for the base property (dc:description, dc:rights, xmp:CreatorTool, …)
// finds the value that the document spelled with an alias, and a writer that
// updates the base property cannot leave a stale alias behind.
//
// Adobe XMP Specification Part 1 §6.3 and Part 2 §1.1.1 define the aliases:
// an application that writes XMP writes the base form only.
//
// The rules are those of the Toolkit without strict aliasing, which is how
// ParseFromBuffer calls it by default:
//
//   - A top-to-top alias (xmp:Format → dc:format) whose base property is
//     absent is transplanted: the node is renamed into the base schema.
//   - A top-to-top alias whose base property exists is deleted, whatever its
//     value: the base property wins, and a different alias value is lost.
//   - An alias to an array item (photoshop:Author → dc:creator[1],
//     photoshop:Caption → dc:description[x-default]) whose base array is
//     absent creates the array (an rdf:Seq, or an rdf:Alt for text) and
//     becomes its first item, with xml:lang="x-default" for text.
//   - An alias to an array item whose base array exists but has no such item
//     becomes that item, inserted first; when the item exists the alias is
//     deleted.
//   - A schema left without properties is removed.
//
// Two cases are left unchanged instead of being folded; the Toolkit reports
// them as fatal errors or builds an invalid tree, and neither is acceptable
// for a parser that must not lose a document: an alias to an array item that
// is not a simple value (or an alias to text with an xml:lang other than
// x-default), and an alias whose base property is a struct.
//
// The simple base property of an array alias (dc:creator written as an
// attribute) stands for the one-item array that the Toolkit's
// NormalizeDCArrays builds; it counts as the existing first item or default
// item when it is not empty, and is replaced by the array when it is empty.
func (p *rdfParser) moveExplicitAliases() {
	prev := noNode
	for s := p.t[0].firstChild; s != noNode; {
		next := p.t[s].next
		rows, isAliasSchema := aliasesByNS[p.t[s].ns]
		if p.t[s].flags&nodeBlank == 0 && isAliasSchema && p.foldSchema(s, rows) && p.t[s].firstChild == noNode {
			p.unlinkSchema(prev, s)
		} else {
			prev = s
		}
		s = next
	}
}

// foldSchema folds the aliases of rows found among the properties of the
// schema s. It reports whether it removed or moved any property.
func (p *rdfParser) foldSchema(s int32, rows []aliasRow) bool {
	changed := false
	prev := noNode
	for c := p.t[s].firstChild; c != noNode; {
		next := p.t[c].next
		row := aliasRowFor(rows, p.t[c].name)
		if row != nil && p.foldAlias(s, prev, c, row.base) {
			changed = true
		} else {
			prev = c
		}
		c = next
	}
	return changed
}

// aliasRowFor returns the row of rows for the property name, or nil.
func aliasRowFor(rows []aliasRow, name string) *aliasRow {
	for i := range rows {
		if rows[i].aliasLocal == name {
			return &rows[i]
		}
	}
	return nil
}

// foldAlias folds the alias property c of schema s (prev is its predecessor
// in the schema) into base. It reports whether c left the schema s.
func (p *rdfParser) foldAlias(s, prev, c int32, base Alias) bool { //nolint:cyclop,gocyclo // the cases of MoveExplicitAliases: absent or present base, property or array item alias
	n := &p.t[c]
	if base.Form == AliasAltTextDefault && n.flags&nodeAlternate != 0 {
		return p.foldAltAlias(s, prev, c, base)
	}
	item := base.Form != AliasSimple
	if item && (!n.isSimple() || (base.Form == AliasAltTextDefault && !p.t.plainDefaultLang(c))) {
		return false // see moveExplicitAliases: left as written
	}
	bs := p.findSchema(base.NS)
	bn := noNode
	if bs != noNode {
		bn = p.t.childNamed(bs, base.Local)
	}
	if bn != noNode && p.t[bn].isStruct() {
		return false // a struct cannot hold the value of the alias
	}

	switch {
	case !item && bn != noNode:
		// Top-to-top alias, base exists: delete the alias (non-strict).
		p.t.unlinkChild(s, prev, c)
		return true
	case !item:
		// Top-to-top alias, no base: transplant the property.
		p.t.unlinkChild(s, prev, c)
		p.est += len(base.Local)
		n.ns, n.name = base.NS, base.Local
		dst := p.baseSchema(bs, base.NS) // may grow p.t: n is not used after this
		p.t.appendChild(dst, c)
		return true
	}

	// Alias to an array item.
	if bn != noNode && p.t[bn].isSimple() && p.t[bn].value != "" {
		// The one-item array of NormalizeDCArrays already has the item.
		p.t.unlinkChild(s, prev, c)
		return true
	}
	if bn == noNode {
		flags := nodeArray | nodeOrdered
		if base.Form == AliasAltTextDefault {
			flags |= nodeAlternate
		}
		dst := p.baseSchema(bs, base.NS) // may grow p.t: evaluate before the next call that reads it
		bn = p.t.add(base.NS, base.Local, "", flags)
		p.est += len(base.Local) + 8
		p.t.appendChild(dst, bn)
	} else if p.t[bn].isSimple() {
		// An empty simple property: NormalizeDCArrays leaves an empty array.
		p.t[bn].flags |= nodeArray | nodeOrdered
		if base.Form == AliasAltTextDefault {
			p.t[bn].flags |= nodeAlternate
		}
	}
	if p.t[bn].isArray() && p.t.hasItemFor(bn, base.Form) {
		p.t.unlinkChild(s, prev, c)
		return true
	}

	// TransplantArrayItemAlias: the alias becomes the first item.
	p.t.unlinkChild(s, prev, c)
	n = &p.t[c]
	n.ns, n.name = "", ""
	n.flags |= nodeItem
	if p.t[bn].flags&nodeAlternate != 0 && p.t.itemLang(c) == "" {
		q := p.t.add(XMLNamespaceURI, "lang", "x-default", 0)
		p.est += len("lang") + len("x-default") + 8
		p.t.prependQual(c, q)
	}
	p.t.prependChild(bn, c)
	return true
}

// foldAltAlias folds the alias c of the default item of a language
// alternative when c is itself a language alternative, which is how
// tiff:ImageDescription is written in practice (it is "Lang Alt" in XMP
// Part 2, and the Toolkit's table also lists it as an alias of
// dc:description[x-default]). The Toolkit's rules cover only a simple alias
// there and would make the array an item of the base array, so this is an
// extension that applies the same rules item by item:
//
//   - no base property: the whole alternative becomes the base property;
//   - a base alternative: the x-default item of the alias is dropped when the
//     base has one, and becomes its first item when it has none; an item of
//     any other language is added at the end unless the base has that
//     language, so no translation is lost.
//
// A base property that is not a language alternative or a simple value
// without qualifiers (a struct, a bag) leaves the alias as written.
func (p *rdfParser) foldAltAlias(s, prev, c int32, base Alias) bool {
	bs := p.findSchema(base.NS)
	bn := noNode
	if bs != noNode {
		bn = p.t.childNamed(bs, base.Local)
	}
	if bn == noNode {
		p.t.unlinkChild(s, prev, c)
		p.est += len(base.Local)
		p.t[c].ns, p.t[c].name = base.NS, base.Local
		dst := p.baseSchema(bs, base.NS) // may grow p.t: no pointer into it is held
		p.t.appendChild(dst, c)
		return true
	}
	if p.t[bn].isSimple() && p.t[bn].firstQual == noNode {
		// NormalizeDCArrays: a simple value is the x-default item of a
		// one-item alternative.
		v := p.t[bn].value
		p.t[bn].value = ""
		p.t[bn].flags |= nodeArray | nodeOrdered | nodeAlternate
		if v != "" {
			it := p.t.add("", "", v, nodeItem)
			q := p.t.add(XMLNamespaceURI, "lang", "x-default", 0)
			p.t.appendQual(it, q)
			p.t.appendChild(bn, it)
			p.est += len("lang") + len("x-default") + 8
		}
	}
	if p.t[bn].flags&nodeArrayMask != nodeArray|nodeOrdered|nodeAlternate {
		return false
	}
	p.mergeAltItems(c, bn)
	p.t.unlinkChild(s, prev, c)
	return true
}

// mergeAltItems moves the items of the alias alternative c into the base
// alternative bn, as described at foldAltAlias.
func (p *rdfParser) mergeAltItems(c, bn int32) {
	for it := p.t[c].firstChild; it != noNode; {
		next := p.t[it].next
		lang := p.t.itemLang(it)
		switch {
		case !p.t[it].isSimple():
			// Not an alternative item; dropped with the alias array.
		case lang == "" || lang == "x-default":
			if !p.t.hasItemFor(bn, AliasAltTextDefault) {
				if lang == "" {
					q := p.t.add(XMLNamespaceURI, "lang", "x-default", 0)
					p.t.prependQual(it, q)
					p.est += len("lang") + len("x-default") + 8
				}
				p.t.prependChild(bn, it)
			}
		case !p.t.hasLangItem(bn, lang):
			p.t.appendChild(bn, it)
		}
		it = next
	}
}

// baseSchema returns the schema node of the namespace ns, bs when it is
// already known, creating it on first use. A schema made here has no
// prefix of its own: the namespaces of the alias bases (dc, xmp, xmpRights)
// have well-known prefixes.
func (p *rdfParser) baseSchema(bs int32, ns string) int32 {
	if bs != noNode {
		return bs
	}
	bs = p.newSchema(ns, nil)
	p.lastNS, p.lastSchema = "", 0
	return bs
}

// unlinkSchema removes the schema node s, whose predecessor in the root's
// children is prev, from the tree and from the schema index.
func (p *rdfParser) unlinkSchema(prev, s int32) {
	p.t.unlinkChild(0, prev, s)
	p.nschemas--
	delete(p.s.schemas, p.t[s].ns) // a no-op unless the schema index is in use
	p.lastNS, p.lastSchema = "", 0
}

// childNamed returns the child of p named name, or noNode.
func (l nodeList) childNamed(p int32, name string) int32 {
	for c := l[p].firstChild; c != noNode; c = l[c].next {
		if l[c].name == name {
			return c
		}
	}
	return noNode
}

// unlinkChild removes the child c of p from the child list of p; prev is the
// child before c, or noNode when c is the first child.
func (l nodeList) unlinkChild(p, prev, c int32) {
	next := l[c].next
	if prev == noNode {
		l[p].firstChild = next
	} else {
		l[prev].next = next
	}
	if l[p].lastChild == c {
		l[p].lastChild = prev
	}
	l[c].next = noNode
}

// plainDefaultLang reports whether the node c has no language, or the
// language x-default: the only forms of an alias to the default item of a
// language alternative (TransplantArrayItemAlias reports a language as an
// error; x-default is accepted here because it states the same thing).
func (l nodeList) plainDefaultLang(c int32) bool {
	lang := l.itemLang(c)
	return lang == "" || lang == "x-default"
}

// hasItemFor reports whether the array b already has the item that an alias
// of the given form denotes: the item with xml:lang="x-default" for a text
// alias, any first item for an ordered-array alias (LookupLangItem and the
// children test of MoveExplicitAliases).
func (l nodeList) hasItemFor(b int32, form AliasForm) bool {
	if form != AliasAltTextDefault {
		return l[b].firstChild != noNode
	}
	for it := l[b].firstChild; it != noNode; it = l[it].next {
		if l.itemLang(it) == "x-default" {
			return true
		}
	}
	return false
}

// hasLangItem reports whether the array b has an item with xml:lang equal to
// lang.
func (l nodeList) hasLangItem(b int32, lang string) bool {
	for it := l[b].firstChild; it != noNode; it = l[it].next {
		if l.itemLang(it) == lang {
			return true
		}
	}
	return false
}
