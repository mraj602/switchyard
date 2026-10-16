import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  tabTitle, tabColour, badgeText, nameError, labelError, viewOrder, cycleView, viewForDigit, movedIds, droppedIds,
  effectiveView, windowLabel, windowRows, readingStatus, hubBanner, hubSummary, hubChip, ownerPill, hubUsable, foldersOf,
  switchState, shareSpec, SHARE_SWITCHES, isPrimary, loginCommand, SHARE_FOLDER_RE, fitTabs, progressText,
} from './view.js';
import { SHARE_SPEC_PATTERN } from '../shared/ipc.js';

const NOW = new Date(2026, 9, 13, 14, 0, 0).getTime();
const local = (h, m, day = 13) => new Date(2026, 9, day, h, m, 0).toISOString();
const tab = (o = {}) => ({
  id: 't_0123456789abcdef', name: 'work', label: null, colour: null, reading: 'on', needs_warning: false,
  send_to_hub: true, read_state: 'idle', stopped: null, retry_at: null, last_read: null, windows: [],
  windows_source: null, signed_in: true, ...o,
});

test('tab title, colour and badge', () => {
  assert.equal(tabTitle(tab()), 'work');
  assert.equal(tabTitle(tab({ label: 'acme' })), 'acme');
  assert.equal(tabColour(tab({ colour: '#6A4FD6' }), 0), '#6a4fd6');
  assert.equal(tabColour(tab({ colour: 'red; background:url(x)' }), 1), 'var(--seat-1)');
  assert.equal(tabColour(tab(), 5), 'var(--seat-1)');
  assert.equal(tabColour(tab(), -1), 'var(--seat-3)');
  assert.deepEqual([0, 1, 9, 10, 99, null, -2].map(badgeText), ['', '1', '9', '9+', '9+', '', '']);
});

test('input rules', () => {
  assert.equal(nameError(''), 'Give the tab a name.');
  assert.equal(nameError('   '), 'Give the tab a name.');
  assert.equal(nameError('a'.repeat(25)).includes('24'), true);
  assert.equal(nameError('a'.repeat(24)), '');
  assert.equal(labelError(''), '');
  assert.equal(labelError('work-2'), '');
  assert.notEqual(labelError('Work'), '');
  assert.notEqual(labelError('-x'), '');
  assert.notEqual(labelError('a'.repeat(25)), '');
});

test('view order, cycling, digits and moving', () => {
  const state = { view: 'hub', tabs: [tab({ id: 'a1a1a1a1' }), tab({ id: 'b2b2b2b2' })] };
  const order = viewOrder(state);
  assert.deepEqual(order, ['hub', 'a1a1a1a1', 'b2b2b2b2']);
  assert.equal(cycleView(order, 'hub', 1), 'a1a1a1a1');
  assert.equal(cycleView(order, 'hub', -1), 'b2b2b2b2');
  assert.equal(cycleView(order, 'b2b2b2b2', 1), 'hub');
  assert.equal(cycleView(order, 'settings', 1), 'a1a1a1a1');
  assert.equal(cycleView([], 'hub', 1), 'hub');
  assert.equal(viewForDigit(state, 0), 'hub');
  assert.equal(viewForDigit(state, 2), 'b2b2b2b2');
  assert.equal(viewForDigit(state, 3), null);
  assert.deepEqual(movedIds(['a', 'b', 'c'], 'a', 1), ['b', 'a', 'c']);
  assert.deepEqual(movedIds(['a', 'b', 'c'], 'c', -1), ['a', 'c', 'b']);
  assert.equal(movedIds(['a', 'b', 'c'], 'a', -1), null);
  assert.equal(movedIds(['a', 'b', 'c'], 'c', 1), null);
  assert.equal(movedIds(['a'], 'zz', 1), null);
  assert.equal(effectiveView({ view: 'settings' }, 'a1a1a1a1'), 'a1a1a1a1');
  assert.equal(effectiveView({ view: 'settings' }, null), 'settings');
});

test('window rows: 5h and 7d first, unknown keys under their raw name', () => {
  const rows = windowRows(tab({
    windows: [
      { key: 'seven_day_opus', label: null, used_pct: 12, resets_at: local(9, 0, 19) },
      { key: 'seven_day', label: null, used_pct: 30, resets_at: local(9, 0, 19) },
      { key: 'five_hour', label: null, used_pct: 41, resets_at: local(15, 10) },
      { key: 'mystery_window', label: null, used_pct: null, resets_at: null },
    ],
  }), NOW);
  assert.deepEqual(rows.map((r) => r.key), ['five_hour', 'seven_day', 'mystery_window', 'seven_day_opus']);
  assert.deepEqual(rows.map((r) => r.label), ['5-hour', '7-day', 'Mystery window', 'Seven day opus']);
  assert.equal(rows[0].reset, '15:10');
  assert.equal(rows[1].reset, 'Mon 09:00');
  assert.equal(rows[2].used, null);
  assert.deepEqual(rows.map((r) => r.primary), [true, true, false, false]);
  assert.equal(windowLabel({ key: 'x', label: 'Sonnet week' }), 'Sonnet week');
});

test('reading status covers every state', () => {
  assert.equal(readingStatus(tab({ reading: 'never_asked', needs_warning: true }), NOW).action, 'review');
  assert.equal(readingStatus(tab({ needs_warning: true }), NOW).kind, 'never_asked');
  assert.equal(readingStatus(tab({ reading: 'off', read_state: 'off' }), NOW).text, 'Reading off');
  const s429 = readingStatus(tab({ read_state: 'stopped', stopped: { status: 429, at: local(13, 0) } }), NOW);
  assert.equal(s429.text, 'Stopped: rate limited (429).');
  assert.equal(s429.action, 'resume');
  assert.equal(readingStatus(tab({ stopped: { status: 401, at: local(13, 0) } }), NOW).text, 'Stopped: signed out (401).');
  assert.equal(readingStatus(tab({ stopped: { status: 403, at: local(13, 0) } }), NOW).text, 'Usage not available for this account (403).');
  assert.equal(readingStatus(tab({ signed_in: false }), NOW).kind, 'signed_out');
  assert.equal(readingStatus(tab({ read_state: 'retrying', retry_at: local(14, 20) }), NOW).text, 'Retrying at 14:20');
  assert.equal(readingStatus(tab({ read_state: 'retrying' }), NOW).text, 'Retrying');
  assert.equal(readingStatus(tab({ read_state: 'reading' }), NOW).text, 'Reading…');
  assert.equal(readingStatus(tab({ last_read: local(13, 58) }), NOW).text, 'read 2 min ago');
  assert.equal(readingStatus(tab(), NOW).text, 'Waiting for the first read');
});

test('hub banners and summaries', () => {
  const hub = (o) => ({ url: 'https://hub.example', connected: true, reachable: true, paired: true, owner: false, version: '0.6.0', local: false, service: null, admin: null, needs_you: 0, status: 'ok', ...o });
  assert.equal(hubBanner(hub()), null);
  assert.equal(hubBanner(null), null);
  // an ok hub with an error (a 403): shown, nothing to do about the token
  assert.deepEqual(hubBanner(hub({ error: 'forbidden (403)' })), { level: 'error', title: null, text: 'forbidden (403)', command: null, action: null });
  // no hub (no sy, or sy not connected): the Connect card, with the TARGET words and no terminal command
  for (const status of ['no_sy', 'not_connected']) {
    const b = hubBanner(hub({ status, url: null }));
    assert.equal(b.title, 'Connect to a hub.');
    assert.equal(b.text, "Enter your hub's address. Account tabs work without a hub.");
    assert.equal(b.action, 'connect');
    assert.equal(b.command, null);
  }
  assert.match(hubBanner(hub({ status: 'dial' })).text, /forward mode/i);
  assert.equal(hubBanner(hub({ status: 'needs_pairing' })).action, 'pair');
  assert.equal(hubBanner(hub({ status: 'needs_pairing', admin: 'local' })).action, 'pair-click');
  assert.equal(hubBanner(hub({ status: 'needs_pairing', local: true })).action, 'pair-click');
  // a removed device: the wording, then the same pair action as a first pairing
  assert.equal(hubBanner(hub({ status: 'needs_pairing', removed: true })).title, 'This computer was removed from the hub.');
  assert.match(hubBanner(hub({ status: 'needs_pairing', removed: true })).text, /^Someone removed it in Devices\. Pair again to see the board\./);
  assert.equal(hubBanner(hub({ status: 'needs_pairing' })).title, 'Pair this computer.');
  assert.equal(hubBanner(hub({ status: 'needs_pairing', admin: 'local' })).text, "This computer can reach the hub's computer, so pairing needs no code.");
  assert.match(hubBanner(hub({ status: 'needs_pairing' })).text, /^Get a code on any owner device: More, Devices, Add desktop app\./);
  assert.equal(hubBanner(hub({ status: 'needs_pairing', removed: true })).action, 'pair');
  assert.equal(hubBanner(hub({ status: 'needs_pairing', removed: true, admin: 'local' })).action, 'pair-click');
  assert.doesNotMatch(hubBanner(hub({ status: 'needs_pairing' })).text, /removed/);
  // the board could not be signed in: our own panel, never the board's login wording
  assert.equal(hubBanner(hub({ board_blocked: true })).level, 'error');
  assert.doesNotMatch(hubBanner(hub({ board_blocked: true })).text, /owner-link|browser/i);
  assert.equal(hubUsable(hub({ board_blocked: true })), false);
  assert.match(hubBanner(hub({ status: 'unreachable' })).text, /hub\.example/);
  assert.equal(hubBanner(hub({ status: 'unreachable' })).title, "Can't reach the hub.");
  assert.equal(hubBanner(hub({ status: 'unreachable' })).action, 'retry');
  assert.equal(hubBanner(hub({ status: 'unreachable', local: true })).action, 'retry-service');
  assert.match(hubBanner(hub({ status: 'unreachable', last_seen: '2026-10-16T10:00:00Z' }), Date.parse('2026-10-16T10:05:00Z')).text, /Last seen/);
  assert.equal(hubBanner(hub({ status: 'something_new' })), null);
  assert.equal(hubSummary(hub()), 'Connected · v0.6.0');
  assert.equal(hubSummary(hub({ version: 'dev' })), 'Connected · dev'); // not "vdev"
  assert.equal(hubSummary(hub({ status: 'needs_pairing' })), 'Needs pairing');
  // the top-bar chip and the owner pill
  assert.deepEqual(hubChip(hub()), { text: 'Hub · Connected', level: 'ok' });
  assert.equal(hubChip(hub({ status: 'needs_pairing' })).text, 'Hub · Not paired');
  assert.equal(hubChip(hub({ status: 'needs_pairing', removed: true })).text, 'Hub · Removed');
  assert.equal(hubChip(hub({ status: 'unreachable' })).level, 'error');
  assert.equal(hubChip(hub({ status: 'no_sy' })).text, 'Hub · Not connected');
  assert.deepEqual(ownerPill(hub()), { text: 'Read-only', owner: false });
  assert.match(ownerPill(hub({ owner: true, owner_until: '2026-11-14T10:00:00Z' })).text, /^Owner · until 14 Nov$/);
  assert.deepEqual(ownerPill(hub({ owner: true })), { text: 'Owner', owner: true });
  assert.equal(ownerPill(hub({ status: 'needs_pairing' })), null);
  assert.equal(hubUsable(hub()), true);
  assert.equal(hubUsable(hub({ status: 'unreachable' })), false);
});

const folder = (o = {}) => ({
  folder: '~/.claude-2', label: 'work', logged_in: true,
  items: {
    'settings.json': { state: 'linked' }, 'CLAUDE.md': { state: 'linked' }, rules: { state: 'linked' },
    skills: { state: 'linked' }, agents: { state: 'linked' }, commands: { state: 'linked' },
    plugins: { state: 'separate' }, projects: { state: 'separate' }, 'history.jsonl': { state: 'copy' },
  },
  mcp_sync: true, ...o,
});

test('sharing switches read the item states', () => {
  const f = folder();
  const st = Object.fromEntries(SHARE_SWITCHES.map((s) => [s.id, switchState(f, s)]));
  assert.deepEqual(st, { settings: 'on', instructions: 'on', addons: 'on', plugins: 'off', mcp: 'on', history: 'off', prompts: 'on' });
  assert.equal(switchState(folder({ mcp_sync: undefined }), SHARE_SWITCHES[4]), 'unknown');
  assert.equal(switchState(folder({ items: { 'settings.json': { state: 'elsewhere' } } }), SHARE_SWITCHES[0]), 'elsewhere');
  assert.equal(switchState(folder({ folder: '~/.claude' }), SHARE_SWITCHES[0]), 'source');
  assert.equal(isPrimary(folder({ primary: true })), true);
});

test('shareSpec matches the C11 pattern and names what turns off', () => {
  const f = folder();
  const re = new RegExp(SHARE_SPEC_PATTERN);
  for (const sw of SHARE_SWITCHES) {
    for (const on of [true, false]) {
      const spec = shareSpec(f, sw.id, on);
      assert.match(spec, re, `${sw.id} ${on}`);
    }
  }
  assert.equal(shareSpec(f, 'plugins', true), 'settings,instructions,addons,plugins,mcp,prompts');
  assert.equal(shareSpec(f, 'settings', false), '-settings,instructions,addons,mcp,prompts');
  const all = folder({ items: { 'settings.json': { state: 'linked' } }, mcp_sync: false });
  assert.equal(shareSpec(all, 'settings', false), '-settings');
});

test('folders and commands', () => {
  assert.deepEqual(foldersOf([1]), [1]);
  assert.deepEqual(foldersOf({ folders: [2] }), [2]);
  assert.deepEqual(foldersOf({ state: { folders: [3] } }), [3]);
  assert.deepEqual(foldersOf(null), []);
  assert.equal(loginCommand(folder()), 'sy account login work');
  assert.equal(loginCommand(folder({ label: '' })), 'sy account login ~/.claude-2');
  assert.equal(SHARE_FOLDER_RE.test('~/.claude-2'), true);
  assert.equal(SHARE_FOLDER_RE.test('~/a b'), false);
});

test('droppedIds: drop before or after a tab; no-ops are null', () => {
  const ids = ['a', 'b', 'c', 'd'];
  assert.deepEqual(droppedIds(ids, 'a', 'c', false), ['b', 'a', 'c', 'd']);
  assert.deepEqual(droppedIds(ids, 'a', 'c', true), ['b', 'c', 'a', 'd']);
  assert.deepEqual(droppedIds(ids, 'd', 'a', false), ['d', 'a', 'b', 'c']);
  assert.equal(droppedIds(ids, 'b', 'a', true), null, 'already there');
  assert.equal(droppedIds(ids, 'b', 'c', false), null, 'already there');
  assert.equal(droppedIds(ids, 'b', 'b', true), null);
  assert.equal(droppedIds(ids, 'x', 'a', true), null, 'unknown dragged id (the Hub)');
  assert.equal(droppedIds(ids, 'a', 'hub', false), null, 'the Hub is not a drop target');
});

test('fitTabs: everything fits, or the Hub and the shown tab stay and the rest go in order', () => {
  const w = [60, 100, 100, 100, 100, 100, 100, 100];
  assert.deepEqual(fitTabs(w, 2000, 90, -1), w.map(() => true));
  // room for Hub + 3 tabs + the "more" button
  const keep = fitTabs(w, 60 + 3 * 102 + 94, 90, -1);
  assert.deepEqual(keep, [true, true, true, true, false, false, false, false]);
  // the shown tab (index 7) is always kept, so one fewer fits before it
  const sel = fitTabs(w, 60 + 3 * 102 + 94, 90, 7);
  assert.deepEqual(sel, [true, true, true, false, false, false, false, true]);
  // no room at all: still Hub and the shown tab
  assert.deepEqual(fitTabs(w, 10, 90, 3), [true, false, false, true, false, false, false, false]);
});

test('progressText: SSH admin channels say how long it can take', () => {
  assert.equal(progressText({ admin: 'ssh:vm' }, 'pair'), "Pairing… asking the hub's computer over SSH (can take 20 seconds)");
  assert.match(progressText({ admin: 'ssh:vm' }, 'owner'), /^Signing in as owner… asking the hub's computer over SSH/);
  assert.equal(progressText({ admin: 'local', local: true }, 'pair'), 'Pairing…');
  assert.equal(progressText({}, 'owner'), 'Signing in as owner…');
});
