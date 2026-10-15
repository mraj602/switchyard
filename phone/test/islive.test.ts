// The phone's open rule matches the board's isLive and the hub's `dormant` (states.md 5).
import { isLive, IDLE_MS } from '../src/lib/format';

const now = Date.parse('2026-10-15T18:00:00Z');
const ago = (ms: number) => new Date(now - ms).toISOString();

describe('isLive', () => {
  it.each([
    ['ended', { state: 'ended' as const }, false],
    ['gone', { state: 'gone' as const }, false],
    ['needs you, even if dormant', { state: 'asked' as const, needs_you: true, dormant: true }, true],
    ['hub says dormant', { state: 'done' as const, dormant: true }, false],
    ['hub says not dormant', { state: 'done' as const, dormant: false, activity_at: ago(30 * 864e5) }, true],
    ['working', { state: 'working' as const }, true],
    ['stale working', { state: 'working' as const, stale: true, activity_at: ago(13 * 36e5) }, false],
    ['done recently', { state: 'done' as const, activity_at: ago(36e5) }, true],
    ['done past the idle mark', { state: 'done' as const, activity_at: ago(IDLE_MS + 1) }, false],
    ['done, falls back to state_since', { state: 'done' as const, state_since: ago(2 * 864e5) }, false],
    ['done, no times known', { state: 'done' as const }, true],
  ])('%s', (_n, s, want) => {
    expect(isLive(s, now)).toBe(want);
  });
});
