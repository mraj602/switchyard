// Account (seat) colours and lookups. The API sends `colour`; when it is missing the
// board assigns teal, violet, gold, blue in seat order.

const FALLBACK = ['#0E7C76', '#6A4FD6', '#9A7400', '#2F6FDE'];
const NAMED = { teal: FALLBACK[0], violet: FALLBACK[1], gold: FALLBACK[2], blue: FALLBACK[3] };
export const NEUTRAL = '#8A9098';

// Colours end up in a style attribute, so only hex or known names get through.
export function validColour(c) {
  if (typeof c !== 'string') return null;
  if (/^#[0-9a-fA-F]{3,8}$/.test(c)) return c;
  return NAMED[c.toLowerCase()] || null;
}

// seats: the overview's seat list (already in display order).
export function seatIndex(seats) {
  const map = new Map();
  seats.forEach((s, i) => map.set(s.id, { seat: s, colour: validColour(s.colour) || FALLBACK[i % FALLBACK.length] }));
  return map;
}

export function seatName(seat) {
  return seat.label || (seat.email ? seat.email.split('@')[0] : '') || seat.id;
}

// Colour and label for a session's account.
export function sessionSeat(session, index) {
  if (!session.seat) return { colour: NEUTRAL, label: 'Unattributed' };
  const known = index.get(session.seat);
  return {
    colour: validColour(session.seat_colour) || (known && known.colour) || NEUTRAL,
    label: session.seat_label || (known && seatName(known.seat)) || 'Account',
  };
}
