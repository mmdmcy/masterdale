//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package autodale

import "syscall"

func rootDiskUsage() (diskUsage, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return diskUsage{}, false
	}
	blockSize := uint64(st.Bsize)
	return diskUsage{
		totalBytes: st.Blocks * blockSize,
		freeBytes:  st.Bavail * blockSize,
	}, true
}
