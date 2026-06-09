---
name: project-jpeg-conformance-battery
description: JPEG container spec conformance test battery (task #155) — §1 assertions covered, bug found in processAPP1Segment
metadata:
  type: project
---

Task #155 implemented `format/jpeg/conformance_test.go` — a JPEG §1 conformance battery covering all assertions in `docs/conformance/containers.md §1`.

## What was implemented

26 sub-test functions covering:
- §1(c): `TestJPEGMarkerLengthIncludesItself`, `TestJPEGMaxSingleSegmentPayload`, `TestJPEGFF00StuffingPassthrough`, `TestJPEGStandaloneMarkersNoLength`
- §1(d): `TestJPEGEXIFPrefixRequired`, `TestJPEGXMPNamespaceURIPrefix`, `TestJPEGExtendedXMPHeaderLayout`, `TestJPEGIPTCResourceID0404`
- §1(e): `TestJPEGWriteAPPnBeforeSOS`, `TestJPEGWriteSegmentLengthCorrect`, `TestJPEGEXIFPayloadLimit65533`, `TestJPEGExtendedXMPFullLengthOffsetConsistent`
- §1(f): `TestJPEGRobustTruncatedAfterSOI`, `TestJPEGRobustMissingEOI`, `TestJPEGRobustLpGreaterThanRemaining`, `TestJPEGRobustLpLessThan2`, `TestJPEGRobustAPPnPastEOF`, `TestJPEGRobustMultipleExifUsesFirst`, `TestJPEGRobustExtendedXMPMissingChunks`, `TestJPEGRobustExtendedXMPDuplicateOffsets`, `TestJPEGRobustExtendedXMPGUIDMismatch`, `TestJPEGRobustMultipleAPP13Concatenation`, `TestJPEGRobustMalformedIRBLength`, `TestJPEGRobustFillBytesBeforeMarker`
- Corpus parity: `TestJPEGCorpusParity` (skips if no corpus present)

## Bug found and fixed in processAPP1Segment

`processAPP1Segment` in `jpeg.go` unconditionally overwrote `rawEXIF` and `rawXMP` on every matching APP1, instead of first-wins.

**Fix**: Added `if rawEXIF == nil` guard (EXIF) and `if rawXMP == nil` guard (XMP) so subsequent segments are ignored.

**Spec citation**: containers.md §1(f): "multiple Exif\0\0 (use first)"; Adobe XMP Part 3 §1.1.3 (one standard XMP per file).

**Why:** Without this fix, a JPEG with two EXIF APP1 segments returns the second one, violating the spec and silently corrupting metadata for any camera that writes two EXIF segments.

**How to apply:** Any future JPEG metadata parser must guard all first-seen metadata buckets against overwrites from duplicate segments.
