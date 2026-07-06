---
name: audit-findings-20260609-xmp
description: XMP package security + conformance audit 2026-06-09 — 4 findings: NS-URI injection (HIGH), local-name injection (HIGH), rdf:resource silent drop (MEDIUM), bare lang over-capture (LOW)
metadata:
  type: project
---

Audit date: 2026-06-09
Scope: xmp/ package only (all *.go and *_test.go)
Tooling: govulncheck PASS, go vet PASS, go test -race PASS, FuzzParseXMP 30s 0 crashers

## Confirmed Findings

### XMP-NS-URI-01 — HIGH
Namespace URI XML injection via Encode path.
- Location: write.go serialise() ~line 85: `buf.WriteString(ns)` (namespace URI written raw into xmlns attribute value)
- The namespace URI is written verbatim into `xmlns:nsN="<URI>"` with no XML escaping.
- A URI containing `"` terminates the attribute early and injects arbitrary XML into the rdf:Description start tag's attribute list. A URI with `<` injects element content.
- REACHABLE FROM UNTRUSTED INPUT: A crafted XMP file with `&quot;` (decoded to `"` by unescapeXML) in a namespace URI, when re-serialized via Encode, injects XML into the output. Confirmed by test.
- Fix: apply writeXMLEscaped (or an attribute-value-safe escape) to the ns URI in serialise().

### XMP-LOCAL-01 — HIGH
Property local name XML injection via Encode path.
- Location: write.go writeSimpleProperty, writeMultiValuedProperty, writeStructProperty, writeStructInListProperty — all write local names directly into element names with no validation.
- Local names go into `<prefix:local>` / `</prefix:local>` with no sanitization. A local name containing `<` or `>` breaks the element boundary.
- REACHABLE FROM UNTRUSTED INPUT: scanName (rdf.go) does NOT include `<` in isNameTerminator. So a crafted XMP element like `<dc:prop<inject>` produces local name `prop<inject` in storage. Encode then emits `<dc:prop<inject>...` which creates a spurious `<inject>` element.
- Fix: validate that local names are valid XML NCNames before writing; replace or skip names containing XML-illegal chars. Also fix isNameTerminator to include `<`.
- Note: value injection is blocked — writeXMLEscaped is used for all values. The gap is only local names and namespace URIs.

### XMP-RDFRESOURCE-01 — MEDIUM
rdf:resource attribute on property elements silently dropped (spec deviation RDF-03/RDF-04).
- Location: rdf.go applyAttrShorthands() line 285: condition `p.propDepth > 0` is false at the time applyAttrShorthands runs for a property element, because onStartProperty (which sets propDepth) is dispatched AFTER applyAttrShorthands in onStartElement.
- Properties like `<xmpMM:DerivedFrom rdf:resource="xmp.did:ABC"/>` store "" instead of "xmp.did:ABC". This is a silent data loss.
- Spec: XMP Part 1 §C.2.5 — rdf:resource on a property element specifies a URI as the property value.
- Fix: move rdf:resource handling to after onStartProperty is dispatched, OR handle it in onStartProperty when attrs are available.

### XMP-BARE-LANG-01 — LOW
Bare unqualified `lang` attribute on rdf:li treated as xml:lang (over-capture).
- Location: rdf.go onStartListItem() line 262: condition `a.ns == "" || a.ns == "http://www.w3.org/XML/1998/namespace"` accepts ns="" (unqualified attribute) as xml:lang.
- XML Namespaces §6.1: xml:lang is ONLY the attribute `lang` in the XML namespace `http://www.w3.org/XML/1998/namespace`. An unqualified `lang` attribute (no prefix, ns="") is NOT xml:lang.
- A crafted `<rdf:li lang="en">keyword</rdf:li>` stores "en|keyword" instead of "keyword".
- Real-world impact is low: non-spec-compliant XMP producers may emit bare lang attributes; over-capture produces wrong lang prefix rather than crash. But it deviates from spec (NS-01).
- Fix: change condition to `a.ns == "http://www.w3.org/XML/1998/namespace"` only.
- Note: issue #80 fixed foreign-namespace lang (ex:lang) but did not fix the bare ns="" case.

## Items Verified Clean
- XXE / entity expansion: BLOCKED — skipBang discards DOCTYPE; entities never expanded from DOCTYPE internal subset.
- DOCTYPE internal subset partial skip (first '>' found): leaves `]>` as stray bytes but outer loop handles them gracefully; parsing continues correctly.
- Value injection: BLOCKED — writeXMLEscaped applied to all property values.
- Namespace prefix injection: BLOCKED — prefixes are always from prefixMap (safe ASCII) or generated "ns0/ns1/..." (safe).
- rdf:about injection: BLOCKED — hardcoded as "".
- UnescapeXML pool safety after truncation: SAFE.
- builderPool reuse safety: SAFE.
- govulncheck: PASS.
- Race detector: PASS.
- FuzzParseXMP: 30s, 0 crashers.
- ROB-04 (unknown namespace round-trip): PASS.
- ROB-05 (duplicate property first-wins): PASS.
- RDF-06 (x-default last): PASS.
- NS-02 (arbitrary prefix): PASS.
- PW-07 (wrapper optional): PASS.
- Nesting depth cap: PASS.
- All existing security tests: PASS.

## VT-08 Note (additional vector)
Namespace URI also bypasses VT-08 (forbidden C0 chars): a URI with NUL or other forbidden C0 chars reaches the XML output unescaped. This is a consequence of the same root cause as XMP-NS-URI-01 (no escaping of namespace URI in serialise()).
**Why:** fixes to both are the same: apply writeXMLEscaped to the namespace URI in serialise().
