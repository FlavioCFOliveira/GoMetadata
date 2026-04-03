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
//
// Compliance: ISO 16684-1:2019 §7, Adobe XMP Specification Part 1 §C.
func parseRDF(b []byte, x *XMP) error {
	dec := xml.NewDecoder(bytes.NewReader(b))

	// Depth tracking (absolute element nesting level, 1-based).
	var depth int
	// Depth at which the current rdf:Description was opened.
	var descDepth int
	// Depth at which the current property element was opened.
	var propDepth int
	// Namespace and local name of the current property element.
	var propNS, propLocal string
	// True when inside an rdf:Alt / rdf:Seq / rdf:Bag.
	var inColl bool
	// Accumulated rdf:li values for the current collection.
	var liValues []string

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

			switch {
			case t.Name.Space == NSrdf && t.Name.Local == "Description":
				// rdf:Description — begin a new property block.
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

			case descDepth > 0 && depth == descDepth+1 && propDepth == 0:
				// Direct child of rdf:Description → property element.
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
				}

			case propDepth > 0 && depth == propDepth+1:
				// Child of a property element: may be rdf:Alt / Seq / Bag.
				if t.Name.Space == NSrdf &&
					(t.Name.Local == "Alt" || t.Name.Local == "Seq" || t.Name.Local == "Bag") {
					inColl = true
					liValues = liValues[:0]
				}
			}

		case xml.EndElement:
			switch {
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
				propNS, propLocal = "", ""
				propDepth = 0

			case depth == descDepth:
				descDepth = 0
			}
			depth--

		case xml.CharData:
			text := strings.TrimSpace(string(t))
			if text == "" || propDepth == 0 {
				continue
			}

			if inColl && depth == propDepth+2 {
				// Inside rdf:li (propDepth+1 = collection, propDepth+2 = li).
				liValues = append(liValues, text)
			} else if depth == propDepth {
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
