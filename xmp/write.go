package xmp

import (
	"bytes"
	"encoding/xml"
	"sort"
	"strings"
)

// xmpPadding is the pre-computed 2 KB whitespace padding block for XMP in-place
// editing (XMP §7.3). Initialised once at package load; never mutated.
var xmpPadding = func() [2048]byte {
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

// encode serialises x to a padded XMP packet.
// The packet uses UTF-8 encoding and a read/write <?xpacket?> wrapper
// with 2 KB of whitespace padding per XMP §7.3 (in-place editing support).
func encode(x *XMP) ([]byte, error) {
	var buf bytes.Buffer

	// Opening packet wrapper with UTF-8 BOM marker (XMP §7.1).
	buf.WriteString("<?xpacket begin=\"\xef\xbb\xbf\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n")
	buf.WriteString("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\">\n")
	buf.WriteString(" <rdf:RDF xmlns:rdf=\"http://www.w3.org/1999/02/22-rdf-syntax-ns#\">\n")

	// Sort namespace URIs for deterministic output (ISO 16684-1 §7.4).
	nsList := make([]string, 0, len(x.Properties))
	for ns, props := range x.Properties {
		if len(props) > 0 {
			nsList = append(nsList, ns)
		}
	}
	sort.Strings(nsList)

	for _, ns := range nsList {
		props := x.Properties[ns]
		prefix := prefixOf(ns)
		buf.WriteString("  <rdf:Description rdf:about=\"\" xmlns:")
		buf.WriteString(prefix)
		buf.WriteString("=\"")
		buf.WriteString(ns)
		buf.WriteString("\">\n")

		// Sort property names for deterministic output.
		localList := make([]string, 0, len(props))
		for local := range props {
			localList = append(localList, local)
		}
		sort.Strings(localList)

		for _, local := range localList {
			val := props[local]
			// Fast path: most properties are single-valued — avoid strings.Split alloc.
			if strings.IndexByte(val, '\x1e') < 0 {
				// Simple property.
				buf.WriteString("   <")
				buf.WriteString(prefix)
				buf.WriteByte(':')
				buf.WriteString(local)
				buf.WriteByte('>')
				xml.EscapeText(&buf, []byte(val)) //nolint:errcheck
				buf.WriteString("</")
				buf.WriteString(prefix)
				buf.WriteByte(':')
				buf.WriteString(local)
				buf.WriteString(">\n")
			} else {
				// Multi-valued: use the per-property collection type (ISO 16684-1 §7.5).
				values := strings.Split(val, "\x1e")
				ctype := collectionType(ns, local)
				buf.WriteString("   <")
				buf.WriteString(prefix)
				buf.WriteByte(':')
				buf.WriteString(local)
				buf.WriteString(">\n    <rdf:")
				buf.WriteString(ctype)
				buf.WriteString(">\n")
				for _, v := range values {
					if ctype == "Alt" {
						// Preserve xml:lang if stored as "lang|value" (P1-H).
						lang, val, hasLang := strings.Cut(v, "|")
						if hasLang {
							buf.WriteString("     <rdf:li xml:lang=\"")
							xml.EscapeText(&buf, []byte(lang)) //nolint:errcheck
							buf.WriteString("\">")
							xml.EscapeText(&buf, []byte(val)) //nolint:errcheck
						} else {
							buf.WriteString("     <rdf:li xml:lang=\"x-default\">")
							xml.EscapeText(&buf, []byte(v)) //nolint:errcheck
						}
					} else {
						buf.WriteString("     <rdf:li>")
						xml.EscapeText(&buf, []byte(v)) //nolint:errcheck
					}
					buf.WriteString("</rdf:li>\n")
				}
				buf.WriteString("    </rdf:")
				buf.WriteString(ctype)
				buf.WriteString(">\n   </")
				buf.WriteString(prefix)
				buf.WriteByte(':')
				buf.WriteString(local)
				buf.WriteString(">\n")
			}
		}

		buf.WriteString("  </rdf:Description>\n")
	}

	buf.WriteString(" </rdf:RDF>\n</x:xmpmeta>\n")

	// 2 KB padding of spaces / newlines for in-place editing (XMP §7.3).
	// Uses the pre-computed package-level array to avoid a per-call allocation.
	buf.Write(xmpPadding[:])
	buf.WriteString("\n<?xpacket end=\"w\"?>")

	return buf.Bytes(), nil
}
