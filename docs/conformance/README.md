# Specification-Conformance Test Contracts

This directory holds the **authoritative normative-requirements checklists** that drive
GoMetadata's specification-conformance test battery. Each checklist enumerates the
MUST/SHALL rules of an official specification as concrete, testable assertions, with the
spec section cited for every rule, and a note on how to construct a minimal byte fixture.

These documents are the **contract** for the conformance tests. Each rule maps to one or
more table-driven test cases. Rule identifiers (e.g. `S-08`, `IIM-BIN-05`, `JPEG-04`,
`ROB-03`) are used verbatim as Go test/sub-test names so that a failing test points
directly back to the violated specification clause.

## Project requirement: 100% specification compliance

GoMetadata targets **100% conformance** with the official specification of every metadata
format and every container format it supports (see `CLAUDE.md` → "Strict specification
compliance"). The battery proves correctness against the spec, not merely code coverage.

## Checklists

| File | Scope | Primary specifications |
|---|---|---|
| [`exif-tiff.md`](exif-tiff.md) | EXIF, TIFF 6.0, BigTIFF | CIPA DC-008 (Exif 3.0) / DC-X008 (Exif 2.32) / JEITA CP-3451; Adobe TIFF 6.0; BigTIFF |
| [`iptc.md`](iptc.md) | IPTC IIM, IPTC Core/Extension, APP13/8BIM IRB | IPTC-NAA IIM 4.2; IPTC Photo Metadata 2025.1; Adobe Photoshop File Formats |
| [`xmp.md`](xmp.md) | XMP data model, serialization, embedding, MWG reconciliation | ISO 16684-1:2019 / 16684-2:2014; Adobe XMP Parts 1–3; MWG v2.0 |
| [`containers.md`](containers.md) | JPEG, PNG, WebP, ISO BMFF, HEIF, AVIF, DNG, TIFF/EP, proprietary RAW | ITU-T T.81/T.871; W3C PNG 3rd ed.; RFC 9649; ISO/IEC 14496-12, 23008-12; AOM AVIF; Adobe DNG 1.7.1.0; ISO 12234-2 |

## How the battery is organised

- Each format has a dedicated `*_conformance_test.go` suite in the package that implements it.
- Tests are **table-driven**, **deterministic**, and contain **no `t.Skip`** (project policy).
- Each test asserts one or more of: structural conformance, positive parse correctness,
  negative/robustness graceful-degradation (never a panic), write byte-correctness
  (offsets, alignment, padding, ordering, length fields, checksums), and round-trip fidelity.
- Where a real-world corpus exists for the format, corpus-parity tests reinforce the
  synthetic spec-fixture tests.

## Provenance

The checklists were compiled from the official specifications and authoritative
reverse-engineering references (ExifTool, LibRaw, Exiv2, libheif/libavif, lclevy CR2/CR3).
Each checklist lists its sources. When the source is a proprietary, reverse-engineered
format, the most authoritative public reference is cited in place of a formal standard.
