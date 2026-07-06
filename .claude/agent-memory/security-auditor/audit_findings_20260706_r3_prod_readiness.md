# Production-Readiness Security Audit — Round 3 (2026-07-06)

Sprint 41 (roadmap `gometadata`). Fresh full-surface audit for production
readiness, on top of the R1/R2 rounds (#243–#253, shipped in v1.2.0).

## Baseline before audit
`go build`, `go vet`, `govulncheck`, `go test -race`, and `golangci-lint`
all green at `18f8ece`. All prior-round findings confirmed fixed.

## Method
- 5 parallel auditors by domain:
  1. `exif/` (IFD/MakerNote/GPS offset arithmetic, cycles, overflow)
  2. containers (HEIF/ISO-BMFF, TIFF + relocators, WebP/RIFF, format detect)
  3. jpeg / png / 7×raw (segment lengths, PNG decompression bomb, RAW offsets)
  4. iptc / xmp (IIM length fields, XXE, billion-laughs, packet-scan loops)
  5. top-level io / write / concurrency (symlink/TOCTOU, atomic replace, caps, `-race`)
- 12-target empirical fuzz sweep (FuzzRead end-to-end + per-parser), 0 crashers.
- Independent adversarial verification pass on the two HIGH fixes.

## Findings (5) and resolution — all fixed and verified
| ID | Sev | Location | Fix commit |
|----|-----|----------|-----------|
| EXIF-IFDCHAIN-01 | HIGH (CWE-400/405/834) | exif/ifd.go traverse* | c2d820f (#255) |
| CR3-EXTSIZE-01 | HIGH (CWE-682/390) | format/raw/cr3/cr3.go | 80c52a6 (#256) |
| HEIF-32BIT-01 | MED (CWE-681→190→129) | format/heif/heif.go | 31f6823 (#257) |
| FUZZ-COVERAGE | MED | jpeg/png/7×raw write paths | 201018c (#258) |
| IO-HARDENING | LOW/INFO (CWE-732-adj) | write.go + SECURITY.md | d93edb0 (#259) |

### EXIF-IFDCHAIN-01 (required two cycles)
Overlapping IFD next-chain → quadratic heap/CPU (64 KB → 2.8 GB; 96 KB → OOM).
Cycle map caught exact-offset loops only; no chain-length / cumulative-entry cap.
Fix: shared per-Parse `traverseBudget` (cap 512 IFDs; cumulative entries
`len(b)/6`, floor 64) across classic-TIFF chain + Exif/GPS/Interop sub-IFDs.
Adversarial verification found a RESIDUAL: budget charged post-dedup, so an IFD
of ~65535 identical-tag entries (dedup→1) evaded the entries dimension (770 KB
→ ~4 GB / ~1.74 GB retained). Second cycle charges the pre-dedup PARSED count
and right-sizes the dedup backing array. Verified: 770 KB → ~24 MB / chain 3;
258 KB → 5.3 MB. MakerNote nil-budget confirmed NOT an independent amplifier.

### CR3-EXTSIZE-01
`cr3.Inject` hardcoded 8-byte box headers; ISO 14496-12 §4.2 extended-size
(`size==1`+largesize) moov/uuid boxes (accepted on read) caused silent EXIF
discard / sibling CMT2+XMP loss with no error. Fix: re-derive real header length
via `parseCR3BoxHeader` at both slice sites + `size>=headerLen+16` guard in
`flatUUIDBoxRange` (latent OOB). Adversarially verified across extended moov,
extended uuid, both, undersized uuid, size==0, non-zero offset, and stco
relocation over the 16→8 header shrink. CONFIRMED-FIXED.

### HEIF-32BIT-01
`extractExifFromData` / `parseIinfItemCount`: `int(uint32)` narrowing → negative
slice-index panic on 32-bit GOARCH for prefixes ≥ 0x80000000. Fix: `uint64`
before narrowing (project pattern from detect.go/tiff.go). Verified via GOARCH=386/arm/mips
cross-compile; no other unguarded narrowings on the HEIF read path. CONFIRMED-FIXED.

### FUZZ-COVERAGE
No write-path fuzz targets existed for jpeg/png/raw — the gap that let
CR3-EXTSIZE-01 ship. Added 9 `FuzzXxxInject` targets (2.7M–7.9M execs/20s, 0
crashers); FuzzCR3Inject seeded with the extended-size PoC.

### IO-HARDENING
Temp file now masks setuid/setgid/sticky (rewrite must not propagate privilege
bits). Documented write trust boundaries in SECURITY.md + doc comments (symlink
follow is intentional; streaming Write may emit partial output — WriteFile is atomic).

## Clean verdicts (no findings)
- IPTC / XMP: 0 findings. XXE and billion-laughs are structurally impossible —
  the XMP parser is a hand-rolled non-recursive scanner with an explicit depth
  cap and no `encoding/xml` in the parse path. All allocation caps boundary-tested.
- Containers / io / write / concurrency: PRODUCTION-READY. `-race` clean;
  read+write allocation bounded via `io.LimitReader(maxFileSize+1)`; atomic
  replace + fsync correct; no error info-leak.

## Final gate (all green)
gofmt · go build ./... · go vet ./... · go test -race ./... · golangci-lint (0)
· govulncheck (none) · final fuzz sweep (FuzzRead 1.27M, FuzzTIFFExtract,
FuzzParseEXIF) 0 crashers.

## Verdict
**GoMetadata is PRODUCTION-READY.** All 5 findings fixed, verified, and
regression-tested. KG: Audit node 691 (R3-prod-readiness), 5 commits, 9 fuzz
targets, 5 regression tests.
