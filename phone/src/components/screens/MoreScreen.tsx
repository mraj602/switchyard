// More (PhoneNow's fourth tab): Alerts, Analytics, Machines, Peers, then Settings last. Owner-only PWA
// screens (Devices, Add phone, rule editing) are not in the app (P4).
import { Pressable, Text, View } from 'react-native';
import { plural } from '../../lib/format';
import { useHubApi } from '../hub-api';
import { Logo } from '../logo';
import { go } from '../nav';
import { useTheme } from '../theme';
import { Card, Muted, Page } from '../ui';

function Item({ label, sub, onPress, testID }: { label: string; sub: string; onPress: () => void; testID: string }) {
  const t = useTheme();
  return (
    <Pressable accessibilityRole="button" accessibilityLabel={label} onPress={onPress} testID={testID}>
      <Card>
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
          <View style={{ flexShrink: 1 }}>
            <Text style={{ color: t.text, fontWeight: '600', fontSize: 16 }}>{label}</Text>
            <Muted>{sub}</Muted>
          </View>
          <Text accessibilityElementsHidden style={{ color: t.muted, fontSize: 22 }}>›</Text>
        </View>
      </Card>
    </Pressable>
  );
}

export default function MoreScreen() {
  const api = useHubApi();
  const t = useTheme();
  const machines = api.data.overview?.machines.length ?? 0;
  return (
    <Page title="More">
      <Item testID="more-alerts" label="Alerts" sub="Which pushes you get, and the recent ones" onPress={go.alerts} />
      <Item testID="more-analytics" label="Analytics" sub="Tokens and API-equivalent cost by day, model and project" onPress={go.analytics} />
      <Item testID="more-machines" label="Machines" sub={`${plural(machines, 'machine')} reporting to this hub`} onPress={() => go.machines()} />
      <Item testID="more-peers" label="Peers" sub="Which accounts may message each other, and Pause all peers" onPress={go.peers} />
      <Item testID="more-settings" label="Settings" sub="Notifications, this phone, app version" onPress={go.settings} />
      <Muted>Devices, Add phone and rule editing are on the web board and the desktop app.</Muted>
      <View style={{ alignItems: 'center', marginTop: 24, opacity: 0.9 }}>
        <Logo size={22} colour={t.muted} />
      </View>
    </Page>
  );
}
