//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

// execFile runs path in this console and exits with its code: Windows has no exec, so sy
// waits for the launcher (a .cmd runs through cmd.exe /c) with the console handed over.
func execFile(path string, argv []string) error {
	cmd := exec.Command(path, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitWith(ee.ExitCode(), nil)
	}
	return err
}
