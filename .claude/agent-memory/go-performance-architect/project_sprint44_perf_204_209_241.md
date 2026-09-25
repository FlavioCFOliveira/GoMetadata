---
name: project_sprint44_perf_204_209_241
description: Sprint 44 perf tasks #204-#209,#241 (2026-09-25) — escape-analysis config pattern, ISO-8859-1 hand decode, IPTC pre-count/sort-skip, MWG-02 digest cache, WebP/RIFF dispatch, #206 API-neutral follow-up
type: project
---

Sprint 44 "Performance and Efficiency Laboratory" merged 7 tasks in one pass on branch
feature/44-performance-and-efficiency-laboratory-260925. 6 implemented in the first pass;
#206 was initially stopped per its own API-safety gate (literal "unexported bool" wording
would have broken the observable `Records[0]` contract) and reported back. The coordinator
then specified an API-neutral revision (fixed-size arrays embedded in `*IPTC`, preserving
`Records[0]`'s exact shape) in a follow-up message, which WAS implemented — see item 11 below.

**Why:** CLAUDE.md mandates optimisation-only, zero-regression, evidence-based (benchstat)
changes; #206's literal wording would have broken the public `IPTC.Records[10][]Dataset`
field contract, but the underlying 2-allocation cost was still real and fixable without an
API change once the storage-embedding trick was applied.

**How to apply / key technical findings for future similar tasks:**

1. **Escape analysis is NOT runtime-branch-sensitive.** `if len(opts) > 0 { o(&cfg) }` still
   heap-allocates `cfg` even though the branch is never taken for zero-opt callers — the
   compiler only sees the static call graph. The ONLY fix: confine the address-taking +
   indirect-call to a separate, `//go:noinline` function (inlining would re-merge the escape
   into the caller). Verified empirically in a sandbox repro before touching prod code. This
   pattern (`applyReadOptions`/`applyWriteOptions` in read.go/write.go) is now the canonical
   idiom for functional-options structs in this codebase — reuse it verbatim for any future
   `Config` struct built from a `[]func(*Config)` slice.
2. **ISO-8859-1 == byte value == code point for the full 0x00-0xFF range** (unlike
   Windows-1252). Verified byte-for-byte against `golang.org/x/text/encoding/charmap.ISO8859_1`
   for all 256 single bytes + a combined 256-byte buffer, zero mismatches. Hand-rolled 2-byte
   UTF-8 emission (`0xC2`/`0xC3` lead byte rule) eliminates the x/text dependency from
   `iptc/encoding.go` entirely (x/text is still required — xmp/encoding.go uses it for UTF-16/32).
3. **`iptc.IPTC.Records` is an exported field** — `Records[0]` (the internal UTF-8-flag
   pseudo-record) is public-observable/mutable despite being described as "internal" in its
   doc comment, and existing in-package tests assert on `len(Records[0])` directly
   (FINDING-002 regression tests). Any future task proposing to change `Records[0]`'s
   representation must go through the same stop-and-report gate as #206.
4. **`slices.IsSortedFunc` treats equal-key runs as sorted** — this is exactly the same
   tie-behaviour as `slices.SortStableFunc`, so skipping clone+sort when already-sorted is
   byte-identical output, not just "usually the same". Used in `iptc.Encode` (#207).
5. **`io.ReadFull(r, buf[:])` through an `io.Reader`/`io.ReadSeeker` interface always
   heap-allocates `buf`**, regardless of whether `buf` is declared inside the callee or hoisted
   by the caller — the compiler can't prove an unknown concrete `Reader` doesn't retain the
   slice. The only real mitigation is amortisation: hoist ONE buffer above a multi-iteration
   loop (`riff.ReadChunkBuf(r, hdr *[8]byte)` pattern) so N chunk reads pay 1 allocation instead
   of N. `ReadChunk` (single-shot) is kept as a thin wrapper for API compatibility and is
   unavoidably unchanged (1 alloc every call, same as before).
6. **`switch chunk.FourCCString()` allocates even though `switch string(byteSlice)` has a
   known compiler zero-alloc special case** — the special case only fires when the
   `string(...)` conversion is syntactically direct in the switch expression, not when it's
   inside a called method (`(*Chunk).FourCCString`). Fix: switch on the `[4]byte` array value
   directly against package-level `[4]byte` constants (`fourCCEXIF`, `fourCCXMP` in
   format/webp/webp.go).
7. **MWG-02 IPTC-trust-elevation caching (#208)**: safe to cache eagerly at end of `Read`
   because `rawIPTC`/`rawIPTCDigest` are provably set exactly once (grep-verified across the
   whole repo) and never reassigned — Set* methods only ever mutate `m.IPTC` (parsed struct),
   never the raw bytes. Added a `digestMatchFn` test seam (`var digestMatchFn = iptc.DigestMatch`)
   specifically so a test can assert "exactly 1 MD5 call" — this is the pattern to reuse
   whenever an AC demands "prove call count N", not an ad hoc mock.
8. **Task #241 (`preCountDatasets`) required isolated net-win verification**: temporarily
   reverting *only* the pre-count line (keep #205 applied on both sides) and re-benchmarking
   showed a real but small regression (+5.21% ns/op) for a 5-dataset record (< the old fixed
   cap of 12, so no regrowth avoided either way) — but B/op dropped 43.86% in that same case
   (smaller `make` requests are cheaper for mallocgc even without changing alloc *count*).
   Aggregate geomean across 4 dataset-count buckets: −7.53% ns/op, −49.06% B/op. Verdict: net
   win, kept. **Precedent: when an optimisation trades scan-cost for allocation-size, always
   isolate it from any OTHER stacked optimisation in the same benchmark before judging
   "net win"** — the combined `BenchmarkIPTCParse` before/after (−57.85% ns/op) was almost
   entirely attributable to #205, not #241; without the isolation experiment #241's true,
   modest contribution would have been misattributed.
9. **Benchmark run hygiene**: running `golangci-lint`/`staticcheck` concurrently with a
   backgrounded `go test -bench` corrupts `ns/op` measurements (saw ±30-40% variance vs. ±1%
   in an isolated rerun) while leaving `B/op`/`allocs/op` unaffected (those are deterministic
   bookkeeping, not wall-clock). Always let a benchmark background job finish completely
   before running other CPU-bound tooling; do not multitask the validation gate against a
   live benchmark capture.
10. **Pre-existing findings surfaced, not fixed** (per Scope Discipline): `govulncheck` reports
    GO-2026-5970 in `golang.org/x/text@v0.35.0` (infinite loop on invalid input), not reachable
    from this module's own call paths, present regardless of this sprint's changes; 9
    pre-existing golangci-lint issues in `exif/`, `examples/`, `format/tiff/relocate_bigtiff.go`
    (unused nolint directives, weak-RNG in a benchmark, `modernize` suggestions) untouched by
    this sprint.
11. **#206 API-neutral follow-up (embedded fixed-size storage, not an unexported bool)**:
    when the literal task wording would break a public field's observable contract, the fix
    is not always "don't do it" — check whether the SAME allocation-elimination goal can be
    reached by piggy-backing on an allocation that already exists. Here: `IPTC.Records[0]`'s
    exact shape (`[]Dataset{{Record:0,DataSet:0,Value:[]byte{1}}}`) is preserved byte-for-byte
    by adding two unexported fields (`utf8Slot [1]Dataset`, `utf8Val [1]byte`) directly to the
    `IPTC` struct and re-pointing `Records[0]` at cap-clamped (`[:1:1]`) slices of them in a
    single `setUTF8Flag` method. Since these fields live inside `*IPTC` itself (already
    heap-allocated via `new(IPTC)` in `Parse`), there is no separate allocation site for them —
    `go build -gcflags="-m -m"` shows **zero** "moved to heap" lines in the entire `iptc`
    package after this change. The `[:1:1]` cap clamp is what makes external
    `append(i.Records[0], ...)` safe: cap==len forces `growslice` into a fresh array instead of
    writing into `IPTC`'s own struct memory, exactly reproducing the growth behaviour of the
    former `append`-produced 1-element slice (whose backing array was also already at
    `cap==len==1`). Net: 6→4 allocs/op on `BenchmarkIPTCParseUTF8Declared` (exactly −2, AC met),
    −5.58% ns/op, +16 B/op (expected: the struct itself is now permanently larger by the two
    embedded arrays' size — same trade-off shape as the exif sub-IFD arena, task #198).
    **Reusable pattern**: any small, fixed-cardinality "pseudo-record"/sentinel value that must
    live inside an already-exported container field can be backed this way instead of a fresh
    per-call allocation, as long as (a) the container is always accessed via pointer (so the
    embedded arrays are guaranteed to be part of a single existing allocation) and (b) any
    slice handed out of the embedded storage is cap-clamped so external mutation via append
    cannot corrupt it. Verify there is no `Clone`/value-copy path for the container type before
    applying this — a value copy would leave old slice headers aliasing the pre-copy instance's
    embedded arrays (documented as an invariant in `IPTC`'s doc comment for future maintainers;
    no such path exists in this module today, repo-wide grep-verified).

See [[project_task240_entry_pool]] and [[project_task203_magic_pool]] for related earlier
allocation-reduction precedents in this codebase.
