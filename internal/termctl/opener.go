package termctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/mraj602/switchyard/internal/termenv"
)

// OpenTimeout is how long the OS opener may run.
const OpenTimeout = 5 * time.Second

// ErrNotAllowed is returned by Open for a URL that is not on the allow-list.
var ErrNotAllowed = errors.New("url is not on the focus allow-list")

// Argv is the opener command for goos: xdg-open on Linux (and anything else), open on
// macOS, rundll32.exe url.dll,FileProtocolHandler on Windows. Never a shell.
func Argv(goos, url string) []string {
	switch goos {
	case "darwin":
		return []string{"open", url}
	case "windows":
		return []string{"rundll32.exe", "url.dll,FileProtocolHandler", url}
	}
	return []string{"xdg-open", url}
}

// Runner runs one argv and returns what it wrote to stderr. Tests pass a fake.
type Runner func(ctx context.Context, argv []string) (stderr []byte, err error)

// ExecOpener opens URLs with the OS opener. Open refuses a URL that is not on the allow-list
// (defence in depth: callers check first).
type ExecOpener struct {
	GOOS string
	// Run defaults to running the program with os/exec.
	Run Runner
	// Stderr receives the opener's stderr (sy points it at <state>/logs/sy.log). May be nil.
	Stderr io.Writer
}

// Open implements Opener.
func (o ExecOpener) Open(ctx context.Context, url string) error {
	if !Allowed(url) {
		return ErrNotAllowed
	}
	run := o.Run
	if run == nil {
		run = execRun
	}
	ctx, cancel := context.WithTimeout(ctx, OpenTimeout)
	defer cancel()
	argv := Argv(o.GOOS, url)
	stderr, err := run(ctx, argv)
	if o.Stderr != nil && len(stderr) > 0 {
		_, _ = o.Stderr.Write(stderr)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", argv[0], err)
	}
	return nil
}

func execRun(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	cmd.WaitDelay = time.Second
	prepare(cmd)
	err := cmd.Run()
	return errb.Bytes(), err
}

// NewOpener returns the opener of this machine. Builds with the fakeopen tag return a
// recording opener instead when SY_TEST_OPENER is set (see fakeopen.go); release builds do
// not contain that code.
func NewOpener(env termenv.Env, stderr io.Writer) Opener {
	if f := fakeOpener(env); f != nil {
		return f
	}
	return ExecOpener{GOOS: env.GOOS, Stderr: stderr}
}
