package xmp

// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/XMPCore_Impl.hpp:XMP_Node, Copyright 2004 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on this file.

// nodeFlags holds the form of a node. The bits mirror the option bits of the
// Adobe XMP Toolkit's XMP_Node (public/include/XMP_Const.h), plus the private
// bits this package needs to keep the flat Properties view in step with the
// tree.
type nodeFlags uint16

const (
	// nodeStruct marks a struct value (kXMP_PropValueIsStruct).
	nodeStruct nodeFlags = 1 << iota
	// nodeArray marks an array value (kXMP_PropValueIsArray); alone it is an
	// unordered rdf:Bag.
	nodeArray
	// nodeOrdered marks an ordered array, rdf:Seq (kXMP_PropArrayIsOrdered).
	nodeOrdered
	// nodeAlternate marks an alternative array, rdf:Alt
	// (kXMP_PropArrayIsAlternate). It is always set together with nodeOrdered.
	nodeAlternate
	// nodeURI marks a simple value that is a URI, serialised as rdf:resource
	// (kXMP_PropValueIsURI).
	nodeURI
	// nodeHasValueElem marks a struct that contains an rdf:value field and
	// must be turned into a qualified simple property (kRDF_HasValueElem).
	// It is meaningful only while the RDF parser runs.
	nodeHasValueElem
	// nodeItem marks an array item (an rdf:li element).
	nodeItem
	// nodeOwner marks a node whose value is mirrored by exactly one entry of
	// XMP.Properties: a simple top-level property ("P"), an array of simple
	// items ("P", items joined with U+001E), or a simple field of a struct
	// ("P.F") or of a struct array item ("P[N].F").
	nodeOwner
	// nodeNameInterned and nodeValueInterned record that name and value
	// already point into XMP.arena, so a node is never interned twice.
	nodeNameInterned
	nodeValueInterned
	// nodeRetain marks a top-level property that the flat Properties view
	// cannot reproduce exactly and that is therefore kept in XMP.nodes.
	nodeRetain
	// nodeBlank marks a root child that is not a schema but a blank-node
	// block: a top-level rdf:Description (or rdf:Bag/Seq/Alt) that carries an
	// rdf:nodeID referenced from a property. name holds the node ID. Blank
	// nodes are not part of the XMP data model (the Toolkit ignores
	// rdf:nodeID); they are kept verbatim so that RDF-aware readers such as
	// ExifTool resolve the references in a written packet as in the source.
	nodeBlank
	// nodeIndexedChildren and nodeIndexedQuals mark, while the RDF parser
	// runs, a node whose named children or qualifiers are indexed in
	// rdfScratch.dup for the duplicate check (see isDupIn).
	nodeIndexedChildren
	nodeIndexedQuals
)

// nodeArrayMask selects the array-form bits.
const nodeArrayMask = nodeArray | nodeOrdered | nodeAlternate

// noNode terminates a child or qualifier list.
const noNode int32 = -1

// node is one node of the XMP data model (ISO 16684-1:2019 §6): the root,
// a schema (one per namespace), a property, a struct field, an array item,
// or a qualifier.
//
// Children and qualifiers are singly linked lists of indices into the same
// slice, which keeps a whole tree in a single allocation. A qualifier is a
// node with its own namespace and name; xml:lang is stored as the qualifier
// (XMLNamespaceURI, "lang") and is kept first, as the Toolkit does.
type node struct {
	ns, name, value string

	firstChild, lastChild, next int32
	firstQual, lastQual         int32

	flags nodeFlags
}

// isArray reports whether n is an array.
func (n *node) isArray() bool { return n.flags&nodeArray != 0 }

// isStruct reports whether n is a struct.
func (n *node) isStruct() bool { return n.flags&nodeStruct != 0 }

// isSimple reports whether n is neither a struct nor an array.
func (n *node) isSimple() bool { return n.flags&(nodeStruct|nodeArray) == 0 }

// containerName returns the RDF container element name of an array node.
func (n *node) containerName() string {
	switch {
	case n.flags&nodeAlternate != 0:
		return "Alt"
	case n.flags&nodeOrdered != 0:
		return "Seq"
	default:
		return "Bag"
	}
}

// isLangQual reports whether n is the xml:lang qualifier.
func (n *node) isLangQual() bool { return n.ns == XMLNamespaceURI && n.name == "lang" }

// isNodeIDQual reports whether n is an rdf:nodeID reference kept as a
// qualifier (see nodeBlank).
func (n *node) isNodeIDQual() bool { return n.ns == NSrdf && n.name == "nodeID" }

// nodeList is a slice of nodes that holds one or more trees. Only add grows
// the slice and needs a pointer receiver; every other method links or reads
// nodes in the shared backing array and takes the slice by value.
type nodeList []node //nolint:recvcheck // see above: value receivers are deliberate for the linking methods

// add appends a node with empty links and returns its index.
func (l *nodeList) add(ns, name, value string, flags nodeFlags) int32 {
	idx := int32(len(*l)) //nolint:gosec // G115: the node count is bounded by the 16 MiB document cap
	*l = append(*l, node{
		ns: ns, name: name, value: value,
		firstChild: noNode, lastChild: noNode, next: noNode,
		firstQual: noNode, lastQual: noNode,
		flags: flags,
	})
	return idx
}

// appendChild links c as the last child of p.
func (l nodeList) appendChild(p, c int32) {
	l[c].next = noNode
	if l[p].lastChild == noNode {
		l[p].firstChild = c
	} else {
		l[l[p].lastChild].next = c
	}
	l[p].lastChild = c
}

// prependChild links c as the first child of p.
func (l nodeList) prependChild(p, c int32) {
	l[c].next = l[p].firstChild
	l[p].firstChild = c
	if l[p].lastChild == noNode {
		l[p].lastChild = c
	}
}

// appendQual links q as the last qualifier of p.
func (l nodeList) appendQual(p, q int32) {
	l[q].next = noNode
	if l[p].lastQual == noNode {
		l[p].firstQual = q
	} else {
		l[l[p].lastQual].next = q
	}
	l[p].lastQual = q
}

// prependQual links q as the first qualifier of p.
func (l nodeList) prependQual(p, q int32) {
	l[q].next = l[p].firstQual
	l[p].firstQual = q
	if l[p].lastQual == noNode {
		l[p].lastQual = q
	}
}

// insertQualAfter links q after the qualifier at, or first when at is noNode.
func (l nodeList) insertQualAfter(p, at, q int32) {
	if at == noNode {
		l.prependQual(p, q)
		return
	}
	l[q].next = l[at].next
	l[at].next = q
	if l[p].lastQual == at {
		l[p].lastQual = q
	}
}

// childCount returns the number of children of p.
func (l nodeList) childCount(p int32) int {
	n := 0
	for c := l[p].firstChild; c != noNode; c = l[c].next {
		n++
	}
	return n
}

// copySubtree appends a deep copy of src[i] (children and qualifiers) to dst
// and returns the index of the copy. Its next link is cleared. When p is not
// nil, every copied node is interned into p's XMP.arena before it is linked
// (see (*rdfParser).fixNode). Recursion depth is bounded by maxXMLDepth
// because every tree level comes from one XML nesting level.
func copySubtree(dst *nodeList, src nodeList, i int32, p *rdfParser) int32 {
	n := src[i]
	n.firstChild, n.lastChild, n.next = noNode, noNode, noNode
	n.firstQual, n.lastQual = noNode, noNode
	if p != nil {
		p.fixNode(&n)
	}
	idx := int32(len(*dst)) //nolint:gosec // G115: bounded by the 16 MiB document cap
	*dst = append(*dst, n)
	for q := src[i].firstQual; q != noNode; q = src[q].next {
		c := copySubtree(dst, src, q, p)
		dst.appendQual(idx, c)
	}
	for ch := src[i].firstChild; ch != noNode; ch = src[ch].next {
		c := copySubtree(dst, src, ch, p)
		dst.appendChild(idx, c)
	}
	return idx
}

// subtreeSize returns the number of nodes in the subtree rooted at i,
// qualifiers included.
func (l nodeList) subtreeSize(i int32) int {
	n := 1
	for q := l[i].firstQual; q != noNode; q = l[q].next {
		n += l.subtreeSize(q)
	}
	for c := l[i].firstChild; c != noNode; c = l[c].next {
		n += l.subtreeSize(c)
	}
	return n
}
