// Package warp knows Warp's files that sy may touch (docs/spec/terminal.md T8): the per-OS
// `tab_configs` folder of each channel, whether Warp is installed, the six chromatic colour
// names a tab config may use (the nearest one to a seat colour by OKLab distance), the
// `switchyard-<label>.toml` tab config sy renders (with its marker line, so sy can tell its
// own files from the person's), and the `warp://tab_config/…` URI that opens one.
//
// Rules for this package:
//   - Folders and environment come from an injected termenv.Env; nothing here reads HOME or
//     calls os.UserHomeDir (internal/termenv/lint_test.go).
//   - Nothing here writes: rendering returns bytes, and the files are written by
//     internal/setup/apply from a plan (plan.Terminal), journaled and undoable.
//   - sy never writes Warp's settings, launch configurations, themes or keybindings
//     (docs/phases/7.md hard rule 2); only `switchyard-*.toml` files in `tab_configs/`.
//
// Phase 7, WP-E. Depends on: termenv.
package warp

import (
	"os"
	"strings"

	"github.com/mraj602/switchyard/internal/termenv"
)

// Channels (model.WarpStable / model.WarpPreview).
const (
	Stable  = "stable"
	Preview = "preview"
)

// TermProgram is the value of TERM_PROGRAM inside Warp.
const TermProgram = "WarpTerminal"

// Scheme is the URI scheme of a channel: "warp" or "warppreview".
func Scheme(channel string) string {
	if channel == Preview {
		return "warppreview"
	}
	return "warp"
}

// ValidChannel reports whether c is "", Stable or Preview ("" means stable).
func ValidChannel(c string) bool { return c == "" || c == Stable || c == Preview }

// DataFolder is Warp's data folder of a channel for env's OS (the parent of tab_configs):
//
//	macOS    ~/.warp                               ~/.warp-preview
//	Linux    ${XDG_DATA_HOME:-~/.local/share}/warp-terminal   …/warp-terminal-preview
//	Windows  %APPDATA%\warp\Warp\data              %APPDATA%\warp\WarpPreview\data
//
// It is "" when the folder it is built from is unknown (no home, no APPDATA).
func DataFolder(env termenv.Env, channel string) string {
	preview := channel == Preview
	switch env.GOOS {
	case "darwin":
		if env.Home == "" {
			return ""
		}
		if preview {
			return env.Join(env.Home, ".warp-preview")
		}
		return env.Join(env.Home, ".warp")
	case "windows":
		if env.AppData == "" {
			return ""
		}
		if preview {
			return env.Join(env.AppData, "warp", "WarpPreview", "data")
		}
		return env.Join(env.AppData, "warp", "Warp", "data")
	}
	if env.Home == "" && env.XDGDataHome == "" {
		return ""
	}
	if preview {
		return env.Join(env.DataHome(), "warp-terminal-preview")
	}
	return env.Join(env.DataHome(), "warp-terminal")
}

// Folder is the tab_configs folder of a channel ("" when DataFolder is "").
func Folder(env termenv.Env, channel string) string {
	d := DataFolder(env, channel)
	if d == "" {
		return ""
	}
	return env.Join(d, "tab_configs")
}

// Install is what detection found for one channel.
type Install struct {
	Channel string `json:"channel"`
	Data    string `json:"data"`   // Warp's data folder
	Folder  string `json:"folder"` // its tab_configs folder
	// Present: the data folder exists. InWarp: TERM_PROGRAM says this process runs in Warp.
	Present bool `json:"present"`
	InWarp  bool `json:"in_warp,omitempty"`
	// FolderExists: tab_configs exists already (else a plan creates it).
	FolderExists bool `json:"folder_exists,omitempty"`
	// Both: the other channel's data folder exists too.
	Both bool `json:"both,omitempty"`
}

// Detected reports whether Warp counts as installed: its data folder exists, or
// TERM_PROGRAM=WarpTerminal.
func (i Install) Detected() bool { return i.Present || i.InWarp }

// Detect finds Warp for channel ("" = stable, unless only Preview is installed).
func Detect(env termenv.Env, channel string) Install {
	stable, preview := dirExists(DataFolder(env, Stable)), dirExists(DataFolder(env, Preview))
	if channel == "" {
		channel = Stable
		if !stable && preview {
			channel = Preview
		}
	}
	in := Install{Channel: channel, Data: DataFolder(env, channel), Folder: Folder(env, channel),
		InWarp: env.Lookup("TERM_PROGRAM") == TermProgram, Both: stable && preview}
	in.Present = (channel == Stable && stable) || (channel == Preview && preview)
	in.FolderExists = dirExists(in.Folder)
	return in
}

// ChannelOf tells the channel of a tab_configs (or data) folder from its path, by the name
// of Warp's data folder only (DataFolder): .warp-preview (macOS), warp-terminal-preview
// (Linux) or WarpPreview\data (Windows) is Preview; anything else is Stable. "preview"
// elsewhere in the path (a home folder, XDG_DATA_HOME) does not count.
func ChannelOf(folder string) string {
	parts := strings.FieldsFunc(folder, func(r rune) bool { return r == '/' || r == '\\' })
	if n := len(parts); n > 0 && parts[n-1] == "tab_configs" {
		parts = parts[:n-1]
	}
	n := len(parts)
	if n == 0 {
		return Stable
	}
	switch parts[n-1] {
	case ".warp-preview", "warp-terminal-preview":
		return Preview
	case "data":
		if n >= 3 && parts[n-2] == "WarpPreview" && parts[n-3] == "warp" {
			return Preview
		}
	}
	return Stable
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
