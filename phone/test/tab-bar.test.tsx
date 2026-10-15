// The tab bar and page frame on Android 15 edge to edge (T3 emulator findings): no
// missing-icon glyph in the tabs, and the page title clear of the status bar.
import { render, screen } from '@testing-library/react-native';
import { Text } from 'react-native';

const mockTabs: { screenOptions?: Record<string, unknown> } = {};
jest.mock('expo-router', () => {
  const { View } = jest.requireActual('react-native');
  function Tabs({ children, screenOptions }: { children?: unknown; screenOptions?: Record<string, unknown> }) {
    mockTabs.screenOptions = screenOptions;
    return <View>{children as never}</View>;
  }
  Tabs.Screen = function Screen() {
    return null;
  };
  return { Tabs };
});
jest.mock('../src/components/hub-api', () => ({
  useHubApi: () => ({ data: { offline: false, fetchedAt: null }, kick: async () => {}, now: 0 }),
}));

describe('tab bar', () => {
  it('draws no icon, so React Navigation never shows its missing-icon glyph', async () => {
    const TabsLayout = (jest.requireActual('../app/(tabs)/_layout') as { default: () => React.JSX.Element }).default;
    await render(<TabsLayout />);
    const o = mockTabs.screenOptions!;
    expect(o.headerShown).toBe(false);
    expect(typeof o.tabBarIcon).toBe('function');
    expect((o.tabBarIcon as () => unknown)()).toBeNull();
    expect(o.tabBarIconStyle).toEqual({ display: 'none' });
  });
});

describe('Page', () => {
  it('pads for the top safe area (the status bar), not the bottom (the tab bar does)', async () => {
    const { Page } = jest.requireActual('../src/components/ui') as typeof import('../src/components/ui');
    await render(
      <Page title="Now">
        <Text>body</Text>
      </Page>,
    );
    expect(screen.getByText('Now')).toBeTruthy();
    expect(screen.getByTestId('page').props.edges).toMatchObject({ top: 'additive', bottom: 'off' });
  });
});
