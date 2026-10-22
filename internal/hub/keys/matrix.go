package hubkeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/mraj602/switchyard/internal/hub/store"
	"github.com/mraj602/switchyard/internal/keys"
)

// The matrix at the hub (peers.md 5, keys.md 5): version 1 on first use, owner-signed
// documents taken from anyone, edits the hub signs itself, every version stored in
// matrix_doc with a peer_audit row. Evaluating the matrix is the peers packages' job.

// MatrixRecord is a stored matrix version that verified, with its row's metadata.
type MatrixRecord struct {
	Signed    keys.Signed[keys.MatrixBody]
	Widens    bool
	MadeBy    string
	CreatedAt int64
}

func matrixDocOf(r store.SignedDocRow) keys.Doc {
	return keys.Doc{Type: keys.TypeMatrix, Body: keys.B64(r.Body), Sigs: []keys.Sig{{By: r.Signer, FP: r.KeyFP, Sig: keys.B64(r.Sig)}}}
}

// newestMatrix is the newest row (rows are newest first) that verifies; others are logged.
func (s *Service) newestMatrix(rows []store.SignedDocRow, t keys.Trust) *MatrixRecord {
	for _, r := range rows {
		sd, err := keys.VerifyMatrix(matrixDocOf(r), t)
		if err == nil && sd.Body.Version != r.Version {
			err = errors.New("version column differs from the body")
		}
		if err != nil {
			s.logf("keys: matrix_doc version %d ignored: %v", r.Version, err)
			continue
		}
		return &MatrixRecord{Signed: sd, Widens: r.Widens, MadeBy: r.MadeBy, CreatedAt: r.CreatedAt}
	}
	return nil
}

func forbidden(format string, a ...any) error {
	return &Error{http.StatusForbidden, "forbidden", fmt.Sprintf(format, a...)}
}

// MatrixCurrent is the newest verified matrix; when the hub has none yet it signs and stores
// version 1 (nothing allowed, same_account on, not paused, no turn limit; peers.md 5.1).
func (s *Service) MatrixCurrent(ctx context.Context) (*MatrixRecord, error) {
	var rec *MatrixRecord
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		var err error
		rec, err = s.currentTx(tx, "hub")
		return err
	})
	return rec, err
}

func (s *Service) currentTx(tx *store.Tx, madeBy string) (*MatrixRecord, error) {
	t, _, err := s.trustTx(tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.SignedDocs(true, 0)
	if err != nil {
		return nil, err
	}
	if rec := s.newestMatrix(rows, t); rec != nil {
		return rec, nil
	}
	maxV, err := tx.MaxSignedDocVersion(true)
	if err != nil {
		return nil, err
	}
	body := keys.BaselineMatrix(s.Key.FP, time.UnixMilli(tx.NowMS()))
	body.Version = maxV + 1 // above any row that did not verify
	return s.storeHubMatrix(tx, body, madeBy, false, "matrix")
}

// storeHubMatrix signs body with the hub key, stores it and writes the audit row.
func (s *Service) storeHubMatrix(tx *store.Tx, body keys.MatrixBody, madeBy string, widens bool, event string) (*MatrixRecord, error) {
	d, err := s.SignMatrix(body)
	if err != nil {
		return nil, err
	}
	raw, _ := d.BodyBytes()
	sig, _ := keys.UnB64(d.Sigs[0].Sig)
	if err := tx.InsertSignedDoc(true, store.SignedDocRow{Version: body.Version, Body: raw, Signer: keys.ByHub, KeyFP: s.Key.FP,
		Sig: sig, Widens: widens, MadeBy: madeBy, CreatedAt: tx.NowMS()}); err != nil {
		return nil, err
	}
	if err := s.auditMatrix(tx, madeBy, event, body, keys.ByHub, widens); err != nil {
		return nil, err
	}
	return &MatrixRecord{Signed: keys.Signed[keys.MatrixBody]{Body: body, Raw: raw, Signer: keys.ByHub, Doc: d}, Widens: widens,
		MadeBy: madeBy, CreatedAt: tx.NowMS()}, nil
}

func (s *Service) auditMatrix(tx *store.Tx, actor, event string, b keys.MatrixBody, signer string, widens bool) error {
	d, _ := json.Marshal(map[string]any{"version": b.Version, "signer": signer, "widens": widens, "paused": b.Paused,
		"same_account": b.SameAccount, "max_turns": b.MaxTurns, "pairs": len(b.Allow)})
	return tx.InsertPeerAudit(store.PeerAuditRow{At: tx.NowMS(), Actor: actor, Event: event, Detail: d})
}

// PutOwnerMatrix takes an owner-signed matrix document from any authenticated caller
// (keys.md 5.3): it must verify against the active owner key and be the next version.
func (s *Service) PutOwnerMatrix(ctx context.Context, doc keys.Doc, madeBy string) (*MatrixRecord, error) {
	var rec *MatrixRecord
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		t, owner, err := s.trustTx(tx)
		if err != nil {
			return err
		}
		if owner == nil {
			return conflict("this hub has no active owner key")
		}
		cur, err := s.currentTx(tx, "hub")
		if err != nil {
			return err
		}
		next, err := keys.VerifyMatrix(doc, t)
		if err != nil {
			return badRequest("matrix document: %v", err)
		}
		if err := keys.HubTakesOwnerMatrix(&cur.Signed.Body, next); err != nil {
			if errors.Is(err, keys.ErrVersion) {
				return conflict("%v; build on version %d", err, cur.Signed.Body.Version)
			}
			return badRequest("%v", err)
		}
		maxV, err := tx.MaxSignedDocVersion(true)
		if err != nil {
			return err
		}
		if next.Body.Version <= maxV {
			return conflict("the hub already stores matrix version %d; build on it", maxV)
		}
		widens := keys.Widens(cur.Signed.Body, next.Body)
		sig, _ := keys.UnB64(doc.Sigs[0].Sig)
		if err := tx.InsertSignedDoc(true, store.SignedDocRow{Version: next.Body.Version, Body: next.Raw, Signer: keys.ByOwner,
			KeyFP: owner.FP, Sig: sig, Widens: widens, MadeBy: madeBy, CreatedAt: tx.NowMS()}); err != nil {
			return err
		}
		if err := s.auditMatrix(tx, madeBy, matrixEvent(cur.Signed.Body, next.Body), next.Body, keys.ByOwner, widens); err != nil {
			return err
		}
		rec = &MatrixRecord{Signed: next, Widens: widens, MadeBy: madeBy, CreatedAt: tx.NowMS()}
		return nil
	})
	s.notifyMatrix(rec, err)
	return rec, err
}

// MatrixEdit is api.yaml MatrixEdit: changes applied to the current version. Deny entries
// are [from, to] account uuids, either side "*" for every account.
type MatrixEdit struct {
	BaseVersion *int       `json:"base_version"`
	Allow       [][]string `json:"allow"`
	Deny        [][]string `json:"deny"`
	Paused      *bool      `json:"paused"`
	SameAccount *bool      `json:"same_account"`
	MaxTurns    *int       `json:"max_turns"`
}

// Apply builds the next body from cur.
func (e MatrixEdit) Apply(cur keys.MatrixBody, now time.Time) (keys.MatrixBody, error) {
	next := cur
	next.Version = cur.Version + 1
	next.IssuedAt = keys.FormatTime(now)
	next.Allow = slices.Clone(cur.Allow)
	for _, p := range e.Allow {
		if len(p) != 2 || p[0] == "" || p[1] == "" || p[0] == "*" || p[1] == "*" {
			return next, badRequest("allow entries are [from, to] account uuids")
		}
		if p[0] == p[1] {
			return next, badRequest("a pair of one account is governed by same_account")
		}
		next.Allow = append(next.Allow, p)
	}
	for _, d := range e.Deny {
		if len(d) != 2 || d[0] == "" || d[1] == "" || (d[0] == "*" && d[1] == "*") {
			return next, badRequest("deny entries are [from, to] account uuids, either side may be *")
		}
		next.Allow = slices.DeleteFunc(next.Allow, func(p []string) bool {
			return (d[0] == "*" || d[0] == p[0]) && (d[1] == "*" || d[1] == p[1])
		})
	}
	next.Allow = keys.SortAllow(next.Allow)
	if next.Allow == nil {
		next.Allow = [][]string{}
	}
	if e.Paused != nil {
		next.Paused = *e.Paused
	}
	if e.SameAccount != nil {
		next.SameAccount = *e.SameAccount
	}
	if e.MaxTurns != nil {
		next.MaxTurns = *e.MaxTurns
	}
	if err := next.Check(); err != nil {
		return next, badRequest("%v", err)
	}
	return next, nil
}

func matrixEvent(old, next keys.MatrixBody) string {
	same := slices.EqualFunc(old.Allow, next.Allow, slices.Equal[[]string]) && old.SameAccount == next.SameAccount && old.MaxTurns == next.MaxTurns
	switch {
	case same && !old.Paused && next.Paused:
		return "pause"
	case same && old.Paused && !next.Paused:
		return "resume"
	}
	return "matrix"
}

// EditMatrix applies an edit and signs the result with the hub key (peers.md 5.4). An edit
// that widens needs an owner session (keys.HubMaySignMatrix: ErrHubWidening is 403).
func (s *Service) EditMatrix(ctx context.Context, e MatrixEdit, madeBy string, ownerSession bool) (*MatrixRecord, error) {
	var rec *MatrixRecord
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		cur, err := s.currentTx(tx, "hub")
		if err != nil {
			return err
		}
		if e.BaseVersion != nil && *e.BaseVersion != cur.Signed.Body.Version {
			return conflict("the matrix is at version %d, not %d", cur.Signed.Body.Version, *e.BaseVersion)
		}
		maxV, err := tx.MaxSignedDocVersion(true)
		if err != nil {
			return err
		}
		next, err := e.Apply(cur.Signed.Body, time.UnixMilli(tx.NowMS()))
		if err != nil {
			return err
		}
		if err := keys.HubMaySignMatrix(cur.Signed.Body, next, ownerSession); err != nil {
			if errors.Is(err, keys.ErrHubWidening) {
				return forbidden("%v", err)
			}
			return conflict("%v", err)
		}
		next.Version = max(next.Version, maxV+1) // above a stored row that did not verify
		rec, err = s.storeHubMatrix(tx, next, madeBy, keys.Widens(cur.Signed.Body, next), matrixEvent(cur.Signed.Body, next))
		return err
	})
	s.notifyMatrix(rec, err)
	return rec, err
}

func (s *Service) notifyMatrix(rec *MatrixRecord, err error) {
	if err == nil && rec != nil && s.OnMatrix != nil {
		s.OnMatrix(rec.Signed.Doc)
	}
}
