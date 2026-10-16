package xmp

import (
	"bytes"
	"encoding/binary"
	"strings"
	"sync"
	"unicode"
	"unsafe"
)

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
	// The first-byte test rejects almost every tag without a memequal call.
	return pos < len(b) && b[pos] == '!' && bytes.HasPrefix(b[pos:], cdataOpen)
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

// collectTextContent gathers the text content of an element starting at b[pos],
// handling interleaved CDATA sections per XML 1.0 §2.7.
//
// The function concatenates:
//   - Raw text segments (entity-unescaped via unescapeXML)
//   - CDATA section content (delivered as-is — no entity expansion inside CDATA)
//
// The text is verbatim: white space is not trimmed (XML 1.0 §2.10), and line
// ends are normalised (XML 1.0 §2.11, see normaliseEOL).
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
		s := unescapeXML(normaliseEOL(b[pos:]))
		return len(b), s
	}

	// There is a '<' ahead.  Peek at what follows it.
	ahead := pos + textEnd + 1 // position after '<'
	if ahead >= len(b) || !isCDATA(b, ahead) {
		// No CDATA involved — fast path: return text before '<', leave pos at '<'.
		s := unescapeXML(normaliseEOL(b[pos : pos+textEnd]))
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
			bld.WriteString(unescapeXML(normaliseEOL(b[pos:])))
			pos = len(b)
			break
		}

		// Append text segment before '<'.
		seg := b[pos : pos+nextTag]
		if len(seg) > 0 {
			bld.WriteString(unescapeXML(normaliseEOL(seg)))
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
				bld.Write(normaliseEOL(content))
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

// nameTerminatorLUT is a 256-entry lookup table indexed by byte value,
// true for every byte that terminates an XML name token in the context of
// attribute/tag parsing: ' ', '\t', '\n', '\r', '>', '/', '=', '<'.
//
// #287: scanName/isNameTerminator was measured at 28.8% of Read's self time
// on a representative Canon 7D JPEG (dominated by attribute/tag name
// scanning in scanAttrs). Replacing the eight-way branch chain with a single
// array index turns per-byte dispatch into a predictable, branch-free memory
// load, which the CPU can pipeline far more effectively than a chain of
// data-dependent comparisons evaluated in sequence for every byte of every
// name in the document.
//
// XML 1.0 §2.3 (NameStartChar, NameChar): '<' is not a legal XML name character.
// Including it as a name terminator prevents a crafted document from smuggling
// '<' into a stored local name, which would allow XML injection when that name
// is later emitted unescaped in Encode (#171).
var nameTerminatorLUT = [256]bool{ //nolint:gochecknoglobals // read-only table; global avoids per-call allocation
	' ':  true,
	'\t': true,
	'\n': true,
	'\r': true,
	'>':  true,
	'/':  true,
	'=':  true,
	'<':  true,
}

// isNameTerminator reports whether c is a byte that terminates an XML name
// token in the context of attribute/tag parsing. See nameTerminatorLUT for
// the exact set of terminators and the rationale for the table form.
func isNameTerminator(c byte) bool { return nameTerminatorLUT[c] }

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
	if i := bytes.IndexByte(b[pos:], quote); i >= 0 {
		pos += i
	} else {
		pos = len(b)
	}
	val = unescapeAttrValue(b[valStart:pos])
	if pos < len(b) {
		pos++ // skip closing quote
	}
	return val, pos
}

// unescapeAttrValue returns the value of an attribute whose literal text is b:
// normalised as XML 1.0 §3.3.3 requires (see normaliseAttrValue) and then
// unescaped like any character data (see unescapeXML). A value with no
// '&', TAB, LF or CR, the usual case, is returned as a transient alias of b.
func unescapeAttrValue(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if indexAttrSpace(b) < 0 {
		return unescapeXML(b)
	}
	return unescapeXML(normaliseAttrValue(b))
}

// normaliseAttrValue applies the attribute-value normalisation of XML 1.0
// §3.3.3 to the literal text b of an attribute value, before entity and
// character references are expanded: every literal TAB (#x9), LF (#xA) and CR
// (#xD) becomes one space (#x20), and the two characters of a CR LF pair
// become one space, because §2.11 has already reduced the pair to a single
// LF when the processor hands the text to the normaliser. Attributes of
// type CDATA (all attributes of an XMP packet: it has no DTD, so none is
// declared with another type) receive no further normalisation. A character
// reference to one of the three characters (&#9; &#10; &#13;) is expanded
// later and so stays that character. It returns b itself, without copying,
// when b holds none of the three characters, which is the common case.
func normaliseAttrValue(b []byte) []byte {
	i := indexAttrSpace(b)
	if i < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	out = append(out, b[:i]...)
	for ; i < len(b); i++ {
		c := b[i]
		if c == '\r' && i+1 < len(b) && b[i+1] == '\n' {
			i++ // CR LF is one line end (XML 1.0 §2.11), so one space
		}
		if c == '\t' || c == '\n' || c == '\r' {
			c = ' '
		}
		out = append(out, c)
	}
	return out
}

// indexAttrSpace returns the index of the first TAB, LF or CR of b, or -1. It
// first tests eight bytes at a time whether any byte is below 0x0E, the exact
// "has a byte less than n" test of Hacker's Delight §6-1, which holds for TAB,
// LF and CR and for the C0 controls that XML forbids in a document; the usual
// attribute value has none, and costs one test per eight bytes (task #344).
// A word with such a byte sends the rest of b to the byte loop.
func indexAttrSpace(b []byte) int {
	const ones, highs = 0x0101010101010101, 0x8080808080808080
	i := 0
	for ; i+8 <= len(b); i += 8 {
		w := binary.LittleEndian.Uint64(b[i:])
		if (w-ones*0x0E)&^w&highs != 0 {
			break
		}
	}
	for ; i < len(b); i++ {
		if c := b[i]; c == '\t' || c == '\n' || c == '\r' {
			return i
		}
	}
	return -1
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

// transientAliasString returns a string sharing b's backing array, via
// unsafe.String, instead of copying.
//
// CONTRACT: b MUST be a sub-slice of the transient input/normalised buffer
// that Parse passes to parseRDF for THIS parse call — i.e. this function
// must only ever be called from within parseRDF or one of its helpers
// (scanXML, the RDF parser, and the projection step). The
// returned string is safe to use ONLY for the remainder of that same
// synchronous Parse call: it must either be discarded before Parse returns,
// or copied into XMP.arena via (*XMP).intern before Parse returns, if it
// needs to survive. Parse itself never retains a reference to the buffer
// this function aliases — see the XMP.arena field doc (xmp.go) — so by the
// time Parse returns, any NOT-interned transient alias has already become
// unreachable garbage; nothing further needs to happen for it to be safe
// against the caller mutating/recycling its own input buffer (bug #72).
//
// Do not call this function with a []byte of unknown provenance (e.g. a
// slice the caller might still be able to reach/mutate concurrently) — the
// "transient, synchronous-call-only" contract above is what makes this safe,
// not any property of b itself.
func transientAliasString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b)) //nolint:gosec // G103: b is always a sub-slice of the transient, synchronous-parse-only input buffer (see contract doc above); the result is used only within the same Parse call and, if it needs to survive, is copied into XMP.arena via intern() before Parse returns
}

// transientAliasBytes is the inverse view of transientAliasString: it returns
// the bytes of s without copying. The result must not be modified.
func transientAliasBytes(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s)) //nolint:gosec // G103: read-only view of s, used only for byte inspection within the same call
}

// intern copies s into x.arena (growing it if necessary) and returns an
// unsafe.String view into the newly appended region — a permanent,
// independently-owned, arena-backed string safe to store in Properties or
// in the retained tree for the lifetime of x.
//
// Parse calls it exactly at the point a value or key is decided to be kept
// (see project.go), so arena only ever grows by the bytes actually stored —
// comments, whitespace, ignored attribute values, and namespace declarations
// that are never referenced never touch arena at all, regardless of how large
// they are in the source document.
//
// Append-only growth safety: appending beyond arena's current capacity makes
// Go allocate a NEW backing array and copy the OLD content into it; the OLD
// backing array is never written to again. Any string returned by an EARLIER
// intern() call still points at that OLD array — which remains valid and
// unchanged forever — so it stays correct regardless of how many later
// intern() calls grow arena onto a new backing array. Worst-case total
// memory retained across all of arena's historical backing arrays (kept
// alive only by whichever intern()-returned strings still reference them) is
// bounded to approximately 2x the final stored-content size: standard slice
// growth (geometric, ~2x per regrowth) means the sum of all superseded
// backing arrays' sizes is itself a geometric series dominated by the final
// size.
func (x *XMP) intern(s string) string {
	if s == "" {
		return ""
	}
	start := len(x.arena)
	x.arena = append(x.arena, s...)
	return unsafe.String(unsafe.SliceData(x.arena[start:]), len(s)) //nolint:gosec // G103: x.arena[start:] is the region just appended by this call (bytes [start:start+len(s)]), always in bounds; the resulting string is backed by XMP.arena, which is never pooled and never mutated after being appended to (see XMP.arena's field doc and this function's own append-only-growth-safety doc above)
}

// unescapeXML converts b to a string, replacing the five predefined XML
// entities and numeric character references. When b contains no '&', it
// aliases b directly via transientAliasString (zero allocation) rather than
// copying: the fast path returns a TRANSIENT alias (see
// transientAliasString's contract) that is safe only within the current
// synchronous Parse call; the caller (the projection and retention steps in
// project.go) is responsible for calling (*XMP).intern on any value it
// actually decides to keep. Values that are
// computed here but end up discarded (dropped attributes, stray
// non-property text) are never copied anywhere and become garbage the
// instant they go out of scope — costing nothing.
func unescapeXML(b []byte) string {
	if bytes.IndexByte(b, '&') < 0 {
		// Fast path: no XML entities present. Alias b directly (see
		// transientAliasString's contract) instead of copying — safe because
		// this value is either discarded or interned into XMP.arena before
		// Parse returns.
		return transientAliasString(b)
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
// Returns true and writes a rune if the reference is a valid numeric
// character reference syntax; returns false (writing nothing) only when
// parseHex/parseDec reject the digits outright (surrogate or > U+10FFFF).
//
// Task #274 / XML 1.0 §2.2, §4.1: a numeric character reference is only
// well-formed when its referent matches the production
// Char ::= #x9 | #xA | #xD | [#x20-#xD7FF] | [#xE000-#xFFFD] | [#x10000-#x10FFFF].
// parseHex/parseDec already reject surrogates and values above U+10FFFF (a
// Unicode-scalar-value check), but a value CAN be a legal Unicode scalar
// value and still fall in one of the §2.2 forbidden ranges — the C0 control
// block (except the three whitespace exceptions #x9/#xA/#xD) and the two
// BMP non-characters U+FFFE/U+FFFF. Admitting such a code point verbatim is
// doubly wrong: it accepts ill-formed XML, AND — for U+001E specifically —
// it hands a caller this library's INTERNAL multi-value sentinel byte (see
// the '\x1e' dispatch in write.go's Encode), which would later cause a
// genuinely scalar/Simple property to be spuriously re-serialised as an
// rdf:Bag. isForbiddenXMLCharRef guards against the whole forbidden set and
// substitutes U+FFFD — the identical replacement policy writeXMLEscaped
// already applies on the output side (xmp/write.go) for the same set — so a
// forbidden code point can never enter a parsed property value regardless of
// how the source document spelled it.
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
	if !ok {
		return false
	}
	if isForbiddenXMLCharRef(r) {
		r = '�' // REPLACEMENT CHARACTER; Unicode §5.22 "Best Practice for U+FFFD Substitution".
	}
	bld.WriteRune(r)
	return true
}

// isForbiddenXMLCharRef reports whether r — already confirmed by the caller
// (decodeCharRef) to be a valid Unicode scalar value that is not a
// surrogate — is nonetheless outside the XML 1.0 §2.2 Char production and
// therefore MUST NOT appear in a well-formed XML document:
//
//	Char ::= #x9 | #xA | #xD | [#x20-#xD7FF] | [#xE000-#xFFFD] | [#x10000-#x10FFFF]
//
// Equivalently, r is forbidden when it falls in the C0 control block
// (#x0-#x1F) other than the three whitespace exceptions TAB/LF/CR, or is one
// of the two BMP non-characters U+FFFE/U+FFFF. This mirrors, byte-for-byte,
// the forbidden set writeXMLEscaped (xmp/write.go) already replaces with
// U+FFFD on the output side, so the parser and serialiser apply one
// consistent policy in both directions.
func isForbiddenXMLCharRef(r rune) bool {
	switch {
	case r <= 0x08:
		return true
	case r == 0x0B || r == 0x0C:
		return true
	case r >= 0x0E && r <= 0x1F:
		return true
	case r == 0xFFFE || r == 0xFFFF:
		return true
	default:
		return false
	}
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
// XML 1.0 §4.1 / Unicode §3.9: a numeric character reference must be a
// Unicode scalar value (≤ U+10FFFF, not a surrogate U+D800–U+DFFF). A
// reference outside this range is rejected by returning (0, false); the
// caller (decodeCharRef) then returns false without writing anything, so
// decodeEntity silently drops the reference — the "&ref; literal" fallback
// only applies to unrecognised NAMED entities (decodeNamedEntity), not
// out-of-range numeric ones. This mirrors the guard in decodeUTF32
// (encoding.go). Note: a value that PASSES this check is a valid Unicode
// scalar value but may still be an XML 1.0 §2.2 forbidden Char (e.g. a C0
// control) — that narrower check is decodeCharRef's isForbiddenXMLCharRef,
// applied after this function returns ok=true.
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
// XML 1.0 §4.1 / Unicode §3.9: a numeric character reference must be a
// Unicode scalar value (≤ U+10FFFF, not a surrogate U+D800–U+DFFF). A
// reference outside this range is rejected by returning (0, false); the
// caller (decodeCharRef) then returns false without writing anything, so
// decodeEntity silently drops the reference — the "&ref; literal" fallback
// only applies to unrecognised NAMED entities (decodeNamedEntity), not
// out-of-range numeric ones. This mirrors the guard in decodeUTF32
// (encoding.go). Note: a value that PASSES this check is a valid Unicode
// scalar value but may still be an XML 1.0 §2.2 forbidden Char (e.g. a C0
// control) — that narrower check is decodeCharRef's isForbiddenXMLCharRef,
// applied after this function returns ok=true.
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

// normaliseEOL returns b with every line end normalised to a single U+000A,
// as XML 1.0 §2.11 requires of the processor before it hands character data
// to the application: "\r\n" and a lone "\r" both become "\n". It returns b
// itself, without copying, when b holds no '\r', which is the common case. A
// character reference such as &#13; is decoded later and stays a carriage
// return (XML 1.0 §2.11).
func normaliseEOL(b []byte) []byte {
	i := bytes.IndexByte(b, '\r')
	if i < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	out = append(out, b[:i]...)
	for ; i < len(b); i++ {
		if b[i] != '\r' {
			out = append(out, b[i])
			continue
		}
		out = append(out, '\n')
		if i+1 < len(b) && b[i+1] == '\n' {
			i++
		}
	}
	return out
}

// isASCIISpace reports whether b is an ASCII whitespace character.
func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
