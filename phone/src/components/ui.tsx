// Shared UI: page frame, cards, chips, buttons, meters, rings, toast, offline banner.
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { Pressable, RefreshControl, ScrollView, StyleSheet, Text, View, type StyleProp, type ViewStyle } from 'react-native';
import { useHubApi } from './hub-api';
import { SPACE, useTheme } from './theme';
import { clock, pct, seatName, stateInfo, type StateInfo } from '../lib/format';
import type { Seat, Session, Window } from '../contract/api';

// ---- page ---------------------------------------------------------------------------------

export function OfflineBanner() {
  const api = useHubApi();
  const t = useTheme();
  if (!api.data.offline) return null;
  const at = api.data.fetchedAt;
  const text = at == null ? 'Offline · nothing saved yet' : `Offline · showing ${clock(new Date(at).toISOString(), api.now)}`;
  return (
    <View accessibilityRole="alert" style={[s.banner, { backgroundColor: t.warn }]} testID="offline-banner">
      <Text style={s.bannerText}>{text}</Text>
    </View>
  );
}

export function Page({ title, meta, children, toast }: { title?: string; meta?: string; children: ReactNode; toast?: ReactNode }) {
  const api = useHubApi();
  const t = useTheme();
  const [busy, setBusy] = useState(false);
  const refresh = useCallback(async () => {
    setBusy(true);
    try {
      await api.kick();
    } catch {
      // The poller marks the data offline itself.
    }
    setBusy(false);
  }, [api]);
  return (
    <View style={[s.fill, { backgroundColor: t.bg }]}>
      <OfflineBanner />
      <ScrollView
        contentContainerStyle={s.page}
        refreshControl={<RefreshControl refreshing={busy} onRefresh={refresh} />}
        contentInsetAdjustmentBehavior="automatic"
      >
        {title ? (
          <View style={s.head}>
            <Text accessibilityRole="header" style={[s.h1, { color: t.text }]}>{title}</Text>
            {meta ? <Text style={[s.small, { color: t.muted }]}>{meta}</Text> : null}
          </View>
        ) : null}
        {children}
      </ScrollView>
      {toast}
    </View>
  );
}

export function Section({ title, right, children }: { title: string; right?: ReactNode; children?: ReactNode }) {
  const t = useTheme();
  return (
    <View style={s.section}>
      <View style={s.sectionHead}>
        <Text accessibilityRole="header" style={[s.h2, { color: t.text }]}>{title}</Text>
        {right}
      </View>
      {children}
    </View>
  );
}

export function Card({ children, style, onPress, label, dim, accent }: {
  children: ReactNode; style?: StyleProp<ViewStyle>; onPress?: () => void; label?: string; dim?: boolean; accent?: string;
}) {
  const t = useTheme();
  const body = (
    <View style={[s.card, { backgroundColor: t.card, borderColor: t.line, opacity: dim ? 0.55 : 1 }, accent ? { borderLeftColor: accent, borderLeftWidth: 4 } : null, style]}>
      {children}
    </View>
  );
  if (!onPress) return body;
  return <Pressable accessibilityRole="button" accessibilityLabel={label} onPress={onPress}>{body}</Pressable>;
}

export const Muted = ({ children, style }: { children: ReactNode; style?: StyleProp<ViewStyle> }) => {
  const t = useTheme();
  return <Text style={[s.small, { color: t.muted }, style as never]}>{children}</Text>;
};

export function Calm({ children }: { children: ReactNode }) {
  const t = useTheme();
  return <Text style={[s.calm, { color: t.muted }]}>{children}</Text>;
}

// ---- small parts --------------------------------------------------------------------------

export const Dot = ({ colour }: { colour: string }) => <View accessibilityElementsHidden style={[s.dot, { backgroundColor: colour }]} />;

export function useToneColour() {
  const t = useTheme();
  return (tone: 'ok' | 'warn' | 'bad' | 'muted' | 'needs' | 'working' | 'done' | 'failed' | 'ended') =>
    tone === 'ok' || tone === 'working' ? t.ok : tone === 'warn' ? t.warn : tone === 'bad' || tone === 'failed' ? t.bad : tone === 'needs' ? t.needs : t.muted;
}

export function StateTag({ session, long, info }: { session?: Pick<Session, 'state' | 'detail' | 'needs_you'>; long?: boolean; info?: StateInfo }) {
  const colour = useToneColour();
  const i = info ?? stateInfo(session as Session);
  return (
    <Text style={[s.tag, { color: colour(i.cls) }]}>
      <Text accessibilityElementsHidden>{i.glyph} </Text>
      {long ? i.long : i.label}
    </Text>
  );
}

export function Chip({ label, on, onPress, count, testID }: { label: string; on: boolean; onPress: () => void; count?: number; testID?: string }) {
  const t = useTheme();
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityState={{ selected: on }}
      onPress={onPress}
      testID={testID}
      style={[s.chip, { borderColor: on ? t.accent : t.line, backgroundColor: on ? t.accent : t.card }]}
    >
      <Text style={{ color: on ? t.accentText : t.text, fontSize: 13 }}>{count == null ? label : `${label} ${count}`}</Text>
    </Pressable>
  );
}

export function Btn({ label, onPress, onLongPress, kind = 'plain', disabled, testID }: {
  label: string; onPress: () => void; onLongPress?: () => void; kind?: 'plain' | 'primary' | 'danger'; disabled?: boolean; testID?: string;
}) {
  const t = useTheme();
  const bg = kind === 'primary' ? t.accent : 'transparent';
  const fg = kind === 'primary' ? t.accentText : kind === 'danger' ? t.bad : t.text;
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={label}
      accessibilityState={{ disabled: !!disabled }}
      disabled={disabled}
      onPress={onPress}
      {...(onLongPress ? { onLongPress } : {})}
      testID={testID}
      style={[s.btn, { backgroundColor: bg, borderColor: kind === 'danger' ? t.bad : kind === 'primary' ? t.accent : t.line, opacity: disabled ? 0.45 : 1 }]}
    >
      <Text style={{ color: fg, fontWeight: '600' }}>{label}</Text>
    </Pressable>
  );
}

export function Meter({ value, colour, label }: { value: number | null | undefined; colour: string; label: string }) {
  const t = useTheme();
  const v = Math.min(100, Math.max(0, value ?? 0));
  return (
    <View accessibilityRole="progressbar" accessibilityLabel={label} accessibilityValue={{ min: 0, max: 100, now: value == null ? undefined : Math.round(value) }} style={[s.meter, { backgroundColor: t.track }]}>
      <View style={{ width: `${v}%`, height: '100%', backgroundColor: value != null && value >= 90 ? t.bad : colour, borderRadius: 4 }} />
    </View>
  );
}

export function KV({ k, v }: { k: string; v: ReactNode }) {
  const t = useTheme();
  return (
    <View style={s.kv}>
      <Text style={[s.small, { color: t.muted, flexShrink: 0, marginRight: SPACE.m }]}>{k}</Text>
      {typeof v === 'string' || typeof v === 'number' ? <Text style={[s.value, { color: t.text }]}>{v}</Text> : v}
    </View>
  );
}

/** One account's 5-hour window: a ring-style badge with the percentage and the reset time. */
export function Ring({ seat, colour, now, onPress }: { seat: Seat; colour: string; now: number; onPress?: () => void }) {
  const t = useTheme();
  const w: Window | undefined = (seat.windows ?? []).find((x) => x.key === 'five_hour');
  const used = w && w.used_pct != null ? w.used_pct : null;
  const hot = used != null && used >= 90;
  const name = seatName(seat);
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={`${name}, ${used == null ? 'no data' : pct(used) + ' of the 5-hour window'}`}
      onPress={onPress}
      style={s.ring}
    >
      <View style={[s.ringCircle, { borderColor: used == null ? t.track : colour }]}>
        <Text style={{ color: hot ? t.bad : t.text, fontWeight: '700' }}>{used == null ? '–' : String(Math.round(used))}</Text>
      </View>
      <View style={{ width: 44, marginTop: 4 }}><Meter value={used} colour={colour} label={`${name} 5-hour`} /></View>
      <Text numberOfLines={1} style={[s.small, { color: t.text, marginTop: 2 }]}>{name}</Text>
      <Text style={[s.tiny, { color: hot ? t.bad : t.muted }]}>{w?.resets_at ? clock(w.resets_at, now) : ''}</Text>
    </Pressable>
  );
}

// ---- toast --------------------------------------------------------------------------------

export function useToast(): { show(msg: string): void; node: ReactNode } {
  const [msg, setMsg] = useState('');
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);
  const show = useCallback((m: string) => {
    setMsg(m);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setMsg(''), 3500);
  }, []);
  const node = msg ? (
    <View accessibilityLiveRegion="polite" style={s.toast} testID="toast"><Text style={s.toastText}>{msg}</Text></View>
  ) : null;
  return { show, node };
}

export const s = StyleSheet.create({
  fill: { flex: 1 },
  page: { padding: SPACE.l, paddingBottom: 48, gap: SPACE.m },
  head: { gap: 2, marginBottom: SPACE.xs },
  h1: { fontSize: 28, fontWeight: '700' },
  h2: { fontSize: 17, fontWeight: '700' },
  small: { fontSize: 13 },
  tiny: { fontSize: 11 },
  value: { fontSize: 14, flexShrink: 1, textAlign: 'right' },
  section: { gap: SPACE.s, marginTop: SPACE.s },
  sectionHead: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' },
  card: { borderWidth: StyleSheet.hairlineWidth, borderRadius: 12, padding: SPACE.m, gap: SPACE.s },
  calm: { fontSize: 15, paddingVertical: SPACE.m },
  dot: { width: 9, height: 9, borderRadius: 5, marginRight: 6 },
  tag: { fontSize: 13, fontWeight: '600' },
  chip: { borderWidth: 1, borderRadius: 16, paddingVertical: 6, paddingHorizontal: 12, marginRight: SPACE.s, marginBottom: SPACE.s },
  btn: { borderWidth: 1, borderRadius: 10, paddingVertical: 10, paddingHorizontal: 14, alignItems: 'center', justifyContent: 'center', minHeight: 44 },
  meter: { height: 8, borderRadius: 4, overflow: 'hidden' },
  kv: { flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', paddingVertical: 4 },
  ring: { alignItems: 'center', width: 64, marginRight: SPACE.s },
  ringCircle: { width: 48, height: 48, borderRadius: 24, borderWidth: 5, alignItems: 'center', justifyContent: 'center' },
  banner: { paddingVertical: 6, paddingHorizontal: SPACE.l },
  bannerText: { color: '#FFFFFF', fontSize: 13, fontWeight: '600' },
  toast: { position: 'absolute', left: SPACE.l, right: SPACE.l, bottom: SPACE.xl, backgroundColor: '#222B30', padding: SPACE.m, borderRadius: 10 },
  toastText: { color: '#FFFFFF' },
  row: { flexDirection: 'row', alignItems: 'center', flexWrap: 'wrap' },
  rowBetween: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
});
