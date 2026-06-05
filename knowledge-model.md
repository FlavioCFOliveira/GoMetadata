# Knowledge Model — GoMetadata

This file is the authoritative description of the project's knowledge graph (a Label
Property Graph stored in `rmp graph`, roadmap `gometadata`). **The file and the graph must
mirror each other** — whenever a new label, edge type, or property is introduced, update
both in the same step.

- **Provenance**: every node **and** edge carries `gitCommit` (full hash of the commit when
  the element was last confirmed) and `gitDate` (ISO date, e.g. `2026-06-01`).
- **Granularity decision**: the project has 1026 tests and 1000 exported functions. These are
  **not** modeled one-node-each (that would be noise). Tests live as a `testCount` property on
  `Package` (individual `Test` nodes exist only when a test verifies a tracked `Feature`).
  Functions are modeled only for the public API surface. Benchmarks and fuzz targets *are*
  modeled individually — they are bounded (60 / 27) and security/performance-relevant.
- **Source of truth**: files are ground truth. On a graph/file mismatch, trust the files,
  fix the graph, and note the discrepancy.

---

## Node labels (11)

| Label | Key | Count* | Properties |
|---|---|---|---|
| `Package` | `name` | 29 | `name, path, module, layer, description, testCount, entryReachable, gitCommit, gitDate` |
| `File` | `path` | 75 | `path, name, package, gitCommit, gitDate` |
| `Type` | `name`+`package` | 34 | `name, package, file, kind, exported, gitCommit, gitDate` |
| `Function` | `name`+`package` | 11 | `name, package, file, signature, kind, gitCommit, gitDate` |
| `Feature` | `name` | 34 | `name, description, domain, package, file, spec_ref, type, commit_introduced, commit_fixed, gitCommit, gitDate` |
| `FuzzTarget` | `name` | 27 | `name, package, file, gitCommit, gitDate` |
| `Benchmark` | `name` | 60 | `name, package, file, gitCommit, gitDate` |
| `Spec` | `name` | 9 | `name, standard, ref, domain, gitCommit, gitDate` |
| `Commit` | `hash` | 12 | `hash, short_hash, message, author, date, scope, gitCommit, gitDate` |
| `Test` | `name` | ~4 | `name, package, file, type, description, commit_introduced, gitCommit, gitDate` |
| `FormatCapability` | `format` | 13 | `format, extensions, read, write, exif, iptc, xmp, container, gitCommit, gitDate` |

\* approximate at bootstrap (`402a067`, 2026-06-01).

### `layer` values for `Package`
`entry` (root `gometadata`) · `exif` · `format` · `format-container`
(jpeg/png/webp/heif/tiff) · `format-raw` (arw/cr2/cr3/dng/nef/orf/rw2) · `internal` ·
`example`.

### `domain` values for `Feature` / `Spec`
`exif` · `iptc` · `xmp` · `container` · `api` · `hardening`.

### `entryReachable` on `Package` (boolean)
Computed from the graph's own `DEPENDS_ON` edges: `true` if the package is the entry
(`gometadata`) or transitively imported by it; `false` otherwise. At `402a067` (bootstrap):
20 `true`, 23 `false` (8 `example` alternate-entry packages + the 12-package
`exif/makernote` dead subtree + 3 `internal` orphans). After task #89 (dead-code removal,
see below): all 14 dead non-example packages deleted; the `false` set is now 8 `example`
packages only. Query: `MATCH (p:Package {entryReachable:false}) RETURN p.name`.

---

## Capabilities matrix (`FormatCapability`)

To make the module's **real, code-verified capabilities** directly queryable (added
2026-06-03, for the v1.1.0 release), the graph carries one `FormatCapability` node per
supported container format (13). Each node is the authoritative answer to "what can the module
do with format X" and mirrors the README *Supported formats* table and `format.SupportsWrite`:

| Property | Meaning |
|---|---|
| `format` | container name — JPEG, TIFF, PNG, WebP, HEIF, AVIF, CR2, CR3, NEF, ARW, DNG, ORF, RW2 |
| `extensions` | recognised extensions (detection is by magic bytes, never by extension) |
| `read` | metadata read supported — `true` for all 13 |
| `write` | metadata write supported — **equals `format.SupportsWrite`**: `true` for JPEG/PNG/WebP/HEIF/AVIF/CR3/TIFF; `false` for DNG (re-gated task #101, bug #98 SubIFD value-loss), CR2/NEF/ARW/ORF/RW2 (gated by `ErrWriteNotSupported`, task #95). TIFF write: copy-and-relocate (tasks #92/#93). CR3 write: stco/co64 relocation (task #91). DNG: re-enabled as task #94, re-gated as task #101 pending bug #98 |
| `exif` / `iptc` / `xmp` | which metadata blocks the format carries on read (`iptc` only JPEG + TIFF) |
| `container` | structural family: `JPEG` / `TIFF` / `TIFF-based` / `ISOBMFF` / `RIFF` / `PNG` |

These are **standalone nodes** (no edges) — consistent with engine constraint #2 (heterogeneous
edges to pre-existing nodes cannot be added incrementally); the `format` / `container`
properties carry the linkage. Example queries:

```cypher
MATCH (c:FormatCapability {write:true})              RETURN c.format            // writable formats (7: JPEG, PNG, WebP, HEIF, AVIF, CR3, TIFF)
MATCH (c:FormatCapability {iptc:true})               RETURN c.format            // carry IPTC (JPEG, TIFF, DNG)
MATCH (c:FormatCapability {container:'TIFF-based'})  RETURN c.format, c.write   // TIFF-based formats (TIFF writable; DNG/CR2/NEF/ARW/ORF/RW2 read-only)
```

Out-of-scope formats (CRW, RAF, MRW, IIQ, X3F, SRW, PEF, RWL — see `doc.go`) are intentionally
NOT modelled as `FormatCapability` nodes: the module returns `UnsupportedFormatError` for them.

---

## Edge types (10)

**Structural**

| Type | Pattern | Meaning |
|---|---|---|
| `PART_OF` | `Package → Package` | directory/structural nesting (e.g. `exif/makernote → exif`) |
| `DEPENDS_ON` | `Package → Package` | Go import dependency (intra-module only) |
| `CONTAINS` | `Package → {File, Feature, Type, Function, FuzzTarget, Benchmark}` | package owns the element |
| `DEFINES` | `File → {Type, Function}` | file declares the type/function |

**Semantic**

| Type | Pattern | Meaning |
|---|---|---|
| `COMPLIES_WITH` | `Feature → Spec` | feature implements/conforms to a standard |
| `FUZZES` | `FuzzTarget → {Package, Feature}` | robustness surface under test |
| `TESTS` | `Test → {Feature, Package}` | test verifies the target |
| `HAS_TEST` | `Package → Test` | package owns the (feature-linked) test |
| `INTRODUCED` | `Commit → Feature` | commit that introduced the feature |
| `FIXED` | `Commit → Feature` | commit that fixed/hardened the feature |

---

## Notable structural facts (at `402a067`)

- The **root package is the dispatcher**: it imports every container/format package
  (`exif`, `format`, `format/{jpeg,png,webp,heif,tiff}`, `format/raw/*`, `iptc`, `xmp`,
  `internal/metaerr`) and reconciles results into the unified `Metadata`.
- `format` itself only does container **detection** (`FormatID`, `Container`,
  `SupportsWrite`); per-format extraction lives in the subpackages.
- TIFF-based RAW formats (`arw, cr2, dng, nef, orf, rw2`) `DEPENDS_ON` `format/tiff`, which
  `DEPENDS_ON` `exif`. `cr3` is **self-contained** (BMFF parsed inline; depends on no
  intra-module package).
- `internal/testutil` has no intra-module production importer (test-only helper); it is kept
  because it is imported by test files across the module and provides no dead-code risk.
- **Dead-code subtrees removed in task #89 (2026-06-05):** `exif/makernote/` (dispatch.go +
  11 vendor packages: canon, dji, fujifilm, leica, nikon, olympus, panasonic, pentax,
  samsung, sigma, sony), `internal/bmff`, and `internal/byteorder` — 14 packages / 33 files
  deleted. None had any live intra-module importer (confirmed by `grep` before deletion). The
  live MakerNote dispatch remains at `exif/makernote_parse.go` inside the `exif` package.
  Package count: 43 → 29.

---

## Maintenance

- **Post-commit sync**: `git diff --name-only HEAD~1 HEAD`; for each changed file, update its
  `File` node's provenance, MERGE new nodes, DETACH DELETE removed ones, and bump the
  provenance of touched `Feature`/`Package` nodes.
- **Hygiene**: label-less or orphaned nodes are data-quality bugs —
  `MATCH (n) WHERE size(labels(n)) = 0 RETURN count(n)` must return 0.

---

## Bootstrap result (2026-06-01, commit `402a067`)

Materialized **296 nodes, 422 edges**; zero label-less nodes, zero nodes missing provenance.

| Label | n | | Label | n |
|---|---|---|---|---|
| File | 75 | | FuzzTarget | 26 |
| Benchmark | 60 | | Function | 11 |
| Package | 43 (29 after task #89) | | Spec | 9 |
| Type | 34 | | Commit | 5 |
| Feature | 29 | | Test | 4 |

Edges: `CONTAINS` 235 · `DEPENDS_ON` 50 · `DEFINES` 45 · `FUZZES` 26 · `PART_OF` 24 ·
`COMPLIES_WITH` 26 · `TESTS` 7 · `HAS_TEST` 4 · `FIXED` 3 · `INTRODUCED` 2.

Accepted orphans: 4 `Commit` nodes (release/history commits not tied to a single tracked
feature — linking them would be fabrication).

---

## Update — Sprint 8 hardening battery (2026-06-03, commits `dcc23f7`..`af1c9e0`)

Sprint 8 (test-hardening battery, now CLOSED) raised coverage and closed several DoS/robustness
gaps across the Tier 1/2 critical packages. Graph after this update: **309 nodes, 428 edges**
(was 296 / 422); zero label-less nodes; every Sprint-8 commit present exactly once.

**Provenance + `testCount` bumped** on the 7 touched `Package` nodes (and the 7 changed
production `File` nodes):

| Package | testCount (was → now) | confirmed at |
|---|---|---|
| `internal/iobuf` | 8 → 21 | `dcc23f7` (2026-06-02) |
| `exif` | 201 → 216 | `b26f020` (2026-06-02) |
| `format/tiff` | 17 → 38 | `d79db96` (2026-06-02) |
| `gometadata` | 109 → 130 | `236acb5` (2026-06-03) |
| `xmp` | 70 → 108 | `2330d74` (2026-06-03) |
| `iptc` | 51 → 74 | `3f5768e` (2026-06-03) |
| `format/jpeg` | 43 → 84 | `af1c9e0` (2026-06-03) |

**New nodes**: 7 `Commit` (the Sprint-8 commits), 5 `Feature` (domain `hardening`), 1
`FuzzTarget` (`FuzzRead`, the root end-to-end target; total fuzz targets 26 → 27).

**New hardening `Feature` nodes** (each linked `Commit -[:FIXED]-> Feature`):

| Feature `id` | package | commit_fixed |
|---|---|---|
| `feat:iobuf:pool_safety_caps` | `internal/iobuf` | `dcc23f7` |
| `feat:tiff:bigtiff_magic_gate` | `format/tiff` | `d79db96` |
| `feat:xmp:document_size_cap` | `xmp` | `2330d74` |
| `feat:iptc:encode_receiver_purity` | `iptc` | `3f5768e` |
| `feat:jpeg:extended_xmp_guid_cap` | `format/jpeg` | `af1c9e0` |

`236acb5 -[:INTRODUCED]-> (FuzzTarget FuzzRead)`. Edge totals: `FIXED` 3 → 8, `INTRODUCED` 2 → 3.

**Incremental-edge compromise** (consequence of engine constraint #2 below): the 5 new
`Feature` nodes and the `FuzzRead` `FuzzTarget` were attached to their owning packages via the
`package` **property**, not a `CONTAINS`/`FUZZES` edge — heterogeneous edges to pre-existing
nodes cannot be added via `MATCH`+`MERGE`, and a single-`CREATE` form would duplicate the
existing `Package` node. Query these by property, e.g.
`MATCH (f:Feature {package:'xmp', domain:'hardening'}) RETURN f.name`. A future full rebuild
would restore the structural edges. The `Commit -[:FIXED/INTRODUCED]-> {Feature,FuzzTarget}`
edges WERE created (both endpoints new, via the single-`CREATE` working pattern).

**Orphan commit**: `b26f020` (exif test-only commit) is tracked as a `Commit` node with no
feature edge — it added tests/fuzz seeds without a production-code feature. Accepted orphans
are now 5.

**Transversal Sprint-8 outcomes** (not modelled as nodes; recorded here): the CI workflow
gained a `fuzz` job running 6 targets (`FuzzRead`, `FuzzParseEXIF`, `FuzzParseIPTC`,
`FuzzParseXMP`, `FuzzJPEGExtract`, `FuzzHEIFExtract`) at 10s `-race` each; the final
security-auditor pass cleared the sprint with **no MEDIUM+ findings open** and a bounded
aggregate memory model (~336 MiB worst-case for an adversarial ~260 MiB JPEG). Backlog `#54`
(full BigTIFF read support) was opened to track the deferred scope from `feat:tiff:bigtiff_magic_gate`.

---

## Update — Sprint 9: production-readiness doc corrections (2026-06-03)

A production-readiness re-assessment (commit `84993f0` baseline) returned **APTO COM
CONDIÇÕES**: build/vet/lint/staticcheck/govulncheck clean, 83.6% coverage, 27 fuzz targets
(~120M execs, 0 crashers), race-clean, single indirect dependency (`golang.org/x/text`),
zero open MEDIUM+ security findings, and a performance profile meeting the ultra-performance
goal (zero-alloc hot paths confirmed). The one blocking condition was a **documentation
defect**, fixed in Sprint 9 (task #55):

- The code was already faithful — the write gate (`write.go:88`), `format.SupportsWrite`
  (`format/format.go:66`), `doc.go` and `ErrWriteNotSupported` all correctly report that the
  7 TIFF-based containers (TIFF, CR2, NEF, ARW, DNG, ORF, RW2) are read-only.
- Only the docs diverged: `README.md` (Supported formats Write column `Yes`→`No¹` for those 7
  + footnote; feature rows; fuzz count `18`→`27`) and `CHANGELOG.md` (the `[1.0.0]` "read and
  write" bullet). Now every format's documented Write capability matches `format.SupportsWrite`.

Deferred (tracked, not done): full RAW/TIFF write support (epic `#33`), an optional
`WithMaxInputBytes` guard (LOW), and per-MakerNote / ORF / RW2 / CR3 benchmark gaps (LOW).
The unreachable `exif/makernote/*` duplicate subtree, `internal/bmff`, and `internal/byteorder`
were subsequently removed in task #89 (2026-06-05).

Graph delta: +2 orphan `Commit` nodes (the `docs:` fidelity fix `348704c` and this model-sync
commit). `Commit` 12 → 14; accepted orphans 5 → 7. **No new labels, edge types, or
properties** — the graph shape is unchanged.

---

## Update — Task #89: dead-code removal (2026-06-05)

Deleted three unreachable subtrees after confirming zero live importers via `grep`:

| Subtree removed | Files deleted | Reason |
|---|---|---|
| `exif/makernote/` (dispatch + 11 vendor pkgs) | 29 | Parallel implementation; live dispatch is `exif/makernote_parse.go` |
| `internal/bmff/` | 2 | HEIF/CR3 inline their own BMFF parsing |
| `internal/byteorder/` | 2 | Code uses `encoding/binary` directly |

**Total**: 14 packages / 33 files deleted. Package count: 43 → **29**.

Graph delta: 14 `Package` nodes removed (and their `PART_OF`/`DEPENDS_ON`/`CONTAINS` edges).
The `makernote` layer value is now obsolete. No new labels, edge types, or properties added.
`entryReachable:false` set reduced from 23 to 8 (example packages only).

The `exif/makernote_parse.go` file (package `exif`) is **NOT removed** — it is the live
MakerNote dispatch and is entry-reachable via the `exif` package.

---

## ⚠️ Engine constraints — Groadmap `rmp graph` v1.6.0 (READ BEFORE MUTATING)

Discovered empirically during bootstrap. Violating these corrupts the store:

1. **NEVER run unfiltered `MATCH (n) DETACH DELETE n`.** It does not delete nodes — it
   *strips their labels and properties*, leaving undeletable ghost records and corrupting the
   append-only WAL. Any `DELETE` carries WAL-corruption risk; prefer a full rebuild.
2. **Heterogeneous edges cannot be added between pre-existing nodes.**
   `MATCH (a:LabelA …) MATCH (b:LabelB …) MERGE (a)-[:R]->(b)` silently creates **0** edges
   when `LabelA ≠ LabelB` (every MATCH form tested). It works only for **same-label**
   endpoints (e.g. `Package`→`Package`, used for `PART_OF`/`DEPENDS_ON`).
3. **Working pattern for heterogeneous edges**: a single `CREATE` declaring *both* endpoint
   nodes as variables and the edge between them. The whole connected graph is therefore built
   as one ~75 KB `CREATE` produced by the regenerator (kept at `/tmp/gen_kg.py` during the
   bootstrap session).
4. **Guard rails**: `create` accepts only `CREATE`/`MERGE` (no `SET`; no pattern predicates in
   `WHERE`); `update` only `SET`/`REMOVE`; `delete` only `DELETE`/`DETACH DELETE`.
5. **Full-rebuild recipe**: reset the WAL (`mv ~/.roadmaps/gometadata/graph/wal` aside so
   `rmp` recreates an empty store), run the mega-`CREATE`, then add the same-label
   `PART_OF`/`DEPENDS_ON` edges via `MATCH`+`MERGE`.
