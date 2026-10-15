// The Analytics screen (src/components/screens/AnalyticsScreen.tsx) on a fake hub client.
import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import AnalyticsScreen from '../src/components/screens/AnalyticsScreen';
import type { AnalyticsQuery, AnalyticsReplies, AnalyticsTokens } from '../src/contract/api';
import { fakeClient, fx, makeHarness, routerMock } from './screen-harness';

// eslint-disable-next-line @typescript-eslint/no-require-imports
jest.mock('expo-router', () => require('./screen-harness').routerMock);

const tok = (usd: number, replies: number): AnalyticsTokens => ({
  input: usd * 1000, output: usd * 100, cache_5m: 0, cache_1h: 0, cache_read: usd * 5000, replies, api_usd: usd, unpriced_replies: 0,
});

/** A reply for each report, shaped by the query like the hub does (days, seat). */
function reply(report: keyof AnalyticsReplies, q: AnalyticsQuery = {}) {
  const days = q.days ?? 30;
  const head = { report, from: '2026-10-01', to: '2026-10-15', tz: 'Asia/Kolkata', seat: q.seat ?? null };
  const scale = q.seat ? 0.5 : 1;
  switch (report) {
    case 'daily':
      return {
        ...head,
        totals: { ...tok(10 * scale, 7), unpriced_replies: 2 },
        days: Array.from({ length: days }, (_, i) => ({ ...tok(i % 3, 1), day: new Date(Date.UTC(2026, 9, 1 + i)).toISOString().slice(0, 10), source: 'transcripts' as const, total_tokens: 100 })),
        seats: [],
      };
    case 'models':
      return {
        ...head,
        models: [
          { ...tok(8 * scale, 5), model: 'claude-opus-4-6', label: 'Opus 4.6', priced: true },
          { ...tok(0, 2), model: 'mystery-9', label: 'mystery-9', priced: false },
        ],
      };
    case 'projects':
      return { ...head, projects: [{ ...tok(9 * scale, 6), project: '/p/alpha', name: 'alpha', sessions: 3, last_ts: '2026-10-14T20:00:00.000Z' }] };
    case 'heatmap': {
      const grid = (v: number) => Array.from({ length: 7 }, (_, wd) => Array.from({ length: 24 }, (_, hr) => (wd === 2 && hr === 14 ? v : 0)));
      return { ...head, api_usd: grid(4 * scale), replies: grid(3) };
    }
    case 'sessions':
      return {
        ...head,
        sessions: Array.from({ length: 7 }, (_, i) => ({
          ...tok(7 - i, 2), session_id: `sess-${i}`, seat: 'seat_work', seat_name: 'work', project: `/p/proj${i}`, models: ['claude-opus-4-6'],
          first_ts: '2026-10-13T09:00:00.000Z', last_ts: '2026-10-14T20:00:00.000Z',
        })),
      };
    case 'limits':
      return {
        ...head,
        windows: [
          { seat: 'seat_work', seat_name: 'work', key: 'five_hour', label: '5-hour', started_at: '2026-10-12T05:00:00.000Z', resets_at: '2026-10-12T10:00:00.000Z', peak_pct: 40, final_pct: 35, unused_pct: 65, samples: 4 },
          { seat: 'seat_work', seat_name: 'work', key: 'seven_day', label: '7-day', started_at: null, resets_at: '2026-10-14T10:00:00.000Z', peak_pct: 91.4, final_pct: null, unused_pct: null, samples: 9 },
        ],
      };
    case 'hits':
      return {
        ...head,
        hits: [{ ts: '2026-10-14T10:00:00.000Z', seat: 'seat_work', seat_name: 'work', session_id: 's1', type: 'five_hour', status: 'rejected', resets_at: '2026-10-14T12:00:00.000Z', wait_s: 7200 }],
      };
  }
}

const analytics = () => jest.fn(async (report: keyof AnalyticsReplies, q?: AnalyticsQuery) => reply(report, q));

async function show(client = fakeClient({ analytics: analytics() })) {
  const h = makeHarness({ client });
  await render(<AnalyticsScreen />, { wrapper: h.Wrapper });
  return client;
}

beforeEach(() => routerMock.router.push.mockClear());

describe('Analytics', () => {
  it('asks for 30 days of every report, all accounts, and shows the totals', async () => {
    const client = await show();
    expect(await screen.findByTestId('total-usd')).toHaveTextContent('$10.00');
    expect(client.analytics.mock.calls.map((c) => c[0]).sort()).toEqual(['daily', 'heatmap', 'hits', 'limits', 'models', 'projects', 'sessions']);
    expect(client.analytics.mock.calls.every((c) => c[1]?.days === 30 && c[1]?.seat === undefined)).toBe(true);
    expect(screen.getByText('2 replies not priced')).toBeTruthy();
    expect(screen.getByTestId('total-replies')).toHaveTextContent('7');
    expect(screen.getByTestId('day-bars').props.accessibilityLabel).toMatch(/\$10\.00 in all/);
    expect(screen.getByTestId('day-bars').children).toHaveLength(30);
  });

  it('lists models (with the unpriced one flagged), projects and limit hits', async () => {
    await show();
    expect(await screen.findByText('Opus 4.6')).toBeTruthy();
    expect(screen.getByTestId('model-0')).toHaveTextContent(/\$8\.00/);
    expect(screen.getByTestId('model-1')).toHaveTextContent(/no price/);
    expect(screen.getByTestId('project-0')).toHaveTextContent(/alpha/);
    expect(screen.getByTestId('project-0')).toHaveTextContent(/3 sessions/);
    expect(screen.getByTestId('hit')).toHaveTextContent(/5-hour limit, back in 2 h/);
    expect(screen.getByTestId('hit')).toHaveTextContent(/work/);
  });

  it('draws the weekday by hour heatmap, shading only the busy cell', async () => {
    await show();
    const map = await screen.findByTestId('heatmap');
    expect(map.props.accessibilityLabel).toMatch(/weekday and hour/);
    expect(screen.getByText('Tue')).toBeTruthy();
    expect(screen.getByTestId('heat-2-14')).toBeTruthy();
    expect(screen.queryByTestId('heat-2-15')).toBeNull();
  });

  it('lists the five dearest sessions by project with cost and tokens', async () => {
    await show();
    expect(await screen.findByTestId('session-0')).toHaveTextContent(/proj0/);
    expect(screen.getByTestId('session-0')).toHaveTextContent(/\$7\.00/);
    expect(screen.getByTestId('session-0')).toHaveTextContent(/tokens · work · 14 Oct/);
    expect(screen.getByTestId('session-4')).toHaveTextContent(/proj4/);
    expect(screen.queryByTestId('session-5')).toBeNull();
  });

  it('shows limit windows newest first with peak and final percent', async () => {
    await show();
    const rows = await screen.findAllByTestId('limit-window');
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent(/work · 7-day/);
    expect(rows[0]).toHaveTextContent(/91% peak · – final/);
    expect(rows[1]).toHaveTextContent(/work · 5-hour/);
    expect(rows[1]).toHaveTextContent(/40% peak · 35% final/);
  });

  it('a range chip asks again for that many days', async () => {
    const client = await show();
    await screen.findByTestId('total-usd');
    await fireEvent.press(screen.getByTestId('range-7'));
    await waitFor(() => expect(client.analytics).toHaveBeenCalledWith('daily', { days: 7, seat: undefined }));
    await waitFor(() => expect(screen.getByTestId('day-bars').children).toHaveLength(7));
  });

  it('an account chip filters by that seat, Unattributed by "unattributed"', async () => {
    const client = await show();
    await screen.findByTestId('total-usd');
    const seat = fx.overview.seats.find((s) => !s.hidden)!;
    await fireEvent.press(screen.getByTestId(`seat-${seat.id}`));
    await waitFor(() => expect(client.analytics).toHaveBeenCalledWith('models', { days: 30, seat: seat.id }));
    await waitFor(() => expect(screen.getByTestId('total-usd')).toHaveTextContent('$5.00'));
    await fireEvent.press(screen.getByTestId('seat-unattributed'));
    await waitFor(() => expect(client.analytics).toHaveBeenCalledWith('daily', { days: 30, seat: 'unattributed' }));
    await fireEvent.press(screen.getByTestId('seat-all'));
    await waitFor(() => expect(screen.getByTestId('total-usd')).toHaveTextContent('$10.00'));
  });

  it('empty ranges say so instead of drawing empty lists', async () => {
    const empty = jest.fn(async (report: keyof AnalyticsReplies, q?: AnalyticsQuery) => {
      const r = reply(report, q) as Record<string, unknown>;
      return { ...r, ...(report === 'models' ? { models: [] } : report === 'projects' ? { projects: [] } : report === 'hits' ? { hits: [] } : report === 'sessions' ? { sessions: [] } : report === 'limits' ? { windows: [] } : {}) };
    });
    await show(fakeClient({ analytics: empty }));
    expect(await screen.findByText('No limit hits in this range.')).toBeTruthy();
    expect(screen.getAllByText('No token data in this range.')).toHaveLength(3);
    expect(screen.getByText('No limit windows in this range.')).toBeTruthy();
  });

  it('shows the error when the hub fails', async () => {
    await show(fakeClient({ analytics: jest.fn(async () => { throw new Error('Hub answered 503'); }) }));
    expect(await screen.findByText('Hub answered 503')).toBeTruthy();
  });
});
