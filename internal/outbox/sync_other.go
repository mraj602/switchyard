//go:build !linux && !darwin

package outbox

import "os"

// syncFile is FlushFileBuffers on Windows and fsync elsewhere.
func syncFile(f *os.File) error { return f.Sync() }
