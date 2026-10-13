//go:build !windows

package localonnx

import (
	"fmt"

	"github.com/ebitengine/purego"
)

// ortDlopen loads the ONNX Runtime shared library on unix-like platforms and
// returns a symbol resolver.
func ortDlopen(path string) (func(name string) uintptr, error) {
	handle, err := purego.Dlopen(path, purego.RTLD_NOW)
	if err != nil {
		return nil, fmt.Errorf("dlopen %s: %w", path, err)
	}
	return func(name string) uintptr {
		addr, err := purego.Dlsym(handle, name)
		if err != nil {
			return 0
		}
		return addr
	}, nil
}
