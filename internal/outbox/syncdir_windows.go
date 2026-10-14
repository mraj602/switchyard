//go:build windows

package outbox

// syncDir is a no-op on Windows: directories cannot be opened for FlushFileBuffers, and NTFS
// journals metadata.
func syncDir(dir string) error { return nil }
