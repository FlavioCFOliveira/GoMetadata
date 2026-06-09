//go:build windows

package gometadata

import (
	"os"
	"testing"
)

// assertOwnershipPreserved is a no-op on Windows: the platform does not
// expose POSIX uid/gid ownership semantics via os.FileInfo.Sys(), so there
// is nothing to assert.
func assertOwnershipPreserved(_ *testing.T, _, _ os.FileInfo) {}
