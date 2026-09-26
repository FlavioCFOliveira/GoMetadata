---
name: project_sprint44_batchD_228_234
description: Sprint 44 Batch D (2026-09-25) — heif [4]byte box/item types + single-pass iloc/meta inject + iobuf.ReadAll; png writeChunk/readChunk pooling + zlib bytes.Reader pool; webp/riff offset-tracked, seek-free Extract + single-buffer RIFF header. Post-merge security fixes same day: HEIF-ILOC-DUPBOX-01 (HIGH) and PNG-BYTESREADERPOOL-RETENTION-01 (MEDIUM) — see feedback_heif_ilocdupbox_iterative_fix.md
metadata:
  type: project
---

Batch D (#228–#234) implemented in one merged pass on
feature/44-performance-and-efficiency-laboratory-260925 (2026-09-25), uncommitted at
write time. Golden SHA-256 identical: 992 HEIF/PNG/WebP corpus+fixture files (747
successful `Read`+`SetCopyright`+`Write` hashes, 245 matching error outcomes) unchanged
before vs after. All benchmarks improved or flat (`benchstat -count=10`, p=0.000 except
noted); no ns/op regression. Full gate green: `go build`/`vet`/`test -race` (whole repo),
`golangci-lint run ./...` (0 new issues; 6 pre-existing issues in untouched files left
alone per Scope Discipline), `staticcheck ./...` (clean), `govulncheck ./...` (clean,
after rebuilding it with the local Go toolchain — the Homebrew-installed one was built
against an older Go and failed to load `math/rand/v2` packages). All 7 fuzz targets
(`FuzzHEIFExtract`, `FuzzHEIFInject`, `FuzzPNGExtract`, `FuzzPNGInject`,
`FuzzWebPExtract`, `FuzzWebPInject`, `FuzzRIFFRead`) ran 60s clean, zero crashers.

**POST-MERGE SECURITY FIX (2026-09-25, same day)**: a follow-up security audit of this
exact diff found two BLOCKING-class defects the above initial pass missed —
HEIF-ILOC-DUPBOX-01 (HIGH, silent extent-offset corruption in #229's arithmetic size
computation) and PNG-BYTESREADERPOOL-RETENTION-01 (MEDIUM, #233's pooled `*bytes.Reader`
retaining the caller's input after `Put`). Both fixed same-day; see
[[feedback_heif_ilocdupbox_iterative_fix]] for the HEIF fix's own two-round story (the
literal prescribed fix was locally correct but incomplete — strengthened fuzzing, exactly
as the audit itself mandated, found a second distinct trigger for the same root cause
within the same session) and the `#233` fix note (`putBytesReader` calls `Reset(nil)`
before every `Put`) in BENCHMARKS.md's Batch D section. The "0 new lint issues" and
"60s clean" fuzz claims above were true of the PRE-security-fix state; after both fixes,
the gate was re-run in full (build/vet/race/lint/staticcheck/govulncheck/golden-hash) and
`FuzzHEIFInject` specifically needed THREE 60-second attempts (the first two each found a
real, distinct bug within the first several seconds) before two consecutive clean runs
were achieved — see the linked feedback file for why "the fuzzer found nothing yet" is not
sufficient evidence after fixing a bug the fuzzer was specifically strengthened to catch.

**Why:** these seven tasks were the last of Sprint 44's HEIF/PNG/WebP/RIFF allocation
targets; #229 in particular required a from-scratch redesign of the iloc/meta
double-build (see item 2 below) rather than a mechanical string→[4]byte swap.

**Key technical findings:**

1. **[4]byte box/item-type conversion pattern (heif #228, png #232, webp #234)**:
   the established codebase convention (`cr3.go`'s `boxMoov = [4]byte{'m','o','o','v'}`,
   `webp.go`'s pre-existing `fourCCEXIF`/`fourCCXMP`) extends cleanly to HEIF's
   `parseHEIFBoxHeader`/`isContainerBox`/`findBox`/`findInnerBox`/`flatBoxRangeInFile`
   (all box types) and item types (`parseInfe`/`parseInfeV0V1`/`parseInfeV2V3`, now
   returning `(id uint16, typ [4]byte, ok bool)` instead of `(uint16, string)` — the
   `ok` flag replaces the old `itemType != ""` sentinel check). One quirk worth noting
   for any future similar conversion: `selectBestItem`'s original string target list
   included `"rdf+xml"` (7 ASCII bytes) alongside `"mime"` — ISO 14496-12 §8.11.6 fixes
   `item_type` at exactly 4 bytes, so `"rdf+xml"` could never have matched any parsed
   type even in the original string-based code; it was silently dead code from day one.
   Do not try to represent an inherently-longer-than-4-byte string literal as a `[4]byte`
   sentinel when converting a candidate list like this — just drop it, and say so in a
   comment (searched-and-confirmed via `grep` across the whole test suite, no test
   depended on the dead branch).

2. **#229's real design win was arithmetic, not just pooling** — the `buildInjectComponents`
   flow built the iloc AND meta boxes TWICE (a "placeholder" pass just to learn the size
   delta for ancestor-box patching, then a "final" pass with real offsets). The key
   insight that made a single-pass design possible: iloc's per-item/per-extent field
   widths (`offsetSize`/`lengthSize`/`baseOffsetSize`/`indexSize`, all nibble-packed
   FullBox header fields) are FIXED regardless of the actual VALUES stored — so the
   box's total serialised LENGTH never depends on offset/length values, only on
   structure (item count, extent count per item). `updateIlocItemsInPlace` fixes that
   structure up front (every updated item collapses to exactly 1 placeholder extent,
   mutated in place rather than copied — parseIlocFull's result is never aliased
   elsewhere, so in-place mutation is safe by construction, not by convention). Given
   fixed structure, `ilocBoxSize`/`ilocSizeDelta` compute the exact byte delta the meta
   box will change by using PURE ARITHMETIC — zero bytes serialised — which is enough to
   patch ancestor container sizes and compute the final item base offset BEFORE the real
   offset/length values are even assigned. `buildIlocBox`/`buildMetaBox` then each run
   ONCE, already carrying final values, each writing into a single pre-sized buffer
   (header placeholder in the same buffer as the body, size field patched in place at
   the end — same "header placeholder, body, patch size" shape as HEIF's own existing
   `parseCR3BoxHeader`-adjacent code and as PNG's `writeChunk`/webp's `buildWebPBody`
   below). This is the general lesson: before assuming "compute size, then build, then
   build again with real values" is necessary, check whether the format's own field
   widths are value-independent — if so, the size can usually be computed once via
   arithmetic mirroring the existing size-precompute helper, and only ONE real build is
   ever needed. Result: HEIFInject 34→12 allocs/op (exceeds the originally-stated
   6–10 target), 1768→812 B/op, 559.9n→316.9n (−43%).

3. **HEIF/webp Inject swap to `iobuf.ReadAll` (#230) is a clean drop-in** — unlike
   `tiff`/`orf`/`rw2`'s Extract/Inject (task #224, prior batch), `iobuf.ReadAll` itself
   never uses a pool (plain exact-size `make`), so the "release pooled buffer only after
   output is written" caution in the task brief did not actually apply here — both
   `heif.Inject`'s and `webp.Inject`'s existing full-file-buffer ownership patterns
   (subslicing `data`/`original` directly, e.g. HEIF's `suffix := data[metaAbsEnd:]`)
   needed zero changes beyond the read call itself. Non-seekable-fallback regression
   test pattern: a `seekStartOnlyReader` that allows ONLY `Seek(0, io.SeekStart)` (the
   unconditional leading rewind `Inject` itself performs before ever calling
   `iobuf.ReadAll`) and fails every other Seek — a blanket `failSeeker` (fails ALL
   seeks, mirroring `internal/iobuf`'s own test helper) does NOT work for testing a
   function that does its own Seek-to-start before delegating, since that leading seek
   fails first and never reaches the fallback path being tested.

4. **webp/riff #234's offset-tracking redesign eliminates seeks entirely, not just
   amortises them** — `riff.ReadChunkHeaderAt(r io.Reader, hdr *[8]byte, offset int64)`
   is a NEW function (added, not a modification of `ReadChunk`/`ReadChunkBuf`, which
   stay untouched — both are directly tested standalone in `riff_test.go` with no
   caller-side offset tracking, so preserving them avoided any test churn there) that
   takes the chunk's data offset as a caller-supplied parameter instead of discovering
   it via `Seek(0, io.SeekCurrent)`. `webp.readWebPChunks` tracks its own `offset int64`
   (start 12, advance by `8+chunk.Size+padding` after each chunk, whether the chunk was
   read via `readPaddedChunk` or skipped via `riff.SkipChunk`) — this removes ONE seek
   PER CHUNK regardless of type (previously paid even for non-metadata/pass-through
   chunks). Separately, `readPaddedChunk`'s OWN stream-availability guard used to
   Seek-to-end-and-back on EVERY metadata chunk; `measureStreamEnd` now does that AT
   MOST ONCE per `Extract` call, lazily (only when a metadata chunk is actually
   encountered, `streamEnd < 0` sentinel) — a file with zero EXIF/XMP chunks now pays
   ZERO seek cost total (better than baseline, which paid 1 SeekCurrent per chunk via
   `riff.ReadChunkBuf` even for non-metadata ones). `readPaddedChunk`'s own signature
   dropped `io.ReadSeeker` for plain `io.Reader`: the odd-size RIFF padding byte is read
   and discarded (`io.ReadFull` into a 1-byte scratch, tolerating `io.EOF`) instead of
   `Seek(1, io.SeekCurrent)` — since the function no longer seeks at all, it no longer
   needs Seek capability. Result: WebPInject 215.8n→128.1n (−41%), 899→320 B/op (−64%),
   10→4 allocs/op (−60%); WebPExtract unaffected (flat, no regression) since Extract's
   own chunk loop mostly skips non-metadata chunks via `SkipChunk` regardless.
   Regression test pattern: a `seekRecorder` wrapping `*bytes.Reader` that records every
   `Seek`'s `whence` argument, asserting `io.SeekCurrent` never appears in the trace for
   a full `Extract` call over a file with VP8X+EXIF+XMP+one non-metadata chunk.

5. **[4]byte FourCC conversion for webp's `collectOriginalChunks`/`writeRIFFChunk`
   requires updating BOTH directions** — the original `id := string(original[pos:pos+4])`
   (read side, string allocation per non-metadata chunk — the majority of chunks in a
   real file) is the obvious #234 target, but `writeRIFFChunk`'s OWN `id string`
   parameter must ALSO become `[4]byte`, otherwise `buildWebPBody`'s
   `writeRIFFChunk(body, c.id, c.data)` call (forwarding EVERY preserved chunk) would
   need `string(c.id[:])` at the call site — reintroducing, on the WRITE side, exactly
   the allocation eliminated on the read side. Always trace a converted value all the
   way to its LAST consumer before declaring a [4]byte conversion complete; a partial
   conversion that stops at an intermediate struct field just moves the allocation.

6. **buildWebPBody's single-pooled-buffer RIFF header (#234)** mirrors HEIF's
   `buildIlocBox`/`buildMetaBox` and PNG's `writeChunk` "header placeholder, body,
   patch size" shape: write `"RIFF"` + a 4-byte zero placeholder + `"WEBP"` directly into
   the SAME pooled `*bytes.Buffer` used for the chunk stream (not a separate
   `make([]byte, 12)`), append every chunk, then `binary.LittleEndian.PutUint32` the
   size field in place via `body.Bytes()[4:8]` once the final length is known. This
   collapses the former "write 12-byte header, then write body" (2 `w.Write` calls, 1
   extra allocation) into one `w.Write(body.Bytes())` call.

7. **See [[feedback_png_chunktype_escape]] for the two escape-analysis/pooling pitfalls
   found and fixed in #232** (control-flow-insensitive escape via error-path `%q`
   formatting of a `[4]byte` parameter; `sync.Pool` round-trip cost exceeding a plain
   tiny-object escape) — these cost the most iteration time in this batch and are worth
   reading before touching any other hot-path chunk/box-type reader.

**Retained-slice ownership / offset-math changes for the follow-up security audit**
(per task brief, esp. #229 and #230):
- `updateIlocItemsInPlace` MUTATES `ilocInfo.items` (and, when capacity allows, reuses
  an existing `ilocFullItem.extents` backing array) rather than copying — safe only
  because `parseIlocFull` produces a freshly-allocated result per `buildInjectComponents`
  call with no other aliasing reference; do not reuse this pattern against a
  caller-supplied or cached `ilocBoxInfo`.
- `iobuf.ReadAll`'s return value (`data`/`original` in `heif.Inject`/`webp.Inject`) is a
  plain, non-pooled `make`-backed slice — existing direct subslicing (e.g. HEIF's
  `suffix := data[metaAbsEnd:]`, retained inside `injectComponents` and written later)
  is unchanged in lifetime/ownership semantics from the prior `io.ReadAll` result; no
  new aliasing hazard introduced.
- `webp.readWebPChunks`'s `offset int64` bookkeeping is a plain local counter with no
  aliasing implications; `riff.ReadChunkHeaderAt`'s `Chunk.Offset` field is now
  caller-trusted rather than self-verified via Seek — an incorrect caller-supplied
  offset would silently produce a wrong `Chunk.Offset` (used later for
  `SkipChunk`/`readPaddedChunk`'s arithmetic) without any error; `webp.go` is the only
  production caller and its offset arithmetic is exercised by
  `TestExtractNoPerChunkSeekCurrent` plus the full existing WebP conformance/corpus
  suite, but a future caller of `ReadChunkHeaderAt` must independently prove its own
  offset tracking is correct — the function itself performs no validation.
