package cli

import (
	"os"
	"testing"
)

// TestMain points the state folder at a temp dir for every test, so no test reads or makes
// the real pair_pepper or hub_sign.key. Tests that need their own call t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sy-hub-cli-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("SY_STATE_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
