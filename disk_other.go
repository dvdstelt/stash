//go:build !(linux || darwin || freebsd)

package main

// diskFree can't ask the operating system here, so it doesn't limit uploads by disk space.
func diskFree(dir string) int64 { return 1 << 50 }
