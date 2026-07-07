---
name: project_task273_xmp_container_preservation
description: task #273 XMP round-trip container-type corruption fix (commit 39144d3) — containerTypes field, arrayProperties table consolidation, Lang-Alt leak fix
type: project
---

Task #273 (Clean-GO sprint) fixed a real round-trip CORRUPTION defect in
xmp/, not just a coverage gap: `xmp.Properties` (`map[string]map[string]string`)
carried no per-property RDF container-type tag, so `collectionType`/
`isCollectionProperty` (namespace.go) were the SOLE authority on every
`Encode()` call for every property present, regardless of provenance. A
plain `Parse() -> (unrelated field write) -> Encode()` round trip — no
`Set()` call on the affected property required — silently downgraded any
array-typed property outside the allowlist's namespace/name coverage to a
bare scalar, including every property in every unknown/custom namespace
(which an allowlist can never enumerate). Direct violation of CLAUDE.md's
"existing metadata not explicitly modified is preserved exactly" mandate
(ISO 16684-1 §7.5), reachable on ordinary read-modify-write, not an edge
case.

**Fix (commit 39144d3):**
- Added an unexported `XMP.containerTypes map[string]map[string]string`
  field (xmp.go), parallel to `Properties`, recording the Seq/Bag/Alt kind
  actually observed at parse time, keyed by the top-level/parent local name
  (same key as `Properties` uses for that property).
- `recordContainerType` (rdf.go) populates it from `onStartCollection`, the
  moment an `rdf:Alt/Seq/Bag` element opens — before any `rdf:li` children
  are read — so a collection holding exactly one item is still recorded.
- `effectiveContainerType` (namespace.go, a method on `*XMP`) is the single
  new authority consulted by write.go: parse-time record takes priority
  over the spec-sourced table, which is now only the fallback for
  properties with no source document (i.e. newly `Set()`).
- Consolidated `collectionType`/`isCollectionProperty` (previously two
  independently hand-written switches that had already drifted apart twice
  — task #43's xmpMM entry, task #272's dc-only scope) into one
  `arrayProperties` map — the two functions can no longer disagree with
  each other.
- Expanded that table with every array-typed property in xmpRights, xmp
  (Basic), tiff, exif, photoshop, Iptc4xmpCore, and Iptc4xmpExt — all
  cross-checked deterministically against exiv2.org's raw XMP tag-table
  HTML (see performance-methodology note below), not guessed.
- Corrected xmpMM:Ingredients/Pantry from the wrong "Seq" to the
  spec-correct "Bag" (namespace.go's old #43 comment incorrectly cited
  both alongside History); added the previously-missing xmpMM:Versions
  (Seq), which used to fall through to the wrong Bag default.
- The Lang-Alt "lang|value" internal-separator leak (a single-item
  non-x-default Alt property outside the allowlist emitted the literal
  string `"fr|Bonjour"` into output) is fixed as a DIRECT SIDE EFFECT of
  the container-type fix: once the property is correctly identified as
  Alt (via parse-time record or the expanded table), it's routed through
  `writeMultiValuedProperty`'s Alt branch, which already strips the prefix
  correctly. No separate code path was needed for this half of the defect.

**Consultant-tool substitution note:** the `Agent` tool for invoking
`xmp-metadata-expert` was not available in this session's toolset. Verified
the comprehensive container-type table myself via `curl` + a small Python
regex parse of the raw HTML from `exiv2.org/tags-xmp-<ns>.html` (deterministic,
not an AI-summarized WebFetch, which was demonstrably lossy/inconsistent
for the xmpMM and Iptc4xmpExt tables — cross-checked the same page twice via
WebFetch and got self-contradictory "Ingredients: XmpBag" vs "Pantry: XmpText"
summaries for value types that were actually consistent in the raw table).
**Lesson: for any future task requiring a large verbatim reference table
from an HTML source, prefer `curl` + deterministic parsing over WebFetch's
AI summarization — the summarizer silently drops or garbles rows in big
tables.** See [[feedback_webfetch_html_tables_unreliable]].

**Performance lesson (non-obvious, cost real iteration time):**
Go's compiler has a special-case optimization: `switch tl { case "lit1":
... }` (or `tl == "lit1"`) where `tl := string(byteSlice)` avoids
allocating `tl` at all, IF AND ONLY IF `tl` is used exclusively inside
comparison expressions against string literals — never passed to a
function call or stored anywhere. The moment `tl` is also passed as an
argument to another function (even on a branch that turns out not to
allocate anything itself, even when that function is fully inlined), the
compiler must materialize `tl` as a real heap string, because escape
analysis is static per-function, not per-runtime-branch. This cost +4
allocations / +633 B per parse of a representative multi-collection XMP
document in an early version of this fix (passing `tl` straight into
`specTableMatches`/`recordContainerType`). Fix: split into
`onStartCollection` (keeps `tl` used ONLY in the `switch tl { case "Alt":
...}` comparison) calling `startCollection(ctype string)` with a STRING
LITERAL argument ("Alt"/"Seq"/"Bag" — references to static read-only
data) rather than `tl` itself. Restored to the exact pre-fix baseline: 45
allocs/op, +8 B/op (from the new struct field alone) on
`BenchmarkRDFParse`. Verified via `testing.AllocsPerRun` bisection, not
assumption. **Rule for the future: when a `[]byte`-derived string is used
in a hot-path comparison with the "zero-alloc compiler optimisation"
comment already present, any refactor that adds a new use of that same
variable (passing it to a helper, storing it, returning it) must be
re-verified with `testing.AllocsPerRun`, not just `go test -bench`, because
the allocation delta from losing this optimisation can be small in `ns/op`
terms but real and attributable via bisection.**

**Why:** the round-trip fidelity mandate (CLAUDE.md, ISO 16684-1 §7.5) is
inviolable; this closes an entire CLASS of corruption, not just the
instances found so far. The performance-methodology and consultant-tool
notes are process lessons for repeating this kind of "verify a large
external spec table" task correctly and efficiently.
**How to apply:** any future addition to `xmp/namespace.go`'s
`arrayProperties` table should be cross-checked the same way (raw HTML
parse of exiv2.org, or the actual Adobe XMP spec PDF) — never trust a
single AI-summarized fetch of a large table. Any future change to
`onStartCollection`/hot parse-path functions that touches a `[]byte`→
`string` conversion already marked "zero-alloc compiler optimisation"
must be re-benchmarked with `testing.AllocsPerRun`.
