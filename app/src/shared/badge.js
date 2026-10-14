// The needs-you count as a tray image key (C9): assets/tray/tray-{0..9,9plus}.png, each with
// an @2x twin. Pure, so every platform branch of tray.js can share it.

/** Keys for counts 0..9, then '9plus'. */
export const BADGE_KEYS = Object.freeze(['0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '9plus']);

/** @param {unknown} count @returns {number} a whole number >= 0 (anything else is 0) */
export function normalizeCount(count) {
  const n = Math.floor(Number(count));
  return Number.isFinite(n) && n > 0 ? n : 0;
}

/** @param {unknown} count @returns {string} '0'..'9' or '9plus' */
export function badgeKey(count) {
  const n = normalizeCount(count);
  return n > 9 ? '9plus' : String(n);
}

/** @param {unknown} count @returns {string} the PNG file name for the count (the 1x image) */
export function badgeFile(count) {
  return `tray-${badgeKey(count)}.png`;
}

/** The text for a macOS tray title / dock badge: '' for 0, '9+' above nine. */
export function badgeText(count) {
  const n = normalizeCount(count);
  if (n === 0) return '';
  return n > 9 ? '9+' : String(n);
}
