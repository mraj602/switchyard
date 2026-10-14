package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

// seat.look_src values: who set a seat's label and colour (desktop.md C10, schema v5).
const (
	LookUI     = "ui"     // PATCH /v1/accounts/{seat}: always wins
	LookSlot   = "slot"   // a slot_info label: applies only while look_src is NULL or 'slot'
	LookImport = "import" // the prototype importer
)

var (
	seatLabelRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,23}$`)
	seatColourRE = regexp.MustCompile(`^#[0-9a-f]{6}$`)
)

// ValidSeatLabel and ValidSeatColour are the rules of setup.md 3 (and api.yaml SeatPatch).
func ValidSeatLabel(s string) bool  { return seatLabelRE.MatchString(s) }
func ValidSeatColour(s string) bool { return seatColourRE.MatchString(s) }

// ErrBadSeatPatch is returned (wrapped) for an invalid SeatPatch.
var ErrBadSeatPatch = errors.New("invalid seat patch")

// SeatPatch is api.yaml SeatPatch. LabelSet and ColourSet are true when the key was
// present; a nil value with the flag set clears label or colour.
type SeatPatch struct {
	LabelSet  bool
	Label     *string
	ColourSet bool
	Colour    *string
	Order     *int64
	Hidden    *bool
}

// Validate applies the label and colour rules. It needs at least one key.
func (p SeatPatch) Validate() error {
	if !p.LabelSet && !p.ColourSet && p.Order == nil && p.Hidden == nil {
		return fmt.Errorf("%w: no field to change", ErrBadSeatPatch)
	}
	if p.Label != nil && !ValidSeatLabel(*p.Label) {
		return fmt.Errorf("%w: label must match ^[a-z0-9][a-z0-9-]{0,23}$", ErrBadSeatPatch)
	}
	if p.Colour != nil && !ValidSeatColour(*p.Colour) {
		return fmt.Errorf("%w: colour must be #rrggbb in lower case", ErrBadSeatPatch)
	}
	if p.Order != nil && (*p.Order < 0 || *p.Order > 1_000_000) {
		return fmt.Errorf("%w: order out of range", ErrBadSeatPatch)
	}
	return nil
}

// PatchSeat applies p to the seat; touching label or colour sets look_src to 'ui'. It
// returns ErrNotFound for an unknown seat.
func (tx *Tx) PatchSeat(id string, p SeatPatch) error {
	if err := p.Validate(); err != nil {
		return err
	}
	var one int
	if err := tx.queryRow(`SELECT 1 FROM seat WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if p.LabelSet {
		if _, err := tx.exec(`UPDATE seat SET label = ?, look_src = 'ui' WHERE id = ?`, p.Label, id); err != nil {
			return err
		}
	}
	if p.ColourSet {
		if _, err := tx.exec(`UPDATE seat SET colour = ?, look_src = 'ui' WHERE id = ?`, p.Colour, id); err != nil {
			return err
		}
	}
	if p.Order != nil {
		if _, err := tx.exec(`UPDATE seat SET sort_order = ? WHERE id = ?`, *p.Order, id); err != nil {
			return err
		}
	}
	if p.Hidden != nil {
		h := 0
		if *p.Hidden {
			h = 1
		}
		if _, err := tx.exec(`UPDATE seat SET hidden = ? WHERE id = ?`, h, id); err != nil {
			return err
		}
	}
	return nil
}

// SeatSlotLook applies a slot_info label and colour (record.md 4.7) only while the seat's
// look_src is NULL or 'slot', and then sets look_src to 'slot'. An invalid or empty value is
// left alone. It reports whether anything was applied.
func (tx *Tx) SeatSlotLook(seatID, label, colour string) (bool, error) {
	if !ValidSeatLabel(label) {
		label = ""
	}
	if !ValidSeatColour(colour) {
		colour = ""
	}
	if label == "" && colour == "" {
		return false, nil
	}
	res, err := tx.exec(`UPDATE seat SET label = coalesce(?, label), colour = coalesce(?, colour), look_src = 'slot'
		WHERE id = ? AND (look_src IS NULL OR look_src = 'slot')`, nullStr(label), nullStr(colour), seatID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SeatLookImport is the prototype importer's SeatLook: it sets look_src to 'import', but
// leaves the label and colour of a seat set in the UI alone (the order is still applied).
func (tx *Tx) SeatLookImport(seatID, label, colour string, order *int64) error {
	_, err := tx.exec(`UPDATE seat SET
		label = CASE WHEN look_src = 'ui' THEN label ELSE coalesce(?, label) END,
		colour = CASE WHEN look_src = 'ui' THEN colour ELSE coalesce(?, colour) END,
		sort_order = coalesce(?, sort_order),
		look_src = CASE WHEN look_src = 'ui' OR (? = 0 AND ? = 0) THEN look_src ELSE 'import' END
		WHERE id = ?`, nullStr(label), nullStr(colour), order, boolInt(label != ""), boolInt(colour != ""), seatID)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
