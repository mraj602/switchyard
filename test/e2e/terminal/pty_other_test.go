//go:build !linux

package terminal

import "testing"

// E11 needs a pty made with /dev/ptmx (Linux).
func TestE11NeverTypes(t *testing.T) {
	t.Skip("E11 builds a pty by hand and runs on Linux only")
}
