// Accounts (PhoneAccounts; designs Phone-Accounts-Compact, Phone-Account-Detail, Phone-11/12): a
// calm list, one row per login: its dot, name and one bar for the tighter of the 5 h and 7 d
// limits with that window's reset time, and one status line only when something needs attention.
// Tapping a row opens it in place: every window with its reset, pace and peak, where the login
// is signed in (Claude Code on each machine, from the hub, and this phone's claude.ai tab, P17.7)
// with the matching action (Open claude.ai, Sign in on this phone, Fix this tab: the Phone-12
// sheet), the details, and "Count down on Lock Screen" where the Live Activity qualifies (iOS).
// "+ Add account" is in the header (a tab for a hub account this phone has none for).
import { useState } from 'react';
import { LayoutAnimation, Modal, Pressable, Text, View } from 'react-native';
import type { Seat } from '../../contract/api';
import {
  agoLabel, clock, mostRoom, paceInfo, pct, planLabel, plural, seatIndex, seatName, sortWindows, tighterWindow, until, windowLabel, worstPace,
} from '../../lib/format';
import {
  attention, codeLine, fixChoices, phoneTab, phoneTabLine, TAB_LOGIN, TAB_PRIVACY_NOTE, tabAction, type Line, type PhoneTab,
} from '../../lib/tabs';
import { useHubApi } from '../hub-api';
import { go } from '../nav';
import { giveTab, profilesChanged, signOutTab, useTabMemos, useTabProfiles } from '../tab-memo';
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
  const [open, setOpen] = useState<Set<string>>(() => new Set(focus ? [focus] : []));
  const o = api.data.overview;
  const accounts = api.data.accounts.length ? api.data.accounts : o?.seats ?? [];
  if (!o && !accounts.length) {
    return <Page title="Accounts"><Calm>{api.data.offline ? 'Offline, and nothing is saved yet.' : 'Loading…'}</Calm></Page>;
  }
  const idx = seatIndex(accounts);
  const seats = accounts.filter((s) => !s.hidden);
  const best = mostRoom(seats, api.now);
  const tabOf = (s: Seat) => phoneTab(s, accounts, { supported: tabs, profiles, memo: memos?.[s.id] });
  const fixSeat = fixing ? seats.find((s) => s.id === fixing) ?? null : null;
  const allOpen = seats.length > 0 && seats.every((s) => open.has(s.id));
  const toggle = (ids: string[], on: boolean) => {
    LayoutAnimation.configureNext(LayoutAnimation.Presets.easeInEaseOut);
    setOpen((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (on) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  };

  return (
    <Page
      title="Accounts"
      toast={toast.node}
      right={tabs ? <AddAccount seats={seats} profiles={profiles} /> : undefined}
    >
      {seats.length > 1 ? (
        <Pressable
          accessibilityRole="button"
          testID="expand-all"
          onPress={() => toggle(seats.map((x) => x.id), !allOpen)}
          style={{ alignSelf: 'flex-start', paddingVertical: 2 }}
        >
          <Text style={{ color: t.muted, fontSize: 14, fontWeight: '600' }}>{allOpen ? 'Collapse all' : 'Expand all'}</Text>
        </Pressable>
      ) : null}
      {best ? (
        <Text testID="most-room" style={{ color: t.text }}>
          {'Most room right now: '}
          <Text style={{ fontWeight: '700' }}>{seatName(best.seat)}</Text>
          {` (5 h ${pct(best.five)} · 7 d ${pct(best.seven)})`}
        </Text>
      ) : null}
      {seats.length === 0 ? (
        <Calm>No accounts yet.</Calm>
      ) : (
        <View style={{ borderTopWidth: 1, borderTopColor: t.line }}>
          {seats.map((s) => (
            <AccountRow
              key={s.id}
              seat={s}
              colour={idx.get(s.id)?.colour ?? '#8A9098'}
              now={api.now}
              highlight={focus === s.id}
              expanded={open.has(s.id)}
              onToggle={() => toggle([s.id], !open.has(s.id))}
              canCountdown={!!api.countdown?.can(s.id)}
              tab={tabOf(s)}
              onTab={() => go.tab(s.id)}
              onFix={() => setFixing(s.id)}
              onCountdown={() => api.countdown?.start(s.id).then(() => toast.show('Counting down on the Lock Screen'), () => toast.show('Could not start the countdown'))}
            />
          ))}
        </View>
      )}
      {seats.length ? <Muted>The bar shows the tighter of the 5 h and 7 d limits. Tap an account for everything else.</Muted> : null}
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

/**
 * + Add account: hub accounts this phone has no claude.ai tab for yet. Picking one opens that
 * seat's tab at claude.ai (the sign-in is the person's, in the tab); the app never signs in or
 * reads the page. An account the hub does not list has not been used yet on a computer.
 * `profiles` is the screen's useTabProfiles: listed by owner, so a profile moveProfile gave to
 * another seat counts for that seat, and a seat whose profile was given away is offered again.
 */
function AddAccount({ seats, profiles }: { seats: Seat[]; profiles: string[] | null }) {
  const t = useTheme();
  const [open, setOpen] = useState(false);
  const show = () => {
    setOpen(true);
    profilesChanged(); // fresh from the module
  };
  const todo = seats.filter((s) => profiles && !profiles.includes(s.id));
  return (
    <>
      <Btn kind="outline" label="+ Add account" testID="add-account" onPress={show} />
      <Modal visible={open} transparent animationType="slide" onRequestClose={() => setOpen(false)}>
        <Pressable accessibilityLabel="Close" style={{ flex: 1, backgroundColor: 'rgba(0,0,0,0.5)' }} onPress={() => setOpen(false)} />
        <View testID="add-account-sheet" style={{ backgroundColor: t.bg, padding: 16, gap: 10, borderTopLeftRadius: 16, borderTopRightRadius: 16, maxHeight: '75%' }}>
          <Text accessibilityRole="header" style={{ color: t.text, fontSize: 18, fontWeight: '700' }}>Which account?</Text>
          {profiles === null ? (
            <Muted>Loading…</Muted>
          ) : todo.length === 0 ? (
            <Muted>{seats.length === 0 ? 'No accounts on this hub yet.' : 'Every account on this hub already has a tab on this phone. Open it from its card.'}</Muted>
          ) : (
            todo.map((s) => (
              <Card
                key={s.id}
                label={`Add ${seatName(s)}`}
                onPress={() => {
                  setOpen(false);
                  go.tab(s.id);
                }}
              >
                <Text testID={`add-seat-${s.id}`} style={{ color: t.text, fontWeight: '700' }}>{seatName(s)}</Text>
                <Muted>{[s.email, planLabel(s.plan), s.org_name].filter(Boolean).join(' · ')}</Muted>
              </Card>
            ))
          )}
          <Muted>Not listed? An account appears after it is used once in Claude Code or the desktop app.</Muted>
          <Btn label="Close" testID="add-account-close" onPress={() => setOpen(false)} />
        </View>
      </Modal>
    </>
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

function AccountRow({ seat, colour, now, highlight, expanded, onToggle, canCountdown, onCountdown, tab, onTab, onFix }: {
  seat: Seat; colour: string; now: number; highlight: boolean; expanded: boolean; onToggle: () => void; canCountdown: boolean;
  onCountdown: () => void; tab: PhoneTab; onTab: () => void; onFix: () => void;
}) {
  const t = useTheme();
  const tone = useToneColour();
  const ws = sortWindows(seat.windows ?? []);
  const name = seatName(seat);
  const worst = worstPace(ws, now);
  const tight = tighterWindow(seat, now);
  const resetAt = tight?.w?.resets_at && Date.parse(tight.w.resets_at) > now ? tight.w.resets_at : null;
  const att = attention(seat, tab, now);
  const attColour = tab.kind === 'other' || tab.kind === 'stranger' ? t.needs : tight && tight.used >= 90 ? t.bad : t.needs;
  const webAt = seat.web_raw_at;
  const tabLine = phoneTabLine(tab);
  const action = tabAction(tab);
  const seen = tabSeen(tab, now);
  const hot = !!tight && tight.used >= 90;
  return (
    <View
      testID={`seat-${seat.id}`}
      style={{ borderBottomWidth: 1, borderBottomColor: t.line, backgroundColor: highlight ? t.card : undefined }}
    >
      <Pressable
        testID={`row-${seat.id}`}
        accessibilityRole="button"
        accessibilityLabel={name}
        accessibilityHint={expanded ? 'Hides the details' : 'Shows every limit and where it is signed in'}
        accessibilityState={{ expanded }}
        onPress={onToggle}
        style={{ paddingVertical: 11, gap: 6 }}
      >
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: 10 }}>
          <Dot colour={colour} />
          <Text accessibilityRole="header" style={{ color: t.text, fontSize: 16, fontWeight: '700', flexShrink: 1 }}>{name}</Text>
          <View style={{ marginLeft: 'auto', flexDirection: 'row', alignItems: 'baseline', gap: 8 }}>
            {tight ? <Text style={{ color: t.muted, fontSize: 13 }}>{tight.short}</Text> : null}
            <Text testID={`pct-${seat.id}`} style={{ color: hot ? t.bad : t.text, fontSize: 15, fontWeight: '600' }}>{tight ? pct(tight.used) : 'no data'}</Text>
            <Text accessibilityElementsHidden style={{ color: t.muted, fontSize: 16, width: 12 }}>{expanded ? '⌄' : '›'}</Text>
          </View>
        </View>
        {tight ? (
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 10 }}>
            <View style={{ flex: 1 }}>
              <Meter value={tight.used} colour={colour} label={`${name} ${tight.short} used`} />
            </View>
            {resetAt ? <Text testID={`reset-${seat.id}`} style={{ color: t.muted, fontSize: 12 }}>{`resets ${clock(resetAt, now)}`}</Text> : null}
          </View>
        ) : null}
        {att ? (
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}>
            <Text accessibilityElementsHidden style={{ color: attColour, fontWeight: '700', fontSize: 13 }}>!</Text>
            <Text testID={`attention-${seat.id}`} style={{ color: attColour, fontSize: 13, flexShrink: 1 }}>
              {att.line.parts.map((p, i) => (
                <Text key={i} style={p.b ? { fontWeight: '700' } : undefined}>{p.t}</Text>
              ))}
              {att.more ? <Text style={{ color: t.muted }}>{`  +${att.more}`}</Text> : null}
            </Text>
          </View>
        ) : null}
      </Pressable>
      {expanded ? (
        <View testID={`detail-${seat.id}`} style={{ paddingBottom: 14, paddingLeft: 18, gap: 10 }}>
          {ws.length ? (
            ws.map((w) => {
              const p = paceInfo(w, now);
              return (
                <View key={w.key} style={{ gap: 4 }}>
                  <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
                    <Text style={{ color: t.text }}>{windowLabel(w)}</Text>
                    <Text style={{ color: w.used_pct != null && w.used_pct >= 90 ? t.bad : t.text }}>
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
          <View style={{ gap: 6 }}>
            <Text style={{ color: t.text, fontWeight: '700' }}>Signed in</Text>
            <StatusLine line={codeLine(seat)} testID={`code-${seat.id}`} />
            {tabLine ? <StatusLine line={tabLine} testID={`phonetab-${seat.id}`} /> : null}
            {seen ? <Muted style={{ marginLeft: 24 }}>{seen}</Muted> : null}
            {action === 'open' ? <Btn label="Open claude.ai" testID={`tab-${seat.id}`} onPress={onTab} /> : null}
            {action === 'sign_in' ? <Btn kind="primary" label="Sign in on this phone" testID={`tab-${seat.id}`} onPress={onTab} /> : null}
            {action === 'fix' ? <Btn kind="danger" label="Fix this tab" testID={`fix-${seat.id}`} onPress={onFix} /> : null}
          </View>
          <View style={{ gap: 2 }}>
            <Muted>{[planLabel(seat.plan), seat.org_name].filter(Boolean).join(' · ')}</Muted>
            {seat.email ? <Muted>{seat.email}</Muted> : null}
            <Muted>{seat.live_sessions ? plural(seat.live_sessions, 'live session') : 'No live sessions'}</Muted>
            <Muted>{webAt ? `claude.ai usage read ${agoLabel(webAt, now)}` : 'No claude.ai web usage: figures come from the status line only.'}</Muted>
          </View>
          {canCountdown ? <Btn label="Count down on Lock Screen" testID={`countdown-${seat.id}`} onPress={onCountdown} /> : null}
        </View>
      ) : null}
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
