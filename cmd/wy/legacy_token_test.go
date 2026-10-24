package main

import (
	"strings"
	"testing"
)

// Legacy compat: tokens made before the rename start sy_ and are still tokens to connect and
// to the manifest's redaction.
func TestTokenShapeAndRedactAcceptLegacyPrefix(t *testing.T) {
	body := strings.Repeat("a", 43)
	for _, tok := range []string{"wy_m_" + body, "sy_m_" + body, "sy_d_" + body} {
		if !tokenShape.MatchString(tok) {
			t.Errorf("tokenShape refuses %q", tok[:5])
		}
		if got := redactArgs([]string{"connect", "http://h", tok}); got[2] != "<redacted>" {
			t.Errorf("redactArgs kept %q", tok[:5])
		}
	}
	if tokenShape.MatchString("xy_m_" + body) {
		t.Error("tokenShape accepts another prefix")
	}
}
