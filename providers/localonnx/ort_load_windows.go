//go:build windows

package localonnx

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// ortDlopen loads the ONNX Runtime shared library on Windows and returns a
// symbol resolver (purego's Dlopen is unix-only; see its docs).
func ortDlopen(path string) (func(name string) uintptr, error) {
	dll := windows.NewLazySystemDLL(path)
	if err := dll.Load(); err != nil {
		return nil, fmt.Errorf("LoadLibrary %s: %w", path, err)
	}
	return func(name string) uintptr {
		proc := dll.NewProc(name)
		if err := proc.Find(); err != nil {
			return 0
		}
		return proc.Addr()
	}, nil
}
