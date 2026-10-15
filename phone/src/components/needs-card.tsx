// A needs-you row (PhoneNow): state, the ask, account and project, alert line, Seen / Snooze
// (also by swiping: right = Seen, left = Snooze), Open in Claude, Copy resume command, View.
import { useEffect, useState } from 'react';
import { Animated, PanResponder, Text, View } from 'react-native';
import type { Session } from '../contract/api';
import { alertText, ago, httpsUrl, projectOf, sessionSeat, stateInfo, type SeatIndex } from '../lib/format';
import { AlertButtons } from './alert-actions';
import { useHubApi } from './hub-api';
import { copyText } from './links';
import { go } from './nav';
import { openInClaude } from './tabs';
import { useTheme } from './theme';
import { Btn, Card, Dot, Muted, StateTag, useToneColour } from './ui';

const THRESHOLD = 88;

/** Swipe right = Seen, left = Snooze, past the threshold (as the PWA's swipe.js). */
export const swipeAction = (dx: number): 'seen' | 'snooze' | null => (dx >= THRESHOLD ? 'seen' : dx <= -THRESHOLD ? 'snooze' : null);

/** What the session wants from you, as one line. Never the internal `detail` code (the board's ask). */
export const ask = (s: Pick<Session, 'question' | 'last_message'>): string => s.question || s.last_message || '';

export function LinkButtons({ session, toast }: { session: Session; toast: (m: string) => void }) {
  const url = httpsUrl(session.remote_url);
  return (
    <>
      {url ? <Btn kind="primary" label="Open in Claude" testID={`open-${session.id}`} onPress={() => void openInClaude(session).catch(() => toast('Could not open the link'))} /> : null}
      {session.resume ? (
        <Btn
          label="Copy resume command"
          testID={`copy-${session.id}`}
          onPress={() => void copyText(session.resume as string).then((ok) => toast(ok ? 'Copied' : 'Copy failed'))}
        />
      ) : null}
    </>
  );
}

export function NeedsCard({ session, seats, now, act, canAct, snoozeMin, toast }: {
  session: Session;
  seats: SeatIndex;
  now: number;
  act: (kind: 'seen' | 'snooze', alertId: string) => void;
  canAct: (s: Session) => boolean;
  snoozeMin: number;
  toast: (m: string) => void;
}) {
  const t = useTheme();
  const tone = useToneColour();
  const info = stateInfo(session);
  const seat = sessionSeat(session, seats);
  const a = session.alert;
  const offline = useHubApi().data.offline;
  const acting = canAct(session) && !offline;
  const seen = a?.status === 'seen';
  const [x] = useState(() => new Animated.Value(0));

  // Latest props for the gesture handlers (created once).
  const [latest] = useState(() => ({ a, acting, act }));
  useEffect(() => {
    Object.assign(latest, { a, acting, act });
  });
  const [pan] = useState(() =>
      PanResponder.create({
        onMoveShouldSetPanResponder: (_e, g) => latest.acting && Math.abs(g.dx) > 12 && Math.abs(g.dx) > Math.abs(g.dy) * 1.5,
        onPanResponderMove: (_e, g) => x.setValue(Math.max(-140, Math.min(140, g.dx))),
        onPanResponderRelease: (_e, g) => {
          Animated.spring(x, { toValue: 0, useNativeDriver: true }).start();
          const { a: al, acting: ok, act: run } = latest;
          if (!ok || !al) return;
          const what = swipeAction(g.dx);
          if (what) run(what, al.id);
        },
        onPanResponderTerminate: () => Animated.spring(x, { toValue: 0, useNativeDriver: true }).start(),
      }),
  );

  return (
    <Animated.View style={{ transform: [{ translateX: x }], opacity: seen ? 0.55 : 1 }} {...pan.panHandlers} testID={`needs-${session.id}`}>
      <Card accent={tone(info.cls)} onPress={() => go.session(session.id, a?.id)} label={`${info.long}: ${projectOf(session)}`}>
        <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
          <StateTag session={session} long />
          <Muted>{ago(session.state_since, now)}</Muted>
        </View>
        <Text style={{ color: t.text, fontSize: 15, fontFamily: session.state === 'needs_permission' ? 'Courier' : undefined }} numberOfLines={4}>
          {ask(session) || 'No details were sent.'}
        </Text>
        <View style={{ flexDirection: 'row', alignItems: 'center' }}>
          <Dot colour={seat.colour} />
          <Muted>{`${seat.label} · ${projectOf(session)}${session.machine_name ? ' · ' + session.machine_name : ''}`}</Muted>
        </View>
        {a ? <Muted>{alertText(a, now)}</Muted> : null}
      </Card>
      <View style={{ gap: 8, marginTop: 6 }}>
        <AlertButtons session={session} act={act} canAct={canAct} snoozeMin={snoozeMin} />
        <LinkButtons session={session} toast={toast} />
      </View>
    </Animated.View>
  );
}
