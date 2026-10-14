// This phone's device record and its prefs (GET and PATCH /v1/devices/self), for Alerts and
// Settings. savePrefs sends the whole merged prefs object, as the PWA does, so a hub that
// replaces prefs on PATCH loses nothing.
import { useCallback, useEffect, useState } from 'react';
import type { Device, DevicePrefs } from '../contract/api';
import { useHubApi } from './hub-api';

export function useSelf(onError: (msg: string) => void) {
  const api = useHubApi();
  const client = api.client;
  const [self, setSelf] = useState<Device | null>(null);

  useEffect(() => {
    if (!client) return;
    let live = true;
    client.deviceSelf().then(
      (d) => live && setSelf(d),
      (e: unknown) => live && onError(e instanceof Error ? e.message : 'Could not load this phone'),
    );
    return () => {
      live = false;
    };
  }, [client, onError]);

  const savePrefs = useCallback(
    async (patch: Partial<DevicePrefs>) => {
      if (!client || !self) return;
      const prefs = { ...(self.prefs ?? {}), ...patch };
      const before = self;
      setSelf({ ...self, prefs }); // optimistic
      try {
        setSelf(await client.patchSelf(prefs));
      } catch (e) {
        setSelf(before);
        onError(e instanceof Error ? e.message : 'Could not save');
      }
    },
    [client, self, onError],
  );

  return { self, savePrefs };
}
