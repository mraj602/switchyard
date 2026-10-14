// app/_layout.tsx (phone.md P3, P6.4, P6.5, P11): the task, the Android widget handler, the
// state hooks and initNotifications once at module scope; unpaired goes to Pair; paired stays
// where it is. expo-router and WP-N's initNotifications are mocked.
import { act, render } from '@testing-library/react-native';
import { SECURE_HUB_KEY } from '../src/contract/constants';
import { secureState } from './fakes';
import { flush } from './hubfake';

const mockReplace = jest.fn();
const mockPush = jest.fn();
let mockSegments: string[] = [];
const mockInit = jest.fn(() => () => {});

jest.mock('expo-router', () => {
  const { View } = jest.requireActual('react-native');
  function Stack({ children }: { children?: unknown }) {
    return <View>{children as never}</View>;
  }
  Stack.Screen = function Screen() {
    return null;
  };
  return { Stack, router: { replace: (...a: unknown[]) => mockReplace(...a), push: (...a: unknown[]) => mockPush(...a) }, useSegments: () => mockSegments };
});
jest.mock('../src/lib/notify', () => ({
  ...jest.requireActual('../src/lib/notify'),
  initNotifications: (...a: unknown[]) => (mockInit as (...x: unknown[]) => unknown)(...a),
}));
jest.mock('../src/lib/links', () => ({ routeToHref: (r: { name: string }) => `/${r.name}` }));

const stored = { origin: 'https://hub.test.ts.net', token: 'sy_p_T', device: 'd', name: 'n', paired_at: 't' };

async function mountLayout() {
  const RootLayout = (jest.requireActual('../app/_layout') as { default: () => React.JSX.Element }).default;
  await render(<RootLayout />);
  await act(async () => {
    await flush(60);
  });
}

beforeEach(() => {
  mockReplace.mockClear();
  mockPush.mockClear();
  mockSegments = [];
  secureState.items.clear();
  globalThis.fetch = (() => Promise.reject(new Error('offline'))) as typeof fetch;
});

describe('RootLayout', () => {
  it('registers notifications once at module scope with the bridge', async () => {
    let fakes!: typeof import('./fakes');
    let hooks!: typeof import('../src/state').stateHooks;
    jest.isolateModules(() => {
      // eslint-disable-next-line @typescript-eslint/no-require-imports
      require('../app/_layout');
      fakes = jest.requireActual('./fakes');
      hooks = (jest.requireActual('../src/state') as typeof import('../src/state')).stateHooks;
    });
    expect(mockInit).toHaveBeenCalledTimes(1);
    const deps = (mockInit.mock.calls[0] as unknown as [{ run: unknown; onReceived: unknown; app: { platform: string }; notifications: unknown }])[0];
    expect(typeof deps.run).toBe('function');
    expect(typeof deps.onReceived).toBe('function');
    expect(deps.notifications).toBeTruthy();
    // the background task is defined and the state hooks are filled, at module scope
    expect(fakes.taskState.tasks.has('SY_NOTIFY_TASK')).toBe(true);
    expect(typeof hooks.afterRefresh).toBe('function');
    expect(typeof hooks.onCleared).toBe('function');
    expect(typeof hooks.onPaired).toBe('function');
    // rendering the layout again does not register again
    await mountLayout();
    expect(mockInit).toHaveBeenCalledTimes(1);
  });

  it('goes to Pair when nothing is paired', async () => {
    await mountLayout();
    expect(mockReplace).toHaveBeenCalledWith('/pair');
  });

  it('stays on Pair without bouncing', async () => {
    mockSegments = ['pair'];
    await mountLayout();
    expect(mockReplace).not.toHaveBeenCalled();
  });

  it('does not redirect while paired, and reads the keychain with the option set', async () => {
    secureState.items.set(SECURE_HUB_KEY, JSON.stringify(stored));
    await mountLayout();
    expect(mockReplace).not.toHaveBeenCalled();
    const { callsOf } = jest.requireActual('./fakes') as typeof import('./fakes');
    const get = callsOf('secure.getItemAsync')[0]!;
    // the fake's AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY constant is 1
    expect(get.args[1]).toEqual({ keychainAccessible: 1 });
  });

  it('goes to Pair on a 401 from the hub', async () => {
    secureState.items.set(SECURE_HUB_KEY, JSON.stringify(stored));
    globalThis.fetch = (async () => ({ status: 401, ok: false, headers: { get: () => null }, text: async () => '{}', json: async () => ({ code: 'unauthorized' }) })) as unknown as typeof fetch;
    await mountLayout();
    expect(mockReplace).toHaveBeenCalledWith('/pair');
    expect(secureState.items.has(SECURE_HUB_KEY)).toBe(false);
  });
});
