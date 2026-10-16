// The tab bar and page frame on Android 15 edge to edge (T3 emulator findings): no
// missing-icon glyph in the tabs, and the page title clear of the status bar.
import { render, screen } from '@testing-library/react-native';
import { Text } from 'react-native';

const mockTabs: { screenOptions?: Record<string, unknown>; screens: Record<string, Record<string, unknown>> } = { screens: {} };
jest.mock('expo-router', () => {
  const { View } = jest.requireActual('react-native');
  function Tabs({ children, screenOptions }: { children?: unknown; screenOptions?: Record<string, unknown> }) {
    mockTabs.screenOptions = screenOptions;
    return <View>{children as never}</View>;
  }
  Tabs.Screen = function Screen({ name, options }: { name: string; options: Record<string, unknown> }) {
    mockTabs.screens[name] = options;
    return null;
  };
  return { Tabs };
});
jest.mock('../src/components/hub-api', () => ({
  useHubApi: () => ({ data: { offline: false, fetchedAt: null }, kick: async () => {}, now: 0 }),
}));

describe('tab bar', () => {
  it('gives every tab its own drawn icon, so React Navigation never shows its missing-icon glyph', async () => {
    const TabsLayout = (jest.requireActual('../app/(tabs)/_layout') as { default: () => React.JSX.Element }).default;
    await render(<TabsLayout />);
    const o = mockTabs.screenOptions!;
    expect(o.headerShown).toBe(false);
    expect(o.tabBarIconStyle).toBeUndefined();
    for (const name of ['index', 'sessions', 'accounts', 'more']) {
      const icon = mockTabs.screens[name]!.tabBarIcon as (p: { color: string; focused: boolean }) => React.JSX.Element;
      expect(typeof icon).toBe('function');
      await render(icon({ color: '#000000', focused: true }));
      expect(screen.getByTestId(`tab-icon-${name}`, { includeHiddenElements: true })).toBeTruthy();
    }
  });

  it('tints the bar from the theme, not the platform blue', async () => {
    const { LIGHT } = jest.requireActual('../src/components/theme') as typeof import('../src/components/theme');
    const TabsLayout = (jest.requireActual('../app/(tabs)/_layout') as { default: () => React.JSX.Element }).default;
    await render(<TabsLayout />);
    const o = mockTabs.screenOptions!;
    expect(o.tabBarActiveTintColor).toBe(LIGHT.text);
    expect(o.tabBarStyle).toMatchObject({ backgroundColor: LIGHT.card });
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
    expect(screen.queryByTestId('page-back')).toBeNull();
  });

  it('a stack screen (back) gets a Back button and pads for the gesture bar too', async () => {
    const { Page } = jest.requireActual('../src/components/ui') as typeof import('../src/components/ui');
    await render(
      <Page title="Alerts" back>
        <Text>body</Text>
      </Page>,
    );
    expect(screen.getByTestId('page-back')).toBeTruthy();
    expect(screen.getByTestId('page').props.edges).toMatchObject({ top: 'additive', bottom: 'additive' });
  });
});
