//go:build !linux

package peers

import "net"

// PeerPID is only implemented on Linux in PE1; macOS (LOCAL_PEERPID) arrives with the agent.
func PeerPID(c *net.UnixConn) (pid, uid int, err error) { return 0, 0, ErrUnsupported }
