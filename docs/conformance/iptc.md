# IPTC IIM + Core/Extension + APP13/8BIM — Conformance Test Contract

Normative-requirements checklist for the GoMetadata conformance test battery. Rule IDs
(`IIM-BIN-*`, `IIM-REC-*`, `IIM-CS-*`, `IRB-APP13-*`, `ROBUST-*`) are used verbatim as Go
sub-test names. Cite in code as `// IIM §X.Y`, `// Adobe-IRB §"Image Resources"`, `// MWG §X.Y`.

## Section 1: Exact Spec Identifiers

| Label | Title | Org | Version | Year |
|---|---|---|---|---|
| IIM | IPTC-NAA Information Interchange Model | IPTC + NAA | 4.2 | 2014 |
| Core | IPTC Photo Metadata Standard — Core | IPTC | 1.5 (2025.1 release) | 2025 |
| Extension | IPTC Photo Metadata Standard — Extension | IPTC | 1.9 (2025.1 release) | 2025 |
| IRB | Adobe Photoshop File Formats — "Image Resources" | Adobe | living | current |
| MWG | Guidelines for Handling Image Metadata | Metadata Working Group | 2.0 | Nov 2010 |

## Section 2: Normative Structural Rules

### 2.1 IIM Dataset Binary Layout
- **IIM-BIN-01** Every dataset begins with octet `0x1C`. — IIM §1.5(i). Fixture: octet 0 ≠ 0x1C → skip/reject, no crash.
- **IIM-BIN-02** Octet 2 = record number; valid 1–9. Out-of-range must not crash. — IIM §1.5(ii).
- **IIM-BIN-03** Octet 3 = dataset number; unknown ds within valid record → skip by declared length, continue. — IIM §1.5(iii).
- **IIM-BIN-04** Octets 4–5 = big-endian uint16 length (standard form when MSB of octet 4 = 0); max 32767. — IIM §1.5(iv).
- **IIM-BIN-05** Extended form when MSB of octet 4 = 1: lower 15 bits = count `n` (1–4) of following length bytes; those `n` bytes = big-endian length (max 2^32−1). — IIM §1.6.2.
  Fixture (40000-byte 2:120): `1C 02 78 80 04 00 00 9C 40 …`.
- **IIM-BIN-06** Header is always 5 bytes (marker+record+dataset+2 length). < 5 bytes remaining → do not read. — IIM §1.5.
- **IIM-BIN-07** Record 1 datasets precede Record 2; violation must not crash (may flag non-conformant). — IIM §1.5(v).
- **IIM-BIN-08** Record Version (1:00 / 2:00) MUST be first in its record. — IIM §1.5(v), §2.2.1.

### 2.2 Record Structure & Mandatory Datasets
- **IIM-REC-01** 1:00 EnvelopeRecordVersion mandatory when Record 1 present; uint16 = 4; 2 bytes. — IIM §1.6.1.
- **IIM-REC-02** 2:00 ApplicationRecordVersion mandatory when Record 2 present; uint16 = 4; 2 bytes. — IIM §2.2.1.
  Implemented and tested: `Encode` always emits 2:00 as the first Record-2 dataset (`TestIIMREC02`,
  `TestIIMREC02RoundTrip`; fixed audit 2026-06-09 FINDING 3).
- **IIM-REC-03** 1:20 FileFormat, 1:22 FileVersion mandatory in transmission context; parser must not reject embedded streams lacking them.

### 2.3 Coded Character Set (1:90)
- **IIM-CS-01** 1:90 CodedCharacterSet = ISO 2022 escape sequences, max 32 octets, optional, not repeatable. — IIM §1.5.1.
- **IIM-CS-02** UTF-8 designation = 3-byte ESC%G `1B 25 47`. — IIM §1.5.1. Full decl: `1C 01 5A 00 03 1B 25 47`.
- **IIM-CS-03** Absent 1:90 ⇒ default ISO 8859-1 (Latin-1); bytes 0x80–0xFF are Latin-1, transcode to UTF-8.
- **IIM-CS-04** Tolerate non-standard ASCII "UTF8" marker as a UTF-8 declaration (real-world Adobe variant).

### 2.4 APP13 / Photoshop IRB
- **IRB-APP13-01** JPEG APP13 marker `FF ED` + big-endian uint16 length (includes itself, excludes marker); max payload 65533.
- **IRB-APP13-02** Payload begins with 14-byte `"Photoshop 3.0\0"`; also accept legacy 19-byte `"Adobe_Photoshop2.5:"`.
- **IRB-APP13-03** Each resource block begins with 4-byte `"8BIM"` (`38 42 49 4D`).
- **IRB-APP13-04** Bytes 4–5 = big-endian uint16 resource ID; `0x0404` = IPTC-NAA IIM.
- **IRB-APP13-05** Pascal name: 1 length byte + name bytes, padded with one `0x00` to even total (zero-length name = `00 00`).
- **IRB-APP13-06** big-endian uint32 data length (excludes itself and preceding fields).
- **IRB-APP13-07** Data payload padded with one `0x00` iff length is odd (next block on even boundary).
- **IRB-APP13-08** Resource `0x0404` payload is a raw, unwrapped IIM dataset stream.
- **IRB-APP13-09** Multiple APP13 segments: concatenate all "Photoshop 3.0" payloads.
  Implemented and tested: the JPEG layer concatenates every "Photoshop 3.0" APP13 payload before
  IRB parsing, so IPTC in any segment is found regardless of position (`TestIRBAPP1309`,
  `TestROBUST15`; fixed FINDING 4).

## Section 3: Per-Dataset Conformance Table (Record 2)
Min/Max = IIM octet bounds for the value (excl. 5-byte header). Rep. = repeatable.

| DS | Name | Max oct. | Rep. | Value format |
|---|---|---|---|---|
| 00 | ApplicationRecordVersion | 2 | No | uint16 BE = 4 |
| 05 | ObjectName (Title) | 64 | No | Text |
| 10 | Urgency | 1 | No | digit '1'–'8' |
| 12 | SubjectReference | 236 | Yes | `IPN:name:l1:l2:l3` |
| 15 | Category | 3 | No | up to 3 uppercase letters |
| 20 | SupplementalCategories | 32 | Yes | Text |
| 25 | Keywords | 64 | Yes | Text (one per occurrence) |
| 26 | ContentLocationCode | 3 | Yes | ISO 3166-1 alpha-3 |
| 27 | ContentLocationName | 64 | Yes | Text |
| 30 | ReleaseDate | 8 | No | CCYYMMDD |
| 35 | ReleaseTime | 11 | No | HHMMSS±HHMM |
| 40 | SpecialInstructions | 256 | No | Text |
| 55 | DateCreated | 8 | No | CCYYMMDD |
| 60 | TimeCreated | 11 | No | HHMMSS±HHMM |
| 62 | DigitalCreationDate | 8 | No | CCYYMMDD |
| 63 | DigitalCreationTime | 11 | No | HHMMSS±HHMM |
| 65 | OriginatingProgram | 32 | No | Text |
| 70 | ProgramVersion | 10 | No | Text |
| 80 | By-line | 32 | Yes | Text (creator) |
| 85 | By-lineTitle | 32 | Yes | Text |
| 90 | City | 32 | No | Text |
| 92 | Sub-location | 32 | No | Text |
| 95 | Province-State | 32 | No | Text |
| 100 | Country-PrimaryLocationCode | 3 | No | ISO 3166-1 alpha-3 |
| 101 | Country-PrimaryLocationName | 64 | No | Text |
| 103 | OriginalTransmissionReference | 32 | No | Text |
| 105 | Headline | 256 | No | Text |
| 110 | Credit | 32 | No | Text |
| 115 | Source | 32 | No | Text |
| 116 | CopyrightNotice | 128 | No | Text |
| 118 | Contact | 128 | Yes | Text |
| 120 | Caption-Abstract | 2000 | No | Text |
| 122 | Writer-Editor | 32 | Yes | Text |
| 130 | ImageType | 2 | No | digit + type char |
| 131 | ImageOrientation | 1 | No | 'P'/'L'/'S' |
| 135 | LanguageIdentifier | 3 | No | ISO 639-2 |

Test implications: writer MUST NOT exceed Max (truncate or error); reader MUST tolerate
over-max values from the wild; non-repeatable duplicates → deterministic (first) + no panic;
repeatable datasets MUST return all occurrences.

## Section 4: IPTC ↔ XMP Mapping & Reconciliation
Namespaces: `Iptc4xmpCore` = `http://iptc.org/std/Iptc4xmpCore/1.0/xmlns/`,
`Iptc4xmpExt` = `http://iptc.org/std/Iptc4xmpExt/2008-02-29/`,
`dc` = `http://purl.org/dc/elements/1.1/`, `photoshop` = `http://ns.adobe.com/photoshop/1.0/`.

| IIM | XMP property | Type |
|---|---|---|
| 2:05 ObjectName | dc:title | Lang Alt (x-default) |
| 2:25 Keywords | dc:subject | bag Text |
| 2:55 + 2:60 | photoshop:DateCreated | Date (ISO 8601) |
| 2:80 By-line | dc:creator | seq ProperName (order preserved) |
| 2:85 By-lineTitle | photoshop:AuthorsPosition | Text |
| 2:90 City | photoshop:City | Text |
| 2:95 Province-State | photoshop:State | Text |
| 2:100 CountryCode | Iptc4xmpCore:CountryCode | Text |
| 2:101 Country | photoshop:Country | Text |
| 2:105 Headline | photoshop:Headline | Text |
| 2:110 Credit | photoshop:Credit | Text |
| 2:115 Source | photoshop:Source | Text |
| 2:116 CopyrightNotice | dc:rights | Lang Alt (x-default) |
| 2:120 Caption | dc:description | Lang Alt (x-default) |
| 2:122 Writer | photoshop:CaptionWriter | Text |

- **RECONCILE-01** Dual-write parity (write path): every convenience setter that
  targets more than one backend (IIM, XMP, EXIF) MUST leave all targeted
  backends holding semantically equivalent content, modulo each backend's own
  lossy constraints (sub-second truncation, single-string flattening of a
  repeatable field, 8859-1 vs UTF-8 transcoding). — IPTC Photo Metadata 2025.1
  Mapping Guidelines; MWG §2 ("Synchronization").
- **RECONCILE-02** Read priority (MWG §4.2.3.3, "MWG-01"): EXIF > XMP > IIM
  for {Caption/Abstract, CopyrightNotice, By-line, DateCreated+TimeCreated};
  XMP > IIM for Keywords, which EXIF does not carry (MWG §5.1). Exception
  (MWG §4.2.3.2, scenario 4, "MWG-02"): when Photoshop IRB resource `0x0425`
  ("IPTC Digest") is present, compare `MD5(raw 0x0404 IIM stream)` to the
  stored 16-byte value. All-zero stored digest (an "unknown" sentinel of this
  library; MWG §4.2.3.2, pp. 26-29, defines none) OR a computed/stored mismatch ⇒ the IIM value wins over EXIF and XMP unless it
  equals the IIM value predicted from the XMP value (the XMP value truncated
  to the DataSet length limit; for dates, the IIM export of the XMP date).
  Digest match, or resource absent, ⇒ MWG-01 applies unchanged. A value of
  only spaces or NULs is absent (MWG §4.2.3.3). The write side of the digest
  (creation and refresh) is RECONCILE-06. Proven by
  `TestConformance_MWG02` (metadata_conformance_test.go),
  `TestPrecedence_ExplicitPolicyComment` (root_task50_test.go) and
  `TestTask308_ReadPolicy_Text`, `TestTask308_ReadPolicy_Keywords`,
  `TestTask308_DateTimeOriginalSources`,
  `TestTask308_DateTimeOriginalEditedIIMWinsOverEXIF` (reconcile_task308_test.go).
- **RECONCILE-03** Date bridging (write path): a single capture timestamp is
  split into IIM 2:55 `CCYYMMDD` + 2:60 `HHMMSS±HHMM` (IIM §2.2.23), EXIF
  ExifIFD 0x9003 DateTimeOriginal (EXIF §4.6.5), and XMP `photoshop:DateCreated`
  as RFC 3339 (Adobe XMP Spec Part 2 §1.2.7 / IPTC Mapping Guidelines 2025.1).
  Sub-second precision is dropped on both IIM and XMP (neither format's chosen
  layout carries a fractional-second slot). `xmp:CreateDate` (digitization
  date) is a distinct field and MUST NOT be written by this bridge.
- **RECONCILE-04** By-line sequencing (bidirectional): IIM 2:80 By-line
  (repeatable, IIM §2.2.25) order MUST be preserved into XMP `dc:creator`'s
  ordered `rdf:Seq` (ISO 16684-1 §7.5, ordered array) and vice versa on read.
  The convenience setter also flattens the ordered list into EXIF IFD0 0x013B
  Artist (EXIF §4.6.4 Table 3) as a single string joined with `"; "`
  (semicolon-space), the MWG list-handling convention for single-string EXIF
  fields that mirror a multi-valued XMP/IIM property (MWG §5.7): an item that
  contains `"; "` or begins with a quote is enclosed in quotes and its quotes
  are doubled, and on read the Artist string is split back into items.
- **RECONCILE-05** Lang Alt ↔ IIM plain-string reconciliation: an IIM Record 2
  text dataset (2:05 ObjectName, 2:116 CopyrightNotice, 2:120 Caption/Abstract)
  round-trips through XMP as an `rdf:Alt` collection whose `x-default`
  (ISO 16684-1 §7.5 / P1-H) item is authoritative in both directions. Any
  non-default `xml:lang` alternative present in the XMP `rdf:Alt` is preserved
  on the XMP side but has no IIM representation (IIM carries one un-tagged
  string) and MUST be dropped, not concatenated, when the value flows into IIM.

- **RECONCILE-06** IPTC digest on write (MWG Guidelines v2.0 §4.2.3.2, pp.
  26-29; task #323). The digest is the MD5 of the entire IIM block, stored as a
  16-byte binary value in Photoshop resource 1061 (`0x0425`; p. 26 and the
  "IPTC-IIM checksum" paragraph, p. 29). The rule is the one of p. 29: "If
  IPTC-IIM and XMP are both present, whether changed or not, a Changer MUST
  create or update the checksum value" (a Creator that writes both MUST create
  it, p. 28). So, on every write that stores the IIM and has XMP in the output
  (written now or present in the file; JPEG: an XMP packet is written; TIFF:
  XMP written or tag `0x02BC` present; PNG: XMP written, for a raw profile in
  the Photoshop IRB form), the digest is set to the MD5 of the IIM written
  (TIFF: of the stored tag `0x83BB` value including its LONG zero padding): it
  is **created** when the Photoshop block holds none (in a new `0x8649` block
  holding only it, for TIFF) and **rewritten** when it holds one, a stale
  digest and an unchanged IIM included. When the output has no XMP, or the IIM
  is removed, the two conditions of the MUST are not both met and MWG gives no
  rule for an IIM-only file (its Changer guidance, p. 28, is to write IIM and
  XMP together): every digest is then left as it is and none is created. The
  read side is unchanged: a stale digest found in a source file still elevates
  the IIM value on read (RECONCILE-02), but a write that stores IIM and XMP
  together has reconciled both and so refreshes it. Where reference
  implementations diverge, the specification wins: ExifTool `MWG.pm` updates
  only an absent or matching digest, and the Adobe XMP Toolkit
  (`ReconcileIPTC.cpp:SetIPTCDigest`) writes it unconditionally, also for an
  IIM-only write. Proven by `TestAppendSpliced_DigestPolicy` (internal/irb),
  `TestInject_IPTCDigestPolicy` (format/jpeg), `TestUpsertIPTC_DigestPolicy`
  (format/tiff), `TestInject_RawProfileIPTCDigest` (format/png),
  `TestWrite_JPEGIPTCDigestCreated`, `TestWrite_JPEGStaleIPTCDigestRefreshed`
  (iptc_photoshop_irb_test.go) and `TestCorpus_IPTCDigest`.
- **IIM-COPY-01** IPTC copy in a JPEG trailer (task #322): an AFCP trailer
  (AXS File Concatenation Protocol, 1992) whose directory has an `IPTC` data
  set, and a FotoStation trailer whose tag 1 record holds the IIM, are
  rewritten with the IIM written to APP13 when a write replaces it; a nil IIM
  leaves them. A trailer is found only right after EOI (FotoStation also right
  after the AFCP trailer) and only when consistent; otherwise it is copied
  unchanged. `TestTrailers_AFCPRewrite`, `TestTrailers_FollowedByOtherData`,
  `TestTrailers_FotoStationOnly`, `TestTrailers_Malformed`,
  `TestTrailers_Corpus`.
- **IIM-COPY-02** AFCP offsets (task #322; AFCP specification): the directory
  offsets and the EOF record are absolute file offsets. When the trailer moves
  they are rewritten for its position in the output, entries keep their order
  and data, reserved fields are zero; a write that moves nothing and changes no
  IPTC copy keeps the trailer byte for byte (CLAUDE.md §5). `TestTrailers_*`
  (`checkAFCPConsistent`), `TestTrailers_UnchangedIsByteIdentical`.
- **TIFF-AFCP-01** AFCP trailer of a TIFF (task #354, the policy of #322: update the copies): a TIFF write
  writes the AFCP trailer found at the end of the source after the last image block, with its `IPTC` data set
  equal to the IIM written to tag `0x83BB` (or kept when there is none) and its offsets and EOF record set for the
  position it takes in the output (ExifTool `ExifTool.pm` `IdentifyTrailer`/`ProcessTrailers`, study). The trailer
  must start after every image block that is copied: a candidate inside image data is image data. The shared logic
  lives in `internal/trailer`, used by `format/jpeg` and `format/tiff` (`writeRelocated`, so every TIFF-based
  write path). Bytes of a source that no IFD addresses are otherwise not carried over by a TIFF write.
  `TestTask354TIFFAFCPTrailerRewritten` (both byte orders, whole file and streamed).
- **TIFF-AFCP-02** FotoStation records at the end of a TIFF, before and after an AFCP trailer, have their IPTC record
  replaced. `TestTask354TIFFFotoStationTrailer`.
- **TIFF-AFCP-03** A nil IIM keeps the IPTC copy but still moves the offsets; a look-alike inside image data and an
  inconsistent trailer are not rewritten; a file without a trailer is written as before.
  `TestTask354TIFFTrailerEdgeCases`.
- **IIM-COPY-03** IPTC copy in the Nikon Capture data (task #322): the IPTC
  entry (`0x9EF5F6E0`) of MakerNote tag `0x0E01` of a NEF is rewritten with the
  IIM written to tag `0x83BB`: in place with zero padding when it fits its
  capacity (no size or offset changes), otherwise rebuilt and moved to the end
  of the MakerNote with the count and offset of `0x0E01` updated, the old bytes
  cleared. `TestRewriteNikonMakerNoteIPTC`, `TestNEF_NikonCaptureCorpus`.

## Section 5: Robustness Cases (MUST NOT crash)
- **ROBUST-01** Oversized standard length, value truncated by EOF → Truncated, rescan.
- **ROBUST-02** Extended n > 4 → reject dataset, Truncated, rescan.
- **ROBUST-03** Extended n=4 length > buffer (e.g. 0xFFFFFFFF) → skip, no 4 GiB alloc.
- **ROBUST-04** Standard length > remaining → partial or skip, no OOB.
- **ROBUST-05** Truncated extended length-byte block → skip, Truncated.
- **ROBUST-06** Record number outside 1–9 → skip, rescan, no panic.
- **ROBUST-07** Unknown dataset number → skip, continue parsing the rest.
- **ROBUST-08** Non-repeatable dataset twice → deterministic (first), no corruption.
- **ROBUST-09** Repeatable zero-length value (empty keyword) → empty string, no panic.
- **ROBUST-10** Aggregate stream > 256 MiB DoS guard → early terminate, Truncated, bounded mem.
- **ROBUST-11** APP13 missing/wrong "Photoshop 3.0" id → ignore segment.
- **ROBUST-12** 8BIM block wrong signature ("8BPS") → stop at bad block, no crash.
- **ROBUST-13** Pascal name length > remaining → terminate IRB parse, Truncated.
- **ROBUST-14** IRB data length 0xFFFFFFFF > buffer → Truncated, no 4 GiB read.
- **ROBUST-15** IPTC block in a non-first APP13 segment → must still be found. **(FINDING 4)**
- **ROBUST-16** TIFF tag `0x83BB` BYTE/UNDEFINED with genuine trailing `0x00` → do NOT strip. **(FINDING 2)**
- **ROBUST-17** Extended length n=0 (out of spec) → no length bytes read, skip/Truncated.
- **ROBUST-18** Value with embedded NUL ("Hello\0World") → return full value, no truncation at NUL.

## Sources
- IPTC-NAA IIM v4.2; IPTC Photo Metadata 2025.1; Adobe Photoshop File Formats Spec
- ExifTool TagNames/IPTC; exiv2.org IPTC + XMP-iptcCore/iptcExt tables
- MWG Guidelines v2.0; SixLabors/ImageSharp PR #1944; Exiv2 bug #533
