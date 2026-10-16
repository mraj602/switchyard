// Accounts (PhoneAccounts; design Phone-11): one card per login, "most room", every limit window with its
// reset and pace, where the login is signed in (Claude Code on each machine, from the hub, and
// this phone's claude.ai tab, P17.7) with the matching action (Open claude.ai, Sign in on this
// phone, Fix this tab: the Phone-12 sheet), and "Count down on Lock Screen" where the Live Activity
// qualifies (iOS).
import { useState } from 'react';
import { Modal, Pressable, Text, View } from 'react-native';
import type { Seat, Window } from '../../contract/api';
import {
  agoLabel, clock, mostRoom, paceInfo, pct, planLabel, plural, seatIndex, seatName, sortWindows, until, windowLabel, worstPace,
} from '../../lib/format';
import {
  codeLine, fixChoices, phoneTab, phoneTabLine, TAB_LOGIN, TAB_PRIVACY_NOTE, tabAction, type Line, type PhoneTab,
} from '../../lib/tabs';
import { useHubApi } from '../hub-api';
import { go } from '../nav';
import { giveTab, signOutTab, useTabMemos, useTabProfiles } from '../tab-memo';
import { useTabsSupported } from '../tabs';
import { useTheme } from '../theme';
import { Btn, Calm, Card, Dot, Meter, Muted, Page, useToast, useToneColour } from '../ui';

export default function AccountsScreen({ seat: focus }: { seat?: string }) {
  const api = useHubApi();
  const t = useTheme();
  const toast = useToast();
  const supported = useTabsSupported();
  const tabs = supported === true;
  const profiles = useTabProfiles(supported);
  const memos = useTabMemos();
  const [fixing, setFixing] = useState<string | null>(null);
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
  const tabOf = (s: Seat) => phoneTab(s, accounts, { supported: tabs, profiles, memo: memos?.[s.id] });
  const fixSeat = fixing ? seats.find((s) => s.id === fixing) ?? null : null;

  return (
    <Page
      title="Accounts"
      meta="One card per login. Each shows where it is signed in: Claude Code on your machines, and its claude.ai tab on this phone."
      toast={toast.node}
    >
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
            tab={tabOf(s)}
            onTab={() => go.tab(s.id)}
            onFix={() => setFixing(s.id)}
            onCountdown={() => api.countdown?.start(s.id).then(() => toast.show('Counting down on the Lock Screen'), () => toast.show('Could not start the countdown'))}
          />
        ))
      )}
      {fixSeat ? (
        <FixSheet
          seat={fixSeat}
          tab={tabOf(fixSeat)}
          profiles={profiles}
          colourOf={(id) => idx.get(id)?.colour ?? '#8A9098'}
          onClose={() => setFixing(null)}
          onSignOut={() => {
            setFixing(null);
            signOutTab(fixSeat.id).then(
              () => go.tab(fixSeat.id, TAB_LOGIN),
              (e: unknown) => toast.show((e as { code?: string }).code === 'ERR_SY_TABS_IN_USE' ? 'Close the tab first, then try again' : 'Could not clear the tab'),
            );
          }}
          onUse={(other) => {
            setFixing(null);
            giveTab(fixSeat.id, other.id).then(
              () => toast.show(`This tab is now ${seatName(other)}'s`),
              () => toast.show('Could not move the tab'),
            );
          }}
        />
      ) : null}
    </Page>
  );
}

/** One status line (Phone-11): an icon by tone, then the words, names in bold. */
function StatusLine({ line, testID }: { line: Line; testID: string }) {
  const t = useTheme();
  const colour = line.tone === 'ok' ? t.ok : line.tone === 'warn' ? t.needs : t.muted;
  return (
    <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: 8 }}>
      <View accessibilityElementsHidden importantForAccessibility="no-hide-descendants" style={{ width: 16, alignItems: 'center', marginTop: 2 }}>
        {line.tone === 'ok' ? (
          <Text style={{ color: colour, fontWeight: '700', fontSize: 14, lineHeight: 16 }}>✓</Text>
        ) : line.tone === 'warn' ? (
          <View style={{ width: 15, height: 15, borderRadius: 8, backgroundColor: colour, alignItems: 'center', justifyContent: 'center' }}>
            <Text style={{ color: '#FFFFFF', fontWeight: '700', fontSize: 11, lineHeight: 13 }}>!</Text>
          </View>
        ) : (
          <View style={{ width: 13, height: 13, borderRadius: 7, borderWidth: 2, borderColor: colour, marginTop: 1 }} />
        )}
      </View>
      <Text testID={testID} style={{ color: line.tone === 'ok' ? t.text : colour, fontSize: 14, flexShrink: 1 }}>
        {line.parts.map((p, i) => (
          <Text key={i} style={p.b ? { fontWeight: '700' } : undefined}>{p.t}</Text>
        ))}
      </Text>
    </View>
  );
}

/** The phone tab line is what the tab showed when it was last open: say when that was. */
const tabSeen = (v: PhoneTab, now: number): string | null => ('at' in v ? `as of the tab's last visit, ${agoLabel(new Date(v.at).toISOString(), now)}` : null);

function AccountCard({ seat, colour, now, highlight, canCountdown, onCountdown, tab, onTab, onFix }: {
  seat: Seat; colour: string; now: number; highlight: boolean; canCountdown: boolean; onCountdown: () => void;
  tab: PhoneTab; onTab: () => void; onFix: () => void;
}) {
  const t = useTheme();
  const tone = useToneColour();
  const ws = sortWindows(seat.windows ?? []);
  const name = seatName(seat);
  const worst = worstPace(ws, now);
  // Desktop tabs only: Claude Code's machines have their own line.
  const desktops = [...new Set((seat.places ?? []).filter((p) => p.tab_id).map((p) => p.machine_name).filter(Boolean))].join(', ');
  const webAt = seat.web_raw_at;
  const tabLine = phoneTabLine(tab);
  const action = tabAction(tab);
  const seen = tabSeen(tab, now);
  const wrong = action === 'fix';
  return (
    <View testID={`seat-${seat.id}`} accessibilityLabel={name}>
      <Card
        accent={colour}
        style={highlight ? { borderColor: colour, borderWidth: 2 } : wrong ? { borderColor: t.needs, borderWidth: 1.5, borderLeftColor: colour } : undefined}
      >
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'baseline' }}>
          <Text accessibilityRole="header" style={{ color: t.text, fontSize: 18, fontWeight: '700' }}>{name}</Text>
          <Muted>{[planLabel(seat.plan), seat.org_name].filter(Boolean).join(' · ')}</Muted>
        </View>
        <Muted>
          {[seat.email, desktops ? `desktop tab on ${desktops}` : '', seat.live_sessions ? plural(seat.live_sessions, 'live session') : ''].filter(Boolean).join(' · ')}
        </Muted>
        <View style={{ gap: 6, marginTop: 4 }}>
          <StatusLine line={codeLine(seat)} testID={`code-${seat.id}`} />
          {tabLine ? <StatusLine line={tabLine} testID={`phonetab-${seat.id}`} /> : null}
          {seen ? <Muted style={{ marginLeft: 24 }}>{seen}</Muted> : null}
        </View>
        {ws.length ? (
          ws.map((w) => {
            const p = paceInfo(w, now);
            return (
              <View key={w.key} style={{ gap: 4, marginTop: 6 }}>
                <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
                  <Text style={{ color: t.text }}>{windowLabel(w)}</Text>
                  <Text style={{ color: w.used_pct != null && w.used_pct >= 90 ? t.bad : t.text }}>
                    {/* The reset time is on the line below; red already says the window is nearly full. */}
                    {w.used_pct == null ? 'no data' : `${pct(w.used_pct)} used`}
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
        {action === 'open' ? <Btn label="Open claude.ai" testID={`tab-${seat.id}`} onPress={onTab} /> : null}
        {action === 'sign_in' ? <Btn kind="primary" label="Sign in on this phone" testID={`tab-${seat.id}`} onPress={onTab} /> : null}
        {action === 'fix' ? <Btn kind="danger" label="Fix this tab" testID={`fix-${seat.id}`} onPress={onFix} /> : null}
        {canCountdown ? <Btn label="Count down on Lock Screen" testID={`countdown-${seat.id}`} onPress={onCountdown} /> : null}
      </Card>
    </View>
  );
}

/** Phone-12: the tab of `seat` is signed in as another account. */
function FixSheet({ seat, tab, profiles, colourOf, onClose, onSignOut, onUse }: {
  seat: Seat; tab: PhoneTab; profiles: string[] | null; colourOf: (id: string) => string;
  onClose: () => void; onSignOut: () => void; onUse: (other: Seat) => void;
}) {
  const t = useTheme();
  const choices = fixChoices(tab, profiles);
  if (!choices) return null;
  const name = seatName(seat);
  const other = tab.kind === 'other' ? tab.seat : null;
  const otherName = other ? seatName(other) : null;
  return (
    <Modal transparent animationType="slide" visible onRequestClose={onClose}>
      <Pressable testID="fix-backdrop" accessibilityLabel="Close" style={{ flex: 1, backgroundColor: 'rgba(23,25,27,0.35)' }} onPress={onClose} />
      <View testID="fix-sheet" style={{ backgroundColor: t.card, borderTopLeftRadius: 16, borderTopRightRadius: 16, paddingHorizontal: 18, paddingTop: 12, paddingBottom: 24, gap: 14 }}>
        <View style={{ width: 40, height: 4, borderRadius: 2, backgroundColor: t.line, alignSelf: 'center' }} />
        <Text accessibilityRole="header" style={{ color: t.text, fontSize: 19, fontWeight: '600' }}>
          {otherName ? `${name}'s tab is signed in as ${otherName}` : `${name}'s tab is signed in as another account`}
        </Text>
        <Text style={{ color: t.text, fontSize: 15, lineHeight: 22 }}>
          {otherName ? (
            <>
              {'The claude.ai account in this tab is the one Claude Code uses as '}
              <Text style={{ fontWeight: '700' }}>{otherName}</Text>
              {". Switchyard matched them by the account's ID, not by email."}
            </>
          ) : (
            "The claude.ai account in this tab is not one of the accounts on your hub. Switchyard matched them by the account's ID, not by email."
          )}
        </Text>
        <View style={{ backgroundColor: t.bg, borderRadius: 10, padding: 12, gap: 8 }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
            <Dot colour={colourOf(seat.id)} />
            <Text style={{ color: t.text }}>{'This tab is for '}<Text style={{ fontWeight: '700' }}>{name}</Text></Text>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
            <Dot colour={other ? colourOf(other.id) : '#8A9098'} />
            <Text style={{ color: t.text }}>
              {'It is signed in as '}
              {otherName ? <Text style={{ fontWeight: '700' }}>{otherName}</Text> : 'an account no seat has'}
            </Text>
          </View>
        </View>
        <Btn kind="primary" label={`Sign out here and sign in as ${name}`} testID="fix-signout" onPress={onSignOut} />
        {choices.use ? <Btn label={`Use this tab for ${seatName(choices.use)} instead`} testID="fix-use" onPress={() => onUse(choices.use!)} /> : null}
        <Muted>{TAB_PRIVACY_NOTE}</Muted>
      </View>
    </Modal>
  );
}
