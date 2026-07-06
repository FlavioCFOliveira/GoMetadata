---
name: audit-findings-20260706-xmp-root-concurrency
description: Fresh audit 2026-07-06 of xmp/, root API (metadata.go/read.go/write.go/write_unix.go), and the concurrency model. 1 new finding (XMPCONC-01, HIGH) — sub-struct types (*xmp.XMP, *exif.EXIF) remain unsynchronised and reachable via exported Metadata fields, bypassing the Metadata mutex added for finding #128. All prior 2026-06-09 XMP findings (NS-URI/local-name injection, rdf:resource, bare lang) reconfirmed CLOSED.
metadata:
  type: project
---

Audit date: 2026-07-06.
Scope: xmp/ package (all *.go), root metadata.go/read.go/write.go/options.go/errors.go/doc.go/write_unix.go/write_windows.go, concurrency model of the whole public API.

## Baseline confirmed
- go build/vet PASS. go test -race ./xmp/... . PASS.
- FuzzParseXMP: 6.8M execs / 60s, 0 crashers.
- FuzzRead (root, end-to-end): 5.4M execs / 60s, 0 crashers.
- Prior 2026-06-09 findings in xmp/ (see audit_findings_20260609_xmp.md) — ALL CONFIRMED CLOSED in current code:
  - XMP-NS-URI-01 (ns URI injection): write.go:92 now calls writeXMLEscaped(buf, ns) for the xmlns attribute value. Verified.
  - XMP-LOCAL-01 (local-name injection): every element-name write site (writeSimpleProperty, writeMultiValuedProperty incl. xml:lang attr value, writeStructProperty, writeStructInListProperty) now routes through writeXMLName (strips illegal NCName bytes) for names and writeXMLEscaped for values/attr-values. Verified complete — no write site left unescaped.
  - XMP-RDFRESOURCE-01 (rdf:resource dropped): rdf.go onStartProperty (called from onStartElement's atProperty() branch) now scans attrs for rdf:resource immediately after propDepth is set and stores it. Verified.
  - XMP-BARE-LANG-01 (bare lang over-capture): rdf.go onStartListItem now requires `a.ns == "http://www.w3.org/XML/1998/namespace"` exactly (XMLNamespaceURI pre-bound in nsTable[0]). Verified — no longer accepts a.ns=="".
- RACE-01 (2026-06-09, Metadata.Set* concurrent map write via putProp): CLOSED for the intended call path — metadata.go now has `mu sync.Mutex` and every Set* method (SetCaption, SetCopyright, SetCreator, SetCameraModel, SetGPS, SetKeywords, SetLensModel, SetMake, SetDateTimeOriginal, SetExposureTime, SetFNumber, SetISO, SetFocalLength, SetOrientation, SetImageSize) locks m.mu for its whole body including ensureEXIF/ensureIPTC/ensureXMP. Verified exhaustively — no Set* method is missing the lock. Getters are intentionally unlocked (documented contract: safe only when no concurrent Set* is in flight).

## NEW Finding

### XMPCONC-01 — HIGH — sub-struct concurrency guard is NOT complete
RACE-01's fix only protects callers who exclusively use `Metadata.Set*`. It does
NOT protect the exported `m.XMP` (*xmp.XMP), `m.EXIF` (*exif.EXIF), or `m.IPTC`
(*iptc.IPTC) pointers once a caller obtains them (e.g. from a `Read()` result,
or from `NewMetadata` + direct field assignment) and calls THEIR OWN Set*
methods concurrently, bypassing `Metadata.mu` entirely. `xmp.(*XMP).putProp`
(xmp/xmp.go:357-365) does a plain, unsynchronised `x.Properties[ns][local] = value`
map write with no internal lock — confirmed by a concrete probe test:
concurrent goroutines calling `x.SetCaption` / `x.SetKeywords` directly on a
shared `*xmp.XMP` triggered:
  1. `go test -race`: WARNING: DATA RACE in `putProp` (map read/write race at xmp.go:361-364).
  2. Plain `go test` (no -race), repeated: `fatal error: concurrent map writes` — an
     UNRECOVERABLE runtime crash (not a panic; cannot be caught with recover()),
     killing the entire process.
- Root cause: `xmp.XMP` (and `exif.EXIF`, whose `IFD.Entries []IFDEntry` has the
  same unsynchronised-mutation problem via slice mutation instead of map mutation)
  have no internal synchronisation and no documented thread-safety contract.
  `iptc.IPTC` already carries an explicit disclaimer ("Thread-safety: not safe
  for concurrent use", iptc/iptc.go:569-571) — `xmp.XMP` and `exif.EXIF` do not
  have the equivalent comment anywhere, which is an inconsistency: the project
  already recognised and documented this exact contract for one sub-package but
  never applied it to the other two.
- `metadata.go`'s own doc comment (lines 43-56) says "Concurrent Set* calls on
  the same *Metadata are safe" — true only for `Metadata.Set*`; it does not warn
  that the embedded `*exif.EXIF` / `*iptc.IPTC` / `*xmp.XMP` pointers it exposes
  as public fields are themselves unsafe for direct concurrent mutation. A
  developer who reads that comment and reasonably assumes "the whole object
  graph returned by Read() is safe under Metadata's mutex" will hit this crash.
- Trigger condition: `m := gometadata.Read(r)`; then from ≥2 goroutines call
  `m.XMP.SetCaption(...)` / `m.XMP.SetKeywords(...)` (or any XMP Set*) directly
  on the shared `*xmp.XMP`, without going through `Metadata.SetCaption` etc.
  Reachable any time an integrator fans out per-field mutation across a worker
  pool while sharing one XMP struct (e.g. batch-tagging many derived outputs
  from one template), or holds one XMP object as a shared "current metadata"
  cache mutated by concurrent request handlers.
- Impact: Denial of Service — unrecoverable process crash (`fatal error:
  concurrent map writes`), affecting the whole process, not just the calling
  goroutine. CWE-362 (race condition) / CWE-833 is adjacent but the practical
  effect here is closer to CWE-400 (uncontrolled resource — availability loss)
  via a language-level fatal error.
- Exploitability: Confirmed (reproduced with both -race and repeated plain
  `go test`, see PoC below). Requires caller-controlled concurrency pattern
  (not directly triggerable purely from malicious file bytes), so it is a
  "documented contract gap" class finding rather than a pure untrusted-input
  vulnerability — but it is squarely in the audit brief's concurrency-model
  scope and the current code provides neither synchronisation nor documentation
  to prevent it.
- PoC (deleted after use, not committed): a test in package xmp with 8
  goroutines each looping `x.SetCaption(...)`, `x.SetKeywords(...)`, `x.Caption()`
  on a shared `*XMP{Properties: make(map[string]map[string]string)}` reliably
  triggers both the race detector warning and (without -race, `-count=20`) the
  `fatal error: concurrent map writes` crash within a fraction of a second.
- Remediation options (for go-performance-architect to choose, tradeoff is
  performance vs. safety-by-default):
  (a) Cheapest / matches existing precedent: add the same "Thread-safety: not
      safe for concurrent use" doc comment (like iptc.IPTC) to `xmp.XMP` and
      `exif.EXIF` type declarations and to their Set* methods, AND strengthen
      metadata.go's concurrency-contract comment to explicitly state that the
      embedded EXIF/IPTC/XMP objects are not independently safe for concurrent
      mutation outside `Metadata.Set*` — mutating them directly requires the
      caller's own synchronisation. Zero runtime cost.
  (b) More robust: add an internal `sync.Mutex` to `xmp.XMP` and `exif.EXIF`
      (mirroring `iptc.IPTC`'s existing pattern if it has one, or Metadata's),
      locked in every exported Set*/mutating method, so the sub-structs are
      safe even when used standalone (outside Metadata). Has a small hot-path
      cost per Set* call (acceptable — Set* is not the zero-alloc parsing hot
      path); would fully close the gap without relying on caller discipline.
  Recommend (a) as the immediate minimum (consistency + honesty about the
  contract, zero cost) and consider (b) for a future release given the
  project's "nothing may compromise the code/services" mandate.
- Suggested regression test: a `-short`-skipped or build-tag-gated concurrency
  test is not appropriate for CI (races are timing-dependent); instead, add the
  doc-comment fix and a unit test asserting the documented contract via
  `go vet`'s static race-shape is not sufficient — the real verification is the
  ad hoc probe already run in this audit. If (b) is chosen, add a `-race`
  regression test analogous to the one used here, asserting concurrent
  `SetCaption`/`SetKeywords` on a shared `*xmp.XMP` do not race.

## Items Verified Clean (new checks performed this audit, beyond 2026-06-09 scope)
- XXE / DOCTYPE / billion-laughs: still blocked — skipBang discards `<!...>` constructs
  wholesale (rdf.go skipBang/skipSpecialTag); entities are never expanded from a
  DOCTYPE internal subset. No named/parameter entity expansion exists anywhere in
  the parser (unescapeXML only understands the five predefined XML entities plus
  numeric character references, never DTD-declared entities).
- maxXMPDocumentBytes (16 MiB) cap correctness: applied post-normaliseToUTF8,
  pre-parseRDF, in xmp.Parse. UTF-16 transcode input is separately capped at
  maxXMPTranscodeBytes (16 MiB) inside toUTF8, and UTF-32 decode output is capped
  at maxUnescapedXMLBytes (1 MiB) inside decodeUTF32 — worst-case transient
  allocation during Parse is bounded (~32-48 MB even in adversarial UTF-16/32
  cases), never unbounded.
- xmp.Scan (packet boundary scan): all bytes.Index results are bounds-safe by
  construction (a found match's end position can never exceed len(b)); the
  explicit `fullEnd > len(b)` guard is defensive but mathematically unreachable.
  "Packet boundary confusion" via an embedded literal "<?xpacket end=" string
  ahead of the true close tag can truncate the scanned packet early, but this
  only produces a malformed/partial XML body that parseRDF's lenient design
  already handles without panicking (proven by 6.8M-exec fuzz run) — a data-
  integrity/spec-boundary characteristic, not a memory-safety bug, and consistent
  with XMP Part 1 §7.3.3's own textual-marker-scanning approach.
- Nesting depth cap: parseStartTag increments p.depth then rejects (ErrXMLNestingDepth)
  before indexing nsDepth[p.depth] when p.depth > 100; nsDepth is sized [101]entry
  so all valid indices 1..100 are in bounds. The hand-rolled scanner is iterative
  (no native call-stack recursion for element nesting), so Go stack exhaustion via
  deep XML nesting is structurally impossible regardless of the depth cap.
- unsafe.String / aliasing: none present in xmp/ anymore. unescapeXML's fast path
  (no '&' in input) returns `string(b)` — a real heap copy — not an unsafe alias
  (fix #72, reconfirmed present at rdf.go:1098-1116). All other value/name storage
  sites (`string(tagLocal)`, attribute values via readQuotedValue→unescapeXML) are
  independent copies; the *XMP returned by Parse retains no slice into the input
  buffer `b`.
- sync.Pool reuse safety (builderPool, liPool, nsListPool, localListPool,
  encBufPool): confirmed genuinely safe, including the builderPool pattern used by
  unescapeXML/onCharDataListItem/collectTextContent. Verified against Go 1.26
  stdlib source: `strings.Builder.Reset()` sets `b.buf = nil` (not `b.buf[:0]`),
  so a string previously returned by `Builder.String()` (which is an unsafe
  zero-copy alias of the builder's backing array) is never mutated after Reset+Put
  — the next reuse of the pooled builder always allocates a brand-new backing
  array on its first Grow/Write call. This is NOT the same hazard class as #72
  (which was about aliasing the caller's own `[]byte` input, not a Builder's
  self-owned buffer) — confirmed there is no regression of that class here.
- Dispatch/API (read.go, write.go): format detection and per-format extractor/
  injector dispatch tables are exhaustive and every error path returns cleanly;
  no panic path found. FuzzRead confirms best-effort Read never panics across
  5.4M executions including deliberately malformed EXIF/IPTC/XMP payloads and
  nesting-depth-exceeding XMP.
- WriteFile (write_unix.go / write.go WriteFile): re-verified #124/#125 — symlink
  resolved via EvalSymlinks before any I/O; temp file created in the resolved
  file's own directory (guarantees same-filesystem atomic rename); original mode
  preserved via Chmod before any data is written; ownership best-effort preserved
  via chownFile; Write failure, Sync failure, or Close failure all leave the
  original file untouched (temp file removed via defer, renamed flag guards
  against removing an already-renamed temp); directory fsync is best-effort after
  a successful rename. No path where a partial/corrupt file replaces the original.
  Residual generic TOCTOU (a symlink path component could theoretically be swapped
  between EvalSymlinks and the subsequent os.Open of the resolved path) is an
  inherent OS-level limitation shared by virtually every atomic-rename-based
  editor (vim, sed -i, etc.) and not fixable without platform-specific
  O_NOFOLLOW/openat2 support unavailable portably in the Go stdlib — informational,
  not a new actionable finding.
- xmp.NSgeo (added in commit d9e1b46) is used for reading (GPS() fallback) but is
  NOT present in write.go's `prefixMap`, so round-tripping a document with only
  geo:lat/geo:long produces a generated `nsN:lat` prefix instead of the
  conventional `geo:` prefix on Encode. This is a minor spec-conformance /
  cosmetic gap (the namespace URI itself is still written and escaped correctly,
  so no security impact and no data loss — XML namespace semantics are governed
  by URI, not prefix spelling) — noted for future conformance work, not filed as
  a security finding.

## Fuzz targets in scope inventory
- `xmp.FuzzParseXMP` (xmp/fuzz_test.go) — only fuzz target inside xmp/.
- root `FuzzRead` (fuzz_test.go) — end-to-end Read() fuzzer covering format
  detection + all three parsers together; this is the closest thing to a
  root-level dispatch fuzz target and there is no separate FuzzWrite/FuzzWriteFile
  at the root (write path is not fuzzed directly — write.go/WriteFile take a
  caller-constructed *Metadata, not raw untrusted bytes, so this is an intentional
  and reasonable scope boundary, not a gap).
