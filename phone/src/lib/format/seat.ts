// Account (seat) colours and lookups (webui components/seat.js).
import type { Seat, Session } from '../../contract/api';
import { humanKey } from './core';

const FALLBACK = ['#0E7C76', '#6A4FD6', '#9A7400', '#2F6FDE'];
const NAMED: Record<string, string> = { teal: FALLBACK[0]!, violet: FALLBACK[1]!, gold: FALLBACK[2]!, blue: FALLBACK[3]! };
export const NEUTRAL = '#8A9098';

/** Only hex or a known name gets through. */
export function validColour(c: unknown): string | null {
  if (typeof c !== 'string') return null;
  if (/^#[0-9a-fA-F]{3,8}$/.test(c)) return c;
  return NAMED[c.toLowerCase()] ?? null;
}

export type SeatIndex = Map<string, { seat: Seat; colour: string }>;

export function seatIndex(seats: Seat[]): SeatIndex {
  const map: SeatIndex = new Map();
  seats.forEach((s, i) => map.set(s.id, { seat: s, colour: validColour(s.colour) ?? FALLBACK[i % FALLBACK.length]! }));
  return map;
}

const PLANS: Record<string, string> = {
  max_20x: 'Max 20x', max_5x: 'Max 5x', max: 'Max', pro: 'Pro', team: 'Team', enterprise: 'Enterprise', free: 'Free',
};

/** The plan as people say it ("Max 20x"), never the raw key ("max_20x"); an unknown key is humanised. */
export function planLabel(plan: string | null | undefined): string {
  if (!plan) return '';
  return PLANS[plan.toLowerCase()] ?? humanKey(plan);
}

export function seatName(seat: Pick<Seat, 'label' | 'email' | 'id'>): string {
  return seat.label || (seat.email ? seat.email.split('@')[0] : '') || seat.id;
}

export function sessionSeat(s: Session, index: SeatIndex): { colour: string; label: string } {
  if (!s.seat) return { colour: NEUTRAL, label: 'Unattributed' };
  const known = index.get(s.seat);
  return {
    colour: validColour(s.seat_colour) ?? known?.colour ?? NEUTRAL,
    label: s.seat_label || (known && seatName(known.seat)) || 'Account',
  };
}
