// Which account is signed in where (phone.md P17.7; designs Phone-11 and Phone-12): the URL and account rules, the
// memo of each tab's last visit, the Accounts card lines and actions, and the Fix this tab sheet
// on the sy-tabs fake.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { act, fireEvent, render, screen } from '@testing-library/react-native';
import AccountsScreen from '../src/components/screens/AccountsScreen';
import ClaudeTabScreen from '../src/components/screens/ClaudeTabScreen';
import { resetTabMemos } from '../src/components/tab-memo';
import { resetTabsSupport } from '../src/components/tabs';
import { TAB_MEMO_KEY } from '../src/contract/constants';
import type { Seat } from '../src/contract/api';
import {
  codeLine, fixChoices, isReadableUrl, isSignInUrl, nextMemo, phoneTab, phoneTabLine, readMemos, TAB_LOGIN, tabAction,
  type Line, type TabMemo,
} from '../src/lib/tabs';
import { callsOf, tabsState } from './fakes';
import { freshData, makeHarness, routerMock } from './screen-harness';

// eslint-disable-next-line @typescript-eslint/no-require-imports
jest.mock('expo-router', () => require('./screen-harness').routerMock);

const A = 'seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4A';
const B = 'seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4B';
const C = 'seat_01JA2W5N0G8H3Q7S2V6X9Y1Z4C';
const ACC_A = '3f1c6a2e-0000-4000-8000-00000000000a';
const ACC_B = '3f1c6a2e-0000-4000-8000-00000000000b';
const ACC_X = '3f1c6a2e-0000-4000-8000-0000000000ff';
const T = 1_791_000_000_000;

const seat = (id: string, label: string, account: string, more: Partial<Seat> = {}): Seat => ({ id, label, account, org: 'org-1', ...more });
const SA = seat(A, 'claude-7', ACC_A);
const SB = seat(B, 'claude-6', ACC_B);
const SEATS = [SA, SB];
const text = (l: Line | null) => l?.parts.map((p) => p.t).join('') ?? null;
const bold = (l: Line) => l.parts.filter((p) => p.b).map((p) => p.t);

beforeEach(async () => {
  resetTabsSupport();
  resetTabMemos();
  await AsyncStorage.clear();
  routerMock.router.push.mockClear();
});

describe('URL rules (the desktop isReadableUrl)', () => {
  it('a claude.ai page other than login or logout is signed in', () => {
    for (const u of ['https://claude.ai/', 'https://claude.ai/new', 'https://claude.ai/chat/x', 'https://www.claude.ai/x', 'https://claude.ai/loginx']) {
      expect(isReadableUrl(u)).toBe(true);
    }
    for (const u of ['https://claude.ai/login', 'https://claude.ai/login/', 'https://claude.ai/logout', 'https://claude.ai/login?r=1',
      'https://claude.ai/login/x', 'http://claude.ai/', 'https://claude.ai.evil.com/', 'https://claude.ai:8443/', null, '', 'about:blank']) {
      expect(isReadableUrl(u)).toBe(false);
    }
  });

  it('the login page and the sign-in hops are signed out; other sites say nothing', () => {
    expect(isSignInUrl('https://claude.ai/login')).toBe(true);
    expect(isSignInUrl('https://console.anthropic.com/x')).toBe(true);
    expect(isSignInUrl('https://claude.com/login')).toBe(true);
    expect(isSignInUrl('https://claude.ai/new')).toBe(false);
    expect(isSignInUrl('https://challenges.cloudflare.com/x')).toBe(false);
    expect(isSignInUrl('https://example.com/')).toBe(false);
  });
});

describe('nextMemo', () => {
  const page = (url: string, height = 800) => ({ state: 'loaded' as const, url, height });

  it('a signed-in page, then the account answer', () => {
    const m1 = nextMemo(undefined, page('https://claude.ai/new'), T);
    expect(m1).toEqual({ signed_in: true, account: null, at: T });
    const m2 = nextMemo(m1, { state: 'account', account: ACC_A }, T + 1);
    expect(m2).toEqual({ signed_in: true, account: ACC_A, at: T + 1 });
    // A later page keeps the account; an in-page move too.
    expect(nextMemo(m2, page('https://claude.ai/chat/1'), T + 2)).toEqual({ signed_in: true, account: ACC_A, at: T + 2 });
    expect(nextMemo(m2, { state: 'url', url: 'https://claude.ai/recents' }, T + 3)?.account).toBe(ACC_A);
  });

  it('the login page or a 401 is signed out and forgets the account', () => {
    const m: TabMemo = { signed_in: true, account: ACC_A, at: T };
    expect(nextMemo(m, page('https://claude.ai/login'), T + 1)).toEqual({ signed_in: false, account: null, at: T + 1 });
    expect(nextMemo(m, { state: 'url', url: 'https://claude.ai/logout' }, T + 1)).toEqual({ signed_in: false, account: null, at: T + 1 });
    expect(nextMemo(m, { state: 'account', status: 401 }, T + 1)).toEqual({ signed_in: false, account: null, at: T + 1 });
    // Then signing in as someone else does not keep the old account.
    const out = nextMemo(m, page('https://claude.ai/login'), T + 1);
    expect(nextMemo(out, { state: 'url', url: 'https://claude.ai/new' }, T + 2)).toEqual({ signed_in: true, account: null, at: T + 2 });
  });

  it('other answers, blank pages, other sites and other events change nothing', () => {
    const m: TabMemo = { signed_in: true, account: ACC_A, at: T };
    for (const e of [
      { state: 'account' as const, status: 0 }, { state: 'account' as const, status: 403 }, { state: 'account' as const, account: 'me@example.com' },
      page('https://claude.ai/new', 0), page('https://example.com/'), { state: 'loading' as const, url: 'https://claude.ai/login' },
      { state: 'http' as const, status: 403 }, { state: 'crashed' as const },
    ]) {
      expect(nextMemo(m, e, T + 9)).toBe(m);
    }
  });

  it('stored memos are checked', () => {
    expect(readMemos({ [A]: { signed_in: true, account: ACC_A, at: T }, [B]: { signed_in: false, account: ACC_B, at: T } })).toEqual({
      [A]: { signed_in: true, account: ACC_A, at: T }, [B]: { signed_in: false, account: null, at: T },
    });
    expect(readMemos({ 'me@example.com': { signed_in: true, account: ACC_A, at: T }, [A]: { signed_in: 'yes', at: T }, [B]: null })).toEqual({});
    expect(readMemos({ [A]: { signed_in: true, account: 'x', at: T } })).toEqual({ [A]: { signed_in: true, account: null, at: T } });
    for (const bad of [null, [], 'x', 3]) expect(readMemos(bad)).toEqual({});
  });
});

describe('phoneTab (matched by account uuid, never by email)', () => {
  const on = (memo?: TabMemo, profiles: string[] | null = [A]) => phoneTab(SA, SEATS, { supported: true, profiles, memo });

  it('every state, its line and its action', () => {
    const cases: [ReturnType<typeof on>, string | null, ReturnType<typeof tabAction>][] = [
      [phoneTab(SA, SEATS, { supported: false, profiles: [] }), null, null],
      [on(undefined, []), 'Phone tab: not signed in', 'sign_in'],
      [on(undefined), 'Phone tab: not checked yet. Open it to check.', 'open'],
      [on({ signed_in: false, account: null, at: T }), 'Phone tab: not signed in', 'sign_in'],
      [on({ signed_in: true, account: null, at: T }), 'Phone tab: signed in', 'open'],
      [on({ signed_in: true, account: ACC_A, at: T }), 'Phone tab: signed in, matches this account', 'open'],
      [on({ signed_in: true, account: ACC_B, at: T }), 'Phone tab: signed in as claude-6, not this account', 'fix'],
      [on({ signed_in: true, account: ACC_X, at: T }), 'Phone tab: signed in as an account no seat has', 'fix'],
    ];
    for (const [v, line, action] of cases) {
      expect(text(phoneTabLine(v))).toBe(line);
      expect(tabAction(v)).toBe(action);
    }
    expect(bold(phoneTabLine(on({ signed_in: true, account: ACC_B, at: T }))!)).toEqual(['claude-6']);
  });

  it('a seat with the same email but another account uuid is not a match', () => {
    const twin = seat(C, 'twin', ACC_X, { email: 'same@example.com' });
    const self = { ...SA, email: 'same@example.com' };
    expect(phoneTab(self, [self, twin], { supported: true, profiles: [A], memo: { signed_in: true, account: ACC_B, at: T } }).kind).toBe('stranger');
  });

  it('a memo without a profile (signed out in Settings) is not a tab', () => {
    expect(on({ signed_in: true, account: ACC_A, at: T }, [B]).kind).toBe('none');
  });

  it('Fix this tab offers the move only when the other seat has no tab', () => {
    const other = on({ signed_in: true, account: ACC_B, at: T });
    expect(fixChoices(other, [A])).toEqual({ signOut: true, use: SB });
    expect(fixChoices(other, [A, B])).toEqual({ signOut: true, use: null });
    expect(fixChoices(on({ signed_in: true, account: ACC_X, at: T }), [A])).toEqual({ signOut: true, use: null });
    expect(fixChoices(on({ signed_in: true, account: ACC_A, at: T }), [A])).toBeNull();
  });
});

describe('codeLine (from the hub: Seat.places and Seat.signed_out)', () => {
  const place = (machine_name: string, slot: string | null, tab_id: string | null = null) => ({ machine: `m_${machine_name}`, machine_name, slot, tab_id });
  const out = (machine_name: string) => ({ machine: `m_${machine_name}`, machine_name, slot: '~/.claude', since: '2026-10-12T09:00:00.000Z' });

  it('signed in on each machine once, then signed out where it is not also signed in', () => {
    const l = codeLine({ ...SA, places: [place('worker', '~/.claude'), place('worker', '~/.claude-2'), place('desk', null, 't_1')], signed_out: [out('mraj602'), out('worker')] });
    expect(text(l)).toBe('Claude Code: signed in on worker · signed out on mraj602');
    expect(bold(l)).toEqual(['worker', 'mraj602']);
    expect(l.tone).toBe('ok');
  });

  it('two machines, signed out only, or nowhere', () => {
    expect(text(codeLine({ ...SA, places: [place('mraj602', '~/.claude'), place('worker', '~/.claude')] }))).toBe('Claude Code: signed in on mraj602, worker');
    expect(codeLine({ ...SA, signed_out: [out('x')] })).toEqual({ tone: 'muted', parts: [{ t: 'Claude Code: ' }, { t: 'signed out on ' }, { t: 'x', b: true }] });
    expect(text(codeLine({ ...SA, places: [place('desk', null, 't_1')] }))).toBe('Claude Code: not signed in on any machine');
  });
});

// ---- screens

function data() {
  const d = freshData();
  const base = d.accounts[0]!;
  const places = [{ machine: 'm_w', machine_name: 'worker', slot: '~/.claude', tab_id: null }];
  const seats = [
    { ...base, id: A, label: 'claude-7', account: ACC_A, hidden: false, places, signed_out: [{ machine: 'm_r', machine_name: 'mraj602', slot: '~/.claude', since: '2026-10-12T09:00:00.000Z' }] },
    { ...base, id: B, label: 'claude-6', account: ACC_B, hidden: false, places, signed_out: [] },
  ] as Seat[];
  return { accounts: seats, overview: { ...d.overview!, seats } };
}

async function show(ui: React.ReactElement) {
  const h = makeHarness({ data: data() });
  await render(ui, { wrapper: h.Wrapper });
  await act(async () => undefined);
  return h.probe;
}

const event = async (e: Parameters<NonNullable<NonNullable<typeof tabsState.view>['onTabState']>>[0]['nativeEvent']) =>
  act(async () => tabsState.view!.onTabState!({ nativeEvent: e }));

async function memoOf(seatId: string) {
  return (JSON.parse((await AsyncStorage.getItem(TAB_MEMO_KEY)) ?? '{}') as Record<string, TabMemo>)[seatId];
}

describe('screens', () => {
  it('the tab remembers signed in, and as which account, from its own events', async () => {
    await show(<ClaudeTabScreen seat={A} />);
    await screen.findByTestId('tab-view');
    await event({ state: 'loaded', url: 'https://claude.ai/login', height: 500 });
    expect(await memoOf(A)).toMatchObject({ signed_in: false, account: null });
    await event({ state: 'url', url: 'https://claude.ai/new' });
    await event({ state: 'account', url: 'https://claude.ai/new', account: ACC_B });
    expect(await memoOf(A)).toMatchObject({ signed_in: true, account: ACC_B });
  });

  it('Accounts: both lines and Sign in on this phone for a seat without a tab', async () => {
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    expect(screen.getByTestId(`code-${A}`)).toHaveTextContent('Claude Code: signed in on worker · signed out on mraj602');
    expect(screen.getByTestId(`phonetab-${A}`)).toHaveTextContent('Phone tab: not signed in');
    expect(screen.getByTestId(`tab-${A}`)).toHaveTextContent('Sign in on this phone');
    await fireEvent.press(screen.getByTestId(`tab-${A}`));
    expect(routerMock.router.push).toHaveBeenCalledWith(`/tabs/${A}`);
  });

  it('Accounts: a matching tab offers Open claude.ai', async () => {
    tabsState.profiles = [A];
    await AsyncStorage.setItem(TAB_MEMO_KEY, JSON.stringify({ [A]: { signed_in: true, account: ACC_A, at: T } }));
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    expect(screen.getByTestId(`phonetab-${A}`)).toHaveTextContent('Phone tab: signed in, matches this account');
    await fireEvent.press(screen.getByTestId(`tab-${A}`));
    expect(routerMock.router.push).toHaveBeenCalledWith(`/tabs/${A}`);
  });

  it('Fix this tab: sign out here clears the profile and opens the login page', async () => {
    tabsState.profiles = [A];
    await AsyncStorage.setItem(TAB_MEMO_KEY, JSON.stringify({ [A]: { signed_in: true, account: ACC_B, at: T } }));
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    expect(screen.getByTestId(`phonetab-${A}`)).toHaveTextContent('Phone tab: signed in as claude-6, not this account');
    await fireEvent.press(screen.getByTestId(`fix-${A}`));
    expect(screen.getByText("claude-7's tab is signed in as claude-6")).toBeTruthy();
    expect(screen.getByTestId('fix-use')).toHaveTextContent('Use this tab for claude-6 instead');
    await fireEvent.press(screen.getByTestId('fix-signout'));
    await act(async () => undefined);
    expect(callsOf('tabs.deleteProfile')).toEqual([{ fn: 'tabs.deleteProfile', args: [A] }]);
    expect(routerMock.router.push).toHaveBeenCalledWith(`/tabs/${A}?url=${encodeURIComponent(TAB_LOGIN)}`);
    expect(await memoOf(A)).toBeUndefined();
  });

  it('Fix this tab: use it for the other seat moves the profile and its memo', async () => {
    tabsState.profiles = [A];
    await AsyncStorage.setItem(TAB_MEMO_KEY, JSON.stringify({ [A]: { signed_in: true, account: ACC_B, at: T } }));
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    await fireEvent.press(screen.getByTestId(`fix-${A}`));
    await fireEvent.press(screen.getByTestId('fix-use'));
    await act(async () => undefined);
    expect(callsOf('tabs.moveProfile')).toEqual([{ fn: 'tabs.moveProfile', args: [A, B] }]);
    expect(tabsState.profiles).toEqual([B]);
    expect(await memoOf(B)).toMatchObject({ signed_in: true, account: ACC_B });
    expect(await memoOf(A)).toBeUndefined();
    expect(screen.getByTestId(`phonetab-${B}`)).toHaveTextContent('Phone tab: signed in, matches this account');
    expect(screen.getByTestId(`phonetab-${A}`)).toHaveTextContent('Phone tab: not signed in');
  });

  it('+ Add account follows a moved tab: the seat it went to is not offered, the one it left is', async () => {
    tabsState.profiles = [A];
    await AsyncStorage.setItem(TAB_MEMO_KEY, JSON.stringify({ [A]: { signed_in: true, account: ACC_B, at: T } }));
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    await fireEvent.press(screen.getByTestId('add-account'));
    await act(async () => undefined);
    expect(screen.queryByTestId(`add-seat-${A}`)).toBeNull();
    expect(screen.getByTestId(`add-seat-${B}`)).toBeTruthy();
    await fireEvent.press(screen.getByTestId('add-account-close'));
    await fireEvent.press(screen.getByTestId(`fix-${A}`));
    await fireEvent.press(screen.getByTestId('fix-use'));
    await act(async () => undefined);
    await fireEvent.press(screen.getByTestId('add-account'));
    await act(async () => undefined);
    expect(screen.getByTestId(`add-seat-${A}`)).toBeTruthy();
    expect(screen.queryByTestId(`add-seat-${B}`)).toBeNull();
  });

  it('Fix this tab: only sign out when the other seat has a tab already', async () => {
    tabsState.profiles = [A, B];
    await AsyncStorage.setItem(TAB_MEMO_KEY, JSON.stringify({ [A]: { signed_in: true, account: ACC_B, at: T } }));
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    await fireEvent.press(screen.getByTestId(`fix-${A}`));
    expect(screen.getByTestId('fix-signout')).toBeTruthy();
    expect(screen.queryByTestId('fix-use')).toBeNull();
  });

  it('no phone tab line where tabs are not supported', async () => {
    tabsState.supported = false;
    await show(<AccountsScreen />);
    await fireEvent.press(await screen.findByTestId('expand-all'));
    expect(screen.getByTestId(`code-${A}`)).toBeTruthy();
    expect(screen.queryByTestId(`phonetab-${A}`)).toBeNull();
    expect(screen.queryByTestId(`fix-${A}`)).toBeNull();
  });
});
