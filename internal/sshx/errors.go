package sshx

import (
	"fmt"
	"strings"
)

// Class is the kind of an ssh failure (exit 255), read from its stderr.
type Class string

// Failure classes.
const (
	ClassUnknownHostKey Class = "unknown_host_key"
	ClassChangedHostKey Class = "changed_host_key"
	ClassAuth           Class = "auth"
	ClassConnect        Class = "connect"
	ClassForward        Class = "forward"
	ClassOther          Class = "ssh"
)

// Error is ssh's own failure: it could not connect, authenticate, verify the host or set up
// a forward. Exit is ssh's status (255).
type Error struct {
	Class  Class
	Host   string
	Exit   int
	Detail string // the most telling stderr line, no secrets (ssh prints none)
}

func (e *Error) Error() string {
	switch e.Class {
	case ClassUnknownHostKey:
		return fmt.Sprintf("the host key of %s is not known: ssh %s once by hand to check and accept its host key", e.Host, e.Host)
	case ClassChangedHostKey:
		return fmt.Sprintf("the host key of %s CHANGED since you last connected: check the server before you trust it; wy changes nothing until ssh %s works again", e.Host, e.Host)
	case ClassAuth:
		return fmt.Sprintf("ssh could not log in to %s (%s): check that ssh %s works without a password prompt", e.Host, e.Detail, e.Host)
	case ClassConnect:
		return fmt.Sprintf("cannot connect to %s: %s", e.Host, e.Detail)
	case ClassForward:
		return fmt.Sprintf("ssh to %s could not set up the port forward: %s", e.Host, e.Detail)
	}
	if e.Detail != "" {
		return fmt.Sprintf("ssh to %s failed: %s", e.Host, e.Detail)
	}
	return fmt.Sprintf("ssh to %s failed (exit %d)", e.Host, e.Exit)
}

// Classify reads an ssh failure from its exit status and stderr.
func Classify(host string, exit int, stderr string) *Error {
	e := &Error{Host: host, Exit: exit, Class: ClassOther, Detail: lastLine(stderr)}
	low := strings.ToLower(stderr)
	pick := func(needle string) string {
		for _, l := range strings.Split(stderr, "\n") {
			if strings.Contains(strings.ToLower(l), needle) {
				return strings.TrimSpace(l)
			}
		}
		return e.Detail
	}
	switch {
	case strings.Contains(low, "remote host identification has changed") || strings.Contains(low, "host key for") && strings.Contains(low, "has changed"):
		e.Class = ClassChangedHostKey
	case strings.Contains(low, "host key verification failed") || strings.Contains(low, "no ") && strings.Contains(low, "host key is known"):
		e.Class = ClassUnknownHostKey
	case strings.Contains(low, "remote port forwarding failed") || strings.Contains(low, "cannot listen to port") ||
		strings.Contains(low, "remote forwarding failed"):
		e.Class, e.Detail = ClassForward, pick("forward")
	case strings.Contains(low, "permission denied") || strings.Contains(low, "too many authentication failures"):
		e.Class, e.Detail = ClassAuth, "Permission denied"
	case strings.Contains(low, "could not resolve hostname"), strings.Contains(low, "connection refused"),
		strings.Contains(low, "connection timed out"), strings.Contains(low, "operation timed out"),
		strings.Contains(low, "no route to host"), strings.Contains(low, "network is unreachable"),
		strings.Contains(low, "connection closed by"), strings.Contains(low, "connection reset"):
		e.Class = ClassConnect
		for _, n := range []string{"could not resolve", "refused", "timed out", "no route", "unreachable", "closed by", "reset"} {
			if strings.Contains(low, n) {
				e.Detail = pick(n)
				break
			}
		}
	}
	return e
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			if len(l) > 200 {
				l = l[:200]
			}
			return l
		}
	}
	return ""
}
