package peers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SendResult is the outcome of a same-machine send (peers.md 4.3).
type SendResult struct {
	Msg    string
	Thread string
	Target Session
	Sender Session
	// HeldLikely: the two permission classes differ, so Claude Code will probably hold the
	// message for the receiver's approval (peers.md 8.5). The send still counts as delivered.
	HeldLikely bool
}

// Line is the one status line `sy peers send` prints on success.
func (r SendResult) Line() string {
	s := fmt.Sprintf("delivered to %s (msg %s, thread %s)", r.Target.Handle, r.Msg, r.Thread)
	if r.HeldLikely {
		s += ": the receiving session is in a different permission mode, so Claude Code may ask its user to approve it"
	}
	return s
}

// ReplySocketPath is `$XDG_RUNTIME_DIR/sy/r/<16 hex>.sock`, the first 16 hex of
// SHA-256(sender session id) (peers.md 8.6). PE1-local only names it (the agent that
// listens there is PE2); a native SendMessage to it finds nobody, and the header's
// "Reply with: sy peers send" line is the reply path.
func ReplySocketPath(runtime, senderSession string) string {
	if runtime == "" {
		runtime = "/run/user/" + fmt.Sprint(os.Getuid())
	}
	sum := sha256.Sum256([]byte(senderSession))
	return filepath.Join(runtime, "sy", "r", hex.EncodeToString(sum[:])[:16]+".sock")
}

// Header is the first two lines of the message body (peers.md 8.5).
func Header(handle, label, machine, thread string) string {
	return fmt.Sprintf("[Switchyard] from %s · account %s · machine %s · thread %s\nReply with: sy peers send %s \"…\"",
		handle, label, machine, thread, handle)
}

func (e Env) newID() string {
	if e.NewID != nil {
		return e.NewID()
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Send is the same-machine path of peers.md 4.2: the sender's checks in order, then the
// write into the target's inbox (8.5). A *Refusal is final: nothing is retried. The caller
// has been identified by Whoami; the directory is re-read here so a session that just ended
// is `target_gone`.
//
// Order: off; target resolves and is live; content; rate limits; matrix; loop guard;
// delivery. The matrix, thread and rate files are updated only on delivery, except that a
// rate token is taken when the rate check passes.
func (e Env) Send(c Caller, to, text string) (SendResult, *Refusal) {
	if e.Off() {
		return SendResult{}, Refuse(CodeOff, "peers are off on this machine (sy peers on turns them back on)")
	}
	dir := e.Directory()
	var sender *Session
	for i := range dir {
		if dir[i].Entry.SessionID == c.Entry.SessionID {
			sender = &dir[i]
		}
	}
	if sender == nil {
		return SendResult{}, Refuse(CodeNotASession, "this session's inbox socket is not answering")
	}
	target, _, rf := Resolve(dir, to)
	if rf != nil {
		return SendResult{}, rf
	}
	if target.Entry.SessionID == sender.Entry.SessionID {
		return SendResult{}, Refuse(CodeUnknownTarget, "that is this session")
	}
	clean, rf := CheckText(text)
	if rf != nil {
		return SendResult{}, rf
	}
	now := e.now()
	rates := LoadRates(e.RatesPath())
	if wait, ok := CheckRate(rates, now, sender.Entry.SessionID, PairKey(sender.Entry.SessionID, target.Entry.SessionID)); !ok {
		return SendResult{}, Refuse(CodeRateLimited, "too many messages; retry in %s", wait.Round(time.Second))
	}
	rates.Prune(now)
	_ = rates.Save(e.RatesPath())

	m := e.matrix()
	threads := LoadThreads(e.ThreadsPath())
	live := map[string]bool{}
	for _, s := range dir {
		live[s.Entry.SessionID] = true
	}
	threads.Expire(now, func(id string) bool { return live[id] })
	th := threads.FindOpen(sender.Entry.SessionID, target.Entry.SessionID)
	if d := m.Allowed(party(*sender), party(target), th); !d.Allowed {
		reason := d.Reason
		if reason == "" {
			reason = CodeNotAllowed
		}
		return SendResult{}, &Refusal{Code: reason, Detail: d.Detail}
	}
	if th == nil {
		th = threads.Start(sender.Entry.SessionID, target.Entry.SessionID, sender.Account, target.Account, now)
	}
	maxTurns := m.MaxTurns()
	if maxTurns <= 0 {
		maxTurns = e.MaxTurns
	}
	if LoopGuard(th, newestHuman(sender, &target), maxTurns, now) {
		_ = threads.Save(e.ThreadsPath()) // keeps paused_at
		return SendResult{}, Refuse(CodeLoopGuard, "%d automated turns without a human prompt in either session; paused until you type in one of them", maxTurns)
	}

	from, err := UDSAddress(ReplySocketPath(e.Runtime, sender.Entry.SessionID))
	if err != nil {
		return SendResult{}, Refuse(CodeFailed, "%v", err)
	}
	w := Wrapper{
		From: from, FromSession: sender.Entry.SessionID, FromName: sender.Handle, FromMode: sender.Mode,
		Body: Header(sender.Handle, sender.AccountLabel, e.Machine.Name, th.ID) + "\n\n" + clean,
	}
	msg := e.newID()
	frame := NewFrame(msg, target.Entry.SessionID, w)
	if err := WriteFrame(target.Entry.SocketOf(e.Runtime), frame); err != nil {
		code := CodeFailed
		if errors.Is(err, ErrTargetGone) {
			code = CodeTargetGone
		}
		return SendResult{}, Refuse(code, "%v", err)
	}
	th.RecordMessage(now)
	_ = threads.Save(e.ThreadsPath())
	_ = MarkDelivered(e.State, target.Entry.SessionID, now)
	e.audit(msg, th, *sender, target, len(clean), "delivered", now)
	return SendResult{Msg: msg, Thread: th.ID, Target: target, Sender: *sender, HeldLikely: sender.Mode != target.Mode}, nil
}

func newestHuman(a, b *Session) time.Time {
	var t time.Time
	for _, s := range []Session{*a, *b} {
		if s.HasFile && s.File.HumanAt != nil && s.File.HumanAt.After(t) {
			t = *s.File.HumanAt
		}
	}
	return t
}

// auditLine is one line of <state>/peers/audit.jsonl: ids, sizes and statuses, never text
// (peers.md 10).
type auditLine struct {
	TS     string `json:"ts"`
	Msg    string `json:"msg"`
	Thread string `json:"thread"`
	From   string `json:"from"`
	To     string `json:"to"`
	FromA  string `json:"from_account"`
	ToA    string `json:"to_account"`
	Size   int    `json:"size"`
	Local  bool   `json:"local"`
	Status string `json:"status"`
}

func (e Env) audit(msg string, th *Thread, from, to Session, size int, status string, now time.Time) {
	b, err := json.Marshal(auditLine{
		TS: now.UTC().Format(time.RFC3339Nano), Msg: msg, Thread: th.ID,
		From: from.Entry.SessionID, To: to.Entry.SessionID, FromA: from.Account, ToA: to.Account,
		Size: size, Local: true, Status: status,
	})
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(e.AuditPath()), 0o700) != nil {
		return
	}
	f, err := os.OpenFile(e.AuditPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// Describe turns a refusal into the one line `sy peers send` prints (peers.md 4.3).
func Describe(r *Refusal) string {
	switch r.Code {
	case CodeNotAllowed:
		return "not delivered: " + r.Detail + ". Ask the owner to change it; do not retry."
	case CodePaused:
		return "not delivered: peers are paused for every machine. Ask the owner to resume them; do not retry."
	case CodeNoMatrix:
		return "not delivered: the matrix is not configured on this machine, so nothing is allowed yet. Ask the owner to set it up; do not retry."
	case CodeOff:
		return "not delivered: " + r.Detail + "."
	case CodeLoopGuard:
		return "not delivered: " + r.Detail + "."
	case CodeRateLimited:
		return "not delivered: " + r.Detail + "."
	case CodeSecret:
		return "not delivered: " + r.Detail + ". Remove it and send again only if the user asks."
	case CodeAmbiguous:
		return "not delivered: " + r.Detail + ". Pick one handle and send again."
	}
	return "not delivered: " + strings.TrimPrefix(r.Detail, r.Code+": ")
}
