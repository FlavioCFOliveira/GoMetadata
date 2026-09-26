---
name: feedback_png_chunktype_escape
description: Go escape analysis is control-flow-insensitive — slicing a small value-type parameter only inside a rare error branch still forces it to escape on every call; sync.Pool round-trips can be slower than a plain escape for tiny (4-8 byte) objects
type: feedback
---

Two related, non-obvious findings from task #232 (PNG `readChunk`/`readNonEmptyChunk`/
`readLargeChunk` chunk-type `[4]byte` conversion, Sprint 44 Batch D, 2026-09-25).

**Finding 1 — escape analysis is control-flow-insensitive for address-of.**
A `[4]byte` (or any small value type) function PARAMETER whose address is taken ANYWHERE
in the function body — even inside an `if err != nil` branch that executes on a tiny
fraction of calls — is marked as escaping to the heap for **every single call**, not just
the calls that take that branch. Concretely: `chunkType[:]` used only inside
`fmt.Errorf("...%q...", chunkType[:], err)` on an error path still forces `chunkType` to be
heap-allocated on the successful, hot-path calls too, because escape analysis answers "can
this value's address ever flee this function" as a static, whole-function question — it
does not distinguish "escapes on this branch" from "escapes always". This is DIFFERENT
from (and independent of) the well-known "passing a stack buffer through an io.Reader/
io.Writer interface call always escapes" rule documented in `internal/riff.ReadChunkBuf`
— that one is about an unconditionally-executed call; this one is about an error-path-only
usage still tainting the whole function.

**Reusable fix**: route the address-of/slice operation through a small, dedicated,
`//go:noinline` helper (e.g. `chunkTypeStr(t [4]byte) string { return string(t[:]) }`) and
call that helper only at the point of actually formatting the error. Passing a value type
BY VALUE into another function's call does NOT, by itself, force the caller's own copy to
escape — only inlining the callee's body back into the caller would re-taint it, which
`//go:noinline` forbids. This isolates the escaping copy to a call that only actually
allocates when the rare branch is taken and executes the call.

**Finding 2 — sync.Pool is not always faster than a plain escape for tiny objects.**
An initial implementation routed PNG's 8-byte chunk header and 4-byte CRC trailer through
`iobuf.Get`/`Put` (matching the established pattern for the size-VARIABLE chunk-data
buffer in the same function), reasoning "their address crosses an io.Reader interface call
regardless, so pool the allocation instead of paying it fresh every call" — the same logic
that correctly motivated pooling in `writeChunk` (task #231) and `readWebPChunks`'s 8-byte
header buffer (task #209/#234). For PNG's read path this measured WORSE: `BenchmarkPNGExtract`
went from 15→21 allocs/op and ~286ns→~320ns (a real regression), while removing the pooling
(keeping plain `var hdr [8]byte` / `var crcB [4]byte` stack arrays, escaping once per call
exactly as before) combined with the chunkType `[4]byte` conversion alone gave 15→14 allocs
and ~286ns→~259ns (a genuine improvement, confirmed via `benchstat -count=10`, p=0.000).
**The `sync.Pool` Get+Put round trip's own bookkeeping cost (interface type assertion,
per-P cache access) can exceed the cost of a single small (≤8 byte) heap escape that Go's
tiny-object allocator handles cheaply.** Pooling pays off for size-VARIABLE or larger
buffers (like the chunk-data buffer, sized 1 byte to 64 KiB) where avoiding a `make()`
proportional to size is the real win — not for small, fixed-size scratch arrays whose
"escape" is a single small, constant-size allocation either way.

**Why (this incident)**: caught only because the AC required `benchstat -count 10`
before/after with "no ns/op regression beyond noise" as a hard gate — a plausible-sounding,
pattern-matched optimization (writeChunk's fix looked structurally identical) turned out to
be a regression for the read-path sibling. **How to apply**: (a) before adding
`//nolint:wrapcheck`-adjacent "obviously correct" pooling to a NEW call site, measure it in
isolation even when an adjacent, structurally similar call site already benefited from the
same pattern — the direction of the win is not guaranteed to transfer; (b) whenever a
`[4]byte`/`[N]byte` value parameter is used in an error-message `%q`/`%v` format call,
check `go build -gcflags="-m -m"` for "moved to heap: <param>" attributed to that function
BEFORE concluding the parameter is cheap to keep as a value type — if present, route the
error-only usage through a `//go:noinline` helper first, then re-measure.

See [[project_sprint44_batchD_228_234]] for the full task list this was found in.
