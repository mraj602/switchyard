package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/mraj602/switchyard/internal/hub/cli"
)

// runPhone implements `sy phone pair [--url U] [--name N] [--ttl D] [--json] [--qr]`: the
// same as `sy hub pair --kind phone --wait` (pairing.md 2.4). It runs on the hub machine.
// Registered in the command table by main.go.
func runPhone(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] != "pair" {
		fmt.Fprintln(stderr, "usage: sy phone pair [--url <https address>] [--name N] [--ttl 10m] [--json] [--qr]")
		if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
			return nil
		}
		return errUsage
	}
	return hubPairResult(cli.PhonePair(args[1:], stdout, stderr))
}

// hubPairResult maps the pairing commands' errors to exit codes: bad flags 2, a code that
// expired while waiting 3 (pairing.md 2.4).
func hubPairResult(err error) error {
	switch {
	case errors.Is(err, cli.ErrUsage):
		return errUsage
	case errors.Is(err, cli.ErrExpired):
		return exitWith(3, nil)
	case errors.Is(err, cli.ErrNotImplemented):
		return exitWith(exitNotImplemented, err) // an older sy without the command
	}
	return err
}
