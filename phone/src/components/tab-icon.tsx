// The tab bar's icons, drawn from plain Views (the app ships no icon font or SVG renderer).
// 24 x 24; `colour` is the tab's tint, `accent` (the focused tab only) fills the icon's key part
// and draws a pill behind the icon, so the selected tab reads at a glance.
import { View } from 'react-native';

/** The icon box the tab bar gives each icon: wide enough for the selected tab's pill. */
export const TAB_ICON_BOX = { width: 56, height: 30 } as const;

export type TabIconName = 'index' | 'sessions' | 'accounts' | 'more';

export function TabIcon({ name, colour, accent }: { name: TabIconName; colour: string; accent: string | null }) {
  const key = accent ?? colour;
  const pill = { ...TAB_ICON_BOX, borderRadius: TAB_ICON_BOX.height / 2, backgroundColor: accent ? `${accent}38` : 'transparent' };
  return (
    <View style={{ ...pill, alignItems: 'center', justifyContent: 'center' }} testID={`tab-icon-${name}`} accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
      <View style={{ width: 24, height: 24, alignItems: 'center', justifyContent: 'center' }}>
        {name === 'index' ? (
          // Now: a live dot in a ring.
          <View style={{ width: 20, height: 20, borderRadius: 10, borderWidth: 2, borderColor: colour, alignItems: 'center', justifyContent: 'center' }}>
            <View style={{ width: 8, height: 8, borderRadius: 4, backgroundColor: key }} />
          </View>
        ) : name === 'sessions' ? (
          // Sessions: a list, a state dot before each row.
          <View style={{ gap: 4 }}>
            {[0, 1, 2].map((i) => (
              <View key={i} style={{ flexDirection: 'row', alignItems: 'center', gap: 3 }}>
                <View style={{ width: 4, height: 4, borderRadius: 2, backgroundColor: i === 0 ? key : colour }} />
                <View style={{ width: 14, height: 2, borderRadius: 1, backgroundColor: colour }} />
              </View>
            ))}
          </View>
        ) : name === 'accounts' ? (
          // Accounts: a login (head and shoulders).
          <View style={{ alignItems: 'center', gap: 2 }}>
            <View style={{ width: 9, height: 9, borderRadius: 5, backgroundColor: key }} />
            <View style={{ width: 17, height: 8, borderTopLeftRadius: 8, borderTopRightRadius: 8, borderWidth: 2, borderBottomWidth: 0, borderColor: colour }} />
          </View>
        ) : (
          // More: three dots.
          <View style={{ flexDirection: 'row', gap: 3 }}>
            {[0, 1, 2].map((i) => (
              <View key={i} style={{ width: 5, height: 5, borderRadius: 3, backgroundColor: i === 1 ? key : colour }} />
            ))}
          </View>
        )}
      </View>
    </View>
  );
}
