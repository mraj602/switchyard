// Settings → Claude tabs (phone.md P17.3): one row per tab profile with "Sign out (clear this
// tab)", which deletes the seat's WebView profile. Android builds with the wy-tabs module only.
import { useCallback, useEffect, useState } from 'react';
import { Alert as RNAlert, Text, View } from 'react-native';
import * as WyTabs from '../../modules/wy-tabs';
import { SIGN_IN_NOTE, TAB_PRIVACY_NOTE, tabRows } from '../lib/tabs';
import { useHubApi } from './hub-api';
import { signOutTab } from './tab-memo';
import { useTabsSupported } from './tabs';
import { useTheme } from './theme';
import { Btn, Card, Muted, Section } from './ui';

export function ClaudeTabsSection({ toast }: { toast: (m: string) => void }) {
  const api = useHubApi();
  const t = useTheme();
  const supported = useTabsSupported();
  const [profiles, setProfiles] = useState<string[] | null>(null);

  const load = useCallback(() => {
    WyTabs.listProfiles().then(setProfiles, () => setProfiles([]));
  }, []);
  useEffect(() => {
    if (supported) load();
  }, [supported, load]);

  if (!WyTabs.available || supported === null) return null;
  const seats = api.data.accounts.length ? api.data.accounts : api.data.overview?.seats ?? [];
  const rows = tabRows(profiles ?? [], seats);

  const signOut = (seat: string, name: string) =>
    RNAlert.alert(`Sign out of ${name}?`, "This clears the tab's sign-in, cookies and saved data on this phone.", [
      { text: 'Cancel', style: 'cancel' },
      {
        text: 'Sign out',
        style: 'destructive',
        onPress: () =>
          void signOutTab(seat).then(
            () => {
              toast('Signed out');
              load();
            },
            (e: unknown) => toast((e as { code?: string }).code === 'ERR_WY_TABS_IN_USE' ? 'Close the tab first, then try again' : 'Could not clear the tab'),
          ),
      },
    ]);

  return (
    <Section title="Claude tabs">
      <Card>
        {!supported ? (
          <Muted>{"This phone's WebView cannot keep separate claude.ai sign-ins; links open in the browser."}</Muted>
        ) : (
          <>
            <Muted>{`One claude.ai tab per account, each with its own sign-in. Open one from Accounts. ${SIGN_IN_NOTE}`}</Muted>
            <Muted>{TAB_PRIVACY_NOTE}</Muted>
            {rows.length === 0 ? <Muted>No tabs yet.</Muted> : null}
            {rows.map((r) => (
              <View key={r.seat} testID={`tabrow-${r.seat}`} style={{ gap: 4, marginTop: 6 }}>
                <Text style={{ color: t.text, fontWeight: '600' }}>{r.name}</Text>
                <Btn kind="danger" label="Sign out (clear this tab)" testID={`tab-signout-${r.seat}`} onPress={() => signOut(r.seat, r.name)} />
              </View>
            ))}
          </>
        )}
      </Card>
    </Section>
  );
}
