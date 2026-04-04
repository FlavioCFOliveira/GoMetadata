package xmp

import (
	"bytes"
	"fmt"
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
var liPool = sync.Pool{New: func() any {
	s := make([]string, 0, 8)
	return &s
}}

// builderPool recycles strings.Builder instances used by unescapeXML.
// They are only taken from the pool when the input actually contains '&'.
var builderPool = sync.Pool{New: func() any { return &strings.Builder{} }}

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
	// Depth tracking (absolute element nesting level, 1-based).
	var depth int
	// Depth at which the current top-level rdf:Description was opened.
	var descDepth int
	// Depth at which the current property element was opened.
	var propDepth int
	// Namespace and local name of the current property element.
	var propNS, propLocal string
	// True when inside an rdf:Alt / rdf:Seq / rdf:Bag.
	var inColl bool
	// True when inside a struct value node (nested rdf:Description inside a property).
	var inStruct bool
	// Depth of the struct's rdf:Description element.
	var structDepth int
	// Field element currently being parsed inside a struct.
	var structFieldNS, structFieldLocal string
	var structFieldDepth int
	// xml:lang of the current rdf:li element (P1-H, used for rdf:Alt).
	var liLang string

	// Stack-allocated namespace table. The scanner pushes entries as it
	// encounters xmlns:prefix="uri" declarations; resolveNS scans backward so
	// inner declarations shadow outer ones correctly.
	var nsTable [32]nsEntry
	nsCount := 0

	// Stack-allocated attribute buffer. 16 attributes per element is sufficient
	// for all real XMP properties; excess attributes are silently dropped.
	var attrBuf [16]xmpAttr

	// Pooled list accumulator for rdf:li values.
	liVals := liPool.Get().(*[]string)
	*liVals = (*liVals)[:0]
	defer liPool.Put(liVals)

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

		// ── Comment: <!-- ... --> ────────────────────────────────────────────
		if pos+2 < len(b) && b[pos] == '!' && b[pos+1] == '-' && b[pos+2] == '-' {
			end := bytes.Index(b[pos:], []byte("-->"))
			if end < 0 {
				break
			}
			pos += end + 3
			continue
		}

		// ── Processing instruction: <? ... ?> ────────────────────────────────
		if b[pos] == '?' {
			end := bytes.Index(b[pos:], []byte("?>"))
			if end < 0 {
				break
			}
			pos += end + 2
			continue
		}

		// ── CDATA / DOCTYPE — skip gracefully ────────────────────────────────
		if b[pos] == '!' {
			end := bytes.IndexByte(b[pos:], '>')
			if end < 0 {
				break
			}
			pos += end + 1
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

			// EndElement dispatch — mirrors rdf.go:144-173.
			switch {
			case inStruct && structFieldDepth > 0 && depth == structFieldDepth:
				// Closing a struct field element.
				structFieldNS, structFieldLocal = "", ""
				structFieldDepth = 0

			case inStruct && depth == structDepth:
				// Closing the struct value node.
				inStruct = false
				structDepth = 0

			case depth == propDepth && propDepth > 0:
				// Closing the current property element.
				if inColl && len(*liVals) > 0 {
					storeProperty(x, propNS, propLocal, strings.Join(*liVals, "\x1e"))
					*liVals = (*liVals)[:0]
				}
				inColl = false
				inStruct = false
				propNS, propLocal = "", ""
				propDepth = 0

			case depth == descDepth:
				descDepth = 0
			}
			depth--
			continue
		}

		// ── Start tag or self-closing tag ────────────────────────────────────
		depth++
		if depth > 100 {
			return fmt.Errorf("xmp: XML nesting depth exceeded 100 levels")
		}

		// Parse the tag name: [prefix:]local.
		// scanName returns zero-copy byte slices; string(tagLocal) for comparisons
		// is optimised by the Go compiler to a zero-allocation byte comparison.
		tagPrefix, tagLocal, newPos := scanName(b, pos)
		pos = newPos

		// Resolve the element's namespace URI.
		ns := resolveNS(nsTable[:nsCount], tagPrefix)

		// Parse attributes. xmlns declarations are registered into nsTable;
		// regular attributes land in attrBuf.
		var nAttrs int
		nsCount, nAttrs, pos = scanAttrs(b, pos, &nsTable, nsCount, &attrBuf)
		attrs := attrBuf[:nAttrs]

		// Detect self-closing '/>' — consume '/' and '>'.
		selfClose := false
		if pos < len(b) && b[pos] == '/' {
			selfClose = true
			pos++
		}
		if pos < len(b) && b[pos] == '>' {
			pos++
		}

		// ── StartElement dispatch — mirrors rdf.go:61-141 ────────────────────

		// First pass over attrs: handle rdf:parseType="Resource" and
		// rdf:resource shorthand (XMP Part 1 §C.2.5 / §C.2.6).
		for _, a := range attrs {
			if a.ns == NSrdf && a.loc == "parseType" && a.val == "Resource" {
				inStruct = true
				structDepth = depth
			}
			if a.ns == NSrdf && a.loc == "resource" && propDepth > 0 && depth == propDepth {
				storeProperty(x, propNS, propLocal, a.val)
			}
		}

		switch {
		// ── Struct field element inside a struct node (P1-G) ──────────────
		case inStruct && depth == structDepth+1 && structFieldDepth == 0:
			structFieldNS = ns
			structFieldLocal = string(tagLocal)
			structFieldDepth = depth

		// ── rdf:Description: top-level block or struct value node ─────────
		case ns == NSrdf && string(tagLocal) == "Description":
			if propDepth > 0 && depth == propDepth+1 && !inStruct {
				// Struct value node: nested rdf:Description inside a property
				// element (XMP Part 1 §C.2.6). Store inline attrs as
				// "propLocal.fieldLocal" keys in the parent property's namespace.
				inStruct = true
				structDepth = depth
				for _, a := range attrs {
					if a.ns == "" || a.ns == NSrdf || a.ns == NSx {
						continue
					}
					storeProperty(x, propNS, propLocal+"."+a.loc, a.val)
				}
			} else if descDepth == 0 {
				// Top-level rdf:Description — begin a new property block.
				// Shorthand properties are inline attributes (XMP Part 1 §C.2.4).
				descDepth = depth
				for _, a := range attrs {
					if a.ns == "" || a.ns == NSrdf || a.ns == NSx {
						continue
					}
					storeProperty(x, a.ns, a.loc, a.val)
				}
			}

		// ── Property element: direct child of top-level rdf:Description ───
		case descDepth > 0 && depth == descDepth+1 && propDepth == 0:
			propNS = ns
			propLocal = string(tagLocal) // string() here: stored as map key
			propDepth = depth
			// rdf:resource shorthand — already handled in the attr loop above.
			// rdf:parseType="Resource" — already handled above.

		// ── Collection container: rdf:Alt / Seq / Bag ────────────────────
		case propDepth > 0 && depth == propDepth+1 && !inStruct:
			tl := string(tagLocal) // zero-alloc compare (compiler optimisation)
			if ns == NSrdf && (tl == "Alt" || tl == "Seq" || tl == "Bag") {
				inColl = true
				*liVals = (*liVals)[:0]
			}

		// ── rdf:li inside a collection ────────────────────────────────────
		case inColl && depth == propDepth+2:
			// Capture xml:lang attribute for rdf:Alt items (P1-H).
			liLang = ""
			for _, a := range attrs {
				if a.loc == "lang" {
					liLang = a.val
					break
				}
			}
		}

		if selfClose {
			// Self-closing element: immediately apply EndElement logic.
			switch {
			case inStruct && structFieldDepth > 0 && depth == structFieldDepth:
				structFieldNS, structFieldLocal = "", ""
				structFieldDepth = 0
			case inStruct && depth == structDepth:
				inStruct = false
				structDepth = 0
			case depth == propDepth && propDepth > 0:
				if inColl && len(*liVals) > 0 {
					storeProperty(x, propNS, propLocal, strings.Join(*liVals, "\x1e"))
					*liVals = (*liVals)[:0]
				}
				inColl = false
				inStruct = false
				propNS, propLocal = "", ""
				propDepth = 0
			case depth == descDepth:
				descDepth = 0
			}
			depth--
			continue
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

		if len(text) == 0 {
			continue
		}

		s := unescapeXML(text)

		// ── CharData dispatch — mirrors rdf.go:176-218 ────────────────────
		switch {
		case inStruct && structFieldDepth > 0 && depth == structFieldDepth:
			// Text content of a struct field element (P1-G).
			// Store as "propLocal.fieldLocal" in the parent property namespace.
			// If the field is in a different namespace, use that namespace.
			fieldNS := structFieldNS
			if fieldNS == "" {
				fieldNS = propNS
			}
			key := propLocal + "." + structFieldLocal
			if x.Properties[fieldNS] == nil {
				x.Properties[fieldNS] = make(map[string]string)
			}
			if x.Properties[fieldNS][key] == "" {
				x.Properties[fieldNS][key] = s
			}

		case inColl && depth == propDepth+2:
			// Inside rdf:li (propDepth+1 = collection, propDepth+2 = li).
			// For rdf:Alt items, preserve non-default xml:lang as "lang|value" (P1-H).
			if liLang != "" && liLang != "x-default" {
				*liVals = append(*liVals, liLang+"|"+s)
			} else {
				*liVals = append(*liVals, s)
			}

		case propDepth > 0 && !inStruct && depth == propDepth:
			// Direct text content of a simple property (XMP Part 1 §C.2.3).
			if x.Properties[propNS] == nil {
				x.Properties[propNS] = make(map[string]string)
			}
			// Only store if not already set (e.g. by rdf:resource attribute).
			if x.Properties[propNS][propLocal] == "" {
				x.Properties[propNS][propLocal] = s
			}
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
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
			c == '>' || c == '/' || c == '=' {
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
		for pos < len(b) && (b[pos] == ' ' || b[pos] == '\t' || b[pos] == '\n' || b[pos] == '\r') {
			pos++
		}
		if pos >= len(b) || b[pos] == '>' || b[pos] == '/' {
			break
		}

		// Parse attribute name. scanName returns zero-copy byte slices.
		attrPrefix, attrLocal, p := scanName(b, pos)
		pos = p

		// Skip optional whitespace around '='.
		for pos < len(b) && (b[pos] == ' ' || b[pos] == '\t' || b[pos] == '\n' || b[pos] == '\r') {
			pos++
		}
		if pos >= len(b) || b[pos] != '=' {
			// Malformed: attribute without value — skip.
			continue
		}
		pos++ // skip '='

		// Skip optional whitespace before the quote.
		for pos < len(b) && (b[pos] == ' ' || b[pos] == '\t' || b[pos] == '\n' || b[pos] == '\r') {
			pos++
		}
		if pos >= len(b) {
			break
		}

		// Parse quoted attribute value.
		quote := b[pos]
		if quote != '"' && quote != '\'' {
			// Malformed: unquoted attribute value — skip to next whitespace.
			for pos < len(b) && b[pos] != ' ' && b[pos] != '\t' && b[pos] != '>' && b[pos] != '/' {
				pos++
			}
			continue
		}
		pos++ // skip opening quote
		valStart := pos
		for pos < len(b) && b[pos] != quote {
			pos++
		}
		val := unescapeXML(b[valStart:pos])
		if pos < len(b) {
			pos++ // skip closing quote
		}

		// Classify: xmlns declaration vs. regular attribute.
		// string(attrPrefix) == "xmlns" is a zero-alloc comparison (Go compiler).
		if string(attrPrefix) == "xmlns" {
			// xmlns:prefix="uri" — register namespace binding.
			// attrLocal is a zero-copy slice; no string conversion needed here.
			if nsCount < len(nsTable) {
				nsTable[nsCount] = nsEntry{prefix: attrLocal, uri: val}
				nsCount++
			}
		} else if string(attrLocal) == "xmlns" && len(attrPrefix) == 0 {
			// xmlns="uri" — default namespace declaration; ignore (XMP never uses it).
		} else {
			// Regular attribute: resolve its namespace and store.
			if nAttrs < len(out) {
				resolvedNS := resolveNS(nsTable[:nsCount], attrPrefix)
				out[nAttrs] = xmpAttr{ns: resolvedNS, loc: string(attrLocal), val: val}
				nAttrs++
			}
		}
	}
	return nsCount, nAttrs, pos
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
		return unsafe.String(unsafe.SliceData(b), len(b))
	}

	bld := builderPool.Get().(*strings.Builder)
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
			var r rune
			var ok bool
			if len(ref) > 2 && (ref[1] == 'x' || ref[1] == 'X') {
				r, ok = parseHex(ref[2:])
			} else {
				r, ok = parseDec(ref[1:])
			}
			if ok {
				bld.WriteRune(r)
			}
		default:
			// Unknown entity — emit the original reference.
			bld.WriteByte('&')
			bld.Write(ref)
			bld.WriteByte(';')
		}
	}

	s := bld.String()
	bld.Reset()
	builderPool.Put(bld)
	return s
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
	for start < len(b) && (b[start] == ' ' || b[start] == '\t' || b[start] == '\n' || b[start] == '\r') {
		start++
	}
	end := len(b)
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\n' || b[end-1] == '\r') {
		end--
	}
	return b[start:end]
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
