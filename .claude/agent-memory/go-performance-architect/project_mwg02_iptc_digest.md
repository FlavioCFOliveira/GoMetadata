---
name: project_mwg02_iptc_digest
description: MWG-02 IPTC digest reconciliation implemented (task #168) — digest-aware precedence in Caption/Copyright/Creator/Keywords
metadata:
  type: project
---

MWG-02 IPTC-digest reconciliation is implemented (task #168).

**Why:** MWG Guidelines v2.0 §3.3.1 requires comparing MD5(raw 0x0404 IIM block) to the
stored 0x0425 Photoshop resource to decide IPTC vs XMP read priority. This logic lives in
the root cross-format reconciliation layer, not inside any single format package.

**How to apply:**
- `iptc.Digest(rawIIM)` → `[16]byte` MD5; `iptc.DigestMatch(rawIIM, stored)` → `(match, unknown bool)`.
- JPEG's `format/jpeg.ExtractFull()` surfaces both rawIPTC and iptcDigest (nil when absent).
- `read.go` → `extractByFormat` calls `ExtractFull` for JPEG; wires `rawIPTCDigest` into `Metadata`.
- `metadata.go` → `iptcTrustElevated()` applies the three-state logic:
  - nil digest → default XMP priority (MWG-01).
  - all-zero sentinel → IPTC elevated.
  - non-zero digest mismatch → IPTC elevated.
  - non-zero digest match → XMP priority (default).
- `Caption`, `Copyright`, `Creator`, `Keywords` check `iptcTrustElevated()` and flip IPTC before XMP when true.
- `metadata_conformance_test.go` contains `TestConformance_MWG02` with 7 parallel sub-tests (all pass under -race).
- `iptc/digest.go` + `iptc/digest_test.go` are the new pure-computation files.
- `format/jpeg/jpeg.go`: `extractIPTCFromIRBPayloads` removed; replaced by `parseIRBForIPTCAndDigest` + `extractIPTCAndDigestFromIRBPayloads`; internal shared impl renamed `extractFullInternal` to avoid revive confusing-naming with public `ExtractFull`.
