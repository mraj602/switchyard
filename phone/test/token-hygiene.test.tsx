// Token hygiene (phone.md P3): the device token lives only in expo-secure-store. It never reaches
// logs, AsyncStorage, the cache, the widget keys, a notification call, or an error message.
// The static half (no console in src, app) is also an eslint rule (`no-console`, eslint.config.js).
import AsyncStorage from '@react-native-async-storage/async-storage';
import { act, render } from '@testing-library/react-native';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { KEYCHAIN_ACCESSIBLE } from '../src/contract/constants';
import { createMemorySecureStore, createSecureHub, secureOptions } from '../src/lib/secure';
import type { StoredHub } from '../src/lib/secure';
import * as Notifications from '../modules/sy-notify';
import { Platform } from 'react-native';
import { createGlue } from '../src/lib/integrate';
import type { NotificationsLike } from '../src/lib/notify';
import { HubGlue, HubProvider, installStateHooks, stateHooks, systemGluePlatform, useHub } from '../src/state';
import type { HubState } from '../src/state';
import { calls } from './fakes';
import accountsFx from './fixtures/hub/accounts.json';
import alertsFx from './fixtures/hub/alerts.json';
import meFx from './fixtures/hub/me.json';
import overviewFx from './fixtures/hub/overview.json';
import rulesFx from './fixtures/hub/rules.json';
import sessionsFx from './fixtures/hub/sessions.json';
import { fakeAppState, fakeFetch, flush, manualClock, ORIGIN, response, TOKEN } from './hubfake';

const stored: StoredHub = { origin: ORIGIN, token: TOKEN, device: 'dev_phone', name: 'Pixel · app', paired_at: '2026-10-13T00:00:00.000Z' };

function walk(dir: string): string[] {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) return e.name === 'node_modules' ? [] : walk(p);
    return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) ? [p] : [];
  });
}

describe('the token stays in the keychain', () => {
  it('no source file under src/ or app/ logs (static check)', () => {
    const root = path.resolve(__dirname, '..');
    const files = [...walk(path.join(root, 'src')), ...walk(path.join(root, 'app'))];
    const offenders = files.filter((f) => /\bconsole\s*\./.test(fs.readFileSync(f, 'utf8')));
    expect(offenders.map((f) => path.relative(root, f))).toEqual([]);
  });

  it('a whole session of pairing, registering, polling, widgets, a 401 and an unpair leaves it nowhere else', async () => {
    const os = jest.replaceProperty(Platform, 'OS', 'android'); // widgets write sy.widget.v1
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) => jest.spyOn(console, m).mockImplementation(() => {}));
    await AsyncStorage.clear();
    const secureMem = createMemorySecureStore();
    const secure = createSecureHub(secureMem, secureOptions(KEYCHAIN_ACCESSIBLE));
    let rejectAll = false;
    const f = fakeFetch((url, init) => {
      if (rejectAll) return response(401, { error: `bad token ${TOKEN}`, code: 'unauthorized' });
      if (url.endsWith('/v1/overview')) return response(200, overviewFx);
      if (url.includes('/v1/sessions')) return response(200, sessionsFx);
      if (url.endsWith('/v1/accounts')) return response(200, accountsFx);
      if (url.includes('/v1/alerts')) return response(200, alertsFx);
      if (url.endsWith('/v1/rules')) return response(200, rulesFx);
      if (init.method === 'DELETE') return response(204);
      const device = { ...meFx.device, prefs: { ...meFx.device.prefs, widgets: true } };
      if (url.endsWith('/v1/devices/self')) return response(200, device);
      return response(200, { ...meFx, device });
    });
    const app = { version: '1', build: '1', model: 'P', platform: 'android' as const, dev: false };
    const clock = manualClock();
    const glue = createGlue(systemGluePlatform(app, clock));
    installStateHooks(glue);
    const glueAfter = stateHooks.afterRefresh!;
    const afterRefresh = jest.fn(glueAfter);
    stateHooks.afterRefresh = afterRefresh;
    let latest: HubState | null = null;
    const Probe = () => {
      latest = useHub();
      return null;
    };
    await render(
      <HubProvider
        deps={{ secure, cache: AsyncStorage, fetch: f, clock, appState: fakeAppState(), app, notifications: Notifications as unknown as NotificationsLike }}
        navigate={jest.fn()}
        openUrl={jest.fn()}
      >
        <HubGlue glue={glue} clock={clock}>
          <Probe />
        </HubGlue>
      </HubProvider>,
    );
    await act(async () => {
      await flush(40);
      await latest!.pairWith(stored);
      await flush(80);
      await latest!.refresh();
      await flush(80);
    });
    expect(await AsyncStorage.getItem('sy.widget.v1')).toBeTruthy(); // the widgets ran
    expect(f.calls.some((c) => c.init.method === 'PATCH' && c.url.endsWith('/v1/devices/self'))).toBe(true); // and the registration

    // paired and polled: the cache and every AsyncStorage key are token-free
    expect(secureMem.dump()['sy.hub.v1']).toContain(TOKEN);
    const keys = await AsyncStorage.getAllKeys();
    expect(keys.length).toBeGreaterThan(0);
    for (const k of keys) expect(`${k}=${await AsyncStorage.getItem(k)}`).not.toContain(TOKEN);
    expect(JSON.stringify(afterRefresh.mock.calls)).not.toContain(TOKEN);
    expect(JSON.stringify(latest!.data)).not.toContain(TOKEN);
    expect(JSON.stringify(latest!.hub)).not.toContain(TOKEN);

    // a 401 and an unpair
    rejectAll = true;
    await act(async () => {
      await latest!.refresh();
      await flush(60);
    });
    expect(latest!.status).toBe('unpaired');
    expect(latest!.notice).not.toContain(TOKEN);

    expect(JSON.stringify(calls)).not.toContain(TOKEN); // notification, task and widget fakes
    for (const s of spies) {
      expect(JSON.stringify(s.mock.calls)).not.toContain(TOKEN);
      s.mockRestore();
    }
    os.restore();
    stateHooks.afterRefresh = null;
    stateHooks.onCleared = null;
    stateHooks.onPaired = null;
  });
});
