---
name: project-xmp-data-model
description: XMP package internal data model conventions for property key encoding (struct fields, struct-in-array)
metadata:
  type: project
---

XMP properties are stored as `Properties[nsURI][localKey] = value`. The `localKey` encoding conventions are:

- **Simple property**: `"Model"` — plain local name
- **Multi-valued property**: `"nature\x1elandscape"` — U+001E-joined items
- **Struct field** (rdf:parseType="Resource"): `"CreatorContactInfo.CiEmailWork"` — "parent.field" dotted key, stored in the parent property's namespace URI
- **Struct-in-list-item** (rdf:Seq/Bag of structs, e.g. xmpMM:History): `"History[0].action"` — 0-based bracket index, stored in the property's namespace URI

The serialiser (`write.go` `classifyProps`) partitions keys by these patterns before dispatching to `writeSimpleProperty`, `writeStructProperty`, or `writeStructInListProperty`. This ensures valid XML (no dots in element names). Round-trip is tested by `TestStructInArrayRoundTrip` and `TestStructPropertyRoundTrip`.

**Why:** XMP Part 1 §C.2.5 / §C.2.6 require struct values to use `rdf:parseType="Resource"` wrapper. Dots in element names are illegal XML 1.0.

**How to apply:** When adding new accessor methods for structured XMP properties, use `x.Get(ns, "parent.field")` for simple structs and `x.Get(ns, "parent[N].field")` for array-of-struct items.

**Since Sprint 45 E4a (2026-10-14):** these keys are a projection of a parsed
data-model tree. Anything deeper (struct in struct field, arrays in structs,
qualifiers, URI values, empty arrays, rdf:nodeID) has no key and is kept in the
unexported `XMP.nodes`; Encode writes unmodified retained properties verbatim and
merges edits of their keys. Composed keys are subject to a size budget (#277).
See [[project_sprint45_e4a_xmp_tree]].
