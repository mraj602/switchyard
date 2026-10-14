package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// obj is a JSON object that keeps its keys in insertion order, because Claude Code rewrites
// settings.json that way and the e2e tests compare bytes. Values are kept raw.
type obj struct {
	keys []string
	vals map[string]json.RawMessage
}

func newObj() *obj { return &obj{vals: map[string]json.RawMessage{}} }

// parseObj reads a JSON object; empty input is an empty object.
func parseObj(b []byte) (*obj, error) {
	o := newObj()
	if len(bytes.TrimSpace(b)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		o.set(k, raw)
	}
	return o, nil
}

// readObj reads a file as an object; a missing file is an empty object.
func readObj(path string) (*obj, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newObj(), nil
	}
	if err != nil {
		return nil, err
	}
	o, err := parseObj(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return o, nil
}

func enc(v any) json.RawMessage {
	if r, ok := v.(json.RawMessage); ok {
		return r
	}
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return json.RawMessage(bytes.TrimSpace(buf.Bytes()))
}

func (o *obj) get(k string) (json.RawMessage, bool) { v, ok := o.vals[k]; return v, ok }

// set replaces k in place, or appends it.
func (o *obj) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = enc(v)
}

func (o *obj) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// sub returns the object at k (empty when absent or not an object).
func (o *obj) sub(k string) *obj {
	if raw, ok := o.vals[k]; ok {
		if s, err := parseObj(raw); err == nil {
			return s
		}
	}
	return newObj()
}

func (o *obj) str(k string) string {
	var s string
	_ = json.Unmarshal(o.vals[k], &s)
	return s
}

func (o *obj) compact() []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(enc(k))
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

// bytes renders with a 2-space indent and a final newline.
func (o *obj) bytes() []byte {
	var out bytes.Buffer
	if err := json.Indent(&out, o.compact(), "", "  "); err != nil {
		return o.compact()
	}
	out.WriteByte('\n')
	return out.Bytes()
}

// resolveForWrite follows a chain of symlinks at path to the file that holds the content,
// which may not exist yet. Claude Code writes through links: the link stays, the target is
// replaced by rename.
func resolveForWrite(path string) string {
	for i := 0; i < 40; i++ {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return path
		}
		l, err := os.Readlink(path)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(l) {
			l = filepath.Join(filepath.Dir(path), l)
		}
		path = l
	}
	return path
}

// writeThrough writes data to path by temp file + rename at the resolved target, mode kept
// (perm for new files). breakLink replaces the link itself with a regular file instead
// (fault injection FAKE_CLAUDE_BREAK_LINK).
func writeThrough(path string, data []byte, perm os.FileMode, breakLink bool) error {
	target := resolveForWrite(path)
	if breakLink {
		target = path
	}
	if fi, err := os.Stat(target); err == nil {
		perm = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chmod(tmp, perm)
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
