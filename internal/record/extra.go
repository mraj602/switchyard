package record

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Extra holds the fields of a JSON object that a Go type does not know. Every type in this
// package that has an `Extra` field uses it, so nothing a newer client or Claude Code adds
// is dropped (record.md section 1 rule 1, section 6).
type Extra = map[string]json.RawMessage

var knownKeys sync.Map // reflect.Type -> map[string]bool

// jsonKeys returns the JSON names of the exported, tagged fields of struct type t.
func jsonKeys(t reflect.Type) map[string]bool {
	if v, ok := knownKeys.Load(t); ok {
		return v.(map[string]bool)
	}
	m := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" || !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		m[name] = true
	}
	knownKeys.Store(t, m)
	return m
}

// marshalWithExtra encodes v (a struct value of an alias type, so the type's own
// MarshalJSON is not involved) and appends the extra keys, sorted, after the known ones.
func marshalWithExtra(v any, extra Extra) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return b, nil
	}
	known := jsonKeys(reflect.TypeOf(v))
	keys := make([]string, 0, len(extra))
	for k := range extra {
		if !known[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.Write(b[:len(b)-1]) // drop the closing brace
	empty := len(b) == 2
	for _, k := range keys {
		if !empty {
			buf.WriteByte(',')
		}
		empty = false
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(extra[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// unmarshalWithExtra decodes data into v (pointer to an alias struct) and stores the keys
// v does not declare in *extra (left nil when there are none).
func unmarshalWithExtra(data []byte, v any, extra *Extra) error {
	if err := json.Unmarshal(data, v); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	known := jsonKeys(reflect.TypeOf(v).Elem())
	var rest Extra
	for k, raw := range all {
		if !known[k] {
			if rest == nil {
				rest = Extra{}
			}
			rest[k] = raw
		}
	}
	*extra = rest
	return nil
}
