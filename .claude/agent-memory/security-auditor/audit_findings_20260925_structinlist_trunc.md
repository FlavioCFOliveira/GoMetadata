---
name: audit-findings-20260925-structinlist-trunc
description: Final audit 2026-09-25 of the XMPARENA-NS-REINTERN-01 remediation (xmp/rdf.go, xmp/fuzz_test.go, xmp/xmparena_ns_reintern01_test.go). recordContainerType's guarded intern fix CONFIRMED CLOSES the CRITICAL finding. buildStructInListKey's 256-byte component truncation CONFIRMED closes the DoS vector but introduces a NEW HIGH correctness/spec-conformance regression: silent Parse->Encode round-trip corruption of long (legal, XML-Name-unbounded) property/field names, AND silent data loss via key collision between two distinct long names sharing a 256-byte prefix. NOT CLEARED.
metadata:
  type: project
---

## Scope

Third same-day audit pass on the uncommitted `xmp/` diff, following
[[audit_findings_20260925_task210_xmpbuf]] (task #210 aliasing design) and
[[audit_findings_20260925_arena_reaudit]] (XMPARENA-NS-REINTERN-01 discovery).
This pass reviews the remediation for XMPARENA-NS-REINTERN-01:
`xmp/rdf.go` (`recordContainerType` guard fix + new
`maxStructInListKeyComponentLen` truncation in `buildStructInListKey`),
`xmp/fuzz_test.go` (new `fuzzArenaRatioBound` invariant), and new
`xmp/xmparena_ns_reintern01_test.go` (DoS-bound regression battery).

## Part 1 — recordContainerType fix: CONFIRMED CLOSES XMPARENA-NS-REINTERN-01

```go
func recordContainerType(x *XMP, ns, local, ctype string) {
	if x.containerTypes == nil {
		x.containerTypes = make(map[string]map[string]string)
	}
	if x.containerTypes[ns] == nil {
		ns = x.intern(ns)
		x.containerTypes[ns] = make(map[string]string)
	}
	if x.containerTypes[ns][local] != "" {
		return
	}
	x.containerTypes[ns][x.intern(local)] = ctype
}
```

Now mirrors `storeProperty`'s pattern exactly: `ns` is interned only when its
outer map entry is first created (once per distinct namespace); `local` is
guarded by a first-wins check evaluated *before* interning (matching
`storeProperty`'s own guard), so a repeated `(ns, local)` pair never
re-interns. Independently reproduced the original CVE PoC (nsLen=50000,
nProps=1500) at scratch scale: **ratio dropped from 562x (pre-fix) to
0.87x (post-fix)** — confirmed closed, not just asserted by the maintainers'
own tests. `TestXMPArenaNSReintern*` battery (4 vectors: container-siblings,
simple-properties, shorthand-attributes, struct-fields) and the new
`fuzzArenaRatioBound = 32` invariant in `FuzzParseXMP` all PASS; fuzz run
(60s, ~19.3M execs) found 0 crashers and 0 ratio-bound violations.

Side-effect note (correct, disclosed in the diff's own comment): duplicate
struct-in-list-typed property declarations now get first-wins container-type
recording, consistent with `storeProperty`'s first-wins value policy
(previously the recorded container type could reflect the LAST duplicate
declaration while the stored VALUE reflected the FIRST — an inconsistency
this fix also removes). Not a regression.

**Verdict on Part 1: CLOSED.**

## Part 2 — buildStructInListKey truncation: DoS closed, NEW HIGH correctness regression

```go
const maxStructInListKeyComponentLen = 256

func buildStructInListKey(propLocal string, idx int, fieldLocal string) string {
	if len(propLocal) > maxStructInListKeyComponentLen {
		propLocal = propLocal[:maxStructInListKeyComponentLen]
	}
	if len(fieldLocal) > maxStructInListKeyComponentLen {
		fieldLocal = fieldLocal[:maxStructInListKeyComponentLen]
	}
	... builds "propLocal[idx].fieldLocal" ...
}
```

### Why this exists (legitimate DoS closure)
For a struct-in-list property, the wrapping tag name (`propLocal`) is written
ONCE in the source document but re-embedded as a fresh prefix in a
NEWLY-INTERNED key for EVERY `rdf:li` item — the exact "declare once,
reference cheaply many times" asymmetry that made XMPARENA-NS-REINTERN-01
severe, just via struct-in-list item count instead of sibling-element count.
Confirmed (maintainers' own test, `TestXMPArenaStructInListKeyLengthBound`):
uncapped, this scales unboundedly with `propLocal` length x item count;
capped, the ratio is bounded to a small constant (~5-6x) regardless of name
length or item count. **This part of the fix is sound and necessary.**

### The correctness problem (confirmed, not theoretical)

`buildStructInListKey`'s output becomes a `Properties` map key — and
`Properties` is an **exported field** (`x.Properties map[string]map[string]string`),
directly inspectable by any caller; there is no `Get`-style helper in
between. It is ALSO the sole source of truth `write.go`'s
`writeStructInListProperty`/`parseStructKey`/`collectStructInListIndices`
use to regenerate the property/field XML tag names on `Encode` — confirmed
by reading `xmp/write.go`: `parent` and `field` are extracted directly by
string-splitting the stored key (`local[:bracketIdx]`,
`local[len(prefix2):]`), then passed straight to `writeXMLName`. There is no
independent, untruncated copy of the original name kept anywhere.

Two concrete, empirically-confirmed failure modes (scratch-only PoCs, not
committed — reproduced against the actual current diff):

1. **Round-trip corruption** (ISO 16684-1 / XML 1.0 §2.3 has NO length limit
   on Name production — this is legal XMP): a single struct-in-list field
   name of 300 bytes is stored as a 256-byte-truncated key
   (`"P[0]." + 256×'F'`, confirmed via direct `x.Properties` inspection), and
   `Encode(x)` emits `<ns0:FFFF...(256 F's)...>` in the output XML —
   the original 300-byte name is **not present anywhere** in the encoded
   output. A Parse → Encode round trip permanently and silently corrupts a
   legal property/field name, violating CLAUDE.md's non-negotiable "100% XMP
   compliant... without corrupting... metadata" / "existing metadata not
   explicitly modified is preserved exactly" mandate.
2. **Collision-based silent data loss** (more severe): two DISTINCT
   top-level struct-in-list property names sharing an identical 256-byte
   prefix but different suffixes (`"Q"×256+"_PROP_A_DISTINCT"` vs.
   `"Q"×256+"_PROP_B_DISTINCT"`), each wrapping item index 0 with the same
   short field name, truncate to the IDENTICAL key
   (`"QQQ...[0].f"`). `storeProperty`'s pre-existing first-wins guard then
   silently drops the second property's value entirely: **1 stored entry
   instead of 2** — `valueB` vanishes with no error, warning, or any other
   observable signal (the `xmp` package has no `Warnings`-style mechanism at
   all — confirmed via grep). A companion test confirms this specific
   collision requires the SAME rdf:li item index; when indices differ
   (`"P[0]."` vs `"P[1]."`), the index itself disambiguates and no collision
   occurs — so the bug is narrower than "any two 256-byte-prefix-sharing
   names always collide", but is real and reachable whenever the collision
   happens to land at the same index (same top-level property with two
   colliding sibling names, or two colliding parent property names each
   with one item).

Both are **confirmed reachable via the public API** (`xmp.Parse` +
`xmp.Encode`, and direct `x.Properties` inspection) with zero malformed
input — perfectly legal XML/XMP, no entities, no encoding tricks.

### Severity assessment
HIGH (not CRITICAL — this is a silent data-integrity violation on
legal-but-unusual input, not a crash/memory-safety issue, and requires
property/field names beyond 256 bytes, which is rare in real-world XMP
producers though explicitly legal per the spec and squarely inside this
project's own "100% spec compliant, never corrupts metadata" mandate).
Confirmed exploitability (not theoretical): direct reproduction via 2
independent PoC scenarios, matching the exact mechanism the coordinator
asked to be checked for.

### Proposed remediation options (presented, not unilaterally chosen —
this is a design tradeoff, not a one-line bug fix)

1. **Budget-based admission control (recommended)**: track a cumulative byte
   budget for struct-in-list-item keys (e.g., a running counter on the
   parser or `*XMP`, budget proportional to input document length — same
   spirit as the existing `#122`/`#134` extended-XMP truncation-with-warning
   pattern already used elsewhere in this codebase). Every property/field
   name that IS accepted is stored byte-exact, in full, never truncated;
   once the budget is exhausted, additional struct-in-list items are
   dropped wholesale (not corrupted) — ideally surfaced as a parse warning
   if/when the `xmp` package grows a warnings mechanism. This preserves
   full round-trip fidelity for every name up to the point the budget is
   reached, with zero collision risk ever (dropped items are simply absent,
   never merged into an unrelated key).
2. **Reject-or-cap the rdf:li item COUNT per property** (simpler to
   implement, matches the existing `attrBuf`/`nsTable`-capacity precedent of
   silently dropping entries beyond a fixed count) combined with **never
   truncating name components** — bounds the common "many items, moderate
   name length" attack shape without altering any accepted name, but does
   NOT alone bound the "few items, extremely long name" shape (a single
   legitimate long name is already bounded by document size, so this may be
   an acceptable residual — needs sizing/confirmation).
3. **Redesign the underlying storage so `propLocal` is not re-embedded per
   item** (e.g., a nested map keyed by index, avoiding the bracket-string
   convention entirely) — the structurally "textbook correct" fix with zero
   truncation and true O(distinct content) bounds, but changes the shape of
   the exported `Properties` map's key convention and `write.go`'s
   `parseStructKey`/`collectStructInListIndices` parsing logic — a larger,
   API-shape-affecting change requiring explicit user sign-off before
   implementation, not a same-cycle one-line fix.

## Tooling results (this pass)

- `go build ./...`, `go vet ./...`: clean.
- `golangci-lint run ./xmp/...`: 0 issues (scratch files removed before this
  check; only the real diff + 3 legitimate test files present).
- `go test -race -count=1 ./xmp/... .`: root package `.` PASS, 0 races;
  `./xmp/...` shows only my OWN scratch correctness-regression assertions
  failing (by design — they demonstrate the bug), no race detected in any
  case, no panic, no memory-safety issue — purely a logic/data-integrity
  finding.
- `go test -fuzz=FuzzParseXMP -fuzztime=60s ./xmp/...` (after removing
  scratch files so the pre-fuzz unit-test gate passes cleanly): PASS,
  ~19.3M execs, 0 crashers, 0 `fuzzArenaRatioBound` violations. Fuzzing in
  this budget did not independently discover the truncation/collision
  correctness issue either (it requires a specific, large, structured
  corpus shape — two long, prefix-sharing names — that undirected mutation
  is unlikely to construct in 60s; found only via targeted reasoning about
  the fix's own mechanism, same pattern as XMPARENA-NS-REINTERN-01 itself).
- `govulncheck`: still not runnable (pre-existing local toolchain-version
  mismatch, unrelated to this diff).

## Verdict

**NOT CLEARED.** Part 1 (recordContainerType) is CLOSED — confirmed via
independent reproduction of the original CVE at a 562x→0.87x ratio drop.
Part 2 (buildStructInListKey truncation) correctly closes its own,
narrower DoS vector but introduces a NEW HIGH finding: silent,
attacker-independent (reachable with perfectly legal XMP) data corruption
(round-trip name truncation) and data loss (collision-based silent
overwrite) for struct-in-list property/field names longer than 256 bytes.
Recommend presenting the 3 remediation options above to the user before
delegating a fix to `go-performance-architect` — this is a genuine
correctness/robustness-vs-simplicity tradeoff (per CLAUDE.md's Decision
Policy: ambiguous, must ask), not a mechanical bug fix.
