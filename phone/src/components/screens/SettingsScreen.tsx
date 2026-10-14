// Settings (P4): hub and status, notifications and Send test push, private pushes, tap
// behaviour, own quiet hours, the widgets refresh switch, app version and Unpair.
import { useState } from 'react';
import { Alert as RNAlert, Switch, Text, TextInput, View } from 'react-native';
import type { PushTestResult, QuietHours, TapMode } from '../../contract/api';
import { clock } from '../../lib/format';
import { useHubApi } from '../hub-api';
import { go } from '../nav';
import { useTheme } from '../theme';
import { useSelf } from '../use-self';
import { Btn, Calm, Card, Chip, KV, Muted, Page, Section, useToast } from '../ui';

const PUSH_TEXT: Record<string, string> = {
  registered: 'On',
  blocked: 'Blocked in system settings. Allow notifications for this app there, then come back.',
  'no-project': "This build can't receive pushes.",
  error: 'Could not register this phone. Try again.',
};

const HHMM = /^([01]\d|2[0-3]):[0-5]\d$/;
const deviceTz = (): string | undefined => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || undefined;
  } catch {
    return undefined;
  }
};

export function testText(r: PushTestResult | null, busy: boolean): string {
  if (busy) return 'Sending…';
  if (!r) return '';
  if (r.outcome === 'sent') return 'Sent. It should arrive in a moment.';
  return `The push service said: ${r.error || r.outcome}${r.http_status ? ` (${r.http_status})` : ''}`;
}

export default function SettingsScreen() {
  const api = useHubApi();
  const t = useTheme();
  const toast = useToast();
  const { self, savePrefs } = useSelf(toast.show);
  const [test, setTest] = useState<PushTestResult | null>(null);
  const [testing, setTesting] = useState(false);
  const prefs = self?.prefs ?? {};
  const offline = api.data.offline;
  const off = offline || !self;

  const target = (self?.targets ?? []).find((x) => x.kind === 'expo' && x.status !== 'gone');

  const sendTest = async () => {
    if (!api.client || !target) return;
    setTesting(true);
    setTest(null);
    try {
      setTest(await api.client.testTarget(target.id));
    } catch (e) {
      setTest({ outcome: 'failed', error: e instanceof Error ? e.message : 'failed' });
    }
    setTesting(false);
  };

  const confirmUnpair = () =>
    RNAlert.alert(`Unpair from ${api.host}?`, 'This phone will stop receiving pushes and must be paired again.', [
      { text: 'Cancel', style: 'cancel' },
      { text: 'Unpair', style: 'destructive', onPress: () => void api.unpair() },
    ]);

  const priv = prefs.private == null ? 'default' : prefs.private ? 'on' : 'off';
  const tap: TapMode = prefs.tap ?? 'auto';
  const qh = prefs.quiet_hours;

  return (
    <Page title="Settings" toast={toast.node}>
      <Section title="Hub">
        <Card>
          <KV k="Address" v={api.host} />
          <KV k="Version" v={api.me?.hub.version || '–'} />
          <KV
            k="Status"
            v={offline ? `Offline${api.data.fetchedAt ? ` · last update ${clock(new Date(api.data.fetchedAt).toISOString(), api.now)}` : ''}` : 'Connected'}
          />
        </Card>
      </Section>

      <Section title="Notifications">
        <Card>
          <Text testID="notif-state" style={{ color: t.text }}>
            {api.pushState ? PUSH_TEXT[api.pushState] ?? '' : 'Off. Turn on to get a push when a session needs you.'}
          </Text>
          {api.link ? (
            <>
              <Muted>{"Delivered over this phone's own connection to your hub."}</Muted>
              {api.pushState !== 'registered' && api.pushState !== 'blocked' ? (
                <Btn kind="primary" label="Turn on notifications" testID="turn-on" onPress={() => void api.enablePush()} />
              ) : null}
              <Btn label="Notification settings" testID="notify-settings" onPress={go.notifySettings} />
            </>
          ) : api.pushState === 'registered' ? (
            <>
              <Btn label="Send test push" testID="test-push" disabled={off || !target || testing} onPress={() => void sendTest()} />
              {test || testing ? <Muted>{testText(test, testing)}</Muted> : null}
            </>
          ) : api.pushState === 'blocked' || api.pushState === 'no-project' ? null : (
            <Btn kind="primary" label="Turn on notifications" testID="turn-on" onPress={() => void api.enablePush()} />
          )}
        </Card>
      </Section>

      <Section title="This phone">
        {!self ? (
          <Calm>Loading…</Calm>
        ) : (
          <Card>
            <Text style={{ color: t.text, fontWeight: '600' }}>Private pushes</Text>
            <Muted>{'The lock screen says only "1 session needs you"; details load from your hub when opened.'}</Muted>
            <View style={{ flexDirection: 'row', flexWrap: 'wrap' }}>
              {(['default', 'on', 'off'] as const).map((v) => (
                <Chip
                  key={v}
                  testID={`private-${v}`}
                  label={v === 'default' ? 'Hub default' : v === 'on' ? 'On' : 'Off'}
                  on={priv === v}
                  onPress={() => !off && void savePrefs({ private: v === 'default' ? null : v === 'on' })}
                />
              ))}
            </View>

            <Text style={{ color: t.text, fontWeight: '600', marginTop: 8 }}>When you tap a push</Text>
            <Muted>Claude app uses the Remote Control link when there is one.</Muted>
            <View style={{ flexDirection: 'row', flexWrap: 'wrap' }}>
              {([['auto', 'Automatic'], ['session', 'Open the session'], ['claude', 'Open in Claude']] as const).map(([v, label]) => (
                <Chip key={v} testID={`tap-${v}`} label={label} on={tap === v} onPress={() => !off && void savePrefs({ tap: v })} />
              ))}
            </View>

            <QuietEditor value={qh} disabled={off} onSave={(q) => void savePrefs({ quiet_hours: q })} onInvalid={() => toast.show('Use 24-hour times like 23:00')} />

            <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', marginTop: 8 }}>
              <View style={{ flexShrink: 1, paddingRight: 8 }}>
                <Text style={{ color: t.text, fontWeight: '600' }}>Widgets refresh</Text>
                <Muted>The hub sends a quiet push now and then so widgets stay fresh.</Muted>
              </View>
              <Switch
                accessibilityLabel="Widgets refresh"
                testID="pref-widgets"
                value={prefs.widgets === true}
                disabled={off}
                onValueChange={(on) => void savePrefs({ widgets: on })}
              />
            </View>
          </Card>
        )}
      </Section>

      {api.autoCountdown ? (
        <Section title="Lock Screen">
          <Card>
            <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
              <View style={{ flexShrink: 1, paddingRight: 8 }}>
                <Text style={{ color: t.text, fontWeight: '600' }}>Auto countdown</Text>
                <Muted>Count down on the Lock Screen when a limit push is opened.</Muted>
              </View>
              <Switch
                accessibilityLabel="Auto countdown"
                testID="auto-countdown"
                value={api.autoCountdown.on}
                onValueChange={(on) => void api.autoCountdown?.set(on)}
              />
            </View>
          </Card>
        </Section>
      ) : null}

      <Section title="App version">
        <Card>
          <KV k="Version" v={`${api.app.version} (${api.app.build})`} />
          <KV k="Device" v={`${api.app.model} · ${api.app.platform}`} />
        </Card>
      </Section>

      <Section title="This device">
        <Card>
          <KV k="Name" v={self?.name ?? api.me?.device.name ?? '–'} />
          <Btn kind="danger" label="Unpair this phone" testID="unpair" onPress={confirmUnpair} />
        </Card>
      </Section>
    </Page>
  );
}

function QuietEditor({ value, disabled, onSave, onInvalid }: {
  value: QuietHours | null | undefined; disabled: boolean; onSave: (q: QuietHours | null) => void; onInvalid: () => void;
}) {
  const t = useTheme();
  const on = !!value?.enabled;
  const [start, setStart] = useState(value?.start ?? '23:00');
  const [end, setEnd] = useState(value?.end ?? '08:00');
  const save = (s: string, e: string) => {
    if (!HHMM.test(s) || !HHMM.test(e)) return onInvalid();
    onSave({ enabled: true, start: s, end: e, tz: value?.tz || deviceTz(), critical: value?.critical ?? true });
  };
  const input = { borderWidth: 1, borderColor: t.line, borderRadius: 8, padding: 8, color: t.text, minWidth: 72, textAlign: 'center' as const };
  return (
    <View style={{ marginTop: 8, gap: 6 }}>
      <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
        <View style={{ flexShrink: 1, paddingRight: 8 }}>
          <Text style={{ color: t.text, fontWeight: '600' }}>Own quiet hours</Text>
          <Muted>{on ? 'This phone is quiet in this window.' : 'Off: this phone follows the hub.'}</Muted>
        </View>
        <Switch
          accessibilityLabel="Own quiet hours"
          testID="pref-quiet"
          value={on}
          disabled={disabled}
          onValueChange={(v) => (v ? save(start, end) : onSave(null))}
        />
      </View>
      {on ? (
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
          <TextInput accessibilityLabel="Quiet from" testID="quiet-start" value={start} onChangeText={setStart} onEndEditing={() => save(start, end)} editable={!disabled} style={input} />
          <Text style={{ color: t.muted }}>to</Text>
          <TextInput accessibilityLabel="Quiet until" testID="quiet-end" value={end} onChangeText={setEnd} onEndEditing={() => save(start, end)} editable={!disabled} style={input} />
        </View>
      ) : null}
    </View>
  );
}
