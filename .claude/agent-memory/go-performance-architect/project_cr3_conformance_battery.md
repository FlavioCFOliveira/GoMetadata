---
name: project_cr3_conformance_battery
description: CR3 specification-conformance test battery (task #163) — 53 new tests covering ISO BMFF + Canon CR3 spec in format/raw/cr3/conformance_test.go
metadata:
  type: project
---

CR3 conformance battery implemented at `format/raw/cr3/conformance_test.go` (task #163).

**Why:** Spec compliance mandate (CLAUDE.md); Canon CR3 is ISOBMFF-based (ftyp brand "crx "), not TIFF; unique CMT1/CMT2/CMT3/CMT4 box structure per lclevy canon_cr3.

**How to apply:** When adding new CR3 tests, follow this file's pattern: stable rule ID comments (e.g. `CR3-CMT2-exif-ifd`), cite ISO 14496-12 §section and containers.md §8.

**Coverage (53 new conformance tests + 2 corpus-parity skips):**
- BMFF-box-*: normal/largesize/size0/size2to7/pastEOF/exactHeader/truncated/largesizeTruncated (8 tests)
- BMFF-ftyp-*: crx  brand detection, brand position at [8:12] (2 tests)
- BMFF-uuid-*: layout, Canon UUID byte values (2 tests)
- BMFF-child-iter-bounded: child scan stops at parent boundary (1 test)
- CR3-CMT1-*: IFD0 extracted, big-endian, no Exif\0\0 prefix, missing→nil (4 tests)
- CR3-CMT2-*: ExifIFD merged, not merged when within, missing no error (3 tests)
- CR3-CMT3/CMT4-*: present does not corrupt CMT1 (2 tests); all CMT coexist (1 test)
- CR3-XMP-*: extracted, FourCC trailing space, absent→nil (3 tests)
- CR3-IPTC-nil: always nil (1 test)
- CR3-detect-*: no-moov error, empty input error (2 tests)
- CR3-write-*: moov size updated, preserve=false error, nil passthrough, stco/co64 relocated, no-relocate-before-moov, shrink delta<0, overflow detection (8 tests)
- CR3-round-trip-*: EXIF, XMP, both, replace existing XMP, image data preserved (5 tests)
- CR3-robust-*: malformed moov, missing UUID fallback, extra CMT boxes, truncated stream, size too small, uuid too short, deep nesting, no-moov inject passthrough, garbage input, duplicate ftyp (10 tests)
- CR3-corpus-*: no-panic + round-trip (2 corpus-parity tests, skip when corpus absent)

**Spec violations found:** None. The implementation was already correct.

**Lint issues encountered (resolved):**
- `copyloopvar`: loop variable copies redundant in Go 1.22+ — removed
- `gci`: gofmt canonical alignment must be exact — ran `gofmt -w`
- `makezero`: append to non-zero-initialized slice → use `make([]byte, 0, cap)`
- `nolintlint`: unused directives when gosec does not actually fire → remove speculative nolints
- `wrapcheck` on `os.Open` in test helper → nolint on the return line only
- `unparam`: helper with always-same param → changed to no-param function
- `gocritic appendCombine`: two consecutive appends → merge into one

[[project_heif_avif_conformance_battery]]
