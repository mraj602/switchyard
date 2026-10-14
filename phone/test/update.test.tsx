// The in-app updater (phone.md P16.3) on the JS side: the once-a-day check, what is offered, the
// refusal words and Settings' Updates row. The native module is the recording fake of
// test/fakes.ts (`linkState.update`, `installResult`, ...); the real checks (checksum, signing
// key) are Kotlin's and tested there.
import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import * as SyLink from '../modules/sy-link';
import SettingsScreen from '../src/components/screens/SettingsScreen';
import type { UpdateInfo } from '../src/contract/link';
import type { Clock, KVStore } from '../src/contract/platform';
import { createUpdater, DAY_MS, REFUSED_TEXT, UPDATE_KEY, updateText, type Updater } from '../src/lib/update';
import { callsOf, linkState } from './fakes';
import { makeHarness } from './screen-harness';

const INFO: UpdateInfo = {
  version: '0.9.0',
  build: 900,
  url: 'https://hub.example.ts.net/v1/app/android/apk',
  sha256: 'a'.repeat(64),
  certSha256: 'b'.repeat(64),
  size: 1234,
  notes: 'Fixes the widget refresh.',
  source: 'hub',
};

function memoryKV(): KVStore & { map: Map<string, string> } {
  const map = new Map<string, string>();
  return {
    map,
    getItem: async (k) => map.get(k) ?? null,
    setItem: async (k, v) => void map.set(k, v),
    removeItem: async (k) => void map.delete(k),
  };
}

function fakeClock(start = 1_000_000): Clock & { t: number } {
  const c = { t: start, now: () => c.t, setTimeout: () => 0, clearTimeout: () => undefined };
  return c;
}

function make(over: { build?: number; hub?: { origin: string; token: string } | null } = {}) {
  const kv = memoryKV();
  const clock = fakeClock();
  const hub = over.hub === undefined ? { origin: 'https://hub.example.ts.net', token: 'sy_p_secrettoken' } : over.hub;
  const u = createUpdater({ link: SyLink, loadHub: async () => hub, kv, clock, installedBuild: over.build ?? 800 });
  return { u, kv, clock };
}

describe('updater', () => {
  it('checks the hub with the paired origin and token and offers a newer build', async () => {
    linkState.update = INFO;
    const { u } = make();
    await u.check();
    expect(u.view().phase).toBe('available');
    expect(u.view().info).toEqual(INFO);
    expect(updateText(u.view())).toBe('Update 0.9.0 available');
    expect(callsOf('link.checkUpdate').map((c) => c.args)).toEqual([['https://hub.example.ts.net', '<token>']]);
  });

  it('offers nothing when the module finds nothing or only a build that is not newer', async () => {
    const { u } = make();
    await u.check();
    expect(u.view().phase).toBe('none');
    expect(updateText(u.view())).toBe('Up to date');
    linkState.update = { ...INFO, build: 800 };
    await u.check();
    expect(u.view().phase).toBe('none');
    expect(u.view().info).toBeNull();
  });

  it('checks once a day: not twice within 24 hours, again after', async () => {
    const { u, clock } = make();
    await u.checkIfDue();
    await u.checkIfDue();
    expect(callsOf('link.checkUpdate')).toHaveLength(1);
    clock.t += DAY_MS - 1;
    await u.checkIfDue();
    expect(callsOf('link.checkUpdate')).toHaveLength(1);
    clock.t += 1;
    await u.checkIfDue();
    expect(callsOf('link.checkUpdate')).toHaveLength(2);
    // Check for update in Settings is never throttled
    await u.check();
    expect(callsOf('link.checkUpdate')).toHaveLength(3);
  });

  it('a failed check is not remembered as a check, so the next start tries again', async () => {
    linkState.updateError = "can't reach the hub or GitHub";
    const { u } = make();
    await u.checkIfDue();
    expect(u.view().phase).toBe('error');
    expect(updateText(u.view())).toBe("can't reach the hub or GitHub");
    linkState.updateError = null;
    await u.checkIfDue();
    expect(callsOf('link.checkUpdate')).toHaveLength(2);
    expect(u.view().phase).toBe('none');
  });

  it('does nothing without a pairing, and never calls the module', async () => {
    const { u } = make({ hub: null });
    await u.check();
    expect(u.view().phase).toBe('error');
    expect(callsOf('link.checkUpdate')).toHaveLength(0);
  });

  it('install hands the offer to the module; started waits for the user in Android', async () => {
    linkState.update = INFO;
    const { u } = make();
    await u.check();
    await u.install();
    expect(callsOf('link.installUpdate').map((c) => c.args)).toEqual([['0.9.0']]);
    expect(u.view().phase).toBe('started');
    expect(u.view().message).toMatch(/confirm/);
  });

  it('a refusal (wrong checksum or signing key) is said plainly and drops the offer', async () => {
    linkState.update = INFO;
    linkState.installResult = 'refused';
    const { u, kv } = make();
    await u.check();
    await u.install();
    expect(u.view().phase).toBe('refused');
    expect(u.view().info).toBeNull();
    expect(updateText(u.view())).toBe(REFUSED_TEXT);
    expect(REFUSED_TEXT).toMatch(/not signed by your key/);
    expect(JSON.parse(kv.map.get(UPDATE_KEY)!).info).toBeNull();
  });

  it('an install error shows the module message without its prefix', async () => {
    linkState.update = INFO;
    linkState.installError = 'check for updates first';
    const { u } = make();
    await u.check();
    await u.install();
    expect(u.view().phase).toBe('error');
    expect(u.view().message).toBe('check for updates first');
  });

  it('remembers the offer across restarts and drops it once that build is installed', async () => {
    linkState.update = INFO;
    const a = make();
    await a.u.check();
    const again = createUpdater({ link: SyLink, loadHub: async () => null, kv: a.kv, clock: a.clock, installedBuild: 800 });
    await again.load();
    expect(again.view().phase).toBe('available');
    expect(again.view().info?.version).toBe('0.9.0');
    const installed = createUpdater({ link: SyLink, loadHub: async () => null, kv: a.kv, clock: a.clock, installedBuild: 900 });
    await installed.load();
    expect(installed.view().info).toBeNull();
    expect(installed.view().phase).toBe('idle');
  });

  it('an offer remembered across a restart still installs: the hub is asked again first', async () => {
    // The module keeps the hub token only in memory from the last checkUpdate (push.md 14.8);
    // after a restart within the day there is no new check, and Install must not fail.
    linkState.update = INFO;
    const a = make();
    await a.u.check();
    linkState.hubAuth = false; // the process restarted
    const hub = { origin: 'https://hub.example.ts.net', token: 'sy_p_secrettoken' };
    const again = createUpdater({ link: SyLink, loadHub: async () => hub, kv: a.kv, clock: a.clock, installedBuild: 800 });
    await again.load();
    await again.checkIfDue(); // checked less than a day ago: nothing
    expect(callsOf('link.checkUpdate')).toHaveLength(1);
    await again.install();
    expect(again.view().phase).toBe('started');
    expect(callsOf('link.checkUpdate')).toHaveLength(2);
    expect(callsOf('link.installUpdate')).toHaveLength(1);
  });

  it('install drops a remembered offer the hub no longer has (unpaired and paired again)', async () => {
    linkState.update = INFO;
    const { u } = make();
    await u.check();
    linkState.update = null; // the new hub has nothing newer
    await u.install();
    expect(callsOf('link.installUpdate')).toHaveLength(0);
    expect(u.view().phase).toBe('none');
    expect(u.view().info).toBeNull();
  });

  it('never stores or shows the token', async () => {
    linkState.update = INFO;
    const { u, kv } = make();
    await u.check();
    expect(JSON.stringify([...kv.map.values()])).not.toContain('secrettoken');
    expect(JSON.stringify(u.view())).not.toContain('secrettoken');
  });
});

async function showSettings(updater?: Updater) {
  const h = makeHarness(updater ? { api: { updater } } : {});
  await render(<SettingsScreen />, { wrapper: h.Wrapper });
  return h.probe;
}

describe('Settings: Updates row', () => {
  it('is absent without an updater (iOS, the Expo profile)', async () => {
    await showSettings();
    await screen.findByText('App version');
    expect(screen.queryByTestId('update-check')).toBeNull();
  });

  it('Check for update shows the offer, its notes and Install', async () => {
    linkState.update = INFO;
    const { u } = make();
    await showSettings(u);
    expect(await screen.findByTestId('update-state')).toHaveTextContent('Not checked yet');
    expect(screen.queryByTestId('update-install')).toBeNull();
    await fireEvent.press(screen.getByTestId('update-check'));
    await waitFor(() => expect(screen.getByTestId('update-state')).toHaveTextContent('Update 0.9.0 available'));
    expect(screen.getByText('Fixes the widget refresh.')).toBeTruthy();
    expect(screen.getByText(/From your hub/)).toBeTruthy();
    expect(screen.getByTestId('update-install')).toHaveTextContent('Install 0.9.0');
  });

  it('says Up to date when nothing is newer', async () => {
    const { u } = make();
    await showSettings(u);
    await fireEvent.press(await screen.findByTestId('update-check'));
    await waitFor(() => expect(screen.getByTestId('update-state')).toHaveTextContent('Up to date'));
    expect(screen.queryByTestId('update-install')).toBeNull();
  });

  it('Install starts the install and then asks for the confirmation in Android', async () => {
    linkState.update = INFO;
    const { u } = make();
    await showSettings(u);
    await fireEvent.press(await screen.findByTestId('update-check'));
    await fireEvent.press(await screen.findByTestId('update-install'));
    await waitFor(() => expect(callsOf('link.installUpdate')).toHaveLength(1));
    await waitFor(() => expect(screen.getByTestId('update-state')).toHaveTextContent(/confirm/));
    expect(screen.queryByTestId('update-install')).toBeNull();
  });

  it('a refused update says it is not signed by your key', async () => {
    linkState.update = INFO;
    linkState.installResult = 'refused';
    const { u } = make();
    await showSettings(u);
    await fireEvent.press(await screen.findByTestId('update-check'));
    await fireEvent.press(await screen.findByTestId('update-install'));
    await waitFor(() => expect(screen.getByTestId('update-state')).toHaveTextContent(/not signed by your key/));
    expect(screen.queryByTestId('update-install')).toBeNull();
  });

  it('shows a check failure in words', async () => {
    linkState.updateError = "can't reach the hub or GitHub";
    const { u } = make();
    await showSettings(u);
    await fireEvent.press(await screen.findByTestId('update-check'));
    await waitFor(() => expect(screen.getByTestId('update-state')).toHaveTextContent("can't reach the hub or GitHub"));
  });
});
