package hubsrc

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/mraj602/watchyard/internal/atomicfile"
	"github.com/mraj602/watchyard/internal/board"
)

// LastViewFile is <state>/board/last-view.json.
func LastViewFile(stateDir string) string { return filepath.Join(stateDir, "board", "last-view.json") }

// SaveLastView writes the last hub View atomically, mode 0600.
func SaveLastView(stateDir string, v board.View) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p := LastViewFile(stateDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(p, append(b, '\n'), 0o600)
}

// LoadLastView reads last-view.json. It returns nil when there is none, when it does not
// parse, or when it was made for another hub URL (a View from a different hub is not shown).
func LoadLastView(stateDir, hubURL string) *board.View {
	b, err := os.ReadFile(LastViewFile(stateDir))
	if err != nil {
		return nil
	}
	v, err := board.Decode(b)
	if err != nil || !board.SameHub(v.Hub.URL, hubURL) {
		return nil
	}
	return &v
}
