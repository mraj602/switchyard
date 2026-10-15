// Phone tabs (phone.md P4): Now, Sessions, Accounts, More. Wave 0 skeleton; owned by WP-S
// from wave 1 on (the screens are app/(tabs)/index.tsx, sessions.tsx, accounts.tsx, more.tsx).
import { Tabs } from 'expo-router';

export const TAB_SCREENS = [
  { name: 'index', title: 'Now' },
  { name: 'sessions', title: 'Sessions' },
  { name: 'accounts', title: 'Accounts' },
  { name: 'more', title: 'More' },
] as const;

export default function TabsLayout() {
  return (
    // No icon set ships with the app: without tabBarIcon React Navigation draws its
    // missing-icon glyph (a box, and "⏷" in the accessibility label). Labels only.
    <Tabs screenOptions={{ headerShown: false, tabBarIcon: () => null, tabBarIconStyle: { display: 'none' } }}>
      {TAB_SCREENS.map((t) => (
        <Tabs.Screen key={t.name} name={t.name} options={{ title: t.title }} />
      ))}
    </Tabs>
  );
}
