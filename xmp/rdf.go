package xmp

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// parseRDF walks the RDF graph rooted at the x:xmpmeta element and populates
// the Properties map in x. It handles rdf:Alt, rdf:Seq, and rdf:Bag
// collections by joining their rdf:li values with U+001E (record separator).
// rdf:Alt items preserve xml:lang as a "lang|value" prefix (P1-H).
// Struct properties (nested rdf:Description) are stored as "parent.child" keys
// within the parent property's namespace (P1-G).
//
// Compliance: ISO 16684-1:2019 §7, Adobe XMP Specification Part 1 §C.
func parseRDF(b []byte, x *XMP) error {
	dec := xml.NewDecoder(bytes.NewReader(b))

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
	// Accumulated rdf:li values for the current collection.
	var liValues []string
	// xml:lang of the current rdf:li element (P1-H, used for rdf:Alt).
	var liLang string

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("xmp: XML parse error: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			// Guard against pathological XML with deeply nested elements.
			if depth > 100 {
				return fmt.Errorf("xmp: XML nesting depth exceeded 100 levels")
			}

			switch {
			// ── Struct field element inside a struct node (P1-G) ──────────────
			case inStruct && depth == structDepth+1 && structFieldDepth == 0:
				structFieldNS = t.Name.Space
				structFieldLocal = t.Name.Local
				structFieldDepth = depth

			// ── rdf:Description: top-level block or struct value node ─────────
			case t.Name.Space == NSrdf && t.Name.Local == "Description":
				if propDepth > 0 && depth == propDepth+1 && !inStruct {
					// Struct value node: nested rdf:Description inside a property
					// element (XMP Part 1 §C.2.6). Store inline attributes as
					// "propLocal.fieldLocal" keys in the parent property's namespace.
					inStruct = true
					structDepth = depth
					for _, attr := range t.Attr {
						ns := attr.Name.Space
						if ns == "" || ns == NSrdf || ns == NSx {
							continue
						}
						key := propLocal + "." + attr.Name.Local
						if x.Properties[propNS] == nil {
							x.Properties[propNS] = make(map[string]string)
						}
						x.Properties[propNS][key] = attr.Value
					}
				} else if descDepth == 0 {
					// Top-level rdf:Description — begin a new property block.
					descDepth = depth
					// Shorthand: properties as inline attributes (XMP Part 1 §C.2.4).
					for _, attr := range t.Attr {
						ns := attr.Name.Space
						if ns == "" || ns == NSrdf || ns == NSx {
							continue
						}
						if x.Properties[ns] == nil {
							x.Properties[ns] = make(map[string]string)
						}
						x.Properties[ns][attr.Name.Local] = attr.Value
					}
				}

			// ── Property element: direct child of top-level rdf:Description ───
			case descDepth > 0 && depth == descDepth+1 && propDepth == 0:
				propNS = t.Name.Space
				propLocal = t.Name.Local
				propDepth = depth
				// rdf:resource shorthand (XMP Part 1 §C.2.5).
				for _, attr := range t.Attr {
					if attr.Name.Space == NSrdf && attr.Name.Local == "resource" {
						if x.Properties[propNS] == nil {
							x.Properties[propNS] = make(map[string]string)
						}
						x.Properties[propNS][propLocal] = attr.Value
					}
					// rdf:parseType="Resource" is equivalent to a nested rdf:Description
					// (XMP Part 1 §C.2.6). Mark as struct immediately.
					if attr.Name.Space == NSrdf && attr.Name.Local == "parseType" && attr.Value == "Resource" {
						inStruct = true
						structDepth = depth
					}
				}

			// ── Collection container: rdf:Alt / Seq / Bag ────────────────────
			case propDepth > 0 && depth == propDepth+1 && !inStruct:
				if t.Name.Space == NSrdf &&
					(t.Name.Local == "Alt" || t.Name.Local == "Seq" || t.Name.Local == "Bag") {
					inColl = true
					liValues = liValues[:0]
				}

			// ── rdf:li inside a collection ────────────────────────────────────
			case inColl && depth == propDepth+2:
				// Capture xml:lang attribute for rdf:Alt items (P1-H).
				liLang = ""
				for _, attr := range t.Attr {
					if attr.Name.Local == "lang" {
						liLang = attr.Value
						break
					}
				}
			}

		case xml.EndElement:
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
				if inColl && len(liValues) > 0 {
					if x.Properties[propNS] == nil {
						x.Properties[propNS] = make(map[string]string)
					}
					x.Properties[propNS][propLocal] = strings.Join(liValues, "\x1e")
					liValues = liValues[:0]
				}
				inColl = false
				inStruct = false
				propNS, propLocal = "", ""
				propDepth = 0

			case depth == descDepth:
				descDepth = 0
			}
			depth--

		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text == "" {
				continue
			}

			switch {
			case inStruct && structFieldDepth > 0 && depth == structFieldDepth:
				// Text content of a struct field element (P1-G).
				// Store as "propLocal.fieldLocal" in the parent property namespace.
				// If the field is in a different namespace, use that namespace.
				ns := structFieldNS
				if ns == "" {
					ns = propNS
				}
				key := propLocal + "." + structFieldLocal
				if x.Properties[ns] == nil {
					x.Properties[ns] = make(map[string]string)
				}
				if x.Properties[ns][key] == "" {
					x.Properties[ns][key] = text
				}

			case inColl && depth == propDepth+2:
				// Inside rdf:li (propDepth+1 = collection, propDepth+2 = li).
				// For rdf:Alt items, preserve non-default xml:lang as "lang|value" (P1-H).
				// The x-default lang tag is the canonical value; store it without prefix
				// to preserve backward compatibility with the existing Get() API.
				if liLang != "" && liLang != "x-default" {
					liValues = append(liValues, liLang+"|"+text)
				} else {
					liValues = append(liValues, text)
				}

			case propDepth > 0 && !inStruct && depth == propDepth:
				// Direct text content of a simple property (XMP Part 1 §C.2.3).
				if x.Properties[propNS] == nil {
					x.Properties[propNS] = make(map[string]string)
				}
				// Only store if not already set (e.g. by rdf:resource attribute).
				if x.Properties[propNS][propLocal] == "" {
					x.Properties[propNS][propLocal] = text
				}
			}
		}
	}

	return nil
}
