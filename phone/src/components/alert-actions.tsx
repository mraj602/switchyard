// Seen and Snooze, shared by Now, Session and the swipe gesture. Optimistic: the reducer is told
// first (the row dims), then the hub; a failure toasts and a refresh puts the truth back.
import { useCallback } from 'react';
import { View } from 'react-native';
import type { Session } from '../contract/api';
import { actionable } from '../lib/format';
import { useHubApi } from './hub-api';
import { Btn } from './ui';

export const DEFAULT_SNOOZE_MIN = 15;

export function useAlertActions(toast: (msg: string) => void) {
  const api = useHubApi();
  const snoozeMin = api.snoozeMin ?? DEFAULT_SNOOZE_MIN;

  const canAct = useCallback(
    (s: Pick<Session, 'alert' | 'needs_you'>): boolean => {
      const a = s.alert;
      if (!a || !api.client) return false;
      if (api.me && !api.me.device.scopes.includes('push')) return false;
      return actionable(a) || (a.status === 'skipped' && s.needs_you);
    },
    [api.client, api.me],
  );

  const act = useCallback(
    async (kind: 'seen' | 'snooze', alertId: string) => {
      const client = api.client;
      if (!client || api.data.offline) return;
      if (kind === 'seen') api.dispatch({ type: 'seen', alert: alertId });
      else api.dispatch({ type: 'snoozed', alert: alertId, until: new Date(api.now + snoozeMin * 60e3).toISOString() });
      try {
        if (kind === 'seen') await client.seen(alertId);
        else await client.snooze(alertId, snoozeMin);
      } catch (e) {
        const status = (e as { status?: number | null }).status;
        toast(status === 409 ? 'That alert already changed' : e instanceof Error ? e.message : 'Could not reach the hub');
      }
      try {
        await api.kick();
      } catch {
        // Offline is the poller's to show.
      }
    },
    [api, snoozeMin, toast],
  );

  return { act, canAct, snoozeMin };
}

export function AlertButtons({ session, act, canAct, snoozeMin }: {
  session: Session;
  act: (kind: 'seen' | 'snooze', alertId: string) => void;
  canAct: (s: Session) => boolean;
  snoozeMin: number;
}) {
  const offline = useHubApi().data.offline;
  const a = session.alert;
  if (!a || !canAct(session)) return null;
  const canSnooze = actionable(a);
  // Offline: the buttons stay but are disabled, as in the PWA (P5).
  return (
    <View style={{ flexDirection: 'row', gap: 8 }}>
      <Btn label="Seen" testID={`seen-${session.id}`} disabled={offline} onPress={() => act('seen', a.id)} />
      {canSnooze ? <Btn label={`Snooze ${snoozeMin} min`} testID={`snooze-${session.id}`} disabled={offline} onPress={() => act('snooze', a.id)} /> : null}
    </View>
  );
}
