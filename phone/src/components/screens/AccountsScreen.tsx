// Accounts (PhoneAccounts): one card per login, "most room", every limit window with its
// reset and pace, "Open claude.ai" (the seat's account tab, P17, Android) and "Count down on
// Lock Screen" where the Live Activity qualifies (iOS).
import { Text, View } from 'react-native';
import type { Seat, Window } from '../../contract/api';
import {
  agoLabel, clock, mostRoom, paceInfo, pct, plural, seatIndex, seatName, sortWindows, until, windowLabel, worstPace,
} from '../../lib/format';
import { useHubApi } from '../hub-api';
import { go } from '../nav';
import { useTabsSupported } from '../tabs';
import { useTheme } from '../theme';
import { Btn, Calm, Card, Meter, Muted, Page, useToast, useToneColour } from '../ui';

export default function AccountsScreen({ seat: focus }: { seat?: string }) {
  const api = useHubApi();
  const t = useTheme();
  const toast = useToast();
  const tabs = useTabsSupported() === true;
  const o = api.data.overview;
  const accounts = api.data.accounts.length ? api.data.accounts : o?.seats ?? [];
  if (!o && !accounts.length) {
    return <Page title="Accounts"><Calm>{api.data.offline ? 'Offline, and nothing is saved yet.' : 'Loading…'}</Calm></Page>;
  }
  const idx = seatIndex(accounts);
  const seats = accounts.filter((s) => !s.hidden);
  const best = mostRoom(seats, api.now);
  const week = best?.seat.windows?.find((w) => w.key === 'seven_day');
  const left = (w: Window) => pct(100 - (w.used_pct ?? 0));

  return (
    <Page title="Accounts" meta="One card per login. Folders and tabs with the same login share a card." toast={toast.node}>
      {best ? (
        <Text testID="most-room" style={{ color: t.text }}>
          {'Most room right now: '}
          <Text style={{ fontWeight: '700' }}>{seatName(best.seat)}</Text>
          {` (${left(best.five)} of 5h${week && week.used_pct != null ? `, ${left(week)} of week` : ''})`}
        </Text>
      ) : null}
      {seats.length === 0 ? (
        <Calm>No accounts yet.</Calm>
      ) : (
        seats.map((s) => (
          <AccountCard
            key={s.id}
            seat={s}
            colour={idx.get(s.id)?.colour ?? '#8A9098'}
            now={api.now}
            highlight={focus === s.id}
            canCountdown={!!api.countdown?.can(s.id)}
            onTab={tabs ? () => go.tab(s.id) : undefined}
            onCountdown={() => api.countdown?.start(s.id).then(() => toast.show('Counting down on the Lock Screen'), () => toast.show('Could not start the countdown'))}
          />
        ))
      )}
    </Page>
  );
}

function AccountCard({ seat, colour, now, highlight, canCountdown, onCountdown, onTab }: {
  seat: Seat; colour: string; now: number; highlight: boolean; canCountdown: boolean; onCountdown: () => void; onTab?: () => void;
}) {
  const t = useTheme();
  const tone = useToneColour();
  const ws = sortWindows(seat.windows ?? []);
  const name = seatName(seat);
  const worst = worstPace(ws, now);
  const places = [...new Set(((seat as Seat & { places?: { machine_name?: string }[] }).places ?? []).map((p) => p.machine_name).filter(Boolean))].join(', ');
  const webAt = (seat as Seat & { web_raw_at?: string | null }).web_raw_at;
  return (
    <View testID={`seat-${seat.id}`} accessibilityLabel={name}>
      <Card accent={colour} style={highlight ? { borderColor: colour, borderWidth: 2 } : undefined}>
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'baseline' }}>
          <Text accessibilityRole="header" style={{ color: t.text, fontSize: 18, fontWeight: '700' }}>{name}</Text>
          <Muted>{[seat.plan, seat.org_name].filter(Boolean).join(' · ')}</Muted>
        </View>
        <Muted>
          {[seat.email, places ? `used on ${places}` : '', seat.live_sessions ? plural(seat.live_sessions, 'live session') : ''].filter(Boolean).join(' · ')}
        </Muted>
        {ws.length ? (
          ws.map((w) => {
            const p = paceInfo(w, now);
            return (
              <View key={w.key} style={{ gap: 4, marginTop: 6 }}>
                <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
                  <Text style={{ color: t.text }}>{windowLabel(w)}</Text>
                  <Text style={{ color: w.used_pct != null && w.used_pct >= 90 ? t.bad : t.text }}>
                    {w.used_pct == null ? 'no data' : `${pct(w.used_pct)}${w.resets_at ? ` · ${clock(w.resets_at, now)}` : ''}${w.used_pct >= 90 ? ' high' : ''}`}
                  </Text>
                </View>
                <Meter value={w.used_pct} colour={colour} label={`${name} ${windowLabel(w)} used`} />
                {w.resets_at ? (
                  <Muted>{`resets ${until(w.resets_at, now)} (${clock(w.resets_at, now)})${w.peak_pct != null ? ` · peak ${pct(w.peak_pct)}` : ''}`}</Muted>
                ) : null}
                {p ? <Text style={{ color: tone(p.cls), fontSize: 13 }}>{p.text}</Text> : null}
              </View>
            );
          })
        ) : (
          <Muted>No limit data yet.</Muted>
        )}
        {worst && worst.p.band === 'runaway' ? <Text style={{ color: t.bad, fontWeight: '600' }}>{worst.p.text}</Text> : null}
        <Muted>{webAt ? `claude.ai usage read ${agoLabel(webAt, now)}` : 'No claude.ai web usage: figures come from the status line only.'}</Muted>
        {onTab ? <Btn label="Open claude.ai" testID={`tab-${seat.id}`} onPress={onTab} /> : null}
        {canCountdown ? <Btn label="Count down on Lock Screen" testID={`countdown-${seat.id}`} onPress={onCountdown} /> : null}
      </Card>
    </View>
  );
}
