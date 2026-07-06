---
name: xml-namespace-prepopulation
description: The rdfParser must pre-populate the xml prefix→XMLNamespaceURI mapping at init; without it xml:lang resolves to empty string, breaking all rdf:Alt/lang handling
metadata:
  type: feedback
---

Pre-populate entry 0 of the nsTable with `nsEntry{prefix: xmlPrefixBytes, uri: XMLNamespaceURI}` and set `nsCount=1` before the parse loop. This is required because XML Namespaces §3 permanently pre-binds the `xml` prefix; without it, `xml:lang` on `rdf:li` elements resolves to `""` and no xml:lang handler fires. All nsDepth scope tracking naturally preserves this entry since depth starts at 1.

**Why:** Discovered during finding #180 fix: changing `onStartListItem` to require `a.ns == XMLNamespaceURI` broke all rdf:Alt tests because the ns resolved to `""`.

**How to apply:** Any time you modify rdfParser initialization, ensure `p.nsTable[0] = nsEntry{prefix: xmlPrefixBytes, uri: XMLNamespaceURI}` and `p.nsCount = 1` are set before the parse loop. Related: [[feedback-xmp-parser-lenient]].
