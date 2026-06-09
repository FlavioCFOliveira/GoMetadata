---
name: project-png-conformance-battery
description: PNG container conformance test battery (task #157) — 40 top-level tests, 2 spec violations fixed
metadata:
  type: project
---

PNG conformance battery implemented in `format/png/conformance_test.go` (task #157).

**Why:** Sprint 23/24 conformance test series; PNG is a major container format.

**How to apply:** When extending PNG tests, follow the sub-test naming convention `PNG-<rule>` matching containers.md §2 rule IDs.

## Sub-tests covered (40 top-level, ~1000+ corpus sub-tests)

- `TestPNGSignaturePositive` / `TestPNGSignatureBadByte` — PNG-signature §5.2 (8 per-byte sub-tests)
- `TestPNGChunkLayoutLengthCountsDataOnly` / `TestPNGChunkLayoutZeroLengthLegal` — PNG-chunk-layout §5.3
- `TestPNGChunkCRCPolynomial` / `TestPNGChunkCRCCoversTypeAndData` — CRC poly 0xEDB88320 §5.5
- `TestPNGChunkCRCMismatchDetected` — eXIf/iTXt/tEXt CRC mismatch detection
- `TestPNGChunkCRCValidPasses` — positive CRC path
- `TestPNGChunkLengthMax2Pow31` / `TestPNGChunkLengthSpecBoundary` — §11.2.1 length guards
- `TestPNGChunkTypeBitsAncillary` / `TestPNGChunkTypeBitsSafeToCopy` — property bits §5.4
- `TestPNGIHDRFirst` / `TestPNGIENDLast` / `TestPNGIENDLastInject` — ordering §5.6
- `TestPNGEXIfNoExifPrefix` / `TestPNGEXIfInjectNoPrefix` / `TestPNGEXIfCorpusNoPrefix` — no Exif\0\0 prefix §11.3.4.4
- `TestPNGITXtKeywordXMLComAdobeXMP` / `TestPNGITXtCompressionFlag0` / `TestPNGITXtEmptyLangAndTranslatedKeyword` / `TestPNGITXtUseFirstXMP` / `TestPNGITXtXMPRoundTrip` — XMP embedding PNG-01..05
- `TestPNGWriteCRCOverTypeAndData` / `TestPNGWriteAncillaryBetweenIHDRAndIEND` / `TestPNGWritePreserveSafeToCopyChunks` — write correctness §2(e)
- 8× `TestPNGRobust*` tests — robustness §2(f)
- 3× `TestPNGCorpus*` tests — corpus parity (919 files)

## Spec violations found and fixed

1. **PNG-04 / XMP Part 3 §1.6 — `handleXMPChunk` overwrote first XMP with subsequent iTXt**: The `iTXt` case did not check `existing != nil`. Fixed in `png.go:handleXMPChunk` by adding early-return when `existing != nil` (applies to all chunk types including iTXt).

2. **TestPNGChunkLengthMaxAccepted had wrong expectation**: `MaxInt32` passes the spec guard (≤ 2^31-1) but fails the application-level 256 MiB guard, which also returns `ErrChunkTooLarge`. Both guards are correct and intentional. Test renamed to `TestPNGChunkLengthSpecBoundary` with corrected expectation.
