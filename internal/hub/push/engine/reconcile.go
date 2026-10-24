package engine

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/mraj602/watchyard/internal/hub/events"
	"github.com/mraj602/watchyard/internal/hub/ingest"
	"github.com/mraj602/watchyard/internal/hub/push/model"
	"github.com/mraj602/watchyard/internal/hub/push/rules"
	"github.com/mraj602/watchyard/internal/hub/store"
)

// work is one transaction's view: rules, settings, time, and what to publish after commit.
type work struct {
	e     *Engine
	tx    *store.Tx
	now   time.Time
	nowMS int64
	env   rules.Env
	loc   *time.Location
	hub   string // public URL
	evs   []events.Event
}

// hubTZ is the hub.tz setting, else Config.TZ (the machine's zone, rules.SystemZone), else UTC.
func (e *Engine) hubTZ(tx *store.Tx) string {
	if raw, ok, err := tx.AlertSetting(store.AlertKeyHubTZ); err == nil && ok {
		var v string
		if json.Unmarshal(raw, &v) == nil && v != "" {
			return v
		}
	}
	if e.cfg.TZ != "" {
		return e.cfg.TZ
	}
	return "UTC"
}

// LoadSettings reads push.settings over the defaults (push.md 2); a bad stored value is
// logged and the defaults are used.
func LoadSettings(tx *store.Tx, hubTZ string) (model.Settings, error) {
	raw, _, err := tx.AlertSetting(store.AlertKeyPushSettings)
	if err != nil {
		return model.Settings{}, err
	}
	s, perr := model.ParseSettings(raw, hubTZ)
	if perr != nil {
		s, _ = model.ParseSettings(nil, hubTZ)
	}
	return s, nil
}

func (e *Engine) newWork(tx *store.Tx, now time.Time) (*work, error) {
	tz := e.hubTZ(tx)
	set, err := LoadSettings(tx, tz)
	if err != nil {
		return nil, err
	}
	rs, err := tx.RuleList()
	if err != nil {
		return nil, err
	}
	hub := e.cfg.PublicURL
	if hub == "" {
		if raw, ok, _ := tx.AlertSetting(store.AlertKeyPublicURL); ok {
			_ = json.Unmarshal(raw, &hub)
		}
	}
	w := &work{e: e, tx: tx, now: now, nowMS: now.UnixMilli(), loc: rules.Location(tz), hub: hub}
	w.env = rules.Env{Rules: rs, Settings: set, NowMS: w.nowMS, AwakeMS: e.awakeMS.Load()}
	return w, nil
}

// event queues one `alert` stream event (api.yaml StreamAlert).
func (w *work) event(change string, a model.Alert) {
	data, _ := json.Marshal(map[string]any{"change": change, "alert": a.AlertBrief, "session": a.Session,
		"seat": a.Seat, "machine": a.Machine})
	w.evs = append(w.evs, events.Event{Type: "alert", Data: data})
}

// startup is push.md 3.5's start-up reconcile and 3.8's baseline.
func (e *Engine) startup(ctx context.Context, now time.Time) error {
	var evs []events.Event
	err := e.st.Write(ctx, func(tx *store.Tx) error {
		w, err := e.newWork(tx, now)
		if err != nil {
			return err
		}
		_, done, err := tx.AlertSetting(store.AlertKeyBaselineDone)
		if err != nil {
			return err
		}
		w.env.Baseline = !done
		sessions, seats, machines, err := tx.AlertSubjects(w.nowMS)
		if err != nil {
			return err
		}
		if err := w.reconcile(sessions, seats, machines); err != nil {
			return err
		}
		if w.env.Baseline {
			if err := w.sweepMachines(); err != nil {
				return err
			}
			if err := tx.PutAlertSetting(store.AlertKeyBaselineDone, []byte("true"), w.nowMS); err != nil {
				return err
			}
			e.log.Printf("alert baseline recorded")
		}
		evs = w.evs
		return nil
	})
	if err == nil {
		e.publish(evs)
	}
	return err
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// reconcileChanges reconciles every subject a batch touched.
func (w *work) reconcileChanges(ch ingest.Changes) error {
	return w.reconcile(sortedKeys(ch.Sessions), sortedKeys(ch.Seats), sortedKeys(ch.Machines))
}

func (w *work) reconcile(sessions, seats, machines []string) error {
	for _, id := range sessions {
		if err := w.reconcileSession(id); err != nil {
			return err
		}
	}
	for _, id := range seats {
		if err := w.reconcileSeat(id); err != nil {
			return err
		}
	}
	if len(machines) > 0 {
		ms, err := w.tx.AlertMachines(machines, w.nowMS)
		if err != nil {
			return err
		}
		for _, m := range ms {
			if err := w.reconcileMachine(m); err != nil {
				return err
			}
		}
	}
	return nil
}

// sessionNow turns a store row into the rules' view.
func sessionNow(id string, r store.AlertSessionRow, found bool) rules.SessionNow {
	if !found {
		return rules.SessionNow{ID: id}
	}
	return rules.SessionNow{
		ID: id, Found: true, State: r.State, NeedsYou: r.NeedsYou, SinceMS: r.SinceAt, Seat: r.SeatID,
		Project: r.Project, Machine: r.MachineID, Remote: r.Remote, Detail: r.Detail, Peers: r.Peers, Facts: sessionFacts(r),
	}
}

// seatName is the board's name of a seat (store.SeatDisplayNames), else its label or email
// local part (push.md 5.5).
func seatName(name, label, email string) string {
	if name != "" {
		return name
	}
	return rules.SeatLabel(label, email)
}

// sessionFacts are the text inputs of a session alert (push.md 5.5).
func sessionFacts(r store.AlertSessionRow) model.Facts {
	f := model.Facts{
		Project: r.Project, Machine: r.MachineName, State: r.State, Detail: r.Detail, Tool: r.Tool,
		Question: r.Question, RemoteURL: r.Remote, ResetsAt: model.Time(r.LimitResetsAt),
	}
	if f.Machine == "" {
		f.Machine = r.MachineID
	}
	if r.SeatID != "" {
		f.Seat = seatName(r.SeatName, r.SeatLabel, r.Email)
	}
	switch {
	case r.State == "needs_permission" || r.Detail == "elicitation":
		f.Message = r.Message
	default:
		f.Message = r.LastMessage
	}
	return f
}

func (w *work) reconcileSession(id string) error {
	r, found, err := w.tx.AlertSession(id)
	if err != nil {
		return err
	}
	cur := sessionNow(id, r, found)
	_, want := rules.SessionWant(cur)
	var eps []string
	if want != "" {
		eps = []string{want}
	}
	alerts, err := w.tx.AlertsOf("session_id", id, eps)
	if err != nil {
		return err
	}
	return w.apply(rules.Decide(rules.Current{Session: &cur}, alerts, w.env))
}

func (w *work) seatNow(id string) (rules.SeatNow, bool, error) {
	r, found, err := w.tx.AlertSeat(id, w.nowMS)
	if err != nil || !found {
		return rules.SeatNow{ID: id}, found, err
	}
	cur := rules.SeatNow{ID: id, Facts: model.Facts{Seat: seatName(r.Name, r.Label, r.Email)}}
	for _, x := range r.Windows {
		wn := rules.WindowNow{Key: x.Key, ResetsMS: x.ResetsAt, UpdatedMS: x.UpdatedAt}
		if x.Peak != nil {
			wn.Peak, wn.HasPeak = *x.Peak, true
		}
		if x.Final != nil {
			wn.Final = *x.Final
		}
		cur.Windows = append(cur.Windows, wn)
	}
	return cur, true, nil
}

func (w *work) reconcileSeat(id string) error {
	cur, _, err := w.seatNow(id)
	if err != nil {
		return err
	}
	alerts, err := w.tx.AlertsOf("seat_id", id, rules.SeatEpisodes(cur))
	if err != nil {
		return err
	}
	var own []model.Alert
	for _, a := range alerts {
		if rules.IsWindowTrigger(a.Trigger) {
			own = append(own, a)
		}
	}
	return w.apply(rules.Decide(rules.Current{Seat: &cur}, own, w.env))
}

func machineNow(m store.AlertMachineRow) rules.MachineNow {
	name := m.Name
	if name == "" {
		name = m.ID
	}
	return rules.MachineNow{ID: m.ID, LastSeenMS: m.LastSeen, Live: m.Live, Facts: model.Facts{Machine: name}}
}

func (w *work) reconcileMachine(m store.AlertMachineRow) error {
	cur := machineNow(m)
	alerts, err := w.tx.AlertsOf("machine_id", m.ID, []string{rules.MachineEpisode(cur)})
	if err != nil {
		return err
	}
	var own []model.Alert
	for _, a := range alerts {
		if a.Trigger == model.TriggerMachineSilent {
			own = append(own, a)
		}
	}
	return w.apply(rules.Decide(rules.Current{Machine: &cur}, own, w.env))
}

// sweepMachines is the silence sweep (push.md 3.6 step 2) over every machine.
func (w *work) sweepMachines() error {
	ms, err := w.tx.AlertMachines(nil, w.nowMS)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if err := w.reconcileMachine(m); err != nil {
			return err
		}
	}
	return nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// apply carries out reconcile's ops.
func (w *work) apply(ops []rules.Op) error {
	for _, op := range ops {
		switch op.Kind {
		case rules.OpCreate:
			a := op.Alert
			a.ID = store.NewID("al_", w.now)
			if w.holdNew(a) {
				// Catch-up hold (push.md 3.3): created already overdue from a machine with more
				// records queued; its direct_push record may be a few batches behind.
				a.DueAt = model.Time(w.nowMS + CatchUpStep.Milliseconds())
			}
			ok, err := w.tx.AlertInsert(a)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if a.Status == model.StatusSkipped {
				w.event("skipped", a)
				if r := deref(a.Reason); r != model.ReasonBaseline {
					w.e.log.Printf("alert skip %s trigger=%s %s reason=%s", a.ID, a.Trigger, subjectLog(a), r)
				}
			} else {
				w.event("created", a)
			}
		case rules.OpEnd:
			if err := w.end(op.Alert, op.Status, op.Reason); err != nil {
				return err
			}
		}
	}
	return nil
}

// end moves an alert to cancelled or closed (push.md 4) and drops its unsent deliveries.
func (w *work) end(a model.Alert, status model.Status, reason string) error {
	a.Status, a.Reason, a.DueAt, a.EndedAt = status, &reason, 0, model.Time(w.nowMS)
	if err := w.tx.AlertSave(a, w.nowMS); err != nil {
		return err
	}
	if err := w.tx.DeliveriesSkipUnsent(a.ID, "resolved", w.nowMS); err != nil {
		return err
	}
	if err := w.dismiss(a); err != nil {
		return err
	}
	w.event(string(status), a)
	if reason == model.ReasonRuleOff || reason == model.ReasonStale {
		w.e.log.Printf("alert %s %s trigger=%s %s reason=%s", status, a.ID, a.Trigger, subjectLog(a), reason)
	}
	return nil
}

func subjectLog(a model.Alert) string {
	switch {
	case a.Session != nil:
		return "session=" + *a.Session
	case a.Seat != nil:
		return "seat=" + *a.Seat + " window=" + deref(a.WindowKey)
	case a.Machine != nil:
		return "machine=" + *a.Machine
	}
	return ""
}

// holdNew reports whether a session alert being created gets the catch-up hold: pending,
// already due, and its machine's latest batch reported more records queued.
func (w *work) holdNew(a model.Alert) bool {
	return a.Status == model.StatusPending && rules.IsSessionTrigger(a.Trigger) && a.Machine != nil &&
		int64(a.DueAt) <= w.nowMS && w.e.catchingUp(*a.Machine, w.now)
}

// holdAtFire reports whether a pending session alert that is due is postponed once more by
// the catch-up hold, and until when: while its machine still reports a backlog, by
// CatchUpStep, never past CatchUpMax after the alert was created.
func (w *work) holdAtFire(a model.Alert) (int64, bool) {
	if a.Status != model.StatusPending || !rules.IsSessionTrigger(a.Trigger) || a.Machine == nil {
		return 0, false
	}
	limit := int64(a.CreatedAt) + CatchUpMax.Milliseconds()
	if w.nowMS >= limit || !w.e.catchingUp(*a.Machine, w.now) {
		return 0, false
	}
	return min(w.nowMS+CatchUpStep.Milliseconds(), limit), true
}
