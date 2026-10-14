package claudecli

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var addWord = regexp.MustCompile(`(?m)(^|\s)add(\s|$)`)

// FeatureError lists what the installed claude lacks (setup.md 3 step 1.1).
type FeatureError struct{ Missing []string }

func (e *FeatureError) Error() string {
	return "claude lacks: " + strings.Join(e.Missing, "; ")
}

// CheckFeatures verifies by behaviour, not version number, that `claude plugin marketplace
// --help` lists `add` and that `claude auth status --json` parses (as folder f). A version
// older than 2.1.296 is not an error: callers warn using Version. The returned error is a
// *FeatureError when a feature is missing, or the underlying error when claude cannot run.
func (e *Exec) CheckFeatures(ctx context.Context, f Folder) error {
	var missing []string
	o, err := e.run(ctx, &f, "plugin", "marketplace", "--help")
	if err != nil {
		return err
	}
	help := append(append([]byte{}, o.stdout...), o.stderr...)
	if o.exit != 0 || !addWord.Match(help) {
		missing = append(missing, "`claude plugin marketplace add`")
	}
	if _, err := e.AuthStatus(ctx, f); err != nil {
		if _, ok := err.(*Error); !ok {
			return err
		}
		missing = append(missing, fmt.Sprintf("`claude auth status --json` (%v)", err))
	}
	if len(missing) > 0 {
		return &FeatureError{Missing: missing}
	}
	return nil
}
