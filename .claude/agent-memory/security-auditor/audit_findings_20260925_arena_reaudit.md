---
name: audit-findings-20260925-arena-reaudit
description: Re-audit 2026-09-25 of the XMPBUF-RETAIN-01 remediation (compact value-arena design, XMP.arena + intern() + transientAliasString, replacing the whole-body-clone design). Retention-by-content goal ACHIEVED and aliasing model re-verified sound, BUT found a NEW CRITICAL single-request memory-exhaustion bug in recordContainerType's unguarded per-call re-intern of the namespace URI — up to ~17 GiB arena growth extrapolated from a single 16 MiB document, empirically confirmed at smaller scale (492 KB document -> 381.7 MiB arena / ~1.87 GiB heap churn, 794x-to-1526x amplification). BLOCKED — CRITICAL.
metadata:
  type: project
---

## Scope

Second re-audit pass, same day as [[audit_findings_20260925_task210_xmpbuf]], of the
uncommitted xmp/ diff after the user's chosen remediation for XMPBUF-RETAIN-01
("compact value arena" design) was implemented. Diff at time of this pass:
`xmp/encoding.go`, `xmp/rdf.go`, `xmp/xmp.go`, plus `xmp/task210_test.go` and
new `xmp/xmpbuf_retain01_test.go`.

New design: `XMP.buf` (single whole-body clone) replaced by `XMP.arena`
(compact, append-only, initial cap `min(len(body), 256)`). `bufAliasString`
renamed/generalised to `transientAliasString` — returns `unsafe.String` views
into the **transient, per-call input/normalised buffer** (no longer a
Parse-owned clone at all — `normaliseToUTF8` reverted to its original
single-return-value signature, since nothing needs to know whether it
transcoded any more). `(*XMP).intern(s string) string` is the ONLY function
that copies a value into the permanent, `*XMP`-owned `arena`; it is called
exactly from `storeProperty` (on `ns` — guarded, only the first time a
namespace's inner map is created — and unconditionally on `local`/`val`,
which is correct because those are decided-to-keep exactly once thanks to the
first-wins duplicate guard evaluated *before* interning) and from
`recordContainerType` (on `ns` — **unconditionally, every call, no guard** —
and on `local`).

## Verdict on the 4 questions the remediation review asked for

1. **No transient alias escapes Parse un-interned on any path.** Exhaustively
   traced every write site into `x.Properties`/`x.containerTypes` (grepped for
   all `.Properties[`/`.containerTypes[`/`.intern(` occurrences in rdf.go —
   only 2 functions ever write these maps: `storeProperty` and
   `recordContainerType`, both close over every value/key via `intern()`
   before persisting). Traced every producer of a transient value
   (`transientAliasString` — used only for `p.propLocal` in `onStartProperty`
   and `xmpAttr.loc` in `classifyAndStoreAttr`; `unescapeXML`'s entity-free
   fast path — used for character data and all attribute values via
   `readQuotedValue`) through every consumer: string concatenation
   (`propLocal+"."+string(a.loc)`, `buildStructInListKey`) always produces an
   independent copy by Go's `+`/`strings.Builder` semantics regardless of
   operand transience; `strings.Join`/`bld.String()` (multi-item collections,
   xml:lang-prefixed rdf:Alt items) likewise always copy; every terminal
   `storeProperty`/`recordContainerType` call interns its `ns`/`local`/`val`
   arguments. Text that reaches `onCharData` but matches no active property
   context (none of the 3 switch cases in `onCharData`) is silently dropped
   with no `default` case — becomes unreachable garbage, never touches
   `arena`, matching pre-#210 behaviour. Confirmed empirically: the
   maintainers' own `xmp/xmpbuf_retain01_test.go` (`TestXMPBufRetain01CommentFiller`
   / `TestXMPBufRetain01DroppedAttrFiller`, run PASS) and my own independent
   reproduction of the original XMPBUF-RETAIN-01 PoC against the new code
   (scratch-only) both show **0 bytes** heap growth for 10×4 MiB / 40×15 MiB
   padded documents that the pre-fix code would have retained in full.
2. **Arena append-only growth safety.** `intern()`'s only write is
   `x.arena = append(x.arena, s...)`; Go's `append` semantics guarantee any
   growth-triggered reallocation copies old content into a NEW backing array
   and never mutates the old one in place — a string returned by an earlier
   `intern()` call keeps pointing at valid, unchanged memory regardless of
   how many later calls grow `arena` past its capacity. No code anywhere
   truncates, reslices-down, or reuses `x.arena` (grepped — the only writer is
   `intern()`, and it never appears in a `sync.Pool`). Sound.
3. **Zero-length/boundary derivation** for both `transientAliasString` and
   `intern` special-case `len==0`/`s==""` before touching
   `unsafe.SliceData`; `intern`'s `unsafe.String(unsafe.SliceData(x.arena[start:]), len(s))`
   is always in-bounds because `start := len(x.arena)` is captured
   immediately before the `append` that grows `arena` by exactly `len(s)`
   bytes. Sound.
4. **Race + fuzz.** `go test -race -count=1 ./xmp/... .`: PASS, 0 races.
   `go test -fuzz=FuzzParseXMP -fuzztime=60s ./xmp/...`: PASS, ~19.8M execs,
   0 crashers. Bug-#72-style pool-reuse regression tests
   (`xmp/task210_test.go`, unmodified by this second remediation pass) still
   PASS against the new transient-alias design — confirms the "read
   transiently during the synchronous call, alias the caller's buffer only
   until intern() or discard" contract genuinely closes #72 the same way the
   whole-body-clone design did.

**All 4 of the aliasing/mutation/bounds/race questions are answered SOUND.**
The retention-by-content goal (the actual point of the XMPBUF-RETAIN-01 fix)
is achieved and empirically verified for the two vectors the maintainers
identified (comment filler, dropped-attribute-value filler).

## NEW FINDING — XMPARENA-NS-REINTERN-01 — Unbounded per-request memory amplification via unguarded namespace re-interning — **CRITICAL**

- **Location**: `xmp/rdf.go`, `recordContainerType` (around line 1571-1580,
  specifically the unconditional `ns = x.intern(ns)` — contrast with
  `storeProperty`'s correctly-guarded `if x.Properties[ns] == nil { ns =
  x.intern(ns); ... }` a few lines below it).
- **Vulnerability class**: Resource Exhaustion — algorithmic/memory
  amplification (decompression-bomb-equivalent: a short, cheap-to-repeat XML
  construct — a namespace-prefixed element using an already-declared prefix —
  causes an unbounded-relative-to-document-size copy of the FULL namespace
  URI string on every repetition).
- **Root cause**: XML namespace prefixes let a document declare a
  potentially-large URI **once** (`xmlns:t="<huge URI>"`) and then reference
  it arbitrarily many times via a cheap short prefix (`<t:p0/>`, `<t:p1/>`,
  ...). `storeProperty` correctly exploits this asymmetry safely by
  interning `ns` only the first time a given namespace's `Properties` inner
  map is created (subsequent calls skip the `intern` because the `if
  x.Properties[ns] == nil` guard is false). `recordContainerType` — called
  once per `rdf:Alt`/`Seq`/`Bag`-typed property, i.e. once per sibling
  element using that prefix — has **no equivalent guard**: it re-copies the
  full namespace URI into `arena` on every single call, regardless of
  whether that exact namespace was already interned by a prior call (its own
  or `storeProperty`'s).
- **Trigger condition**: any XMP document containing a moderately long
  namespace URI declared once and referenced by N sibling
  rdf:Alt/Seq/Bag-typed properties. No malformed XML, no entity tricks, no
  encoding tricks — pure, spec-legal(-ish) XML namespace reuse. Reachable
  through the public `xmp.Parse` API and therefore through
  `gometadata.Read`/`ReadFile` on any image whose XMP segment contains such a
  packet — no special preconditions, no need for the caller to retain the
  result (unlike XMPBUF-RETAIN-01, this blows up memory **during a single
  synchronous `Parse` call**, before it ever returns).
- **PoC** (scratch-only, not committed; white-box test inside package `xmp`
  reading `x.arena` directly plus `runtime.MemStats`):
  - `nsLen=1,000` bytes, `nProps=1,500` (all sharing one `xmlns:t="http://x/"+1000×'N'"`
    declaration): document size 84,560 bytes → `arena` len **1,528,789 bytes**
    (≈1,528,789 / 84,560 ≈ **18×** the document size).
  - `nsLen=10,000`, `nProps=1,500`: document size 93,560 bytes → `arena` len
    **15,037,789 bytes** (≈161× document size).
  - `nsLen=50,000`, `nProps=1,500`: document size 133,560 bytes → `arena` len
    **75,077,789 bytes** (≈562× document size).
  - Larger-scale confirmation: `nsLen=50,000`, `nProps=8,000`: document size
    **504,060 bytes (492 KiB)** → `arena` len **400,207,789 bytes (381.7 MiB)**,
    measured heap delta **2,002,994,584 bytes (≈1.87 GiB)**, parse time
    168 ms — a **794×** amplification of `arena` size alone versus the input
    document size, from a single, unremarkable-looking ~500 KB file.
  - All three small-scale data points match the predicted law
    `arena_waste ≈ nProps × nsLen` almost exactly (predicted vs. measured
    within noise), confirming the mechanism is linear and fully
    attacker-controlled on both axes, constrained only by the existing
    `maxXMPDocumentBytes` = 16 MiB document-size cap. Solving for the
    document-size-constrained worst case (`nsLen + nProps × ~45 bytes/element
    ≈ 16 MiB`, maximised at `nsLen ≈ 8 MiB`) extrapolates to **on the order of
    tens of GiB of arena allocation from a single ~16 MiB XMP packet** — this
    extrapolation was NOT executed in the sandbox (to avoid an actual OOM),
    but is a direct, confirmed-linear extrapolation from three consistent,
    independently-verified smaller-scale measurements.
- **Impact**: A single attacker-supplied image file, well within any
  reasonable per-file size limit an application might impose (the 492 KiB
  PoC alone forces ~1.9 GiB of heap churn in under 200 ms), causes
  `gometadata.Read`/`xmp.Parse` to attempt gigabytes-to-tens-of-gigabytes of
  allocation synchronously, inside a single request/call — a direct
  process-crashing (OOM-kill) or severe-latency DoS vector reachable by any
  untrusted caller of the public API, with no retention/caching precondition
  required (unlike XMPBUF-RETAIN-01, which needed the caller to hold parsed
  objects over time). This is more severe than the finding it was meant to
  fix: XMPBUF-RETAIN-01 was a *linear*, *retention-dependent* amplification
  bounded by `maxXMPDocumentBytes` per retained object; this is a *single-call*
  amplification that can exceed the document-size cap by multiple orders of
  magnitude.
- **Exploitability**: Confirmed (reproduced at 3 scales with exact match to
  the predicted linear law; extrapolation to the worst case is arithmetic,
  not speculative).
- **Remediation**: mirror `storeProperty`'s existing, already-correct
  pattern — only intern `ns` in `recordContainerType` the first time its
  outer map entry is created:
  ```go
  func recordContainerType(x *XMP, ns, local, ctype string) {
  	if x.containerTypes == nil {
  		x.containerTypes = make(map[string]map[string]string)
  	}
  	if x.containerTypes[ns] == nil {
  		ns = x.intern(ns)
  		x.containerTypes[ns] = make(map[string]string)
  	}
  	x.containerTypes[ns][x.intern(local)] = ctype
  }
  ```
  This is a single-line reordering (move `ns = x.intern(ns)` inside the `if
  x.containerTypes[ns] == nil` block, matching `storeProperty` exactly) and
  fully closes the amplification: total `ns`-interning cost across
  `recordContainerType` becomes bounded by the number of *distinct*
  namespaces ever given a container type, which is itself bounded by the sum
  of their declared-URI lengths in the document (≤ document size), restoring
  the intended "retention proportional to content" invariant. No test in the
  existing `xmp/xmpbuf_retain01_test.go` battery exercises this
  many-siblings-same-namespace-with-collections vector — a new permanent
  regression test covering it should be added alongside the fix.
- **Suggested test**: a permanent regression test parsing a document with
  many (e.g. 1,000+) sibling rdf:Alt/Seq/Bag-typed properties sharing one
  namespace prefix bound to a moderately long (e.g. 10 KiB) URI, asserting
  `len(x.arena)` (white-box, same-package test) stays within a small
  constant multiple of the total *distinct* stored content
  (namespace-URI-once + all local/val bytes), not `nProps × len(nsURI)`.

## Tooling results (this pass)

- `go build ./...`, `go vet ./...`: clean.
- `golangci-lint run ./xmp/...`: 0 issues (verified with all scratch test
  files removed; only the diff + `task210_test.go` + `xmpbuf_retain01_test.go`
  present).
- `go test -race -count=1 ./xmp/... .`: PASS, 0 races.
- `go test -fuzz=FuzzParseXMP -fuzztime=60s ./xmp/...`: PASS, ~19.8M execs,
  0 crashers. (Fuzzing did not independently discover
  XMPARENA-NS-REINTERN-01 in this 60s budget — it requires a fairly specific,
  large, structured corpus entry (long xmlns URI + many sibling collection
  elements) that undirected byte-level mutation is unlikely to stumble onto
  quickly; this is a case where targeted/adversarial reasoning found what
  fuzzing alone did not in the time budget.)
- `govulncheck`: still not runnable in this environment (pre-existing Go
  toolchain version mismatch, unrelated to this diff).

## Verdict

**BLOCKED — CRITICAL.** XMPBUF-RETAIN-01 itself is correctly fixed and the
`unsafe.String`/arena aliasing-safety model is otherwise sound (all 4
questions answered SAFE, matching [[audit_findings_20260925_task210_xmpbuf]]'s
prior conclusion on the aliasing design generally). However, the concrete
remediation introduced a new, more severe, single-request memory-exhaustion
vulnerability (XMPARENA-NS-REINTERN-01) that must be fixed — and, given its
trivial one-line nature, should be fixed in the SAME development cycle before
this diff is committed. Recommend delegating the one-line fix (above) to
`go-performance-architect`, adding the suggested regression test, then a
follow-up audit pass to confirm closure before clearance.
