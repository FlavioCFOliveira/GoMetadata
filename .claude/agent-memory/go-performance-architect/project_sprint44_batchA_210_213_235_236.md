---
name: project_sprint44_batchA_210_213_235_236
description: Sprint 44 Batch A (2026-09-25) — xmp RDF-parse compact value-arena (#210, superseding an initial whole-body-clone design after security findings XMPBUF-RETAIN-01, XMPARENA-NS-REINTERN-01, and a REVERTED struct-in-list truncation fix tracked as backlog BUG #277), deferred attr []byte conversion (#211), single-item collection fast path (#212), map pre-sizing (#213), benchmark reader hoisting convention (#236), cr3/orf/rw2 benchmarks (#235)
type: project
---

**SUPERSEDED DESIGN NOTICE**: item 1 below originally described #210 as a
single whole-body-clone anchor (`XMP.buf`). A same-day security audit found
that design pins the ENTIRE parsed document in memory regardless of how
little content it stores (finding XMPBUF-RETAIN-01, MEDIUM) — see item 10,
which is the ACTUAL shipped design (a compact, append-only value arena,
`XMP.arena`, interned only at the point of storage). Item 1 is kept for
historical/comparative context (it explains the aliasing-safety reasoning,
which item 10's design still relies on) but do NOT reuse the "single
whole-buffer clone/alias anchor" pattern for a future parser without also
applying item 10's "intern at point of storage, not at value-computation
time" refinement. A SECOND, immediate re-audit of item 10's own remediation
found a further, MORE severe bug (XMPARENA-NS-REINTERN-01, CRITICAL) in the
same diff — see item 11, the single most important lesson of this whole
batch: "intern at point of storage" is necessary but NOT sufficient; every
such storage decision point must ALSO be individually guarded against
re-interning a value/key that was already interned for the SAME distinct map
key on a PRIOR call — a lesson item 10 itself did not yet capture.

Continuation of [[project_sprint44_perf_204_209_241]] on the same branch
(feature/44-performance-and-efficiency-laboratory-260925). Six tasks
implemented in one merged pass per the Work Synergy policy: #236 and #235
first (test/benchmark-only, establish accurate baselines), then #210-#213
together (all four touch the same handful of functions in xmp/rdf.go and
xmp/xmp.go).

**Why:** #210-#213 all target `unescapeXML`/`storeProperty`/`classifyAndStoreAttr`/
`closeProp` — the top four `alloc_objects` pprof hotspots in `BenchmarkRDFParse`
(45.85%, 24.8%, 15.83%, 8.11% respectively, in that order, before this batch).
#236 was sequenced first because several benchmarks used as this batch's own
before/after measurement (root's `Read_JPEG_WithXMP`, `ReadCombinedMetadataJPEG`)
had the same reader-in-loop artifact as the cited HEIF/PNG/WebP examples.

**How to apply / key technical findings for future similar tasks:**

1. **The "safe rollout" pattern for buffer-ownership + `unsafe.String` aliasing**
   (task #210): a public `Parse([]byte) (*Result, error)`-shaped API that must
   defend against the caller mutating/pooling its input after return (the
   general #72-class hazard) does NOT have to pay N small `string(sub)` copies
   — one for every extracted value/key. Instead: copy the relevant region ONCE
   into a fresh, non-pooled, **result-owned** buffer anchored on the returned
   struct (here `XMP.buf`), then let every internal helper return
   `unsafe.String(unsafe.SliceData(sub), len(sub))` views into THAT buffer.
   Safety rests on 4 invariants (documented on the anchor field): (a) never
   pooled, (b) never mutated after construction, (c) GC-kept-alive for exactly
   as long as any derived string is reachable (interior-pointer keep-alive),
   (d) the parse entry point is the ONLY caller of the internal parse function,
   always with the anchor buffer as input — this last point means an
   `unsafe.String`-returning helper's precondition ("input must be a sub-slice
   of the anchor buffer") is enforceable BY CONSTRUCTION (single call path),
   not by a runtime check. **Split the copy cost further**: if the input
   passes through an encoding-normalisation step that sometimes ALREADY
   allocates a fresh buffer (here: BOM transcoding, UTF-16/32→UTF-8), thread a
   `transcoded bool` out of that step so the anchor-buffer construction can
   skip the extra clone when the transcode step already produced an
   independent buffer — no wasted double-copy on that (rare) path.
2. **`unsafe.String`/`unsafe.SliceData` in a hot-path helper needs an explicit
   `//nolint:gosec // G103: <justification>` on the exact return line**,
   matching this repo's G115/G602 nolint convention (`nolint:gosec //
   G<code>: <one-line reasoning>`). This was the FIRST production use of
   `unsafe.String` in this codebase (the only prior occurrence was a
   since-reverted one, task #72) — cite the precondition doc + the buffer's
   field doc by name in the justification, not just "safe, trust me".
3. **Deferred `[]byte`→`string` conversion for "attribute"-shaped structs**
   (task #211): when a struct field is read by MANY call sites but only a
   FEW of them ever actually need it as an independent `string` (most just
   compare it against literals and discard it), store it as `[]byte` (a
   zero-copy sub-slice) and convert with a plain `string(b)` ONLY at the few
   storage call sites. Comparison call sites use `string(b) == "literal"`,
   which the Go compiler recognises as a zero-allocation special case when
   the comparison is syntactically direct (not through an intermediate
   variable or method call — see finding #6 in the parent memory file for the
   syntactic requirement). This is a much smaller, more conservative win than
   full aliasing (#1 above) and does not need `unsafe` — appropriate when the
   AC only asks for a modest allocation reduction (~3-5/op) rather than
   eliminating the copy entirely.
4. **Single-item collection fast path** (task #212): `strings.Join(vals, sep)`
   still allocates for a 1-element slice even though there is nothing to
   join. `len(vals) == 1` → store `vals[0]` directly (it is already an
   independently-owned string) instead of calling `Join`. Applies to ANY
   accumulate-then-join pattern where the common case is a single element —
   check real-world corpus stats first (here: 3 of 4 collections in the
   representative packet were single-item).
5. **Map pre-sizing heuristic**: `make(map[K]V, 8)` for small, per-parent
   collections (here: one inner `map[string]string` per XMP namespace,
   typically 1-4 keys in real documents) is a safe, "cheap to over-hint"
   default. Do NOT try to derive an exact size analytically if doing so
   would require an extra pre-scan pass — the cost of a wasted few bytes of
   over-allocated bucket array is far cheaper than a second scan of the
   input. Leave genuinely rare/lazy maps (here: `containerTypes`, task #273)
   unsized — pre-sizing a map that's usually never even created defeats its
   own lazy-allocation design.
6. **Benchmark reader-hoisting is now the canonical convention repo-wide**
   (task #236): `r := bytes.NewReader(data)` ONCE outside `b.N`, then
   `r.Seek(0, io.SeekStart)` as the first line inside the loop, for every
   benchmark that calls an `Extract`/`Inject`/`Read`/`Write` taking an
   `io.ReadSeeker`. Applied across root, xmp-adjacent root benchmarks,
   format/heif, format/png, format/webp, format/tiff, format/raw/{cr2,nef,
   dng,arw,cr3,orf,rw2}. Saves exactly 1 alloc/op (the `*bytes.Reader` heap
   escape) and ~40-50 B/op every time — apply this to any NEW benchmark
   added in this codebase going forward; a benchmark that constructs
   `bytes.NewReader` inside the loop is now considered a bug in the
   benchmark itself, not the library.
7. **B/op can legitimately INCREASE while allocs/op drops and ns/op still
   improves** — do not mistake this for a regression. The "#210 pattern"
   (one big copy replacing many small copies) retains bytes that the small-
   copy approach never would have (e.g. XML markup/tag-name bytes around
   the actual property values) — B/op went from 2164→3079 (RDFParse,
   +42.28%) and 20.78Ki→25.81Ki (root `ReadCombinedMetadataJPEG` on a REAL
   corpus file, +24.19%) while allocs/op fell 60% and 50.6% respectively and
   `sec/op` STILL improved in every row (fewer `mallocgc` calls costs more in
   wall-clock than the extra `memmove` bytes). Always report B/op honestly
   even when it moves the "wrong" way — the AC in this batch targeted
   allocs/op specifically, not bytes, and said so explicitly.
8. **Fully empirically verify EVERY number in a benchmark table before
   writing it down** — do not infer a "before" number from an FR/TR prose
   description (e.g. "HEIF Extract reports 15 vs true 14") even when it
   sounds authoritative; that prose was itself an approximation. For task
   #236's own before/after table, wrote temporary `zz_scratch_before_test.go`
   twin benchmarks (exact pre-edit function bodies, copied from the `Read`
   tool output captured before editing) into each package, ran them
   side-by-side with the real (post-hoist) benchmark, recorded the ACTUAL
   numbers, then deleted the scratch files (`git status --short` confirms
   clean). This caught a real discrepancy: the guessed B/op deltas (596→580,
   200→184, 48→32) were wrong in the "before" column; the measured deltas
   were 629→580, 232→184, 80→32 (bigger `bytes.Reader` escape footprint than
   assumed) — same Δallocs (-1) but different Δbytes. **Reusable protocol**:
   scratch-twin-benchmark-then-delete is the general technique for isolating
   ANY single mechanical change's own effect without a git stash/checkout
   round-trip through a working tree that has many other uncommitted changes.
9. **New regression-test writing beyond what already exists**: even when an
   existing regression test (`TestParsedPropertyIndependentOfInputBuffer`,
   task #72) already covers the exact invariant a new task's AC restates,
   write an ADDITIONAL, stricter test when the task instructions explicitly
   say "write the regression test" — here, `xmp/task210_test.go` adds (a) a
   REAL `sync.Pool`-Get/Put/reused-by-unrelated-Get scenario (not just
   zeroing a plain slice) and (b) a distinct-byte-pattern overwrite (not
   all-zero, which could coincidentally mask a subtler aliasing bug). Both
   run clean under `-race`.

10. **XMPBUF-RETAIN-01 (security finding, MEDIUM) and its fix — the critical
    refinement to item 1's pattern**: a "single result-owned anchor buffer
    that every kept value aliases into" is UNSAFE for retention (not memory
    safety — retention) whenever the parser scans MORE than it stores, which
    is true of essentially every real-world parser (XML markup, comments,
    whitespace, dropped/unused attributes are all "scanned but not kept").
    Go's GC keeps a WHOLE backing array alive if even ONE reachable string
    has an interior pointer into it — so cloning/aliasing the WHOLE input
    once, upfront, means the *entire scanned input* (not just the stored
    subset) is retained for the parsed object's whole lifetime. PoC: a ~15
    MiB document (one trivial property + a huge XML comment the parser
    already skips without storing) retained ~14.6 MiB per parsed object —
    amplification bounded only by the format's own size cap, unrelated to
    actual content. **The fix, reusable for any future "own the returned
    strings" parser design**: do not alias/clone the WHOLE input upfront.
    Instead (a) read the input TRANSIENTLY and read-only during the
    synchronous parse call (a plain alias is fine here — `unsafe.String`
    directly into the caller's own buffer — because nothing survives past
    the call unless explicitly promoted); (b) maintain a separate, small,
    per-document, APPEND-ONLY arena (`XMP.arena`) that starts small
    (`min(len(input), aSmallConstantCap)` — bounded regardless of adversarial
    input size, NOT a fraction of input size with no absolute cap, or the
    padding attack just moves to inflating the initial guess) and grows via
    plain `append`; (c) at the FEW call sites that actually decide "this
    value/key is worth keeping forever" (here: `storeProperty`,
    `recordContainerType` — NOT `unescapeXML` itself, which cannot know a
    value's eventual fate), copy the value into the arena via one `intern()`
    call and use THAT returned string from then on. Append-only growth is
    what makes this safe across arena regrowth: Go's `append`, when it
    reallocates, copies OLD content into a NEW array and never touches the
    OLD array again — so a string returned by an EARLIER `intern()` call
    keeps pointing at a backing array that is retained (by that string alone)
    but never mutated, remaining correct forever; worst-case total retention
    across all of an arena's historical backing arrays sums to ≈2x final
    content size (geometric growth series), not more. **Critical residual
    vector to check for**: even with per-storage-decision interning, a naive
    "eagerly copy INTO the arena the moment a value is computed" (rather
    than "only at the point it's decided to be KEPT") reintroduces a milder
    version of the same bug for any input that computes-then-discards large
    values (e.g. a huge value on an attribute that turns out to be dropped,
    like `rdf:about` here) — always trace the full computed-to-discarded
    lifecycle of every value type before deciding an eager-intern point is
    safe. **Test-writing lesson learned the hard way**: when writing a
    permanent heap-retention regression test (parse N documents, keep them
    reachable, assert bounded growth via `runtime.GC`+`ReadMemStats`),
    measure `before` BEFORE allocating ANY per-iteration input buffers, and
    allocate each iteration's input as a LOOP-LOCAL variable referenced
    nowhere else (so it naturally goes out of scope each iteration) — an
    initial version of this test measured `before` AFTER pre-building all N
    input buffers (so they were already counted in the baseline) and reused
    a SINGLE shared input across all N `Parse` calls (so even a genuinely
    buggy aliasing implementation showed near-zero incremental growth, since
    all N results aliased the SAME already-counted backing array) — the test
    passed 100% vacuously in both the fixed AND a deliberately-reintroduced-
    bug state. **Always validate a new regression test's discriminating
    power empirically**: temporarily patch the fix into a no-op / reverted
    state, confirm the test FAILS with the expected-magnitude signature, then
    restore (`diff` against a pre-experiment backup to confirm byte-identical
    restoration) — do not trust a retention/regression test that has never
    been observed to fail.

11. **XMPARENA-NS-REINTERN-01 (security re-audit, CRITICAL) — "intern at
    point of storage" is necessary but NOT sufficient; every storage
    decision point needs its OWN first-time-only guard.** A same-day
    re-audit of item 10's own shipped fix found that `recordContainerType`
    (a SECOND function that also calls `intern()`) copied the namespace URI
    into the arena UNCONDITIONALLY on every call — it had moved interning to
    the "point of storage" as item 10 prescribes, but forgot to ALSO check
    "have I already interned THIS distinct key before, on a prior call?"
    (the guard `storeProperty`, written first, already had:
    `if x.Properties[ns] == nil { ns = x.intern(ns); ... }`). Because XML
    namespace prefixes let a document declare a long URI ONCE and reference
    it via a cheap short prefix arbitrarily many times, and
    `recordContainerType` runs once per SIBLING collection-typed property
    sharing that prefix, this reproduced a WORSE version of the very bug
    item 10 had just fixed — 794x amplification (492 KiB doc → 381.7 MiB
    arena), and worse in kind: single-call (not retention-dependent), so it
    doesn't even need the caller to hold onto the parsed result. **Reusable
    checklist for ANY future "compact arena, intern at point of storage"
    parser design**: (a) enumerate EVERY function that calls `intern()` —
    not just the one the original bug report was about; (b) for each one,
    confirm it has an EXPLICIT "is this exact map key already present?"
    guard evaluated BEFORE interning, mirroring whichever sibling function in
    the same file already has it correct (copy the guard, don't reinvent
    it — the existing `storeProperty` pattern was RIGHT THERE in the same
    file the whole time); (c) do not stop at namespace URIs — ALSO audit any
    string built by concatenating a prefix/suffix that itself is bounded-once
    in the source document but gets embedded into N distinct, all-different
    map keys (found independently during this same audit: `buildStructInListKey`
    embedded an unbounded `propLocal`/`fieldLocal` as a repeated prefix/suffix
    across N struct-in-list-item keys — NOT a "re-intern the same key" bug
    like the namespace one, but the SAME underlying pathology of "a
    bounded-in-source token cheaply multiplied by an attacker-inflatable N");
    for THIS sub-class (distinct keys, not redundant re-interning of the SAME
    key), the fix is a length CAP on the repeated component
    (`maxStructInListKeyComponentLen`, 256 bytes — real identifiers are never
    remotely this long), not a "seen before" guard, since every generated key
    genuinely is distinct and must be stored. (d) Write a PERMANENT test for
    EVERY storage path found in (a) that shares a namespace/prefix across
    many siblings (not just the one the bug report demonstrated) — this
    audit added 4 near-identical namespace-reuse tests (container-typed
    properties, simple properties, shorthand attributes, struct fields)
    specifically because each exercises a DIFFERENT call site into
    `storeProperty`/`recordContainerType`, and a fix that accidentally missed
    guarding one of them would only be caught by testing that SPECIFIC path.
    (e) Add a standing FUZZ invariant (`len(arena) ≤ k·len(input)`) in
    addition to hand-crafted worst-case tests — the auditor's own fuzzing run
    did NOT independently find either bug in 60s (both need a fairly large,
    structured, adversarially-specific document that undirected random
    mutation is unlikely to stumble onto quickly), so hand-crafted tests
    remain the primary defence for this class of bug, but a fuzz invariant
    still adds a second, more diffuse layer of protection against variants
    no one has thought of yet, at negligible cost.

12. **BUG #277 — a length CAP is not a safe default fix for anything that
    becomes part of a value the caller can observe or that gets re-serialised
    — "truncate silently" and "collide silently" are both correctness bugs,
    not acceptable degradation, the moment the truncated thing is (a) an
    EXPORTED field's map key (directly inspectable, no `Get`-style
    indirection to hide behind) and (b) the SOLE source of truth a writer
    uses to reconstruct output (no untruncated copy kept anywhere else).**
    Item 11's `maxStructInListKeyComponentLen` fix (256-byte truncation of
    `propLocal`/`fieldLocal` in `buildStructInListKey`) closed
    XMPARENA-NS-REINTERN-01's struct-in-list variant correctly from a
    DoS-severity standpoint, but a FOURTH same-day audit pass found it
    silently corrupts round-tripped output for any legal XML Name longer than
    256 bytes (ISO 16684-1 / XML 1.0 §2.3 places NO length limit on Name) and
    silently DROPS one of two values when two distinct long names share a
    truncated prefix (via the pre-existing first-wins guard, with the `xmp`
    package having no warnings mechanism to surface either failure). **User
    decision: REVERT completely** rather than attempt a smaller patch —
    remove the cap, remove its test, restore the exact pre-existing key
    construction, and move the DoS-severity concern to a tracked backlog item
    (BUG #277) with candidate designs recorded in the reverted function's own
    doc comment for whoever picks it up later (budget-based admission control
    that drops whole items rather than corrupting names; item-count capping
    with untruncated names; or a data-model change so the prefix is not
    re-embedded per item — the last one changes the public `Properties` map's
    key convention and needs explicit sign-off first). **Reusable checklist
    before capping/truncating ANYTHING derived from parsed input**: (a) is
    the truncated value ever exposed via an exported field, or fed back into
    a serialiser/writer with no independent full-length copy retained
    elsewhere? If yes, truncation is very likely a correctness bug in
    disguise, not a DoS mitigation — prefer a REJECTING/DROPPING strategy
    (discard the whole item, entry, or document past a budget) over a
    SILENTLY-MODIFYING one (truncate/collide) whenever the two are options
    for the same vulnerability, because "this item is now absent" is a
    detectable, if unfortunate, outcome, while "this item is now WRONG" is
    not. (b) When adjusting a test invariant (here: the `FuzzParseXMP` arena
    ratio) to tolerate a KNOWN, deliberately-deferred backlog item rather than
    hide it, prefer EXCLUDING that item's specific, measured contribution
    (here: a small counter field, `structInListKeyBytes`, incremented at the
    exact 2 call sites that produce the backlogged pattern) over just loosening
    the overall bound — a loosened bound would also mask a regression in
    every OTHER, already-fixed vector the invariant is supposed to protect;
    an excluded, measured contribution keeps full sensitivity everywhere else.
    Verify the exclusion actually works BEFORE trusting it (ran the exact
    known-bad-case document through the adjusted invariant by hand: 1.2 GB
    raw arena, 1.2 GB counted as struct-in-list, ~80 KB residual — confirmed
    the invariant now passes for the deliberately-tolerated case while still
    failing by 1-2 orders of magnitude for the OTHER, still-guarded vector
    when experimentally re-broken).

See [[project_task198_arena]] for the general "pay once inside an existing
allocation to avoid paying N times elsewhere" trade-off shape this batch
reuses twice (#210's arena-based storage, #213's pre-sized maps) — and see
item 10 above for why "pay once, upfront, for EVERYTHING scanned" is the
wrong generalisation of that shape for a parser that discards most of what
it scans.
