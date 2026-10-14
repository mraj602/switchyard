//go:build fakeopen

package termctl

import (
	"context"
	"encoding/json"
	"os"

	"github.com/mraj602/switchyard/internal/termenv"
)

// Test seam (docs/spec/terminal.md T7): with `-tags fakeopen` and SY_TEST_OPENER=<file>,
// Open appends the argv it would have run, as a JSON array on one line, to that file and
// runs nothing. Release builds do not contain this file.

type recordOpener struct {
	file, goos string
}

func fakeOpener(env termenv.Env) Opener {
	f := env.Lookup("SY_TEST_OPENER")
	if f == "" {
		return nil
	}
	return recordOpener{file: f, goos: env.GOOS}
}

func (r recordOpener) Open(_ context.Context, url string) error {
	if !Allowed(url) {
		return ErrNotAllowed
	}
	line, _ := json.Marshal(Argv(r.goos, url))
	f, err := os.OpenFile(r.file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}
