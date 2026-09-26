---
name: feedback-corpuswide-bench-and-cpu-contention
description: Three benchmarking-methodology rules confirmed by the Batch F follow-up (2026-09-26) — corpus-wide per-file sweeps are required for any data-dependent algorithm change, background CPU contention can fully fabricate "regressions" that don't exist, and `pgrep -f` wait-loops can match their own command line and never exit.
metadata:
  type: feedback
---

**Rule 1: A fixed 5-15 fixture e2e harness is not sufficient evidence for "no regression" when the changed code makes data-dependent trade-offs (varies its behavior per input's structure, not just its size).**

Why: Sprint 44 Batch F's #289 (TIFF metadata-prefix read) passed its 5-fixture harness
(tiff/cr2/nef/arw/dng) cleanly, but a coordinator-requested corpus-wide per-file `Read`
benchmark (`testing.Benchmark` called directly per file from a standalone `main()`, HEAD vs
current, across all ~500 real corpus files) found: (a) a DoS-class ~268 MB allocation on a
325-byte malformed file the harness never included, and (b) a real, non-adversarial 8-13x
regression on small/medium TIFF files whose IFD chain needed many small growth passes — a
pattern none of the 5 harness fixtures happened to exhibit. Fixing (b) also required a THIRD
growth-policy design before landing on one that didn't break a DIFFERENT harness fixture
(NEF) that had been passing the whole time.

How to apply: whenever a change makes a data-DEPENDENT decision (how many passes/retries
something needs, how close some computed value is to a threshold, which code path a
structural property of the input selects) — not just a size-dependent one — write a
standalone per-file corpus sweep (reuse `testing.Benchmark` from a `main()` for
per-file granularity; `go test -bench` alone only gives you the fixtures you hardcoded)
and report the DISTRIBUTION (median/p90/p95/p99/max ratio vs baseline), not just
whether the fixed harness fixtures passed. See
[[sprint44-batchF-288-289]] for the full incident.

**Rule 2: A benchmark result that looks like a large, surprising, or inconsistent-with-a-prior-measurement regression should first be checked against `pgrep` for other CPU-intensive processes (other `go test`/fuzz/benchmark runs) before being trusted or investigated as a real code problem.**

Why: in the same session, a `BenchmarkRoundTrip` measurement showed 55-70% regressions
across every format, and a corpus-wide Read sweep showed p90=2.75x/max=14.1x — both taken
while OTHER background fuzz/benchmark jobs from the SAME session were still running and
competing for CPU. Re-measured with zero other processes active (verified via `pgrep`
first), the SAME benchmarks showed mostly net-neutral RoundTrip results and a
p90=1.05x/max=1.17x corpus-wide distribution — the "regressions" were entirely CPU
contention artifacts, not real.

How to apply: before reporting ANY benchmark number as evidence (especially one that
contradicts an earlier measurement, or that looks disproportionately bad relative to what
the code change should plausibly cause), confirm nothing else CPU-intensive is running from
earlier in the same session before trusting the number. If in doubt, re-run once cleanly
rather than investigating a phantom regression.

**Rule 3: `pgrep -f "<substring>"` matches the FULL COMMAND LINE of every process,
including the wait-loop's own `pgrep` invocation if the substring appears in it — a
`until ! pgrep -f "foo" > /dev/null; do sleep N; done` loop waiting for a process named
`foo` to finish will never exit, because the loop's own command line (visible via `ps`)
contains the string `"foo"` and matches itself on every iteration.** Confirmed by the
coordinator directly (2026-09-26): 12 such loops were left running indefinitely in one
session, each waiting on a `corpusbench-cur` process that had already finished, because the
loop checking for it also matched itself. Use `pgrep -x <exact-binary-name>` (exact match,
no substring), or capture the PID directly (`cmd & pid=$!; wait "$pid"`), instead of `pgrep
-f` with a substring, whenever the wait condition's own command line might contain the
search string.
