// Package expotest is a fake Expo push service for tests (push.md 12, phases/8.md WP-H). It
// is a TLS httptest server for POST /--/api/v2/push/send and /--/api/v2/push/getReceipts.
// It checks every request against push.md 12.2 (and records what was wrong), records the
// messages, issues ticket ids, and plays scripted answers: ok, every ticket
// details.error, top-level errors, 401, 429 with Retry-After, 500 and slow answers; and
// receipts ok, DeviceNotRegistered or missing. It shares no code with the expo package.
//
// A real sender with its default dial guard cannot reach it (loopback), so tests build the
// sender with the fake's Client and URL as base URL. It answers any Host, so the fakeclock
// build's SY_TEST_EXPO_DIAL can send https://exp.host traffic to it.
package expotest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Paths of Expo's push API.
const (
	SendPath     = "/--/api/v2/push/send"
	ReceiptsPath = "/--/api/v2/push/getReceipts"
)

// MaxMessageBytes is Expo's per-message limit.
const MaxMessageBytes = 4096

var (
	tokenRE    = regexp.MustCompile(`^Expo(nent)?PushToken\[[A-Za-z0-9_-]{8,200}\]$`)
	isoRE      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	channels   = map[string]bool{"sy_needs_you": true, "sy_limits": true, "sy_machines": true, "sy_test": true}
	payloadKey = []string{"v", "type", "alert", "alerts", "trigger", "title", "body", "tag", "url", "remote_url", "tap",
		"session", "needs_you", "actions", "private", "ts"}
)

// Message is one received Expo message, decoded loosely.
type Message struct {
	To               string         `json:"to"`
	Title            *string        `json:"title"`
	Body             *string        `json:"body"`
	Data             map[string]any `json:"data"`
	CategoryID       *string        `json:"categoryId"`
	ChannelID        *string        `json:"channelId"`
	Sound            *string        `json:"sound"`
	Badge            *int           `json:"badge"`
	ContentAvailable *bool          `json:"contentAvailable"`
	Priority         string         `json:"priority"`
	TTL              *int64         `json:"ttl"`
}

// Received is one send request.
type Received struct {
	Header http.Header
	Raw    []byte  // the request body
	Msg    Message // the first (only) message
	Ticket string  // the ticket id issued ("" when the answer was not ok)
	// Err lists what made the request invalid (push.md 12.2); nil when it was valid.
	Err error
	At  time.Time
}

// Reply is one scripted answer to a send.
type Reply struct {
	// Status is the HTTP status; 0 means 200 with a ticket.
	Status int
	// TicketError makes the 200 ticket {status: error, details.error: TicketError}.
	TicketError string
	// Errors is a top-level errors array of these codes (with Status, default 200).
	Errors []string
	// RetryAfter is the Retry-After header.
	RetryAfter string
	// Body replaces the whole answer.
	Body string
	// Delay is waited before answering (timeouts).
	Delay time.Duration
	// Echo puts the message's title and body into the answer's message fields (as Expo
	// does with the token), so tests can prove the sender never keeps answer text.
	Echo bool
}

// Receipt states of SetReceipt.
const (
	ReceiptOK                  = "ok"
	ReceiptDeviceNotRegistered = "DeviceNotRegistered"
	ReceiptMissing             = "missing"
)

// ReceiptsRequest is one getReceipts call.
type ReceiptsRequest struct {
	Header http.Header
	IDs    []string
	Err    error
}

// Server is the fake.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	token    string // expected bearer token; "" requires no Authorization header
	script   []Reply
	got      []Received
	receipts map[string]string // ticket -> state; issued tickets default to ok
	rscript  []Reply           // scripted answers to getReceipts (Status, Errors, Body)
	asked    []ReceiptsRequest
	next     int
	signal   chan struct{}
	now      func() time.Time
}

// New starts the fake. Close it when done.
func New() *Server {
	s := &Server{receipts: map[string]string{}, signal: make(chan struct{}, 1024), now: time.Now}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.handle))
	return s
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// URL is the base URL (https://127.0.0.1:port).
func (s *Server) URL() string { return s.srv.URL }

// Addr is host:port, for SY_TEST_EXPO_DIAL.
func (s *Server) Addr() string { return strings.TrimPrefix(s.srv.URL, "https://") }

// Client trusts the server's certificate and follows no redirect.
func (s *Server) Client() *http.Client {
	c := s.srv.Client()
	c.Timeout = 10 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

// ExpectToken makes the fake require `Authorization: Bearer tok` (Expo's enhanced
// security); "" (the default) requires no Authorization header. A mismatch answers 401.
func (s *Server) ExpectToken(tok string) {
	s.mu.Lock()
	s.token = tok
	s.mu.Unlock()
}

// Script queues send answers, one per request in order; an empty queue answers ok.
func (s *Server) Script(r ...Reply) {
	s.mu.Lock()
	s.script = append(s.script, r...)
	s.mu.Unlock()
}

// ScriptReceipts queues answers to getReceipts (Status, Errors, Body, Delay are used); an
// empty queue answers from the receipt states.
func (s *Server) ScriptReceipts(r ...Reply) {
	s.mu.Lock()
	s.rscript = append(s.rscript, r...)
	s.mu.Unlock()
}

// SetReceipt sets a ticket's receipt: ReceiptOK, ReceiptMissing, or an error code such as
// ReceiptDeviceNotRegistered.
func (s *Server) SetReceipt(ticket, state string) {
	s.mu.Lock()
	s.receipts[ticket] = state
	s.mu.Unlock()
}

// SetAllReceipts sets the receipt of every ticket issued so far.
func (s *Server) SetAllReceipts(state string) {
	s.mu.Lock()
	for k := range s.receipts {
		s.receipts[k] = state
	}
	s.mu.Unlock()
}

// Requests returns a copy of every send request so far.
func (s *Server) Requests() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.got...)
}

// ReceiptRequests returns a copy of every getReceipts request so far.
func (s *Server) ReceiptRequests() []ReceiptsRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ReceiptsRequest(nil), s.asked...)
}

// Wait blocks until n send requests have arrived in total, or the timeout passes.
func (s *Server) Wait(n int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		s.mu.Lock()
		have := len(s.got)
		s.mu.Unlock()
		if have >= n {
			return true
		}
		select {
		case <-s.signal:
		case <-deadline:
			return false
		}
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case SendPath:
		s.send(w, r)
	case ReceiptsPath:
		s.getReceipts(w, r)
	default:
		writeErrors(w, http.StatusNotFound, "", "NOT_FOUND")
	}
}

// common checks the parts every request shares.
func (s *Server) common(r *http.Request) []string {
	var bad []string
	if r.Method != http.MethodPost {
		bad = append(bad, "method "+r.Method)
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		bad = append(bad, fmt.Sprintf("Content-Type %q", ct))
	}
	if a := r.Header.Get("Accept"); !strings.Contains(a, "application/json") {
		bad = append(bad, fmt.Sprintf("Accept %q", a))
	}
	return bad
}

func (s *Server) authorized(r *http.Request) bool {
	s.mu.Lock()
	want := s.token
	s.mu.Unlock()
	got := r.Header.Get("Authorization")
	if want == "" {
		return got == ""
	}
	return got == "Bearer "+want
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	rec := Received{Header: r.Header.Clone(), Raw: body, At: s.now()}
	bad := s.common(r)
	bad = append(bad, checkSend(body, &rec.Msg)...)
	if len(bad) > 0 {
		rec.Err = errors.New(strings.Join(bad, "; "))
	}
	s.mu.Lock()
	var reply Reply
	if len(s.script) > 0 {
		reply, s.script = s.script[0], s.script[1:]
	}
	s.mu.Unlock()

	authorized := s.authorized(r)
	var answer []byte
	status := reply.Status
	switch {
	case reply.Body != "":
		answer = []byte(reply.Body)
	case !authorized && status == 0:
		status = http.StatusUnauthorized
		answer = errorsJSON("UNAUTHORIZED")
	case rec.Err != nil && status == 0:
		status = http.StatusBadRequest
		answer = errorsJSON("VALIDATION_ERROR")
	case len(reply.Errors) > 0:
		answer = errorsJSON(reply.Errors...)
	case status != 0 && status != http.StatusOK:
		answer = errorsJSON(codeFor(status))
	case reply.TicketError != "":
		t := map[string]any{"status": "error", "message": fmt.Sprintf("%q is not a registered push notification recipient", rec.Msg.To),
			"details": map[string]any{"error": reply.TicketError}}
		if reply.Echo {
			t["message"] = echo(rec.Msg)
		}
		answer, _ = json.Marshal(map[string]any{"data": t})
	default:
		s.mu.Lock()
		s.next++
		rec.Ticket = fmt.Sprintf("0199e3a0-0000-7000-8000-%012d", s.next)
		s.receipts[rec.Ticket] = ReceiptOK
		s.mu.Unlock()
		t := map[string]any{"status": "ok", "id": rec.Ticket}
		if reply.Echo {
			t["message"] = echo(rec.Msg)
		}
		answer, _ = json.Marshal(map[string]any{"data": t})
	}
	if status == 0 {
		status = http.StatusOK
	}
	s.mu.Lock()
	s.got = append(s.got, rec)
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
	if reply.Delay > 0 {
		select {
		case <-time.After(reply.Delay):
		case <-r.Context().Done():
			return
		}
	}
	if reply.RetryAfter != "" {
		w.Header().Set("Retry-After", reply.RetryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(answer)
}

func echo(m Message) string {
	var parts []string
	if m.Title != nil {
		parts = append(parts, *m.Title)
	}
	if m.Body != nil {
		parts = append(parts, *m.Body)
	}
	return strings.Join(parts, " / ")
}

func (s *Server) getReceipts(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	req := ReceiptsRequest{Header: r.Header.Clone()}
	bad := s.common(r)
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		bad = append(bad, "body is not {ids: [...]}")
	} else if len(in.IDs) == 0 || len(in.IDs) > 1000 {
		bad = append(bad, fmt.Sprintf("%d ids (want 1-1000)", len(in.IDs)))
	}
	req.IDs = in.IDs
	if len(bad) > 0 {
		req.Err = errors.New(strings.Join(bad, "; "))
	}
	s.mu.Lock()
	s.asked = append(s.asked, req)
	var reply Reply
	if len(s.rscript) > 0 {
		reply, s.rscript = s.rscript[0], s.rscript[1:]
	}
	data := map[string]any{}
	for _, id := range in.IDs {
		st, ok := s.receipts[id]
		switch {
		case !ok || st == ReceiptMissing:
		case st == ReceiptOK:
			data[id] = map[string]any{"status": "ok"}
		default:
			data[id] = map[string]any{"status": "error", "message": "receipt error", "details": map[string]any{"error": st}}
		}
	}
	s.mu.Unlock()
	if reply.Delay > 0 {
		select {
		case <-time.After(reply.Delay):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case reply.Body != "":
		w.WriteHeader(max(reply.Status, http.StatusOK))
		_, _ = io.WriteString(w, reply.Body)
	case !s.authorized(r):
		writeErrors(w, http.StatusUnauthorized, "", "UNAUTHORIZED")
	case req.Err != nil:
		writeErrors(w, http.StatusBadRequest, "", "VALIDATION_ERROR")
	case len(reply.Errors) > 0 || (reply.Status != 0 && reply.Status != http.StatusOK):
		codes := reply.Errors
		if len(codes) == 0 {
			codes = []string{codeFor(reply.Status)}
		}
		writeErrors(w, max(reply.Status, http.StatusOK), reply.RetryAfter, codes...)
	default:
		b, _ := json.Marshal(map[string]any{"data": data})
		_, _ = w.Write(b)
	}
}

func codeFor(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "UNAUTHORIZED"
	case http.StatusTooManyRequests:
		return "TOO_MANY_REQUESTS"
	case http.StatusRequestEntityTooLarge:
		return "PUSH_TOO_MANY_NOTIFICATIONS"
	case http.StatusBadRequest:
		return "VALIDATION_ERROR"
	}
	return "INTERNAL_SERVER_ERROR"
}

func errorsJSON(codes ...string) []byte {
	var es []map[string]any
	for _, c := range codes {
		es = append(es, map[string]any{"code": c, "message": "scripted " + c})
	}
	b, _ := json.Marshal(map[string]any{"errors": es})
	return b
}

func writeErrors(w http.ResponseWriter, status int, retryAfter string, codes ...string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(errorsJSON(codes...))
}

// checkSend validates a send body against push.md 12.2 and decodes the message into m.
func checkSend(body []byte, m *Message) []string {
	var bad []string
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var list []json.RawMessage
		if err := json.Unmarshal(trimmed, &list); err != nil || len(list) != 1 {
			return []string{"want exactly one message"}
		}
		trimmed = list[0]
	}
	if len(trimmed) > MaxMessageBytes {
		bad = append(bad, fmt.Sprintf("message is %d bytes (limit %d)", len(trimmed), MaxMessageBytes))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return append(bad, "body is not a JSON object")
	}
	if err := json.Unmarshal(trimmed, m); err != nil {
		return append(bad, "message fields: "+err.Error())
	}
	known := map[string]bool{"to": true, "title": true, "body": true, "data": true, "categoryId": true, "channelId": true,
		"sound": true, "badge": true, "contentAvailable": true, "priority": true, "ttl": true}
	for k := range raw {
		if !known[k] {
			bad = append(bad, "unknown field "+k)
		}
	}
	if !tokenRE.MatchString(m.To) {
		bad = append(bad, "to is not an Expo push token")
	}
	if m.TTL == nil || *m.TTL <= 0 {
		bad = append(bad, "ttl missing")
	}
	if m.Data == nil {
		return append(bad, "data missing")
	}
	if m.Data["v"] != float64(1) {
		bad = append(bad, "data.v is not 1")
	}
	if m.Data["type"] == "refresh" {
		return append(bad, checkRefresh(raw, m)...)
	}
	return append(bad, checkShown(raw, m)...)
}

func checkRefresh(raw map[string]json.RawMessage, m *Message) []string {
	var bad []string
	for _, k := range []string{"title", "body", "sound", "categoryId", "badge", "channelId"} {
		if _, ok := raw[k]; ok {
			bad = append(bad, k+" in a refresh")
		}
	}
	if m.ContentAvailable == nil || !*m.ContentAvailable {
		bad = append(bad, "refresh without contentAvailable")
	}
	if m.Priority != "normal" {
		bad = append(bad, "refresh priority is not normal")
	}
	if m.TTL == nil || *m.TTL != 900 {
		bad = append(bad, "refresh ttl is not 900")
	}
	keys := sortedKeys(m.Data)
	if strings.Join(keys, ",") != "needs_you,ts,type,v" {
		bad = append(bad, "refresh data keys "+strings.Join(keys, ","))
	}
	if _, ok := m.Data["needs_you"].(float64); !ok {
		bad = append(bad, "refresh data.needs_you is not a number")
	}
	if ts, _ := m.Data["ts"].(string); !isoRE.MatchString(ts) {
		bad = append(bad, "refresh data.ts is not a time")
	}
	return bad
}

func checkShown(raw map[string]json.RawMessage, m *Message) []string {
	var bad []string
	d := m.Data
	typ, _ := d["type"].(string)
	if typ != "alert" && typ != "summary" && typ != "test" {
		bad = append(bad, fmt.Sprintf("data.type %q", typ))
	}
	for _, k := range sortedKeys(d) {
		found := false
		for _, p := range payloadKey {
			found = found || p == k
		}
		if !found {
			bad = append(bad, "unknown data key "+k)
		}
	}
	for _, k := range []string{"title", "body", "tag", "url", "tap", "needs_you", "private", "ts"} {
		if _, ok := d[k]; !ok {
			bad = append(bad, "data."+k+" missing")
		}
	}
	if m.Title == nil || *m.Title == "" {
		bad = append(bad, "title missing")
	}
	if m.Title != nil && d["title"] != nil && *m.Title != d["title"] && d["title"] != "" {
		bad = append(bad, "title differs from data.title")
	}
	if m.Body != nil && *m.Body != d["body"] {
		bad = append(bad, "body differs from data.body")
	}
	if u, _ := d["url"].(string); !strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//") || strings.Contains(u, `\`) {
		bad = append(bad, "data.url is not a hub-relative path")
	}
	if d["private"] == true {
		if _, ok := d["remote_url"]; ok {
			bad = append(bad, "remote_url in a private push")
		}
	}
	actions, hasActions := d["actions"].(map[string]any)
	hasActions = hasActions && (actions["snooze"] != nil || actions["seen"] != nil)
	switch {
	case hasActions && (m.CategoryID == nil || *m.CategoryID != "sy_alert"):
		bad = append(bad, "categoryId is not sy_alert with actions")
	case !hasActions && m.CategoryID != nil:
		bad = append(bad, "categoryId without actions")
	}
	if m.ChannelID == nil || !channels[*m.ChannelID] {
		bad = append(bad, "channelId missing or unknown")
	} else if want := channelFor(d); *m.ChannelID != want {
		bad = append(bad, fmt.Sprintf("channelId %s, want %s", *m.ChannelID, want))
	}
	if m.Sound == nil || *m.Sound != "default" {
		bad = append(bad, "sound is not default")
	}
	if m.Priority != "high" && m.Priority != "normal" {
		bad = append(bad, fmt.Sprintf("priority %q", m.Priority))
	}
	if m.Badge != nil {
		if n, _ := d["needs_you"].(float64); int(n) != *m.Badge {
			bad = append(bad, "badge is not data.needs_you")
		}
	} else if typ != "test" {
		bad = append(bad, "badge missing")
	}
	if _, ok := raw["contentAvailable"]; ok {
		bad = append(bad, "contentAvailable on a shown push")
	}
	return bad
}

// channelFor is phone.md P6.1, written out independently of the expo package.
func channelFor(d map[string]any) string {
	switch d["type"] {
	case "test":
		return "sy_test"
	case "summary":
		return "sy_needs_you"
	}
	switch d["trigger"] {
	case "limit_80", "limit_95", "reset":
		return "sy_limits"
	case "machine_silent":
		return "sy_machines"
	}
	return "sy_needs_you"
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
