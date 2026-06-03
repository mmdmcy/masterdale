//go:build windows

package autodale

import (
	"syscall"
	"unsafe"
)

var getDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

func rootDiskUsage() (diskUsage, bool) {
	var freeAvailableToCaller uint64
	var totalBytes uint64
	var freeBytes uint64
	ok, _, _ := getDiskFreeSpaceExW.Call(
		0,
		uintptr(unsafe.Pointer(&freeAvailableToCaller)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&freeBytes)),
	)
	if ok == 0 {
		return diskUsage{}, false
	}
	return diskUsage{totalBytes: totalBytes, freeBytes: freeBytes}, true
}
