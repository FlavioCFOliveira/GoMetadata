---
name: project_webp_conformance_battery
description: WebP (RIFF) container conformance battery — task #158; spec coverage and test counts
metadata:
  type: project
---

WebP conformance battery implemented in `format/webp/conformance_test.go` (task #158).

**Why:** Project requires 100% spec conformance per CLAUDE.md; containers.md §3 is authoritative for WebP assertions.

**Coverage (44 top-level tests, some with sub-tests):**
- WebP-RIFF-header: §3(b) magic detection — positive + bad RIFF + bad brand + 4 too-short cases
- WebP-FileSize-semantics: §3(c) File-Size = bytes-after-size-field; mismatch lenient (no crash)
- WebP-chunk-layout: §3(c) Chunk-Size excludes FourCC/size/padding; zero-size legal
- WebP-odd-chunk-padding: §3(c) odd → exactly 1 x00 pad; missing-pad lenient; non-zero pad lenient
- WebP-VP8X-EXIF-flag / XMP-flag / both / cleared / reserved-zero / VP8X-required: §3(c)(e)
- WebP-XMP-fourcc-trailing-space: §3(d) "XMP " not "XMP\x00"; verified on extract+write
- WebP-EXIF-no-prefix: §3(d) raw TIFF, no Exif\0\0; verified on extract+write
- WebP-write-ChunkSize-exact / FileSize-updated / pad-iff-odd: §3(e)
- WebP-round-trip: EXIF, XMP, both, idempotent — §3(d)(e)
- WebP-robust-*: §3(f) — FileSize-mismatch, chunk-past-EOF, truncated-VP8X, flag-chunk-mismatch,
  duplicate-metadata, metadata-before-image, no-image-chunk, only-RIFF-header, garbage, preserve-false,
  JPEG-wire-frame
- WebP-corpus-*: extract-no-panic, inject-round-trip, header-valid, FileSize-field (skip if no corpus)

**No spec violations found** in `format/webp/` or `internal/riff/` — all 44 conformance assertions pass.

**How to apply:** Format for future WebP conformance tests; VP8X flags byte is a uint32 LE (bit3=EXIF, bit2=XMP); reserved mask = 0xFFFFFF41.
