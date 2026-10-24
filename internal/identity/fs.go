package identity

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FS is the read-only view of the user's Claude folders that this package uses. Every
// access to ~/.claude* goes through it, so a test can record exactly what is opened.
// (State-folder files that wy owns, such as machine.json, are accessed directly.)
type FS interface {
	Open(name string) (io.ReadCloser, error)
	Stat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	EvalSymlinks(name string) (string, error)
}

// ErrForbidden is returned for files wy must never read (identity.md section 4).
var ErrForbidden = errors.New("identity: refusing to access a credentials file")

var claudeFolder = regexp.MustCompile(`^\.claude([-_].+)?$`)

// Forbidden reports whether name is a file wy must never open: .credentials.json,
// sessions/*.key, or anything under a Claude folder's daemon/ folder. Names are compared
// without case: the default file systems of macOS and Windows ignore it, so
// ".Credentials.json" opens the same file there.
func Forbidden(name string) bool {
	clean := strings.ToLower(filepath.ToSlash(filepath.Clean(name)))
	parts := strings.Split(clean, "/")
	base := parts[len(parts)-1]
	if base == ".credentials.json" {
		return true
	}
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "sessions" && strings.HasSuffix(base, ".key") {
			return true
		}
		if parts[i] == "daemon" && i > 0 && claudeFolder.MatchString(parts[i-1]) {
			return true
		}
	}
	return false
}

// OSFS is the real file system with the Forbidden guard built in.
type OSFS struct{}

func (OSFS) Open(name string) (io.ReadCloser, error) {
	if Forbidden(name) {
		return nil, ErrForbidden
	}
	return os.Open(name)
}

func (OSFS) Stat(name string) (fs.FileInfo, error) {
	if Forbidden(name) {
		return nil, ErrForbidden
	}
	return os.Stat(name)
}

func (OSFS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }

func (OSFS) EvalSymlinks(name string) (string, error) { return filepath.EvalSymlinks(name) }
