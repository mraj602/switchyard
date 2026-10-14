//go:build linux

package peers

import (
	"fmt"
	"net"
	"syscall"
)

// PeerPID is the pid of the process on the other end of a Unix socket connection, from
// SO_PEERCRED (peers.md 2.3). The agent uses it to check a caller's ancestry.
func PeerPID(c *net.UnixConn) (pid, uid int, err error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var cred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, 0, err
	}
	if serr != nil {
		return 0, 0, fmt.Errorf("peer credentials: %w", serr)
	}
	return int(cred.Pid), int(cred.Uid), nil
}
