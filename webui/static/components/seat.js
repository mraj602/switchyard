// Account (seat) colours, names and lookups. The API sends `colour`; when it is missing the
// board assigns teal, violet, gold, blue in seat order.
//
// Names: the owner's label, else the part of the email before the @. When two accounts would
// read alike ("you" and "you"), each gets its domain too ("you · client").
// seatIndex() remembers the board's seats, so seatName() gives every page the same name.

const FALLBACK = ['#0E7C76', '#6A4FD6', '#9A7400', '#2F6FDE'];
const NAMED = { teal: FALLBACK[0], violet: FALLBACK[1], gold: FALLBACK[2], blue: FALLBACK[3] };
export const NEUTRAL = '#8A9098';

// Colours end up in a style attribute, so only hex or known names get through.
export function validColour(c) {
  if (typeof c !== 'string') return null;
  if (/^#[0-9a-fA-F]{3,8}$/.test(c)) return c;
  return NAMED[c.toLowerCase()] || null;
}

const prefixOf = (email) => (email ? String(email).split('@')[0] : '');
const domainOf = (email) => {
  const d = email && String(email).includes('@') ? String(email).split('@')[1] : '';
  return d ? d.split('.')[0] : '';
};
// "you" and "you" look the same to a reader.
const squash = (s) => String(s || '').toLowerCase().replace(/[^a-z0-9]/g, '');

const baseName = (seat) => seat.label || prefixOf(seat.email) || seat.id;

// id -> display name for a list of seats (pure; seatIndex keeps the latest result).
export function displayNames(seats) {
  const out = new Map();
  const groups = new Map();
  for (const s of seats || []) {
    const k = squash(baseName(s));
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(s);
  }
  for (const list of groups.values()) {
    for (const s of list) {
      const name = baseName(s);
      const dom = domainOf(s.email);
      out.set(s.id, list.length > 1 && dom ? `${name} · ${dom}` : name);
    }
  }
  return out;
}

// True when some accounts only differ by their domain (the owner should give them labels).
export function namesCollide(seats) {
  const seen = new Set();
  for (const s of seats || []) {
    const k = squash(baseName(s));
    if (seen.has(k)) return true;
    seen.add(k);
  }
  return false;
}

let names = new Map();

// seats: the overview's seat list (already in display order).
export function seatIndex(seats) {
  names = displayNames(seats);
  const map = new Map();
  seats.forEach((s, i) => map.set(s.id, { seat: s, colour: validColour(s.colour) || FALLBACK[i % FALLBACK.length], order: i }));
  return map;
}

export function seatName(seat) {
  if (!seat) return '';
  return names.get(seat.id) || baseName(seat);
}

// Name of a seat id (or an account email) the board may or may not know.
export function seatNameOf(id, seats) {
  const s = (seats || []).find((x) => x.id === id || x.account === id || x.email === id);
  return s ? seatName(s) : '';
}

// Colour and label for a session's account.
export function sessionSeat(session, index) {
  if (!session.seat) return { colour: NEUTRAL, label: 'Unattributed' };
  const known = index.get(session.seat);
  return {
    colour: validColour(session.seat_colour) || (known && known.colour) || NEUTRAL,
    label: (known && seatName(known.seat)) || session.seat_label || 'Account',
  };
}
