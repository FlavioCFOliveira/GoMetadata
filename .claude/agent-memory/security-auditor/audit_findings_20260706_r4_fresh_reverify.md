---
name: audit_findings_20260706_r4_fresh_reverify
description: Fresh independent production-readiness re-audit (round 4) at HEAD 7781cb0 — full 29-target fuzz sweep + static re-verification of all round-3 fixes, 0 new findings
type: project
---

# Round 4 fresh independent audit — 2026-07-06, HEAD 7781cb0

Requested as a standalone "start from scratch" production-readiness audit, separate from
the same-day R1/R2/R3 rounds already recorded. Working tree had no uncommitted code changes.

## Method
- Enumerated all `func Fuzz*` (29 total, not 28 as an earlier memory note said — recount:
  1 exif + 2 heif + 2 jpeg + 2 png + 2 arw + 2 cr2 + 2 cr3 + 2 dng + 2 nef + 2 orf + 2 rw2 +
  2 tiff + 2 webp + 1 root FuzzRead + 1 riff + 1 iptc + 1 xmp = 29).
- Ran all 29 in 4 parallel batches (45-60s each), ~27.6M total execs, **0 crashers**, no new
  testdata/fuzz/* corpus files written (confirms no live crash).
- Re-read and independently verified (not just cited) all 5 round-3 fixes (#255-#259):
  EXIF-IFDCHAIN-01, CR3-EXTSIZE-01, HEIF-32BIT-01, FUZZ-COVERAGE, IO-HARDENING (setuid mask).
  All confirmed correctly implemented, no residual gap.
- Cross-checked the maxFileSize=256MiB cap is applied on literally every io.ReadAll(r) call
  across the whole module (tiff/orf/rw2/cr3/heif have it directly; arw/dng/nef/cr2 inherit it
  by delegating Extract/Inject entirely to package `tiff`).
- Traced PNG's zlibDecompress call sites: keyword check happens BEFORE decompression in all
  3 extractors (extractXMPFromITXt/ZTxt/TExt), and handleXMPChunk's "first XMP wins" early
  return means at most ONE zlib decompression is ever attempted per Extract() call — so an
  attacker cannot chain many small compressed chunks for amplification (verified by reading
  code, this was not explicitly stated in prior audit notes).
- Verified RIFF/WebP chunk loop always advances (ReadChunk consumes exactly 8 header bytes
  via io.ReadFull regardless of declared Size) — no infinite-loop risk from a 0-size chunk.
- Verified XMP RDF parser's depth>100 cap (xmp/rdf.go:648-649) and confirmed no encoding/xml
  in the parse path (billion-laughs/XXE structurally inapplicable, by reading not just citing).

## Result
No new findings. All prior findings (R1/R2/R3, and every 06-04/06-09 finding) remain fixed
with no regression. govulncheck/golangci-lint/go vet/-race all clean.

## Verdict
**READY** for production (security lens). See production_readiness_20260706.md and
audit_findings_20260706_r3_prod_readiness.md for the fuller historical trail this round
re-confirmed.
