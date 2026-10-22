// Package importproto imports a usage.db from the earlier Electron prototype into the hub
// (`sy hub import-prototype`). It opens a *copy* of the prototype
// database read-only, turns its rows into ordinary records (hook_event, web_usage,
// token_rows, slot_info) and feeds them through the hub's own ingest, so the typed tables
// and session states are built exactly as for live records. A few facts that no record
// kind carries (seat labels and colours, session start/transcript/model, org plans) are
// written directly afterwards.
//
// Record ids are deterministic (ULID time = the row's time, randomness = SHA-256 of the
// row's identity), and every record comes from the device dev_import, so running the
// importer twice adds nothing.
//
// Depends on: record, identity, hub/store, hub/ingest, hub/usage.
package importproto

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mraj602/switchyard/internal/hub/ingest"
	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/hub/usage"
	"github.com/mraj602/switchyard/internal/record"
)

// DeviceID is the device every imported record belongs to.
const DeviceID = "dev_import"

// Options for Import.
type Options struct {
	Machine     string // machine id; "" = MachineFile, else the dev_import device's, else new
	MachineFile string // <state>/machine.json
	MachineName string // name of a new machine row; "" = hostname
	Now         func() time.Time
}

// Row is one line of the report: a prototype table, its count, and the hub's count.
type Row struct {
	Table  string `json:"table"`
	Source int64  `json:"source"`
	Hub    string `json:"hub_table"`
	Count  int64  `json:"hub"`
	Note   string `json:"note,omitempty"`
}

// Report is what an import did.
type Report struct {
	Machine    string   `json:"machine"`
	Records    int      `json:"records_sent"`
	Accepted   int      `json:"accepted"`
	Duplicates int      `json:"duplicates"`
	Rejected   int      `json:"rejected"`
	TypeErrors int64    `json:"type_errors"`
	Rows       []Row    `json:"counts"`
	Notes      []string `json:"notes,omitempty"`
}

// Print writes the report as a table.
func (r *Report) Print(w io.Writer) {
	fmt.Fprintf(w, "machine %s: %d records sent, %d new, %d already imported, %d rejected, %d not typed\n\n",
		r.Machine, r.Records, r.Accepted-r.Duplicates, r.Duplicates, r.Rejected, r.TypeErrors)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROTOTYPE\tROWS\tHUB\tROWS\tNOTE")
	for _, x := range r.Rows {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t%s\n", x.Table, x.Source, x.Hub, x.Count, x.Note)
	}
	tw.Flush()
	for _, n := range r.Notes {
		fmt.Fprintln(w, "note:", n)
	}
}

type login struct{ account, org, email string }

type importer struct {
	src     *sql.DB
	st      *store.Store
	machine string
	opt     Options
	tables  map[string]bool

	dirs     map[string]login // config dir -> login (absent = logged out)
	dirOrder []string
	accounts map[string]protoAccount // prototype account_id
	owner    map[string]login        // session_owner
	sessDirs map[string][]string     // session -> config dirs
	sessions map[string]protoSession
	t0       int64
	notes    []string
}

type protoAccount struct {
	label, color, email, plan string
	ord                       int64
}

type protoSession struct {
	accountID, cwd, transcript, model, remote string
	started                                   int64
}

// Import imports the prototype database at src into st.
func Import(ctx context.Context, st *store.Store, src string, opt Options) (*Report, error) {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if _, err := os.Stat(src); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", store.FileURI(src, "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	im := &importer{src: db, st: st, opt: opt, tables: map[string]bool{}}
	if err := im.load(ctx); err != nil {
		return nil, fmt.Errorf("read prototype database: %w", err)
	}
	if err := im.resolveMachine(ctx); err != nil {
		return nil, err
	}
	recs, err := im.records(ctx)
	if err != nil {
		return nil, err
	}
	rep := &Report{Machine: im.machine, Records: len(recs)}
	g := &ingest.Ingester{Store: st, Now: opt.Now}
	dev, err := st.DeviceByID(ctx, DeviceID)
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(recs); i += ingest.MaxBatchRecords {
		end := min(i+ingest.MaxBatchRecords, len(recs))
		body, err := json.Marshal(map[string]any{"v": 1, "client": "sy-import-prototype/1", "records": recs[i:end]})
		if err != nil {
			return nil, err
		}
		reply, err := g.Ingest(ctx, dev, body, nil)
		if err != nil {
			return nil, fmt.Errorf("ingest batch %d: %w", i/ingest.MaxBatchRecords, err)
		}
		rep.Accepted += len(reply.Accepted)
		rep.Duplicates += len(reply.Duplicates)
		rep.Rejected += len(reply.Rejected)
		for _, r := range reply.Rejected {
			if len(im.notes) < 20 {
				im.notes = append(im.notes, fmt.Sprintf("rejected %s: %s %s", r.ID, r.Reason, r.Detail))
			}
		}
	}
	if err := im.direct(ctx); err != nil {
		return nil, err
	}
	if err := im.report(ctx, rep); err != nil {
		return nil, err
	}
	rep.Notes = append(rep.Notes, im.notes...)
	return rep, nil
}

func (im *importer) has(t string) bool { return im.tables[t] }

func (im *importer) load(ctx context.Context) error {
	rows, err := im.src.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		im.tables[n] = true
	}
	rows.Close()
	im.dirs, im.accounts, im.owner = map[string]login{}, map[string]protoAccount{}, map[string]login{}
	im.sessDirs, im.sessions = map[string][]string{}, map[string]protoSession{}
	each := func(table, q string, fn func(*sql.Rows) error) error {
		if !im.has(table) {
			im.notes = append(im.notes, "prototype has no "+table+" table")
			return nil
		}
		rows, err := im.src.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := fn(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	err = each("config_dirs", `SELECT dir, coalesce(email, ''), coalesce(account_uuid, ''), coalesce(org_uuid, '') FROM config_dirs ORDER BY dir`, func(r *sql.Rows) error {
		var dir string
		var l login
		if err := r.Scan(&dir, &l.email, &l.account, &l.org); err != nil {
			return err
		}
		im.dirOrder = append(im.dirOrder, dir)
		if l.account != "" && l.org != "" {
			im.dirs[dir] = l
		}
		return nil
	})
	if err != nil {
		return err
	}
	err = each("accounts", `SELECT id, coalesce(label, ''), coalesce(color, ''), coalesce(email, ''), coalesce(ord, 0), coalesce(plan, '') FROM accounts`, func(r *sql.Rows) error {
		var id string
		var a protoAccount
		if err := r.Scan(&id, &a.label, &a.color, &a.email, &a.ord, &a.plan); err != nil {
			return err
		}
		im.accounts[id] = a
		return nil
	})
	if err != nil {
		return err
	}
	err = each("session_owner", `SELECT session_id, coalesce(account_uuid, ''), coalesce(org_uuid, '') FROM session_owner`, func(r *sql.Rows) error {
		var id string
		var l login
		if err := r.Scan(&id, &l.account, &l.org); err != nil {
			return err
		}
		if l.account != "" && l.org != "" {
			im.owner[id] = l
		}
		return nil
	})
	if err != nil {
		return err
	}
	err = each("session_dirs", `SELECT session_id, config_dir FROM session_dirs ORDER BY session_id, config_dir`, func(r *sql.Rows) error {
		var id, dir string
		if err := r.Scan(&id, &dir); err != nil {
			return err
		}
		im.sessDirs[id] = append(im.sessDirs[id], dir)
		return nil
	})
	if err != nil {
		return err
	}
	err = each("sessions", `SELECT session_id, coalesce(account_id, ''), coalesce(started_at, 0), coalesce(cwd, ''), coalesce(transcript_path, ''),
		coalesce(model, ''), coalesce(remote_url, '') FROM sessions`, func(r *sql.Rows) error {
		var id string
		var s protoSession
		if err := r.Scan(&id, &s.accountID, &s.started, &s.cwd, &s.transcript, &s.model, &s.remote); err != nil {
			return err
		}
		im.sessions[id] = s
		return nil
	})
	if err != nil {
		return err
	}
	var t0 sql.NullInt64
	parts := []string{}
	for _, t := range []string{"events", "tokens", "snapshots", "windows", "limit_hits"} {
		if im.has(t) {
			parts = append(parts, "SELECT min(ts) AS ts FROM "+t)
		}
	}
	if len(parts) > 0 {
		if err := im.src.QueryRowContext(ctx, `SELECT min(ts) FROM (`+strings.Join(parts, " UNION ALL ")+`)`).Scan(&t0); err != nil {
			return err
		}
	}
	im.t0 = t0.Int64
	if im.t0 == 0 {
		im.t0 = im.opt.Now().UnixMilli()
	}
	return nil
}

var machineIDPattern = regexp.MustCompile(`^m_[0-9A-HJKMNP-TV-Z]{26}$`)

func (im *importer) resolveMachine(ctx context.Context) error {
	name := im.opt.MachineName
	if name == "" {
		h, _ := os.Hostname()
		name = strings.ToLower(strings.SplitN(h, ".", 2)[0])
	}
	if name == "" {
		name = "imported"
	}
	dev, err := im.st.DeviceByID(ctx, DeviceID)
	switch {
	case err == nil:
		if im.opt.Machine != "" && im.opt.Machine != dev.MachineID {
			return fmt.Errorf("an earlier import used machine %s; pass --machine %s (or leave it out)", dev.MachineID, dev.MachineID)
		}
		im.machine = dev.MachineID
		return nil
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	im.machine = im.opt.Machine
	if im.machine == "" && im.opt.MachineFile != "" {
		if b, err := os.ReadFile(im.opt.MachineFile); err == nil {
			var m struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if json.Unmarshal(b, &m) == nil && machineIDPattern.MatchString(m.ID) {
				im.machine = m.ID
				if im.opt.MachineName == "" && m.Name != "" {
					name = m.Name
				}
			}
		}
	}
	if im.machine == "" {
		im.machine = "m_" + record.NewID(im.opt.Now())
	}
	if !machineIDPattern.MatchString(im.machine) {
		return fmt.Errorf("bad machine id %q", im.machine)
	}
	now := im.opt.Now().UnixMilli()
	tok := sha256.Sum256([]byte("dev_import:" + im.machine + ":" + record.NewID(im.opt.Now())))
	return im.st.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.EnsureMachine(im.machine, name, im.t0); err != nil {
			return err
		}
		// Never usable as a token: the hash is of a random string and the device is revoked.
		return tx.InsertDevice(store.Device{ID: DeviceID, Kind: "machine", Name: "prototype import", MachineID: im.machine,
			TokenHint: "import", Scopes: []string{"write"}, CreatedAt: now, CreatedBy: "import", RevokedAt: now}, tok[:])
	})
}

// detID is a deterministic ULID: the time of the row and 80 bits of SHA-256(key).
func detID(ts int64, key string) string {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	if ts < 0 {
		ts = 0
	}
	var out [26]byte
	ms := uint64(ts)
	for i := 9; i >= 0; i-- {
		out[i] = crockford[ms&31]
		ms >>= 5
	}
	h := sha256.Sum256([]byte(key))
	var acc uint32
	var bits uint
	j := 10
	for _, b := range h[:10] {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[j] = crockford[(acc>>bits)&31]
			j++
		}
	}
	return string(out[:])
}

var homeDir = regexp.MustCompile(`^(/home/[^/]+|/Users/[^/]+|/root)/(\.claude[^/]*)$`)

// slotOf writes a config dir with ~ like sy does (identity.md 3).
func slotOf(dir string) string {
	if m := homeDir.FindStringSubmatch(dir); m != nil {
		return "~/" + m[2]
	}
	return dir
}

var transcriptDir = regexp.MustCompile(`^(.*/\.claude[^/]*)/projects/`)

func seatRef(l login, src string) *record.Seat {
	return &record.Seat{Account: l.account, Org: l.org, Email: l.email, Src: src}
}

// loginOfAccount maps a prototype account id to a login through its email.
func (im *importer) loginOfAccount(id string) (login, bool) {
	a, ok := im.accounts[id]
	if !ok || a.email == "" {
		return login{}, false
	}
	for _, dir := range im.dirOrder {
		if l, ok := im.dirs[dir]; ok && strings.EqualFold(l.email, a.email) {
			return l, true
		}
	}
	return login{}, false
}

// sessionLogin is the best evidence of a session's seat: its Remote Control owner, the
// login of its only config folder, or its prototype account.
func (im *importer) sessionLogin(id string) (login, bool) {
	if l, ok := im.owner[id]; ok {
		return l, true
	}
	if dirs := im.sessDirs[id]; len(dirs) == 1 {
		if l, ok := im.dirs[dirs[0]]; ok {
			return l, true
		}
	}
	if s, ok := im.sessions[id]; ok {
		return im.loginOfAccount(s.accountID)
	}
	return login{}, false
}

func (im *importer) newRecord(kind, id string, ts int64) *record.Record {
	return &record.Record{V: record.Version, ID: id, Kind: kind, TS: record.FormatTime(time.UnixMilli(ts)),
		Machine: im.machine, Client: "sy-import-prototype/1"}
}

func (im *importer) records(ctx context.Context) ([]*record.Record, error) {
	var out []*record.Record
	// Slots and their logins, as of the oldest row.
	for _, dir := range im.dirOrder {
		r := im.newRecord(record.KindSlotInfo, detID(im.t0, "slot_info|"+dir), im.t0)
		r.Slot = slotOf(dir)
		p := record.SlotInfoPayload{Folder: r.Slot, Default: r.Slot == "~/.claude"}
		if l, ok := im.dirs[dir]; ok {
			r.Seat = seatRef(l, "claude_json")
			p.LoggedIn = true
			p.OAuthAccount, _ = json.Marshal(map[string]string{"accountUuid": l.account, "emailAddress": l.email, "organizationUuid": l.org})
		}
		if err := r.SetPayload(p); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := im.snapshotRecords(ctx, &out); err != nil {
		return nil, err
	}
	if err := im.eventRecords(ctx, &out); err != nil {
		return nil, err
	}
	if err := im.tokenRecords(ctx, &out); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TS != out[j].TS {
			return out[i].TS < out[j].TS
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (im *importer) snapshotRecords(ctx context.Context, out *[]*record.Record) error {
	if !im.has("snapshots") {
		return nil
	}
	rows, err := im.src.QueryContext(ctx, `SELECT ts, account_id, coalesce(email, ''), coalesce(org_uuid, ''), coalesce(org_name, ''), raw FROM snapshots ORDER BY ts, account_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	bad := 0
	for rows.Next() {
		var ts int64
		var acct, email, org, orgName, raw string
		if err := rows.Scan(&ts, &acct, &email, &org, &orgName, &raw); err != nil {
			return err
		}
		if !json.Valid([]byte(raw)) || org == "" {
			bad++
			continue
		}
		l := login{org: org, email: email}
		for _, dir := range im.dirOrder { // same org and email, else same org
			if d, ok := im.dirs[dir]; ok && d.org == org && (l.account == "" || strings.EqualFold(d.email, email)) {
				l.account = d.account
			}
		}
		if l.account == "" {
			if d, ok := im.loginOfAccount(acct); ok && d.org == org {
				l.account = d.account
			}
		}
		if l.account == "" {
			l.account = "prototype-" + acct // no uuid known: one seat per prototype account
		}
		r := im.newRecord(record.KindWebUsage, detID(ts, "snapshots|"+acct+"|"+fmt.Sprint(ts)), ts)
		r.Seat = seatRef(l, "claude_ai")
		orgJSON, _ := json.Marshal(map[string]string{"uuid": org, "name": orgName})
		var account json.RawMessage
		if email != "" {
			account, _ = json.Marshal(map[string]string{"email_address": email})
		}
		if err := r.SetPayload(record.WebUsagePayload{TabID: acct, Org: orgJSON, Account: account, Usage: json.RawMessage(raw)}); err != nil {
			return err
		}
		*out = append(*out, r)
	}
	if bad > 0 {
		im.notes = append(im.notes, fmt.Sprintf("%d snapshots skipped (no org uuid or raw not JSON)", bad))
	}
	return rows.Err()
}

func (im *importer) eventRecords(ctx context.Context, out *[]*record.Record) error {
	if !im.has("events") {
		return nil
	}
	rows, err := im.src.QueryContext(ctx, `SELECT ts, coalesce(account_id, ''), coalesce(kind, ''), coalesce(cwd, ''), coalesce(session_id, ''),
		coalesce(raw, ''), coalesce(url, '') FROM events ORDER BY ts, rowid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts int64
		var acct, kind, cwd, sid, raw, url string
		if err := rows.Scan(&ts, &acct, &kind, &cwd, &sid, &raw, &url); err != nil {
			return err
		}
		var obj map[string]any
		if raw == "" || json.Unmarshal([]byte(raw), &obj) != nil {
			name := map[string]string{"finished": "Stop", "idle": "Notification", "permission": "Notification"}[kind]
			if name == "" {
				name = kind
			}
			obj = map[string]any{"hook_event_name": name, "session_id": sid, "cwd": cwd}
			if kind == "idle" {
				obj["notification_type"] = "idle_prompt"
			} else if kind == "permission" {
				obj["notification_type"] = "permission_prompt"
			}
		}
		if sid == "" {
			sid, _ = obj["session_id"].(string)
		}
		var dir string
		if tp, _ := obj["transcript_path"].(string); tp != "" {
			if m := transcriptDir.FindStringSubmatch(tp); m != nil {
				dir = m[1]
			}
		}
		if dir == "" && len(im.sessDirs[sid]) == 1 {
			dir = im.sessDirs[sid][0]
		}
		var seat *record.Seat
		if l, ok := im.owner[sid]; ok {
			seat = seatRef(l, "claude_json")
		} else if l, ok := im.dirs[dir]; ok {
			seat = seatRef(l, "claude_json")
		} else if l, ok := im.loginOfAccount(acct); ok {
			seat = seatRef(l, "claude_json")
		}
		stdin, _ := json.Marshal(obj)
		meta := record.Meta{Machine: im.machine, Seat: seat, Client: "sy-import-prototype/1", Now: time.UnixMilli(ts)}
		if dir != "" {
			meta.Slot = slotOf(dir)
		}
		r, err := record.NewHookEvent(meta, stdin, record.HookOptions{LastMessages: true})
		if err != nil {
			im.notes = append(im.notes, fmt.Sprintf("event at %d skipped: %v", ts, err))
			continue
		}
		r.ID = detID(ts, "events|"+fmt.Sprint(ts)+"|"+sid+"|"+kind)
		if url != "" {
			if r.Derived == nil {
				r.Derived = record.Derived{}
			}
			r.Derived.Set("remote_url", url)
		}
		*out = append(*out, r)
	}
	return rows.Err()
}

func (im *importer) tokenRecords(ctx context.Context, out *[]*record.Record) error {
	type facts struct {
		rows []record.TokenRow
		hits []json.RawMessage
	}
	bySession := map[string]*facts{}
	get := func(id string) *facts {
		f := bySession[id]
		if f == nil {
			f = &facts{}
			bySession[id] = f
		}
		return f
	}
	if im.has("tokens") {
		rows, err := im.src.QueryContext(ctx, `SELECT key, ts, coalesce(session_id, ''), coalesce(project, ''), coalesce(model, ''), coalesce(input, 0),
			coalesce(output, 0), coalesce(cache_5m, 0), coalesce(cache_1h, 0), coalesce(cache_read, 0), coalesce(thinking, 0), coalesce(sidechain, 0)
			FROM tokens ORDER BY session_id, ts, key`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r record.TokenRow
			var ts, thinking, side int64
			if err := rows.Scan(&r.Key, &ts, &r.SessionID, &r.Cwd, &r.Model, &r.Input, &r.Output, &r.Cache5m, &r.Cache1h, &r.CacheRead, &thinking, &side); err != nil {
				rows.Close()
				return err
			}
			r.TS = record.FormatTime(time.UnixMilli(ts))
			r.Thinking = &thinking
			sc := side == 1
			r.Sidechain = &sc
			get(r.SessionID).rows = append(get(r.SessionID).rows, r)
		}
		rows.Close()
	}
	if im.has("limit_hits") {
		rows, err := im.src.QueryContext(ctx, `SELECT session_id, ts, coalesce(type, ''), coalesce(status, ''), coalesce(resets_at, 0) FROM limit_hits ORDER BY session_id, ts`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sid, typ, status string
			var ts, resets int64
			if err := rows.Scan(&sid, &ts, &typ, &status, &resets); err != nil {
				rows.Close()
				return err
			}
			h := map[string]any{"session_id": sid, "ts": record.FormatTime(time.UnixMilli(ts)), "type": typ, "status": status}
			if resets > 0 {
				h["resets_at"] = record.FormatTime(time.UnixMilli(resets))
			}
			b, _ := json.Marshal(h)
			get(sid).hits = append(get(sid).hits, b)
		}
		rows.Close()
	}
	for id := range im.sessions {
		get(id)
	}
	for id := range im.owner {
		get(id)
	}
	ids := make([]string, 0, len(bySession))
	for id := range bySession {
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, sid := range ids {
		f := bySession[sid]
		info := map[string]any{"session_id": sid}
		if l, ok := im.sessionLogin(sid); ok {
			info["owner_account"], info["owner_org"] = l.account, l.org
		}
		if s, ok := im.sessions[sid]; ok && s.remote != "" {
			info["remote_url"] = s.remote
		}
		infoJSON, _ := json.Marshal(info)
		chunks := (len(f.rows) + record.MaxTokenRows - 1) / record.MaxTokenRows
		if chunks == 0 {
			chunks = 1
		}
		for c := 0; c < chunks; c++ {
			rows := f.rows[min(c*record.MaxTokenRows, len(f.rows)):min((c+1)*record.MaxTokenRows, len(f.rows))]
			p := record.TokenRowsPayload{Rows: rows}
			var ts int64
			key := sid
			if len(rows) > 0 {
				t, _ := time.Parse(time.RFC3339Nano, rows[0].TS)
				ts, key = t.UnixMilli(), sid+"|"+rows[0].Key
			} else if s, ok := im.sessions[sid]; ok && s.started > 0 {
				ts = s.started
			} else if len(f.hits) > 0 {
				var h struct{ TS string }
				_ = json.Unmarshal(f.hits[0], &h)
				t, _ := time.Parse(time.RFC3339Nano, h.TS)
				ts = t.UnixMilli()
			} else {
				ts = im.t0
			}
			if c == 0 {
				p.Sessions = []json.RawMessage{infoJSON}
				p.LimitHits = f.hits
			}
			r := im.newRecord(record.KindTokenRows, detID(ts, fmt.Sprintf("tokens|%s|%d", key, c)), ts)
			if err := r.SetPayload(p); err != nil {
				return err
			}
			*out = append(*out, r)
		}
	}
	return nil
}

// direct writes what no record kind carries.
func (im *importer) direct(ctx context.Context) error {
	return im.st.Write(ctx, func(tx *store.Tx) error {
		for id, s := range im.sessions {
			if _, ok, err := tx.GetSession(id); err != nil || !ok {
				if err != nil {
					return err
				}
				continue
			}
			info := store.SessionInfo{Cwd: s.cwd, Project: project(s.cwd), TranscriptPath: s.transcript, Model: s.model,
				RemoteURL: s.remote, StartedAt: s.started}
			if err := tx.PatchSession(id, info); err != nil {
				return err
			}
		}
		for id, a := range im.accounts {
			l, ok := im.loginOfAccount(id)
			if !ok {
				l = login{account: "prototype-" + id}
			}
			seat, found, err := seatOf(tx, l)
			if err != nil {
				return err
			}
			if !found {
				// The seat of a prototype account that matched no login, made from its snapshots.
				for _, org := range im.orgsOfAccount(ctx, id) {
					if s, ok, err := seatOf(tx, login{account: "prototype-" + id, org: org}); err == nil && ok {
						seat, found = s, true
					}
				}
			}
			if !found {
				im.notes = append(im.notes, fmt.Sprintf("account %s (%s): no seat to label", id, a.label))
				continue
			}
			colour := strings.ToLower(a.color)
			if !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(colour) {
				colour = ""
			}
			ord := a.ord
			if err := tx.SeatLookImport(seat, a.label, colour, &ord); err != nil {
				return err
			}
			if plan := normPlan(a.plan); plan != "" && l.org != "" {
				if err := tx.OrgInfo(l.org, "", plan, "", ""); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (im *importer) orgsOfAccount(ctx context.Context, id string) []string {
	if !im.has("snapshots") {
		return nil
	}
	rows, err := im.src.QueryContext(ctx, `SELECT DISTINCT org_uuid FROM snapshots WHERE account_id = ? AND org_uuid IS NOT NULL`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var o string
		if rows.Scan(&o) == nil {
			out = append(out, o)
		}
	}
	return out
}

func seatOf(tx *store.Tx, l login) (string, bool, error) {
	if l.org == "" {
		var id string
		err := tx.QueryRowContext(tx.Ctx(), `SELECT id FROM seat WHERE account_uuid = ? ORDER BY created_at LIMIT 1`, l.account).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return id, err == nil, err
	}
	var id string
	err := tx.QueryRowContext(tx.Ctx(), `SELECT id FROM seat WHERE account_uuid = ? AND org_uuid = ?`, l.account, l.org).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

func normPlan(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "":
		return ""
	case "max 20x":
		return "max_20x"
	case "max 5x", "max":
		return "max_5x"
	case "pro":
		return "pro"
	case "team":
		return "team"
	case "enterprise":
		return "enterprise"
	case "free":
		return "free"
	}
	return usage.PlanOf(nil, p, "", "")
}

func project(cwd string) string {
	if cwd == "" {
		return ""
	}
	return path.Base(strings.TrimRight(cwd, "/"))
}

func (im *importer) report(ctx context.Context, rep *Report) error {
	src := func(t string) int64 {
		if !im.has(t) {
			return 0
		}
		var n int64
		_ = im.src.QueryRowContext(ctx, `SELECT count(*) FROM `+t).Scan(&n)
		return n
	}
	hub := func(q string, args ...any) int64 {
		var n int64
		_ = im.st.R().QueryRowContext(ctx, q, args...).Scan(&n)
		return n
	}
	imp := `(SELECT id FROM record WHERE device_id = '` + DeviceID + `')`
	var protoSessions int64
	if im.has("sessions") {
		protoSessions = src("sessions")
	}
	allSessions := map[string]bool{}
	for id := range im.sessions {
		allSessions[id] = true
	}
	for _, t := range []string{"tokens", "events", "limit_hits", "session_owner", "session_dirs"} {
		if !im.has(t) {
			continue
		}
		rows, err := im.src.QueryContext(ctx, `SELECT DISTINCT session_id FROM `+t+` WHERE session_id IS NOT NULL AND session_id != ''`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				allSessions[id] = true
			}
		}
		rows.Close()
	}
	labelled := hub(`SELECT count(*) FROM seat WHERE label IS NOT NULL`)
	rep.Rows = []Row{
		{Table: "sessions", Source: protoSessions, Hub: "session", Count: hub(`SELECT count(*) FROM session`),
			Note: fmt.Sprintf("%d distinct session ids across all prototype tables", len(allSessions))},
		{Table: "events", Source: src("events"), Hub: "hook_event", Count: hub(`SELECT count(*) FROM hook_event WHERE record_id IN ` + imp)},
		{Table: "snapshots", Source: src("snapshots"), Hub: "web_usage", Count: hub(`SELECT count(*) FROM web_usage WHERE record_id IN ` + imp)},
		{Table: "windows", Source: src("windows"), Hub: "limit_sample (web)", Count: hub(`SELECT count(*) FROM limit_sample WHERE source = 'web' AND record_id IN ` + imp),
			Note: "rebuilt from the snapshots' raw responses"},
		{Table: "tokens", Source: src("tokens"), Hub: "token_row", Count: hub(`SELECT count(*) FROM token_row`)},
		{Table: "limit_hits", Source: src("limit_hits"), Hub: "limit_hit", Count: hub(`SELECT count(*) FROM limit_hit`)},
		{Table: "config_dirs", Source: src("config_dirs"), Hub: "slot", Count: hub(`SELECT count(*) FROM slot WHERE machine_id = ?`, im.machine)},
		{Table: "accounts", Source: src("accounts"), Hub: "seat (labelled)", Count: labelled,
			Note: fmt.Sprintf("%d seats in all", hub(`SELECT count(*) FROM seat`))},
		{Table: "breakdown", Source: src("breakdown"), Hub: "-", Note: "not copied: in web_usage.raw (seven_day_breakdown)"},
		{Table: "files, kv", Source: src("files") + src("kv"), Hub: "-", Note: "not copied: prototype scanner state"},
	}
	rep.TypeErrors = hub(`SELECT count(*) FROM record WHERE device_id = ? AND type_error IS NOT NULL`, DeviceID)
	if rep.TypeErrors > 0 {
		rows, err := im.st.R().QueryContext(ctx, `SELECT type_error, count(*) FROM record WHERE device_id = ? AND type_error IS NOT NULL GROUP BY 1 ORDER BY 2 DESC LIMIT 5`, DeviceID)
		if err == nil {
			for rows.Next() {
				var e string
				var n int
				if rows.Scan(&e, &n) == nil {
					im.notes = append(im.notes, fmt.Sprintf("%d records not typed: %s", n, e))
				}
			}
			rows.Close()
		}
	}
	return nil
}
