//go:build !windows

package gometadata

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// assertOwnershipPreserved verifies that the uid and gid of fiAfter match
// those of fiBefore. It is the Unix half of TestWriteFilePreservesOwnershipAndMode.
//
// Chown-to-self is always permitted for the file owner, so this assertion is
// testable without root: WriteFile reads uid/gid from the original file and
// applies them to the temp file via chownFile before the rename.
func assertOwnershipPreserved(t *testing.T, fiBefore, fiAfter os.FileInfo) {
	t.Helper()

	before, beforeOK := fiBefore.Sys().(*syscall.Stat_t)
	after, afterOK := fiAfter.Sys().(*syscall.Stat_t)
	if !beforeOK || !afterOK {
		// syscall.Stat_t not available — skip the uid/gid assertion.
		return
	}
	if after.Uid != before.Uid {
		t.Errorf("#125 uid: got %d, want %d", after.Uid, before.Uid)
	}
	if after.Gid != before.Gid {
		t.Errorf("#125 gid: got %d, want %d", after.Gid, before.Gid)
	}
}

// TestWriteFileMasksSetuidSetgidSticky is the gate for audit finding #259(a):
// WriteFile must never propagate setuid, setgid, or the sticky bit from the
// original file onto the re-encoded replacement, even when the original
// file legitimately carried them.
//
// write.go masks these bits off with
// fi.Mode() &^ (os.ModeSetuid | os.ModeSetgid | os.ModeSticky) before calling
// tmp.Chmod, unconditionally — CWE-732-adjacent hardening: a metadata
// rewrite must never (re)create a privilege-escalation surface.
//
// Setting S_ISGID via chmod(2) is subject to a kernel privilege check: on
// several Unix implementations (observed on Darwin), the call fails
// entirely with EPERM unless the calling process's effective or a
// supplementary group ID matches the file's group. The test therefore
// chowns the file's group to the test process's own gid (always permitted
// for a file the process owns) before requesting setgid, so the setup step
// itself does not depend on any elevated privilege. If, despite that, the
// underlying platform still cannot grant setuid/setgid/sticky at setup time
// (e.g. a restrictive sandbox or an unusual filesystem), the test skips
// cleanly: this is an OS/privilege limitation, not a library bug, per
// docs/TESTING.md §2.1.
func TestWriteFileMasksSetuidSetgidSticky(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "image.jpg")
	original := buildMinimalJPEG(minimalTIFFPayload())

	if err := os.WriteFile(target, original, 0o755); err != nil { //nolint:gosec // G306: intentional 0755 for privilege-bit masking test
		t.Fatalf("setup: %v", err)
	}

	// Chown the group to our own gid so the setgid chmod below does not
	// depend on the file's inherited group (which on BSD-derived systems
	// defaults to the parent directory's group, not necessarily one the
	// process belongs to). Self-to-self group chown requires no privilege.
	if err := os.Chown(target, -1, os.Getgid()); err != nil {
		t.Skipf("#259 setup: cannot chgrp to own gid on this platform: %v", err)
	}

	wantSetup := os.FileMode(0o755) | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if err := os.Chmod(target, wantSetup); err != nil {
		// EPERM here is an OS/privilege limitation (docs/TESTING.md §2.1):
		// some sandboxes refuse S_ISUID/S_ISGID/S_ISVTX entirely regardless
		// of ownership. There is nothing the library can do to influence
		// this, so the test cannot exercise the masking logic here.
		t.Skipf("#259 setup: cannot set setuid/setgid/sticky on this platform: %v", err)
	}

	fiBefore, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}
	wantBits := os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if fiBefore.Mode()&wantBits != wantBits {
		// The kernel silently cleared a bit instead of returning an error;
		// same OS/privilege-limitation category as above.
		t.Skipf("#259 setup: setuid/setgid/sticky not all present after chmod (mode=%v); skipping", fiBefore.Mode())
	}

	m, err := ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	m.SetCopyright("© 2026 #259 setuid/setgid/sticky mask gate")

	if err := WriteFile(target, m); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fiAfter, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}

	if fiAfter.Mode()&wantBits != 0 {
		t.Errorf("#259 setuid/setgid/sticky not masked: got mode %v", fiAfter.Mode())
	}

	wantPerm := os.FileMode(0o755)
	if gotPerm := fiAfter.Mode().Perm(); gotPerm != wantPerm {
		t.Errorf("#259 ordinary permission bits not preserved: got %v, want %v", gotPerm, wantPerm)
	}
}
