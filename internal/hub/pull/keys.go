// Package pull is the hub pulling servers over SSH (machines.md 3.4, 3.5): the hub's key
// pair, the generated ssh_config and known_hosts (host keys pinned at registration, never
// learned), the hub side of the drain protocol over any pipe pair (Serve), and the scheduler
// that runs one `ssh` per target. Records go through ingest.Ingester in-process, never
// through the API. The remote end is internal/client.Drain.
package pull

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner runs a helper program (ssh-keygen, ssh-keyscan) and returns its stdout. Tests
// replace it; ExecRunner is the real one.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs name with os/exec. stderr is folded into the error.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), fmt.Errorf("%s: %s", name, firstLine(msg))
	}
	return out.Bytes(), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// KeyFile and KeyPubFile are the hub's pull key pair inside the pull folder.
const (
	KeyFile    = "id_ed25519"
	KeyPubFile = "id_ed25519.pub"
)

// EnsureKey makes the hub's pull key pair in dir (0700) if missing, with
// `ssh-keygen -t ed25519 -N ” -C 'watchyard-pull@<hubName>'`, and returns the public key
// line. An existing key is never replaced. The key is used for nothing else.
func EnsureKey(ctx context.Context, dir, hubName string, run Runner) (string, error) {
	if run == nil {
		run = ExecRunner
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	priv := filepath.Join(dir, KeyFile)
	if _, err := os.Stat(priv); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		_ = os.Remove(priv + ".pub")
		if _, err := run(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "watchyard-pull@"+hubName, "-f", priv); err != nil {
			return "", fmt.Errorf("could not make the pull key (the ssh client is required on the hub host): %w", err)
		}
	}
	if err := os.Chmod(priv, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if pub, err := PublicKey(dir); err == nil {
		return pub, nil
	}
	out, err := run(ctx, "ssh-keygen", "-y", "-f", priv)
	if err != nil {
		return "", err
	}
	pub := strings.TrimSpace(string(out)) + " watchyard-pull@" + hubName
	if err := os.WriteFile(priv+".pub", []byte(pub+"\n"), 0o644); err != nil {
		return "", err
	}
	return pub, nil
}

// PublicKey reads the hub's pull public key line; os.ErrNotExist when there is none.
func PublicKey(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, KeyPubFile))
	if err != nil {
		return "", err
	}
	s := firstLine(string(b))
	if !strings.HasPrefix(s, "ssh-") && !strings.HasPrefix(s, "ecdsa-") {
		return "", errors.New("pull public key is unreadable")
	}
	return s, nil
}

var hostKeyTypes = map[string]bool{
	"ssh-ed25519": true, "ssh-rsa": true, "ecdsa-sha2-nistp256": true, "ecdsa-sha2-nistp384": true,
	"ecdsa-sha2-nistp521": true, "sk-ssh-ed25519@openssh.com": true, "sk-ecdsa-sha2-nistp256@openssh.com": true,
}

// ParseHostKeys reads public host keys, one `<type> <base64> [comment]` per line (the form of
// /etc/ssh/ssh_host_*_key.pub), or ssh-keyscan lines with a leading host. Blank lines and
// `#` comments are skipped; every key must decode and carry its own type. Duplicates collapse.
// The result is `<type> <base64>` strings.
func ParseHostKeys(r io.Reader) ([]string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var out []string
	seen := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) > 0 && !hostKeyTypes[f[0]] && len(f) >= 3 {
			f = f[1:] // ssh-keyscan: host type key
		}
		if len(f) < 2 || !hostKeyTypes[f[0]] {
			return nil, fmt.Errorf("not a public host key: %q", truncate(line, 40))
		}
		if _, err := Fingerprint(f[0] + " " + f[1]); err != nil {
			return nil, err
		}
		k := f[0] + " " + f[1]
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no host keys given")
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// Fingerprint is the OpenSSH `SHA256:<base64>` fingerprint of a `<type> <base64>` key.
func Fingerprint(key string) (string, error) {
	f := strings.Fields(key)
	if len(f) < 2 {
		return "", errors.New("not a public key")
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(blob) < 8 {
		return "", errors.New("public key is not valid base64")
	}
	// The blob starts with a length-prefixed type string that must equal the stated type.
	n := int(blob[0])<<24 | int(blob[1])<<16 | int(blob[2])<<8 | int(blob[3])
	if n < 1 || 4+n > len(blob) || string(blob[4:4+n]) != f[0] {
		return "", fmt.Errorf("public key does not match its type %s", f[0])
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// Scan runs ssh-keyscan against host:port from the hub host and returns the keys it found
// with their fingerprints (so a terminal can show them).
func Scan(ctx context.Context, run Runner, host string, port int) (keys []string, err error) {
	if run == nil {
		run = ExecRunner
	}
	out, err := run(ctx, "ssh-keyscan", "-T", "10", "-p", fmt.Sprint(port), "--", host)
	if err != nil && len(out) == 0 {
		return nil, err
	}
	keys, perr := ParseHostKeys(bytes.NewReader(out))
	if perr != nil {
		return nil, fmt.Errorf("ssh-keyscan found no host key on %s:%d", host, port)
	}
	return keys, nil
}

// PinByFingerprint keeps only the scanned keys whose fingerprint equals fp. An error when
// none matches.
func PinByFingerprint(keys []string, fp string) ([]string, error) {
	fp = strings.TrimSpace(fp)
	var out []string
	for _, k := range keys {
		if got, err := Fingerprint(k); err == nil && got == fp {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no host key on the server has fingerprint %s", fp)
	}
	return out, nil
}
