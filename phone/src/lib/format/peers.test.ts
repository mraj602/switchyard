import { blockAllEdit, blockedAll } from './peers';

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
