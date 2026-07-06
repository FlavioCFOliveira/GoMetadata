---
name: audit-findings-20260609-containers
description: 2026-06-09 audit of format/jpeg/, format/png/, format/webp/, internal/riff/ — 4 new findings
metadata:
  type: project
---

# Audit 2026-06-09 — Container/Format Packages (JPEG, PNG, WebP, RIFF)

**Scope:** format/jpeg/, format/png/, format/webp/, internal/riff/

**Tooling:** go vet PASS, go test -race PASS (all 4 packages), 4 fuzz targets × 30s — 0 crashers.

## Findings

### F-CON-01 — JPEG Inject rawIPTC=nil drops all Photoshop APP13 resources — MEDIUM
- **Location:** format/jpeg/jpeg.go `Inject()`, `isOldMetadataSegment()`
- **Class:** Silent data loss on write path
- **Description:** When `rawIPTC=nil` is passed to `Inject`, all APP13 segments whose payload begins with "Photoshop 3.0\0" are unconditionally stripped via `isOldMetadataSegment`. Non-IPTC 8BIM resources in those segments (0x0425 IPTC-Digest, 0x040C thumbnail sketch, 0x040F ICC clipping path, etc.) are permanently lost. Only the 0x0404 IPTC-NAA resource should be removed; all siblings should be preserved.
- **Trigger:** Caller passes rawIPTC=nil to remove IPTC from a JPEG that carries additional Photoshop 8BIM resources in APP13.
- **PoC:** Confirmed — see TestJPEGInjectNilIPTCDropsAllPhotoshop scratch test (output: 0x0425 gone).
- **Impact:** Silent data loss. Photo editing pipelines that strip IPTC also lose Photoshop metadata.
- **Exploitability:** Confirmed (data-loss path).
- **Spec:** CLAUDE.md §5 "preserve all existing metadata not explicitly modified"; containers.md §1(e).
- **Remediation:** When rawIPTC=nil, instead of stripping the entire APP13: read origIRB (always, not only when rawIPTC!=nil), then build a new IRB by calling `spliceIPTCIntoIRB(origIRB, nil)` — which would omit the 0x0404 block while keeping all other 8BIM blocks.

### F-CON-02 — JPEG Inject loses sibling resources from second+ APP13 segments when replacing IPTC — MEDIUM
- **Location:** format/jpeg/jpeg.go `extractOriginalIRB()`
- **Class:** Silent data loss on write path
- **Description:** `extractOriginalIRB` only captures the first Photoshop APP13 segment. When IPTC spans multiple APP13 segments (e.g. original had IPTC in segment 2 and a thumbnail/digest in segment 2 as well), sibling 8BIM resources from segments beyond the first are silently dropped after Inject.
- **Trigger:** Input JPEG has multiple Photoshop APP13 segments with non-0x0404 resources in the second+ segments.
- **PoC:** Confirmed — TestMultiAPP13SiblingPreservationSecondSegment scratch test.
- **Impact:** Data loss in multi-segment APP13 files (rare but real: old tools that split large APP13 payloads).
- **Exploitability:** Confirmed.
- **Remediation:** `extractOriginalIRB` should concatenate IRB payloads from ALL APP13 segments (same as the read path already does). Then `spliceIPTCIntoIRB` operates on the combined IRB, preserving all sibling blocks.

### F-CON-03 — PNG Inject writes output signature before validating input signature — LOW
- **Location:** format/png/png.go `Inject()` lines ~281-291
- **Class:** Spec deviation, defense-in-depth gap
- **Description:** `Inject` reads the 8-byte PNG signature from `r` but does NOT validate it. It writes the correct `pngSig` to `w` unconditionally. If a non-PNG input is passed, `w` receives the valid PNG signature before any error is detected. If `w` is a persistent writer (os.File) the file is partially corrupted before the error propagates.
- **Trigger:** Caller passes a non-PNG io.ReadSeeker directly to png.Inject (bypasses the format dispatcher).
- **PoC:** Confirmed — TestPNGInjectMissingSignatureValidation: out.Bytes()[:8] == pngSig when non-PNG input given.
- **Impact:** Partial file corruption if w is a file and error is not checked before use of output.
- **Exploitability:** Probable (requires misuse of internal API, but library is public).
- **Remediation:** Validate `sig == pngSig` immediately after reading; return ErrInvalidSignature before writing to `w`.

### F-CON-04 — PNG Inject produces structurally incomplete output (no IEND) when source is missing IEND — LOW
- **Location:** format/png/png.go `Inject()` chunk loop
- **Class:** Spec conformance — PNG §5.6 IEND required as last chunk
- **Description:** When source PNG is missing IEND (truncated/malformed), Inject exits the chunk loop on io.EOF without writing IEND to the output. The output PNG is structurally incomplete per PNG §5.6.
- **Trigger:** Source PNG lacks IEND chunk (malformed/truncated input).
- **PoC:** Confirmed — TestPNGInjectMissingIENDOutputMissingIEND: IEND not present in output bytes.
- **Impact:** Output PNG rejected by strict readers; cascades downstream corruption.
- **Exploitability:** Probable (malformed input triggers incorrect output).
- **Remediation:** After the chunk loop exits via EOF (not errPNGDone), write an IEND chunk to ensure output is always well-formed per spec.

## Confirmed FIXED (from memory)
- #145 (multiple APP13 last-wins): CONFIRMED FIXED — all APP13 payloads are now concatenated in `scanMetadataSegmentsWithWire`.

## Not-finding (checked and clean)
- VP8X flag bits (EXIF=0x08, XMP=0x04) confirmed correct against real corpus files.
- All fuzz targets: 0 crashers in 30s each (FuzzJPEGExtract, FuzzPNGExtract, FuzzWebPExtract, FuzzWebPInject).
- go test -race: PASS all 4 packages.
- iobuf pool: no contamination or race issues.
- JPEG readSegment scratch reallocation: correct.
- PNG CRC pool (defer Put after error return): safe, hash is Reset() on next Get.
- PNG chunk zero-length (legal): handled.
- WebP odd-size padding: correct.
- WebP VP8X size < 10: handled (nil origVP8XData, zero canvas).
- RIFF SkipChunk: no overflow in practice.

**Why:** F-CON-01 and F-CON-02 are the highest-priority write-path data-loss bugs.
**How to apply:** Before any release, ensure both are fixed and have regression tests.
