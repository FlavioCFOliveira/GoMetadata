# XMP — Conformance Test Contract

Normative-requirements checklist for the GoMetadata conformance test battery. Rule IDs
(`PW-*`, `RDF-*`, `NS-*`, `VT-*`, `JPEG-*`, `TIFF-*`, `PNG-*`, `HEIF-*`, `MWG-*`, `ROB-*`)
are used verbatim as Go sub-test names. 67 rules total.

> Note for implementers: a few byte-length constants below (e.g. the JPEG XMP namespace
> identifier length) must be reconciled against the actual codebase during implementation —
> verify empirically, the spec text is the contract for *behaviour*, the code is ground truth
> for *exact bytes*. `"http://ns.adobe.com/xap/1.0/"` is 28 chars → 29 bytes including the
> terminating NUL.

## Section 1: Specification Identifiers
| ID | Document | Version / Date | Scope |
|---|---|---|---|
| S1 | ISO 16684-1 / Adobe XMP Part 1 | 2019 / Apr 2012 | data model, serialization, core props, value types, namespaces, packet |
| S2 | ISO 16684-2 / Adobe XMP Part 2 | 2014 / Feb 2022 | standard schemas (dc, xmp, xmpMM, photoshop, exif, tiff, IPTC-XMP, …) |
| S3 | Adobe XMP Part 3 | Jan 2020 | embedding in JPEG/TIFF/PNG/HEIF/PDF/… |
| S4 | MWG Guidelines | v2.0 / 2010 | reconciliation across EXIF/IPTC-IIM/XMP |
| S5 | W3C RDF/XML Syntax | 2004-02-10 | RDF/XML serialization |
| S6 | XML 1.0 (5th ed.) | 2008-11-26 | well-formedness, char ranges, namespaces |

## Section 2: Normative Structural Rules

### 2.1 Packet Wrapper — XMP Part 1 §7.1
- **PW-01** Packet begins with `<?xpacket begin="<BOM>" id="W5M0MpCehiHzreSzNTczkc9d"?>`; begin value = BOM in the file encoding (`EF BB BF` UTF-8).
- **PW-02** `id` MUST be exactly `W5M0MpCehiHzreSzNTczkc9d`.
- **PW-03** Packet ends with `<?xpacket end="r"?>` or `end="w"`; missing end → error/partial+diagnostic.
- **PW-04** For in-place writeable packets, whitespace padding fills space before the end PI.
- **PW-05** BOM encodes byte order (UTF-8 / UTF-16 LE / UTF-16 BE); empty/absent ⇒ assume UTF-8.
- **PW-06** Scanning locates begin PI by searching `3C 3F 78` (`<?x`) then matching `xpacket`+`begin`; no fixed offset.
- **PW-07** Wrapper optional when the container delimits XMP (APP1, TIFF tag 700, PNG iTXt, HEIF item) — handle both.

### 2.2 RDF/XML Serialization — XMP Part 1 §7.2–§7.4
- **RDF-01** Outer element `<x:xmpmeta xmlns:x="adobe:ns:meta/">`; recognise by URI, not prefix.
- **RDF-02** Exactly one `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`.
- **RDF-03** One or more `<rdf:Description rdf:about="">`; accept any `rdf:about`.
- **RDF-04** Properties as attributes (shorthand) or child elements (expanded); handle both; write expanded for arrays/structs.
- **RDF-05** Arrays: `rdf:Seq` (ordered), `rdf:Bag` (unordered), `rdf:Alt` (lang alt); preserve semantics on round-trip.
- **RDF-06** `rdf:Alt` MUST contain an `rdf:li xml:lang="x-default"`; return x-default when no lang match.
- **RDF-07** Qualifiers (other than xml:lang) via `rdf:value` + qualifier elements; MUST preserve on write.
- **RDF-08** Structures = nested `rdf:Description`; recurse to extract fields.

### 2.3 Namespaces — XMP Part 1 §6 / XML Namespaces
- **NS-01** Namespace URIs are canonical; resolve properties by URI, never by prefix.
- **NS-02** Prefixes are arbitrary NCNames; never hard-code prefix strings.
- **NS-03** Writer MUST NOT bind two distinct URIs to the same prefix; generate `ns0`, `ns1`, …
- **NS-04** Preferred prefix↔URI bindings (conventional): dc, xmp, xmpRights, xmpMM, xmpBJ, xmpTPg, xmpDM, xmpNote, photoshop, crs, tiff, exif, aux, Iptc4xmpCore, Iptc4xmpExt, x=`adobe:ns:meta/`, rdf, xml.

### 2.4 Value Types — XMP Part 1 §8
- **VT-01** Text = arbitrary Unicode string (XML char data).
- **VT-02** Integer = signed decimal, no fraction, no hex.
- **VT-03** Real = signed decimal, optional fraction, no scientific notation.
- **VT-04** Boolean = `True`/`False` (capitalised); write strict, accept lowercase/numeric variants for robustness.
- **VT-05** Date = ISO 8601 subset: `YYYY`, `YYYY-MM`, `YYYY-MM-DD`, `…Thh:mmTZD`, `…Thh:mm:ssTZD`, `…Thh:mm:ss.sTZD` (TZD = `Z` or `±hh:mm`). Accept all six; write most precise without inventing precision.
- **VT-06** URI = RFC 3986; serialize as Text value (not rdf:resource unless a resource ref).
- **VT-07** GUID = 32 hex digits, no separators/braces; accept both cases, write lowercase.
- **VT-08** Forbidden XML 1.0 §2.2 code points (U+0000–08, 0B, 0C, 0E–1F, FFFE, FFFF): never emit; on read substitute U+FFFD or skip, never crash.

## Section 3: Embedding Rules

### 3.1 JPEG — XMP Part 3 §1.1
- **JPEG-01** Standard XMP in APP1 (`FF E1`); payload begins with `http://ns.adobe.com/xap/1.0/\0` then the packet.
- **JPEG-02** APP1 ≤ 65535; max standard XMP data = 65535 − 2 − 29 = 65504 bytes.
- **JPEG-03** Exactly one standard XMP APP1; reader uses first, writer ensures one.
- **JPEG-04** Excess > 65504 → ExtendedXMP APP1(s) with id `http://ns.adobe.com/xmp/extension/\0`.
- **JPEG-05** ExtendedXMP header after id = 32 ASCII hex GUID + uint32 BE fullLength + uint32 BE offset.
- **JPEG-06** GUID = MD5 of full extended XMP = value of `xmpNote:HasExtendedXMP` in standard packet; validate before reassembly.
- **JPEG-07** Reassembly validates: same GUID, same fullLength, Σ chunk lengths = fullLength, no dup/overlap offsets; incomplete → error.
- **JPEG-08** Merge extended `rdf:Description` into primary model without duplicating standard props.

### 3.2 TIFF — XMP Part 3 §1.3
- **TIFF-01** XMP in tag 700 (`0x02BC`); type BYTE(1) or UNDEFINED(7); accept both, write BYTE.
- **TIFF-02** No size limit; full packet stored verbatim.
- **TIFF-03** Wrapper not required; handle with/without; never write APP1-framed payload into the tag.

### 3.3 PNG — XMP Part 3 §1.6
- **PNG-01** XMP in `iTXt` with keyword `XML:com.adobe.xmp\0`.
- **PNG-02** iTXt compression flag MUST be 0 (uncompressed).
- **PNG-03** Packet end attribute = `r` for PNG embedding.
- **PNG-04** Exactly one XMP iTXt; reader uses first.
- **PNG-05** Language tag + translated keyword empty (two NULs); accept any on read.

### 3.4 HEIF / ISO BMFF — XMP Part 3 §1.8
- **HEIF-01** XMP item: `item_type='mime'`, `content_type='application/rdf+xml'`; identify by these, not item ID.
- **HEIF-02** XMP item referenced by `cdsc` from the primary image item; follow it.
- **HEIF-03** Wrapper not required; handle both.

## Section 4: Reconciliation / MWG (v2.0)
- **MWG-01** Read priority XMP > EXIF > IPTC-IIM on conflict.
- **MWG-02** IPTC digest (Photoshop resource `0x0425`): match → XMP priority; mismatch → elevate IIM trust.
- **MWG-03** dc:description[x-default] ↔ EXIF UserComment `0x9286` ↔ IIM 2:120; sync all on write.
- **MWG-04** dc:creator[1] ↔ EXIF Artist `0x013B` ↔ IIM 2:80; sync all on write.
- **MWG-05** dc:rights[x-default] ↔ EXIF Copyright `0x8298` ↔ IIM 2:116; sync all on write.
- **MWG-06** photoshop:DateCreated ↔ EXIF DateTimeOriginal `0x9003`+SubSec `0x9291` ↔ IIM 2:55+2:60; combine/split.
- **MWG-07** dc:subject ↔ IIM 2:25 keywords (no EXIF equivalent); sync both.
- **MWG-08** exif:GPS* (XMP) ↔ EXIF GPS IFD `0x0002/0x0004/0x0006/0x0005`; decimal degrees in XMP.
- **MWG-09** Write sync scope: update all formats present unless API requests a format-specific mode.

## Section 5: Robustness Cases (MUST NOT crash)
- **ROB-01** Missing end PI → partial content + diagnostic, no crash.
- **ROB-02** Malformed RDF (not well-formed) → descriptive error, no silent partial.
- **ROB-03** XXE / billion-laughs: disable external entities, cap entity expansion; reject/cap `<!DOCTYPE>` internal entities.
- **ROB-04** Unknown-namespace properties preserved on read, re-serialized on write losslessly.
- **ROB-05** Duplicate property → last-value + diagnostic; continue parsing.
- **ROB-06** Arbitrary-offset packet scan (linear search), handle marker spanning buffer boundary.
- **ROB-07** HasExtendedXMP present but no chunks → standard content + error/warning, not silent truncation.
- **ROB-08** Duplicate/overlapping ExtendedXMP offsets → error or first-occurrence, never longer-than-declared buffer.
- **ROB-09** UTF-16/32 BOM → transcode to UTF-8 before XML decode; unrecognisable → error.
- **ROB-10** Filter/replace forbidden XML C0 chars before serialization (U+FFFD acceptable).
- **ROB-11** Prefix-collision on output → distinct prefixes per distinct URI.
- **ROB-12** Deep nesting → enforce max recursion depth (~100); error if exceeded; no stack overflow.

## Sources
- ISO 16684-1:2019, ISO 16684-2:2014; Adobe XMP Specification Parts 1/2/3
- MWG Guidelines v2.0; W3C RDF/XML Syntax (2004); XML 1.0 (5th ed.)
- Adobe XMP namespaces docs; Exiv2 "Metadata in JPEG files"; ExifTool MWG composite tags
