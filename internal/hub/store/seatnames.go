package store

import (
	"database/sql"
	"strings"
)

// SeatIdent is what a seat's display name is made from.
type SeatIdent struct {
	ID, Label, Email string
}

// seatBase is the owner's label, else the part of the email before the @, else the email,
// else the seat id.
func seatBase(s SeatIdent) string {
	if l := strings.TrimSpace(s.Label); l != "" {
		return l
	}
	if at := strings.IndexByte(s.Email, '@'); at > 0 {
		return s.Email[:at]
	}
	if s.Email != "" {
		return s.Email
	}
	return s.ID
}

// seatDomain is the first part of the email's domain ("acme" of alex@acme.dev).
func seatDomain(email string) string {
	at := strings.IndexByte(email, '@')
	if at < 0 {
		return ""
	}
	d := email[at+1:]
	if i := strings.IndexByte(d, '.'); i >= 0 {
		d = d[:i]
	}
	return d
}

// squashName is how a reader compares names: "you" and "you" look alike.
func squashName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SeatDisplayNames is the board's account name for each seat (webui components/seat.js
// displayNames, push.md 5.5): the label, else the email's local part; when two seats would
// read alike, each gets its domain too ("alex · gmail").
func SeatDisplayNames(seats []SeatIdent) map[string]string {
	groups := map[string]int{}
	for _, s := range seats {
		groups[squashName(seatBase(s))]++
	}
	out := make(map[string]string, len(seats))
	for _, s := range seats {
		name := seatBase(s)
		if dom := seatDomain(s.Email); groups[squashName(name)] > 1 && dom != "" {
			name += " · " + dom
		}
		out[s.ID] = name
	}
	return out
}

// alertSeatNames is SeatDisplayNames over the board's seats (the shown ones) plus the
// extra seat ids asked for, so a hidden seat still gets a name.
func (tx *Tx) alertSeatNames(extra ...string) (map[string]string, error) {
	rows, err := tx.QueryContext(tx.ctx, `SELECT se.id, se.label, a.email, `+seatHiddenSQL+` FROM seat se
		LEFT JOIN account a ON a.uuid = se.account_uuid ORDER BY se.sort_order, se.created_at, se.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := map[string]bool{}
	for _, id := range extra {
		want[id] = true
	}
	var seats []SeatIdent
	for rows.Next() {
		var s SeatIdent
		var label, email sql.NullString
		var hidden int
		if err := rows.Scan(&s.ID, &label, &email, &hidden); err != nil {
			return nil, err
		}
		if hidden == 1 && !want[s.ID] {
			continue
		}
		s.Label, s.Email = label.String, email.String
		seats = append(seats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return SeatDisplayNames(seats), nil
}
