// Extension points of the app state (phone.md P3, P5). Owner: WP-P. WP-I fills them at module
// scope in app/_layout.tsx with `installStateHooks(createGlue(...))` (src/state/glue.tsx, logic
// in src/lib/integrate): the provider calls them and never imports notify, widget or live.
import type { HubData } from '../lib/data';
import type { HubClient } from '../lib/hub';
import type { StoredHub } from '../lib/secure';

export interface StateHooks {
  /** After every good refresh: rebuild the widget snapshot and update the Live Activity (P5). */
  afterRefresh: ((d: HubData) => Promise<void>) | null;
  /** The keychain entry, cache and widget data were cleared: end Live Activities, reset badge (P3). */
  onCleared: (() => Promise<void> | void) | null;
  /** A claim was kept in the keychain: register for pushes (P6.3). Errors are ignored. */
  onPaired: ((client: HubClient, hub: StoredHub) => Promise<void> | void) | null;
}

export const stateHooks: StateHooks = { afterRefresh: null, onCleared: null, onPaired: null };
