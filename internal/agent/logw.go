package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rotatingLog appends to a file, rotating it at max bytes and keeping keep older files
// (agent.log.1 ... agent.log.<keep>): peers.md 2.2 says 5 MB and 3.
type rotatingLog struct {
	mu        sync.Mutex
	path      string
	max, keep int
	f         *os.File
	size      int
}

func newRotatingLog(path string, max, keep int) (*rotatingLog, error) {
	l := &rotatingLog{path: path, max: max, keep: keep}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return l, l.open()
}

func (l *rotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	l.size = 0
	if fi, err := f.Stat(); err == nil {
		l.size = int(fi.Size())
	}
	l.f = f
	return nil
}

func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+len(p) > l.max && l.size > 0 {
		l.f.Close()
		for i := l.keep; i >= 1; i-- {
			from := l.path
			if i > 1 {
				from = fmt.Sprintf("%s.%d", l.path, i-1)
			}
			_ = os.Rename(from, fmt.Sprintf("%s.%d", l.path, i))
		}
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += n
	return n, err
}

func (l *rotatingLog) Close() error { return l.f.Close() }
