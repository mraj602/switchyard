import type { Seat } from '../../contract/api';
import { orderSeats, seatIndex } from './seat';

const seat = (id: string, order?: number): Seat => ({ id, account: `a-${id}`, org: 'o', order }) as Seat;

describe('orderSeats (the owner\'s order of the accounts)', () => {
  it('sorts by order, ties and seats without one in the order given', () => {
    const list = [seat('c', 2), seat('none1'), seat('a', 0), seat('tie1', 1), seat('none2'), seat('tie2', 1)];
    expect(orderSeats(list).map((s) => s.id)).toEqual(['a', 'tie1', 'tie2', 'c', 'none1', 'none2']);
  });

  it('returns the same array when it is already in order, and does not change its input', () => {
    const ordered = [seat('a', 0), seat('b', 1)];
    expect(orderSeats(ordered)).toBe(ordered);
    const input = [seat('b', 1), seat('a', 0)];
    const out = orderSeats(input);
    expect(out).not.toBe(input);
    expect(input.map((s) => s.id)).toEqual(['b', 'a']);
  });

  it('follows a reorder from the hub (a new order renumbers 0..n-1)', () => {
    const before = [seat('a', 0), seat('b', 1), seat('c', 2)];
    const after = [seat('a', 2), seat('b', 0), seat('c', 1)];
    expect(orderSeats(before).map((s) => s.id)).toEqual(['a', 'b', 'c']);
    expect(orderSeats(after).map((s) => s.id)).toEqual(['b', 'c', 'a']);
  });

  it('handles an empty list', () => {
    expect(orderSeats([])).toEqual([]);
  });
});

describe('seatIndex', () => {
  it('assigns fallback colours by position in the hub order', () => {
    const idx = seatIndex([seat('b', 1), seat('a', 0)]);
    expect(idx.get('a')?.colour).toBe('#0E7C76');
    expect(idx.get('b')?.colour).toBe('#6A4FD6');
  });
});
