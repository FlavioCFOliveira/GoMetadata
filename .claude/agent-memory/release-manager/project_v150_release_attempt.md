---
name: v1.5.0 release attempt
description: 2026-10-16 v1.5.0 attempt halted at the security gate because the Agent tool (security-auditor) was unavailable; pre-flight evidence and a second unannounced incompatible change (xmp.NSxmpNote)
metadata:
  type: project
---

Attempt on develop @ 3d74da6 (v1.4.0..HEAD, 59 commits). No branch, tag or push was created.

- apidiff (v1.4.0 vs HEAD, per package via `apidiff -w` in a v1.4.0 worktree) shows TWO incompatible changes: `exif.TypeUTF8` 13 -> 129 (briefed, user accepted) and `xmp.NSxmpNote` value "http://ns.adobe.com/xap/1.0/se/Note/" -> "http://ns.adobe.com/xmp/note/" (commit 946e57b, NOT briefed). Document both in the compatibility notice.
- apidiff gotcha: run under bash (zsh does not word-split `$pkgs`); `-m` means module mode, do not pass it for package diffs.
- Gates green at HEAD: build, vet, gofmt, go mod tidy (clean), go test -race -count=1 ./..., golangci-lint 2.14.0 (0 issues), govulncheck (0 affecting).
- 44 fuzz targets x 15s: zero crashers, tree stayed clean.
- Security gate: ~20k lines of non-test Go changed in sprints 45/46 with NO security-auditor record (last record 2026-09-26). Session had no Agent tool, so CLEARED could not be obtained; stopped and escalated per the hard gate (unlike v1.3.0 where self-verification was used).
- core.hooksPath still stale (/Users/flaviocfo/dev/img-metadata/.git/hooks).
- origin/develop was behind (321704f); Github/develop and local develop were equal.
