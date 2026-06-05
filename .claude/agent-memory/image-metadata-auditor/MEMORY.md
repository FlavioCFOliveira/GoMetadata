# Memory Index

- [Test corpus sources](reference_test_corpus.md) — 12 public repos/sources; confirmed file lists, download URLs, and edge-case coverage per source
- [Corpus gap analysis](project_corpus_gaps.md) — Identified gaps resolved in download.sh: progressive JPEG, XMP sidecar, multi-page TIFF, BigTIFF endianness, exotic RAW formats, MakerNote variants, XMP-only
- [GoMetadata real-world robustness audit 2026-06](project_robustness_audit.md) — Six-area audit vs ExifTool/Exiv2/libexif: makernote offsets, format coverage, IFD1 thumbnail, encoding edge cases, write fidelity, MWG reconciliation
- [Reliability fragility audit 2026-06](project_reliability_audit_2026-06.md) — 6 confirmed findings: iobuf pool orphan (JPEG), IPTC Dataset race, XMP UTF-32 no size cap, XMP char-ref overflow, IFD uint32 wrap, HEIF rawXMP aliasing
