// claude.ai account tabs (phone.md P17): the sy-tabs module behind one small hook, and "Open in
// Claude" routed by src/lib/tabs (the seat's tab, else the browser).
import { useEffect, useState } from 'react';
import * as SyTabs from '../../modules/sy-tabs';
import { openTarget } from '../lib/tabs';
import { openRemote } from './links';
import { go } from './nav';
import { forgetMemo, profilesChanged } from './tab-memo';

let supported: Promise<boolean> | null = null;

/** Whether this phone can keep one sign-in per tab (asked once per run). */
export function tabsSupported(): Promise<boolean> {
  supported ??= SyTabs.available ? SyTabs.isSupported().catch(() => false) : Promise.resolve(false);
  return supported;
}

/** Seat ids that have a claude.ai tab profile on this phone (empty where tabs are unsupported). */
export async function tabProfiles(): Promise<string[]> {
  if (!SyTabs.available || !(await tabsSupported())) return [];
  return SyTabs.listProfiles().catch(() => []);
}

/**
 * Sign out of every claude.ai tab on this phone (deletes each tab profile). Best effort: a tab
 * that is open refuses; the rest are cleared. Returns how many could not be cleared.
 */
export async function clearAllTabs(): Promise<number> {
  const ids = await tabProfiles();
  const results = await Promise.allSettled(ids.map((id) => SyTabs.deleteProfile(id)));
  // What a cleared tab showed goes with it (P17.7).
  await Promise.all(ids.filter((_, i) => results[i]!.status === 'fulfilled').map((id) => forgetMemo(id)));
  if (ids.length) profilesChanged();
  return results.filter((r) => r.status === 'rejected').length;
}

/** For tests: forget the cached answer. */
export function resetTabsSupport(): void {
  supported = null;
}

/** null while asking. */
export function useTabsSupported(): boolean | null {
  const [ok, setOk] = useState<boolean | null>(null);
  useEffect(() => {
    let live = true;
    void tabsSupported().then((v) => live && setOk(v));
    return () => {
      live = false;
    };
  }, []);
  return ok;
}

/**
 * Open in Claude (P17.2): the session's seat tab, or the browser (no tabs on this phone, no
 * known seat, not a claude.ai link). False when there is no https link to open.
 */
export async function openInClaude(session: { seat?: string | null; remote_url?: string | null }): Promise<boolean> {
  const target = openTarget(session, { supported: await tabsSupported() });
  if (!target) return false;
  if (target.kind === 'tab') {
    go.tab(target.seat, target.url);
    return true;
  }
  return openRemote(target.url);
}
