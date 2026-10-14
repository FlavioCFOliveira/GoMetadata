package xmp

// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FileHandlers/JPEG_Handler.cpp:JPEG_MetaHandler::ProcessXMP, Copyright 2004 Adobe, SPDX-License-Identifier: BSD-3-Clause
// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/XMPUtils.cpp:XMPUtils::MergeFromJPEG, Copyright 2003 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on this file.
//
// The Toolkit parses the standard packet and the extended serialisation into
// two trees and copies every top-level property of the second into the first,
// replacing a property that both carry (XMPUtils::MergeFromJPEG calls
// ApplyTemplate with kXMPTemplate_ReplaceExistingProperties). This port
// parses both documents into one tree instead, the extended serialisation
// first, so that the duplicate check of the RDF parser gives the same result:
// a property that both portions carry keeps its extended value, as a whole
// property. These differences remain: the properties of the extended
// serialisation precede those of the standard packet in their schema, the
// prefix that the extended serialisation binds to an unknown namespace is
// the one kept, and an extended serialisation that holds no property is
// reported with ErrNoExtendedProperties so that the caller can fall back to
// another recovery strategy.

import "errors"

// hasExtendedXMP is the local name of the property that links the standard
// XMP packet of a JPEG file to its extended XMP (Adobe XMP Specification
// Part 3 §1.1.3.1).
const hasExtendedXMP = "HasExtendedXMP"

// ErrNoExtendedProperties is returned by ParseExtended when the extended XMP
// holds no XMP property.
var ErrNoExtendedProperties = errors.New("xmp: extended XMP holds no properties")

// ParseExtended parses the two portions of the XMP of a JPEG file — the
// standard XMP packet and the extended XMP serialisation reassembled from
// its chunks — into one XMP value.
//
// Adobe XMP Specification Part 3 (2020) §1.1.3.1: "A JPEG reader must
// recompose the StandardXMP and ExtendedXMP into a single data model tree
// containing all of the XMP for the JPEG file, and remove the
// xmpNote:HasExtendedXMP property." The result therefore holds every
// top-level property of both portions, with nested structures, qualifiers
// and array forms intact, and no xmpNote:HasExtendedXMP property (in the
// NSxmpNote namespace or in the URI that earlier releases of this package
// used). A top-level property present in both portions takes the value of
// the extended portion, as a whole property, as in the Adobe XMP Toolkit
// (XMPUtils::MergeFromJPEG). rdf:about and x:xmptk are those of the standard
// packet when it has them.
//
// Both inputs are read like the input of Parse: a leading byte order mark
// selects UTF-16 or UTF-32, a <?xpacket?> wrapper is optional, and each
// document is limited to the same size. ParseExtended returns the errors of
// Parse for either document, and ErrNoExtendedProperties when the extended
// document holds no property.
func ParseExtended(standard, extended []byte) (*XMP, error) {
	stdBody, err := documentBody(standard)
	if err != nil {
		return nil, err
	}
	extBody, err := documentBody(extended)
	if err != nil {
		return nil, err
	}
	x := &XMP{Properties: make(map[string]map[string]string, 8)}
	if err := parseRDFExtended(stdBody, extBody, x); err != nil {
		return nil, err
	}
	return x, nil
}

// parseRDFExtended is parseRDF for the two portions of the XMP of a JPEG
// file: both element trees feed one parse tree, the extended serialisation
// first, so the duplicate check of addChildNode keeps the extended value of a
// property that both portions carry — the replacement that
// XMPUtils::MergeFromJPEG of the Adobe XMP Toolkit performs. rdf:about and
// x:xmptk come from the standard packet, falling back to the extended
// serialisation. HasExtendedXMP is removed before the tree is projected
// into x.
//
// Every string of the parse tree is a transient view of std, ext or the
// element tree of its document (see transientAliasString), so both element
// trees stay alive until finish has copied what x keeps into x.arena.
func parseRDFExtended(std, ext []byte, x *XMP) error {
	s := rdfScratchPool.Get().(*rdfScratch) //nolint:forcetypeassert,revive // rdfScratchPool.New always stores *rdfScratch; pool invariant
	defer s.release()
	if err := scanXML(std, &s.doc); err != nil {
		return err
	}
	es := rdfScratchPool.Get().(*rdfScratch) //nolint:forcetypeassert,revive // rdfScratchPool.New always stores *rdfScratch; pool invariant
	defer es.release()
	if err := scanXML(ext, &es.doc); err != nil {
		return err
	}

	p := rdfParser{x: x, d: &es.doc, s: s, t: s.nodes[:0]}
	p.t.add("", "", "", 0) // root
	p.walk(0)
	if p.topSeen == 0 {
		s.nodes = p.t
		return ErrNoExtendedProperties
	}
	extAbout, extAboutSet, extToolkit := p.about, p.aboutSet, p.toolkit
	p.about, p.aboutSet, p.toolkit = "", false, ""
	p.d = &s.doc
	p.walk(0)
	if !p.aboutSet {
		p.about, p.aboutSet = extAbout, extAboutSet
	}
	if p.toolkit == "" {
		p.toolkit = extToolkit
	}

	p.removeTop(NSxmpNote, hasExtendedXMP)
	p.removeTop(nsXMPNoteLegacy, hasExtendedXMP)
	p.finish(len(std) + len(ext))
	s.nodes = p.t
	return nil
}

// removeTop unlinks the top-level property (ns, name) from its schema node,
// if present. A schema left without properties produces nothing in finish.
// Port of the DeleteProperty call of XMPUtils::MergeFromJPEG.
func (p *rdfParser) removeTop(ns, name string) {
	s := p.findSchema(ns)
	if s == noNode {
		return
	}
	prev := noNode
	for c := p.t[s].firstChild; c != noNode; prev, c = c, p.t[c].next {
		if p.t[c].name != name || p.t[c].ns != ns {
			continue
		}
		next := p.t[c].next
		if prev == noNode {
			p.t[s].firstChild = next
		} else {
			p.t[prev].next = next
		}
		if p.t[s].lastChild == c {
			p.t[s].lastChild = prev
		}
		p.t[c].next = noNode
		return
	}
}
