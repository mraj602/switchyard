// The Analytics helpers (src/lib/format/analytics.ts), on the same cases as webui/static/analytics-fmt.test.cjs.
import { barPercents, cacheHit, hitText, shortDay, tokens, tokenSum, usd } from '../src/lib/format';

describe('usd', () => {
  it.each<[number | null | undefined, string]>([
    [0, '$0.00'],
    [4.2, '$4.20'],
    [1234567.891, '$1,234,567.89'],
    [0.0001, '<$0.01'],
    [null, '–'],
    [NaN, '–'],
  ])('%s -> %s', (n, text) => expect(usd(n)).toBe(text));
});

describe('tokens', () => {
  it.each<[number | null, string]>([
    [999, '999'],
    [1500, '1.5k'],
    [2_600_000, '2.6M'],
    [3_000_000_000, '3.0B'],
    [null, '–'],
  ])('%s -> %s', (n, text) => expect(tokens(n)).toBe(text));
});

describe('sums', () => {
  it('adds every counter and gives the cache hit rate', () => {
    const r = { input: 500, output: 100, cache_5m: 0, cache_1h: 0, cache_read: 1500 };
    expect(tokenSum(r)).toBe(2100);
    expect(cacheHit(r)).toBe(0.75);
    expect(cacheHit({ output: 5 })).toBeNull();
  });
});

describe('bars', () => {
  it('the largest is 100, a small value still shows, zero stays 0', () => {
    expect(barPercents([0, 1, 100, 50])).toEqual([0, 2, 100, 50]);
    expect(barPercents([0, 0])).toEqual([0, 0]);
    expect(barPercents([])).toEqual([]);
  });
});

describe('days and hits', () => {
  it('shortDay', () => {
    expect(shortDay('2026-10-04')).toBe('4 Oct');
    expect(shortDay('garbage')).toBe('garbage');
  });

  it('hitText', () => {
    expect(hitText({ type: 'five_hour', wait_s: 7200 })).toBe('5-hour limit, back in 2 h');
    expect(hitText({ type: 'seven_day', wait_s: 3 * 86400 })).toBe('7-day limit, back in 3 d');
    expect(hitText({ type: 'five_hour', wait_s: 10 })).toBe('5-hour limit, back in 1 min');
    expect(hitText({ type: 'seven_day', wait_s: null })).toBe('7-day limit');
    expect(hitText({ type: null, wait_s: null })).toBe('limit');
  });
});
