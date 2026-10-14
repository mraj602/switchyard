// Every incoming URL runs through the deep-link allow-list (phone.md P7). Anything not on it
// opens Now; a pair link opens the Pair screen pre-filled and never claims by itself.
import { nativeIntentToHref } from '../src/lib/links';

export function redirectSystemPath({ path }: { path: string; initial: boolean }): string {
  return nativeIntentToHref(path);
}
