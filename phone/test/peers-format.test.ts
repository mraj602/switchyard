// The Peers helpers (src/lib/format/peers.ts), on the same cases as webui/static/peers-fmt.test.cjs.
import { auditWho, cellState, detailText, eventLabel, messageWho, narrowings, outcomeLabel, peerAccounts, sizeLabel } from '../src/lib/format';

const A = 'acct-a';
const B = 'acct-b';
const C = 'acct-c';

describe('matrix cells', () => {
  it('none, one way, back and both', () => {
    expect(cellState([], A, B)).toBe('none');
    expect(cellState([[A, B]], A, B)).toBe('out');
    expect(cellState([[A, B]], B, A)).toBe('in');
    expect(cellState([[A, B], [B, A]], A, B)).toBe('both');
    expect(cellState([[A, C]], A, B)).toBe('none');
  });

  it('the phone can only narrow: deny entries, never an allow', () => {
    const both: [string, string][] = [[A, B], [B, A]];
    expect(narrowings(both, A, B, 7)).toEqual([
      { target: 'out', edit: { base_version: 7, deny: [[B, A]] } },
      { target: 'in', edit: { base_version: 7, deny: [[A, B]] } },
      { target: 'none', edit: { base_version: 7, deny: [[A, B], [B, A]] } },
    ]);
    expect(narrowings([[A, B]], A, B, 2)).toEqual([{ target: 'none', edit: { base_version: 2, deny: [[A, B]] } }]);
    expect(narrowings([[A, B]], B, A, 2)).toEqual([{ target: 'none', edit: { base_version: 2, deny: [[A, B]] } }]);
    expect(narrowings([], A, B, 2)).toEqual([]);
    for (const n of narrowings(both, A, B, 1)) expect(n.edit).not.toHaveProperty('allow');
  });
});

describe('accounts', () => {
  it('visible seats first, then accounts only the matrix names', () => {
    const seats = [
      { account: A, label: 'work' },
      { account: B, label: 'home', hidden: true },
      { account: C, email: 'c@x' },
      { account: A, label: 'dup' },
    ];
    const m = { accounts: { 'acct-z': 'old' }, allow: [[A, 'acct-z']] as [string, string][] };
    expect(peerAccounts(seats, m).map((a) => [a.account, a.label])).toEqual([[A, 'work'], [C, 'c@x'], ['acct-z', 'old']]);
  });
});

describe('audit sentences', () => {
  it('show metadata only', () => {
    expect(eventLabel('pause')).toBe('All peers paused');
    expect(eventLabel('some_new_event')).toBe('some new event');
    expect(detailText({ version: 3, widens: true, paused: false, pairs: 1, text: 'secret', nested: { a: 1 } })).toBe('v3 · widened · not paused · 1 pair');
    expect(detailText({ size: 2400, status: 'refused' })).toBe('2.4 KB · refused');
    expect(detailText(null)).toBe('');
    expect(auditWho({ actor: 'phone', detail: {} })).toBe('phone');
    expect(auditWho({ actor: 'hub', detail: { from: 'a', to: 'b' } }, (x) => x.toUpperCase())).toBe('A → B');
    expect(messageWho({ from: { account: A }, to: {} })).toBe('acct-a → ?');
    expect(outcomeLabel('target_gone')).toBe('target gone');
    expect(sizeLabel(12)).toBe('12 B');
    expect(sizeLabel(34000)).toBe('34 KB');
  });
});
