package main

import (
	"fmt"
	"io"
	"os"

	"github.com/mraj602/switchyard/internal/hook"
	"github.com/mraj602/switchyard/internal/statusline"
)

const hookUsage = `usage: sy hook [<Event>]            record one Claude Code hook event (JSON on stdin)
       sy hook --title <Event>      print the tab title hook JSON, or nothing (terminal.titles)

Run by Claude Code's hooks; it always exits 0 and never changes what Claude Code does.
`

// runHook is `sy hook [event]` and `sy hook --title <Event>`. It never returns an error and
// writes to stdout only the title hook's JSON (or the usage for --help): a hook must not
// change what Claude Code does. --help, -h and help print usage and touch no state.
func runHook(args []string, stdout, _ io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Fprint(stdout, hookUsage)
			return nil
		case hook.TitleFlag:
			hook.RunTitle(args[1:], os.Stdin, stdout, os.Getenv)
			return nil
		}
	}
	hook.Run(args, os.Stdin, os.Getenv)
	return nil
}

// runStatusline is `sy statusline [--wrap cmd]`.
func runStatusline(args []string, stdout, stderr io.Writer) error {
	return statusline.Run(args, os.Stdin, stdout, stderr, os.Getenv)
}
