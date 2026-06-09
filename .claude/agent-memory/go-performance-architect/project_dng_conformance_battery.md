---
name: project_dng_conformance_battery
description: DNG specification-conformance test battery (task #161) — 37 tests covering §7 assertions from containers.md
metadata:
  type: project
---

DNG conformance battery added at `format/raw/dng/conformance_test.go` (task #161).

37 test functions across 6 categories:

- **DNG-detect** (4): DNGVersion 0xC612 LE/BE, TIFF-without-DNGVersion, DNGBackwardVersion 0xC613
- **DNG-IFD0** (3): NewSubFileType reduced-res, SubIFDs 0x014A LE/BE
- **DNG-metadata** (5): XMP 0x02BC LE/BE, IPTC 0x83BB, EXIF pointer 0x8769, GPS pointer 0x8825, XMP+IPTC combined
- **DNG-BigTIFF** (3): BigTIFF extract LE/BE, BigTIFF DNGVersion preserved
- **DNG-write** (6): round-trip XMP/IPTC/both, SubIFD chain preserved, IFD entries sorted, OOL word-aligned
- **DNG-robust** (14): DNGVersion malformed/absent, offset-past-EOF, count-overflow, truncated-tag-700, IFD cycle, empty input, truncated header, bad BOM, unknown magic, BigTIFF bad bytesize, next-IFD OOB, error-prefix, inject-error-prefix
- **DNG-corpus** (2): extract + round-trip over testdata/corpus/raw *.dng files (skip when absent)

**No spec violations found.** All tests pass; 0 lint issues; races clean.

**Why:** containers.md §7 DNG conformance contract requires full test coverage of §7(b)-(f).

**How to apply:** Fixture builders `buildDNG`, `buildDNGWithSubIFD`, `buildBigTIFFDNG`, `buildCyclicSubIFDDNG` in conformance_test.go are reusable patterns for future DNG-touching tests.
