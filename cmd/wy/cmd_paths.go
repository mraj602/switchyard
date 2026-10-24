package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/mraj602/watchyard/internal/paths"
)

// runPaths implements `wy paths`: each folder and where it came from.
func runPaths(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("paths", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return usageErr(err)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "wy paths: unexpected argument %q\n", fs.Arg(0))
		return errUsage
	}
	d, err := paths.Resolve(paths.FromOS())
	if err != nil {
		return err
	}
	for _, row := range []struct {
		name string
		dir  paths.Dir
	}{{"config", d.Config}, {"state", d.State}, {"data", d.Data}} {
		fmt.Fprintf(stdout, "%-7s %s  (%s)\n", row.name, row.dir.Path, row.dir.Describe())
	}
	return nil
}
