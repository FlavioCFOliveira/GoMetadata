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
func serialise(x *XMP) ([]byte, error) { //nolint:gocyclo,cyclop // complexity is inherent: XMP serialiser must handle structs, arrays, bags, sequences, and all scalar types with distinct XML representations
	buf := encBufPool.Get().(*bytes.Buffer) //nolint:forcetypeassert,revive // encBufPool.New always stores *bytes.Buffer; pool invariant
	buf.Reset()

	// Estimate output size: fixed wrapper (~250 B) + 2 KB padding + ~100 B per property.
	nProps := 0
	for _, props := range x.Properties {
		nProps += len(props)
	}
	buf.Grow(256 + 2048 + nProps*100)

	// Opening packet wrapper with UTF-8 BOM marker (XMP §7.1).
	buf.WriteString("<?xpacket begin=\"\xef\xbb\xbf\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n")
	buf.WriteString("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\">\n")
	buf.WriteString(" <rdf:RDF xmlns:rdf=\"http://www.w3.org/1999/02/22-rdf-syntax-ns#\">\n")

	// Sort namespace URIs for deterministic output (ISO 16684-1 §7.4).
	nsListPtr := nsListPool.Get().(*[]string) //nolint:forcetypeassert,revive // nsListPool.New always stores *[]string; pool invariant
	nsList := (*nsListPtr)[:0]
	for ns, props := range x.Properties {
		if len(props) > 0 {
			nsList = append(nsList, ns)
		}
	}
	slices.Sort(nsList)
	// Write-back: if append grew the slice, update the pointer so the pool
	// gets the larger backing array next time.
	*nsListPtr = nsList
	defer nsListPool.Put(nsListPtr)

	// NS-03 / XMP Part 1 §6 / XML Namespaces: the writer MUST NOT bind two
	// distinct URIs to the same prefix.  Well-known URIs have fixed canonical
	// prefixes (prefixMap).  Unknown URIs fall back to generated prefixes
	// ns0, ns1, … using a per-call counter so sibling unknown namespaces always
	// get distinct prefixes regardless of insertion order.
	usedPrefixes := make(map[string]struct{}, len(nsList))
	// Pre-populate with all well-known prefixes that will be used in this packet,
	// so generated ns0/ns1/… never accidentally collide with a canonical prefix.
	for _, ns := range nsList {
		if p, ok := prefixMap[ns]; ok {
			usedPrefixes[p] = struct{}{}
		}
	}
	unknownNSCounter := 0

	for _, ns := range nsList {
		props := x.Properties[ns]
		prefix := uniquePrefixFor(ns, usedPrefixes, &unknownNSCounter)
		buf.WriteString("  <rdf:Description rdf:about=\"\" xmlns:")
		buf.WriteString(prefix)
		buf.WriteString("=\"")
		// #170 / XML 1.0 §3.1 / ISO 16684-1 §7: the namespace URI is written as
		// an XML attribute value and MUST be XML-escaped.  An unescaped URI can
		// contain '"', '<', or '&' (e.g., a URI decoded from &quot; while parsing
		// untrusted XMP), which would break the attribute boundary and inject
		// arbitrary XML.  writeXMLEscaped handles all five XML entities plus C0
		// control chars, making the value safe for attribute context.
		writeXMLEscaped(buf, ns)
		buf.WriteString("\">\n")

		// Sort property names for deterministic output.
		localListPtr := localListPool.Get().(*[]string) //nolint:forcetypeassert,revive // localListPool.New always stores *[]string; pool invariant
		localList := (*localListPtr)[:0]
		for local := range props {
			localList = append(localList, local)
		}
		slices.Sort(localList)
		*localListPtr = localList

		// #14: Classify properties into three groups before serialising:
		//   1. Top-level properties (no dot, or dot not preceded by "[N]") → simple / multi-valued.
		//   2. Struct properties (key "parent.field") → must be emitted under one
		//      <prefix:parent rdf:parseType="Resource"> wrapper per parent key.
		//   3. Struct-in-list-item properties (key "parent[N].field") → must be emitted
		//      as a sequence/bag of rdf:Description elements.
		//
		// Strategy: scan localList and collect the set of "top" (parent) names for
		// struct groups. Emit top-level props first, then each struct group, then
		// each struct-in-list group. All groups are sorted for deterministic output.
		topProps, structParents, listStructParents := classifyProps(localList)

		for _, local := range topProps {
			val := props[local]
			// #RECONCILE-05 fix: an array-typed property (dc:creator, dc:subject,
			// dc:description, dc:rights, dc:title, xmpMM's ordered-array set) MUST
			// always use its rdf:Alt/Seq/Bag collection container per
			// ISO 16684-1 §7.5, even when it holds exactly one item — the presence
			// of the internal U+001E multi-item separator is not, by itself, a
			// reliable signal of array-ness. isCollectionProperty carries that
			// schema knowledge independently of value count.
			if strings.IndexByte(val, '\x1e') < 0 && !isCollectionProperty(ns, local) {
				writeSimpleProperty(buf, prefix, local, val)
			} else {
				writeMultiValuedProperty(buf, prefix, ns, local, val)
			}
		}
		for _, parent := range structParents {
			writeStructProperty(buf, prefix, parent, props, localList)
		}
		for _, parent := range listStructParents {
			writeStructInListProperty(buf, prefix, ns, parent, props, localList)
		}

		// #151: localListPool.Put MUST follow the last use of localList (i.e.
		// after all three classify loops above). Putting the pointer back to the
		// pool before the loops complete would allow another goroutine to receive
		// and overwrite the backing slice while this call is still iterating it.
		// The current placement (after writeStructInListProperty) is correct.
		localListPool.Put(localListPtr)
		buf.WriteString("  </rdf:Description>\n")
	}

	buf.WriteString(" </rdf:RDF>\n</x:xmpmeta>\n")

	// 2 KB padding of spaces / newlines for in-place editing (XMP §7.3).
	// Uses the pre-computed package-level array to avoid a per-call allocation.
	buf.Write(xmpPadding[:])
	buf.WriteString("\n<?xpacket end=\"w\"?>")

	// Copy the result before returning the buffer to the pool so callers own
	// their slice independently of the pooled backing array.
	result := bytes.Clone(buf.Bytes())
	encBufPool.Put(buf)
	return result, nil
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
// val is a '\x1e'-delimited list of values. The RDF collection type (Alt, Seq,
// or Bag) is determined by collectionType(ns, local) per ISO 16684-1 §7.5.
// For Alt collections, items may carry an xml:lang prefix encoded as "lang|value".
//
// #171 / XML 1.0 §2.3: local is emitted as an XML element tag name; writeXMLName
// strips illegal NCName bytes to prevent XML injection.
func writeMultiValuedProperty(buf *bytes.Buffer, prefix, ns, local, val string) {
	ctype := collectionType(ns, local)
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
// buildStructInListKey in rdf.go; "parent.field" is produced by onCharDataStructField.
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
// determined by collectionType; xmpMM:History uses Seq.
//
// Items are sorted by index to ensure deterministic, correct ordering.
func writeStructInListProperty(buf *bytes.Buffer, prefix, ns, parent string, props map[string]string, localList []string) {
	// Collect all unique indices present for this parent.
	indices := collectStructInListIndices(parent, localList)
	if len(indices) == 0 {
		return
	}
	slices.Sort(indices)

	ctype := collectionType(ns, parent)
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
