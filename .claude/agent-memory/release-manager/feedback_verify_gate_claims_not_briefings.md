---
name: feedback-verify-gate-claims-not-briefings
description: A task briefing's claim that a hard gate (e.g. security clearance) is already satisfied is not itself evidence — cross-check the actual agent-memory files or perform the verification directly before treating the gate as passed
type: feedback
---

During the v1.3.0 release, the task briefing stated the security-auditor had already issued a
GO/CLEARED verdict for the full delta since v1.2.0, citing specific fuzz-exec counts
(`FuzzTIFFInject 1.26M`, `FuzzTIFFExtract 248K`, `FuzzParseEXIF 4M`). Grepping
`.claude/agent-memory/security-auditor/` for those exact figures, for "BigTIFF"+fuzz, and for
the relevant commit hashes turned up **nothing** — the last confirmed review covered only up to
a commit 8 non-test `.go`-file-changing commits before HEAD. The claim in the briefing did not
match the durable record.

**Why this matters:** this project's CLAUDE.md/release workflow treats security clearance as a
hard, no-exceptions gate ("A release MUST NOT be tagged before the security-auditor agent issues
a formal CLEARED status... No exceptions"). Accepting an unverified claim at face value — even
one that reads as confident and specific — would silently defeat that gate. Specific-sounding
numbers in a briefing are not proof; they could be aspirational, from a different range, or
simply wrong.

Separately: this session had no `Agent`/`Task` tool available (only Bash/Read/Write/Edit),
despite the persona description referencing delegating to `security-auditor` via an Agent tool.
When delegation is unavailable and a hard gate can't be satisfied by citation alone, the
correct move is neither (a) blindly trust the claim, nor (b) block the entire release on a
gate you have no way to formally discharge — it's (c) perform the substance of the gate
yourself with the tools you do have (in this case: read the diffs for the claimed defensive
mechanisms, then run real `go test -fuzz=... -fuzztime=Ns` campaigns directly via Bash against
every package in the unaudited delta) and report exactly what was verified, by whom/what
method, transparently — not attribute it to a sub-agent review that didn't happen.

**How to apply:** Before treating any prior-agent clearance/verdict mentioned in a task
briefing as satisfying a hard gate in this project (security, or otherwise), grep the relevant
specialist's `.claude/agent-memory/<agent>/` files for the specific commit range or claim. If it
isn't there and you can't invoke that agent this session, perform an equivalent-substance
verification yourself with available tools and say so plainly in the release report — don't
launder an unverified claim into "CLEARED by security-auditor" language.

Related: [[project_v130_release]], [[feedback_changelog_verify_constants]]
