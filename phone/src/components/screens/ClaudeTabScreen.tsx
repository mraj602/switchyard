// A claude.ai account tab (phone.md P17.3): the seat's own WebView profile, the sign-in note,
// Open in browser and Close, and the fallback strip when claude.ai does not load here (P17.4).
import { router } from 'expo-router';
import { useEffect, useRef, useState } from 'react';
import { ActivityIndicator, Pressable, Text, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { TabView, type TabEvent } from '../../../modules/sy-tabs';
import { seatIndex, seatName } from '../../lib/format';
import { assessTab, claudeUrl, HINT_START, nextHint, nextMemo, showSignInHint, SIGN_IN_NOTE, TAB_HOME, validSeat, verdictText, type TabEventAt } from '../../lib/tabs';
import { useHubApi } from '../hub-api';
import { openRemote } from '../links';
import { profilesChanged, readHintDismissed, setHintDismissed, updateMemo } from '../tab-memo';
import { useTabsSupported } from '../tabs';
import { useTheme } from '../theme';
import { Btn, Calm, Dot, Muted, s, useToast } from '../ui';

export default function ClaudeTabScreen({ seat, url: asked }: { seat: string; url?: string }) {
  const api = useHubApi();
  const t = useTheme();
  const toast = useToast();
  const supported = useTabsSupported();
  const url = claudeUrl(asked) ?? TAB_HOME;
  const [events, setEvents] = useState<TabEventAt[]>([]);
  // The route keys this screen by seat and link, so a new link starts a fresh assessment.
  const [opened] = useState(() => Date.now());
  const [now, setNow] = useState(opened);
  const [current, setCurrent] = useState(url);
  // Sign-in state of this visit (P17.3): the hint is on sign-in pages only, and per account.
  const [hint, setHint] = useState(HINT_START);
  const [dismissed, setDismissed] = useState(true); // until the stored choice is read
  useEffect(() => {
    let live = true;
    void readHintDismissed().then((d) => live && setDismissed(d[seat] === true));
    return () => {
      live = false;
    };
  }, [seat]);
  const dismissHint = () => {
    setDismissed(true);
    void setHintDismissed(seat, true);
  };

  const verdict = assessTab(events, opened, now);
  const waiting = verdict.kind === 'waiting';
  useEffect(() => {
    if (!waiting) return;
    const h = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(h);
  }, [waiting]);

  const known = seatIndex(api.data.accounts.length ? api.data.accounts : api.data.overview?.seats ?? []).get(seat);
  const name = known ? seatName(known.seat) : 'Account';
  const browser = () => void openRemote(current).then((ok) => ok || toast.show('Could not open the link'), () => toast.show('Could not open the link'));

  const first = useRef(true);
  const onTabState = ({ nativeEvent: e }: { nativeEvent: TabEvent }) => {
    if (e.state === 'download') toast.show('Downloads are blocked in Claude tabs. Use Open in browser.');
    if (e.state === 'external') toast.show('Opened in the browser');
    if (e.url && (e.state === 'loading' || e.state === 'loaded' || e.state === 'url') && claudeUrl(e.url)) setCurrent(e.url);
    setHint((h) => nextHint(h, e));
    const at = Date.now();
    setEvents((xs) => [...xs.slice(-30), { ...e, at }]);
    setNow(at);
    // P17.7: remember signed in or not, and as which account, for the Accounts screen.
    if (validSeat(seat)) {
      void updateMemo(seat, (prev) => nextMemo(prev, e, at));
      if (first.current) {
        first.current = false;
        profilesChanged(); // the view made the profile
      }
    }
  };

  // Until the first page load the WebView is blank: say so with a spinner.
  const loaded = events.some((e) => e.state === 'loaded');
  const problem = supported === false ? verdictText({ kind: 'failed', reason: 'unsupported' }) : verdictText(verdict);

  return (
    <SafeAreaView style={[s.fill, { backgroundColor: t.bg }]} edges={['top', 'bottom']}>
      <View style={[s.rowBetween, { paddingHorizontal: 16, paddingVertical: 6, gap: 8 }]}>
        <View style={[s.row, { flexShrink: 1 }]}>
          <Dot colour={known?.colour ?? '#8A9098'} />
          <Text accessibilityRole="header" numberOfLines={1} style={{ color: t.text, fontSize: 17, fontWeight: '700', flexShrink: 1 }}>
            {name}
          </Text>
        </View>
        <View style={[s.row, { gap: 6 }]}>
          <Btn label="Open in browser" testID="tab-browser" onPress={browser} />
          <Btn label="Close" testID="tab-close" onPress={() => (router.canGoBack() ? router.back() : router.replace('/' as never))} />
        </View>
      </View>
      {showSignInHint(hint, dismissed) ? (
        <View style={[s.rowBetween, { paddingHorizontal: 16, paddingBottom: 6, gap: 8 }]}>
          <Muted style={{ flexShrink: 1 }}>{SIGN_IN_NOTE}</Muted>
          <Pressable accessibilityRole="button" accessibilityLabel="Dismiss sign-in hint" testID="tab-hint-dismiss" hitSlop={10} onPress={dismissHint}>
            <Text style={{ color: t.muted, fontSize: 20, lineHeight: 22 }}>×</Text>
          </Pressable>
        </View>
      ) : null}
      {problem ? (
        <View testID="tab-fallback" style={{ backgroundColor: t.bad, paddingHorizontal: 16, paddingVertical: 8, gap: 6 }}>
          <Text style={{ color: '#FFFFFF', fontWeight: '600' }}>{`claude.ai did not load in this tab. ${problem}`}</Text>
          <Btn label="Open in browser" testID="tab-fallback-browser" onPress={browser} />
        </View>
      ) : null}
      {!validSeat(seat) ? (
        <Calm>This account has no tab.</Calm>
      ) : supported ? (
        <View style={s.fill}>
          <TabView key={seat} seat={seat} url={url} onTabState={onTabState} style={s.fill} testID="tab-view" />
          {!loaded && !problem ? (
            <View pointerEvents="none" testID="tab-loading" style={{ position: 'absolute', top: 32, left: 0, right: 0, alignItems: 'center' }}>
              <ActivityIndicator color={t.muted} />
            </View>
          ) : null}
        </View>
      ) : supported === null ? (
        <Calm>Loading…</Calm>
      ) : null}
      {toast.node}
    </SafeAreaView>
  );
}
