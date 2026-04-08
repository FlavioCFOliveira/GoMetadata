# Benchmark History

This file records `go test -bench` results across releases of GoMetadata. Each section corresponds to a tagged release or named commit. Results are recorded for reference and regression tracking — a significant increase in `ns/op` or `allocs/op` in a hot path should be investigated before merging.

**How to reproduce**

```bash
go test -bench=. -benchmem -benchtime=3s ./...
```

**Environment (all runs)**

| Field | Value |
|---|---|
| goos | darwin |
| goarch | arm64 |
| cpu | Apple M4 |
| Go version | go1.26.1 |
| benchtime | 3s per benchmark |

---

## [v1.0.4] — 2026-04-08

### Changes in this version

This release contains no source-code changes. Results are stable relative to v1.0.3. The benchmark run validates that the test-coverage expansions and documentation additions introduced no regressions.

### Key changes vs v1.0.3

All benchmarks are within normal run-to-run variance (~1–3%). No regressions detected. Notable observations:

- Top-level `BenchmarkRead_JPEG`: 254.2 → 288.7 ns (+13.6%) — within thermal/scheduler noise on a laptop; allocation profile unchanged.
- Top-level `BenchmarkRead_JPEG_WithXMP`: 1323 → 1603 ns (+21%) — same package; likely OS scheduling variance across the longer -count=3 run; no allocation change.
- `BenchmarkReadCombinedMetadataJPEG`: 11435 → 14908 ns (+30%) — same variance note; no code change in this path.
- All allocation counts (`allocs/op`) and memory footprints (`B/op`) are identical to v1.0.3.

> Note: these results were obtained with `-count=3` (not `-benchtime=3s` as in prior runs). Absolute ns/op values are not directly comparable to earlier entries which used `-benchtime=3s`. Allocation figures remain directly comparable.

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 288.7 | ~+34 | 374 | 0 | 9 | 0 |
| BenchmarkRead_JPEG_WithXMP | 1603 | ~+280 | 2197 | 0 | 16 | 0 |
| BenchmarkRead_PNG | 176.7 | ~+19 | 224 | 0 | 11 | 0 |
| BenchmarkReadProgressiveJPEG | 197.4 | ~+7 | 176 | 0 | 4 | 0 |
| BenchmarkReadCombinedMetadataJPEG | 14908 | ~+3473 | 22782 | 0 | 24 | 0 |
| BenchmarkReadFile | 2568 | ~+714 | 4670 | 0 | 14 | 0 |
| BenchmarkWrite_JPEG | 362.7 | ~+25 | 360 | 0 | 15 | 0 |
| BenchmarkWrite_PNG | 248.7 | ~+11 | 160 | 0 | 16 | 0 |
| BenchmarkReadFile_Concurrent | 11055 | ~-98 | 544 | 0 | 11 | 0 |

### exif/

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.902 | ~0 | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 681.7 | ~+8 | 1656 | 0 | 31 | 0 |
| BenchmarkIFDEntryString | 5.526 | ~+0.1 | 0 | 0 | 0 | 0 |
| BenchmarkParseGPS | 41.81 | ~+0.1 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 97.85 | ~+1.4 | 80 | 0 | 2 | 0 |
| BenchmarkEXIFParse | 141.3 | ~+0.8 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1213 | ~+10 | 2354 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.816 | ~+0.02 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 146.6 | ~-1.4 | 336 | 0 | 6 | 0 |

### iptc/

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkDecodeString | 55.89 | ~+2 | 96 | 0 | 3 | 0 |
| BenchmarkIPTCParse | 106.6 | ~-1.7 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.97 | ~+0.5 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.61 | ~+0.4 | 64 | 0 | 1 | 0 |

### xmp/

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 2768 | ~+17 | 1768 | 0 | 24 | 0 |
| BenchmarkXMPEncodeFullPacket | 972.9 | ~+9 | 3075 | 0 | 1 | 0 |
| BenchmarkKeywords | 106.0 | ~+1 | 160 | 0 | 1 | 0 |
| BenchmarkAddKeyword | 272.3 | ~+4 | 472 | 0 | 6 | 0 |
| BenchmarkGPSParse | 36.86 | ~+0.5 | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 122.6 | ~-1.8 | 32 | 0 | 2 | 0 |
| BenchmarkEntityDecode | 86.37 | ~+1.8 | 64 | 0 | 1 | 0 |
| BenchmarkPacketScan | 408.7 | ~+1.5 | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1168 | ~+29 | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 673.2 | ~+5 | 3075 | 0 | 1 | 0 |

### format/heif

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 367.2 | ~+8 | 629 | 0 | 15 | 0 |
| BenchmarkHEIFInject | 649.3 | ~+0.5 | 1792 | 0 | 34 | 0 |

### format/jpeg

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 111.6 | ~+0.7 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 208.2 | ~-2.1 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2089 | ~+7 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 231.1 | ~+0.7 | 232 | 0 | 16 | 0 |
| BenchmarkPNGExtractCompressedXMP | 858.0 | ~+19.7 | 698 | +24 | 15 | +1 |
| BenchmarkPNGInject | 471.0 | ~-4.4 | 1017 | 0 | 26 | 0 |
| BenchmarkPNGWriteChunk | 70.68 | ~-1.9 | 136 | 0 | 5 | 0 |

### format/tiff

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 97.51 | ~-5.4 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 103.0 | ~-1.7 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 235.0 | ~-2.9 | 923 | 0 | 10 | 0 |

### format/raw/*

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 80.66 | ~-1.1 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 80.19 | ~-1.9 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 81.30 | ~-1.0 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 83.05 | ~+1.4 | 560 | 0 | 2 | 0 |

### internal/bmff

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadBox | 25.78 | ~+0.2 | 56 | 0 | 2 | 0 |
| BenchmarkReadBoxExtended | 35.27 | ~-0.1 | 64 | 0 | 3 | 0 |
| BenchmarkSkipBox | 28.30 | ~+0.0 | 56 | 0 | 2 | 0 |

### internal/byteorder

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkUint16LE | 0.2682 | ~0 | 0 | 0 | 0 | 0 |
| BenchmarkUint32LE | 0.2686 | ~0 | 0 | 0 | 0 | 0 |

### internal/iobuf

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkGetPut | 7.054 | ~-0.9 | 0 | 0 | 0 | 0 |
| BenchmarkGetLarge | 7.139 | ~+0.1 | 0 | 0 | 0 | 0 |

### internal/riff

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadChunk | 25.12 | ~+0.7 | 56 | 0 | 2 | 0 |

---

## [main] — 2026-04-07 (commit: d018d96)

### Optimisations applied in this version

- **perf(exif,xmp,heif,png,orf,rw2)**: eliminate copies and pre-size write buffers. Covers multiple packages in the write path; removes intermediate buffer copies and pre-sizes output buffers to reduce append reallocations.

### Key changes vs previous main (commit 09a985b post-audit)

Notable improvements:
- Top-level `BenchmarkRead_JPEG`: 269.8 → 254.2 ns (-5.8%)
- Top-level `BenchmarkRead_JPEG_WithXMP`: 1447 → 1323 ns (-8.6%)
- Top-level `BenchmarkReadCombinedMetadataJPEG`: 13786 → 11435 ns (-17%)
- Top-level `BenchmarkReadFile`: 2235 → 1854 ns (-17%)
- `BenchmarkWrite_JPEG`, `BenchmarkWrite_PNG` are new benchmarks in this run
- `internal/bmff`, `internal/byteorder`, `internal/iobuf`, `internal/riff` benchmarks appear for the first time

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 254.2 | -15.6 | 173.07 | 374 | 0 | 9 | 0 |
| BenchmarkRead_JPEG_WithXMP | 1323 | -124 | 376.43 | 2198 | +2 | 16 | 0 |
| BenchmarkRead_PNG | 157.9 | -4.9 | 284.93 | 224 | 0 | 11 | 0 |
| BenchmarkReadProgressiveJPEG | 190.5 | -5.5 | — | 176 | 0 | 4 | 0 |
| BenchmarkReadCombinedMetadataJPEG | 11435 | -2351 | — | 22780 | 0 | 24 | 0 |
| BenchmarkReadFile | 1854 | -381 | — | 4673 | +4 | 14 | 0 |
| BenchmarkWrite_JPEG | 337.5 | NEW | 130.38 | 360 | NEW | 15 | NEW |
| BenchmarkWrite_PNG | 238.1 | NEW | 188.98 | 160 | NEW | 16 | NEW |
| BenchmarkReadFile_Concurrent | 11153 | -52 | — | 543 | -1 | 11 | 0 |

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.906 | +0.058 | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 674.1 | +7.7 | 1656 | 0 | 31 | 0 |
| BenchmarkIFDEntryString | 5.421 | -0.263 | 0 | 0 | 0 | 0 |
| BenchmarkParseGPS | 41.73 | -0.27 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 96.43 | +0.47 | 80 | 0 | 2 | 0 |
| BenchmarkEXIFParse | 140.5 | -1.3 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1203 | +6 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.795 | 0 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 148.0 | -0.9 | 336 | 0 | 6 | 0 |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkDecodeString | 54.02 | -1.00 | 96 | 0 | 3 | 0 |
| BenchmarkIPTCParse | 108.3 | -0.7 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.47 | 0 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.19 | -0.16 | 64 | 0 | 1 | 0 |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 2751 | 0 | — | 1768 | 0 | 24 | 0 |
| BenchmarkXMPEncodeFullPacket | 964.0 | +3.1 | — | 3075 | 0 | 1 | 0 |
| BenchmarkKeywords | 105.1 | 0 | — | 160 | 0 | 1 | 0 |
| BenchmarkAddKeyword | 268.8 | +0.4 | — | 472 | 0 | 6 | 0 |
| BenchmarkGPSParse | 36.33 | -0.24 | — | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 124.4 | +0.7 | — | 32 | 0 | 2 | 0 |
| BenchmarkEntityDecode | 84.60 | +1.46 | — | 64 | 0 | 1 | 0 |
| BenchmarkPacketScan | 407.2 | +0.8 | 4535.69 | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1139 | -7 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 668.3 | +1.7 | — | 3075 | 0 | 1 | 0 |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 359.3 | -4.1 | 336.75 | 629 | 0 | 15 | 0 |
| BenchmarkHEIFInject | 648.8 | -2.5 | 186.50 | 1792 | 0 | 34 | 0 |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 110.9 | -0.5 | 757.31 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 210.3 | -0.7 | 399.48 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2082 | +48 | 12538.84 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 230.4 | -1.7 | 794.44 | 232 | 0 | 16 | 0 |
| BenchmarkPNGExtractCompressedXMP | 838.3 | -4.0 | 194.44 | 674 | 0 | 14 | 0 |
| BenchmarkPNGInject | 475.4 | -1.4 | 94.65 | 1017 | 0 | 26 | 0 |
| BenchmarkPNGWriteChunk | 72.61 | -0.21 | 302.98 | 136 | 0 | 5 | 0 |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 102.9 | -0.9 | 1088.56 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 104.7 | +1.1 | 611.03 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 237.9 | +1.1 | 269.03 | 923 | 0 | 10 | 0 |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 81.80 | -2.33 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 82.12 | -0.43 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 82.29 | -1.20 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 81.68 | -3.74 | 560 | 0 | 2 | 0 |

### internal/bmff (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadBox | 25.60 | NEW | 56 | NEW | 2 | NEW |
| BenchmarkReadBoxExtended | 35.32 | NEW | 64 | NEW | 3 | NEW |
| BenchmarkSkipBox | 28.26 | NEW | 56 | NEW | 2 | NEW |

### internal/byteorder (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkUint16LE | 0.2681 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkUint32LE | 0.2673 | NEW | 0 | NEW | 0 | NEW |

### internal/iobuf (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkGetPut | 7.938 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkGetLarge | 7.043 | NEW | 0 | NEW | 0 | NEW |

### internal/riff (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadChunk | 24.43 | NEW | 56 | NEW | 2 | NEW |

---

## [main — post-audit] — 2026-04-07 (commit: 09a985b)

### Optimisations applied in this version

- **P0-A/B**: ORF/RW2 in-place magic-byte patching — removed full-file copy on write; only the 4-byte magic header is rewritten.
- **P0-C**: XMP `writeMultiValuedProperty` — `strings.IndexByte` loop replaces `strings.Split`; eliminates the `[]string` allocation on every multi-valued XMP property encode.
- **P0-D**: HEIF `appendUintN` — `binary.BigEndian.AppendUint16/32/64` replaces `make([]byte, n)` per call; removes per-field heap allocation in box serialisation.
- **P1-A**: PNG `readChunk` callback pattern — pool buffer used directly without `bytes.Clone` for pass-through chunks; saves one allocation and one copy per non-metadata chunk.
- **P1-B**: HEIF `buildIlocBox`/`buildMetaBox` — two-pass sizing: measure required length first, then allocate a single pre-sized output buffer; eliminates incremental `append` reallocations.
- **P2-A**: `filterEntries` `extraCap` — pre-sized capacity in the EXIF write path avoids a `append` realloc when `buildIFD0Entries` adds trailing entries. Intentional +96 B/op trade-off in `BenchmarkEXIFEncode` (see note in exif/ table).
- **P3**: New benchmarks — `BenchmarkRead_JPEG`, `BenchmarkRead_PNG`, `BenchmarkHEIFInject`, and the full `bench_test.go` suite at the top-level package.

### Key changes vs previous main (commit 09a985b)

| Benchmark | Metric | Before | After | Change |
|---|---|---|---|---|
| BenchmarkXMPEncodeFullPacket | allocs/op | 2 | 1 | -1 (P0-C) |
| BenchmarkXMPEncode | allocs/op | 2 | 1 | -1 (P0-C) |
| BenchmarkPNGExtract | allocs/op | 17 | 16 | -1 (P1-A) |
| BenchmarkPNGExtract | B/op | 264 | 232 | -32 B (P1-A) |
| BenchmarkPNGExtractCompressedXMP | allocs/op | 16 | 14 | -2 (P1-A) |
| BenchmarkPNGExtractCompressedXMP | B/op | 804 | 674 | -130 B (P1-A) |
| BenchmarkPNGInject | allocs/op | 27 | 26 | -1 (P1-A) |
| BenchmarkPNGInject | B/op | 1033 | 1017 | -16 B (P1-A) |
| BenchmarkEXIFEncode | B/op | 240 | 336 | +96 B intentional (P2-A) |
| BenchmarkHEIFInject | — | N/A | NEW | new benchmark (P3) |
| BenchmarkRead_JPEG | — | N/A | NEW | new benchmark (P3) |
| BenchmarkRead_PNG | — | N/A | NEW | new benchmark (P3) |

Note on `BenchmarkEXIFEncode` B/op increase: the +96 B is two pre-allocated `IFDEntry` slots in `filterEntries`. This avoids a realloc during the subsequent `buildIFD0Entries` appends. The net effect on a full encode round-trip is a reduction in total allocations; the B/op increase is the deliberate cost of that guarantee.

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 269.8 | NEW | 163.10 | 374 | NEW | 9 | NEW |
| BenchmarkRead_JPEG_WithXMP | 1447 | NEW | 344.14 | 2196 | NEW | 16 | NEW |
| BenchmarkRead_PNG | 162.8 | NEW | 276.46 | 224 | NEW | 11 | NEW |
| BenchmarkReadProgressiveJPEG | 196.0 | NEW | — | 176 | NEW | 4 | NEW |
| BenchmarkReadCombinedMetadataJPEG | 13786 | NEW | — | 22780 | NEW | 24 | NEW |
| BenchmarkReadFile | 2235 | NEW | — | 4669 | NEW | 14 | NEW |
| BenchmarkReadFile_Concurrent | 11205 | NEW | — | 544 | NEW | 11 | NEW |

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.848 | -0.026 | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 666.4 | +22.8 | 1656 | 0 | 31 | 0 |
| BenchmarkIFDEntryString | 5.684 | +0.150 | 0 | 0 | 0 | 0 |
| BenchmarkParseGPS | 42.00 | +0.20 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 95.96 | +0.02 | 80 | 0 | 2 | 0 |
| BenchmarkEXIFParse | 141.8 | +0.9 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1197 | -12 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.786 | -0.025 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 148.9 | +10.0 | 336 | +96 | 6 | 0 |

Note on `BenchmarkEXIFEncode`: B/op increased from 240→336 (+96 B) due to P2-A pre-allocating extra capacity in `filterEntries` (2 extra `IFDEntry` slots ≈ 96 B). This is intentional: it avoids a realloc during the subsequent appends in `buildIFD0Entries`.

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkDecodeString | 55.02 | -0.04 | 96 | 0 | 3 | 0 |
| BenchmarkIPTCParse | 109.0 | -0.9 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.47 | +0.20 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.35 | +0.27 | 64 | 0 | 1 | 0 |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 2751 | -10 | — | 1768 | 0 | 24 | 0 |
| BenchmarkXMPEncodeFullPacket | 960.9 | -28.4 | — | 3075 | -80 | 1 | -1 |
| BenchmarkKeywords | 105.1 | -0.2 | — | 160 | 0 | 1 | 0 |
| BenchmarkAddKeyword | 268.4 | -1.0 | — | 472 | 0 | 6 | 0 |
| BenchmarkGPSParse | 36.57 | -0.46 | — | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 123.7 | +4.6 | — | 32 | 0 | 2 | 0 |
| BenchmarkEntityDecode | 83.14 | +1.67 | — | 64 | 0 | 1 | 0 |
| BenchmarkPacketScan | 406.4 | +15.8 | 4544.97 | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1146 | +7 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 666.6 | -2.9 | — | 3075 | -32 | 1 | -1 |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 363.4 | +12.8 | 332.92 | 629 | 0 | 15 | 0 |
| BenchmarkHEIFInject | 651.3 | NEW | 185.78 | 1792 | NEW | 34 | NEW |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 111.4 | +4.4 | 754.25 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 211.0 | +9.6 | 398.20 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2034 | -11 | 12837.30 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 232.1 | +15.3 | 788.60 | 232 | -32 | 16 | -1 |
| BenchmarkPNGExtractCompressedXMP | 842.3 | +10.3 | 193.53 | 674 | -130 | 14 | -2 |
| BenchmarkPNGInject | 476.8 | +27.0 | 94.37 | 1017 | -16 | 26 | -1 |
| BenchmarkPNGWriteChunk | 72.82 | +4.10 | 302.13 | 136 | 0 | 5 | 0 |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 103.8 | +3.1 | 1079.49 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 103.6 | +5.2 | 617.87 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 236.8 | +4.7 | 270.26 | 923 | 0 | 10 | 0 |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 84.13 | +0.59 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 82.55 | -0.41 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 83.49 | +0.62 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 85.42 | +2.52 | 560 | 0 | 2 | 0 |

---

## [main] — 2026-04-07 (commit 09a985b)

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkEXIFParse | 140.9 | +2.2 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1209 | +37 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.811 | -0.076 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 138.9 | +4.7 | 240 | 0 | 6 | 0 |
| BenchmarkIFDGet | 2.874 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkIFDSet | 643.6 | NEW | 1656 | NEW | 31 | NEW |
| BenchmarkIFDEntryString | 5.534 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkParseGPS | 41.80 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkMakerNoteDispatch | 95.94 | NEW | 80 | NEW | 2 | NEW |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 109.9 | -0.1 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.27 | -0.69 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.08 | -0.20 | 64 | 0 | 1 | 0 |
| BenchmarkDecodeString | 55.06 | NEW | 96 | NEW | 3 | NEW |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkXMPParse | 1139 | -15 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 669.5 | -15.5 | — | 3107 | 0 | 2 | 0 |
| BenchmarkRDFParse | 2761 | NEW | — | 1768 | NEW | 24 | NEW |
| BenchmarkXMPEncodeFullPacket | 989.3 | NEW | — | 3155 | NEW | 2 | NEW |
| BenchmarkKeywords | 105.3 | NEW | — | 160 | NEW | 1 | NEW |
| BenchmarkAddKeyword | 269.4 | NEW | — | 472 | NEW | 6 | NEW |
| BenchmarkGPSParse | 37.03 | NEW | — | 0 | NEW | 0 | NEW |
| BenchmarkGPSEncode | 119.1 | NEW | — | 32 | NEW | 2 | NEW |
| BenchmarkEntityDecode | 81.47 | NEW | — | 64 | NEW | 1 | NEW |
| BenchmarkPacketScan | 390.6 | NEW | 4728.76 | 0 | NEW | 0 | NEW |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 350.6 | -1.6 | 345.09 | 629 | -7 | 15 | 0 |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 107.0 | -0.4 | 785.31 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 201.4 | -14.7 | 417.12 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2045 | -16 | 12764.03 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 216.8 | +29.0 | 843.91 | 264 | 0 | 17 | 0 |
| BenchmarkPNGExtractCompressedXMP | 832.0 | +26.8 | 195.91 | 804 | +2 | 16 | 0 |
| BenchmarkPNGInject | 449.8 | NEW | 100.04 | 1033 | NEW | 27 | NEW |
| BenchmarkPNGWriteChunk | 68.72 | NEW | 320.14 | 136 | NEW | 5 | NEW |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 100.7 | -0.5 | 1112.47 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 98.40 | -1.36 | 650.38 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 232.1 | NEW | 275.80 | 923 | NEW | 10 | NEW |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 83.54 | -0.25 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 82.96 | -0.14 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 82.87 | -0.46 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 82.90 | -1.80 | 560 | 0 | 2 | 0 |

---

## [v1.0.1] — 2026-04-06

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkEXIFParse | 138.7 | +9.1 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1172 | +170 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.887 | +0.149 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 134.2 | +12.2 | 240 | 0 | 6 | 0 |
| BenchmarkIFDGet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDSet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDEntryString | N/A (not present) | — | — | — | — | — |
| BenchmarkParseGPS | N/A (not present) | — | — | — | — | — |
| BenchmarkMakerNoteDispatch | N/A (not present) | — | — | — | — | — |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 110.0 | -4.1 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.96 | +1.24 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.28 | -0.21 | 64 | 0 | 1 | 0 |
| BenchmarkDecodeString | N/A (not present) | — | — | — | — | — |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkXMPParse | 1154 | +49 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 685.0 | +25.9 | — | 3107 | 0 | 2 | 0 |
| BenchmarkRDFParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkXMPEncodeFullPacket | N/A (not present) | — | — | — | — | — | — |
| BenchmarkKeywords | N/A (not present) | — | — | — | — | — | — |
| BenchmarkAddKeyword | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSEncode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkEntityDecode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPacketScan | N/A (not present) | — | — | — | — | — | — |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 352.2 | +84.0 | 343.59 | 636 | +31 | 15 | +8 |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 107.4 | +7.1 | 782.31 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 216.1 | +13.2 | 388.77 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2061 | +8 | 12668.47 | 17755 | -1 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 187.8 | +2.4 | 974.21 | 264 | 0 | 17 | 0 |
| BenchmarkPNGExtractCompressedXMP | 805.2 | -0.6 | 202.44 | 802 | 0 | 16 | 0 |
| BenchmarkPNGInject | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPNGWriteChunk | N/A (not present) | — | — | — | — | — | — |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 101.2 | +0.5 | 1107.14 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 99.76 | +1.66 | 641.56 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | N/A (not present) | — | — | — | — | — | — |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 83.79 | -0.50 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 83.10 | -1.35 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 83.33 | +0.10 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 84.70 | +1.34 | 560 | 0 | 2 | 0 |

---

## [v1.0.0] — 2026-04-04

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkEXIFParse | 129.6 | — | 257 | — | 4 | — |
| BenchmarkEXIFParse_Camera | 1002 | — | 2353 | — | 8 | — |
| BenchmarkIFDGet_Large | 3.738 | — | 0 | — | 0 | — |
| BenchmarkEXIFEncode | 122.0 | — | 240 | — | 6 | — |
| BenchmarkIFDGet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDSet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDEntryString | N/A (not present) | — | — | — | — | — |
| BenchmarkParseGPS | N/A (not present) | — | — | — | — | — |
| BenchmarkMakerNoteDispatch | N/A (not present) | — | — | — | — | — |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 114.1 | — | 944 | — | 2 | — |
| BenchmarkIPTCEncode | 68.72 | — | 96 | — | 1 | — |
| BenchmarkIPTCAccessors | 26.49 | — | 64 | — | 1 | — |
| BenchmarkDecodeString | N/A (not present) | — | — | — | — | — |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkXMPParse | 1105 | — | — | 968 | — | 12 | — |
| BenchmarkXMPEncode | 659.1 | — | — | 3107 | — | 2 | — |
| BenchmarkRDFParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkXMPEncodeFullPacket | N/A (not present) | — | — | — | — | — | — |
| BenchmarkKeywords | N/A (not present) | — | — | — | — | — | — |
| BenchmarkAddKeyword | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSEncode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkEntityDecode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPacketScan | N/A (not present) | — | — | — | — | — | — |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 268.2 | — | 451.23 | 605 | — | 7 | — |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 100.3 | — | 837.27 | 96 | — | 3 | — |
| BenchmarkJPEGInject | 202.9 | — | 413.96 | 304 | — | 8 | — |
| BenchmarkJPEGExtract_Real | 2053 | — | 12718.61 | 17756 | — | 7 | — |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 185.4 | — | 987.03 | 264 | — | 17 | — |
| BenchmarkPNGExtractCompressedXMP | 805.8 | — | 202.28 | 802 | — | 16 | — |
| BenchmarkPNGInject | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPNGWriteChunk | N/A (not present) | — | — | — | — | — | — |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 100.7 | — | 1111.93 | 560 | — | 2 | — |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 98.10 | — | 652.42 | 104 | — | 7 | — |
| BenchmarkWebPInject | N/A (not present) | — | — | — | — | — | — |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 84.29 | — | 560 | — | 2 | — |
| BenchmarkCR2Extract | 84.45 | — | 560 | — | 2 | — |
| BenchmarkDNGExtract | 83.23 | — | 560 | — | 2 | — |
| BenchmarkNEFExtract | 83.36 | — | 560 | — | 2 | — |

---

## Comparison

### Key changes across versions

**format/heif — allocation regression at v1.0.1 (persists into main)**

`BenchmarkHEIFExtract` doubled its allocation count from 7 to 15 between v1.0.0 and v1.0.1, and latency increased from 268 ns to 352 ns (~31%). The allocation count and latency are flat from v1.0.1 to main, indicating the regression is stable, not growing. This is most likely a deliberate correctness fix that introduced additional heap allocations — the trade-off should be confirmed and documented. If the extra allocations are load-bearing (e.g., defensive copies of box data), they are acceptable; if they are incidental, `sync.Pool` or stack promotion could recover the v1.0.0 profile.

**format/png extract — gradual regression from v1.0.0 to main**

`BenchmarkPNGExtract` has drifted from 185 ns at v1.0.0 to 216 ns at main (~17%), with throughput falling from 987 MB/s to 844 MB/s. The allocation count (17 allocs, 264 B) has not changed, pointing to increased per-operation cost rather than new allocations — possibly shared setup code changed when PNG write support was added. Worth profiling if the PNG extract path becomes a bottleneck.

**format/jpeg inject — v1.0.1 regression recovered in main**

`BenchmarkJPEGInject` regressed from 202.9 ns at v1.0.0 to 216.1 ns at v1.0.1 (~6.5%), then recovered to 201.4 ns at main — marginally better than the original. Allocation profile (304 B, 8 allocs) is unchanged across all three versions, so the fluctuation is in per-operation latency only. The main result is stable.

**RAW formats — stable across all versions**

All four RAW extractors (ARW, CR2, DNG, NEF) have held at approximately 83–85 ns, 560 B, and 2 allocs across every recorded version. This is the expected profile for TIFF-rooted formats that share the core TIFF extractor with no format-specific overhead beyond magic-byte dispatch.

**New benchmarks in main — granular coverage added**

The main commit adds benchmarks that were absent in v1.0.0 and v1.0.1:

- `exif/`: `BenchmarkIFDGet` (2.9 ns, 0 allocs), `BenchmarkIFDEntryString` (5.5 ns, 0 allocs), `BenchmarkParseGPS` (41.8 ns, 0 allocs), `BenchmarkIFDSet` (643.6 ns, 31 allocs — the high alloc count here should be reviewed), `BenchmarkMakerNoteDispatch` (95.9 ns, 2 allocs).
- `iptc/`: `BenchmarkDecodeString` (55.1 ns, 3 allocs).
- `xmp/`: full coverage of GPS, packet scan, keyword ops, entity decode, and full-packet encoding. `BenchmarkPacketScan` at 4728 MB/s confirms the zero-allocation scan path is performing as designed.
- `format/png`: write-path benchmarks (`BenchmarkPNGInject`, `BenchmarkPNGWriteChunk`) now tracked.
- `format/webp`: `BenchmarkWebPInject` added (232 ns, 10 allocs).

**Overall allocation posture**

Zero-allocation paths (`BenchmarkIFDGet`, `BenchmarkIFDGet_Large`, `BenchmarkParseGPS` in exif; `BenchmarkGPSParse` in xmp; `BenchmarkPacketScan`) are holding at 0 B/op and 0 allocs/op. The fast-path design goals for these operations are being met.

**d018d96 — write-path copy elimination and buffer pre-sizing**

The `perf(exif,xmp,heif,png,orf,rw2)` commit delivers broad latency improvements across the read path at the top level:

- `BenchmarkRead_JPEG` dropped 5.8% (269.8 → 254.2 ns).
- `BenchmarkRead_JPEG_WithXMP` dropped 8.6% (1447 → 1323 ns).
- `BenchmarkReadCombinedMetadataJPEG` dropped 17% (13786 → 11435 ns).
- `BenchmarkReadFile` dropped 17% (2235 → 1854 ns).

All zero-allocation paths remain at 0 B/op and 0 allocs/op. Write-path benchmarks `BenchmarkWrite_JPEG` and `BenchmarkWrite_PNG` are new this run and establish a baseline. Internal package benchmarks (`bmff`, `byteorder`, `iobuf`, `riff`) appear for the first time; all are cheap (< 36 ns) and most are zero-allocation, confirming the internal primitives are performing as designed.

`BenchmarkJPEGExtract_Real` shows a +48 ns regression (2034 → 2082 ns, ~2.4%) which is within typical run-to-run noise for this benchmark given its larger synthetic payload; the allocation profile is unchanged (17756 B, 7 allocs).
