// Settings (P4): hub and status, notifications, private pushes, tap
// behaviour, own quiet hours, the widgets refresh switch, Claude tabs (P17), appearance (light, dark
// or device default), app version and Unpair.
import { useEffect, useState } from 'react';
import { Alert as RNAlert, Switch, Text, TextInput, View } from 'react-native';
import type { PushTestResult, QuietHours, TapMode } from '../../contract/api';
import { loadTheme, saveTheme, THEME_CHOICES, type ThemeChoice } from '../appearance';
import { clock } from '../../lib/format';
import { systemCache } from '../../state/platform';
import { updateText, type Updater } from '../../lib/update';
import { ClaudeTabsSection } from '../claude-tabs-section';
import { useUpdate } from '../use-update';
import { useHubApi } from '../hub-api';
import { go } from '../nav';
import { clearAllTabs, tabProfiles } from '../tabs';
import { switchColors, useTheme } from '../theme';
import { useSelf } from '../use-self';
import { Btn, Calm, Card, Chip, KV, Muted, Page, Section, useToast } from '../ui';

const PUSH_TEXT: Record<string, string> = {
  registered: 'On',
  blocked: 'Blocked in system settings. Allow notifications for this app there, then come back.',
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
  const prefs = self?.prefs ?? {};
  const offline = api.data.offline;
  const off = offline || !self;

  const confirmUnpair = () =>
    RNAlert.alert(`Unpair from ${api.host}?`, 'This phone will stop receiving pushes and must be paired again.', [
      { text: 'Cancel', style: 'cancel' },
      { text: 'Unpair', style: 'destructive', onPress: () => void askTabs() },
    ]);

  // After the first confirm: with claude.ai tabs on this phone, ask whether to sign out of them too.
  const askTabs = async () => {
    if ((await tabProfiles()).length === 0) return api.unpair();
    RNAlert.alert('Also sign out of the claude.ai tabs on this phone?', 'Yes clears their sign-ins, cookies and saved data. No keeps them.', [
      { text: 'Cancel', style: 'cancel' },
      { text: 'No, keep them', onPress: () => void api.unpair() },
      { text: 'Yes, sign out', style: 'destructive', onPress: () => void clearAllTabs().then(() => api.unpair(), () => api.unpair()) },
    ]);
  };

  const priv = prefs.private == null ? 'default' : prefs.private ? 'on' : 'off';
  const tap: TapMode = prefs.tap ?? 'auto';
  const qh = prefs.quiet_hours;

  return (
    <Page back title="Settings" toast={toast.node}>
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
          ) : (
            <Muted>{"Push notifications use the Android app's own connection to your hub. This phone has none."}</Muted>
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
                {...switchColors(t)}
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
                {...switchColors(t)}
                accessibilityLabel="Auto countdown"
                testID="auto-countdown"
                value={api.autoCountdown.on}
                onValueChange={(on) => void api.autoCountdown?.set(on)}
              />
            </View>
          </Card>
        </Section>
      ) : null}

      <ClaudeTabsSection toast={toast.show} />

      <AppearanceSection />

      <Section title="App version">
        <Card>
          <KV k="Version" v={`${api.app.version} (${api.app.build})`} />
          <KV k="Device" v={`${api.app.model} · ${api.app.platform}`} />
          {api.updater ? <UpdateRow updater={api.updater} /> : null}
        </Card>
        <Muted>Watchyard is an unofficial, self-hosted tool and is not affiliated with Anthropic.</Muted>
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

/** The Updates row (phone.md P16.3): state, Check for update, and Install when one is offered. */
function UpdateRow({ updater }: { updater: Updater }) {
  const t = useTheme();
  const v = useUpdate(updater);
  const busy = v.phase === 'checking' || v.phase === 'installing';
  return (
    <View style={{ marginTop: 8, gap: 6 }}>
      <Text style={{ color: t.text, fontWeight: '600' }}>Updates</Text>
      <Text testID="update-state" style={{ color: v.phase === 'error' || v.phase === 'refused' ? t.text : t.muted }}>
        {updateText(v)}
      </Text>
      {v.info && v.phase !== 'refused' ? (
        <>
          {v.info.notes ? <Muted>{v.info.notes}</Muted> : null}
          <Muted>{`From ${v.info.source === 'hub' ? 'your hub' : 'GitHub'}. Android asks you to confirm before it installs.`}</Muted>
          {v.phase !== 'started' ? (
            <Btn kind="primary" label={`Install ${v.info.version}`} testID="update-install" disabled={busy} onPress={() => void updater.install()} />
          ) : null}
        </>
      ) : null}
      <Btn label="Check for update" testID="update-check" disabled={busy} onPress={() => void updater.check()} />
    </View>
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
          {...switchColors(t)}
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

/** Appearance: device default (the default), light or dark; this phone only. */
function AppearanceSection() {
  const t = useTheme();
  const [choice, setChoice] = useState<ThemeChoice | null>(null);
  useEffect(() => {
    let live = true;
    void loadTheme(systemCache).then((c) => live && setChoice(c));
    return () => {
      live = false;
    };
  }, []);
  return (
    <Section title="Appearance">
      <Card>
        <Text style={{ color: t.text, fontWeight: '600' }}>Theme</Text>
        <Muted>{"Device default follows your phone's light or dark setting."}</Muted>
        <View style={{ flexDirection: 'row', flexWrap: 'wrap' }}>
          {THEME_CHOICES.map((c) => (
            <Chip
              key={c.value}
              testID={`theme-${c.value}`}
              label={c.label}
              on={(choice ?? 'system') === c.value}
              onPress={() => {
                setChoice(c.value);
                void saveTheme(systemCache, c.value);
              }}
            />
          ))}
        </View>
      </Card>
    </Section>
  );
}
