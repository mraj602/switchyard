// claude.ai account tabs (phone.md P17): the routing (session → seat → tab, and the browser
// fallbacks), the load assessment, and the tab, Accounts and Settings screens on the sy-tabs fake.
import { Alert as RNAlert, Linking } from 'react-native';
import { act, fireEvent, render, screen } from '@testing-library/react-native';
import ClaudeTabScreen from '../src/components/screens/ClaudeTabScreen';
import AccountsScreen from '../src/components/screens/AccountsScreen';
import NowScreen from '../src/components/screens/NowScreen';
import SettingsScreen from '../src/components/screens/SettingsScreen';
import { openInClaude, resetTabsSupport } from '../src/components/tabs';
import type { Seat, Session } from '../src/contract/api';
import {
  assessTab, claudeUrl, LOAD_TIMEOUT_MS, openTarget, profileFor, REFUSAL_GRACE_MS, SIGN_IN_NOTE, TAB_HOME, tabRows, verdictText,
  type TabEventAt,
} from '../src/lib/tabs';
import { callsOf, tabsState } from './fakes';
import { freshData, makeHarness, routerMock } from './screen-harness';

// eslint-disable-next-line @typescript-eslint/no-require-imports
jest.mock('expo-router', () => require('./screen-harness').routerMock);
jest.mock('expo-clipboard', () => ({ setStringAsync: jest.fn(async () => true) }));

const A = 'seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4A';
const B = 'seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4B';
const LINK_A = 'https://claude.ai/code/session_01AAAA';
const LINK_B = 'https://claude.ai/code/session_01BBBB';

beforeEach(() => {
  resetTabsSupport();
  routerMock.router.push.mockClear();
});

describe('routing (P17.2)', () => {
  const on = { supported: true };

  it('names profiles by seat id only', () => {
    expect(profileFor(A)).toBe(`sy-tab-${A}`);
    for (const bad of ['me@example.com', 'seat_work', '', null, `${A}x`, 'seat_01ja2w5n0g8h3q7s2v6x9y1z4a']) expect(profileFor(bad)).toBeNull();
  });

  it('a session opens in its own seat tab', () => {
    expect(openTarget({ seat: A, remote_url: LINK_A }, on)).toEqual({ kind: 'tab', seat: A, url: LINK_A });
    expect(openTarget({ seat: B, remote_url: LINK_B }, on)).toEqual({ kind: 'tab', seat: B, url: LINK_B });
  });

  it('falls back to the browser without tab support, a seat, or a claude.ai link', () => {
    expect(openTarget({ seat: A, remote_url: LINK_A }, { supported: false })).toEqual({ kind: 'browser', url: LINK_A, reason: 'unsupported' });
    expect(openTarget({ seat: null, remote_url: LINK_A }, on)).toEqual({ kind: 'browser', url: LINK_A, reason: 'unattributed' });
    expect(openTarget({ seat: 'seat_work', remote_url: LINK_A }, on)).toMatchObject({ kind: 'browser', reason: 'unattributed' });
    expect(openTarget({ seat: A, remote_url: 'https://example.com/x' }, on)).toMatchObject({ kind: 'browser', reason: 'not_claude' });
    expect(openTarget({ seat: A, remote_url: 'https://claude.ai.evil.com/x' }, on)).toMatchObject({ kind: 'browser', reason: 'not_claude' });
  });

  it('opens nothing that is not https', () => {
    for (const u of [null, undefined, '', 'http://claude.ai/x', 'javascript:alert(1)', 'intent://claude.ai']) {
      expect(openTarget({ seat: A, remote_url: u }, on)).toBeNull();
    }
  });

  it('claudeUrl takes claude.ai and its subdomains only', () => {
    expect(claudeUrl('https://claude.ai/new')).toBe('https://claude.ai/new');
    expect(claudeUrl('https://www.claude.ai/')).toBe('https://www.claude.ai/');
    for (const u of ['https://claude.ai:8443/', 'https://u:p@claude.ai/', 'https://evilclaude.ai/', 'http://claude.ai/']) expect(claudeUrl(u)).toBeNull();
  });
});

describe('openInClaude', () => {
  it('pushes the seat tab with the link when tabs are supported', async () => {
    const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    expect(await openInClaude({ seat: B, remote_url: LINK_B })).toBe(true);
    expect(routerMock.router.push).toHaveBeenCalledWith(`/tabs/${B}?url=${encodeURIComponent(LINK_B)}`);
    expect(open).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it('uses the browser when the WebView has no multi-profile support', async () => {
    tabsState.supported = false;
    const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    expect(await openInClaude({ seat: A, remote_url: LINK_A })).toBe(true);
    expect(open).toHaveBeenCalledWith(LINK_A);
    expect(routerMock.router.push).not.toHaveBeenCalled();
    open.mockRestore();
  });

  it('uses the browser when the module is missing (iOS, Expo Go)', async () => {
    tabsState.available = false;
    const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    await openInClaude({ seat: A, remote_url: LINK_A });
    expect(open).toHaveBeenCalledWith(LINK_A);
    expect(callsOf('tabs.isSupported')).toHaveLength(0);
    open.mockRestore();
  });
});

describe('assessTab (P17.4)', () => {
  const T = 1_000_000;
  const ev = (state: TabEventAt['state'], at: number, extra: Partial<TabEventAt> = {}): TabEventAt => ({ state, at, ...extra });

  it('waits, then times out without a finished page', () => {
    expect(assessTab([], T, T + 1000)).toEqual({ kind: 'waiting' });
    expect(assessTab([ev('loading', T + 10, { url: LINK_A })], T, T + LOAD_TIMEOUT_MS)).toEqual({ kind: 'waiting' });
    expect(assessTab([ev('loading', T, { url: LINK_A })], T, T + LOAD_TIMEOUT_MS)).toEqual({ kind: 'timeout' });
  });

  it('a page with content is ok; an empty one is blank', () => {
    expect(assessTab([ev('loading', T), ev('loaded', T + 5, { url: LINK_A, height: 900 })], T, T + 6)).toEqual({ kind: 'ok' });
    expect(assessTab([ev('loading', T), ev('loaded', T + 5, { url: LINK_A, height: 0 })], T, T + 6)).toEqual({ kind: 'blank' });
    expect(assessTab([ev('loaded', T + 5, { url: 'about:blank', height: 50 })], T, T + 6)).toEqual({ kind: 'blank' });
  });

  it('a 403 is a refusal only if no new page follows within the grace time', () => {
    const challenged = [ev('loading', T, { url: TAB_HOME }), ev('http', T + 1, { status: 403 }), ev('loaded', T + 2, { url: TAB_HOME, height: 400 })];
    expect(assessTab(challenged, T, T + 2000)).toEqual({ kind: 'waiting' });
    expect(assessTab(challenged, T, T + 1 + REFUSAL_GRACE_MS)).toEqual({ kind: 'refused', status: 403 });
    const passed = [...challenged, ev('loading', T + 3000, { url: TAB_HOME }), ev('loaded', T + 4000, { url: TAB_HOME, height: 800 })];
    expect(assessTab(passed, T, T + 60_000)).toEqual({ kind: 'ok' });
  });

  it('network errors, crashes and an unsupported WebView fail at once', () => {
    expect(assessTab([ev('error', T, { url: TAB_HOME })], T, T)).toEqual({ kind: 'failed', reason: 'network' });
    expect(assessTab([ev('loaded', T, { url: TAB_HOME, height: 9 }), ev('crashed', T + 1)], T, T + 1)).toEqual({ kind: 'failed', reason: 'crashed' });
    expect(assessTab([ev('error', T, { reason: 'unsupported' })], T, T)).toEqual({ kind: 'failed', reason: 'unsupported' });
  });

  it('downloads and external links do not change the verdict', () => {
    const ok = [ev('loaded', T, { url: TAB_HOME, height: 9 }), ev('download', T + 1), ev('external', T + 2, { url: 'https://github.com' })];
    expect(assessTab(ok, T, T + 50_000)).toEqual({ kind: 'ok' });
  });

  it('has words for every failure and none for ok or waiting', () => {
    expect(verdictText({ kind: 'ok' })).toBeNull();
    expect(verdictText({ kind: 'waiting' })).toBeNull();
    expect(verdictText({ kind: 'refused', status: 403 })).toMatch(/403/);
    for (const v of [{ kind: 'blank' }, { kind: 'timeout' }, { kind: 'failed', reason: 'crashed' }] as const) expect(verdictText(v)).toBeTruthy();
  });
});

describe('tabRows', () => {
  it('names known seats, marks removed ones, ignores other profiles, keeps seat order', () => {
    const seats = [{ id: B, label: 'bee' }, { id: A, label: null, email: 'ay@example.com' }] as Seat[];
    expect(tabRows([A, 'seat_other', B, 'seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4C'], seats)).toEqual([
      { seat: B, name: 'bee', known: true },
      { seat: A, name: 'ay', known: true },
      { seat: 'seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4C', name: 'Removed account', known: false },
    ]);
  });
});

// Two seats with ULID ids and one session each, so "B opens in B" is visible end to end.
function twoSeatData() {
  const d = freshData();
  const seats = d.accounts.slice(0, 2).map((s, i) => ({ ...s, id: i === 0 ? A : B, label: i === 0 ? 'alpha' : 'beta', hidden: false }));
  const base = d.overview!.needs_you[0]!;
  const sa = { ...base, id: 'sess-a', seat: A, seat_label: 'alpha', remote_url: LINK_A } as Session;
  const sb = { ...base, id: 'sess-b', seat: B, seat_label: 'beta', remote_url: LINK_B, alert: base.alert && { ...base.alert, id: 'al_b' } } as Session;
  return {
    accounts: seats,
    overview: { ...d.overview!, seats, needs_you: [sa, sb] },
    sessions: [sa, sb],
  };
}

async function show(ui: React.ReactElement, data = twoSeatData()) {
  const h = makeHarness({ data });
  await render(ui, { wrapper: h.Wrapper });
  return h.probe;
}

describe('screens', () => {
  it('Now: Open in Claude on a session from B opens B\'s tab, and A\'s opens A\'s', async () => {
    await show(<NowScreen />);
    await fireEvent.press(await screen.findByTestId('open-sess-b'));
    await act(async () => undefined);
    expect(routerMock.router.push).toHaveBeenLastCalledWith(`/tabs/${B}?url=${encodeURIComponent(LINK_B)}`);
    await fireEvent.press(screen.getByTestId('open-sess-a'));
    await act(async () => undefined);
    expect(routerMock.router.push).toHaveBeenLastCalledWith(`/tabs/${A}?url=${encodeURIComponent(LINK_A)}`);
  });

  it('the tab binds the seat, loads the link and says how to sign in', async () => {
    await show(<ClaudeTabScreen seat={B} url={LINK_B} />);
    expect(await screen.findByTestId('tab-view')).toBeTruthy();
    expect(tabsState.view).toMatchObject({ seat: B, url: LINK_B });
    expect(screen.getByText('beta')).toBeTruthy();
    expect(screen.getByText(SIGN_IN_NOTE)).toBeTruthy();
    expect(screen.queryByTestId('tab-fallback')).toBeNull();
  });

  it('the tab never loads a link off claude.ai', async () => {
    await show(<ClaudeTabScreen seat={A} url="https://evil.example/x" />);
    await screen.findByTestId('tab-view');
    expect(tabsState.view?.url).toBe(TAB_HOME);
  });

  it('a blank or crashed tab offers the browser', async () => {
    const open = jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
    await show(<ClaudeTabScreen seat={A} url={LINK_A} />);
    await screen.findByTestId('tab-view');
    await act(async () => tabsState.view!.onTabState!({ nativeEvent: { state: 'loaded', url: LINK_A, height: 0 } }));
    expect(screen.getByTestId('tab-fallback')).toBeTruthy();
    expect(screen.getByText(/stayed blank/)).toBeTruthy();
    await fireEvent.press(screen.getByTestId('tab-fallback-browser'));
    expect(open).toHaveBeenCalledWith(LINK_A);
    open.mockRestore();
  });

  it('a phone without multi-profile WebView gets the explanation, not a tab', async () => {
    tabsState.supported = false;
    await show(<ClaudeTabScreen seat={A} url={LINK_A} />);
    expect(await screen.findByText(/cannot keep separate claude.ai sign-ins/)).toBeTruthy();
    expect(screen.queryByTestId('tab-view')).toBeNull();
  });

  it('Accounts: Open claude.ai opens the seat tab', async () => {
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    await fireEvent.press(await screen.findByTestId(`tab-${A}`));
    expect(routerMock.router.push).toHaveBeenCalledWith(`/tabs/${A}`);
  });

  it('Accounts: no tab buttons without support', async () => {
    tabsState.supported = false;
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    await act(async () => undefined);
    expect(screen.queryByText('Open claude.ai')).toBeNull();
  });

  it('Settings: Sign out (clear this tab) deletes that profile after a confirmation', async () => {
    tabsState.profiles = [A, B];
    const alert = jest.spyOn(RNAlert, 'alert').mockImplementation((_t, _m, buttons) => {
      buttons?.find((b) => b.style === 'destructive')?.onPress?.();
    });
    await show(<SettingsScreen />);
    expect(await screen.findByTestId(`tabrow-${A}`)).toBeTruthy();
    expect(screen.getByText(new RegExp(SIGN_IN_NOTE.replace('.', '\\.')))).toBeTruthy();
    await fireEvent.press(screen.getByTestId(`tab-signout-${B}`));
    await act(async () => undefined);
    expect(alert).toHaveBeenCalledWith('Sign out of beta?', expect.any(String), expect.any(Array));
    expect(callsOf('tabs.deleteProfile')).toEqual([{ fn: 'tabs.deleteProfile', args: [B] }]);
    expect(tabsState.profiles).toEqual([A]);
    expect(screen.queryByTestId(`tabrow-${B}`)).toBeNull();
    alert.mockRestore();
  });

  it('Settings: an open tab cannot be cleared', async () => {
    tabsState.profiles = [A];
    tabsState.inUse.add(A);
    jest.spyOn(RNAlert, 'alert').mockImplementation((_t, _m, buttons) => buttons?.find((b) => b.style === 'destructive')?.onPress?.());
    await show(<SettingsScreen />);
    await fireEvent.press(await screen.findByTestId(`tab-signout-${A}`));
    expect(await screen.findByText('Close the tab first, then try again')).toBeTruthy();
    expect(tabsState.profiles).toEqual([A]);
    jest.restoreAllMocks();
  });

  it('Accounts: + Add account lists only seats without a tab, and picking one opens its tab', async () => {
    tabsState.profiles = [A];
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    await fireEvent.press(await screen.findByTestId('add-account'));
    expect(await screen.findByText('Which account?')).toBeTruthy();
    expect(screen.queryByTestId(`add-seat-${A}`)).toBeNull();
    expect(screen.getByText('Not listed? An account appears after it is used once in Claude Code or the desktop app.')).toBeTruthy();
    await fireEvent.press(screen.getByTestId(`add-seat-${B}`));
    expect(routerMock.router.push).toHaveBeenCalledWith(`/tabs/${B}`);
    expect(screen.queryByText('Which account?')).toBeNull();
  });

  it('Accounts: the sheet says so when every seat has a tab', async () => {
    tabsState.profiles = [A, B];
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    await fireEvent.press(await screen.findByTestId('add-account'));
    expect(await screen.findByText(/already has a tab on this phone/)).toBeTruthy();
    expect(screen.queryByTestId(`add-seat-${A}`)).toBeNull();
  });

  it('Accounts: no Add account without tab support', async () => {
    tabsState.supported = false;
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    await act(async () => undefined);
    expect(screen.queryByTestId('add-account')).toBeNull();
  });

  describe('Unpair asks about the tabs', () => {
    const run = async (answer: string | null) => {
      const alert = jest
        .spyOn(RNAlert, 'alert')
        .mockImplementation((title, _m, buttons) => {
          const pick = title.startsWith('Unpair') ? 'Unpair' : answer;
          buttons?.find((b) => b.text === pick)?.onPress?.();
        });
      const p = await show(<SettingsScreen />);
      await fireEvent.press(await screen.findByTestId('unpair'));
      await act(async () => undefined);
      await act(async () => undefined);
      return { alert, p };
    };

    it('Yes removes the tab profiles, then unpairs', async () => {
      tabsState.profiles = [A, B];
      const { alert, p } = await run('Yes, sign out');
      expect(alert).toHaveBeenCalledWith('Also sign out of the claude.ai tabs on this phone?', expect.any(String), expect.any(Array));
      expect(tabsState.profiles).toEqual([]);
      expect(p.api.unpair).toHaveBeenCalledTimes(1);
      alert.mockRestore();
    });

    it('No keeps them and still unpairs', async () => {
      tabsState.profiles = [A, B];
      const { alert, p } = await run('No, keep them');
      expect(tabsState.profiles).toEqual([A, B]);
      expect(p.api.unpair).toHaveBeenCalledTimes(1);
      alert.mockRestore();
    });

    it('Cancel on the second question unpairs nothing', async () => {
      tabsState.profiles = [A];
      const { alert, p } = await run('Cancel');
      expect(tabsState.profiles).toEqual([A]);
      expect(p.api.unpair).not.toHaveBeenCalled();
      alert.mockRestore();
    });

    it('without any tab there is no second question', async () => {
      tabsState.profiles = [];
      const { alert, p } = await run(null);
      expect(alert).toHaveBeenCalledTimes(1);
      expect(p.api.unpair).toHaveBeenCalledTimes(1);
      alert.mockRestore();
    });
  });

  it('Page: a moved hub shows its card with Switch to the new address', async () => {
    const switchToMoved = jest.fn(async () => 'switched' as const);
    const h = makeHarness({ data: twoSeatData(), api: { moved: { to: 'https://new.example.ts.net' }, switchToMoved } });
    await render(<AccountsScreen />, { wrapper: h.Wrapper });
    expect(await screen.findByText('Your hub moved to new.example.ts.net')).toBeTruthy();
    await fireEvent.press(screen.getByText('Switch to the new address'));
    expect(switchToMoved).toHaveBeenCalledTimes(1);
  });

  it('Page: a failed switch says to try again', async () => {
    const switchToMoved = jest.fn(async () => 'failed' as const);
    const h = makeHarness({ data: twoSeatData(), api: { moved: { to: 'https://new.example.ts.net' }, switchToMoved } });
    await render(<AccountsScreen />, { wrapper: h.Wrapper });
    await fireEvent.press(await screen.findByText('Switch to the new address'));
    expect(await screen.findByText(/Could not reach the new address yet/)).toBeTruthy();
  });
});
