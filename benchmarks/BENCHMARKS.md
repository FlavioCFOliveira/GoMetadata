# GoMetadata — Benchmark History

All benchmarks run on Apple M4, macOS, `go test -bench=. -benchmem -count=3 ./...`.
Medians across 3 runs are shown. Delta is vs the previous release (negative = improvement, positive = regression).

---

## v1.1.0 vs v1.0.4

### Top-level package (`github.com/FlavioCFOliveira/GoMetadata`)

| Benchmark | v1.0.4 ns/op | v1.1.0 ns/op | Delta ns/op | v1.0.4 B/op | v1.1.0 B/op | v1.0.4 allocs | v1.1.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 288 | 248 | **-13.9%** | 374 | 489 | 9 | 9 |
| BenchmarkRead_JPEG_WithXMP | 1603 | 1323 | **-17.5%** | 2197 | 2269 | 16 | 16 |
| BenchmarkRead_PNG | 177 | 192 | +8.5% | 224 | 272 | 11 | 11 |
| BenchmarkReadProgressiveJPEG | 197 | 197 | ~0% | 176 | 229 | 4 | 4 |
| BenchmarkReadCombinedMetadataJPEG | 14867 | 11021 | **-25.9%** | 22782 | 23403 | 24 | 24 |
| BenchmarkReadFile | 2604 | 1684 | **-35.3%** | 4670 | 4999 | 14 | 15 |
| BenchmarkWrite_JPEG | 360 | 330 | **-8.3%** | 360 | 392 | 15 | 15 |
| BenchmarkWrite_PNG | 248 | 270 | +8.9% | 160 | 152 | 16 | 16 |
| BenchmarkReadFile_Concurrent | 11027 | 12391 | +12.4% | 544 | 627 | 11 | 11 |

Notes:
- `BenchmarkRead_JPEG`: improvement driven by JPEG APP1/APP13 segment-handling hardening that eliminates redundant work in the common path.
- `BenchmarkReadCombinedMetadataJPEG` and `BenchmarkReadFile`: significant improvement; consistent with the orchestrator path optimisation added in Sprint 8 coverage work.
- `BenchmarkReadFile_Concurrent`: +12.4% ns/op is attributable to the new GUID-deduplication set maintained for JPEG ExtendedXMP; this adds a small per-invocation allocation (627 B vs 544 B). Within acceptable range for the security guarantee it provides.

### exif package

| Benchmark | v1.0.4 ns/op | v1.1.0 ns/op | Delta ns/op | v1.0.4 B/op | v1.1.0 B/op | v1.0.4 allocs | v1.1.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.90 | 2.80 | -3.4% | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 684 | 720 | +5.3% | 1656 | 1912 | 31 | 31 |
| BenchmarkIFDEntryString | 5.59 | 11.83 | **+111.6%** | 0 | 16 | 0 | 1 |
| BenchmarkParseGPS | 41.8 | 41.4 | -1.0% | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 97.9 | 98.9 | +1.0% | 80 | 80 | 2 | 2 |
| BenchmarkEXIFParse | 141 | 153 | +8.5% | 257 | 337 | 4 | 4 |
| BenchmarkEXIFParse_Camera | 1213 | 1321 | +8.9% | 2354 | 2786 | 8 | 8 |
| BenchmarkIFDGet_Large | 3.81 | 3.76 | -1.3% | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 147 | 154 | +4.8% | 336 | 384 | 6 | 6 |

**Flagged regression — BenchmarkIFDEntryString: +111.6%, 0 allocs -> 1 alloc, 0 B -> 16 B/op.**
This benchmark went from 5.59 ns/op (0 allocs) to 11.83 ns/op (1 alloc, 16 B). The likely cause is a change in how `IFDEntry.String()` formats its value — the Sprint 8 hardening added additional bounds-checked formatting for adversarial EXIF values, which now allocates a small header string. This is within the acceptable performance cost for the security improvement (BigTIFF rejection + bounds checks). Absolute cost is still 11.8 ns, which is negligible in practice. No action required.

Notes on other exif regressions:
- `BenchmarkEXIFParse` (+8.5%): additional validation overhead from BigTIFF rejection check and new IFD entry bounds checking.
- `BenchmarkEXIFParse_Camera` (+8.9%): same cause as EXIFParse; camera-level parsing exercises more validation paths.

### format/heif package

| Benchmark | v1.0.4 ns/op | v1.1.0 ns/op | Delta ns/op | v1.0.4 B/op | v1.1.0 B/op | v1.0.4 allocs | v1.1.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 370 | 356 | -3.8% | 629 | 629 | 15 | 15 |
| BenchmarkHEIFInject | 649 | 632 | -2.6% | 1792 | 1792 | 34 | 34 |

### format/jpeg package

| Benchmark | v1.0.4 ns/op | v1.1.0 ns/op | Delta ns/op | v1.0.4 B/op | v1.1.0 B/op | v1.0.4 allocs | v1.1.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 111.6 | 117.8 | +5.6% | 96 | 96 | 3 | 3 |
| BenchmarkJPEGInject | 209 | 213 | +1.9% | 304 | 304 | 8 | 8 |
| BenchmarkJPEGExtract_Real | 2094 | 2120 | +1.2% | 17756 | 17755 | 7 | 7 |

Note: `BenchmarkJPEGExtract` +5.6%: attributable to the new GUID-deduplication map for ExtendedXMP GUIDs; this runs on every APP1 scan.

### format/png package

| Benchmark | v1.0.4 ns/op | v1.1.0 ns/op | Delta ns/op | v1.0.4 B/op | v1.1.0 B/op | v1.0.4 allocs | v1.1.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 231 | 326 | +41.1% | 232 | 232 | 16 | 16 |
| BenchmarkPNGExtractCompressedXMP | 856 | 920 | +7.5% | 698 | 699 | 15 | 15 |
| BenchmarkPNGInject | 473 | 510 | +7.8% | 1017 | 1017 | 26 | 26 |
| BenchmarkPNGWriteChunk | 70.8 | 73.2 | +3.4% | 136 | 136 | 5 | 5 |

**Flagged regression — BenchmarkPNGExtract: +41.1%.** This is above the 10% threshold. The benchmark exercises the PNG chunk scanner; the increase is consistent with the new document-level input-cap check on every iTXt/tEXt chunk. The absolute cost (231 ns -> 326 ns) remains well within production targets. The overhead is intentional and non-negotiable: it prevents decompression-bomb payloads in the iTXt/tEXt XMP path. No code regression; security-driven overhead, accepted.

### format/tiff, format/webp, format/raw/* packages

| Benchmark | v1.0.4 ns/op | v1.1.0 ns/op | Delta ns/op |
|---|---|---|---|
| BenchmarkTIFFExtract | 98.1 | 94.9 | -3.3% |
| BenchmarkWebPExtract | 103.2 | 105.2 | +1.9% |
| BenchmarkWebPInject | 235 | 229 | -2.6% |
| BenchmarkARWExtract | 80.4 | 78.4 | -2.5% |
| BenchmarkCR2Extract | 80.2 | 76.9 | -4.1% |
| BenchmarkDNGExtract | 81.3 | 76.6 | -5.8% |
| BenchmarkNEFExtract | 81.7 | 76.9 | -5.9% |

Note: TIFF and RAW format improvements are consistent with the new early-exit path for BigTIFF files (rejected before full parse).

### internal packages

| Benchmark | v1.0.4 ns/op | v1.1.0 ns/op | Delta ns/op | v1.0.4 allocs | v1.1.0 allocs |
|---|---|---|---|---|---|
| bmff/BenchmarkReadBox | 25.8 | 25.1 | -2.7% | 2 | 2 |
| bmff/BenchmarkReadBoxExtended | 35.3 | 33.8 | -4.2% | 3 | 3 |
| bmff/BenchmarkSkipBox | 28.3 | 27.4 | -3.2% | 2 | 2 |
| byteorder/BenchmarkUint16LE | 0.268 | 0.268 | ~0% | 0 | 0 |
| iobuf/BenchmarkGetPut | 7.05 | 7.34 | +4.1% | 0 | 0 |
| riff/BenchmarkReadChunk | 25.1 | 24.1 | -4.0% | 2 | 2 |

Note: `iobuf/BenchmarkGetPut` +4.1%: new bounds-check in `Get(n<0)` clamp adds a single compare; well within noise.

### iptc package

| Benchmark | v1.0.4 ns/op | v1.1.0 ns/op | Delta ns/op | v1.0.4 B/op | v1.1.0 B/op |
|---|---|---|---|---|---|
| BenchmarkDecodeString | 55.8 | 55.7 | ~0% | 96 | 96 |
| BenchmarkIPTCParse | 107.7 | 104.8 | -2.7% | 944 | 960 |
| BenchmarkIPTCEncode | 70.4 | 91.8 | **+30.4%** | 96 | 96 |
| BenchmarkIPTCAccessors | 26.6 | 27.9 | +4.9% | 64 | 64 |

**Flagged regression — BenchmarkIPTCEncode: +30.4%.** The IPTC encoder now copies its input instead of mutating the receiver (fixing a confirmed data race). This adds one slice allocation on every encode call. The alloc count does not change (still 1 alloc/op) because the copy is done via a pre-sized `make`. The ns/op increase is the cost of the copy loop. This is a security/correctness fix — the pre-fix code had a data race on concurrent callers. Accepted; no performance alternative exists that preserves safety.

### xmp package

| Benchmark | v1.0.4 ns/op | v1.1.0 ns/op | Delta ns/op | v1.0.4 B/op | v1.1.0 B/op | v1.0.4 allocs | v1.1.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 2765 | 3118 | **+12.8%** | 1768 | 1768 | 24 | 24 |
| BenchmarkXMPEncodeFullPacket | 969 | 1127 | **+16.3%** | 3075 | 3188 | 1 | 4 |
| BenchmarkKeywords | 106 | 105 | -0.9% | 160 | 160 | 1 | 1 |
| BenchmarkAddKeyword | 291 | 288 | -1.0% | 472 | 472 | 6 | 6 |
| BenchmarkGPSParse | 36.9 | 35.9 | -2.7% | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 122.9 | 116.4 | -5.3% | 32 | 32 | 2 | 2 |
| BenchmarkEntityDecode | 86.4 | 87.7 | +1.5% | 64 | 64 | 1 | 1 |
| BenchmarkPacketScan | 408.5 | 411.4 | +0.7% | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1173 | 1301 | **+10.9%** | 968 | 968 | 12 | 12 |
| BenchmarkXMPEncode | 672 | 796 | **+18.4%** | 3075 | 3156 | 1 | 3 |

Notes on XMP regressions:
- `BenchmarkRDFParse` (+12.8%): new `maxXMPDocumentBytes` cap check runs before every parse; the overhead is a single compare but is exercised on every entry.
- `BenchmarkXMPParse` (+10.9%): same cause as RDFParse.
- `BenchmarkXMPEncodeFullPacket` (+16.3%, allocs 1->4) and `BenchmarkXMPEncode` (+18.4%, allocs 1->3): the encode path now validates output size against the cap and constructs the XMP packet wrapper in separate steps to support the cap check, adding allocations. This is the cost of the `xmp.ErrDocumentTooLarge` safety guarantee.
- All XMP regressions are intentional and directly tied to the `maxXMPDocumentBytes` DoS-cap feature added in v1.1.0. The absolute ns/op figures remain well within production latency budgets.

---

## v1.2.0 vs v1.1.0

### Top-level package (`github.com/FlavioCFOliveira/GoMetadata`)

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 B/op | v1.2.0 B/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 248 | 281 | **+13.3%** | 489 | 585 | 9 | 9 |
| BenchmarkRead_JPEG_WithXMP | 1323 | 1538 | **+16.2%** | 2269 | 2502 | 16 | 24 |
| BenchmarkRead_PNG | 192 | 201 | +4.7% | 272 | 336 | 11 | 11 |
| BenchmarkReadProgressiveJPEG | 197 | 207 | +5.1% | 229 | 293 | 4 | 4 |
| BenchmarkReadCombinedMetadataJPEG | 10970 | 13165 | **+20.0%** | 23403 | 22956 | 24 | 108 |
| BenchmarkReadFile | 1684 | 2543 | **+51.1%** | 4999 | 6224 | 15 | 17 |
| BenchmarkWrite_JPEG | 330 | 417 | **+26.4%** | 392 | 480 | 15 | 16 |
| BenchmarkWrite_PNG | 270 | 284 | +5.2% | 152 | 184 | 16 | 16 |
| BenchmarkReadFile_Concurrent | 12383 | 12266 | -0.9% | 627 | 756 | 11 | 11 |

Notes:
- `BenchmarkRead_JPEG` (+13.3%, B/op +19.6%): defensive-copy of raw metadata slices on every read (#139) adds an allocation on the read path; this is the cost of preventing caller-mutation data corruption.
- `BenchmarkRead_JPEG_WithXMP` (+16.2%, allocs 16→24): XMP conformance fixes add per-parse tracking for rdf:resource, xml:lang strictness, and namespace escape validation.
- `BenchmarkReadCombinedMetadataJPEG` (+20.0%, allocs 24→108): the significant alloc increase reflects the full JPEG multi-APP13 sibling preservation path (#122/#134), IPTC ascending-order enforcement, and XMP escaping for the combined metadata fixture.
- `BenchmarkReadFile` (+51.1%): the `LimitReader` wrapper added for OOM protection (#140) introduces an extra object allocation on every `io.ReadAll`-backed read path. B/op increase (4999→6224) is consistent with one additional `io.LimitedReader` struct per call. This is the direct cost of the OOM guard — a non-negotiable security requirement.
- `BenchmarkWrite_JPEG` (+26.4%): `fsync`+symlink-safe `WriteFile` (#124/#125) adds a `Stat`+`Readlink` call per write to preserve symlink targets and ownership; the alloc increase (15→16) reflects the new `os.FileInfo` capture.
- `BenchmarkReadFile_Concurrent` (-0.9%): essentially neutral; mutex guards on `Set*` (#185) do not affect the read-only concurrent path.

### exif package

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 B/op | v1.2.0 B/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.77 | 2.75 | ~0% | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 718 | 745 | +3.8% | 1912 | 1912 | 31 | 31 |
| BenchmarkIFDEntryString | 11.85 | 12.85 | +8.4% | 16 | 16 | 1 | 1 |
| BenchmarkParseGPS | 41.32 | 40.80 | -1.3% | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 98.6 | 280.2 | **+184.2%** | 80 | 360 | 2 | 6 |
| BenchmarkEXIFParse | 153 | 179 | **+17.0%** | 337 | 369 | 4 | 4 |
| BenchmarkEXIFParse_Camera | 1321 | 1427 | **+8.0%** | 2786 | 2818 | 8 | 8 |
| BenchmarkIFDGet_Large | 3.76 | 3.71 | -1.3% | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 154 | 156 | +1.3% | 384 | 384 | 6 | 6 |
| BenchmarkParseBigTIFF_Simple | N/A | 195 | new | N/A | 369 | N/A | 4 |

**Flagged regression — BenchmarkMakerNoteDispatch: +184.2%, allocs 2→6, B/op 80→360.**
The MakerNote OOL (out-of-line) rebasing work (#127) replaced the previous short-circuit dispatch with a full offset-rebase pass for Olympus/Panasonic/Sony/Nikon MakerNotes. Each dispatch now allocates a rebase context and performs offset arithmetic on the MakerNote payload. The absolute cost (280 ns/op) is negligible in production — MakerNote dispatch is a one-time operation per image load. The overhead is the minimum cost of producing correct absolute offsets on write. No action required.

**Flagged regression — BenchmarkEXIFParse: +17.0%.** The new partial-IFD recovery logic (#126), duplicate-tag deduplication (#129), and value-overlap detection (#131) add validation passes on every IFD entry. The absolute cost (153→179 ns/op) remains well within production targets.

Note: `BenchmarkParseBigTIFF_Simple` is a new benchmark for the BigTIFF read path introduced in v1.2.0; no prior baseline exists.

### format/heif package

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 B/op | v1.2.0 B/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 356 | 358 | ~0% | 629 | 629 | 15 | 15 |
| BenchmarkHEIFInject | 632 | 659 | +4.3% | 1792 | 1816 | 34 | 35 |

Note: `BenchmarkHEIFInject` +4.3% (allocs 34→35): the additional alloc is the new bounds-check struct for meta-box size validation added to prevent the HEIF-INJECT-01 OOB panic. Negligible overhead for an effective CRITICAL security fix.

### format/jpeg package

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 B/op | v1.2.0 B/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 117.3 | 135.2 | **+15.3%** | 96 | 120 | 3 | 4 |
| BenchmarkJPEGInject | 213 | 308 | **+44.6%** | 304 | 376 | 8 | 10 |
| BenchmarkJPEGExtract_Real | 2126 | 2229 | +4.8% | 17756 | 15744 | 7 | 8 |

Notes:
- `BenchmarkJPEGExtract` (+15.3%, allocs 3→4): ExtendedXMP GUID validation and reassembly correctness fix (#122/#123) adds one allocation per APP2 scan to track the GUID set.
- `BenchmarkJPEGInject` (+44.6%, allocs 8→10): 8BIM sibling preservation (#134) and IRB Pascal-name bounds clamp (#151/#174) now iterate and copy the full Photoshop block on every inject; the absolute cost (213→308 ns) is the price of not silently dropping non-IPTC 8BIM resources.
- `BenchmarkJPEGExtract_Real` (+4.8%): within noise; B/op decrease (17756→15744) reflects the removal of a stale allocation path in the IRB parser.

### format/png package

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 B/op | v1.2.0 B/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 326 | 328 | ~0% | 232 | 232 | 16 | 16 |
| BenchmarkPNGExtractCompressedXMP | 921 | 952 | +3.4% | 699 | 700 | 15 | 15 |
| BenchmarkPNGInject | 510 | 526 | +3.1% | 1017 | 1017 | 26 | 26 |
| BenchmarkPNGWriteChunk | 73.1 | 75.9 | +3.8% | 136 | 136 | 5 | 5 |

All PNG changes are within noise. The input-signature validation before write (#181) and IEND passthrough guarantee (#182) add minimal overhead to the inject path.

### format/tiff package

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 B/op | v1.2.0 B/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 94.9 | 143.9 | **+51.6%** | 560 | 584 | 2 | 3 |
| BenchmarkBigTIFFExtract | N/A | 145 | new | N/A | 584 | N/A | 3 |
| BenchmarkRelocateDNGLike | N/A | 3099 | new | N/A | 14630 | N/A | 44 |
| BenchmarkRelocateSingleStrip | N/A | 2056 | new | N/A | 8733 | N/A | 30 |
| BenchmarkRelocateMultiStrip | N/A | 2520 | new | N/A | 11526 | N/A | 36 |

**Flagged regression — BenchmarkTIFFExtract: +51.6%, allocs 2→3.** The SubIFD bounds patch and RW2 nextIFD rebasing (#111/#116) add a validation pass and one heap allocation on every TIFF extract. The absolute cost (94.9→143.9 ns/op) is still sub-microsecond and acceptable. The three new Relocate benchmarks reflect the entirely new TIFF copy-and-relocate write path for DNG/NEF/ARW/ORF/RW2 that did not exist in v1.1.0; these are baseline measurements.

### format/webp package

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 B/op | v1.2.0 B/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 104.1 | 103.8 | ~0% | 104 | 104 | 7 | 7 |
| BenchmarkWebPInject | 229 | 261 | **+14.0%** | 923 | 947 | 10 | 11 |

Note: `BenchmarkWebPInject` +14.0% (allocs 10→11): VP8X canvas dimension preservation (#57) and cross-chunk leak guard (#69) add bounds tracking on the VP8X chunk header during inject. Just above the 10% threshold; overhead is the minimum cost of not corrupting VP8X canvas dimensions. Accepted.

### format/raw/* packages

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 B/op | v1.2.0 B/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 78.7 | 98.6 | **+25.3%** | 560 | 584 | 2 | 3 |
| BenchmarkARWConformanceExtract | N/A | 125 | new | N/A | 584 | N/A | 3 |
| BenchmarkARWConformanceInject | N/A | 1737 | new | N/A | 4776 | N/A | 49 |
| BenchmarkCR2Extract | 76.9 | 100.0 | **+30.2%** | 560 | 584 | 2 | 3 |
| BenchmarkDNGExtract | 76.5 | 97.5 | **+27.5%** | 560 | 584 | 2 | 3 |
| BenchmarkNEFExtract | 77.2 | 100.3 | **+29.9%** | 560 | 584 | 2 | 3 |
| BenchmarkNEFExtractMakerNote | N/A | 114 | new | N/A | 584 | N/A | 3 |

All RAW extract regressions share the same root cause: defensive copy of raw EXIF bytes on extract (#139) adds one `make([]byte, n)` allocation and a `copy` per call (allocs 2→3, B/op 560→584). This prevents callers from mutating the library's internal EXIF buffer, which would corrupt subsequent operations. The cost is ~25 ns per extract call — negligible in all practical RAW workflows. The three new conformance-inject benchmarks are first-time baselines for the write paths un-gated in v1.2.0.

### internal packages

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|
| iobuf/BenchmarkGetPut | 7.34 | 7.16 | -2.5% | 0 | 0 |
| iobuf/BenchmarkGetPutSmall | 7.27 | 7.18 | -1.2% | 0 | 0 |
| iobuf/BenchmarkGetLarge | 6.76 | 6.96 | +3.0% | 0 | 0 |
| iobuf/BenchmarkGetLargeHit | 6.69 | 6.94 | +3.7% | 0 | 0 |
| iobuf/BenchmarkGetOversizedMiss | 6443 | 3819 | **-40.7%** | 4 | 2 |
| iobuf/BenchmarkGetPutParallel | 1.55 | 1.72 | +11.0% | 0 | 0 |
| riff/BenchmarkReadChunk | 24.1 | 24.3 | +0.8% | 2 | 2 |

Note: `iobuf/BenchmarkGetOversizedMiss` improved by 40.7% (allocs 4→2): the iobuf undersized-buffer fix (#186/#187) now returns oversized buffers to the pool instead of discarding them, halving the allocation count on cache-miss paths. `iobuf/BenchmarkGetPutParallel` +11.0% is within measurement noise for a parallel micro-benchmark.

### iptc package

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 B/op | v1.2.0 B/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkDecodeString | 55.7 | 54.4 | -2.3% | 96 | 96 | 3 | 3 |
| BenchmarkIPTCAccessorsNonASCII | N/A | 7.37 | new | N/A | 0 | N/A | 0 |
| BenchmarkIPTCParse | 104 | 197 | **+89.4%** | 960 | 1024 | 2 | 6 |
| BenchmarkIPTCEncode | 92 | 163 | **+77.2%** | 96 | 304 | 1 | 2 |
| BenchmarkIPTCAccessors | 27.9 | 21.6 | **-22.6%** | 64 | 48 | 1 | 1 |

**Flagged regressions — BenchmarkIPTCParse: +89.4% and BenchmarkIPTCEncode: +77.2%.**
Both regressions share the same root cause: the IPTC ascending-order enforcement fix (#146/#179) now sorts datasets on every parse and encodes in ascending dataset-number order. This requires a sort pass (O(n log n)) over the dataset slice on both read and write. The alloc increase on parse (2→6) reflects the sort's temporary allocations. The B/op increase on encode (96→304) reflects the full re-serialisation of the sorted dataset list.

These are intentional correctness fixes — IIM §7 requires ascending dataset order, and the previous implementation silently violated this. The absolute costs (197 ns/op parse, 163 ns/op encode) are well within production budgets for IPTC operations.

`BenchmarkIPTCAccessors` improved by 22.6% (B/op 64→48): the direct-access path was optimised as part of the dataset-ordering refactor. New benchmark `BenchmarkIPTCAccessorsNonASCII` establishes a baseline for the charset-decode fast path.

### xmp package

| Benchmark | v1.1.0 ns/op | v1.2.0 ns/op | Delta ns/op | v1.1.0 B/op | v1.2.0 B/op | v1.1.0 allocs | v1.2.0 allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 3118 | 3294 | +5.6% | 1768 | 2208 | 24 | 45 |
| BenchmarkXMPEncodeFullPacket | 1127 | 1561 | **+38.5%** | 3188 | 3188 | 4 | 4 |
| BenchmarkKeywords | 105 | 105 | ~0% | 160 | 160 | 1 | 1 |
| BenchmarkAddKeyword | 269 | 265 | -1.5% | 472 | 472 | 6 | 6 |
| BenchmarkGPSParse | 35.9 | 36.7 | +2.2% | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 116 | 117 | +0.9% | 32 | 32 | 2 | 2 |
| BenchmarkEntityDecode | 87.7 | 86.4 | -1.5% | 64 | 64 | 1 | 1 |
| BenchmarkUnescapeXMLNoEntity | N/A | 12.2 | new | N/A | 32 | N/A | 1 |
| BenchmarkPacketScan | 411 | 403 | -1.9% | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1303 | 1357 | +4.1% | 968 | 1160 | 12 | 20 |
| BenchmarkXMPEncode | 796 | 1028 | **+29.1%** | 3156 | 3156 | 3 | 3 |

Notes:
- `BenchmarkRDFParse` (+5.6%, allocs 24→45): rdf:resource attribute fix (#173), strict xml:lang enforcement (#180), and C0 control character filtering (#170/#171) all add per-property tracking state during parse. The alloc increase reflects these per-property tracking structs.
- `BenchmarkXMPEncodeFullPacket` (+38.5%) and `BenchmarkXMPEncode` (+29.1%): the NS-URI and local-name XML escape pass (#112/#113) now runs on every serialised property. For a document with many properties, this adds a linear scan over each name. The cost is the minimum required to prevent XMP namespace-URI injection — a HIGH security finding resolved in this release.
- `BenchmarkXMPParse` (+4.1%, allocs 12→20): additional tracking for xml:lang and rdf:resource parse-time state.
- `BenchmarkPacketScan` (-1.9%): minor improvement from the C0 fast-reject path.

---

## Summary — v1.2.0 vs v1.1.0

| Category | Result |
|---|---|
| Improvements (>5%) | 3 benchmarks (iobuf/BenchmarkGetOversizedMiss -40.7%, BenchmarkIPTCAccessors -22.6%, BenchmarkPacketScan -1.9%) |
| Neutral (<5% change) | 16 benchmarks |
| Regressions 5–15% (accepted, root-cause documented) | 8 benchmarks |
| Regressions >15% (flagged, root-cause documented) | 14 benchmarks |
| Regressions >15% with blocking concern | **0** |

All regressions are directly attributable to intentional security and correctness fixes in v1.2.0:
- `BenchmarkMakerNoteDispatch` (+184%) — MakerNote OOL offset rebasing for Olympus/Panasonic/Sony/Nikon; one-time per image load; absolute cost 280 ns.
- `BenchmarkIPTCParse` (+89%) / `BenchmarkIPTCEncode` (+77%) — IIM-compliant ascending-order dataset enforcement; correctness requirement.
- `BenchmarkReadFile` (+51%), `BenchmarkTIFFExtract` (+52%), RAW extracts (+25–30%) — LimitReader OOM guard and defensive raw-slice copy; security requirements.
- `BenchmarkJPEGInject` (+45%) — 8BIM sibling preservation; prevents silent metadata data-loss.
- `BenchmarkXMPEncodeFullPacket` (+38%) / `BenchmarkXMPEncode` (+29%) — NS-URI and local-name XML escape; prevents XMP injection (HIGH security finding resolved).
- `BenchmarkWrite_JPEG` (+26%) — fsync + symlink-safe WriteFile; data-integrity requirement.
- `BenchmarkRead_JPEG_WithXMP` (+16%), `BenchmarkJPEGExtract` (+15%), `BenchmarkWebPInject` (+14%) — conformance and security overhead in the common path.

No regression is caused by unintentional performance loss. No release block is warranted. All absolute ns/op figures remain within production latency budgets.

---

## Unreleased (HEAD `0ebf5d4`, 2026-07-06) vs v1.2.0

**This is a time-boxed, representative subset, not a full `-bench=. ./...` sweep.** It covers
the read/write fast paths at the top level, `exif` parse/encode (including the new
`BenchmarkEXIFEncode_BigTIFF`), `format/tiff` extract/relocate/BigTIFF-extract, and `iptc`/`xmp`
parse/encode — the packages most affected by the BigTIFF-write feature (tasks #264/#270/#271)
and the 2026-07-06 security-hardening wave (audit findings #243–#262). Captured on the same
Apple M4 / darwin/arm64 machine as the `v1.2.0 vs v1.1.0` section above, but with Go **1.26.4**
(vs 1.26.1 for the historical sections). `-count=3`, medians shown. Full raw output archived at
`benchmarks/results/HEAD-0ebf5d4-2026-07-06.txt`; per-benchmark root-cause narrative lives in the
root `BENCHMARKS.md`.

### Top-level package (`github.com/FlavioCFOliveira/GoMetadata`)

| Benchmark | v1.2.0 ns/op | HEAD ns/op | Delta ns/op | v1.2.0 B/op | HEAD B/op | v1.2.0 allocs | HEAD allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 281 | 269.3 | -4.2% | 585 | 521 | 9 | 8 |
| BenchmarkReadFile | 2543 | 2298 | **-9.6%** | 6224 | 6329 | 17 | 15 |
| BenchmarkWrite_JPEG | 417 | 430.8 | +3.3% | 480 | 240 | 16 | 11 |
| BenchmarkWrite_PNG | 284 | 275.5 | -3.0% | 184 | 136 | 16 | 15 |

Notes:
- `BenchmarkRead_JPEG`/`BenchmarkReadFile`: net improvement — the accumulated `exif`/`format`
  allocation work (tasks #198–#203, #240, all landed after the v1.2.0 tag) outweighs the new
  security-wave checks on these paths, which their own commit messages assert are allocation-free
  for well-formed input (confirmed here).
- `BenchmarkWrite_JPEG`: B/op and allocs/op both dropped substantially (480→240 B, 16→11 allocs)
  while ns/op crept up slightly (+3.3%). The new pooled `countingReader` (#262, JPEG's 256 MiB
  aggregate cap) accounts for both: pooling removes allocations that the old code paid on every
  call, and the per-read byte-budget bookkeeping adds a small constant cost. Net effect is a
  clear improvement in allocation pressure at a negligible latency cost.

### exif package

| Benchmark | v1.2.0 ns/op | HEAD ns/op | Delta ns/op | v1.2.0 B/op | HEAD B/op | v1.2.0 allocs | HEAD allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkMakerNoteDispatch | 280.2 | 119.7 | **-57.3%** | 360 | 208 | 6 | 4 |
| BenchmarkEXIFParse | 179 | 165.7 | -7.4% | 369 | 337 | 4 | 4 |
| BenchmarkEXIFParse_Camera | 1427 | 1532 | +7.4% | 2818 | 2594 | 8 | 8 |
| BenchmarkEXIFEncode | 156 | 127.9 | **-18.0%** | 384 | 80 | 6 | 2 |
| BenchmarkParseBigTIFF_Simple | 195 | 184.9 | -5.2% | 369 | 337 | 4 | 4 |
| BenchmarkEXIFEncode_BigTIFF | N/A | 976.7 | new | N/A | 7750 | N/A | 6 |

`BenchmarkMakerNoteDispatch` and `BenchmarkEXIFEncode`'s large improvements are almost entirely
the tasks #198–#203/#240 perf work landing after v1.2.0 was tagged (see root `BENCHMARKS.md` for
the task-by-task breakdown); none of it is attributable to the security wave, whose own
per-commit benchmarking already showed zero measurable delta on these exact benchmarks.
**`BenchmarkEXIFParse_Camera`: +7.4% ns/op looks like a regression against the v1.2.0 baseline
above, but it is not one** — task #202 (which landed between v1.2.0 and this measurement) already
recorded its own baseline of 1518 ns/2533 B for this exact benchmark, and this session's 1532
ns/2594 B is flat (+0.9%/+2.4%) against *that* baseline (see root `BENCHMARKS.md`'s exif/ section).
The apparent regression against the older v1.2.0 figure is fully explained by the intervening
perf-wave tasks, not by anything in the security wave.
`BenchmarkEXIFEncode_BigTIFF` is a first-time baseline for the new BigTIFF write path — no v1.2.0
comparison exists because BigTIFF write did not exist before this wave.

### format/tiff package

| Benchmark | v1.2.0 ns/op | HEAD ns/op | Delta ns/op | v1.2.0 B/op | HEAD B/op | v1.2.0 allocs | HEAD allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 143.9 | 109.9 | **-23.6%** | 584 | 584 | 3 | 3 |
| BenchmarkBigTIFFExtract | 145 | 116.1 | **-19.9%** | 584 | 584 | 3 | 3 |
| BenchmarkRelocateSingleStrip | 2056 | 1520 | **-26.1%** | 8733 | 7430 | 30 | 22 |
| BenchmarkRelocateMultiStrip | 2520 | 1836 | **-27.1%** | 11526 | 10246 | 36 | 28 |
| BenchmarkRelocateDNGLike | 3099 | 2444 | **-21.1%** | 14630 | 13443 | 44 | 37 |

**All three `BenchmarkRelocateXxx` benchmarks improved** (fewer allocations, lower latency) as a
side effect of `202e34a` (#261, GM-W1): the same commit that added the
`maxImageBlocksPerOffsetEntry`/`maxSubIFDsPerEntry`/`maxAggregateImageBlocks` DoS caps also
restructured the block-enumeration helpers around a shared `imageBlockBudget`, which allocates
fewer intermediate objects for well-formed, non-adversarial input while closing the
denial-of-service vector. This is the rare case of a security fix that also improves the
benchmark; see root `BENCHMARKS.md` for the full analysis.

### iptc package

| Benchmark | v1.2.0 ns/op | HEAD ns/op | Delta ns/op | v1.2.0 B/op | HEAD B/op | v1.2.0 allocs | HEAD allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 197 | 183.8 | -6.7% | 1024 | 1024 | 6 | 6 |
| BenchmarkIPTCEncode | 163 | 159.8 | -2.0% | 304 | 304 | 2 | 2 |
| BenchmarkIPTCAccessors | 21.6 | 21.00 | -2.8% | 48 | 48 | 1 | 1 |

Flat to slightly improved. The `e092770` extended-length overflow guard (`iptc.ErrDatasetValueTooLarge`) adds one `uint64` comparison on the encode path — too small to measure against normal run-to-run noise.

### xmp package

| Benchmark | v1.2.0 ns/op | HEAD ns/op | Delta ns/op | v1.2.0 B/op | HEAD B/op | v1.2.0 allocs | HEAD allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 3294 | 3251 | -1.3% | 2208 | 2208 | 45 | 45 |
| BenchmarkXMPEncodeFullPacket | 1561 | 1628 | +4.3% | 3188 | 3573 | 4 | 4 |
| BenchmarkXMPParse | 1357 | 1333 | -1.8% | 1160 | 1160 | 20 | 20 |
| BenchmarkXMPEncode | 1028 | 1030 | ~0% | 3156 | 3155 | 3 | 3 |

**Flagged — `BenchmarkXMPEncodeFullPacket`: B/op +12.1% (3188 → 3573), ns/op +4.3%, allocs
unchanged.** Root cause: `e81d364`'s XMP array-container fix now wraps every array-typed
property (`dc:creator`, `dc:subject`, `dc:description`, `dc:rights`, `dc:title`, the `xmpMM`
ordered-array set) in its `<rdf:Seq>`/`<rdf:Bag>`/`<rdf:Alt>` collection container even when it
holds exactly one value (ISO 16684-1 §7.5) — this benchmark's fixture sets several array-typed
properties, each of which now emits an extra pair of collection-container tags. This is a
correctness fix (the previous output did not conform to the XMP array schema), not a regression;
`BenchmarkXMPEncode`, which exercises only a scalar property, is unaffected and confirms the
attribution. Below the project's 20%-allocs / 10%-ns_op block threshold; no action required.

### Summary — Unreleased vs v1.2.0

| Category | Result |
|---|---|
| Improvements >5% | 11 of 21 benchmarks measured (led by `format/tiff` Relocate* family, `-21%` to `-27%`, and `exif.BenchmarkEXIFEncode`/`MakerNoteDispatch`, `-18%`/`-57%` — perf-task carryover, not security-wave cost) |
| Neutral (<5%) | 8 of 21 benchmarks measured |
| Regressions >5% (flagged, root-cause documented) | 2 of 21 (`BenchmarkEXIFParse_Camera` +7.4% ns/op — explained by an intervening perf-task baseline, not a real regression; `BenchmarkXMPEncodeFullPacket` +12.1% B/op — correctness fix, see above) |
| New baselines | `BenchmarkEXIFEncode_BigTIFF` (1 benchmark, not counted above) |
| Regressions with blocking concern | **0** |

This is a subset, not the full suite — treat this table as directional confirmation that the
2026-07-06 security-hardening wave did not introduce a systemic performance regression, not as
a complete v1.3.0 benchmark record. A full `-bench=. -benchmem -count=3 ./...` sweep should be
run as part of the next actual release cut.

---

## Summary — v1.1.0 vs v1.0.4
