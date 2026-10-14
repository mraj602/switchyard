//go:build darwin

package peers

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// PeerPID on macOS: LOCAL_PEERPID for the pid and LOCAL_PEERCRED for the uid (peers.md 2.3).
func PeerPID(c *net.UnixConn) (pid, uid int, err error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var serr error
	if err := raw.Control(func(fd uintptr) {
		pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if serr != nil {
			return
		}
		var cred *unix.Xucred
		if cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED); serr == nil {
			uid = int(cred.Uid)
		}
	}); err != nil {
		return 0, 0, err
	}
	if serr != nil {
		return 0, 0, fmt.Errorf("peer credentials: %w", serr)
	}
	return pid, uid, nil
}
