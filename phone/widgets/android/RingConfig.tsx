// Configuration screen of the Account ring widget (phone.md P11), opened by Android when the
// widget is placed and from "Reconfigure" (widgetFeatures "reconfigurable"). It lists the
// accounts of the last snapshot (no network, no keychain), stores the pick per widget id
// (wy.widget.ring.v1: a seat id) and draws the widget once before closing.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useEffect, useState } from 'react';
import { Pressable, Text, View } from 'react-native';
import type { WidgetConfigurationScreenProps } from 'react-native-android-widget';

import { DARK } from '../../src/components/theme';
import type { WidgetSnapshot } from '../../src/contract/snapshot';
import { androidProps, readSnapshot } from '../../src/lib/widget';
import { ringSeat, setRingSeat } from '../../src/lib/widget/extra';
import { WyRing } from './WyRing';

const nowMs = (): number => Date.now();

export function RingConfig({ widgetInfo, renderWidget, setResult }: WidgetConfigurationScreenProps) {
  const [snap, setSnap] = useState<WidgetSnapshot | null | undefined>(undefined);
  const [chosen, setChosen] = useState<string | undefined>(undefined);
  const t = DARK;

  useEffect(() => {
    void (async () => {
      setChosen(await ringSeat(AsyncStorage, widgetInfo.widgetId));
      setSnap(await readSnapshot(AsyncStorage));
    })();
  }, [widgetInfo.widgetId]);

  const pick = async (id: string) => {
    await setRingSeat(AsyncStorage, widgetInfo.widgetId, id);
    renderWidget(<WyRing {...androidProps(snap ?? null, nowMs(), undefined, widgetInfo.height, widgetInfo.width, id)} />);
    setResult('ok');
  };

  return (
    <View style={{ flex: 1, backgroundColor: t.bg, padding: 20, paddingTop: 48 }}>
      <Text style={{ color: t.text, fontSize: 20, fontWeight: '700' }}>Which account?</Text>
      <Text style={{ color: t.muted, fontSize: 13, marginTop: 4, marginBottom: 16 }}>The ring shows the tighter of its 5 h and 7 d limits.</Text>
      {snap === undefined ? null : snap === null || snap.seats.length === 0 ? (
        <Text style={{ color: t.muted, fontSize: 14 }}>Open Watchyard once to load your accounts, then add the widget again.</Text>
      ) : (
        snap.seats.map((s) => (
          <Pressable
            key={s.id}
            onPress={() => void pick(s.id)}
            accessibilityRole="button"
            style={{ flexDirection: 'row', alignItems: 'center', backgroundColor: t.card, borderColor: s.id === chosen ? t.accent : t.line, borderWidth: 1, borderRadius: 12, padding: 14, marginBottom: 8 }}
          >
            <View style={{ width: 12, height: 12, borderRadius: 6, backgroundColor: s.colour, marginRight: 12 }} />
            <Text style={{ color: t.text, fontSize: 16, fontWeight: '600', flex: 1 }}>{s.label}</Text>
            {s.id === chosen ? <Text style={{ color: t.accent, fontSize: 13 }}>current</Text> : null}
          </Pressable>
        ))
      )}
      <Pressable onPress={() => setResult('cancel')} accessibilityRole="button" style={{ marginTop: 12, padding: 12, alignItems: 'center' }}>
        <Text style={{ color: t.muted, fontSize: 15 }}>Cancel</Text>
      </Pressable>
    </View>
  );
}
