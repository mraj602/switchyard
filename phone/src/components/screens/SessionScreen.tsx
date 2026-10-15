// Session (PhoneSession): state, the question or last message, Open in Claude, Copy resume
// command, Seen / Snooze, the facts and the state history.
import { useEffect, useState } from 'react';
import { Text, View } from 'react-native';
import type { Alert, SessionDetail } from '../../contract/api';
import {
  ago, agoLabel, alertText, dateTime, httpsUrl, humanKey, pct, projectOf, seatIndex, sessionSeat, stateInfo,
} from '../../lib/format';
import { AlertButtons, useAlertActions } from '../alert-actions';
import { useHubApi } from '../hub-api';
import { LinkButtons } from '../needs-card';
import { useTabsSupported } from '../tabs';
import { openTarget } from '../../lib/tabs';
import { useTheme } from '../theme';
import { Btn, Calm, Card, Dot, KV, Muted, Page, Section, StateTag, useToast, useToneColour } from '../ui';

const EVENT_NAMES: Record<string, string> = {
  SessionStart: 'Session started', SessionEnd: 'Session ended', UserPromptSubmit: 'You sent a prompt',
  Stop: 'Claude finished', StopFailure: 'Turn failed', PermissionRequest: 'Permission requested',
  PermissionDenied: 'Permission denied', Notification: 'Notification', PreToolUse: 'Tool starting',
  PostToolUse: 'Tool finished', PreCompact: 'Compacting', PostCompact: 'Compacted',
};

// The events where the state changed, plus the first and the last.
function transitions(tl: SessionDetail['timeline']) {
  return tl.filter((e, i) => i === 0 || i === tl.length - 1 || (e.state_after && e.state_after !== tl[i - 1]?.state_after));
}

export default function SessionScreen({ id, alert: alertParam }: { id: string; alert?: string }) {
  const api = useHubApi();
  const t = useTheme();
  const tone = useToneColour();
  const toast = useToast();
  const { act, canAct, snoozeMin } = useAlertActions(toast.show);
  const [detail, setDetail] = useState<SessionDetail | null>(null);
  const [error, setError] = useState('');
  const [linked, setLinked] = useState<Alert | null>(null);
  const [showAll, setShowAll] = useState(false);
  const tabs = useTabsSupported() === true;

  const client = api.client;
  const stamp = api.data.fetchedAt;
  useEffect(() => {
    if (!client) return;
    let live = true;
    client.session(id).then(
      (d) => {
        if (live) {
          setDetail(d);
          setError('');
        }
      },
      (e: unknown) => {
        if (live) setError((e as { status?: number }).status === 404 ? 'That session no longer exists.' : 'Could not load this session.');
      },
    );
    return () => {
      live = false;
    };
    // Reload after each good refresh (the poller sets fetchedAt) so Seen and Snooze show.
  }, [client, id, stamp]);

  useEffect(() => {
    if (!client || !alertParam) return;
    let live = true;
    client.alert(alertParam).then((a) => live && setLinked(a), () => undefined);
    return () => {
      live = false;
    };
  }, [client, alertParam]);

  // The list row shows first; the detail fills in the history. The list's alert is the live
  // one (optimistic Seen and Snooze land there), so it wins over the detail's.
  const fromList = api.data.sessions.find((x) => x.id === id) ?? api.data.overview?.needs_you.find((x) => x.id === id);
  const s: SessionDetail | null = detail && fromList ? { ...detail, alert: fromList.alert ?? detail.alert } : detail ?? (fromList ? { ...fromList, timeline: [] } : null);

  if (!s) {
    return <Page title="Session"><Calm>{error || 'Loading session…'}</Calm></Page>;
  }

  const idx = seatIndex(api.data.overview?.seats ?? []);
  const seat = sessionSeat(s, idx);
  const info = stateInfo(s);
  const tl = s.timeline ?? [];
  const events = showAll ? tl : transitions(tl);
  const text = s.question || s.last_message;
  const url = httpsUrl(s.remote_url);

  const facts: [string, string | null | undefined][] = [
    ['Machine', s.machine_name],
    ['Folder', s.slot],
    ['Working directory', s.cwd],
    ['Model', s.model],
    ['Kind', s.kind],
    ['Context used', s.context_pct != null ? pct(s.context_pct) : null],
    ['Started', s.started_at ? `${dateTime(s.started_at)} (${agoLabel(s.started_at, api.now)})` : null],
    ['Last event', s.last_event_at ? agoLabel(s.last_event_at, api.now) : null],
    ['Ended', s.ended_at ? dateTime(s.ended_at) : null],
    ['Session id', s.id],
  ];

  return (
    <Page toast={toast.node}>
      <View style={{ gap: 4 }}>
        <Text accessibilityRole="header" style={{ color: t.text, fontSize: 24, fontWeight: '700' }}>
          {projectOf(s)}
          {s.title || s.name ? <Text style={{ color: t.muted, fontWeight: '400' }}>{` · ${s.title || s.name}`}</Text> : null}
        </Text>
        <View style={{ flexDirection: 'row', alignItems: 'center', flexWrap: 'wrap' }}>
          <StateTag session={s} long />
          <Muted>{` for ${ago(s.state_since, api.now)}`}</Muted>
          {s.stale ? <Muted>{' · stale: no events for 12 h'}</Muted> : null}
          {s.machine_stale ? <Muted>{' · machine quiet'}</Muted> : null}
        </View>
        <View style={{ flexDirection: 'row', alignItems: 'center' }}>
          <Dot colour={seat.colour} />
          <Muted>{seat.label}</Muted>
        </View>
      </View>

      <View style={{ gap: 8 }}>
        <LinkButtons session={s} toast={toast.show} />
        <AlertButtons session={s} act={act} canAct={canAct} snoozeMin={snoozeMin} />
      </View>
      {s.alert ? <Muted>{`Alert: ${alertText(s.alert, api.now)}`}</Muted> : null}
      {linked && (!s.alert || s.alert.id !== linked.id) ? <Muted>{`Alert from your push: ${alertText(linked, api.now)}`}</Muted> : null}
      {url ? (
        <Muted>
          {openTarget(s, { supported: tabs })?.kind === 'tab'
            ? `Opens in the claude.ai tab of ${seat.label}.`
            : 'The Claude app is signed in to one account at a time: if it shows another account, use the resume command.'}
        </Muted>
      ) : null}
      {s.resume ? <Text selectable style={{ color: t.text, fontFamily: 'Courier', fontSize: 12 }}>{s.resume}</Text> : null}

      {text ? (
        <Card accent={tone(info.cls)}>
          <Muted>{s.question ? 'Waiting on you' : 'Last message'}</Muted>
          <Text style={{ color: t.text, fontSize: 15, fontFamily: s.state === 'needs_permission' ? 'Courier' : undefined }}>{text}</Text>
        </Card>
      ) : null}

      <Section title="Details">
        <Card>
          {facts.filter(([, v]) => v).map(([k, v]) => <KV key={k} k={k} v={v as string} />)}
        </Card>
      </Section>

      <Section
        title="State history"
        right={tl.length > events.length || showAll ? <Btn label={showAll ? 'State changes only' : `Show all ${tl.length} events`} onPress={() => setShowAll(!showAll)} /> : undefined}
      >
        {events.length === 0 ? (
          <Calm>No events yet.</Calm>
        ) : (
          <Card>
            {[...events].reverse().map((e, i) => (
              <View key={`${e.ts}-${i}`} style={{ paddingVertical: 4 }}>
                <Muted>{dateTime(e.ts)}</Muted>
                <Text style={{ color: t.text }}>
                  {EVENT_NAMES[e.event] || humanKey(e.event)}
                  {e.tool_name ? ` ${e.tool_name}` : ''}
                  {e.subtype ? ` (${e.subtype})` : ''}
                  {e.state_after ? ` → ${humanKey(e.state_after)}` : ''}
                </Text>
                {e.last_message ? <Muted>{e.last_message}</Muted> : null}
              </View>
            ))}
          </Card>
        )}
      </Section>
    </Page>
  );
}
