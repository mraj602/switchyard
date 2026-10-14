package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:static
var static embed.FS

// FS returns the board's files rooted at static/, so index.html is at the top. The hub
// serves it without authentication; the pages call the API with the session cookie.
func FS() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // the embed directive guarantees the directory exists
	}
	return sub
}
