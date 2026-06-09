---
name: project_tiff_conformance_battery
description: TIFF+BigTIFF container conformance test battery (task #156) — rules covered, all passing, 0 violations found
metadata:
  type: project
---

TIFF+BigTIFF conformance battery implemented in `format/tiff/conformance_test.go` (task #156).

**Why:** CLAUDE.md requires 100% spec conformance; task #156 targets the `format/tiff` container layer (not the exif package's CF-1 battery).

**51 sub-tests across 6 groups, all PASS (2 corpus SKIPs = directory absent):**

- S (structural/header): byteorder II/MM, magic 42 LE/BE, magic 43 bad-bytesize, unknown magic, IFD0 offset=0/past-EOF/odd, min-length <8, truncated-after-header, empty IFD (count=0), partial IFD count, OOL odd offset, next-IFD OOB, unsorted entries, inline threshold=4, OOL threshold=5, uint64 overflow guard
- BigTIFF: 16-byte header, 8-byte offsets LE+BE, bytesize≠8 rejected, reserved-field non-zero ignored, min-length <16, count>65535 capped, inline threshold=8, uint64 count overflow
- S-18/type-sizes: all classic types (1-12) + BigTIFF-specific LONG8/SLONG8/IFD8 (16-18)
- TIFF-01/02/03 (XMP): TypeByte write, no size limit, accept BYTE+UNDEFINED on read, no APP1 framing
- IPTC 0x83BB: TypeLong write, padding round-trip, TypeLong trailing-NUL trim, TypeByte/Undefined NOT trimmed (ROBUST-16)
- R (robustness): cyclic IFD no hang, overlapping IFDs, value-past-EOF, truncated mid-IFD, zero-count entry
- Write byte-correctness: IFD sorted ascending (S-12), OOL offsets word-aligned (S-11), full round-trip EXIF+XMP+IPTC, BE round-trip
- Corpus parity: exiftool BigTIFF_LE/BE/.btf (PASS), full corpus SKIP (absent), adversarial exiv2 SKIP (absent)

**No spec violations found in `format/tiff`** — all rules already implemented correctly.

**No new bugs fixed** — battery validates existing behaviour, no regressions introduced.

**How to apply:** When extending BigTIFF or TIFF write support, add corresponding conformance sub-tests to this file following the `TestConformance_*` naming convention with inline spec citations.
