// Shared look of the Android widgets added after Limits and Sessions (phone.md P11).
export const BG = '#17191B'; // the brand ink (theme.ts, the launcher icon)
export const FG = '#ECEEEF';
export const DIM = '#9AA1A8';
export const ACCENT = '#E8B04B'; // the brand gold
export const INK = '#17191B';
export const NEEDS = '#FF9F0A';
export const OFFLINE = '#F0877D';
export const ONLINE = '#4FC38A';
export const TRACK = '#2A2E32';
export const EMPTY_TEXT = 'Open Switchyard once to load';

export type Hex = `#${string}`;

export const ROOT = { height: 'match_parent', width: 'match_parent', backgroundColor: BG, borderRadius: 16, padding: 12 } as const;
