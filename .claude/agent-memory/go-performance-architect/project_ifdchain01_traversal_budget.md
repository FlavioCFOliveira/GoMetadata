---
name: project_ifdchain01_traversal_budget
description: EXIF-IFDCHAIN-01 fix (task #255) — traverseBudget caps cumulative entries + chain length across IFD0/sub-IFD traversal to kill O(N^2) DoS
metadata:
  type: project
---

Fixed EXIF-IFDCHAIN-01 (CWE-400/405/834): overlapping IFD next-chains at
distinct stride-4 offsets caused O(K*C) memory/CPU amplification (measured
pre-fix: 16KB->174MB/59ms, 64KB->2.82GB/1.24s). The visited-offset cycle
detector never caught this because every offset in the malicious chain is
distinct.

**Why:** rmp task #255, security audit finding. Cycle detection (map[uint32]bool
keyed by exact offset) bounds cycles but not a chain of K IFDs at K unique
offsets whose 12-byte entry regions overlap almost entirely.

**Fix design** (`exif/ifd.go`, `traverseBudget` type immediately above `traverse()`):
- Two plain-int fields: `entries` (cumulative retained-entry cap) and `ifds`
  (cumulative chain-length cap). No allocation — passed as `*traverseBudget`.
- `newTraverseBudget(n int)`: `entries = max(n/6, 64)`, `ifds = 512`
  (`traverseEntryBudgetDivisor=6`, `traverseEntryBudgetFloor=64`,
  `maxTraverseChainIFDs=512`). n/6 is ~2x the max non-overlapping 12-byte
  entries a buffer of size n could legitimately hold — generous for every
  real camera file (IFD0->IFD1, a few dozen entries each).
- Checked via `budget.exhausted()` BEFORE parsing each IFD in the chain loop
  (in `traverse`, `traverseWithArena`, `traverseBigTIFF`); charged via
  `budget.spend(len(ifd.Entries))` AFTER linking the IFD into the chain.
  One-hop overshoot is possible (check-before-parse) but bounded by the
  per-IFD buffer-fit clamp (~len(b)/12), so total work stays O(len(b)).
- `traverse`/`traverseWithArena`/`traverseBigTIFF` all gained a trailing
  `budget *traverseBudget` parameter. `nil` means "size a fresh budget from
  this call's own buffer length" (used by all ~17 MakerNote call sites in
  `makernote_parse.go` — each MakerNote blob is independently bounded, no
  invasive dispatch-table signature changes needed).
- `Parse()` (`exif/exif.go`) creates ONE `traverseBudget` per call (classic
  TIFF and BigTIFF branches each create their own) and threads its address
  through IFD0's `traverse`/`traverseBigTIFF` call AND into
  `parseExifSubIFDs`/`parseGPSSubIFD` (classic) or
  `parseExifSubIFDsBigTIFF`/`parseGPSSubIFDBigTIFF` (BigTIFF) — so IFD0 +
  ExifIFD + GPSIFD + InteropIFD share ONE allowance per Parse call, closing
  the "fresh full allowance per sub-IFD pointer" loophole for the
  highest-value attack surface (fully attacker-controlled main buffer).
- Two new warnKind values: `warnTraverseBudgetExceeded` (classic),
  `warnBigTIFFTraverseBudgetExceeded` (BigTIFF), following the existing
  task #200 deferred-parseWarn pattern exactly (compact struct, rendered via
  `warnString`, no fmt.Sprintf on the hot path).

**How to apply:** any future traversal entry point that follows a next-IFD
or similar chain must thread a `*traverseBudget` (share one across sibling
calls within the same logical Parse when the buffer is attacker-controlled;
nil-default is fine for independent, generally-smaller blobs like MakerNote).
See [[feedback_parseifdentry_abi_constraint]] — `spend()`/`exhausted()` are
per-IFD (chain-loop) calls, not per-entry, so they are nowhere near the
ARM64 register-budget hot path that constrained `parseIFDEntry`'s signature;
no ABI concerns here.

**Regression test:** `exif/ifdchain01_regression_test.go`,
`TestEXIFIFDCHAIN01_TraversalBudget` with two subtests:
`EXIF-IFDCHAIN-01_overlapping_chain_bounded` (600-hop, 250-entries-per-hop
stride-4 overlapping chain; asserts chain length <= `maxTraverseChainIFDs`
and retained entries <= `newTraverseBudget(len(buf)).entries + perHop`) and
`EXIF-IFDCHAIN-01_wellformed_multi_ifd_unaffected` (plain IFD0->IFD1, proves
no regression). Verified the malicious subtest actually fails (chainLen=600)
when `exhausted()` is stubbed to `return false` — confirms it is a real gate,
not a vacuous test.

**Benchmark evidence (Apple M4, before vs after, count=3):** BenchmarkEXIFParse
166-167ns/337B/4allocs both; BenchmarkEXIFParse_Camera ~1550-1560ns/2594B/8allocs
both; BenchmarkParseBigTIFF_Simple ~186-189ns/337B/4allocs both;
BenchmarkMakerNoteDispatch ~120-121ns/208B/4allocs both; BenchmarkParseGPS
~42ns/0B/0allocs both. No measurable regression — the budget check is a
couple of int comparisons per IFD (chain length is tiny for real files).

**ROUND 2 RESIDUAL (2026-07-06, adversarial re-verification found a HIGH):**
charging `budget.spend(len(ifd.Entries))` — the RETAINED, post-dedup count —
was insufficient. An attacker fills each IFD with up to 65535 (classic
uint16 count-field ceiling) IDENTICAL-TAG entries; the duplicate-tag dedup
pass (audit finding #126, TIFF 6.0 §2) collapses each IFD to ~1 retained
entry, so the entries budget dimension barely moves and only the (looser)
512-IFD chain cap remains — a bounded but severe ~4000x amplification.
Measured via public `gometadata.ReadFile`: 258KB->chain 512/~0.83s CPU/
~595MB retained; 770KB->chain 512/~5.35s CPU/~13GB peak heap/~1.74GB
retained, plateauing at a constant (512 x 65535-entry) ceiling.

**Fix:** `fillIFD`/`fillIFDBigTIFF` now return a 4th value, `parsedCount`
(`int`/`uint64`) — the CLAMPED, buffer-fit-checked loop bound they actually
iterated, returned VERBATIM (== the `count` parameter), BEFORE the dedup
pass runs. `parseSingleIFD`/`parseSingleIFDInto`/`parseSingleIFDBigTIFF`
forward it (tail-call `return fillIFD(...)` still type-checks since both
signatures gained the same extra return in the same position — zero extra
plumbing there). `traverse`/`traverseWithArena`/`traverseBigTIFF` now do
`budget.spend(parsedCount)` instead of `budget.spend(len(ifd.Entries))`.
`traverseWithArena`'s 3-branch dispatch (arena-hit / entries-exhausted /
arena-nil) needed a single shared `var parsedCount int` set in all three
branches before the unified `budget.spend(parsedCount)` call after linking.
No ABI concern: these are per-IFD functions (chain-loop granularity), not
the per-entry `parseIFDEntry` hot path — adding one plain `int` return is
free. Reused this pattern for the BigTIFF equivalents exactly (uint64
narrowed to int at the `budget.spend` call site, safe since
`parsedCount <= bigTIFFMaxEntries` = 65535).

**Secondary hardening (same fix commit):** the in-place dedup compaction
(`out := ifd.Entries[:1]; ...; ifd.Entries = out`) shrinks LOGICAL length but
not backing-array CAPACITY — a large-cap-but-len-1 array stays fully
reachable (and thus GC-retained) via the slice header. Added: when
`len(out) < preDedupLen` (a duplicate was actually found — cold path,
already paying for a parseWarn append), copy to a freshly `make`'d
right-sized slice. Applied to both `fillIFD` and `fillIFDBigTIFF`. Cheap
because it's gated strictly behind "a duplicate was found", never touching
the common no-duplicate hot path.

**Regression test added:** `EXIF-IFDCHAIN-01_duplicate_tag_dedup_undercount_bounded`
subtest + `buildIdenticalTagIFDChain` helper (same file). Every entry in
every hop is written explicitly (TypeUndefined, count=1, inline, always
safe) with the SAME tag, deterministically forcing near-total dedup
collapse — the exact residual shape. Two-pass construction (all count-fields
+ entries first, ALL next-pointers last) is required: unlike the
zero-filled first subtest, explicit per-hop entries content is large enough
(perHop*12 bytes) to alias a NEIGHBOUR's next-pointer if written in a single
pass — same `3*perHop > hops` margin rule applies, generalized from
count-field-vs-next-pointer to entries-region-vs-next-pointer collisions.
Asserts chain length `< hops` (not just `<= maxTraverseChainIFDs`, since
with perHop large the chain plateaus at exactly `hops` pre-fix — matches the
audit's own observation) AND a `runtime.MemStats.HeapAlloc` delta bound
(32 MiB ceiling; measured ~5 MiB post-fix vs ~800MB-4GB pre-fix at
hops=512, perHop=20000-65535 — reproduced the coordinator's 258KB/~910MB
and 770KB/~4.1GB numbers almost exactly via a throwaway measurement harness
before finalizing thresholds). **Gotcha found empirically:** with
perHop=2000 the identical-tag construction did NOT collapse dedup as
tightly as expected (~27 retained/hop, not ~1) because the count-field
write lattice — spaced `stride` apart across the WHOLE buffer — corrupts a
FIXED-size prefix of every hop's entries region regardless of perHop; that
fixed corruption zone is a much smaller *fraction* of perHop when perHop is
large (20000+), which is why the test uses a large perHop rather than a
small one. Always verify a new adversarial-construction test actually FAILS
when the fix is stubbed out before trusting its thresholds — a near-miss
pass (26.65 MiB against a 32 MiB threshold, found via 5x repeat runs) is a
sign the margin is too tight, not that the test is correct.
