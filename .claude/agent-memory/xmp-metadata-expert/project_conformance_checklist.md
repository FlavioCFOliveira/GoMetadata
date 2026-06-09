---
name: xmp-conformance-checklist
description: Exhaustive XMP normative-requirements checklist produced 2026-06-09 for 100%-compliance test battery; covers all spec identifiers, packet wrapper, RDF model, namespaces, value types, embedding rules, MWG reconciliation, and robustness cases
type: project
---

Conformance checklist produced 2026-06-09 for the GoMetadata XMP conformance test battery.
Covers 5 sections, 50+ individual rules, each with spec citation and fixture note.

## Scope

- Spec identifiers: ISO 16684-1:2019, ISO 16684-2:2014, Adobe XMP Parts 1/2/3
- Packet wrapper: rules PW-01 through PW-07 (begin PI, BOM, id attribute, end PI, padding, scanning, optional wrapper)
- RDF/XML model: rules RDF-01 through RDF-08 (document structure, rdf:about, arrays, xml:lang, x-default, qualifiers, structs, scalars)
- Namespaces: rules NS-01 through NS-04 (URI canonicity, prefix arbitrariness, full registry table, DC property types)
- Value types: rules VT-01 through VT-08 (Text, Integer, Real, Boolean, Date/6 formats, URI, URL, GUID)
- JPEG embedding: rules JPEG-01 through JPEG-08 (APP1 identifier, 65533-byte limit, one-per-file, ExtendedXMP identifier 35 bytes, ExtendedXMP header 40 bytes, GUID MD5 validation, chunk reassembly validation, merge strategy)
- TIFF embedding: rules TIFF-01 through TIFF-03 (tag 700, BYTE/UNDEFINED type, no size limit, no wrapper required)
- PNG embedding: rules PNG-01 through PNG-05 (iTXt keyword exact, no compression, end="r" required, one-per-file, empty lang/translated keyword)
- HEIF/ISO BMFF embedding: rules HEIF-01 through HEIF-03 (item_type='mime', content_type='application/rdf+xml', cdsc reference, no wrapper required)
- MWG reconciliation: rules MWG-01 through MWG-09 (priority, IPTC digest, description/creator/copyright/DateTimeOriginal/keywords/GPS field mappings, write synchronisation)
- Robustness: rules ROB-01 through ROB-12 (missing end PI, malformed RDF, XXE/billion-laughs, unknown NS, duplicate properties, arbitrary offset scanning, ExtendedXMP missing/duplicate chunks, non-UTF-8 encoding, illegal C0 chars, NS prefix collision on output, deep nesting)

## Key namespace URIs confirmed
- dc: http://purl.org/dc/elements/1.1/
- xmp: http://ns.adobe.com/xap/1.0/
- xmpRights: http://ns.adobe.com/xap/1.0/rights/
- xmpMM: http://ns.adobe.com/xap/1.0/mm/
- xmpBJ: http://ns.adobe.com/xap/1.0/bj/
- xmpTPg: http://ns.adobe.com/xap/1.0/t/pg/
- xmpDM: http://ns.adobe.com/xmp/1.0/DynamicMedia/
- xmpNote: http://ns.adobe.com/xmp/note/
- photoshop: http://ns.adobe.com/photoshop/1.0/
- crs: http://ns.adobe.com/camera-raw-settings/1.0/
- tiff: http://ns.adobe.com/tiff/1.0/
- exif: http://ns.adobe.com/exif/1.0/
- aux: http://ns.adobe.com/exif/1.0/aux/
- Iptc4xmpCore: http://iptc.org/std/Iptc4xmpCore/1.0/xmlns/
- Iptc4xmpExt: http://iptc.org/std/Iptc4xmpExt/2008-02-29/
- x (xmpmeta wrapper): adobe:ns:meta/

**Why:** Conformance test battery contract for GoMetadata library.
**How to apply:** Each rule is a test case. Rules map directly to table-driven Go conformance tests.
The go-performance-architect translates rules into tests; this agent confirms spec accuracy.
