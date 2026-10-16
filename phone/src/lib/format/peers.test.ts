import { blockAllEdit, blockedAll, detailText, turnsLabel, NO_LIMIT_HELP } from './peers';

describe('Block all (the phone only narrows)', () => {
  it('denies every pair and turns same account off', () => {
    expect(blockAllEdit([['a', 'b'], ['b', 'a'], ['a', 'c']], true, 7)).toEqual({
      base_version: 7,
      deny: [['a', 'b'], ['b', 'a'], ['a', 'c']],
      same_account: false,
    });
  });

  it('leaves same account alone when it is already off, and omits an empty deny list', () => {
    expect(blockAllEdit([['a', 'b']], false, 2)).toEqual({ base_version: 2, deny: [['a', 'b']] });
    expect(blockAllEdit([], true, 2)).toEqual({ base_version: 2, same_account: false });
  });

  it('knows when there is nothing left to block', () => {
    expect(blockedAll([], false)).toBe(true);
    expect(blockedAll([], true)).toBe(false);
    expect(blockedAll([['a', 'b']], false)).toBe(false);
  });
});

describe('the turn limit (read-only on the phone)', () => {
  it('shows 0 as No limit and a number as itself', () => {
    expect(turnsLabel(0)).toBe('No limit');
    expect(turnsLabel(30)).toBe('30');
  });

  it('says no turn limit in the audit detail', () => {
    expect(detailText({ max_turns: 0 })).toBe('no turn limit');
    expect(detailText({ max_turns: 40 })).toBe('40 turns');
  });

  it('explains no limit with the sentence the board shows', () => {
    expect(NO_LIMIT_HELP).toBe('No limit lets agents keep talking with no one watching; the other limits still apply.');
  });
});
