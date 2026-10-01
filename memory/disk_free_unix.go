//go:build !windows

package memory

import (
	"os"
	"path/filepath"
	"syscall"
)

// diskFreeBytes returns free disk space in bytes for the filesystem
// holding path (POSIX implementation via statfs).
func diskFreeBytes(path string) (int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(abs, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// osPathDir returns the directory portion of path.
func osPathDir(path string) string { return filepath.Dir(path) }
