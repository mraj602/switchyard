package machineadd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mraj602/switchyard/internal/atomicfile"
)

// authorized_keys for pull mode (machines.md 3.4, remote side):
//
//	restrict,command="/home/me/.local/bin/sy outbox drain --stdio" ssh-ed25519 AAAA… switchyard-pull@hub-host sy-pull:pt_01JA…
//
// The trailing `sy-pull:<target id>` finds the line again. A line with the same key (or the
// same marker) is replaced; any other line is never touched, byte for byte.

// LegacyRestrict replaces `restrict` on OpenSSH before 7.2.
const LegacyRestrict = "no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty,no-user-rc"

// PullMarker is the comment that marks a target's line.
func PullMarker(target string) string { return "sy-pull:" + target }

var (
	targetRe = regexp.MustCompile(`^pt_[0-9A-Za-z]{1,64}$`)
	cidrRe   = regexp.MustCompile(`^[0-9A-Fa-f:.,/*?]+$`)
	keyType  = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)$`)
	b64Re    = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
	// syPathRe is what may stand in the forced command: sshd runs command= through the
	// user's login shell with -c, so a shell metacharacter in the path would run something
	// else.
	syPathRe = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
)

// PullKeyLine builds the authorized_keys line. pub is the hub's public key line ("<type>
// <base64> [comment]"), sy the absolute path of the installed sy, from a CIDR list or "".
func PullKeyLine(pub, sy, target, from string, restrict bool) (line string, key string, err error) {
	f := strings.Fields(pub)
	if len(f) < 2 || !keyType.MatchString(f[0]) || !b64Re.MatchString(f[1]) {
		return "", "", errors.New("--key-stdin: not an SSH public key line")
	}
	if !targetRe.MatchString(target) {
		return "", "", fmt.Errorf("--target: %q is not a pull target id (pt_...)", target)
	}
	if !filepath.IsAbs(sy) || !syPathRe.MatchString(sy) {
		return "", "", fmt.Errorf("the sy path %q cannot go in a forced command", sy)
	}
	if from != "" && !cidrRe.MatchString(from) {
		return "", "", fmt.Errorf("--from: %q is not a list of addresses or CIDRs", from)
	}
	opts := "restrict"
	if !restrict {
		opts = LegacyRestrict
	}
	if from != "" {
		opts += `,from="` + from + `"`
	}
	opts += `,command="` + sy + ` outbox drain --stdio"`
	comment := ""
	if len(f) >= 3 {
		c := strings.Join(f[2:], " ")
		if !strings.HasPrefix(c, "sy-pull:") {
			comment = " " + strings.Fields(c)[0]
		}
	}
	key = f[0] + " " + f[1]
	return opts + " " + key + comment + " " + PullMarker(target), key, nil
}

// lineMatches: the line holds key (anywhere as type+blob) or ends with the marker.
func lineMatches(line, key, marker string) bool {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return false
	}
	if marker != "" && (strings.HasSuffix(t, " "+marker) || t == marker) {
		return true
	}
	return key != "" && strings.Contains(" "+t+" ", " "+key+" ")
}

// AuthorizePull adds or replaces the hub's line in <home>/.ssh/authorized_keys. Running it
// twice leaves one line. The file keeps its mode (0600 when created) and ~/.ssh is 0700
// when created. A copy of the previous file is kept as authorized_keys.sy-bak.
func AuthorizePull(home, line, key, target string) (changed bool, err error) {
	return editAuthorizedKeys(home, func(lines []string) []string {
		var out []string
		placed := false
		for _, l := range lines {
			if lineMatches(l, key, PullMarker(target)) {
				if !placed {
					out = append(out, line)
					placed = true
				}
				continue
			}
			out = append(out, l)
		}
		if !placed {
			out = append(out, line)
		}
		return out
	})
}

// RemovePull removes the target's line (machines.md 4.5 `authorize-pull --remove`).
func RemovePull(home, target string) (bool, error) {
	return editAuthorizedKeys(home, func(lines []string) []string {
		var out []string
		for _, l := range lines {
			if !lineMatches(l, "", PullMarker(target)) {
				out = append(out, l)
			}
		}
		return out
	})
}

func editAuthorizedKeys(home string, edit func([]string) []string) (bool, error) {
	dir := filepath.Join(home, ".ssh")
	p := filepath.Join(dir, "authorized_keys")
	cur, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	exists := err == nil
	// Split keeping every line's bytes; a last line without a newline stays without one.
	var lines []string
	trailing := len(cur) == 0 || cur[len(cur)-1] == '\n'
	if len(cur) > 0 {
		lines = strings.Split(strings.TrimSuffix(string(cur), "\n"), "\n")
	}
	out := edit(lines)
	var b bytes.Buffer
	for i, l := range out {
		b.WriteString(l)
		if i < len(out)-1 || trailing || i >= len(lines) {
			b.WriteByte('\n')
		}
	}
	if bytes.Equal(b.Bytes(), cur) {
		return false, nil
	}
	if !exists && len(out) == 0 {
		return false, nil
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return false, err
		}
	}
	mode := os.FileMode(0o600)
	if exists {
		if fi, err := os.Stat(p); err == nil {
			mode = fi.Mode().Perm()
		}
		if err := atomicfile.Write(p+".sy-bak", cur, 0o600); err != nil {
			return false, err
		}
	}
	if err := atomicfile.Write(p, b.Bytes(), mode); err != nil {
		return false, err
	}
	return true, nil
}
