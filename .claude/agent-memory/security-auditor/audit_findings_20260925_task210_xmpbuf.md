---
name: audit-findings-20260925-task210-xmpbuf
description: Focused adversarial audit 2026-09-25 of uncommitted task #210/#211-#213 (perf-lab Batch A) xmp/ changes — XMP.buf single-copy anchor + unsafe.String aliasing in unescapeXML/onStartProperty/xmpAttr.loc. Aliasing model CLEARED (all invariants hold); 1 NEW MEDIUM finding: XMPBUF-RETAIN-01 (whole-document memory pinning, up to ~4000x amplification vs prior per-property-copy model).
metadata:
  type: project
---

## Scope

Uncommitted working-tree diff (base HEAD dcfa1b2, not yet committed) in
`xmp/encoding.go`, `xmp/packet.go`, `xmp/rdf.go`, `xmp/xmp.go`, plus new
`xmp/task210_test.go`. Task #210 (+#211-#213): `Parse` now copies the RDF body
ONCE into a fresh, non-pooled, Parse-owned `XMP.buf`; `unescapeXML`'s
entity-free path and the tagLocal→propLocal / `xmpAttr.loc` local-name
conversions return `unsafe.String` views into that buffer via a new
`bufAliasString` helper (xmp/rdf.go), instead of each paying an independent
`string(b)` heap copy (which is how bug #72 — see
[[audit_findings_20260604]] XMP-001 — was originally fixed).

## Aliasing-safety verdict: SOUND, all 5 adversarial questions answered

1. **Can an aliased string ever point into caller-owned/pooled memory?** No.
   Verified structurally + empirically for every branch:
   - `!transcoded` (common UTF-8 case, `normaliseToUTF8` returns `b`
     unchanged): `Parse` unconditionally does `x.buf = bytes.Clone(body)`
     — the ONLY memory `parseRDF`/`unescapeXML`/`bufAliasString` ever see is
     this fresh clone, never the caller's own slice, regardless of where that
     slice came from (caller stack var, sync.Pool, mmap, streaming read
     buffer). `xmp.Scan` is a pure locate-and-slice (packet.go:25-67, no
     copy) — this is fine specifically because Parse's clone-vs-alias branch
     doesn't care whether `body` is a sub-slice of the caller's buffer or of
     an intermediate transcode buffer; it re-derives that from `transcoded`,
     not from inspecting `body`'s provenance.
   - `transcoded` (UTF-16/32 BOM detected): confirmed by reading
     `golang.org/x/text@v0.35.0` (matches go.mod) `transform/transform.go:678`
     — `Bytes()` = `doAppend(t, 0, make([]byte, len(b)), b)` — ALWAYS
     allocates a fresh `make([]byte, ...)` destination independent of `b`'s
     backing array, even for 0-length input. `decodeUTF32` (encoding.go)
     likewise always `make([]byte, 0, maxOut)` + append, never returns a
     sub-slice of its input. Empirically confirmed with a real UTF-16LE and
     a real UTF-32BE PoC (scratch-only, not committed): Parse, then
     overwrite the ORIGINAL caller buffer with `0xFF`/`0xAA` — properties
     unaffected in both cases (PASS).
   - Only 3 real call sites to `xmp.Parse` in the whole module: `read.go:294`
     (`parseXMP`), `format/jpeg/jpeg.go:1677` and `:1681`
     (`reassembleExtendedXMPByParse`, extended-XMP GUID reassembly). The
     extended-XMP path re-serialises the merged result via `xmppkg.Encode`
     (which itself does `bytes.Clone(buf.Bytes())` before returning its
     pooled `bytes.Buffer` — pre-existing, confirmed still correct) into a
     brand-new `[]byte`, which is THEN re-parsed by `parseXMP` in read.go —
     so the transient `mainXMP`/`extXMP` structs' `.buf`s never leak into the
     final `m.XMP`. `parseRDF` itself has exactly one call site
     (`xmp.go:201`, `parseRDF(x.buf, x)`) — no other internal function can
     reach it with a non-`x.buf` slice.
   - PNG/TIFF/WebP/HEIF/RAW containers do not call `xmp.Parse`/`xmp.Scan`
     directly; they all funnel through `read.go`'s single `parseXMP` call.

2. **Is XMP.buf ever mutated after Parse?** No. Assigned exactly once
   (`xmp.go:196`/`198`), never written again anywhere in the package; no
   `Clone`/reset/reuse method exists on `*XMP` (grepped `func.*Clone` — only
   `RawEXIF`/`RawIPTC`/`RawXMP` on the top-level `Metadata`, all of which
   `bytes.Clone` their own separately-stored raw segment bytes, not
   `XMP.buf`). No re-parse-into-same-struct pattern exists. Setters
   (`Set*`/`putProp`) only write into the `Properties` map, never touch
   `.buf`.

3. **Is the unsafe.String derivation correct at boundaries?** Yes.
   `bufAliasString` explicitly special-cases `len(b)==0 → return ""` before
   touching `unsafe.SliceData`, avoiding the zero-length/zerobase-pointer
   edge case entirely. For `len(b)>0`, `b` is always an ordinary Go sub-slice
   produced by the tokenizer's own slicing (`buf[i:j]`) — `unsafe.SliceData`
   + `unsafe.String` on an already-valid, already-in-bounds slice is exactly
   the documented-safe use of that API (no manual pointer arithmetic
   anywhere in the diff).

4. **Race: concurrent reads after Parse?** Clean.
   `go test -race` PASS on `./xmp/...` and `.` (root); scratch stress test
   (32 goroutines × 2000 iterations, `CameraModel`/`Caption`/`Keywords`/`Get`/
   map iteration concurrently) — 0 races. `attrBuf`/`nsTable` live in the
   per-call, stack-scoped `rdfParser` value (`p := rdfParser{x: x}` inside
   `parseRDF`), not a package-level or pooled resource — no cross-goroutine
   sharing between concurrent `Parse` calls on different documents either.

## NEW FINDING — XMPBUF-RETAIN-01 — Whole-document memory retention amplification — MEDIUM

- **Location**: xmp/xmp.go:196/198 (`x.buf` assignment), interacting with
  xmp/rdf.go `bufAliasString`/`unescapeXML`/`onStartProperty`.
- **Vulnerability class**: Resource Exhaustion — memory-retention
  amplification (not a crash, not attacker-triggered unbounded growth per se
  — bounded by the pre-existing `maxXMPDocumentBytes` = 16 MiB cap — but a
  structural regression in the "safe" dimension traded for the "fast"
  dimension, per CLAUDE.md's correct→safe→fast priority order, and not
  disclosed as a tradeoff in the task's own doc comments).
- **Description**: Every string returned via `bufAliasString` (all
  entity-free property values, all property/struct/attribute keys) is a
  Go interior pointer into `x.buf`. Go's GC keeps the ENTIRE backing array of
  `x.buf` alive for as long as ANY single derived string is reachable — which
  is always true in normal usage (that string IS the property value the
  caller asked for). Before task #210, each such string was an INDEPENDENT
  `string(b)` heap copy sized to just that substring; the original raw
  document bytes (Scan's sub-slice of the caller's or transcode buffer) had
  no other referents and were reclaimed by the GC shortly after `Parse`
  returned. After #210, a `*XMP` (and therefore any `Metadata` holding it)
  now pins the FULL RDF body — up to the 16 MiB `maxXMPDocumentBytes` cap —
  in memory for its entire lifetime, regardless of how few properties it
  actually contains.
- **Trigger condition**: A caller that retains parsed `*xmp.XMP` /
  `*gometadata.Metadata` objects beyond a single transient use — e.g. an
  in-memory cache, an indexing service, a gallery backend holding metadata
  for many images — combined with attacker-supplied images whose XMP packet
  is padded with filler (XML comments, which the parser skips without
  storing) up to near the 16 MiB document cap while carrying only a handful
  of trivial real properties.
- **PoC**: scratch-only Go test (not committed): built a synthetic ~15 MiB
  XMP document (one `tiff:Model="X"` property + a giant `<!-- AAAA... -->`
  comment filler) and parsed 40 copies, keeping the resulting `*XMP` slice
  alive (Scenario A) vs. dropping all `*XMP` refs and keeping only
  `strings.Clone`d extracted values (Scenario B), with `runtime.GC()` +
  `runtime.ReadMemStats` bracketing each scenario.
  - Scenario A (current behaviour): heap grew **611,738,872 bytes for 40
    docs ≈ 14.58 MiB/doc** — matches the ~15 MiB synthetic document size
    almost exactly, confirming the WHOLE body is retained per parsed object.
  - Scenario B (baseline / pre-#210-equivalent — only extracted strings
    kept): heap did not grow at all (net negative after GC reclaimed the
    40 × 15 MiB buffers), i.e. effectively **KiB**, not MiB, per doc.
  - **Amplification factor: ~6×10^8 in absolute bytes for this synthetic
    case; conceptually ~4000× versus the documented real-world XMP size
    range** (maxXMPDocumentBytes's own design-rationale comment cites
    "a few hundred bytes" to "roughly 4 MiB" for legitimate corpus files,
    with a hard 16 MiB cap for the pathological case).
- **Impact**: For N cached/long-lived parsed images with maximally-padded
  malicious XMP, resident memory is now ~N × 16 MiB instead of ~N × (sum of
  real property byte sizes, typically sub-KiB to a few KiB) — a
  memory-exhaustion DoS amplifier bounded per-file at 16 MiB but with no
  cross-file cap (any code that iterates over a directory/stream of
  uploaded images and keeps `Metadata`/`XMP` objects around, e.g. for
  batch indexing or a gallery cache, inherits this amplification per file
  with no additional attacker effort beyond padding each file to the
  existing size cap).
- **Exploitability**: Confirmed (retention mechanism, measured empirically)
  / Probable (real-world DoS impact — depends on the calling application's
  caching/retention pattern, which is outside this package's control, but is
  a common and foreseeable usage pattern for a metadata-indexing library).
- **Remediation** (delegate implementation to `go-performance-architect`):
  the cheapest fix that preserves #210's zero-extra-copy win for the common
  case is to trim `x.buf` down to just the bytes actually referenced after
  parsing completes — e.g. re-`bytes.Clone` only the still-live substrings
  once parsing is done and reassign the property-map strings to fresh,
  independently-sized copies, OR (simpler, and consistent with the original
  #72 fix's spirit) accept the single extra pass-through copy only for
  documents whose `len(x.buf)` is large relative to the amount of content
  actually extracted (e.g. a threshold check: if `len(x.buf) > sum of stored
  property/key byte lengths (or just some fixed absolute threshold, e.g. 64
  KiB) after parseRDF returns, `string()`-copy every stored value/key at
  that point and drop the `x.buf` reference, so the GC can reclaim the
  padding immediately`). Either approach keeps the hot/common-case zero-copy
  win (small real-world documents, where `x.buf` and the extracted content
  are close in size, so trimming would not help anyway) while closing the
  amplification for the worst-case padded/near-cap-size document. This is a
  design decision with a real performance/memory tradeoff — present the
  options to the user before delegating a fix, per CLAUDE.md's Decision
  Policy (ambiguous tradeoff, not a clear-cut bug).
- **Suggested test**: a permanent (non-scratch) regression test in
  `xmp/task210_test.go` or a new file, asserting that after `Parse` returns,
  `unsafe.Sizeof`-adjacent runtime memory retained by a `*XMP` parsed from a
  large-filler/small-content document stays within some small multiple
  (e.g. 4×) of the total extracted property/key byte length, NOT the
  original document size — this would have caught this finding before
  merge. A `go test -run ... -memprofile` based benchmark comparing
  retained heap for a "real-world small doc" vs "padded near-cap doc" is
  also appropriate for `bench_test.go`.

## Tooling results

- `go build ./...`: clean.
- `go vet ./...`: clean.
- `golangci-lint run ./xmp/...`: 0 issues (verified AFTER removing all
  scratch-only test files from `xmp/`; the committed diff + `task210_test.go`
  alone are lint-clean).
- `go test -race -count=1 ./xmp/... .`: PASS, 0 races (both packages).
- `go test -fuzz=FuzzParseXMP -fuzztime=60s ./xmp/...`: PASS, ~19.5M execs,
  0 crashers, 0 new persistent failures.
- `govulncheck ./...`: could not run — environment toolchain mismatch
  (installed `govulncheck` built against an older Go source-processing
  version than the active `go1.27.1` on PATH vs. the module's `go 1.26`
  directive); this is a pre-existing local-environment issue, unrelated to
  this diff, not a finding against the code.

## Verdict

**NOT CLEARED** pending a decision from the user on XMPBUF-RETAIN-01 (a
real, empirically-confirmed memory-retention regression, MEDIUM severity,
bounded but substantial amplification). All 5 adversarial aliasing/mutation/
bounds/concurrency questions the task asked about are otherwise answered
SAFE — no memory-safety, use-after-free, data-race, or OOB finding in the
`unsafe.String` aliasing design itself; the design is sound and the fuzz/race
sweep found nothing. This is the first XMP-specific finding since
[[audit_findings_20260706_xmp_root_concurrency_pass2]] (XMPCONC-01, closed).
