package outbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/mraj602/switchyard/internal/record"
)

const (
	// MaxLine is the largest line, newline included (outbox.md section 2).
	MaxLine = record.MaxEncodedBytes
	// DefaultLockWait is how long a writer waits for the day-file lock before spilling.
	DefaultLockWait = 50 * time.Millisecond

	lockRetry = time.Millisecond
	dayLayout = "2006-01-02"
)

// ErrTooLarge means one encoded record is over MaxLine.
var ErrTooLarge = errors.New("outbox: record over 1 MiB")

var (
	dayFileRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.jsonl$`)
	spillFileRe = regexp.MustCompile(`^spill-\d+-[0-9A-Za-z]+\.jsonl$`)
)

// Outbox is the local queue of records waiting for the hub, in directory Dir (normally
// <state>/outbox). It is safe for any number of concurrent processes and goroutines.
type Outbox struct {
	Dir string
	// NoFsync skips the sync of step 8 (config.toml outbox.fsync = false).
	NoFsync bool
	// LockWait is the time to wait for the day-file lock; zero means DefaultLockWait.
	LockWait time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	// beforeCompactRename is a test seam: called after the compacted copy is written and
	// before it replaces the day file.
	beforeCompactRename func()

	marksMu sync.Mutex
	marks   map[string]int64 // corrupt.marks.json: day file -> end offset of the last corrupt line logged
}

// New returns an outbox in dir with the default policy.
func New(dir string) *Outbox { return &Outbox{Dir: dir} }

func (o *Outbox) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Outbox) lockWait() time.Duration {
	if o.LockWait > 0 {
		return o.LockWait
	}
	return DefaultLockWait
}

// Result says where Append put a record.
type Result struct {
	ID      string
	File    string // base name of the file written
	Spilled bool   // the day file's lock was busy for LockWait, so a spill file was used
}

// Append writes one record: outbox.md section 3.
func (o *Outbox) Append(r *record.Record) (Result, error) {
	line, err := r.Encode()
	if err != nil {
		return Result{ID: r.ID}, fmt.Errorf("%w: %v", ErrTooLarge, err)
	}
	res, err := o.AppendLine(append(line, '\n'))
	res.ID = r.ID
	return res, err
}

// AppendLine writes one already encoded line, which must be a single compact JSON record
// ending in exactly one "\n". Used by `sy outbox append`.
func (o *Outbox) AppendLine(line []byte) (Result, error) {
	if len(line) > MaxLine {
		return Result{}, ErrTooLarge
	}
	deadline := time.Now().Add(o.lockWait())
	for attempt := 0; attempt < 8; attempt++ {
		name := o.dayFile()
		path := filepath.Join(o.Dir, name)
		_, statErr := os.Stat(path)
		created := errors.Is(statErr, fs.ErrNotExist)

		f, err := o.open(path)
		if err != nil {
			return Result{}, err
		}
		locked, err := lockUntil(f, deadline)
		if err != nil {
			f.Close()
			return Result{}, err
		}
		if !locked {
			f.Close()
			return o.spill(line)
		}
		// Step 5: is the file we locked still the one at path (compaction replaces it)?
		if !sameAsPath(f, path) {
			unlock(f)
			f.Close()
			continue
		}
		err = o.writeLocked(f, line)
		unlock(f)
		if err == nil && !o.NoFsync {
			err = syncFile(f)
			if err == nil && created {
				err = syncDir(o.Dir)
			}
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return Result{File: name}, err
	}
	return o.spill(line)
}

// open opens the day file for appending, creating the folder on first use.
func (o *Outbox) open(path string) (*os.File, error) {
	// O_RDWR, not O_WRONLY as the first draft of the spec said: step 6 reads the last byte.
	const flags = os.O_RDWR | os.O_APPEND | os.O_CREATE
	f, err := os.OpenFile(path, flags, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(o.Dir, 0o700); err != nil {
			return nil, err
		}
		f, err = os.OpenFile(path, flags, 0o600)
	}
	return f, err
}

// lockUntil retries the non-blocking lock every millisecond until deadline.
func lockUntil(f *os.File, deadline time.Time) (bool, error) {
	for {
		ok, err := tryLock(f)
		if ok || err != nil {
			return ok, err
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		time.Sleep(lockRetry)
	}
}

func sameAsPath(f *os.File, path string) bool {
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(a, b)
}

// writeLocked is steps 6 and 7: repair a torn last line, then one write of the line. The
// repair newline goes into the same write so the two cannot be separated.
func (o *Outbox) writeLocked(f *os.File, line []byte) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if size := st.Size(); size > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], size-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	_, err = f.Write(line) // os.File.Write continues after a short write
	return err
}

// spill writes the record to its own file when the day file is busy (step 4).
func (o *Outbox) spill(line []byte) (Result, error) {
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return Result{}, err
	}
	name := fmt.Sprintf("spill-%d-%s.jsonl", os.Getpid(), record.NewID(o.now()))
	f, err := os.OpenFile(filepath.Join(o.Dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Result{}, err
	}
	_, err = f.Write(line)
	if err == nil && !o.NoFsync {
		if err = syncFile(f); err == nil {
			err = syncDir(o.Dir)
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return Result{File: name, Spilled: true}, err
}

// dayFile is step 2: today's UTC file, or the newest existing one if that is later.
func (o *Outbox) dayFile() string {
	name := o.now().UTC().Format(dayLayout) + ".jsonl"
	days, _, _ := o.list()
	if n := len(days); n > 0 && days[n-1] > name {
		name = days[n-1]
	}
	return name
}

// list returns the day files and the spill files, each sorted by name.
func (o *Outbox) list() (days, spills []string, err error) {
	ents, err := os.ReadDir(o.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	for _, e := range ents {
		switch n := e.Name(); {
		case e.IsDir():
		case dayFileRe.MatchString(n):
			days = append(days, n)
		case spillFileRe.MatchString(n):
			spills = append(spills, n)
		}
	}
	sort.Strings(days)
	sort.Strings(spills)
	return days, spills, nil
}
