//go:build !fakeopen

package termctl

import "github.com/mraj602/watchyard/internal/termenv"

// fakeOpener is the release build's answer: there is no test seam.
func fakeOpener(termenv.Env) Opener { return nil }
