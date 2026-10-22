package desktop

import (
	"path/filepath"
	"strings"

	"github.com/mraj602/switchyard/internal/machineadd"
)

// BatchMode returns p with `-o BatchMode=yes` after the ssh program, so a command-prefix
// admin channel (`ssh hub-host sy`) fails at once instead of waiting on a password or host key
// prompt that the desktop app's user cannot see (desktop.md C4). A prefix that is not ssh,
// or already sets BatchMode, is returned unchanged.
func BatchMode(p machineadd.PrefixAdmin) machineadd.PrefixAdmin {
	if len(p.Words) == 0 {
		return p
	}
	if b := filepath.Base(p.Words[0]); b != "ssh" && b != "ssh.exe" {
		return p
	}
	for i, w := range p.Words {
		lw := strings.ToLower(w)
		if strings.HasPrefix(lw, "batchmode=") || (lw == "-obatchmode=yes") || (i > 0 && lw == "-o" && i+1 < len(p.Words) && strings.HasPrefix(strings.ToLower(p.Words[i+1]), "batchmode=")) {
			return p
		}
	}
	words := make([]string, 0, len(p.Words)+2)
	words = append(words, p.Words[0], "-o", "BatchMode=yes")
	words = append(words, p.Words[1:]...)
	p.Words = words
	return p
}
