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
  **(Known gap, audit 2026-06-09 FINDING 3: Encode may not emit 2:00 — this test must drive a fix.)**
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
- **IRB-APP13-09** Multiple APP13 segments: concatenate all "Photoshop 3.0" payloads. **(Known gap, FINDING 4: last-segment-wins must be fixed.)**

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

- **RECONCILE-01** Dual-write: write both IIM and XMP with equivalent content.
- **RECONCILE-02** Read priority: XMP > IIM on conflict. — MWG §2.
- **RECONCILE-03** Split XMP photoshop:DateCreated into 2:55 (CCYYMMDD) + 2:60 (HHMMSS±HHMM); drop sub-seconds on IIM write.
- **RECONCILE-04** Preserve By-line order into dc:creator seq.
- **RECONCILE-05** Lang Alt ↔ IIM string via the `x-default` key.

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
