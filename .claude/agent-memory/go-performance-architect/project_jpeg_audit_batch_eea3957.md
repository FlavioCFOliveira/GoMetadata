---
name: project_jpeg_audit_batch_eea3957
description: JPEG audit batch fixes #122/#123/#134/#135/#151/#174 — extended XMP validation, xmp.Parse merge, truncation surface, GUID normalization, IRB pad clamp, 8BIM sibling preservation
metadata:
  type: project
---

Commit eea3957 fixed six audit findings in format/jpeg/jpeg.go + read.go + xmp/write.go.

Key architectural changes:

- **#122**: `mergeExtendedChunksValidated` validates chunk[0].offset==0, contiguous offsets, totalLen==declaredFullLen; `appendExtendedXMPChunk` now tracks `extFullLens map[string]uint64` alongside extSizes; `processAPP1Segment` and call sites updated with the extra map.
- **#123**: `reassembleExtendedXMPByParse` uses `xmppkg.Parse` on both main+ext and merges Properties maps; returns nil when extXMP has 0 properties (raw RDF fragment, not a full packet) so fallback byte-splice handles those cases correctly.
- **#134**: `xmpResult.truncated bool` threaded from `appendExtendedXMPChunk` → `buildXMPResult` → `scanMetadataSegmentsWithWire` → `extractFullInternal`; `ExtractFull` gains a 7th return `xmpTruncated bool`; `read.go`'s `extractByFormat` also returns it; `Read` converts it to a `ParseWarning(ErrExtendedXMPTruncated)`.
- **#135**: `appendExtendedXMPChunk` calls `isAllHex(rawGUID)` + `strings.ToUpper`; `buildXMPResult` also uppercases the GUID extracted from main before map lookup.
- **#151**: `parseIRB` clamps `pos = len(b)` after the even-padding increment; `xmp/write.go` gains a comment documenting localListPool.Put ordering.
- **#174**: `extractOriginalIRB` accumulates ALL APP13 payloads (goto-loop pattern); `Inject` always extracts origIRB; `spliceIPTCIntoIRB(origIRB, nil)` removes 0x0404 while preserving siblings; `writeIPTCOrSiblings` helper emits stripped APP13 for nil-IPTC+siblings case; `writeIPTCSegmentRaw` writes pre-built IRB bytes.

**Why:** the `reassembleExtendedXMPByParse` fallback to byte-splice is essential — existing test fixtures and most real extended XMP payloads are raw RDF fragments, not complete XMP packets; the xmp.Parse path only activates for full documents with non-rdf prefixes.

**How to apply:** whenever touching extended XMP reassembly: always thread both `extFullLens` and `extTruncated` through the call chain; `buildXMPResult` is the single integration point.
