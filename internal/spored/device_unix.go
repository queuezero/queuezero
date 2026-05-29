//go:build unix

package spored

import (
	"fmt"
	"os"
	"syscall"
)

// deviceID returns the device number backing path, used by MountProbe to detect
// whether a path is a distinct mount. Works on Linux (compute nodes) and other
// Unix (the dev/CI machine).
func deviceID(path string) (uint64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("%s: no syscall.Stat_t (unsupported platform)", path)
	}
	return uint64(st.Dev), nil
}
