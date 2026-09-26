---
name: audit-20260926-task297-streamcopyfast
description: Sprint-44 task #297 (internal/iobuf.StreamCopyN fast paths) security pass — CLEARED. Pooled *io.LimitedReader (limitedReaderPool) verified to clear R before every Put (no Batch-D-class retention); short-read/EOF semantics correctly normalized to io.ErrUnexpectedEOF; WriteTo fast path correct after partial-Read+Seek; -race clean under concurrent use of both fast paths
metadata:
  type: project
---

Narrow audit 2026-09-26 (uncommitted diff on HEAD a03b320): internal/iobuf/streamcopy.go adds
streamCopyNFast (two fast paths: *bytes.Buffer sink via pooled *io.LimitedReader.ReadFrom;
*bytes.Reader source with n==Len() via WriteTo); read.go parseEXIF uses a fixed [3]exif.ParseOption
array instead of append(nil,...); exif/exif.go's three ParseOption constructors marked
//go:noinline (pure compiler directive, zero behavioral change, confirmed by reading — not
re-verified further).

**limitedReaderPool retention — VERIFIED CLEAN, no Batch-D-class issue.** `lr.R = nil` executes
unconditionally immediately after `buf.ReadFrom(lr)` returns, BEFORE `limitedReaderPool.Put(lr)`,
on every path (success, short-read, and genuine underlying-reader error) — no early return sits
between them. Since `io.LimitedReader.R` is an exported field (unlike Batch D's `bytes.Reader.s`,
which needed `unsafe`+`reflect` to inspect), this was checked directly, more simply than the PNG
finding. Independently confirmed via a from-scratch PoC (opaque-wrapped source forcing the
*bytes.Buffer-sink path, then draining/inspecting 64 pool slots): 0/64 drained `*io.LimitedReader`
values retain a non-nil `R`, both on the success path and after injecting a genuine (non-EOF)
error mid-copy via a custom `errReader`.

**Short-read/EOF semantics — VERIFIED CORRECT.** `(*bytes.Buffer).ReadFrom` treats `io.EOF` as
success (per its documented contract) and would otherwise silently under-deliver bytes on a
genuinely short source; the code explicitly detects `err == nil && written != n` and substitutes
`io.ErrUnexpectedEOF`, matching `io.ReadFull`'s convention used by the slow/pooled path. Confirmed
via PoC: a source with only 100 bytes asked to supply 200 via the fast path returns
`io.ErrUnexpectedEOF`, not a silent partial success. A genuine underlying error (non-EOF) is
propagated unmasked (not overwritten by the short-read substitution, since that branch only fires
when `err == nil`).

**WriteTo fast path — VERIFIED CORRECT after partial consumption and Seek.** `bytes.Reader.Len()`
reflects the unread portion from the CURRENT position (Go stdlib clamps it to >=0 even after a
Seek past the declared end), so `n == br.Len()` correctly captures "what's left from here," and
`WriteTo` writes exactly that regardless of prior raw `Read` calls or an explicit `Seek` performed
before `StreamCopyN` was invoked. Confirmed via PoC: consume 500 bytes via `io.ReadFull`, then
`Seek(300, SeekCurrent)`, then `StreamCopyN(r, w, r.Len())` — output matches `data[800:]` exactly,
reader fully drained afterward.

**Concurrency — VERIFIED SAFE under -race.** 64 goroutines exercising BOTH fast paths
simultaneously (20 repeated runs under `-race`): clean. `sync.Pool.Get`/`Put` themselves are
Go-stdlib-guaranteed safe for concurrent use, and each `*io.LimitedReader` obtained via `Get()` is
exclusively owned by the calling goroutine until its matching `Put()` — no shared mutable state
crosses goroutines outside the pool's own synchronization.

**No output regression.** Corpus-wide Write() SHA-256 comparison (git-stash before/after) across
the full 3339-file real-world testdata/corpus: 0 differences beyond the 3 already-known,
pre-existing non-deterministic JPEG extended-XMP-GUID files (unrelated to this change, reconfirmed
present in every prior batch's audit).

Tooling: go build/vet clean; go test ./... all green; go test -race on internal/iobuf clean
(including a stress run, -count=20, of the concurrent-fast-paths PoC). All 5 ad hoc verification
tests were removed from the tree after passing (no defect found, nothing to leave behind); the
package's own new tests (TestStreamCopyNBytesReaderSourceFullRemainder/Partial,
TestStreamCopyNBytes{Reader,Buffer}...NoAddedAllocation) and read_task297_test.go
(TestParseEXIFAllocsPerRun) were left as committed by the implementer and pass. No stray scratch
file (internal/iobuf/zzalloccheck_test.go, flagged by the coordinator as a thing to check) was
present in the tree.

Verdict: CLEARED for commit.
