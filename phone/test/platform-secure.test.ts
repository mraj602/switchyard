// Regression (found by test/e2e/android): on Android expo-secure-store has no
// AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY constant (it is undefined), and the app crashed at start
// with "secure: keychainAccessible is required". The jest fake defines it like iOS does.
describe('systemSecureHub on Android', () => {
  afterEach(() => {
    jest.resetModules();
    jest.dontMock('expo-secure-store');
  });

  it('is made without the iOS-only constant and still stores a hub', async () => {
    jest.resetModules();
    jest.doMock('expo-secure-store', () => {
      const real = jest.requireActual('./fakes').secureStore;
      return { ...real, AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY: undefined };
    });
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const { systemSecureHub } = require('../src/state/platform') as typeof import('../src/state/platform');
    const store = systemSecureHub();
    await store.save({ origin: 'https://h.ts.net', token: 't', device: 'd', name: 'n', paired_at: 'x' });
    expect((await store.load())?.origin).toBe('https://h.ts.net');
  });
});
