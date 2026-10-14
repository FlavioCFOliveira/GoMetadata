---
name: project_sprint45_e4b_extxmp_aliasing
description: Sprint 45 E4b (2026-10-14) — extended XMP in JPEG (#278 GUID element form + tree merge via xmp.ParseExtended, #295 MD5 GUID, #315 xmpNote URI), #296 IFDEntry.Value read-only + 3-index caps, #245 docs; measured facts and traps
metadata:
  type: project
---

Uncommitted at report time on feature/45-open-source-contributions (base 544deae);
5 cumulative stage patches A(#315)→B(#278)→C(#295)→D(#296)→E(#245).

**Spec facts verified in Adobe XMP Spec Part 3 (2020) §1.1.3.1 p.13-14** (PDF in the
Toolkit clone `docs/XMPSpecificationPart3.pdf`, `pdftotext` works): ExtendedXMP MUST be
serialised without packet wrapper; GUID = MD5 of the full ExtendedXMP serialisation,
capital A-F; xmpNote URI = http://ns.adobe.com/xmp/note/; reader MUST recompose into one
tree and REMOVE xmpNote:HasExtendedXMP. All 9 real corpus files: GUID == MD5(ext) (oracle).
Pre-existing code cites "§1.1.4" for these — wrong section number.

**Design:** `xmp.ParseExtended(std, ext)` parses both element trees into ONE parse tree
EXTENDED FIRST (user decision 2026-10-14: extended value wins per whole property, as
Toolkit XMPUtils::MergeFromJPEG) so addChildNode's first-wins duplicate check gives the
replacement; about/xmptk saved from ext walk, reset, std walk, std preferred. Unlinks
HasExtendedXMP in both URIs, then `finish`. Byte-splice fallback now splices ext
Descriptions BEFORE main's first rdf:Description so the same policy holds there. Needs a second pooled rdfScratch for the ext xmlDoc (strings
alias each doc until finish). `ErrNoExtendedProperties` (counter `topSeen`) keeps the old
byte-splice fallback for fragment fixtures in jpeg tests. Only remaining divergence:
ext properties precede std ones in schema order; ext's prefix for unknown namespaces kept.

**Measured:** Read_JPEG_ExtendedXMP -17% ns, -22% allocs; Write_JPEG_ExtendedXMP +67%
(MD5 over ~130 KB, spec-mandated). #296 option B (clone rawEXIF at Read) costs +9..68%
ns, +45..91% B/op on Canon_40D/D810.nef/IMG_1361.dng/mri.tif and still can't fix two
entries sharing one OOL value → chose read-only contract + full slice expressions
(`b[a:b:b]`, free). Bug injection: 97 uncapped values; append to Make overwrote Model.

**Traps:** `git worktree` + symlinked testdata/corpus breaks exif TestEncodedSizeMatchesEncode
(WalkDir won't follow) → use `cp -cR` (APFS clone) and delete after. Fuzzers just
finished → load 30; re-run benches (external Firefox load also present).

**Reported, not fixed:** writer keeps stub-main partitioning (PackageForJPEG split
deferred by user to end of sprint) (readers without extended
support see no properties; spec §1.1.3.2 wants important data in main); SetCaption on
Google files creates IFD0 without YCbCrPositioning (exiftool warning, pre-existing);
"§1.1.4" citations; 2 KB padding not dropped before deciding to split.
