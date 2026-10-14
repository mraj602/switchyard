//go:build windows

package peers

// lockFile locks nothing on Windows, where peers are not supported (peers.md 2.1).
func lockFile(string) func() { return func() {} }
