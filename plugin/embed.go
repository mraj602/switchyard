// Package plugin embeds the Claude Code plugins in plugin/ so `wy` can write them into its
// own local marketplace (setup.md 5.2): watchyard (hooks.json, plugin.json and the skills)
// and, optionally, watchyard-pane (the opt-in pane mod, docs/spec/terminal.md T5).
package plugin

import (
	"embed"
	"io/fs"

	"github.com/mraj602/watchyard/internal/brand"
)

// Name is the plugin's name and its folder name under plugin/ (the skills are /<Name>:setup
// and so on).
const Name = brand.Slug

// PaneName is the pane mod's plugin name and folder name under plugin/. It is a separate
// plugin so the early-access mod API can never break the recording hooks (T5, decision 4).
const PaneName = brand.Slug + "-pane"

// The "all:" prefix is required: without it go:embed skips ".claude-plugin" (a dot folder),
// and the plugin would have no plugin.json.
//
//go:embed all:watchyard
var content embed.FS

//go:embed all:watchyard-pane
var paneContent embed.FS

// FS returns the plugin tree rooted at the plugin folder: ".claude-plugin/plugin.json",
// "hooks/hooks.json", "skills/<name>/SKILL.md", "README.md". The tree is read-only and
// identical to the repository folder at build time, so hook commands still call `wy` by name;
// internal/setup/pluginmkt rewrites them to an absolute path when it writes the copy.
func FS() fs.FS { return sub(content, Name) }

// PaneFS returns the pane mod's tree rooted at its folder: ".claude-plugin/plugin.json",
// "hooks/hooks.json" (modules only), "hooks/register.tsx", "types/index.d.ts" and its tests.
// register.tsx holds the placeholder "__SY_PATH__", which internal/setup/pluginmkt replaces
// with the absolute path of wy when it writes the copy.
func PaneFS() fs.FS { return sub(paneContent, PaneName) }

func sub(f embed.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err) // the embedded folder is fixed at build time
	}
	return s
}
