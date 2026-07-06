---
name: project_gmw1_imageblock_budget
description: GM-W1 (task #261) TIFF write-path DoS fix — per-entry caps + aggregate imageBlockBudget in format/tiff/relocate.go
metadata:
  type: project
---

GM-W1 (CWE-770/CWE-405, rmp task #261): confirmed HIGH-severity DoS in the
TIFF/DNG/CR2/NEF/ARW/ORF/RW2 WRITE path. `extractParallelOffsetBlocks`
(strips/tiles, tag 0x0111/0x0117/0x0144/0x0145) and `enumerateSubIFDsAt`
(SubIFDs, tag 0x014A) in `format/tiff/relocate.go` allocated one
heap struct (*imageBlock / *subIFDInfo) per attacker-declared Count element,
with no cap beyond "does the array fit in the source buffer" (up to ~64M
elements for a 256 MiB file). PoC: n=10,000,000 strips in a 76.3 MiB file
drove ~2.1 GiB TotalAlloc / 2.2 GiB RSS; n=10,000,000 SubIFDs in a 38.1 MiB
file drove ~263 MiB alloc. Reachable via a plain `gometadata.Read`→`Write`
round-trip (m.EXIF non-nil after Read always triggers relocation) — no
caller-side metadata edit required.

Fix (mirrors read-path `traverseBudget` in exif/ifd.go, see [[project_ifdchain01_traversal_budget]]):
- `maxImageBlocksPerOffsetEntry = 65536` — per-entry cap for strips/tiles
  (TIFF 6.0 §7/§15: real files need at most tens of thousands).
- `maxSubIFDsPerEntry = 1024` — per-entry cap for 0x014A (real DNG has 1-3).
- `maxAggregateImageBlocks = 262144` (2^18, fixed — NOT scaled by file size,
  unlike traverseBudget's entries dimension) — a single `imageBlockBudget`
  shared across `enumerateImageBlocks` + `enumerateSubIFDs` per relocate
  call, closing the residual amplification where many entries/IFDs, each
  individually within its own per-entry cap, are chained via the IFD1
  next-IFD chain (≤512, exif's maxTraverseChainIFDs) or nested SubIFD
  recursion (≤8, maxSubIFDDepth) to multiply the total far beyond any real
  file. IMPORTANT: scaling the aggregate budget by len(base) (like
  traverseBudget does) does NOT work here — an attacker can choose a large
  legal file size (up to 256 MiB) specifically to inflate a size-scaled
  budget, then exploit near-zero-cost elements (size=0 "phantom" strips) to
  approach that inflated budget. A FIXED ceiling is the correct mirror of
  traverseBudget's `ifds` dimension (also fixed at 512), not its `entries`
  dimension.
- `ErrTooManyImageBlocks` sentinel added to `format/tiff/errors.go`.
- Cap checks happen BEFORE `make([]*imageBlock, 0, n)` / `make(map[uint32]bool, n)`
  — order matters: put the per-entry cap check immediately after computing
  `n` from `.Count`, before any array-length validation, so rejection is
  O(1) regardless of how large n claims to be.
- Belt-and-suspenders: also clamp the `map[uint32]bool` size hint in
  `enumerateSubIFDsAt` with `min(n, maxSubIFDsPerEntry)` even though n is
  already capped by that point — matches existing codebase vocabulary
  ("belt-and-suspenders", see extractRawIFD comments).

Signature change: `enumerateImageBlocks`, `enumerateIFDBlocks`,
`extractParallelOffsetBlocks`, `enumerateSubIFDs`, `enumerateSubIFDsAt` all
gained a trailing `*imageBlockBudget` parameter. This function is called
from 5 places (`relocate.go`, `relocate_nef.go`, `relocate_rw2.go`,
`relocate_orf.go`, `relocate_arw.go`) — each construct one
`newImageBlockBudget()` and share it across both the `enumerateImageBlocks`
and `enumerateSubIFDs` calls in that function so the budget is truly
per-write, not per-call.

Testing note: `parseIFDEntry` (exif/ifd.go) REJECTS an OOL IFD entry whose
`valOff+totalSize > len(b)` — so you cannot get a huge Count with a tiny
file through a real `exif.Parse`; the value array must actually be present
in the buffer. To build a "huge Count, small file" end-to-end fixture, use
TypeShort (2 bytes/element) instead of TypeLong, and leave the array
zero-filled with `make([]byte, n)` — my cap check fires on the Count field
alone, before any element is read, so array contents never matter. For unit
tests of the internal functions directly (bypassing exif.Parse entirely),
construct `*exif.IFDEntry{Count: huge, Value: make([]byte, 4)}` by hand —
this is the only way to prove "rejected before touching the value array" in
isolation. New tests live in
`format/tiff/relocate_imageblock_budget_test.go`.

Related: [[project_ifdchain01_traversal_budget]] (read-path equivalent),
[[project_heif_iloc_offbyone_243]] (same CWE-770/405 family, HEIF).
