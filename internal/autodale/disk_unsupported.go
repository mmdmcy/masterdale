//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package autodale

func rootDiskUsage() (diskUsage, bool) {
	return diskUsage{}, false
}
