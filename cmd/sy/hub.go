package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/mraj602/switchyard/internal/hub/cli"
	"github.com/mraj602/switchyard/internal/hub/store"
)

// runHub routes `sy hub …` to internal/hub/cli.
func runHub(args []string, stdout, stderr io.Writer) error {
	// `sy hub schema --max`: the newest hub schema this binary opens. `sy update` asks the
	// new binary this before replacing a hub host's binary (machines.md 7.3 step 5).
	if len(args) == 2 && args[0] == "schema" && args[1] == "--max" {
		fmt.Fprintln(stdout, store.LatestVersion())
		return nil
	}
	err := cli.Run(args, stdout, stderr)
	var refused *cli.RefusedError
	if errors.As(err, &refused) {
		return exitWith(cli.ExitRefused, err) // an import into a hub that has data (machines.md 8.2)
	}
	// hubPairResult (phone.go) maps usage errors to exit 2 and an expired `pair --wait` to 3.
	return hubPairResult(err)
}
