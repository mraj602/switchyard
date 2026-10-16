// What each claude.ai tab showed when it was last open (phone.md P17.7): signed in or not, and
// the account uuid claude.ai named in it. Kept in AsyncStorage (TAB_MEMO_KEY) so the Accounts
// screen can say it without opening any tab; local to this install, never sent to the hub.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useEffect, useState } from 'react';
import * as SyTabs from '../../modules/sy-tabs';
import { TAB_MEMO_KEY } from '../contract/constants';
import { readMemos, type TabMemo, type TabMemos } from '../lib/tabs';

let memos: TabMemos | null = null;
let loading: Promise<TabMemos> | null = null;
const listeners = new Set<(m: TabMemos) => void>();
let writes: Promise<void> = Promise.resolve();

function load(): Promise<TabMemos> {
  if (memos) return Promise.resolve(memos);
  loading ??= AsyncStorage.getItem(TAB_MEMO_KEY)
    .then((raw) => {
      try {
        return readMemos(raw ? JSON.parse(raw) : {});
      } catch {
        return {};
      }
    })
    .catch(() => ({}))
    .then((m) => {
      memos ??= m;
      return memos;
    });
  return loading;
}

/** Change the memos (one write at a time, in order); listeners hear the new value. */
function change(fn: (m: TabMemos) => TabMemos): Promise<void> {
  writes = writes.then(async () => {
    const next = fn({ ...(await load()) });
    memos = next;
    for (const l of listeners) l(next);
    await AsyncStorage.setItem(TAB_MEMO_KEY, JSON.stringify(next)).catch(() => undefined);
  });
  return writes;
}

/** Set (or with undefined, forget) one seat's memo from its previous one. */
export const updateMemo = (seat: string, fn: (prev: TabMemo | undefined) => TabMemo | undefined): Promise<void> =>
  change((m) => {
    const prev = m[seat];
    const next = fn(prev);
    if (next === prev) return m;
    if (next) m[seat] = next;
    else delete m[seat];
    return m;
  });

export const forgetMemo = (seat: string) => updateMemo(seat, () => undefined);

/** The memo goes with the profile when a tab is given to another seat. */
export const moveMemo = (from: string, to: string): Promise<void> =>
  change((m) => {
    if (m[from]) m[to] = m[from]!;
    else delete m[to];
    delete m[from];
    return m;
  });

/** For tests: forget the in-memory copy. */
export function resetTabMemos(): void {
  memos = null;
  loading = null;
  writes = Promise.resolve();
  listeners.clear();
}

/** The memos (null while loading), live. */
export function useTabMemos(): TabMemos | null {
  const [m, setM] = useState<TabMemos | null>(memos);
  useEffect(() => {
    let live = true;
    const on = (x: TabMemos) => live && setM(x);
    listeners.add(on);
    void load().then(on);
    return () => {
      live = false;
      listeners.delete(on);
    };
  }, []);
  return m;
}

// ---- the tab profiles (which seats have a tab on this phone), reloaded when one changes

const profileListeners = new Set<() => void>();

/** A profile was made, deleted or moved: every useTabProfiles reloads. */
export function profilesChanged(): void {
  for (const l of profileListeners) l();
}

/** Seat ids with a tab profile (null while asking; [] where tabs are not supported). */
export function useTabProfiles(supported: boolean | null): string[] | null {
  const [profiles, setProfiles] = useState<string[] | null>(null);
  const [n, setN] = useState(0);
  useEffect(() => {
    const on = () => setN((x) => x + 1);
    profileListeners.add(on);
    return () => {
      profileListeners.delete(on);
    };
  }, []);
  useEffect(() => {
    if (!supported) return;
    let live = true;
    SyTabs.listProfiles().then(
      (p) => live && setProfiles(p),
      () => live && setProfiles([]),
    );
    return () => {
      live = false;
    };
  }, [supported, n]);
  return supported === false ? [] : profiles;
}

/** Sign out a seat's tab (deletes its profile) and forget what it showed. */
export async function signOutTab(seat: string): Promise<boolean> {
  const ok = await SyTabs.deleteProfile(seat);
  await forgetMemo(seat);
  profilesChanged();
  return ok;
}

/** Phone-12 "Use this tab for <other> instead": the profile, and its memo, become `to`'s. */
export async function giveTab(from: string, to: string): Promise<boolean> {
  const ok = await SyTabs.moveProfile(from, to);
  await moveMemo(from, to);
  profilesChanged();
  return ok;
}
