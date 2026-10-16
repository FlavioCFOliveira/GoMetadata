package xmp

// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/XMPUtils.cpp:XMPUtils::PackageForJPEG,EstimateSizeForJPEG,CreateEstimatedSizeMap,MoveLargestProperty, Copyright 2003 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on this file.
//
// PackageForJPEG follows the Toolkit's partitioning step by step (Adobe XMP
// Specification Part 3 (2020) §1.1.3.2, p. 15): serialise as compactly as
// possible; if that is too large, move xmp:Thumbnails, then every property of
// the Camera Raw namespace, then photoshop:History, then the other top-level
// properties in order of estimated serialised size, largest first, to the
// extended portion until the standard portion fits. Differences from the
// Toolkit:
//   - xmp:Thumbnails is moved to the extended portion instead of being
//     deleted: Part 3 does not mandate deleting it, and a write must not lose
//     metadata it was not asked to change;
//   - the compact form removes formatting white space (compactMarkup) but
//     does not use attribute shorthand for simple properties, so the sizes
//     measured here are somewhat larger than the Toolkit's and a packet very
//     close to the limit may be split that Adobe software would keep whole;
//   - the xmpNote:HasExtendedXMP dummy value is added before the first
//     measurement of a partition, as Part 3 §1.1.3.1 (p. 15) requires ("be
//     sure to first add the xmpNote:HasExtendedXMP property ... with an
//     initial 32-byte dummy value"), where the Toolkit adds it at its third
//     step;
//   - a top-level property that refers to a blank node block (rdf:nodeID)
//     stays in the standard portion together with the block, as the Toolkit
//     has no such construct.

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: MD5 is the digest that Adobe XMP Specification Part 3 §1.1.3.1 mandates for the extended XMP GUID; it is an identifier, not a security control
	"slices"
	"strings"
)

// jpegStdLimit is the size in bytes, padding included, to which the standard
// XMP packet of a JPEG is reduced (XMPUtils.cpp:PackageForJPEG, kStdXMPLimit).
// It is below the 65502-byte APP1 limit of Part 3 §1.1.3.1 (p. 14) to leave
// room for the packet padding.
const jpegStdLimit = 65000

// jpegStdPadding is the largest padding added to the standard packet
// (XMPUtils.cpp:PackageForJPEG: "Adjust the standard XMP padding to be up to
// 2KB").
const jpegStdPadding = 2047

// nsCameraRaw is the Camera Raw namespace (prefix crs).
const nsCameraRaw = "http://ns.adobe.com/camera-raw-settings/1.0/"

// nsXMPBasic is the namespace of xmp:Thumbnails.
const nsXMPBasic = NSxmp

// hasExtendedXMPDummy is the 32-byte value that stands for the GUID while the
// standard portion is measured (Part 3 §1.1.3.1, p. 15).
const hasExtendedXMPDummy = "123456789-123456789-123456789-12"

// JPEGPackage is the result of PackageForJPEG.
type JPEGPackage struct {
	// Standard is the standard XMP packet, <?xpacket?> wrapper included, at
	// most 65000 bytes long. When Extended is not nil it holds the property
	// xmpNote:HasExtendedXMP with the value GUID.
	Standard []byte
	// Extended is the extended XMP serialisation, without a packet wrapper,
	// or nil when the whole XMP fits the standard packet.
	Extended []byte
	// GUID is the uppercase hexadecimal MD5 digest of Extended (32 digits),
	// or "" when Extended is nil.
	GUID string
}

// topProp is one top-level property of an XMP value: the Properties keys that
// belong to it and its retained tree node.
type topProp struct {
	ns, name string
	keys     []string // Properties keys, sorted
	node     int32    // retained node in XMP.nodes, or noNode
	pinned   bool     // refers to a blank node block: never moved
	id       int      // index in the slice PackageForJPEG works on
}

// isLink reports whether tp is xmpNote:HasExtendedXMP, in either of the
// namespace URIs this package has used for it.
func (tp *topProp) isLink() bool {
	return tp.name == hasExtendedXMP && (tp.ns == NSxmpNote || tp.ns == nsXMPNoteLegacy)
}

// topProps lists the top-level properties of x, namespaces and names in
// sorted order, retained properties included. A Properties key belongs to the
// retained property that owns it (propNames.bucketOf), and otherwise to the
// property its form names: "P", "P.F" or "P[N].F" belongs to P.
func (x *XMP) topProps() []topProp { //nolint:gocyclo // two sources of properties (the retained tree and the flat keys) with the key-to-property mapping of each
	var out []topProp
	for _, ns := range x.namespaces(nil) {
		props := x.Properties[ns]
		first := len(out)
		byName := map[string]int{}
		var pn propNames
		if si := x.schemaIndex(ns); si != noNode {
			pn.t = x.nodes
			for c := x.nodes[si].firstChild; c != noNode; c = x.nodes[c].next {
				pn.props = append(pn.props, c)
				byName[x.nodes[c].name] = len(out)
				out = append(out, topProp{ns: ns, name: x.nodes[c].name, node: c, pinned: x.nodes.hasNodeID(c)})
			}
			if len(pn.props) > dupLinearLimit {
				pn.index = make(map[string]int, len(pn.props))
				for o, c := range pn.props {
					if _, dup := pn.index[x.nodes[c].name]; !dup {
						pn.index[x.nodes[c].name] = o
					}
				}
			}
		}
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			var idx int
			if o := pn.bucketOf(k); o >= 0 {
				idx = first + o
			} else {
				name := k
				if parent, _, listStruct, isStruct := parseStructKey(k); listStruct || isStruct {
					name = parent
				}
				i, ok := byName[name]
				if !ok {
					i = len(out)
					byName[name] = i
					out = append(out, topProp{ns: ns, name: name, node: noNode})
				}
				idx = i
			}
			out[idx].keys = append(out[idx].keys, k)
		}
	}
	return out
}

// hasNodeID reports whether the subtree of node i holds an rdf:nodeID
// reference to a blank node block.
func (l nodeList) hasNodeID(i int32) bool {
	for q := l[i].firstQual; q != noNode; q = l[q].next {
		if l[q].isNodeIDQual() {
			return true
		}
	}
	for c := l[i].firstChild; c != noNode; c = l[c].next {
		if l.hasNodeID(c) {
			return true
		}
	}
	return false
}

// subset returns a new XMP value that holds the top-level properties of props
// for which keep returns true, with the rdf:about, x:xmptk and prefixes of x;
// blank node blocks are copied when blanks is true. Its strings are those of
// x, so it shares x.arena and must not outlive the use of x.
func (x *XMP) subset(props []topProp, keep func(*topProp) bool, blanks bool) *XMP { //nolint:gocyclo // copies the flat keys and the retained node of each property, and the blank node blocks
	sub := &XMP{Properties: make(map[string]map[string]string, len(x.Properties)), about: x.about, toolkit: x.toolkit, prefixes: x.prefixes, arena: x.arena}
	var nodes nodeList
	schemas := map[string]int32{}
	if len(x.nodes) > 0 {
		nodes.add("", "", "", 0)
	}
	for i := range props {
		tp := &props[i]
		if !keep(tp) {
			continue
		}
		if len(tp.keys) > 0 {
			m := sub.Properties[tp.ns]
			if m == nil {
				m = make(map[string]string, len(tp.keys))
				sub.Properties[tp.ns] = m
			}
			for _, k := range tp.keys {
				m[k] = x.Properties[tp.ns][k]
			}
		}
		if tp.node == noNode {
			continue
		}
		si, ok := schemas[tp.ns]
		if !ok {
			si = nodes.add(tp.ns, "", "", 0)
			nodes.appendChild(0, si)
			schemas[tp.ns] = si
		}
		nodes.appendChild(si, copySubtree(&nodes, x.nodes, tp.node, nil))
	}
	if blanks {
		for s := x.firstBlank(); s != noNode; s = x.nextBlank(s) {
			if len(nodes) == 0 {
				nodes.add("", "", "", 0)
			}
			nodes.appendChild(0, copySubtree(&nodes, x.nodes, s, nil))
		}
	}
	if len(nodes) > 1 {
		sub.nodes = nodes
	}
	return sub
}

// schemaNS returns the retained property nodes of the namespace ns as a
// slice, empty when the namespace has no retained schema.
func (x *XMP) schemaNS(ns string) []int32 {
	si := x.schemaIndex(ns)
	if si == noNode {
		return nil
	}
	var out []int32
	for c := x.nodes[si].firstChild; c != noNode; c = x.nodes[c].next {
		out = append(out, c)
	}
	return out
}

// firstBlank and nextBlank iterate the blank node blocks of x.nodes.
func (x *XMP) firstBlank() int32 {
	if len(x.nodes) == 0 {
		return noNode
	}
	return x.nextBlank2(x.nodes[0].firstChild)
}

func (x *XMP) nextBlank(s int32) int32 { return x.nextBlank2(x.nodes[s].next) }

func (x *XMP) nextBlank2(s int32) int32 {
	for ; s != noNode; s = x.nodes[s].next {
		if x.nodes[s].flags&nodeBlank != 0 {
			return s
		}
	}
	return noNode
}

// compactSize returns the size of the compact serialisation of x with its
// packet wrapper and without padding.
func compactSize(x *XMP) (int, error) {
	buf, err := serialiseBuf(x, serialiseCompact)
	if err != nil {
		return 0, err
	}
	n := buf.Len()
	encBufPool.Put(buf)
	return n, nil
}

// PackageForJPEG partitions x for storage in a JPEG file (Adobe XMP
// Specification Part 3 §1.1.3, pp. 14-15): the whole XMP as one standard
// packet when its compact serialisation fits 65000 bytes, and otherwise a
// standard packet of at most 65000 bytes with the most important properties
// plus an extended serialisation with the rest. The standard packet carries
// xmpNote:HasExtendedXMP, whose value is the GUID of the extended
// serialisation (the uppercase hexadecimal MD5 digest of its bytes, so the
// same XMP always gives the same result).
//
// The properties move in the order of Part 3 §1.1.3.2 (p. 15) until the
// standard packet fits: xmp:Thumbnails, the Camera Raw namespace,
// photoshop:History, then the others by estimated serialised size, largest
// first. Every property is kept, in one portion or the other. An
// xmpNote:HasExtendedXMP property that x carries is dropped. PackageForJPEG
// returns ErrTooLargeForJPEG when the standard portion cannot be made to fit.
// It does not modify x.
func PackageForJPEG(x *XMP) (*JPEGPackage, error) {
	props := x.topProps()
	props = slices.DeleteFunc(props, func(tp topProp) bool { return tp.isLink() })
	for i := range props {
		props[i].id = i
	}
	pt := &partition{x: x, props: props, moved: make([]bool, len(props))}

	whole, err := serialiseBuf(pt.standard(false), serialiseCompact)
	if err != nil {
		return nil, err
	}
	full := whole.Len()
	if full <= jpegStdLimit {
		std := bytes.Clone(whole.Bytes())
		encBufPool.Put(whole)
		return &JPEGPackage{Standard: padStandard(std)}, nil
	}
	encBufPool.Put(whole)
	if err := pt.reduce(full); err != nil { //nolint:govet // shadow: the outer err is not used after this point
		return nil, err
	}

	ext, err := serialiseMode(pt.extended(), serialiseCompact|serialiseNoWrapper)
	if err != nil {
		return nil, err
	}
	// Part 3 §1.1.3.1 (p. 14): the GUID is the MD5 digest of the full
	// ExtendedXMP serialisation, as 32 hexadecimal digits, capital A-F.
	sum := md5.Sum(ext) //nolint:gosec // G401: see the import comment; the spec mandates MD5
	const hexDigits = "0123456789ABCDEF"
	guid := make([]byte, 0, 2*len(sum))
	for _, c := range sum {
		guid = append(guid, hexDigits[c>>4], hexDigits[c&0x0F])
	}
	s := pt.standard(true)
	s.Properties[NSxmpNote][hasExtendedXMP] = string(guid)
	std, err := serialiseMode(s, serialiseCompact)
	if err != nil {
		return nil, err
	}
	return &JPEGPackage{Standard: padStandard(std), Extended: ext, GUID: string(guid)}, nil
}

// StripExtendedLink returns packet, an XMP packet, without its
// xmpNote:HasExtendedXMP property, re-serialised like Encode does. Part 3
// §1.1.3.1 (p. 15): the property exists only in the file, and a JPEG reader
// removes it when it recomposes the standard and extended portions; the Adobe
// XMP Toolkit removes it also when the extended portions do not match its GUID
// (JPEG_Handler.cpp:JPEG_MetaHandler::ProcessXMP). A packet without the
// property is returned unchanged. It returns the errors of Parse.
func StripExtendedLink(packet []byte) ([]byte, error) {
	x, err := Parse(packet)
	if err != nil {
		return nil, err
	}
	props := x.topProps()
	if !slices.ContainsFunc(props, func(tp topProp) bool { return tp.isLink() }) {
		return packet, nil
	}
	return serialise(x.subset(props, func(tp *topProp) bool { return !tp.isLink() }, true))
}

// WithExtendedLink returns packet, an XMP packet, with the property
// xmpNote:HasExtendedXMP set to guid, re-serialised like Encode does. A JPEG
// writer that keeps extended XMP segments it cannot merge (task #357) uses it
// to link the standard packet it writes to them: Part 3 §1.1.3.1 (p. 14) stores
// the GUID of the ExtendedXMP in that property, and a reader incorporates only
// the extended segments whose GUID matches it. It returns the errors of Parse.
func WithExtendedLink(packet []byte, guid string) ([]byte, error) {
	x, err := Parse(packet)
	if err != nil {
		return nil, err
	}
	x.putProp(NSxmpNote, hasExtendedXMP, guid)
	return serialise(x)
}

// partition is the state of one PackageForJPEG call: the top-level
// properties of the XMP and which of them have moved to the extended portion.
type partition struct {
	x     *XMP
	props []topProp
	moved []bool
}

// standard returns the standard portion: the properties that have not moved,
// plus xmpNote:HasExtendedXMP with the dummy value when link is true or any
// property has moved.
func (pt *partition) standard(link bool) *XMP {
	s := pt.x.subset(pt.props, func(tp *topProp) bool { return !pt.moved[tp.id] }, true)
	if link || slices.Contains(pt.moved, true) {
		if s.Properties[NSxmpNote] == nil {
			s.Properties[NSxmpNote] = map[string]string{}
		}
		s.Properties[NSxmpNote][hasExtendedXMP] = hasExtendedXMPDummy
	}
	return s
}

// extended returns the extended portion: the properties that have moved.
func (pt *partition) extended() *XMP {
	return pt.x.subset(pt.props, func(tp *topProp) bool { return pt.moved[tp.id] }, false)
}

// measure returns the compact size of the standard portion, which carries
// the link property from the start of the partition (Part 3 §1.1.3.1, p. 15).
func (pt *partition) measure() (int, error) {
	return compactSize(pt.standard(true))
}

// moveIf moves every movable property for which pred holds and reports
// whether it moved any.
func (pt *partition) moveIf(pred func(*topProp) bool) (moved bool) {
	for i := range pt.props {
		if tp := &pt.props[i]; !pt.moved[i] && !tp.pinned && pred(tp) {
			pt.moved[i] = true
			moved = true
		}
	}
	return moved
}

// linkOverhead returns the number of bytes that xmpNote:HasExtendedXMP adds to
// a compact packet that has no other xmpNote property: the size of a packet
// that holds only the property, less the size of an empty packet.
func linkOverhead() (int, error) {
	link := &XMP{Properties: map[string]map[string]string{NSxmpNote: {hasExtendedXMP: hasExtendedXMPDummy}}}
	with, err := compactSize(link)
	if err != nil {
		return 0, err
	}
	without, err := compactSize(&XMP{Properties: map[string]map[string]string{}})
	return with - without, err
}

// reduce moves properties to the extended portion in the order of Part 3
// §1.1.3.2 (p. 15) until the standard portion fits jpegStdLimit
// (XMPUtils.cpp:PackageForJPEG). full is the compact size of the XMP before
// anything moves and without the link property; the size with the link is
// derived from it, so the whole XMP is not serialised again.
func (pt *partition) reduce(full int) error { //nolint:gocyclo // one branch per step of the Toolkit's partitioning
	steps := [...]func(*topProp) bool{
		func(tp *topProp) bool { return tp.ns == nsXMPBasic && tp.name == "Thumbnails" },
		func(tp *topProp) bool { return tp.ns == nsCameraRaw },
		func(tp *topProp) bool { return tp.ns == NSphotoshop && tp.name == "History" },
	}
	size := full
	if len(pt.x.Properties[NSxmpNote]) > 0 || len(pt.x.schemaNS(NSxmpNote)) > 0 {
		var err error
		if size, err = pt.measure(); err != nil {
			return err
		}
	} else {
		over, err := linkOverhead()
		if err != nil {
			return err
		}
		size += over
	}
	for _, pred := range steps {
		if size <= jpegStdLimit {
			return nil
		}
		var err error
		if !pt.moveIf(pred) {
			continue // nothing moved: the size is unchanged
		}
		if size, err = pt.measure(); err != nil {
			return err
		}
	}
	if size > jpegStdLimit {
		var err error
		if size, err = pt.moveLargest(size); err != nil {
			return err
		}
	}
	if size > jpegStdLimit {
		return ErrTooLargeForJPEG
	}
	return nil
}

// sizedProp is a movable property with its estimated serialised size.
type sizedProp struct{ id, est int }

// moveLargest moves the remaining top-level properties in order of estimated
// size, largest first, as XMPUtils.cpp:PackageForJPEG does: an inner loop
// moves what the estimates say is enough, the outer loop measures and repeats
// in case the estimates were too low. Equal estimates move in the order the
// Toolkit's multimap yields them: properties are entered from the last to the
// first, and the one entered last moves first. It returns the new size.
func (pt *partition) moveLargest(size int) (int, error) {
	cands := make([]sizedProp, 0, len(pt.props))
	for i := len(pt.props) - 1; i >= 0; i-- {
		if tp := &pt.props[i]; !pt.moved[i] && !tp.pinned {
			cands = append(cands, sizedProp{id: i, est: max(pt.x.estimateJPEG(tp), 1)})
		}
	}
	slices.SortStableFunc(cands, func(a, b sizedProp) int { return a.est - b.est })
	for size > jpegStdLimit && len(cands) > 0 {
		tempLen := size
		for tempLen > jpegStdLimit && len(cands) > 0 {
			c := cands[len(cands)-1]
			cands = cands[:len(cands)-1]
			pt.moved[c.id] = true
			tempLen -= min(c.est, tempLen) // do not go negative
		}
		var err error
		if size, err = pt.measure(); err != nil {
			return 0, err
		}
	}
	return size, nil
}

// padStandard adds up to jpegStdPadding spaces of padding before the packet
// trailer, so that the packet is at most jpegStdLimit bytes long
// (XMPUtils.cpp:PackageForJPEG).
func padStandard(std []byte) []byte {
	trailer := []byte(`<?xpacket end="w"?>`)
	i := bytes.LastIndex(std, trailer)
	extra := min(jpegStdPadding, jpegStdLimit-len(std))
	if i < 0 || extra <= 0 {
		return std
	}
	out := make([]byte, 0, len(std)+extra)
	out = append(out, std[:i]...)
	out = append(out, bytes.Repeat([]byte{' '}, extra)...)
	return append(out, std[i:]...)
}

// estimateJPEG estimates the serialised size of the top-level property tp in
// the compact form that the Toolkit measures (XMPUtils.cpp:
// EstimateSizeForJPEG): a property serialised as an attribute, element, array
// or struct, with a fixed overhead per form. Qualifiers are not counted.
func (x *XMP) estimateJPEG(tp *topProp) int { //nolint:gocyclo // one case per form of a flat property key
	if tp.node != noNode {
		return x.nodes.estimateJPEG(tp.node, false)
	}
	name := estName(tp.ns, tp.name)
	props := x.Properties[tp.ns]
	table := arrayProperties[tp.ns]
	est := 0
	structs := map[string]bool{} // distinct item indices of "P[N].F" keys
	haveStruct, haveListStruct := false, false
	for _, k := range tp.keys {
		v := props[k]
		parent, field, listStruct, isStruct := parseStructKey(k)
		switch {
		case listStruct:
			haveListStruct = true
			idx := k[len(parent)+1:]
			if end := strings.IndexByte(idx, ']'); end >= 0 {
				idx = idx[:end]
			}
			if !structs[idx] {
				structs[idx] = true
				est += 8 + 9 + 25 // the rdf:li element and its parseType attribute
			}
			est += estName(tp.ns, field) + 3 + len(v)
		case isStruct:
			haveStruct = true
			est += estName(tp.ns, field) + 3 + len(v)
		case strings.Contains(v, "\x1e") || tableContainerKnown(table, k):
			n := strings.Count(v, "\x1e") + 1
			est += 2*name + 5 + 9 + 10 + n*(8+9) + len(v) - (n - 1)
		default:
			est += name + 3 + len(v)
		}
	}
	switch {
	case haveListStruct:
		est += 2*name + 5 + 9 + 10
	case haveStruct:
		est += 2*name + 5 + 25
	}
	return est
}

// tableContainerKnown reports whether name is an array property of table.
func tableContainerKnown(table map[string]string, name string) bool {
	_, ok := table[name]
	return ok
}

// estName returns the size of the qualified name prefix:local.
func estName(ns, local string) int {
	p, ok := prefixMap[ns]
	if !ok {
		p = "ns0"
	}
	return len(p) + 1 + len(local)
}

// estimateJPEG ports XMPUtils.cpp:EstimateSizeForJPEG for the retained node
// i. inArray is true when its parent is an array, whose items carry no name.
func (l nodeList) estimateJPEG(i int32, inArray bool) int {
	n := &l[i]
	name := estName(n.ns, n.name)
	est := 0
	switch {
	case n.isSimple():
		if !inArray {
			est += name + 3 // assume attribute form
		}
		est += len(n.value)
	case n.isArray():
		if !inArray {
			est += 2*name + 5
		}
		est += 9 + 10 // the rdf:Xyz tags
		est += l.childCount(i) * (8 + 9)
		for c := n.firstChild; c != noNode; c = l[c].next {
			est += l.estimateJPEG(c, true)
		}
	default:
		if !inArray {
			est += 2*name + 5
		}
		est += 25 // the rdf:parseType="Resource" attribute
		for c := n.firstChild; c != noNode; c = l[c].next {
			est += l.estimateJPEG(c, false)
		}
	}
	return est
}
