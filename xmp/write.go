package xmp

import (
	"bytes"
	"slices"
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
func serialise(x *XMP) ([]byte, error) {
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

	for _, ns := range nsList {
		props := x.Properties[ns]
		prefix := prefixOf(ns)
		buf.WriteString("  <rdf:Description rdf:about=\"\" xmlns:")
		buf.WriteString(prefix)
		buf.WriteString("=\"")
		buf.WriteString(ns)
		buf.WriteString("\">\n")

		// Sort property names for deterministic output.
		localListPtr := localListPool.Get().(*[]string) //nolint:forcetypeassert,revive // localListPool.New always stores *[]string; pool invariant
		localList := (*localListPtr)[:0]
		for local := range props {
			localList = append(localList, local)
		}
		slices.Sort(localList)
		*localListPtr = localList

		for _, local := range localList {
			val := props[local]
			// Fast path: most properties are single-valued — avoid strings.Split alloc.
			if strings.IndexByte(val, '\x1e') < 0 {
				writeSimpleProperty(buf, prefix, local, val)
			} else {
				writeMultiValuedProperty(buf, prefix, ns, local, val)
			}
		}

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
func writeSimpleProperty(buf *bytes.Buffer, prefix, local, val string) {
	buf.WriteString("   <")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	buf.WriteString(local)
	buf.WriteByte('>')
	writeXMLEscaped(buf, val)
	buf.WriteString("</")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	buf.WriteString(local)
	buf.WriteString(">\n")
}

// writeMultiValuedProperty writes a multi-valued XMP property element to buf.
// val is a '\x1e'-delimited list of values. The RDF collection type (Alt, Seq,
// or Bag) is determined by collectionType(ns, local) per ISO 16684-1 §7.5.
// For Alt collections, items may carry an xml:lang prefix encoded as "lang|value".
func writeMultiValuedProperty(buf *bytes.Buffer, prefix, ns, local, val string) {
	ctype := collectionType(ns, local)
	buf.WriteString("   <")
	buf.WriteString(prefix)
	buf.WriteByte(':')
	buf.WriteString(local)
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
	buf.WriteString(local)
	buf.WriteString(">\n")
}

// writeXMLEscaped writes s to buf with XML character escaping, operating
// directly on the string to avoid the []byte(s) conversion that
// encoding/xml.EscapeText requires.  Handles the five predefined XML
// entities plus the CR character (XML 1.0 §2.2 and §4.6).
func writeXMLEscaped(buf *bytes.Buffer, s string) {
	last := 0
	for i := range len(s) {
		var esc string
		switch s[i] {
		case '&':
			esc = "&amp;"
		case '<':
			esc = "&lt;"
		case '>':
			esc = "&gt;"
		case '"':
			esc = "&#34;"
		case '\'':
			esc = "&#39;"
		case '\r':
			esc = "&#xD;"
		default:
			continue
		}
		buf.WriteString(s[last:i])
		buf.WriteString(esc)
		last = i + 1
	}
	buf.WriteString(s[last:])
}
