//go:build !windows

package gometadata

import (
	"os"
	"syscall"
)

// chownFile sets the uid/gid of dst to match the uid/gid of src on Unix
// systems. It is a best-effort operation: EPERM (not running as root or
// lacking CAP_CHOWN) and unsupported-filesystem errors are silently ignored
// so that WriteFile continues to completion even when ownership transfer is
// not permitted.
//
// The intent is to preserve the original owner when WriteFile atomically
// replaces a file: the temp file is initially owned by the effective user;
// chownFile corrects that before the rename so the replacement has the same
// uid/gid as the file it replaces.
func chownFile(dst, src *os.File) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(src.Fd()), &st); err != nil {
		return // cannot stat source; skip silently
	}
	_ = dst.Chown(int(st.Uid), int(st.Gid))
}

// fsyncDir opens the directory at dirPath and calls Sync on it, then closes it.
// Directory fsync flushes the directory entry created by os.Rename to durable
// storage, so the renamed file is visible after a crash.
//
// This is a best-effort operation: some filesystems (e.g. FAT32, tmpfs) and
// some OS configurations do not support directory fsync and return EINVAL or
// similar. All errors are silently ignored per the contract for audit finding
// #124.
func fsyncDir(dirPath string) {
	d, err := os.Open(dirPath)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
