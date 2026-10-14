package outbox

import (
	"os"
	"path/filepath"
)

// AnsweredBase is the base name of the sidecar (answered.jsonl, rotated to answered.1.jsonl)
// that keeps the records of answered spill files for episode keys.
const AnsweredBase = "answered"

// Rejected is one record the hub refused for good (the IngestReply shape).
type Rejected struct {
	ID     string
	Reason string
	Detail string
}

// Answer is what a hub said about one batch: the ingest reply of an HTTP upload, or the
// ack of a drain frame. Accepted includes Duplicates (a duplicate was stored earlier).
type Answer struct {
	Accepted   []string
	Duplicates []string
	Rejected   []Rejected
}

// Applied says what Apply did.
type Applied struct {
	Missing    int // records of the batch the answer did not name
	Accepted   int // answered and not rejected (includes Duplicates)
	Duplicates int
	Rejected   int
}

// Apply is the one place an answer from the hub is applied (outbox.md 5 step 3, 8), for
// `sy flush` over HTTP and `sy outbox drain` over a pipe alike: rejected records go to
// rejected.jsonl first (so a crash here only repeats a log line), answered spill files are
// deleted, and *cur is moved past the longest prefix of day-file records the hub answered
// (and past corrupt or blank lines read with them) when the whole batch was answered, or
// past the answered prefix. A batch with a record the answer left out moves the cursor only
// over that answered prefix; Failures and RetryAfter reset when nothing is missing.
//
// Apply changes *cur but does not write cursor.json: the caller writes it (WriteCursor), so
// that "the cursor moves only after the answer" stays one visible step. It touches no
// record that the answer does not name.
func (o *Outbox) Apply(cur *Cursor, b *Batch, ans Answer, logf func(string, ...any)) Applied {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	answered := make(map[string]bool, len(ans.Accepted)+len(ans.Rejected))
	accepted := make(map[string]bool, len(ans.Accepted))
	for _, id := range ans.Accepted {
		answered[id] = true
		accepted[id] = true
	}
	rejected := make(map[string]Rejected, len(ans.Rejected))
	for _, r := range ans.Rejected {
		answered[r.ID] = true
		rejected[r.ID] = r
	}
	dup := make(map[string]bool, len(ans.Duplicates))
	for _, id := range ans.Duplicates {
		dup[id] = true
	}

	var res Applied
	prefix, dayCount, prefixOK := 0, 0, true
	for _, it := range b.Items {
		if !answered[it.ID] {
			res.Missing++
		}
		if it.Spill {
			continue
		}
		dayCount++
		if prefixOK && answered[it.ID] {
			prefix++
		} else {
			prefixOK = false
		}
	}

	// An id the hub reports as both accepted and rejected counts as accepted.
	for _, it := range b.Items {
		if r, ok := rejected[it.ID]; ok && !accepted[it.ID] {
			reason := r.Reason
			if r.Detail != "" {
				reason += ": " + r.Detail
			}
			if err := o.LogRejected(it, reason); err != nil {
				logf("rejected log: %v", err)
			}
			res.Rejected++
		} else if answered[it.ID] {
			res.Accepted++
			if dup[it.ID] {
				res.Duplicates++
			}
		}
	}

	// An answered spill file is deleted, but its records are first copied to answered.jsonl:
	// direct push folds the outbox to find the record that entered a session's state, and
	// its episode key is that record's ts. Without the copy a later record would become the
	// entering one and the same episode would be pushed a second time.
	for _, it := range b.Items {
		if it.Spill && answered[it.ID] {
			if err := o.appendCapped(AnsweredBase, it.Raw); err != nil {
				logf("answered log: %v", err)
			}
		}
	}
	for _, it := range b.Items {
		if it.Spill && answered[it.ID] {
			_ = os.Remove(filepath.Join(o.Dir, it.File))
		}
	}

	switch {
	case prefix == dayCount:
		cur.File, cur.Offset = b.Next.File, b.Next.Offset
	case prefix > 0:
		last := b.Items[0]
		n := 0
		for _, it := range b.Items {
			if it.Spill {
				continue
			}
			n++
			last = it
			if n == prefix {
				break
			}
		}
		cur.File, cur.Offset = last.File, last.End
	}
	if res.Missing == 0 {
		cur.Failures, cur.RetryAfter = 0, ""
	}
	return res
}
