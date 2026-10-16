// Machines (PhoneNow's Machines list; docs/spec/machines.md 6.1): reach, last seen, backlog,
// version against the hub and the open silent alert. Read-only: rename, remove and pull are
// owner actions on the board.
import { Text, View } from 'react-native';
import { agoLabel, backlogText, machineAlertText, machineStatus, plural, reachText, seatIndex, seatName, sortMachines, versionLine, type MachineFull } from '../../lib/format';
import { useHubApi } from '../hub-api';
import { useTheme } from '../theme';
import { Calm, Card, KV, Muted, Page, useToneColour } from '../ui';

export default function MachinesScreen({ machine: focus }: { machine?: string }) {
  const api = useHubApi();
  const t = useTheme();
  const tone = useToneColour();
  const o = api.data.overview;
  if (!o) {
    return <Page back title="Machines"><Calm>{api.data.offline ? 'Offline, and nothing is saved yet.' : 'Loading…'}</Calm></Page>;
  }
  const idx = seatIndex(o.seats);
  const list = sortMachines((o.machines as MachineFull[]).filter((m) => !m.removed_at));
  const hubVersion = api.me?.hub.version;
  const seatText = (id?: string | null) => (id && idx.get(id) ? seatName(idx.get(id)!.seat) : id || 'not logged in');

  return (
    <Page back title="Machines" meta={`${plural(list.length, 'machine')} reporting to this hub`}>
      {list.length === 0 ? (
        <Calm>{"No machines have connected yet. Add machines from a computer: sy machine add, or the board's Devices."}</Calm>
      ) : (
        list.map((m) => {
          const st = machineStatus(m, api.now);
          const ver = versionLine(m, hubVersion);
          const reach = reachText(m);
          const backlog = backlogText(m, api.now);
          const alert = machineAlertText(m.alert, api.now);
          const slots = (m as MachineFull & { slots?: { folder: string; logged_in?: boolean; seat?: string | null; launcher?: string | null; claude_version?: string | null }[] }).slots ?? [];
          return (
            <View key={m.id} testID={`machine-${m.id}`}>
              <Card style={focus === m.id ? { borderColor: t.accent, borderWidth: 2 } : undefined}>
                <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'baseline' }}>
                  <Text accessibilityRole="header" style={{ color: t.text, fontSize: 18, fontWeight: '700' }}>{m.name}</Text>
                  <Text style={{ color: tone(st.tone), fontSize: 13 }}>{st.text}</Text>
                </View>
                <Muted>{reach ?? 'reach not reported'}{(m.held ?? 0) > 0 ? ` · ${plural(m.held as number, 'record')} held` : ''}</Muted>
                {alert ? <Text style={{ color: t.warn, fontSize: 13 }}>{alert}</Text> : null}
                <KV k="System" v={[m.os, m.arch].filter(Boolean).join(' · ') || '–'} />
                <KV k="sy version" v={<Text style={{ color: tone(ver.tone), fontSize: 14, flexShrink: 1, textAlign: 'right' }}>{ver.text}</Text>} />
                <KV k="Last record" v={agoLabel(m.last_record, api.now) || '–'} />
                <KV k="Last seen" v={agoLabel(m.last_seen, api.now) || '–'} />
                <KV k="Queued records" v={backlog || (m.backlog == null ? 'not reported' : '0')} />
                <KV k="Live sessions" v={String(m.live_sessions ?? 0)} />
                {slots.length ? (
                  <View>
                    <Muted>Folders</Muted>
                    {slots.map((sl) => (
                      <Text key={sl.folder} style={{ color: t.text, fontSize: 13 }}>
                        {sl.folder}
                        <Text style={{ color: t.muted }}>{` · ${sl.logged_in ? seatText(sl.seat) : 'logged out'}${sl.launcher ? ' · ' + sl.launcher : ''}${sl.claude_version ? ' · Claude ' + sl.claude_version : ''}`}</Text>
                      </Text>
                    ))}
                  </View>
                ) : null}
              </Card>
            </View>
          );
        })
      )}
    </Page>
  );
}
