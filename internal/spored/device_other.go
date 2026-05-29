//go:build !unix

package spored

import "fmt"

// deviceID is unsupported on non-Unix platforms. The reporter runs on Linux
// compute nodes; this stub only keeps the package building elsewhere.
func deviceID(path string) (uint64, error) {
	return 0, fmt.Errorf("mount probe unsupported on this platform")
}
