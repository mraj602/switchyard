//go:build !windows

package tui

import "os"

// EnableVT is a no-op outside Windows: Unix terminals read escape codes already. The
// returned restore does nothing.
func EnableVT(in, out *os.File) (restore func(), err error) {
	return func() {}, nil
}
