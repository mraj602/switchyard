// Screen tests on the devserver fixtures (phases/8.md WP-S accept list). The hub API is a fake
// (test/screen-harness.tsx); fetch stays off.
import { Alert as RNAlert, Linking } from 'react-native';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import AccountsScreen from '../src/components/screens/AccountsScreen';
import AlertRedirectScreen from '../src/components/screens/AlertRedirectScreen';
import AlertsScreen from '../src/components/screens/AlertsScreen';
import MachinesScreen from '../src/components/screens/MachinesScreen';
import MoreScreen from '../src/components/screens/MoreScreen';
import NowScreen from '../src/components/screens/NowScreen';
import SessionScreen from '../src/components/screens/SessionScreen';
import { swipeAction } from '../src/components/needs-card';
import SessionsScreen from '../src/components/screens/SessionsScreen';
import SettingsScreen from '../src/components/screens/SettingsScreen';
import { clock, mostRoom } from '../src/lib/format';
import type { Device } from '../src/contract/api';
import { NOW, fakeClient, freshData, fx, makeHarness, routerMock } from './screen-harness';

// eslint-disable-next-line @typescript-eslint/no-require-imports
jest.mock('expo-router', () => require('./screen-harness').routerMock);
jest.mock('expo-clipboard', () => ({ setStringAsync: jest.fn(async () => true) }));
jest.mock('../src/lib/links', () => ({
  hashToRoute: jest.fn((u: string) => (u.startsWith('/#/sessions/') ? { name: 'session', id: 'x' } : { name: 'now' })),
  routeToHref: jest.fn((r: { name: string; id?: string }) => (r.name === 'session' ? `/sessions/${r.id}` : '/')),
}));

const Clipboard = jest.requireMock('expo-clipboard') as { setStringAsync: jest.Mock };

const ID3 = 'c41e0000-0000-4000-8000-000000000003'; // asked, alert pending, Remote Control link
const ID4 = 'c41e0000-0000-4000-8000-000000000004'; // needs_permission, alert fired
const ID5 = 'c41e0000-0000-4000-8000-000000000005'; // failed (rate limit), alert seen

async function show(ui: React.ReactElement, setup: Parameters<typeof makeHarness>[0] = {}) {
  const h = makeHarness(setup);
  await render(ui, { wrapper: h.Wrapper });
  return h.probe;
}

beforeEach(() => {
  routerMock.router.push.mockClear();
  routerMock.router.replace.mockClear();
  Clipboard.setStringAsync.mockClear();
});

describe('Now', () => {
  it('lists the needs-you rows in fixture order', async () => {
    await show(<NowScreen />);
    const ids = screen.getAllByTestId(/^needs-/).map((n) => String(n.props.testID).slice('needs-'.length));
    expect(ids).toEqual(fx.overview.needs_you.map((s) => s.id));
    expect(screen.getByText('Needs you · 3')).toBeTruthy();
    // A ring per visible account, machines with their state.
    expect(screen.getAllByLabelText(/of the 5-hour window|no data/).length).toBe(fx.overview.seats.filter((s) => !s.hidden).length);
    expect(screen.getByText('laptop')).toBeTruthy();
    expect(screen.getByText('9 sessions running')).toBeTruthy();
  });

  it('Seen calls the hub and dims the row', async () => {
    const p = await show(<NowScreen />);
    expect(screen.getByTestId(`needs-${ID3}`).props.style.opacity).toBe(1);
    await fireEvent.press(screen.getByTestId(`seen-${ID3}`));
    expect(p.client.seen).toHaveBeenCalledWith('al_pending');
    expect(p.dispatched).toContainEqual({ type: 'seen', alert: 'al_pending' });
    await waitFor(() => expect(screen.getByTestId(`needs-${ID3}`).props.style.opacity).toBe(0.55));
    expect(p.api.kick).toHaveBeenCalled();
  });

  it('Snooze calls the hub with the hub minutes and marks the alert snoozed', async () => {
    const p = await show(<NowScreen />);
    await fireEvent.press(screen.getByTestId(`snooze-${ID4}`));
    expect(p.client.snooze).toHaveBeenCalledWith('al_fired', 15);
    const d = p.dispatched.find((a) => a.type === 'snoozed');
    expect(d).toMatchObject({ type: 'snoozed', alert: 'al_fired' });
    expect(Date.parse((d as { until: string }).until)).toBe(NOW + 15 * 60e3);
    expect(await screen.findByText(/snoozed until/)).toBeTruthy();
  });

  it('a seen alert has no buttons', async () => {
    await show(<NowScreen />);
    expect(screen.queryByTestId(`seen-${ID5}`)).toBeNull();
  });

  it('a 409 toasts and still refreshes', async () => {
    const client = fakeClient({ seen: jest.fn(async () => { throw Object.assign(new Error('conflict'), { status: 409 }); }) });
    const p = await show(<NowScreen />, { client });
    await fireEvent.press(screen.getByTestId(`seen-${ID3}`));
    expect(await screen.findByText('That alert already changed')).toBeTruthy();
    expect(p.api.kick).toHaveBeenCalled();
  });

  it('shows the cache under an offline banner and disables Seen and Snooze', async () => {
    const p = await show(<NowScreen />, { data: { offline: true, fetchedAt: NOW - 120e3 } });
    expect(screen.getByText(`Offline · showing ${clock(new Date(NOW - 120e3).toISOString(), NOW)}`)).toBeTruthy();
    expect(screen.getByText('Needs you · 3')).toBeTruthy();
    expect(screen.getByTestId(`seen-${ID3}`).props.accessibilityState.disabled).toBe(true);
    await fireEvent.press(screen.getByTestId(`seen-${ID3}`));
    expect(p.client.seen).not.toHaveBeenCalled();
  });

  it('Open in Claude and Copy resume command', async () => {
    const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    await show(<NowScreen />);
    await fireEvent.press(screen.getByTestId(`open-${ID3}`));
    expect(open).toHaveBeenCalledWith('https://claude.ai/code/session_demo');
    expect(screen.queryByTestId(`open-${ID4}`)).toBeNull(); // no remote_url
    await fireEvent.press(screen.getByTestId(`copy-${ID3}`));
    expect(Clipboard.setStringAsync).toHaveBeenCalledWith(`claude-2 --resume ${ID3}`);
    expect(await screen.findByText('Copied')).toBeTruthy();
    open.mockRestore();
  });

  it('swipe right is Seen, left is Snooze, a short drag is nothing', () => {
    expect(swipeAction(120)).toBe('seen');
    expect(swipeAction(88)).toBe('seen');
    expect(swipeAction(-100)).toBe('snooze');
    expect(swipeAction(87)).toBeNull();
    expect(swipeAction(-20)).toBeNull();
  });

  it('opens a session when a row is tapped', async () => {
    await show(<NowScreen />);
    await fireEvent.press(screen.getByLabelText(/^Asked a question: api/));
    expect(routerMock.router.push).toHaveBeenCalledWith(`/sessions/${ID3}?alert=al_pending`);
  });
});

describe('Sessions', () => {
  const rows = () => screen.queryAllByLabelText(/^Session /);

  it('shows live sessions, grouped by account, and filters', async () => {
    await show(<SessionsScreen />);
    expect(rows().length).toBe(fx.sessions.sessions.filter((s) => s.state !== 'ended' && s.state !== 'gone').length);
    await fireEvent.press(screen.getByTestId('filter-needs_you'));
    expect(rows().length).toBe(3);
    await fireEvent.press(screen.getByTestId('filter-working'));
    expect(rows().length).toBe(fx.sessions.sessions.filter((s) => s.state === 'working').length);
    await fireEvent.press(screen.getByTestId('filter-done'));
    expect(rows().length).toBe(fx.sessions.sessions.filter((s) => s.state === 'done').length);
    await fireEvent.press(screen.getByTestId('filter-ended'));
    expect(screen.getByText('No ended sessions.')).toBeTruthy();
  });

  it('starts on the filter and grouping of its route params', async () => {
    await show(<SessionsScreen filter="needs_you" by="machine" />);
    expect(rows().length).toBe(3);
    expect(screen.getAllByRole('header').map((h) => h.props.children).length).toBeGreaterThan(1);
  });

  it('filters by machine', async () => {
    await show(<SessionsScreen machine="m_gpu" />);
    const want = fx.sessions.sessions.filter((s) => s.machine === 'm_gpu' && s.state !== 'ended').length;
    expect(rows().length).toBe(want);
  });

  it('opens a session', async () => {
    await show(<SessionsScreen filter="needs_you" />);
    await fireEvent.press(rows()[0]!);
    expect(routerMock.router.push).toHaveBeenCalledWith(expect.stringMatching(/^\/sessions\/c41e/));
  });
});

describe('Session', () => {
  it('shows the question, the history and the links; Seen works from here', async () => {
    const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    const p = await show(<SessionScreen id={ID3} alert="al_pending" />);
    expect(await screen.findByText('Waiting on you')).toBeTruthy();
    expect(screen.getAllByText(/Should I also update the migration/).length).toBeGreaterThan(0);
    expect(screen.getByText('State history')).toBeTruthy();
    expect(screen.getByText(/signed in to one account at a time/)).toBeTruthy();
    await fireEvent.press(screen.getByTestId(`open-${ID3}`));
    expect(open).toHaveBeenCalledWith('https://claude.ai/code/session_demo');
    await fireEvent.press(screen.getByTestId(`copy-${ID3}`));
    expect(Clipboard.setStringAsync).toHaveBeenCalledWith(`claude-2 --resume ${ID3}`);
    await fireEvent.press(screen.getByTestId(`seen-${ID3}`));
    expect(p.client.seen).toHaveBeenCalledWith('al_pending');
    open.mockRestore();
  });

  it('never opens a link that is not https', async () => {
    const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    const detail = { ...fx.session, remote_url: 'javascript:alert(1)' };
    await show(<SessionScreen id={ID3} />, { client: fakeClient({ session: jest.fn(async () => detail) }) });
    await screen.findByText('State history');
    expect(screen.queryByTestId(`open-${ID3}`)).toBeNull();
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it('says so when the session is gone', async () => {
    const client = fakeClient({ session: jest.fn(async () => { throw Object.assign(new Error('nf'), { status: 404 }); }) });
    await show(<SessionScreen id="nope" />, { client });
    expect(await screen.findByText('That session no longer exists.')).toBeTruthy();
  });
});

describe('Accounts', () => {
  it('names the account with the most room', async () => {
    await show(<AccountsScreen />);
    const best = mostRoom(fx.accounts.accounts as never, NOW);
    expect(best).not.toBeNull();
    expect(screen.getByText(new RegExp(`^Most room right now: ${best!.seat.label} \\(\\d+% of 5h`))).toBeTruthy();
  });

  it('shows one card per visible account with its windows', async () => {
    await show(<AccountsScreen seat="seat_work" />);
    for (const s of fx.accounts.accounts.filter((x) => !x.hidden)) expect(screen.getByTestId(`seat-${s.id}`)).toBeTruthy();
    expect(screen.getAllByText('5-hour').length).toBeGreaterThan(0);
    expect(screen.getAllByText(/^resets in /).length).toBeGreaterThan(0);
  });

  it('offers the Lock Screen countdown only when the app core says a card qualifies', async () => {
    const start = jest.fn(async () => undefined);
    await show(<AccountsScreen />, { api: { countdown: { can: (id) => id === 'seat_work', start } } });
    expect(screen.getAllByText('Count down on Lock Screen')).toHaveLength(1);
    await fireEvent.press(screen.getByTestId('countdown-seat_work'));
    expect(start).toHaveBeenCalledWith('seat_work');
  });

  it('has no countdown button by default', async () => {
    await show(<AccountsScreen />);
    expect(screen.queryByText('Count down on Lock Screen')).toBeNull();
  });
});

describe('More, Machines', () => {
  it('More links to Alerts, Machines and Settings', async () => {
    await show(<MoreScreen />);
    await fireEvent.press(screen.getByTestId('more-alerts'));
    await fireEvent.press(screen.getByTestId('more-machines'));
    await fireEvent.press(screen.getByTestId('more-settings'));
    expect(routerMock.router.push.mock.calls.map((c) => c[0])).toEqual(['/more/alerts', '/more/machines', '/more/settings']);
    expect(screen.getByText('Coming later')).toBeTruthy();
  });

  it('Machines shows reach, backlog and version', async () => {
    await show(<MachinesScreen machine="m_gpu" />);
    expect(screen.getByText('laptop')).toBeTruthy();
    expect(screen.getByText('gpu-box')).toBeTruthy();
    expect(screen.getByText(/^1 240 queued, oldest /)).toBeTruthy();
    expect(screen.getAllByText(/same as hub/).length).toBeGreaterThan(0);
  });
});

describe('Alerts', () => {
  it('a rule switch writes prefs.mute, and a rule that is off on the hub is disabled', async () => {
    const p = await show(<AlertsScreen />);
    const sw = await screen.findByTestId('rule-asked');
    await waitFor(() => expect(screen.getByTestId('rule-asked').props.disabled).toBe(false));
    expect(screen.getByTestId('rule-reset').props.disabled).toBe(true); // rule_reset is off on the hub
    expect(screen.getByText('Off on the hub')).toBeTruthy();
    expect(sw.props.value).toBe(true);
    await fireEvent(screen.getByTestId('rule-asked'), 'valueChange', false);
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalled());
    expect(p.client.patchSelf.mock.calls[0]![0]).toMatchObject({ mute: ['asked'] });
    await waitFor(() => expect(screen.getByTestId('rule-asked').props.value).toBe(false));
    // Limits group mutes both triggers.
    await fireEvent(screen.getByTestId('rule-limits'), 'valueChange', false);
    expect(p.client.patchSelf.mock.calls.at(-1)![0]).toMatchObject({ mute: ['asked', 'limit_80', 'limit_95'] });
  });

  it('lists the recent pushes with their outcome', async () => {
    await show(<AlertsScreen />);
    expect(await screen.findByText('work · api · asked a question')).toBeTruthy();
    expect(screen.getAllByText(/^(open|seen|waiting|snoozed|cancelled|closed|skipped)$/).length).toBeGreaterThan(1);
  });
});

describe('Settings', () => {
  const expoDevice = (): Device => ({
    ...(structuredClone(fx.me.device) as unknown as Device),
    targets: [{ id: 'pt_app', kind: 'expo', enabled: true, status: 'ok', name: 'Pixel · app' }],
  });

  it('private, tap and widgets switches PATCH the right prefs', async () => {
    const p = await show(<SettingsScreen />);
    await screen.findByTestId('private-on');
    await waitFor(() => expect(screen.getByTestId('private-on').props.accessibilityState.selected).toBe(false));
    await fireEvent.press(screen.getByTestId('private-on'));
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(1));
    expect(p.client.patchSelf.mock.calls[0]![0]).toMatchObject({ private: true, tap: 'auto' });
    await waitFor(() => expect(screen.getByTestId('private-on').props.accessibilityState.selected).toBe(true));

    await fireEvent.press(screen.getByTestId('tap-claude'));
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(2));
    expect(p.client.patchSelf.mock.calls[1]![0]).toMatchObject({ private: true, tap: 'claude' });

    await fireEvent(screen.getByTestId('pref-widgets'), 'valueChange', true);
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(3));
    expect(p.client.patchSelf.mock.calls[2]![0]).toMatchObject({ widgets: true, tap: 'claude' });

    await fireEvent.press(screen.getByTestId('private-default'));
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(4));
    expect(p.client.patchSelf.mock.calls[3]![0]).toMatchObject({ private: null });
  });

  it('own quiet hours: on writes an object, off writes null, bad times are refused', async () => {
    const p = await show(<SettingsScreen />);
    await screen.findByTestId('pref-quiet');
    await waitFor(() => expect(screen.getByTestId('pref-quiet').props.disabled).toBe(false));
    await fireEvent(screen.getByTestId('pref-quiet'), 'valueChange', true);
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(1));
    expect(p.client.patchSelf.mock.calls[0]![0].quiet_hours).toMatchObject({ enabled: true, start: '23:00', end: '08:00' });
    await fireEvent.changeText(await screen.findByTestId('quiet-start'), '25:99');
    await act(async () => fireEvent(screen.getByTestId('quiet-start'), 'endEditing'));
    expect(await screen.findByText('Use 24-hour times like 23:00')).toBeTruthy();
    expect(p.client.patchSelf).toHaveBeenCalledTimes(1);
    await fireEvent(screen.getByTestId('pref-quiet'), 'valueChange', false);
    await waitFor(() => expect(p.client.patchSelf).toHaveBeenCalledTimes(2));
    expect(p.client.patchSelf.mock.calls[1]![0].quiet_hours).toBeNull();
  });

  it('Send test push goes to the expo target and reports the outcome', async () => {
    const client = fakeClient({ deviceSelf: jest.fn(async () => expoDevice()) });
    const p = await show(<SettingsScreen />, { client });
    await waitFor(() => expect(screen.getByTestId('test-push').props.accessibilityState.disabled).toBe(false));
    await fireEvent.press(screen.getByTestId('test-push'));
    expect(p.client.testTarget).toHaveBeenCalledWith('pt_app');
    expect(await screen.findByText('Sent. It should arrive in a moment.')).toBeTruthy();
  });

  it('test push is disabled offline', async () => {
    const client = fakeClient({ deviceSelf: jest.fn(async () => expoDevice()) });
    await show(<SettingsScreen />, { client, data: { offline: true } });
    await screen.findByText(/^Offline/, { exact: false }).catch(() => undefined);
    expect(screen.getByTestId('test-push').props.accessibilityState.disabled).toBe(true);
  });

  it('says why notifications are off, and offers to turn them on', async () => {
    const p = await show(<SettingsScreen />, { api: { pushState: null } });
    await fireEvent.press(screen.getByTestId('turn-on'));
    expect(p.api.enablePush).toHaveBeenCalled();
  });

  it('Auto countdown (iOS): a local switch, shown only when the app core offers it', async () => {
    await show(<SettingsScreen />);
    expect(screen.queryByTestId('auto-countdown')).toBeNull();
    const set = jest.fn(async () => undefined);
    const p = await show(<SettingsScreen />, { api: { autoCountdown: { on: true, set } } });
    expect(screen.getByText('Count down on the Lock Screen when a limit push is opened.')).toBeTruthy();
    expect(screen.getByTestId('auto-countdown').props.value).toBe(true);
    await fireEvent(screen.getByTestId('auto-countdown'), 'valueChange', false);
    expect(set).toHaveBeenCalledWith(false);
    expect(p.client.patchSelf).not.toHaveBeenCalled(); // not a hub pref
  });

  it('blocked and no-project states', async () => {
    await show(<SettingsScreen />, { api: { pushState: 'blocked' } });
    expect(screen.getByTestId('notif-state').props.children).toMatch(/^Blocked in system settings/);
    expect(screen.queryByTestId('turn-on')).toBeNull();
  });

  it('shows the version and unpairs after a confirmation', async () => {
    const alert = jest.spyOn(RNAlert, 'alert').mockImplementation((_t, _m, buttons) => {
      buttons?.find((b) => b.style === 'destructive')?.onPress?.();
    });
    const p = await show(<SettingsScreen />);
    expect(screen.getByText('0.8.0 (12)')).toBeTruthy();
    expect(screen.getByText('hub.example.ts.net')).toBeTruthy();
    await fireEvent.press(screen.getByTestId('unpair'));
    expect(alert).toHaveBeenCalledWith('Unpair from hub.example.ts.net?', expect.any(String), expect.any(Array));
    expect(p.api.unpair).toHaveBeenCalled();
    alert.mockRestore();
  });

  it('keeps data when loaded fresh', () => {
    expect(freshData().overview?.needs_you.length).toBe(3);
  });
});

describe('Alert redirect', () => {
  it('replaces itself with the alert deep link', async () => {
    await show(<AlertRedirectScreen id="al_pending" />);
    await waitFor(() => expect(routerMock.router.replace).toHaveBeenCalledWith('/sessions/x'));
  });

  it('never loops back to itself', async () => {
    const client = fakeClient({ alert: jest.fn(async () => ({ ...fx.alert, url: '/#/alerts/al_x' })) });
    await show(<AlertRedirectScreen id="al_x" />, { client });
    await waitFor(() => expect(routerMock.router.replace).toHaveBeenCalledWith('/'));
  });

  it('says so when the alert is gone', async () => {
    const client = fakeClient({ alert: jest.fn(async () => { throw Object.assign(new Error('nf'), { status: 404 }); }) });
    await show(<AlertRedirectScreen id="al_gone" />, { client });
    expect(await screen.findByText('That alert no longer exists.')).toBeTruthy();
    await fireEvent.press(screen.getByText('Go to Now'));
    expect(routerMock.router.replace).toHaveBeenCalledWith('/');
  });
});
