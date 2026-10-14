package claudecli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// Regression: on Windows variable names are case-insensitive, so `claude_config_dir=` in
// the parent environment must be removed too, or the child may see two values.
func TestEnvironWindowsCaseInsensitive(t *testing.T) {
	base := []string{"A=1", "claude_config_dir=/old", "Claude_Config_Dir=/old2", "CLAUDE_CONFIG_DIR_X=keep", "=C:=C:\\"}
	f := Folder{Dir: `C:\h\.claude-2`}
	got := f.environ("windows", base)
	want := []string{"A=1", "CLAUDE_CONFIG_DIR_X=keep", "=C:=C:\\", `CLAUDE_CONFIG_DIR=C:\h\.claude-2`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("windows: %q", got)
	}
	// Elsewhere names are case-sensitive: a lower-case variable is a different one.
	got = f.environ("linux", base)
	want = []string{"A=1", "claude_config_dir=/old", "Claude_Config_Dir=/old2", "CLAUDE_CONFIG_DIR_X=keep", "=C:=C:\\", `CLAUDE_CONFIG_DIR=C:\h\.claude-2`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("linux: %q", got)
	}
	if got := (Folder{Unset: true}).environ("windows", base); !reflect.DeepEqual(got, []string{"A=1", "CLAUDE_CONFIG_DIR_X=keep", "=C:=C:\\"}) {
		t.Errorf("unset: %q", got)
	}
	if base[1] != "claude_config_dir=/old" {
		t.Error("base modified")
	}
}

// Regression: `claude auth status` must report the folder's login, so login credentials in
// the environment are removed for auth commands (and only for them).
func TestAuthEnvironStripsEnvLogins(t *testing.T) {
	base := []string{"A=1", "ANTHROPIC_API_KEY=sk", "CLAUDE_CODE_OAUTH_TOKEN=tok", "ANTHROPIC_AUTH_TOKEN=t2", "anthropic_api_key=lc", "ANTHROPIC_BASE_URL=keep"}
	f := Folder{Dir: "/h/.claude-2"}
	if got, want := f.authEnviron("linux", base), []string{"A=1", "anthropic_api_key=lc", "ANTHROPIC_BASE_URL=keep", "CLAUDE_CONFIG_DIR=/h/.claude-2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("linux: %q", got)
	}
	if got, want := f.authEnviron("windows", base), []string{"A=1", "ANTHROPIC_BASE_URL=keep", "CLAUDE_CONFIG_DIR=/h/.claude-2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("windows: %q", got)
	}
	if got := f.environ("linux", base); len(got) != len(base)+1 {
		t.Errorf("non-auth environ dropped variables: %q", got)
	}
}

// The real runner passes the stripped environment to `claude auth status`.
func TestExecAuthStatusWithoutEnvLogin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '{\"loggedIn\":true,\"email\":\"%s|%s\"}\\n' \"$ANTHROPIC_API_KEY$CLAUDE_CODE_OAUTH_TOKEN\" \"$CLAUDE_CONFIG_DIR\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	x := &Exec{Path: bin, Environ: []string{"PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=sk", "CLAUDE_CODE_OAUTH_TOKEN=tok", "CLAUDE_CONFIG_DIR=/wrong"}}
	st, err := x.AuthStatus(context.Background(), Folder{Dir: "/h/.claude-2"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Email != "|/h/.claude-2" {
		t.Errorf("auth status saw %q", st.Email)
	}
}
