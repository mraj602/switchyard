// claude.ai account tabs (phone.md P17): the sy-tabs module behind one small hook, and "Open in
// Claude" routed by src/lib/tabs (the seat's tab, else the browser).
import { useEffect, useState } from 'react';
import * as SyTabs from '../../modules/sy-tabs';
import { openTarget } from '../lib/tabs';
import { openRemote } from './links';
import { go } from './nav';

let supported: Promise<boolean> | null = null;

/** Whether this phone can keep one sign-in per tab (asked once per run). */
export function tabsSupported(): Promise<boolean> {
  supported ??= SyTabs.available ? SyTabs.isSupported().catch(() => false) : Promise.resolve(false);
  return supported;
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
