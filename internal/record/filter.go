package record

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// secretKey is the key filter of record.md section 1 rule 4.
var secretKey = regexp.MustCompile(`(?i)(token|secret|password|credential|cookie|api[_-]?key|session[_-]?key)`)

// isCounter reports whether a key that matches the filter is one of the allow-listed token
// counters (record.md 4.3): a key containing "tokens" (plural) holding a number or boolean
// (`input_tokens`, `total_input_tokens`, `exceeds_200k_tokens`, `totalTokens`), or an object
// whose key ends in `tokens_details` (`output_tokens_details`). A singular "token", "id_token"
// and so on is never a counter, and neither is a string value.
func isCounter(key string, val any) bool {
	lk := strings.ToLower(key)
	if !strings.Contains(lk, "tokens") {
		return false
	}
	switch val.(type) {
	case json.Number, bool, nil:
		return true
	case map[string]any:
		return strings.HasSuffix(lk, "tokens_details")
	}
	return false
}

// FilterKeys deletes, at any depth, every object key that matches the secret-key pattern
// except the allow-listed counters, from the JSON value raw. It returns the cleaned JSON and
// the paths removed (dot notation, "[]" for arrays, e.g. `a.b[].token`). When nothing is
// removed it returns raw unchanged. Numbers keep their original text.
func FilterKeys(raw json.RawMessage) (json.RawMessage, []string, error) {
	if !secretKey.Match(raw) {
		return raw, nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil, err
	}
	var paths []string
	v = filterValue(v, "", &paths)
	if len(paths) == 0 {
		return raw, nil, nil
	}
	sort.Strings(paths)
	out, err := encodeNoEscape(v)
	return out, paths, err
}

func filterValue(v any, path string, removed *[]string) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if secretKey.MatchString(k) && !isCounter(k, child) {
				delete(t, k)
				*removed = append(*removed, p)
				continue
			}
			t[k] = filterValue(child, p, removed)
		}
	case []any:
		for i, child := range t {
			t[i] = filterValue(child, path+"[]", removed)
		}
	}
	return v
}

func encodeNoEscape(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// filterObject applies FilterKeys to each value of a top-level object, so paths start at
// the key. skip names top-level keys that are exempt (web_usage's "usage").
func filterObject(obj map[string]json.RawMessage, skip ...string) ([]string, error) {
	var all []string
	for k, raw := range obj {
		if secretKey.MatchString(k) {
			// Evaluate the top-level key itself the same way nested keys are.
			var v any
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			if err := dec.Decode(&v); err == nil && !isCounter(k, v) {
				delete(obj, k)
				all = append(all, k)
				continue
			}
		}
		if contains(skip, k) {
			continue
		}
		cleaned, paths, err := FilterKeys(raw)
		if err != nil {
			return nil, err
		}
		if len(paths) > 0 {
			obj[k] = cleaned
			for _, p := range paths {
				all = append(all, k+"."+p)
			}
		}
	}
	sort.Strings(all)
	return all, nil
}

func contains(s []string, x string) bool {
	for _, e := range s {
		if e == x {
			return true
		}
	}
	return false
}
