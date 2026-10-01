//go:build windows

package memory

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// diskFreeBytes returns free disk space in bytes for the volume holding
// path (Windows implementation via GetDiskFreeSpaceExW).
func diskFreeBytes(path string) (int64, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")

	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	utf16Path, err := syscall.UTF16PtrFromString(abs)
	if err != nil {
		return 0, err
	}

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	r1, _, errNo := proc.Call(
		uintptr(unsafe.Pointer(utf16Path)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r1 == 0 {
		if errno, ok := errNo.(syscall.Errno); ok && errno != 0 {
			return 0, errno
		}
		return 0, errNo
	}
	return int64(freeBytesAvailable), nil
}

// osPathDir returns the directory portion of path.
func osPathDir(path string) string { return filepath.Dir(path) }
