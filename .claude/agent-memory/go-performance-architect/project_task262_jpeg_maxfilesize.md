---
name: project_task262_jpeg_maxfilesize
description: format/jpeg #262 defense-in-depth maxFileSize cap via pooled countingReader; io.WriterTo fast-path pitfall discovered via benchmarking
metadata:
  type: project
---

Task #262 added the project-wide #140-style `maxFileSize` (256 MiB) aggregate
cap to `format/jpeg`, the one container package that lacked it (audit rated
INFO — JPEG's per-segment 65535-byte length field already bounded
amplification; this was uniformity hardening, not a vulnerability fix).

**Why JPEG couldn't reuse the sibling `io.ReadAll(io.LimitReader(r,
maxFileSize+1))` idiom**: format/jpeg streams the marker sequence
incrementally (`readSegment`) rather than buffering the whole file; replacing
that with a bulk read would have been a memory regression. Solution: a pooled
`*countingReader` (`format/jpeg/limits.go`) wrapping the caller's
`io.ReadSeeker`, tracking cumulative bytes read and returning
`ErrFileTooLarge` once the count exceeds `maxFileSize`. **Resets its budget on
every `Seek` call** — safe here because Extract/Inject only ever seek to
fixed, code-controlled offsets (0, or 2 to skip SOI), never
attacker-influenced ones — so Inject's IRB pre-scan and its separate main copy
pass are each independently bounded rather than double-counting the same
bytes and producing false positives on legitimate large files.

Independently, `scanMetadataSegmentsWithWire` and `extractOriginalIRB` each
track an `app13Total` running sum, separate from the read-count cap, so a
flood of many small Photoshop APP13 segments cannot accumulate unbounded
`app13Payloads` memory even before the read-cap would trip. In practice the
read-cap (a strict superset of app13 bytes) usually trips at the same
segment or one earlier — the app13-specific check is a genuine, testable
backstop, not dead code: it fires synchronously right after the segment that
crosses `app13Total`'s own threshold completes, *before* the loop's next
`readSegment` call would observe the read-cap's `exceeded` flag. See
`format/jpeg/oom_gate_test.go::TestExtractAPP13FloodExceedsAggregateCap`
(2× 1000-byte payload segments, cap=1500 — generous margin, not
razor-thin) for a worked example of the exact arithmetic needed to make this
branch fire deterministically instead of the generic read-cap.

**Pitfall discovered via benchmarking (measure-to-decide in action)**:
wrapping `r` in `countingReader` initially raised `BenchmarkJPEGInject` from
376 B/op (10 allocs) to 1017 B/op (11 allocs) — NOT from the wrapper struct
itself (sync.Pool absorbed that to zero extra allocs, confirmed via
`go tool pprof -alloc_objects`), but because `writeSOS`'s `io.Copy(w, r)`
silently lost its `io.WriterTo` fast path: `*bytes.Reader` implements
`WriteTo` (one exact-sized `Write` call), but `*countingReader` didn't, so
`io.Copy` fell back to `dst.(io.ReaderFrom)` — `*bytes.Buffer.ReadFrom`
speculatively grows its backing array by `bytes.MinRead` (512 bytes) per call
regardless of how little data remains. Fix: implement `WriteTo` on
`countingReader` that delegates to `c.r`'s own `WriteTo` when present. This
in turn reintroduced the exact bug the whole cap exists to prevent — a
`WriteTo` call copies everything in one uninterruptible shot — so a
`remainingFitsBudget()` guard (two `Seek` calls: `SeekCurrent`/`SeekEnd`,
then restore) proves the remaining data fits the budget *before* trusting the
fast path; otherwise falls back to the incremental, cap-checked
`io.Copy(w, readerOnly{c})` (a wrapper type that hides `countingReader`'s own
`WriteTo` from `io.Copy`'s `src.(WriterTo)` check to prevent infinite
recursion). `*os.File` doesn't implement `io.WriterTo`, so this extra
Seek-pair cost is only ever paid for in-memory readers where `Seek` is O(1).

Net result: `BenchmarkJPEGExtract` and `BenchmarkJPEGInject` both show
**zero** B/op and allocs/op regression vs. pre-#262 baseline (120 B/4 allocs,
376 B/10 allocs respectively); ns/op rose ~11-12% (137→152 ns/op Extract,
315→352 ns/op Inject) from the added interface indirection and cap-check
branches — a one-time per-call cost, not a per-entry hot-loop cost, and
judged an acceptable, well-documented trade-off for the security hardening.

Also fixed two adjacent, unrelated lint findings surfaced by touching this
code: a `govet shadow` false-positive on `if err := readSOI(soi); err != nil`
in `extractFullInternal` (renamed to `soiErr`), and a genuine `nilerr` finding
in `extractOriginalIRB` where a `Seek(2, ...)` failure was silently swallowed
as `(nil, nil)` — now properly surfaced as an error, since a second `Seek`
failing right after the caller's own `Seek(0, ...)` just succeeded signals a
genuinely broken `io.ReadSeeker`, not a benign condition.

Files: `format/jpeg/errors.go` (maxFileSize + ErrFileTooLarge),
`format/jpeg/limits.go` (new — countingReader/WriteTo/remainingFitsBudget/pool),
`format/jpeg/jpeg.go` (extractFullInternal, scanMetadataSegmentsWithWire,
extractOriginalIRB, Inject), `format/jpeg/oom_gate_test.go` (new — 9 tests).

See also [[feedback_append_byteorder_escape]] for prior wrapcheck-adjacent
`io.Reader`/`io.EOF` identity lessons in this codebase.
