# Memory Index

- [GoMetadata release patterns](project_release_patterns.md) — two remotes (origin/Github), pre-commit hook enforces tests+lint, benchmark run-convention, BENCHMARKS.md needed, benchmarks/results/ archive, go.mod tidy may change on release
- [v1.1.0 release record](project_v110_release.md) — gate results, go.mod tidy finding, security-driven benchmark regressions, CHANGELOG correction record
- [v1.2.0 release record](project_v120_release.md) — gate results, security clearance method (memory-file review), benchmark regressions (all root-caused), macOS head -n -1 workaround
- [Feedback: verify constants before CHANGELOG](feedback_changelog_verify_constants.md) — always read source for cap values/error names/fix mechanisms; never infer from design comments
- [v1.3.0 readiness assessment](project_v130_readiness_assessment.md) — gates green, security-auditor already CLEARED for full v1.2.0..HEAD range, MINOR bump justified; docs gaps now CLOSED (see below), repo is release-READY
- [v1.3.0 docs-gap-closure record](project_v130_docs_gap_closure.md) — SECURITY.md fuzz table was missing Inject/RIFFRead targets too (not just the count), root BENCHMARKS.md sections are non-chronological, GM-W1 fix improved benchmarks, cross-file baseline mismatches, median-precision lesson
