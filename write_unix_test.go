//go:build !windows

package gometadata

import (
	"os"
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
