---
name: project_testing_fixtures
description: testdata/fixtures/ strategy — committed fixtures + TestMain generator for Example tests that don't need corpus
metadata:
  type: project
---

Critical test paths (Example tests, top-level API integration tests) use committed fixtures in `testdata/fixtures/`. The corpus at `testdata/corpus/` is gitignored; tests using corpus files are all guarded with conditional `t.Skipf`.

**Committed fixtures:**
- `testdata/fixtures/BigTIFF_LE.tif` and `BigTIFF_BE.tif` (4744 bytes each) — also duplicated in `exif/testdata/`
- `testdata/fixtures/Canon_40D.jpg` (7.9 KB) — has Make/Model/Flash; no IPTC
- `testdata/fixtures/exif-samples-11-tests.jpg` — synthetic JPEG with Make=Canon, ColorSpace=sRGB, ExposureTime, FNumber, FocalLength, DateTimeOriginal
- `testdata/fixtures/canon_hdr_YES.jpg` — synthetic JPEG with Orientation=6
- `testdata/fixtures/jolla.jpg` — synthetic JPEG with WhiteBalance=1 (manual)
- `testdata/fixtures/67-0_length_string.jpg` — synthetic JPEG with GPS altitude=340m
- `testdata/fixtures/IPTC-PhotometadataRef-Std2021.1.jpg` — synthetic JPEG with IPTC+XMP copyright/caption/keywords

**Generator:** `testmain_test.go` → `TestMain` → `generateFixtures()`. Idempotent: skips existing files. Hand-crafted binary TIFF payloads are used for tags not settable via the public API (ColorSpace, WhiteBalance, GPS altitude).

**Why:** The `go test ./...` pipeline must pass in CI without downloading the corpus. Validated by temporarily hiding `testdata/corpus/` and confirming all 25 packages pass.

**How to apply:** When adding a new Example function: check if an existing fixture has the needed tag; if not, add a new `buildFixtureXxx()` to testmain_test.go and register it in `generateFixtures()`.
