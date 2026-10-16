// The needs-you count as text for the dock badge (C9). Pure, so every platform branch of
// tray.js can share it.

/** @param {unknown} count @returns {number} a whole number >= 0 (anything else is 0) */
export function normalizeCount(count) {
  const n = Math.floor(Number(count));
  return Number.isFinite(n) && n > 0 ? n : 0;
}

/** The text for a macOS tray title / dock badge: '' for 0, '9+' above nine. */
export function badgeText(count) {
  const n = normalizeCount(count);
  if (n === 0) return '';
  return n > 9 ? '9+' : String(n);
}
