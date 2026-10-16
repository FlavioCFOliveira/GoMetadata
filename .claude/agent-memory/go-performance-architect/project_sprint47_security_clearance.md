---
name: sprint47-security-clearance
description: Sprint 47 (2026-10-16) fixes of audit findings F-01..F-10 (tasks #372-#383); limits chosen, traps hit, how to reproduce
metadata:
  type: project
---

Audit-driven fixes, one batched effort, regression tests named `F-NN` in `security_audit_v150_test.go` of heif, png, tiff, exif, xmp, plus `internal/trailer/trailer_test.go`. PoC modules live in the session scratchpad (`poc1`..`poc10`), each a module with `replace` to the repo.

Limits introduced (all return a specific error, none truncate silently except the thumbnail drop, which adds an `EXIF.Warnings` entry):
- heif: extents validated against file length / idat before `make`; `patchAncestorSize` walks only boxes starting before `meta`, returns `errMalformedAncestorBox`.
- trailer: `maxAFCPSignatures`=256 signatures collected in one pass, EOF record looked up in that list; more than 256 means "not AFCP" (copied unchanged).
- xmp: `maxXMLElements`=1<<19, `maxXMLAttrs`=1<<20 -> `ErrXMLTooComplex`; hash index of ns bindings above 32 in scope; CDATA chain joined once.
- exif: JPEG thumbnail bytes of one Parse bounded by `len(b)` (`traverseBudget.thumb`, `chargeThumbnail`); EncodeRebased shares one carry allowance (MakerNote + private IFDs) and caps 64 private IFDs -> `ErrCarriedDataTooLarge`.
- tiff: `checkBlocks` (sum of block sizes <= file length) before the header is written -> `ErrImageBlocksOverlap`; each SubIFD visited costs `subIFDBudgetCost`=4 budget units.
- png: `profileBudget` 32 MiB of inflated raw-profile text per Extract/Inject -> `ErrRawProfileBudget`; later chunks of an already filled kind are not inflated.

**Traps hit:**
- A fixed-size hash table (256 buckets) is still quadratic: the oldest binding (`xml`, index 0) sits at the tail of a chain of N/256 entries. The table must grow with the scope (`rebuildNSIndex`, doubling). Measured: 100k decls 143 ms -> 14.7 ms only after growth.
- Zero-value structs are used in tests (`var st extractState`): a budget stored as "left" starts at 0 and rejects everything; store "used" so the zero value is a full budget.
- The AFCP PoC only reproduces with a base offset other than 0: with base 0 the repeated unit matches the first candidate immediately.
- `append` grows big slices by 25%, so a table filled to the cap copies ~5x its final size; `growDoubling` cut the 15 MiB `<a/>` PoC from 245 to 133 MiB.
- IFD0 thumbnail is never written by Encode but still consumes the thumbnail budget (IFD0 comes first in the chain).
- `GOARCH=386 go vet ./...` on darwin fails with "default PIE binary requires external (cgo) linking" (no darwin/386 port); use `GOOS=linux GOARCH=386`.
- Timing assertions must leave room for `-race` (about 15x slower); allocation assertions need a sequential, non-parallel top-level test because `TotalAlloc` is process wide (`//nolint:paralleltest`).
- Public error text must avoid IFD/APP13/rdf vocabulary even for new sentinels.
