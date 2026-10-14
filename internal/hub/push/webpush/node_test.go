package webpush_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/switchyard/internal/hub/push/model"
	"github.com/mraj602/switchyard/internal/hub/push/webpush"
)

// Independent check (push.md 11.2): Martin Thomson's http_ece, the reference implementation
// Node's web-push library uses, decrypts bodies made by our encoder, padded and unpadded,
// and a whole payload as the sender builds it.
//
// Opt in with SY_WEBPUSH_NODE=1. Needs node and npm; the package is installed into a temp
// directory (network), or set SY_WEBPUSH_NODE_MODULES to a directory that already holds
// node_modules/http_ece.
const eceScript = `
const ece = require('http_ece');
const crypto = require('crypto');
const fs = require('fs');
const [, , bodyFile, privB64, authB64] = process.argv;
const ecdh = crypto.createECDH('prime256v1');
ecdh.setPrivateKey(Buffer.from(privB64, 'base64url'));
const out = ece.decrypt(fs.readFileSync(bodyFile), {
  version: 'aes128gcm',
  privateKey: ecdh,
  authSecret: Buffer.from(authB64, 'base64url'),
});
process.stdout.write(out.toString('base64'));
`

func TestHTTPECEDecryptsOurBodies(t *testing.T) {
	if os.Getenv("SY_WEBPUSH_NODE") != "1" {
		t.Skip("set SY_WEBPUSH_NODE=1 to run (needs node and npm)")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node not found: ", err)
	}
	dir := os.Getenv("SY_WEBPUSH_NODE_MODULES")
	if dir == "" {
		npm, err := exec.LookPath("npm")
		if err != nil {
			t.Fatal("npm not found: ", err)
		}
		dir = t.TempDir()
		cmd := exec.Command(npm, "install", "--no-audit", "--no-fund", "--silent", "--prefix", dir, "http_ece@1.2.1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("npm install http_ece: %v\n%s", err, out)
		}
	}
	// The script lives beside node_modules so require() finds the package.
	script := filepath.Join(dir, "decrypt.cjs")
	if err := os.WriteFile(script, []byte(eceScript), 0o600); err != nil {
		t.Fatal(err)
	}

	decrypt := func(body []byte, priv *ecdh.PrivateKey, auth []byte) []byte {
		t.Helper()
		bf := filepath.Join(t.TempDir(), "body.bin")
		if err := os.WriteFile(bf, body, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, node, script, bf,
			base64.RawURLEncoding.EncodeToString(priv.Bytes()), base64.RawURLEncoding.EncodeToString(auth))
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, stderr.String())
		}
		plain, err := base64.StdEncoding.DecodeString(string(out))
		if err != nil {
			t.Fatal(err)
		}
		return plain
	}

	for i, pad := range []int{0, webpush.PadMultiple} {
		priv, _ := ecdh.P256().GenerateKey(rand.Reader)
		auth := make([]byte, 16)
		_, _ = rand.Read(auth)
		for _, plain := range []string{"x", rfcPlainNode, strings.Repeat("ünï ", 600)} {
			body, err := webpush.Encrypt(rand.Reader, []byte(plain), priv.PublicKey().Bytes(), auth, pad)
			if err != nil {
				t.Fatal(err)
			}
			if got := decrypt(body, priv, auth); string(got) != plain {
				t.Fatalf("variant %d: http_ece read %q, want %q", i, got, plain)
			}
		}
	}

	// A whole payload as the sender builds it, read back from the request body.
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	p := model.Payload{V: 1, Type: model.TypeTest, Title: "Test", Body: "Switchyard test push", Tag: "sy-test", URL: "/#/", Tap: "auto"}
	plain, _ := json.Marshal(p)
	body, err := webpush.Encrypt(rand.Reader, plain, priv.PublicKey().Bytes(), auth, webpush.PadMultiple)
	if err != nil {
		t.Fatal(err)
	}
	var back model.Payload
	if err := json.Unmarshal(decrypt(body, priv, auth), &back); err != nil || back.Title != "Test" || back.Tag != "sy-test" {
		t.Fatalf("payload: %v %+v", err, back)
	}
}

const rfcPlainNode = "When I grow up, I want to be a watermelon"
