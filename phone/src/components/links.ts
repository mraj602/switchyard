// Opening links and copying text (P4: Open in Claude through Linking.openURL, Copy resume
// command through expo-clipboard). Kept apart so screens and tests have one place to fake.
import * as Clipboard from 'expo-clipboard';
import { Linking } from 'react-native';
import { httpsUrl } from '../lib/format';

/** Opens an https URL from the hub; false when it is not one. */
export async function openRemote(url: unknown): Promise<boolean> {
  const safe = httpsUrl(url);
  if (!safe) return false;
  await Linking.openURL(safe);
  return true;
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await Clipboard.setStringAsync(text);
    return true;
  } catch {
    return false;
  }
}
