package pluginmkt

import (
	"bytes"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// panePlaceholder is the string literal register.tsx holds where the absolute path of wy goes.
const panePlaceholder = `"__SY_PATH__"`

// paneFile returns one file of the pane mod as it is written into the marketplace (path in
// slash form, relative to the plugin folder; plugin.json has had its version set already).
//
// hooks/register.tsx has its "__SY_PATH__" placeholder replaced with syPath as a TypeScript
// string literal. The path becomes source code the mod runs as argv[0], so a path holding a
// double quote, a backslash or a line break is refused (docs/spec/terminal.md T5); on Windows
// the separators are first written as forward slashes, as HookCommand does. Every other file
// is copied unchanged.
func paneFile(p string, data []byte, syPath string) ([]byte, error) {
	if p != "hooks/register.tsx" {
		return data, nil
	}
	lit, err := paneLiteral(syPath)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(data, []byte(panePlaceholder)) {
		return nil, fmt.Errorf("pluginmkt: pane %s has no %s placeholder", p, panePlaceholder)
	}
	return bytes.ReplaceAll(data, []byte(panePlaceholder), []byte(lit)), nil
}

func paneLiteral(syPath string) (string, error) {
	if syPath == "" {
		return "", fmt.Errorf("pluginmkt: pane: empty wy path")
	}
	p := syPath
	if runtime.GOOS == "windows" {
		p = filepath.ToSlash(p)
	}
	if strings.ContainsAny(p, "\"\\\r\n") {
		return "", fmt.Errorf("pluginmkt: pane: the wy path %q holds a quote, a backslash or a line break", syPath)
	}
	return `"` + p + `"`, nil
}
