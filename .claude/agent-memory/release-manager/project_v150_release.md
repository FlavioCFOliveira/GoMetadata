---
name: v1.5.0 release record
description: v1.5.0 RELEASED 2026-10-17 (tag v1.5.0, main 9bd1e4e, develop back-merge 804ffed); gates, benchmark regression bisect, gitflow details
metadata:
  type: project
---

Released after the security gate was CLEARED (audit_20261016_v150_preclearance.md; fixes f6f5291 + 68357f7). Release branch release/1.5.0 from develop 1a72990; release commit 0d6cf53 (docs only after gates); main merge 9bd1e4e (--no-ff), tag v1.5.0 annotated; develop back-merge 804ffed. Pushed main, develop, tag to Github and origin, all fast-forward. GitHub release https://github.com/FlavioCFOliveira/GoMetadata/releases/tag/v1.5.0 (notes = CHANGELOG section, 70 KB, accepted). release/1.5.0 and feature/47-* branches kept (user decides cleanup); release branch not pushed.

Gates on release branch: build, vet, gofmt, go mod tidy (clean), go test -race -count=1 ./... (26 pkgs ok), golangci-lint 2.14.0 (0 issues), govulncheck (0 affecting).

Benchmark regressions (143 benchmarks, all root-caused by worktree A/B bisect, none from sprint 47 fixes): ReadCombinedMetadataJPEG +72% / 39->56 allocs from 5af9080 (XMP conformance); MWGAccessors 47->78 ns from 83810f6 (table-driven reconciliation); Relocate* +3 allocs from F-07a planner.

**Why / how to apply:** worktree benchmarks of old commits need `testdata` symlinked in (corpus is gitignored; benchmark silently Skips otherwise and prints nothing). The v1.4.0 baseline is post-fuzz-inflated, so ns/op improvements vs it are partly artefact; confirm regressions with a clean A/B. `gh release view --json isLatest` is not a valid field; use `gh release list`. The date rolled over midnight mid-release: use the date at tag time in the CHANGELOG header.
