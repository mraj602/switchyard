// Now (PhoneNow): rings per account, the needs-you inbox, running count per machine.
import { ScrollView, Text, View } from 'react-native';
import { plural, seatIndex, machineStatus, type MachineFull } from '../../lib/format';
import { useAlertActions } from '../alert-actions';
import { useHubApi } from '../hub-api';
import { go } from '../nav';
import { NeedsCard } from '../needs-card';
import { useTheme } from '../theme';
import { Calm, Card, Muted, Page, Ring, Section, useToast, useToneColour } from '../ui';

export default function NowScreen() {
  const api = useHubApi();
  const t = useTheme();
  const tone = useToneColour();
  const toast = useToast();
  const { act, canAct, snoozeMin } = useAlertActions(toast.show);
  const o = api.data.overview;

  if (!o) {
    return (
      <Page title="Now" toast={toast.node}>
        <Calm>{api.data.offline ? 'Offline, and nothing is saved yet.' : 'Loading…'}</Calm>
      </Page>
    );
  }

  const idx = seatIndex(o.seats);
  const seats = o.seats.filter((s) => !s.hidden);
  const needs = o.needs_you;
  const canSwipe = needs.some((s) => canAct(s));

  return (
    <Page title="Now" toast={toast.node}>
      {seats.length ? (
        <ScrollView horizontal showsHorizontalScrollIndicator={false} accessibilityLabel="5-hour limit per account">
          {seats.map((s) => (
            <Ring key={s.id} seat={s} colour={idx.get(s.id)?.colour ?? '#8A9098'} now={api.now} onPress={() => go.accounts(s.id)} />
          ))}
        </ScrollView>
      ) : null}

      <Section title={`Needs you · ${needs.length}`} right={canSwipe ? <Muted>swipe: seen · snooze</Muted> : undefined}>
        {needs.length ? (
          <View style={{ gap: 12 }}>
            {needs.map((s) => (
              <NeedsCard key={s.id} session={s} seats={idx} now={api.now} act={act} canAct={canAct} snoozeMin={snoozeMin} toast={toast.show} />
            ))}
          </View>
        ) : (
          <Calm>All sessions are working or idle. You will see anything that needs an answer here.</Calm>
        )}
      </Section>

      <Section title="Machines">
        {o.machines.length ? (
          o.machines.map((m) => {
            const st = machineStatus(m, api.now);
            return (
              <Card key={m.id} onPress={() => go.machines(m.id)} label={`Machine ${m.name}`}>
                <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
                  <View>
                    <Text style={{ color: t.text, fontWeight: '600' }}>{m.name}</Text>
                    <Muted>{`${plural((m as MachineFull).live_sessions ?? 0, 'session')} running`}</Muted>
                  </View>
                  <Text style={{ color: tone(st.tone), fontSize: 13 }}>{st.text}</Text>
                </View>
              </Card>
            );
          })
        ) : (
          <Calm>No machines have connected yet.</Calm>
        )}
      </Section>
    </Page>
  );
}
