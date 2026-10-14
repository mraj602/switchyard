//go:build !linux && !darwin

package peers

import "net"

// PeerPID is only implemented on Linux and macOS.
func PeerPID(c *net.UnixConn) (pid, uid int, err error) { return 0, 0, ErrUnsupported }
