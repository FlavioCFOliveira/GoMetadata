---
name: xml-namespace-prepopulation
description: The rdfParser must pre-populate the xml prefix→XMLNamespaceURI mapping at init; without it xml:lang resolves to empty string, breaking all rdf:Alt/lang handling
metadata:
  type: feedback
---

Pre-populate the namespace scope with the `xml` → XMLNamespaceURI binding before scanning (since Sprint 45 E4a: `xmlDoc.reset` in xmp/xmlscan.go appends `nsBinding{prefix: xmlPrefixBytes, uri: XMLNamespaceURI}`; the old nsTable/nsCount are gone). This is required because XML Namespaces §3 permanently pre-binds the `xml` prefix; without it, `xml:lang` on `rdf:li` elements resolves to `""` and no xml:lang handler fires. All nsDepth scope tracking naturally preserves this entry since depth starts at 1.

**Why:** Discovered during finding #180 fix: changing `onStartListItem` to require `a.ns == XMLNamespaceURI` broke all rdf:Alt tests because the ns resolved to `""`.

**How to apply:** Any time you modify the scanner's scope initialization (`xmlDoc.reset`), keep the xml binding as the first scope entry. Related: [[feedback-xmp-parser-lenient]].
