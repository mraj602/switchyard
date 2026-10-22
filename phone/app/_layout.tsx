// Root layout (phone.md P3, P6.4, P11, P12).
// Module scope (once, guarded against Fast Refresh): the Android widget task handler, the
// integration's state hooks, and initNotifications, so a cold-start notification
// response is read exactly once; its decisions reach the hub through
// `bridge`, which the provider installs after it has read the keychain. The tree: the
// HubProvider (the one source of truth), the glue that derives the screens' HubApi from it,
// and the gate: unpaired (including a 401 on any call) goes to Pair.
import * as Linking from 'expo-linking';
import * as Notifications from 'expo-notifications';
import { router, Stack, useSegments } from 'expo-router';
import { useEffect, useMemo } from 'react';
import { StatusBar, useColorScheme } from 'react-native';
import { createGlue } from '../src/lib/integrate';
import type { Glue } from '../src/lib/integrate';
import { initNotifications } from '../src/lib/notify';
import type { NotificationsLike } from '../src/lib/notify';
import { routeToHref } from '../src/lib/links';
import { loadTheme } from '../src/components/appearance';
import { bridge, HubGlue, HubProvider, installStateHooks, systemGluePlatform, useHub } from '../src/state';
import type { ProviderDeps } from '../src/state';
import { getAppInfo, systemAppState, systemCache, systemClock, systemFetch, systemLink, systemSecureHub } from '../src/state/platform';
import { registerAndroidWidgets } from '../widgets/android/task-handler';

const g = globalThis as { __syNotifInit?: boolean; __syGlue?: Glue };
if (!g.__syNotifInit || !g.__syGlue) {
  g.__syNotifInit = true; // Fast Refresh re-evaluates modules: register once
  registerAndroidWidgets();
  const glue = createGlue(systemGluePlatform(getAppInfo()));
  g.__syGlue = glue;
  installStateHooks(glue);
  initNotifications({
    notifications: Notifications as unknown as NotificationsLike,
    app: getAppInfo(),
    run: (d, response) => {
      glue.noteResponse(response); // the auto countdown of a limit push (P12)
      return bridge.run(d, response);
    },
    onReceived: bridge.onReceived,
    probe: bridge.probe,
  });
}
const glue: Glue = g.__syGlue!;
// The saved colour scheme (Settings, Appearance) before the first screen paints, as far as it can.
void loadTheme(systemCache);

function Gate() {
  const { status } = useHub();
  const segments = useSegments();
  const first = segments[0] as string | undefined;
  useEffect(() => {
    if (status === 'unpaired' && first !== 'pair') router.replace('/pair');
  }, [status, first]);
  return null;
}

export default function RootLayout() {
  // Edge to edge: the status bar sits on the app's own background, so its icons follow the theme.
  const dark = useColorScheme() === 'dark';
  const deps: ProviderDeps = useMemo(
    () => ({
      secure: systemSecureHub(),
      cache: systemCache,
      fetch: systemFetch,
      clock: systemClock,
      appState: systemAppState,
      app: getAppInfo(),
      notifications: Notifications as unknown as NotificationsLike,
      link: systemLink(getAppInfo()),
    }),
    [],
  );
  const navigate = useMemo(() => (r: Parameters<typeof routeToHref>[0]) => void router.push(routeToHref(r) as never), []);
  const openUrl = useMemo(() => (u: string) => Linking.openURL(u), []);
  return (
    <HubProvider deps={deps} navigate={navigate} openUrl={openUrl}>
      <HubGlue glue={glue}>
        <StatusBar barStyle={dark ? 'light-content' : 'dark-content'} backgroundColor="transparent" translucent />
        <Gate />
        <Stack screenOptions={{ headerShown: false }}>
          <Stack.Screen name="(tabs)" />
          <Stack.Screen name="pair" options={{ gestureEnabled: false }} />
        </Stack>
      </HubGlue>
    </HubProvider>
  );
}
