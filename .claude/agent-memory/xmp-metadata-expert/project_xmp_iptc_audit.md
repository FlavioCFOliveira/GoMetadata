---
name: XMP/IPTC Spec Audit — 2026-04-03
description: Detailed spec compliance audit of xmp/ and iptc/ packages; scores and specific defects documented
type: project
---

Audit conducted against XMP ISO 16684-1 and IPTC IIM 4.2.

## XMP Score: 7/10

### Confirmed correct
- Packet scanning: `<?xpacket begin=` + `<?xpacket end=` markers; no premature `?>` termination
- BOM embedded as attribute value in `begin=` field
- `id="W5M0MpCehiHzreSzNTczkc9d"` magic ID present
- `rdf:Alt/Seq/Bag` with `rdf:li` accumulation
- `xml:lang` on `rdf:li` stored as `lang|value`; x-default stored without prefix
- `rdf:parseType="Resource"` → struct handling
- `rdf:resource` shorthand attribute
- Inline attribute shorthand on `rdf:Description`
- All core namespaces correct (xmp, xmpRights, xmpMM, dc, photoshop, exif, tiff, aux, Iptc4xmpCore, Iptc4xmpExt)
- `xml.EscapeText` used for all character data in write
- 2 KB whitespace padding + `<?xpacket end="w"?>`
- Deterministic output via sort

### Defects / Gaps
1. **No UTF-16 packet support** — XMP §7.2 allows UTF-16/UTF-32; `bytes.Index` on UTF-8 literals fails
2. **`begin=` attribute value not validated** — accepts malformed PIs
3. **Array-of-structs not parsed** — `rdf:li` containing nested `rdf:Description` (e.g., xmpMM:History) silently dropped
4. **`xml:lang` not namespace-qualified** in rdf:li scanning (minor)
5. **Struct fields cross-namespace key attribution** — stored under field NS not parent NS
6. **Struct properties not serialized correctly** — `propLocal.fieldLocal` emitted as dotted XML element name (invalid)
7. **`collectionType` incomplete** — xmpMM:History/Ingredients should be Seq, not Bag
8. **Missing type-support namespaces**: stEvt, stRef, stDim, stVer, xmpBJ, xmpTPg, xmpDM, xmpG, xmpGImg
9. **`rdf:about` not validated** — spec requires consistent subject URI across all rdf:Description blocks

## IPTC Score: 7.5/10

### Confirmed correct
- Tag marker 0x1C scanning
- 5-byte header: marker + record + dataset + 2-byte size
- Standard 2-byte length (big-endian)
- Extended length: `0x80 | nBytes` in high byte; subsequent nBytes carry actual length
- IRB: 8BIM marker, resource ID, Pascal string name + even-padding, 4-byte size, data even-padding
- Resource ID 0x0404 for IPTC-NAA
- UTF-8 declaration: ESC % G (0x1B 0x25 0x47) in 1:90
- ISO-8859-1 fallback via golang.org/x/text charmap
- UTF-8 declaration re-emitted on encode when originally present
- Dataset registry: good coverage of photographic datasets (5, 25, 55, 60, 80, 105, 116, 120, etc.)

### Defects / Gaps
1. **Destructive IRB write** — buildIRB emits only 0x0404; all other resource blocks (ICC, thumbnail, XMP, etc.) discarded
2. **Multi-segment APP13 not supported** — large IPTC can span multiple APP13 segments; only first parsed
3. **No auto UTF-8 declaration on write** — SetCaption/SetCopyright with non-ASCII values written without 1:90 dataset injection
4. **`isUTF8Declaration` len==3 too strict** — some tools append NUL padding; HasPrefix would be more robust
5. **No ISO 2022 charset diversity** — non-UTF-8, non-Latin-1 declared charsets (e.g., Shift-JIS) produce garbled output
6. **Malformed data silently truncates** — break on integrity failure drops all subsequent datasets without error
7. **Missing IIM datasets**: 2:22, 2:26-2:31, 2:35-2:38, 2:42, 2:45-2:47, 2:75 (agency workflow fields)
8. **Missing convenience accessors**: ObjectName (2:05), DateCreated (2:55), Headline (2:105), City/ProvinceState/CountryName

**Why:** Research-only audit for library gap assessment.
**How to apply:** Use these findings to prioritize remediation work; P0 is struct serialization (XMP) and destructive IRB write (IPTC).
