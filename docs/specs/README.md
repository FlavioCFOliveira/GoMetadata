# Authoritative Specifications (Local Copies)

This directory holds local, offline copies of the authoritative EXIF, IPTC, and XMP
specifications that GoMetadata conforms to (see the compliance table in `CLAUDE.md`).
The copies exist for offline consultation while implementing and testing the library.

Every file in this directory except this `README.md` is ignored by git (`.gitignore`:
`docs/specs/*`, `!docs/specs/README.md`). The documents are copyrighted by their
publishers (Adobe, CIPA/JEITA, IPTC, ISO), and most of them may not be redistributed.
They must therefore never be committed to this public, MIT-licensed repository. Each
contributor obtains them from the official source listed below.

## Index

| Title | Version | File | Official URL | Licence/redistribution | SHA-256 | Status |
|---|---|---|---|---|---|---|
| Exchangeable image file format for digital still cameras: Exif Version 3.0 (CIPA DC-008-Translation) | DC-008-2023 / DC-008-2024 | — | https://www.cipa.jp/std/documents/download_e.html?CIPA_DC-008-2024-E | © CIPA/JEITA; download requires accepting the CIPA disclaimer; no redistribution grant | — | user-supplied: click-through |
| Exchangeable image file format for digital still cameras: Exif Version 2.32 (CIPA DC-X008-Translation) | DC-X008-2019 | — | https://www.cipa.jp/e/std/std-sec.html | © CIPA/JEITA; download requires accepting the CIPA disclaimer; no redistribution grant | — | user-supplied: click-through |
| Exchangeable image file format for digital still cameras: Exif Version 2.3 (CIPA DC-008-Translation) | DC-008-2012 (revised December 2012) | `CIPA_DC-008-2012_Exif-2.3.pdf` | https://www.cipa.jp/e/std/std-sec.html | © CIPA/JEITA; no redistribution grant | `6072847e70a6c7e32c636ee41225fdc620085ab6427bbadf3c036b07b97bf907` | present |
| IPTC-NAA Information Interchange Model | 4.2 (July 2014) | `IPTC_IIM-4.2_Information-Interchange-Model.pdf` | https://www.iptc.org/std/IIM/4.2/specification/IIMV4.2.pdf | © IPTC, rights reserved; IPTC licence terms (http://www.iptc.org/goto?license) | `d9c74e601daafe121553c4c110126065b02e104ba04cc8f8da1adccaf25af0e3` | present |
| IPTC Photo Metadata Standard (IPTC Core 1.5 / IPTC Extension 1.9) | 2025.1 rev 1 (2025-11-26) | `IPTC_PhotoMetadata-2025.1_Photo-Metadata-Standard_2025-11-26.html` | https://www.iptc.org/std/photometadata/specification/IPTC-PhotoMetadata-2025.1.html | © IPTC; Creative Commons Attribution 4.0 | `275a1717429f2208c608b92805d6fc7aebdebc3c1ed05d1f97373832d5913320` | present |
| XMP Specification Part 1: Data Model, Serialization, and Core Properties | April 2012 | `Adobe_XMP-Part1-2012_Data-Model-Serialization.pdf` | https://github.com/adobe/XMP-Toolkit-SDK/blob/main/docs/XMPSpecificationPart1.pdf | © Adobe, all rights reserved; no reproduction without written consent | `e0056b9d5c29d4d21eda1856cf947b54cee3013944638a9871aed7d67a949090` | present |
| XMP Specification Part 2: Additional Properties | 2022 revision | `Adobe_XMP-Part2-2022_Additional-Properties.pdf` | https://github.com/adobe/XMP-Toolkit-SDK/blob/main/docs/XMPSpecificationPart2.pdf | © Adobe, all rights reserved; no reproduction without written consent | `1749401a460699a4f5f69d74f6f4a619e94632d136584fd86d3dc3f59189f759` | present |
| XMP Specification Part 3: Storage in Files | January 2020 | `Adobe_XMP-Part3-2020_Storage-in-Files.pdf` | https://github.com/adobe/XMP-Toolkit-SDK/blob/main/docs/XMPSpecificationPart3.pdf | © Adobe, all rights reserved; no reproduction without written consent | `2e80d671f87501c91ab18c0722c233e35a72d1c9245c70accb743917ebe2eb18` | present |
| Guidelines for Handling Image Metadata (Metadata Working Group) | 2.0 (November 2010) | `MWG_Guidelines-2.0-2010_Handling-Image-Metadata.pdf` | https://web.archive.org/web/2012/http://www.metadataworkinggroup.org/pdf/mwg_guidance.pdf (archival copy of the original http://www.metadataworkinggroup.org/pdf/mwg_guidance.pdf, which is offline) | © 2008-2010 Adobe, Apple, Canon, Microsoft, Nokia, Sony; use under the document's own Terms and Conditions; no redistribution grant | `3c7cb408b816843b61696660047b01cd3908e2744584b9f71ff678b2ee142c15` | present |
| ISO 16684-1: Graphic technology — Extensible metadata platform (XMP) — Part 1: Data model, serialization and core properties | 2019 (edition 2) | — | https://www.iso.org/standard/75163.html | © ISO; paid licence, single user; no redistribution | — | user-supplied: paid |
| ISO 16684-2: Graphic technology — Extensible metadata platform (XMP) — Part 2: Description of XMP schemas using RELAX NG | 2014 (edition 1) | — | https://www.iso.org/standard/57422.html | © ISO; paid licence, single user; no redistribution | — | user-supplied: paid |

Notes:

- The Adobe XMP Specification Parts 2 and 3 published by Adobe are later revisions (2022
  and 2020) than the 2016 editions; the copies above are the current official editions.
- CIPA has published Exif 3.1 (CIPA DC-008-Translation-2026, 2026-01-30), which supersedes
  Exif 3.0. Exif 2.32 (DC-X008-2019) is no longer listed on the CIPA standards page.
- The IPTC Photo Metadata Standard 2025.1 is published as HTML only; no PDF edition exists.

## How to add a missing document

1. Download the document from the official URL in the table. Accept any licence or
   disclaimer yourself; purchase paid standards from the publisher.
2. Save it in this directory as `<publisher>_<doc-id>_<short-title>.<ext>`, for example
   `CIPA_DC-008-2024_Exif-3.0.pdf` or `ISO_16684-1-2019_XMP-Data-Model.pdf`.
3. Verify that the file is genuine (`file <name>` reports a PDF document) and compute its
   hash with `shasum -a 256 <name>`.
4. Update the corresponding table row: set File, SHA-256, and Status (`present`).
5. Confirm that git ignores the file: `git check-ignore -v docs/specs/<name>`.
