---
name: project_bigtiff_read
description: BigTIFF read — task #54 fully closed across 3 commits; exif.Parse now BigTIFF-aware (941c3d1)
metadata:
  type: project
---

Task #54 is fully closed across three commits: 803070e, 8f5752d, 941c3d1.

**Commit 941c3d1 (final — the CORE)** — BigTIFF-aware exif.Parse with parameterised IFD traversal:

The first two commits added a side BigTIFF reader in `format/tiff` and fixed format detection, but `exif.Parse` still hard-rejected magic 0x002B, so ALL EXIF tags were silently dropped from BigTIFF files.

**What 941c3d1 adds:**
- `exif/exif.go`: `Parse()` now switches on TIFF magic — 0x002A = classic path (byte-for-byte unchanged), 0x002B = BigTIFF path with 16-byte header + 8-byte IFD offsets. Added `parseExifSubIFDsBigTIFF` and `parseGPSSubIFDBigTIFF` using BigTIFF traversal.
- `exif/ifd.go`: full BigTIFF IFD traversal stack:
  - `parseIFDEntryBigTIFF` — 20-byte entries (2+2+8+8), inline threshold=8, anti-DoS capped at maxBigTIFFCount=2^30
  - `parseSingleIFDBigTIFF` — parses single BigTIFF IFD, bigTIFFMaxEntries=65535 cap
  - `traverseBigTIFF` — iterative chain walk with uint64 cycle detection via `visitedPoolBigTIFF` (recycled sync.Pool)
  - `readBigTIFFSubIFDOffset` — handles TypeLong (4-byte, used by tiffcp) and TypeLong8/TypeIFD8 (8-byte) sub-IFD pointers
- `exif/type.go`: BigTIFF-only types TypeLong8(16)/TypeSLong8(17)/TypeIFD8(18) + `typeSizeBigTIFF()`. `typeSize()` returns 0 for these (BigTIFF-only contexts use `typeSizeBigTIFF`).
- `exif/bigtiff_test.go`: 17 unit tests + 1 benchmark. `BenchmarkParseBigTIFF_Simple` ~175 ns/op, 4 allocs (classic: 153 ns/op, 4 allocs — zero regression on alloc count).
- 6+7 BigTIFF fuzz seeds added to `exif/fuzz_test.go` and root `fuzz_test.go`.
- `read_test.go`: `TestReadBigTIFF` uses real tiffcp-generated fixtures with hard assertions (t.Fatal) — Make=Canon, Model=Canon EOS DIGITAL REBEL, Caption=The picture caption.

**Fixtures (committed in 803070e):**
- `testdata/corpus/tiff/exiftool/BigTIFF_LE.tif` — `tiffcp -8 -L ExifTool.tif` (4744 bytes)
- `testdata/corpus/tiff/exiftool/BigTIFF_BE.tif` — `tiffcp -8 -B ExifTool.tif` (4744 bytes)
  IPTC is not preserved by tiffcp; only EXIF tags are present.

**Evidence:**
- classic JPEG: Make=Canon, Model=Canon EOS DIGITAL REBEL
- BigTIFF LE: Make=Canon, Model=Canon EOS DIGITAL REBEL
- BigTIFF BE: Make=Canon, Model=Canon EOS DIGITAL REBEL
- 25/25 packages pass `go test -race ./...`
- `golangci-lint run ./...`: 0 issues
- `staticcheck ./...`: clean
- `govulncheck ./...`: no vulnerabilities
- FuzzParseEXIF 30s/10M+ execs: no crash
- FuzzRead 20s/2.8M+ execs: no crash

**Why:** BigTIFF magic 0x002B was hard-rejected at line ~161 of exif.go. The parser needed parameterised traversal for 64-bit field widths and an 8-byte inline threshold (vs 4 in classic TIFF). The inline threshold difference is critical: tiffcp stores "Canon\x00" (6 bytes) INLINE in the BigTIFF value-or-offset field, not as an OOL offset — this required correct decoding in `parseIFDEntryBigTIFF`.

**How to apply:** BigTIFF write is NOT implemented — Encode/serialise only handles classic 0x002A. Do not attempt to write BigTIFF files; the writer will produce a classic TIFF header. Sub-IFD pointers in BigTIFF may be TypeLong (4-byte, tiffcp) or TypeLong8/TypeIFD8 (8-byte); always use `readBigTIFFSubIFDOffset` to read them.

---

**Commit 803070e** added BigTIFF read to `format/tiff/tiff.Extract` (internal only).
**Commit 8f5752d** fixed public-API routing: `format.Detect` now recognises 0x002B and routes through `FormatTIFF → tiff.Extract`.
EOF
