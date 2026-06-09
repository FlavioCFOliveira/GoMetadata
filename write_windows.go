//go:build windows

package gometadata

import "os"

// chownFile is a no-op on Windows: the platform does not expose POSIX
// uid/gid ownership semantics via os.File.Chown.
func chownFile(_, _ *os.File) {}

// fsyncDir is a no-op on Windows: directory file descriptors are not
// directly sync-able via the standard Go API on this platform.
func fsyncDir(_ string) {}
