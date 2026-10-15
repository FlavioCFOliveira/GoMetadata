# XMP — Conformance Test Contract

Normative-requirements checklist for the GoMetadata conformance test battery. Rule IDs
(`PW-*`, `RDF-*`, `NS-*`, `VT-*`, `JPEG-*`, `TIFF-*`, `PNG-*`, `HEIF-*`, `MWG-*`, `ROB-*`)
are used verbatim as Go sub-test names. 74 rules total.

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
- **PW-05** BOM encodes byte order (UTF-8 / UTF-16 LE / UTF-16 BE); empty/absent ⇒ assume UTF-8 unless the NUL-byte pattern of the first character says otherwise (PW-08).
- **PW-06** Scanning locates begin PI by searching `3C 3F 78` (`<?x`) then matching `xpacket`+`begin`; no fixed offset.
- **PW-08** A UTF-16 or UTF-32 packet without a BOM is recognised from the first bytes (`00 nn` UTF-16BE, `nn 00` UTF-16LE, `00 00 00 nn` UTF-32BE, `nn 00 00 00` UTF-32LE; XMP Part 1 §7.1 p. 9, §7.3.2; XML 1.0 App. F.1; Toolkit `DetermineInputEncoding`) and decoded to the same properties as the UTF-8 form. A first byte of 0x80 or more that is not a BOM is UTF-8 (divergence from the Toolkit).
- **PW-07** Wrapper optional when the container delimits XMP (APP1, TIFF tag 700, PNG iTXt, HEIF item) — handle both.

### 2.2 RDF/XML Serialization — XMP Part 1 §7.2–§7.4
- **RDF-01** Outer element `<x:xmpmeta xmlns:x="adobe:ns:meta/">`; recognise by URI, not prefix.
- **RDF-02** Exactly one `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`.
- **RDF-03** One or more `<rdf:Description rdf:about="">`; accept any `rdf:about`.
- **RDF-04** Properties as attributes (shorthand) or child elements (expanded); handle both; write expanded for arrays/structs.
- **RDF-05** Arrays: `rdf:Seq` (ordered), `rdf:Bag` (unordered), `rdf:Alt` (lang alt); preserve semantics on round-trip.
- **RDF-06** `rdf:Alt` MUST contain an `rdf:li xml:lang="x-default"`; return x-default when no lang match.
- **RDF-07** Qualifiers (other than xml:lang) via `rdf:value` + qualifier elements; MUST preserve on write.
- **RDF-09** Element text of a literal property is verbatim: leading, trailing and inner white space is kept, as in the attribute form (XML 1.0 §2.10; XMP Part 1 §7.5; Toolkit `LiteralPropertyElement`); CRLF and CR read as LF (XML 1.0 §2.11). White space between child elements is not content.
- **RDF-10** Registered aliases (35, Adobe XMP Part 2 §1.1.1; Toolkit `RegisterStandardAliases`) are folded into the base property on parse by the rules of `MoveExplicitAliases`: base absent ⇒ the alias is transplanted (a text or array-item alias becomes the x-default or first item of a new array); base present ⇒ the alias is deleted (base wins); readers find the value at the base property and writers write the base form only.
- **RDF-11** Setting the default text of a language alternative (`SetCaption`, `SetCopyright`) updates the x-default item and the items that repeated its old text, keeps the other languages, and inserts x-default first when absent (XMP Part 1 §8.2.2.4; Toolkit `SetLocalizedText`).
- **RDF-12** `CameraModel` is `tiff:Model` only; `xmp:CreatorTool` names the software and is never a fallback (XMP Part 1 §8.4 p. 27).
- **RDF-08** Structures = nested `rdf:Description`; recurse to extract fields.
  - Since task #303 the parser builds the XMP data model tree (ISO 16684-1:2019 §6), so
    structures and arrays nest to any depth (an array inside a struct field, a struct inside an
    array item, and so on) and keep their qualifiers. `XMP.Properties` exposes the documented
    keys for the first two levels ("parent.field", "parent[N].field"); every deeper node is kept
    in the tree and written back unchanged by `Encode`.

### 2.3 Namespaces — XMP Part 1 §6 / XML Namespaces
- **NS-01** Namespace URIs are canonical; resolve properties by URI, never by prefix.
- **NS-02** Prefixes are arbitrary NCNames; never hard-code prefix strings.
- **NS-03** Writer MUST NOT bind two distinct URIs to the same prefix; reuse the source document's prefix of a namespace without a well-known prefix when it is free, otherwise generate `ns0`, `ns1`, …
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
- **JPEG-08** Merge extended `rdf:Description` into primary model without duplicating props; a top-level property present in both portions takes the extended value as a whole (Adobe XMP Toolkit `XMPUtils::MergeFromJPEG`).
- **JPEG-09** ExtendedXMP serialized without a packet wrapper; GUID written as 32 hex digits with capital A–F (S3 §1.1.3.1).
- **JPEG-10** Reader recomposes StandardXMP and ExtendedXMP into one data model tree and removes `xmpNote:HasExtendedXMP`; the xmpNote namespace URI is `http://ns.adobe.com/xmp/note/` (S3 §1.1.3.1).
- **JPEG-11** Writer avoids partitioning (S3 §1.1.3.2 p. 15, task #332): a packet whose compact serialisation fits one APP1 segment (65502 bytes, S3 §1.1.3.1 p. 14) is written whole, without its padding; the packet padding alone never causes a split. `TestTask332_CompactNotSplit`, `TestTask332_PaddedPacketThatFitsUnpadded`.
- **JPEG-12** Writer partitions as S3 §1.1.3.2 (p. 15) recommends: StandardXMP at most 65000 bytes, compact, carrying `xmpNote:HasExtendedXMP` (added with a 32-byte dummy before the size is measured, S3 §1.1.3.1 p. 15); properties move to ExtendedXMP in order xmp:Thumbnails, the Camera Raw namespace, photoshop:History, then top-level properties by estimated size, largest first, until it fits; the two portions recompose into the original XMP. Divergence from the Toolkit (`XMPUtils::PackageForJPEG`), documented: xmp:Thumbnails moves instead of being deleted. `TestPackageForJPEG_*` (xmp), `TestTask332_PartitionKeepsStandardScalars`.
- **JPEG-13** The extended serialisation has no packet wrapper and its GUID is the uppercase MD5 of its bytes (JPEG-09, JPEG-06); an XMP that cannot be parsed, or whose standard portion cannot fit, is stored whole as extended XMP behind a stub (fallback). `TestTask332_FallbackExtendedWrite`, `TestTask295_ExtendedXMPWriteDeterministic`.
- **JPEG-14** Reader removes `xmpNote:HasExtendedXMP` from the StandardXMP also when extended chunks exist but none matches its GUID, or the matching chunks do not recompose, as the Toolkit does (`JPEG_Handler.cpp:ProcessXMP`); a write of that XMP does not bring it back. `TestTask332_UnmatchedLinkRemoved`, `TestStripExtendedLink`.

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
- **PNG-06** No more than one chunk containing XMP (S3 §1.1.5 p. 16, task #325): an XMP packet read from a `Raw profile type xmp` text chunk (or an `APP1` profile that carries a packet) is the fallback when no native iTXt exists; a write that emits the XMP iTXt chunk drops that profile, so one current copy remains. A profile that cannot be decoded is kept, and a nil XMP payload leaves it. `TestInject_RawProfileXMPDropped`, `TestExtract_RawProfileXMP`, `TestCorpus_PNGRawProfiles`.

### 3.4 HEIF / ISO BMFF — XMP Part 3 §1.8
- **HEIF-01** XMP item: `item_type='mime'`, `content_type='application/rdf+xml'`; identify by these, not item ID.
- **HEIF-02** XMP item referenced by `cdsc` from the primary image item; follow it.
- **HEIF-03** Wrapper not required; handle both.

## Section 4: Reconciliation / MWG (v2.0)
- **MWG-01** Read priority EXIF > XMP > IPTC-IIM on conflict (MWG §4.2.3.1, §4.2.3.3) for the fields mapped in all three formats (description, copyright, creator, date/time original); XMP > IPTC-IIM for keywords (§5.1). A value of only spaces or NULs is absent (§4.2.3.3).
- **MWG-02** IPTC digest (Photoshop resource `0x0425`): match or absent → MWG-01 priority; mismatch → an IIM value that differs from the value predicted from XMP wins over EXIF and XMP (§4.2.3.2 scenario 4, §4.2.3.3).
- **MWG-03** dc:description[x-default] ↔ EXIF ImageDescription `0x010E` ↔ IIM 2:120 (MWG §5.2); sync all on write.
- **MWG-04** dc:creator[1] ↔ EXIF Artist `0x013B` ↔ IIM 2:80; sync all on write.
- **MWG-05** dc:rights[x-default] ↔ EXIF Copyright `0x8298` ↔ IIM 2:116; sync all on write.
- **MWG-06** photoshop:DateCreated ↔ EXIF DateTimeOriginal `0x9003`+SubSec `0x9291` (+OffsetTime `0x9011`) ↔ IIM 2:55+2:60; combine/split (MWG §5.3).
- **MWG-07** dc:subject ↔ IIM 2:25 keywords (no EXIF equivalent); sync both.
- **MWG-08** exif:GPS* (XMP) ↔ EXIF GPS IFD `0x0002/0x0004/0x0006/0x0005`; decimal degrees in XMP.
- **MWG-09** Write sync scope: update all formats present unless API requests a format-specific mode.

## Section 5: Robustness Cases (MUST NOT crash)
- **ROB-01** Missing end PI → partial content + diagnostic, no crash.
- **ROB-02** Malformed RDF (not well-formed) → descriptive error, no silent partial. The scanner checks Element Type Match (every end tag names the innermost open start tag), stray end tags, malformed start tags and unclosed elements (XML 1.0 §3; error wraps `ErrXMLNotWellFormed` and names the element). `Write` carries an unparseable packet unchanged and the setters create no XMP over it.
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
