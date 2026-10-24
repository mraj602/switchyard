package devserver

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mraj602/watchyard/webui"
)

// ---- a minimal reader for the schemas in docs/spec/api.yaml ------------------------
//
// api.yaml writes properties in flow style (`key: {type: string}`), which a few regular
// expressions can read without a YAML dependency. It is enough to check that the fixtures
// carry every required field, no field the spec does not know, and the declared scalar types.

type schema struct {
	required []string
	props    map[string]string // property -> declared type ("" when not a simple scalar)
}

var (
	reSchema = regexp.MustCompile(`^    ([A-Za-z]+):\s*$`)
	reReq    = regexp.MustCompile(`^      required: \[([^\]]*)\]`)
	reFlow   = regexp.MustCompile(`^( {8}| {12})([a-z_0-9]+): \{(.*)\}\s*$`)
	reBlock  = regexp.MustCompile(`^( {8}| {12})([a-z_0-9]+):\s*$`)
	reType   = regexp.MustCompile(`type: (\[[^\]]*\]|[a-z]+)`)
	reWord   = regexp.MustCompile(`[a-z]+`)
)

func loadSchemas(t *testing.T) map[string]*schema {
	t.Helper()
	b, err := os.ReadFile("../../docs/spec/api.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*schema{}
	var cur *schema
	name := ""
	in := false
	for _, line := range strings.Split(string(b), "\n") {
		if line == "  schemas:" {
			in = true
			continue
		}
		if !in {
			continue
		}
		if m := reSchema.FindStringSubmatch(line); m != nil {
			cur = &schema{props: map[string]string{}}
			out[m[1]] = cur
			name = m[1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := reReq.FindStringSubmatch(line); m != nil {
			for _, r := range strings.Split(m[1], ",") {
				cur.required = append(cur.required, strings.TrimSpace(r))
			}
		} else if m := reFlow.FindStringSubmatch(line); m != nil && (len(m[1]) == 8 || name == "Alert") {
			// 12-space lines are nested properties, except in Alert (allOf: its own
			// properties sit one level deeper).
			typ := ""
			if tm := reType.FindStringSubmatch(m[3]); tm != nil && !strings.Contains(m[3], "$ref") {
				typ = tm[1]
			}
			cur.props[m[2]] = typ
		} else if m := reBlock.FindStringSubmatch(line); m != nil && (len(m[1]) == 8 || name == "Alert") {
			cur.props[m[2]] = ""
		}
	}
	return out
}

func jsonKind(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if x == float64(int64(x)) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	default:
		return "object"
	}
}

func typeOK(decl, kind string) bool {
	if decl == "" {
		return true
	}
	for _, d := range reWord.FindAllString(decl, -1) {
		if d == kind || (d == "number" && kind == "integer") {
			return true
		}
	}
	return false
}

func check(t *testing.T, schemas map[string]*schema, name, where string, v any) {
	t.Helper()
	obj, ok := v.(map[string]any)
	if !ok {
		t.Errorf("%s: want object for %s", where, name)
		return
	}
	sc := schemas[name]
	if sc == nil {
		t.Fatalf("schema %s not found in api.yaml", name)
	}
	for _, r := range sc.required {
		if _, ok := obj[r]; !ok {
			t.Errorf("%s: missing required %s.%s", where, name, r)
		}
	}
	for k, val := range obj {
		decl, known := sc.props[k]
		if !known {
			t.Errorf("%s: field %s.%s is not in api.yaml", where, name, k)
			continue
		}
		if !typeOK(decl, jsonKind(val)) {
			t.Errorf("%s: %s.%s is %s, spec says %s", where, name, k, jsonKind(val), decl)
		}
	}
}

func roundTrip(t *testing.T, v any) any {
	t.Helper()
	b, _ := json.Marshal(v)
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func each(t *testing.T, v any, f func(item any)) {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("want array, got %T", v)
	}
	for _, x := range arr {
		f(x)
	}
}

var stateEnum = map[string]bool{"working": true, "needs_permission": true, "asked": true, "done": true, "failed": true, "ended": true, "gone": true}

func TestFixturesMatchAPISpec(t *testing.T) {
	schemas := loadSchemas(t)
	d := Fixtures(time.Date(2026, 10, 12, 14, 3, 22, 0, time.UTC))
	ov := roundTrip(t, d.Overview(4100)).(map[string]any)
	check(t, schemas, "Overview", "overview", ov)
	each(t, ov["seats"], func(s any) {
		check(t, schemas, "Seat", "seat", s)
		each(t, s.(map[string]any)["windows"], func(w any) {
			check(t, schemas, "Window", "window", w)
			if p, ok := w.(map[string]any)["pace"].(map[string]any); ok {
				for _, k := range []string{"projected_pct", "band"} {
					if _, ok := p[k]; !ok {
						t.Errorf("pace missing %s", k)
					}
				}
			}
		})
	})
	each(t, ov["machines"], func(m any) { check(t, schemas, "Machine", "machine", m) })
	each(t, ov["needs_you"], func(s any) { check(t, schemas, "Session", "needs_you", s) })

	rx := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)
	each(t, roundTrip(t, d.Sessions), func(s any) {
		check(t, schemas, "Session", "session", s)
		m := s.(map[string]any)
		if !stateEnum[m["state"].(string)] {
			t.Errorf("bad state %v", m["state"])
		}
		if !rx.MatchString(m["state_since"].(string)) {
			t.Errorf("state_since %q is not RFC 3339 with milliseconds", m["state_since"])
		}
		detail, ok := d.SessionDetail(m["id"].(string))
		if !ok {
			t.Fatal("no detail")
		}
		dm := roundTrip(t, detail).(map[string]any)
		tl := dm["timeline"]
		delete(dm, "timeline")
		delete(dm, "tokens")
		check(t, schemas, "Session", "detail", dm)
		each(t, tl, func(e any) {
			em := e.(map[string]any)
			for _, k := range []string{"ts", "event"} {
				if _, ok := em[k]; !ok {
					t.Errorf("timeline event without %s", k)
				}
			}
		})
	})
}

// ---- the mock behaves like the hub where the board depends on it --------------------

func TestMockAuthAndStream(t *testing.T) {
	hub := New(Options{Static: webui.FS(), Dev: true, PingEvery: time.Hour})
	ts := httptest.NewServer(hub.Handler())
	defer ts.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	get := func(p string) *http.Response {
		r, err := c.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := get("/v1/overview"); r.StatusCode != 401 {
		t.Fatalf("unauthenticated overview = %d, want 401", r.StatusCode)
	}
	if r := get("/health"); r.StatusCode != 200 {
		t.Fatalf("/health = %d", r.StatusCode)
	}
	if r := get("/"); r.StatusCode != 200 {
		t.Fatalf("index = %d, static files need no login", r.StatusCode)
	}
	bad, _ := c.Post(ts.URL+"/v1/session/login", "application/json", strings.NewReader(`{"token":"nope"}`))
	if bad.StatusCode != 401 {
		t.Fatalf("bad login = %d", bad.StatusCode)
	}
	ok, _ := c.Post(ts.URL+"/v1/session/login", "application/json", strings.NewReader(`{"token":"`+DefaultToken+`"}`))
	if ok.StatusCode != 204 {
		t.Fatalf("login = %d", ok.StatusCode)
	}
	if r := get("/v1/overview"); r.StatusCode != 200 {
		t.Fatalf("overview after login = %d", r.StatusCode)
	}
	if r := get("/v1/sessions/nope"); r.StatusCode != 404 {
		t.Fatalf("missing session = %d", r.StatusCode)
	}

	res := get("/v1/stream")
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("stream content type %q", ct)
	}
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	wait := func(want string) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case l := <-lines:
				if l == want {
					return
				}
			case <-deadline:
				t.Fatalf("never saw %q", want)
			}
		}
	}
	wait("event: hello")
	resp, _ := c.Post(ts.URL+"/_dev/promote", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("promote = %d", resp.StatusCode)
	}
	wait("event: session")
}
