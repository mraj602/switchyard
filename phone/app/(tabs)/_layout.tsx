// Phone tabs (phone.md P4): Now, Sessions, Accounts, More. Wave 0 skeleton; owned by WP-S
// from wave 1 on (the screens are app/(tabs)/index.tsx, sessions.tsx, accounts.tsx, more.tsx).
import { Tabs } from 'expo-router';
import type { ColorValue } from 'react-native';
import { TabIcon, type TabIconName } from '../../src/components/tab-icon';
import { useTheme } from '../../src/components/theme';

export const TAB_SCREENS = [
  { name: 'index', title: 'Now' },
  { name: 'sessions', title: 'Sessions' },
  { name: 'accounts', title: 'Accounts' },
  { name: 'more', title: 'More' },
] as const;

export default function TabsLayout() {
  const t = useTheme();
  return (
    // No icon font ships with the app: the icons are drawn from plain Views (tab-icon.tsx), so
    // React Navigation never falls back to its missing-icon glyph. Colours follow the theme.
    <Tabs
      screenOptions={{
        headerShown: false,
        tabBarActiveTintColor: t.text,
        tabBarInactiveTintColor: t.muted,
        tabBarStyle: { backgroundColor: t.card, borderTopColor: t.line },
        tabBarLabelStyle: { fontSize: 12, fontWeight: '600' },
      }}
    >
      {TAB_SCREENS.map((s) => (
        <Tabs.Screen
          key={s.name}
          name={s.name}
          options={{
            title: s.title,
            tabBarIcon: ({ color, focused }: { color: ColorValue; focused: boolean }) => (
              <TabIcon name={s.name as TabIconName} colour={String(color)} accent={focused ? t.accent : null} />
            ),
          }}
        />
      ))}
    </Tabs>
  );
}
