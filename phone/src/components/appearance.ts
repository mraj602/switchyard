// The app's colour scheme (phone.md P12): device default, light or dark; device default unless
// chosen. Local to this install. Applied with Appearance.setColorScheme, so every
// useColorScheme() in the app (theme.ts, the status bar) follows it without a provider.
import { Appearance } from 'react-native';
import type { KVStore } from '../contract/platform';

export type ThemeChoice = 'system' | 'light' | 'dark';

/** AsyncStorage key of the choice. Never a token or secret. */
export const THEME_KEY = 'wy.theme.v1';

export const THEME_CHOICES: readonly { value: ThemeChoice; label: string }[] = [
  { value: 'system', label: 'Device default' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
];

export function parseTheme(v: unknown): ThemeChoice {
  return v === 'light' || v === 'dark' ? v : 'system';
}

export function applyTheme(choice: ThemeChoice): void {
  try {
    Appearance.setColorScheme(choice === 'system' ? 'unspecified' : choice);
  } catch {
    // Older platforms: the app keeps following the device.
  }
}

/** Reads the saved choice and applies it; device default when nothing (or junk) is saved. */
export async function loadTheme(kv: KVStore): Promise<ThemeChoice> {
  let choice: ThemeChoice = 'system';
  try {
    choice = parseTheme(await kv.getItem(THEME_KEY));
  } catch {
    // unreadable storage: device default
  }
  applyTheme(choice);
  return choice;
}

export async function saveTheme(kv: KVStore, choice: ThemeChoice): Promise<void> {
  applyTheme(choice);
  try {
    await kv.setItem(THEME_KEY, choice);
  } catch {
    // applied for this run even when it cannot be saved
  }
}
