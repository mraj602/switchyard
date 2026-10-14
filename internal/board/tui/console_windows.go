//go:build windows

package tui

import (
	"os"

	"golang.org/x/sys/windows"
)

// EnableVT puts the console behind out into VT mode (ENABLE_VIRTUAL_TERMINAL_PROCESSING, so
// the escape codes of a frame are drawn) and the one behind in into VT input
// (ENABLE_VIRTUAL_TERMINAL_INPUT, so arrows arrive as escape sequences). restore puts both
// back to the modes they had. A handle that is not a console is left alone: restore then
// does nothing for it and err is nil.
func EnableVT(in, out *os.File) (restore func(), err error) {
	type saved struct {
		h    windows.Handle
		mode uint32
	}
	var undo []saved
	set := func(f *os.File, add uint32) error {
		if f == nil {
			return nil
		}
		h := windows.Handle(f.Fd())
		var old uint32
		if windows.GetConsoleMode(h, &old) != nil {
			return nil // not a console
		}
		if old&add == add {
			return nil
		}
		if err := windows.SetConsoleMode(h, old|add); err != nil {
			return err
		}
		undo = append(undo, saved{h, old})
		return nil
	}
	restore = func() {
		for i := len(undo) - 1; i >= 0; i-- {
			_ = windows.SetConsoleMode(undo[i].h, undo[i].mode)
		}
		undo = nil
	}
	if err := set(out, windows.ENABLE_PROCESSED_OUTPUT|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		restore()
		return func() {}, err
	}
	if err := set(in, windows.ENABLE_VIRTUAL_TERMINAL_INPUT); err != nil {
		restore()
		return func() {}, err
	}
	return restore, nil
}
