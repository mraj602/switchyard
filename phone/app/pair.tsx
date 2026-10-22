// Pair screen (phone.md P2).
// Scan the QR (or open a switchyard://pair link) or type an address and code; claim; keep the
// token in the keychain through the state layer; then go to Now. `pairWith` resolves only after
// the integration's onPaired (src/lib/integrate: GET /v1/rules, the notifications prompt and
// registration, P6.3), so this is the one navigation and Now comes after the prompt (P2). A
// second pairing asks "Unpair from <host>?" first.
import { CameraView, useCameraPermissions } from 'expo-camera';
import { router, useLocalSearchParams } from 'expo-router';
import { useCallback, useMemo, useRef, useState } from 'react';
import { Pressable, ScrollView, StatusBar, StyleSheet, Text, TextInput, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Logo } from '../src/components/logo';
import { claim, ClaimError, claimMessage, hostOf, networkKind, pairInputMessage, parsePairInput } from '../src/lib/pair';
import type { PairInput } from '../src/lib/pair';
import { useHub } from '../src/state';

// Pair keeps the brand's dark ink in light mode too (the launcher icon's colours), so its status
// bar always uses light icons.
const C = { bg: '#111315', card: '#1A1D20', well: '#1E2226', text: '#F2F3F5', muted: '#B8BDC3', faint: '#7A8088', accent: '#E8B04B', error: '#F28B82' };

function paramString(v: string | string[] | undefined): string | undefined {
  return Array.isArray(v) ? v[0] : v;
}

export default function PairScreen() {
  const hub = useHub();
  const params = useLocalSearchParams<{ origin?: string; secret?: string; code?: string; hub?: string; key?: string }>();
  const [permission, requestPermission] = useCameraPermissions();
  const [typed, setTyped] = useState(false);
  const [address, setAddress] = useState('');
  const [codeText, setCodeText] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<PairInput | null>(null);
  const [noticeSeen, setNoticeSeen] = useState(false);
  const busyRef = useRef(false);
  const lastScan = useRef<string | null>(null);

  const finish = useCallback(
    async (input: PairInput) => {
      if (busyRef.current) return;
      busyRef.current = true;
      setBusy(true);
      setError(null);
      try {
        const stored = await claim(hub.claimDeps, input);
        await hub.pairWith(stored);
        router.replace('/');
      } catch (e) {
        if (e instanceof ClaimError) setError(e.message);
        else {
          const kind = networkKind(e);
          setError(claimMessage(kind, hostOf(input.origin)));
        }
      } finally {
        busyRef.current = false;
        setBusy(false);
      }
    },
    [hub],
  );

  const submit = useCallback(
    (input: PairInput) => {
      if (hub.status === 'paired' && hub.hub) {
        setConfirm(input);
        return;
      }
      void finish(input);
    },
    [hub.status, hub.hub, finish],
  );

  const onText = useCallback(
    (text: string, opts?: { address?: string }) => {
      const r = parsePairInput(text, { dev: hub.dev, ...(opts?.address ? { address: opts.address } : {}) });
      if (!r.ok) {
        setError(pairInputMessage(r.reason));
        return false;
      }
      submit(r.input);
      return true;
    },
    [hub.dev, submit],
  );

  // A switchyard://pair link opened the screen: origin, secret and code arrive as params (the
  // names src/lib/links routeToHref writes). A link never claims by itself (P7): it is parsed
  // into a pre-filled card, and only the tap on "Pair with <host>" claims.
  const origin = paramString(params.origin);
  const secret = paramString(params.secret);
  const code = paramString(params.code);
  const hubFp = paramString(params.hub);
  const sealKey = paramString(params.key);
  const linkKey = origin ? `${origin}|${secret ?? ''}|${code ?? ''}|${hubFp ?? ''}|${sealKey ?? ''}` : null;
  // A new link starts clean: an earlier attempt's "Can't reach ..." is not about this one
  // (adjusted while rendering, React's pattern for state that follows a prop).
  const [seenLink, setSeenLink] = useState(linkKey);
  if (linkKey !== seenLink) {
    setSeenLink(linkKey);
    if (linkKey) setError(null);
  }
  const parsedLink = useMemo(() => {
    if (!origin) return null;
    const q = [
      `u=${encodeURIComponent(origin)}`,
      secret ? `s=${encodeURIComponent(secret)}` : '',
      code ? `c=${encodeURIComponent(code)}` : '',
      hubFp ? `h=${encodeURIComponent(hubFp)}` : '',
      sealKey ? `k=${encodeURIComponent(sealKey)}` : '',
    ]
      .filter(Boolean)
      .join('&');
    return parsePairInput(`switchyard://pair?${q}`, { dev: hub.dev });
  }, [origin, secret, code, hubFp, sealKey, hub.dev]);
  // The link card is shown until the person taps Pair or Cancel on it (then this link is done).
  const [doneLink, setDoneLink] = useState<string | null>(null);
  const linkOpen = hub.status !== 'loading' && linkKey !== null && doneLink !== linkKey;
  const linkInput: PairInput | null = linkOpen && parsedLink?.ok ? parsedLink.input : null;
  const linkError = linkOpen && parsedLink && !parsedLink.ok ? pairInputMessage(parsedLink.reason) : null;
  const shownError = error ?? linkError;

  const onScanned = useCallback(
    ({ data }: { data: string }) => {
      if (busyRef.current || confirm || linkInput || data === lastScan.current) return;
      lastScan.current = data;
      setDoneLink(linkKey); // a scan replaces a refused link's message
      onText(data);
    },
    [onText, confirm, linkInput, linkKey],
  );

  const camera = permission?.granted ? (
    <CameraView
      style={styles.camera}
      facing="back"
      barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
      onBarcodeScanned={onScanned}
      testID="camera"
    />
  ) : (
    <View style={[styles.camera, styles.center]}>
      <Text style={styles.muted}>{permission && !permission.canAskAgain ? 'Camera access is off in system settings.' : 'The camera scans the QR code on your computer.'}</Text>
      {permission?.canAskAgain !== false && (
        <Pressable accessibilityRole="button" onPress={() => void requestPermission()} style={styles.link}>
          <Text style={styles.linkText}>Allow camera</Text>
        </Pressable>
      )}
    </View>
  );

  return (
    <SafeAreaView style={styles.root}>
      <StatusBar barStyle="light-content" backgroundColor="transparent" translucent />
      <ScrollView contentContainerStyle={styles.body} keyboardShouldPersistTaps="handled">
        <Logo size={32} colour={C.text} />
        <Text accessibilityRole="header" style={styles.h1}>
          Pair with your hub
        </Text>
        <Text style={styles.muted}>
          On your computer, open Add phone (in the desktop app or on the hub&apos;s board) and scan the QR code, or type the address and code.
        </Text>

        {confirm ? (
          <View style={styles.card} testID="confirm">
            <Text style={styles.cardTitle}>Unpair from {hub.hub ? hostOf(hub.hub.origin) : 'this hub'}?</Text>
            <Text style={styles.muted}>This app can be paired with one hub. Pairing with {hostOf(confirm.origin)} removes the current one.</Text>
            <Pressable
              accessibilityRole="button"
              onPress={() => {
                const input = confirm;
                setConfirm(null);
                void (async () => {
                  await hub.unpair();
                  await finish(input);
                })();
              }}
              style={styles.button}
            >
              <Text style={styles.buttonText}>Unpair and pair</Text>
            </Pressable>
            <Pressable accessibilityRole="button" onPress={() => setConfirm(null)} style={styles.link}>
              <Text style={styles.linkText}>Cancel</Text>
            </Pressable>
          </View>
        ) : linkInput ? (
          <View style={styles.card} testID="link-confirm">
            <Text style={styles.cardTitle}>Pair with {hostOf(linkInput.origin)}?</Text>
            <Text style={styles.muted}>A pairing link opened the app. Pair only if you just made it on your computer.</Text>
            <Pressable
              accessibilityRole="button"
              disabled={busy}
              onPress={() => {
                const input = linkInput;
                setDoneLink(linkKey);
                submit(input);
              }}
              style={[styles.button, busy && styles.dim]}
            >
              <Text style={styles.buttonText}>{busy ? 'Pairing…' : `Pair with ${hostOf(linkInput.origin)}`}</Text>
            </Pressable>
            <Pressable accessibilityRole="button" onPress={() => setDoneLink(linkKey)} style={styles.link}>
              <Text style={styles.linkText}>Cancel</Text>
            </Pressable>
          </View>
        ) : typed ? (
          <View style={styles.form}>
            <Text style={styles.label}>Hub address</Text>
            <TextInput
              accessibilityLabel="Hub address"
              placeholder="for example hub.example.ts.net"
              placeholderTextColor={C.faint}
              cursorColor={C.accent}
              selectionColor={C.accent}
              autoCapitalize="none"
              autoCorrect={false}
              keyboardType="url"
              value={address}
              onChangeText={setAddress}
              style={styles.input}
            />
            <Text style={styles.label}>Code from Add phone</Text>
            <TextInput
              accessibilityLabel="Code"
              placeholder="6 digits"
              placeholderTextColor={C.faint}
              cursorColor={C.accent}
              selectionColor={C.accent}
              keyboardType="number-pad"
              value={codeText}
              onChangeText={setCodeText}
              style={styles.input}
            />
            <Pressable accessibilityRole="button" disabled={busy} onPress={() => onText(codeText, { address })} style={[styles.button, busy && styles.dim]}>
              <Text style={styles.buttonText}>{busy ? 'Pairing…' : 'Pair'}</Text>
            </Pressable>
          </View>
        ) : (
          camera
        )}

        {shownError && (
          <Text accessibilityRole="alert" style={styles.error}>
            {shownError}
          </Text>
        )}
        {hub.notice && !noticeSeen && !shownError && !confirm && !linkInput && (
          <View style={styles.card} testID="notice-card">
            <Text accessibilityRole="alert" style={styles.cardTitle}>{hub.notice}</Text>
            <Pressable
              accessibilityRole="button"
              onPress={() => {
                setNoticeSeen(true);
                setTyped(false);
                setError(null);
                lastScan.current = null;
                if (!permission?.granted && permission?.canAskAgain !== false) void requestPermission();
              }}
              style={styles.button}
            >
              <Text style={styles.buttonText}>Scan again</Text>
            </Pressable>
          </View>
        )}

        {!confirm && !linkInput && (
          <Pressable
            accessibilityRole="button"
            onPress={() => {
              setTyped((t) => !t);
              setError(null);
              setDoneLink(linkKey);
              lastScan.current = null;
            }}
            style={styles.link}
          >
            <Text style={styles.linkText}>{typed ? 'Scan the QR code instead' : 'Type a code and address instead'}</Text>
          </Pressable>
        )}

        <View style={styles.card}>
          <Text style={styles.small}>
            The phone gets its own read-only key. It can see your sessions and usage, and it can never control a session or see your Claude logins. Remove it any time on the board&apos;s Devices page.
          </Text>
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: C.bg },
  body: { padding: 20, gap: 16, flexGrow: 1 },
  h1: { color: C.text, fontSize: 26, fontWeight: '600' },
  muted: { color: C.muted, fontSize: 15, lineHeight: 22 },
  small: { color: C.muted, fontSize: 13, lineHeight: 19 },
  camera: { height: 300, borderRadius: 18, overflow: 'hidden', backgroundColor: C.well },
  center: { alignItems: 'center', justifyContent: 'center', padding: 20, gap: 8 },
  link: { minHeight: 44, justifyContent: 'center' },
  linkText: { color: C.accent, fontSize: 15 },
  card: { backgroundColor: C.card, borderRadius: 14, padding: 14, gap: 10 },
  cardTitle: { color: C.text, fontSize: 17, fontWeight: '600' },
  form: { gap: 8 },
  label: { color: C.text, fontSize: 14, fontWeight: '600', marginTop: 4 },
  input: { backgroundColor: C.well, color: C.text, borderRadius: 12, padding: 14, fontSize: 16, minHeight: 48 },
  button: { backgroundColor: C.accent, borderRadius: 12, minHeight: 48, alignItems: 'center', justifyContent: 'center' },
  buttonText: { color: '#1A1300', fontSize: 16, fontWeight: '600' },
  dim: { opacity: 0.6 },
  error: { color: C.error, fontSize: 15 },
});
