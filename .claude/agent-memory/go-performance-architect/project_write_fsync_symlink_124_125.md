---
name: project_write_fsync_symlink_124_125
description: WriteFile audit fixes #124 (fsync before rename) and #125 (symlink target + uid/gid preservation), commit 278a38b
metadata:
  type: project
---

WriteFile was fixed in commit 278a38b for two audit findings:

**#124 — fsync before rename**: `tmp.Sync()` is now called after the write and before `Close`/`Rename`. If `Sync` fails the temp is removed and the original file is left intact. After `Rename`, the parent directory is fsynced via `fsyncDir` (best-effort, errors ignored).

**#125 — symlink and ownership**: `filepath.EvalSymlinks(path)` is called at the top of `WriteFile`; the resolved real path is used as the rename target so `Rename` replaces the real file, not the symlink. `chownFile(tmp, f)` (best-effort, ignores EPERM) transfers uid/gid from the original file to the temp before rename.

**Why:** A crash between write and sync could leave a truncated replacement file. Renaming onto a symlink replaced the link with a regular file, breaking all other references to the real path.

**Platform isolation pattern:** Unix-only syscalls are in `write_unix.go` (`//go:build !windows`); Windows stubs are in `write_windows.go` (`//go:build windows`). Test uid/gid assertions are in `write_unix_test.go` / `write_windows_test.go` using the same split. The main `write_test.go` calls `assertOwnershipPreserved(t, fiBefore, fiAfter)` which dispatches to the platform-specific helper.

**Gate tests:**
- `TestWriteFileSyncsBeforeRename/produces_complete_output`
- `TestWriteFileSyncsBeforeRename/original_intact_on_rename_failure`
- `TestWriteFilePreservesSymlink`
- `TestWriteFilePreservesOwnershipAndMode`

**How to apply:** Any future OS-level WriteFile enhancements (e.g. Windows ACL preservation) should follow the same `_unix.go` / `_windows.go` split pattern.
