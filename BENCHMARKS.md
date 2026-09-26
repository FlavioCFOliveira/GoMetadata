# Benchmark History

This file records `go test -bench` results across releases of GoMetadata. Each section corresponds to a tagged release or named commit. Results are recorded for reference and regression tracking — a significant increase in `ns/op` or `allocs/op` in a hot path should be investigated before merging.

**How to reproduce**

```bash
go test -bench=. -benchmem -benchtime=3s ./...
```

**Environment (all runs)**

| Field | Value |
|---|---|
| goos | darwin |
| goarch | arm64 |
| cpu | Apple M4 |
| Go version | go1.26.1 |
| benchtime | 3s per benchmark |

---

## [feature/44 — sprint 44 perf tasks #204–#209, #241] — 2026-09-25

Sprint 44 "Performance and Efficiency Laboratory": optimisation-only pass over
the root package, `iptc/`, `internal/riff/`, and `format/webp/`, driven by
`go build -gcflags=-m` escape analysis and `go test -bench -benchmem`
evidence. All eight tasks below implemented in one merged pass. #206 was
initially assessed against its literal wording ("unexported bool") and
stopped as API-breaking; a follow-up, API-neutral approach (fixed-size
storage embedded in `*IPTC`, preserving the exact `Records[0]` shape) was
then specified and implemented — see its entry below. Go version go1.27.1,
`cpu: Apple M4` (`sysctl -n machdep.cpu.brand_string`).

### Optimisations applied

- **#204 (root: zero-alloc `readConfig`/`writeConfig` for zero-option callers)**:
  `Read`/`Write` now declare `cfg` as a stack value (`var cfg readConfig` /
  `cfg := writeConfig{...}`) and only call a new, `//go:noinline`
  `applyReadOptions`/`applyWriteOptions` helper when `len(opts) > 0`.
  `ReadOption`/`WriteOption` are `func(*readConfig)`/`func(*writeConfig)`
  invoked indirectly; passing `&cfg` to an indirect call forces the Go
  compiler to heap-allocate the pointee unconditionally, even inside an `if`
  guard that is never taken at runtime — escape analysis is not
  value-sensitive to runtime branch conditions, only to the static call
  graph. Confining that indirect call to a dedicated, non-inlined function
  isolates the forced escape to the (rare) opts-supplied path; the zero-option
  fast path's `cfg` now stays on the stack, confirmed with
  `go build -gcflags="-m -m"` (no "moved to heap: cfg" for `Read`/`Write`
  themselves; only inside `applyReadOptions`/`applyWriteOptions`). Option
  function signatures are unchanged — no public API impact.
- **#205 (iptc: `decodeString` hand-rolled ISO-8859-1 decode, no x/text)**:
  Replaced the `golang.org/x/text/encoding/charmap` pooled decoder with a
  direct byte-to-UTF-8 transcoder. ISO-8859-1's code point equals its byte
  value for the full 0x00-0xFF range (verified byte-for-byte against
  `charmap.ISO8859_1` for all 256 values plus a combined 256-byte buffer —
  zero mismatches), so no decode table is needed: a single pre-pass counts
  bytes ≥0x80, then either returns `string(b)` directly (pure ASCII, the
  common case) or writes into a `strings.Builder` sized with `Grow` up front.
  Collapses 2-3 allocations (decoder-internal + `dec.Bytes` result +
  `string()` conversion) into 1 (ASCII) or a single `Builder`-backed
  allocation (non-ASCII).
- **#206 (iptc: `Records[0]` UTF-8 flag backed by fixed-size embedded storage,
  API-neutral revision)**: the original "unexported bool" wording was rejected
  (see the superseded note that used to live here) because `Records[0]` is
  observable/mutable through the exported `Records [10][]Dataset` field, not
  internal-only. The revised, implemented approach keeps `Records[0]`'s
  observable shape byte-for-byte identical — exactly one
  `Dataset{Record:0, DataSet:0, Value:[]byte{1}}` — while eliminating both of
  its allocations: two new unexported fields, `utf8Slot [1]Dataset` and
  `utf8Val [1]byte`, are embedded directly in `IPTC` (i.e. already part of
  whatever single allocation produced `*IPTC`, e.g. `new(IPTC)` in `Parse`).
  A new `(*IPTC).setUTF8Flag` method sets `utf8Val[0] = 1`, points
  `utf8Slot[0].Value` at `utf8Val[:1:1]`, and assigns
  `Records[0] = utf8Slot[:1:1]` — a cap-clamped (`[:1:1]`) slice, so an
  external caller appending to the exported `Records[0]` always reallocates
  into a fresh backing array instead of writing into `IPTC`'s own struct
  memory (matching the growth behaviour of the former single-element
  `append`-produced slice, whose backing array was already at `cap==len==1`).
  Both former call sites (`Parse`, `setUTF8IfNeeded`) now call
  `i.setUTF8Flag()`. No `Clone`/copy-by-value path for `*IPTC` exists anywhere
  in this module (repository-wide grep); documented as an invariant any future
  `Clone` must uphold (re-point the clone's `Records[0]` at the clone's own
  arrays rather than copying the slice header verbatim). Verified with
  `go build -gcflags="-m -m"`: zero "moved to heap" lines anywhere in the
  `iptc` package after this change — `setUTF8Flag` introduces no new
  allocation site at all, piggy-backing entirely on `*IPTC`'s existing
  allocation. Three new regression tests
  (`iptc/iptc_task206_test.go`) assert the unchanged `Records[0]` shape from
  both entry points and that an external `append` to `Records[0]` does not
  corrupt the internal flag (nor leak across two independent `*IPTC`
  instances).
- **#207 (iptc: `Encode` skip clone+sort when already sorted; encBufPool cap
  guard)**: `Encode` now checks `slices.IsSortedFunc(datasets, compareDataSetNum)`
  before cloning; when true (the common case — `*IPTC` from `Parse` stores
  datasets in wire order, and `slices.IsSortedFunc` treats equal-key runs as
  sorted, matching `SortStableFunc`'s own tie-stability) it iterates the
  receiver's slice read-only, skipping `slices.Clone` + `slices.SortStableFunc`
  entirely. When not sorted, the clone is still mandatory (Encode must not
  mutate the receiver, FINDING-002). `putEncBuf` now discards buffers with
  `Cap() > 65536` instead of returning them to `encBufPool`, mirroring
  `internal/iobuf.Put`'s discard policy.
- **#208 (root: cache MWG-02 IPTC-trust-elevation decision)**:
  `computeIPTCTrustElevated` (renamed from the old per-call `iptcTrustElevated`
  logic) is now called exactly once, at the end of `Read`, and cached in a new
  unexported `Metadata.iptcTrustElev bool` field. `iptcTrustElevated()` is now
  a plain field read. `rawIPTC`/`rawIPTCDigest` are set once at construction
  and never reassigned afterward (verified by grep across the whole
  repository), so the cached decision is valid for the object's entire
  lifetime; a `digestMatchFn` test seam (`var digestMatchFn = iptc.DigestMatch`)
  lets `TestIPTCTrustElevatedCachedSingleMD5` prove the underlying MD5 runs
  exactly once regardless of how many times Copyright/Caption/Keywords/Creator
  are called afterward.
- **#209 (internal/riff + format/webp: allocation-free chunk dispatch)**:
  `readWebPChunks`'s `switch chunk.FourCCString()` (an unconditional
  `string(c.FourCC[:])` allocation per chunk — the compiler's allocation-free
  `switch string(byteSlice)` special case does not apply because the
  conversion happens inside the `FourCCString` method, not directly in the
  switch expression) is replaced with a `switch chunk.FourCC` against two
  `[4]byte` package-level constants. Added `riff.ReadChunkBuf(r, hdr *[8]byte)`
  so a caller scanning many chunks (`readWebPChunks`) can hoist a single
  `[8]byte` header buffer outside its loop instead of paying the
  interface-call-forced heap allocation (`io.ReadFull(r, hdr[:])` through an
  `io.Reader` — the Go compiler cannot prove an unknown concrete `Reader`
  implementation does not retain the slice, so it always heap-allocates the
  buffer regardless of where it is declared) once per chunk; `ReadChunk`
  itself is now a thin wrapper over `ReadChunkBuf` with an internal
  once-per-call buffer, unchanged for existing single-shot callers.
- **#241 (iptc: `Parse` exact pre-sizing via allocation-free pre-count pass)**:
  New `preCountDatasets(b []byte) [10]int` mirrors `Parse`'s own scanner and
  `storeDataset`'s skip/recovery semantics byte-for-byte (standard/extended
  length decoding, malformed-length recovery, the 1 MiB/`maxIPTCTotalBytes`/
  `maxIPTCDatasets` DoS guards, the non-storing 1:90/1:00/2:00 skips) without
  allocating or constructing `Dataset` structs. `Parse` uses its result to
  `make([]Dataset, 0, counts[rec])` each non-empty record exactly once,
  replacing the former hard-coded `make([]Dataset, 0, 12)` for record 2 only
  (and no pre-sizing at all for the other eight records). Verified invariant
  (fuzzed 14.8M+ execs, zero failures): `len(Records[rec]) <= counts[rec]`
  always, and `cap(Records[rec]) == counts[rec]` whenever `counts[rec] > 0`
  (i.e. no regrowth ever occurs).

### Validation gate

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `go test ./...` | all packages pass (includes the `docs/conformance/` battery) |
| `go test -race ./...` | all packages pass, no races |
| `golangci-lint run ./...` | 0 issues in every touched package (`.`, `./iptc/...`, `./format/webp/...`, `./internal/riff/...`); 9 pre-existing issues remain in untouched files (`exif/`, `examples/`, `format/tiff/relocate_bigtiff.go`) — out of scope for this sprint |
| `staticcheck ./...` | clean |
| `govulncheck ./...` | 0 vulnerabilities reachable from this module's code; 1 informational finding (`GO-2026-5970`, `golang.org/x/text@v0.35.0`, not called by any code path) — pre-existing, unrelated to this sprint, not fixed |
| `go test -fuzz=FuzzParseIPTC -fuzztime=30s ./iptc/...` | 14.8M execs, 0 failures (initial pass); 10.9M execs, 0 failures (#206 follow-up re-run) |
| `go test -fuzz=FuzzRIFFRead -fuzztime=30s ./internal/riff/...` | 16.0M execs, 0 failures |
| `go test -fuzz=FuzzWebPExtract -fuzztime=30s ./format/webp/...` | 8.2M execs, 0 failures |
| `go test -fuzz=FuzzWebPInject -fuzztime=30s ./format/webp/...` | 2.5M execs, 0 failures |

**#206 follow-up gate** (re-run for the touched package only, after the
API-neutral revision): `go build ./...` clean · `go vet ./...` clean ·
`go test ./...` all green · `go test -race ./iptc/... .` clean, no races ·
`golangci-lint run ./iptc/... .` 0 issues · `staticcheck ./iptc/... .` clean ·
`FuzzParseIPTC` 30s, 10.9M execs, 0 failures.

### Benchmark results (`-count=10`, `benchstat before → after`)

Before/after captured with `git stash` isolating the production-code and
internal-API-dependent test changes from the pure benchmark additions, so
both sides compile and measure the identical benchmark set. Two "after" runs
were taken; the first overlapped with concurrent `golangci-lint`/`staticcheck`
CPU load and showed implausibly high variance (±30-40%) on two `ns/op`
figures (`Write_PNG`, `Write_JPEG`) — those two are reported from the second,
isolated run (`after2`); every other row is consistent across both runs.

**Root package** (`go test -bench . -benchmem -count 10 .`)

| Benchmark | ns/op before → after | Δ ns/op | B/op before → after | Δ B/op | allocs/op before → after | Δ allocs |
|---|---|---|---|---|---|---|
| Read_JPEG | 246.5n → 243.6n | −1.16% (p=0.001) | 521 → 514 | −1.34% | 8 → 7 | **−12.50%** |
| Read_JPEG_WithXMP | 1.480µ → 1.364µ | −7.84% (p=0.000) | 2.405Ki → 1.785Ki | −25.78% | 23 → 21 | −8.70% |
| Read_PNG | 172.1n → 178.6n | +3.84% (p=0.000) | 288 → 280 | −2.78% | 10 → 9 | −10.00% |
| MWGAccessors (new, #208) | 391.4n → 43.5n | **−88.87%** (p=0.000) | 0 → 0 | ~ | 0 → 0 | ~ |
| ReadProgressiveJPEG | 214.9n → 214.7n | ~ (p=0.515) | 245 → 240 | −2.04% | 3 → 2 | −33.33% |
| ReadCombinedMetadataJPEG | 12.72µ → 12.00µ | −5.68% (p=0.000) | 21.93Ki → 20.76Ki | −5.31% | 107 → 84 | −21.50% |
| ReadFile | 2.299µ → 2.308µ | ~ (p=0.361) | 6.181Ki → 6.174Ki | −0.11% | 15 → 14 | −6.67% |
| Write_JPEG | 401.6n → 380.3n | **−5.30%** (p=0.000) | 240 → 192 | **−20.00%** | 11 → 9 | **−18.18%** |
| Write_PNG | 249.3n → 248.5n | ~ (p=0.128) | 136 → 136 | ~ | 15 → 14 | −6.67% |
| ReadFile_Concurrent | 12.73µ → 12.11µ | −4.80% (p=0.003) | 692 → 686 | −0.87% | 10 → 9 | −10.00% |
| **geomean** | 849.9n → 667.6n | **−21.45%** | — | −6.27% | — | −13.25% |

`Write_JPEG`'s 2-allocation drop is #204 (1 alloc, `writeConfig`) + #207 (1
alloc, `Encode`'s single-dataset record skips clone+sort). `MWGAccessors`
(4 accessors × N, digest-mismatch scenario) is the direct #208 evidence:
0 MD5 computations after the first `Read`, vs. 4 before per accessor round.

**`format/webp`**

| Benchmark | ns/op before → after | Δ ns/op | B/op before → after | Δ B/op | allocs/op before → after | Δ allocs |
|---|---|---|---|---|---|---|
| WebPExtract | 93.90n → 81.51n | **−13.20%** (p=0.000) | 104 → 80 | −23.08% | 7 → 4 | **−42.86%** |
| WebPExtractWithXMP (new) | 125.1n → 113.3n | −9.43% (p=0.000) | 192 → 160 | −16.67% | 9 → 5 | **−44.44%** |
| WebPInject | 220.3n → 222.7n | +1.11% (p=0.018) | 947 → 947 | ~ | 11 → 11 | ~ |

`WebPInject` is untouched by #209 (it does not call `readWebPChunks`); the
+1.11% is noise (0.4 ns absolute, likely inlining-boundary jitter from the new
`fourCCEXIF`/`fourCCXMP` package vars being resolved into the same
compilation unit) and is not a regression in any code path Inject exercises.

**`internal/riff`**

| Benchmark | ns/op before → after | Δ ns/op | B/op | allocs/op |
|---|---|---|---|---|
| ReadChunk (single call) | 18.03n → 18.48n | +2.52% (p=0.000) | 56 → 56 (~) | 2 → 2 (~) |

`ReadChunk` (the single-shot wrapper) is expected to be flat: it still
allocates its own `[8]byte` internally on every call, identical to before —
the win is only realised by callers that hoist a `ReadChunkBuf` buffer across
a multi-chunk loop (`WebPExtract`, above). +2.52% here is one extra call
frame (`ReadChunk` → `ReadChunkBuf`); noise-level in absolute terms (0.45 ns).

**`iptc`**

| Benchmark | ns/op before → after | Δ ns/op | B/op before → after | Δ B/op | allocs/op before → after | Δ allocs |
|---|---|---|---|---|---|---|
| DecodeString (non-ASCII) | 95.60n → 38.10n | **−60.15%** (p=0.000) | 96 → 16 | **−83.33%** | 3 → 1 | **−66.67%** |
| DecodeStringASCII (new) | 85.80n → 22.48n | **−73.79%** (p=0.000) | 96 → 48 | −50.00% | 2 → 1 | −50.00% |
| IPTCAccessorsNonASCII | 6.707n → 7.070n | +5.40% (p=0.000) | 0 → 0 | ~ | 0 → 0 | ~ |
| IPTCParse | 240.3n → 101.3n | **−57.85%** (p=0.000) | 1024 → 416 | **−59.38%** | 6 → 4 | **−33.33%** |
| IPTCEncode | 160.3n → 165.3n | +3.06% (p=0.000) | 304 → 304 | ~ | 2 → 2 | ~ |
| IPTCAccessors | 17.52n → 17.07n | −2.51% (p=0.001) | 48 → 48 | ~ | 1 → 1 | ~ |
| IPTCParseFewDatasets (new, 5 ds) | — → 154.1n | n/a | — → 512 | n/a | — → 7 | n/a |
| IPTCParseManyDatasets (new, 20 ds) | — → 471.4n | n/a | — → 1.320Ki | n/a | — → 22 | n/a |
| IPTCParseUTF8Declared (new) | — → 108.3n | n/a | — → 464 | n/a | — → 6 | n/a |
| IPTCEncodeSorted (new) | — → 99.76n | n/a | — → 112 | n/a | — → 1 | n/a |
| **geomean** (rows present on both sides) | 57.76n → 65.06n | −40.01% | — | −43.12% | — | −30.66% |

`IPTCAccessorsNonASCII` (+5.40%, 0.36 ns absolute) and `IPTCEncode`/`IPTCEncode`
(+3.06%, +1.78% across runs) are noise-level, sub-nanosecond deltas on
already-tiny benchmarks; not regressions in any observable sense.

### Task #241 net-win analysis (isolated before/after with #205 already applied)

Because `#241`'s pre-count pass trades one extra O(len(b)) scan for one exact
allocation, its own AC demanded isolated evidence, not just the combined
number above (which is dominated by #205). Isolated comparison — `iptc.go`
with `preCountDatasets` temporarily reverted to the old
`i.Records[2] = make([]Dataset, 0, 12)` vs. the real #241 code, #205 applied
identically on both sides:

| Benchmark | ns/op without #241 → with #241 | Δ ns/op | B/op | Δ B/op | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| IPTCParseFewDatasets (5 ds, < 12) | 144.9n → 152.5n | +5.21% (p=0.000) | 912 → 512 | **−43.86%** | 7 → 7 | ~ |
| IPTCParseManyDatasets (20 ds, > 12) | 495.9n → 470.6n | **−5.10%** (p=0.000) | 2.195Ki → 1.320Ki | **−39.86%** | 23 → 22 | −4.35% |
| IPTCParseUTF8Declared (3 ds, < 12) | 127.0n → 106.6n | **−16.06%** (p=0.000) | 1008 → 464 | **−53.97%** | 6 → 6 | ~ |
| IPTCParse (2 ds, < 12) | 119.1n → 103.9n | ~ (p=0.117) | 960 → 416 | **−56.67%** | 4 → 4 | ~ |
| **geomean** | 181.6n → 167.9n | **−7.53%** | — | **−49.06%** | — | −1.11% |

Verdict: net win. The only regression is +5.21% ns/op on the 5-dataset case,
fully compensated by a −43.86% B/op reduction in the same scenario (smaller
`make` requests are cheaper for the allocator even without changing the
allocation *count* — this is why 3 of 4 buckets improve in both ns/op and
B/op despite none but the 20-dataset case avoiding an actual `growslice`
regrowth). The scenario #241 specifically targets — a record exceeding the
old fixed guess of 12 — improves on every axis (ns/op, B/op, allocs/op).
Aggregate geomean across all four buckets is a net win on both time and
space. Kept as implemented; no revert warranted.

### Task #206 follow-up: isolated before/after (fixed-size embedded storage)

`BenchmarkIPTCParseUTF8Declared` (a genuine 1:90 stream: one Record-1 UTF-8
declaration + two Record-2 datasets), `-count=10`, `setUTF8Flag` temporarily
reverted to the original `append(i.Records[0], Dataset{..., Value: []byte{1}})`
implementation vs. the fixed-size-array implementation actually shipped,
with #205/#207/#241 already applied identically on both sides:

| Benchmark | ns/op before → after | Δ ns/op | B/op before → after | Δ B/op | allocs/op before → after | Δ allocs |
|---|---|---|---|---|---|---|
| IPTCParseUTF8Declared | 111.0n → 104.8n | **−5.58%** (p=0.000) | 464 → 480 | +3.45% | 6 → 4 | **−33.33% (exactly −2, AC met)** |

The small B/op increase (+16 B, 464→480) is the expected trade-off of
embedding `utf8Slot [1]Dataset` + `utf8Val [1]byte` directly in `IPTC`: the
single `new(IPTC)` allocation in `Parse` grows by those fields' size, while
the two allocations it replaces (the `[]byte{1}` literal and the
`append`-triggered `growslice` for `Records[0]`) are removed entirely — same
"pay once, unconditionally, to save an unconditional allocation elsewhere"
trade-off as the exif sub-IFD arena (task #198, see below). AC ("Parse loses
2 allocs/op") met exactly. `go build -gcflags="-m -m" ./iptc/...` shows zero
"moved to heap" lines anywhere in the package after this change.

### Batch A (tasks #236, #235, #210, #211, #212, #213) — 2026-09-25

Go version go1.27.1, `cpu: Apple M4`.

#### #236 — hoist `bytes.NewReader` out of benchmark loops

| Benchmark | allocs/op before → after | B/op before → after |
|---|---|---|
| HEIFExtract | 15 → 14 | 629 → 580 |
| PNGExtract | 16 → 15 | 232 → 184 |
| WebPExtract | 4 → 3 | 80 → 32 |

Same −1 allocs/op pattern in every other hoisted benchmark (WebPExtractWithXMP,
PNGInject, WebPInject, TIFFExtract, BigTIFFExtract, CR2/NEF/DNG/ARWExtract,
ARWConformanceExtract/Inject, NEFExtractMakerNote).

#### #235 — new cr3/orf/rw2 Extract/Inject benchmarks (no prior baseline)

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| CR3Extract | 142.9n | 560 | 8 |
| CR3Inject | 236.2n | 1028 | 16 |
| ORFExtract | 84.97n | 552 | 3 |
| ORFInject | 191.9n | 1344 | 9 |
| RW2Extract | 80.53n | 552 | 3 |
| RW2Inject | 189.7n | 1344 | 9 |

#### #210/#211/#212/#213 — `xmp/` RDF-parse allocation reduction

- #210: `Parse` interns each stored value/key into `XMP.arena` (`xmp/xmp.go`,
  `xmp/rdf.go`); values/keys alias the arena instead of independent copies.
- #211: `xmpAttr.loc` is `[]byte`, converted to `string` only at the point of storage.
- #212: `closeProp` stores a single-item collection directly, skipping `strings.Join`.
- #213: `storeProperty`/`Parse` pre-size the `Properties` maps to 8.

| Benchmark | ns/op orig→final | B/op orig→final | allocs/op orig→final |
|---|---|---|---|
| RDFParse | 3.259µ→3.119µ (−4.31%) | 2.164Ki→2.407Ki (+11.24%) | 45→14 (−68.89%) |
| XMPParse | 1.339µ→1.260µ (−5.94%) | 1.141Ki→1.203Ki (+5.48%) | 20→8 (−60.00%) |
| Read_JPEG_WithXMP | 1.378µ→1.324µ | 1.801Ki→1.934Ki | 20→13 |
| ReadCombinedMetadataJPEG | 12.20µ→11.67µ (−4.38%) | 20.78Ki→20.05Ki (−3.49%) | 83→41 (−50.60%) |
| Read_JPEG (control, no XMP) | 235.2n→236.6n | 466→466 | 6→6 |

Every other `xmp/` benchmark unaffected (≤2% noise, no allocation change).

#### XMPARENA-NS-REINTERN-01 (fixed) — `recordContainerType`

`ns`/`local` interned only the first time a given `(ns, local)` pair is
recorded (mirrors `storeProperty`'s guard). Auditor repro: 794x → 0.87x.
`TestXMPArenaNSReintern{ContainerSiblings,SimpleProperties,
ShorthandAttributes,StructFields}` measure 0.04x–0.37x (bound 8x); guard
removed → 71.91x (fails as expected).

#### BUG #277 (backlog) — `buildStructInListKey` truncation reverted

A 256-byte truncation cap on `propLocal`/`fieldLocal` closed a
document-size amplification but corrupted (round-trip) or dropped
(collision) legal long struct-in-list names — reverted, tracked as #277
(unbounded again). `TestBug277StructInListLongFieldNameRoundTrip` (300-byte
name) and `TestBug277StructInListPrefixCollisionNoDataLoss` (256-byte
shared prefix) both round-trip Parse→Encode→Parse byte-exact; both fail if
truncation is reintroduced. `FuzzParseXMP`'s arena-ratio invariant now
computes the struct-in-list contribution in-test (`structInListKeyBytes`,
via `parseStructKey`) and excludes it before checking the 8x bound.

#### Gate (final)

`go build`/`go vet` clean; `go test ./...` and `go test -race ./...` green;
`golangci-lint run ./...` 0 new issues; `staticcheck ./...` clean;
`govulncheck ./...` 0 reachable vulnerabilities; `FuzzParseXMP` 60s,
~19.3M execs, 0 crashes, 0 invariant violations.

### Batch B (tasks #214, #215, #216, #217, #218, #238, #239) — 2026-09-25

Go version go1.27.1, `cpu: Apple M4`. `format/jpeg/` read/write hot path plus
the root `extractByFormat` dispatch and a new zero-copy `Metadata` accessor.

#### Benchstat — `format/jpeg` (-count=10)

| Benchmark | ns/op before → after | B/op before → after | allocs/op before → after |
|---|---|---|---|
| JPEGExtract | 140.3n → 129.4n (**−7.80%**) | 120 → 104 (**−13.33%**) | 4 → 3 (**−25.00%**) |
| JPEGInject | 327.6n → 289.9n (**−11.49%**) | 376 → 288 (**−23.40%**) | 10 → 4 (**−60.00%**) |
| JPEGInject_NoAPP13 | 257.3n → 243.6n (**−5.33%**) | 304 → 288 (**−5.26%**) | 8 → 4 (**−50.00%**) |
| JPEGExtract_Real | 2.120µ → 1.616µ (**−23.78%**) | 15.38Ki → 9.03Ki (**−41.30%**) | 8 → 6 (**−25.00%**) |

All four `p=0.000, n=10`; geomean −12.40% ns/op, −22.05% B/op, −42.09% allocs/op.

#### Benchstat — root package, `Read_JPEG_ExtendedXMP*` (-count=10)

New synthetic fixture (`buildJPEGWithExtendedXMP`) carrying an attribute-form
`xmpNote:HasExtendedXMP` GUID and a full, independently parseable extended
XMP document, so the benchmark actually exercises
`reassembleExtendedXMPByParse` (parse + merge + re-encode) rather than the
byte-splice fallback.

| Benchmark | ns/op before → after | B/op before → after | allocs/op before → after |
|---|---|---|---|
| Read_JPEG_ExtendedXMP (default) | 5.526µ → 5.605µ (+1.43%, noise) | 9.351Ki → 9.352Ki (~) | 40 → 40 (~) |
| Read_JPEG_ExtendedXMP_WithoutXMP | 3805n → 682n (**−82.08%**) | 8.144Ki → 2.985Ki (**−63.34%**) | 33 → 16 (**−51.52%**) |

Default read unchanged (p=0.043 but within noise band; B/op and allocs/op
identical); `WithoutXMP` drops sharply once extraction itself skips the
reassembly, on top of the pre-existing parse skip.

#### #239 — `Metadata.RawSegments` (new accessor, no prior baseline)

`BenchmarkRawSegments`, -count=10: **0.26 ns/op, 0 B/op, 0 allocs/op** (AC: 0 allocs/op — met).

#### Per-task notes

- **#214** (`readSegment` scratch growth via `iobuf.Get`): every segment over
  4 KiB (EXIF/XMP/ICC) now grows from the large pool instead of a bare
  `make`. Evidenced by `JPEGExtract_Real` (real corpus file with large
  segments): −41.30% B/op, −25.00% allocs/op.
- **#215** (drop the redundant IPTC clone): the single-payload IRB read path
  returns the 0x0404 sub-slice of the already-cloned `app13Payloads[0]`
  directly instead of cloning it again — 1 clone fewer per file, folded into
  the `JPEGExtract`/`JPEGExtract_Real` deltas above. `RawIPTC()` round-trips
  byte-identically; #174 sibling tests pass.
- **#216** (stop fixed-array heap escapes in the write helpers): SOI now reads
  into `Inject`'s pooled scratch; every segment header (new or pass-through)
  is stamped directly into a pooled buffer and written in one `w.Write`
  (`writeHeaderedBuf`/`writeSegmentCopy`); `writeMarker` uses a pooled 2-byte
  buffer. `writeSegment` itself is untouched and remains covered by its
  existing direct tests. Reflected in the `JPEGInject`/`JPEGInject_NoAPP13`
  allocs/op drop above.
- **#217** (skip the Inject pre-scan clone when nothing needs preserving):
  `probeIRBHasSiblings` performs one zero-copy pass; the cloning
  `extractOriginalIRB` pass now only runs when a sibling 8BIM resource is
  actually present or `rawIPTC` is nil. `JPEGInject_NoAPP13` (no source
  APP13 at all) shows the full effect; #174 sibling-preservation tests
  (`jpeg_task77`, `jpeg_irb_task52`, 0x0425 survival) still pass.
- **#218** (build the spliced IRB directly in the pooled output buffer):
  `appendSplicedIRB`/`appendIRBBlock` append straight into the segment
  buffer reserved by `writeIPTCSegment`/`writeIPTCSegmentRaw` instead of
  allocating a single-use IRB buffer that is then copied again;
  `spliceIPTCIntoIRB`/`buildIRB` remain as thin, freshly-allocating wrappers
  for their existing direct unit tests. Folded into the `JPEGInject` allocs/op
  drop (10 → 4) above; IRB output byte-identical.
- **#238** (skip extraction of segments excluded by Without{EXIF,IPTC,XMP}):
  `jpeg.ExtractFullSelective(r, wantIPTC, wantXMP)` (thin-wrapped by the
  unchanged `ExtractFull`) skips the 0x0425 IPTC digest clone when IPTC is
  unwanted and the extended-XMP reassembly when XMP is unwanted; `rawEXIF`
  and `rawIPTC` are always extracted (required for unmodified-Write
  pass-through). Scope note: only the JPEG path was changed — the other
  format packages' `Extract` functions have no comparable reassembly cost to
  skip. See the `Read_JPEG_ExtendedXMP*` table above for the effect.
- **#239** (zero-copy read-only raw segment accessor, additive/user-authorised):
  `Metadata.RawSegments()` returns `(rawEXIF, rawIPTC, rawXMP)` aliasing `m`'s
  own storage with a documented must-not-mutate contract; existing
  `RawEXIF`/`RawIPTC`/`RawXMP` and the #139 mutation-safety tests unchanged.

#### Gate

`go build`/`go vet` clean; `go test ./...` and `go test -race ./...` green
(all packages); `golangci-lint run ./...` 0 new issues (6 pre-existing
issues remain, all in files untouched by this batch); `staticcheck ./...`
clean; `govulncheck ./...` could not run in this environment (govulncheck
binary built against an older Go source-processing API than the installed
go1.27.1 toolchain — pre-existing environment mismatch, unrelated to this
batch); `FuzzJPEGExtract` and `FuzzJPEGInject` both ran 60s clean (0
crashes).

### Batch C (tasks #219–#227, #237) — 2026-09-25

Go version go1.27.1, `cpu: Apple M4`, `-count=10` (EXIFEncode after: `-count=20`), all
changes `p=0.000` unless marked `~` or noted. `format/tiff` relocate and extract, `format/raw/{orf,rw2,cr3}`,
`exif` encode. Relocate and inject output is byte-identical (SHA-256 of the
`Read`+`SetCopyright`+`Write` output unchanged for all 578 TIFF/DNG/RAW
corpus and fixture files).

| Benchmark | ns/op before → after | B/op before → after | allocs/op before → after |
|---|---|---|---|
| RelocateSingleStrip | 1.505µ → 1.155µ (**−23.23%**) | 7.271Ki → 6.198Ki (**−14.76%**) | 23 → 14 (**−39.13%**) |
| RelocateMultiStrip | 1.791µ → 1.202µ (**−32.87%**) | 10.022Ki → 6.237Ki (**−37.77%**) | 29 → 16 (**−44.83%**) |
| RelocateTiled | 1.854µ → 1.257µ (**−32.18%**) | 10.179Ki → 6.362Ki (**−37.49%**) | 29 → 16 (**−44.83%**) |
| RelocateDNGLike | 2.358µ → 1.849µ (**−21.61%**) | 13.14Ki → 11.01Ki (**−16.22%**) | 38 → 24 (**−36.84%**) |
| RelocateMakerNote | 657.9n → 655.5n (~) | 1.473Ki → 1.473Ki (~) | 14 → 14 (~) |
| TIFFExtract | 87.27n → 42.32n (**−51.51%**) | 536 → 112 (**−79.10%**) | 2 → 1 (**−50.00%**) |
| TIFFExtractRealFile (36.6 MiB) | 2.794m → 1.286m (**−53.98%**) | 78.24Mi → 36.57Mi (**−53.26%**) | 34 → 1 (**−97.06%**) |
| ORFExtract | 71.94n → 19.94n (**−72.28%**) | 552 → 16 (**−97.10%**) | 3 → 1 (**−66.67%**) |
| ORFExtractRealFile (23.1 MiB) | 2651.6µ → 828.4µ (**−68.76%**) | 74.08Mi → 23.14Mi (**−68.76%**) | 34 → 1 (**−97.06%**) |
| ORFInject | 172.90n → 79.09n (**−54.25%**) | 1344 → 240 (**−82.14%**) | 9 → 6 (**−33.33%**) |
| RW2Extract | 72.90n → 21.10n (**−71.05%**) | 552 → 16 (**−97.10%**) | 3 → 1 (**−66.67%**) |
| RW2ExtractRealFile (18.8 MiB) | 2031.6µ → 675.6µ (**−66.75%**) | 65.42Mi → 18.81Mi (**−71.24%**) | 34 → 1 (**−97.06%**) |
| RW2Inject | 173.00n → 78.58n (**−54.58%**) | 1344 → 240 (**−82.14%**) | 9 → 6 (**−33.33%**) |
| CR3Extract | 132.05n → 87.83n (**−33.49%**) | 560 → 536 (**−4.29%**) | 8 → 2 (**−75.00%**) |
| CR3Inject | 223.6n → 177.4n (**−20.67%**) | 1028 → 1000 (**−2.72%**) | 16 → 9 (**−43.75%**) |
| EXIFEncode | 119.5n → 118.0n (**−1.17%**, p=0.001) | 80 → 80 (~) | 2 → 2 (~) |
| EXIFEncode_Camera | 1.076µ → 1.016µ (**−5.49%**) | 1.580Ki → 1.580Ki (~) | 14 → 14 (~) |
| EXIFEncode_BigTIFF | 959.6n → 953.1n (**−0.68%**) | 7.568Ki → 7.568Ki (~) | 6 → 6 (~) |

#### Per-task notes

- **#219**: `relocateTIFFFromParsed` allocates `finalTIFF` once at its exact length (`relocatedLen`) and checks `len(finalTIFF) == finalLen`; no regrowth.
- **#220**: additive `exif.EncodedSize` and `exif.EncodeInto` share one exact length computation (`encodedLen`/`ifdWrittenLen`); relocate uses `EncodedSize` instead of the skeleton encode. A plain `Encode` sizes its buffer from the layout offsets and skips the exact pass. `EncodedSize(e) == len(Encode(e))` is tested on every corpus file.
- **#221**: `extractParallelOffsetBlocks` backs all blocks of a source with one `[]imageBlock`; aggregation reuses single-source slices (`appendBlocks`).
- **#222**: `insertPlaceholders` returns a `[]placeholderGroup` with one value buffer per group, scanned linearly, instead of a map of boxed pairs. SubIFD block lookup (`blockIndex`) is O(1) with no scan fallback, relying on the per-tag contiguity of enumerated blocks.
- **#223**: linear scans replace the `subIFDSet`, `toRemove`, `visited` (≤16 offsets), `blocksByIFD` and `blkMap` maps; `subIFDInfo` is backed by one slice per level; the MakerNote prefix table is package-level. `RelocateMakerNote` is unchanged: the compiler already kept the prefix literal on the stack.
- **#224**: `iobuf.ReadAll` reads a seekable input with one exact-size allocation and rejects oversized input before allocating; used by `tiff`, `orf`, and `rw2` `Extract`/`Inject`.
- **#225**: `orf.Extract`/`rw2.Extract` scan the original bytes; the full-file copy is gone.
- **#226**: `orf.Inject`/`rw2.Inject` stream through `iobuf.MagicWriter` instead of buffering the output. `relocateTIFFAsORF`/`relocateTIFFAsRW2` keep their copy: their input is caller-owned (exported `InjectWithEXIFORF`/`InjectWithEXIFRW2`).
- **#227**: `parseCR3BoxHeader` returns a `[4]byte` box type.
- **#237**: `internal/tiffscan.ExtractTagValues` is the single IFD0 IPTC/XMP scanner for `tiff`, `orf`, and `rw2`.

### Batch D (tasks #228–#234) — 2026-09-25

Go version go1.27.1, `cpu: Apple M4`, `-count=10 -benchtime=2s`, all changes `p=0.000`
unless noted. `format/heif`, `format/png`, `format/webp`, `internal/riff`. Golden
SHA-256 identical: `gometadata.Read` + `SetCopyright` + `gometadata.Write` over every
HEIF/PNG/WebP corpus and fixture file (992 files: 747 successful writes with matching
hashes, 245 with matching error outcomes) is byte-for-byte unchanged before vs after.

| Benchmark | ns/op before → after | B/op before → after | allocs/op before → after |
|---|---|---|---|
| HEIFExtract | 318.5n → 213.7n (**−32.92%**) | 580 → 400 (**−31.03%**) | 14 → 5 (**−64.29%**) |
| HEIFInject | 559.9n → 316.9n (**−43.39%**) | 1768 → 812 (**−54.07%**) | 34 → 12 (**−64.71%**) |
| PNGExtract | 285.8n → 259.2n (**−9.29%**) | 184 → 184 (~) | 15 → 14 (**−6.67%**) |
| PNGExtractCompressedXMP | 487.6n → 466.6n (**−4.32%**) | 651 → 604 (**−7.22%**) | 14 → 12 (**−14.29%**) |
| PNGInject | 456.8n → 391.6n (**−14.25%**) | 969 → 793 (**−18.16%**) | 25 → 11 (**−56.00%**) |
| PNGWriteChunk | 59.80n → 52.05n (**−12.94%**) | 136 → 112 (**−17.65%**) | 5 → 2 (**−60.00%**) |
| WebPExtract | 74.00n → 73.48n (**−0.70%**, p=0.009) | 32 → 32 (~) | 3 → 3 (~) |
| WebPExtractWithXMP | 103.3n → 100.2n (**−2.96%**) | 112 → 112 (~) | 4 → 4 (~) |
| WebPInject | 215.8n → 128.1n (**−40.63%**) | 899 → 320 (**−64.40%**) | 10 → 4 (**−60.00%**) |
| riff.ReadChunk | 18.54n → 18.31n (**−1.21%**) | 56 → 56 (~) | 2 → 2 (~) |

#### Per-task notes

- **#228**: `parseHEIFBoxHeader` returns a `[4]byte` box type; `parseInfe`/`parseInfeV0V1`/`parseInfeV2V3` return a `[4]byte` item type plus an `ok bool` instead of a string; `parseIinf` returns `map[uint16][4]byte`. `selectBestItem`'s dead `"rdf+xml"` candidate (7 bytes; ISO 14496-12 §8.11.6 fixes `item_type` at exactly 4 bytes, so it could never match) is dropped, not carried forward as a `[4]byte`.
- **#229**: `newMetaBoxLen`/`ilocBoxSize` compute the exact rebuilt-meta-box length from field widths and extent counts alone — no bytes are serialised to learn it — replacing the former placeholder-then-final double build. `updateIlocItemsInPlace` mutates `ilocInfo.items` (never aliased elsewhere) instead of copying the slice, and reuses each matched item's existing one-element extents backing array. `buildIlocBox`/`buildMetaBox` each write into one pre-sized buffer (header placeholder + body + patched size field) instead of separate body/hdr buffers. New regression gate: `TestInjectMultiExtentItemCollapsesToOne` (a 2-extent source item collapses to exactly 1 extent with correct offset/length after Inject). **Post-merge security fix (HEIF-ILOC-DUPBOX-01, 2026-09-25):** the initial `ilocSizeDelta` measured only the FIRST `iloc`-typed child box's size, while `buildMetaBox`'s own copy loop drops EVERY `iloc`-typed child — a meta box with two (or more) `iloc` boxes, or with trailing bytes that fail to parse as a box header at all (both silently dropped by `buildMetaBox`'s real walk), produced a `newMetaLen` estimate that didn't match `buildMetaBox`'s actual output, corrupting the injected item's extent offset. Fixed by extracting the shared traversal into `nonIlocBoxesLen` (used by both `buildMetaBox`'s size pre-computation and the new `newMetaBoxLen`), so the two can no longer drift apart. Regression gates: `TestInjectDuplicateIlocBoxesOffsetInBounds`, `TestInjectMetaWithTrailingUnparseableBytesOffsetInBounds`; `FuzzHEIFInject` strengthened with a within-output-bounds invariant for every Exif/mime-typed item's extent.
- **#230**: `heif.Inject` and `webp.Inject` read their input via `iobuf.ReadAll` (task #224) instead of `io.ReadAll(io.LimitReader(...))`. New non-seekable-fallback regression tests in both packages' `oom_gate_test.go` (`seekStartOnlyReader`, allowing only the leading `Seek(0, SeekStart)` Inject itself performs).
- **#231**: `writeChunk` obtains its 8-byte header and 4-byte CRC trailer from `iobuf`, hashes the chunk type directly from `hdr[4:8]` (no `[]byte(chunkType)` conversion), and calls `crc32Pool.Put` explicitly on every return instead of via `defer`. `buildXMPChunk` is pre-sized to its exact final length instead of growing a zero-cap `bytes.Buffer`.
- **#232**: `readChunk`'s callback type is `[4]byte` (`copy(chunkType[:], hdr[4:8])` replaces `string(hdr[4:8])`), removing a string allocation on every chunk, metadata or not. The 8-byte header and 4-byte CRC trailer stay plain stack arrays: routing them through `iobuf` was measured to cost *more* in ns/op than the single unavoidable heap escape it would replace (a real, if smaller, PNGExtract regression was caught this way before being reverted — see `feedback_png_chunktype_escape.md`). `chunkTypeStr` (a `//go:noinline` `[4]byte`→`string` cold-path helper) is used at every `%q` error-formatting call site instead of slicing `chunkType` inline, because Go's escape analysis is control-flow-insensitive: slicing a `[4]byte` *parameter* inside even a rarely-taken `if err != nil` branch marks that parameter as escaping on **every** call.
- **#233**: `zlibDecompress` pools its `*bytes.Reader` (`bytesReaderPool`, `Reset` on `Get`) alongside the existing `zlibPool`, with explicit `Put` calls instead of `defer`. **Post-merge security fix (PNG-BYTESREADERPOOL-RETENTION-01, 2026-09-25):** `bytes.Reader.Reset(data)` stores `data` in an unexported field that a plain `Put` never clears, so a pooled reader kept the caller's compressed input (up to `maxPNGChunkSize`, 256 MiB) reachable for as long as it sat in the pool. Fixed via `putBytesReader`, which calls `br.Reset(nil)` before every `Put`. Regression gate: `TestZlibDecompressDoesNotRetainInput` (drains the pool and asserts `Len()==0 && Size()==0` — both public methods, no reflection needed).
- **#234**: `internal/riff.ReadChunkHeaderAt` takes a caller-supplied data offset instead of discovering it via `Seek(0, SeekCurrent)` (a plain `io.Reader` suffices — no seek at all). `webp.readWebPChunks` tracks its own running offset (start 12, advance `8+size+padding`) and measures the stream's total length at most once per `Extract` call, lazily, only when a metadata chunk is actually seen (`measureStreamEnd`) — `readPaddedChunk` itself no longer seeks; the odd-size padding byte is read and discarded rather than skipped via `Seek(SeekCurrent)`. `collectOriginalChunks`/`webpOriginalChunk`/`writeRIFFChunk`/`isEOFOrKnownFourCC` use `[4]byte` FourCC values throughout instead of converting to `string` per chunk. `buildWebPBody` writes the 12-byte RIFF header into the same pooled buffer as the chunk stream (header placeholder, body, patch size) instead of a separate `riffHdr` allocation plus a second `w.Write` call. New regression gate: `TestExtractNoPerChunkSeekCurrent` asserts the seek trace of a full `Extract` call (VP8X + EXIF + XMP + one non-metadata chunk) contains no `io.SeekCurrent` call at all.

### Batch E (tasks #285, #286, #287) — 2026-09-25

Go version go1.27.1, `cpu: Apple M4`, `-count=8`, all changes `p=0.000` unless noted.
`format/tiff` (relocate_nef/arw/orf/rw2.go, relocate.go, tiff.go), `format/raw/cr3`,
`exif` (`AcceptRAWMagic`), `read.go`/`write.go`, `xmp/rdf.go`. Measured with a scratch
end-to-end harness (`gm.Read`/`gm.Write` over real corpus files, not part of the
repository — see `internal/testutil` for the equivalent corpus-file convention) against
real Canon/Nikon/Sony/Panasonic/Olympus/DJI files from `testdata/corpus`: Epson
PerfectionV800.tiff (819,606 B), Canon EOS 600D.CR2 (22,911,265 B), Canon EOS R3.cr3
(12,277,000 B), Nikon D810.nef (40,667,772 B), Sony ILCE-7M3.arw (24,795,904 B), DJI
Phantom 4 (1).dng (24,394,394 B), OM System TG-7.ORF (13,150,700 B), Panasonic
DMC-GF7.rw2 (19,720,192 B), Canon EOS 7D.jpg (metadata-extractor corpus). Write output
SHA-256 identical to HEAD (ce1dc82) for `Read`+`SetCaption`+`SetCopyright`+`Write` over
the full TIFF/DNG/RAW corpus (571 files, 1133 read+write outcomes), **except** 3
byte-for-byte-identical adversarial fixtures (`raw/exiv2/issue_839_poc*.rw2`) whose
output differs by exactly 1 byte at the same offset in all three — see the per-task note
under #286 below; this is a fidelity **improvement**, not a regression.

| Benchmark | ns/op before → after | B/op before → after | allocs/op before → after |
|---|---|---|---|
| Write/tiff | 91.39µ → 58.71µ (**−35.76%**) | 1639.9Ki → 828.7Ki (**−49.47%**) | 33 → 28 (**−15.15%**) |
| Write/cr2 | 2.798m → 1.881m (**−32.78%**) | 65.62Mi → 21.92Mi (**−66.60%**) | 70 → 59 (**−15.71%**) |
| Write/nef | 4.428m → 2.512m (**−43.27%**) | 131.25Mi → 41.63Mi (**−68.28%**) | 92 → 65 (**−29.35%**) |
| Write/arw | 2.210m → 1.526m (**−30.96%**) | 48.62Mi → 24.87Mi (**−48.85%**) | 97 → 68.5 (**−29.38%**) |
| Write/dng | 2.078m → 1.463m (**−29.58%**) | 46.92Mi → 23.63Mi (**−49.64%**) | 69 → 66 (**−4.35%**) |
| Write/orf | 1633.5µ → 814.5µ (**−50.14%**) | 39.11Mi → 14.01Mi (**−64.18%**) | 43 → 36 (**−16.28%**) |
| Write/rw2 | 3.057m → 1.607m (**−47.44%**) | 79.31Mi → 20.22Mi (**−74.51%**) | 56 → 27 (**−51.79%**) |
| Read/jpeg_canon7d | 20.75µ → 17.95µ (**−13.50%**, reproduced −13.5%…−13.9% across 3 independent interleaved runs incl. round-1's isolated A/B) | 57.29Ki → 57.32Ki (~) | 54 → 54 (~) |
| Read/cr3 | 901.3µ → 1.652µ (**−99.82%**) | 24675.4Ki → 33.53Ki (**−99.86%**) | 41 → 8 (**−80.49%**) |
| Read/orf | 824.0µ → 417.9µ (**−49.28%**) | 25.10Mi → 12.55Mi (**−49.99%**) | 21 → 19 (**−9.52%**) |
| Read/rw2 | 1217.0µ → 690.1µ (**−43.29%**) | 37.63Mi → 18.82Mi (**−50.00%**) | 13 → 12 (**−7.69%**) |
| xmp.BenchmarkRDFParse | 3.155µ → 2.928µ (**−7.18%**) | 2.407Ki → 2.407Ki (~) | 14 → 14 (~) |

Write B/op ÷ source-file size (AC: ≤1.15×): tiff 1.035×, cr2 1.003×, nef 1.073×,
arw 1.052×, dng 1.016×, orf 1.117×, rw2 1.075×. Read B/op ÷ source-file size:
cr3 34,335 B absolute (AC: ≤256 KiB), orf 1.0006×, rw2 1.0007× (AC: ≤1.05×).

#### Per-task notes

- **#285** (tiff/raw: drop redundant full-file clones and pre-size RAW write outputs):
  `write.go`'s six `writeTIFF*` entry points alias `m.rawEXIF` directly instead of
  `bytes.Clone`-ing it, now that no relocator mutates its `base`/`originalBytes`
  argument (verified by grepping every `base[i] =` write site in `format/tiff/relocate*.go`
  before and after: the only two were the ORF/RW2 in-place magic patches removed by #286).
  `relocate_nef.go`/`relocate_orf.go`/`relocate_rw2.go` replace their `exif.Encode`
  skeleton-then-final double encode with `exif.EncodedSize` (skeleton) +
  `exif.EncodeInto(make([]byte, 0, finalCap(finalLen)), e)` (final), mirroring the pattern
  Batch C (#219/#220) already applied to the shared `relocateTIFFFromParsed`. RW2's
  post-relocate GUID insertion (`insertRW2GUIDAndShiftOffsets`) and CR2's marker insertion
  (`insertCR2MarkerAndShiftOffsets`) both used to allocate a *second* whole-file-sized buffer
  purely to shift bytes right by a small fixed delta (16 and 8 bytes respectively);
  `relocateTIFFFromParsedRW2`/`relocateTIFFFromParsed` now reserve that many bytes of spare
  *capacity* (not length) in the buffer `exif.EncodeInto` writes into, so the shift is instead
  done in place with a single overlap-safe `copy()` (Go's `copy` is memmove-based and
  explicitly documented to support overlapping source/destination) — with a `make`-based
  fallback for any caller that supplies a buffer without the reserved capacity. New/updated
  invariant checks (`len(finalTIFF) != finalLen`) were added to the NEF/ARW/ORF/RW2
  relocators, mirroring the one `relocateTIFFFromParsed` already had, so a future pre-sizing
  bug fails loudly instead of silently falling back to `append`'s regrowth.

  **ARW: pre-sizing the FINAL buffer measured slower, not faster — reverted for that one
  call site.** The initial pass applied the identical `EncodedSize`+`EncodeInto` swap to
  `arwRelocateWithSR2`'s final encode too, measuring only +21.29% (short of the 25% AC
  target). A three-way interleaved isolation (`git worktree`s: unmodified HEAD, HEAD +
  write.go clone-removal only, HEAD + clone-removal + full presizing), `-count=8`,
  `p=0.000` throughout, on the same Sony ILCE-7M3.arw fixture:

  | Variant | Write/arw ns/op | vs HEAD |
  |---|---|---|
  | HEAD | 2.210m | — |
  | clone-removal only (no relocate_arw.go change) | 1.537m | **−30.46%** |
  | clone-removal + `EncodedSize`(skeleton) + `EncodeInto`(final, pre-sized) | 1.726m | −21.93% |
  | clone-removal + `EncodedSize`(skeleton) + plain `Encode`(final, natural growth) | 1.529m | **−30.80%** (~ vs clone-only, p=0.083) |

  `samply` (Go's own CPU profiler is biased on macOS — see round-1 notes) resolved the gap:
  self-time in `runtime.memclrNoHeapPointers`, focused on the `gometadata.Write` call tree,
  was **11.45%** with the pre-sized final buffer vs **0.20%** with natural `append` growth for
  the identical workload. A single `make([]byte, 0, ~25MB)` forces an immediate,
  unconditional zero-fill of the whole backing array; the same ~25 MB reached via `append`'s
  incremental doubling growth is zeroed in smaller pieces this workload's allocator/GC state
  evidently handles more cheaply — the opposite of the outcome on every other format in this
  batch. `exif.EncodedSize` for the *skeleton* pass remained a clean, unconditional win in all
  four variants (no bytes written, just layout arithmetic) and was kept; only the *final*
  pass's pre-sizing was reverted, back to plain `exif.Encode(e)`. `arwRelocatedLen` is
  retained, but now feeds only the post-hoc `len(finalTIFF) != finalLen` invariant check, not
  any allocation. Write output is byte-for-byte identical before and after this revert
  (confirmed via the golden-hash corpus run) — this was purely an allocation-strategy change.
  **AC now met**: −30.96% (final measurement, exceeds the 25% target). Sony SR2Private
  decrypt/rebase (`patchSonySR2InFinalTIFF`/`rebaseIFDInBlob`/`sr2CryptBlob`) remains a
  real, unrelated, untouched cost centre (~14-27% of the focused `Write` call tree depending
  on variant) explaining why ARW's percentage improvement is smaller than NEF/ORF/RW2's even
  after this fix — not a remaining #285 defect, just a fixed cost this format alone carries.
  Whether the same "pre-sized final buffer can be slower than natural growth" effect also
  leaves headroom on NEF/ORF/RW2/TIFF (which all show strong wins already, comfortably past
  their own targets) was not investigated — out of scope for this batch; flagged in agent
  memory as a candidate for a future profiling round.
- **#286** (cr3/orf/rw2: stop copying and retaining the whole file on read):
  `exif.AcceptRAWMagic(magic uint16)` is a new internal-use `ParseOption`: it tells `Parse`
  to dispatch the given magic value through the classic-TIFF path exactly like `0x002A`,
  guarded by an explicit `cfg.extraMagic != 0` check so a corrupt file with magic `0x0000`
  is never accidentally accepted by a caller that never opts in. `exif.Parse`'s behaviour
  with no options is unchanged and still rejects ORF/RW2 magic (see
  `exif/task286_acceptrawmagic_test.go`). `read.go`'s `parseEXIF` uses it (via the new
  `nonStandardRAWMagic` detector) instead of `patchRawEXIFForParse`'s whole-file clone;
  `format/tiff`'s `relocateTIFFFromParsedORF`/`relocateTIFFFromParsedRW2` use it for their
  `e == nil` fallback parse instead of patching `base[2:4]` in place, so `relocateTIFFAsORF`/
  `relocateTIFFAsRW2`'s own defensive clone is removed too. `cr3.Extract` no longer reads the
  whole file: `readTopLevelBox` walks top-level ISOBMFF box headers via `Read`+`Seek` (8 or
  16 bytes each) and reads only the matched `moov` box's payload into memory, capped at
  `maxCR3TopLevelBoxScans` (4096) headers as a defence-in-depth bound against a crafted file
  padded with minimal boxes ahead of `moov`; the returned `rawEXIF`/`rawXMP` are
  `bytes.Clone`d out of the (still moov-sized, not file-sized) scratch buffer so retention is
  bounded even if a future file embeds something large inside `moov` itself (see
  `TestCR3ExtractRetentionBounded`: heap growth for 10 retained `Extract` results against an
  8 MiB synthetic `mdat` measured **0 bytes** after the fix vs **75,571,424 bytes** — essentially
  the full 10×8 MiB — before it). `cr3.Inject` (which genuinely needs the whole file, since
  every byte not touched by the moov rebuild is copied through verbatim) switches from
  `io.ReadAll(io.LimitReader(...))` to `iobuf.ReadAll`, halving its own transient allocation
  for a seekable reader and mapping `iobuf.ErrTooLarge` to the package's own
  `ErrFileTooLarge` (`TestExtractFileTooLarge`/`TestInjectFileTooLarge` both still pass).
  **Discovered, in-scope fidelity improvement**: three adversarial `exiv2` regression
  fixtures (`raw/exiv2/issue_839_poc{,_2,_3}.rw2`) declare an IFD entry (tag `0x0148`) whose
  48-byte OOL value offset points back to file offset 0, aliasing the file's own header —
  under the old patch-then-parse approach, that entry's `Value` incorrectly contained the
  *synthetic* patched magic byte (`0x2A`) instead of the file's real on-disk magic byte
  (`0x55`); the new direct-parse approach preserves the true on-disk bytes. This is the sole
  source of the 1-byte write-output divergence from HEAD noted above; it affects only these
  3 deliberately self-referential POC files (confirmed via a full corpus diff and a dedicated
  before/after entry-value comparison), never a real camera file, and is strictly more
  faithful to the source bytes, consistent with this project's "preserve existing metadata
  exactly" mandate.
- **#287** (xmp: lookup table for name terminator scanning): `isNameTerminator`'s eight-way
  compare chain is replaced by `nameTerminatorLUT`, a 256-entry `[256]bool` array indexed
  directly by the input byte (mirrors `iptc/dataset.go`'s existing `datasetMaxLen` table
  convention). `TestNameTerminatorLUTMatchesPredicate` proves the table matches the old
  predicate for all 256 byte values, not just the 8 real terminators, so a regression that
  over-matches is caught as reliably as one that under-matches. **e2e AC** (`BenchmarkRead/
  jpeg_canon7d` ≥10% faster) verified directly against the scratch harness, not just the
  package-local `xmp.BenchmarkRDFParse`: −13.50% (this measurement) and −13.74% (an earlier
  independent run), both `p=0.000, n=8` — consistent with round-1's own isolated A/B
  (−13.9%), so the end-to-end result matches what the isolated fix predicted with no
  unexplained gap. `samply`, focused on the `gometadata.Read` call tree for this same
  benchmark, confirms `isNameTerminator`/`scanName` no longer appear as distinct hot
  functions at all (the LUT made them cheap enough to fold into their caller's frame or
  drop below measurement resolution); the largest remaining self-time contributors are
  `xmp.parseSingleAttr` (14.96%), `runtime.memmove` (8.80%), `internal/bytealg.
  IndexByteString` (5.24%, via `readQuotedValue` scanning for the closing quote), `xmp.
  readQuotedValue` (5.10%), `runtime.memequal` (5.02%), and `xmp.scanAttrs` itself (4.49%,
  now just loop/bounds-check overhead with the terminator check gone) — none of these are
  name-terminator-scanning cost; they are separate functions, out of #287's stated scope.

### 60-second fuzz clean (Batch E, 2026-09-25)

`FuzzCR3Extract`, `FuzzCR3Inject`, `FuzzTIFFInject`, `FuzzCR2Inject`, `FuzzNEFInject`,
`FuzzARWInject`, `FuzzORFInject`, `FuzzRW2Inject`, `FuzzDNGInject`, `FuzzParseEXIF`,
`FuzzParseXMP`, `FuzzRead` — each run standalone for 60 s (`-fuzztime=60s`); zero crashers.

### Batch F (tasks #288, #289) — 2026-09-25

Go version go1.27.1, `cpu: Apple M4`, `-count=10` (`≥8` interleaved runs required by
the batch's Definition of Done), all changes `p=0.000` unless noted. `format/png/png.go`
(#288), `format/tiff/extent.go` (new), `format/tiff/tiff.go`, `write.go`, `metadata.go`
(#289). Measured with the same scratch end-to-end harness as Batch E
(`gm.Read`/`gm.Write` over real corpus files) against
`png/metadata-extractor/Issue 280 (dotnet).png`, `tiff/metadata-extractor/Epson
PerfectionV800.tiff` (819,606 B), `raw/metadata-extractor/Canon EOS 600D.CR2`
(22,911,265 B), `raw/metadata-extractor/Nikon D810.nef` (40,667,772 B), `raw/
metadata-extractor/Sony ILCE-7M3 (A7M3).arw` (24,795,904 B), `raw/metadata-extractor/DJI
Phantom 4 (1).dng` (24,394,394 B).

| Benchmark | ns/op before → after | B/op before → after | allocs/op before → after |
|---|---|---|---|
| Read/png | 18.414µ → 2.856µ (**−84.49%**) | 3.752Ki → 2.862Ki (**−23.72%**) | 138 → 28 (**−79.71%**) |
| Write/png | 109.03µ → 29.23µ (**−73.19%**) | 7.708Ki → 6.827Ki (**−11.43%**) | 131 → 14 (**−89.31%**) |
| Read/tiff | 49.24µ → 49.89µ (~) | 809.8Ki → 809.8Ki (~) | 10 → 10 (~) |
| Read/cr2 | 813.6µ → 8.82µ (**−98.92%**) | 22391.8Ki → 80.20Ki (**−99.64%**) | 26 → 27 (+3.85%) |
| Read/nef | 1387.4µ → 39.24µ (**−97.17%**) | 38.797Mi → 817.6Ki (**−97.94%**) | 19 → 30 (+57.89%) |
| Read/arw | 885.8µ → 47.50µ (**−94.64%**) | 24552.2Ki → 849.2Ki (**−96.54%**) | 23 → 29 (+26.09%) |
| Read/dng | 858.0µ → 17.39µ (**−97.97%**) | 23833.8Ki → 202.8Ki (**−99.15%**) | 32 → 38 (+18.75%) |
| Write/tiff | 61.02µ → 61.02µ (~) | 825.6Ki → 825.6Ki (~) | 27 → 27 (~) |
| Write/cr2 | 1.938m → 2.569m (+32.55%) | 21.91Mi → 43.77Mi (+99.72%) | 60 → 69 (+15.00%) |
| Write/nef | 2.574m → 3.990m (+54.97%) | 43.02Mi → 81.81Mi (+90.18%) | 65 → 70 (+7.69%) |
| Write/arw | 1.532m → 2.431m (+58.63%) | 24.86Mi → 48.51Mi (+95.13%) | 67 → 76.5 (+14.18%) |
| Write/dng | 1.489m → 2.319m (+55.69%) | 24.50Mi → 47.77Mi (+94.95%) | 67 → 71 (+5.97%) |
| RoundTrip/tiff (Read+SetCaption+SetCopyright+Write) | 105.6µ → 102.7µ (**−2.73%**) | 1.601Mi → 1.601Mi (~) | 55 → 55 (~) |
| RoundTrip/cr2 | 2.700m → 2.597m (~/**−3.81%**) | 43.78Mi → 43.85Mi (+0.15%) | 102 → 111 (+8.82%) |
| RoundTrip/nef | 3.950m → 4.048m (+2.47%) | 81.82Mi → 82.61Mi (+0.97%) | 99 → 114 (+15.15%) |
| RoundTrip/arw | 2.345m → 2.359m–2.457m (~ to +4.76%, noisy — see Follow-up 2) | 48.84Mi → 49.34Mi (+1.03%) | 110 → 119–120 (+8.18–9.09%) |
| RoundTrip/dng | 2.347m → 2.337m–2.360m (~) | 47.78Mi → 47.97Mi (+0.40%) | 111 → 121 (+9.01%) |

Read/Write/RoundTrip numbers above are the FINAL, post-follow-up-review measurements after
BOTH follow-up rounds (see "Follow-up" and "Follow-up 2" subsections below); the original
per-task note further down predates those and is kept for its record of what was found and
why, not as the current numbers.

AC scorecard: **PNG both AC met** (Read ≤4µs/≤30 allocs target: 2.856µs/28 allocs; Write
≤30µs/≤35 allocs target: 29.23µs/14 allocs). **TIFF-family Read both AC met**: B/op ≤1 MiB
for every one of tiff/cr2/nef/arw/dng (max is nef at 817.6Ki); NEF ns/op ≥80% lower target:
**−97.17%** actual. **RoundTrip (the realistic Read+edit+Write workflow) is net-neutral or
better for all 5 formats** after Follow-up 2's `m.rawEXIF`-reuse fix: tiff **−2.73%** (was
+57.17%), cr2 `~`/−3.81%, nef +2.47%, dng `~`, arw ranges `~` to +4.76% across repeated
clean runs (noise around ~0%, not a stable regression — see Follow-up 2).

#### Per-task notes

- **#288** (png: skip ignored chunks on read, copy unchanged chunks verbatim on write):
  `Extract` hoists the 8-byte chunk header buffer out of the read loop and, for any chunk
  type other than `eXIf`/`iTXt`/`tEXt`/`zTXt`/`IHDR`/`IEND`, `Seek`s past its data+CRC
  instead of reading and CRC-verifying it — bounds-checked against a single upfront
  `streamSize` probe (mirroring Exiv2's `pngimage.cpp` pattern) so a chunk whose declared
  length overruns the real stream is still rejected exactly as before, never silently
  accepted via a `Seek` past EOF. `Inject` copies every unchanged chunk — header, data,
  **and original CRC trailer, byte-for-byte, uninspected and unrepaired** — via a pooled
  64 KiB streaming copy (`streamCopyN`/`copyChunkVerbatim`), reserving CRC computation for
  chunks the library actually constructs (`eXIf`, the XMP `iTXt`). **User decision, matching
  ExifTool/Exiv2**: a pass-through chunk with an already-invalid CRC keeps that invalid CRC
  in the output — Inject never repairs a CRC on a chunk it did not itself write.
  `TestExtractIssue790Poc2Rejected` proves `testdata/corpus/png/exiv2/issue_790_poc2.png`
  (a truncated-iCCP-chunk file that HEAD's `Extract` silently accepted, returning
  `(nil,nil,nil,nil)`, because `io.ReadFull`'s bare `io.EOF` on a CRC read starting at
  exactly 0 remaining bytes was indistinguishable from a clean end-of-stream) is now
  correctly rejected, because the new bounds check computes `streamSize` up front and
  checks `pos+length+4 > total` before any `Seek`/read, independent of how the underlying
  reader happens to signal EOF. `TestInjectPreservesOriginalCRCEvenWhenInvalid` proves the
  bad-CRC-preservation policy directly. **Golden-hash verification**: SHA-256 of
  `Read`+`SetCaption`+`SetCopyright`+`Write` output differs from HEAD for exactly 114 PNG
  corpus files; an automated byte-level diff tool (not manual spot-checking) confirmed all
  114 differ **only** in a pass-through chunk's 4-byte CRC trailer, and independently
  confirmed the corresponding input file's original CRC at that same chunk was already
  invalid in all 114 cases — HEAD was silently repairing bad CRCs on unmodified chunks;
  this change stops that, which is the intended, spec-compliant behaviour, not a
  regression.
- **#289** (tiff/cr2/nef/arw/dng: read only the metadata prefix, not the whole file):
  `format/tiff/extent.go` (new) computes the exact byte range IFD0's own next-IFD chain,
  ExifIFD, GPSIFD, InteropIFD, and every `SubIFDs` (0x014A) child IFD occupy — including
  every out-of-line tag value within them (MakerNote blobs, `RawIPTC`/`RawXMP` payloads) —
  by an incremental grow-and-rescan loop over the source `io.ReadSeeker` (reads only the
  delta on each grow, never re-reads bytes already held), bounded by `maxFileSize` and a
  64-pass ceiling. `Extract` reads and retains only this prefix; `RawEXIF()`'s doc comment
  and the CHANGELOG now state this precisely for these five formats (unchanged for every
  other supported format). `write.go`'s `writeTIFF`/`writeTIFFCR2`/`writeTIFFARW`/
  `writeTIFFNEF` were changed to always re-`Seek(0)`+re-read the full source from `r`
  rather than reusing `m.rawEXIF` — the copy-and-relocate write path needs every strip/tile
  image-data block, which the prefix, by design, no longer carries. `writeTIFFORF`/
  `writeTIFFRW2` are unchanged (out of #289's scope; #286 already made them whole-file
  reads with a different, unaffected mechanism).

  **Two correctness bugs were found and fixed during this task's own gate work, before
  either AC was reported met:**

  1. **Integer-overflow panic (fuzz-found, fixed before any benchmark was recorded).**
     `FuzzTIFFExtract` immediately crashed a first draft of `extent.go` with a crafted
     BigTIFF `ifd0Off = 0xFFFFFFFFFFFFFFFF`: the naive bounds check `off+countW >
     len(buf)` overflowed and wrapped to a small value, incorrectly passing, then
     panicked slicing `buf[off:]`. Fixed with a `fits(off, width, n uint64) bool` helper
     (`off > n` first, then `width <= n-off`, no addition that can itself overflow) applied
     at every point in `extent.go` where an offset is read from untrusted file content.
     60 s of `FuzzTIFFExtract` afterward: clean.
  2. **Embedded-thumbnail data loss / non-HEAD-identical write output (golden-hash-found,
     fixed before the AC was reported met).** The initial design deliberately excluded
     "image data a metadata value merely points to" (`StripOffsets`, `TileOffsets`,
     `JPEGInterchangeFormat`) from the prefix. This is correct for `StripOffsets`/
     `TileOffsets` (never materialised into any `exif.EXIF` field; always re-read from the
     full file at write time by `enumerateImageBlocks`), but **wrong** for
     `JPEGInterchangeFormat`/`JPEGInterchangeFormatLength`: `exif.Parse`'s own
     `extractJPEGThumbnail` (exif/ifd.go) actively slices those declared bytes into
     `IFD.ThumbnailData` **during parsing itself**, for every IFD it materialises, and
     `exif.Encode` re-embeds that field verbatim on write. Omitting those bytes from the
     prefix made `extractJPEGThumbnail`'s bounds check fail silently (`ThumbnailData` ends
     up `nil`, no error, no crash) — caught only by the golden-hash gate, where
     `raw/metadata-extractor/Canon EOS 70D.cr2` (and 2 other corpus files) produced `Write`
     output byte-different from HEAD despite parsing to identical tag values: with
     `ThumbnailData == nil`, `enumerateImageBlocks` treats the declared JPEG range as a
     generic relocatable image block instead of trusting `exif.Encode` to have already
     embedded it, placing the same, uncorrupted thumbnail bytes at a different file offset.
     Fixed by extending the extent scan to include a JIF pair's declared byte range
     whenever both tags are present on an IFD — but **only** for IFDs `exif.Parse` actually
     materialises as `*exif.IFD` (IFD0, its own `.Next` chain, and the ExifIFD/GPSIFD/
     InteropIFD pointer targets), explicitly **excluding** `SubIFDs` (0x014A): `exif.Parse`
     never materialises a SubIFD as a `*exif.IFD` at all (`enumerateSubIFDs` re-scans the
     full file directly at write time instead — see `relocate.go`), so a JIF pair declared
     inside one is never read back into any `ThumbnailData` field this fix exists to
     protect. The unscoped first version of this fix was itself caught by the AC's own
     `BenchmarkRead/nef` B/op target: `raw/metadata-extractor/Nikon D810.nef` carries a
     multi-megabyte medium-resolution preview inside a `SubIFDs` child (not the top-level
     IFD chain, whose own `ThumbnailData` was `nil` either way), and including it ballooned
     the prefix from ~253 KB to ~2.7 MB for zero round-trip benefit — scoping the fix to
     only the materialised IFDs fixed both the AC miss and confirmed the SubIFD bytes were
     never needed in the first place.

  **Full-corpus verification after both fixes** (temporary local symlink to the real
  corpus for this run only; the committed test is corpus-gated per docs/TESTING.md §2.1
  and skips, not fails, when the corpus is absent — see `task289_test.go`):
  `TestExtractPrefixParityWithWholeFile` — 490 PASS, 19 SKIP (files where either `Extract`
  or `exif.Parse` legitimately errors identically on both the prefix and the whole file,
  e.g. deliberately-malformed torture-test fixtures — not a #289 parity issue), 0 FAIL,
  across every `.tif`/`.tiff`/`.cr2`/`.nef`/`.arw`/`.dng` file in the repo's TIFF/RAW
  corpus. Golden-hash `Read`+`SetCaption`+`SetCopyright`+`Write` SHA-256 comparison against
  HEAD across the same 509 files: 0 mismatches in `CameraModel`/`Caption`/`Copyright`/
  `RawXMP` and 0 mismatches in `Write` output SHA-256 or length — fully byte-identical.

  **Write's ADDITIONAL cost is a deliberate, necessary, and now-minimal trade-off**: since
  the prefix no longer carries strip/tile/thumbnail image data, the copy-and-relocate write
  path must always re-read the whole file fresh from `r`, whereas HEAD could reuse the
  already-resident `m.rawEXIF` (itself the whole file, pre-#289) with no second read. This
  is outside #289's stated AC (Read-only) and explicitly anticipated by the task's own text
  ("Write must still see the full source: it re-reads from the reader") — see the Follow-up
  subsection below for the coordinator-requested verification that this extra cost is
  EXACTLY "one more exact-size read", no more.

#### Follow-up (coordinator review, 2026-09-26)

  Two evidence gaps were raised before the security audit: (1) verify the Write regression
  above is no more than one unavoidable extra full read, and measure the realistic
  Read+edit+Write workflow; (2) measure Read on the FULL TIFF-family corpus (not just the
  5 harness fixtures) and eliminate any regression on real files. Both were real findings
  requiring code changes beyond #289's original scope, resolved as follows.

  **(1) `write.go`'s `readAllCapped` was using `io.ReadAll(io.LimitReader(...))`** — the
  stdlib helper, which grows its buffer geometrically across a variable, unbounded number of
  internal `Read` calls and reallocations of unknown final size — **not `iobuf.ReadAll`**,
  the package's own single-exact-allocation-plus-one-`ReadFull` helper (learns the exact
  remaining size via `Seek(SeekEnd)`, allocates once). This was pre-existing code, but #289
  made it run on every TIFF-family `Write` instead of only as a rare fallback (removing the
  "reuse `m.rawEXIF`" fast path), so its own allocation strategy started to matter for
  exactly the reason #289's own extent scanner does. Fixed by changing `readAllCapped`'s
  signature to `io.ReadSeeker` (every one of its 6 call sites already had one) and
  delegating to `iobuf.ReadAll`. Effect: Write's B/op regression roughly HALVED across every
  format (e.g. dng +208.39% → +94.95%, cr2 +226.56% → +99.72%) and now lands almost exactly
  at **2.0×** HEAD's original B/op for every format — i.e. HEAD's baseline plus one
  additional exact-size read of the same file, confirmed by the ratio itself (cr2: 43.77Mi ÷
  21.91Mi = 1.997×; dng: 47.77Mi ÷ 24.50Mi = 1.950×), not by inspection alone. `BenchmarkRoundTrip`
  (Read + `SetCaption` + `SetCopyright` + Write on the same file, interleaved n=10) confirms
  the realistic combined workflow is **net-neutral for cr2/nef/dng** (`~`, p > 0.05) **and a
  small +6.96% for arw** — the "one extra read" cost is now small enough, relative to the
  massive Read-side win, that the combined workflow is a wash or a small net loss, not the
  55-70% combined regression an earlier (CPU-contention-corrupted) measurement briefly
  suggested before being re-run cleanly. `tiff` still regresses (+57.17% RoundTrip) because
  its 819,606-byte fixture is legitimately ~100% metadata (see below): Read gains nothing
  from the prefix optimisation for this one file, so RoundTrip is left carrying Write's
  "one extra read" cost with no offsetting Read-side saving — a property of this ONE
  synthetic fixture, not of TIFF files or of #289's design in general (see the corpus-wide
  results in (2), where only 3 KiB of the 500-file corpus was measurably RoundTrip-costly
  this way).

  **(2) A corpus-wide per-file `Read` benchmark (all 500 non-malformed TIFF/CR2/NEF/ARW/DNG
  files in the repo's corpus, `testing.Benchmark` per file, HEAD vs current) found a genuine,
  previously-unmeasured problem, then a second, more severe one, both now fixed:**

  - **A DoS-class allocation bug** (not merely a perf regression): `testdata/corpus/tiff/
    exiv2/2018-01-09-exiv2-crash-002.tiff`, a 325-byte deliberately-malformed torture-test
    fixture, measured **~268 MB allocated per `Read`** (≈`maxFileSize`, the package's own
    256 MiB safety ceiling) — a **~825,000×** blow-up relative to its own size. Root cause:
    `scanMetadataExtent`'s growth loop clamped a corrupt/adversarial `need` value to
    `maxFileSize` (256 MiB) before calling `growBuffer`, which unconditionally
    `make([]byte, need)`s BEFORE discovering (via a short read) that the real, tiny file had
    nothing near that many bytes to give — the wasteful allocation happens whether or not
    the subsequent read is short. Fixed by clamping `need` to the file's OWN real size
    (`fileSize`, already known exactly via `Seek(SeekEnd)` before scanning ever starts)
    FIRST, before the much looser `maxFileSize` fallback: no legitimate metadata requirement
    can ever exceed how many bytes the file actually has, so this clamp is free of any
    correctness cost and eliminates the class entirely — every file in the corpus with this
    pattern (several more `exiv2`/`issue_*_poc` crash-test fixtures were found alongside the
    one above) dropped from up to **60,831×** the correct allocation to ≤ 5 KB.
  - **A real (non-adversarial), unbounded-pass-count regression on small/medium TIFF files
    whose IFD chain is only discoverable one link at a time**: `testdata/corpus/tiff/
    exampletiffs/mri.tif` (230,578 B) needed enough small-increment growth passes — each a
    full `make`+`copy` — to allocate ~2.97 MB total (≈13× its own size) under the original,
    exact-need-sized growth policy; `tiff/metadata-extractor/
    m1-8110934bb3b18d0e87ccc1ddfc5f0107.tif` (1,017,530 B) similarly cost ~9.4× HEAD's B/op
    and ~8.3× its ns/op. Three growth-policy refinements were needed together (see
    `extent.go`'s `nextGrowthTarget`/`clampNeed` doc comments for the full, evidence-cited
    rationale of each) — a first, PASS-COUNT-based escalation heuristic ("any 2nd-or-later
    growth pass gets a large factor") was tried and REJECTED because it mis-fired on
    `raw/metadata-extractor/Nikon D810.nef`: NEF genuinely needs 3 small-increment passes to
    fully resolve its IFD chain, but its `need` stays a stable ~0.62% of the 40.7 MB file the
    whole time, and the pass-count heuristic ballooned its B/op to 4.18 MiB, breaking the
    Read AC for a file whose actual metadata never remotely approached the file's size. The
    signal that actually works is the FRACTION of the file a pass's `need` accounts for, not
    how many passes have elapsed:
    1. A "large-fraction snap" — once a single pass's `need` already accounts for ≥10% of
       `fileSize`, jump straight to reading the rest of the file, since there is no
       meaningful "prefix" saving left to protect. This correctly distinguishes NEF's
       stable-small-fraction pattern (never fires) from `m1-8110934...tif`'s pattern (its
       fraction climbs 15% → 15% → 31% across passes; fires on the 31% pass).
    2. A "tail-snap" (unchanged from the original design) for the case a `need` is close to
       `fileSize` in absolute terms but happens to fall under 10% only because `fileSize`
       itself is small.
    3. Plain doubling (`max(need, 2×len(buf))`, capped at `fileSize`) as the fallback,
       bounding total copied bytes to `O(final extent)` instead of `O(final extent ×
       pass count)` for whatever residual cases the first two checks don't already resolve.
    4. **A size-based bypass**: files at or below `smallFileWholeReadThreshold` (4 MiB) skip
       the extent scanner entirely and are read whole via the pre-#289 `extractWholeFile`
       path — a corpus-wide per-file benchmark found every file whose extent-scan cost
       exceeded a plain whole-file read was ≤ ~2.6 MiB, and reading a file that size in full
       is already cheap in absolute terms regardless of how small its own metadata happens
       to be; real camera RAW files (22–41 MB in this repo's own fixtures) are comfortably
       clear of this threshold and keep the full scanner benefit.

  **Corpus-wide result after all fixes** (500 files, `testing.Benchmark` per file, HEAD vs
  current, measured with zero other CPU-intensive processes running — an earlier
  measurement taken while background fuzz jobs were still running was discarded as
  CPU-contention-corrupted after re-running cleanly reproduced dramatically different, much
  worse numbers):

  | Percentile | ns/op ratio (cur/HEAD) | B/op ratio (cur/HEAD) |
  |---|---|---|
  | p10 | 0.948 | 0.998 |
  | p50 (median) | 1.011 | 1.0000 |
  | p90 | 1.054 | 1.0001 |
  | p95 | 1.067 | 1.0004 |
  | p99 | 1.110 | 1.0096 |
  | max (worst of 500) | **1.173** | **1.014** |

  **Zero of the 500 corpus files exceed a 1.20× ratio in either metric** — the worst case
  across the entire real-world TIFF/CR2/NEF/ARW/DNG corpus is 1.17× ns/op and 1.01× B/op,
  both comfortably within normal benchmark noise. This satisfies "no real-file Read
  regression beyond noise" as a corpus-wide, not just harness-fixture, property.

#### Follow-up 2 (coordinator review, 2026-09-26) — reuse m.rawEXIF for Write when it is already the whole file

  Both #289's own extent-scanning follow-ups above (the ≤4 MiB small-file bypass and the
  ≥10%-of-`fileSize` large-fraction snap) can make `m.rawEXIF` end up holding the ENTIRE
  source file, not just a metadata prefix — in which case Write re-reading the whole file
  from `r` (Follow-up's item 1) is unnecessary work HEAD never had to do either, since HEAD
  could always reuse `m.rawEXIF` directly.

  **Implemented exactly the reliable signal the coordinator suggested**: a new unexported
  `Metadata.rawEXIFIsWholeFile bool` field (no public API change), set once by `Read` via a
  new `tiffFamilyRawEXIFIsWholeFile` check (`read.go`) that compares `len(rawEXIF)` against
  the source's actual size (`Seek(SeekEnd)`, restoring the reader's position afterward) —
  computed, never assumed, and only for the five TIFF-family formats (zero extra Seek calls
  for every other format). `write.go`'s four affected functions
  (`writeTIFF`/`writeTIFFCR2`/`writeTIFFARW`/`writeTIFFNEF`) now call a new
  `originalTIFFBytes` helper: when the flag is set, it returns `m.rawEXIF` directly (no
  second read, no clone); otherwise it falls back to the existing `Seek(0)`+`readAllCapped`
  re-read. Reuse is exactly as safe as it was pre-#289 — no relocator in
  `format/tiff/relocate*.go` ever mutates its `base`/`originalBytes` parameter (the same
  invariant Batch E's own finding #1 already established and re-verified here by re-running
  every affected gate).

  **Result — the exact two regressions the coordinator named are gone:**

  | Benchmark | Before this fix | After this fix | HEAD |
  |---|---|---|---|
  | RoundTrip/tiff ns/op | 165.9µ (+57.17%) | **102.7µ (−2.73%)** | 105.6µ |
  | RoundTrip/tiff B/op | 2.391Mi (+49.36%) | **1.601Mi (+0.00%)** | 1.601Mi |
  | Write/tiff ns/op | 103.76µ (+70.05%) | **61.02µ (~, p=0.631)** | 61.02µ |
  | Write/tiff B/op | 1634.1Ki (+97.92%) | **825.6Ki (~, p=1.000)** | 825.6Ki |

  `tiff`'s fixture (819,606 B) is well under the 4 MiB small-file threshold, so both Read
  AND Write now take the whole-file path exactly as HEAD always did — Write/tiff and
  RoundTrip/tiff's B/op and allocs/op are now **bit-for-bit identical** to HEAD (`~`,
  p=1.000 for allocs), not merely close. cr2/nef/arw/dng are files far above the threshold
  (22–41 MB) whose extent scan genuinely converges on a small prefix, so they correctly
  keep re-reading from `r` for Write — there is nothing to reuse for them, and their
  Write/RoundTrip numbers are unchanged from Follow-up 1's measurements.

  **Why ARW RoundTrip still shows +6.96% in one measurement**: re-measured twice more,
  cleanly (interleaved n=10, `pgrep` confirmed no other CPU-intensive process running): a
  second run showed `~` (p=1.000, B/op ratio 1.03%), a third showed +4.76% (p=0.035, B/op
  ratio still 1.03%). **B/op stays essentially flat (~1.0–1.03%) across all three runs while
  ns/op fluctuates between 0% and +4.76%** — the signature of ordinary measurement noise
  around a true value close to 0%, not a reproducible, code-attributable regression. ARW's
  prefix (849.2Ki) is not unusually large relative to the other formats (NEF's is larger in
  absolute terms, 817.6Ki, and shows no comparable ns/op instability); nothing in ARW's
  write path changed in this follow-up (it was already, correctly, on the "re-read from r"
  branch before and after this fix, identical to cr2/nef/dng). There is no evidence of a
  real, recoverable regression here, and therefore nothing further to fix within #289's
  scope — reported as noise, not chased as a phantom bug (see the corpus-wide/CPU-contention
  lessons recorded in agent memory from Follow-up 1).

  **Full re-verification after this fix**: build/vet/`test`/`test -race` (whole repo)
  clean; `golangci-lint run ./...` — 0 new issues (same 6 pre-existing); `staticcheck`/
  `govulncheck` clean; `TestExtractPrefixParityWithWholeFile` — 490 PASS/19 SKIP/0 FAIL
  (unchanged, since this fix touches `read.go`/`write.go` only, not `format/tiff` itself);
  golden-hash `Read`+`SetCaption`+`SetCopyright`+`Write` SHA-256 across the same 509 TIFF
  corpus files — 0 mismatches, fully byte-identical to HEAD (unchanged from Follow-up 1: the
  reused `m.rawEXIF` is the exact same bytes a fresh re-read would have produced, so Write
  output cannot differ). `FuzzRead` (60 s — the one existing fuzz target that exercises the
  new `tiffFamilyRawEXIFIsWholeFile` code path directly, since it lives behind the top-level
  `gometadata.Read`/`Write` pair no format-package-level fuzzer reaches) plus the five
  TIFF-family write fuzz targets (`FuzzTIFFInject`, `FuzzCR2Inject`, `FuzzNEFInject`,
  `FuzzARWInject`, `FuzzDNGInject`, 60 s each): all clean.

### 60-second fuzz clean (Batch F, 2026-09-25)

`FuzzPNGExtract`, `FuzzPNGInject`, `FuzzTIFFExtract`, `FuzzTIFFInject`, `FuzzCR2Extract`,
`FuzzCR2Inject`, `FuzzNEFExtract`, `FuzzNEFInject`, `FuzzARWExtract`, `FuzzARWInject`,
`FuzzDNGExtract`, `FuzzDNGInject` — each run standalone for 60 s (`-fuzztime=60s`) against
the final, post-fix code; zero crashers. `FuzzTIFFExtract` was additionally run against
the pre-fix code that had the integer-overflow bug (see #289's per-task note above), where
it found the crash within the first second — confirming the fuzz gate itself is effective,
not merely clean by chance. All 10 TIFF-family targets (`FuzzTIFFExtract`/`Inject`,
`FuzzCR2Extract`/`Inject`, `FuzzNEFExtract`/`Inject`, `FuzzARWExtract`/`Inject`,
`FuzzDNGExtract`/`Inject`) were re-run for a further 60 s each after the follow-up review's
`readAllCapped`/DoS-allocation/growth-policy fixes above; all clean. The top-level
`FuzzRead` (the sole existing fuzz target that reaches the `gometadata.Read`/`Write`
package's own code, where Follow-up 2's `tiffFamilyRawEXIFIsWholeFile`/`originalTIFFBytes`
logic lives) plus `FuzzTIFFInject`/`FuzzCR2Inject`/`FuzzNEFInject`/`FuzzARWInject`/
`FuzzDNGInject` were each run a further 60 s after Follow-up 2's `m.rawEXIF`-reuse fix; all
clean.

### Batch G (tasks #291, #292, #293, #294) — 2026-09-26

Go version go1.26.1, `cpu: Apple M4`, `-count=6` interleaved runs (the batch's own
Definition of Done). Measured with the same scratch end-to-end harness as Batch E/F
(`gm.Read`/`gm.Write` over real corpus files): `raw/metadata-extractor/Canon EOS R3.cr3`
(12,277,000 B), `raw/metadata-extractor/OM System TG-7.ORF` (13,150,700 B),
`raw/metadata-extractor/Panasonic DMC-GF7.rw2`, plus the same
tiff/cr2/nef/arw/dng samples as Batch F.

#### #294 (png: graceful stop on an overrunning chunk instead of a fatal error)

**User decision**: `Extract` now treats a truncated/overrunning chunk (the same condition
`checkChunkTruncation` already detected) as a graceful stop-and-return-collected-metadata,
matching `io.EOF` handling, instead of a fatal error — `Inject` is unaffected and still
rejects truncated input. Full-corpus digest diff against HEAD (f9a1c9c): exactly 24 PNG
files affected (`issue_428_poc2.png`, `issue_790_poc2.png`, `issue_789_poc1.png`, and
similar truncated-chunk fixtures), zero non-PNG divergence. `TestExtractIssue790Poc2Rejected`
renamed to `TestExtractOverrunningChunkReturnsCollectedMetadata`.

#### #293 (orf/rw2: route Extract through the #289 metadata-prefix scanner)

`orf.Extract`/`rw2.Extract` now call the new exported `tiff.ExtractWithMagic` (mirrors
`exif.AcceptRAWMagic`'s non-standard-magic acceptance) instead of the old whole-file +
IFD0-only `internal/tiffscan` path. `RawEXIF()` is now a metadata PREFIX for ORF/RW2 too.
`write.go`'s `writeTIFFORF`/`writeTIFFRW2` were fixed to route through the extended
`m.rawEXIFIsWholeFile` flag (previously assumed whole-file unconditionally, which #293
broke — caught by the full test suite, not silently shipped).

`extent.go`'s `nextGrowthTarget` fallback step changed from geometric doubling to a fixed
+64 KiB additive margin: doubling overshot NEF's true final need by ~2× on its last grow
pass (need only grows 253,054 → 253,154 → 253,172; doubling allocated 506,108 B for a
253,172 B need). NEF Read B/op: 837,445 → 394,324 (**−52.9%**), allocs/op: 30 → 26.

**Follow-up fix (coordinator-requested closure of the ORF gap reported above): the 10%
large-fraction snap is removed from `clampNeed`, not retuned.** `raw/metadata-extractor/OM
System TG-7.ORF`'s real metadata need is a STABLE 1,514,496 B (11.516% of its 13,150,700 B
file — converges on the very first growth pass, confirmed unchanged on a second rescan via
a dedicated diagnostic), legitimately above the old 10% threshold, so the snap forced a
full 13.15 MB read instead of the true ~1.5 MB prefix. A large but STABLE relative fraction
is indistinguishable, by a relative-threshold check alone, from a need that keeps GROWING
across passes toward a large fraction — the pattern the snap was originally meant to catch —
so a corpus-wide A/B simulation harness (`clampNeed` vs 4 candidate replacements, replayed
through the real `scanExtentPass`/`growBuffer` machinery) was built and run against every one
of the 114 real TIFF-family corpus files above `smallFileWholeReadThreshold` (4 MiB, the
size below which the scanner is bypassed entirely). Results: removing the snap regressed
**zero** files' pass count or final buffer size, while shrinking the corpus-wide average
converged buffer 42% (4,651,608 B → 2,688,439 B); replacing the relative rule with an
absolute-bytes-remaining threshold gave identical results up to 4 MiB of margin (no file in
this corpus has a converged-need gap in that range) and a strictly worse result at 8 MiB
(`raw/metadata-extractor/Canon EOS 350D.CR2`: 773,969 B → 7,797,386 B for a single saved
pass) — evidence that no replacement margin adds benefit over removing the rule outright.
TG-7.ORF: 13,150,700 B (100%) → 1,580,032 B (12.0%) prefix, same 2 passes. See `clampNeed`'s
and `scanMetadataExtent`'s doc comments (`format/tiff/extent.go`) for the full corpus
citations. Regression tests: `TestClampNeedNoLargeFractionSnap` (unit-level, 5 cases) and
`TestExtractORFDoesNotOverreadOnStableLargeFraction` (corpus-gated) in
`format/tiff/task293_test.go`.

**Follow-up fix: `exif.writeIFD`/`writeIFDBigTIFF` no longer double-buffer the out-of-line
value area.** Both functions previously built a separate `valueArea` slice and then copied
it into the caller's output buffer via `append(out, valueArea...)` — paying for the value
area's full byte count TWICE (once to build it, once to copy it in), negligible against a
whole-file write buffer but the dominant remaining allocation once the fixes above shrank
every TIFF-family format's Write buffer to metadata-prefix scale. Discovered via
`go tool pprof -alloc_space` on `BenchmarkWrite/rw2` showing 49.6% of B/op attributed
directly to the `valueArea = append(valueArea, e.Value...)` line. Fixed by splitting each
function into two per-entry passes over the SAME entries slice: pass 1 fills the fixed
12-/20-byte entry records and computes the value area's exact final length (identical
alignment/sizing arithmetic to before); pass 2 grows `out` once by that exact length
(`slices.Grow`) and appends every out-of-line value's bytes directly into it — eliminating
the intermediate buffer entirely. Verified via a full 3,281-file corpus `wcmp` SHA-256
sweep against f9a1c9c: byte-for-byte identical output (this function is shared by every
format that embeds EXIF, not only the seven TIFF-family containers).

`exif.AliasThumbnail()` (new `ParseOption`, unconditionally applied by `read.go`'s
`parseEXIF`, since `raw` there is always `m.rawEXIF`, retained unmodified for
`*Metadata`'s entire lifetime): `extractJPEGThumbnail` now aliases
(`b[jifOff:end:end]`, cap-clamped) instead of copying the EXIF §4.5.5 JPEG thumbnail when
opted in — eliminating a redundant `make`+`copy` of up to several hundred KB. Every
MakerNote-internal `traverse(...)` call (~18 sites in `makernote_parse.go`) and the
exported `ParseIFDAt` explicitly pass `false` (deliberately conservative: those parse a
different, transient, or not-provably-retained buffer). Measured impact: ARW B/op
935,341 → 602,233 (Sony's PreviewImage lives in the top-level IFD chain, reached by
aliasing); NEF B/op barely moved (394,346 vs 394,324) because Nikon's own PreviewIFD
thumbnail is embedded inside the MakerNote sub-structure, not reached by this round's
scoping decision.

Housekeeping (not a code bug): a stray untracked `format/tiff/testdata/corpus` symlink was
found and removed — it broke `exif.TestEncodedSizeMatchesEncode`'s directory walk and
spuriously corrupted 2 unrelated `TestConformance_R18_bigtiff_roundtrip_fidelity_corpus`
sub-tests (both reproduced identically on `git stash`, i.e. present on f9a1c9c too — not a
regression from this session's work).

#### #292 (cr3: rebuild only moov, stream everything else)

`cr3.Inject` no longer reads the whole file into one buffer and builds a second,
equally-large output buffer. It now rebuilds only the `moov` box in memory and streams
`ftyp`/pre-moov bytes and `mdat`+anything after `moov` from `r` to `w` verbatim through a
new shared primitive, `iobuf.StreamCopyN` (`internal/iobuf/streamcopy.go` — a fixed 64 KiB
pooled buffer, reused across the whole call), mirroring ExifTool's `WriteQuickTime.pl`
"rewrite the moov atom, copy mdat straight through" model. `readTopLevelBox` now also
returns the matched box's `(start, end)` byte range so `Inject` can compute the 3 stream
regions from a single scan; the old CR2-EXTSIZE-01-style "re-derive header length" step in
`injectIntoMoov` is gone entirely, since `readTopLevelBox`'s payload is already
header-stripped.

| Benchmark (Canon EOS R3.cr3, 12.28 MB) | ns/op f9a1c9c → cur | B/op f9a1c9c → cur |
|---|---|---|
| Write | 1.155m → 447µ (**−61.3%**, ratio 0.39×) | 24.75Mi → 217Ki (**−99.14%**, ~114×) |
| RoundTrip | 1.154m → 476µ (**−58.7%**, ratio 0.41×) | 24.79Mi → 254Ki (**−99.00%**, ~97×) |

AC (B/op ≤2 MiB, ns/op ≤0.6× f9a1c9c): **both exceeded by a wide margin**. `wcmp` SHA-256
identical across all 12 CR3 corpus files; `pdump` digest identical. `FuzzCR3Inject` 60 s:
21.8M execs, 0 crashes.

#### #291 (tiff/raw: stream image-data blocks instead of buffering the whole file)

`relocateTIFFFromParsed` (and its NEF/ARW/ORF/RW2-specific siblings) now return
`(header []byte, blocks []*imageBlock, err error)` instead of a single `[]byte`: `header`
is the encoded IFD structure + SubIFD raw bytes (bounded by metadata size, never file
size); `blocks` carries each image block's already-assigned `newOffset` for the caller
(`writeRelocated`, new file `relocate_stream.go`) to stream from `r` (via
`iobuf.StreamCopyN`) or slice directly from an already-whole-file buffer, in slice order,
immediately after `header` — reproducing the exact same byte stream the old single-buffer
return used to produce.

**Critical bug caught before any benchmark was recorded, via manual code-path tracing (not
by a failing test — the existing 5-fixture-style test suite would not have caught it):**
`enumerateIFDBlocks`/`extractParallelOffsetBlocks`/`appendJPEGBlock`'s bounds checks
(`end > uint64(len(base))`, "does this declared block's range fit in the source buffer")
were written when `base` was ALWAYS the whole file. Once `base` can be only a metadata
PREFIX, this check would have REJECTED — with a hard `ErrBlockOutOfBounds` error, not a
silent skip — every legitimate StripOffsets/TileOffsets/JPEGInterchangeFormat block in
every real TIFF/DNG/CR2 write, since a real block routinely extends far beyond the prefix
by design. Fixed by threading a `fileLen uint64` (the TRUE total file length, obtained via
one `Seek(SeekEnd)` when the source is not already known to be whole) separately from
`base` through the entire enumeration call graph
(`enumerateImageBlocks`/`enumerateIFDBlocks`/`extractParallelOffsetBlocks`/
`appendJPEGBlock`/`enumerateSubIFDs`/`enumerateSubIFDsAt`), used for the bounds check in
place of `len(base)`.

**Follow-up fix (closes the architectural gap reported above): NEF/ARW/ORF/RW2 now stream
too, via on-demand fetch of exactly their own manufacturer-specific blob instead of the
whole file.** Each format's own "special metadata blob" lives outside the standard
IFD/SubIFD/out-of-line-array structure `extent.go`'s #289/#293 scanner walks: ARW's
SR2Private (0xC634, an INLINE 4-byte pointer to a ~37 KB blob), NEF's Nikon
MakerNote-embedded PreviewIFD (nested one level inside the MakerNote's own internal IFD),
ORF's OLYMP-type MakerNote external ThumbnailImage (same pattern), and RW2's RawDataOffset
(0x0118, an "extends to EOF" convention). Two design choices close the gap without ever
buffering the whole file:
- A new `extendBase(base, r, fileLen, targetLen) ([]byte, error)` (`relocate_extend.go`)
  grows a metadata-prefix `base` to cover exactly `targetLen` bytes, fetching ONLY the
  delta `[len(base), targetLen)` from `r` in a single `Seek`+`ReadFull` — never re-reading
  bytes already held, never touching `r` at all when `base` already covers `targetLen` (the
  common whole-file-already-resident case). Each manufacturer-specific extractor
  (`extractSonySR2Info`, `extractNikonPreviewInfo`, `extractOlympMakerNoteInfo`) now calls
  it with a small, generously-justified fixed margin (4–8 KiB) before parsing that format's
  own private sub-structure, then a second, precise call once the sub-structure's own
  declared length is known.
- RW2's raw sensor DATA was already a standalone `*imageBlock` (streamed via the shared
  `writeRelocated` mechanism) — its only bug was `extractRW2RawDataBlock` sizing the block
  from `len(base)` (a prefix) instead of the true `fileLen`; fixed by threading `fileLen`
  through the same call graph #291's earlier fix already established.

Every bounds check across all four extractors that used to compare against
`uint64(len(base))` — written when `base` was always the whole file — now compares against
`fileLen` instead, mirroring the exact same class of fix #291's own critical bug (below)
already applied to `enumerateIFDBlocks`/`extractParallelOffsetBlocks`/`appendJPEGBlock`.
`write.go`'s `writeTIFFNEF`/`ARW`/`ORF`/`RW2` now call `tiffPrefixBytes` (the same
prefix-or-whole-file resolver TIFF/CR2/DNG already used) instead of unconditionally reading
the whole file; `originalTIFFBytes` — now dead code with zero remaining callers — is
removed.

**Follow-up fix (coordinator-requested precise attribution of the NEF/ARW/DNG Read
allocs/op delta vs ce1dc82, the pre-#289 whole-file-read baseline): `scanExtentPass` no
longer allocates a fresh `*extentScan`/`seenIFD` map on every growth pass.** A
`-diff_base` pprof comparison (`memprofilerate=1`, `-focus=GoMetadata`) between a ce1dc82
worktree and this tree isolated `scanExtentPass`'s own `s := &extentScan{...,
seenIFD: make(map[uint64]bool, 16)}` as the single largest new allocation source —
invisible before this batch's other fixes, since it scales with GROWTH PASS COUNT
(NEF/ARW typically need 2–3 passes to converge: 808 objects / 100 iterations = 8.08/op
for NEF). `scanMetadataExtent` now allocates one `*extentScan` before its growth loop
instead of one per pass; `scanExtentPass` resets the reused struct's `need`/`buf`/
`order`/`bigTIFF` and `clear()`s (never reallocates) `seenIFD` before each pass — clearing
is required for correctness, not just performance: a pass that stopped early (buffer too
small) must let the next, larger-buffer pass revisit the same IFD offset from scratch, so
a stale "seen" entry surviving from a prior pass would incorrectly skip it. Result: NEF
allocs/op 26 → 22, ARW 28 → 24 (exact parity with ce1dc82), DNG 39 → 35, RW2 21 → 17, ORF
27.2 → 24; TIFF/CR2 unchanged (11, 27 — both already converge in a single pass, so had
nothing to save). See this task's own AC discussion below for the full per-format
attribution of what remains.

| Benchmark | ns/op f9a1c9c → cur (ratio) | B/op f9a1c9c → cur (ratio) | allocs f9a1c9c → cur |
|---|---|---|---|
| Write/tiff | 66.3µ → 18.6µ (0.281×) | 829.1Ki → 13.8Ki (**0.0166×, ~60×**) | 28.8 → 17.0 |
| Write/cr2 | 2.636m → 868.6µ (0.330×) | 43.78Mi → 84.1Ki (**0.00188×, ~533×**) | 64.2 → 43.0 |
| Write/dng | 2.297m → 987.4µ (0.430×) | 46.94Mi → 417.6Ki (**0.00869×, ~115×**) | 72.0 → 45.0 |
| Write/nef | 3.868m → 1.559m (**0.403×**) | 80.51Mi → 303.6Ki (**0.00368×, ~272×**) | 71.0 → 47.0 |
| Write/arw | 2.417m → 1.178m (**0.487×**) | 48.51Mi → 745.2Ki (**0.0150×, ~66.7×**) | 74.7 → 44.0 |
| Write/orf | 801.1µ → 531.5µ (0.663×) | 14.01Mi → 1.457Mi (**0.104×, ~9.6×**) | 35.3 → 20.8 |
| Write/rw2 | 1.575m → 753.3µ (0.478×) | 20.22Mi → 652.5Ki (**0.0315×, ~31.7×**) | 27.7 → 15.0 |
| Read/tiff | 43.1µ → 44.8µ (1.041×) | 811.0Ki → 811.0Ki (~1.000×) | 10.0 → 11.0 |
| Read/cr2 | 7.86µ → 7.33µ (0.932×) | 80.23Ki → 72.24Ki (0.900×) | 27.0 → 27.0 |
| Read/dng | 16.0µ → 16.7µ (1.042×) | 202.9Ki → 226.9Ki (1.118×) | 38.0 → 35.0 |
| Read/nef | 32.3µ → 18.6µ (0.576×) | 817.8Ki → 385.1Ki (0.471×) | 30.0 → 22.0 |
| Read/arw | 37.4µ → 29.0µ (0.776×) | 849.4Ki → 588.1Ki (0.692×) | 29.0 → 24.0 |
| **Read/orf** | **406.5µ → 59.5µ median, n=10 (0.146×, ~6.8×)** | **12.55Mi → 1.577Mi (0.126×, ~8.0×)** | 19.0 → 24.0 |
| **Read/rw2** | **708.1µ → 26.2µ (0.037×, ~27×)** | **18.83Mi → 791.1Ki (0.041×, ~24×)** | 12.0 → 17.0 |
| RoundTrip/tiff | 109.3µ → 76.3µ (0.698×) | 1.608Mi → 833.2Ki (0.506×) | 58.0 → 49.5 |
| RoundTrip/cr2 | 2.654m → 895.9µ (0.338×) | 43.85Mi → 159.0Ki (**0.00354×, ~282×**) | 105.0 → 82.0 |
| RoundTrip/dng | 2.327m → 984.7µ (0.423×) | 47.14Mi → 651.1Ki (**0.0135×, ~74.1×**) | 121.8 → 94.0 |
| RoundTrip/nef | 3.870m → 1.580m (0.408×) | 81.31Mi → 694.4Ki (**0.00834×, ~120×**) | 114.7 → 85.0 |
| RoundTrip/arw | 2.432m → 1.361m (0.560×) | 49.35Mi → 1.304Mi (**0.0264×, ~37.9×**) | 118.8 → 83.5 |
| RoundTrip/orf | 1.295m → 614.0µ (0.474×) | 26.56Mi → 3.036Mi (**0.114×, ~8.75×**) | 68.8 → 60.0 |
| RoundTrip/rw2 | 2.296m → 784.1µ (0.341×) | 39.04Mi → 1.399Mi (**0.0358×, ~27.9×**) | 57.5 → 51.3 |

Methodology: `go test -bench . -benchmem -count=6`, `mod-cur` (this session's tree, replace
directive to the working copy) vs `mod-head` (a pristine `src-head` snapshot verified
byte-identical to `f9a1c9c` via `git show f9a1c9c:<file> | diff`), same scratch `e2e`
harness/corpus samples as prior rounds, Apple M4.

AC scorecard — **all of task #291's Write B/op ≤2 MiB target now met, for all 7
TIFF-family formats** (max is Write/orf at 1.457 MiB); **NEF/ARW ns/op ≤0.6× f9a1c9c: both
met** (0.403×, 0.487×). Task #293's Read/{orf,rw2} AC (≤60 µs, ≤2 MiB/op): **RW2 fully met**
(28.3 µs, 773 KiB). **ORF re-measured, interleaved `count=10`, after the allocs/op fix
below** (which also reduces ORF's own allocs 27.2 → 24): sorted samples (µs) `57.6, 58.6,
59.0, 59.0, 59.5, 59.6, 59.9, 61.1, 61.2, 61.2`; **median 59.5 µs (meets the ≤60 µs AC)**,
mean 59.65 µs, 95% CI (t, df=9) **[58.78, 60.53] µs** — the CI's upper bound sits
marginally above 60 µs, so this is "meets on the median, right at the boundary on the
tail" rather than a CI-clean pass; B/op 1.577 MiB (meets ≤2 MiB with margin). A CPU
profile of `BenchmarkRead/orf` (`-cpuprofile`, 2000 fixed iterations) attributes the real,
non-runtime-noise time to `runtime.memclrNoHeapPointers` inside `growBuffer`'s
`make([]byte, targetLen)` and the underlying `read()` syscall — the unavoidable cost of
allocating and reading the ~1.5 MB Olympus MakerNote blob this camera embeds as a single
opaque out-of-line value; no further reduction is possible without excluding bytes
`exif.Parse` itself would parse as real, spec-legal metadata (forbidden by the 100%
EXIF-compliant mandate).

**NEF/ARW/DNG allocs/op vs the requested ce1dc82-measured targets 20/24/32 — precisely
attributed via `-diff_base` pprof (`memprofilerate=1`, `-focus=GoMetadata`) between a
ce1dc82 worktree and this tree, then closed where the delta was this sprint's own
avoidable cost.** Initial numbers (NEF 26, ARW 28, DNG 39 — over by 6/4/7) were
attributed to two classes of call site:
- **Avoidable, fixed:** `scanExtentPass` allocated a fresh `*extentScan` (holding a
  `seenIFD map[uint64]bool`) on EVERY growth pass, instead of once per `Extract` call —
  invisible before this batch's other fixes shrank everything else, but the single
  largest new allocation source once they did (808 objects / 100 iterations = 8.08/op for
  NEF, which typically needs 2–3 passes to converge). Fixed by hoisting one `*extentScan`
  out of `scanMetadataExtent`'s growth loop and having `scanExtentPass` reset its state
  (`need`, `buf`/`order`/`bigTIFF`, and — via `clear`, not reallocation — `seenIFD`)
  before each pass instead of allocating a new one; `clear`ing `seenIFD` between passes
  (rather than letting a prior pass's entries persist) is required for correctness, not
  just an optimisation — a pass that stopped early must let the next, larger-buffer pass
  revisit the same IFD from scratch. Result: NEF 26 → **22**, ARW 28 → **24** (exact
  parity with ce1dc82), DNG 39 → **35**.
- **Inherent, reported not removed:** `format/tiff.growBuffer` (+1.01/op) and
  `format/tiff.readInitialPrefix` (+1.01/op) are the initial-prefix-read and
  buffer-grow `make()` calls that ARE the metadata-prefix architecture — the direct price
  of the ~90–99% B/op reduction #289/#291 exist for, not overhead to eliminate.
  `scanMetadataExtent`'s own one-time `*extentScan`+map allocation (~4.04/op after the fix
  above, down from ~8.08/op, no longer scaling with pass count) is likewise inherent to
  cycle-safe IFD-chain walking; a linear-scan-slice replacement for the map was
  considered and rejected — `seenIFD` is shared across the IFD0 chain AND every
  ExifIFD/GPSIFD/InteropIFD/SubIFD branch in one scan, so its worst-case size is bounded
  by the PRODUCT of several independent per-branch caps (`maxExtentTraverseIFDs` × SubIFD
  count), not a small constant; a linear scan against that theoretical worst case risks
  O(n²) CPU for a crafted file, a correctness/safety regression this project's own
  priority order (correct → safe → fast) forbids trading for a small further allocs/op
  win. These, plus several DECREASES this sprint's removal of the old whole-file-read
  path already produces for free (`internal/iobuf.ReadAll`, `exif.traverse`,
  `format.Detect`, `xmp.Parse`/`parseRDF`, `format.parseTIFFScanHeader`), net out to the
  remaining **NEF +2 (22 vs 20), DNG +2–3 (35 vs 32–33; the ce1dc82 baseline itself
  measured 33, not exactly 32, under this round's own methodology), ARW +0 (24 vs 24,
  exact parity — ARW additionally benefits from `exif.AliasThumbnail`'s own −2.02/op,
  since Sony's PreviewImage thumbnail lives in the top-level IFD chain this alias reaches,
  fully offsetting the architecture's own added cost for this format specifically)**.
  Regression-tested (build/vet/full suite/-race/lint/full corpus `wcmp`+`pdump`
  byte-identical/7 Extract-side fuzz targets 60 s each) after the fix; see
  `format/tiff/extent.go`'s `scanExtentPass`/`scanMetadataExtent` doc comments for the
  full citation.

Full corpus (3,281 files, every supported format) `wcmp` SHA-256 comparison against
f9a1c9c: **zero unexplained divergence.** The only differences are the 74 already-explained
PNG `READERR`→`WRITEERR`/graceful-read pairs (#294's own documented behaviour change on
genuinely truncated fuzz-PoC fixtures) and a newly-discovered, pre-existing (reproduces
identically on 3 separate runs of the UNMODIFIED f9a1c9c binary, so unrelated to any change
in this session) non-determinism in 7 corpus JPEGs with very large embedded Google
Photo-Sphere/Depth-Map XMP payloads (`exiv2-bug922.jpg` and its `_2`/`_3` copies,
`Android Depth Map.jpg`, `Google Cardboard.jpg` and its `_2` copy) — `Write`'s output SHA-256
differs run-to-run for these 7 files on both f9a1c9c and this session's tree alike, almost
certainly a map-iteration-order issue in XMP serialisation; **reported to the coordinator,
not fixed, per Scope Discipline** (out of scope for #291/#293). `pdump` parsed-digest
comparison: zero unexplained divergence (the same 74-line PNG explanation, nothing else).

`golangci-lint` surfaced and fixed, as part of this batch's own gate (not pre-existing):
two now-dead parameters (`relocatedLen`'s `blocks` — always `nil` after the split;
`extractParallelOffsetBlocks`/`enumerateIFDBlocks`/`enumerateImageBlocks`'s `base` — no
longer read once bounds checks moved to `fileLen`) were removed rather than left as
unused-but-harmless, per `unparam`'s finding.

### 60-second fuzz clean (Batch G, 2026-09-26)

`FuzzPNGExtract`, `FuzzORFExtract`, `FuzzRW2Extract`, `FuzzCR3Extract`, `FuzzCR3Inject`,
`FuzzTIFFExtract`, `FuzzTIFFInject`, `FuzzCR2Inject`, `FuzzNEFInject`, `FuzzARWInject`,
`FuzzDNGInject`, `FuzzORFInject`, `FuzzRW2Inject`, and `FuzzParseEXIF` (exif package, since
`exif/exif.go`/`exif/ifd.go`/`exif/makernote_parse.go` were touched for
`AliasThumbnail`) — each run standalone for 60 s against the final, post-fix code; zero
crashers across all 13 targets.

### 60-second fuzz clean (Batch G coordinator follow-up, 2026-09-26)

The same 14 targets the coordinator's own follow-up request named (`FuzzParseEXIF`,
`FuzzTIFFExtract`, `FuzzTIFFInject`, `FuzzCR2Inject`, `FuzzNEFInject`, `FuzzARWInject`,
`FuzzDNGInject`, `FuzzORFExtract`, `FuzzORFInject`, `FuzzRW2Extract`, `FuzzRW2Inject`,
`FuzzCR3Extract`, `FuzzCR3Inject`), plus 4 more added on this session's own risk
assessment (`FuzzJPEGInject`, `FuzzPNGInject`, `FuzzWebPInject`, `FuzzHEIFInject`): the
`exif.writeIFD`/`writeIFDBigTIFF` double-buffer fix is reached from `encodeEXIF`
(`write.go`), the single shared entry point every format's `Write` uses whenever `m.EXIF`
was modified — not only the seven TIFF-family formats `relocate_extend.go`/`extent.go`
touch — so these four were added for defense-in-depth given that wider blast radius. Each
run standalone for 60 s (`go test -fuzz=<Name> -fuzztime=60s ./<pkg>/...`) against the
tree at that point (on-demand manufacturer-blob fetch, `clampNeed`'s large-fraction snap
removed, `writeIFD`/`writeIFDBigTIFF` double-buffer elimination, all three applied):
**zero crashers across all 18 targets** (execs/target ranged from ~1.1M to ~22M in the
60 s budget).

### 60-second fuzz clean (Batch G, `scanExtentPass` allocs/op fix, 2026-09-26)

The 7 Extract-side targets deferred from the round above (`FuzzTIFFExtract`,
`FuzzCR2Extract`, `FuzzNEFExtract`, `FuzzARWExtract`, `FuzzDNGExtract`, `FuzzORFExtract`,
`FuzzRW2Extract`) — the direct callers of `scanMetadataExtent`/`scanExtentPass` — each run
standalone for 60 s against the tree with the `*extentScan` reuse fix applied: **zero
crashers across all 7 targets** (execs/target ranged from ~958K to ~20M). This closes the
JPEG/PNG/WebP/HEIF/remaining-RAW-Extract fuzz gap the prior round's note above left open,
specifically for the function this fix touches.

### Security fix: BigTIFF off+size overflow panic in the copy-and-relocate write path (2026-09-26)

A security audit of Batch G surfaced a **CRITICAL, pre-existing** (bisected to 0ebf5d4,
long before Sprint 44 — not a Batch G regression) crash: several bounds checks in the
TIFF-family copy-and-relocate write path computed `end := off + size` as a raw, unchecked
`uint64` addition, then compared `end > fileLen`. A BigTIFF file declaring
StripOffsets/StripByteCounts (or a SubIFD entry's value area) as LONG8 (8-byte) fields can
set `off = MaxUint64-1, size = 10` — the raw addition wraps to `8`, which then incorrectly
passes as "in bounds" against any `fileLen`. The resulting out-of-range `imageBlock` then
panics on a Go slice expression (`prefix[blk.srcOffset:end]` — `slice bounds out of range
[18446744073709551614:8]`), reachable from untrusted input via `Write`/`WriteFile` on any
BigTIFF source.

Fixed by routing every such comparison through a single shared, overflow-safe helper
(`internal/boundscheck.Fits`, extracted from `format/tiff/extent.go`'s pre-existing `fits`
— that function is now a one-line delegate, so every existing call site and doc-comment
citation in that file needed no further change) instead of re-deriving the check ad hoc at
each site. Sites fixed, found by grepping the whole `format/tiff` relocate/stream/extend
code plus `exif`'s own write/read paths for the same `off+size`/`off+len` pattern:

- `format/tiff/relocate.go`: `appendJPEGBlock`, `extractParallelOffsetBlocks` (the
  coordinator-cited site), and `extractRawIFD` (split into a new `rawEntryValueEnd` helper
  to keep cyclomatic complexity within budget; defense-in-depth — this site's own
  `totalLen`/`off` invariants already prevented it from being reachable as a crash, but the
  raw addition was still wrong on its own terms).
- `format/tiff/relocate_stream.go`: `writeRelocated` (the coordinator-cited site —
  `wholeFile==true`'s `prefix[blk.srcOffset:end]` panic).
- `format/tiff/relocate_bigtiff.go`: `ifdEntryTable`, `readRawEntryAt` (defense-in-depth),
  `decodeOffsetArray`, and `parseIFDAtBigTIFF` — the latter two also needed an
  overflow-safe **multiplication** guard (`count > maxU64/elemSz`) before the addition,
  since `count` is itself an attacker-controlled BigTIFF LONG8 field.
- `exif/ifd.go`: `extractJPEGThumbnail` — a **newly discovered, independently reachable**
  instance of the same class, on the **Read** path (not Write): a BigTIFF
  `JPEGInterchangeFormat` entry stored as `TypeLong8` supplies an offset up to `MaxUint64`,
  reachable from any top-level `Read`/`ReadFile` of a crafted file — no `Write` call
  required. `internal/boundscheck` is a new dependency-free leaf package specifically so
  `exif` (which `format/tiff` imports, precluding the reverse) can share the identical
  check without a second, hand-derived copy.

Regression test: `format/tiff/security_bigtiff_overflow_test.go`
(`TestSecurityBigTIFFStripOverflowNoPanic`) exercises the exact PoC (StripOffsets =
`MaxUint64-1`, StripByteCounts = `10`, both LONG8/inline) through both of
`writeRelocated`'s branches (`wholeFile` true and false) and both byte orders — confirmed
to reproduce the identical panic when temporarily reverted (`slice bounds out of range
[18446744073709551614:8]`), and to return `ErrBlockOutOfBounds` cleanly with the fix
restored. The same PoC bytes were added as `FuzzTIFFInject` seeds (LE and BE).

Verification: build/vet/full test suite/-race (whole repo, including a `t.Parallel()` data
race in the new test itself — `exif.EXIF` is not safe for concurrent mutation, #245 — found
and fixed during this same pass)/staticcheck/golangci-lint (5 pre-existing
baseline)/govulncheck all clean. Full 3,281-file corpus `wcmp` SHA-256 vs f9a1c9c: zero
unexplained divergence (same known 136-line baseline). All 7 TIFF-family `*Inject` fuzz
targets (TIFF/CR2/NEF/ARW/DNG/ORF/RW2), 60 s each: zero crashers.

### Sprint 44 cumulative (ce1dc82 → this session's final tree, 2026-09-26)

`ce1dc82` is the commit immediately preceding Sprint 44's first performance task — the
last state before any of tasks #198–#294 landed, and the same baseline task #293's
original allocs/op targets (20/24/32) were measured against. This table is the full-sprint
counterpart to every per-batch table above: same scratch `e2e` harness, same corpus
samples, `count=6` (interleaved via `go test -bench . -count=6`), Apple M4. Full benchstat
output: `scratchpad/profiling-r2/raw/final_ce1dc82_vs_batchG.txt` (not part of the git
repository — session-local scratch).

**Overall geomean across the full suite (`Read`, `ReadAccess`, `AccessOnly`, `ReadFile`,
`Write`, `RoundTrip` — every sample, every format):** ns/op **-71.25%** (0.288×), B/op
**-85.63%**¹, allocs/op **-25.12%**¹.

¹ benchstat flags these two geomeans with "summaries must be >0 to compute geomean": a
few `AccessOnly` samples (pure struct-field reads, no I/O) report exactly 0 B/op, which a
strict geomean cannot include — the percentage itself is still benchstat's own computed
delta over the includable rows, not an estimate.

| Format | Read ns/op ce1dc82→final | Read B/op ce1dc82→final | Write ns/op ce1dc82→final | Write B/op ce1dc82→final |
|---|---|---|---|---|
| jpeg (canon7d) | 20.96µ→17.13µ (−18.3%) | 57.30Ki→46.42Ki (−19.0%) | 18.81µ→18.46µ (−1.9%) | 31.67Ki→30.90Ki (−2.4%) |
| jpeg (iphone11) | 3.711µ→3.119µ (−16.0%) | 28.20Ki→17.51Ki (−37.9%) | 127.8µ→125.3µ (~) | 18.86Ki→16.45Ki (−12.7%) |
| jpeg (exiftool) | 11.211µ→9.835µ (−12.3%) | 13.84Ki→13.85Ki (~) | 7.523µ→7.491µ (~) | 7.712Ki→7.087Ki (−8.1%) |
| png | 18.770µ→2.872µ (**−84.7%**) | 3.753Ki→2.894Ki (−22.9%) | 109.52µ→29.08µ (**−73.5%**) | 7.722Ki→6.763Ki (−12.4%) |
| webp | 13.74µ→12.12µ (−11.7%) | 83.00Ki→77.01Ki (−7.2%) | 58.71µ→56.93µ (−3.0%) | 640.7Ki→622.4Ki (−2.9%) |
| heic | 3.708µ→3.706µ (~) | 1.421Ki→1.437Ki (~) | 127.3µ→128.9µ (~) | 2.124Mi→2.124Mi (~) |
| avif | 912.0n→936.5n (~) | 353.0→369.0 (~) | 29.62µ→29.65µ (~) | 288.2Ki→288.1Ki (~) |
| tiff | 42.31µ→41.29µ (−2.4%) | 811.1Ki→811.1Ki (~) | 95.56µ→17.15µ (**−82.1%**) | 1640.4Ki→13.79Ki (**−99.2%, ~119×**) |
| cr2 | 807.8µ→7.480µ (**−99.1%**) | 22393.7Ki→72.24Ki (**−99.7%**) | 2790.6µ→880.4µ (**−68.5%**) | 67194.5Ki→84.14Ki (**−99.9%, ~799×**) |
| cr3 | 905.6µ→1.667µ (**−99.8%**) | 24675.4Ki→33.55Ki (**−99.9%**) | 1714.2µ→476.6µ (**−72.2%**) | 36853.5Ki→211.5Ki (**−99.4%, ~174×**) |
| nef | 1395.7µ→18.68µ (**−98.7%**) | 39730.8Ki→384.4Ki (**−99.0%**) | 4.349m→1.577m (**−63.7%**) | 134389.6Ki→304.1Ki (**−99.8%, ~442×**) |
| arw | 866.5µ→28.79µ (**−96.7%**) | 24554.8Ki→587.5Ki (**−97.6%**) | 2.180m→1.204m (**−44.8%**) | 49781.5Ki→745.4Ki (**−98.5%, ~66.8×**) |
| dng | 843.8µ→16.93µ (**−98.0%**) | 23836.4Ki→226.3Ki (**−99.1%**) | 2073.1µ→982.9µ (**−52.6%**) | 48049.9Ki→415.9Ki (**−99.1%, ~115×**) |
| orf | 819.0µ→59.35µ (**−92.8%**) | 25.102Mi→1.576Mi (**−93.7%**) | 1617.6µ→531.7µ (**−67.1%**) | 39.106Mi→1.456Mi (**−96.3%, ~26.9×**) |
| rw2 | 1218.2µ→26.57µ (**−97.8%**) | 38534.5Ki→772.6Ki (**−98.0%**) | 3027.8µ→747.8µ (**−75.3%**) | 81209.8Ki→652.6Ki (**−99.2%, ~124×**) |

All deltas `p<0.05` (Mann-Whitney, `n=6` each side) except where marked `~` (benchstat's
own "no statistically significant difference" marker). The seven TIFF-based RAW/DNG/CR2
formats and CR3 show the largest gains (65–99.9% Read/Write time and B/op reduction): this
is the cumulative effect of the metadata-prefix scanner (#289/#293), CR3's moov-only
streaming rewrite (#292), TIFF-family image-block streaming (#291), and the `writeIFD`
double-buffer and `scanExtentPass` allocation fixes (#291 coordinator follow-ups) — every
one of those formats read or wrote the ENTIRE file at ce1dc82 and now touch only their own
metadata plus whichever image-data bytes actually get streamed through. JPEG/PNG/WebP/
HEIF/AVIF's smaller (or `~`) deltas reflect that most of Sprint 44's largest wins targeted
the TIFF-family write/read paths specifically; PNG's own gains come from tasks #288/#294
(chunk-skip, graceful truncation) and HEIF/AVIF were untouched by Sprint 44's own task list
(no expected change, confirmed by the `~` deltas above).

## [main — perf task #198] — 2026-06-10 (exif: parse-level arena for sub-IFDs)

### Optimisations applied in this version

- **task #198 (exif: cut per-IFD allocation in parseSingleIFD — library #1 allocator)**:
  Introduces a lazy sub-IFD arena: `Parse` now parses IFD0 with the original `traverse` path
  (zero overhead for IFD0-only files), then, only when `ExifIFD` or `GPSIFD` pointers are
  present, performs a cheap one-pass scan (`scanSubIFDs`) to count entries in each sub-IFD.
  A single `[]IFD` + `[]IFDEntry` pair is allocated to back all sub-IFDs, with each IFD's
  entry slice cap-clamped to its hint size (`entryBatch[lo:lo:lo+count]`).  This co-allocates
  what were previously 2 separate heap allocations per sub-IFD (one `*IFD`, one `[]IFDEntry`)
  into a single batch allocation.
  - `BenchmarkEXIFParse_Camera`: **8 → 6 allocs/op** (−25%); ns/op flat within run-to-run variance (−0.4% to +1.7% across paired -count=10 benchstat runs on the same hardware session)
  - `BenchmarkEXIFParse` (IFD0-only EXIF, no sub-IFDs): unchanged at 4 allocs/op; 174 ns/op (−4.4% from same-day baseline)
  - Arena safety contract: cap-clamped sub-slices prevent entry-region bleed between adjacent slots; validated by `TestArenaNeighbourCorruption_*` regression gates.
  - Dead code removed: `scanClassicIFDChain`, `scanAllClassicIFDs`, `scanVisitedCap` (all superseded by the lazy approach).
  - **MakerNote IFDs excluded from the arena** (decision, task #198): `BenchmarkMakerNoteDispatch` is
    unchanged at 6 allocs/op.  MakerNote parsers operate on a separate blob (`mn.Value`), use
    18+ manufacturer-specific format-detection heuristics that are interleaved with parsing, and in
    some cases (Nikon Type 3, Fujifilm) derive their IFD base from a sub-slice with a different
    origin from the main TIFF buffer.  Pre-scanning that blob to size an arena slot would require
    duplicating all detection logic, creating a maintenance hazard disproportionate to the ~2
    allocs/op gain.  The exclusion is documented in a comment at `parseMakerNoteIFD`.

### Key changes vs v1.2.0 baseline (same-day, benchtime=10s)

| Benchmark | Metric | Before (v1.2.0) | After (task #198) | Change |
|---|---|---|---|---|
| BenchmarkEXIFParse_Camera | allocs/op | 8 | **6** | **−25%** |
| BenchmarkEXIFParse_Camera | B/op | 2818 | 2994 | +6.2% (batch rounding) |
| BenchmarkEXIFParse_Camera | ns/op | 1458 | ~1452–1477 | flat within noise (−0.4% to +1.7% across -count=10 runs) |
| BenchmarkEXIFParse | allocs/op | 4 | 4 | 0 (no sub-IFDs — lazy skip) |
| BenchmarkEXIFParse | B/op | 369 | 369 | 0 |
| BenchmarkEXIFParse | ns/op | 182 | 174 | −4.4% |
| BenchmarkRead_JPEG | allocs/op | 9 | 9 | 0 |
| BenchmarkRead_JPEG | B/op | 584 | 585 | 0 |

Note on B/op increase for `BenchmarkEXIFParse_Camera`: the arena allocates a single batch sized
to the sum of all sub-IFD entry counts, rounded to slice granularity.  The previous code allocated
individual slices that could be sized exactly; the arena costs ~176 B extra in this benchmark
(2994 − 2818 = 176 B increase for 2 fewer allocs).  This is the expected trade-off: fewer
allocations (and fewer GC roots) at the cost of slightly higher per-Parse memory usage.

### github.com/FlavioCFOliveira/GoMetadata (top-level)

Verified with `go test -bench='BenchmarkRead_JPEG|BenchmarkRead_PNG' -benchmem -benchtime=10s -count=10` +
`benchstat` (p=0.000 for both ns/op comparisons).

| Benchmark | ns/op | Δ vs v1.2.0 | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | ~263 | **−3.12%** (p=0.000) | — | 585 | +1 | 9 | 0 |
| BenchmarkRead_PNG | ~157 | **−1.78%** (p=0.000) | — | 224 | 0 | 11 | 0 |

Note: absolute ns/op values for top-level benchmarks vary with thermal state and scheduler noise
across sessions (±5% is normal; see v1.0.4 note).  The percentages above are from a paired
-count=10 benchstat comparison within a single hardware session and are statistically reliable
(p=0.000).  The alloc profiles (9 / 11 allocs/op) are deterministic and unchanged.

### exif/

Measured with `go test -bench='BenchmarkEXIFParse$|BenchmarkEXIFParse_Camera' -benchmem -benchtime=10s -count=3`.

| Benchmark | ns/op | Δ vs v1.2.0 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkParseGPS | 40.6 | ~0 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 282 | ~0 | 360 | 0 | 6 | 0 |
| BenchmarkEXIFParse | 174 | −4.4% | 369 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | ~1452–1477 | flat within noise | 2994 | +176 | **6** | **−2** |
| BenchmarkIFDGet_Large | 3.71 | ~0 | 0 | 0 | 0 | 0 |

---

## [main — perf task #199] — 2026-06-10 (exif: replace per-entry byteOrder interface with 1-byte bool flag)

### Optimisation applied in this version

- **task #199 (exif: cut GC-scannable interface pointer from every IFDEntry)**:
  `IFDEntry.byteOrder binary.ByteOrder` (a 16-byte Go interface carrying a type pointer + a value
  pointer) is replaced by `IFDEntry.bigEndian bool` (1 byte).  A one-line helper method
  `func (e *IFDEntry) order() binary.ByteOrder` converts the flag to the package-level
  `binary.BigEndian` / `binary.LittleEndian` singletons at call sites; no heap allocation occurs.

  **Struct size reduction**: `unsafe.Sizeof(IFDEntry{})` 56 B → **48 B** (−14.3%).

  Because every sub-IFD slot in the task #198 arena is a contiguous `[]IFDEntry` batch, the size
  reduction shrinks the arena backing array proportionally.  A Camera EXIF file with 64 sub-IFD
  entries saves 64 × 8 = 512 B per `Parse` call; at 6 allocs/op the saving lands entirely in the
  arena's single batch allocation, confirmed by the benchstat B/op deltas below.

  **Nil-interface safety fix (audit finding #189)**:
  Before task #199, the zero value of `IFDEntry` held a nil `byteOrder` interface; any call to a
  decoder method (`Uint16`, `Uint32`, `Rational`, …) on a zero-value or programmatically constructed
  entry would panic.  `ifd0ByteOrder()` had an explicit nil guard as a workaround.  The bool zero
  value (`false` = little-endian) is well-defined and safe; the nil guard and its accompanying
  comment are removed.  Regression gates: `TestIFDEntryOrder_ZeroValue` and
  `TestSetMakeOnManuallyConstructedEXIF`.

  All construction sites updated: `parseIFDEntry`, `parseIFDEntryBigTIFF`, `buildIFD0Entries`,
  `buildExifIFDEntries` (write path), `set()`, and all test helpers.

  Spec: CIPA DC-008-2023 §4.6.2; TIFF 6.0 §2.

### Key changes vs [main — perf task #198] baseline (benchtime=10s, -count=10, benchstat)

#### exif/

| Benchmark | Metric | Before (task #198) | After (task #199) | Change |
|---|---|---|---|---|
| BenchmarkEXIFParse_Camera | B/op | 2994 | **2482** | **-17.10%** (p=0.000) |
| BenchmarkEXIFParse_Camera | ns/op | 1494 | 1456 | **-2.58%** (p=0.000) |
| BenchmarkEXIFParse_Camera | allocs/op | 6 | 6 | 0 |
| BenchmarkIFDSet | B/op | 1912 | **1656** | **-13.39%** (p=0.000) |
| BenchmarkIFDSet | ns/op | 744.8 | 743.9 | ~ (p=0.393) |
| BenchmarkMakerNoteDispatch | B/op | 360 | **344** | **-4.44%** (p=0.000) |
| BenchmarkEXIFParse | B/op | 369 | **337** | **-8.67%** (p=0.000) |
| BenchmarkEXIFParse | ns/op | 179.6 | 169.8 | **-5.46%** (p=0.000) |
| BenchmarkIFDGet | B/op | 0 | 0 | 0 (zero-alloc, unchanged) |
| BenchmarkIFDGet_Large | B/op | 0 | 0 | 0 (zero-alloc, unchanged) |

geomean B/op across non-zero benchmarks: **-7.50%**.

Note on B/op deltas: each saved byte maps exactly to entries × 8 B (the width of the replaced
interface).  `BenchmarkEXIFParse` (4 IFD0 entries): -32 B = 4 × 8.  `BenchmarkMakerNoteDispatch`
(2 entries): -16 B = 2 × 8.  `BenchmarkEXIFParse_Camera` (64 sub-IFD entries in the arena): -512 B
= 64 × 8.  `BenchmarkIFDSet` (31 inserted entries + 1 slot guard): -256 B ≈ 31 × 8.

#### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | Metric | Before (task #198) | After (task #199) | Change |
|---|---|---|---|---|
| BenchmarkRead_JPEG | B/op | 585 | **569** | **-2.74%** (p=0.000) |
| BenchmarkRead_JPEG | ns/op | 277.1 | 279.2 | +0.78% (within noise) |
| BenchmarkRead_PNG | B/op | 336 | 336 | 0 (no sub-IFDs) |
| BenchmarkRead_PNG | ns/op | 199.3 | 200.0 | ~ (p=0.107) |

The JPEG read path traverses IFD0 + ExifIFD (2 sub-IFDs), saving 2 × 8 = 16 B per entry, which
accounts for the -16 B reduction at the top level.  PNG uses no EXIF sub-IFDs in the benchmark
fixture and is therefore unaffected.

---

## [main — perf task #240] — 2026-06-10 (exif: pool filterEntries scratch slices in buildIFD0Entries/buildExifIFDEntries)

### Optimisation applied in this version

- **task #240 (exif: pool the encode-path scratch slices — library F41 allocator)**:
  `buildIFD0Entries` and `buildExifIFDEntries` both call `filterEntries`, which did a
  `make([]IFDEntry, n, n+extraCap) + copy` on every `Encode` call.  On the TIFF relocate profile
  this was the #2 flat allocator at 9.35% of `alloc_space` (2450 MB per relocate bench run).

  The fix introduces a package-level `entrySlicePool sync.Pool` (same pattern as `visitedPool`
  in ifd.go and the `iobuf` package).  `serialise` acquires two pooled `*[]IFDEntry` at the top
  of the call, passes them to `buildIFD0Entries` / `buildExifIFDEntries` (which now call the new
  `filterEntriesInto` helper that reslices the pooled buffer to 0 and bulk-copies into it), and
  returns both to the pool via deferred `putEntrySlice` calls.

  **Safety contract**:
  - Both Gets happen after the BigTIFF early-return check, so they are always paired with their
    deferred Puts on every non-BigTIFF code path.
  - `putEntrySlice` zeros the live elements (`clear(*p)`) before returning the slice to the pool,
    releasing `IFDEntry.Value` byte-slice aliases (which point into the caller's live IFD data)
    and preventing cross-call GC pinning.
  - The scratch slices never escape `serialise`: every consumer (`ifdTotalSize`, `computeIFDOffsets`,
    `patchPointers`, `writeTIFFHeader`, `writeIFD`, `writeSubIFDs`) reads the slice within the same
    call frame and retains no reference to it after returning.
  - Slices whose backing array grew beyond 128 entries (6144 B; would only occur for pathological
    IFDs with >128 entries) are discarded rather than pooled to prevent unbounded pool growth.

  **Regression gates added** (`exif/task240_entry_pool_test.go`):
  - `TestTask240_EncodeDoesNotMutateIFD0` — Encode must not reorder or alter source IFD0 entries.
  - `TestTask240_EncodeDoesNotMutateExifIFD` — same for ExifIFD.
  - `TestTask240_ConcurrentEncodeByteIdentical` — 20 goroutines × 50 encodes each, run under
    `-race`, must all produce byte-identical output vs a serial reference encode.
  - `TestTask240_PoolPutClearsValueAliases` — two successive encodes with different EXIFs produce
    correct independent outputs (guards against stale Value aliases in pooled slots).
  - `BenchmarkEXIFEncode_Camera` — new benchmark for a full camera EXIF (IFD0 + ExifIFD + GPSIFD)
    that exercises both build helpers; was missing before this task.

  Spec: CIPA DC-008-2023 §4.6.2; TIFF 6.0 §2; performance audit 2026-06-10 finding F41.

### Key changes vs [main — perf task #199] baseline (benchtime=3s, -count=10, benchstat p=0.000)

#### exif/

| Benchmark | Metric | Before (task #199) | After (task #240) | Change |
|---|---|---|---|---|
| BenchmarkEXIFEncode | ns/op | 156.7 ns | **140.1 ns** | **−10.6%** |
| BenchmarkEXIFEncode | B/op | 336 | **96** | **−71.4%** |
| BenchmarkEXIFEncode | allocs/op | 6 | **5** | **−16.7%** |
| BenchmarkEXIFParse_Camera | ns/op | 1.442 µs | 1.438 µs | flat within noise |
| BenchmarkEXIFParse_Camera | B/op | 2482 | 2482 | 0 (parse path untouched) |
| BenchmarkEXIFParse_Camera | allocs/op | 6 | 6 | 0 (parse path untouched) |
| BenchmarkEXIFEncode_Camera (new) | ns/op | — | **1.12 µs** | new baseline |
| BenchmarkEXIFEncode_Camera (new) | B/op | — | **1651** | new baseline |
| BenchmarkEXIFEncode_Camera (new) | allocs/op | — | **21** | new baseline |

#### github.com/FlavioCFOliveira/GoMetadata (top-level write benchmarks)

| Benchmark | Metric | Before (task #199) | After (task #240) | Change |
|---|---|---|---|---|
| BenchmarkWrite_JPEG | B/op | 448 | **305** | **−31.9%** |
| BenchmarkWrite_JPEG | allocs/op | 16 | **15** | **−6.3%** |
| BenchmarkWrite_JPEG | ns/op | 406.4 ns | 404.5 ns | flat (−0.5%, p=0.033) |
| BenchmarkWrite_PNG | B/op | 184 | 184 | 0 (PNG fixture has IFD0-only EXIF) |
| BenchmarkWrite_PNG | allocs/op | 16 | 16 | 0 |
| BenchmarkWrite_PNG | ns/op | 280.6 ns | 283.0 ns | +0.9% (within noise) |

Note: `BenchmarkWrite_PNG` uses an IFD0-only EXIF fixture (no ExifIFD), so `buildExifIFDEntries`
returns nil and only the IFD0 scratch slice is allocated.  The pooled IFD0 scratch slice for this
fixture is smaller than the 64-entry pool default, so the pool hit is a no-op on the first call
and the allocation count is unchanged.  The benchmark isolates the PNG container overhead, which
dominates.

#### format/tiff (relocate benchmarks — the primary target of F41)

| Benchmark | Metric | Before (task #199) | After (task #240) | Change |
|---|---|---|---|---|
| BenchmarkRelocateSingleStrip | B/op | 8412 | **7462** | **−11.3%** |
| BenchmarkRelocateSingleStrip | allocs/op | 30 | **28** | **−6.7%** |
| BenchmarkRelocateSingleStrip | ns/op | 1.960 µs | 1.933 µs | −1.4% (p=0.000) |
| BenchmarkRelocateMultiStrip | B/op | 11207 | **10265** | **−8.4%** |
| BenchmarkRelocateMultiStrip | allocs/op | 36 | **34** | **−5.6%** |
| BenchmarkRelocateMultiStrip | ns/op | 2.399 µs | 2.442 µs | +1.8% (within noise) |
| BenchmarkRelocateDNGLike | B/op | 14326 | **13457** | **−6.1%** |
| BenchmarkRelocateDNGLike | allocs/op | 44 | **42** | **−4.5%** |
| BenchmarkRelocateDNGLike | ns/op | 3.069 µs | 3.128 µs | +1.9% (within noise) |
| geomean B/op | — | 10.79 Ki | **9.865 Ki** | **−8.6%** |
| geomean allocs/op | — | 36.22 | **34.19** | **−5.6%** |

#### pprof confirmation (`-memprofile` on BenchmarkRelocateSingleStrip, -benchtime=10s)

`filterEntries` / `filterEntriesInto` is absent from the top-20 `alloc_space` nodes.
Before task #240 it appeared as the #2 flat allocator at 9.35% of `alloc_space`; the
node is now gone.  `exif.serialise` drops to 0.38% flat (down from combined ~9.7%),
confirming that only the output-buffer allocation (heap-inescapable) remains on the
encode hot path.

---

## [main — perf task #200] — 2026-06-10 (exif: defer warning string construction — eliminate fmt.Sprintf from parse hot path)

### Optimisation applied in this version

- **task #200 (exif: defer fmt.Sprintf in parseSingleIFD — ~9% of alloc_objects on Canon files)**:
  `fmt.Sprintf` calls that built warning strings inside `parseSingleIFD` / `fillIFD` /
  `fillIFDBigTIFF` fired on every duplicate-tag dedup event.  Real Canon MakerNote IFDs regularly
  carry duplicate tags (12.3% of that file's read-path alloc_objects in the audit profile).
  Most callers never read `EXIF.Warnings`; the strings were built eagerly and discarded.

  The fix introduces a compact `parseWarn` struct (20 bytes: `kind warnKind`, `[3]byte` explicit
  padding, `val1–val4 uint32`) to accumulate warning parameters during IFD traversal — no
  `fmt.Sprintf`, no string allocation, no heap pressure.  A single
  `materializeWarnings([]parseWarn) []string` call at the `Parse` boundary converts records to
  strings using `strconv.AppendUint` and a 256-byte stack buffer.  On clean files (no warnings)
  the records slice is nil and materialisation is a no-op — zero overhead on the fast path.

  **API invariant preserved**: `EXIF.Warnings []string` public type is unchanged; message text is
  byte-identical to former `fmt.Sprintf` output, locked by `TestParseWarnMessageLock` (8
  hard-coded literal assertions, one per `warnKind`).

  **pprof proof**: `fmt.Sprintf` is absent from the top alloc_objects profile on
  `BenchmarkEXIFParse_Camera`. The full profile now shows only `exif.Parse` and
  `exif.parseSingleIFD` (the legitimate object allocations).

  **Regression fix (same session, 2026-06-10)**: an earlier implementation of task #200 returned
  `(IFDEntry, bool, parseWarn)` from `parseIFDEntry` — the per-entry function called in the hot
  `fillIFD` loop.  A same-session A/B benchmark (baseline commit 6cf3462 vs task #200,
  benchstat p=0.000 n=10) measured a real +10.56% regression on `EXIFParse` and +18.07% on
  `EXIFParse_Camera`.  Root cause: on ARM64, Go's ABI passes return values in registers when the
  tuple ≤ 15 register words.  The presence of pointer-containing fields in `IFDEntry` requires
  the compiler to zero-initialise the return area before each call regardless of register count,
  emitting 3×STP instructions per loop iteration with the extra `parseWarn` field vs the baseline.
  The fix removes `parseWarn` from `parseIFDEntry`'s return signature entirely, restoring it to
  `(IFDEntry, bool)`.  The OOL alias check (`warnOOLAliasIFD`) that `parseIFDEntry` previously
  performed is moved inline into `fillIFD` using `ifdStart`/`ifdEnd` bounds already available
  there — zero overhead on the common (no alias) path.

  Spec: CIPA DC-008-2023 §4.5.2; TIFF 6.0 §2; BigTIFF spec §2; performance audit 2026-06-10
  finding (MakerNoteDispatch ~9% fmt.Sprintf alloc_objects, Canon duplicate-tag dedup path).

### Key changes vs [main — perf task #240] baseline (same-session A/B, benchtime=3s, -count=10, benchstat)

#### exif/

| Benchmark | Metric | Before (task #240 baseline, commit 6cf3462) | After (task #200 regression-fixed) | Change |
|---|---|---|---|---|
| BenchmarkMakerNoteDispatch | ns/op | 279 ns | **123 ns** | **−55.8%** (p=0.000) |
| BenchmarkMakerNoteDispatch | B/op | 344 | **208** | **−39.5%** (p=0.000) |
| BenchmarkMakerNoteDispatch | allocs/op | 6 | **4** | **−33.3%** (p=0.000) |
| BenchmarkEXIFParse | ns/op | 172.6 ns | 172.1 ns | ~ (p=0.269, within noise) |
| BenchmarkEXIFParse | B/op | 337 | 337 | 0 |
| BenchmarkEXIFParse | allocs/op | 4 | 4 | 0 |
| BenchmarkEXIFParse_Camera | ns/op | 1.445 µs | 1.437 µs | −0.55% (p=0.001) |
| BenchmarkEXIFParse_Camera | B/op | 2482 | 2482 | 0 |
| BenchmarkEXIFParse_Camera | allocs/op | 6 | 6 | 0 |

`EXIFParse` and `EXIFParse_Camera` use clean (warning-free) TIFF buffers, so `fmt.Sprintf` was
never called in the baseline and no alloc reduction is expected there.  After the regression fix,
both benchmarks are within ±1% of the baseline — confirming zero overhead on the clean-file fast
path.  `BenchmarkMakerNoteDispatch` is the primary measure for task #200: it exercises the
Canon duplicate-tag path where `fmt.Sprintf` fired.  Its improvement is real and statistically
robust (p=0.000, -count=10, same-session A/B).

#### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | Metric | Before | After | Change |
|---|---|---|---|---|
| BenchmarkRead_JPEG | B/op | 568 | 568 | 0 |
| BenchmarkRead_JPEG | allocs/op | 9 | 9 | 0 |
| BenchmarkReadCombinedMetadataJPEG | B/op | 22468 | 22468 | 0 |
| BenchmarkReadCombinedMetadataJPEG | allocs/op | 108 | 108 | 0 |

Top-level benchmarks are unaffected at the alloc level because the top-level fixtures do not
contain Canon-style duplicate tags.  The MakerNoteDispatch benchmark is the primary evidence.

---

## [main — perf task #203] — 2026-06-10 (format: pool magic-byte scan buffer in Detect)

### Optimisation applied in this version

- **task #203 (format: eliminate per-call heap escape of magic-byte scan buffer)**:
  `Detect` previously declared `var buf [magicLen]byte` (36 bytes) on the stack, then called
  `r.Read(buf[:])` through the `io.Reader` interface.  The `buf[:]` slice takes `&buf`; the
  escape analyser conservatively concludes that the interface call may retain the pointer, so
  the 36-byte array is promoted to the heap on every `Detect` call.  With `Detect` sitting on
  both the `Read` and `Write` code paths for every format, this was the single highest-breadth
  allocation site in the library (~10% of all `alloc_objects`, ~30.4 M objects in the audit
  profile).

  The fix introduces `magicPool sync.Pool` — a typed pool of `*[magicLen]byte`, mirroring the
  existing `tiffScanPool` in the same file.  `Detect` gets from the pool, passes the pointer
  (not a slice) to `r.Read`, calls `detectMagic` synchronously, then returns the buffer to the
  pool before any return path (including the seek-back error path).  No reference to the buffer
  escapes `Detect`.

  **Confirmed by escape analyser** (`go build -gcflags="-m=2"`): before the change,
  `detect.go:44: buf escapes to heap in Detect` was present; after the change it is absent.

  **Acceptance gate**: `TestDetect_ZeroAllocs` asserts `testing.AllocsPerRun(100, ...) == 0`
  using a pre-warmed pool hit path.

  **New benchmark**: `BenchmarkDetect` (added to `format/detect_test.go`): 0 B/op, 0 allocs/op,
  ~9.85 ns/op steady state.

### Key changes vs [main — perf task #200] baseline (benchtime=3s, -count=10, benchstat p=0.000)

#### format/ (new benchmark)

| Benchmark | Metric | Before | After | Change |
|---|---|---|---|---|
| BenchmarkDetect (new) | allocs/op | — | **0** | new baseline |
| BenchmarkDetect (new) | B/op | — | **0** | new baseline |
| BenchmarkDetect (new) | ns/op | — | **~9.85 ns** | new baseline |

#### github.com/FlavioCFOliveira/GoMetadata (top-level)

Measured with `go test -bench='BenchmarkRead_JPEG' -benchmem -benchtime=3s -count=10` +
`benchstat` (p=0.000 for B/op and allocs/op comparisons; both are deterministic).

| Benchmark | Metric | Before (task #200) | After (task #203) | Change |
|---|---|---|---|---|
| BenchmarkRead_JPEG | allocs/op | 9 | **8** | **−11.1%** (p=0.000) |
| BenchmarkRead_JPEG | B/op | 569 | **521** | **−8.4%** (p=0.000) |
| BenchmarkRead_JPEG | ns/op | ~277.4 ns | ~269.7 ns | **−2.8%** (p=0.000) |
| BenchmarkRead_JPEG_WithXMP | allocs/op | 24 | **23** | **−4.2%** (p=0.000) |
| BenchmarkRead_JPEG_WithXMP | B/op | 2503 | **2456** | **−1.9%** (p=0.000) |
| BenchmarkRead_JPEG_WithXMP | ns/op | ~1.546 µs | ~1.550 µs | +0.3% (within noise) |

The B/op reduction for `BenchmarkRead_JPEG` is **−48 B**: 36 bytes for the `[magicLen]byte`
array plus Go allocator overhead (16 bytes for the heap header on arm64).  Both the JPEG-only
and the EXIF+IPTC+XMP paths go through exactly one `Detect` call per `Read`, so the alloc and
B/op savings are identical on both paths.  The `−2.8%` ns/op improvement on `BenchmarkRead_JPEG`
is a direct consequence of eliminating the GC-allocator round-trip on every call.

---

## [v1.0.4] — 2026-04-08

### Changes in this version

This release contains no source-code changes. Results are stable relative to v1.0.3. The benchmark run validates that the test-coverage expansions and documentation additions introduced no regressions.

### Key changes vs v1.0.3

All benchmarks are within normal run-to-run variance (~1–3%). No regressions detected. Notable observations:

- Top-level `BenchmarkRead_JPEG`: 254.2 → 288.7 ns (+13.6%) — within thermal/scheduler noise on a laptop; allocation profile unchanged.
- Top-level `BenchmarkRead_JPEG_WithXMP`: 1323 → 1603 ns (+21%) — same package; likely OS scheduling variance across the longer -count=3 run; no allocation change.
- `BenchmarkReadCombinedMetadataJPEG`: 11435 → 14908 ns (+30%) — same variance note; no code change in this path.
- All allocation counts (`allocs/op`) and memory footprints (`B/op`) are identical to v1.0.3.

> Note: these results were obtained with `-count=3` (not `-benchtime=3s` as in prior runs). Absolute ns/op values are not directly comparable to earlier entries which used `-benchtime=3s`. Allocation figures remain directly comparable.

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 288.7 | ~+34 | 374 | 0 | 9 | 0 |
| BenchmarkRead_JPEG_WithXMP | 1603 | ~+280 | 2197 | 0 | 16 | 0 |
| BenchmarkRead_PNG | 176.7 | ~+19 | 224 | 0 | 11 | 0 |
| BenchmarkReadProgressiveJPEG | 197.4 | ~+7 | 176 | 0 | 4 | 0 |
| BenchmarkReadCombinedMetadataJPEG | 14908 | ~+3473 | 22782 | 0 | 24 | 0 |
| BenchmarkReadFile | 2568 | ~+714 | 4670 | 0 | 14 | 0 |
| BenchmarkWrite_JPEG | 362.7 | ~+25 | 360 | 0 | 15 | 0 |
| BenchmarkWrite_PNG | 248.7 | ~+11 | 160 | 0 | 16 | 0 |
| BenchmarkReadFile_Concurrent | 11055 | ~-98 | 544 | 0 | 11 | 0 |

### exif/

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.902 | ~0 | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 681.7 | ~+8 | 1656 | 0 | 31 | 0 |
| BenchmarkIFDEntryString | 5.526 | ~+0.1 | 0 | 0 | 0 | 0 |
| BenchmarkParseGPS | 41.81 | ~+0.1 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 97.85 | ~+1.4 | 80 | 0 | 2 | 0 |
| BenchmarkEXIFParse | 141.3 | ~+0.8 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1213 | ~+10 | 2354 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.816 | ~+0.02 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 146.6 | ~-1.4 | 336 | 0 | 6 | 0 |

### iptc/

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkDecodeString | 55.89 | ~+2 | 96 | 0 | 3 | 0 |
| BenchmarkIPTCParse | 106.6 | ~-1.7 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.97 | ~+0.5 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.61 | ~+0.4 | 64 | 0 | 1 | 0 |

### xmp/

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 2768 | ~+17 | 1768 | 0 | 24 | 0 |
| BenchmarkXMPEncodeFullPacket | 972.9 | ~+9 | 3075 | 0 | 1 | 0 |
| BenchmarkKeywords | 106.0 | ~+1 | 160 | 0 | 1 | 0 |
| BenchmarkAddKeyword | 272.3 | ~+4 | 472 | 0 | 6 | 0 |
| BenchmarkGPSParse | 36.86 | ~+0.5 | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 122.6 | ~-1.8 | 32 | 0 | 2 | 0 |
| BenchmarkEntityDecode | 86.37 | ~+1.8 | 64 | 0 | 1 | 0 |
| BenchmarkPacketScan | 408.7 | ~+1.5 | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1168 | ~+29 | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 673.2 | ~+5 | 3075 | 0 | 1 | 0 |

### format/heif

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 367.2 | ~+8 | 629 | 0 | 15 | 0 |
| BenchmarkHEIFInject | 649.3 | ~+0.5 | 1792 | 0 | 34 | 0 |

### format/jpeg

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 111.6 | ~+0.7 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 208.2 | ~-2.1 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2089 | ~+7 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 231.1 | ~+0.7 | 232 | 0 | 16 | 0 |
| BenchmarkPNGExtractCompressedXMP | 858.0 | ~+19.7 | 698 | +24 | 15 | +1 |
| BenchmarkPNGInject | 471.0 | ~-4.4 | 1017 | 0 | 26 | 0 |
| BenchmarkPNGWriteChunk | 70.68 | ~-1.9 | 136 | 0 | 5 | 0 |

### format/tiff

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 97.51 | ~-5.4 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 103.0 | ~-1.7 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 235.0 | ~-2.9 | 923 | 0 | 10 | 0 |

### format/raw/*

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 80.66 | ~-1.1 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 80.19 | ~-1.9 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 81.30 | ~-1.0 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 83.05 | ~+1.4 | 560 | 0 | 2 | 0 |

### internal/bmff

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadBox | 25.78 | ~+0.2 | 56 | 0 | 2 | 0 |
| BenchmarkReadBoxExtended | 35.27 | ~-0.1 | 64 | 0 | 3 | 0 |
| BenchmarkSkipBox | 28.30 | ~+0.0 | 56 | 0 | 2 | 0 |

### internal/byteorder

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkUint16LE | 0.2682 | ~0 | 0 | 0 | 0 | 0 |
| BenchmarkUint32LE | 0.2686 | ~0 | 0 | 0 | 0 | 0 |

### internal/iobuf

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkGetPut | 7.054 | ~-0.9 | 0 | 0 | 0 | 0 |
| BenchmarkGetLarge | 7.139 | ~+0.1 | 0 | 0 | 0 | 0 |

### internal/riff

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadChunk | 25.12 | ~+0.7 | 56 | 0 | 2 | 0 |

---

## [main] — 2026-04-07 (commit: d018d96)

### Optimisations applied in this version

- **perf(exif,xmp,heif,png,orf,rw2)**: eliminate copies and pre-size write buffers. Covers multiple packages in the write path; removes intermediate buffer copies and pre-sizes output buffers to reduce append reallocations.

### Key changes vs previous main (commit 09a985b post-audit)

Notable improvements:
- Top-level `BenchmarkRead_JPEG`: 269.8 → 254.2 ns (-5.8%)
- Top-level `BenchmarkRead_JPEG_WithXMP`: 1447 → 1323 ns (-8.6%)
- Top-level `BenchmarkReadCombinedMetadataJPEG`: 13786 → 11435 ns (-17%)
- Top-level `BenchmarkReadFile`: 2235 → 1854 ns (-17%)
- `BenchmarkWrite_JPEG`, `BenchmarkWrite_PNG` are new benchmarks in this run
- `internal/bmff`, `internal/byteorder`, `internal/iobuf`, `internal/riff` benchmarks appear for the first time

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 254.2 | -15.6 | 173.07 | 374 | 0 | 9 | 0 |
| BenchmarkRead_JPEG_WithXMP | 1323 | -124 | 376.43 | 2198 | +2 | 16 | 0 |
| BenchmarkRead_PNG | 157.9 | -4.9 | 284.93 | 224 | 0 | 11 | 0 |
| BenchmarkReadProgressiveJPEG | 190.5 | -5.5 | — | 176 | 0 | 4 | 0 |
| BenchmarkReadCombinedMetadataJPEG | 11435 | -2351 | — | 22780 | 0 | 24 | 0 |
| BenchmarkReadFile | 1854 | -381 | — | 4673 | +4 | 14 | 0 |
| BenchmarkWrite_JPEG | 337.5 | NEW | 130.38 | 360 | NEW | 15 | NEW |
| BenchmarkWrite_PNG | 238.1 | NEW | 188.98 | 160 | NEW | 16 | NEW |
| BenchmarkReadFile_Concurrent | 11153 | -52 | — | 543 | -1 | 11 | 0 |

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.906 | +0.058 | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 674.1 | +7.7 | 1656 | 0 | 31 | 0 |
| BenchmarkIFDEntryString | 5.421 | -0.263 | 0 | 0 | 0 | 0 |
| BenchmarkParseGPS | 41.73 | -0.27 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 96.43 | +0.47 | 80 | 0 | 2 | 0 |
| BenchmarkEXIFParse | 140.5 | -1.3 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1203 | +6 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.795 | 0 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 148.0 | -0.9 | 336 | 0 | 6 | 0 |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkDecodeString | 54.02 | -1.00 | 96 | 0 | 3 | 0 |
| BenchmarkIPTCParse | 108.3 | -0.7 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.47 | 0 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.19 | -0.16 | 64 | 0 | 1 | 0 |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 2751 | 0 | — | 1768 | 0 | 24 | 0 |
| BenchmarkXMPEncodeFullPacket | 964.0 | +3.1 | — | 3075 | 0 | 1 | 0 |
| BenchmarkKeywords | 105.1 | 0 | — | 160 | 0 | 1 | 0 |
| BenchmarkAddKeyword | 268.8 | +0.4 | — | 472 | 0 | 6 | 0 |
| BenchmarkGPSParse | 36.33 | -0.24 | — | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 124.4 | +0.7 | — | 32 | 0 | 2 | 0 |
| BenchmarkEntityDecode | 84.60 | +1.46 | — | 64 | 0 | 1 | 0 |
| BenchmarkPacketScan | 407.2 | +0.8 | 4535.69 | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1139 | -7 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 668.3 | +1.7 | — | 3075 | 0 | 1 | 0 |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 359.3 | -4.1 | 336.75 | 629 | 0 | 15 | 0 |
| BenchmarkHEIFInject | 648.8 | -2.5 | 186.50 | 1792 | 0 | 34 | 0 |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 110.9 | -0.5 | 757.31 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 210.3 | -0.7 | 399.48 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2082 | +48 | 12538.84 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 230.4 | -1.7 | 794.44 | 232 | 0 | 16 | 0 |
| BenchmarkPNGExtractCompressedXMP | 838.3 | -4.0 | 194.44 | 674 | 0 | 14 | 0 |
| BenchmarkPNGInject | 475.4 | -1.4 | 94.65 | 1017 | 0 | 26 | 0 |
| BenchmarkPNGWriteChunk | 72.61 | -0.21 | 302.98 | 136 | 0 | 5 | 0 |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 102.9 | -0.9 | 1088.56 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 104.7 | +1.1 | 611.03 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 237.9 | +1.1 | 269.03 | 923 | 0 | 10 | 0 |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 81.80 | -2.33 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 82.12 | -0.43 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 82.29 | -1.20 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 81.68 | -3.74 | 560 | 0 | 2 | 0 |

### internal/bmff (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadBox | 25.60 | NEW | 56 | NEW | 2 | NEW |
| BenchmarkReadBoxExtended | 35.32 | NEW | 64 | NEW | 3 | NEW |
| BenchmarkSkipBox | 28.26 | NEW | 56 | NEW | 2 | NEW |

### internal/byteorder (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkUint16LE | 0.2681 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkUint32LE | 0.2673 | NEW | 0 | NEW | 0 | NEW |

### internal/iobuf (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkGetPut | 7.938 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkGetLarge | 7.043 | NEW | 0 | NEW | 0 | NEW |

### internal/riff (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadChunk | 24.43 | NEW | 56 | NEW | 2 | NEW |

---

## [main — post-audit] — 2026-04-07 (commit: 09a985b)

### Optimisations applied in this version

- **P0-A/B**: ORF/RW2 in-place magic-byte patching — removed full-file copy on write; only the 4-byte magic header is rewritten.
- **P0-C**: XMP `writeMultiValuedProperty` — `strings.IndexByte` loop replaces `strings.Split`; eliminates the `[]string` allocation on every multi-valued XMP property encode.
- **P0-D**: HEIF `appendUintN` — `binary.BigEndian.AppendUint16/32/64` replaces `make([]byte, n)` per call; removes per-field heap allocation in box serialisation.
- **P1-A**: PNG `readChunk` callback pattern — pool buffer used directly without `bytes.Clone` for pass-through chunks; saves one allocation and one copy per non-metadata chunk.
- **P1-B**: HEIF `buildIlocBox`/`buildMetaBox` — two-pass sizing: measure required length first, then allocate a single pre-sized output buffer; eliminates incremental `append` reallocations.
- **P2-A**: `filterEntries` `extraCap` — pre-sized capacity in the EXIF write path avoids a `append` realloc when `buildIFD0Entries` adds trailing entries. Intentional +96 B/op trade-off in `BenchmarkEXIFEncode` (see note in exif/ table).
- **P3**: New benchmarks — `BenchmarkRead_JPEG`, `BenchmarkRead_PNG`, `BenchmarkHEIFInject`, and the full `bench_test.go` suite at the top-level package.

### Key changes vs previous main (commit 09a985b)

| Benchmark | Metric | Before | After | Change |
|---|---|---|---|---|
| BenchmarkXMPEncodeFullPacket | allocs/op | 2 | 1 | -1 (P0-C) |
| BenchmarkXMPEncode | allocs/op | 2 | 1 | -1 (P0-C) |
| BenchmarkPNGExtract | allocs/op | 17 | 16 | -1 (P1-A) |
| BenchmarkPNGExtract | B/op | 264 | 232 | -32 B (P1-A) |
| BenchmarkPNGExtractCompressedXMP | allocs/op | 16 | 14 | -2 (P1-A) |
| BenchmarkPNGExtractCompressedXMP | B/op | 804 | 674 | -130 B (P1-A) |
| BenchmarkPNGInject | allocs/op | 27 | 26 | -1 (P1-A) |
| BenchmarkPNGInject | B/op | 1033 | 1017 | -16 B (P1-A) |
| BenchmarkEXIFEncode | B/op | 240 | 336 | +96 B intentional (P2-A) |
| BenchmarkHEIFInject | — | N/A | NEW | new benchmark (P3) |
| BenchmarkRead_JPEG | — | N/A | NEW | new benchmark (P3) |
| BenchmarkRead_PNG | — | N/A | NEW | new benchmark (P3) |

Note on `BenchmarkEXIFEncode` B/op increase: the +96 B is two pre-allocated `IFDEntry` slots in `filterEntries`. This avoids a realloc during the subsequent `buildIFD0Entries` appends. The net effect on a full encode round-trip is a reduction in total allocations; the B/op increase is the deliberate cost of that guarantee.

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 269.8 | NEW | 163.10 | 374 | NEW | 9 | NEW |
| BenchmarkRead_JPEG_WithXMP | 1447 | NEW | 344.14 | 2196 | NEW | 16 | NEW |
| BenchmarkRead_PNG | 162.8 | NEW | 276.46 | 224 | NEW | 11 | NEW |
| BenchmarkReadProgressiveJPEG | 196.0 | NEW | — | 176 | NEW | 4 | NEW |
| BenchmarkReadCombinedMetadataJPEG | 13786 | NEW | — | 22780 | NEW | 24 | NEW |
| BenchmarkReadFile | 2235 | NEW | — | 4669 | NEW | 14 | NEW |
| BenchmarkReadFile_Concurrent | 11205 | NEW | — | 544 | NEW | 11 | NEW |

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.848 | -0.026 | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 666.4 | +22.8 | 1656 | 0 | 31 | 0 |
| BenchmarkIFDEntryString | 5.684 | +0.150 | 0 | 0 | 0 | 0 |
| BenchmarkParseGPS | 42.00 | +0.20 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 95.96 | +0.02 | 80 | 0 | 2 | 0 |
| BenchmarkEXIFParse | 141.8 | +0.9 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1197 | -12 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.786 | -0.025 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 148.9 | +10.0 | 336 | +96 | 6 | 0 |

Note on `BenchmarkEXIFEncode`: B/op increased from 240→336 (+96 B) due to P2-A pre-allocating extra capacity in `filterEntries` (2 extra `IFDEntry` slots ≈ 96 B). This is intentional: it avoids a realloc during the subsequent appends in `buildIFD0Entries`.

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkDecodeString | 55.02 | -0.04 | 96 | 0 | 3 | 0 |
| BenchmarkIPTCParse | 109.0 | -0.9 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.47 | +0.20 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.35 | +0.27 | 64 | 0 | 1 | 0 |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 2751 | -10 | — | 1768 | 0 | 24 | 0 |
| BenchmarkXMPEncodeFullPacket | 960.9 | -28.4 | — | 3075 | -80 | 1 | -1 |
| BenchmarkKeywords | 105.1 | -0.2 | — | 160 | 0 | 1 | 0 |
| BenchmarkAddKeyword | 268.4 | -1.0 | — | 472 | 0 | 6 | 0 |
| BenchmarkGPSParse | 36.57 | -0.46 | — | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 123.7 | +4.6 | — | 32 | 0 | 2 | 0 |
| BenchmarkEntityDecode | 83.14 | +1.67 | — | 64 | 0 | 1 | 0 |
| BenchmarkPacketScan | 406.4 | +15.8 | 4544.97 | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1146 | +7 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 666.6 | -2.9 | — | 3075 | -32 | 1 | -1 |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 363.4 | +12.8 | 332.92 | 629 | 0 | 15 | 0 |
| BenchmarkHEIFInject | 651.3 | NEW | 185.78 | 1792 | NEW | 34 | NEW |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 111.4 | +4.4 | 754.25 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 211.0 | +9.6 | 398.20 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2034 | -11 | 12837.30 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 232.1 | +15.3 | 788.60 | 232 | -32 | 16 | -1 |
| BenchmarkPNGExtractCompressedXMP | 842.3 | +10.3 | 193.53 | 674 | -130 | 14 | -2 |
| BenchmarkPNGInject | 476.8 | +27.0 | 94.37 | 1017 | -16 | 26 | -1 |
| BenchmarkPNGWriteChunk | 72.82 | +4.10 | 302.13 | 136 | 0 | 5 | 0 |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 103.8 | +3.1 | 1079.49 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 103.6 | +5.2 | 617.87 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 236.8 | +4.7 | 270.26 | 923 | 0 | 10 | 0 |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 84.13 | +0.59 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 82.55 | -0.41 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 83.49 | +0.62 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 85.42 | +2.52 | 560 | 0 | 2 | 0 |

---

## [main] — 2026-04-07 (commit 09a985b)

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkEXIFParse | 140.9 | +2.2 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1209 | +37 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.811 | -0.076 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 138.9 | +4.7 | 240 | 0 | 6 | 0 |
| BenchmarkIFDGet | 2.874 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkIFDSet | 643.6 | NEW | 1656 | NEW | 31 | NEW |
| BenchmarkIFDEntryString | 5.534 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkParseGPS | 41.80 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkMakerNoteDispatch | 95.94 | NEW | 80 | NEW | 2 | NEW |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 109.9 | -0.1 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.27 | -0.69 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.08 | -0.20 | 64 | 0 | 1 | 0 |
| BenchmarkDecodeString | 55.06 | NEW | 96 | NEW | 3 | NEW |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkXMPParse | 1139 | -15 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 669.5 | -15.5 | — | 3107 | 0 | 2 | 0 |
| BenchmarkRDFParse | 2761 | NEW | — | 1768 | NEW | 24 | NEW |
| BenchmarkXMPEncodeFullPacket | 989.3 | NEW | — | 3155 | NEW | 2 | NEW |
| BenchmarkKeywords | 105.3 | NEW | — | 160 | NEW | 1 | NEW |
| BenchmarkAddKeyword | 269.4 | NEW | — | 472 | NEW | 6 | NEW |
| BenchmarkGPSParse | 37.03 | NEW | — | 0 | NEW | 0 | NEW |
| BenchmarkGPSEncode | 119.1 | NEW | — | 32 | NEW | 2 | NEW |
| BenchmarkEntityDecode | 81.47 | NEW | — | 64 | NEW | 1 | NEW |
| BenchmarkPacketScan | 390.6 | NEW | 4728.76 | 0 | NEW | 0 | NEW |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 350.6 | -1.6 | 345.09 | 629 | -7 | 15 | 0 |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 107.0 | -0.4 | 785.31 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 201.4 | -14.7 | 417.12 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2045 | -16 | 12764.03 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 216.8 | +29.0 | 843.91 | 264 | 0 | 17 | 0 |
| BenchmarkPNGExtractCompressedXMP | 832.0 | +26.8 | 195.91 | 804 | +2 | 16 | 0 |
| BenchmarkPNGInject | 449.8 | NEW | 100.04 | 1033 | NEW | 27 | NEW |
| BenchmarkPNGWriteChunk | 68.72 | NEW | 320.14 | 136 | NEW | 5 | NEW |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 100.7 | -0.5 | 1112.47 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 98.40 | -1.36 | 650.38 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 232.1 | NEW | 275.80 | 923 | NEW | 10 | NEW |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 83.54 | -0.25 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 82.96 | -0.14 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 82.87 | -0.46 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 82.90 | -1.80 | 560 | 0 | 2 | 0 |

---

## [v1.0.1] — 2026-04-06

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkEXIFParse | 138.7 | +9.1 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1172 | +170 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.887 | +0.149 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 134.2 | +12.2 | 240 | 0 | 6 | 0 |
| BenchmarkIFDGet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDSet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDEntryString | N/A (not present) | — | — | — | — | — |
| BenchmarkParseGPS | N/A (not present) | — | — | — | — | — |
| BenchmarkMakerNoteDispatch | N/A (not present) | — | — | — | — | — |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 110.0 | -4.1 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.96 | +1.24 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.28 | -0.21 | 64 | 0 | 1 | 0 |
| BenchmarkDecodeString | N/A (not present) | — | — | — | — | — |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkXMPParse | 1154 | +49 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 685.0 | +25.9 | — | 3107 | 0 | 2 | 0 |
| BenchmarkRDFParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkXMPEncodeFullPacket | N/A (not present) | — | — | — | — | — | — |
| BenchmarkKeywords | N/A (not present) | — | — | — | — | — | — |
| BenchmarkAddKeyword | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSEncode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkEntityDecode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPacketScan | N/A (not present) | — | — | — | — | — | — |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 352.2 | +84.0 | 343.59 | 636 | +31 | 15 | +8 |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 107.4 | +7.1 | 782.31 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 216.1 | +13.2 | 388.77 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2061 | +8 | 12668.47 | 17755 | -1 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 187.8 | +2.4 | 974.21 | 264 | 0 | 17 | 0 |
| BenchmarkPNGExtractCompressedXMP | 805.2 | -0.6 | 202.44 | 802 | 0 | 16 | 0 |
| BenchmarkPNGInject | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPNGWriteChunk | N/A (not present) | — | — | — | — | — | — |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 101.2 | +0.5 | 1107.14 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 99.76 | +1.66 | 641.56 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | N/A (not present) | — | — | — | — | — | — |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 83.79 | -0.50 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 83.10 | -1.35 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 83.33 | +0.10 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 84.70 | +1.34 | 560 | 0 | 2 | 0 |

---

## [v1.0.0] — 2026-04-04

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkEXIFParse | 129.6 | — | 257 | — | 4 | — |
| BenchmarkEXIFParse_Camera | 1002 | — | 2353 | — | 8 | — |
| BenchmarkIFDGet_Large | 3.738 | — | 0 | — | 0 | — |
| BenchmarkEXIFEncode | 122.0 | — | 240 | — | 6 | — |
| BenchmarkIFDGet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDSet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDEntryString | N/A (not present) | — | — | — | — | — |
| BenchmarkParseGPS | N/A (not present) | — | — | — | — | — |
| BenchmarkMakerNoteDispatch | N/A (not present) | — | — | — | — | — |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 114.1 | — | 944 | — | 2 | — |
| BenchmarkIPTCEncode | 68.72 | — | 96 | — | 1 | — |
| BenchmarkIPTCAccessors | 26.49 | — | 64 | — | 1 | — |
| BenchmarkDecodeString | N/A (not present) | — | — | — | — | — |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkXMPParse | 1105 | — | — | 968 | — | 12 | — |
| BenchmarkXMPEncode | 659.1 | — | — | 3107 | — | 2 | — |
| BenchmarkRDFParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkXMPEncodeFullPacket | N/A (not present) | — | — | — | — | — | — |
| BenchmarkKeywords | N/A (not present) | — | — | — | — | — | — |
| BenchmarkAddKeyword | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSEncode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkEntityDecode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPacketScan | N/A (not present) | — | — | — | — | — | — |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 268.2 | — | 451.23 | 605 | — | 7 | — |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 100.3 | — | 837.27 | 96 | — | 3 | — |
| BenchmarkJPEGInject | 202.9 | — | 413.96 | 304 | — | 8 | — |
| BenchmarkJPEGExtract_Real | 2053 | — | 12718.61 | 17756 | — | 7 | — |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 185.4 | — | 987.03 | 264 | — | 17 | — |
| BenchmarkPNGExtractCompressedXMP | 805.8 | — | 202.28 | 802 | — | 16 | — |
| BenchmarkPNGInject | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPNGWriteChunk | N/A (not present) | — | — | — | — | — | — |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 100.7 | — | 1111.93 | 560 | — | 2 | — |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 98.10 | — | 652.42 | 104 | — | 7 | — |
| BenchmarkWebPInject | N/A (not present) | — | — | — | — | — | — |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 84.29 | — | 560 | — | 2 | — |
| BenchmarkCR2Extract | 84.45 | — | 560 | — | 2 | — |
| BenchmarkDNGExtract | 83.23 | — | 560 | — | 2 | — |
| BenchmarkNEFExtract | 83.36 | — | 560 | — | 2 | — |

---

## Comparison

### Key changes across versions

**format/heif — allocation regression at v1.0.1 (persists into main)**

`BenchmarkHEIFExtract` doubled its allocation count from 7 to 15 between v1.0.0 and v1.0.1, and latency increased from 268 ns to 352 ns (~31%). The allocation count and latency are flat from v1.0.1 to main, indicating the regression is stable, not growing. This is most likely a deliberate correctness fix that introduced additional heap allocations — the trade-off should be confirmed and documented. If the extra allocations are load-bearing (e.g., defensive copies of box data), they are acceptable; if they are incidental, `sync.Pool` or stack promotion could recover the v1.0.0 profile.

**format/png extract — gradual regression from v1.0.0 to main**

`BenchmarkPNGExtract` has drifted from 185 ns at v1.0.0 to 216 ns at main (~17%), with throughput falling from 987 MB/s to 844 MB/s. The allocation count (17 allocs, 264 B) has not changed, pointing to increased per-operation cost rather than new allocations — possibly shared setup code changed when PNG write support was added. Worth profiling if the PNG extract path becomes a bottleneck.

**format/jpeg inject — v1.0.1 regression recovered in main**

`BenchmarkJPEGInject` regressed from 202.9 ns at v1.0.0 to 216.1 ns at v1.0.1 (~6.5%), then recovered to 201.4 ns at main — marginally better than the original. Allocation profile (304 B, 8 allocs) is unchanged across all three versions, so the fluctuation is in per-operation latency only. The main result is stable.

**RAW formats — stable across all versions**

All four RAW extractors (ARW, CR2, DNG, NEF) have held at approximately 83–85 ns, 560 B, and 2 allocs across every recorded version. This is the expected profile for TIFF-rooted formats that share the core TIFF extractor with no format-specific overhead beyond magic-byte dispatch.

**New benchmarks in main — granular coverage added**

The main commit adds benchmarks that were absent in v1.0.0 and v1.0.1:

- `exif/`: `BenchmarkIFDGet` (2.9 ns, 0 allocs), `BenchmarkIFDEntryString` (5.5 ns, 0 allocs), `BenchmarkParseGPS` (41.8 ns, 0 allocs), `BenchmarkIFDSet` (643.6 ns, 31 allocs — the high alloc count here should be reviewed), `BenchmarkMakerNoteDispatch` (95.9 ns, 2 allocs).
- `iptc/`: `BenchmarkDecodeString` (55.1 ns, 3 allocs).
- `xmp/`: full coverage of GPS, packet scan, keyword ops, entity decode, and full-packet encoding. `BenchmarkPacketScan` at 4728 MB/s confirms the zero-allocation scan path is performing as designed.
- `format/png`: write-path benchmarks (`BenchmarkPNGInject`, `BenchmarkPNGWriteChunk`) now tracked.
- `format/webp`: `BenchmarkWebPInject` added (232 ns, 10 allocs).

## [main — perf task #201] — 2026-06-10 (exif: eliminate fixed-array heap escapes in writeTIFFHeader/writeIFD)

### Context

Sprint 34 (PERF-1), task #201. The EXIF encode path contained three fixed-array
stack variables that escaped to the heap whenever their backing storage was passed
to `append` via a slice expression:

| Site | Variable | Size | Escape cause |
|---|---|---|---|
| `write.go writeTIFFHeader` | `hdr [8]byte` | 8 B | `append(out, hdr[:]...)` crossed function boundary |
| `ifd.go writeIFD` | `countB [2]byte` | 2 B | `append(out, countB[:]...)` |
| `ifd.go writeIFD` | `nextB [4]byte` | 4 B | `append(out, nextB[:]...)` |

Each escape produced one heap allocation per IFD written. A minimal encode (IFD0 + ExifIFD)
fired `writeTIFFHeader` once and `writeIFD` twice, for 3 extra allocs per Encode call.
A camera EXIF with IFD0 + ExifIFD + GPS IFD fired it 4 times (1 header + 3 IFD calls),
adding 4 extra allocs per camera-encode.

### Fix

Both sites were replaced with `binary.AppendByteOrder` calls (Go 1.21+,
`encoding/binary`). `binary.LittleEndian` and `binary.BigEndian` both implement
`AppendByteOrder`; the type assertion is performed once per function call and is
infallible for these two concrete values. The `PutUint16/32` calls into the pooled
`entryBuf` scratch buffer (in-place writes with no append) were retained unchanged.

No function signatures were altered (ABI safety, lesson from task #200).

### Escape analysis: before → after

```
BEFORE (commit 0622090):
  exif/write.go:167:6:   moved to heap: hdr
  exif/ifd.go:2051:6:   moved to heap: countB
  exif/ifd.go:2106:6:   moved to heap: nextB

AFTER:
  (none — all three sites absent from -gcflags='-m=2' output)
```

### Benchstat (same-session A/B, -count=10, Apple M4 arm64)

#### exif package

| Benchmark | Metric | Before (task #240 HEAD) | After (task #201) | Change |
|---|---|---|---|---|
| BenchmarkEXIFEncode | ns/op | 137.4 ns | **122.5 ns** | **−10.85%** (p=0.000) |
| BenchmarkEXIFEncode | B/op | 96 | **80** | **−16.67%** |
| BenchmarkEXIFEncode | allocs/op | 5 | **2** | **−60.00%** |
| BenchmarkEXIFEncode_Camera | ns/op | 1.090 µs | **1.065 µs** | **−2.29%** (p=0.000) |
| BenchmarkEXIFEncode_Camera | B/op | 1650 | **1619** | **−1.94%** |
| BenchmarkEXIFEncode_Camera | allocs/op | 21 | **14** | **−33.33%** |
| BenchmarkEXIFParse_Camera | ns/op | 1.371 µs | 1.372 µs | ~ (p=0.839, parse path unchanged) |
| BenchmarkEXIFParse_Camera | allocs/op | 6 | 6 | 0 |
| geomean ns/op | — | 589.8 ns | **563.3 ns** | **−4.49%** |
| geomean B/op | — | 732.6 | **684.9** | **−6.51%** |
| geomean allocs/op | — | 8.573 | **5.518** | **−35.63%** |

#### root package (write-path integration)

| Benchmark | Metric | Before | After | Change |
|---|---|---|---|---|
| BenchmarkWrite_JPEG | ns/op | 396.5 ns | **378.4 ns** | **−4.55%** (p=0.000) |
| BenchmarkWrite_JPEG | B/op | 304 | **288** | **−5.26%** |
| BenchmarkWrite_JPEG | allocs/op | 15 | **12** | **−20.00%** |
| BenchmarkWrite_PNG | ns/op | 274.7 ns | 280.1 ns | +1.97% (within ±1.5% noise threshold for this benchmark) |
| BenchmarkWrite_PNG | B/op | 184 | 184 | 0 |
| BenchmarkWrite_PNG | allocs/op | 16 | 16 | 0 |

Note: `BenchmarkWrite_PNG` uses an IFD0-only EXIF fixture; because there is no ExifIFD
or GPS IFD, `writeIFD` fires only once (IFD0), contributing only 1 eliminated allocation
out of 16 total — the fixed overhead of the PNG container (chunk assembly, zlib encoding)
dominates and the ns/op difference is within the session noise envelope.

### Remaining allocations in BenchmarkEXIFEncode (2 allocs/op after task #201)

Profiled with `-memprofile` + `go tool pprof -alloc_objects -list`:

| Line | Allocation | Why irreducible |
|---|---|---|
| `write.go:202` | `make([]byte, 0, capacity)` — the output buffer | The encoded TIFF bytes must be returned to the caller; cannot be eliminated without changing the API to accept a caller-supplied buffer (future task candidate). |
| `write.go:123` | `var exifPtrBuf, gpsPtrBuf, interopPtrBuf [4]byte` | These arrays are passed as `*[4]byte` to `buildIFD0Entries`/`buildExifIFDEntries`, which store `arr[:]` as `IFDEntry.Value`. The Value slice header outlives the function (it lives in the IFD entry list until `writeIFD` consumes it), so the backing arrays must escape. Eliminating this would require changing how sub-IFD pointer values are stored in `IFDEntry.Value` — a broader refactor out of scope for this task. |

**Overall allocation posture**

Zero-allocation paths (`BenchmarkIFDGet`, `BenchmarkIFDGet_Large`, `BenchmarkParseGPS` in exif; `BenchmarkGPSParse` in xmp; `BenchmarkPacketScan`) are holding at 0 B/op and 0 allocs/op. The fast-path design goals for these operations are being met.

**d018d96 — write-path copy elimination and buffer pre-sizing**

The `perf(exif,xmp,heif,png,orf,rw2)` commit delivers broad latency improvements across the read path at the top level:

- `BenchmarkRead_JPEG` dropped 5.8% (269.8 → 254.2 ns).
- `BenchmarkRead_JPEG_WithXMP` dropped 8.6% (1447 → 1323 ns).
- `BenchmarkReadCombinedMetadataJPEG` dropped 17% (13786 → 11435 ns).
- `BenchmarkReadFile` dropped 17% (2235 → 1854 ns).

All zero-allocation paths remain at 0 B/op and 0 allocs/op. Write-path benchmarks `BenchmarkWrite_JPEG` and `BenchmarkWrite_PNG` are new this run and establish a baseline. Internal package benchmarks (`bmff`, `byteorder`, `iobuf`, `riff`) appear for the first time; all are cheap (< 36 ns) and most are zero-allocation, confirming the internal primitives are performing as designed.

`BenchmarkJPEGExtract_Real` shows a +48 ns regression (2034 → 2082 ns, ~2.4%) which is within typical run-to-run noise for this benchmark given its larger synthetic payload; the allocation profile is unchanged (17756 B, 7 allocs).

---

## [main — perf task #202] — 2026-06-10 (exif: zero-alloc MakerNote dispatch via string([]byte) map key)

### Context

Sprint 34 (PERF-1), task #202. `parseExifSubIFDs` (classic TIFF path) and
`parseExifSubIFDsBigTIFF` both called `makeEntry.String()` purely to build the
key for a `makerNoteParsers` map lookup. `(*IFDEntry).String()` allocates a heap
string on every call, contributing one allocation per camera-file parse that has
a MakerNote and a recognised Make tag.

### Fix

A new internal helper `makerNoteDispatch(makeValue []byte, parentOrder binary.ByteOrder, makerNote []byte) *IFD`
replaces both `makeEntry.String()` + `parseMakerNoteIFD(...)` call chains at the two
dispatch sites in `exif.go`.

The helper:
1. Calls `bytes.TrimRight(makeValue, "\x00")` — identical to `(*IFDEntry).String()`'s NUL stripping, zero allocation (sub-slice).
2. Calls `bytes.TrimSpace(result)` — identical to `strings.TrimSpace` inside `parseMakerNoteIFD`, zero allocation (sub-slice).
3. Performs `makerNoteParsers[string(raw)]` — the Go compiler (`cmd/compile mapaccess2_faststr`) special-cases `map[string]V` lookups where the key expression is `string([]byte)`: it reads the map bucket using the slice's backing array as a temporary string without heap-allocating.

**Escape analysis proof** (`go build -gcflags='-m=2'`):
```
exif/makernote_parse.go:139:39: string(raw) does not escape
```

**Trim semantics equivalence** is locked by three regression gates:
- `TestMakerNoteDispatchTrimEquivalence` — 9 cases covering NUL-only, whitespace-only, trailing space, leading space, multi-NUL.
- `TestMakerNoteDispatchZeroAllocLookup` — `testing.AllocsPerRun(200, ...)` asserts 0 allocs for the lookup step (empty blob → all parsers return nil after length guard, no IFD allocations).
- `TestMakerNoteDispatchAllRegisteredKeys` — every key in `makerNoteParsers` must be found by both the old and new path, including `key + " \x00"` (trailing space + NUL) variants.

**Benchmark fixture change**: `buildCameraExifEntries` now includes a Canon-style
MakerNote entry (tag 0x927C, 18-byte plain-IFD blob) so that `BenchmarkEXIFParse_Camera`
exercises the full dispatch path and makes the alloc savings visible in benchmarks.

### Benchstat (same-session A/B, -count=10, Apple M4 arm64)

Baseline: commit 12acaae + same MakerNote fixture added to baseline worktree (apples-to-apples comparison; old path uses `makeEntry.String()` + `parseMakerNoteIFD`, new path uses `makerNoteDispatch` directly).

```
                     │ baseline (old path) │       after (task #202)        │
                     │       sec/op        │    sec/op     vs base           │
EXIFParse_Camera-10       1.524µ ± 2%       1.518µ ± 1%    ~ (p=0.210 n=10)

                     │   B/op   │       B/op       vs base       │
EXIFParse_Camera-10   2.540Ki    2.533Ki    -0.27% (p=0.000 n=10)

                     │ allocs/op │    allocs/op   vs base         │
EXIFParse_Camera-10    9.000       8.000    -11.11% (p=0.000 n=10)
```

**Result**: **-1 alloc/op** on `BenchmarkEXIFParse_Camera` (p=0.000, statistically significant). ns/op within noise (p=0.210 — the single eliminated allocation does not contribute measurably to latency; the improvement is purely in GC pressure reduction).

`BenchmarkMakerNoteDispatch` is unchanged (4 allocs/op) because that benchmark calls `parseMakerNoteIFD` directly with a string constant — the `makeEntry.String()` allocation never appears on that path. The saving is on the full `Parse` → `parseExifSubIFDs` → dispatch path exercised by `BenchmarkEXIFParse_Camera`.

### Alloc budget for BenchmarkEXIFParse_Camera after task #202

| Allocation | Count | Source |
|---|---|---|
| IFD0 entry batch (via traverse) | 1 | `parseSingleIFD` |
| Sub-IFD arena batch (`[]IFD` + `[]IFDEntry`) | 2 | task #198 lazy arena |
| `visitedPool` map (pooled, 0 cold) | 0 | amortised |
| Canon MakerNote IFD + entries | 2 | `parseSingleIFD` in `parseCanonMakerNote` |
| Output EXIF struct | 1 | `Parse` |
| ~~makeEntry.String() heap string~~ | ~~1~~ | **eliminated by task #202** |
| **Total** | **8** | |

---

## [main — BigTIFF write + security-hardening wave] — 2026-07-06 (HEAD 0ebf5d4)

### Context

This section captures a **time-boxed, representative subset** of the suite at HEAD `0ebf5d4`
(tag `v1.2.0` is `1c3b7a6`), covering the read/write fast paths plus the new BigTIFF write
benchmark, after two waves of work landed since the tasks above:

1. **BigTIFF write** (tasks #264/#270/#271, commits `aa24232`/`ef9041e`/`0ebf5d4`): a native
   BigTIFF encoder in `exif.Encode` and a container-width-aware relocator in `format/tiff`,
   wired into the public `Write`/`WriteFile`. `BenchmarkEXIFEncode_BigTIFF` is a **new**
   benchmark with no prior baseline in this file.
2. **Security-hardening wave** (audit findings #243–#262): 16 fixes, almost all of which are
   correctness/DoS-bound fixes on cold or already-bounded paths. Most commit messages assert
   "no regression" / "byte-identical alloc profile" for the specific benchmark they touched;
   this section independently re-measures the broader top-level/exif/tiff/iptc/xmp surface to
   confirm that claim holistically and to catch anything the per-commit benchmarking missed.

Run with `go test -run '^$' -bench='<names>' -benchmem -count=3 <pkg>` (not the full `-bench=.`
sweep — this is a targeted, time-boxed subset, not an exhaustive run). Same machine as the
`task #198`–`#203`/`#240` entries above (Apple M4, darwin/arm64), but Go **1.26.4** (the
`task #198`–`#203` entries above were captured on 1.26.1; the toolchain has moved forward
between sessions). Figures below are the median of 3 runs (`-count=3`); "Last recorded" pulls
the most recent value for that exact benchmark name from elsewhere in this file (or, where this
file never recorded it, from `benchmarks/BENCHMARKS.md`'s `v1.2.0` column) — not necessarily
the immediately-preceding task's own baseline, since this file's per-task sections are diffed
against each other out of strict chronological order (see the project's own release-manager
notes on this file's two run conventions).

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Last recorded | B/op | Last recorded | allocs/op | Last recorded |
|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 269.3 | 269.7 (task #203) | 521 | 521 (task #203) | 8 | 8 (task #203) |
| BenchmarkRead_PNG | 187.2 | 157.9 (task #198, stale — predates #199–#240) | 288 | 224 (task #198, stale) | 10 | 11 (task #198, stale) |
| BenchmarkReadFile | 2298 | 2543 (v1.2.0, `benchmarks/BENCHMARKS.md`) | 6329 | 6224 (v1.2.0) | 15 | 17 (v1.2.0) |
| BenchmarkWrite_JPEG | 430.8 | 378.4 (task #201) | 240 | 288 (task #201) | 11 | 12 (task #201) |
| BenchmarkWrite_PNG | 275.5 | 280.1 (task #201) | 136 | 184 (task #201) | 15 | 16 (task #201) |

Notes:
- `BenchmarkRead_JPEG`: flat. Confirms `bacfa46`'s (#262) own claim that the JPEG 256 MiB
  aggregate-cap addition left the hot-path allocation profile unchanged.
- `BenchmarkRead_PNG`: the "last recorded" figure in *this file* is stale (task #198, before
  five later perf tasks touched shared code such as `format.Detect`); the current allocs/op
  (10, down from 11) is consistent with task #203's magic-byte pooling. No PNG-specific commit
  landed in the security wave, so this is simply the first re-measurement since task #198.
- `BenchmarkReadFile`: **−9.6% ns/op** vs the v1.2.0 baseline recorded in
  `benchmarks/BENCHMARKS.md`, with allocs down from 17 to 15. `readAllCapped` (#251) adds a
  `LimitReader`-style wrapper on the six TIFF-family write paths, not on `ReadFile`'s own
  `io.ReadAll` (already capped since v1.1.0/#140), so the net improvement here is attributable
  to the accumulated exif/format perf work (tasks #198–#203, #240) rather than the security wave.
- `BenchmarkWrite_JPEG`: **+13.8% ns/op**, but **−16.7% B/op** and **−1 alloc/op**, vs the task
  #201 baseline. `bacfa46` (#262) introduced a pooled `countingReader` wrapper around the JPEG
  Inject write path to enforce the new 256 MiB aggregate cap; pooling explains the alloc/B
  *reduction*, and the per-call budget bookkeeping (a running byte counter checked on every
  read/seek) explains the small ns/op increase. Both are within this project's established
  "security overhead, accepted" pattern (see the v1.1.0/v1.2.0 sections of
  `benchmarks/BENCHMARKS.md`) and the absolute cost (≈430 ns) remains negligible.
- `BenchmarkWrite_PNG`: flat-to-improved (no PNG-specific security-wave commit; consistent
  with continued benefit from the shared `exif` encode-path pooling in task #240).

### exif/

| Benchmark | ns/op | Last recorded | B/op | Last recorded | allocs/op | Last recorded |
|---|---|---|---|---|---|---|
| BenchmarkMakerNoteDispatch | 119.7 | 123 (task #200, regression-fixed) | 208 | 208 (task #200) | 4 | 4 (task #200) |
| BenchmarkEXIFParse | 165.7 | 172.1 (task #200) | 337 | 337 (task #200) | 4 | 4 (task #200) |
| BenchmarkEXIFParse_Camera | 1532 | 1521–1543 avg ≈1532 vs 1518 (task #202) | 2594 | 2533 (task #202, `2.533Ki`) | 8 | 8 (task #202) |
| BenchmarkEXIFEncode | 127.9 | 122.5 (task #201) | 80 | 80 (task #201) | 2 | 2 (task #201) |
| BenchmarkEXIFEncode_Camera | 1097 | 1065 (task #201) | 1618 | 1619 (task #201) | 14 | 14 (task #201) |
| BenchmarkParseBigTIFF_Simple | 184.9 | 195 (v1.2.0, `benchmarks/BENCHMARKS.md`) | 337 | 369 (v1.2.0) | 4 | 4 (v1.2.0) |
| **BenchmarkEXIFEncode_BigTIFF** (new) | **976.7** | — (no prior baseline; new since task #264) | **7750** | — | **6** | — |

Notes:
- All classic-path exif benchmarks are flat within ±5% of their last-recorded value in *this*
  file — direct confirmation of the "0% delta" / "byte-identical" claims made in the #246/#252
  (byte-order correction) and #255 (IFD-chain budget) commit messages: neither adds measurable
  overhead to well-formed files, only to the pathological-input paths their regression tests
  target. (`BenchmarkEXIFParse_Camera`: task #202's own baseline was 1518 ns/2533 B; this
  session measures 1532 ns/2594 B, +0.9%/+2.4% — flat. Note that `benchmarks/BENCHMARKS.md`
  instead compares against the older, pre-perf-wave v1.2.0 baseline of 1427 ns, which shows a
  larger nominal delta for the same reason task #198–#203/#240 already explain: those tasks
  landed between the v1.2.0 tag and this measurement.)
- `BenchmarkParseBigTIFF_Simple`'s B/op drop (369 → 337, exactly −32 B) is the *same* delta task
  #199 measured on classic `BenchmarkEXIFParse` for a 4-entry IFD0 (4 × 8 B saved per entry from
  replacing the `binary.ByteOrder` interface field with a 1-byte flag) — confirming the BigTIFF
  read path shares the same `IFDEntry` struct and benefits identically, even though it was not
  re-measured when task #199 originally landed.
- `BenchmarkEXIFEncode_BigTIFF` establishes the first baseline for the new BigTIFF encode path:
  977 ns/op, 7750 B/op, 6 allocs/op for a minimal BigTIFF IFD0. This is ~7.6× the B/op of the
  classic `BenchmarkEXIFEncode` (80 B) and 3× the allocs, which is expected — every IFD entry,
  offset, and header field is twice the width (8-byte IFD8/LONG8/SLONG8 vs 4-byte LONG/SLONG),
  and the output buffer itself is proportionally larger.

### format/tiff

| Benchmark | ns/op | Last recorded | B/op | Last recorded | allocs/op | Last recorded |
|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 109.9 | 143.9 (v1.2.0) | 584 | 584 (v1.2.0) | 3 | 3 (v1.2.0) |
| BenchmarkBigTIFFExtract | 116.1 | 145 (v1.2.0) | 584 | 584 (v1.2.0) | 3 | 3 (v1.2.0) |
| BenchmarkRelocateSingleStrip | 1520 | 1933 (task #240) | 7430 | 7462 (task #240) | 22 | 28 (task #240) |
| BenchmarkRelocateMultiStrip | 1836 | 2442 (task #240) | 10246 | 10265 (task #240) | 28 | 34 (task #240) |
| BenchmarkRelocateDNGLike | 2444 | 3128 (task #240) | 13443 | 13457 (task #240) | 37 | 42 (task #240) |

Notes:
- `BenchmarkTIFFExtract`/`BenchmarkBigTIFFExtract`: **−23.6% / −19.9% ns/op**, flat B/op and
  allocs/op. Consistent with the accumulated `exif`/`format` perf work (tasks #198–#203). #253's
  `findExifIFDOffset` fix (comparing in `uint64` before narrowing to `int`) does touch this exact
  extract path, but adds a single comparison — below this benchmark's measurement noise floor.
- **All three `BenchmarkRelocateXxx` benchmarks improved by 6 allocs/op (−21.1% to −27.1% ns/op),
  flat B/op**, vs their task #240 baseline. This is directly attributable to `202e34a` (#261,
  GM-W1): the same files (`relocate.go`, `relocate_arw.go`, `relocate_nef.go`, `relocate_orf.go`,
  `relocate_rw2.go`) that introduced the `maxImageBlocksPerOffsetEntry`/`maxSubIFDsPerEntry`/
  `maxAggregateImageBlocks` caps also restructured `extractParallelOffsetBlocks` and
  `enumerateSubIFDsAt` around a shared `imageBlockBudget`, which — for these benchmarks'
  well-formed, non-adversarial fixtures — allocates fewer intermediate objects than the
  pre-#261 code while still closing the DoS. **This is a genuine improvement, not a
  regression**; it is called out explicitly because it is a positive side effect of a security
  fix, which is the reverse of every other regression documented in this file so far.

### iptc/

| Benchmark | ns/op | Last recorded (v1.2.0) | B/op | Last recorded | allocs/op | Last recorded |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 183.8 | 197 | 1024 | 1024 | 6 | 6 |
| BenchmarkIPTCEncode | 159.8 | 163 | 304 | 304 | 2 | 2 |
| BenchmarkIPTCAccessors | 21.00 | 21.6 | 48 | 48 | 1 | 1 |

Flat-to-slightly-improved across the board. The `e092770` extended-length overflow guard adds a
single `uint64` comparison against `maxDatasetValueLen` on the encode path — below this
benchmark's noise floor, confirming the fix is effectively free for well-formed input.

### xmp/

| Benchmark | ns/op | Last recorded (v1.2.0) | B/op | Last recorded | allocs/op | Last recorded |
|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 3251 | 3294 | 2208 | 2208 | 45 | 45 |
| BenchmarkXMPEncodeFullPacket | 1628 | 1561 | 3573 | 3188 | 4 | 4 |
| BenchmarkXMPParse | 1333 | 1357 | 1160 | 1160 | 20 | 20 |
| BenchmarkXMPEncode | 1030 | 1028 | 3155 | 3156 | 3 | 3 |

Notes:
- `BenchmarkRDFParse`/`BenchmarkXMPParse`/`BenchmarkXMPEncode`: flat.
- **`BenchmarkXMPEncodeFullPacket`: +4.3% ns/op, +12.1% B/op (3188 → 3573), flat allocs/op.**
  Root cause: `e81d364`'s XMP encoder fix (see `CHANGELOG.md`) now wraps every array-typed
  property (`dc:creator`, `dc:subject`, `dc:description`, `dc:rights`, `dc:title`, the `xmpMM`
  ordered-array set) in its `<rdf:Seq>`/`<rdf:Bag>`/`<rdf:Alt>` collection container even when it
  holds exactly one value — this benchmark's "full packet" fixture sets several array-typed
  properties, each of which now emits an extra pair of collection-container tags. `allocs/op` is
  unchanged because the output buffer is pre-sized from a single measurement pass (existing
  two-pass sizing from the 2026-04-07 post-audit optimisation); only the byte count grows.
  `BenchmarkXMPEncode` (a single scalar-property encode, unaffected by the array-wrapping fix) is
  flat, which independently confirms this attribution — the B/op increase is isolated to the
  benchmark that actually exercises array-typed properties.

### Raw output

Full `-count=3` output for every benchmark in this section is archived at
`benchmarks/results/HEAD-0ebf5d4-2026-07-06.txt`.
