// Jest setup (phone.md P1, phases/8.md wave 0): every native module the app uses is a small
// recording fake from test/fakes.ts, and the network is off.

jest.mock('expo-notifications', () => jest.requireActual('./fakes').notifications);
jest.mock('expo-secure-store', () => jest.requireActual('./fakes').secureStore);
jest.mock('expo-camera', () => jest.requireActual('./fakes').camera);
jest.mock('expo-widgets', () => jest.requireActual('./fakes').widgets);
jest.mock('@expo/ui/swift-ui', () => jest.requireActual('./fakes').swiftUI);
jest.mock('@expo/ui/swift-ui/modifiers', () => jest.requireActual('./fakes').swiftUIModifiers);
jest.mock('react-native-android-widget', () => jest.requireActual('./fakes').androidWidget);
jest.mock('expo-task-manager', () => jest.requireActual('./fakes').taskManager);
jest.mock('@react-native-async-storage/async-storage', () =>
  jest.requireActual('@react-native-async-storage/async-storage/jest/async-storage-mock'),
);

// No test touches the network (hard rule 6): code under test gets an injected fetch.
const offline = () => Promise.reject(new Error('network is off in tests: inject a fetch'));
(globalThis as { fetch: unknown }).fetch = offline;

beforeEach(() => {
  jest.requireActual<typeof import('./fakes')>('./fakes').resetFakes();
  // the in-memory half of the one-act-per-tap guard (src/lib/notify claimAct)
  jest.requireActual<typeof import('../src/lib/notify')>('../src/lib/notify').resetActed();
});
