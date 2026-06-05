---
name: sprint8-holistic-clearance
description: Sprint 8 final holistic security clearance — aggregate memory model, interaction checks, fuzz sweep, tooling results; CLEARED 2026-06-03
metadata:
  type: project
---

# Sprint 8 Holistic Security Clearance — 2026-06-03

**Verdict: SPRINT CLEARED**

## Aggregate Memory Model (worst-case single Read call)

### JPEG (most complex path — all three metadata types + extended XMP)

| Segment | Cap | Notes |
|---|---|---|
| EXIF (per APP1) | 65,533 bytes (JPEG 16-bit length) | Checked in format/jpeg; rawEXIF = bytes.Clone of ≤65,527 bytes |
| IPTC aggregate | 256 MiB | maxIPTCTotalBytes in iptc.go:115 |
| Standard XMP (per APP1) | 65,504 bytes (maxXMPPayload) | Copy of single APP1 payload |
| Extended XMP per GUID | 16 MiB | maxExtendedXMPTotal in jpeg.go:82 |
| Extended XMP GUID count | 4 | maxExtendedXMPGUIDs in jpeg.go:83 |
| Extended XMP aggregate | 64 MiB | 4 × 16 MiB |
| XMP transcode (UTF-16→UTF-8) | 16 MiB input → ≤24 MiB output | maxXMPTranscodeBytes + 1.5× BMP amplification |
| XMP document (post-transcode) | 16 MiB | maxXMPDocumentBytes in xmp.go:49 |
| XMP entity expansion | 1 MiB per attribute | maxUnescapedXMLBytes |
| EXIF IFD parse (per IFD) | O(input size) — 65,535 entries × 48 bytes ≈ 3 MiB | Linear in rawEXIF; rawEXIF ≤ 65,527 bytes for JPEG |
| Pool buffers (iobuf) | ≤ 65,536 bytes returned to pool; oversized discarded | iobuf.Put cap > largeSize discards |

**Worst-case JPEG TOTAL**: EXIF(65KB) + IPTC(256MB) + XMP(64MB) + XMP-doc(16MB) ≈ **~336 MiB**

The dominant term is IPTC at 256 MiB. This is a pre-existing cap value. The input file needed to saturate it must itself be ≥ 256 MiB (datasets are slices of the input buffer), so the amplification ratio is ≈ 1.3× for a fully adversarial JPEG.

**Is it sane?** For JPEG specifically, yes. The dominant cap (256 MiB IPTC) cannot be triggered by a small input file — it requires a 256 MiB+ JPEG. The XMP caps (64 MiB extended + 16 MiB doc) are well-bounded. No unbounded aggregate exists in the JPEG path after Sprint 8.

### TIFF / RAW formats (TIFF, ARW, CR2, DNG, NEF, ORF, RW2)

These formats use **uncapped `io.ReadAll(r)`** at the container level (tiff.go:22, orf.go:26, rw2.go:25, cr3.go:72). The entire file is read into memory. This is a **pre-existing design decision** (not a Sprint 8 regression) and has been the architectural model since the library's inception. The exif.Parse layer then applies:
- maxIFDEntryPrealloc = 1,024 entries per preallocate (actual can reach 65,535)  
- count×typeSize uint64 overflow protection
- offset bounds checks on all out-of-line values

**Risk assessment**: A crafted 4 GB TIFF file would cause 4 GB allocation. This is a known, pre-existing LOW/INFO finding (the OS/process memory limit is the only defence). It predates Sprint 8 and is not introduced or worsened by Sprint 8 changes.

### HEIF / AVIF

`maxItemPayloadSize = 256 MiB` per item, but the initial `io.ReadAll(r)` (heif.go:65) reads the entire file without a cap. Same category as TIFF — pre-existing pattern.

## Interaction Analysis (Sprint 8 changes vs each other)

### Interaction 1: iobuf Put-discard ↔ IPTC Encode
- IPTC Encode uses `encBufPool` (`sync.Pool` of `*bytes.Buffer`), NOT `iobuf`. Completely separate pool system. Put-discard change in iobuf has zero interaction with IPTC Encode. SAFE.

### Interaction 2: IPTC Encode purity (FINDING-002 fix) ↔ JPEG write path
- `jpeg.Inject` calls `writeIPTCSegment` → `buildIRB` → uses the []byte returned by `iptc.Encode`. The Encode output is a fresh `bytes.Clone`; it does not alias the IPTC receiver. The receiver's Records array is unmodified. Concurrent writes to separate `*IPTC` structs are safe; concurrent writes to the SAME `*IPTC` struct remain caller responsibility (documented). SAFE.

### Interaction 3: tiff magic gate ↔ RAW delegation
- ErrUnsupportedMagic wraps through all RAW error chains (arw:/cr2:/dng:/nef: prefix + tiff: prefix). `errors.Is` traversal confirmed. ORF/RW2 bypass tiff.Extract entirely; they apply their own magic checks before any TIFF parse. No regression. SAFE.

### Interaction 4: iobuf Put-discard ↔ JPEG write path
- `writeEXIFSegment`, `writeXMPSegments`, `writeIPTCSegment` all call `iobuf.Get(n)` and `iobuf.Put(p)`. All n values are bounded (JPEG segment limits ≤ 65,535 bytes) and well within `largeSize = 65,536`. No buffer will be discarded by the new cap > largeSize guard during a normal JPEG write. SAFE.

### Interaction 5: xmp maxXMPDocumentBytes ↔ JPEG extended XMP reassembly
- `reassembleExtendedXMP` produces a slice of size `main + extBytes`. extBytes ≤ 64 MiB (4 GUID × 16 MiB). The reassembled bytes are then passed to `xmp.Parse`, which checks `len(b) > maxXMPDocumentBytes (16 MiB)`. This means a JPEG with extended XMP > 16 MiB will be extracted correctly (rawXMP = reassembled bytes), but XMP parse will return ErrDocumentTooLarge, and in best-effort mode the XMP segment is warned and discarded. The metadata struct will have nil XMP. This is the correct, safe behavior. The cap is applied at the right layer. SAFE.

## Fuzz Sweep (Sprint 8 holistic — 2026-06-03)

All runs: 0 crashers, 0 panics.

| Target | Exec count | Duration |
|---|---|---|
| FuzzRead (root) | 5,331,916 | 25s |
| FuzzParseEXIF | 8,122,182 | 25s |
| FuzzParseIPTC | 10,837,386 | 25s |
| FuzzParseXMP | 12,270,408 | 25s |
| FuzzJPEGExtract | 15,105,300 | 30s |
| FuzzTIFFExtract | 298,534 | 25s (I/O bound) |
| FuzzHEIFExtract | 13,535,383 | 25s |

**Note on FuzzJPEGExtract**: First run returned "context deadline exceeded" — this is the fuzz harness timeout error reported when -fuzztime expires between goroutine wakeups, NOT a crasher. Re-ran at -fuzztime=30s: PASS, 15.1M execs, 0 crashers.

**Note on FuzzTIFFExtract**: 298K execs in 25s because `io.ReadAll(r)` in tiff.Extract causes the fuzzer to read the entire byte slice from the test reader on each iteration — I/O-bound behavior. Not a safety issue; tiff was already fuzz-covered in Task #48 at 4.6M execs (40s) with a larger seed corpus in that run's cache.

## Tooling Results

- `go test -race ./...`: **PASS** — all 35 packages
- `govulncheck ./...`: **PASS** — 0 vulnerabilities in called symbols; 3 in required-but-uncalled modules (informational)
- `go vet ./...`: **PASS** — 0 findings
- `golangci-lint run ./...`: **PASS** — 0 issues

## Open Findings (MEDIUM or higher severity)

**None.** All findings from Rounds 1 and 2 are CLOSED:

- FINDING-001 (IPTC OOB): CLOSED — Round 1
- FINDING-002 (HEIF findBox): CLOSED — Round 1  
- FINDING-003 (CR3 findBox): CLOSED — Round 1
- FINDING-004 (PNG unbounded allocation): CLOSED — Round 1
- FINDING-005 (WebP unbounded allocation): CLOSED — Round 1
- FINDING-006 (WriteFile EXDEV): CLOSED — Round 1
- FINDING-007 (JPEG rawEXIF < 8 bytes): CLOSED — Round 1
- FINDING-008 (XMP UTF-16 2× amplification): CLOSED — Sprint 8 pre-task-51
- FINDING-009 (JPEG extended XMP aggregate cap): CLOSED — Sprint 8 pre-task-53

Pre-existing LOW/INFO items (not regressions, not blocking):
- Uncapped `io.ReadAll` in TIFF/RAW/HEIF container reads — file-size == memory use; no multiplier; process memory limit is sole defence. Known architecture decision predating Sprint 8.
- `appendUTF8Rune` invalid codepoints produce bytes, not panic — INFO.
