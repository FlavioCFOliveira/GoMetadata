---
name: feedback_lint_iteration_after_new_code
description: Always run golangci-lint after adding new branches/tests to hot-path parsers — new nolint directives and complexity bumps are common and must be resolved before declaring done
metadata:
  type: feedback
---

Adding even a small new guard branch to an already-complex binary parser function (e.g.
adding a zero-field-size DoS cap to `readIlocFullExtents`/`readIlocSimpleExtents` in
`format/heif/heif.go`) can push `gocyclo`/`cyclop` over the threshold by exactly 1. The
project's established convention for these binary-parser hot paths is a `//nolint:gocyclo`
(or `cyclop,gocyclo`) directive with a one-line justification citing the spec section —
see the many precedents already in `format/heif/heif.go` (e.g. `parseIlocItemSimple`).
Do not restructure a tight, already-reviewed parsing loop just to dodge a complexity
linter when the branches are all mandatory spec-derived bounds checks; add the documented
nolint instead.

Separately: new test-helper code that does `uint32(smallLocalVar)` conversions where the
local var is provably bounded (e.g. `i` from `range itemCount`, or an `int` derived from a
compile-time constant like `headerFixed`) frequently does NOT trigger gosec G115 even
though a similar-looking `uint32(len(x))` elsewhere in the same file does. Never
copy-paste a `//nolint:gosec // G115: ...` comment onto a new line without first running
`golangci-lint run` to confirm gosec actually fires there — `nolintlint` will flag the
unused directive otherwise. This reconfirms [[feedback_nolint_placement_on_line]] and
[[feedback_gosec_g115_inconsistent]].

**Why:** discovered while closing out the 2026-07-06 second security-audit batch — 6
lint issues appeared only after adding the FIX 1/FIX 3/FIX 5 tests and the FIX 1 loop
guards; all were resolved by (a) adding 2 justified `//nolint:gocyclo` directives on the
two `heif.go` extent-reader functions, (b) removing 2 stale/unused `//nolint:gosec`
directives from test helpers, and (c) 2 straightforward gocritic fixups (combine
sequential `append` calls; use `bytes.Equal` instead of `string(x) != string(y)`).

**How to apply:** after implementing any fix that touches a hot-path parser or adds new
test helpers with manual byte-packing, run `golangci-lint run ./...` before considering
the work done — do not rely on `go build`/`go vet` alone.
