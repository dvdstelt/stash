//go:build linux || darwin || freebsd

package main

import "syscall"

// diskFree is the space available to Stash on the disk that holds dir.
func diskFree(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
